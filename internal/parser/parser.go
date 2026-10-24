package parser

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/jhillyerd/enmime/v2"
)

// Attachment is a MIME part that is either attached or inline.
type Attachment struct {
	Filename    string
	ContentType string
	ContentID   string
	Inline      bool
	Data        []byte
}

// Message is the interpretation of an SMTP message (envelope + MIME).
type Message struct {
	MailFrom    string
	RcptTo      []string
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	ReplyTo     string
	Subject     string
	Text        string
	HTML        string
	Headers     map[string]string
	Attachments []Attachment
	Raw         []byte
}

func Parse(mailFrom string, rcptTo []string, r io.Reader, maxBytes int64) (*Message, error) {
	limited := io.LimitReader(r, maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read DATA: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("message larger than SMTP_MAX_MESSAGE_BYTES (%d)", maxBytes)
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse MIME: %w", err)
	}

	msg := &Message{
		MailFrom: mailFrom,
		RcptTo:   append([]string{}, rcptTo...),
		From:     firstNonEmpty(env.GetHeader("From"), mailFrom),
		To:       splitAddresses(env.GetHeader("To")),
		Cc:       splitAddresses(env.GetHeader("Cc")),
		Bcc:      splitAddresses(env.GetHeader("Bcc")),
		ReplyTo:  env.GetHeader("Reply-To"),
		Subject:  env.GetHeader("Subject"),
		Text:     env.Text,
		HTML:     env.HTML,
		Headers:  selectedHeaders(env),
		Raw:      raw,
	}

	for _, a := range env.Attachments {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, false))
	}
	for _, a := range env.Inlines {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, true))
	}

	msg.applyEnvelopeRecipients()
	if msg.From == "" {
		return nil, fmt.Errorf("message has no sender")
	}
	if len(msg.deliveryRecipients()) == 0 {
		return nil, fmt.Errorf("message has no recipients")
	}
	if msg.Text == "" && msg.HTML == "" {
		msg.Text = " "
	}
	return msg, nil
}

func (m *Message) deliveryRecipients() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(list []string) {
		for _, a := range list {
			key := canonicalize(a)
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, a)
		}
	}
	add(m.To)
	add(m.Cc)
	add(m.Bcc)
	return out
}

func (m *Message) applyEnvelopeRecipients() {
	if len(m.To) == 0 && len(m.Cc) == 0 && len(m.Bcc) == 0 {
		m.To = append([]string{}, m.RcptTo...)
		return
	}
	known := map[string]struct{}{}
	for _, a := range m.To {
		known[canonicalize(a)] = struct{}{}
	}
	for _, a := range m.Cc {
		known[canonicalize(a)] = struct{}{}
	}
	for _, a := range m.Bcc {
		known[canonicalize(a)] = struct{}{}
	}
	for _, a := range m.RcptTo {
		key := canonicalize(a)
		if key == "" {
			continue
		}
		if _, ok := known[key]; ok {
			continue
		}
		m.Bcc = append(m.Bcc, a)
		known[key] = struct{}{}
	}
}

func partToAttachment(p *enmime.Part, inline bool) Attachment {
	name := p.FileName
	if name == "" {
		name = p.ContentID
	}
	if name == "" {
		name = "attachment"
	}
	return Attachment{
		Filename:    name,
		ContentType: p.ContentType,
		ContentID:   p.ContentID,
		Inline:      inline,
		Data:        p.Content,
	}
}

func selectedHeaders(env *enmime.Envelope) map[string]string {
	keys := []string{
		"Message-Id",
		"In-Reply-To",
		"References",
		"List-Unsubscribe",
		"List-Unsubscribe-Post",
	}
	out := map[string]string{}
	for _, k := range keys {
		if v := env.GetHeader(k); v != "" {
			out[k] = v
		}
	}
	return out
}

func splitAddresses(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(header)
	if err != nil {
		parts := strings.Split(header, ",")
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func canonicalize(addr string) string {
	parsed, err := mail.ParseAddress(addr)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(addr))
	}
	return strings.ToLower(parsed.Address)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
