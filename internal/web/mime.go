package web

import (
	"encoding/base64"
	"strings"
	"time"
)

func rfc2822Now() string {
	return time.Now().Format("Mon, 02 Jan 2006 15:04:05 -0700")
}

func nowMillis() int64 { return time.Now().UnixNano() / 1e6 }

// mimeWordEncode encodes a header word for non-ASCII subjects (RFC 2047).
func mimeWordEncode(s string) string {
	if s == "" || isASCII(s) {
		return s
	}
	return "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func b64Wrap(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}

func isASCII(s string) bool {
	for _, c := range s {
		if c > 127 {
			return false
		}
	}
	return true
}

func parseRcpts(parts ...string) []string {
	var out []string
	for _, p := range parts {
		for _, x := range strings.Split(p, ",") {
			x = strings.ToLower(strings.TrimSpace(x))
			if strings.Contains(x, "@") && validAddr(x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// validAddr enforces a strict email character set so that header-injection
// remnants (spaces, colons, control chars) can never become recipients.
func validAddr(a string) bool {
	i := strings.LastIndexByte(a, '@')
	if i <= 0 || i == len(a)-1 {
		return false
	}
	local, domain := a[:i], a[i+1:]
	if strings.Contains(domain, "..") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	okChars := func(s, extra string) bool {
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case strings.ContainsRune(extra, r):
			default:
				return false
			}
		}
		return true
	}
	return okChars(local, "._%+=-") && okChars(domain, ".-")
}
