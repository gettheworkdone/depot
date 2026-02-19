package protocol

import (
	"io"
	"sync"

	"github.com/gorilla/websocket"
)

const wsEOFMarker = "__DEPOT_EOF__"

// WSStream adapts a websocket connection to an io.ReadWriteCloser.
// Incoming binary frames are read sequentially and concatenated as a byte stream.
// A dedicated text frame marker is used to signal EOF from client to server.
type WSStream struct {
	conn    *websocket.Conn
	reader  io.Reader
	writeMu sync.Mutex
}

func NewWSStream(conn *websocket.Conn) *WSStream {
	return &WSStream{conn: conn}
}

func (w *WSStream) Read(p []byte) (int, error) {
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
				if string(b) == wsEOFMarker {
					return 0, io.EOF
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

	wr, err := w.conn.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := wr.Write(p)
	closeErr := wr.Close()
	if writeErr != nil {
		return n, writeErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func (w *WSStream) SendEOF() error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.conn.WriteMessage(websocket.TextMessage, []byte(wsEOFMarker))
}

func (w *WSStream) Close() error {
	return w.conn.Close()
}
