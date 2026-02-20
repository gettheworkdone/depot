package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	statusOK   = "OK\n"
	statusFail = "NO\n"
)

func WriteAuth(w io.Writer, password string) error {
	_, err := io.WriteString(w, password+"\n")
	return err
}

func ReadAuth(r io.Reader) (string, error) {
	reader := bufio.NewReader(r)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if strings.Contains(line, "\x00") {
		return "", errors.New("invalid auth line")
	}
	return strings.TrimSuffix(line, "\n"), nil
}

func SendOK(w io.Writer) error {
	_, err := io.WriteString(w, statusOK)
	return err
}

func SendFail(w io.Writer) error {
	_, err := io.WriteString(w, statusFail)
	return err
}

func ExpectOK(r io.Reader) error {
	reader := bufio.NewReader(r)
	line, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if line != statusOK {
		return fmt.Errorf("authentication failed")
	}
	return nil
}
