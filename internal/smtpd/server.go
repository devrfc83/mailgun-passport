package smtpd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/devrfc83/mailgun-passport/internal/config"
	"github.com/devrfc83/mailgun-passport/internal/parser"
)

const maxAuthFailures = 3

// authFailureDelay slows down password guessing; tests shorten it.
var authFailureDelay = time.Second

type Sender interface {
	Send(ctx context.Context, msg *parser.Message) error
}

type Server struct {
	cfg    config.Config
	sender Sender
	log    *slog.Logger
	smtp   *smtp.Server
	auth   *authLimiter
	// serving is true while the SMTP listener accepts connections.
	serving atomic.Bool
}

func New(cfg config.Config, sender Sender, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, sender: sender, log: log, auth: newAuthLimiter(cfg.AuthMaxFailures, log)}
	be := &backend{server: s}
	srv := smtp.NewServer(be)
	srv.Addr = cfg.Listen
	srv.Domain = cfg.SMTPDomain
	srv.ReadTimeout = cfg.ReadTimeout
	srv.WriteTimeout = cfg.WriteTimeout
	srv.MaxMessageBytes = cfg.MaxMessageBytes
	srv.MaxRecipients = cfg.MaxRecipients
	srv.AllowInsecureAuth = cfg.AllowInsecureAuth
	srv.EnableSMTPUTF8 = true
	if cfg.TLSCertFile != "" {
		certs, err := newCertReloader(cfg.TLSCertFile, cfg.TLSKeyFile, log)
		if err != nil {
			return nil, err
		}
		srv.TLSConfig = &tls.Config{
			GetCertificate: certs.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
	}
	s.smtp = srv
	return s, nil
}

func (s *Server) ListenAndServe() error {
	l, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

func (s *Server) Serve(l net.Listener) error {
	s.log.Info("listening on SMTP", "addr", l.Addr().String(), "domain", s.cfg.SMTPDomain,
		"auth", s.cfg.AuthRequired(), "starttls", s.cfg.TLSCertFile != "",
		"senders", senderSummary(s.cfg.AllowedSenderDomains), "max_connections", s.cfg.MaxConnections)
	s.serving.Store(true)
	defer s.serving.Store(false)
	return s.smtp.Serve(newLimitListener(l, s.cfg.MaxConnections, s.log))
}

// Serving reports whether the SMTP listener is accepting connections.
func (s *Server) Serving() bool {
	return s.serving.Load()
}

func (s *Server) Close() error {
	return s.smtp.Close()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.smtp.Shutdown(ctx)
}

type backend struct {
	server *Server
}

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	s := &session{server: b.server}
	if addr := c.Conn().RemoteAddr(); addr != nil {
		s.remote = addr.String()
		s.client, s.limited = authClient(addr)
	}
	return s, nil
}

// session needs no locking: go-smtp serves each connection from one
// goroutine and calls these methods one at a time.
type session struct {
	server       *Server
	remote       string
	client       netip.Prefix
	limited      bool // count AUTH failures under client
	authed       bool
	authFailures int
	mailFrom     string
	rcptTo       []string
}

func (s *session) AuthMechanisms() []string {
	if !s.server.cfg.AuthRequired() {
		return nil
	}
	return []string{sasl.Plain, sasl.Login}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.server.cfg.AuthRequired() {
		return nil, smtp.ErrAuthUnsupported
	}
	if mech != sasl.Plain && mech != sasl.Login {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	if s.authFailures >= maxAuthFailures {
		return nil, &smtp.SMTPError{
			Code:         421,
			EnhancedCode: smtp.EnhancedCode{4, 7, 0},
			Message:      "too many authentication failures, reconnect later",
		}
	}
	if s.limited && s.server.auth.blocked(s.client) {
		return nil, &smtp.SMTPError{
			Code:         421,
			EnhancedCode: smtp.EnhancedCode{4, 7, 0},
			Message:      "too many authentication failures from your address, try again later",
		}
	}
	check := func(username, password string) error {
		if secretEqual(username, s.server.cfg.SMTPUsername) && secretEqual(password, s.server.cfg.SMTPPassword) {
			s.authed = true
			return nil
		}
		s.authFailures++
		s.server.log.Warn("SMTP authentication failed", "remote", s.remote, "username", username)
		if s.limited {
			s.server.auth.fail(s.client)
		}
		time.Sleep(authFailureDelay)
		return smtp.ErrAuthFailed
	}
	if mech == sasl.Login {
		return &loginServer{authenticate: check}, nil
	}
	return sasl.NewPlainServer(func(_, username, password string) error {
		return check(username, password)
	}), nil
}

func (s *session) requireAuth() error {
	if !s.server.cfg.AuthRequired() {
		return nil
	}
	if !s.authed {
		return errAuthRequired
	}
	return nil
}

// errAuthRequired is the RFC 4954 reply; go-smtp's own uses 502.
var errAuthRequired = &smtp.SMTPError{
	Code:         530,
	EnhancedCode: smtp.EnhancedCode{5, 7, 0},
	Message:      "Authentication required",
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	if from != "" {
		if err := s.checkSender(from); err != nil {
			return err
		}
	}
	s.mailFrom = from
	s.rcptTo = nil
	s.server.log.Debug("MAIL FROM", "from", from)
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	// A repeated recipient would get one copy per RCPT TO in mime mode.
	if !slices.ContainsFunc(s.rcptTo, func(r string) bool { return strings.EqualFold(r, to) }) {
		s.rcptTo = append(s.rcptTo, to)
	}
	s.server.log.Debug("RCPT TO", "to", to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	parse := parser.Parse
	if s.server.cfg.MailgunSendMode == config.SendModeMIME {
		parse = parser.ParseHeaders
	}
	msg, err := parse(s.mailFrom, s.rcptTo, r, s.server.cfg.MaxMessageBytes)
	if err != nil {
		s.server.log.Warn("failed to parse message", "err", err)
		// go-smtp reports its own failures while reading DATA, such as
		// 552 for a message over SMTP_MAX_MESSAGE_BYTES; pass them through.
		if smtpErr, ok := errors.AsType[*smtp.SMTPError](err); ok {
			return smtpErr
		}
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 6, 0},
			Message:      "invalid message: " + err.Error(),
		}
	}
	if err := s.checkSender(msg.From); err != nil {
		return err
	}

	if err := s.server.sender.Send(context.Background(), msg); err != nil {
		s.server.log.Error("Mailgun rejected message", "err", err, "from", msg.From)
		s.server.log.Debug("rejected message details", "subject", msg.Subject)
		return deliveryError(err)
	}
	return nil
}

func senderSummary(domains []string) string {
	if len(domains) == 0 {
		return "any"
	}
	return strings.Join(domains, ",")
}

// checkSender enforces SMTP_ALLOWED_SENDER_DOMAINS on an address.
func (s *session) checkSender(addr string) error {
	if len(s.server.cfg.AllowedSenderDomains) == 0 {
		return nil
	}
	domain, err := parser.AddressDomain(addr)
	if err == nil && s.server.cfg.SenderDomainAllowed(domain) {
		return nil
	}
	s.server.log.Warn("sender domain not allowed", "remote", s.remote, "sender", addr)
	return &smtp.SMTPError{
		Code:         550,
		EnhancedCode: smtp.EnhancedCode{5, 7, 1},
		Message:      "sender domain not allowed",
	}
}

func (s *session) Reset() {
	s.mailFrom = ""
	s.rcptTo = nil
}

func (s *session) Logout() error {
	return nil
}

// DeliveryError is implemented by Sender errors that know whether a retry may
// succeed and how to describe the failure to the SMTP client.
type DeliveryError interface {
	error
	Temporary() bool
	ClientMessage() string
}

// deliveryError maps a send failure to an SMTP reply: 451 asks the client to
// retry later, 554 tells it the message will never be accepted. Errors of
// unknown kind are treated as temporary so no message is dropped.
func deliveryError(err error) *smtp.SMTPError {
	de, ok := errors.AsType[DeliveryError](err)
	if !ok {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "error sending via Mailgun, try again later"}
	}
	if !de.Temporary() {
		return &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 0, 0}, Message: de.ClientMessage()}
	}
	return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 4, 0}, Message: de.ClientMessage() + ", try again later"}
}

// secretEqual compares in constant time without leaking the secret's length.
func secretEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func IsClosed(err error) bool {
	return errors.Is(err, smtp.ErrServerClosed) || errors.Is(err, net.ErrClosed)
}
