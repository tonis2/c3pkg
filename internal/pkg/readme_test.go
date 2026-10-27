package pkg

import (
	"strings"
	"testing"
)

func TestRenderReadmeHighlightsCode(t *testing.T) {
	src := "# T\n\n```c3\nmodule vk;\nfn void main() { int x = 1; } // hi\n```\n\n```\nno language\n```\n\n```nosuchlang\nx\n```\n"
	got := string(RenderReadme(src))
	for _, want := range []string{`<pre class="chroma">`, `<span class="k">module</span>`, `<span class="c1">`, `no language`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Count(got, `class="chroma"`) != 1 {
		t.Error("only the fence with a known language should be highlighted")
	}
	if strings.Contains(got, "style=") {
		t.Error("inline styles are forbidden by the CSP")
	}
}

func TestRenderReadmeStillSanitizes(t *testing.T) {
	src := "<script>alert(1)</script>\n\n<span class=\"k\" onclick=\"x()\" style=\"color:red\">raw</span>\n\n[x](javascript:alert(1))\n\n```c3\n<script>alert(1)</script>\n```\n"
	got := string(RenderReadme(src))
	for _, bad := range []string{"<script", "onclick", "style=", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;") {
		t.Error("code content must stay escaped")
	}
}

func TestHighlightCode(t *testing.T) {
	got := string(HighlightCode("bash", "curl -fL https://x/<y>\n"))
	if !strings.Contains(got, `class="chroma"`) || !strings.Contains(got, "&lt;") || strings.Contains(got, "<y>") {
		t.Errorf("bash block: %s", got)
	}
	if strings.Contains(got, "\n</code>") {
		t.Errorf("trailing newline should be trimmed: %q", got)
	}
	plain := string(HighlightCode("nosuchlang", "a <b>"))
	if plain != "<pre><code>a &lt;b&gt;</code></pre>" {
		t.Errorf("unknown language: %s", plain)
	}
}
