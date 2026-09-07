// Package filter applies per-account incoming-mail rules.
package filter

import (
	"regexp"
	"strings"
	"time"

	"cloudpost/internal/mailstore"
)

// Engine evaluates filter rules for an account.
type Engine struct{ Store *mailstore.Store }

// Apply runs enabled filters for account over a freshly delivered message.
// raw is the message blob; msgID its row id.
func (e *Engine) Apply(accountID, msgID int64, folderID int64, subject, from, to, body string) {
	filters, err := e.Store.ListFilters(accountID)
	if err != nil {
		return
	}
	for _, f := range filters {
		if !f.Enabled {
			continue
		}
		hay := ""
		switch f.CondField {
		case "from":
			hay = from
		case "to":
			hay = to
		case "subject":
			hay = subject
		case "body":
			hay = subject + "\n" + body
		default:
			hay = from + "\n" + to + "\n" + subject + "\n" + body
		}
		if !match(f.CondOp, f.CondValue, hay) {
			continue
		}
		switch f.Action {
		case "move":
			if f.ActionArg != "" {
				_ = e.Store.MoveToFolderName(accountID, msgID, f.ActionArg)
			}
		case "move_account":
			// Auto-move: deliver the message into another LOCAL mailbox's
			// INBOX and remove it here. Typical use: remote-fetched mail
			// matching a condition (e.g. To contains @partner.example)
			// lands in a dedicated local mailbox.
			if f.ActionArg != "" {
				e.moveAccount(accountID, msgID, f.ActionArg)
			}
		case "markread":
			_ = e.Store.SetFlags(accountID, []int64{msgID}, "add", []string{`\Seen`})
		case "star":
			_ = e.Store.SetFlags(accountID, []int64{msgID}, "add", []string{`\Flagged`})
		case "trash":
			_ = e.Store.MoveToFolderName(accountID, msgID, "Trash")
		case "discard":
			_ = e.Store.DeleteMessages(accountID, []int64{msgID})
		}
		// First matching rule wins.
		return
	}
}

// moveAccount copies the raw message into the target local account's INBOX
// and deletes the original. Failures keep the message where it is.
func (e *Engine) moveAccount(accountID, msgID int64, targetAddr string) {
	t, err := e.Store.GetAccountByAddressAny(strings.ToLower(strings.TrimSpace(targetAddr)))
	if err != nil {
		return
	}
	inbox, err := e.Store.FolderByName(t.ID, "INBOX")
	if err != nil {
		return
	}
	raw, _, err := e.Store.GetMessageRaw(accountID, msgID)
	if err != nil {
		return
	}
	if _, _, err := e.Store.Deliver(t.ID, inbox.ID, raw, nil); err != nil {
		return
	}
	_ = e.Store.DeleteMessages(accountID, []int64{msgID})
}

func match(op, value, hay string) bool {
	switch op {
	case "equals":
		return strings.EqualFold(strings.TrimSpace(hay), value)
	case "starts":
		return strings.HasPrefix(strings.ToLower(hay), strings.ToLower(value))
	case "ends":
		return strings.HasSuffix(strings.ToLower(hay), strings.ToLower(value))
	case "regex":
		rx, err := regexp.Compile(value)
		return err == nil && rx.MatchString(hay)
	default: // contains
		return strings.Contains(strings.ToLower(hay), strings.ToLower(value))
	}
}

var _ = time.Now
