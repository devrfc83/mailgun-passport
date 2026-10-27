package health

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type clientErr struct{}

func (clientErr) Error() string         { return "mailgun: HTTP 503: <html>long body</html>" }
func (clientErr) ClientMessage() string { return "Mailgun API returned HTTP 503" }

func TestEndpoints(t *testing.T) {
	cases := []struct {
		name    string
		serving bool
		mailgun error
		health  int
		ready   int
		reason  string
	}{
		{"all good", true, nil, 200, 200, "ok"},
		{"Mailgun failing", true, clientErr{}, 200, 503, "Mailgun: Mailgun API returned HTTP 503"},
		{"plain error", true, errors.New("dial tcp: refused"), 200, 503, "Mailgun: dial tcp: refused"},
		{"SMTP down", false, nil, 503, 503, "SMTP listener is not running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Handler(func() bool { return tc.serving }, func() error { return tc.mailgun })
			for path, want := range map[string]int{"/health": tc.health, "/ready": tc.ready} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != want {
					t.Fatalf("%s: %d, want %d", path, rec.Code, want)
				}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
			if got := strings.TrimSpace(rec.Body.String()); got != tc.reason {
				t.Fatalf("/ready body: %q, want %q", got, tc.reason)
			}
		})
	}

	h := Handler(func() bool { return true }, func() error { return nil })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", rec.Code)
	}
}

func TestCheck(t *testing.T) {
	healthy := true
	srv := httptest.NewServer(Handler(func() bool { return healthy }, func() error { return nil }))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	if err := Check(addr, "/health"); err != nil {
		t.Fatal(err)
	}
	// A wildcard address is reached over loopback.
	port := addr[strings.LastIndexByte(addr, ':'):]
	if err := Check("0.0.0.0"+port, "/health"); err != nil {
		t.Fatal(err)
	}
	healthy = false
	err := Check(addr, "/health")
	if err == nil || !strings.Contains(err.Error(), "SMTP listener is not running") {
		t.Fatalf("expected the reason in the error, got %v", err)
	}
	if err := Check("127.0.0.1:1", "/health"); err == nil {
		t.Fatal("expected an error when nothing listens")
	}
}
