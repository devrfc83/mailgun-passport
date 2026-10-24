package config

import (
	"os"
	"testing"
	"time"
)

func TestFromEnvDefaultsAndRequired(t *testing.T) {
	t.Setenv("MAILGUN_API_KEY", "")
	t.Setenv("MAILGUN_DOMAIN", "")
	_, err := fromEnv()
	if err == nil {
		t.Fatal("expected error without MAILGUN_API_KEY")
	}

	t.Setenv("MAILGUN_API_KEY", "key-test")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
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

func TestInvalidSendMode(t *testing.T) {
	t.Setenv("MAILGUN_API_KEY", "k")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("MAILGUN_SEND_MODE", "nope")
	if _, err := fromEnv(); err == nil {
		t.Fatal("expected error for invalid send mode")
	}
}

func TestGodotenvDoesNotOverride(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".env", []byte("MAILGUN_API_KEY=from-file\nMAILGUN_DOMAIN=file.example.com\nSMTP_LISTEN=0.0.0.0:25\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MAILGUN_API_KEY", "from-systemd")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("SMTP_LISTEN", "placeholder")
	if err := os.Unsetenv("SMTP_LISTEN"); err != nil {
		t.Fatal(err)
	}

	loadDotEnv()
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailgunAPIKey != "from-systemd" {
		t.Fatalf("systemd debe ganar: %s", cfg.MailgunAPIKey)
	}
	if cfg.Listen != "0.0.0.0:25" {
		t.Fatalf(".env debe rellenar lo que systemd no definió: %s", cfg.Listen)
	}
}
