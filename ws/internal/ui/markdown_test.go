package ui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/config"
)

func TestRenderMarkdownGFMFeaturesAndNoColor(t *testing.T) {
	md := `<!-- hidden html comment -->
# Release Notes

Summary with **bold**, *italic*, ~~old~~, ` + "`inline_code`" + `, and [PR #42](https://example.com/pr/42).

## Checklist
- [x] Ship Charm tables
- [ ] Add unit tests

> Note: Keep terminal backgrounds transparent.

| Check | Status |
| --- | --- |
| vet | pass |
| race | pass |

` + "```go\nfunc hello() string { return \"ok\" }\n```"

	roles := NewRoles(config.Palette{
		Mode:   "dark",
		Tokens: map[string]string{"bg": "#282828", "fg": "#ebdbb2", "accent": "#fe8019"},
	}, false)

	width := 56
	rendered := RenderMarkdown(md, width, roles)
	if strings.Contains(rendered, "hidden html comment") {
		t.Fatalf("expected HTML comment stripped, got:\n%s", rendered)
	}
	if !strings.Contains(StripANSI(rendered), "Release Notes") ||
		!strings.Contains(StripANSI(rendered), "☑ Ship Charm tables") ||
		!strings.Contains(StripANSI(rendered), "☐ Add unit tests") ||
		!strings.Contains(StripANSI(rendered), "▎ Note:") ||
		!strings.Contains(StripANSI(rendered), "race") {
		t.Fatalf("missing expected rendered markdown content:\n%s", rendered)
	}

	bgRe := regexp.MustCompile(`\x1b\[(?:[0-9]+;)*(?:4[0-8]|10[0-7])(?:;|m)`)
	if bgRe.MatchString(rendered) {
		t.Fatalf("unexpected ANSI background sequence in markdown output:\n%q", rendered)
	}
	for i, line := range strings.Split(rendered, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("line %d exceeds max width %d (got %d): %q", i, width, w, line)
		}
	}

	plainRoles := NewRoles(config.Palette{Mode: "dark"}, true)
	plain := RenderMarkdown(md, width, plainRoles)
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in NoColor markdown output, got %q", plain)
	}
	if !strings.Contains(plain, "Release Notes") || !strings.Contains(plain, "func hello()") {
		t.Fatalf("expected markdown text preserved under NoColor:\n%s", plain)
	}
}

func TestRenderMarkdownWrapsLongProseLines(t *testing.T) {
	roles := NewRoles(config.Palette{Mode: "dark"}, true)
	longPara := "This is a deliberately long GitHub issue description paragraph that should wrap cleanly within thirty columns without splitting words awkwardly."
	out := RenderMarkdown(longPara, 30, roles)
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("expected prose to wrap into multiple lines, got %d:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 30 {
			t.Fatalf("wrapped line %d width=%d > 30: %q", i, w, l)
		}
	}
}

func TestRenderMarkdownPreservesCommentsInCodeFencesAndClampsNarrowTables(t *testing.T) {
	roles := NewRoles(config.Palette{Mode: "dark"}, false)
	md := "<!-- prose comment removed -->\n\n```html\n<!-- keep-in-fence -->\n```\n\n| ColumnNameVeryWide | AnotherVeryWideColumnName |\n| --- | --- |\n| value-one-1234567890 | value-two-1234567890 |\n"
	width := 28
	out := RenderMarkdown(md, width, roles)
	plain := StripANSI(out)
	if strings.Contains(plain, "prose comment removed") {
		t.Fatalf("expected prose HTML comment stripped:\n%s", plain)
	}
	if !strings.Contains(plain, "<!-- keep-in-fence -->") {
		t.Fatalf("expected HTML comment inside code fence preserved:\n%s", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("narrow markdown line %d width=%d > %d: %q", i, w, width, line)
		}
	}
}

func TestRenderMarkdownGitHubDetailsSummaryAndHTMLTags(t *testing.T) {
	roles := NewRoles(config.Palette{
		Mode:   "dark",
		Tokens: map[string]string{"bg": "#282828", "fg": "#ebdbb2", "accent": "#fe8019"},
	}, false)

	md := `## Template

<details>
<summary>Environment &amp; <b>Setup</b></summary>

- [x] Export ` + "`WS_DIR`" + `
- [ ] Run <a href="https://example.com/docs">setup guide</a><br/>and verify &lt;ok&gt;

` + "```sh\necho inside-details\n```" + `
</details>

<details open><summary>Inline Summary</summary>Single line detail &nbsp; <img src="x.png" alt="architecture"/></details>

` + "```xml\n<details><summary>literal</summary></details>\n```" + `
`
	width := 60
	out := RenderMarkdown(md, width, roles)
	plain := StripANSI(out)

	for _, want := range []string{
		"▾ Environment & Setup",
		"│ ☑ Export WS_DIR",
		"setup guide (https://example.com/docs)",
		"and verify <ok>",
		"echo inside-details",
		"▾ Inline Summary",
		"│ Single line detail [image: architecture]",
		"<details><summary>literal</summary></details>",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected %q in rendered GitHub template markdown:\n%s", want, plain)
		}
	}
	proseOnly, _, _ := strings.Cut(plain, "[xml]")
	for _, rawTag := range []string{"</details>", "</summary>", "<a href", "<img ", "<br"} {
		if strings.Contains(proseOnly, rawTag) {
			t.Fatalf("did not expect raw HTML %q outside code fences:\n%s", rawTag, proseOnly)
		}
	}
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Fatalf("line %d exceeds max width %d (got %d): %q", i, width, w, line)
		}
	}

	plainNoColor := RenderMarkdown(md, width, NewRoles(config.Palette{Mode: "dark"}, true))
	if strings.Contains(plainNoColor, "\x1b") {
		t.Fatalf("expected zero ANSI under NO_COLOR=1:\n%q", plainNoColor)
	}
}
