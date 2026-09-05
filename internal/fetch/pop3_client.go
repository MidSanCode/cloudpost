package fetch

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"cloudpost/internal/mailstore"
)

// pop3Client is a minimal RFC 1939 POP3 client.
type pop3Client struct {
	conn net.Conn
	br   *bufio.Reader
}

func pop3Connect(host string, port int, tlsMode string) (*pop3Client, error) {
	if port == 0 {
		if tlsMode == "none" || tlsMode == "starttls" {
			port = 110
		} else {
			port = 995
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	raw, err := net.DialTimeout("tcp", addr, 20*time.Second)
	if err != nil {
		return nil, err
	}
	// Verify server certificates (ServerName pinned to the configured host).
	tlsConf := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	c := &pop3Client{conn: raw, br: bufio.NewReaderSize(raw, 32<<10)}
	if _, err := c.readLine(); err != nil {
		c.close()
		return nil, fmt.Errorf("greeting: %w", err)
	}
	switch tlsMode {
	case "ssl":
		tc := tls.Client(raw, tlsConf)
		if err := tc.Handshake(); err != nil {
			c.close()
			return nil, fmt.Errorf("tls: %w", err)
		}
		c.conn = tc
		c.br = bufio.NewReaderSize(tc, 32<<10)
	case "starttls":
		if err := c.cmd("STLS"); err != nil {
			c.close()
			return nil, fmt.Errorf("stls: %w", err)
		}
		tc := tls.Client(raw, tlsConf)
		if err := tc.Handshake(); err != nil {
			c.close()
			return nil, fmt.Errorf("tls: %w", err)
		}
		c.conn = tc
		c.br = bufio.NewReaderSize(tc, 32<<10)
	}
	return c, nil
}

func (c *pop3Client) close() { _ = c.conn.Close() }

func (c *pop3Client) readLine() (string, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// cmd sends a command and checks +OK status.
func (c *pop3Client) cmd(format string, args ...any) error {
	_ = c.conn.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := fmt.Fprintf(c.conn, format+"\r\n", args...); err != nil {
		return err
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "-ERR") {
		return fmt.Errorf("%s", strings.TrimPrefix(line, "-ERR "))
	}
	if !strings.HasPrefix(line, "+OK") {
		return fmt.Errorf("unexpected: %q", line)
	}
	return nil
}

// readMultiline reads a dot-terminated response.
func (c *pop3Client) readMultiline() ([]string, error) {
	var lines []string
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if line == "." {
			return lines, nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		lines = append(lines, line)
	}
}

// AUTH PLAIN for servers requiring it; falls back to USER/PASS.
func (c *pop3Client) login(user, pass string) error {
	if err := c.cmd("AUTH PLAIN %s", base64.StdEncoding.EncodeToString([]byte("\x00"+user+"\x00"+pass))); err == nil {
		return nil
	}
	if err := c.cmd("USER %s", user); err != nil {
		return fmt.Errorf("user: %w", err)
	}
	if err := c.cmd("PASS %s", pass); err != nil {
		return fmt.Errorf("pass: %w", err)
	}
	return nil
}

// fetchPOP3 pulls new messages from a remote POP3 account.
func (f *Fetcher) fetchPOP3(a *mailstore.Account) (int, error) {
	c, err := pop3Connect(a.RemoteHost, a.RemotePort, a.RemoteTLS)
	if err != nil {
		return 0, err
	}
	defer c.close()
	if err := c.login(a.RemoteUser, a.RemotePass); err != nil {
		return 0, fmt.Errorf("login: %w", err)
	}
	// UIDL listing.
	if err := c.cmd("UIDL"); err != nil {
		return 0, fmt.Errorf("uidl: %w", err)
	}
	uidlLines, err := c.readMultiline()
	if err != nil {
		return 0, err
	}
	type msgRef struct {
		seq  int
		uidl string
	}
	var refs []msgRef
	for _, line := range uidlLines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		seq, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		refs = append(refs, msgRef{seq: seq, uidl: fields[1]})
	}
	seen := f.seenUIDLs(a.ID)
	count := 0
	for _, ref := range refs {
		if seen[ref.uidl] {
			continue
		}
		if err := c.cmd("RETR %d", ref.seq); err != nil {
			continue
		}
		lines, err := c.readMultiline()
		if err != nil {
			return count, err
		}
		raw := []byte(strings.Join(lines, "\r\n") + "\r\n")
		if _, _, err := f.deliverWithFilters(a, raw, 0); err != nil {
			return count, fmt.Errorf("store: %w", err)
		}
		count++
		seen[ref.uidl] = true
		if count%20 == 0 {
			f.saveUIDLs(a.ID, seen)
		}
		if !a.FetchKeepOnServer {
			_ = c.cmd("DELE %d", ref.seq)
		}
	}
	f.saveUIDLs(a.ID, seen)
	_ = c.cmd("QUIT")
	return count, nil
}
