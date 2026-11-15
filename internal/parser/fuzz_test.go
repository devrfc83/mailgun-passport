package parser

import (
	"bytes"
	"strings"
	"testing"
)

// The relay parses whatever an SMTP client sends, so these fuzz targets check
// that no input panics and that the invariants the rest of the relay relies
// on hold. Run them for longer with `make fuzz FUZZTIME=5m`; plain `go test`
// runs only the seeds below and the inputs stored in testdata/fuzz.

var fuzzSeeds = []string{
	simpleMail,
	"From: a@example.com\r\nTo: b@example.com\r\nSubject: Hi\r\n\r\nBody\r\n",
	"From: =?UTF-8?B?w4FsdmFybw==?= <a@example.com>\r\nSubject: =?UTF-8?Q?caf=C3=A9?=\r\n\r\nx",
	"To: \"Doe, John\" <j@example.com>, k@example.com\r\nCc: l@example.com\r\nBcc: m@example.com\r\n\r\n",
	"From: a@example.com\r\nSubject: folded\r\n header continues\r\n\tand again\r\nBcc: x@example.com\r\n y@example.com\r\n\r\nbody",
	"From: a@example.com\nTo: b@example.com\nContent-Type: multipart/mixed; boundary=\"X\"\n\n--X\nContent-Type: text/plain\n\nhi\n--X\nContent-Type: application/pdf; name=a.pdf\nContent-Disposition: attachment; filename=a.pdf\nContent-Transfer-Encoding: base64\n\nJVBERg==\n--X--\n",
	"X-Mailgun-Tag: welcome\r\nX-Mailgun-Variables: {\"a\": 1}\r\nFrom: a@example.com\r\n\r\n",
	"",
	"\r\n\r\n",
	"From",
	"From: <>\r\n\r\n",
	strings.Repeat("To: a@example.com, ", 200) + "\r\n\r\n",
}

var fuzzRecipients = []string{
	"b@example.com",
	"Bob <bob@example.com>",
	"m@example.com",
	"not an address",
	"",
}

// checkMessage verifies what every successfully parsed message must satisfy.
func checkMessage(t *testing.T, data []byte, envelope []string, msg *Message) {
	t.Helper()
	if !bytes.Equal(msg.Raw, data) {
		t.Fatal("Raw is not the message as received")
	}
	if strings.TrimSpace(msg.From) == "" {
		t.Fatal("accepted a message without a sender")
	}
	// Every envelope recipient goes to exactly one of To, Cc and Bcc.
	want := map[string]int{}
	for _, a := range envelope {
		if key := canonicalize(a); key != "" {
			want[key] = 1
		}
	}
	got := map[string]int{}
	for _, list := range [][]string{msg.To, msg.Cc, msg.Bcc} {
		for _, a := range list {
			got[canonicalize(a)]++
		}
	}
	for key, n := range got {
		if n != 1 {
			t.Fatalf("recipient %q listed %d times", key, n)
		}
		if want[key] == 0 {
			t.Fatalf("recipient %q is not in the envelope %q", key, envelope)
		}
	}
	for key := range want {
		if got[key] == 0 {
			t.Fatalf("envelope recipient %q was dropped", key)
		}
	}
	for name := range msg.MailgunHeaders {
		if !strings.HasPrefix(name, "x-mailgun-") || name != strings.ToLower(name) {
			t.Fatalf("unexpected Mailgun header %q", name)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range fuzzSeeds {
		for _, rcpt := range fuzzRecipients {
			f.Add([]byte(seed), "a@example.com", rcpt)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, mailFrom, rcpt string) {
		envelope := []string{rcpt}
		msg, err := Parse(mailFrom, envelope, bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		checkMessage(t, data, envelope, msg)
	})
}

func FuzzParseHeaders(f *testing.F) {
	for _, seed := range fuzzSeeds {
		for _, rcpt := range fuzzRecipients {
			f.Add([]byte(seed), "a@example.com", rcpt)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, mailFrom, rcpt string) {
		envelope := []string{rcpt}
		msg, err := ParseHeaders(mailFrom, envelope, bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		checkMessage(t, data, envelope, msg)
		if msg.Text != "" || msg.HTML != "" || len(msg.Attachments) != 0 {
			t.Fatal("ParseHeaders decoded the body")
		}
	})
}

// FuzzModesAgree checks that mime mode, which reads only the headers, sees the
// same sender, subject and recipients as parsed mode.
func FuzzModesAgree(f *testing.F) {
	for _, seed := range fuzzSeeds {
		f.Add([]byte(seed), "m@example.com")
	}
	f.Fuzz(func(t *testing.T, data []byte, rcpt string) {
		envelope := []string{"b@example.com", rcpt}
		full, errFull := Parse("a@example.com", envelope, bytes.NewReader(data), 1<<20)
		head, errHead := ParseHeaders("a@example.com", envelope, bytes.NewReader(data), 1<<20)
		if errFull != nil || errHead != nil {
			return
		}
		if full.Subject != head.Subject || full.ReplyTo != head.ReplyTo {
			t.Fatalf("subject %q/%q, reply-to %q/%q", full.Subject, head.Subject, full.ReplyTo, head.ReplyTo)
		}
		if strings.Join(full.To, "|") != strings.Join(head.To, "|") ||
			strings.Join(full.Cc, "|") != strings.Join(head.Cc, "|") ||
			strings.Join(full.Bcc, "|") != strings.Join(head.Bcc, "|") {
			t.Fatalf("recipients differ: %q %q %q vs %q %q %q",
				full.To, full.Cc, full.Bcc, head.To, head.Cc, head.Bcc)
		}
	})
}

func FuzzStripHeader(f *testing.F) {
	for _, seed := range fuzzSeeds {
		f.Add([]byte(seed), "Bcc")
		f.Add([]byte(seed), "subject")
	}
	f.Fuzz(func(t *testing.T, raw []byte, name string) {
		if name == "" || strings.ContainsAny(name, ":\r\n \t") {
			return
		}
		out := StripHeader(raw, name)
		end := headerEnd(raw)
		// The body, and the blank line before it, are never touched.
		if !bytes.HasSuffix(out, raw[end:]) {
			t.Fatal("the part after the header section changed")
		}
		if len(out) > len(raw) {
			t.Fatal("output is longer than the input")
		}
		// No line of the header section starts the removed field.
		prefix := strings.ToLower(name) + ":"
		for line := range bytes.Lines(out[:len(out)-len(raw[end:])]) {
			if strings.HasPrefix(strings.ToLower(string(line)), prefix) {
				t.Fatalf("header %q still present in %q", name, line)
			}
		}
		if !bytes.Equal(StripHeader(out, name), out) {
			t.Fatal("stripping twice changed the output")
		}
	})
}

func FuzzAddressDomain(f *testing.F) {
	for _, seed := range []string{
		"a@example.com", "Alice <a@Example.COM>", "\"a@b\"@example.com", "a@[127.0.0.1]",
		"a@", "@example.com", "", "a@b@c.com", "<a@example.com> junk",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, addr string) {
		domain, err := AddressDomain(addr)
		if err != nil {
			return
		}
		if domain == "" || domain != strings.ToLower(domain) {
			t.Fatalf("domain %q of %q", domain, addr)
		}
	})
}
