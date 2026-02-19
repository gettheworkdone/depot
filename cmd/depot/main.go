package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"depot/internal/protocol"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "server address")
	password := flag.String("password", "", "server password")
	proto := flag.String("proto", "tcp", "transport protocol: tcp, httpws, or httpswss")
	wsPath := flag.String("ws-path", "/ws", "websocket path when -proto=httpws|httpswss")
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

		dialer := websocket.Dialer{}
		conn, resp, err := dialer.Dial(u.String(), http.Header{})
		if err != nil {
			if resp != nil {
				fmt.Fprintf(os.Stderr, "connect error: %v (http status: %s)\n", err, resp.Status)
			} else {
				fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
			}
			os.Exit(1)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(*password)); err != nil {
			fmt.Fprintf(os.Stderr, "auth write error: %v\n", err)
			os.Exit(1)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil || string(msg) != "OK\n" {
			fmt.Fprintln(os.Stderr, "authentication failed")
			os.Exit(1)
		}
		rwc = protocol.NewWSStream(conn)
	default:
		fmt.Fprintln(os.Stderr, "-proto must be one of: tcp, httpws, httpswss")
		os.Exit(2)
	}
	defer rwc.Close()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	doneIn := make(chan struct{})
	go func() {
		_, _ = io.Copy(rwc, os.Stdin)
		if tcpConn, ok := rwc.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		if ws, ok := rwc.(*protocol.WSStream); ok {
			_ = ws.SendEOF()
		}
		close(doneIn)
	}()

	_, _ = io.Copy(os.Stdout, rwc)
	<-doneIn
}
