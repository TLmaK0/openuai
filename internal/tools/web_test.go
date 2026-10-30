package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Characters whose lowercase form has a different length in bytes than the
// original. strings.ToLower on a page containing them used to shift the
// indices stripHTMLTags compared against, which crashed the app (#109).
var lengthChangingChars = []struct{ name, s string }{
	{"Kelvin sign U+212A", "\u212A"},         // 3 bytes -> "k" (1)
	{"I with dot above U+0130", "\u0130"},    // 2 bytes -> "i" (1)
	{"Ohm sign U+2126", "\u2126"},            // 3 bytes -> omega (2)
	{"capital sharp S U+1E9E", "\u1E9E"},     // 3 bytes -> sharp s (2)
	{"A with stroke U+023A", "\u023A"},       // 2 bytes -> U+2C65 (3)
	{"invalid UTF-8 byte", "\xff"},           // 1 byte -> U+FFFD (3)
	{"truncated UTF-8 sequence", "\xe2\x84"}, // 2 bytes -> 2x U+FFFD (6)
	{"mixed", "\u212A\u0130\xff\u023A\u2126\u1E9E"},
}

func TestStripHTMLTagsLengthChangingCharacters(t *testing.T) {
	for _, c := range lengthChangingChars {
		t.Run(c.name, func(t *testing.T) {
			x := c.s
			page := "<html><head><TITLE>T" + x + "</TITLE><style>p{color:red}" + x + "</style>" +
				"<SCRIPT>var a = '" + x + "<p>hidden</p>';</SCRIPT></head><body>" +
				"<p>Temp: 300" + x + "</p><Div>" + x + "stanbul</Div>" +
				"<script type=\"x\">" + strings.Repeat(x, 50) + "</script>" +
				"<ul><li>one " + x + "</li><LI>two</LI></ul>" + x + "<br/>end</body></html>"

			got := stripHTMLTags(page) // must not panic

			want := "T" + x + "\nTemp: 300" + x + "\n" + x + "stanbul\none " + x + "\ntwo" + x + "\nend"
			if got != want {
				t.Fatalf("stripHTMLTags:\n got %q\nwant %q", got, want)
			}
			for _, hidden := range []string{"hidden", "color:red", "var a"} {
				if strings.Contains(got, hidden) {
					t.Fatalf("script/style content %q leaked into %q", hidden, got)
				}
			}
		})
	}
}

// The crash happened on a ~200 KB page with tags after many such characters,
// where the shifted index ran past the end of the lowercased copy.
func TestStripHTMLTagsLargePageNearEnd(t *testing.T) {
	for _, c := range lengthChangingChars {
		t.Run(c.name, func(t *testing.T) {
			page := "<p>" + strings.Repeat(c.s+" ", 40000) + "</p><script>x()</script><style>a{}</style><p>tail"
			got := stripHTMLTags(page)
			if !strings.HasSuffix(got, " \ntail") || strings.Contains(got, "x()") || strings.Contains(got, "a{}") {
				t.Fatalf("unexpected end of text: %q", got[len(got)-min(len(got), 80):])
			}
		})
	}
}

// Text ending right after a partial tag name must not read past the end.
func TestStripHTMLTagsTruncatedTags(t *testing.T) {
	for _, s := range []string{"<", "</", "<scr", "</scrip", "<sty", "</styl", "\u212A<", "\u212A</script", "<SCRIPT", "a<br", "\u0130<h"} {
		_ = stripHTMLTags(s)
	}
}

// oldStripHTMLTags is the previous implementation, kept to check that the fix
// does not change the text of normal pages (those where lowercasing keeps the
// length in bytes, which covers ASCII and almost all real text).
func oldStripHTMLTags(s string) string {
	var b strings.Builder
	inTag, inScript, inStyle, lastWasSpace := false, false, false, false
	lower := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		if i+7 <= len(s) && lower[i:i+7] == "<script" {
			inScript = true
		}
		if i+8 <= len(s) && lower[i:i+8] == "</script" {
			inScript = false
			inTag = true
		}
		if i+6 <= len(s) && lower[i:i+6] == "<style" {
			inStyle = true
		}
		if i+7 <= len(s) && lower[i:i+7] == "</style" {
			inStyle = false
			inTag = true
		}
		if inScript || inStyle {
			continue
		}
		if s[i] == '<' {
			inTag = true
			if i+1 < len(s) {
				next := strings.ToLower(s[i:])
				if strings.HasPrefix(next, "<br") || strings.HasPrefix(next, "<p") ||
					strings.HasPrefix(next, "<div") || strings.HasPrefix(next, "<h") ||
					strings.HasPrefix(next, "<li") || strings.HasPrefix(next, "<tr") {
					b.WriteByte('\n')
					lastWasSpace = true
				}
			}
			continue
		}
		if s[i] == '>' {
			inTag = false
			continue
		}
		if inTag {
			continue
		}
		ch := s[i]
		if ch == '\n' || ch == '\r' || ch == '\t' || ch == ' ' {
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		} else {
			b.WriteByte(ch)
			lastWasSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func TestStripHTMLTagsSameOutputForNormalPages(t *testing.T) {
	pages := []string{
		`<!DOCTYPE html><HTML><Head><title>Docs</title><STYLE>body{margin:0}</STYLE>
<script src="a.js"></script><Script>if (a < b) { x = "<div>" }</Script></Head>
<BODY><H1>Title</H1><p>First   paragraph
with	tabs and newlines.</p><DIV class="x">Block<br>line<BR/>two</DIV>
<table><TR><td>cell</td></TR></table><ul><li>a</li><LI>b</LI></ul>
<pre>code &lt;tag&gt;</pre><a href="/x">link</a></BODY></HTML>`,
		"<p>Caf\u00E9, ni\u00F1o, stra\u00DFe, \u0395\u03BB\u03BB\u03B7\u03BD\u03B9\u03BA\u03AC, \u0440\u0443\u0441\u0441\u043A\u0438\u0439, \u65E5\u672C\u8A9E, emoji \U0001F389</p><script>var s='\u00C4\u00D6\u00DC'</script><h2>\u00DCn\u00EFc\u00F6d\u00E9</h2>",
		"plain text without tags",
		"",
		"<p>unclosed <b>bold",
	}
	for i, p := range pages {
		if got, want := stripHTMLTags(p), oldStripHTMLTags(p); got != want {
			t.Errorf("page %d: output changed:\n got %q\nwant %q", i, got, want)
		}
	}
}

// End to end: web_fetch on an HTML page with these characters returns its
// text instead of crashing.
func TestWebFetchHTMLWithLengthChangingCharacters(t *testing.T) {
	page := "<html><head><style>p{}</style><script>var k='\u212A';</script></head><body>" +
		strings.Repeat("<p>Kelvin \u212A, \u0130stanbul, bad \xff byte</p>", 5000) +
		"<p>THE END</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	res := WebFetch{}.Execute(context.Background(), map[string]string{"url": srv.URL, "max_bytes": "2000000"})
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Kelvin \u212A, \u0130stanbul") || !strings.Contains(res.Output, "THE END") {
		t.Fatalf("text missing from output (len %d): %.300q", len(res.Output), res.Output)
	}
	if strings.Contains(res.Output, "var k") || strings.Contains(res.Output, "<p>") {
		t.Fatalf("tags or script leaked into output: %.300q", res.Output)
	}
}

func TestHTMLToTextReturnsText(t *testing.T) {
	text, err := htmlToText("<p>\u212A</p><script>x</script>ok")
	if err != nil || text != "\u212Aok" {
		t.Fatalf("htmlToText = %q, %v", text, err)
	}
}
