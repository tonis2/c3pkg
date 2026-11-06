package web

import (
	"fmt"
	"html"
	"html/template"
	"regexp"
	"strings"
	"time"

	"github.com/tonis/c3pkg/internal/pkg"
)

var templateFuncs = template.FuncMap{
	"date": func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return t.Format("2 Jan 2006")
	},
	"ago": func(s string) string {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return s
		}
		return humanAgo(time.Since(t))
	},
	"size": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
		}
		return fmt.Sprintf("%d B", n)
	},
	"join":        strings.Join,
	"hl":          pkg.HighlightCode, // {{hl "bash" "text"}}: a highlighted <pre><code> block
	"installLine": installLine,
	"lockedLine":  lockedLine,
	"lineBlock":   lineBlock,
}

// installLine is the entry for a project's "dependencies". It names the registry itself, so a
// project needs no "fetch-url" for it.
func installLine(base, owner, name, version string) string {
	return fmt.Sprintf(`"%s:%s/%s@%s"`, base, owner, name, version)
}

// lockedLine is installLine with the sha256 of the .c3l, which c3c checks the download against.
func lockedLine(base, owner, name, version, sha256 string) string {
	return fmt.Sprintf(`"%s:%s/%s@%s:%s"`, base, owner, name, version, sha256)
}

var lineBreak = regexp.MustCompile(`:([^/])`)

// lineBlock shows a line in a <pre><code> block that can wrap after each ':', but not in the "://".
func lineBlock(line string) template.HTML {
	return template.HTML("<pre><code>" + lineBreak.ReplaceAllString(html.EscapeString(line), ":<wbr>$1") + "</code></pre>")
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month")
	}
	return plural(int(d/(365*24*time.Hour)), "year")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit + " ago"
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
