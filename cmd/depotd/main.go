package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	"depot/internal/protocol"

	"github.com/creack/pty"
)

func main() {
	addr := flag.String("listen", "0.0.0.0:2222", "listen address")
	password := flag.String("password", "", "required client password")
	flag.Parse()

	if *password == "" {
		fmt.Fprintln(os.Stderr, "-password is required")
		os.Exit(2)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	defer ln.Close()

	var mu sync.Mutex
	busy := false

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}

		mu.Lock()
		if busy {
			mu.Unlock()
			_ = protocol.SendFail(conn)
			_ = conn.Close()
			continue
		}
		busy = true
		mu.Unlock()

		go func(c net.Conn) {
			defer c.Close()
			defer func() {
				mu.Lock()
				busy = false
				mu.Unlock()
			}()
			handleConn(c, *password)
		}(conn)
	}
}

func handleConn(conn net.Conn, expectedPassword string) {
	pw, err := protocol.ReadAuth(conn)
	if err != nil || pw != expectedPassword {
		_ = protocol.SendFail(conn)
		return
	}
	if err := protocol.SendOK(conn); err != nil {
		return
	}

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
		_, _ = io.Copy(conn, ptmx)
		close(doneOut)
	}()

	_, err = io.Copy(ptmx, conn)
	if err == nil || errors.Is(err, io.EOF) {
		_, _ = ptmx.Write([]byte{4})
	}

	<-doneOut
}
