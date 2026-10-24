package smtpd

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/parser"
)

type fakeSender struct {
	mu   sync.Mutex
	msgs []*parser.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, msg *parser.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	return f.err
}

func TestSMTPAcceptsAndForwards(t *testing.T) {
	sender := &fakeSender{}
	cfg := config.Config{
		Listen:            "127.0.0.1:0",
		SMTPDomain:        "localhost",
		MaxMessageBytes:   1 << 20,
		MaxRecipients:     10,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		AllowInsecureAuth: true,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(cfg, sender, log)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close(); _ = ln.Close() })

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	addr := ln.Addr().String()
	msg := strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: Bob <bob@example.com>",
		"Subject: Ping",
		"",
		"Hola desde SMTP",
		"",
	}, "\r\n")
	if err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sender.mu.Lock()
		n := len(sender.msgs)
		sender.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.msgs) != 1 {
		t.Fatalf("mensajes: %d", len(sender.msgs))
	}
	got := sender.msgs[0]
	if got.Subject != "Ping" {
		t.Fatalf("subject: %s", got.Subject)
	}
	if !strings.Contains(got.Text, "Hola desde SMTP") {
		t.Fatalf("text: %s", got.Text)
	}
}
