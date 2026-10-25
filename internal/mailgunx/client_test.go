package mailgunx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/parser"
)

const testMail = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>, Carol <carol@example.com>\r\n" +
	"Bcc: secret@example.com\r\n" +
	"Subject: Hello\r\n" +
	"\r\n" +
	"Body\r\n"

type captured struct {
	path  string
	form  map[string][]string
	files map[string]string
}

// fakeMailgun answers like the Mailgun send API and records the last request.
func fakeMailgun(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got.path = r.URL.Path
		got.form = r.MultipartForm.Value
		got.files = map[string]string{}
		for name, headers := range r.MultipartForm.File {
			f, err := headers[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(f)
			_ = f.Close()
			got.files[name] = string(data)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestClient(t *testing.T, base, mode string) *Client {
	t.Helper()
	c, err := New(config.Config{
		MailgunAPIKey:   "key-test",
		MailgunDomain:   "mg.example.com",
		MailgunAPIBase:  base,
		MailgunTimeout:  5 * time.Second,
		MailgunSendMode: mode,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func parse(t *testing.T, rcpt ...string) *parser.Message {
	t.Helper()
	msg, err := parser.Parse("alice@example.com", rcpt, strings.NewReader(testMail), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

const okBody = `{"id":"<1@mg.example.com>","message":"Queued. Thank you."}`

func TestMIMEUsesEnvelopeAndStripsBcc(t *testing.T) {
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	c := newTestClient(t, srv.URL, config.SendModeMIME)

	if err := c.Send(context.Background(), parse(t, "bob@example.com", "secret@example.com")); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v3/mg.example.com/messages.mime" {
		t.Fatalf("path: %s", got.path)
	}
	if strings.Join(got.form["to"], ",") != "bob@example.com,secret@example.com" {
		t.Fatalf("to: %#v", got.form["to"])
	}
	mime := got.files["message"]
	if strings.Contains(strings.ToLower(mime), "bcc:") {
		t.Fatalf("Bcc header leaked:\n%s", mime)
	}
	if !strings.Contains(mime, "To: Bob <bob@example.com>, Carol <carol@example.com>") {
		t.Fatalf("original headers must be preserved:\n%s", mime)
	}
}

func TestParsedSendsOnlyToEnvelope(t *testing.T) {
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	c := newTestClient(t, srv.URL, config.SendModeParsed)

	if err := c.Send(context.Background(), parse(t, "carol@example.com", "secret@example.com")); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v3/mg.example.com/messages" {
		t.Fatalf("path: %s", got.path)
	}
	if to := strings.Join(got.form["to"], ","); !strings.Contains(to, "carol@example.com") || strings.Contains(to, "bob@") {
		t.Fatalf("to: %q", to)
	}
	if strings.Join(got.form["bcc"], ",") != "secret@example.com" {
		t.Fatalf("bcc: %#v", got.form["bcc"])
	}
}

func TestParsedHiddenRecipientsUseBatchSend(t *testing.T) {
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	c := newTestClient(t, srv.URL, config.SendModeParsed)

	if err := c.Send(context.Background(), parse(t, "secret@example.com", "other@example.com")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.form["to"], ",") != "secret@example.com,other@example.com" {
		t.Fatalf("to: %#v", got.form["to"])
	}
	if len(got.form["recipient-variables"]) != 1 {
		t.Fatalf("hidden recipients would see each other without recipient-variables: %#v", got.form)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status    int
		temporary bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusRequestEntityTooLarge, false},
		{http.StatusUnauthorized, true},
		{http.StatusNotFound, true},
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		srv, _ := fakeMailgun(t, tc.status, `{"message":"from parameter is not a valid address"}`)
		c := newTestClient(t, srv.URL, config.SendModeMIME)

		err := c.Send(context.Background(), parse(t, "bob@example.com"))
		var mgErr *Error
		if !errors.As(err, &mgErr) {
			t.Fatalf("%d: expected *Error, got %T %v", tc.status, err, err)
		}
		if mgErr.Status != tc.status || mgErr.Temporary() != tc.temporary {
			t.Fatalf("%d: status %d temporary %v", tc.status, mgErr.Status, mgErr.Temporary())
		}
		if !strings.Contains(mgErr.Reason, "not a valid address") {
			t.Fatalf("%d: reason %q", tc.status, mgErr.Reason)
		}
	}
}

func TestUnreachableIsTemporary(t *testing.T) {
	srv, _ := fakeMailgun(t, http.StatusOK, okBody)
	srv.Close()
	c := newTestClient(t, srv.URL, config.SendModeMIME)

	err := c.Send(context.Background(), parse(t, "bob@example.com"))
	var mgErr *Error
	if !errors.As(err, &mgErr) || mgErr.Status != 0 || !mgErr.Temporary() {
		t.Fatalf("expected temporary unreachable error, got %v", err)
	}
}
