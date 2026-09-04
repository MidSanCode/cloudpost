// Package smtpd implements the built-in inbound SMTP service.
package smtpd

import (
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	gosasl "github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"cloudpost/internal/mailstore"
	"cloudpost/internal/state"
)

// Hooks wires the SMTP backend to the rest of the system.
type Hooks struct {
	// EnqueueRaw queues an outbound message for delivery (relay or MX).
	EnqueueRaw func(from string, rcpts []string, raw []byte) error
	// PostDeliver runs after a local delivery (filters etc.).
	PostDeliver func(accountID, msgID int64, raw []byte)
}

// Deps is everything the SMTP backend needs.
type Deps struct {
	Store  *mailstore.Store
	State  *state.State
	Domain string
	Hooks  Hooks
}

type backend struct{ deps Deps }

func (b *backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &session{deps: b.deps}, nil
}

type session struct {
	deps          Deps
	from          string
	rcpts         []rcptTarget
	authenticated bool
	authAccountID int64
}

type rcptTarget struct {
	address string
	local   bool
	accID   int64
	fldrID  int64
}

func (s *session) AuthMechanisms() []string { return []string{"PLAIN", "LOGIN"} }

func (s *session) Auth(mech string) (gosasl.Server, error) {
	switch mech {
	case "PLAIN":
		return gosasl.NewPlainServer(func(identity, username, password string) error {
			return s.authenticate(username, password)
		}), nil
	case "LOGIN":
		return &loginServer{callback: s.authenticate}, nil
	}
	return nil, smtp.ErrAuthUnknownMechanism
}

// loginServer implements the obsolete SASL LOGIN mechanism server-side.
type loginServer struct {
	callback func(username, password string) error
	step     int
	user     string
}

func (s *loginServer) Start() (string, []byte, error) { return "LOGIN", []byte("Username:"), nil }

func (s *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch s.step {
	case 0:
		s.user = string(response)
		s.step = 1
		return []byte("Password:"), false, nil
	case 1:
		s.step = 2
		if err := s.callback(s.user, string(response)); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	}
	return nil, true, gosasl.ErrUnexpectedServerChallenge
}

func (s *session) authenticate(username, password string) error {
	acc, ok := s.deps.Store.VerifyLocalLogin(username, password)
	if !ok {
		return smtp.ErrAuthFailed
	}
	s.authenticated = true
	s.authAccountID = acc.ID
	return nil
}

func (s *session) Reset() { s.from, s.rcpts = "", nil }

func (s *session) Logout() error { return nil }

func (s *session) Mail(from string, opts *smtp.MailOptions) error {
	if !s.authenticated {
		return smtp.ErrAuthRequired
	}
	s.from = strings.ToLower(strings.Trim(from, "<>"))
	return nil
}

func (s *session) Rcpt(to string, opts *smtp.RcptOptions) error {
	if !s.authenticated {
		return smtp.ErrAuthRequired
	}
	addr := strings.ToLower(strings.Trim(to, "<>"))
	_, domain := splitAddr(addr)
	if strings.EqualFold(domain, s.effectiveDomain()) {
		acc, err := s.deps.Store.GetAccountByAddress(addr)
		if err != nil {
			return &smtp.SMTPError{Code: 550, Message: "No such mailbox here"}
		}
		inbox, err := s.deps.Store.FolderByName(acc.ID, "INBOX")
		if err != nil {
			return &smtp.SMTPError{Code: 451, Message: "Mailbox temporarily unavailable"}
		}
		s.rcpts = append(s.rcpts, rcptTarget{address: addr, local: true, accID: acc.ID, fldrID: inbox.ID})
		return nil
	}
	s.rcpts = append(s.rcpts, rcptTarget{address: addr, local: false})
	return nil
}

func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 50<<20))
	if err != nil {
		return &smtp.SMTPError{Code: 552, Message: "Failed to read message"}
	}
	anyOK := false
	var localRcpts, remoteRcpts []string
	for _, rc := range s.rcpts {
		if rc.local {
			msgID, _, err := s.deps.Store.Deliver(rc.accID, rc.fldrID, raw, nil)
			if err != nil {
				log.Printf("[smtp] local deliver to %s failed: %v", rc.address, err)
				continue
			}
			if s.deps.Hooks.PostDeliver != nil {
				s.deps.Hooks.PostDeliver(rc.accID, msgID, raw)
			}
			localRcpts = append(localRcpts, rc.address)
			anyOK = true
		} else {
			remoteRcpts = append(remoteRcpts, rc.address)
		}
	}
	if len(remoteRcpts) > 0 && s.deps.Hooks.EnqueueRaw != nil {
		if err := s.deps.Hooks.EnqueueRaw(s.from, remoteRcpts, raw); err != nil {
			log.Printf("[smtp] enqueue outbound failed: %v", err)
		} else {
			anyOK = true
		}
	}
	if !anyOK {
		return &smtp.SMTPError{Code: 451, Message: "Delivery failed"}
	}
	log.Printf("[smtp] %s -> local=%v remote=%v bytes=%d", s.from, localRcpts, remoteRcpts, len(raw))
	s.Reset()
	return nil
}

// effectiveDomain resolves the primary domain at request time so that the
// installation wizard's domain (set after startup) takes effect immediately.
func (s *session) effectiveDomain() string {
	if cfg := s.deps.State.Config(); cfg != nil && cfg.PrimaryDomain != "" {
		return cfg.PrimaryDomain
	}
	return s.deps.Domain
}

func splitAddr(addr string) (local, domain string) {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return addr, ""
	}
	return addr[:i], addr[i+1:]
}

// Server wraps the go-smtp server lifecycle.
type Server struct {
	srv *smtp.Server
	ln  net.Listener
	wg  sync.WaitGroup
}

// New builds the SMTP server.
func New(deps Deps) *Server {
	b := &backend{deps: deps}
	s := smtp.NewServer(b)
	s.Domain = deps.Domain
	s.MaxRecipients = 50
	s.MaxMessageBytes = 50 << 20
	s.AllowInsecureAuth = true
	s.EnableSMTPUTF8 = true
	return &Server{srv: s}
}

// Listen starts listening on addr.
func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Serve accepts connections; blocks until Close.
func (s *Server) Serve() error {
	return s.srv.Serve(s.ln)
}

// Close shuts the server down.
func (s *Server) Close() {
	_ = s.ln.Close()
	_ = s.srv.Close()
	s.wg.Wait()
}

var _ = os.Getenv
