package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"depot/internal/protocol"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "server address")
	password := flag.String("password", "", "server password")
	proto := flag.String("proto", "tcp", "transport protocol: tcp, httpws, or httpswss")
	wsPath := flag.String("ws-path", "/ws", "websocket path when -proto=httpws|httpswss")
	wsUserAgent := flag.String("ws-user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36", "WebSocket User-Agent header")
	wsOrigin := flag.String("ws-origin", "", "optional WebSocket Origin header")
	noRaw := flag.Bool("no-raw", false, "do not switch local terminal to raw mode")
	wsFrameMode := flag.String("ws-frame-mode", "text", "WebSocket framing mode: text or binary")
	wsReadTimeout := flag.Duration("ws-read-timeout", 60*time.Second, "WebSocket read timeout")
	wsWriteTimeout := flag.Duration("ws-write-timeout", 10*time.Second, "WebSocket write timeout")
	wsPingInterval := flag.Duration("ws-ping-interval", 20*time.Second, "WebSocket ping interval")
	wsBatchWait := flag.Duration("ws-batch-wait", 2*time.Millisecond, "max delay to coalesce small outgoing websocket writes")
	wsMaxBatch := flag.Int("ws-max-batch", 8192, "maximum bytes coalesced per websocket frame")
	wsQueue := flag.Int("ws-queue", 1024, "queued websocket write chunks")
	tcpNoDelay := flag.Bool("tcp-no-delay", true, "set TCP_NODELAY on client socket")
	tcpKeepAlive := flag.Duration("tcp-keepalive", 30*time.Second, "TCP keepalive period (<=0 disables keepalive tuning)")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "-password is required")
		os.Exit(2)
	}

	var rwc io.ReadWriteCloser
	switch *proto {
	case "tcp":
		conn, err := net.Dial("tcp", *addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
			os.Exit(1)
		}
		tuneConn(conn, *tcpNoDelay, *tcpKeepAlive)
		if err := protocol.WriteAuth(conn, *password); err != nil {
			fmt.Fprintf(os.Stderr, "auth write error: %v\n", err)
			os.Exit(1)
		}
		if err := protocol.ExpectOK(conn); err != nil {
			fmt.Fprintln(os.Stderr, "authentication failed")
			os.Exit(1)
		}
		rwc = conn
	case "httpws", "httpswss":
		scheme := "ws"
		if *proto == "httpswss" {
			scheme = "wss"
		}
		u := url.URL{Scheme: scheme, Host: *addr, Path: *wsPath}
		if strings.HasPrefix(*addr, "ws://") || strings.HasPrefix(*addr, "wss://") {
			parsed, err := url.Parse(*addr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "address parse error: %v\n", err)
				os.Exit(1)
			}
			if parsed.Path == "" || parsed.Path == "/" {
				parsed.Path = *wsPath
			}
			u = *parsed
			if *proto == "httpswss" {
				u.Scheme = "wss"
			}
		}

		dialer := websocket.Dialer{EnableCompression: false}
		hdr := http.Header{}
		hdr.Set("User-Agent", *wsUserAgent)
		if *wsOrigin != "" {
			hdr.Set("Origin", *wsOrigin)
		}

		conn, resp, err := dialer.Dial(u.String(), hdr)
		if err != nil {
			if resp != nil {
				fmt.Fprintf(os.Stderr, "connect error: %v (http status: %s)\n", err, resp.Status)
			} else {
				fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
			}
			os.Exit(1)
		}
		tuneConn(conn.UnderlyingConn(), *tcpNoDelay, *tcpKeepAlive)
		if err := conn.WriteMessage(websocket.TextMessage, []byte(*password)); err != nil {
			fmt.Fprintf(os.Stderr, "auth write error: %v\n", err)
			os.Exit(1)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil || string(msg) != "OK\n" {
			fmt.Fprintln(os.Stderr, "authentication failed")
			os.Exit(1)
		}
		frameMode := protocol.WSFrameModeTextB64
		if *wsFrameMode == "binary" {
			frameMode = protocol.WSFrameModeBinary
		} else if *wsFrameMode != "text" {
			fmt.Fprintln(os.Stderr, "-ws-frame-mode must be text or binary")
			os.Exit(2)
		}
		rwc = protocol.NewWSStreamWithOptions(conn, protocol.WSOptions{
			ReadTimeout:  *wsReadTimeout,
			WriteTimeout: *wsWriteTimeout,
			PingInterval: *wsPingInterval,
			BatchWait:    *wsBatchWait,
			MaxBatchSize: *wsMaxBatch,
			QueueSize:    *wsQueue,
			FrameMode:    frameMode,
		})
	default:
		fmt.Fprintln(os.Stderr, "-proto must be one of: tcp, httpws, httpswss")
		os.Exit(2)
	}
	defer rwc.Close()

	if !*noRaw {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}

	errCh := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(rwc, os.Stdin)
		if copyErr == nil || errors.Is(copyErr, io.EOF) {
			if tcpConn, ok := rwc.(*net.TCPConn); ok {
				_ = tcpConn.CloseWrite()
			}
			if ws, ok := rwc.(*protocol.WSStream); ok {
				_ = ws.SendEOF()
			}
		}
		errCh <- copyErr
	}()

	go func() {
		_, copyErr := io.Copy(os.Stdout, rwc)
		errCh <- copyErr
	}()

	firstErr := <-errCh
	_ = rwc.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}

	if firstErr != nil && !errors.Is(firstErr, io.EOF) {
		os.Exit(1)
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
