package pkg

import (
	"bytes"
	"html"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
)

const MaxReadmeBytes = 512 << 10

// RenderVersion identifies the README renderer. Rendered HTML is stored with the package, so
// bump it whenever the output changes and the stored copies are re-rendered on the next start.
const RenderVersion = 1

// Highlighted code uses CSS classes (see web/static/css/syntax.css): the CSP forbids inline styles.
var (
	chromaFormatter = chromahtml.New(chromahtml.WithClasses(true))

	md = goldmark.New(goldmark.WithExtensions(extension.GFM,
		highlighting.NewHighlighting(highlighting.WithFormatOptions(chromahtml.WithClasses(true)))))

	sanitizer = func() *bluemonday.Policy {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[A-Za-z0-9_+-]+$`)).OnElements("code")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^chroma$`)).OnElements("pre")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^[A-Za-z0-9]{1,4}$`)).OnElements("span")
		return p
	}()
)

// HighlightCode renders src as a highlighted code block. Unknown languages are shown unhighlighted.
// Only for trusted text such as the site's own docs: it is escaped but not sanitized.
func HighlightCode(lang, src string) template.HTML {
	src = strings.TrimRight(src, "\n")
	plain := template.HTML("<pre><code>" + html.EscapeString(src) + "</code></pre>")
	lexer := lexers.Get(lang)
	if lexer == nil {
		return plain
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, src)
	if err != nil {
		return plain
	}
	var buf bytes.Buffer
	if err := chromaFormatter.Format(&buf, styles.Fallback, it); err != nil {
		return plain
	}
	return template.HTML(buf.String())
}

// ValidateReadme checks an uploaded README's size and encoding.
func ValidateReadme(b []byte) string {
	switch {
	case len(b) > MaxReadmeBytes:
		return "The README is larger than 512 KB."
	case !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0:
		return "The README must be UTF-8 text."
	}
	return ""
}

// RenderReadme converts Markdown to sanitized HTML. Raw HTML in the source is dropped by
// goldmark and the result is passed through a sanitizer as a second layer.
func RenderReadme(src string) template.HTML {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return ""
	}
	return template.HTML(sanitizer.SanitizeBytes(buf.Bytes()))
}
