package smtpd

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/metrics"
)

func send(addr string, auth smtp.Auth) error {
	return smtp.SendMail(addr, auth, "alice@example.com", []string{"bob@example.com"}, testMessage)
}

func TestReconfigureRotatesPassword(t *testing.T) {
	cfg := baseConfig()
	cfg.SMTPUsername, cfg.SMTPPassword = "relay", "old-secret"
	sender := &fakeSender{}
	srv, addr := startServerWith(t, cfg, sender)

	if err := send(addr, smtp.PlainAuth("", "relay", "old-secret", "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	cfg.SMTPPassword = "new-secret"
	srv.Reconfigure(cfg)

	if err := send(addr, smtp.PlainAuth("", "relay", "old-secret", "127.0.0.1")); err == nil {
		t.Fatal("the old password still works after a reload")
	}
	if err := send(addr, smtp.PlainAuth("", "relay", "new-secret", "127.0.0.1")); err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}
	if got := len(sender.sent()); got != 2 {
		t.Fatalf("delivered %d messages, want 2", got)
	}
}

func TestReconfigureChangesAllowedSenders(t *testing.T) {
	cfg := baseConfig()
	cfg.AllowedSenderDomains = []string{"example.com"}
	srv, addr := startServerWith(t, cfg, &fakeSender{})
	if err := send(addr, nil); err != nil {
		t.Fatal(err)
	}
	cfg.AllowedSenderDomains = []string{"example.org"}
	srv.Reconfigure(cfg)
	if code := smtpCode(t, send(addr, nil)); code != 550 {
		t.Fatalf("code %d, want 550 once example.com is no longer allowed", code)
	}
}

func TestReloadCertificate(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := writeCert(t, dir, "old")
	cfg := baseConfig()
	cfg.TLSCertFile, cfg.TLSKeyFile = certFile, keyFile
	srv, _ := startServerWith(t, cfg, &fakeSender{})
	if got := servedName(t, srv.certs); got != "old" {
		t.Fatalf("served %q", got)
	}

	writeCert(t, dir, "renewed")
	if err := srv.ReloadCertificate(); err != nil {
		t.Fatal(err)
	}
	if got := servedName(t, srv.certs); got != "renewed" {
		t.Fatalf("served %q after a reload", got)
	}

	// A broken pair is an error, and the certificate in use stays.
	if err := writeFile(certFile, "garbage"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadCertificate(); err == nil {
		t.Fatal("a broken certificate was loaded")
	}
	if got := servedName(t, srv.certs); got != "renewed" {
		t.Fatalf("served %q after a failed reload", got)
	}
}

func TestReloadCertificateWithoutTLS(t *testing.T) {
	srv, _ := startServerWith(t, baseConfig(), &fakeSender{})
	if err := srv.ReloadCertificate(); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitAnswers451(t *testing.T) {
	cfg := baseConfig()
	cfg.RateLimitPerMinute = 2
	m := metrics.New("test", "none")
	sender := &fakeSender{}
	_, addr := startServerWith(t, cfg, sender, WithMetrics(m))

	for i := range 2 {
		if err := send(addr, nil); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	err := send(addr, nil)
	if code := smtpCode(t, err); code != 451 {
		t.Fatalf("third message: code %d, want 451", code)
	}
	if !strings.Contains(err.Error(), "4.7.1") {
		t.Fatalf("not an enhanced 4.7.1 reply: %v", err)
	}
	if got := len(sender.sent()); got != 2 {
		t.Fatalf("delivered %d messages, want 2", got)
	}
	if m.RateLimited.Value("") != 1 {
		t.Fatalf("rate limited metric: %d", m.RateLimited.Value(""))
	}
}

func TestNoRateLimitByDefault(t *testing.T) {
	_, addr := startServerWith(t, baseConfig(), &fakeSender{})
	for i := range 20 {
		if err := send(addr, nil); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
}

func TestMessageAndAuthMetrics(t *testing.T) {
	cfg := baseConfig()
	cfg.SMTPUsername, cfg.SMTPPassword = "relay", "s3cret"
	cfg.AllowedSenderDomains = []string{"example.com"}
	m := metrics.New("test", "none")
	sender := &fakeSender{}
	_, addr := startServerWith(t, cfg, sender, WithMetrics(m))
	good := smtp.PlainAuth("", "relay", "s3cret", "127.0.0.1")

	if err := send(addr, good); err != nil {
		t.Fatal(err)
	}
	_ = send(addr, smtp.PlainAuth("", "relay", "wrong", "127.0.0.1"))
	_ = smtp.SendMail(addr, good, "mallory@evil.test", []string{"bob@example.com"}, testMessage)

	sender.err = deliveryErr{temporary: true}
	_ = send(addr, good)
	sender.err = deliveryErr{temporary: false}
	_ = send(addr, good)
	sender.err = nil
	for result, want := range map[string]uint64{
		"accepted": 1, "sender_rejected": 1, "temporary_failure": 1, "permanent_failure": 1,
	} {
		if got := m.Messages.Value(result); got != want {
			t.Errorf("messages %s = %d, want %d", result, got, want)
		}
	}
	if got := m.AuthAttempts.Value("failure"); got != 1 {
		t.Errorf("auth failures = %d", got)
	}
	if got := m.AuthAttempts.Value("success"); got != 4 {
		t.Errorf("auth successes = %d", got)
	}
}

func TestInvalidMessageMetric(t *testing.T) {
	m := metrics.New("test", "none")
	cfg := baseConfig()
	cfg.MaxMessageBytes = 100
	_, addr := startServerWith(t, cfg, &fakeSender{}, WithMetrics(m))
	err := smtp.SendMail(addr, nil, "alice@example.com", []string{"bob@example.com"},
		[]byte("From: alice@example.com\r\n\r\n"+strings.Repeat("x", 500)))
	if code := smtpCode(t, err); code != 552 {
		t.Fatalf("code %d, want 552", code)
	}
	if m.Messages.Value("invalid") != 1 {
		t.Fatalf("invalid = %d", m.Messages.Value("invalid"))
	}
}

func TestConnectionMetrics(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxConnections = 1
	m := metrics.New("test", "none")
	_, addr := startServerWith(t, cfg, &fakeSender{}, WithMetrics(m))

	first, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "an open connection", func() bool { return m.ConnectionsOpen.Value() == 1 })

	second, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	greeting, _ := io.ReadAll(second)
	_ = second.Close()
	if !strings.HasPrefix(string(greeting), "421 4.7.0") {
		t.Fatalf("second connection got %q", greeting)
	}
	waitFor(t, "a refusal", func() bool { return m.Connections.Value("refused") == 1 })

	_ = first.Close()
	waitFor(t, "the connection to close", func() bool { return m.ConnectionsOpen.Value() == 0 })
	if m.Connections.Value("accepted") != 1 {
		t.Fatalf("accepted = %d", m.Connections.Value("accepted"))
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- limit listener ---------------------------------------------------------

type fakeListener struct {
	conns chan net.Conn
}

func (l *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}
func (l *fakeListener) Close() error   { close(l.conns); return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestLimitListenerFreesSlotsOnceAndIgnoresDoubleClose(t *testing.T) {
	fl := &fakeListener{conns: make(chan net.Conn, 4)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	l := newLimitListener(fl, 1, log, metrics.New("t", "c"))

	c1, s1 := net.Pipe()
	fl.conns <- s1
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	// A second connection while the slot is taken is refused with 421, and
	// Accept keeps waiting for the next one.
	refusedClient, refusedServer := net.Pipe()
	fl.conns <- refusedServer
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(refusedClient)
		got <- string(b)
	}()
	next := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		next <- c
	}()
	if msg := <-got; msg != tooManyConnections {
		t.Fatalf("refusal: %q", msg)
	}
	select {
	case <-next:
		t.Fatal("accepted a connection over the limit")
	case <-time.After(50 * time.Millisecond):
	}

	// Closing twice must free one slot, not two: of the two connections that
	// follow, only the first gets in.
	_ = conn.Close()
	_ = conn.Close()
	c2, s2 := net.Pipe()
	fl.conns <- s2
	var accepted net.Conn
	select {
	case accepted = <-next:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection after the slot was freed")
	}
	extraClient, extraServer := net.Pipe()
	fl.conns <- extraServer
	extra := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(extraClient)
		extra <- string(b)
	}()
	go func() { _, _ = l.Accept() }()
	if msg := <-extra; msg != tooManyConnections {
		t.Fatalf("the slot was freed twice: second connection got %q", msg)
	}
	_ = c1.Close()
	_ = c2.Close()
	_ = accepted.Close()
	_ = fl.Close()
}

func TestLimitListenerZeroMeansUnlimited(t *testing.T) {
	fl := &fakeListener{conns: make(chan net.Conn, 3)}
	l := newLimitListener(fl, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New("t", "c"))
	for range 3 {
		c, s := net.Pipe()
		defer c.Close()
		fl.conns <- s
		conn, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
	}
}

func TestLimitListenerPassesAcceptErrors(t *testing.T) {
	fl := &fakeListener{conns: make(chan net.Conn)}
	l := newLimitListener(fl, 1, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	_ = fl.Close()
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

// --- rate limiter -----------------------------------------------------------

func TestRateLimiterRefillsOverTime(t *testing.T) {
	l := newRateLimiter(60) // one a second
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	client := mustPrefix(t, "192.0.2.1/32")

	for i := range 60 {
		if !l.allow(client) {
			t.Fatalf("message %d of the burst was refused", i)
		}
	}
	if l.allow(client) {
		t.Fatal("the burst should be spent")
	}
	now = now.Add(500 * time.Millisecond)
	if l.allow(client) {
		t.Fatal("half a token is not enough")
	}
	now = now.Add(600 * time.Millisecond)
	if !l.allow(client) {
		t.Fatal("a token should have refilled")
	}
	if l.allow(client) {
		t.Fatal("only one token had refilled")
	}
	// Clients do not share an allowance.
	if !l.allow(mustPrefix(t, "192.0.2.2/32")) {
		t.Fatal("another client was limited")
	}
	// A long pause refills to the burst, not beyond it.
	now = now.Add(time.Hour)
	for i := range 60 {
		if !l.allow(client) {
			t.Fatalf("refilled message %d refused", i)
		}
	}
	if l.allow(client) {
		t.Fatal("the allowance grew beyond the burst")
	}
}

func TestRateLimiterDisabledAllowsEverything(t *testing.T) {
	var l *rateLimiter
	if newRateLimiter(0) != nil || !l.allow(mustPrefix(t, "192.0.2.1/32")) {
		t.Fatal("a disabled limiter refused a message")
	}
}

func TestRateLimiterForgetsIdleClients(t *testing.T) {
	l := newRateLimiter(10)
	now := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return now }
	l.nextSweep = 4
	for i := range 4 {
		l.allow(mustPrefix(t, "192.0.2."+string(rune('1'+i))+"/32"))
	}
	now = now.Add(time.Hour)
	l.allow(mustPrefix(t, "198.51.100.1/32")) // triggers the sweep
	if len(l.buckets) != 1 {
		t.Fatalf("%d clients kept, want only the new one", len(l.buckets))
	}
}

func TestClientPrefixIncludesLoopback(t *testing.T) {
	loop := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	if _, ok := authClient(loop); ok {
		t.Fatal("AUTH limiter should skip loopback")
	}
	key, ok := clientPrefix(loop)
	if !ok || key.String() != "127.0.0.1/32" {
		t.Fatalf("key %v ok %v", key, ok)
	}
	v6, _ := clientPrefix(&net.TCPAddr{IP: net.ParseIP("2001:db8::1234")})
	if v6.String() != "2001:db8::/64" {
		t.Fatalf("v6 key %v", v6)
	}
	if _, ok := clientPrefix(&net.UnixAddr{Name: "x", Net: "unix"}); ok {
		t.Fatal("a unix socket client has no address to count")
	}
}

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(name, content string) error {
	return os.WriteFile(name, []byte(content), 0o600)
}
