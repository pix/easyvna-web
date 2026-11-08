package server

import "testing"

func TestSmallPrintHTML(t *testing.T) {
	for in, want := range map[string]string{
		"plain & simple":                  "plain &amp; simple",
		"<b>Note</b>: see <i>link</i>":    "<b>Note</b>: see <i>link</i>",
		"<B>caps</B>":                     "<b>caps</b>",
		"line<br>two<br/>three<br />four": "line<br>two<br>three<br>four",
		"<b>never closed":                 "<b>never closed</b>",
		"stray</b> close":                 "stray close",
		"<b><i>x</b> y</i>":               "<b><i>x</i></b><i> y</i>",
		"<script>alert(1)</script>":       "&lt;script&gt;alert(1)&lt;/script&gt;",
		`<b onclick="x">no</b>`:           "&lt;b onclick=&#34;x&#34;&gt;no",
		"<img src=x onerror=y>":           "&lt;img src=x onerror=y&gt;",
		"":                                "",
	} {
		if got := string(smallPrintHTML(in)); got != want {
			t.Errorf("smallPrintHTML(%q) = %q, want %q", in, got, want)
		}
	}
	if got := smallPrintText("<b>a</b><br>b"); got != "a  b" {
		t.Errorf("smallPrintText = %q", got)
	}
}
