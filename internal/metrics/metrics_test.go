package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func scrape(t *testing.T, s *Set) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestExposition(t *testing.T) {
	s := New("1.2.3", "abc123")
	s.Messages.Inc("accepted")
	s.Messages.Inc("accepted")
	s.Messages.Inc("invalid")
	s.MailgunRequests.Inc("200")
	s.MailgunRequests.Inc("error")
	s.MailgunDuration.Observe(0.2)
	s.MailgunDuration.Observe(3)
	s.ConnectionsOpen.Add(2)
	s.ConnectionsOpen.Add(-1)
	s.RateLimited.Inc("")
	s.GaugeFunc("queue_messages", "Messages waiting.", func() float64 { return 7 })

	out := scrape(t, s)
	for _, want := range []string{
		"# TYPE mailgun_passport_messages_total counter\n",
		`mailgun_passport_messages_total{result="accepted"} 2` + "\n",
		`mailgun_passport_messages_total{result="invalid"} 1` + "\n",
		// Known series exist from the start, at zero.
		`mailgun_passport_messages_total{result="permanent_failure"} 0` + "\n",
		`mailgun_passport_mailgun_requests_total{code="200"} 1` + "\n",
		`mailgun_passport_mailgun_requests_total{code="error"} 1` + "\n",
		"# TYPE mailgun_passport_mailgun_request_duration_seconds histogram\n",
		`mailgun_passport_mailgun_request_duration_seconds_bucket{le="0.25"} 1` + "\n",
		`mailgun_passport_mailgun_request_duration_seconds_bucket{le="5"} 2` + "\n",
		`mailgun_passport_mailgun_request_duration_seconds_bucket{le="+Inf"} 2` + "\n",
		"mailgun_passport_mailgun_request_duration_seconds_sum 3.2\n",
		"mailgun_passport_mailgun_request_duration_seconds_count 2\n",
		"mailgun_passport_smtp_connections_open 1\n",
		"mailgun_passport_smtp_rate_limited_total 1\n",
		"mailgun_passport_queue_messages 7\n",
		`mailgun_passport_build_info{version="1.2.3",commit="abc123"} 1` + "\n",
		"# TYPE process_start_time_seconds gauge\n",
		"# TYPE go_goroutines gauge\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	s := New("1", "c")
	s.MailgunRequests.Inc("a\"b\\c\nd")
	if out := scrape(t, s); !strings.Contains(out, `code="a\"b\\c\nd"`) {
		t.Fatalf("label not escaped:\n%s", out)
	}
}

func TestNilReceiversDoNothing(t *testing.T) {
	var s *Set
	s.GaugeFunc("x", "y", func() float64 { return 1 })
	s.Write(&strings.Builder{})
	var c *Counter
	c.Inc("a")
	if c.Value("a") != 0 {
		t.Fatal("nil counter counted")
	}
	var h *Histogram
	h.Observe(1)
	if h.Count() != 0 {
		t.Fatal("nil histogram counted")
	}
	var g *Gauge
	g.Add(1)
	if g.Value() != 0 {
		t.Fatal("nil gauge counted")
	}
}

func TestConcurrentUse(t *testing.T) {
	s := New("1", "c")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				s.Messages.Inc("accepted")
				s.MailgunDuration.Observe(0.3)
				s.ConnectionsOpen.Add(1)
				_ = scrape(t, s)
			}
		}()
	}
	wg.Wait()
	if got := s.Messages.Value("accepted"); got != 4000 {
		t.Fatalf("accepted = %d", got)
	}
	if got := s.MailgunDuration.Count(); got != 4000 {
		t.Fatalf("observations = %d", got)
	}
	if got := s.ConnectionsOpen.Value(); got != 4000 {
		t.Fatalf("open = %d", got)
	}
}
