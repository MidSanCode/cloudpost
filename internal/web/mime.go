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
