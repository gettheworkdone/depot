package protocol

import (
	"encoding/base64"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsEOFMarker      = "__DEPOT_EOF__"
	wsDataPref       = "D:"
	defaultReadTO    = 60 * time.Second
	defaultWriteTO   = 10 * time.Second
	defaultPingInt   = 20 * time.Second
	defaultBatchSize = 8192
	defaultQueueSize = 256
)

type WSFrameMode string

const (
	WSFrameModeTextB64 WSFrameMode = "text"
	WSFrameModeBinary  WSFrameMode = "binary"
)

type WSOptions struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	PingInterval time.Duration
	MaxBatchSize int
	QueueSize    int
	BatchWait    time.Duration
	FrameMode    WSFrameMode
}

type wsMsgKind int

const (
	wsMsgData wsMsgKind = iota
	wsMsgEOF
)

type wsMsg struct {
	kind wsMsgKind
	data []byte
	ack  chan error
}

// WSStream adapts websocket frames as a byte stream.
// It supports either text/base64 framing or binary frames and uses a single
// writer loop to avoid concurrent websocket writes.
type WSStream struct {
	conn    *websocket.Conn
	opts    WSOptions
	reader  io.Reader
	pending []byte
	off     int

	sendCh  chan wsMsg
	closeCh chan struct{}
	once    sync.Once
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	return NewWSStreamWithOptions(conn, WSOptions{})
}

func NewWSStreamWithOptions(conn *websocket.Conn, opts WSOptions) *WSStream {
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = defaultReadTO
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTO
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = defaultPingInt
	}
	if opts.MaxBatchSize <= 0 {
		opts.MaxBatchSize = defaultBatchSize
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueueSize
	}
	if opts.FrameMode == "" {
		opts.FrameMode = WSFrameModeTextB64
	}

	w := &WSStream{
		conn:    conn,
		opts:    opts,
		sendCh:  make(chan wsMsg, opts.QueueSize),
		closeCh: make(chan struct{}),
	}

	_ = conn.SetReadDeadline(time.Now().Add(w.opts.ReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(w.opts.ReadTimeout))
	})

	go w.writeLoop()
	return w
}

func (w *WSStream) writeLoop() {
	pingTicker := time.NewTicker(w.opts.PingInterval)
	defer pingTicker.Stop()

	var carry *wsMsg

	for {
		var msg wsMsg
		if carry != nil {
			msg = *carry
			carry = nil
		} else {
			select {
			case <-w.closeCh:
				return
			case <-pingTicker.C:
				_ = w.conn.SetWriteDeadline(time.Now().Add(w.opts.WriteTimeout))
				if err := w.conn.WriteMessage(websocket.PingMessage, []byte("p")); err != nil {
					_ = w.forceClose()
					return
				}
				continue
			case msg = <-w.sendCh:
			}
		}

		switch msg.kind {
		case wsMsgEOF:
			err := w.writeEOF()
			if msg.ack != nil {
				msg.ack <- err
			}
			if err != nil {
				_ = w.forceClose()
				return
			}

		case wsMsgData:
			batch := make([]byte, 0, len(msg.data))
			batch = append(batch, msg.data...)
			acks := []chan error{msg.ack}
			if w.opts.BatchWait > 0 {
				deadline := time.NewTimer(w.opts.BatchWait)
				for len(batch) < w.opts.MaxBatchSize {
					select {
					case next := <-w.sendCh:
						if next.kind != wsMsgData {
							carry = &next
							deadline.Stop()
							goto flush
						}
						batch = append(batch, next.data...)
						acks = append(acks, next.ack)
					case <-deadline.C:
						goto flush
					}
				}
				deadline.Stop()
			} else {
				for len(batch) < w.opts.MaxBatchSize {
					select {
					case next := <-w.sendCh:
						if next.kind != wsMsgData {
							carry = &next
							goto flush
						}
						batch = append(batch, next.data...)
						acks = append(acks, next.ack)
					default:
						goto flush
					}
				}
			}

		flush:
			err := w.writeData(batch)
			for _, ack := range acks {
				if ack != nil {
					ack <- err
				}
			}
			if err != nil {
				_ = w.forceClose()
				return
			}
		}
	}
}

func (w *WSStream) writeData(batch []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(w.opts.WriteTimeout))
	if w.opts.FrameMode == WSFrameModeBinary {
		return w.conn.WriteMessage(websocket.BinaryMessage, batch)
	}
	payload := wsDataPref + base64.StdEncoding.EncodeToString(batch)
	return w.conn.WriteMessage(websocket.TextMessage, []byte(payload))
}

func (w *WSStream) writeEOF() error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(w.opts.WriteTimeout))
	if w.opts.FrameMode == WSFrameModeBinary {
		return w.conn.WriteMessage(websocket.TextMessage, []byte(wsEOFMarker))
	}
	return w.conn.WriteMessage(websocket.TextMessage, []byte(wsEOFMarker))
}

func (w *WSStream) Read(p []byte) (int, error) {
	if w.off < len(w.pending) {
		n := copy(p, w.pending[w.off:])
		w.off += n
		if w.off >= len(w.pending) {
			w.pending = nil
			w.off = 0
		}
		return n, nil
	}

	for {
		if w.reader == nil {
			_ = w.conn.SetReadDeadline(time.Now().Add(w.opts.ReadTimeout))
			msgType, r, err := w.conn.NextReader()
			if err != nil {
				return 0, err
			}
			switch msgType {
			case websocket.BinaryMessage:
				if w.opts.FrameMode == WSFrameModeBinary {
					w.reader = r
					continue
				}
				w.reader = r
				continue
			case websocket.TextMessage:
				b, err := io.ReadAll(r)
				if err != nil {
					return 0, err
				}
				s := string(b)
				if s == wsEOFMarker {
					return 0, io.EOF
				}
				if strings.HasPrefix(s, wsDataPref) {
					decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, wsDataPref))
					if err != nil || len(decoded) == 0 {
						continue
					}
					n := copy(p, decoded)
					if n < len(decoded) {
						w.pending = decoded
						w.off = n
					}
					return n, nil
				}
				if w.opts.FrameMode == WSFrameModeBinary {
					continue
				}
				continue
			default:
				continue
			}
		}

		n, err := w.reader.Read(p)
		if err == io.EOF {
			w.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *WSStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	copyBuf := append([]byte(nil), p...)
	select {
	case <-w.closeCh:
		return 0, io.ErrClosedPipe
	case w.sendCh <- wsMsg{kind: wsMsgData, data: copyBuf}:
		return len(p), nil
	}
}

func (w *WSStream) SendEOF() error {
	ack := make(chan error, 1)
	select {
	case <-w.closeCh:
		return io.ErrClosedPipe
	case w.sendCh <- wsMsg{kind: wsMsgEOF, ack: ack}:
	}
	return <-ack
}

func (w *WSStream) forceClose() error {
	w.once.Do(func() { close(w.closeCh) })
	return w.conn.Close()
}

func (w *WSStream) Close() error {
	_ = w.forceClose()
	return nil
}
