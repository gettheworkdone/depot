package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"depot/internal/protocol"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func main() {
	addr := flag.String("listen", "0.0.0.0:2222", "listen address")
	password := flag.String("password", "", "required client password")
	proto := flag.String("proto", "tcp", "transport protocol: tcp, httpws, or httpswss")
	wsPath := flag.String("ws-path", "/ws", "websocket path when -proto=httpws|httpswss")
	tlsCert := flag.String("tls-cert", "", "TLS certificate PEM file (required for -proto=httpswss)")
	tlsKey := flag.String("tls-key", "", "TLS private key PEM file (required for -proto=httpswss)")
	wsFrameMode := flag.String("ws-frame-mode", "text", "WebSocket framing mode: text or binary")
	wsReadTimeout := flag.Duration("ws-read-timeout", 60*time.Second, "WebSocket read timeout")
	wsWriteTimeout := flag.Duration("ws-write-timeout", 10*time.Second, "WebSocket write timeout")
	wsPingInterval := flag.Duration("ws-ping-interval", 20*time.Second, "WebSocket ping interval")
	wsBatchWait := flag.Duration("ws-batch-wait", 2*time.Millisecond, "max delay to coalesce small outgoing websocket writes")
	wsMaxBatch := flag.Int("ws-max-batch", 8192, "maximum bytes coalesced per websocket frame")
	wsQueue := flag.Int("ws-queue", 1024, "queued websocket write chunks")
	tcpNoDelay := flag.Bool("tcp-no-delay", true, "set TCP_NODELAY on accepted sockets")
	tcpKeepAlive := flag.Duration("tcp-keepalive", 30*time.Second, "TCP keepalive period (<=0 disables keepalive tuning)")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "-password is required")
		os.Exit(2)
	}

	var mu sync.Mutex
	busy := false

	frameMode := protocol.WSFrameModeTextB64
	if *wsFrameMode == "binary" {
		frameMode = protocol.WSFrameModeBinary
	} else if *wsFrameMode != "text" {
		fmt.Fprintln(os.Stderr, "-ws-frame-mode must be text or binary")
		os.Exit(2)
	}
	wsOpts := protocol.WSOptions{
		ReadTimeout:  *wsReadTimeout,
		WriteTimeout: *wsWriteTimeout,
		PingInterval: *wsPingInterval,
		BatchWait:    *wsBatchWait,
		MaxBatchSize: *wsMaxBatch,
		QueueSize:    *wsQueue,
		FrameMode:    frameMode,
	}

	switch *proto {
	case "tcp":
		runTCP(*addr, *password, &mu, &busy, *tcpNoDelay, *tcpKeepAlive)
	case "httpws":
		runHTTPWS(*addr, *wsPath, *password, &mu, &busy, false, "", "", wsOpts, *tcpNoDelay, *tcpKeepAlive)
	case "httpswss":
		if *tlsCert == "" || *tlsKey == "" {
			fmt.Fprintln(os.Stderr, "-tls-cert and -tls-key are required for -proto=httpswss")
			os.Exit(2)
		}
		runHTTPWS(*addr, *wsPath, *password, &mu, &busy, true, *tlsCert, *tlsKey, wsOpts, *tcpNoDelay, *tcpKeepAlive)
	default:
		fmt.Fprintln(os.Stderr, "-proto must be one of: tcp, httpws, httpswss")
		os.Exit(2)
	}
}

func runTCP(addr, password string, mu *sync.Mutex, busy *bool, noDelay bool, keepAlive time.Duration) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		tuneConn(conn, noDelay, keepAlive)

		mu.Lock()
		if *busy {
			mu.Unlock()
			_ = protocol.SendFail(conn)
			_ = conn.Close()
			continue
		}
		*busy = true
		mu.Unlock()

		go func(c net.Conn) {
			defer c.Close()
			defer releaseBusy(mu, busy)
			handleTCPConn(c, password)
		}(conn)
	}
}

func runHTTPWS(addr, wsPath, password string, mu *sync.Mutex, busy *bool, tlsEnabled bool, certFile, keyFile string, wsOpts protocol.WSOptions, noDelay bool, keepAlive time.Duration) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }, EnableCompression: false}

	mux := http.NewServeMux()
	mux.HandleFunc(wsPath, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if *busy {
			mu.Unlock()
			http.Error(w, "busy", http.StatusConflict)
			return
		}
		*busy = true
		mu.Unlock()
		defer releaseBusy(mu, busy)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		tuneConn(conn.UnderlyingConn(), noDelay, keepAlive)

		handleWSConn(conn, password, wsOpts)
	})

	var err error
	if tlsEnabled {
		err = http.ListenAndServeTLS(addr, certFile, keyFile, mux)
	} else {
		err = http.ListenAndServe(addr, mux)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
}

func releaseBusy(mu *sync.Mutex, busy *bool) {
	mu.Lock()
	*busy = false
	mu.Unlock()
}

func handleTCPConn(conn net.Conn, expectedPassword string) {
	pw, err := protocol.ReadAuth(conn)
	if err != nil || pw != expectedPassword {
		_ = protocol.SendFail(conn)
		return
	}
	if err := protocol.SendOK(conn); err != nil {
		return
	}
	runShell(conn)
}

func handleWSConn(conn *websocket.Conn, expectedPassword string, wsOpts protocol.WSOptions) {
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != expectedPassword {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("NO\n"))
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("OK\n")); err != nil {
		return
	}
	stream := protocol.NewWSStreamWithOptions(conn, wsOpts)
	runShell(stream)
}

func runShell(stream io.ReadWriteCloser) {
	cmd := exec.Command("/bin/bash")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return
	}
	defer func() {
		_ = ptmx.Close()
		_ = stream.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	doneOut := make(chan error, 1)
	doneIn := make(chan error, 1)
	go func() {
		_, err := io.Copy(stream, ptmx)
		doneOut <- err
	}()
	go func() {
		_, err := io.Copy(ptmx, stream)
		doneIn <- err
	}()

	select {
	case <-doneOut:
		return
	case err := <-doneIn:
		if err == nil || errors.Is(err, io.EOF) {
			_, _ = ptmx.Write([]byte{4})
		}
		select {
		case <-doneOut:
		case <-time.After(2 * time.Second):
		}
		return
	}
}

func tuneConn(c net.Conn, noDelay bool, keepAlive time.Duration) {
	for c != nil {
		if tcp, ok := c.(*net.TCPConn); ok {
			if noDelay {
				_ = tcp.SetNoDelay(true)
			}
			if keepAlive > 0 {
				_ = tcp.SetKeepAlive(true)
				_ = tcp.SetKeepAlivePeriod(keepAlive)
			}
			return
		}
		type netConner interface{ NetConn() net.Conn }
		if nc, ok := c.(netConner); ok {
			c = nc.NetConn()
			continue
		}
		type underlyingConner interface{ UnderlyingConn() net.Conn }
		if uc, ok := c.(underlyingConner); ok {
			c = uc.UnderlyingConn()
			continue
		}
		return
	}
}
