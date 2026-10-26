package parser

import (
	"bufio"
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

// Parse reads a message and decodes its MIME body into text, HTML and
// attachments, as parsed mode needs.
func Parse(mailFrom string, rcptTo []string, r io.Reader, maxBytes int64) (*Message, error) {
	raw, err := readMessage(rcptTo, r, maxBytes)
	if err != nil {
		return nil, err
	}
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse MIME: %w", err)
	}

	msg, err := newMessage(mailFrom, rcptTo, raw, env.GetHeader)
	if err != nil {
		return nil, err
	}
	msg.Text = env.Text
	msg.HTML = env.HTML
	for _, a := range env.Attachments {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, false))
	}
	for _, a := range env.Inlines {
		msg.Attachments = append(msg.Attachments, partToAttachment(a, true))
	}
	if msg.Text == "" && msg.HTML == "" {
		msg.Text = " "
	}
	return msg, nil
}

// ParseHeaders reads a message but decodes only its header section, so Text,
// HTML and Attachments stay empty. That is all mime mode needs, since it
// forwards Raw unchanged, and it avoids holding a decoded copy of every part.
func ParseHeaders(mailFrom string, rcptTo []string, r io.Reader, maxBytes int64) (*Message, error) {
	raw, err := readMessage(rcptTo, r, maxBytes)
	if err != nil {
		return nil, err
	}
	h, err := enmime.ReadHeader(bufio.NewReader(bytes.NewReader(raw)), ignoreErrors{})
	if err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	return newMessage(mailFrom, rcptTo, raw, func(name string) string {
		return enmime.DecodeRFC2047(h.Get(name))
	})
}

func readMessage(rcptTo []string, r io.Reader, maxBytes int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read DATA: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("message larger than SMTP_MAX_MESSAGE_BYTES (%d)", maxBytes)
	}
	if len(rcptTo) == 0 {
		return nil, fmt.Errorf("message has no recipients")
	}
	return raw, nil
}

// newMessage fills the envelope and header fields; header returns a decoded
// header value, or "" when it is missing.
func newMessage(mailFrom string, rcptTo []string, raw []byte, header func(string) string) (*Message, error) {
	msg := &Message{
		MailFrom: mailFrom,
		RcptTo:   append([]string{}, rcptTo...),
		From:     firstNonEmpty(header("From"), mailFrom),
		ReplyTo:  header("Reply-To"),
		Subject:  header("Subject"),
		Headers:  selectedHeaders(header),
		Raw:      raw,
	}
	if msg.From == "" {
		return nil, fmt.Errorf("message has no sender")
	}
	msg.To, msg.Cc, msg.Bcc = splitRecipients(rcptTo,
		splitAddresses(header("To")),
		splitAddresses(header("Cc")))
	return msg, nil
}

// ignoreErrors discards the warnings enmime collects about malformed header
// lines: it skips them, as the full parse does.
type ignoreErrors struct{}

func (ignoreErrors) AddError(string, string, ...any)   {}
func (ignoreErrors) AddWarning(string, string, ...any) {}

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

func selectedHeaders(header func(string) string) map[string]string {
	keys := []string{
		"Message-Id",
		"In-Reply-To",
		"References",
		"List-Unsubscribe",
		"List-Unsubscribe-Post",
	}
	out := map[string]string{}
	for _, k := range keys {
		if v := header(k); v != "" {
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
