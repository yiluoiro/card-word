package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestMailHeadersCannotInject(t *testing.T) {
	s := NewSMTP(SMTPConfig{Host: "unused", From: "sender@example.test", FromName: "Name\r\nBcc: attacker@example.test"})
	raw := s.build(Message{To: "recipient@example.test\r\nBcc: attacker@example.test", Subject: "Hi\r\nBcc: attacker@example.test"})
	if strings.Contains(raw, "\r\nBcc:") {
		t.Fatal("injected header in message")
	}
}

func TestStartTLSRequiredBeforeMail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	commands := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			commands <- ""
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.Write([]byte("220 test ESMTP\r\n"))
		r := bufio.NewReader(c)
		_, _ = r.ReadString('\n')
		_, _ = c.Write([]byte("250 test\r\n")) // deliberately no STARTTLS
		line, _ := r.ReadString('\n')
		commands <- line
	}()
	s := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "sender@example.test", StartTLS: true})
	if err := s.Send(context.Background(), Message{To: "recipient@example.test"}); err == nil {
		t.Fatal("accepted server without STARTTLS")
	}
	if command := <-commands; strings.HasPrefix(command, "MAIL") {
		t.Fatal("sent mail before required TLS")
	}
}
