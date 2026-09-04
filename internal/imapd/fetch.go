package imapd

import (
	"bytes"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"cloudpost/internal/mailstore"
)

func isUIDSet(numSet imap.NumSet) bool {
	_, ok := numSet.(imap.UIDSet)
	return ok
}

func numSetContains(numSet imap.NumSet, uid, seq uint32) bool {
	switch set := numSet.(type) {
	case imap.SeqSet:
		return seqSetContains(set, seq)
	case imap.UIDSet:
		return uidSetContains(set, uid)
	}
	return false
}

func flagsToImap(flags []string) []imap.Flag {
	var out []imap.Flag
	for _, f := range flags {
		out = append(out, imap.Flag(f))
	}
	return out
}

func bodyStructureFor(m *mailstore.Message) imap.BodyStructure {
	return &imap.BodyStructureSinglePart{
		Type:     "text",
		Subtype:  "plain",
		Params:   map[string]string{"charset": "utf-8"},
		Encoding: "8bit",
		Size:     uint32(m.Size),
		Text:     &imap.BodyStructureText{NumLines: int64(bytes.Count([]byte(m.Snippet), []byte("\n")) + 1)},
	}
}

// sectionContent slices raw message data for a BODY[...] section.
func sectionContent(raw []byte, section *imap.FetchItemBodySection) []byte {
	var out []byte
	switch {
	case len(section.Part) > 0:
		out = extractPart(raw, section.Part)
		if out == nil && len(section.Part) == 1 {
			_, body := splitHeaderBody(raw)
			out = body
		}
	case section.Specifier == imap.PartSpecifierHeader:
		out, _ = splitHeaderBody(raw)
	case section.Specifier == imap.PartSpecifierText:
		_, out = splitHeaderBody(raw)
	default:
		out = raw
	}
	if section.Specifier == imap.PartSpecifierHeader && len(section.HeaderFields) > 0 {
		out = filterHeaderFields(out, section.HeaderFields, false)
	} else if section.Specifier == imap.PartSpecifierHeader && len(section.HeaderFieldsNot) > 0 {
		out = filterHeaderFields(out, section.HeaderFieldsNot, true)
	}
	if p := section.Partial; p != nil {
		if p.Offset >= int64(len(out)) {
			out = nil
		} else {
			end := p.Offset + p.Size
			if end > int64(len(out)) {
				end = int64(len(out))
			}
			out = out[p.Offset:end]
		}
	}
	return out
}

func filterHeaderFields(header []byte, fields []string, exclude bool) []byte {
	want := map[string]bool{}
	for _, f := range fields {
		want[strings.ToLower(f)] = true
	}
	var kept []string
	for _, line := range strings.Split(string(header), "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			if line == "" {
				continue
			}
			if len(kept) > 0 {
				kept[len(kept)-1] += "\r\n " + strings.TrimSpace(line) // folded continuation
			}
			continue
		}
		name := strings.ToLower(strings.TrimSpace(line[:i]))
		if exclude != want[name] {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\r\n") + "\r\n")
}

// Fetch implements FETCH / UID FETCH.
func (s *session) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	if _, err := s.requireAuth(); err != nil {
		return err
	}
	if s.sel == nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	uidMode := isUIDSet(numSet)
	for i, m := range s.sel.msgs {
		seq := uint32(i + 1)
		if !numSetContains(numSet, m.UID, seq) {
			continue
		}
		fw := w.CreateMessage(seq)
		if options.UID || uidMode {
			fw.WriteUID(imap.UID(m.UID))
		}
		if options.Flags {
			fw.WriteFlags(flagsToImap(m.Flags))
		}
		if options.InternalDate {
			fw.WriteInternalDate(time.Unix(m.InternalAt, 0))
		}
		if options.RFC822Size {
			fw.WriteRFC822Size(m.Size)
		}
		if options.Envelope {
			fw.WriteEnvelope(envelopeFor(m))
		}
		if options.BodyStructure != nil {
			fw.WriteBodyStructure(bodyStructureFor(m))
		}
		for _, section := range options.BodySection {
			raw, _, err := s.deps.Store.GetMessageRaw(s.account.ID, m.ID)
			if err != nil {
				raw = nil
			}
			content := sectionContent(raw, section)
			if !section.Peek {
				_ = s.deps.Store.SetFlags(s.account.ID, []int64{m.ID}, "add", []string{`\Seen`})
				if !hasFlag(m.Flags, `\Seen`) {
					m.Flags = append(m.Flags, `\Seen`)
				}
			}
			wc := fw.WriteBodySection(section, int64(len(content)))
			if len(content) > 0 {
				if _, err := wc.Write(content); err != nil {
					return err
				}
			}
			if err := wc.Close(); err != nil {
				return err
			}
		}
		if err := fw.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Store implements STORE / UID STORE.
func (s *session) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	if _, err := s.requireAuth(); err != nil {
		return err
	}
	if s.sel == nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	mode := "set"
	switch flags.Op {
	case imap.StoreFlagsAdd:
		mode = "add"
	case imap.StoreFlagsDel:
		mode = "remove"
	}
	fl := flagStrings(flags.Flags)
	uidMode := isUIDSet(numSet)
	for i, m := range s.sel.msgs {
		seq := uint32(i + 1)
		if !numSetContains(numSet, m.UID, seq) {
			continue
		}
		if err := s.deps.Store.SetFlags(s.account.ID, []int64{m.ID}, mode, fl); err != nil {
			return err
		}
		updated, err := s.deps.Store.GetMessage(s.account.ID, m.ID)
		if err != nil {
			return err
		}
		m.Flags = updated.Flags
		if !flags.Silent {
			fw := w.CreateMessage(seq)
			if uidMode {
				fw.WriteUID(imap.UID(m.UID))
			}
			fw.WriteFlags(flagsToImap(m.Flags))
			if err := fw.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Copy implements COPY / UID COPY.
func (s *session) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	acc, err := s.requireAuth()
	if err != nil {
		return nil, err
	}
	if s.sel == nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	destFolder, err := s.deps.Store.FolderByName(acc.ID, dest)
	if err != nil {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	uidMode := isUIDSet(numSet)
	var srcUIDs, dstUIDs imap.UIDSet
	for i, m := range s.sel.msgs {
		seq := uint32(i + 1)
		if !numSetContains(numSet, m.UID, seq) {
			continue
		}
		raw, _, err := s.deps.Store.GetMessageRaw(acc.ID, m.ID)
		if err != nil {
			continue
		}
		_, dstUID, err := s.deps.Store.Deliver(acc.ID, destFolder.ID, raw, &mailstore.DeliverOpts{Flags: m.Flags})
		if err != nil {
			continue
		}
		srcUIDs.AddNum(imap.UID(m.UID))
		dstUIDs.AddNum(imap.UID(dstUID))
		_ = uidMode
	}
	return &imap.CopyData{
		UIDValidity: destFolder.UIDValidity,
		SourceUIDs:  srcUIDs,
		DestUIDs:    dstUIDs,
	}, nil
}

// Move implements MOVE / UID MOVE (requires SessionMove capability).
func (s *session) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	acc, err := s.requireAuth()
	if err != nil {
		return err
	}
	if s.sel == nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "No mailbox selected"}
	}
	destFolder, err := s.deps.Store.FolderByName(acc.ID, dest)
	if err != nil {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeTryCreate, Text: "No such mailbox"}
	}
	uidMode := isUIDSet(numSet)
	var srcUIDs, dstUIDs imap.UIDSet
	var movedIDs []int64
	var movedSeqs []uint32
	for i, m := range s.sel.msgs {
		seq := uint32(i + 1)
		if !numSetContains(numSet, m.UID, seq) {
			continue
		}
		raw, _, err := s.deps.Store.GetMessageRaw(acc.ID, m.ID)
		if err != nil {
			continue
		}
		_, dstUID, err := s.deps.Store.Deliver(acc.ID, destFolder.ID, raw, &mailstore.DeliverOpts{Flags: m.Flags})
		if err != nil {
			continue
		}
		srcUIDs.AddNum(imap.UID(m.UID))
		dstUIDs.AddNum(imap.UID(dstUID))
		movedIDs = append(movedIDs, m.ID)
		movedSeqs = append(movedSeqs, seq)
	}
	if len(movedIDs) == 0 {
		return nil
	}
	if err := s.deps.Store.DeleteMessages(acc.ID, movedIDs); err != nil {
		return err
	}
	_ = uidMode
	if err := w.WriteCopyData(&imap.CopyData{
		UIDValidity: destFolder.UIDValidity,
		SourceUIDs:  srcUIDs,
		DestUIDs:    dstUIDs,
	}); err != nil {
		return err
	}
	// Report expunges in ascending sequence order.
	removed := map[int64]bool{}
	for _, id := range movedIDs {
		removed[id] = true
	}
	var keep []*mailstore.Message
	seq := uint32(0)
	expungeSeqs := map[uint32]bool{}
	for _, sseq := range movedSeqs {
		expungeSeqs[sseq] = true
	}
	for i, m := range s.sel.msgs {
		seq = uint32(i + 1)
		if removed[m.ID] {
			if err := w.WriteExpunge(seq); err != nil {
				return err
			}
			continue
		}
		keep = append(keep, m)
	}
	s.sel.msgs = keep
	return nil
}
