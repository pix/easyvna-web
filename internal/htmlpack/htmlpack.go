// Package htmlpack builds the single-file browser version of EasyVNA Web:
// one HTML page holding the UI (index.html body, style.css, app.js), the
// browser host (host.js, host.css), Go's wasm_exec.js and the app compiled
// to WebAssembly (gzip + base64, decoded by host.js at load time).
//
// The page is a fixed skeleton plus raw-text blocks (<script> and <style>
// elements, whose text the HTML parser keeps byte for byte). The skeleton
// itself is stored as JSON in one of the blocks, so the running page can
// rebuild its own exact file ("Download for offline use") from the DOM.
package htmlpack

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// Inputs are the files that go into the page.
type Inputs struct {
	IndexHTML  []byte // web/static/index.html
	StyleCSS   []byte // web/static/style.css
	AppJS      []byte // web/static/app.js
	HostCSS    []byte // web/browser/host.css
	HostJS     []byte // web/browser/host.js
	WasmExecJS []byte // $(go env GOROOT)/lib/wasm/wasm_exec.js
	Wasm       []byte // GOOS=js GOARCH=wasm build of ./cmd/easyvna-wasm
	Version    string
}

// Skeleton is the JSON stored in the #ev-skel block: the page is
// Parts[0] + text(Ids[0]) + Parts[1] + ... + text(Ids[n-1]) + Parts[n].
type Skeleton struct {
	IDs   []string `json:"ids"`
	Parts []string `json:"parts"`
}

type block struct {
	id, open, content, close string
}

var (
	bodyRe    = regexp.MustCompile(`(?s)<body[^>]*>(.*)</body>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	appTagRe  = regexp.MustCompile(`<script\s+src="app\.js"\s*>\s*</script>\s*`)
	iconRe    = regexp.MustCompile(`<link rel="icon"[^>]*>`)
	titleRe   = regexp.MustCompile(`<title>([^<]*)</title>`)
)

// gz compresses deterministically (no name, no time stamp).
func gz(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wrap breaks s into lines of n characters (atob ignores the newlines).
func wrap(s string, n int) string {
	var b strings.Builder
	for len(s) > n {
		b.WriteString(s[:n])
		b.WriteByte('\n')
		s = s[n:]
	}
	b.WriteString(s)
	return b.String()
}

// check rejects content the HTML parser would not keep verbatim in a
// raw-text element (or that would end the element early).
func check(id, tag, content string) error {
	low := strings.ToLower(content)
	switch {
	case strings.ContainsRune(content, '\r'):
		return fmt.Errorf("%s: contains a CR (the parser turns CRLF into LF)", id)
	case strings.ContainsRune(content, 0):
		return fmt.Errorf("%s: contains a NUL byte", id)
	case strings.Contains(low, "</"+tag):
		return fmt.Errorf("%s: contains </%s", id, tag)
	case tag == "script" && strings.Contains(low, "<!--"):
		return fmt.Errorf("%s: contains <!-- (would change how <script> is parsed)", id)
	}
	return nil
}

// Build returns the page.
func Build(in Inputs) ([]byte, error) {
	if len(in.Wasm) < 8 || !bytes.Equal(in.Wasm[:4], []byte("\x00asm")) {
		return nil, errors.New("htmlpack: Wasm is not a WebAssembly module")
	}
	idx := string(in.IndexHTML)
	m := bodyRe.FindStringSubmatch(idx)
	if m == nil {
		return nil, errors.New("htmlpack: index.html has no <body>")
	}
	if !appTagRe.MatchString(m[1]) {
		return nil, errors.New(`htmlpack: index.html does not load <script src="app.js">`)
	}
	body := appTagRe.ReplaceAllString(commentRe.ReplaceAllString(m[1], ""), "")
	if strings.Contains(strings.ToLower(body), "<script") {
		return nil, errors.New("htmlpack: index.html body has another <script>")
	}
	icon := iconRe.FindString(idx)
	title := "EasyVNA Web"
	if t := titleRe.FindStringSubmatch(idx); t != nil {
		title = t[1]
	}
	z, err := gz(in.Wasm)
	if err != nil {
		return nil, err
	}
	b64 := wrap(base64.StdEncoding.EncodeToString(z), 4096)

	head := "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<meta name=\"description\" content=\"EasyVNA Web: check and trim LoRa antennas with a NanoVNA, in the browser (Web Serial), with a built-in simulator.\">\n" +
		"<meta name=\"generator\" content=\"EasyVNA Web " + html.EscapeString(in.Version) + "\">\n" +
		"<title>" + title + "</title>\n"
	if icon != "" {
		head += icon + "\n"
	}
	loader := "<div id=\"ev-loading\" class=\"ev-loading\" role=\"status\"><div class=\"ev-spin\" aria-hidden=\"true\"></div>" +
		"<p id=\"ev-loading-text\">Loading EasyVNA Web…</p><noscript><p>EasyVNA Web needs JavaScript.</p></noscript></div>\n"
	blocks := []block{
		{"ev-css", head + `<style id="ev-css">`, string(in.StyleCSS), "</style>\n"},
		{"ev-hostcss", `<style id="ev-hostcss">`, string(in.HostCSS), "</style>\n</head>\n<body>\n" + loader},
		{"ev-body", `<script id="ev-body" type="text/html">`, body, "</script>\n"},
		{"ev-app", `<script id="ev-app" type="text/plain">`, string(in.AppJS), "</script>\n"},
		{"ev-wasmexec", `<script id="ev-wasmexec">`, string(in.WasmExecJS), "</script>\n"},
		{"ev-wasm", `<script id="ev-wasm" type="application/octet-stream">`, b64, "</script>\n"},
		{"ev-skel", `<script id="ev-skel" type="application/json">`, "", "</script>\n"},
		{"ev-host", `<script id="ev-host">`, string(in.HostJS), "</script>\n</body>\n</html>\n"},
	}
	sk := Skeleton{Parts: []string{""}}
	for _, b := range blocks {
		sk.IDs = append(sk.IDs, b.id)
		sk.Parts[len(sk.Parts)-1] += b.open
		sk.Parts = append(sk.Parts, b.close)
	}
	skj, err := json.Marshal(sk) // escapes <, > and & as <...
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	for i := range blocks {
		b := &blocks[i]
		if b.id == "ev-skel" {
			b.content = string(skj)
		}
		tag := "script"
		if strings.HasSuffix(b.open, `<style id="`+b.id+`">`) {
			tag = "style"
		}
		if err := check(b.id, tag, b.content); err != nil {
			return nil, fmt.Errorf("htmlpack: %w", err)
		}
		out.WriteString(sk.Parts[i])
		out.WriteString(b.content)
	}
	out.WriteString(sk.Parts[len(sk.Parts)-1])
	return []byte(out.String()), nil
}

// Rebuild reassembles a page from its block texts, as host.js does in the
// browser (selfHTML). texts maps block id to text content.
func Rebuild(sk Skeleton, texts map[string]string) (string, error) {
	if len(sk.Parts) != len(sk.IDs)+1 {
		return "", errors.New("htmlpack: malformed skeleton")
	}
	var b strings.Builder
	b.WriteString(sk.Parts[0])
	for i, id := range sk.IDs {
		t, ok := texts[id]
		if !ok {
			return "", fmt.Errorf("htmlpack: block %s missing", id)
		}
		b.WriteString(t)
		b.WriteString(sk.Parts[i+1])
	}
	return b.String(), nil
}
