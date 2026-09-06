// Package state holds the global installation and runtime state of cloudpost.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Config is the persisted installation configuration.
type Config struct {
	Installed     bool   `json:"installed"`
	PrimaryDomain string `json:"primary_domain"`
	Hostname      string `json:"hostname"`
	// AdminHash is the bcrypt hash of the admin password. It must persist in
	// config.json so logins survive process restarts; API responses never
	// serialize it (all client payloads are hand-built maps).
	AdminHash   string `json:"admin_hash"`
	SMTPPort    int    `json:"smtp_port"`
	SMTPSSLPort int    `json:"smtp_ssl_port"`
	POP3Port    int    `json:"pop3_port"`
	POP3SSLPort int    `json:"pop3_ssl_port"`
	IMAPPort    int    `json:"imap_port"`
	IMAPSSLPort int    `json:"imap_ssl_port"`
	WebPort     int    `json:"web_port"`
	RelayHost   string `json:"relay_host"`
	RelayPort   int    `json:"relay_port"`
	RelayUser   string `json:"relay_user"`
	RelayPass   string `json:"relay_pass"`

	// DKIM signing for outbound mail (applies to the primary domain only).
	// DKIMKeyPEM holds the RSA private key in PEM (PKCS#1 or PKCS#8); the
	// public half is published as a DNS TXT record <selector>._domainkey.<domain>.
	DKIMEnabled  bool   `json:"dkim_enabled"`
	DKIMSelector string `json:"dkim_selector"`
	DKIMKeyPEM   string `json:"dkim_key_pem,omitempty"`
}

// State is the process-wide runtime state.
type State struct {
	mu       sync.RWMutex
	cfg      *Config
	path     string
	sessions map[string]*Session
	dataDir  string
}

// Session is an authenticated admin web session.
type Session struct {
	Token   string `json:"token"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// New creates the state rooted at dir.
func New(dir string) *State {
	return &State{
		path:     filepath.Join(dir, "config.json"),
		sessions: map[string]*Session{},
		dataDir:  dir,
	}
}

// Load reads config.json if present.
func (s *State) Load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	cfg := &Config{}
	if jsonUnmarshal(b, cfg) == nil {
		// Self-heal legacy configs written before the admin hash was
		// persisted: they claim installed but no login can ever succeed.
		// Drop back to the setup wizard honestly instead of dead-locking.
		if cfg.Installed && cfg.AdminHash == "" {
			cfg.Installed = false
		}
		s.cfg = cfg
	}
}

// Save persists the current config.
func (s *State) Save() error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	b, err := jsonMarshalIndent(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}

// Config returns the current config (may be nil before install).
func (s *State) Config() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// SetConfig installs a new config and persists it.
func (s *State) SetConfig(cfg *Config) error {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return s.Save()
}

// Installed reports whether the installation wizard has completed.
func (s *State) Installed() bool {
	c := s.Config()
	return c != nil && c.Installed
}

// NewSession creates a session token valid for ttlSec seconds.
func (s *State) NewSession(ttlSec int64) *Session {
	now := currentTime()
	sess := &Session{Token: randomToken(), Created: now, Expires: now + ttlSec}
	s.mu.Lock()
	s.sessions[sess.Token] = sess
	s.mu.Unlock()
	return sess
}

// ValidSession checks a token and refreshes nothing; expired sessions are dropped.
func (s *State) ValidSession(token string) *Session {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil
	}
	if currentTime() > sess.Expires {
		delete(s.sessions, token)
		return nil
	}
	return sess
}

// DropSession removes a session.
func (s *State) DropSession(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// DataDir returns the data directory root.
func (s *State) DataDir() string { return s.dataDir }

// SetAdminHash replaces the persisted admin password hash (forced reset).
func (s *State) SetAdminHash(hash string) error {
	s.mu.Lock()
	if s.cfg == nil {
		s.mu.Unlock()
		return fmt.Errorf("not installed")
	}
	s.cfg.AdminHash = hash
	s.mu.Unlock()
	return s.Save()
}

// DropAllSessions invalidates every admin web session; used after a forced
// password reset so cookies issued under the old password cannot outlive it.
func (s *State) DropAllSessions() {
	s.mu.Lock()
	s.sessions = map[string]*Session{}
	s.mu.Unlock()
}

// FactoryReset returns the instance to pre-install state: the config file is
// deleted and every in-memory session is dropped. Database rows and data
// files are wiped separately (see db.Reset and mailstore.WipeBlobs).
func (s *State) FactoryReset() error {
	s.mu.Lock()
	s.cfg = nil
	s.sessions = map[string]*Session{}
	path := s.path
	s.mu.Unlock()
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
