package fetch

import (
	"crypto/tls"
	"fmt"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"cloudpost/internal/mailstore"
)

var readOnlySelect = &imap.SelectOptions{ReadOnly: true}

// dialIMAP connects to a remote IMAP server per account TLS mode.
func dialIMAP(a *mailstore.Account) (*imapclient.Client, error) {
	addr := fmt.Sprintf("%s:%d", a.RemoteHost, a.RemotePort)
	if a.RemotePort == 0 {
		if a.RemoteTLS == "none" || a.RemoteTLS == "starttls" {
			addr = fmt.Sprintf("%s:143", a.RemoteHost)
		} else {
			addr = fmt.Sprintf("%s:993", a.RemoteHost)
		}
	}
	switch a.RemoteTLS {
	case "none":
		return imapclient.DialInsecure(addr, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	case "starttls":
		return imapclient.DialStartTLS(addr, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	default:
		return imapclient.DialTLS(addr, &imapclient.Options{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	}
}

// fetchIMAP pulls new messages from a remote IMAP account.
func (f *Fetcher) fetchIMAP(a *mailstore.Account) (int, error) {
	client, err := dialIMAP(a)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer client.Logout().Wait()
	if err := client.Login(a.RemoteUser, a.RemotePass).Wait(); err != nil {
		return 0, fmt.Errorf("login: %w", err)
	}
	selData, err := client.Select(a.RemoteFolder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, fmt.Errorf("select %s: %w", a.RemoteFolder, err)
	}

	uidValidity := strconv.FormatUint(uint64(selData.UIDValidity), 10)
	if f.stateGet(a.ID, "imap_uidvalidity") != uidValidity {
		f.stateSet(a.ID, "imap_uidvalidity", uidValidity)
		f.stateSet(a.ID, "imap_lastuid", "0")
	}
	lastUID, _ := strconv.ParseUint(f.stateGet(a.ID, "imap_lastuid"), 10, 32)

	crit := &imap.SearchCriteria{}
	uidNext := uint32(selData.UIDNext)
	if lastUID > 0 && uidNext > uint32(lastUID)+1 {
		set := imap.UIDSet{}
		set.AddRange(imap.UID(uint32(lastUID)+1), imap.UID(uidNext-1))
		crit.UID = []imap.UIDSet{set}
	}
	searchData, err := client.UIDSearch(crit, nil).Wait()
	if err != nil {
		return 0, fmt.Errorf("search: %w", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return 0, nil
	}
	set := imap.UIDSet{}
	for _, u := range uids {
		set.AddNum(u)
	}
	fetchOpts := &imap.FetchOptions{
		UID:          true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{{}},
	}
	msgs, err := client.Fetch(set, fetchOpts).Collect()
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}
	full := &imap.FetchItemBodySection{}
	count := 0
	maxUID := uint32(lastUID)
	for _, msg := range msgs {
		raw := msg.FindBodySection(full)
		if len(raw) == 0 {
			continue
		}
		internalAt := msg.InternalDate.Unix()
		if msg.InternalDate.IsZero() {
			internalAt = 0
		}
		if _, _, err := f.deliverWithFilters(a, raw, internalAt); err != nil {
			return count, fmt.Errorf("store: %w", err)
		}
		count++
		if uint32(msg.UID) > maxUID {
			maxUID = uint32(msg.UID)
		}
	}
	f.stateSet(a.ID, "imap_lastuid", strconv.FormatUint(uint64(maxUID), 10))
	return count, nil
}

var _ = strings.TrimSpace
