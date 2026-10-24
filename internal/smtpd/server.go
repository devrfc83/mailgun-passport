package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"mailgun-passport/internal/config"
	"mailgun-passport/internal/parser"
)

type Sender interface {
	Send(ctx context.Context, msg *parser.Message) error
}

type Server struct {
	cfg    config.Config
	sender Sender
	log    *slog.Logger
	smtp   *smtp.Server
}

func New(cfg config.Config, sender Sender, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, sender: sender, log: log}
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
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("cargar certificado TLS: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}
	s.smtp = srv
	return s, nil
}

func (s *Server) ListenAndServe() error {
	s.log.Info("listening on SMTP", "addr", s.cfg.Listen, "domain", s.cfg.SMTPDomain)
	return s.smtp.ListenAndServe()
}

func (s *Server) Serve(l net.Listener) error {
	s.log.Info("listening on SMTP", "addr", l.Addr().String(), "domain", s.cfg.SMTPDomain)
	return s.smtp.Serve(l)
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

func (b *backend) NewSession(_ *smtp.Conn) (smtp.Session, error) {
	return &session{server: b.server}, nil
}

type session struct {
	server   *Server
	mu       sync.Mutex
	authed   bool
	mailFrom string
	rcptTo   []string
}

func (s *session) AuthMechanisms() []string {
	if !s.server.cfg.AuthRequired() {
		return nil
	}
	return []string{sasl.Plain}
}

func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.server.cfg.AuthRequired() {
		return nil, smtp.ErrAuthUnsupported
	}
	if mech != sasl.Plain {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(_, username, password string) error {
		if subtleEqual(username, s.server.cfg.SMTPUsername) && subtleEqual(password, s.server.cfg.SMTPPassword) {
			s.mu.Lock()
			s.authed = true
			s.mu.Unlock()
			return nil
		}
		return smtp.ErrAuthFailed
	}), nil
}

func (s *session) requireAuth() error {
	if !s.server.cfg.AuthRequired() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authed {
		return smtp.ErrAuthRequired
	}
	return nil
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	s.mu.Lock()
	s.mailFrom = from
	s.rcptTo = nil
	s.mu.Unlock()
	s.server.log.Debug("MAIL FROM", "from", from)
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	s.mu.Lock()
	s.rcptTo = append(s.rcptTo, to)
	s.mu.Unlock()
	s.server.log.Debug("RCPT TO", "to", to)
	return nil
}

func (s *session) Data(r io.Reader) error {
	if err := s.requireAuth(); err != nil {
		return err
	}
	s.mu.Lock()
	from := s.mailFrom
	rcpts := append([]string{}, s.rcptTo...)
	s.mu.Unlock()

	msg, err := parser.Parse(from, rcpts, r, s.server.cfg.MaxMessageBytes)
	if err != nil {
		s.server.log.Warn("failed to parse message", "err", err)
		return &smtp.SMTPError{
			Code:         550,
			EnhancedCode: smtp.EnhancedCode{5, 6, 0},
			Message:      "invalid message: " + err.Error(),
		}
	}

	if err := s.server.sender.Send(context.Background(), msg); err != nil {
		s.server.log.Error("Mailgun rejected message", "err", err, "from", msg.From, "subject", msg.Subject)
		code, enhanced, text := classifyMailgunError(err)
		return &smtp.SMTPError{Code: code, EnhancedCode: enhanced, Message: text}
	}
	return nil
}

func (s *session) Reset() {
	s.mu.Lock()
	s.mailFrom = ""
	s.rcptTo = nil
	s.mu.Unlock()
}

func (s *session) Logout() error {
	return nil
}

func classifyMailgunError(err error) (int, smtp.EnhancedCode, string) {
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "429") || strings.Contains(lower, "timeout") || strings.Contains(lower, "temporar") {
		return 451, smtp.EnhancedCode{4, 4, 0}, "temporary error sending: " + msg
	}
	return 451, smtp.EnhancedCode{4, 3, 0}, "error sending via Mailgun: " + msg
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		_ = a == b
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func IsClosed(err error) bool {
	return errors.Is(err, smtp.ErrServerClosed) || errors.Is(err, net.ErrClosed)
}
