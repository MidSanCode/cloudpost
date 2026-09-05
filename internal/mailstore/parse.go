package mailstore

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/ianaindex"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// ParsedInfo carries the header/body facts the store keeps about a message.
type ParsedInfo struct {
	MessageID  string
	InReplyTo  string
	FromName   string
	FromAddr   string
	To         []string
	CC         []string
	Subject    string
	SentAt     int64
	InternalAt int64
	Flags      []string
	Snippet    string
	TextBody   string
	HTMLBody   string
	HasAttach  bool
	// Attachments lists non-body MIME parts in document order. Content is
	// never included; it is fetched on demand via ExtractAttachment.
	Attachments []*Attachment
}

// Attachment describes one non-body MIME part.
type Attachment struct {
	Part        int    `json:"part"` // leaf-part index for ExtractAttachment
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	CID         string `json:"cid,omitempty"`    // Content-ID (inline images), <>-stripped
	Inline      bool   `json:"inline,omitempty"` // Content-Disposition: inline
}

var tagRe = regexp.MustCompile(`<[^>]*>`)
var wsRe = regexp.MustCompile(`\s+`)

// ParseHeaders parses an RFC 5322 message into storage metadata.
func ParseHeaders(raw []byte) *ParsedInfo {
	info := &ParsedInfo{InternalAt: time.Now().Unix()}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		info.Snippet = clip(cleanText(string(raw)), 280)
		return info
	}
	h := msg.Header
	info.MessageID = strings.Trim(h.Get("Message-Id"), "<> \t")
	info.InReplyTo = strings.Trim(h.Get("In-Reply-To"), "<> \t")

	if v := h.Get("Subject"); v != "" {
		info.Subject = clip(decodeHeader(v), 500)
	}
	if addr := parseAddressList(h.Get("From")); len(addr) > 0 {
		info.FromName, info.FromAddr = addr[0].Name, addr[0].Address
	}
	info.To = addressList(h.Get("To"))
	info.CC = addressList(h.Get("Cc"))

	if t, err := mail.ParseDate(h.Get("Date")); err == nil {
		info.SentAt = t.Unix()
	}

	seen := map[string]bool{}
	var leaves []mimeLeaf
	collectLeaves(textproto.MIMEHeader(h), msg.Body, &leaves, 0)
	for i, lf := range leaves {
		pct := lf.header.Get("Content-Type")
		mt := partMediaType(pct)
		dispHeader := lf.header.Get("Content-Disposition")
		disp := strings.ToLower(strings.SplitN(dispHeader, ";", 2)[0])
		filename := parseParams(pct)["name"]
		if fn := parseParams(dispHeader)["filename"]; fn != "" {
			filename = fn
		}
		cid := strings.Trim(lf.header.Get("Content-Id"), "<> \t")
		isBody := (mt == "text/plain" || mt == "text/html") && filename == ""
		if !isBody {
			info.HasAttach = true
			name := filename
			if name == "" && cid != "" {
				name = cid
			}
			if name == "" {
				name = "attachment-" + strconv.Itoa(i+1)
			}
			info.Attachments = append(info.Attachments, &Attachment{
				Part: i, Filename: name, ContentType: mt,
				Size: int64(len(lf.body)), CID: cid, Inline: disp == "inline",
			})
			continue
		}
		body := decodeCharset(parseParams(pct)["charset"], lf.body)
		switch mt {
		case "text/plain":
			if !seen["text"] {
				info.TextBody += string(body)
				seen["text"] = true
			}
		case "text/html":
			if !seen["html"] {
				info.HTMLBody += string(body)
				seen["html"] = true
			}
		}
	}
	info.Snippet = clip(firstNonEmpty(cleanText(info.TextBody), cleanText(stripHTML(info.HTMLBody))), 280)
	return info
}

// mimeLeaf is one non-multipart MIME part with CTE-decoded (binary-safe,
// charset-untouched) content.
type mimeLeaf struct {
	header textproto.MIMEHeader
	body   []byte
}

// collectLeaves walks the MIME tree in document order collecting every leaf
// part (depth-capped, per-part size-capped). The ordering is stable and is
// the index space shared by ParsedInfo.Attachments and ExtractAttachment.
func collectLeaves(h textproto.MIMEHeader, r io.Reader, out *[]mimeLeaf, depth int) {
	if depth > 8 {
		return
	}
	ct := h.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "multipart/") {
		boundary := parseParams(ct)["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(r, boundary)
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return
			}
			pct := p.Header.Get("Content-Type")
			if strings.HasPrefix(strings.ToLower(pct), "multipart/") {
				collectLeaves(p.Header, p, out, depth+1)
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(p, 20<<20))
			*out = append(*out, mimeLeaf{header: p.Header, body: decodeCTE(p.Header.Get("Content-Transfer-Encoding"), body)})
		}
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r, 20<<20))
	*out = append(*out, mimeLeaf{header: h, body: decodeCTE(h.Get("Content-Transfer-Encoding"), body)})
}

// ExtractPart returns the CTE-decoded content of leaf part `part`
// (same ordering as ParsedInfo.Attachments) plus its filename and type.
func ExtractPart(raw []byte, part int) (content []byte, filename, ctype string, err error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, "", "", fmt.Errorf("parse message: %w", err)
	}
	var leaves []mimeLeaf
	collectLeaves(textproto.MIMEHeader(msg.Header), msg.Body, &leaves, 0)
	if part < 0 || part >= len(leaves) {
		return nil, "", "", fmt.Errorf("no such attachment")
	}
	lf := leaves[part]
	filename = parseParams(lf.header.Get("Content-Disposition"))["filename"]
	if filename == "" {
		filename = parseParams(lf.header.Get("Content-Type"))["name"]
	}
	if filename == "" {
		filename = "attachment"
	}
	return lf.body, filename, partMediaType(lf.header.Get("Content-Type")), nil
}

type parsedAddr struct{ Name, Address string }

func parseAddressList(s string) []parsedAddr {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []parsedAddr
	list, err := mail.ParseAddressList(decodeHeader(s))
	if err != nil {
		for _, part := range strings.Split(s, ",") {
			if a, err := mail.ParseAddress(strings.TrimSpace(part)); err == nil {
				out = append(out, parsedAddr{Name: decodeHeader(a.Name), Address: strings.ToLower(a.Address)})
			}
		}
		return out
	}
	for _, a := range list {
		out = append(out, parsedAddr{Name: decodeHeader(a.Name), Address: strings.ToLower(a.Address)})
	}
	return out
}

func addressList(s string) []string {
	var out []string
	for _, a := range parseAddressList(s) {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

func partMediaType(ct string) string {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if mt == "" {
		return "text/plain"
	}
	return mt
}

func decodeCTE(enc string, b []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		dec := base64.NewDecoder(base64.StdEncoding, bytes.NewReader(b))
		if d, err := io.ReadAll(dec); err == nil {
			return d
		}
		// Tolerate padding errors: strip whitespace and retry per-line.
		clean := wsRe.ReplaceAllString(string(b), "")
		if d, err := base64.StdEncoding.DecodeString(clean); err == nil {
			return d
		}
	case "quoted-printable":
		if d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(b))); err == nil {
			return d
		}
	}
	return b
}

// decodeHeader decodes RFC 2047 encoded words with x/text charset support.
func decodeHeader(s string) string {
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	if out, err := dec.DecodeHeader(s); err == nil && utf8.ValidString(out) {
		return out
	}
	return s
}

// encodingFor resolves a MIME charset label to an x/text encoding,
// with explicit handling for East-Asian labels IANA misses.
func encodingFor(charset string) (encoding.Encoding, error) {
	cs := strings.ToLower(strings.TrimSpace(charset))
	switch cs {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return unicode.UTF8, nil
	case "gb2312", "gbk", "cp936", "gb_2312":
		return simplifiedchinese.GBK, nil
	case "gb18030":
		return simplifiedchinese.GB18030, nil
	case "big5", "big5-hkscs", "csbig5":
		return traditionalchinese.Big5, nil
	case "windows-1252", "cp1252", "x-ansi":
		return charmap.Windows1252, nil
	case "windows-1251", "cp1251":
		return charmap.Windows1251, nil
	case "iso-8859-1", "latin1", "cp819":
		return charmap.ISO8859_1, nil
	case "iso-8859-15", "latin9":
		return charmap.ISO8859_15, nil
	case "koi8-r":
		return charmap.KOI8R, nil
	case "euc-kr", "ksc5601", "ks_c_5601-1987":
		return korean.EUCKR, nil
	case "shift_jis", "sjis", "windows-31j", "cp932":
		return japanese.ShiftJIS, nil
	case "euc-jp":
		return japanese.EUCJP, nil
	case "iso-2022-jp":
		return japanese.ISO2022JP, nil
	}
	if e, err := ianaindex.MIME.Encoding(cs); err == nil && e != nil {
		return e, nil
	}
	if e, err := ianaindex.IANA.Encoding(cs); err == nil && e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("unsupported charset %q", charset)
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	e, err := encodingFor(charset)
	if err != nil {
		return nil, err
	}
	if e == unicode.UTF8 {
		return input, nil
	}
	return e.NewDecoder().Reader(input), nil
}

func decodeCharset(charset string, b []byte) []byte {
	charset = strings.ToLower(strings.TrimSpace(charset))
	if utf8.Valid(b) {
		return b
	}
	// Non-UTF8 bytes: try declared charset then common fallbacks.
	var try []string
	if charset != "" {
		try = append(try, charset)
	}
	try = append(try, "gb18030", "big5", "windows-1252", "iso-8859-15")
	for _, cs := range try {
		e, err := encodingFor(cs)
		if err != nil || e == unicode.UTF8 {
			continue
		}
		if out, err := e.NewDecoder().Bytes(b); err == nil && utf8.Valid(out) {
			return out
		}
	}
	return b
}

func parseParams(v string) map[string]string {
	out := map[string]string{}
	if v == "" {
		return out
	}
	_, params, err := mime.ParseMediaType(v)
	if err != nil {
		for _, kv := range strings.Split(v, ";") {
			if i := strings.IndexByte(kv, '='); i > 0 {
				k := strings.ToLower(strings.TrimSpace(kv[:i]))
				val := strings.Trim(strings.TrimSpace(kv[i+1:]), `"`)
				if k != "" && val != "" {
					out[k] = val
				}
			}
		}
		return out
	}
	for k, val := range params {
		out[strings.ToLower(k)] = val
	}
	return out
}

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return tagRe.ReplaceAllString(s, " ")
}

func cleanText(s string) string { return strings.TrimSpace(wsRe.ReplaceAllString(s, " ")) }

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
