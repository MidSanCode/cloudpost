package mailstore

import (
	"encoding/json"
	"os"
	"time"
)

// Scheduled (delayed) outgoing messages. The full RFC 5322 source is written
// to disk under scheduled/ and only delivered when send_at passes; the row
// is deleted on send or cancel, so cancelling is a plain delete.

// ScheduledMessage is one pending delayed send.
type ScheduledMessage struct {
	ID         int64    `json:"id"`
	AccountID  int64    `json:"account_id"`
	From       string   `json:"from"`
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject"`
	SendAt     int64    `json:"send_at"`
	CreatedAt  int64    `json:"created_at"`
}

const scheduledCols = `id, account_id, from_addr, recipients, subject, send_at, created_at`

func scanScheduled(sc interface{ Scan(...any) error }) (*ScheduledMessage, error) {
	m := &ScheduledMessage{}
	var rcpts string
	if err := sc.Scan(&m.ID, &m.AccountID, &m.From, &rcpts, &m.Subject, &m.SendAt, &m.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(rcpts), &m.Recipients)
	return m, nil
}

// ScheduleSend persists a delayed message. Returns the row id and the blob
// path the caller must write the message source to.
func (s *Store) ScheduleSend(accountID int64, from string, rcpts []string, subject string, sendAt int64) (int64, string, error) {
	dir := s.DataDir() + "/scheduled"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, "", err
	}
	f, err := os.CreateTemp(dir, "msg-*.eml")
	if err != nil {
		return 0, "", err
	}
	path := f.Name()
	f.Close()
	rcptsJSON, _ := json.Marshal(rcpts)
	res, err := s.DB.Exec(`INSERT INTO scheduled(account_id,from_addr,recipients,subject,data_path,send_at,created_at)
		VALUES(?,?,?,?,?,?,?)`, accountID, from, string(rcptsJSON), subject, path, sendAt, time.Now().Unix())
	if err != nil {
		os.Remove(path)
		return 0, "", err
	}
	id, _ := res.LastInsertId()
	return id, path, nil
}

// ListScheduled returns pending delayed sends, soonest first. When accountID
// is 0 every account's messages are returned (admin view).
func (s *Store) ListScheduled(accountID int64) ([]*ScheduledMessage, error) {
	q := `SELECT ` + scheduledCols + ` FROM scheduled`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE account_id=?`
		args = append(args, accountID)
	}
	q += ` ORDER BY send_at`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ScheduledMessage
	for rows.Next() {
		m, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetScheduled loads one pending message with its blob path.
func (s *Store) GetScheduled(id int64) (*ScheduledMessage, string, error) {
	row := s.DB.QueryRow(`SELECT `+scheduledCols+`, data_path FROM scheduled WHERE id=?`, id)
	m := &ScheduledMessage{}
	var rcpts, path string
	if err := row.Scan(&m.ID, &m.AccountID, &m.From, &rcpts, &m.Subject, &m.SendAt, &m.CreatedAt, &path); err != nil {
		return nil, "", err
	}
	_ = json.Unmarshal([]byte(rcpts), &m.Recipients)
	return m, path, nil
}

// CancelScheduled removes a pending delayed send (row + blob). The message
// is never delivered afterwards.
func (s *Store) CancelScheduled(accountID, id int64) error {
	var path string
	if err := s.DB.QueryRow(`SELECT data_path FROM scheduled WHERE id=? AND account_id=?`, id, accountID).Scan(&path); err != nil {
		return err
	}
	if path != "" {
		_ = os.Remove(path)
	}
	_, err := s.DB.Exec(`DELETE FROM scheduled WHERE id=? AND account_id=?`, id, accountID)
	return err
}

// DueScheduled returns pending messages whose time has come.
func (s *Store) DueScheduled(now int64) ([]*ScheduledMessage, error) {
	rows, err := s.DB.Query(`SELECT `+scheduledCols+` FROM scheduled WHERE send_at<=? ORDER BY send_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ScheduledMessage
	for rows.Next() {
		m, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteScheduledRow removes the row + blob after the sender dispatched it.
func (s *Store) DeleteScheduledRow(id int64) {
	var path string
	if err := s.DB.QueryRow(`SELECT data_path FROM scheduled WHERE id=?`, id).Scan(&path); err == nil && path != "" {
		_ = os.Remove(path)
	}
	_, _ = s.DB.Exec(`DELETE FROM scheduled WHERE id=?`, id)
}

// DataDir returns the data directory backing this store (scheduled blobs).
func (s *Store) DataDir() string { return s.dataDir }
