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
	wsEOFMarker    = "__DEPOT_EOF__"
	wsDataPref     = "D:"
	wsReadTimeout  = 60 * time.Second
	wsWriteTimeout = 10 * time.Second
	wsPingInterval = 20 * time.Second
	wsMaxBatchSize = 8192
)

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
// It uses text/base64 data frames for middlebox compatibility and a single writer loop
// to avoid concurrent websocket writes.
type WSStream struct {
	conn *websocket.Conn

	reader  io.Reader
	pending []byte
	off     int

	sendCh  chan wsMsg
	closeCh chan struct{}
	once    sync.Once
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	w := &WSStream{
		conn:    conn,
		sendCh:  make(chan wsMsg, 256),
		closeCh: make(chan struct{}),
	}

	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})

	go w.writeLoop()
	return w
}

func (w *WSStream) writeLoop() {
	pingTicker := time.NewTicker(wsPingInterval)
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
				_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
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
			err := w.writeText([]byte(wsEOFMarker))
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

			for len(batch) < wsMaxBatchSize {
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

		flush:
			payload := wsDataPref + base64.StdEncoding.EncodeToString(batch)
			err := w.writeText([]byte(payload))
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

func (w *WSStream) writeText(payload []byte) error {
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return w.conn.WriteMessage(websocket.TextMessage, payload)
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
			_ = w.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
			msgType, r, err := w.conn.NextReader()
			if err != nil {
				return 0, err
			}
			switch msgType {
			case websocket.BinaryMessage:
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
	ack := make(chan error, 1)
	copyBuf := append([]byte(nil), p...)
	select {
	case <-w.closeCh:
		return 0, io.ErrClosedPipe
	case w.sendCh <- wsMsg{kind: wsMsgData, data: copyBuf, ack: ack}:
	}
	err := <-ack
	if err != nil {
		return 0, err
	}
	return len(p), nil
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
