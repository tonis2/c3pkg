package pkg

import (
	"bytes"
	"html/template"
	"regexp"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

const MaxReadmeBytes = 512 << 10

var (
	md = goldmark.New(goldmark.WithExtensions(extension.GFM))

	sanitizer = func() *bluemonday.Policy {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[A-Za-z0-9_+-]+$`)).OnElements("code")
		return p
	}()
)

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
