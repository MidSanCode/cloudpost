// Package web serves the admin UI and the REST API.
package web

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cloudpost/internal/fetch"
	"cloudpost/internal/filter"
	"cloudpost/internal/mailstore"
	"cloudpost/internal/sender"
	"cloudpost/internal/state"
)

// Deps wires the web server.
type Deps struct {
	State     *state.State
	Store     *mailstore.Store
	Engine    *filter.Engine
	Sender    *sender.Sender
	Fetcher   *fetch.Fetcher
	Version   string
	StartedAt time.Time
	BlobDir   string
	StaticDir string
	Ports     *PortManager // optional: enables live port changes
}

// Server is the HTTP server.
type Server struct {
	deps     Deps
	mux      *http.ServeMux
	mailMu   sync.Mutex
	mailSess map[string]*mailSession // token -> webmail session

	webSwapMu sync.Mutex
	webSwap   net.Listener // pending replacement listener after a port change

	resetMu    sync.Mutex
	resetToken string    // one-time factory-reset confirmation token
	resetExp   time.Time // token expiry
}

// TakeWebSwap returns and clears a pending replacement web listener set by a
// live port change; the serving loop in main should switch to it.
func (s *Server) TakeWebSwap() net.Listener {
	s.webSwapMu.Lock()
	defer s.webSwapMu.Unlock()
	ln := s.webSwap
	s.webSwap = nil
	return ln
}

func (s *Server) setWebSwap(ln net.Listener) {
	s.webSwapMu.Lock()
	s.webSwap = ln
	s.webSwapMu.Unlock()
}

// New builds the web server.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mailSess: map[string]*mailSession{}}
	s.routes()
	return s
}

func (s *Server) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.handleAPI)
	staticDir := s.resolveStaticDir()
	if staticDir != "" {
		fs := http.FileServer(http.Dir(staticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Revalidate static assets so app.js updates are picked up.
			w.Header().Set("Cache-Control", "no-cache")
			p := filepath.Join(staticDir, filepath.Clean("/"+r.URL.Path))
			if st, err := os.Stat(p); err != nil || st.IsDir() {
				if r.URL.Path != "/" {
					http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
					return
				}
			}
			fs.ServeHTTP(w, r)
		})
	}
	s.mux = mux
}

func (s *Server) resolveStaticDir() string {
	if s.deps.StaticDir != "" {
		if st, err := os.Stat(s.deps.StaticDir); err == nil && st.IsDir() {
			return s.deps.StaticDir
		}
	}
	candidates := []string{"webui", filepath.Join(s.deps.State.DataDir(), "webui")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "webui"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	// Baseline hardening headers for every response.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Font Awesome CDN + inline styles (M3 tokens, mail HTML in sandboxed
		// iframe); scripts only from this origin; emails render in a
		// sandbox="" srcdoc iframe which additionally blocks scripts.
		// The pinned sha256 allows the SSO/agent script some deployments
		// inject into the page without opening the door to arbitrary inline
		// script (no 'unsafe-inline').
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'sha256-X7c5n+rklTbn02SSOWB9UdJFNB5vYGY1xCfALLI5Dfw='; "+
				"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; "+
				"font-src https://cdn.jsdelivr.net; img-src 'self' data: https:; connect-src 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		s.mux.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io2LimitReader(r.Body, 60<<20))
	return dec.Decode(v)
}

func io2LimitReader(r interface{ Read([]byte) (int, error) }, n int64) interface{ Read([]byte) (int, error) } {
	return &limited{r: r, n: n}
}

type limited struct {
	r interface{ Read([]byte) (int, error) }
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("body too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func pathID(r *http.Request, prefix string) (int64, bool) {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	rest = strings.SplitN(rest, "/", 2)[0]
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

const adminCookie = "cp_admin"
const mailCookie = "cp_mail"

func (s *Server) adminAuth(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil || s.deps.State.ValidSession(c.Value) == nil {
		writeErr(w, http.StatusUnauthorized, "admin login required")
		return false
	}
	return true
}

// mailSession is an authenticated webmail session with a hard expiry.
type mailSession struct {
	accID int64
	exp   time.Time
}

const mailSessionTTL = 7 * 24 * time.Hour

// mailAccount resolves the data-plane session to a local account.
//
// Two authentication paths are accepted:
//   - webmail session cookie / X-Mail-Token (interactive UI);
//   - per-account API access token via "Authorization: Bearer cpat_…"
//     (external automation; tokens are managed by the admin, see
//     /api/accounts/{id}/tokens).
func (s *Server) mailAccount(r *http.Request) *mailstore.Account {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		plain := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if strings.HasPrefix(plain, "cpat_") {
			acc, _, err := s.deps.Store.AccountByAPIToken(plain)
			if err != nil || acc == nil {
				return nil
			}
			return acc
		}
	}
	token := ""
	if c, err := r.Cookie(mailCookie); err == nil {
		token = c.Value
	}
	if h := r.Header.Get("X-Mail-Token"); h != "" {
		token = h
	}
	if token == "" {
		return nil
	}
	s.mailMu.Lock()
	sess, ok := s.mailSess[token]
	if ok && time.Now().After(sess.exp) {
		delete(s.mailSess, token) // expired
		ok = false
	}
	var accID int64
	if ok {
		accID = sess.accID
	}
	s.mailMu.Unlock()
	if !ok {
		return nil
	}
	acc, err := s.deps.Store.GetAccount(accID)
	if err != nil {
		return nil
	}
	return acc
}

// ---------------------------------------------------------------------------
// API router

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	// API responses must never be cached: setup/status flips between installs
	// and factory resets, and a stale cached response bricks the UI.
	w.Header().Set("Cache-Control", "no-store")
	p := r.URL.Path
	switch {
	case p == "/api/setup/status":
		s.handleSetupStatus(w, r)
	case p == "/api/whoami":
		s.handleWhoami(w, r)
	case p == "/api/setup" && r.Method == http.MethodPost:
		s.handleSetup(w, r)
	case p == "/api/login":
		s.handleLogin(w, r)
	case p == "/api/logout":
		s.handleLogout(w, r)
	case p == "/api/mail/login":
		s.handleMailLogin(w, r)
	case p == "/api/mail/logout":
		s.handleMailLogout(w, r)
	case strings.HasPrefix(p, "/api/mail/"):
		s.handleMail(w, r)
	case strings.HasPrefix(p, "/api/accounts"), strings.HasPrefix(p, "/api/filters"),
		strings.HasPrefix(p, "/api/folders"), strings.HasPrefix(p, "/api/queue"),
		strings.HasPrefix(p, "/api/settings"), strings.HasPrefix(p, "/api/status"),
		strings.HasPrefix(p, "/api/remote"), strings.HasPrefix(p, "/api/users"),
		strings.HasPrefix(p, "/api/dkim"), strings.HasPrefix(p, "/api/reset/"):
		if !s.adminAuth(w, r) {
			return
		}
		s.handleAdmin(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// ---------------------------------------------------------------------------
// setup + auth

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.State.Config()
	if cfg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"installed": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":      cfg.Installed,
		"primary_domain": cfg.PrimaryDomain,
		"hostname":       cfg.Hostname,
	})
}

// handleWhoami silently reports which session (if any) is active so the UI
// can route without triggering 401 noise or login-page flashes.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{}
	if c, err := r.Cookie(adminCookie); err == nil && s.deps.State.ValidSession(c.Value) != nil {
		resp["admin"] = true
	}
	if c, err := r.Cookie(mailCookie); err == nil {
		s.mailMu.Lock()
		sess, ok := s.mailSess[c.Value]
		s.mailMu.Unlock()
		if ok && time.Now().Before(sess.exp) {
			resp["mail"] = true
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type setupReq struct {
	AdminPassword string `json:"admin_password"`
	Domain        string `json:"domain"`
	Hostname      string `json:"hostname"`
	WebPort       int    `json:"web_port"`
	SMTPPort      int    `json:"smtp_port"`
	POP3Port      int    `json:"pop3_port"`
	IMAPPort      int    `json:"imap_port"`
	RelayHost     string `json:"relay_host"`
	RelayPort     int    `json:"relay_port"`
	RelayUser     string `json:"relay_user"`
	RelayPass     string `json:"relay_pass"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.deps.State.Installed() {
		writeErr(w, http.StatusConflict, "already installed")
		return
	}
	var req setupReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if len(req.AdminPassword) < 6 {
		writeErr(w, http.StatusBadRequest, "admin_password must be at least 6 chars")
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if req.Domain == "" {
		writeErr(w, http.StatusBadRequest, "domain is required")
		return
	}
	host := req.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	h, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cfg := &state.Config{
		Installed:     true,
		PrimaryDomain: req.Domain,
		Hostname:      host,
		AdminHash:     string(h),
		WebPort:       orDefault(req.WebPort, 8080),
		SMTPPort:      orDefault(req.SMTPPort, 2525),
		POP3Port:      orDefault(req.POP3Port, 1110),
		IMAPPort:      orDefault(req.IMAPPort, 1143),
		RelayHost:     req.RelayHost,
		RelayPort:     req.RelayPort,
		RelayUser:     req.RelayUser,
		RelayPass:     req.RelayPass,
	}
	if err := s.deps.State.SetConfig(cfg); err != nil {
		writeErr(w, 500, "save config: "+err.Error())
		return
	}
	resp := map[string]any{"ok": true}
	// Live-apply custom ports when they differ from what the servers
	// started on (the wizard runs after the daemons are already listening).
	if s.deps.Ports != nil {
		applied, errs := s.deps.Ports.ApplyProtocolPorts(
			orDefault(req.SMTPPort, 2525), orDefault(req.POP3Port, 1110), orDefault(req.IMAPPort, 1143))
		if len(applied) > 0 {
			resp["applied"] = applied
		}
		if len(errs) > 0 {
			resp["errors"] = errs
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	cfg := s.deps.State.Config()
	if cfg == nil || cfg.AdminHash == "" {
		writeErr(w, 400, "not installed")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(cfg.AdminHash), []byte(req.Password)) != nil {
		time.Sleep(400 * time.Millisecond) // slow brute force
		writeErr(w, 401, "wrong password")
		return
	}
	sess := s.deps.State.NewSession(7 * 24 * 3600)
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: sess.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"ok": true, "token": sess.Token})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil {
		s.deps.State.DropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleMailLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	// Admins may open ANY mailbox without its password — including remote
	// accounts' own storage (where remote-fetched mail lands when no
	// 投递目标 is set) — from the console.
	if c, err := r.Cookie(adminCookie); err == nil && s.deps.State.ValidSession(c.Value) != nil {
		acc, err := s.deps.Store.GetAccountByAddressAny(strings.ToLower(strings.TrimSpace(req.Address)))
		if err != nil {
			writeErr(w, 404, "no such account")
			return
		}
		s.startMailSession(w, acc)
		return
	}
	acc, ok := s.deps.Store.VerifyLocalLogin(req.Address, req.Password)
	if !ok {
		time.Sleep(400 * time.Millisecond) // slow brute force
		writeErr(w, 401, "invalid credentials")
		return
	}
	s.startMailSession(w, acc)
}

func (s *Server) startMailSession(w http.ResponseWriter, acc *mailstore.Account) {
	token := randToken()
	s.mailMu.Lock()
	s.mailSess[token] = &mailSession{accID: acc.ID, exp: time.Now().Add(mailSessionTTL)}
	s.mailMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: mailCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(mailSessionTTL.Seconds())})
	writeJSON(w, 200, map[string]any{"ok": true, "account": acc})
}

func (s *Server) handleMailLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(mailCookie); err == nil {
		s.mailMu.Lock()
		delete(s.mailSess, c.Value)
		s.mailMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: mailCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}
