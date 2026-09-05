// Package imapd implements the built-in IMAP4rev1 service on go-imap v2.
package imapd

import (
	"bytes"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"cloudpost/internal/mailstore"
	"cloudpost/internal/state"
)

// Deps wires the server to the store.
type Deps struct {
	Store  *mailstore.Store
	State  *state.State
	Domain string
}

// Server is the IMAP listener.
type Server struct {
	deps   Deps
	srv    *imapserver.Server
	ln     net.Listener
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// New builds the IMAP server.
func New(deps Deps) *Server {
	opts := &imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			s := &session{deps: deps}
			return s, nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapUIDPlus:   {},
			imap.CapMove:      {},
		},
		InsecureAuth: true,
	}
	return &Server{deps: deps, srv: imapserver.New(opts)}
}

// Listen binds the listener.
func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Serve blocks accepting connections.
func (s *Server) Serve() error {
	return s.srv.Serve(s.ln)
}

// Close shuts down the listener.
func (s *Server) Close() {
	s.mu.Lock()
	alreadyClosed := s.closed
	s.closed = true
	s.mu.Unlock()
	if alreadyClosed || s.ln == nil {
		return
	}
	_ = s.ln.Close()
}

type selState struct {
	folder *mailstore.Folder
	msgs   []*mailstore.Message // ascending uid
}

func (ss *selState) seqOf(uid uint32) int {
	for i, m := range ss.msgs {
		if m.UID == uid {
			return i + 1
		}
	}
	return 0
}

type session struct {
	deps       Deps
	account    *mailstore.Account
	sel        *selState
	mu         sync.Mutex
	authedUser string
}

func (s *session) Login(username, password string) error {
	acc, ok := s.deps.Store.VerifyLocalLogin(username, password)
	if !ok {
		time.Sleep(400 * time.Millisecond) // slow brute force
		return imapserver.ErrAuthFailed
	}
	s.account = acc
	return nil
}

func (s *session) Close() error { return nil }

func (s *session) requireAuth() (*mailstore.Account, error) {
	if s.account == nil {
		return nil, imapserver.ErrAuthFailed
	}
	return s.account, nil
}

func (s *session) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	acc, err := s.requireAuth()
	if err != nil {
		return nil, err
	}
	folder, err := s.deps.Store.FolderByName(acc.ID, mailbox)
	if err != nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	msgs, _, err := s.deps.Store.ListMessages(acc.ID, folder.ID, 0, 100000, true)
	if err != nil {
		return nil, err
	}
	// ListMessages returns newest-first; IMAP wants ascending uid.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	s.sel = &selState{folder: folder, msgs: msgs}
	data := &imap.SelectData{
		Flags:          []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft},
		PermanentFlags: []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft},
		NumMessages:    uint32(len(msgs)),
		UIDNext:        imap.UID(folder.NextUID),
		UIDValidity:    folder.UIDValidity,
	}
	return data, nil
}

func (s *session) Create(mailbox string, options *imap.CreateOptions) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	mailbox = strings.TrimSuffix(mailbox, "/")
	if mailbox == "" {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Invalid mailbox name"}
	}
	_, err = s.deps.Store.EnsureFolder(acc.ID, mailbox, "")
	return err
}

func (s *session) Delete(mailbox string) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	folder, err := s.deps.Store.FolderByName(acc.ID, mailbox)
	if err != nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No such mailbox"}
	}
	if mailbox == "INBOX" {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Cannot delete INBOX"}
	}
	return s.deps.Store.DeleteFolder(acc.ID, folder.ID)
}

func (s *session) Rename(mailbox, newName string, options *imap.RenameOptions) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	folder, err := s.deps.Store.FolderByName(acc.ID, mailbox)
	if err != nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No such mailbox"}
	}
	return s.deps.Store.RenameFolder(acc.ID, folder.ID, newName)
}

func (s *session) Subscribe(mailbox string) error   { return nil }
func (s *session) Unsubscribe(mailbox string) error { return nil }

func (s *session) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	folders, err := s.deps.Store.ListFolders(acc.ID)
	if err != nil {
		return err
	}
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	for _, f := range folders {
		for _, pat := range patterns {
			full := pat
			if ref != "" {
				full = strings.TrimSuffix(ref, "/") + "/" + pat
			}
			if !matchPattern(full, f.Name) {
				continue
			}
			ld := &imap.ListData{
				Attrs:   []imap.MailboxAttr{},
				Delim:   '/',
				Mailbox: f.Name,
			}
			if f.Name == "INBOX" {
				ld.Attrs = append(ld.Attrs, imap.MailboxAttrNoInferiors)
			}
			if options.ReturnSubscribed && f.Subscribed {
				ld.ChildInfo = &imap.ListDataChildInfo{Subscribed: true}
			}
			if options.ReturnStatus != nil {
				sd, err := s.statusFor(f, options.ReturnStatus)
				if err == nil {
					ld.Status = sd
				}
			}
			if err := w.WriteList(ld); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

func matchPattern(pattern, name string) bool {
	rx := "^"
	for _, ch := range pattern {
		switch ch {
		case '*':
			rx += ".*"
		case '%':
			rx += "[^/]*"
		default:
			rx += regexp.QuoteMeta(string(ch))
		}
	}
	rx += "$"
	ok, err := regexp.MatchString(rx, name)
	return err == nil && ok
}

func (s *session) statusFor(folder *mailstore.Folder, options *imap.StatusOptions) (*imap.StatusData, error) {
	msgs, _, err := s.deps.Store.ListMessages(s.account.ID, folder.ID, 0, 100000, true)
	if err != nil {
		return nil, err
	}
	sd := &imap.StatusData{Mailbox: folder.Name, UIDNext: imap.UID(folder.NextUID), UIDValidity: folder.UIDValidity}
	var size int64
	var unseen uint32
	var deleted uint32
	for _, m := range msgs {
		size += m.Size
		if !hasFlag(m.Flags, `\Seen`) {
			unseen++
		}
		if hasFlag(m.Flags, `\Deleted`) {
			deleted++
		}
	}
	if options.NumMessages {
		n := uint32(len(msgs))
		sd.NumMessages = &n
	}
	if options.NumRecent {
		r := uint32(0)
		sd.NumRecent = &r
	}
	if options.NumUnseen {
		sd.NumUnseen = &unseen
	}
	if options.NumDeleted {
		sd.NumDeleted = &deleted
	}
	if options.Size {
		sd.Size = &size
	}
	return sd, nil
}

func (s *session) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	acc, err := s.requireAuth()
	if err != nil {
		return nil, err
	}
	folder, err := s.deps.Store.FolderByName(acc.ID, mailbox)
	if err != nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No such mailbox"}
	}
	return s.statusFor(folder, options)
}

func (s *session) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	acc, err := s.requireAuth()
	if err != nil {
		return nil, err
	}
	folder, err := s.deps.Store.FolderByName(acc.ID, mailbox)
	if err != nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	raw, err := io.ReadAll(io.LimitReader(r, 50<<20))
	if err != nil {
		return nil, err
	}
	opts := &mailstore.DeliverOpts{Flags: flagStrings(options.Flags)}
	if !options.Time.IsZero() {
		opts.InternalAt = options.Time.Unix()
	}
	id, uid, err := s.deps.Store.Deliver(acc.ID, folder.ID, raw, opts)
	if err != nil {
		return nil, err
	}
	s.appendToSelection(folder.ID, id, uid)
	return &imap.AppendData{UID: imap.UID(uid), UIDValidity: folder.UIDValidity}, nil
}

func (s *session) appendToSelection(folderID, msgID int64, uid uint32) {
	if s.sel == nil || s.sel.folder.ID != folderID {
		return
	}
	s.sel.msgs = append(s.sel.msgs, &mailstore.Message{ID: msgID, FolderID: folderID, UID: uid})
}

func (s *session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error { return nil }

func (s *session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	select {
	case <-stop:
	case <-time.After(25 * time.Minute):
	}
	return nil
}

func (s *session) Unselect() error { s.sel = nil; return nil }

func (s *session) currentMsgs() *selState { return s.sel }

// resolveNumSet maps a NumSet to message ids/uids/seqs against the selected folder.
func (s *session) resolveNumSet(numSet imap.NumSet, kind imapserver.NumKind) []int64 {
	var out []int64
	if s.sel == nil {
		return nil
	}
	switch set := numSet.(type) {
	case imap.SeqSet:
		nums, _ := set.Nums()
		for _, seq := range nums {
			if int(seq) >= 1 && int(seq) <= len(s.sel.msgs) {
				out = append(out, s.sel.msgs[seq-1].ID)
			}
		}
	case imap.UIDSet:
		nums, _ := set.Nums()
		for _, uid := range nums {
			for _, m := range s.sel.msgs {
				if m.UID == uint32(uid) {
					out = append(out, m.ID)
					break
				}
			}
		}
	}
	return out
}

func (s *session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	if s.sel == nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	var toDelete []int64
	var deletedSeqs []uint32
	for i, m := range s.sel.msgs {
		if !hasFlag(m.Flags, `\Deleted`) {
			continue
		}
		if uids != nil {
			if !uids.Contains(imap.UID(m.UID)) {
				continue
			}
		}
		toDelete = append(toDelete, m.ID)
		deletedSeqs = append(deletedSeqs, uint32(i+1))
	}
	if len(toDelete) == 0 {
		return nil
	}
	if err := s.deps.Store.SetFlags(acc.ID, toDelete, "add", []string{`\Deleted`}); err != nil {
		return err
	}
	if _, err := s.deps.Store.ExpungeFolder(acc.ID, s.sel.folder.ID); err != nil {
		return err
	}
	// Report expunges in ascending sequence order.
	for _, seq := range deletedSeqs {
		if err := w.WriteExpunge(seq); err != nil {
			return err
		}
	}
	var keep []*mailstore.Message
	removed := map[int64]bool{}
	for _, id := range toDelete {
		removed[id] = true
	}
	for _, m := range s.sel.msgs {
		if !removed[m.ID] {
			keep = append(keep, m)
		}
	}
	s.sel.msgs = keep
	return nil
}

func (s *session) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	if _, err := s.requireAuth(); err != nil {
		return nil, err
	}
	if s.sel == nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	out := &imap.SeqSet{}
	if kind == imapserver.NumKindUID {
		out = nil
	}
	uidsOut := &imap.UIDSet{}
	for i, m := range s.sel.msgs {
		seq := uint32(i + 1)
		if !criteriaMatch(criteria, m, seq) {
			continue
		}
		if kind == imapserver.NumKindUID {
			uidsOut.AddNum(imap.UID(m.UID))
		} else {
			out.AddNum(seq)
		}
	}
	var all imap.NumSet
	if kind == imapserver.NumKindUID {
		all = *uidsOut
	} else {
		all = *out
	}
	return &imap.SearchData{All: all}, nil
}

func criteriaMatch(c *imap.SearchCriteria, m *mailstore.Message, seq uint32) bool {
	if len(c.UID) > 0 {
		found := false
		for _, set := range c.UID {
			if uidSetContains(set, m.UID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(c.SeqNum) > 0 {
		found := false
		for _, set := range c.SeqNum {
			if seqSetContains(set, seq) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if !c.Since.IsZero() && time.Unix(m.SentAt, 0).Before(c.Since) {
		return false
	}
	if !c.Before.IsZero() && !time.Unix(m.SentAt, 0).Before(c.Before) {
		return false
	}
	if !c.SentSince.IsZero() && time.Unix(m.SentAt, 0).Before(c.SentSince) {
		return false
	}
	if !c.SentBefore.IsZero() && !time.Unix(m.SentAt, 0).Before(c.SentBefore) {
		return false
	}
	for _, hf := range c.Header {
		key := strings.ToLower(hf.Key)
		val := strings.ToLower(hf.Value)
		var hay string
		switch key {
		case "subject":
			hay = strings.ToLower(m.Subject)
		case "from":
			hay = strings.ToLower(m.FromAddr + " " + m.FromName)
		case "to":
			hay = strings.ToLower(strings.Join(m.ToAddrs, " "))
		case "cc":
			hay = strings.ToLower(strings.Join(m.CCAddrs, " "))
		case "message-id":
			hay = strings.ToLower(m.MessageID)
		}
		if !strings.Contains(hay, val) {
			return false
		}
	}
	for _, t := range c.Text {
		low := strings.ToLower(t)
		if !strings.Contains(strings.ToLower(m.Subject), low) &&
			!strings.Contains(strings.ToLower(m.Snippet), low) &&
			!strings.Contains(strings.ToLower(m.FromAddr), low) {
			return false
		}
	}
	for _, t := range c.Body {
		if !strings.Contains(strings.ToLower(m.Snippet), strings.ToLower(t)) {
			return false
		}
	}
	for _, f := range c.Flag {
		if !hasFlag(m.Flags, string(f)) {
			return false
		}
	}
	for _, f := range c.NotFlag {
		if hasFlag(m.Flags, string(f)) {
			return false
		}
	}
	if c.Larger > 0 && m.Size <= c.Larger {
		return false
	}
	if c.Smaller > 0 && m.Size >= c.Smaller {
		return false
	}
	for i := range c.Not {
		if criteriaMatch(&c.Not[i], m, seq) {
			return false
		}
	}
	for _, pair := range c.Or {
		a, b := pair[0], pair[1]
		if !criteriaMatch(&a, m, seq) && !criteriaMatch(&b, m, seq) {
			return false
		}
	}
	return true
}

func seqSetContains(set imap.SeqSet, n uint32) bool {
	nums, _ := set.Nums()
	for _, x := range nums {
		if x == n {
			return true
		}
	}
	return false
}

func uidSetContains(set imap.UIDSet, n uint32) bool {
	nums, _ := set.Nums()
	for _, x := range nums {
		if uint32(x) == n {
			return true
		}
	}
	return false
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if strings.EqualFold(x, f) {
			return true
		}
	}
	return false
}

func flagStrings(flags []imap.Flag) []string {
	var out []string
	for _, f := range flags {
		out = append(out, string(f))
	}
	return out
}

func imapAddrs(addrs []string) []imap.Address {
	var out []imap.Address
	for _, a := range addrs {
		i := strings.LastIndexByte(a, '@')
		if i < 0 {
			continue
		}
		out = append(out, imap.Address{Mailbox: a[:i], Host: a[i+1:]})
	}
	return out
}

func envelopeFor(m *mailstore.Message) *imap.Envelope {
	env := &imap.Envelope{
		Date:    time.Unix(m.SentAt, 0),
		Subject: m.Subject,
		From:    []imap.Address{{Name: m.FromName, Mailbox: "", Host: ""}},
		To:      imapAddrs(m.ToAddrs),
		Cc:      imapAddrs(m.CCAddrs),
	}
	if i := strings.LastIndexByte(m.FromAddr, '@'); i >= 0 {
		env.From = []imap.Address{{Name: m.FromName, Mailbox: m.FromAddr[:i], Host: m.FromAddr[i+1:]}}
	}
	if m.InReplyTo != "" {
		env.InReplyTo = []string{m.InReplyTo}
	}
	env.MessageID = m.MessageID
	env.Sender = env.From
	env.ReplyTo = env.From
	return env
}

func splitHeaderBody(raw []byte) (header, body []byte) {
	if idx := bytes.Index(raw, []byte("\r\n\r\n")); idx >= 0 {
		return raw[:idx+4], raw[idx+4:]
	}
	if idx := bytes.Index(raw, []byte("\n\n")); idx >= 0 {
		return raw[:idx+2], raw[idx+2:]
	}
	return raw, nil
}

// extractPart returns raw content of MIME part at path (1-based indices).
func extractPart(raw []byte, path []int) []byte {
	if len(path) == 0 {
		return raw
	}
	header, body := splitHeaderBody(raw)
	ct := headerValue(header, "Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "multipart/") {
		return nil
	}
	boundary := boundaryOf(ct)
	if boundary == "" {
		return nil
	}
	parts := splitMultipart(body, boundary)
	idx := path[0]
	if idx < 1 || idx > len(parts) {
		return nil
	}
	return extractPart(parts[idx-1], path[1:])
}

func headerValue(header []byte, name string) string {
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimRight(line, "\r")
		if i := strings.IndexByte(line, ':'); i > 0 && strings.EqualFold(strings.TrimSpace(line[:i]), name) {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

func boundaryOf(ct string) string {
	for _, kv := range strings.Split(ct, ";") {
		kv = strings.TrimSpace(kv)
		if strings.HasPrefix(strings.ToLower(kv), "boundary=") {
			return strings.Trim(kv[len("boundary="):], `"`)
		}
	}
	return ""
}

func splitMultipart(body []byte, boundary string) [][]byte {
	delim := []byte("--" + boundary)
	var parts [][]byte
	var cur []byte
	inPart := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, delim) {
			if inPart && cur != nil {
				parts = append(parts, cur)
			}
			inPart = !bytes.Equal(line, append(delim, []byte("--")...))
			cur = nil
			continue
		}
		if inPart {
			cur = append(cur, line...)
			cur = append(cur, '\n')
		}
	}
	if inPart && cur != nil {
		parts = append(parts, cur)
	}
	return parts
}
