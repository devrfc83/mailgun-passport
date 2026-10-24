package mailgunx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
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
		return mg.SetAPIBase(base)
	}
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
		return fmt.Errorf("mailgun: %w", err)
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
	for _, bcc := range msg.Bcc {
		m.AddBCC(bcc)
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
		data := a.Data
		name := a.Filename
		if a.Inline {
			m.AddReaderInline(name, io.NopCloser(bytes.NewReader(data)))
			continue
		}
		m.AddBufferAttachment(name, data)
	}
	if c.test {
		m.EnableTestMode()
	}
	return c.mg.Send(ctx, m)
}

func (c *Client) sendMIME(ctx context.Context, msg *parser.Message) (mtypes.SendMessageResponse, error) {
	recipients := append([]string{}, msg.To...)
	recipients = append(recipients, msg.Cc...)
	recipients = append(recipients, msg.Bcc...)
	if len(recipients) == 0 {
		recipients = append([]string{}, msg.RcptTo...)
	}
	m := mailgun.NewMIMEMessage(c.domain, io.NopCloser(bytes.NewReader(msg.Raw)), recipients...)
	if c.test {
		m.EnableTestMode()
	}
	return c.mg.Send(ctx, m)
}
