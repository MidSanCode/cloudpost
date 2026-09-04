package web

import (
	"net/http"
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
	resp := map[string]any{"message": msg, "text": info.TextBody, "html": info.HTMLBody}
	writeJSON(w, 200, resp)
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
