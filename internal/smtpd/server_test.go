package smtpd

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/parser"
)

func init() {
	authFailureDelay = 0
}

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

func (f *fakeSender) sent() []*parser.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*parser.Message{}, f.msgs...)
}

type deliveryErr struct {
	temporary bool
}

func (e deliveryErr) Error() string         { return "mailgun said no" }
func (e deliveryErr) Temporary() bool       { return e.temporary }
func (e deliveryErr) ClientMessage() string { return "Mailgun API returned HTTP 400" }

func baseConfig() config.Config {
	return config.Config{
		Listen:            "127.0.0.1:0",
		SMTPDomain:        "localhost",
		MaxMessageBytes:   1 << 20,
		MaxRecipients:     10,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		AllowInsecureAuth: true,
	}
}

// startServer runs the relay on a random loopback port and returns its address.
func startServer(t *testing.T, cfg config.Config, sender Sender) string {
	t.Helper()
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
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String()
}

var testMessage = []byte(strings.Join([]string{
	"From: Alice <alice@example.com>",
	"To: Bob <bob@example.com>",
	"Subject: Ping",
	"",
	"Hello from SMTP",
	"",
}, "\r\n"))

func smtpCode(t *testing.T, err error) int {
	t.Helper()
	var tpErr *textproto.Error
	if !errors.As(err, &tpErr) {
		t.Fatalf("expected an SMTP error reply, got %v", err)
	}
	return tpErr.Code
}

func TestSMTPAcceptsAndForwards(t *testing.T) {
	sender := &fakeSender{}
	addr := startServer(t, baseConfig(), sender)

	if err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, testMessage); err != nil {
		t.Fatal(err)
	}
	msgs := sender.sent()
	if len(msgs) != 1 {
		t.Fatalf("messages: %d", len(msgs))
	}
	got := msgs[0]
	if got.Subject != "Ping" {
		t.Fatalf("subject: %s", got.Subject)
	}
	if !strings.Contains(got.Text, "Hello from SMTP") {
		t.Fatalf("text: %s", got.Text)
	}
	if strings.Join(got.RcptTo, ",") != "bob@example.com" {
		t.Fatalf("rcpt: %#v", got.RcptTo)
	}
}

func TestAuthRequired(t *testing.T) {
	cfg := baseConfig()
	cfg.SMTPUsername = "relay"
	cfg.SMTPPassword = "s3cret"
	sender := &fakeSender{}
	addr := startServer(t, cfg, sender)

	err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, testMessage)
	if code := smtpCode(t, err); code != 530 {
		t.Fatalf("without AUTH: code %d", code)
	}

	auth := smtp.PlainAuth("", "relay", "s3cret", "127.0.0.1")
	if err := smtp.SendMail(addr, auth, "alice@example.com", []string{"bob@example.com"}, testMessage); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent()) != 1 {
		t.Fatal("authenticated message was not forwarded")
	}
}

func TestAuthFailuresAreLimited(t *testing.T) {
	cfg := baseConfig()
	cfg.SMTPUsername = "relay"
	cfg.SMTPPassword = "s3cret"
	addr := startServer(t, cfg, &fakeSender{})

	// net/smtp hangs up after a failed AUTH, so speak the protocol directly.
	c, err := textproto.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cmd := func(line string) int {
		t.Helper()
		id, err := c.Cmd("%s", line)
		if err != nil {
			t.Fatal(err)
		}
		c.StartResponse(id)
		defer c.EndResponse(id)
		code, _, _ := c.ReadResponse(0)
		return code
	}
	if code, _, err := c.ReadResponse(220); err != nil {
		t.Fatalf("greeting: %d %v", code, err)
	}
	cmd("EHLO test")
	plain := func(user, pass string) string {
		return "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00"+user+"\x00"+pass))
	}
	for i := 0; i < maxAuthFailures; i++ {
		if code := cmd(plain("relay", "wrong")); code != 535 {
			t.Fatalf("attempt %d: code %d", i+1, code)
		}
	}
	if code := cmd(plain("relay", "s3cret")); code != 421 {
		t.Fatalf("after %d failures: code %d", maxAuthFailures, code)
	}
}

func TestAllowedSenderDomains(t *testing.T) {
	cfg := baseConfig()
	cfg.AllowedSenderDomains = []string{"example.com"}
	sender := &fakeSender{}
	addr := startServer(t, cfg, sender)

	err := smtp.SendMail(addr, nil, "mallory@evil.test", []string{"bob@example.com"}, testMessage)
	if code := smtpCode(t, err); code != 550 {
		t.Fatalf("envelope sender: code %d", code)
	}

	spoofed := []byte(strings.Replace(string(testMessage), "alice@example.com", "ceo@evil.test", 1))
	err = smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, spoofed)
	if code := smtpCode(t, err); code != 550 {
		t.Fatalf("header sender: code %d", code)
	}

	if err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, testMessage); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("messages: %d", len(sender.sent()))
	}
}

func TestDeliveryErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{deliveryErr{temporary: false}, 554},
		{deliveryErr{temporary: true}, 451},
		{errors.New("unknown failure"), 451},
	}
	for _, tc := range cases {
		addr := startServer(t, baseConfig(), &fakeSender{err: tc.err})
		err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"}, testMessage)
		if code := smtpCode(t, err); code != tc.code {
			t.Fatalf("%v: code %d, want %d", tc.err, code, tc.code)
		}
	}
}

func TestSecretEqual(t *testing.T) {
	if !secretEqual("abc", "abc") || secretEqual("abc", "abd") || secretEqual("abc", "abcd") {
		t.Fatal("secretEqual is wrong")
	}
}
