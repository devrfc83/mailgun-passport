package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/net/publicsuffix"
)

const (
	SendModeParsed = "parsed"
	SendModeMIME   = "mime"
)

// Config groups configuration read from the environment.
// systemd (Environment= / EnvironmentFile=) takes precedence over .env:
// godotenv does not override variables that already exist.
type Config struct {
	Listen               string
	SMTPDomain           string
	MaxMessageBytes      int64
	MaxRecipients        int
	MaxConnections       int
	AuthMaxFailures      int // failed AUTH attempts per client address before blocking; 0 disables
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	SMTPUsername         string
	SMTPPassword         string
	AllowInsecureAuth    bool
	AllowUnauthenticated bool
	// AllowedSenderDomains holds exact domains and "*.domain" patterns for
	// subdomains. Empty allows any sender.
	AllowedSenderDomains []string
	TLSCertFile          string
	TLSKeyFile           string
	MailgunAPIKey        string
	MailgunDomain        string
	MailgunAPIBase       string
	MailgunTimeout       time.Duration
	MailgunTestMode      bool
	MailgunSendMode      string
	LogLevel             string
	LogFile              string
	// HealthListen is the address of the HTTP health endpoints; empty disables them.
	HealthListen string
}

func Load() (Config, error) {
	if err := loadEnvFile(); err != nil {
		return Config{}, err
	}
	return fromEnv()
}

// loadEnvFile fills missing variables from the file named by PASSPORT_ENV_FILE,
// which must exist. No file is read implicitly: under systemd the working
// directory is /, and a stray .env there or next to the binary would change
// the configuration without anyone noticing.
func loadEnvFile() error {
	p := strings.TrimSpace(os.Getenv("PASSPORT_ENV_FILE"))
	if p == "" {
		return nil
	}
	if err := godotenv.Load(p); err != nil {
		return fmt.Errorf("PASSPORT_ENV_FILE: %w", err)
	}
	return nil
}

func fromEnv() (Config, error) {
	var e envReader
	cfg := Config{
		Listen:               e.str("SMTP_LISTEN", "127.0.0.1:587"),
		SMTPDomain:           e.str("SMTP_DOMAIN", "localhost"),
		MaxMessageBytes:      e.positiveInt("SMTP_MAX_MESSAGE_BYTES", 10<<20),
		MaxRecipients:        int(e.positiveInt("SMTP_MAX_RECIPIENTS", 50)),
		MaxConnections:       int(e.positiveInt("SMTP_MAX_CONNECTIONS", 100)),
		AuthMaxFailures:      int(e.nonNegativeInt("SMTP_AUTH_MAX_FAILURES", 10)),
		ReadTimeout:          e.duration("SMTP_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:         e.duration("SMTP_WRITE_TIMEOUT", 30*time.Second),
		SMTPUsername:         e.str("SMTP_USERNAME", ""),
		SMTPPassword:         e.secret("SMTP_PASSWORD"),
		AllowUnauthenticated: e.boolean("SMTP_ALLOW_UNAUTHENTICATED", false),
		TLSCertFile:          e.str("SMTP_TLS_CERT_FILE", ""),
		TLSKeyFile:           e.str("SMTP_TLS_KEY_FILE", ""),
		MailgunAPIKey:        e.secret("MAILGUN_API_KEY"),
		MailgunDomain:        e.str("MAILGUN_DOMAIN", ""),
		MailgunAPIBase:       e.str("MAILGUN_API_BASE", "us"),
		MailgunTimeout:       e.duration("MAILGUN_TIMEOUT", 30*time.Second),
		MailgunTestMode:      e.boolean("MAILGUN_TESTMODE", false),
		MailgunSendMode:      strings.ToLower(e.str("MAILGUN_SEND_MODE", SendModeMIME)),
		LogLevel:             strings.ToLower(e.str("LOG_LEVEL", "info")),
		LogFile:              e.str("LOG_FILE", ""),
		HealthListen:         e.str("HEALTH_LISTEN", ""),
	}

	cfg.AllowedSenderDomains = e.senderDomains("SMTP_ALLOWED_SENDER_DOMAINS", cfg.MailgunDomain)

	loopback, err := isLoopback(cfg.Listen)
	if err != nil {
		e.fail(fmt.Errorf("SMTP_LISTEN: %w", err))
	}
	// Plain-text AUTH is only safe by default when the socket never leaves the host.
	cfg.AllowInsecureAuth = e.boolean("SMTP_ALLOW_INSECURE_AUTH", loopback)

	if cfg.HealthListen != "" {
		if _, _, err := net.SplitHostPort(cfg.HealthListen); err != nil {
			e.fail(fmt.Errorf("HEALTH_LISTEN: %w", err))
		}
	}

	if cfg.MailgunAPIKey == "" {
		e.fail(errors.New("MAILGUN_API_KEY is required (or MAILGUN_API_KEY_FILE)"))
	}
	if cfg.MailgunDomain == "" {
		e.fail(errors.New("MAILGUN_DOMAIN is required"))
	}
	switch cfg.MailgunSendMode {
	case SendModeParsed, SendModeMIME:
	default:
		e.fail(fmt.Errorf("MAILGUN_SEND_MODE must be %q or %q", SendModeParsed, SendModeMIME))
	}
	if err := validateAPIBase(cfg.MailgunAPIBase); err != nil {
		e.fail(fmt.Errorf("MAILGUN_API_BASE: %w", err))
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		e.fail(fmt.Errorf("LOG_LEVEL must be debug, info, warn or error"))
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		e.fail(errors.New("SMTP_TLS_CERT_FILE and SMTP_TLS_KEY_FILE must be defined together"))
	}
	if cfg.AuthRequired() && (cfg.SMTPUsername == "" || cfg.SMTPPassword == "") {
		e.fail(errors.New("SMTP_USERNAME and SMTP_PASSWORD must be defined together"))
	}
	if cfg.AuthRequired() && cfg.TLSCertFile == "" && !cfg.AllowInsecureAuth {
		e.fail(errors.New("SMTP authentication needs STARTTLS (SMTP_TLS_CERT_FILE/SMTP_TLS_KEY_FILE) " +
			"or SMTP_ALLOW_INSECURE_AUTH=true when SMTP_LISTEN is not a loopback address"))
	}
	if err == nil && !loopback && !cfg.AuthRequired() && !cfg.AllowUnauthenticated {
		e.fail(fmt.Errorf("refusing to run an open relay on %s: set SMTP_USERNAME/SMTP_PASSWORD "+
			"or SMTP_ALLOW_UNAUTHENTICATED=true", cfg.Listen))
	}
	return cfg, errors.Join(e.errs...)
}

func (c Config) AuthRequired() bool {
	return c.SMTPUsername != "" || c.SMTPPassword != ""
}

// SenderDomainAllowed reports whether domain (lower case) may appear in
// MAIL FROM and the From header.
func (c Config) SenderDomainAllowed(domain string) bool {
	if len(c.AllowedSenderDomains) == 0 {
		return true
	}
	for _, d := range c.AllowedSenderDomains {
		if d == domain || (strings.HasPrefix(d, "*.") && strings.HasSuffix(domain, d[1:])) {
			return true
		}
	}
	return false
}

// defaultSenderDomains allows the organizational domain of the Mailgun
// sending domain and its subdomains: for mg.example.com, example.com and
// *.example.com. Those are the senders whose mail Mailgun's DKIM signature
// aligns with under DMARC's relaxed rules.
func defaultSenderDomains(mailgunDomain string) []string {
	mailgunDomain = strings.ToLower(mailgunDomain)
	org, err := publicsuffix.EffectiveTLDPlusOne(mailgunDomain)
	if err != nil {
		return []string{mailgunDomain}
	}
	return []string{org, "*." + org}
}

// isLoopback reports whether addr (host:port) only accepts local connections.
func isLoopback(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}

func validateAPIBase(base string) error {
	switch strings.ToLower(base) {
	case "us", "eu":
		return nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("must be us, eu or an http(s) URL, got %q", base)
	}
	return nil
}

// envReader reads typed variables and collects every parse error, so a bad
// value is reported instead of silently falling back to the default.
type envReader struct {
	errs []error
}

func (e *envReader) fail(err error) {
	e.errs = append(e.errs, err)
}

func (e *envReader) str(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	// systemd and godotenv strip quotes, but Docker and Podman --env-file keep
	// them as part of the value: a quoted API key would fail every send.
	// The value itself is not printed, since it may be a secret.
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		e.fail(fmt.Errorf("%s: value is wrapped in quotes; remove them", key))
	}
	return v
}

// secret reads key from the environment or, when key_FILE is set instead,
// from that file (container secrets, systemd credentials). Only the trailing
// line break of the file is removed.
func (e *envReader) secret(key string) string {
	v := e.str(key, "")
	path := e.str(key+"_FILE", "")
	if path == "" {
		return v
	}
	if v != "" {
		e.fail(fmt.Errorf("%s and %s_FILE are both set; use only one", key, key))
		return v
	}
	data, err := os.ReadFile(path)
	if err != nil {
		e.fail(fmt.Errorf("%s_FILE: %w", key, err))
		return ""
	}
	v = strings.TrimRight(string(data), "\r\n")
	if v == "" {
		e.fail(fmt.Errorf("%s_FILE: %s is empty", key, path))
	}
	return v
}

func (e *envReader) list(key string) []string {
	var out []string
	for item := range strings.SplitSeq(e.str(key, ""), ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// senderDomains reads the allowed sender domains. Empty means the default
// derived from the Mailgun domain, and "*" alone allows any sender.
func (e *envReader) senderDomains(key, mailgunDomain string) []string {
	list := e.list(key)
	if len(list) == 0 {
		if mailgunDomain == "" {
			return nil
		}
		return defaultSenderDomains(mailgunDomain)
	}
	for _, d := range list {
		if d == "*" {
			if len(list) > 1 {
				e.fail(fmt.Errorf("%s: \"*\" allows any sender and cannot be combined with domains", key))
			}
			return nil
		}
		if strings.Contains(strings.TrimPrefix(d, "*."), "*") {
			e.fail(fmt.Errorf("%s: %q is not a domain or a *.domain pattern", key, d))
		}
	}
	return list
}

func (e *envReader) boolean(key string, fallback bool) bool {
	v := e.str(key, "")
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(fmt.Errorf("%s: invalid boolean %q", key, v))
		return fallback
	}
	return b
}

func (e *envReader) positiveInt(key string, fallback int64) int64 {
	v := e.str(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n <= 0 {
		e.fail(fmt.Errorf("%s: expected a positive integer, got %q", key, v))
		return fallback
	}
	return n
}

func (e *envReader) nonNegativeInt(key string, fallback int64) int64 {
	v := e.str(key, "")
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 {
		e.fail(fmt.Errorf("%s: expected 0 or a positive integer, got %q", key, v))
		return fallback
	}
	return n
}

func (e *envReader) duration(key string, fallback time.Duration) time.Duration {
	v := e.str(key, "")
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.fail(fmt.Errorf("%s: expected a positive duration such as 30s, got %q", key, v))
		return fallback
	}
	return d
}
