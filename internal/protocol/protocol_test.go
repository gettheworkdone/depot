package protocol

import (
	"bytes"
	"testing"
)

func TestAuthRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteAuth(&buf, "secret"); err != nil {
		t.Fatalf("WriteAuth error: %v", err)
	}
	got, err := ReadAuth(&buf)
	if err != nil {
		t.Fatalf("ReadAuth error: %v", err)
	}
	if got != "secret" {
		t.Fatalf("got %q, want secret", got)
	}
}

func TestExpectOK(t *testing.T) {
	var buf bytes.Buffer
	if err := SendOK(&buf); err != nil {
		t.Fatalf("SendOK error: %v", err)
	}
	if err := ExpectOK(&buf); err != nil {
		t.Fatalf("ExpectOK error: %v", err)
	}
}

func TestExpectOKFail(t *testing.T) {
	var buf bytes.Buffer
	if err := SendFail(&buf); err != nil {
		t.Fatalf("SendFail error: %v", err)
	}
	if err := ExpectOK(&buf); err == nil {
		t.Fatalf("ExpectOK should fail")
	}
}
