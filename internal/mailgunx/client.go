package mailgunx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devrfc83/mailgun-passport/internal/config"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

const (
	apiBaseUS = "https://api.mailgun.net"
	apiBaseEU = "https://api.eu.mailgun.net"
)

type Client struct {
	http    *http.Client
	base    string
	apiKey  string
	domain  string
	timeout time.Duration
	mode    string
	test    bool
	log     *slog.Logger
	health  health
}

// recheckInterval is how often Health probes a failing Mailgun again; tests
// shorten it.
var recheckInterval = 30 * time.Second

// health remembers whether the last API call failed in a way that makes
// clients retry: unreachable, timeout, 401/403/404, 429 or 5xx.
type health struct {
	mu       sync.Mutex
	err      error
	lastCall time.Time
	checking bool
}

func New(cfg config.Config, log *slog.Logger) *Client {
	base := apiBase(cfg.MailgunAPIBase)
	if strings.HasPrefix(strings.ToLower(base), "http://") {
		log.Warn("MAILGUN_API_BASE uses plain HTTP: the API key and every message cross the network unencrypted",
			"base", base)
	}
	return &Client{
		// The default transport honors HTTPS_PROXY and NO_PROXY.
		http:    &http.Client{},
		base:    base,
		apiKey:  cfg.MailgunAPIKey,
		domain:  cfg.MailgunDomain,
		timeout: cfg.MailgunTimeout,
		mode:    cfg.MailgunSendMode,
		test:    cfg.MailgunTestMode,
		log:     log,
	}
}

func apiBase(base string) string {
	base = strings.TrimSpace(base)
	switch strings.ToLower(base) {
	case "", "us":
		return apiBaseUS
	case "eu":
		return apiBaseEU
	default:
		return strings.TrimRight(base, "/")
	}
}

// Error describes a failed Mailgun API call.
type Error struct {
	// Status is the HTTP status returned by Mailgun, or 0 if it was not reached.
	Status int
	// Reason is a short description that is safe to return to SMTP clients.
	Reason string
	Err    error
}

func (e *Error) Error() string         { return e.Err.Error() }
func (e *Error) Unwrap() error         { return e.Err }
func (e *Error) ClientMessage() string { return e.Reason }

// Temporary reports whether sending the same message later may succeed.
// Authentication and unknown-domain errors count as temporary: they are
// operator mistakes, and the client should keep the message until they are fixed.
func (e *Error) Temporary() bool {
	switch e.Status {
	case 0, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return e.Status >= 500
}

func newHTTPError(status int, body []byte) *Error {
	reason := fmt.Sprintf("Mailgun API returned HTTP %d", status)
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		reason += ": " + sanitize(parsed.Message, 200)
	}
	return &Error{
		Status: status,
		Reason: reason,
		Err:    fmt.Errorf("mailgun: HTTP %d: %s", status, sanitize(string(body), 500)),
	}
}

// sanitize keeps s on a single SMTP reply line of at most n bytes, cutting
// only between UTF-8 characters.
func sanitize(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return s
}

func (c *Client) Send(ctx context.Context, msg *parser.Message) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	form := &formData{}
	endpoint := "messages"
	if c.mode == config.SendModeMIME {
		endpoint = "messages.mime"
		c.mimeFields(form, msg)
	} else {
		if err := c.parsedFields(form, msg); err != nil {
			return &Error{Reason: "cannot encode message", Err: err}
		}
	}
	if c.test {
		form.field("o:testmode", "yes")
	}

	var resp struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if err := c.post(ctx, endpoint, form, &resp); err != nil {
		return err
	}
	c.log.Info("message accepted by Mailgun",
		"id", resp.ID,
		"message", resp.Message,
		"from", msg.From,
		"recipients", len(msg.RcptTo),
		"mode", c.mode,
	)
	// Subjects can carry personal data, so they stay out of info logs.
	c.log.Debug("accepted message details", "id", resp.ID, "subject", msg.Subject)
	return nil
}

// Verify checks the API key and domain without sending anything: it posts a
// message with no fields, which Mailgun rejects with 400 once the key and
// domain are accepted. Unlike the domains API, this works with sending keys
// limited to one domain. Test mode guards against it ever being delivered.
func (c *Client) Verify(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	form := &formData{}
	form.field("o:testmode", "yes")
	var resp struct{}
	err := c.post(ctx, "messages", form, &resp)
	if e, ok := errors.AsType[*Error](err); ok && e.Status == http.StatusBadRequest {
		return nil
	}
	return err
}

// Fatal reports whether err from Verify means the configuration is wrong,
// rather than Mailgun being unreachable or having trouble.
func Fatal(err error) bool {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		return false
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

func (c *Client) parsedFields(form *formData, msg *parser.Message) error {
	form.field("from", msg.From)
	form.field("subject", msg.Subject)
	if msg.Text != "" {
		form.field("text", msg.Text)
	}
	if msg.HTML != "" {
		form.field("html", msg.HTML)
	}
	for _, to := range msg.To {
		form.field("to", to)
	}
	for _, cc := range msg.Cc {
		form.field("cc", cc)
	}
	if len(msg.To) == 0 && len(msg.Cc) == 0 {
		// The Mailgun API needs a "to" recipient. Recipient variables turn the
		// call into a batch send, so each hidden recipient only sees itself.
		vars := map[string]map[string]any{}
		for _, r := range msg.Bcc {
			form.field("to", r)
			vars[r] = map[string]any{}
		}
		j, err := json.Marshal(vars)
		if err != nil {
			return err
		}
		form.field("recipient-variables", string(j))
	} else {
		for _, bcc := range msg.Bcc {
			form.field("bcc", bcc)
		}
	}
	if msg.ReplyTo != "" {
		form.field("h:Reply-To", msg.ReplyTo)
	}
	for k, v := range msg.Headers {
		form.field("h:"+k, v)
	}
	for _, a := range msg.Attachments {
		// Mailgun derives an inline part's Content-ID from its file name, which
		// the parser already set; the part's Content-Type is sent explicitly so
		// Mailgun does not have to guess it from that name.
		field := "attachment"
		if a.Inline {
			field = "inline"
		}
		form.file(field, a.Filename, a.ContentType, a.Data)
	}
	return nil
}

// mimeFields forwards the message untouched (minus Bcc) to the SMTP envelope recipients.
func (c *Client) mimeFields(form *formData, msg *parser.Message) {
	for _, to := range msg.RcptTo {
		form.field("to", to)
	}
	form.file("message", "message.mime", "message/rfc822", parser.StripHeader(msg.Raw, "Bcc"))
}

// Health returns nil while Mailgun answers normally, and the last call's
// error after a failure that makes clients retry. While it fails, Health
// starts a background Verify at most every recheckInterval, so the state
// recovers without waiting for the next message.
func (c *Client) Health() error {
	h := &c.health
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil && !h.checking && time.Since(h.lastCall) >= recheckInterval {
		h.checking = true
		go func() {
			_ = c.Verify(context.Background())
			h.mu.Lock()
			h.checking = false
			h.mu.Unlock()
		}()
	}
	return h.err
}

func (c *Client) post(ctx context.Context, endpoint string, form *formData, out any) error {
	err := c.do(ctx, endpoint, form, out)
	e, ok := errors.AsType[*Error](err)
	failing := ok && e.Temporary()
	c.health.mu.Lock()
	c.health.lastCall = time.Now()
	c.health.err = nil
	if failing {
		c.health.err = err
	}
	c.health.mu.Unlock()
	return err
}

func (c *Client) do(ctx context.Context, endpoint string, form *formData, out any) error {
	body, contentType, err := form.encode()
	if err != nil {
		return &Error{Reason: "cannot encode message", Err: err}
	}
	u := c.base + "/v3/" + url.PathEscape(c.domain) + "/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return &Error{Reason: "invalid Mailgun API URL", Err: err}
	}
	req.SetBasicAuth("api", c.apiKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mailgun-passport")

	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Reason: "Mailgun API unreachable", Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newHTTPError(resp.StatusCode, data)
	}
	// The message is queued at this point; an odd body only costs the log its id.
	if err == nil {
		err = json.Unmarshal(data, out)
	}
	if err != nil {
		c.log.Warn("unexpected Mailgun response body", "err", err)
	}
	return nil
}

// formData is a multipart/form-data body. Unlike mime/multipart's
// CreateFormFile, file parts keep their own Content-Type.
type formData struct {
	fields []formField
	files  []formFile
}

type formField struct{ name, value string }

type formFile struct {
	name, filename, contentType string
	data                        []byte
}

func (f *formData) field(name, value string) {
	f.fields = append(f.fields, formField{name, value})
}

func (f *formData) file(name, filename, contentType string, data []byte) {
	f.files = append(f.files, formFile{name, filename, contentType, data})
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func (f *formData) encode() (*bytes.Buffer, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for _, fd := range f.fields {
		if err := w.WriteField(fd.name, fd.value); err != nil {
			return nil, "", err
		}
	}
	for _, fl := range f.files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			quoteEscaper.Replace(fl.name), quoteEscaper.Replace(fl.filename)))
		contentType := fl.contentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		h.Set("Content-Type", contentType)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(fl.data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf, w.FormDataContentType(), nil
}
