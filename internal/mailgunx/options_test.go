package mailgunx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/config"
	"github.com/devrfc83/mailgun-passport/internal/metrics"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

func mailWith(headers ...string) string {
	return strings.Join(headers, "\r\n") + "\r\nFrom: alice@example.com\r\nTo: bob@example.com\r\nSubject: Hi\r\n\r\nBody\r\n"
}

func parseMail(t *testing.T, mail string) *parser.Message {
	t.Helper()
	msg, err := parser.ParseHeaders("alice@example.com", []string{"bob@example.com"}, strings.NewReader(mail), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func sendWith(t *testing.T, cfg config.Config, mode, mail string) *captured {
	t.Helper()
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	cfg.MailgunAPIKey, cfg.MailgunDomain, cfg.MailgunAPIBase = "key-test", "mg.example.com", srv.URL
	cfg.MailgunTimeout, cfg.MailgunSendMode = 5*time.Second, mode
	c := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := c.Send(context.Background(), parseMail(t, mail)); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConfiguredDefaultsAreSent(t *testing.T) {
	for _, mode := range []string{config.SendModeMIME, config.SendModeParsed} {
		t.Run(mode, func(t *testing.T) {
			got := sendWith(t, config.Config{
				MailgunTags:           []string{"relay", "passport"},
				MailgunTrackingClicks: "htmlonly",
				MailgunTrackingOpens:  "no",
				MailgunRequireTLS:     true,
			}, mode, mailWith())
			if strings.Join(got.form["o:tag"], ",") != "relay,passport" {
				t.Fatalf("tags: %#v", got.form["o:tag"])
			}
			for field, want := range map[string]string{
				"o:tracking-clicks": "htmlonly", "o:tracking-opens": "no", "o:require-tls": "yes",
			} {
				if strings.Join(got.form[field], ",") != want {
					t.Fatalf("%s: %#v", field, got.form[field])
				}
			}
		})
	}
}

func TestNothingIsSentWithoutOptions(t *testing.T) {
	got := sendWith(t, config.Config{}, config.SendModeMIME, mailWith())
	for field := range got.form {
		if strings.HasPrefix(field, "o:") || strings.HasPrefix(field, "v:") {
			t.Fatalf("unexpected option %s=%v", field, got.form[field])
		}
	}
}

func TestHeadersOverrideDefaults(t *testing.T) {
	got := sendWith(t, config.Config{
		MailgunTags:           []string{"default-a", "default-b"},
		MailgunTrackingClicks: "yes",
	}, config.SendModeMIME, mailWith(
		"X-Mailgun-Tag: welcome",
		"X-Mailgun-Track-Clicks: HtmlOnly",
		"X-Mailgun-Track-Opens: true",
		"X-Mailgun-Track: no",
		"X-Mailgun-Dkim: Yes",
		"X-Mailgun-Require-Tls: false",
		"X-Mailgun-Skip-Verification: yes",
	))
	for field, want := range map[string]string{
		"o:tag":               "welcome",
		"o:tracking-clicks":   "htmlonly",
		"o:tracking-opens":    "yes",
		"o:tracking":          "no",
		"o:dkim":              "yes",
		"o:require-tls":       "no",
		"o:skip-verification": "yes",
	} {
		if strings.Join(got.form[field], ",") != want {
			t.Errorf("%s: %#v, want %s", field, got.form[field], want)
		}
	}
}

func TestVariablesBecomeVFields(t *testing.T) {
	got := sendWith(t, config.Config{}, config.SendModeParsed,
		mailWith(`X-Mailgun-Variables: {"user_id": 42, "plan": "pro", "beta": true, "": "skipped"}`))
	for field, want := range map[string]string{"v:user_id": "42", "v:plan": "pro", "v:beta": "true"} {
		if strings.Join(got.form[field], ",") != want {
			t.Errorf("%s: %#v, want %s", field, got.form[field], want)
		}
	}
	if _, ok := got.form["v:"]; ok {
		t.Error("a variable with an empty name was sent")
	}
}

func TestInvalidHeadersAreIgnoredNotFatal(t *testing.T) {
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	var logs strings.Builder
	c := New(config.Config{
		MailgunAPIKey: "key-test", MailgunDomain: "mg.example.com", MailgunAPIBase: srv.URL,
		MailgunTimeout: 5 * time.Second, MailgunSendMode: config.SendModeMIME,
		MailgunTags: []string{"default"},
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	msg := parseMail(t, mailWith(
		"X-Mailgun-Track: maybe",
		"X-Mailgun-Tag: "+strings.Repeat("x", 129),
		"X-Mailgun-Variables: [1, 2]",
	))
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.form["o:tracking"]; ok {
		t.Error("an invalid value was passed on")
	}
	if strings.Join(got.form["o:tag"], ",") != "default" {
		t.Errorf("an invalid tag replaced the default: %#v", got.form["o:tag"])
	}
	for _, header := range []string{"x-mailgun-track", "x-mailgun-tag", "x-mailgun-variables"} {
		if !strings.Contains(logs.String(), header) {
			t.Errorf("no warning for %s:\n%s", header, logs.String())
		}
	}
}

func TestHeadersCannotChangeTestMode(t *testing.T) {
	got := sendWith(t, config.Config{}, config.SendModeMIME, mailWith("X-Mailgun-Testmode: yes", "O:testmode: yes"))
	if _, ok := got.form["o:testmode"]; ok {
		t.Fatal("a client switched on test mode")
	}
	got = sendWith(t, config.Config{MailgunTestMode: true}, config.SendModeMIME, mailWith())
	if strings.Join(got.form["o:testmode"], ",") != "yes" {
		t.Fatalf("operator test mode: %#v", got.form["o:testmode"])
	}
}

func TestReconfigureAppliesToNextCall(t *testing.T) {
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	cfg := config.Config{
		MailgunAPIKey: "key-one", MailgunDomain: "mg.one.example", MailgunAPIBase: srv.URL,
		MailgunTimeout: 5 * time.Second, MailgunSendMode: config.SendModeMIME,
		MailgunTags: []string{"one"},
	}
	c := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	msg := parseMail(t, mailWith())
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got.auth != "api:key-one" || got.path != "/v3/mg.one.example/messages.mime" {
		t.Fatalf("before: %s %s", got.auth, got.path)
	}

	cfg.MailgunAPIKey, cfg.MailgunDomain, cfg.MailgunTags = "key-two", "mg.two.example", []string{"two"}
	c.Reconfigure(cfg)
	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got.auth != "api:key-two" || got.path != "/v3/mg.two.example/messages.mime" {
		t.Fatalf("after: %s %s", got.auth, got.path)
	}
	if strings.Join(got.form["o:tag"], ",") != "two" {
		t.Fatalf("tags after: %#v", got.form["o:tag"])
	}
}

func TestReconfigureClearsStaleHealth(t *testing.T) {
	srv, _ := fakeMailgun(t, http.StatusUnauthorized, `{"message":"Forbidden"}`)
	c := newTestClient(t, srv.URL, config.SendModeMIME)
	_ = c.Verify(context.Background())
	if c.Health() == nil {
		t.Fatal("expected a failing state after 401")
	}
	c.Reconfigure(config.Config{
		MailgunAPIKey: "key-new", MailgunDomain: "mg.example.com", MailgunAPIBase: srv.URL,
		MailgunTimeout: time.Second, MailgunSendMode: config.SendModeMIME,
	})
	if err := c.Health(); err != nil {
		t.Fatalf("health after reconfigure: %v", err)
	}
}

func TestMetrics(t *testing.T) {
	srv, _ := fakeMailgun(t, http.StatusOK, okBody)
	m := metrics.New("test", "none")
	c := New(config.Config{
		MailgunAPIKey: "key-test", MailgunDomain: "mg.example.com", MailgunAPIBase: srv.URL,
		MailgunTimeout: 5 * time.Second, MailgunSendMode: config.SendModeMIME,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithMetrics(m))
	if err := c.Send(context.Background(), parseMail(t, mailWith())); err != nil {
		t.Fatal(err)
	}
	if m.MailgunRequests.Value("200") != 1 || m.MailgunDuration.Count() != 1 {
		t.Fatalf("200s: %d, observations: %d", m.MailgunRequests.Value("200"), m.MailgunDuration.Count())
	}

	// A closed server is "error", not a status.
	srv.Close()
	if err := c.Send(context.Background(), parseMail(t, mailWith())); err == nil {
		t.Fatal("expected an error")
	}
	if m.MailgunRequests.Value("error") != 1 || m.MailgunDuration.Count() != 2 {
		t.Fatalf("errors: %d, observations: %d", m.MailgunRequests.Value("error"), m.MailgunDuration.Count())
	}
}
