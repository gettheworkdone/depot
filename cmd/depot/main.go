package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"depot/internal/protocol"

	"golang.org/x/term"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:2222", "server address")
	password := flag.String("password", "", "server password")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "-password is required")
		os.Exit(2)
	}

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect error: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if err := protocol.WriteAuth(conn, *password); err != nil {
		fmt.Fprintf(os.Stderr, "auth write error: %v\n", err)
		os.Exit(1)
	}
	if err := protocol.ExpectOK(conn); err != nil {
		fmt.Fprintln(os.Stderr, "authentication failed")
		os.Exit(1)
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	doneIn := make(chan struct{})
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		close(doneIn)
	}()

	_, _ = io.Copy(os.Stdout, conn)
	<-doneIn
}
