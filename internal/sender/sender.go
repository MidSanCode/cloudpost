// Package sender delivers outbound mail: MX direct delivery with relay
// fallback, plus a SQLite-backed retry queue.
package sender

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"

	"cloudpost/internal/mailstore"
	"cloudpost/internal/state"
)

// Deps wires the sender.
type Deps struct {
	DB    *sql.DB
	State *state.State
	// Store enables delayed-send dispatching (may be nil in tests).
	Store *mailstore.Store
}

// QueueItem is one outbound message in the retry queue.
type QueueItem struct {
	ID         int64    `json:"id"`
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	DataPath   string   `json:"data_path"`
	Status     string   `json:"status"` // pending | sent | failed
	Attempts   int      `json:"attempts"`
	NextTryAt  int64    `json:"next_try_at"`
	LastError  string   `json:"last_error"`
	CreatedAt  int64    `json:"created_at"`
}

// Sender delivers outbound mail.
type Sender struct {
	deps Deps
	mu   sync.Mutex
	stop chan struct{}
}

// New creates a Sender.
func New(deps Deps) *Sender { return &Sender{deps: deps, stop: make(chan struct{})} }

// Enqueue stores an outbound message for delivery.
func (s *Sender) Enqueue(from string, rcpts []string, raw []byte) (int64, error) {
	dir := s.deps.State.DataDir() + "/outbound"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(dir, "msg-*.eml")
	if err != nil {
		return 0, err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return 0, err
	}
	path := f.Name()
	f.Close()
	rcptsJSON, _ := json.Marshal(rcpts)
	res, err := s.deps.DB.Exec(`INSERT INTO send_queue(from_addr,recipients,data_path,status,next_try_at,created_at)
		VALUES(?,?,?,'pending',?,?)`, from, string(rcptsJSON), path, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	log.Printf("[sender] queued #%d %s -> %v", id, from, rcpts)
	return id, nil
}

// Run processes the queue periodically until Stop.
func (s *Sender) Run(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	// First pass immediately.
	s.ProcessQueue()
	for {
		select {
		case <-t.C:
			s.ProcessQueue()
		case <-s.stop:
			return
		}
	}
}

// Stop terminates the delivery loop.
func (s *Sender) Stop() { close(s.stop) }

// ProcessQueue drains due queue items once.
func (s *Sender) ProcessQueue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.deps.DB.Query(`SELECT id,from_addr,recipients,data_path,status,attempts,next_try_at,last_error,created_at
		FROM send_queue WHERE status='pending' AND next_try_at<=? ORDER BY id`, time.Now().Unix())
	if err != nil {
		return
	}
	var items []*QueueItem
	for rows.Next() {
		it := &QueueItem{}
		var rcpts string
		if err := rows.Scan(&it.ID, &it.From, &rcpts, &it.DataPath, &it.Status, &it.Attempts, &it.NextTryAt, &it.LastError, &it.CreatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(rcpts), &it.Recipients)
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		s.deliver(it)
	}
}

func (s *Sender) deliver(it *QueueItem) {
	raw, err := os.ReadFile(it.DataPath)
	if err != nil {
		s.finish(it, "failed", "blob lost: "+err.Error())
		return
	}
	// DKIM-sign outbound mail from the primary domain before delivery.
	raw, _ = s.dkimSign(it.From, raw)
	cfg := s.deps.State.Config()
	var lastErr error
	for _, rcpt := range it.Recipients {
		host := rcptHost(rcpt)
		if host == "" {
			lastErr = fmt.Errorf("invalid recipient %s", rcpt)
			continue
		}
		var err error
		if cfg != nil && cfg.RelayHost != "" {
			err = sendViaRelay(cfg, it.From, rcpt, raw)
		} else {
			err = sendDirect(it.From, rcpt, raw)
		}
		if err != nil {
			lastErr = err
			continue
		}
	}
	if lastErr == nil {
		s.finish(it, "sent", "")
		_ = os.Remove(it.DataPath)
		return
	}
	it.Attempts++
	if it.Attempts >= 8 {
		s.finish(it, "failed", lastErr.Error())
		return
	}
	backoff := int64(60 * (1 << it.Attempts)) // 2m,4m,8m... capped below
	if backoff > 6*3600 {
		backoff = 6 * 3600
	}
	_, _ = s.deps.DB.Exec(`UPDATE send_queue SET attempts=?, next_try_at=?, last_error=? WHERE id=?`,
		it.Attempts, time.Now().Unix()+backoff, lastErr.Error(), it.ID)
	log.Printf("[sender] #%d attempt %d failed: %v (retry in %ds)", it.ID, it.Attempts, lastErr, backoff)
}

func (s *Sender) finish(it *QueueItem, status, errStr string) {
	_, _ = s.deps.DB.Exec(`UPDATE send_queue SET status=?, last_error=? WHERE id=?`, status, errStr, it.ID)
	if status == "sent" {
		log.Printf("[sender] #%d delivered", it.ID)
	} else {
		log.Printf("[sender] #%d failed permanently: %s", it.ID, errStr)
	}
}

func rcptHost(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return addr[i+1:]
}

// sendDirect delivers straight to the recipient MX host.
func sendDirect(from, rcpt string, raw []byte) error {
	host := rcptHost(rcpt)
	mxs, err := net.LookupMX(host)
	if err != nil || len(mxs) == 0 {
		// Fall back to A record (implicit MX).
		if _, err := net.LookupHost(host); err != nil {
			return fmt.Errorf("no MX for %s: %w", host, err)
		}
		mxs = []*net.MX{{Host: host + ".", Pref: 10}}
	}
	addr := mxs[0].Host
	if addr == "" {
		return fmt.Errorf("empty MX host for %s", host)
	}
	return smtpSend(addr+":25", from, []string{rcpt}, raw, false)
}

// sendViaRelay delivers through the configured smarthost.
func sendViaRelay(cfg *state.Config, from, rcpt string, raw []byte) error {
	addr := fmt.Sprintf("%s:%d", cfg.RelayHost, cfg.RelayPort)
	var auth smtp.Auth
	if cfg.RelayUser != "" {
		host := cfg.RelayHost
		auth = smtp.PlainAuth("", cfg.RelayUser, cfg.RelayPass, host)
	}
	return smtpSendAuth(addr, from, []string{rcpt}, raw, auth, true)
}

func smtpSend(addr, from string, rcpts []string, raw []byte, useTLS bool) error {
	return smtpSendAuth(addr, from, rcpts, raw, nil, useTLS)
}

func smtpSendAuth(addr, from string, rcpts []string, raw []byte, auth smtp.Auth, startTLS bool) error {
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	c, err := smtp.NewClient(conn, hostOnly(addr))
	if err != nil {
		conn.Close()
		return fmt.Errorf("greeting: %w", err)
	}
	defer c.Close()
	if startTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: hostOnly(addr)}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}
	}
	if err := c.Hello("localhost"); err != nil {
		// EHLO already implicit; ignore failure.
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("rcpt %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data close: %w", err)
	}
	return c.Quit()
}

func hostOnly(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return strings.Trim(addr[:i], "[]")
	}
	return addr
}
