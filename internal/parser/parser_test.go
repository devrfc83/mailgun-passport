package parser

import (
	"strings"
	"testing"
)

const simpleMail = `From: Alice <alice@example.com>
To: Bob <bob@example.com>, Carol <carol@example.com>
Cc: Dave <dave@example.com>
Subject: Hola
Reply-To: alice-reply@example.com
Message-Id: <abc@example.com>
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8

Cuerpo de texto
--b1
Content-Type: text/html; charset=utf-8

<p>Cuerpo HTML</p>
--b1--
`

func TestParseMultipart(t *testing.T) {
	msg, err := Parse("alice@example.com", []string{"bob@example.com", "carol@example.com", "secret@example.com"}, strings.NewReader(simpleMail), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.From, "alice@example.com") {
		t.Fatalf("from: %s", msg.From)
	}
	if msg.Subject != "Hola" {
		t.Fatalf("subject: %s", msg.Subject)
	}
	if !strings.Contains(msg.Text, "Cuerpo de texto") {
		t.Fatalf("text: %s", msg.Text)
	}
	if !strings.Contains(msg.HTML, "Cuerpo HTML") {
		t.Fatalf("html: %s", msg.HTML)
	}
	if len(msg.To) != 2 {
		t.Fatalf("to: %#v", msg.To)
	}
	if len(msg.Cc) != 1 {
		t.Fatalf("cc: %#v", msg.Cc)
	}
	if len(msg.Bcc) != 1 || !strings.Contains(strings.Join(msg.Bcc, ","), "secret@example.com") {
		t.Fatalf("bcc (sobre SMTP): %#v", msg.Bcc)
	}
	if msg.Headers["Message-Id"] == "" {
		t.Fatal("faltaba Message-Id")
	}
}

func TestParsePlainUsesEnvelope(t *testing.T) {
	raw := "Subject: x\n\nHola\n"
	msg, err := Parse("from@example.com", []string{"to@example.com"}, strings.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if msg.From != "from@example.com" {
		t.Fatalf("from: %s", msg.From)
	}
	if len(msg.To) != 1 || msg.To[0] != "to@example.com" {
		t.Fatalf("to: %#v", msg.To)
	}
}

func TestParseRejectsOversize(t *testing.T) {
	_, err := Parse("a@b.c", []string{"d@e.f"}, strings.NewReader("Subject: x\n\n0123456789"), 8)
	if err == nil {
		t.Fatal("expected size error")
	}
}
