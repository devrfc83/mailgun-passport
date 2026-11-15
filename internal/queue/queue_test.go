package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/metrics"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

const rawMail = "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Hi\r\n\r\nBody\r\n"

type sendErr struct{ temporary bool }

func (e sendErr) Error() string   { return "mailgun said no" }
func (e sendErr) Temporary() bool { return e.temporary }

// fakeSender records deliveries and fails as scripted.
type fakeSender struct {
	mu        sync.Mutex
	errs      []error // returned one per call, then nil
	calls     int
	delivered []*parser.Message
	started   chan struct{} // receives a value when a call starts, if not nil
	release   chan struct{} // a call blocks until this is closed, if not nil
}

func (f *fakeSender) Send(_ context.Context, msg *parser.Message) error {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return err
		}
	}
	f.delivered = append(f.delivered, msg)
	return nil
}

func (f *fakeSender) count() (calls, delivered int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, len(f.delivered)
}

func parseRaw(mailFrom string, rcptTo []string, raw []byte) (*parser.Message, error) {
	return parser.ParseHeaders(mailFrom, rcptTo, bytes.NewReader(raw), int64(len(raw)))
}

func newQueue(t *testing.T, dir string, sender Sender, mutate ...func(*Options)) (*Queue, *metrics.Set) {
	t.Helper()
	m := metrics.New("test", "none")
	opts := Options{Dir: dir, MaxBytes: 1 << 20, MaxAge: time.Hour, Workers: 2, Metrics: m}
	for _, f := range mutate {
		f(&opts)
	}
	q, err := Open(opts, sender, parseRaw, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	q.backoffBase, q.backoffMax = time.Millisecond, 5*time.Millisecond
	return q, m
}

func start(t *testing.T, q *Queue) {
	t.Helper()
	q.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := q.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
}

func message(t *testing.T) *parser.Message {
	t.Helper()
	msg, err := parseRaw("alice@example.com", []string{"bob@example.com", "carol@example.com"}, []byte(rawMail))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestSendIsStoredThenDelivered(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{}
	q, m := newQueue(t, dir, sender)

	// Before Start: stored durably, nothing delivered.
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	names := files(t, dir)
	if len(names) != 1 || !strings.HasSuffix(names[0], ".msg") {
		t.Fatalf("files: %v", names)
	}
	info, err := os.Stat(filepath.Join(dir, names[0]))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v: messages hold private mail", info.Mode().Perm())
	}
	if n, size := q.Stats(); n != 1 || size != info.Size() {
		t.Fatalf("stats: %d messages, %d bytes (file %d)", n, size, info.Size())
	}

	start(t, q)
	eventually(t, "delivery", func() bool { _, d := sender.count(); return d == 1 })
	eventually(t, "removal", func() bool { n, _ := q.Stats(); return n == 0 })
	if names := files(t, dir); len(names) != 0 {
		t.Fatalf("delivered message left behind: %v", names)
	}
	got := sender.delivered[0]
	if got.MailFrom != "alice@example.com" || strings.Join(got.RcptTo, ",") != "bob@example.com,carol@example.com" {
		t.Fatalf("envelope after the round trip: %q %q", got.MailFrom, got.RcptTo)
	}
	if string(got.Raw) != rawMail || got.Subject != "Hi" {
		t.Fatalf("message after the round trip: %q / %q", got.Raw, got.Subject)
	}
	if m.QueueDeliveries.Value("delivered") != 1 {
		t.Fatalf("delivered metric: %d", m.QueueDeliveries.Value("delivered"))
	}
	if n, size := q.Stats(); n != 0 || size != 0 {
		t.Fatalf("stats after delivery: %d, %d", n, size)
	}
}

func TestTemporaryFailuresAreRetried(t *testing.T) {
	sender := &fakeSender{errs: []error{sendErr{true}, errors.New("unknown failure"), sendErr{true}}}
	q, m := newQueue(t, t.TempDir(), sender)
	start(t, q)
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "delivery after retries", func() bool { _, d := sender.count(); return d == 1 })
	if calls, _ := sender.count(); calls != 4 {
		t.Fatalf("calls: %d, want 4", calls)
	}
	if got := m.QueueDeliveries.Value("retry"); got != 3 {
		t.Fatalf("retries: %d", got)
	}
}

func TestPermanentFailureMovesToFailed(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{errs: []error{sendErr{false}}}
	q, m := newQueue(t, dir, sender)
	start(t, q)
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "failure", func() bool { return m.QueueDeliveries.Value("failed") == 1 })
	eventually(t, "removal", func() bool { n, _ := q.Stats(); return n == 0 })
	if names := files(t, dir); len(names) != 0 {
		t.Fatalf("still queued: %v", names)
	}
	failed := files(t, filepath.Join(dir, failedDir))
	if len(failed) != 1 {
		t.Fatalf("failed/: %v", failed)
	}
	if calls, _ := sender.count(); calls != 1 {
		t.Fatalf("a permanent failure was retried: %d calls", calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, failedDir, failed[0]))
	if err != nil || !strings.Contains(string(data), "Subject: Hi") {
		t.Fatalf("the failed message must stay readable: %v %q", err, data)
	}
}

func TestOldMessagesExpire(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{errs: slicesOf(sendErr{true}, 100)}
	q, m := newQueue(t, dir, sender, func(o *Options) { o.MaxAge = 30 * time.Millisecond })
	start(t, q)
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "expiry", func() bool { return m.QueueDeliveries.Value("expired") == 1 })
	if failed := files(t, filepath.Join(dir, failedDir)); len(failed) != 1 {
		t.Fatalf("failed/: %v", failed)
	}
	if m.QueueDeliveries.Value("retry") == 0 {
		t.Fatal("expected retries before giving up")
	}
}

func slicesOf(err error, n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}

func TestFullQueueRefusesTemporarily(t *testing.T) {
	msg := message(t)
	q, _ := newQueue(t, t.TempDir(), &fakeSender{}, func(o *Options) { o.MaxBytes = int64(len(msg.Raw)) + 200 })
	if err := q.Health(); err != nil {
		t.Fatalf("empty queue unhealthy: %v", err)
	}
	if err := q.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	err := q.Send(context.Background(), msg)
	qe, ok := errors.AsType[*Error](err)
	if !ok || !qe.Temporary() || qe.ClientMessage() != "local queue is full" {
		t.Fatalf("err = %v", err)
	}
	if n, _ := q.Stats(); n != 1 {
		t.Fatalf("a refused message was counted: %d", n)
	}
	// Room for a few more bytes is not "full": Health says so only at the limit.
	if err := q.Health(); err != nil {
		t.Fatalf("unhealthy with room left: %v", err)
	}
	q.mu.Lock()
	q.bytes = q.maxBytes
	q.mu.Unlock()
	if err := q.Health(); err == nil {
		t.Fatal("a full queue must report not ready")
	}
}

func TestMessagesSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	q1, _ := newQueue(t, dir, &fakeSender{})
	for range 3 {
		if err := q1.Send(context.Background(), message(t)); err != nil {
			t.Fatal(err)
		}
	}
	// Leftovers of a crash: a half-written file and a corrupt message.
	if err := os.WriteFile(filepath.Join(dir, "00000000000000ff-aa.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000fe-bb.msg"), []byte("not json\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}

	sender := &fakeSender{}
	q2, _ := newQueue(t, dir, sender)
	if n, _ := q2.Stats(); n != 3 {
		t.Fatalf("loaded %d messages, want 3", n)
	}
	if failed := files(t, filepath.Join(dir, failedDir)); len(failed) != 1 {
		t.Fatalf("the corrupt message should be in failed/: %v", failed)
	}
	for _, name := range files(t, dir) {
		if strings.HasSuffix(name, tmpSuffix) {
			t.Fatalf("temporary file kept: %s", name)
		}
	}
	start(t, q2)
	eventually(t, "delivery", func() bool { _, d := sender.count(); return d == 3 })
}

func TestShutdownWaitsForDeliveriesAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{started: make(chan struct{}, 10), release: make(chan struct{})}
	q, _ := newQueue(t, dir, sender, func(o *Options) { o.Workers = 1 })
	for range 3 {
		if err := q.Send(context.Background(), message(t)); err != nil {
			t.Fatal(err)
		}
	}
	q.Start()
	<-sender.started // one delivery in flight, two waiting

	done := make(chan error, 1)
	go func() { done <- q.Shutdown(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("shutdown returned while a delivery was running: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(sender.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, d := sender.count(); d != 1 {
		t.Fatalf("delivered %d, want only the one in flight", d)
	}
	if names := files(t, dir); len(names) != 2 {
		t.Fatalf("left for the next run: %v", names)
	}
}

func TestShutdownGivesUpAtTheDeadline(t *testing.T) {
	sender := &fakeSender{started: make(chan struct{}, 1), release: make(chan struct{})}
	q, _ := newQueue(t, t.TempDir(), sender, func(o *Options) { o.Workers = 1 })
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	q.Start()
	<-sender.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := q.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(sender.release)
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsAnUnusableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := Open(Options{Dir: file, MaxBytes: 1}, &fakeSender{}, parseRaw, log); err == nil {
		t.Fatal("a file is not a queue directory")
	}
	if _, err := Open(Options{}, &fakeSender{}, parseRaw, log); err == nil {
		t.Fatal("an empty directory name was accepted")
	}
}

func TestWriteFailureIsTemporaryAndReportedByHealth(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	q, _ := newQueue(t, dir, &fakeSender{})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := q.Send(context.Background(), message(t))
	qe, ok := errors.AsType[*Error](err)
	if !ok || !qe.Temporary() {
		t.Fatalf("err = %v", err)
	}
	if n, size := q.Stats(); n != 0 || size != 0 {
		t.Fatalf("a failed write was counted: %d, %d", n, size)
	}
	if q.Health() == nil {
		t.Fatal("a disk that cannot be written must report not ready")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	if err := q.Health(); err != nil {
		t.Fatalf("health after recovery: %v", err)
	}
}

func TestParseFailureMovesToFailed(t *testing.T) {
	dir := t.TempDir()
	sender := &fakeSender{}
	q, m := newQueue(t, dir, sender)
	q.parse = func(string, []string, []byte) (*parser.Message, error) { return nil, errors.New("broken") }
	start(t, q)
	if err := q.Send(context.Background(), message(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "failure", func() bool { return m.QueueDeliveries.Value("failed") == 1 })
	if calls, _ := sender.count(); calls != 0 {
		t.Fatalf("sent an unparsable message: %d", calls)
	}
}

func TestConcurrentSends(t *testing.T) {
	sender := &fakeSender{}
	q, _ := newQueue(t, t.TempDir(), sender, func(o *Options) { o.Workers = 4 })
	start(t, q)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg, err := parseRaw("alice@example.com", []string{fmt.Sprintf("u%d@example.com", i)}, []byte(rawMail))
			if err == nil {
				err = q.Send(context.Background(), msg)
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	eventually(t, "all deliveries", func() bool { _, d := sender.count(); return d == 40 })
	eventually(t, "empty queue", func() bool { n, b := q.Stats(); return n == 0 && b == 0 })
	seen := map[string]bool{}
	for _, msg := range sender.delivered {
		seen[msg.RcptTo[0]] = true
	}
	if len(seen) != 40 {
		t.Fatalf("%d distinct recipients delivered, want 40", len(seen))
	}
}

func TestOldestMessageGoesFirst(t *testing.T) {
	sender := &fakeSender{}
	q, _ := newQueue(t, t.TempDir(), sender, func(o *Options) { o.Workers = 1 })
	for i := range 5 {
		msg, err := parseRaw("alice@example.com", []string{fmt.Sprintf("u%d@example.com", i)}, []byte(rawMail))
		if err != nil {
			t.Fatal(err)
		}
		if err := q.Send(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond) // ids start with the time
	}
	start(t, q)
	eventually(t, "deliveries", func() bool { _, d := sender.count(); return d == 5 })
	for i, msg := range sender.delivered {
		if want := fmt.Sprintf("u%d@example.com", i); msg.RcptTo[0] != want {
			t.Fatalf("delivery %d went to %s, want %s", i, msg.RcptTo[0], want)
		}
	}
}
