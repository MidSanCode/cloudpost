package web

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cloudpost/internal/db"
	"cloudpost/internal/fetch"
	"cloudpost/internal/mailstore"
	"cloudpost/internal/state"
)

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) uptime() time.Duration { return time.Since(s.deps.StartedAt) }

func redactConfig(cfg *state.Config) any {
	if cfg == nil {
		return nil
	}
	return map[string]any{
		"installed":      cfg.Installed,
		"primary_domain": cfg.PrimaryDomain,
		"hostname":       cfg.Hostname,
		"web_port":       cfg.WebPort,
		"smtp_port":      cfg.SMTPPort,
		"pop3_port":      cfg.POP3Port,
		"imap_port":      cfg.IMAPPort,
		"relay_host":     cfg.RelayHost,
		"relay_port":     cfg.RelayPort,
	}
}

func bcryptCompare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func bcryptHash(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// handleAdmin routes admin API calls.
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	m := r.Method
	switch {
	case p == "/api/status":
		s.adminStatus(w, r)
	case p == "/api/accounts" && m == http.MethodGet:
		s.adminListAccounts(w, r)
	case p == "/api/accounts" && m == http.MethodPost:
		s.adminCreateAccount(w, r)
	case strings.HasPrefix(p, "/api/accounts/") && strings.HasSuffix(p, "/fetch") && m == http.MethodPost:
		id, _ := pathID(p2(r), "/api/accounts/")
		s.adminFetchNow(w, r, id)
	case strings.HasPrefix(p, "/api/accounts/") && strings.Contains(p, "/folders"):
		rest := strings.TrimPrefix(p, "/api/accounts/")
		segs := strings.Split(rest, "/")
		accID, err := strconv.ParseInt(segs[0], 10, 64)
		if err != nil {
			writeErr(w, 400, "bad account id")
			return
		}
		switch {
		case len(segs) == 2 && segs[1] == "folders" && m == http.MethodGet:
			s.adminListFolders(w, r, accID)
		case len(segs) == 2 && segs[1] == "folders" && m == http.MethodPost:
			s.adminCreateFolder(w, r, accID)
		case len(segs) == 4 && segs[2] == "folders":
			folderID, _ := strconv.ParseInt(segs[3], 10, 64)
			if m == http.MethodDelete {
				s.adminDeleteFolder(w, r, accID, folderID)
			} else if m == http.MethodPut {
				s.adminRenameFolder(w, r, accID, folderID)
			} else {
				writeErr(w, 405, "method not allowed")
			}
		default:
			writeErr(w, 404, "not found")
		}
	case strings.HasPrefix(p, "/api/accounts/") && strings.Contains(p, "/filters"):
		rest := strings.TrimPrefix(p, "/api/accounts/")
		segs := strings.Split(rest, "/")
		accID, err := strconv.ParseInt(segs[0], 10, 64)
		if err != nil {
			writeErr(w, 400, "bad account id")
			return
		}
		switch {
		case len(segs) == 2 && segs[1] == "filters" && m == http.MethodGet:
			s.adminListFilters(w, r, accID)
		case len(segs) == 2 && segs[1] == "filters" && m == http.MethodPost:
			s.adminCreateFilter(w, r, accID)
		default:
			writeErr(w, 404, "not found")
		}
	case strings.HasPrefix(p, "/api/accounts/"):
		id, ok := pathID(r, "/api/accounts/")
		if !ok {
			writeErr(w, 400, "bad id")
			return
		}
		switch m {
		case http.MethodGet:
			s.adminGetAccount(w, r, id)
		case http.MethodPut, http.MethodPatch:
			s.adminUpdateAccount(w, r, id)
		case http.MethodDelete:
			s.adminDeleteAccount(w, r, id)
		default:
			writeErr(w, 405, "method not allowed")
		}
	case strings.HasPrefix(p, "/api/filters/"):
		rest := strings.TrimPrefix(p, "/api/filters/")
		segs := strings.Split(rest, "/")
		if len(segs) == 2 {
			accID, _ := strconv.ParseInt(segs[0], 10, 64)
			fID, _ := strconv.ParseInt(segs[1], 10, 64)
			switch m {
			case http.MethodPut, http.MethodPatch:
				s.adminUpdateFilter(w, r, accID, fID)
			case http.MethodDelete:
				s.adminDeleteFilter(w, r, accID, fID)
			default:
				writeErr(w, 405, "method not allowed")
			}
			return
		}
		writeErr(w, 404, "not found")
	case p == "/api/remote/test" && m == http.MethodPost:
		s.adminTestRemote(w, r)
	case p == "/api/queue" && m == http.MethodGet:
		s.adminQueue(w, r)
	case p == "/api/queue/flush" && m == http.MethodPost:
		s.deps.Sender.ProcessQueue()
		writeJSON(w, 200, map[string]any{"ok": true})
	case p == "/api/settings" && m == http.MethodGet:
		s.adminGetSettings(w, r)
	case p == "/api/settings" && (m == http.MethodPut || m == http.MethodPost):
		s.adminUpdateSettings(w, r)
	case p == "/api/password" && m == http.MethodPost:
		s.adminChangePassword(w, r)
	case p == "/api/reset/prepare" && m == http.MethodPost:
		s.adminResetPrepare(w, r)
	case p == "/api/reset/confirm" && m == http.MethodPost:
		s.adminResetConfirm(w, r)
	case strings.HasPrefix(p, "/api/reset/"):
		writeErr(w, 404, "not found")
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func p2(r *http.Request) *http.Request { return r }

var _ = p2

// ---------------------------------------------------------------------------
// status

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.State.Config()
	var queuePending, queueFailed int64
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE status='pending'`).Scan(&queuePending)
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE status='failed'`).Scan(&queueFailed)
	accounts, _ := s.deps.Store.ListAccounts()
	localN, remoteN := 0, 0
	for _, a := range accounts {
		if a.Kind == "local" {
			localN++
		} else {
			remoteN++
		}
	}
	var msgN int64
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgN)
	writeJSON(w, 200, map[string]any{
		"version":         s.deps.Version,
		"uptime_sec":      int(s.uptime().Seconds()),
		"config":          redactConfig(cfg),
		"accounts_local":  localN,
		"accounts_remote": remoteN,
		"messages":        msgN,
		"queue_pending":   queuePending,
		"queue_failed":    queueFailed,
	})
}

// ---------------------------------------------------------------------------
// accounts

func (s *Server) adminListAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.deps.Store.ListAccounts()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*mailstore.Account{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) adminGetAccount(w http.ResponseWriter, r *http.Request, id int64) {
	acc, err := s.deps.Store.GetAccount(id)
	if err != nil {
		writeErr(w, 404, "no such account")
		return
	}
	writeJSON(w, 200, acc)
}

type accountReq struct {
	Kind              string `json:"kind"`
	Address           string `json:"address"`
	DisplayName       string `json:"display_name"`
	Password          string `json:"password"`
	RemoteProto       string `json:"remote_proto"`
	RemoteHost        string `json:"remote_host"`
	RemotePort        int    `json:"remote_port"`
	RemoteTLS         string `json:"remote_tls"`
	RemoteUser        string `json:"remote_user"`
	RemotePass        string `json:"remote_pass"`
	RemoteFolder      string `json:"remote_folder"`
	RemoteTarget      string `json:"remote_target"`
	FetchEnabled      *bool  `json:"fetch_enabled"`
	FetchIntervalMin  int    `json:"fetch_interval_min"`
	FetchKeepOnServer *bool  `json:"fetch_keep_on_server"`
}

func (s *Server) adminCreateAccount(w http.ResponseWriter, r *http.Request) {
	var req accountReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if req.Kind == "" {
		req.Kind = "local"
	}
	var acc *mailstore.Account
	var err error
	if req.Kind == "local" {
		acc, err = s.deps.Store.CreateLocalAccount(req.Address, req.Password, req.DisplayName)
	} else {
		remote := &mailstore.Account{
			Address: req.Address, DisplayName: req.DisplayName,
			RemoteProto: req.RemoteProto, RemoteHost: req.RemoteHost, RemotePort: req.RemotePort,
			RemoteTLS: req.RemoteTLS, RemoteUser: req.RemoteUser, RemotePass: req.RemotePass,
			RemoteFolder: req.RemoteFolder, RemoteTarget: req.RemoteTarget, FetchIntervalMin: req.FetchIntervalMin,
		}
		if req.FetchEnabled != nil {
			remote.FetchEnabled = *req.FetchEnabled
		} else {
			remote.FetchEnabled = true
		}
		if req.FetchKeepOnServer != nil {
			remote.FetchKeepOnServer = *req.FetchKeepOnServer
		} else {
			remote.FetchKeepOnServer = true
		}
		acc, err = s.deps.Store.CreateRemoteAccount(remote)
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, acc)
}

func (s *Server) adminUpdateAccount(w http.ResponseWriter, r *http.Request, id int64) {
	acc, err := s.deps.Store.GetAccount(id)
	if err != nil {
		writeErr(w, 404, "no such account")
		return
	}
	var req accountReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if req.Address != "" {
		acc.Address = req.Address
	}
	acc.DisplayName = req.DisplayName
	if acc.Kind == "remote" {
		acc.RemoteProto = orStr(req.RemoteProto, acc.RemoteProto)
		acc.RemoteHost = orStr(req.RemoteHost, acc.RemoteHost)
		if req.RemotePort > 0 {
			acc.RemotePort = req.RemotePort
		}
		acc.RemoteTLS = orStr(req.RemoteTLS, acc.RemoteTLS)
		acc.RemoteUser = orStr(req.RemoteUser, acc.RemoteUser)
		if req.RemotePass != "" {
			acc.RemotePass = req.RemotePass
		}
		acc.RemoteFolder = orStr(req.RemoteFolder, acc.RemoteFolder)
		acc.RemoteTarget = req.RemoteTarget
		if req.FetchIntervalMin > 0 {
			acc.FetchIntervalMin = req.FetchIntervalMin
		}
		if req.FetchEnabled != nil {
			acc.FetchEnabled = *req.FetchEnabled
		}
		if req.FetchKeepOnServer != nil {
			acc.FetchKeepOnServer = *req.FetchKeepOnServer
		}
	}
	if err := s.deps.Store.UpdateAccount(acc, req.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	acc, _ = s.deps.Store.GetAccount(id)
	writeJSON(w, 200, acc)
}

func orStr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func (s *Server) adminDeleteAccount(w http.ResponseWriter, r *http.Request, id int64) {
	if err := s.deps.Store.DeleteAccount(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminFetchNow(w http.ResponseWriter, r *http.Request, id int64) {
	acc, err := s.deps.Store.GetAccount(id)
	if err != nil || acc.Kind != "remote" {
		writeErr(w, 404, "no such remote account")
		return
	}
	go func() {
		f := fetch.New(fetch.Deps{Store: s.deps.Store, Engine: s.deps.Engine, DB: s.deps.Store.DB})
		f.FetchAccount(acc)
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "hint": "fetch started in background"})
}

// ---------------------------------------------------------------------------
// remote test

func (s *Server) adminTestRemote(w http.ResponseWriter, r *http.Request) {
	var req accountReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	acc := &mailstore.Account{
		RemoteProto: req.RemoteProto, RemoteHost: req.RemoteHost, RemotePort: req.RemotePort,
		RemoteTLS: req.RemoteTLS, RemoteUser: req.RemoteUser, RemotePass: req.RemotePass,
		RemoteFolder: orStr(req.RemoteFolder, "INBOX"),
	}
	f := fetch.New(fetch.Deps{Store: s.deps.Store, Engine: s.deps.Engine, DB: s.deps.Store.DB})
	res := f.TestConnection(acc)
	writeJSON(w, 200, res)
}

// ---------------------------------------------------------------------------
// folders

func (s *Server) adminListFolders(w http.ResponseWriter, r *http.Request, accID int64) {
	folders, err := s.deps.Store.ListFolders(accID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, folders)
}

func (s *Server) adminCreateFolder(w http.ResponseWriter, r *http.Request, accID int64) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, 400, "name required")
		return
	}
	id, err := s.deps.Store.EnsureFolder(accID, strings.TrimSpace(req.Name), "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) adminRenameFolder(w http.ResponseWriter, r *http.Request, accID, folderID int64) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if err := s.deps.Store.RenameFolder(accID, folderID, strings.TrimSpace(req.Name)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) adminDeleteFolder(w http.ResponseWriter, r *http.Request, accID, folderID int64) {
	if err := s.deps.Store.DeleteFolder(accID, folderID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// filters

func (s *Server) adminListFilters(w http.ResponseWriter, r *http.Request, accID int64) {
	list, err := s.deps.Store.ListFilters(accID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func filterFromReq(r *http.Request, accID int64) (*mailstore.Filter, error) {
	var req mailstore.Filter
	if err := readJSON(r, &req); err != nil {
		return nil, err
	}
	req.AccountID = accID
	return &req, nil
}

func (s *Server) adminCreateFilter(w http.ResponseWriter, r *http.Request, accID int64) {
	f, err := filterFromReq(r, accID)
	if err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if f.Name == "" || f.CondField == "" || f.CondOp == "" || f.Action == "" {
		writeErr(w, 400, "name, cond_field, cond_op and action are required")
		return
	}
	out, err := s.deps.Store.CreateFilter(f)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) adminUpdateFilter(w http.ResponseWriter, r *http.Request, accID, fID int64) {
	existing, err := s.deps.Store.GetFilter(accID, fID)
	if err != nil || existing == nil {
		writeErr(w, 404, "no such filter")
		return
	}
	var req mailstore.Filter
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	existing.Name = orStr(req.Name, existing.Name)
	existing.Enabled = req.Enabled
	existing.Position = req.Position
	existing.CondField = orStr(req.CondField, existing.CondField)
	existing.CondOp = orStr(req.CondOp, existing.CondOp)
	existing.CondValue = orStr(req.CondValue, existing.CondValue)
	existing.Action = orStr(req.Action, existing.Action)
	existing.ActionArg = req.ActionArg
	if err := s.deps.Store.UpdateFilter(existing); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, existing)
}

func (s *Server) adminDeleteFilter(w http.ResponseWriter, r *http.Request, accID, fID int64) {
	if err := s.deps.Store.DeleteFilter(accID, fID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// queue + settings

func (s *Server) adminQueue(w http.ResponseWriter, r *http.Request) {
	rows, err := s.deps.Store.DB.Query(`SELECT id,from_addr,recipients,status,attempts,next_try_at,last_error,created_at
		FROM send_queue ORDER BY id DESC LIMIT 100`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type qItem struct {
		ID         int64  `json:"id"`
		From       string `json:"from"`
		Recipients string `json:"recipients"`
		Status     string `json:"status"`
		Attempts   int    `json:"attempts"`
		NextTryAt  int64  `json:"next_try_at"`
		LastError  string `json:"last_error"`
		CreatedAt  int64  `json:"created_at"`
	}
	var out []qItem = []qItem{}
	for rows.Next() {
		var it qItem
		if rows.Scan(&it.ID, &it.From, &it.Recipients, &it.Status, &it.Attempts, &it.NextTryAt, &it.LastError, &it.CreatedAt) == nil {
			out = append(out, it)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.State.Config()
	if cfg == nil {
		writeErr(w, 400, "not installed")
		return
	}
	out := map[string]any{
		"primary_domain": cfg.PrimaryDomain,
		"hostname":       cfg.Hostname,
		"web_port":       cfg.WebPort,
		"smtp_port":      cfg.SMTPPort,
		"pop3_port":      cfg.POP3Port,
		"imap_port":      cfg.IMAPPort,
		"relay_host":     cfg.RelayHost,
		"relay_port":     cfg.RelayPort,
		"relay_user":     cfg.RelayUser,
		"relay_pass":     cfg.RelayPass,
	}
	if s.deps.Ports != nil {
		wp, sp, pp, ip := s.deps.Ports.CurrentPorts()
		out["effective_web_port"] = wp
		out["effective_smtp_port"] = sp
		out["effective_pop3_port"] = pp
		out["effective_imap_port"] = ip
	}
	writeJSON(w, 200, out)
}

func validPort(p int) bool { return p > 0 && p <= 65535 }

// containsErr reports whether any error message mentions the given service
// name (errors are formatted as "<service>: ...").
func containsErr(errs []string, name string) (bool, bool) {
	for _, e := range errs {
		if strings.HasPrefix(e, name+":") {
			return true, true
		}
	}
	return false, false
}

func (s *Server) adminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.deps.State.Config()
	if cfg == nil {
		writeErr(w, 400, "not installed")
		return
	}
	var req map[string]any
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if v, ok := req["primary_domain"].(string); ok && v != "" {
		cfg.PrimaryDomain = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := req["hostname"].(string); ok && v != "" {
		cfg.Hostname = v
	}
	if v, ok := req["relay_host"].(string); ok {
		cfg.RelayHost = v
	}
	if v, ok := req["relay_port"].(float64); ok {
		cfg.RelayPort = int(v)
	}
	if v, ok := req["relay_user"].(string); ok {
		cfg.RelayUser = v
	}
	if v, ok := req["relay_pass"].(string); ok {
		cfg.RelayPass = v
	}

	// Port changes: validate first, persist, then rebind listeners live.
	changedPorts := map[string]int{}
	for key, cfgField := range map[string]*int{
		"web_port":  &cfg.WebPort,
		"smtp_port": &cfg.SMTPPort,
		"pop3_port": &cfg.POP3Port,
		"imap_port": &cfg.IMAPPort,
	} {
		if v, ok := req[key].(float64); ok {
			p := int(v)
			if p != 0 && !validPort(p) {
				writeErr(w, 400, key+" must be 1-65535")
				return
			}
			if p > 0 && p != *cfgField {
				*cfgField = p
				changedPorts[key] = p
			}
		}
	}

	if err := s.deps.State.SetConfig(cfg); err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	resp := map[string]any{"ok": true, "saved": changedPorts}
	if len(changedPorts) == 0 || s.deps.Ports == nil {
		if len(changedPorts) > 0 {
			resp["hint"] = "restart to apply port changes"
		}
		writeJSON(w, 200, resp)
		return
	}

	// Live rebind for the mail protocols.
	applied, errs := s.deps.Ports.ApplyProtocolPorts(
		changedPorts["smtp_port"], changedPorts["pop3_port"], changedPorts["imap_port"])
	if len(applied) > 0 {
		resp["applied"] = applied
	}
	if len(errs) > 0 {
		resp["errors"] = errs
		// Revert config entries whose rebind failed so a restart does not
		// try to grab a port that is unavailable.
		_, liveSMTP, livePOP3, liveIMAP := s.deps.Ports.CurrentPorts()
		if _, failed := containsErr(errs, "smtp"); failed {
			cfg.SMTPPort = liveSMTP
		}
		if _, failed := containsErr(errs, "pop3"); failed {
			cfg.POP3Port = livePOP3
		}
		if _, failed := containsErr(errs, "imap"); failed {
			cfg.IMAPPort = liveIMAP
		}
		_ = s.deps.State.SetConfig(cfg)
	}

	// Web port: bind a new listener and publish it to the serving loop
	// BEFORE closing the old one, so the swap is never mistaken for a crash.
	if p, ok := changedPorts["web_port"]; ok && s.deps.Ports != nil {
		if ln, err := s.deps.Ports.BindNewWebListener(p); err != nil {
			resp["errors"] = append(errs, err.Error())
		} else if s.deps.Ports.CurrentWebListener() != nil && ln != nil {
			s.setWebSwap(ln)
			s.deps.Ports.ClosePreviousWebListener()
			resp["web_new_port"] = p
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) adminChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil || len(req.New) < 6 {
		writeErr(w, 400, "new password must be at least 6 chars")
		return
	}
	cfg := s.deps.State.Config()
	if err := bcryptCompare(cfg.AdminHash, req.Old); err != nil {
		writeErr(w, 401, "wrong old password")
		return
	}
	h, err := bcryptHash(req.New)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cfg.AdminHash = h
	if err := s.deps.State.SetConfig(cfg); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// factory reset (two-step, destructive)

// adminResetPrepare returns a one-time confirmation token plus a summary of
// what a reset would delete.
func (s *Server) adminResetPrepare(w http.ResponseWriter, r *http.Request) {
	var accounts, messages, queue int64
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accounts)
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages)
	_ = s.deps.Store.DB.QueryRow(`SELECT COUNT(*) FROM send_queue`).Scan(&queue)
	s.resetMu.Lock()
	s.resetToken = randToken()
	s.resetExp = time.Now().Add(5 * time.Minute)
	token := s.resetToken
	s.resetMu.Unlock()
	writeJSON(w, 200, map[string]any{
		"token": token, "accounts": accounts, "messages": messages, "queue": queue,
	})
}

type resetConfirmReq struct {
	Token  string `json:"token"`
	Phrase string `json:"phrase"`
}

// adminResetConfirm performs the wipe. Requires the one-time token from
// /api/reset/prepare and the exact phrase "RESET".
func (s *Server) adminResetConfirm(w http.ResponseWriter, r *http.Request) {
	var req resetConfirmReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	s.resetMu.Lock()
	valid := s.resetToken != "" && req.Token == s.resetToken && time.Now().Before(s.resetExp)
	s.resetToken, s.resetExp = "", time.Time{} // one-time use
	s.resetMu.Unlock()
	if !valid {
		writeErr(w, 400, "确认令牌无效或已过期，请重新发起重置")
		return
	}
	if req.Phrase != "RESET" {
		writeErr(w, 400, "确认短语不正确")
		return
	}
	if err := s.doFullReset(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// doFullReset wipes every table, all message blobs, the outbound queue files
// and the installation config, then clears all sessions. The protocol
// servers keep running; the web UI falls back to the installation wizard.
func (s *Server) doFullReset() error {
	if err := db.Reset(s.deps.Store.DB); err != nil {
		return err
	}
	if err := s.deps.Store.WipeBlobs(s.deps.State.DataDir()); err != nil {
		return err
	}
	if err := s.deps.State.FactoryReset(); err != nil {
		return err
	}
	s.mailMu.Lock()
	s.mailSess = map[string]int64{}
	s.mailMu.Unlock()
	return nil
}
