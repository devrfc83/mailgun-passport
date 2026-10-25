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
//
// The SMTP envelope (RcptTo) decides who receives the message. To and Cc
// keep the header recipients that are also in the envelope, and Bcc holds the
// envelope recipients that no header mentions. Header recipients that are not
// in the envelope are never delivered to.
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
	if len(rcptTo) == 0 {
		return nil, fmt.Errorf("message has no recipients")
	}

	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse MIME: %w", err)
	}

	msg := &Message{
		MailFrom: mailFrom,
		RcptTo:   append([]string{}, rcptTo...),
		From:     firstNonEmpty(env.GetHeader("From"), mailFrom),
		ReplyTo:  env.GetHeader("Reply-To"),
		Subject:  env.GetHeader("Subject"),
		Text:     env.Text,
		HTML:     env.HTML,
		Headers:  selectedHeaders(env),
		Raw:      raw,
	}
	msg.To, msg.Cc, msg.Bcc = splitRecipients(rcptTo,
		splitAddresses(env.GetHeader("To")),
		splitAddresses(env.GetHeader("Cc")))

	for _, a := range env.Attachments {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, false))
	}
	for _, a := range env.Inlines {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, true))
	}

	if msg.From == "" {
		return nil, fmt.Errorf("message has no sender")
	}
	if msg.Text == "" && msg.HTML == "" {
		msg.Text = " "
	}
	return msg, nil
}

// splitRecipients assigns every envelope recipient exactly once: to To or Cc
// when a header names it (keeping the header's display form), otherwise to Bcc.
func splitRecipients(envelope, headerTo, headerCc []string) (to, cc, bcc []string) {
	pending := map[string]bool{}
	for _, a := range envelope {
		if key := canonicalize(a); key != "" {
			pending[key] = true
		}
	}
	take := func(list []string) []string {
		var out []string
		for _, a := range list {
			key := canonicalize(a)
			if pending[key] {
				delete(pending, key)
				out = append(out, a)
			}
		}
		return out
	}
	to = take(headerTo)
	cc = take(headerCc)
	for _, a := range envelope {
		key := canonicalize(a)
		if pending[key] {
			delete(pending, key)
			bcc = append(bcc, a)
		}
	}
	return to, cc, bcc
}

func partToAttachment(p *enmime.Part, inline bool) Attachment {
	name := p.FileName
	// Mailgun derives the Content-ID of an inline part from its file name, so
	// the original Content-ID must be used or cid: references in HTML break.
	if inline && p.ContentID != "" {
		name = p.ContentID
	}
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

// StripHeader returns raw with every occurrence of the named header field
// (including folded continuation lines) removed from the header section.
func StripHeader(raw []byte, name string) []byte {
	end := headerEnd(raw)
	out := make([]byte, 0, len(raw))
	prefix := strings.ToLower(name) + ":"
	skipping := false
	for rest := raw[:end]; len(rest) > 0; {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line = rest[:i+1]
		}
		rest = rest[len(line):]
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				out = append(out, line...)
			}
			continue
		}
		skipping = strings.HasPrefix(strings.ToLower(string(line)), prefix)
		if !skipping {
			out = append(out, line...)
		}
	}
	return append(out, raw[end:]...)
}

// headerEnd returns the offset of the blank line that ends the header section.
func headerEnd(raw []byte) int {
	for i := 0; i < len(raw); {
		j := bytes.IndexByte(raw[i:], '\n')
		if j < 0 {
			return len(raw)
		}
		line := bytes.TrimRight(raw[i:i+j], "\r")
		if len(line) == 0 {
			return i
		}
		i += j + 1
	}
	return len(raw)
}

// AddressDomain returns the lower-cased domain of a single RFC 5322 address.
func AddressDomain(addr string) (string, error) {
	parsed, err := mail.ParseAddress(addr)
	if err != nil {
		return "", err
	}
	at := strings.LastIndexByte(parsed.Address, '@')
	if at < 0 {
		return "", fmt.Errorf("address %q has no domain", parsed.Address)
	}
	return strings.ToLower(parsed.Address[at+1:]), nil
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
