// Package fetch implements remote mailbox fetching (POP3/IMAP) with filters.
package fetch

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"cloudpost/internal/filter"
	"cloudpost/internal/mailstore"
)

// Deps wires the fetcher.
type Deps struct {
	Store  *mailstore.Store
	Engine *filter.Engine
	DB     *sql.DB
}

// Fetcher polls remote accounts on schedule.
type Fetcher struct {
	deps Deps
	stop chan struct{}
}

// New creates a Fetcher.
func New(deps Deps) *Fetcher { return &Fetcher{deps: deps, stop: make(chan struct{})} }

// Run starts the polling loop.
func (f *Fetcher) Run() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			f.PollDue()
		case <-f.stop:
			return
		}
	}
}

// Stop terminates the loop.
func (f *Fetcher) Stop() { close(f.stop) }

// PollDue fetches all accounts whose interval has elapsed.
func (f *Fetcher) PollDue() {
	accounts, err := f.deps.Store.ListAccounts()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, a := range accounts {
		if a.Kind != "remote" || !a.FetchEnabled {
			continue
		}
		if now-a.LastFetchAt < int64(a.FetchIntervalMin)*60 {
			continue
		}
		f.FetchAccount(a)
	}
}

// FetchAccount fetches one account synchronously.
func (f *Fetcher) FetchAccount(a *mailstore.Account) {
	log.Printf("[fetch] fetching %s via %s://%s", a.Address, a.RemoteProto, a.RemoteHost)
	var n int
	var err error
	switch a.RemoteProto {
	case "imap":
		n, err = f.fetchIMAP(a)
	case "pop3":
		n, err = f.fetchPOP3(a)
	default:
		err = fmt.Errorf("unknown proto %q", a.RemoteProto)
	}
	if err != nil {
		log.Printf("[fetch] %s failed: %v", a.Address, err)
		f.deps.Store.MarkFetched(a.ID, false, err.Error())
		return
	}
	f.deps.Store.MarkFetched(a.ID, true, "")
	if n > 0 {
		log.Printf("[fetch] %s: %d new messages", a.Address, n)
	}
}

func (f *Fetcher) stateGet(accountID int64, key string) string {
	var v string
	if f.deps.DB.QueryRow(`SELECT v FROM remote_state WHERE account_id=? AND k=?`, accountID, key).Scan(&v) == nil {
		return v
	}
	return ""
}

func (f *Fetcher) stateSet(accountID int64, key, value string) {
	_, _ = f.deps.DB.Exec(`INSERT INTO remote_state(account_id,k,v) VALUES(?,?,?)
		ON CONFLICT(account_id,k) DO UPDATE SET v=excluded.v`, accountID, key, value)
}

func (f *Fetcher) seenUIDLs(accountID int64) map[string]bool {
	out := map[string]bool{}
	var v string
	if f.deps.DB.QueryRow(`SELECT v FROM remote_state WHERE account_id=? AND k='pop3_uidls'`, accountID).Scan(&v) == nil && v != "" {
		var list []string
		if json.Unmarshal([]byte(v), &list) == nil {
			for _, x := range list {
				out[x] = true
			}
		}
	}
	return out
}

func (f *Fetcher) saveUIDLs(accountID int64, seen map[string]bool) {
	list := make([]string, 0, len(seen))
	for k := range seen {
		list = append(list, k)
	}
	if len(list) > 5000 {
		list = list[len(list)-5000:]
	}
	b, _ := json.Marshal(list)
	f.stateSet(accountID, "pop3_uidls", string(b))
}

func (f *Fetcher) deliverWithFilters(a *mailstore.Account, raw []byte, internalAt int64) (int64, uint32, error) {
	// Remote messages land in the target local mailbox (fallback: a synthetic
	// mailbox under the remote account id so POP3/IMAP/web still work).
	targetID := a.ID
	if a.RemoteTarget != "" {
		if t, err := f.deps.Store.GetAccountByAddress(a.RemoteTarget); err == nil {
			targetID = t.ID
		}
	}
	inbox, err := f.deps.Store.FolderByName(targetID, "INBOX")
	if err != nil {
		return 0, 0, err
	}
	msgID, uid, err := f.deps.Store.Deliver(targetID, inbox.ID, raw, &mailstore.DeliverOpts{InternalAt: int64(internalAt)})
	if err != nil {
		return 0, 0, err
	}
	info := mailstore.ParseHeaders(raw)
	f.deps.Engine.Apply(targetID, msgID, inbox.ID, info.Subject, info.FromAddr, strings.Join(info.To, ","), info.Snippet)
	return msgID, uid, nil
}

func trimErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
