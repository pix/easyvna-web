package server

import (
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/pix/easyvna-web/internal/qr"
)

// ---------- product / source links and photos ----------
//
// An antenna (and a batch) can carry the link of the page it was bought
// from or documented on. It is printed on the sheet as a short readable
// URL and a QR code. The link is never fetched. Only http and https links
// are accepted; tracking parameters are removed from what was pasted, and
// nothing is ever added to it.

const maxLinkLen = 2000

// trackingParams are removed from pasted links (exact names; "utm_",
// "pd_rd_", "pf_rd_" are prefixes).
var trackingParams = map[string]bool{
	"ref": true, "ref_": true, "psc": true, "th": true, "_encoding": true, "content-id": true, "qid": true, "sr": true,
	"keywords": true, "crid": true, "sprefix": true, "dib": true, "dib_tag": true, "spm": true, "scm": true,
	"algo_pvid": true, "algo_exp_id": true, "pdp_npi": true, "pdp_ext_f": true, "gatewayadapt": true, "_randl_currency": true,
	"_randl_shipto": true, "gclid": true, "gbraid": true, "wbraid": true, "fbclid": true, "dclid": true, "msclkid": true,
	"yclid": true, "igshid": true, "mc_cid": true, "mc_eid": true, "_ga": true, "_gl": true,
}

var trackingPrefixes = []string{"utm_", "pd_rd_", "pf_rd_"}

// aliKeep are the parameters kept from a pasted AliExpress link (the rest of
// its query is tracking).
var aliKeep = map[string]bool{"aff_fcid": true, "aff_fsk": true, "aff_platform": true, "aff_trace_key": true, "sk": true, "tag": true}

var (
	amazonHost = regexp.MustCompile(`(^|\.)amazon\.[a-z.]{2,8}$`)
	amazonASIN = regexp.MustCompile(`/(?:dp|gp/product|gp/aw/d|exec/obidos/ASIN|o/ASIN)/([A-Z0-9]{10})(?:[/?]|$)`)
)

func isTracking(name string) bool {
	n := strings.ToLower(name)
	if trackingParams[n] {
		return true
	}
	for _, p := range trackingPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// filterQuery keeps the parameters of raw (in their order) that keep
// accepts, exactly as they were written.
func filterQuery(raw string, keep func(name string) bool) string {
	var out []string
	for _, kv := range strings.Split(raw, "&") {
		if kv == "" {
			continue
		}
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if n, err := url.QueryUnescape(name); err == nil {
			name = n
		}
		if keep(name) {
			out = append(out, kv)
		}
	}
	return strings.Join(out, "&")
}

// CleanLink checks a pasted product link and returns its clean form: the
// scheme and host in lower case, no fragment, tracking parameters removed.
// An Amazon product link becomes https://<host>/dp/<ASIN>, with the "tag"
// parameter kept from the pasted link when it has one. An AliExpress link
// keeps only aff_fcid, aff_fsk, aff_platform, aff_trace_key, sk and tag.
// Other links keep every parameter that is not tracking. "" stays "".
func CleanLink(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	bad := errors.New("the link must be a web address starting with http:// or https://")
	if len(s) > maxLinkLen {
		return "", errors.New("the link is too long (2000 characters at most)")
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", bad
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", bad
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", bad
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	host := strings.TrimPrefix(u.Hostname(), "www.")
	switch {
	case amazonHost.MatchString(host) && amazonASIN.MatchString(u.EscapedPath()):
		asin := amazonASIN.FindStringSubmatch(u.EscapedPath())[1]
		u.Path, u.RawPath = "/dp/"+asin, ""
		u.RawQuery = filterQuery(u.RawQuery, func(n string) bool { return n == "tag" })
	case strings.Contains(host, "aliexpress."):
		u.RawQuery = filterQuery(u.RawQuery, func(n string) bool { return aliKeep[strings.ToLower(n)] })
	default:
		u.RawQuery = filterQuery(u.RawQuery, func(n string) bool { return !isTracking(n) })
	}
	u.ForceQuery = false
	return u.String(), nil
}

// LinkText is the short, readable form of a clean link for print: no
// scheme, no "www.", the middle elided when it is long.
func LinkText(link string) string {
	if link == "" {
		return ""
	}
	t := link
	if u, err := url.Parse(link); err == nil {
		t = strings.TrimPrefix(u.Host, "www.") + u.EscapedPath()
		if u.RawQuery != "" {
			t += "?" + u.RawQuery
		}
	}
	t = strings.TrimSuffix(t, "/")
	const max = 64
	if r := []rune(t); len(r) > max {
		t = string(r[:max-22]) + "…" + string(r[len(r)-21:])
	}
	return t
}

// qrSVG is the QR code of a link for print (error correction M), with its
// quiet zone, at least sizeMm wide and never with modules under 0.3 mm
// (long links make a bigger code). "" for no link.
func qrSVG(link string, sizeMm float64) template.HTML {
	if link == "" {
		return ""
	}
	c, err := qr.Encode([]byte(link), qr.M)
	if err != nil {
		return ""
	}
	n := float64(c.Size + 8)
	mm := math.Max(sizeMm, n*0.3)
	return template.HTML(fmt.Sprintf(`<svg class="qr" viewBox="-4 -4 %.0f %.0f" width="%.1fmm" height="%.1fmm" role="img" aria-label="QR code of the link" shape-rendering="crispEdges">`+
		`<rect x="-4" y="-4" width="%.0f" height="%.0f" fill="#fff"/><path fill="#000" d="%s"/></svg>`, n, n, mm, mm, n, n, c.SVGPath()))
}
