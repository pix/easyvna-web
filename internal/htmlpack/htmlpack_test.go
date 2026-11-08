package htmlpack

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rawText extracts the text of a <script>/<style> element by id the way
// the HTML parser does for raw-text elements: everything up to the first
// matching end tag.
func rawText(t *testing.T, page, id string) string {
	t.Helper()
	re := regexp.MustCompile(`<(script|style) id="` + regexp.QuoteMeta(id) + `"[^>]*>`)
	loc := re.FindStringSubmatchIndex(page)
	if loc == nil {
		t.Fatalf("block %s not found", id)
	}
	tag := page[loc[2]:loc[3]]
	rest := page[loc[1]:]
	end := strings.Index(strings.ToLower(rest), "</"+tag)
	if end < 0 {
		t.Fatalf("block %s not closed", id)
	}
	return rest[:end]
}

func repoInputs(t *testing.T) Inputs {
	t.Helper()
	read := func(p string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", p))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	wasm := append([]byte("\x00asm\x01\x00\x00\x00"), bytes.Repeat([]byte("payload"), 1000)...)
	manifest := read("web/static/firmware/manifest.json")
	var m FirmwareManifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	return Inputs{IndexHTML: read("web/static/index.html"), StyleCSS: read("web/static/style.css"), AppJS: read("web/static/app.js"),
		FlasherJS: read("web/static/flasher.js"), FlasherCSS: read("web/static/flasher.css"),
		FirmwareManifest: manifest, Firmware: read("web/static/firmware/" + m.File),
		HostCSS: read("web/browser/host.css"), HostJS: read("web/browser/host.js"),
		WasmExecJS: []byte("globalThis.Go = class {};\n"), Wasm: wasm, Version: "v9.9.9-test"}
}

func TestBuildRoundTrip(t *testing.T) {
	in := repoInputs(t)
	page, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	p := string(page)
	var sk Skeleton
	if err := json.Unmarshal([]byte(rawText(t, p, "ev-skel")), &sk); err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	for _, id := range sk.IDs {
		texts[id] = rawText(t, p, id)
	}
	got, err := Rebuild(sk, texts)
	if err != nil {
		t.Fatal(err)
	}
	if got != p {
		t.Fatal("page rebuilt from its blocks differs from the file")
	}
	if texts["ev-app"] != lf(in.AppJS) || texts["ev-host"] != lf(in.HostJS) || texts["ev-css"] != lf(in.StyleCSS) ||
		texts["ev-flasher"] != lf(in.FlasherJS) || texts["ev-flashcss"] != lf(in.FlasherCSS) {
		t.Fatal("blocks are not verbatim copies of the sources")
	}
	// flasher.js runs before app.js (host.js runs them in block order).
	if strings.Index(p, `id="ev-flasher"`) > strings.Index(p, `id="ev-app"`) {
		t.Error("flasher block after the app block")
	}
	// The bundled firmware round-trips and its manifest is kept.
	fz, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(texts["ev-fw"], "\n", ""))
	fr, err := gzip.NewReader(bytes.NewReader(fz))
	if err != nil {
		t.Fatal(err)
	}
	if fw, _ := io.ReadAll(fr); !bytes.Equal(fw, in.Firmware) {
		t.Fatal("firmware payload does not round-trip")
	}
	var m FirmwareManifest
	if err := json.Unmarshal([]byte(texts["ev-fwmeta"]), &m); err != nil || m.Size != len(in.Firmware) {
		t.Fatalf("firmware manifest block: %v %+v", err, m)
	}
	body := texts["ev-body"]
	if strings.Contains(body, "app.js") || strings.Contains(body, "flasher.js") || !strings.Contains(body, `id="fwView"`) || strings.Contains(body, "<!--") || !strings.Contains(body, `id="btnConnect"`) {
		t.Fatal("UI body not extracted as expected")
	}
	zb, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(texts["ev-wasm"], "\n", ""))
	zr, err := gzip.NewReader(bytes.NewReader(zb))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := io.ReadAll(zr)
	if !bytes.Equal(w, in.Wasm) {
		t.Fatal("wasm payload does not round-trip")
	}
	for _, want := range []string{`<meta charset="utf-8">`, `content="EasyVNA Web v9.9.9-test"`, `<title>EasyVNA Web</title>`, `rel="icon"`, `id="ev-loading"`} {
		if !strings.Contains(p, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// Nothing is loaded from elsewhere: no src= / href= to other files.
	if m := regexp.MustCompile(`(?i)<(script|link|img|iframe)[^>]+(src|href)="(https?:|//|[a-z0-9_./-]+\.(js|css|wasm))`).FindString(p); m != "" {
		t.Errorf("external reference: %s", m)
	}
	again, _ := Build(in)
	if !bytes.Equal(again, page) {
		t.Error("build is not deterministic")
	}
}

// A CRLF checkout (Windows, core.autocrlf=true) builds the same page.
func TestBuildCRLF(t *testing.T) {
	in := repoInputs(t)
	want, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	crlf := func(b []byte) []byte {
		return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	}
	w := in
	w.IndexHTML, w.StyleCSS, w.AppJS, w.HostCSS, w.HostJS, w.WasmExecJS, w.FlasherJS, w.FlasherCSS, w.FirmwareManifest =
		crlf(in.IndexHTML), crlf(in.StyleCSS), crlf(in.AppJS), crlf(in.HostCSS), crlf(in.HostJS), crlf(in.WasmExecJS), crlf(in.FlasherJS), crlf(in.FlasherCSS), crlf(in.FirmwareManifest)
	got, err := Build(w)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("CRLF sources give a different page")
	}
	if bytes.ContainsRune(got, '\r') {
		t.Fatal("page contains a CR")
	}
}

func TestBuildRejects(t *testing.T) {
	base := repoInputs(t)
	cases := map[string]func(*Inputs){
		"not wasm":  func(in *Inputs) { in.Wasm = []byte("hello world") },
		"no body":   func(in *Inputs) { in.IndexHTML = []byte("<html></html>") },
		"no app.js": func(in *Inputs) { in.IndexHTML = []byte("<body><p>x</p></body>") },
		"extra script": func(in *Inputs) {
			in.IndexHTML = []byte(`<body><script>1</script><script src="app.js"></script></body>`)
		},
		"end tag in js":  func(in *Inputs) { in.AppJS = []byte("var s = '</SCRIPT>';") },
		"comment in js":  func(in *Inputs) { in.HostJS = []byte("var s = '<!--';") },
		"end tag in css": func(in *Inputs) { in.StyleCSS = []byte("a{} </style>") },
		"no flasher.js":  func(in *Inputs) { in.FlasherJS = nil },
		"damaged firmware": func(in *Inputs) {
			in.Firmware = append([]byte(nil), in.Firmware...)
			in.Firmware[100] ^= 1
		},
		"firmware without manifest": func(in *Inputs) { in.FirmwareManifest = []byte("{") },
	}
	for name, mod := range cases {
		in := base
		mod(&in)
		if _, err := Build(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
