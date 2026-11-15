package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
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
//
// A field tagged reload:"hot" takes effect on SIGHUP (see Reload); the others
// need a restart. The env tag names the variable, for messages.
type Config struct {
	Listen             string        `env:"SMTP_LISTEN"`
	SMTPDomain         string        `env:"SMTP_DOMAIN"`
	MaxMessageBytes    int64         `env:"SMTP_MAX_MESSAGE_BYTES"`
	MaxRecipients      int           `env:"SMTP_MAX_RECIPIENTS"`
	MaxConnections     int           `env:"SMTP_MAX_CONNECTIONS"`
	AuthMaxFailures    int           `env:"SMTP_AUTH_MAX_FAILURES"` // failed AUTH attempts per client address before blocking; 0 disables
	RateLimitPerMinute int           `env:"SMTP_RATE_LIMIT_PER_MINUTE"`
	ReadTimeout        time.Duration `env:"SMTP_READ_TIMEOUT"`
	WriteTimeout       time.Duration `env:"SMTP_WRITE_TIMEOUT"`
	// SMTPUsername and SMTPPassword change on SIGHUP only while AUTH stays
	// enabled or disabled; see Reload.
	SMTPUsername         string `env:"SMTP_USERNAME" reload:"hot"`
	SMTPPassword         string `env:"SMTP_PASSWORD" reload:"hot"`
	AllowInsecureAuth    bool   `env:"SMTP_ALLOW_INSECURE_AUTH"`
	AllowUnauthenticated bool   `env:"SMTP_ALLOW_UNAUTHENTICATED"`
	// AllowedSenderDomains holds exact domains and "*.domain" patterns for
	// subdomains. Empty allows any sender.
	AllowedSenderDomains  []string      `env:"SMTP_ALLOWED_SENDER_DOMAINS" reload:"hot"`
	TLSCertFile           string        `env:"SMTP_TLS_CERT_FILE"`
	TLSKeyFile            string        `env:"SMTP_TLS_KEY_FILE"`
	MailgunAPIKey         string        `env:"MAILGUN_API_KEY" reload:"hot"`
	MailgunDomain         string        `env:"MAILGUN_DOMAIN" reload:"hot"`
	MailgunAPIBase        string        `env:"MAILGUN_API_BASE" reload:"hot"`
	MailgunTimeout        time.Duration `env:"MAILGUN_TIMEOUT" reload:"hot"`
	MailgunTestMode       bool          `env:"MAILGUN_TESTMODE" reload:"hot"`
	MailgunSendMode       string        `env:"MAILGUN_SEND_MODE"`
	MailgunTags           []string      `env:"MAILGUN_TAGS" reload:"hot"`
	MailgunTrackingClicks string        `env:"MAILGUN_TRACKING_CLICKS" reload:"hot"` // yes, no, htmlonly or empty for the domain's setting
	MailgunTrackingOpens  string        `env:"MAILGUN_TRACKING_OPENS" reload:"hot"`  // yes, no or empty for the domain's setting
	MailgunRequireTLS     bool          `env:"MAILGUN_REQUIRE_TLS" reload:"hot"`
	// QueueDir enables the on-disk queue: messages are stored there and
	// delivered in the background, with retries.
	QueueDir      string        `env:"QUEUE_DIR"`
	QueueMaxBytes int64         `env:"QUEUE_MAX_BYTES"`
	QueueMaxAge   time.Duration `env:"QUEUE_MAX_AGE"`
	QueueWorkers  int           `env:"QUEUE_WORKERS"`
	LogLevel      string        `env:"LOG_LEVEL" reload:"hot"`
	LogFile       string        `env:"LOG_FILE"`
	// HealthListen is the address of the HTTP health endpoints; empty disables them.
	HealthListen string `env:"HEALTH_LISTEN"`
	// MetricsEnabled adds /metrics to the health endpoints' listener.
	MetricsEnabled bool `env:"METRICS_ENABLED"`
}

func Load() (Config, error) {
	if err := loadEnvFile(); err != nil {
		return Config{}, err
	}
	return fromEnv()
}

// Reload reads the configuration again, as Load does, for a SIGHUP. Variables
// that came from PASSPORT_ENV_FILE are dropped first, so a changed or removed
// line in the file takes effect; variables from the process environment stay.
// Under systemd, EnvironmentFile= is read only when the service starts, so
// edits to that file need a restart.
func Reload() (Config, error) {
	unloadEnvFile()
	return Load()
}

// envFileKeys lists the variables loadEnvFile set, so Reload can unset them.
var (
	envFileMu   sync.Mutex
	envFileKeys []string
)

// loadEnvFile fills missing variables from the file named by PASSPORT_ENV_FILE,
// which must exist. Variables that are already set win. No file is read
// implicitly: under systemd the working directory is /, and a stray .env there
// or next to the binary would change the configuration without anyone noticing.
func loadEnvFile() error {
	p := strings.TrimSpace(os.Getenv("PASSPORT_ENV_FILE"))
	if p == "" {
		return nil
	}
	vars, err := godotenv.Read(p)
	if err != nil {
		return fmt.Errorf("PASSPORT_ENV_FILE: %w", err)
	}
	envFileMu.Lock()
	defer envFileMu.Unlock()
	for k, v := range vars {
		if _, set := os.LookupEnv(k); set {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("PASSPORT_ENV_FILE: %w", err)
		}
		envFileKeys = append(envFileKeys, k)
	}
	return nil
}

func unloadEnvFile() {
	envFileMu.Lock()
	defer envFileMu.Unlock()
	for _, k := range envFileKeys {
		_ = os.Unsetenv(k)
	}
	envFileKeys = nil
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
		RateLimitPerMinute:   int(e.nonNegativeInt("SMTP_RATE_LIMIT_PER_MINUTE", 0)),
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
		MailgunTags:          e.tags("MAILGUN_TAGS"),
		MailgunRequireTLS:    e.boolean("MAILGUN_REQUIRE_TLS", false),
		QueueDir:             e.str("QUEUE_DIR", ""),
		QueueMaxBytes:        e.positiveInt("QUEUE_MAX_BYTES", 256<<20),
		QueueMaxAge:          e.duration("QUEUE_MAX_AGE", 24*time.Hour),
		QueueWorkers:         int(e.positiveInt("QUEUE_WORKERS", 2)),
		LogLevel:             strings.ToLower(e.str("LOG_LEVEL", "info")),
		LogFile:              e.str("LOG_FILE", ""),
		HealthListen:         e.str("HEALTH_LISTEN", ""),
		MetricsEnabled:       e.boolean("METRICS_ENABLED", false),
	}
	cfg.MailgunTrackingClicks = e.choice("MAILGUN_TRACKING_CLICKS", "yes", "no", "htmlonly")
	cfg.MailgunTrackingOpens = e.choice("MAILGUN_TRACKING_OPENS", "yes", "no")

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

	if cfg.MetricsEnabled && cfg.HealthListen == "" {
		e.fail(errors.New("METRICS_ENABLED needs HEALTH_LISTEN: /metrics is served there"))
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

// tags reads a comma-separated list of Mailgun tags. Mailgun accepts at most
// three per message, and each tag at most 128 ASCII characters.
func (e *envReader) tags(key string) []string {
	var out []string
	for item := range strings.SplitSeq(e.str(key, ""), ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	if len(out) > 3 {
		e.fail(fmt.Errorf("%s: Mailgun accepts at most 3 tags per message, got %d", key, len(out)))
	}
	for _, t := range out {
		if len(t) > 128 {
			e.fail(fmt.Errorf("%s: a tag is longer than 128 characters", key))
		}
	}
	return out
}

// choice reads a lower-cased value that must be one of allowed, or empty.
func (e *envReader) choice(key string, allowed ...string) string {
	v := strings.ToLower(e.str(key, ""))
	if v != "" && !slices.Contains(allowed, v) {
		e.fail(fmt.Errorf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v))
	}
	return v
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

// Reload merges next, a freshly loaded configuration, into c for a SIGHUP.
// Only fields tagged reload:"hot" are taken from next; changed returns their
// variable names. restart returns the variables that differ in next but need
// a restart, which merged keeps at their current values.
//
// Enabling or disabling AUTH counts as needing a restart: a server started
// without AUTH was checked against SMTP_ALLOW_UNAUTHENTICATED and, on a
// network, would otherwise end up as an open relay or with no way to log in.
func (c Config) Reload(next Config) (merged Config, changed, restart []string) {
	merged = c
	authChanges := c.AuthRequired() != next.AuthRequired()
	cv, nv, mv := reflect.ValueOf(c), reflect.ValueOf(next), reflect.ValueOf(&merged).Elem()
	t := cv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if reflect.DeepEqual(cv.Field(i).Interface(), nv.Field(i).Interface()) {
			continue
		}
		auth := f.Name == "SMTPUsername" || f.Name == "SMTPPassword"
		if f.Tag.Get("reload") == "hot" && (!auth || !authChanges) {
			mv.Field(i).Set(nv.Field(i))
			changed = append(changed, f.Tag.Get("env"))
		} else {
			restart = append(restart, f.Tag.Get("env"))
		}
	}
	return merged, changed, restart
}
