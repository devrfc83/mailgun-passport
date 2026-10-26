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

	"github.com/devrfc83/mailgun-passport/internal/config"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

const testMail = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>, Carol <carol@example.com>\r\n" +
	"Bcc: secret@example.com\r\n" +
	"Subject: Hello\r\n" +
	"\r\n" +
	"Body\r\n"

type captured struct {
	path  string
	auth  string
	form  map[string][]string
	files map[string]string
	// types and names map a file field to its part's Content-Type and file name.
	types map[string]string
	names map[string]string
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
		if user, pass, ok := r.BasicAuth(); ok {
			got.auth = user + ":" + pass
		}
		got.form = r.MultipartForm.Value
		got.files = map[string]string{}
		got.types = map[string]string{}
		got.names = map[string]string{}
		for name, headers := range r.MultipartForm.File {
			got.types[name] = headers[0].Header.Get("Content-Type")
			got.names[name] = headers[0].Filename
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
	return New(config.Config{
		MailgunAPIKey:   "key-test",
		MailgunDomain:   "mg.example.com",
		MailgunAPIBase:  base,
		MailgunTimeout:  5 * time.Second,
		MailgunSendMode: mode,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	if got.auth != "api:key-test" {
		t.Fatalf("basic auth: %q", got.auth)
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

func TestParsedAttachmentsKeepContentType(t *testing.T) {
	raw := strings.Join([]string{
		"From: a@example.com",
		"To: b@example.com",
		"Subject: files",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="m"`,
		"",
		"--m",
		`Content-Type: multipart/related; boundary="r"`,
		"",
		"--r",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<img src="cid:image001.png@01DA">`,
		"--r",
		"Content-Type: image/png",
		"Content-Disposition: inline; filename=image001.png",
		"Content-ID: <image001.png@01DA>",
		"Content-Transfer-Encoding: base64",
		"",
		"iVBORw0KGgo=",
		"--r--",
		"--m",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="report.pdf"`,
		"",
		"%PDF-1.7",
		"--m--",
		"",
	}, "\r\n")
	msg, err := parser.Parse("a@example.com", []string{"b@example.com"}, strings.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	srv, got := fakeMailgun(t, http.StatusOK, okBody)
	c := newTestClient(t, srv.URL, config.SendModeParsed)

	if err := c.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if got.types["inline"] != "image/png" || got.names["inline"] != "image001.png@01DA" {
		t.Fatalf("inline: type %q name %q", got.types["inline"], got.names["inline"])
	}
	if got.types["attachment"] != "application/pdf" || got.names["attachment"] != "report.pdf" {
		t.Fatalf("attachment: type %q name %q", got.types["attachment"], got.names["attachment"])
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	srv, _ := fakeMailgun(t, http.StatusForbidden, "Forbidden")
	c := newTestClient(t, srv.URL, config.SendModeMIME)

	err := c.Send(context.Background(), parse(t, "bob@example.com"))
	var mgErr *Error
	if !errors.As(err, &mgErr) || mgErr.Status != http.StatusForbidden || !mgErr.Temporary() {
		t.Fatalf("expected temporary 403, got %v", err)
	}
	if mgErr.Reason != "Mailgun API returned HTTP 403" {
		t.Fatalf("reason: %q", mgErr.Reason)
	}
}

func TestAPIBase(t *testing.T) {
	cases := map[string]string{
		"":                          apiBaseUS,
		"us":                        apiBaseUS,
		"EU":                        apiBaseEU,
		"https://mg.internal:8443/": "https://mg.internal:8443",
	}
	for in, want := range cases {
		if got := apiBase(in); got != want {
			t.Fatalf("%q: got %q, want %q", in, got, want)
		}
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

func TestVerify(t *testing.T) {
	cases := []struct {
		status int
		ok     bool
		fatal  bool
	}{
		{http.StatusBadRequest, true, false},
		{http.StatusUnauthorized, false, true},
		{http.StatusForbidden, false, true},
		{http.StatusNotFound, false, true},
		{http.StatusInternalServerError, false, false},
	}
	for _, tc := range cases {
		srv, got := fakeMailgun(t, tc.status, `{"message":"from parameter is missing"}`)
		err := newTestClient(t, srv.URL, config.SendModeMIME).Verify(context.Background())
		if (err == nil) != tc.ok || Fatal(err) != tc.fatal {
			t.Fatalf("HTTP %d: err %v, fatal %v", tc.status, err, Fatal(err))
		}
		if got.path != "/v3/mg.example.com/messages" || got.form["o:testmode"][0] != "yes" ||
			len(got.form) != 1 || len(got.files) != 0 {
			t.Fatalf("HTTP %d: request %+v", tc.status, got)
		}
	}

	// Unreachable is not a configuration error.
	srv, _ := fakeMailgun(t, http.StatusOK, "{}")
	srv.Close()
	err := newTestClient(t, srv.URL, config.SendModeMIME).Verify(context.Background())
	if err == nil || Fatal(err) {
		t.Fatalf("unreachable: err %v, fatal %v", err, Fatal(err))
	}
}
