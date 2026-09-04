package web

import (
	"fmt"
	"net/http"
	"strings"

	"cloudpost/internal/mailstore"
)

// parseForView re-parses a stored blob for the read view.
func parseForView(raw []byte) *mailstore.ParsedInfo {
	return mailstore.ParseHeaders(raw)
}

// mailCompose builds an RFC 5322 message and enqueues it for delivery
// (local recipients are delivered directly).
func (s *Server) mailCompose(w http.ResponseWriter, r *http.Request, acc *mailstore.Account) {
	var req struct {
		To      string `json:"to"`
		CC      string `json:"cc"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if strings.TrimSpace(req.To) == "" {
		writeErr(w, 400, "to is required")
		return
	}
	cfg := s.deps.State.Config()
	domain := ""
	if cfg != nil {
		domain = cfg.PrimaryDomain
	}
	from := acc.Address
	fromName := acc.DisplayName

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", fromName, from)
	fmt.Fprintf(&b, "To: %s\r\n", req.To)
	if req.CC != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", req.CC)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mimeWordEncode(req.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", rfc2822Now())
	fmt.Fprintf(&b, "Message-ID: <%d.%s@%s>\r\n", nowMillis(), randToken()[:12], domain)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	b.WriteString(b64Wrap(req.Body))

	raw := []byte(b.String())
	recips := parseRcpts(req.To, req.CC)

	// Local recipients deliver directly into their INBOX; the rest go to the queue.
	var local, remote []string
	for _, addr := range recips {
		if _, err := s.deps.Store.GetAccountByAddress(addr); err == nil {
			local = append(local, addr)
		} else {
			remote = append(remote, addr)
		}
	}
	for _, addr := range local {
		target, err := s.deps.Store.GetAccountByAddress(addr)
		if err != nil {
			continue
		}
		inbox, err := s.deps.Store.FolderByName(target.ID, "INBOX")
		if err != nil {
			continue
		}
		s.deps.Store.Deliver(target.ID, inbox.ID, raw, nil)
	}
	// Store a copy in the sender's Sent folder.
	if sent, err := s.deps.Store.FolderByName(acc.ID, "Sent"); err == nil {
		s.deps.Store.Deliver(acc.ID, sent.ID, raw, nil)
	}
	if len(remote) > 0 && s.deps.Sender != nil {
		if _, err := s.deps.Sender.Enqueue(from, remote, raw); err != nil {
			writeErr(w, 500, "enqueue: "+err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "local": local, "queued": remote})
}

func parseRcpts(parts ...string) []string {
	var out []string
	for _, p := range parts {
		for _, x := range strings.Split(p, ",") {
			x = strings.ToLower(strings.TrimSpace(x))
			if strings.Contains(x, "@") {
				out = append(out, x)
			}
		}
	}
	return out
}
