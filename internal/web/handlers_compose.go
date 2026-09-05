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

// sanitizeHeader strips CR/LF/NUL to prevent SMTP header injection via
// user-controlled header values (RFC 5322 forbids them mid-field).
func sanitizeHeader(s string) string {
	r := strings.NewReplacer("\r", "", "\n", "", "\x00", "")
	return r.Replace(s)
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
	req.To = sanitizeHeader(req.To)
	req.CC = sanitizeHeader(req.CC)
	req.Subject = sanitizeHeader(req.Subject)
	cfg := s.deps.State.Config()
	domain := ""
	if cfg != nil {
		domain = cfg.PrimaryDomain
	}
	from := acc.Address
	fromName := acc.DisplayName

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", sanitizeHeader(fromName), from)
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
			if strings.Contains(x, "@") && validAddr(x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// validAddr enforces a strict email character set so that header-injection
// remnants (spaces, colons, control chars) can never become recipients.
func validAddr(a string) bool {
	i := strings.LastIndexByte(a, '@')
	if i <= 0 || i == len(a)-1 {
		return false
	}
	local, domain := a[:i], a[i+1:]
	if strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	okChars := func(s, extra string) bool {
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case strings.ContainsRune(extra, r):
			default:
				return false
			}
		}
		return true
	}
	return okChars(local, "._%+=-") && okChars(domain, ".-")
}
