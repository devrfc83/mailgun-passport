// Package queue is an optional on-disk spool for the relay. With it, a
// message is stored durably before the SMTP client gets its 250, and delivered
// to Mailgun in the background, with retries. It is for clients that do not
// retry on 4xx: a web application that sends a password reset during a Mailgun
// outage would otherwise drop the message.
//
// Each message is one file, written with a temporary name, flushed to disk and
// renamed, so a crash leaves either the whole message or nothing. A message
// that Mailgun refuses for good, or that stays undelivered for longer than the
// maximum age, moves to the failed/ subdirectory. Nobody is told by email: the
// log and the metrics report it, and the file stays for the operator.
package queue

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/metrics"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

const (
	fileSuffix = ".msg"
	tmpSuffix  = ".tmp"
	failedDir  = "failed"
	// maxHeaderLine bounds the JSON line that starts every file.
	maxHeaderLine = 1 << 20
)

// Sender delivers a message to Mailgun, as mailgunx.Client does.
type Sender interface {
	Send(ctx context.Context, msg *parser.Message) error
}

// temporary is implemented by send errors that know whether a retry may work,
// such as mailgunx.Error.
type temporary interface {
	error
	Temporary() bool
}

// Parse rebuilds a message from what the spool stored, with the same parser
// the SMTP side used.
type Parse func(mailFrom string, rcptTo []string, raw []byte) (*parser.Message, error)

// Options configure a Queue.
type Options struct {
	Dir      string
	MaxBytes int64         // largest total size of the waiting messages
	MaxAge   time.Duration // after this long undelivered, a message is given up on
	Workers  int
	Metrics  *metrics.Set
}

// Queue stores messages and delivers them in the background.
type Queue struct {
	dir      string
	sender   Sender
	parse    Parse
	log      *slog.Logger
	metrics  *metrics.Set
	maxBytes int64
	maxAge   time.Duration
	workers  int

	// Retry delay: backoffBase doubles with every attempt up to backoffMax.
	backoffBase, backoffMax time.Duration
	now                     func() time.Time

	stop     chan struct{}
	wake     chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once

	mu       sync.Mutex
	entries  map[string]*entry
	bytes    int64 // waiting messages, including those being sent
	writeErr error // last failure to store a message, cleared by the next success
}

type entry struct {
	id       string
	size     int64
	received time.Time
	attempts int
	next     time.Time
	busy     bool
}

// stored is the JSON line at the start of a file; the raw message follows it.
type stored struct {
	Version  int       `json:"v"`
	Received time.Time `json:"received"`
	MailFrom string    `json:"mail_from"`
	RcptTo   []string  `json:"rcpt_to"`
}

// Error is a failure to accept a message into the queue. It is always
// temporary, so the SMTP client keeps the message and retries.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string         { return e.Err.Error() }
func (e *Error) Unwrap() error         { return e.Err }
func (e *Error) Temporary() bool       { return true }
func (e *Error) ClientMessage() string { return e.Reason }

// Open prepares the spool directory, loads the messages a previous run left
// there and returns a Queue. Call Start to begin delivering.
func Open(opts Options, sender Sender, parse Parse, log *slog.Logger) (*Queue, error) {
	if opts.Dir == "" {
		return nil, errors.New("queue directory is empty")
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}
	q := &Queue{
		dir:         opts.Dir,
		sender:      sender,
		parse:       parse,
		log:         log,
		metrics:     opts.Metrics,
		maxBytes:    opts.MaxBytes,
		maxAge:      opts.MaxAge,
		workers:     opts.Workers,
		backoffBase: 30 * time.Second,
		backoffMax:  15 * time.Minute,
		now:         time.Now,
		stop:        make(chan struct{}),
		wake:        make(chan struct{}, opts.Workers),
		entries:     map[string]*entry{},
	}
	if q.metrics == nil {
		q.metrics = metrics.New("", "")
	}
	if err := os.MkdirAll(filepath.Join(q.dir, failedDir), 0o700); err != nil {
		return nil, fmt.Errorf("queue directory: %w", err)
	}
	if err := q.probe(); err != nil {
		return nil, fmt.Errorf("queue directory is not writable: %w", err)
	}
	if err := q.scan(); err != nil {
		return nil, fmt.Errorf("queue directory: %w", err)
	}
	return q, nil
}

// probe checks that files can be created, written and removed in the directory.
func (q *Queue) probe() error {
	f, err := os.CreateTemp(q.dir, ".probe-*"+tmpSuffix)
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString("probe")
	serr := f.Sync()
	cerr := f.Close()
	rerr := os.Remove(name)
	return errors.Join(werr, serr, cerr, rerr)
}

// scan registers the messages already on disk and removes the temporary files
// of writes that a crash cut short.
func (q *Queue) scan() error {
	files, err := os.ReadDir(q.dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		name := f.Name()
		switch {
		case f.IsDir():
		case strings.HasSuffix(name, tmpSuffix):
			_ = os.Remove(filepath.Join(q.dir, name))
		case strings.HasSuffix(name, fileSuffix):
			id := strings.TrimSuffix(name, fileSuffix)
			info, err := f.Info()
			if err != nil {
				return err
			}
			h, _, err := q.read(id)
			if err != nil {
				q.log.Error("unreadable message in the queue, moving it to failed/", "id", id, "err", err)
				q.fail(id)
				continue
			}
			q.entries[id] = &entry{id: id, size: info.Size(), received: h.Received, next: q.now()}
			q.bytes += info.Size()
		}
	}
	if len(q.entries) > 0 {
		q.log.Info("messages waiting in the queue from a previous run", "count", len(q.entries), "bytes", q.bytes)
	}
	return nil
}

// Start launches the delivery workers.
func (q *Queue) Start() {
	for range q.workers {
		q.wg.Add(1)
		go q.worker()
	}
}

// Shutdown stops taking new work and waits for deliveries in progress, until
// ctx ends. Messages not yet delivered stay on disk for the next run.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.stopOnce.Do(func() { close(q.stop) })
	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send stores msg for delivery. It satisfies the SMTP server's Sender: once it
// returns nil, the message is on disk and the client may get its 250.
func (q *Queue) Send(_ context.Context, msg *parser.Message) error {
	size := int64(len(msg.Raw))
	q.mu.Lock()
	if q.bytes+size > q.maxBytes {
		q.mu.Unlock()
		return &Error{Reason: "local queue is full", Err: fmt.Errorf("queue is full (%d of %d bytes)", q.bytes, q.maxBytes)}
	}
	q.bytes += size // reserved while the file is written
	q.mu.Unlock()

	id, fileSize, err := q.write(msg)

	q.mu.Lock()
	defer q.mu.Unlock()
	q.bytes += fileSize - size
	if err != nil {
		q.writeErr = err
		q.log.Error("cannot store the message in the queue", "err", err)
		return &Error{Reason: "cannot store the message locally", Err: err}
	}
	q.writeErr = nil
	q.entries[id] = &entry{id: id, size: fileSize, received: q.now(), next: q.now()}
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return nil
}

// write stores msg durably and returns its id and file size.
func (q *Queue) write(msg *parser.Message) (id string, size int64, err error) {
	var suffix [4]byte
	if _, err := crand.Read(suffix[:]); err != nil {
		return "", 0, err
	}
	id = fmt.Sprintf("%016x-%s", q.now().UnixNano(), hex.EncodeToString(suffix[:]))
	header, err := json.Marshal(stored{Version: 1, Received: q.now().UTC(), MailFrom: msg.MailFrom, RcptTo: msg.RcptTo})
	if err != nil {
		return "", 0, err
	}
	tmp := filepath.Join(q.dir, id+tmpSuffix)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	n, werr := f.Write(append(header, '\n'))
	m, werr2 := f.Write(msg.Raw)
	// The message must be on disk before the client is told it is accepted.
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, werr2, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, filepath.Join(q.dir, id+fileSuffix)); err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	if err := syncDir(q.dir); err != nil {
		// The rename happened; failing now would make the client send a duplicate.
		q.log.Warn("cannot flush the queue directory", "err", err)
	}
	return id, int64(n + m), nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// read returns the stored header and raw message of a file.
func (q *Queue) read(id string) (stored, []byte, error) {
	f, err := os.Open(filepath.Join(q.dir, id+fileSuffix))
	if err != nil {
		return stored{}, nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	line, err := readLine(r)
	if err != nil {
		return stored{}, nil, fmt.Errorf("header: %w", err)
	}
	var h stored
	if err := json.Unmarshal(line, &h); err != nil {
		return stored{}, nil, fmt.Errorf("header: %w", err)
	}
	if h.Version != 1 || len(h.RcptTo) == 0 {
		return stored{}, nil, errors.New("header: unsupported or incomplete")
	}
	raw, err := io.ReadAll(r)
	return h, raw, err
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > maxHeaderLine {
			return nil, errors.New("line too long")
		}
		if err == nil {
			return bytes.TrimRight(line, "\r\n"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		select {
		case <-q.stop:
			return
		default:
		}
		e, wait := q.claim()
		if e == nil {
			timer := time.NewTimer(wait)
			select {
			case <-q.stop:
				timer.Stop()
				return
			case <-q.wake:
			case <-timer.C:
			}
			timer.Stop()
			continue
		}
		q.deliver(e)
	}
}

// claim returns the oldest message that is due and marks it busy. When none is
// due it returns how long to wait for the next one.
func (q *Queue) claim() (*entry, time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	var best *entry
	wait := time.Minute
	for _, e := range q.entries {
		if e.busy {
			continue
		}
		if d := e.next.Sub(now); d > 0 {
			wait = min(wait, d)
			continue
		}
		if best == nil || e.id < best.id {
			best = e
		}
	}
	if best != nil {
		best.busy = true
	}
	return best, wait
}

func (q *Queue) deliver(e *entry) {
	h, raw, err := q.read(e.id)
	if err != nil {
		q.log.Error("cannot read a queued message, moving it to failed/", "id", e.id, "err", err)
		q.finish(e, "failed", true)
		return
	}
	msg, err := q.parse(h.MailFrom, h.RcptTo, raw)
	if err != nil {
		q.log.Error("cannot parse a queued message, moving it to failed/", "id", e.id, "err", err)
		q.finish(e, "failed", true)
		return
	}
	err = q.sender.Send(context.Background(), msg)
	if err == nil {
		q.log.Info("queued message delivered", "id", e.id, "attempts", e.attempts+1,
			"age", q.now().Sub(h.Received).Round(time.Second).String())
		q.finish(e, "delivered", false)
		return
	}
	if de, ok := errors.AsType[temporary](err); ok && !de.Temporary() {
		q.log.Error("Mailgun refused a queued message for good, moving it to failed/", "id", e.id, "err", err)
		q.finish(e, "failed", true)
		return
	}
	if age := q.now().Sub(h.Received); age >= q.maxAge {
		q.log.Error("giving up on a queued message: QUEUE_MAX_AGE reached, moving it to failed/",
			"id", e.id, "age", age.Round(time.Second).String(), "err", err)
		q.finish(e, "expired", true)
		return
	}
	q.retry(e, err)
}

// retry schedules the next attempt of a message that failed for a reason that
// may pass.
func (q *Queue) retry(e *entry, cause error) {
	q.mu.Lock()
	e.attempts++
	delay := min(q.backoffBase<<min(e.attempts-1, 20), q.backoffMax)
	// ±20%, so retries do not arrive together. Not a security matter.
	delay = delay*4/5 + time.Duration(rand.Int64N(int64(delay)*2/5+1)) //nolint:gosec
	e.next = q.now().Add(delay)
	e.busy = false
	attempts := e.attempts
	q.mu.Unlock()
	q.metrics.QueueDeliveries.Inc("retry")
	q.log.Warn("queued message not delivered, will retry", "id", e.id, "attempts", attempts,
		"retry_in", delay.Round(time.Second).String(), "err", cause)
}

// finish removes a message from the queue: deleted when delivered, moved to
// failed/ otherwise.
func (q *Queue) finish(e *entry, result string, keep bool) {
	if keep {
		q.fail(e.id)
	} else if err := os.Remove(filepath.Join(q.dir, e.id+fileSuffix)); err != nil {
		q.log.Error("cannot remove a delivered message from the queue", "id", e.id, "err", err)
	}
	q.metrics.QueueDeliveries.Inc(result)
	q.mu.Lock()
	delete(q.entries, e.id)
	q.bytes -= e.size
	q.mu.Unlock()
}

// fail moves a message to the failed/ subdirectory.
func (q *Queue) fail(id string) {
	from := filepath.Join(q.dir, id+fileSuffix)
	if err := os.Rename(from, filepath.Join(q.dir, failedDir, id+fileSuffix)); err != nil {
		q.log.Error("cannot move a message to failed/", "id", id, "err", err)
	}
}

// Stats returns how many messages wait for delivery and their total size.
func (q *Queue) Stats() (messages int, bytes int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries), q.bytes
}

// Health returns nil while the queue accepts messages: it has room and the
// last write to disk worked. With the queue in front, this is what /ready
// reports, since a Mailgun outage no longer makes clients retry.
func (q *Queue) Health() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.writeErr != nil {
		return &Error{Reason: "cannot store messages locally", Err: q.writeErr}
	}
	if q.bytes >= q.maxBytes {
		return &Error{Reason: "local queue is full", Err: fmt.Errorf("queue is full (%d of %d bytes)", q.bytes, q.maxBytes)}
	}
	return nil
}
