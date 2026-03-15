package protocol

import (
	"encoding/base64"
	"io"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const (
	wsEOFMarker = "__DEPOT_EOF__"
	wsDataPref  = "D:"
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
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	return &WSStream{conn: conn}
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
	if err := w.conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *WSStream) SendEOF() error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, []byte(wsEOFMarker))
}

func (w *WSStream) Close() error {
	return w.conn.Close()
}
