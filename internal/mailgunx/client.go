package mailgunx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mailgun/mailgun-go/v5"
	"github.com/mailgun/mailgun-go/v5/mtypes"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/parser"
)

type Client struct {
	mg      *mailgun.Client
	domain  string
	timeout time.Duration
	mode    string
	test    bool
	log     *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) (*Client, error) {
	mg := mailgun.NewMailgun(cfg.MailgunAPIKey)
	if err := applyAPIBase(mg, cfg.MailgunAPIBase); err != nil {
		return nil, err
	}
	return &Client{
		mg:      mg,
		domain:  cfg.MailgunDomain,
		timeout: cfg.MailgunTimeout,
		mode:    cfg.MailgunSendMode,
		test:    cfg.MailgunTestMode,
		log:     log,
	}, nil
}

func applyAPIBase(mg *mailgun.Client, base string) error {
	base = strings.TrimSpace(base)
	switch strings.ToLower(base) {
	case "", "us", "https://api.mailgun.net":
		return nil
	case "eu", "https://api.eu.mailgun.net":
		return mg.SetAPIBase(mailgun.APIBaseEU)
	default:
		return mg.SetAPIBase(strings.TrimRight(base, "/"))
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

func newError(err error) *Error {
	var apiErr *mailgun.UnexpectedResponseError
	if !errors.As(err, &apiErr) {
		return &Error{Reason: "Mailgun API unreachable", Err: err}
	}
	reason := fmt.Sprintf("Mailgun API returned HTTP %d", apiErr.Actual)
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(apiErr.Data, &body) == nil && body.Message != "" {
		reason += ": " + sanitize(body.Message, 200)
	}
	return &Error{Status: apiErr.Actual, Reason: reason, Err: err}
}

// sanitize keeps s on a single SMTP reply line of at most n bytes.
func sanitize(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func (c *Client) Send(ctx context.Context, msg *parser.Message) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var (
		resp mtypes.SendMessageResponse
		err  error
	)
	if c.mode == config.SendModeMIME {
		resp, err = c.sendMIME(ctx, msg)
	} else {
		resp, err = c.sendParsed(ctx, msg)
	}
	if err != nil {
		return newError(err)
	}
	c.log.Info("message accepted by Mailgun",
		"id", resp.ID,
		"message", resp.Message,
		"from", msg.From,
		"subject", msg.Subject,
		"recipients", len(msg.RcptTo),
		"mode", c.mode,
	)
	return nil
}

func (c *Client) sendParsed(ctx context.Context, msg *parser.Message) (mtypes.SendMessageResponse, error) {
	m := mailgun.NewMessage(c.domain, msg.From, msg.Subject, msg.Text, msg.To...)
	for _, cc := range msg.Cc {
		m.AddCC(cc)
	}
	if len(msg.To) == 0 && len(msg.Cc) == 0 {
		// The Mailgun API needs a "to" recipient. Recipient variables turn the
		// call into a batch send, so each hidden recipient only sees itself.
		for _, r := range msg.Bcc {
			if err := m.AddRecipientAndVariables(r, map[string]any{}); err != nil {
				return mtypes.SendMessageResponse{}, err
			}
		}
	} else {
		for _, bcc := range msg.Bcc {
			m.AddBCC(bcc)
		}
	}
	if msg.HTML != "" {
		m.SetHTML(msg.HTML)
	}
	if msg.ReplyTo != "" {
		m.SetReplyTo(msg.ReplyTo)
	}
	for k, v := range msg.Headers {
		m.AddHeader(k, v)
	}
	for _, a := range msg.Attachments {
		if a.Inline {
			m.AddReaderInline(a.Filename, io.NopCloser(bytes.NewReader(a.Data)))
			continue
		}
		m.AddBufferAttachment(a.Filename, a.Data)
	}
	if c.test {
		m.EnableTestMode()
	}
	return c.mg.Send(ctx, m)
}

// sendMIME forwards the message untouched (minus Bcc) to the SMTP envelope recipients.
func (c *Client) sendMIME(ctx context.Context, msg *parser.Message) (mtypes.SendMessageResponse, error) {
	raw := parser.StripHeader(msg.Raw, "Bcc")
	m := mailgun.NewMIMEMessage(c.domain, io.NopCloser(bytes.NewReader(raw)), msg.RcptTo...)
	if c.test {
		m.EnableTestMode()
	}
	return c.mg.Send(ctx, m)
}
