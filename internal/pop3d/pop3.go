// Package pop3d implements a minimal RFC 1939 POP3 server.
package pop3d

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudpost/internal/mailstore"
	"cloudpost/internal/state"
)

// Deps wires the server to the store.
type Deps struct {
	Store *mailstore.Store
	State *state.State
}

type popMsg struct {
	id      int64
	uid     uint32
	size    int64
	deleted bool
}

type popConn struct {
	deps       Deps
	conn       net.Conn
	br         *bufio.Reader
	account    *mailstore.Account
	lastUser   string
	msgs       []popMsg
	deletedAny bool
}

// Server is the POP3 listener.
type Server struct {
	deps   Deps
	ln     net.Listener
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
}

// New builds a POP3 server.
func New(deps Deps) *Server { return &Server{deps: deps} }

// Listen binds the listener.
func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Serve accepts connections; blocks.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(conn)
		}()
	}
}

// Close shuts down: stops the listener and waits up to 2s for active
// connections to drain (so port rebinding is not blocked by idle clients).
func (s *Server) Close() {
	s.mu.Lock()
	alreadyClosed := s.closed
	s.closed = true
	s.mu.Unlock()
	if alreadyClosed || s.ln == nil {
		return
	}
	_ = s.ln.Close()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
	pc := &popConn{deps: s.deps, conn: conn, br: bufio.NewReaderSize(conn, 64<<10)}
	pc.writeLine("+OK cloudpost POP3 service ready")
	for {
		line, err := pc.readLine()
		if err != nil {
			return
		}
		if !pc.dispatch(line) {
			return
		}
	}
}

func (pc *popConn) dispatch(line string) bool {
	cmd, arg := splitCmd(line)
	switch strings.ToUpper(cmd) {
	case "CAPA":
		pc.writeLine("+OK Capability list follows")
		pc.writeLine("USER")
		pc.writeLine("TOP")
		pc.writeLine("UIDL")
		pc.writeLine("PIPELINING")
		pc.writeLine("IMPLEMENTATION cloudpost")
		pc.writeLine(".")
	case "USER":
		pc.account = nil
		pc.lastUser = strings.TrimSpace(arg)
		pc.writeLine("+OK send PASS")
	case "PASS":
		user := pc.lastUser
		if user == "" {
			pc.writeLine("-ERR USER required before PASS")
			return true
		}
		acc, ok := pc.deps.Store.VerifyLocalLogin(user, arg)
		if !ok {
			time.Sleep(400 * time.Millisecond) // slow brute force
			pc.writeLine("-ERR [AUTH] invalid credentials")
			return true
		}
		pc.account = acc
		if err := pc.loadMailbox(); err != nil {
			pc.writeLine("-ERR mailbox load failed")
			return true
		}
		pc.writeLine(fmt.Sprintf("+OK mailbox open, %d messages", len(pc.msgs)))
	case "STAT":
		if pc.account == nil {
			pc.writeLine("-ERR not authenticated")
			return true
		}
		var n, octets int64
		for _, m := range pc.msgs {
			if !m.deleted {
				n++
				octets += m.size
			}
		}
		pc.writeLine(fmt.Sprintf("+OK %d %d", n, octets))
	case "LIST":
		if pc.account == nil {
			pc.writeLine("-ERR not authenticated")
			return true
		}
		if arg != "" {
			idx, err := strconv.Atoi(arg)
			if err != nil || idx < 1 || idx > len(pc.msgs) || pc.msgs[idx-1].deleted {
				pc.writeLine("-ERR no such message")
				return true
			}
			pc.writeLine(fmt.Sprintf("+OK %d %d", idx, pc.msgs[idx-1].size))
			return true
		}
		var octets int64
		var n int
		for _, m := range pc.msgs {
			if !m.deleted {
				octets += m.size
				n++
			}
		}
		pc.writeLine(fmt.Sprintf("+OK %d messages (%d octets)", n, octets))
		for i, m := range pc.msgs {
			if !m.deleted {
				pc.writeLine(fmt.Sprintf("%d %d", i+1, m.size))
			}
		}
		pc.writeLine(".")
	case "UIDL":
		if pc.account == nil {
			pc.writeLine("-ERR not authenticated")
			return true
		}
		if arg != "" {
			idx, err := strconv.Atoi(arg)
			if err != nil || idx < 1 || idx > len(pc.msgs) || pc.msgs[idx-1].deleted {
				pc.writeLine("-ERR no such message")
				return true
			}
			pc.writeLine(fmt.Sprintf("+OK %d %d", idx, pc.msgs[idx-1].uid))
			return true
		}
		pc.writeLine("+OK unique-id listing follows")
		for i, m := range pc.msgs {
			if !m.deleted {
				pc.writeLine(fmt.Sprintf("%d %d", i+1, m.uid))
			}
		}
		pc.writeLine(".")
	case "RETR":
		if !pc.requireAuth() {
			return true
		}
		idx, ok := pc.parseIdx(arg)
		if !ok {
			return true
		}
		raw, err := pc.fetchRaw(idx)
		if err != nil {
			pc.writeLine("-ERR read failed")
			return true
		}
		pc.writeLine(fmt.Sprintf("+OK %d octets", len(raw)))
		pc.writeData(raw)
	case "TOP":
		if !pc.requireAuth() {
			return true
		}
		fields := strings.Fields(arg)
		if len(fields) != 2 {
			pc.writeLine("-ERR TOP requires two arguments")
			return true
		}
		idx, ok := pc.parseIdx(fields[0])
		if !ok {
			return true
		}
		k, err := strconv.Atoi(fields[1])
		if err != nil || k < 0 {
			pc.writeLine("-ERR bad line count")
			return true
		}
		raw, err := pc.fetchRaw(idx)
		if err != nil {
			pc.writeLine("-ERR read failed")
			return true
		}
		pc.writeLine("+OK top of message follows")
		pc.writeTop(raw, k)
	case "DELE":
		if !pc.requireAuth() {
			return true
		}
		idx, ok := pc.parseIdx(arg)
		if !ok {
			return true
		}
		pc.msgs[idx-1].deleted = true
		pc.deletedAny = true
		pc.writeLine(fmt.Sprintf("+OK message %d deleted", idx))
	case "RSET":
		for i := range pc.msgs {
			pc.msgs[i].deleted = false
		}
		pc.deletedAny = false
		pc.writeLine("+OK")
	case "NOOP":
		pc.writeLine("+OK")
	case "QUIT":
		if pc.account != nil && pc.deletedAny {
			folder, err := pc.deps.Store.FolderByName(pc.account.ID, "INBOX")
			if err == nil {
				var ids []int64
				for _, m := range pc.msgs {
					if m.deleted {
						ids = append(ids, m.id)
					}
				}
				for _, id := range ids {
					_ = pc.deps.Store.SetFlags(pc.account.ID, []int64{id}, "add", []string{`\Deleted`})
				}
				_, _ = pc.deps.Store.ExpungeFolder(pc.account.ID, folder.ID)
			}
		}
		pc.writeLine("+OK cloudpost POP3 signing off")
		return false
	default:
		pc.writeLine("-ERR unknown command")
	}
	return true
}

func (pc *popConn) requireAuth() bool {
	if pc.account == nil {
		pc.writeLine("-ERR not authenticated")
		return false
	}
	return true
}

func (pc *popConn) parseIdx(arg string) (int, bool) {
	idx, err := strconv.Atoi(arg)
	if err != nil || idx < 1 || idx > len(pc.msgs) || pc.msgs[idx-1].deleted {
		pc.writeLine("-ERR no such message")
		return 0, false
	}
	return idx, true
}

func (pc *popConn) loadMailbox() error {
	folder, err := pc.deps.Store.FolderByName(pc.account.ID, "INBOX")
	if err != nil {
		return err
	}
	msgs, _, err := pc.deps.Store.ListMessages(pc.account.ID, folder.ID, 0, 10000, false)
	if err != nil {
		return err
	}
	pc.msgs = pc.msgs[:0]
	for _, m := range msgs {
		pc.msgs = append(pc.msgs, popMsg{id: m.ID, uid: m.UID, size: m.Size})
	}
	return nil
}

func (pc *popConn) fetchRaw(idx int) ([]byte, error) {
	m := pc.msgs[idx-1]
	raw, _, err := pc.deps.Store.GetMessageRaw(pc.account.ID, m.id)
	return raw, err
}

func (pc *popConn) writeData(raw []byte) {
	// Dot-stuffing per RFC 1939.
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	var buf strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, ".") {
			buf.WriteString(".")
		}
		buf.WriteString(line)
		buf.WriteString("\r\n")
	}
	buf.WriteString(".\r\n")
	pc.writeLine(buf.String())
}

func (pc *popConn) writeTop(raw []byte, k int) {
	text := string(raw)
	headerEnd := strings.Index(text, "\r\n\r\n")
	sepLen := 4
	if headerEnd < 0 {
		headerEnd = strings.Index(text, "\n\n")
		sepLen = 2
	}
	var out string
	if headerEnd < 0 {
		out = text
	} else {
		out = text[:headerEnd+sepLen]
		body := text[headerEnd+sepLen:]
		lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
		if k > len(lines) {
			k = len(lines)
		}
		out += strings.Join(lines[:k], "\r\n")
		if k > 0 {
			out += "\r\n"
		}
	}
	pc.writeData([]byte(out))
}

func (pc *popConn) readLine() (string, error) {
	line, err := pc.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (pc *popConn) writeLine(s string) {
	if !strings.HasSuffix(s, "\r\n") {
		s += "\r\n"
	}
	_, _ = io.WriteString(pc.conn, s)
}

func splitCmd(line string) (cmd, arg string) {
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return line[:i], strings.TrimSpace(line[i+1:])
	}
	return line, ""
}

var _ = time.Now
