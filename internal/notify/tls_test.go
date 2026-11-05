package notify

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestImplicitTLSBeforeSMTPGreeting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	firstByte := make(chan byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			firstByte <- 0
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		_, _ = c.Read(b[:])
		firstByte <- b[0]
	}()
	s := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "sender@example.test", UseTLS: true})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Send(ctx, Message{To: "recipient@example.test"}); err == nil {
		t.Fatal("invalid TLS server accepted")
	}
	if b := <-firstByte; b != 0x16 {
		t.Fatalf("expected TLS handshake first, got %x", b)
	}
}
