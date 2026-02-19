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
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "-password is required")
		os.Exit(2)
	}

	var mu sync.Mutex
	busy := false

	switch *proto {
	case "tcp":
		runTCP(*addr, *password, &mu, &busy)
	case "httpws":
		runHTTPWS(*addr, *wsPath, *password, &mu, &busy, false, "", "")
	case "httpswss":
		if *tlsCert == "" || *tlsKey == "" {
			fmt.Fprintln(os.Stderr, "-tls-cert and -tls-key are required for -proto=httpswss")
			os.Exit(2)
		}
		runHTTPWS(*addr, *wsPath, *password, &mu, &busy, true, *tlsCert, *tlsKey)
	default:
		fmt.Fprintln(os.Stderr, "-proto must be one of: tcp, httpws, httpswss")
		os.Exit(2)
	}
}

func runTCP(addr, password string, mu *sync.Mutex, busy *bool) {
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

func runHTTPWS(addr, wsPath, password string, mu *sync.Mutex, busy *bool, tlsEnabled bool, certFile, keyFile string) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

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

		handleWSConn(conn, password)
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

func handleWSConn(conn *websocket.Conn, expectedPassword string) {
	_, msg, err := conn.ReadMessage()
	if err != nil || string(msg) != expectedPassword {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("NO\n"))
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("OK\n")); err != nil {
		return
	}
	stream := protocol.NewWSStream(conn)
	runShell(stream)
}

func runShell(stream io.ReadWriter) {
	cmd := exec.Command("/bin/bash")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	doneOut := make(chan struct{})
	go func() {
		_, _ = io.Copy(stream, ptmx)
		close(doneOut)
	}()

	_, err = io.Copy(ptmx, stream)
	if err == nil || errors.Is(err, io.EOF) {
		_, _ = ptmx.Write([]byte{4})
	}

	<-doneOut
}
