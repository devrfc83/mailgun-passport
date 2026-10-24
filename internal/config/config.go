package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	SendModeParsed = "parsed"
	SendModeMIME   = "mime"
)

// Config groups configuration read from the environment.
// systemd (Environment= / EnvironmentFile=) takes precedence over .env:
// godotenv does not override variables that already exist.
type Config struct {
	Listen            string
	SMTPDomain        string
	MaxMessageBytes   int64
	MaxRecipients     int
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	SMTPUsername      string
	SMTPPassword      string
	AllowInsecureAuth bool
	TLSCertFile       string
	TLSKeyFile        string
	MailgunAPIKey     string
	MailgunDomain     string
	MailgunAPIBase    string
	MailgunTimeout    time.Duration
	MailgunTestMode   bool
	MailgunSendMode   string
	LogLevel          string
	LogFile           string
}

func Load() (Config, error) {
	loadDotEnv()
	return fromEnv()
}

func loadDotEnv() {
	candidates := []string{}
	if p := strings.TrimSpace(os.Getenv("PASSPORT_ENV_FILE")); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, ".env")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}
	seen := map[string]struct{}{}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if err := godotenv.Load(path); err == nil {
			return
		}
	}
}

func fromEnv() (Config, error) {
	cfg := Config{
		Listen:            env("SMTP_LISTEN", "127.0.0.1:587"),
		SMTPDomain:        env("SMTP_DOMAIN", "localhost"),
		MaxMessageBytes:   envInt64("SMTP_MAX_MESSAGE_BYTES", 10<<20),
		MaxRecipients:     int(envInt64("SMTP_MAX_RECIPIENTS", 50)),
		ReadTimeout:       envDuration("SMTP_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:      envDuration("SMTP_WRITE_TIMEOUT", 30*time.Second),
		SMTPUsername:      env("SMTP_USERNAME", ""),
		SMTPPassword:      env("SMTP_PASSWORD", ""),
		AllowInsecureAuth: envBool("SMTP_ALLOW_INSECURE_AUTH", true),
		TLSCertFile:       env("SMTP_TLS_CERT_FILE", ""),
		TLSKeyFile:        env("SMTP_TLS_KEY_FILE", ""),
		MailgunAPIKey:     env("MAILGUN_API_KEY", ""),
		MailgunDomain:     env("MAILGUN_DOMAIN", ""),
		MailgunAPIBase:    env("MAILGUN_API_BASE", "us"),
		MailgunTimeout:    envDuration("MAILGUN_TIMEOUT", 30*time.Second),
		MailgunTestMode:   envBool("MAILGUN_TESTMODE", false),
		MailgunSendMode:   strings.ToLower(env("MAILGUN_SEND_MODE", SendModeParsed)),
		LogLevel:          env("LOG_LEVEL", "info"),
		LogFile:           env("LOG_FILE", "/var/log/mailgun-passport.log"),
	}
	if cfg.MailgunAPIKey == "" {
		return cfg, fmt.Errorf("MAILGUN_API_KEY is required")
	}
	if cfg.MailgunDomain == "" {
		return cfg, fmt.Errorf("MAILGUN_DOMAIN is required")
	}
	switch cfg.MailgunSendMode {
	case SendModeParsed, SendModeMIME:
	default:
		return cfg, fmt.Errorf("MAILGUN_SEND_MODE must be %q or %q", SendModeParsed, SendModeMIME)
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return cfg, fmt.Errorf("SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE must be defined together")
	}
	if cfg.AuthRequired() && (cfg.SMTPUsername == "" || cfg.SMTPPassword == "") {
		return cfg, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD must be defined together")
	}
	return cfg, nil
}

func (c Config) AuthRequired() bool {
	return c.SMTPUsername != "" || c.SMTPPassword != ""
}

func env(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt64(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
