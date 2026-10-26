package smtpd

import (
	"encoding/base64"
	"net/textproto"
	"slices"
	"strings"
	"testing"
)

func TestAuthLogin(t *testing.T) {
	cfg := baseConfig()
	cfg.SMTPUsername = "relay"
	cfg.SMTPPassword = "s3cret"
	addr := startServer(t, cfg, &fakeSender{})
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	cases := []struct {
		name  string
		lines []string
		codes []int
	}{
		{"prompted", []string{"AUTH LOGIN", b64("relay"), b64("s3cret"), "MAIL FROM:<alice@example.com>"},
			[]int{334, 334, 235, 250}},
		{"initial response", []string{"AUTH LOGIN " + b64("relay"), b64("s3cret"), "MAIL FROM:<alice@example.com>"},
			[]int{334, 235, 250}},
		{"wrong password", []string{"AUTH LOGIN", b64("relay"), b64("wrong"), "MAIL FROM:<alice@example.com>"},
			[]int{334, 334, 535, 530}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := textproto.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, _, err := c.ReadResponse(220); err != nil {
				t.Fatalf("greeting: %v", err)
			}
			if err := c.PrintfLine("EHLO test"); err != nil {
				t.Fatal(err)
			}
			if _, msg, err := c.ReadResponse(250); err != nil {
				t.Fatalf("EHLO: %v", err)
			} else if want := "AUTH PLAIN LOGIN"; !slices.Contains(strings.Split(msg, "\n"), want) {
				t.Fatalf("EHLO does not offer %q:\n%s", want, msg)
			}
			for i, line := range tc.lines {
				if err := c.PrintfLine("%s", line); err != nil {
					t.Fatal(err)
				}
				code, msg, _ := c.ReadResponse(0)
				if code != tc.codes[i] {
					t.Fatalf("%q: got %d %s, want %d", line, code, msg, tc.codes[i])
				}
			}
		})
	}
}
