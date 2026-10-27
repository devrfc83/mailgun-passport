package smtpd

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/textproto"
	"testing"
	"time"
)

func TestAuthLimiterBlocksAndExpires(t *testing.T) {
	l := newAuthLimiter(3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	client := netip.MustParsePrefix("192.0.2.7/32")
	other := netip.MustParsePrefix("192.0.2.8/32")

	for i := range 3 {
		if l.blocked(client) {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail(client)
		now = now.Add(time.Minute)
	}
	if !l.blocked(client) {
		t.Fatal("not blocked after 3 failures")
	}
	if l.blocked(other) {
		t.Fatal("another address is blocked too")
	}

	// The block lasts authBlockTime from the last failure.
	now = now.Add(authBlockTime - 2*time.Minute)
	if !l.blocked(client) {
		t.Fatal("block ended early")
	}
	now = now.Add(time.Minute)
	if l.blocked(client) {
		t.Fatal("block did not end")
	}

	// Old failures are forgotten, so one more does not block again.
	l.fail(client)
	if l.blocked(client) {
		t.Fatal("expired failures still counted")
	}
}

func TestAuthLimiterDisabled(t *testing.T) {
	l := newAuthLimiter(0, nil)
	client := netip.MustParsePrefix("192.0.2.7/32")
	for range 100 {
		l.fail(client)
	}
	if l.blocked(client) {
		t.Fatal("a disabled limiter blocked")
	}
}

func TestAuthLimiterSweepsExpiredClients(t *testing.T) {
	l := newAuthLimiter(3, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := range 1024 {
		l.fail(netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 32))
	}
	now = now.Add(authBlockTime)
	l.fail(netip.MustParsePrefix("192.0.2.7/32"))
	if len(l.clients) != 1 {
		t.Fatalf("clients after sweep: %d", len(l.clients))
	}
}

func TestAuthClient(t *testing.T) {
	cases := []struct {
		addr string
		want string // empty: not limited
	}{
		{"192.0.2.7", "192.0.2.7/32"},
		{"::ffff:192.0.2.7", "192.0.2.7/32"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"127.0.0.1", ""},
		{"::1", ""},
	}
	for _, tc := range cases {
		key, ok := authClient(&net.TCPAddr{IP: net.ParseIP(tc.addr), Port: 25})
		got := ""
		if ok {
			got = key.String()
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.addr, got, tc.want)
		}
	}
}

func TestAuthFailuresAreLimitedPerAddress(t *testing.T) {
	authLimitLoopback = true
	t.Cleanup(func() { authLimitLoopback = false })
	cfg := baseConfig()
	cfg.SMTPUsername = "relay"
	cfg.SMTPPassword = "s3cret"
	cfg.AuthMaxFailures = 4
	addr := startServer(t, cfg, &fakeSender{})

	plain := func(pass string) string {
		return "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00relay\x00"+pass))
	}
	// Each connection gets maxAuthFailures (3) tries, so the fourth failure
	// happens on the second connection and the third connection is refused.
	attempt := func(passwords ...string) []int {
		t.Helper()
		c, err := textproto.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, _, err := c.ReadResponse(220); err != nil {
			t.Fatalf("greeting: %v", err)
		}
		if err := c.PrintfLine("EHLO test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.ReadResponse(250); err != nil {
			t.Fatalf("EHLO: %v", err)
		}
		var codes []int
		for _, p := range passwords {
			if err := c.PrintfLine("%s", plain(p)); err != nil {
				t.Fatal(err)
			}
			code, _, _ := c.ReadResponse(0)
			codes = append(codes, code)
		}
		return codes
	}
	if got := attempt("a", "b", "c"); got[2] != 535 {
		t.Fatalf("first connection: %v", got)
	}
	if got := attempt("d", "s3cret"); got[0] != 535 || got[1] != 421 {
		t.Fatalf("second connection: %v", got)
	}
	if got := attempt("s3cret"); got[0] != 421 {
		t.Fatalf("third connection: %v", got)
	}
}
