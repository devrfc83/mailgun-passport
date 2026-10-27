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
	if cfg.MaxConnections != 100 {
		t.Fatalf("max connections: %d", cfg.MaxConnections)
	}
	if cfg.AuthMaxFailures != 10 {
		t.Fatalf("AUTH failures per address: %d", cfg.AuthMaxFailures)
	}
	if cfg.MailgunSendMode != SendModeMIME {
		t.Fatalf("send mode: %s", cfg.MailgunSendMode)
	}
	if !cfg.AllowInsecureAuth {
		t.Fatal("insecure AUTH should default to true on loopback")
	}
	if cfg.HealthListen != "" {
		t.Fatalf("health endpoints should be off by default: %q", cfg.HealthListen)
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
		"SMTP_DOMAIN":              `"mail.example.com"`,
		"SMTP_USERNAME":            "'relay'",

		"SMTP_ALLOWED_SENDER_DOMAINS": "*,example.com",
		"HEALTH_LISTEN":               "8080",
		"SMTP_AUTH_MAX_FAILURES":      "-1",
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

func TestAuthLimitCanBeDisabled(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_AUTH_MAX_FAILURES", "0")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthMaxFailures != 0 {
		t.Fatalf("AUTH failures per address: %d", cfg.AuthMaxFailures)
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

func TestEnvFileDoesNotOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passport.env")
	if err := os.WriteFile(path, []byte("MAILGUN_API_KEY=from-file\nMAILGUN_DOMAIN=file.example.com\nSMTP_LISTEN=127.0.0.1:2525\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PASSPORT_ENV_FILE", path)
	t.Setenv("MAILGUN_API_KEY", "from-systemd")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("SMTP_LISTEN", "placeholder")
	if err := os.Unsetenv("SMTP_LISTEN"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
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
	if err := loadEnvFile(); err == nil {
		t.Fatal("expected error for a missing PASSPORT_ENV_FILE")
	}
}

func TestDotEnvIsNotReadImplicitly(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("SMTP_LISTEN=127.0.0.1:2525\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setRequired(t)
	t.Setenv("PASSPORT_ENV_FILE", "")
	t.Setenv("SMTP_LISTEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:587" {
		t.Fatalf(".env in the working directory was read: listen %s", cfg.Listen)
	}
}

func TestQuotedSecretIsRejectedWithoutLeaking(t *testing.T) {
	setRequired(t)
	t.Setenv("MAILGUN_API_KEY", `"key-very-secret"`)
	_, err := fromEnv()
	if err == nil || !strings.Contains(err.Error(), "MAILGUN_API_KEY: value is wrapped in quotes") {
		t.Fatalf("expected a quote error, got %v", err)
	}
	if strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("error leaks the secret: %v", err)
	}
}

func TestSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "api-key")
	passFile := filepath.Join(dir, "smtp-password")
	if err := os.WriteFile(keyFile, []byte("key-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(" pass with spaces \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILGUN_API_KEY", "")
	t.Setenv("MAILGUN_DOMAIN", "mg.example.com")
	t.Setenv("MAILGUN_API_KEY_FILE", keyFile)
	t.Setenv("SMTP_USERNAME", "relay")
	t.Setenv("SMTP_PASSWORD_FILE", passFile)

	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MailgunAPIKey != "key-from-file" {
		t.Fatalf("key: %q", cfg.MailgunAPIKey)
	}
	if cfg.SMTPPassword != " pass with spaces " {
		t.Fatalf("password: %q", cfg.SMTPPassword)
	}
}

func TestSecretFileErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		key, file, want string
	}{
		"both set":     {"key-test", empty, "both set"},
		"missing file": {"", filepath.Join(dir, "missing"), "MAILGUN_API_KEY_FILE"},
		"empty file":   {"", empty, "is empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("MAILGUN_API_KEY", tc.key)
			t.Setenv("MAILGUN_API_KEY_FILE", tc.file)
			if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDefaultSenderDomainsFollowMailgunDomain(t *testing.T) {
	cases := map[string]string{
		"mg.example.com":    "example.com|*.example.com",
		"MG.Example.COM":    "example.com|*.example.com",
		"example.com":       "example.com|*.example.com",
		"mg.example.co.uk":  "example.co.uk|*.example.co.uk",
		"a.b.mg.example.cl": "example.cl|*.example.cl",
	}
	for mgDomain, want := range cases {
		setRequired(t)
		t.Setenv("MAILGUN_DOMAIN", mgDomain)
		t.Setenv("SMTP_ALLOWED_SENDER_DOMAINS", "")
		cfg, err := fromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(cfg.AllowedSenderDomains, "|"); got != want {
			t.Fatalf("%s: got %s, want %s", mgDomain, got, want)
		}
	}
}

func TestSenderDomainAllowed(t *testing.T) {
	cfg := Config{AllowedSenderDomains: []string{"example.com", "*.example.com", "other.test"}}
	cases := map[string]bool{
		"example.com":      true,
		"mg.example.com":   true,
		"a.b.example.com":  true,
		"other.test":       true,
		"sub.other.test":   false,
		"notexample.com":   false,
		"example.com.evil": false,
		"evil.test":        false,
	}
	for domain, want := range cases {
		if got := cfg.SenderDomainAllowed(domain); got != want {
			t.Fatalf("%s: got %v, want %v", domain, got, want)
		}
	}
	if !(Config{}).SenderDomainAllowed("anything.test") {
		t.Fatal("an empty list must allow any sender")
	}
}

func TestAnySenderNeedsExplicitStar(t *testing.T) {
	setRequired(t)
	t.Setenv("SMTP_ALLOWED_SENDER_DOMAINS", "*")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedSenderDomains) != 0 {
		t.Fatalf("\"*\" should allow any sender: %#v", cfg.AllowedSenderDomains)
	}

	t.Setenv("SMTP_ALLOWED_SENDER_DOMAINS", "ex*ample.com")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "not a domain") {
		t.Fatalf("expected a pattern error, got %v", err)
	}
}
