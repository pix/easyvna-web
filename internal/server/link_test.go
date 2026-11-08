package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanLink(t *testing.T) {
	ok := []struct{ in, want string }{
		{"", ""},
		{"  https://example.com/antenna  ", "https://example.com/antenna"},
		// Amazon: canonical /dp/ASIN, the tag parameter kept from the pasted
		// link, tracking removed.
		{"https://www.amazon.fr/Antenne-LoRa-868MHz-Gain/dp/B0C1234567/ref=sr_1_3?crid=2X&keywords=lora+antenna&qid=1730000000&sr=8-3&tag=abcd-21&th=1",
			"https://www.amazon.fr/dp/B0C1234567?tag=abcd-21"},
		// No tag in the pasted link: none is added.
		{"https://www.amazon.de/gp/product/B0D7654321/ref=ppx_yo_dt_b_asin_title_o00?ie=UTF8&psc=1", "https://www.amazon.de/dp/B0D7654321"},
		{"HTTPS://WWW.AMAZON.COM/dp/B012345678#customerReviews", "https://www.amazon.com/dp/B012345678"},
		// AliExpress: the listed parameters kept from the pasted link, the rest is tracking.
		{"https://www.aliexpress.com/item/1005006123456789.html?spm=a2g0o.productlist.main.1.abc&algo_pvid=xyz&aff_fcid=f1&aff_fsk=k2&aff_platform=link-c-tool&aff_trace_key=tk3&sk=s4&utm_source=x",
			"https://www.aliexpress.com/item/1005006123456789.html?aff_fcid=f1&aff_fsk=k2&aff_platform=link-c-tool&aff_trace_key=tk3&sk=s4"},
		{"https://fr.aliexpress.com/item/1005006123456789.html?spm=abc&gatewayAdapt=glo2fra", "https://fr.aliexpress.com/item/1005006123456789.html"},
		// Other shops: tracking removed, the rest kept as written (tag included).
		{"https://shop.example.org/p/lora-whip?id=42&utm_source=news&utm_medium=mail&fbclid=XYZ&tag=me-20&gclid=1", "https://shop.example.org/p/lora-whip?id=42&tag=me-20"},
		{"http://192.168.1.10:8080/datasheet.pdf", "http://192.168.1.10:8080/datasheet.pdf"},
	}
	for _, c := range ok {
		got, err := CleanLink(c.in)
		if err != nil || got != c.want {
			t.Errorf("CleanLink(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "JavaScript:alert(document.cookie)", "data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd", "ftp://example.com/x", "www.amazon.fr/dp/B0C1234567", "https://", "https://user:pw@example.com/",
		"https://exa mple.com/", "https://example.com/\nx", "vbscript:x", strings.Repeat("https://example.com/", 120)} {
		if got, err := CleanLink(bad); err == nil {
			t.Errorf("CleanLink(%q) accepted: %q", bad, got)
		}
	}
}

func TestLinkTextAndQR(t *testing.T) {
	if got := LinkText("https://www.amazon.fr/dp/B0C1234567?tag=abcd-21"); got != "amazon.fr/dp/B0C1234567?tag=abcd-21" {
		t.Fatal(got)
	}
	long := "https://shop.example.org/" + strings.Repeat("very-long-path-segment/", 6) + "product.html?id=12345&tag=me-20"
	got := LinkText(long)
	if len([]rune(got)) > 64 || !strings.Contains(got, "…") || !strings.HasSuffix(got, "id=12345&tag=me-20") || !strings.HasPrefix(got, "shop.example.org/") {
		t.Fatal(got)
	}
	if LinkText("") != "" || qrSVG("", 20) != "" {
		t.Fatal("empty link")
	}
	svg := string(qrSVG("https://www.amazon.fr/dp/B0C1234567?tag=abcd-21", 20))
	if !strings.HasPrefix(svg, `<svg class="qr"`) || !strings.Contains(svg, `<path fill="#000" d="M`) || !strings.Contains(svg, `width="20.0mm"`) {
		t.Fatal(svg)
	}
	// A long link makes a bigger code (modules of at least 0.3 mm).
	big := string(qrSVG(long+strings.Repeat("&x=1", 60), 20))
	if strings.Contains(big, `width="20.0mm"`) {
		t.Fatal("long link squeezed into 20 mm")
	}
}

func TestLinkAndPhotoOnSheets(t *testing.T) {
	ts, _ := newTestServer(t, false)
	call(t, ts, "POST", "/api/profile", `{"id":"869"}`, nil)
	call(t, ts, "POST", "/api/sweep", "", nil)
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	var errOut map[string]string
	// javascript: is refused.
	if c := call(t, ts, "POST", "/api/sheet", `{"antenna":{"name":"whip","link":"javascript:alert(1)"}}`, &errOut); c != 400 || !strings.Contains(errOut["error"], "http:// or https://") {
		t.Fatalf("%d %v", c, errOut)
	}
	body, _ := json.Marshal(map[string]any{"antenna": map[string]any{"name": "whip", "photo": png,
		"link": "https://www.amazon.fr/x/dp/B0C1234567/ref=sr_1_1?qid=1&tag=abcd-21"}})
	var sh map[string]string
	if c := call(t, ts, "POST", "/api/sheet", string(body), &sh); c != 200 {
		t.Fatal(c)
	}
	html, _ := get(t, ts, "/"+sh["url"])
	for _, want := range []string{`href="https://www.amazon.fr/dp/B0C1234567?tag=abcd-21"`, `rel="noopener noreferrer"`, ">amazon.fr/dp/B0C1234567?tag=abcd-21<", `<svg class="qr"`, `src="data:image/png;base64,`} {
		if !strings.Contains(html, want) {
			t.Fatalf("sheet lacks %s", want)
		}
	}
	var a AntennaInfo
	call(t, ts, "GET", "/api/antennas/whip", "", &a)
	if a.Link != "https://www.amazon.fr/dp/B0C1234567?tag=abcd-21" || a.Photo != png {
		t.Fatalf("record: %q", a.Link)
	}
	// The report: photo and link from the dialog, remembered with the antenna.
	body, _ = json.Marshal(map[string]any{"name": "whip", "link": "https://example.com/whip?utm_source=x&v=2", "photo": png})
	if c := call(t, ts, "POST", "/api/report", string(body), &sh); c != 200 {
		t.Fatal(c)
	}
	html, _ = get(t, ts, "/"+sh["url"])
	if !strings.Contains(html, `href="https://example.com/whip?v=2"`) || !strings.Contains(html, `<svg class="qr"`) || !strings.Contains(html, `class="photo"`) {
		t.Fatal("report lacks photo or link")
	}
	call(t, ts, "GET", "/api/antennas/whip", "", &a)
	if a.Link != "https://example.com/whip?v=2" || len(a.History) == 0 {
		t.Fatalf("record after report: %+v", a.Link)
	}
	if c := call(t, ts, "POST", "/api/report", `{"name":"whip","link":"data:text/html,x"}`, &errOut); c != 400 {
		t.Fatal(c)
	}
	// A bad photo is refused.
	if c := call(t, ts, "POST", "/api/report", `{"name":"whip","photo":"data:image/svg+xml;base64,PHN2Zz4="}`, &errOut); c != 400 {
		t.Fatal(c)
	}
}

func TestBatchLinkAndPhoto(t *testing.T) {
	b := testBatch()
	b.Link = "https://www.aliexpress.com/item/1005006123456789.html?spm=x&aff_fcid=f1"
	b.Photo = "data:image/jpeg;base64,/9j/4AAQ"
	if err := b.clean(); err != nil || b.Link != "https://www.aliexpress.com/item/1005006123456789.html?aff_fcid=f1" {
		t.Fatal(err, b.Link)
	}
	addAnt(b, 868.8e6, 47, 13)
	s := New(Options{Store: func() *Store { st, _ := NewStore(t.TempDir()); return st }()})
	html, err := s.buildBatchSheet("x", b)
	if err != nil || !strings.Contains(string(html), `<svg class="qr"`) || !strings.Contains(string(html), "aliexpress.com/item/1005006123456789.html?aff_fcid=f1") ||
		!strings.Contains(string(html), `class="photo"`) {
		t.Fatal(err)
	}
	b.Link = "javascript:void(0)"
	if err := b.clean(); err == nil {
		t.Fatal("javascript: accepted")
	}
}
