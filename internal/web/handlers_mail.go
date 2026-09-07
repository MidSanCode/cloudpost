package web

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cloudpost/internal/mailstore"
)

// handleMail routes data-plane (mailbox user) API calls.
func (s *Server) handleMail(w http.ResponseWriter, r *http.Request) {
	acc := s.mailAccount(r)
	if acc == nil {
		writeErr(w, http.StatusUnauthorized, "mailbox login required")
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/mail")
	m := r.Method
	switch {
	case p == "/me" && m == http.MethodGet:
		writeJSON(w, 200, acc)
	case p == "/folders" && m == http.MethodGet:
		s.mailFolders(w, acc)
	case p == "/compose" && m == http.MethodPost:
		s.mailCompose(w, r, acc)
	case p == "/scheduled" && m == http.MethodGet:
		s.mailListScheduled(w, acc)
	case strings.HasPrefix(p, "/scheduled/") && m == http.MethodDelete:
		id, err := strconv.ParseInt(strings.TrimPrefix(p, "/scheduled/"), 10, 64)
		if err != nil {
			writeErr(w, 400, "bad id")
			return
		}
		if err := s.deps.Store.CancelScheduled(acc.ID, id); err != nil {
			writeErr(w, 404, "no such scheduled message")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case strings.HasPrefix(p, "/folders/"):
		segs := strings.Split(strings.TrimPrefix(p, "/folders/"), "/")
		folderID, err := strconv.ParseInt(segs[0], 10, 64)
		if err != nil {
			writeErr(w, 400, "bad folder id")
			return
		}
		switch {
		case len(segs) == 1 && m == http.MethodGet:
			s.mailListMessages(w, r, acc, folderID)
		case len(segs) == 2 && segs[1] == "messages" && m == http.MethodGet:
			s.mailListMessages(w, r, acc, folderID)
		case len(segs) == 2 && segs[1] == "messages" && m == http.MethodPost:
			s.mailSearch(w, r, acc, folderID)
		case len(segs) == 5 && segs[1] == "messages" && segs[3] == "attachments":
			s.handleMailAttachment(w, r, acc, folderID, strings.Join(segs[2:], "/"))
		case len(segs) == 5 && segs[1] == "messages" && segs[3] == "cid":
			s.handleMailCID(w, r, acc, folderID, strings.Join(segs[2:], "/"))
		case len(segs) == 4 && segs[1] == "messages" && segs[3] == "raw":
			s.mailMessage(w, r, acc, folderID, segs[2]+"/raw", m)
		case len(segs) == 3 && segs[1] == "messages":
			s.mailMessage(w, r, acc, folderID, segs[2], m)
		case len(segs) == 2 && segs[1] == "expunge" && m == http.MethodPost:
			n, err := s.deps.Store.ExpungeFolder(acc.ID, folderID)
			if err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, map[string]any{"expunged": n})
		case len(segs) == 2 && segs[1] == "readall" && m == http.MethodPost:
			s.mailReadAll(w, acc, folderID)
		default:
			writeErr(w, 404, "not found")
		}
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *Server) mailFolders(w http.ResponseWriter, acc *mailstore.Account) {
	folders, err := s.deps.Store.ListFolders(acc.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, folders)
}

func (s *Server) mailListMessages(w http.ResponseWriter, r *http.Request, acc *mailstore.Account, folderID int64) {
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	msgs, total, err := s.deps.Store.ListMessages(acc.ID, folderID, off, limit, false)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs, "total": total})
}

func (s *Server) mailSearch(w http.ResponseWriter, r *http.Request, acc *mailstore.Account, folderID int64) {
	var req struct {
		Field string `json:"field"`
		Query string `json:"query"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	msgs, err := s.deps.Store.SearchMessages(acc.ID, folderID, req.Field, req.Query, 100)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs})
}

// mailMessage handles /folders/{id}/messages/{action-or-id}
func (s *Server) mailMessage(w http.ResponseWriter, r *http.Request, acc *mailstore.Account, folderID int64, last, method string) {
	// last may be a message id (numeric) or an action on a numeric id: {id}/{action}
	parts := strings.Split(last, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "bad message id")
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if action == "raw" {
		raw, _, err := s.deps.Store.GetMessageRaw(acc.ID, id)
		if err != nil {
			writeErr(w, 404, "no such message")
			return
		}
		w.Header().Set("Content-Type", "message/rfc822")
		_, _ = w.Write(raw)
		return
	}
	if action == "flags" && method == http.MethodPost {
		var req struct {
			Mode  string   `json:"mode"` // set|add|remove
			Flags []string `json:"flags"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		if err := s.deps.Store.SetFlags(acc.ID, []int64{id}, req.Mode, req.Flags); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if action == "move" && method == http.MethodPost {
		var req struct {
			Folder   string `json:"folder"`
			FolderID int64  `json:"folder_id"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		if req.Folder != "" {
			if err := s.deps.Store.MoveToFolderName(acc.ID, id, req.Folder); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		} else if req.FolderID > 0 {
			if err := s.deps.Store.MoveMessages(acc.ID, req.FolderID, []int64{id}); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		} else {
			writeErr(w, 400, "folder or folder_id required")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if action == "delete" && method == http.MethodPost {
		if err := s.deps.Store.SetFlags(acc.ID, []int64{id}, "add", []string{`\Deleted`}); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := s.deps.Store.DeleteMessages(acc.ID, []int64{id}); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if action != "" {
		writeErr(w, 404, "unknown action")
		return
	}
	msg, err := s.deps.Store.GetMessage(acc.ID, id)
	if err != nil {
		writeErr(w, 404, "no such message")
		return
	}
	raw, _, err := s.deps.Store.GetMessageRaw(acc.ID, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.deps.Store.MarkSeen(acc.ID, id)
	info := parseForView(raw)
	resp := map[string]any{"message": msg, "text": info.TextBody}
	// HTML mails are sanitized server-side and rendered in a sandboxed
	// iframe with a blocking CSP meta; remote images stay blocked until the
	// user explicitly allows them (?allow_remote=1 re-sanitizes permissive).
	if info.HTMLBody != "" {
		atts, _ := s.deps.Store.ListAttachments(acc.ID, id)
		cidData := map[string]string{}
		for _, at := range atts {
			if at.CID == "" || !strings.HasPrefix(at.ContentType, "image/") || at.Size > 2<<20 {
				continue
			}
			data, _, _, err := s.deps.Store.ExtractAttachment(acc.ID, id, at.Part)
			if err != nil || len(data) == 0 || len(data) > 2<<20 {
				continue
			}
			cidData[at.CID] = "data:" + at.ContentType + ";base64," + base64.StdEncoding.EncodeToString(data)
		}
		allowRemote := r.URL.Query().Get("allow_remote") == "1"
		sanitized, hasRemote := sanitizeMailHTML(info.HTMLBody, allowRemote, cidData)
		resp["html"] = sanitized
		resp["has_remote_content"] = hasRemote
		resp["remote_allowed"] = allowRemote
	}
	// Attachments metadata only; content is fetched on demand.
	if len(info.Attachments) > 0 {
		resp["attachments"] = info.Attachments
	}
	writeJSON(w, 200, resp)
}

// mailListScheduled lists this account's pending delayed sends.
func (s *Server) mailListScheduled(w http.ResponseWriter, acc *mailstore.Account) {
	list, err := s.deps.Store.ListScheduled(acc.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*mailstore.ScheduledMessage{}
	}
	writeJSON(w, 200, list)
}

// handleMailAttachment serves one attachment with a hard Content-Disposition
// attachment (never inline), sanitized filename, and a size cap.
func (s *Server) handleMailAttachment(w http.ResponseWriter, r *http.Request, acc *mailstore.Account, folderID int64, rest string) {
	// rest = {msgId}/attachments/{part}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[1] != "attachments" {
		writeErr(w, 404, "not found")
		return
	}
	msgID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "bad message id")
		return
	}
	part, err := strconv.Atoi(parts[2])
	if err != nil || part < 0 {
		writeErr(w, 400, "bad attachment index")
		return
	}
	data, filename, _, err := s.deps.Store.ExtractAttachment(acc.ID, msgID, part)
	if err != nil {
		writeErr(w, 404, "no such attachment")
		return
	}
	if len(data) > 50<<20 {
		writeErr(w, 413, "attachment too large")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// handleMailCID serves a small inline image referenced by Content-ID, used
// when the user allows remote/inline content on a message.
func (s *Server) handleMailCID(w http.ResponseWriter, r *http.Request, acc *mailstore.Account, folderID int64, rest string) {
	// rest = {msgId}/cid/{urlencoded-cid}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[1] != "cid" {
		writeErr(w, 404, "not found")
		return
	}
	msgID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "bad message id")
		return
	}
	cid, err := url.QueryUnescape(parts[2])
	if err != nil {
		writeErr(w, 400, "bad cid")
		return
	}
	atts, err := s.deps.Store.ListAttachments(acc.ID, msgID)
	if err != nil {
		writeErr(w, 404, "no such message")
		return
	}
	for _, at := range atts {
		if at.CID != cid || !strings.HasPrefix(at.ContentType, "image/") || at.Size > 2<<20 {
			continue
		}
		data, _, _, err := s.deps.Store.ExtractAttachment(acc.ID, msgID, at.Part)
		if err != nil {
			break
		}
		w.Header().Set("Content-Type", at.ContentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(data)
		return
	}
	writeErr(w, 404, "no such image")
}

func (s *Server) mailReadAll(w http.ResponseWriter, acc *mailstore.Account, folderID int64) {
	msgs, _, err := s.deps.Store.ListMessages(acc.ID, folderID, 0, 100000, false)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var ids []int64
	for _, m := range msgs {
		if !hasSeenFlag(m.Flags) {
			ids = append(ids, m.ID)
		}
	}
	if err := s.deps.Store.SetFlags(acc.ID, ids, "add", []string{`\Seen`}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"marked": len(ids)})
}

func hasSeenFlag(flags []string) bool {
	for _, f := range flags {
		if f == `\Seen` {
			return true
		}
	}
	return false
}
