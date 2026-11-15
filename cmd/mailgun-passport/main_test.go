package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/config"
)

// fakeMailgun answers like the parts of the Mailgun API the relay uses: the
// empty test-mode call that checks the key (400 once the key is accepted, 401
// otherwise) and POST messages.mime.
type fakeMailgun struct {
	*httptest.Server

	mu   sync.Mutex
	key  string // the accepted API key
	down bool   // answer 503 to messages.mime
	sent []sentMessage
}

type sentMessage struct {
	key  string
	to   []string
	tags []string
	mime string
}

func newFakeMailgun(t *testing.T, key string) *fakeMailgun {
	t.Helper()
	f := &fakeMailgun{key: key}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case pass != f.key:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Forbidden"}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"to parameter is missing"}`)
		case f.down:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"message":"try later"}`)
		default:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse form: %v", err)
			}
			msg := sentMessage{key: pass, to: r.MultipartForm.Value["to"], tags: r.MultipartForm.Value["o:tag"]}
			if hdrs := r.MultipartForm.File["message"]; len(hdrs) == 1 {
				file, _ := hdrs[0].Open()
				data, _ := io.ReadAll(file)
				_ = file.Close()
				msg.mime = string(data)
			}
			f.sent = append(f.sent, msg)
			_, _ = io.WriteString(w, `{"id":"<1@mg.example.com>","message":"Queued. Thank you."}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMailgun) set(key string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key, f.down = key, down
}

func (f *fakeMailgun) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage{}, f.sent...)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// env points the relay at mg with the smallest valid configuration, then
// applies extra. Everything else the relay reads is cleared, so the
// developer's own environment cannot leak into a test.
func env(t *testing.T, mg *fakeMailgun, extra map[string]string) (smtpAddr, healthAddr string) {
	t.Helper()
	smtpAddr, healthAddr = freeAddr(t), freeAddr(t)
	for _, name := range []string{
		"SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_PASSWORD_FILE", "SMTP_TLS_CERT_FILE", "SMTP_TLS_KEY_FILE",
		"SMTP_ALLOWED_SENDER_DOMAINS", "SMTP_RATE_LIMIT_PER_MINUTE", "MAILGUN_API_KEY_FILE", "MAILGUN_SEND_MODE",
		"MAILGUN_TAGS", "MAILGUN_TRACKING_CLICKS", "MAILGUN_TRACKING_OPENS", "MAILGUN_REQUIRE_TLS",
		"MAILGUN_TESTMODE", "QUEUE_DIR", "QUEUE_MAX_BYTES", "QUEUE_MAX_AGE", "QUEUE_WORKERS", "LOG_FILE",
		"LOG_LEVEL", "PASSPORT_ENV_FILE", "SMTP_MAX_CONNECTIONS",
	} {
		// Set and unset again: Setenv restores the original value afterwards.
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	vars := map[string]string{
		"MAILGUN_API_KEY": "key-test",
		"MAILGUN_DOMAIN":  "mg.example.com",
		"SMTP_LISTEN":     smtpAddr,
		"HEALTH_LISTEN":   healthAddr,
		"METRICS_ENABLED": "true",
	}
	if mg != nil {
		vars["MAILGUN_API_BASE"] = mg.URL
	}
	for k, v := range extra {
		vars[k] = v
	}
	for k, v := range vars {
		if v == "" {
			t.Setenv(k, "")
			_ = os.Unsetenv(k)
			continue
		}
		t.Setenv(k, v)
	}
	return smtpAddr, healthAddr
}

type relay struct {
	smtp, health string
	hup          chan os.Signal
	stop         context.CancelFunc
	done         chan int
}

// startRelay runs serve and waits until it accepts SMTP connections.
func startRelay(t *testing.T, smtpAddr, healthAddr string) *relay {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &relay{smtp: smtpAddr, health: healthAddr, hup: make(chan os.Signal, 1), stop: cancel, done: make(chan int, 1)}
	go func() { r.done <- serve(ctx, r.hup) }()
	t.Cleanup(cancel)
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case code := <-r.done:
			t.Fatalf("the relay exited with status %d while starting", code)
		default:
		}
		if c, err := net.Dial("tcp", smtpAddr); err == nil {
			_ = c.Close()
			return r
		}
		if time.Now().After(deadline) {
			t.Fatal("the relay never started listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// launch is env followed by startRelay.
func launch(t *testing.T, mg *fakeMailgun, extra map[string]string) *relay {
	t.Helper()
	smtpAddr, healthAddr := env(t, mg, extra)
	return startRelay(t, smtpAddr, healthAddr)
}

// shutdown stops the relay as SIGTERM would and returns its exit code.
func (r *relay) shutdown(t *testing.T) int {
	t.Helper()
	r.stop()
	select {
	case code := <-r.done:
		return code
	case <-time.After(15 * time.Second):
		t.Fatal("the relay did not stop")
		return -1
	}
}

const mail = "From: Alice <alice@example.com>\r\nTo: Bob <bob@example.com>\r\nSubject: Hello\r\n\r\nBody\r\n"

func (r *relay) send(t *testing.T) error {
	t.Helper()
	return smtp.SendMail(r.smtp, nil, "alice@example.com", []string{"bob@example.com"}, []byte(mail))
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// metric returns the value of the first series that starts with name, or "".
func (r *relay) metric(t *testing.T, name string) string {
	t.Helper()
	status, body := get(t, "http://"+r.health+"/metrics")
	if status != http.StatusOK {
		t.Fatalf("/metrics: %d", status)
	}
	for line := range strings.SplitSeq(body, "\n") {
		if rest, ok := strings.CutPrefix(line, name+" "); ok {
			return rest
		}
	}
	return ""
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServeDeliversMailAndStopsOnSignal(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	r := launch(t, mg, nil)

	if err := r.send(t); err != nil {
		t.Fatal(err)
	}
	got := mg.messages()
	if len(got) != 1 || strings.Join(got[0].to, ",") != "bob@example.com" || !strings.Contains(got[0].mime, "Subject: Hello") {
		t.Fatalf("Mailgun received %+v", got)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if _, err := net.DialTimeout("tcp", r.smtp, time.Second); err == nil {
		t.Fatal("still listening after shutdown")
	}
}

func TestInvalidConfigurationExitsWith2(t *testing.T) {
	env(t, nil, map[string]string{"MAILGUN_API_KEY": "", "MAILGUN_DOMAIN": ""})
	if code := serve(context.Background(), nil); code != exitConfig {
		t.Fatalf("exit code %d, want %d", code, exitConfig)
	}
}

func TestRejectedKeyExitsWith2(t *testing.T) {
	mg := newFakeMailgun(t, "another-key")
	env(t, mg, nil)
	if code := serve(context.Background(), nil); code != exitConfig {
		t.Fatalf("exit code %d, want %d", code, exitConfig)
	}
}

func TestUnreachableMailgunDoesNotStopStartup(t *testing.T) {
	smtpAddr, healthAddr := env(t, nil, map[string]string{"MAILGUN_API_BASE": "http://" + freeAddr(t)})
	r := startRelay(t, smtpAddr, healthAddr)
	// Not ready: the relay answers 451 to everything until Mailgun is back.
	eventually(t, "/ready to report the failure", func() bool {
		status, _ := get(t, "http://"+healthAddr+"/ready")
		return status == http.StatusServiceUnavailable
	})
	if status, _ := get(t, "http://"+healthAddr+"/health"); status != http.StatusOK {
		t.Fatalf("/health: %d", status)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestUnusableLogFileExitsWith2(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	env(t, mg, map[string]string{"LOG_FILE": filepath.Join(t.TempDir(), "missing", "relay.log")})
	if code := serve(context.Background(), nil); code != exitConfig {
		t.Fatalf("exit code %d, want %d", code, exitConfig)
	}
}

func TestUnusableQueueDirExitsWith2(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env(t, mg, map[string]string{"QUEUE_DIR": file})
	if code := serve(context.Background(), nil); code != exitConfig {
		t.Fatalf("exit code %d, want %d", code, exitConfig)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	r := launch(t, mg, nil)
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}
	if got := r.metric(t, `mailgun_passport_messages_total{result="accepted"}`); got != "1" {
		t.Fatalf("accepted = %q", got)
	}
	if got := r.metric(t, `mailgun_passport_mailgun_requests_total{code="200"}`); got != "1" {
		t.Fatalf("200s = %q", got)
	}
	// The startup check is the one 400.
	if got := r.metric(t, `mailgun_passport_mailgun_requests_total{code="400"}`); got != "1" {
		t.Fatalf("400s = %q", got)
	}
	if got := r.metric(t, "mailgun_passport_smtp_connections_total{result=\"accepted\"}"); got == "" || got == "0" {
		t.Fatalf("connections = %q", got)
	}
	if got := r.metric(t, "mailgun_passport_build_info{version=\"dev\",commit=\"unknown\"}"); got != "1" {
		t.Fatalf("build info = %q", got)
	}
}

func TestMetricsAreOffByDefault(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	smtpAddr, healthAddr := env(t, mg, map[string]string{"METRICS_ENABLED": ""})
	startRelay(t, smtpAddr, healthAddr)
	if status, _ := get(t, "http://"+healthAddr+"/metrics"); status != http.StatusNotFound {
		t.Fatalf("/metrics answered %d without METRICS_ENABLED", status)
	}
	if status, _ := get(t, "http://"+healthAddr+"/health"); status != http.StatusOK {
		t.Fatalf("/health: %d", status)
	}
}

func TestMailgunOptionsReachTheAPI(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	r := launch(t, mg, map[string]string{"MAILGUN_TAGS": "relay,passport"})
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}
	if got := mg.messages(); len(got) != 1 || strings.Join(got[0].tags, ",") != "relay,passport" {
		t.Fatalf("Mailgun received %+v", got)
	}
}

func TestSIGHUPRotatesTheAPIKey(t *testing.T) {
	mg := newFakeMailgun(t, "key-one")
	keyFile := filepath.Join(t.TempDir(), "api-key")
	writeKey := func(key string) {
		t.Helper()
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeKey("key-one")
	r := launch(t, mg, map[string]string{"MAILGUN_API_KEY": "", "MAILGUN_API_KEY_FILE": keyFile})
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}

	// Rotate: Mailgun accepts the new key, the file holds it, and SIGHUP applies it.
	mg.set("key-two", false)
	writeKey("key-two")
	r.hup <- os.Interrupt
	eventually(t, "the reload", func() bool { return r.metric(t, `mailgun_passport_config_reloads_total{result="success"}`) == "1" })
	if err := r.send(t); err != nil {
		t.Fatalf("send after rotation: %v", err)
	}
	got := mg.messages()
	if len(got) != 2 || got[0].key != "key-one" || got[1].key != "key-two" {
		t.Fatalf("keys used: %+v", got)
	}

	// A key that Mailgun rejects is refused, and the working one stays.
	writeKey("key-three")
	r.hup <- os.Interrupt
	eventually(t, "the refused reload", func() bool { return r.metric(t, `mailgun_passport_config_reloads_total{result="failure"}`) == "1" })
	if err := r.send(t); err != nil {
		t.Fatalf("send after a refused reload: %v", err)
	}
	if got := mg.messages(); got[len(got)-1].key != "key-two" {
		t.Fatalf("the rejected key was used: %+v", got)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestSIGHUPReloadsEnvFileAndKeepsRunningOnErrors(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	envFile := filepath.Join(t.TempDir(), "relay.env")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("MAILGUN_TAGS=first\n")
	r := launch(t, mg, map[string]string{"PASSPORT_ENV_FILE": envFile})
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}

	write("MAILGUN_TAGS=second\n")
	r.hup <- os.Interrupt
	eventually(t, "the reload", func() bool { return r.metric(t, `mailgun_passport_config_reloads_total{result="success"}`) == "1" })
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}

	// An invalid file must not take the relay down or change anything.
	write("MAILGUN_TAGS=a,b,c,d\n")
	r.hup <- os.Interrupt
	eventually(t, "the refused reload", func() bool { return r.metric(t, `mailgun_passport_config_reloads_total{result="failure"}`) == "1" })
	if err := r.send(t); err != nil {
		t.Fatalf("send after an invalid reload: %v", err)
	}
	var tags []string
	for _, m := range mg.messages() {
		tags = append(tags, strings.Join(m.tags, ","))
	}
	if strings.Join(tags, "|") != "first|second|second" {
		t.Fatalf("tags used: %q", tags)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestSIGHUPReopensTheLogFile(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	dir := t.TempDir()
	logFile := filepath.Join(dir, "relay.log")
	r := launch(t, mg, map[string]string{"LOG_FILE": logFile})

	if data, _ := os.ReadFile(logFile); !strings.Contains(string(data), "starting mailgun-passport") {
		t.Fatalf("nothing logged to the file:\n%s", data)
	}
	// logrotate renames the file; after SIGHUP the relay writes to a new one.
	rotated := logFile + ".1"
	if err := os.Rename(logFile, rotated); err != nil {
		t.Fatal(err)
	}
	r.hup <- os.Interrupt
	eventually(t, "a new log file", func() bool {
		data, err := os.ReadFile(logFile)
		return err == nil && strings.Contains(string(data), "configuration unchanged")
	})
	if data, _ := os.ReadFile(rotated); strings.Contains(string(data), "configuration unchanged") {
		t.Fatal("the rotated file kept receiving logs")
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

func TestQueueAcceptsDuringAnOutageAndDeliversAfterARestart(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	spool := filepath.Join(t.TempDir(), "spool")
	smtpAddr, healthAddr := env(t, mg, map[string]string{"QUEUE_DIR": spool})
	mg.set("key-test", true)
	r := startRelay(t, smtpAddr, healthAddr)

	// Mailgun is down, and the client still gets its 250: the message is on disk.
	if err := r.send(t); err != nil {
		t.Fatalf("the queue should accept during an outage: %v", err)
	}
	eventually(t, "the first attempt", func() bool {
		return r.metric(t, `mailgun_passport_queue_deliveries_total{result="retry"}`) != "0"
	})
	// The outage does not make the relay "not ready": clients are not seeing errors.
	if status, body := get(t, "http://"+healthAddr+"/ready"); status != http.StatusOK {
		t.Fatalf("/ready: %d %s", status, body)
	}
	if got := r.metric(t, "mailgun_passport_queue_messages"); got != "1" {
		t.Fatalf("queued messages = %q", got)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if got := queued(t, spool); got != 1 {
		t.Fatalf("%d messages on disk after the shutdown, want 1", got)
	}

	// Mailgun is back and the relay starts again: the message goes out at once.
	mg.set("key-test", false)
	smtpAddr, healthAddr = env(t, mg, map[string]string{"QUEUE_DIR": spool})
	r = startRelay(t, smtpAddr, healthAddr)
	eventually(t, "delivery after the restart", func() bool { return len(mg.messages()) == 1 })
	eventually(t, "the spool to empty", func() bool { return queued(t, spool) == 0 })
	if got := mg.messages()[0]; !strings.Contains(got.mime, "Subject: Hello") || strings.Join(got.to, ",") != "bob@example.com" {
		t.Fatalf("delivered %+v", got)
	}
	if code := r.shutdown(t); code != 0 {
		t.Fatalf("exit code %d", code)
	}
}

// queued counts the messages waiting in a spool directory.
func queued(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.msg"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

func TestQueueInParsedMode(t *testing.T) {
	mg := newFakeMailgun(t, "key-test")
	spool := t.TempDir()
	// Parsed mode posts to /messages, which this fake answers with 400: the
	// message is refused for good and must end up in failed/.
	r := launch(t, mg, map[string]string{"QUEUE_DIR": spool, "MAILGUN_SEND_MODE": "parsed"})
	if err := r.send(t); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the permanent failure", func() bool {
		return r.metric(t, `mailgun_passport_queue_deliveries_total{result="failed"}`) == "1"
	})
	failed, _ := filepath.Glob(filepath.Join(spool, "failed", "*.msg"))
	if len(failed) != 1 || queued(t, spool) != 0 {
		t.Fatalf("failed/: %v, still queued: %d", failed, queued(t, spool))
	}
}

func TestRunHealthcheck(t *testing.T) {
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("queried %s", r.URL.Path)
		}
		if !healthy {
			http.Error(w, "SMTP listener is not running", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)

	env(t, nil, map[string]string{"HEALTH_LISTEN": strings.TrimPrefix(srv.URL, "http://")})
	if code := runHealthcheck(); code != 0 {
		t.Fatalf("healthy: %d", code)
	}
	healthy = false
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("unhealthy: %d", code)
	}

	env(t, nil, map[string]string{"HEALTH_LISTEN": ""})
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("without HEALTH_LISTEN: %d", code)
	}
	env(t, nil, map[string]string{"MAILGUN_API_KEY": ""})
	if code := runHealthcheck(); code != 1 {
		t.Fatalf("with an invalid configuration: %d", code)
	}
}

func TestLogger(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "relay.log")
	logs, err := newLogger("warn", logFile)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()

	logs.Info("not shown")
	logs.Warn("shown at warn")
	logs.SetLevel("debug")
	logs.Debug("shown at debug")
	read := func(name string) string {
		data, _ := os.ReadFile(name)
		return string(data)
	}
	got := read(logFile)
	if strings.Contains(got, "not shown") || !strings.Contains(got, "shown at warn") || !strings.Contains(got, "shown at debug") {
		t.Fatalf("log file:\n%s", got)
	}

	if err := os.Rename(logFile, logFile+".1"); err != nil {
		t.Fatal(err)
	}
	if err := logs.Reopen(); err != nil {
		t.Fatal(err)
	}
	logs.Warn("after rotation")
	if !strings.Contains(read(logFile), "after rotation") || strings.Contains(read(logFile+".1"), "after rotation") {
		t.Fatal("Reopen did not switch to a new file")
	}

	// A failed reopen keeps the file in use.
	if err := os.Mkdir(filepath.Join(dir, "blocked"), 0o700); err != nil {
		t.Fatal(err)
	}
	logs.out.path = filepath.Join(dir, "blocked") // a directory cannot be opened for writing
	if err := logs.Reopen(); err == nil {
		t.Fatal("expected an error")
	}
	logs.Warn("still logging")
	if !strings.Contains(read(logFile), "still logging") {
		t.Fatal("the previous file was dropped after a failed reopen")
	}
}

func TestLoggerWithoutFile(t *testing.T) {
	for _, file := range []string{"", "-", "  "} {
		logs, err := newLogger("info", file)
		if err != nil {
			t.Fatalf("%q: %v", file, err)
		}
		if err := logs.Reopen(); err != nil {
			t.Fatalf("%q: Reopen: %v", file, err)
		}
		logs.Close()
	}
	if _, err := newLogger("info", filepath.Join(t.TempDir(), "missing", "x.log")); err == nil {
		t.Fatal("an unusable path was accepted")
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]string{"debug": "DEBUG", "info": "INFO", "warn": "WARN", "error": "ERROR", "": "INFO", "bogus": "INFO"} {
		if got := parseLevel(in).String(); got != want {
			t.Errorf("parseLevel(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestReparserFollowsSendMode(t *testing.T) {
	raw := []byte(mail)
	for mode, decodesBody := range map[string]bool{"mime": false, "parsed": true} {
		cfg := mustConfig(mode)
		msg, err := reparser(cfg)("alice@example.com", []string{"bob@example.com"}, raw)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if (msg.Text != "") != decodesBody {
			t.Errorf("%s: text %q", mode, msg.Text)
		}
		if string(msg.Raw) != mail {
			t.Errorf("%s: Raw changed", mode)
		}
	}
	if _, err := reparser(mustConfig("mime"))("alice@example.com", nil, raw); err == nil {
		t.Error("a message without recipients was rebuilt")
	}
}

func mustConfig(mode string) config.Config {
	return config.Config{MailgunSendMode: mode}
}
