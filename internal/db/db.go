// Package db manages the SQLite database for cloudpost.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite database at dir/cloudpost.db.
func Open(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "cloudpost.db")
	// _pragma busy_timeout to survive concurrent writers; WAL for reader/writer concurrency.
	d, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", dsn))
	if err != nil {
		return nil, err
	}
	// modernc sqlite has some quirks with many connections; a small pool is fine.
	d.SetMaxOpenConns(1)
	if err := migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func migrate(d *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	role TEXT NOT NULL DEFAULT 'user',
	mailbox_address TEXT DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT NOT NULL CHECK(kind IN ('local','remote')),
	address TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	password_hash TEXT DEFAULT '',
	remote_proto TEXT DEFAULT '',
	remote_host TEXT DEFAULT '',
	remote_port INTEGER DEFAULT 0,
	remote_tls TEXT DEFAULT 'ssl',
	remote_user TEXT DEFAULT '',
	remote_pass TEXT DEFAULT '',
	remote_folder TEXT DEFAULT 'INBOX',
	remote_target TEXT DEFAULT '',
	fetch_enabled INTEGER NOT NULL DEFAULT 1,
	fetch_interval_min INTEGER NOT NULL DEFAULT 15,
	fetch_keep_on_server INTEGER NOT NULL DEFAULT 1,
	last_fetch_at INTEGER NOT NULL DEFAULT 0,
	last_fetch_ok INTEGER NOT NULL DEFAULT 1,
	last_error TEXT DEFAULT '',
	owner_user_id INTEGER,
	created_at INTEGER NOT NULL,
	UNIQUE(address, kind)
);
CREATE TABLE IF NOT EXISTS folders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	special TEXT NOT NULL DEFAULT '',
	subscribed INTEGER NOT NULL DEFAULT 1,
	uid_validity INTEGER NOT NULL,
	next_uid INTEGER NOT NULL DEFAULT 1,
	UNIQUE(account_id, name)
);
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
	uid INTEGER NOT NULL,
	message_id TEXT DEFAULT '',
	in_reply_to TEXT DEFAULT '',
	from_name TEXT DEFAULT '',
	from_addr TEXT DEFAULT '',
	to_addrs TEXT DEFAULT '[]',
	cc_addrs TEXT DEFAULT '[]',
	subject TEXT DEFAULT '',
	sent_at INTEGER NOT NULL DEFAULT 0,
	internal_at INTEGER NOT NULL DEFAULT 0,
	size INTEGER NOT NULL DEFAULT 0,
	flags TEXT NOT NULL DEFAULT '',
	snippet TEXT DEFAULT '',
	has_attach INTEGER NOT NULL DEFAULT 0,
	raw_path TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE(folder_id, uid)
);
CREATE INDEX IF NOT EXISTS idx_messages_folder ON messages(folder_id, uid DESC);
CREATE INDEX IF NOT EXISTS idx_messages_acct ON messages(account_id);
CREATE INDEX IF NOT EXISTS idx_messages_mid ON messages(message_id);
CREATE TABLE IF NOT EXISTS filters (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	position INTEGER NOT NULL DEFAULT 0,
	cond_field TEXT NOT NULL,
	cond_op TEXT NOT NULL,
	cond_value TEXT NOT NULL,
	action TEXT NOT NULL,
	action_arg TEXT DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS send_queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER,
	from_addr TEXT NOT NULL,
	recipients TEXT NOT NULL,
	data_path TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_try_at INTEGER NOT NULL DEFAULT 0,
	last_error TEXT DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS remote_state (
	account_id INTEGER NOT NULL,
	k TEXT NOT NULL,
	v TEXT NOT NULL,
	PRIMARY KEY(account_id, k)
);
CREATE TABLE IF NOT EXISTS api_tokens (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	token_hash TEXT NOT NULL UNIQUE,
	label TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	last_used_at INTEGER NOT NULL DEFAULT 0,
	revoked INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_account ON api_tokens(account_id);
`
	_, err := d.Exec(schema)
	// Tolerate upgrades from older DBs (column added later).
	_, _ = d.Exec(`ALTER TABLE accounts ADD COLUMN remote_target TEXT DEFAULT ''`)
	return err
}

// Reset wipes every row from all tables, keeping the schema intact. Used by
// the factory-reset flow; the schema itself is reused for the next install.
func Reset(d *sql.DB) error {
	tables := []string{
		"messages", "folders", "filters", "send_queue", "remote_state", "api_tokens",
		"accounts", "sessions", "users", "settings",
	}
	for _, t := range tables {
		if _, err := d.Exec("DELETE FROM " + t); err != nil {
			return fmt.Errorf("clear %s: %w", t, err)
		}
	}
	// Reclaim space; failure is non-fatal.
	_, _ = d.Exec("VACUUM")
	return nil
}
