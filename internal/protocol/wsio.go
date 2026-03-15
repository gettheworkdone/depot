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
	wsReadTimeout    = 45 * time.Second
	wsWriteTimeout   = 10 * time.Second
	wsPingInterval   = 15 * time.Second
	wsPongExtendBy   = 45 * time.Second
	wsCloseWaitLimit = 3 * time.Second
)

// WSStream adapts a websocket connection to an io.ReadWriteCloser.
// Incoming frames are read sequentially and concatenated as a byte stream.
// Data is primarily sent in text frames with base64 payload to improve compatibility
// with middleboxes/proxies that may interfere with binary websocket frames.
type WSStream struct {
	conn    *websocket.Conn
	reader  io.Reader
	pending []byte
	off     int
	writeMu sync.Mutex
	closeCh chan struct{}
	once    sync.Once
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	w := &WSStream{
		conn:    conn,
		closeCh: make(chan struct{}),
	}
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongExtendBy))
	})
	go w.pingLoop()
	return w
}

func (w *WSStream) pingLoop() {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-w.closeCh:
			return
		case <-t.C:
			w.writeMu.Lock()
			_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			err := w.conn.WriteMessage(websocket.PingMessage, []byte("p"))
			w.writeMu.Unlock()
			if err != nil {
				_ = w.Close()
				return
			}
		}
	}
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
					if err != nil {
						continue
					}
					if len(decoded) == 0 {
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
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	payload := wsDataPref + base64.StdEncoding.EncodeToString(p)
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := w.conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSStream) SendEOF() error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return w.conn.WriteMessage(websocket.TextMessage, []byte(wsEOFMarker))
}

func (w *WSStream) Close() error {
	w.once.Do(func() { close(w.closeCh) })
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsCloseWaitLimit))
	_ = w.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return w.conn.Close()
}
