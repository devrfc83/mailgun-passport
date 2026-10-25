package parser

import (
	"strings"
	"testing"
)

const simpleMail = `From: Alice <alice@example.com>
To: Bob <bob@example.com>, Carol <carol@example.com>
Cc: Dave <dave@example.com>
Subject: Hello
Reply-To: alice-reply@example.com
Message-Id: <abc@example.com>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8

Plain text body
--b1
Content-Type: text/html; charset=utf-8

<p>HTML body</p>
--b1--
`

func TestParseMultipart(t *testing.T) {
	envelope := []string{"bob@example.com", "carol@example.com", "dave@example.com", "secret@example.com"}
	msg, err := Parse("alice@example.com", envelope, strings.NewReader(simpleMail), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.From, "alice@example.com") {
		t.Fatalf("from: %s", msg.From)
	}
	if msg.Subject != "Hello" {
		t.Fatalf("subject: %s", msg.Subject)
	}
	if !strings.Contains(msg.Text, "Plain text body") {
		t.Fatalf("text: %s", msg.Text)
	}
	if !strings.Contains(msg.HTML, "HTML body") {
		t.Fatalf("html: %s", msg.HTML)
	}
	if len(msg.To) != 2 || !strings.Contains(msg.To[0], "Bob") {
		t.Fatalf("to: %#v", msg.To)
	}
	if len(msg.Cc) != 1 {
		t.Fatalf("cc: %#v", msg.Cc)
	}
	if len(msg.Bcc) != 1 || msg.Bcc[0] != "secret@example.com" {
		t.Fatalf("bcc (envelope only): %#v", msg.Bcc)
	}
	if msg.Headers["Message-Id"] == "" {
		t.Fatal("missing Message-Id")
	}
}

// An MTA that delivers one copy per recipient must not make the relay send
// the message to every address in the headers.
func TestEnvelopeDecidesRecipients(t *testing.T) {
	msg, err := Parse("alice@example.com", []string{"carol@example.com"}, strings.NewReader(simpleMail), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.To) != 1 || !strings.Contains(msg.To[0], "carol@example.com") {
		t.Fatalf("to: %#v", msg.To)
	}
	if len(msg.Cc) != 0 || len(msg.Bcc) != 0 {
		t.Fatalf("cc: %#v bcc: %#v", msg.Cc, msg.Bcc)
	}
}

func TestParsePlainUsesEnvelope(t *testing.T) {
	raw := "Subject: x\n\nHello\n"
	msg, err := Parse("from@example.com", []string{"to@example.com"}, strings.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "from@example.com" {
		t.Fatalf("from: %s", msg.From)
	}
	if len(msg.To) != 0 || len(msg.Bcc) != 1 || msg.Bcc[0] != "to@example.com" {
		t.Fatalf("to: %#v bcc: %#v", msg.To, msg.Bcc)
	}
}

func TestParseRejectsOversize(t *testing.T) {
	_, err := Parse("a@b.c", []string{"d@e.f"}, strings.NewReader("Subject: x\n\n0123456789"), 8)
	if err == nil {
		t.Fatal("expected size error")
	}
}

func TestParseRejectsEmptyEnvelope(t *testing.T) {
	if _, err := Parse("a@b.c", nil, strings.NewReader(simpleMail), 1<<20); err == nil {
		t.Fatal("expected error without envelope recipients")
	}
}

func TestInlineUsesContentID(t *testing.T) {
	raw := strings.Join([]string{
		"From: a@example.com",
		"To: b@example.com",
		"Subject: inline",
		"MIME-Version: 1.0",
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
		"",
	}, "\r\n")
	msg, err := Parse("a@example.com", []string{"b@example.com"}, strings.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments: %#v", msg.Attachments)
	}
	if a := msg.Attachments[0]; !a.Inline || a.Filename != "image001.png@01DA" {
		t.Fatalf("inline attachment: %+v", a)
	}
}

func TestStripHeader(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"Bcc: hidden@example.com,\r\n" +
		"  other@example.com\r\n" +
		"To: b@example.com\r\n" +
		"bcc: again@example.com\r\n" +
		"\r\n" +
		"Bcc: this line is body text\r\n"
	got := string(StripHeader([]byte(raw), "Bcc"))
	want := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"\r\n" +
		"Bcc: this line is body text\r\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestAddressDomain(t *testing.T) {
	d, err := AddressDomain(`"Alice" <Alice@Example.COM>`)
	if err != nil || d != "example.com" {
		t.Fatalf("domain: %q err: %v", d, err)
	}
	if _, err := AddressDomain("not an address"); err == nil {
		t.Fatal("expected error")
	}
}
