package web

import (
	"regexp"
	"strings"
)

// Mail HTML sanitizer: layered defense on top of the sandbox="" iframe.
// The sandbox already blocks script execution; this strips obvious XSS
// payloads so the HTML is also safe when copied/inspected, and injects a
// CSP <meta> into the framed document that blocks remote content (tracking
// pixels) until the user opts in.

var (
	reScriptBlock = regexp.MustCompile(`(?is)<\s*script[^>]*>.*?<\s*/\s*script\s*>`)
	reScriptTag   = regexp.MustCompile(`(?is)<\s*/?\s*script[^>]*>`)
	reDangerTag   = regexp.MustCompile(`(?is)<\s*/?\s*(iframe|object|embed|applet|base|form|meta\s+http-equiv\s*=\s*("|')?refresh)[^>]*>`)
	reOnAttr      = regexp.MustCompile(`(?is)\son[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reBadURL      = regexp.MustCompile(`(?is)\s(href|src|action|formaction|xlink:href|poster|background)\s*=\s*("|')?\s*(javascript|vbscript|data\s*:\s*text/html)[^"'>\s]*`)
	reRemoteRef   = regexp.MustCompile(`(?i)(src|background)\s*=\s*("|')?\s*(https?:)?//`)
)

// sanitizeMailHTML strips active content and injects a CSP meta tag.
// cidData maps Content-ID (<>-stripped, case-preserved) to a data: URI for
// small inline images. Returns the sanitized HTML and whether the mail
// references remote (http(s)) resources that got blocked.
func sanitizeMailHTML(html string, allowRemote bool, cidData map[string]string) (string, bool) {
	s := reScriptBlock.ReplaceAllString(html, "")
	s = reScriptTag.ReplaceAllString(s, "")
	s = reDangerTag.ReplaceAllString(s, "")
	s = reOnAttr.ReplaceAllString(s, "")
	s = reBadURL.ReplaceAllString(s, ` $1="#"`)
	// Inline (cid:) images: rewrite to data: URIs so embedded images render
	// without network access.
	for cid, du := range cidData {
		s = strings.ReplaceAll(s, "cid:"+cid, du)
		s = strings.ReplaceAll(s, "CID:"+cid, du)
	}
	hasRemote := reRemoteRef.MatchString(s)
	csp := "default-src 'none'; style-src 'unsafe-inline' data:; img-src data:; font-src data:; form-action 'none'"
	if allowRemote {
		csp = "default-src 'none'; style-src 'unsafe-inline' data:; img-src data: https: http:; font-src data:; form-action 'none'"
	}
	meta := `<meta http-equiv="Content-Security-Policy" content="` + csp + `">`
	if idx := strings.Index(s, "<head"); idx >= 0 {
		if close := strings.Index(s[idx:], ">"); close >= 0 {
			at := idx + close + 1
			return s[:at] + meta + s[at:], hasRemote
		}
	}
	return meta + s, hasRemote
}

// sanitizeFilename strips control characters and path separators so an
// attachment name can never smuggle header or path content.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || r == '"' || r == '\\' || r == '/' {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "attachment"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}
