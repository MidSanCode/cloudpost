// Package mailstore implements SQLite-backed account, folder and message storage
// with raw RFC-5322 blobs on disk.
package mailstore

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Account mirrors the accounts table.
type Account struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"` // local | remote
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	HasPassword bool   `json:"has_password"`
	RemoteProto string `json:"remote_proto,omitempty"` // imap | pop3
	RemoteHost  string `json:"remote_host,omitempty"`
	RemotePort  int    `json:"remote_port,omitempty"`
	RemoteTLS   string `json:"remote_tls,omitempty"` // ssl | starttls | none
	RemoteUser  string `json:"remote_user,omitempty"`
	// RemotePass is the remote account credential. Never serialized to
	// clients; the API exposes only HasRemotePass.
	RemotePass        string `json:"-"`
	HasRemotePass     bool   `json:"has_remote_pass,omitempty"`
	RemoteFolder      string `json:"remote_folder,omitempty"`
	RemoteTarget      string `json:"remote_target,omitempty"`
	FetchEnabled      bool   `json:"fetch_enabled"`
	FetchIntervalMin  int    `json:"fetch_interval_min"`
	FetchKeepOnServer bool   `json:"fetch_keep_on_server"`
	// Signature is appended to webmail/API-composed mail (SMTP clients
	// build their own MIME, so their messages are left untouched).
	// Supports placeholders {{date}} {{time}} {{datetime}} {{from}} {{address}}.
	Signature   string `json:"signature,omitempty"`
	LastFetchAt int64  `json:"last_fetch_at"`
	LastFetchOK bool   `json:"last_fetch_ok"`
	LastError   string `json:"last_error"`
	CreatedAt   int64  `json:"created_at"`
}

// Folder mirrors the folders table.
type Folder struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"account_id"`
	Name        string `json:"name"`
	Special     string `json:"special"`
	Subscribed  bool   `json:"subscribed"`
	UIDValidity uint32 `json:"uid_validity"`
	NextUID     uint32 `json:"next_uid"`
	Count       int64  `json:"count"`
	Unseen      int64  `json:"unseen"`
}

// Message is a message row (no raw content).
type Message struct {
	ID         int64    `json:"id"`
	AccountID  int64    `json:"account_id"`
	FolderID   int64    `json:"folder_id"`
	UID        uint32   `json:"uid"`
	MessageID  string   `json:"message_id"`
	InReplyTo  string   `json:"in_reply_to"`
	FromName   string   `json:"from_name"`
	FromAddr   string   `json:"from_addr"`
	ToAddrs    []string `json:"to_addrs"`
	CCAddrs    []string `json:"cc_addrs"`
	Subject    string   `json:"subject"`
	SentAt     int64    `json:"sent_at"`
	InternalAt int64    `json:"internal_at"`
	Size       int64    `json:"size"`
	Flags      []string `json:"flags"`
	Snippet    string   `json:"snippet"`
	HasAttach  bool     `json:"has_attach"`
	RawPath    string   `json:"-"`
}

// Filter mirrors the filters table.
type Filter struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Position  int    `json:"position"`
	CondField string `json:"cond_field"`
	CondOp    string `json:"cond_op"`
	CondValue string `json:"cond_value"`
	Action    string `json:"action"`
	ActionArg string `json:"action_arg"`
}

// Store is the mail store.
type Store struct {
	DB      *sql.DB
	blobDir string
	dataDir string
}

// New creates a Store rooted at dataDir (blobs under dataDir/blobs).
func New(database *sql.DB, dataDir string) (*Store, error) {
	blobDir := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{DB: database, blobDir: blobDir, dataDir: dataDir}, nil
}

// WipeBlobs removes every stored message blob, the outbound queue files and
// the scheduled (delayed) messages. Called during factory reset; the
// directories are recreated on demand.
func (s *Store) WipeBlobs(dataDir string) error {
	for _, dir := range []string{s.blobDir, filepath.Join(dataDir, "outbound"), filepath.Join(dataDir, "scheduled")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashPassword(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

func bool2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func i2bool(i int64) bool { return i != 0 }

// ---------------------------------------------------------------------------
// Accounts

const accountCols = `id, kind, address, display_name, password_hash, remote_proto, remote_host,
remote_port, remote_tls, remote_user, remote_pass, remote_folder, remote_target,
fetch_enabled, fetch_interval_min, fetch_keep_on_server, signature,
last_fetch_at, last_fetch_ok, last_error, created_at`

func scanAccount(sc interface{ Scan(...any) error }) (*Account, error) {
	a := &Account{}
	var pw string
	var fe, fo, keep int64
	err := sc.Scan(&a.ID, &a.Kind, &a.Address, &a.DisplayName, &pw, &a.RemoteProto, &a.RemoteHost,
		&a.RemotePort, &a.RemoteTLS, &a.RemoteUser, &a.RemotePass, &a.RemoteFolder, &a.RemoteTarget,
		&fe, &a.FetchIntervalMin, &keep, &a.Signature,
		&a.LastFetchAt, &fo, &a.LastError, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.HasPassword = pw != ""
	a.HasRemotePass = a.RemotePass != ""
	a.FetchEnabled = i2bool(fe)
	a.FetchKeepOnServer = i2bool(keep)
	a.LastFetchOK = i2bool(fo)
	return a, nil
}

// CreateLocalAccount creates a local mailbox account with login credentials.
func (s *Store) CreateLocalAccount(address, password, displayName string) (*Account, error) {
	address = normalizeAddr(address)
	if address == "" {
		return nil, fmt.Errorf("address required")
	}
	h, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	res, err := s.DB.Exec(`INSERT INTO accounts(kind,address,display_name,password_hash,created_at)
		VALUES('local',?,?,?,?)`, address, displayName, h, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := s.ensureSystemFolders(id); err != nil {
		return nil, err
	}
	return s.GetAccount(id)
}

// CreateRemoteAccount creates a remote fetch account.
//
// NOTE: the remote credential is stored recoverable (like every mail client
// does): the fetcher must present it verbatim to the remote IMAP/POP3
// server, so it can never be hashed. Only LOCAL account passwords are
// bcrypt-hashed (they are verified locally, never sent anywhere).
func (s *Store) CreateRemoteAccount(a *Account) (*Account, error) {
	if a.Address == "" || a.RemoteProto == "" || a.RemoteHost == "" {
		return nil, fmt.Errorf("address, proto and host are required")
	}
	if a.RemoteProto != "imap" && a.RemoteProto != "pop3" {
		return nil, fmt.Errorf("proto must be imap or pop3")
	}
	if a.RemoteTLS == "" {
		a.RemoteTLS = "ssl"
	}
	if a.RemoteFolder == "" {
		a.RemoteFolder = "INBOX"
	}
	if a.FetchIntervalMin <= 0 {
		a.FetchIntervalMin = 15
	}
	res, err := s.DB.Exec(`INSERT INTO accounts(kind,address,display_name,remote_proto,remote_host,remote_port,
		remote_tls,remote_user,remote_pass,remote_folder,remote_target,fetch_enabled,fetch_interval_min,fetch_keep_on_server,created_at)
		VALUES('remote',?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		normalizeAddr(a.Address), a.DisplayName, a.RemoteProto, a.RemoteHost, a.RemotePort,
		a.RemoteTLS, a.RemoteUser, a.RemotePass, a.RemoteFolder, normalizeAddr(a.RemoteTarget), bool2i(a.FetchEnabled), a.FetchIntervalMin, bool2i(a.FetchKeepOnServer), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := s.ensureSystemFolders(id); err != nil {
		return nil, err
	}
	return s.GetAccount(id)
}

// GetAccount loads one account.
func (s *Store) GetAccount(id int64) (*Account, error) {
	row := s.DB.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	return scanAccount(row)
}

// GetAccountByAddress finds a local account by address.
func (s *Store) GetAccountByAddress(address string) (*Account, error) {
	row := s.DB.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE kind='local' AND address=?`, normalizeAddr(address))
	return scanAccount(row)
}

// GetAccountByAddressAny resolves any account by address regardless of kind.
// Used by the admin mailbox-open flow: remote accounts store their fetched
// mail under their own id, so admins must be able to open them too.
func (s *Store) GetAccountByAddressAny(address string) (*Account, error) {
	row := s.DB.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE address=?`, normalizeAddr(address))
	return scanAccount(row)
}

// ListAccounts lists all accounts with unread counts.
func (s *Store) ListAccounts() ([]*Account, error) {
	rows, err := s.DB.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY kind, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAccount updates editable fields; empty password leaves it unchanged.
func (s *Store) UpdateAccount(a *Account, newPass string) error {
	if newPass != "" {
		h, err := hashPassword(newPass)
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(`UPDATE accounts SET password_hash=? WHERE id=?`, h, a.ID); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(`UPDATE accounts SET address=?, display_name=?, remote_proto=?, remote_host=?,
		remote_port=?, remote_tls=?, remote_user=?, remote_pass=?, remote_folder=?, remote_target=?,
		fetch_enabled=?, fetch_interval_min=?, fetch_keep_on_server=?, signature=? WHERE id=?`,
		normalizeAddr(a.Address), a.DisplayName, a.RemoteProto, a.RemoteHost, a.RemotePort,
		a.RemoteTLS, a.RemoteUser, a.RemotePass, a.RemoteFolder, normalizeAddr(a.RemoteTarget),
		bool2i(a.FetchEnabled), a.FetchIntervalMin, bool2i(a.FetchKeepOnServer), a.Signature, a.ID)
	return err
}

// MarkFetched records the outcome of a remote fetch round.
func (s *Store) MarkFetched(id int64, ok bool, errStr string) {
	_, _ = s.DB.Exec(`UPDATE accounts SET last_fetch_at=?, last_fetch_ok=?, last_error=? WHERE id=?`,
		now(), bool2i(ok), errStr, id)
}

// DeleteAccount removes an account and all its data.
func (s *Store) DeleteAccount(id int64) error {
	rows, err := s.DB.Query(`SELECT raw_path FROM messages WHERE account_id=?`, id)
	if err == nil {
		var paths []string
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				paths = append(paths, p)
			}
		}
		rows.Close()
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	_, err = s.DB.Exec(`DELETE FROM accounts WHERE id=?`, id)
	return err
}

// VerifyLocalLogin checks address/password for the built-in POP3/IMAP services.
func (s *Store) VerifyLocalLogin(address, password string) (*Account, bool) {
	a, err := s.GetAccountByAddress(address)
	if err != nil {
		return nil, false
	}
	row := s.DB.QueryRow(`SELECT password_hash FROM accounts WHERE id=?`, a.ID)
	var h string
	if row.Scan(&h) != nil || h == "" {
		return nil, false
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) != nil {
		return nil, false
	}
	return a, true
}

func normalizeAddr(a string) string { return strings.ToLower(strings.TrimSpace(a)) }

// ---------------------------------------------------------------------------
// Folders

func (s *Store) ensureSystemFolders(accountID int64) ([]*Folder, error) {
	sys := [][2]string{{"INBOX", "inbox"}, {"Sent", "sent"}, {"Drafts", "drafts"}, {"Trash", "trash"}, {"Junk", "junk"}, {"Archive", "archive"}}
	for _, f := range sys {
		if _, err := s.EnsureFolder(accountID, f[0], f[1]); err != nil {
			return nil, err
		}
	}
	return s.ListFolders(accountID)
}

// EnsureFolder creates a folder when missing; returns its id.
func (s *Store) EnsureFolder(accountID int64, name, special string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM folders WHERE account_id=? AND name=?`, accountID, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO folders(account_id,name,special,uid_validity,next_uid) VALUES(?,?,?,?,1)`,
		accountID, name, special, uint32(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFolders lists folders of an account with counts.
func (s *Store) ListFolders(accountID int64) ([]*Folder, error) {
	rows, err := s.DB.Query(`SELECT f.id,f.account_id,f.name,f.special,f.subscribed,f.uid_validity,f.next_uid,
		COUNT(m.id), SUM(CASE WHEN m.flags NOT LIKE '%\Seen%' THEN 1 ELSE 0 END)
		FROM folders f LEFT JOIN messages m ON m.folder_id=f.id
		WHERE f.account_id=? GROUP BY f.id ORDER BY CASE f.name WHEN 'INBOX' THEN 0 ELSE 1 END, f.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Folder{}
	for rows.Next() {
		f := &Folder{}
		var sub int64
		var unseen sql.NullInt64
		if err := rows.Scan(&f.ID, &f.AccountID, &f.Name, &f.Special, &sub, &f.UIDValidity, &f.NextUID, &f.Count, &unseen); err != nil {
			return nil, err
		}
		f.Subscribed = i2bool(sub)
		f.Unseen = unseen.Int64
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFolder loads one folder.
func (s *Store) GetFolder(accountID, folderID int64) (*Folder, error) {
	list, err := s.ListFolders(accountID)
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		if f.ID == folderID {
			return f, nil
		}
	}
	return nil, sql.ErrNoRows
}

// FolderByName finds a folder by exact name.
func (s *Store) FolderByName(accountID int64, name string) (*Folder, error) {
	list, err := s.ListFolders(accountID)
	if err != nil {
		return nil, err
	}
	for _, f := range list {
		if f.Name == name {
			return f, nil
		}
	}
	return nil, sql.ErrNoRows
}

// RenameFolder renames; system INBOX cannot be renamed.
func (s *Store) RenameFolder(accountID, folderID int64, name string) error {
	if name == "INBOX" || name == "" {
		return fmt.Errorf("invalid folder name")
	}
	_, err := s.DB.Exec(`UPDATE folders SET name=? WHERE id=? AND account_id=? AND name<>'INBOX'`, name, folderID, accountID)
	return err
}

// DeleteFolder removes a non-system folder with its messages.
func (s *Store) DeleteFolder(accountID, folderID int64) error {
	var name string
	if err := s.DB.QueryRow(`SELECT name FROM folders WHERE id=? AND account_id=?`, folderID, accountID).Scan(&name); err != nil {
		return err
	}
	if name == "INBOX" {
		return fmt.Errorf("cannot delete INBOX")
	}
	rows, err := s.DB.Query(`SELECT raw_path FROM messages WHERE folder_id=?`, folderID)
	if err == nil {
		var paths []string
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				paths = append(paths, p)
			}
		}
		rows.Close()
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	_, err = s.DB.Exec(`DELETE FROM folders WHERE id=?`, folderID)
	return err
}

// ---------------------------------------------------------------------------
// Message write path

// DeliverOpts carries optional delivery parameters.
type DeliverOpts struct {
	Flags      []string
	InternalAt int64
}

// Deliver parses raw, stores the blob and inserts the message row.
// It returns the message row id and the assigned folder UID.
func (s *Store) Deliver(accountID, folderID int64, raw []byte, opts *DeliverOpts) (int64, uint32, error) {
	info := ParseHeaders(raw)
	if opts != nil && opts.InternalAt > 0 {
		info.InternalAt = opts.InternalAt
	}
	if info.InternalAt == 0 {
		info.InternalAt = now()
	}
	if info.SentAt == 0 {
		info.SentAt = info.InternalAt
	}
	var flags []string
	if opts != nil {
		flags = opts.Flags
	}
	blobPath, err := s.storeBlob(accountID, raw)
	if err != nil {
		return 0, 0, err
	}
	sortedFlags := info.Flags
	if len(flags) > 0 {
		sortedFlags = mergeFlags(sortedFlags, flags)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var uid int64
	if err := tx.QueryRow(`SELECT next_uid FROM folders WHERE id=?`, folderID).Scan(&uid); err != nil {
		return 0, 0, err
	}
	if _, err := tx.Exec(`UPDATE folders SET next_uid=next_uid+1 WHERE id=?`, folderID); err != nil {
		return 0, 0, err
	}
	toJSON, _ := marshalJSON(info.To)
	ccJSON, _ := marshalJSON(info.CC)
	res, err := tx.Exec(`INSERT INTO messages(account_id,folder_id,uid,message_id,in_reply_to,from_name,from_addr,
		to_addrs,cc_addrs,subject,sent_at,internal_at,size,flags,snippet,has_attach,raw_path,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		accountID, folderID, uid, info.MessageID, info.InReplyTo, info.FromName, info.FromAddr,
		toJSON, ccJSON, info.Subject, info.SentAt, info.InternalAt, len(raw), strings.Join(sortedFlags, " "),
		info.Snippet, bool2i(info.HasAttach), blobPath, now())
	if err != nil {
		return 0, 0, err
	}
	rowID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return rowID, uint32(uid), nil
}

// DeliverToName delivers into a folder by name, creating it when needed.
func (s *Store) DeliverToName(accountID int64, folderName string, raw []byte, opts *DeliverOpts) (int64, uint32, error) {
	folderID, err := s.EnsureFolder(accountID, folderName, "")
	if err != nil {
		return 0, 0, err
	}
	return s.Deliver(accountID, folderID, raw, opts)
}

func (s *Store) storeBlob(accountID int64, raw []byte) (string, error) {
	sum := randHex(12)
	sub := filepath.Join(s.blobDir, strconv.FormatInt(accountID, 10), sum[:2])
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(sub, sum+".eml")
	return p, os.WriteFile(p, raw, 0o600)
}

// ---------------------------------------------------------------------------
// Message read path

const msgCols = `id,account_id,folder_id,uid,message_id,in_reply_to,from_name,from_addr,to_addrs,cc_addrs,
subject,sent_at,internal_at,size,flags,snippet,has_attach,raw_path`

func scanMessage(sc interface{ Scan(...any) error }) (*Message, error) {
	m := &Message{}
	var flags, to, cc, rawPath string
	var hasAttach int64
	if err := sc.Scan(&m.ID, &m.AccountID, &m.FolderID, &m.UID, &m.MessageID, &m.InReplyTo,
		&m.FromName, &m.FromAddr, &to, &cc, &m.Subject, &m.SentAt, &m.InternalAt, &m.Size,
		&flags, &m.Snippet, &hasAttach, &rawPath); err != nil {
		return nil, err
	}
	m.Flags = splitFlags(flags)
	m.ToAddrs = unmarshalList(to)
	m.CCAddrs = unmarshalList(cc)
	m.HasAttach = i2bool(hasAttach)
	m.RawPath = rawPath
	return m, nil
}

// ListMessages returns a page of messages of a folder, newest first.
// includeDeleted=false hides messages flagged \Deleted.
func (s *Store) ListMessages(accountID, folderID int64, offset, limit int, includeDeleted bool) ([]*Message, int64, error) {
	delClause := ``
	if !includeDeleted {
		delClause = ` AND flags NOT LIKE '%\Deleted%'`
	}
	var total int64
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder_id=?`+delClause, folderID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT `+msgCols+` FROM messages WHERE folder_id=?`+delClause+`
		ORDER BY sent_at DESC, uid DESC LIMIT ? OFFSET ?`, folderID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// SearchMessages filters messages of a folder by field LIKE search.
func (s *Store) SearchMessages(accountID, folderID int64, field, q string, limit int) ([]*Message, error) {
	field = strings.ToLower(field)
	allowed := map[string]string{
		"subject": "subject", "from": "from_addr||' '||from_name", "to": "to_addrs", "body": "snippet",
		"": "subject||' '||from_addr||' '||from_name",
	}
	col, ok := allowed[field]
	if !ok {
		col = allowed[""]
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Escape LIKE wildcards so % and _ match literally.
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.DB.Query(`SELECT `+msgCols+` FROM messages WHERE folder_id=? AND `+col+` LIKE ? ESCAPE '\'
		AND flags NOT LIKE '%\Deleted%' ORDER BY sent_at DESC LIMIT ?`, folderID, "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetMessage loads one message row.
func (s *Store) GetMessage(accountID, msgID int64) (*Message, error) {
	row := s.DB.QueryRow(`SELECT `+msgCols+` FROM messages WHERE account_id=? AND id=?`, accountID, msgID)
	return scanMessage(row)
}

// GetMessageRaw reads the raw blob for POP3/IMAP/web download.
func (s *Store) GetMessageRaw(accountID, msgID int64) ([]byte, *Message, error) {
	m, err := s.GetMessage(accountID, msgID)
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(m.RawPath)
	if err != nil {
		return nil, nil, err
	}
	return raw, m, nil
}

// ListAttachments parses the stored raw message and returns attachment
// metadata (no content).
func (s *Store) ListAttachments(accountID, msgID int64) ([]*Attachment, error) {
	raw, _, err := s.GetMessageRaw(accountID, msgID)
	if err != nil {
		return nil, err
	}
	return ParseHeaders(raw).Attachments, nil
}

// ExtractAttachment returns the decoded content of one attachment part.
func (s *Store) ExtractAttachment(accountID, msgID int64, part int) ([]byte, string, string, error) {
	raw, _, err := s.GetMessageRaw(accountID, msgID)
	if err != nil {
		return nil, "", "", err
	}
	return ExtractPart(raw, part)
}

// SetFlags sets/adds/removes IMAP flags on the given message ids of an account.
func (s *Store) SetFlags(accountID int64, ids []int64, mode string, flags []string) error {
	if len(ids) == 0 || len(flags) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var cur string
		if err := tx.QueryRow(`SELECT flags FROM messages WHERE account_id=? AND id=?`, accountID, id).Scan(&cur); err != nil {
			continue
		}
		curFlags := splitFlags(cur)
		switch mode {
		case "set":
			curFlags = append([]string(nil), flags...)
		case "add":
			curFlags = mergeFlags(curFlags, flags)
		case "remove":
			for _, f := range flags {
				curFlags = removeFlag(curFlags, f)
			}
		}
		if _, err := tx.Exec(`UPDATE messages SET flags=? WHERE account_id=? AND id=?`, strings.Join(curFlags, " "), accountID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MoveMessages moves messages to another folder (used by filters).
func (s *Store) MoveMessages(accountID, toFolderID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var folderID int64
		if err := tx.QueryRow(`SELECT folder_id FROM messages WHERE account_id=? AND id=?`, accountID, id).Scan(&folderID); err != nil {
			continue
		}
		if folderID == toFolderID {
			continue
		}
		var uid int64
		if err := tx.QueryRow(`SELECT next_uid FROM folders WHERE id=?`, toFolderID).Scan(&uid); err != nil {
			continue
		}
		if _, err := tx.Exec(`UPDATE folders SET next_uid=next_uid+1 WHERE id=?`, toFolderID); err != nil {
			continue
		}
		if _, err := tx.Exec(`UPDATE messages SET folder_id=?, uid=? WHERE id=?`, toFolderID, uid, id); err != nil {
			continue
		}
	}
	return tx.Commit()
}

// ExpungeFolder permanently removes \Deleted messages from a folder.
func (s *Store) ExpungeFolder(accountID, folderID int64) (int64, error) {
	rows, err := s.DB.Query(`SELECT id, raw_path FROM messages WHERE folder_id=? AND flags LIKE '%\Deleted%'`, folderID)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var p string
		if rows.Scan(&id, &p) == nil {
			ids = append(ids, id)
			paths = append(paths, p)
		}
	}
	rows.Close()
	for _, p := range paths {
		_ = os.Remove(p)
	}
	for _, id := range ids {
		_, _ = s.DB.Exec(`DELETE FROM messages WHERE id=?`, id)
	}
	return int64(len(ids)), nil
}

// DeleteMessages permanently removes messages (rows + blobs).
func (s *Store) DeleteMessages(accountID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var p string
		if err := tx.QueryRow(`SELECT raw_path FROM messages WHERE account_id=? AND id=?`, accountID, id).Scan(&p); err == nil && p != "" {
			_ = os.Remove(p)
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE account_id=? AND id=?`, accountID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkSeen is a convenience wrapper used by web/pop/imap.
func (s *Store) MarkSeen(accountID, msgID int64) {
	_ = s.SetFlags(accountID, []int64{msgID}, "add", []string{`\Seen`})
}

// ---------------------------------------------------------------------------
// Flags helpers

func splitFlags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Fields(s)
	sort.Strings(parts)
	return parts
}

func mergeFlags(cur, add []string) []string {
	set := map[string]bool{}
	for _, f := range cur {
		set[f] = true
	}
	for _, f := range add {
		set[f] = true
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func removeFlag(cur []string, flag string) []string {
	var out []string
	for _, f := range cur {
		if f != flag {
			out = append(out, f)
		}
	}
	return out
}

func marshalJSON(v []string) ([]byte, error) {
	if v == nil {
		v = []string{}
	}
	return json.Marshal(v)
}

func unmarshalList(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	return out
}
