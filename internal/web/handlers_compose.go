package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

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

// composeReq is the shared payload for immediate and delayed sends.
type composeReq struct {
	To      string `json:"to"`
	CC      string `json:"cc"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Attachments are inlined base64 (UI reads files via FileReader; REST
	// callers encode the same way). Each is capped, see maxAttachBytes.
	Attachments []composeAttachment `json:"attachments"`
	// Delay: either delay_min minutes from now, or an absolute unix ts.
	DelayMin int   `json:"delay_min"`
	SendAt   int64 `json:"send_at"`
}

type composeAttachment struct {
	Filename string `json:"filename"`
	Content  string `json:"content"` // base64
}

const (
	maxAttachments = 10
	maxAttachBytes = 20 << 20 // decoded size per attachment
	maxAttachTotal = 35 << 20 // decoded total
)

// renderSignature applies dynamic placeholders to the account signature.
func renderSignature(sig, from, addr, subject string) string {
	if strings.TrimSpace(sig) == "" {
		return ""
	}
	now := time.Now()
	r := strings.NewReplacer(
		"{{datetime}}", now.Format("2006-01-02 15:04"),
		"{{date}}", now.Format("2006-01-02"),
		"{{time}}", now.Format("15:04:05"),
		"{{from}}", from,
		"{{address}}", addr,
		"{{subject}}", subject,
	)
	return r.Replace(sig)
}

// buildMessage assembles the RFC 5322 source for a compose request,
// appending the account signature (with placeholders rendered) and
// building multipart/mixed when attachments are present.
func buildMessage(fromName, from, to, cc, subject, body, signature string, atts []composeAttachment) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", sanitizeHeader(fromName), from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	if cc != "" {
		fmt.Fprintf(&b, "Cc: %s\r\n", cc)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mimeWordEncode(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", rfc2822Now())
	fmt.Fprintf(&b, "Message-ID: <%d.%s@%s>\r\n", nowMillis(), randToken()[:12], fromDomainOf(from))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")

	// Signature is part of the body text (already placeholder-rendered).
	text := body
	if signature != "" {
		text = strings.TrimRight(body, "\r\n") + "\r\n\r\n-- \r\n" + signature
	}

	if len(atts) == 0 {
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n")
		b.WriteString("\r\n")
		b.WriteString(b64Wrap(text))
		return []byte(b.String()), nil
	}

	boundary := "cp_" + randHexBytes(16)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary)
	b.WriteString("\r\n")
	b.WriteString("This is a multi-part message in MIME format.\r\n")
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(b64Wrap(text))
	b.WriteString("\r\n")
	var total int64
	for _, a := range atts {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(a.Content))
		if err != nil {
			return nil, fmt.Errorf("attachment %q: bad base64", a.Filename)
		}
		total += int64(len(data))
		if len(data) > maxAttachBytes || total > maxAttachTotal {
			return nil, fmt.Errorf("attachment %q too large (max 20MB each, 35MB total)", a.Filename)
		}
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		fmt.Fprintf(&b, "Content-Type: application/octet-stream; name=\"%s\"\r\n", sanitizeHeader(a.Filename))
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"%s\"\r\n", sanitizeHeader(a.Filename))
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Wrap(string(data))) // b64Wrap splits any []byte content safely
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String()), nil
}

func randHexBytes(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func fromDomainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "localhost"
}

// deliverBuilt performs the local/remote split for an already-built message:
// local recipients are delivered (filters run), remote ones are enqueued,
// and a copy lands in the sender's Sent folder. Returns both lists.
func (s *Server) deliverBuilt(acc *mailstore.Account, recips []string, raw []byte) (local, remote []string, err error) {
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
	if sent, serr := s.deps.Store.FolderByName(acc.ID, "Sent"); serr == nil {
		s.deps.Store.Deliver(acc.ID, sent.ID, raw, nil)
	}
	if len(remote) > 0 && s.deps.Sender != nil {
		if _, eerr := s.deps.Sender.Enqueue(acc.Address, remote, raw); eerr != nil {
			return local, remote, eerr
		}
	}
	return local, remote, nil
}

// mailCompose handles POST /api/mail/compose: immediate or delayed send
// with optional attachments and account signature.
func (s *Server) mailCompose(w http.ResponseWriter, r *http.Request, acc *mailstore.Account) {
	var req composeReq
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
	recips := parseRcpts(req.To, req.CC)
	if len(recips) == 0 {
		writeErr(w, 400, "no valid recipients")
		return
	}
	sig := renderSignature(acc.Signature, acc.DisplayName, acc.Address, req.Subject)
	raw, err := buildMessage(acc.DisplayName, acc.Address, req.To, req.CC, req.Subject, req.Body, sig, req.Attachments)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	// Delayed send: persist for the scheduled runner; cancellable until due.
	sendAt := int64(0)
	if req.SendAt > 0 {
		sendAt = req.SendAt
	} else if req.DelayMin > 0 {
		sendAt = time.Now().Unix() + int64(req.DelayMin)*60
	}
	if sendAt > time.Now().Unix() {
		id, path, err := s.deps.Store.ScheduleSend(acc.ID, acc.Address, recips, req.Subject, sendAt)
		if err != nil {
			writeErr(w, 500, "schedule: "+err.Error())
			return
		}
		if werr := os.WriteFile(path, raw, 0o644); werr != nil {
			_ = s.deps.Store.CancelScheduled(acc.ID, id)
			writeErr(w, 500, "schedule: "+werr.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "scheduled": true, "id": id, "send_at": sendAt})
		return
	}

	local, remote, err := s.deliverBuilt(acc, recips, raw)
	if err != nil {
		writeErr(w, 500, "enqueue: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "local": local, "queued": remote})
}
