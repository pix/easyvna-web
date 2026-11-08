package server

import (
	"html/template"
	"regexp"
	"strings"
)

// smallPrintTag matches the only markup small print may use once escaped:
// <b> </b> <i> </i> and <br> (also written <br/> or <br />).
var smallPrintTag = regexp.MustCompile(`(?i)&lt;(/?)(b|i)&gt;|&lt;br\s*/?&gt;`)

// smallPrintHTML renders a photo caption or a link note: everything is
// escaped, then bold, italic and line breaks come back as tags, always
// balanced (a stray closing tag is dropped, an open one is closed at the
// end). Any other markup stays visible as text.
func smallPrintHTML(s string) template.HTML {
	esc := template.HTMLEscapeString(s)
	var out strings.Builder
	var open []string
	last := 0
	for _, m := range smallPrintTag.FindAllStringSubmatchIndex(esc, -1) {
		out.WriteString(esc[last:m[0]])
		last = m[1]
		if m[4] < 0 { // <br>
			out.WriteString("<br>")
			continue
		}
		name := strings.ToLower(esc[m[4]:m[5]])
		if m[3] == m[2] { // opening tag
			open = append(open, name)
			out.WriteString("<" + name + ">")
			continue
		}
		// Closing tag: close back to the matching open one, if any.
		i := len(open) - 1
		for i >= 0 && open[i] != name {
			i--
		}
		if i < 0 {
			continue
		}
		for j := len(open) - 1; j >= i; j-- {
			out.WriteString("</" + open[j] + ">")
		}
		for _, t := range open[i+1:] { // reopen the ones closed on the way
			out.WriteString("<" + t + ">")
		}
		open = append(open[:i], open[i+1:]...)
	}
	out.WriteString(esc[last:])
	for j := len(open) - 1; j >= 0; j-- {
		out.WriteString("</" + open[j] + ">")
	}
	return template.HTML(out.String())
}

var smallPrintMarkup = regexp.MustCompile(`(?i)</?(b|i)>|<br\s*/?>`)

// smallPrintText is the small print without its markup, to estimate its
// size.
func smallPrintText(s string) string {
	return strings.TrimSpace(smallPrintMarkup.ReplaceAllString(s, " "))
}

// finePrintMm is the height the fine print block above the footer takes
// (photo caption and link note, full page width, one line more per <br>).
func finePrintMm(caption, note string) float64 {
	h := 0.0
	for _, s := range []string{caption, note} {
		if s == "" {
			continue
		}
		breaks := len(regexp.MustCompile(`(?i)<br\s*/?>`).FindAllString(s, -1))
		h += smallPrintMm(smallPrintText(s), 186) + float64(breaks)*2.6
	}
	if h > 0 {
		h += 2.3
	}
	return h
}
