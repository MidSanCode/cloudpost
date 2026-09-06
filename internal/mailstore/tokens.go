package mailstore

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// API access tokens let external programs call the mailbox REST API
// (/api/mail/*) without an interactive webmail session. The plaintext token
// is shown exactly once at creation; only its SHA-256 hash is persisted, so
// a leaked database does not leak usable credentials.

const apiTokenPrefix = "cpat_"

// APIToken is one mailbox access token (metadata only — never the secret).
type APIToken struct {
	ID         int64  `json:"id"`
	AccountID  int64  `json:"account_id"`
	Label      string `json:"label"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Revoked    bool   `json:"revoked"`
}

func hashAPIToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// CreateAPIToken generates a new token for the account and returns it
// together with the one-time plaintext secret.
func (s *Store) CreateAPIToken(accountID int64, label string) (*APIToken, string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	plain := apiTokenPrefix + hex.EncodeToString(b)
	t := &APIToken{AccountID: accountID, Label: strings.TrimSpace(label), CreatedAt: time.Now().Unix()}
	res, err := s.DB.Exec(`INSERT INTO api_tokens(account_id,token_hash,label,created_at) VALUES(?,?,?,?)`,
		accountID, hashAPIToken(plain), t.Label, t.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	t.ID, _ = res.LastInsertId()
	return t, plain, nil
}

// ListAPITokens returns every token of an account (metadata only).
func (s *Store) ListAPITokens(accountID int64) ([]*APIToken, error) {
	rows, err := s.DB.Query(`SELECT id,account_id,label,created_at,last_used_at,revoked
		FROM api_tokens WHERE account_id=? ORDER BY id DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t := &APIToken{}
		var revoked int
		if err := rows.Scan(&t.ID, &t.AccountID, &t.Label, &t.CreatedAt, &t.LastUsedAt, &revoked); err != nil {
			return nil, err
		}
		t.Revoked = revoked != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken disables a token; revoked tokens stop working immediately.
func (s *Store) RevokeAPIToken(accountID, tokenID int64) error {
	res, err := s.DB.Exec(`UPDATE api_tokens SET revoked=1 WHERE id=? AND account_id=?`, tokenID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no such token")
	}
	return nil
}

// AccountByAPIToken resolves a plaintext bearer token to its mailbox account.
// Returns (nil, nil, nil) for unknown or revoked tokens. last_used_at is
// refreshed at most once per minute to avoid write amplification.
func (s *Store) AccountByAPIToken(plain string) (*Account, *APIToken, error) {
	plain = strings.TrimSpace(plain)
	if !strings.HasPrefix(plain, apiTokenPrefix) {
		return nil, nil, nil
	}
	var t APIToken
	var revoked int
	err := s.DB.QueryRow(`SELECT id,account_id,label,created_at,last_used_at,revoked
		FROM api_tokens WHERE token_hash=?`, hashAPIToken(plain)).
		Scan(&t.ID, &t.AccountID, &t.Label, &t.CreatedAt, &t.LastUsedAt, &revoked)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if revoked != 0 {
		return nil, nil, nil
	}
	acc, err := s.GetAccount(t.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if now := time.Now().Unix(); t.LastUsedAt == 0 || now-t.LastUsedAt >= 60 {
		_, _ = s.DB.Exec(`UPDATE api_tokens SET last_used_at=? WHERE id=?`, now, t.ID)
	}
	return acc, &t, nil
}
