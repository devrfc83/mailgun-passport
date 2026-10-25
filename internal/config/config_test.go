package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setRequired defines the minimum valid configuration.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("MAILGUN_API_KEY", "key-test")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
}

func TestFromEnvDefaultsAndRequired(t *testing.T) {
	t.Setenv("MAILGUN_API_KEY", "")
	t.Setenv("MAILGUN_DOMAIN", "")
	if _, err := fromEnv(); err == nil {
		t.Fatal("expected error without MAILGUN_API_KEY")
	}

	setRequired(t)
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:587" {
		t.Fatalf("listen: %s", cfg.Listen)
	}
	if cfg.MailgunTimeout != 30*time.Second {
		t.Fatalf("timeout: %s", cfg.MailgunTimeout)
	}
	if cfg.MailgunSendMode != SendModeMIME {
		t.Fatalf("send mode: %s", cfg.MailgunSendMode)
	}
	if !cfg.AllowInsecureAuth {
		t.Fatal("insecure AUTH should default to true on loopback")
	}
	if cfg.LogFile != "" {
		t.Fatalf("log file should default to stderr only: %q", cfg.LogFile)
	}
}

func TestSystemdEnvWinsOverDotEnvSemantics(t *testing.T) {
	t.Setenv("MAILGUN_API_KEY", "from-systemd")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("SMTP_LISTEN", "127.0.0.1:2525")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailgunAPIKey != "from-systemd" {
		t.Fatalf("key: %s", cfg.MailgunAPIKey)
	}
	if cfg.Listen != "127.0.0.1:2525" {
		t.Fatalf("listen: %s", cfg.Listen)
	}
}

func TestInvalidValuesAreErrors(t *testing.T) {
	cases := map[string]string{
		"MAILGUN_SEND_MODE":        "nope",
		"SMTP_READ_TIMEOUT":        "30",
		"SMTP_MAX_MESSAGE_BYTES":   "10MB",
		"SMTP_MAX_RECIPIENTS":      "0",
		"SMTP_ALLOW_INSECURE_AUTH": "yes please",
		"MAILGUN_TESTMODE":         "maybe",
		"MAILGUN_API_BASE":         "api.mailgun.net",
		"LOG_LEVEL":                "verbose",
		"SMTP_LISTEN":              "587",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			setRequired(t)
			t.Setenv(key, value)
			_, err := fromEnv()
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected an error naming %s, got %v", key, err)
			}
		})
	}
}

func TestRefusesOpenRelay(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_LISTEN", "0.0.0.0:2525")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "open relay") {
		t.Fatalf("expected open relay refusal, got %v", err)
	}

	t.Setenv("SMTP_ALLOW_UNAUTHENTICATED", "true")
	if _, err := fromEnv(); err != nil {
		t.Fatalf("explicit opt-in should be accepted: %v", err)
	}
}

func TestNonLoopbackAuthNeedsTLSOrExplicitInsecure(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_LISTEN", "0.0.0.0:2525")
	t.Setenv("SMTP_USERNAME", "relay")
	t.Setenv("SMTP_PASSWORD", "secret")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("expected STARTTLS requirement, got %v", err)
	}

	t.Setenv("SMTP_ALLOW_INSECURE_AUTH", "true")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowInsecureAuth {
		t.Fatal("explicit SMTP_ALLOW_INSECURE_AUTH=true was ignored")
	}
}

func TestLoopbackDetection(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:587": true,
		"[::1]:587":     true,
		"localhost:587": true,
		"0.0.0.0:587":   false,
		":587":          false,
		"10.0.0.5:587":  false,
	}
	for addr, want := range cases {
		got, err := isLoopback(addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if got != want {
			t.Fatalf("%s: got %v, want %v", addr, got, want)
		}
	}
}

func TestAllowedSenderDomains(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_ALLOWED_SENDER_DOMAINS", " Example.com, mg.example.com ,")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.AllowedSenderDomains, "|") != "example.com|mg.example.com" {
		t.Fatalf("domains: %#v", cfg.AllowedSenderDomains)
	}
}

func TestGodotenvDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(".env", []byte("MAILGUN_API_KEY=from-file\nMAILGUN_DOMAIN=file.example.com\nSMTP_LISTEN=127.0.0.1:2525\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAILGUN_API_KEY", "from-systemd")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("SMTP_LISTEN", "placeholder")
	if err := os.Unsetenv("SMTP_LISTEN"); err != nil {
		t.Fatal(err)
	}

	if err := loadDotEnv(); err != nil {
		t.Fatal(err)
	}
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailgunAPIKey != "from-systemd" {
		t.Fatalf("systemd should win: %s", cfg.MailgunAPIKey)
	}
	if cfg.Listen != "127.0.0.1:2525" {
		t.Fatalf(".env should fill what systemd did not define: %s", cfg.Listen)
	}
}

func TestExplicitEnvFileMustExist(t *testing.T) {
	t.Setenv("PASSPORT_ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	if err := loadDotEnv(); err == nil {
		t.Fatal("expected error for a missing PASSPORT_ENV_FILE")
	}
}
