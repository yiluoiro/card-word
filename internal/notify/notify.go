// Package notify delivers order notifications. The SMTP implementation is
// built in; plugins can register additional senders (webhooks, Telegram,
// serverless functions).
package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/yiluoiro/card-word/internal/version"
)

// Message is an outbound notification.
type Message struct {
	To      string
	Subject string
	Body    string
	HTML    bool
}

// Sender delivers messages.
type Sender interface {
	Name() string
	Enabled() bool
	Send(ctx context.Context, msg Message) error
}

// SMTPConfig configures the built-in mail sender.
type SMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	FromName   string
	UseTLS     bool // implicit TLS (465)
	StartTLS   bool // upgrade on 587
	SkipVerify bool
}

// SMTP sends mail through a plain SMTP server.
type SMTP struct {
	cfg SMTPConfig
}

// NewSMTP builds a sender, returning nil when the config is incomplete so that
// callers can treat mail as optional.
func NewSMTP(cfg SMTPConfig) *SMTP {
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.From) == "" {
		return nil
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	return &SMTP{cfg: cfg}
}

// Name implements Sender.
func (s *SMTP) Name() string { return "smtp" }

// Enabled implements Sender.
func (s *SMTP) Enabled() bool { return s != nil && s.cfg.Host != "" }

// Send implements Sender.
func (s *SMTP) Send(ctx context.Context, msg Message) error {
	if !s.Enabled() {
		return errors.New("notify: smtp not configured")
	}
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("notify: recipient required")
	}
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port))
	timeout := 20 * time.Second
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("notify: dial smtp: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.cfg.SkipVerify}
	if s.cfg.UseTLS {
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("notify: implicit tls: %w", err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("notify: smtp handshake: %w", err)
	}
	defer client.Close()
	if s.cfg.StartTLS && !s.cfg.UseTLS {
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("notify: starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("notify: smtp auth: %w", err)
		}
	}
	if err := client.Mail(extractAddress(s.cfg.From)); err != nil {
		return fmt.Errorf("notify: mail from: %w", err)
	}
	if err := client.Rcpt(extractAddress(msg.To)); err != nil {
		return fmt.Errorf("notify: rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify: smtp data: %w", err)
	}
	if _, err := w.Write([]byte(s.build(msg))); err != nil {
		return fmt.Errorf("notify: write mail: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: close mail: %w", err)
	}
	return client.Quit()
}

func (s *SMTP) build(msg Message) string {
	from := s.cfg.From
	if s.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", s.cfg.FromName, extractAddress(s.cfg.From))
	}
	var sb strings.Builder
	sb.WriteString("From: " + sanitizeHeader(from) + "\r\n")
	sb.WriteString("To: " + sanitizeHeader(msg.To) + "\r\n")
	sb.WriteString("Subject: " + sanitizeHeader(msg.Subject) + "\r\n")
	sb.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("X-Mailer: card word " + version.Version + "\r\n")
	if msg.HTML {
		sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	} else {
		sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	}
	sb.WriteString("\r\n")
	sb.WriteString(msg.Body)
	return sb.String()
}

func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func extractAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.Index(s[i:], ">"); j > 0 {
			return strings.TrimSpace(s[i+1 : i+j])
		}
	}
	return s
}

// Dispatcher fans a message out to every enabled sender without blocking the
// request that triggered it beyond a bounded deadline.
type Dispatcher struct {
	mu      sync.RWMutex
	senders []Sender
	timeout time.Duration
	logf    func(format string, args ...any)
}

// NewDispatcher builds a dispatcher.
func NewDispatcher(timeout time.Duration, logf func(string, ...any)) *Dispatcher {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Dispatcher{timeout: timeout, logf: logf}
}

// Register adds a sender.
func (d *Dispatcher) Register(s Sender) {
	if s == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senders = append(d.senders, s)
}

// SetSenders replaces every sender, used when SMTP settings change.
func (d *Dispatcher) SetSenders(senders []Sender) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.senders = append([]Sender(nil), senders...)
}

// SendAsync delivers a message in the background.
func (d *Dispatcher) SendAsync(msg Message) {
	d.mu.RLock()
	senders := append([]Sender(nil), d.senders...)
	d.mu.RUnlock()
	if len(senders) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), d.timeout)
		defer cancel()
		for _, s := range senders {
			if !s.Enabled() {
				continue
			}
			if err := s.Send(ctx, msg); err != nil {
				d.logf("notify: sender %s failed: %v", s.Name(), err)
			}
		}
	}()
}
