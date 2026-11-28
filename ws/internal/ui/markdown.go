package ui

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	lgtable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
)

const (
	wsDetailsOpenSentinel  = "\x00WS_DETAILS_OPEN"
	wsSummaryPrefix        = "\x00WS_SUMMARY:"
	wsDetailsCloseSentinel = "\x00WS_DETAILS_CLOSE"
)

var (
	reHTMLDetailsOpen  = regexp.MustCompile(`(?is)<details(?:\s+[^>]*)?>`)
	reHTMLDetailsClose = regexp.MustCompile(`(?is)</details>`)
	reHTMLSummaryBlock = regexp.MustCompile(`(?is)<summary(?:\s+[^>]*)?>(.*?)</summary>`)
	reHTMLAnyTag       = regexp.MustCompile(`(?is)<[^>]+>`)
	reHTMLBr           = regexp.MustCompile(`(?is)<br\s*/?>`)
	reHTMLHr           = regexp.MustCompile(`(?is)<hr\s*/?>`)
	reHTMLAnchor       = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*(?:"([^"]*)"|'([^']*)')[^>]*>(.*?)</a>`)
	reHTMLImg          = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	reHTMLImgAlt       = regexp.MustCompile(`(?is)\balt\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reHTMLStrong       = regexp.MustCompile(`(?is)<(?:strong|b)(?:\s+[^>]*)?>(.*?)</(?:strong|b)>`)
	reHTMLEm           = regexp.MustCompile(`(?is)<(?:em|i)(?:\s+[^>]*)?>(.*?)</(?:em|i)>`)
	reHTMLCode         = regexp.MustCompile(`(?is)<code(?:\s+[^>]*)?>(.*?)</code>`)
	reHTMLDel          = regexp.MustCompile(`(?is)<(?:del|s)(?:\s+[^>]*)?>(.*?)</(?:del|s)>`)
	reHTMLHeading      = regexp.MustCompile(`(?is)<h([1-6])(?:\s+[^>]*)?>(.*?)</h[1-6]>`)
	reHTMLListItem     = regexp.MustCompile(`(?is)<li(?:\s+[^>]*)?>(.*?)</li>`)
	reHTMLBlockBreak   = regexp.MustCompile(`(?is)</(?:p|div|section|blockquote|ul|ol|dl|dt|dd)>`)
	reHTMLStripTags    = regexp.MustCompile(`(?is)<(?:p|div|section|span|ul|ol|dl|dt|dd|thead|tbody|tr|td|th|table|kbd)(?:\s+[^>]*)?>|</(?:span|thead|tbody|tr|td|th|table|kbd)>`)
)

func RenderMarkdown(md string, width int, r Roles) string {
	if width <= 0 {
		width = 72
	}
	width = max(12, width)

	clean := strings.ReplaceAll(md, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	clean = stripHTMLCommentsOutsideFences(clean)
	clean = normalizeGitHubHTMLOutsideFences(clean)
	if strings.TrimSpace(clean) == "" {
		return ""
	}

	rawLines := strings.Split(clean, "\n")
	var out []string
	detailsDepth := 0
	var detailsHasSummary []bool

	contentWidth := func() int {
		if detailsDepth <= 0 {
			return width
		}
		return max(10, width-detailsDepth*2)
	}
	railPrefix := func(depth int) string {
		if depth <= 0 {
			return ""
		}
		return strings.Repeat(r.Border.Render("│ "), depth)
	}
	appendRaw := func(line string) {
		if ansi.StringWidth(line) > width {
			line = TruncateDisplay(line, width)
		}
		if r.NoColor {
			line = StripANSI(line)
		}
		out = append(out, line)
	}
	ensureDetailsSummaryHeader := func() {
		if detailsDepth <= 0 || len(detailsHasSummary) == 0 || detailsHasSummary[len(detailsHasSummary)-1] {
			return
		}
		detailsHasSummary[len(detailsHasSummary)-1] = true
		hdr := r.Accent.Render("▸ Details")
		if r.NoColor {
			hdr = "▸ Details"
		}
		appendRaw(railPrefix(detailsDepth-1) + TruncateDisplay(hdr, max(10, width-(detailsDepth-1)*2)))
	}
	appendLine := func(line string) {
		ensureDetailsSummaryHeader()
		effW := contentWidth()
		if ansi.StringWidth(line) > effW {
			line = TruncateDisplay(line, effW)
		}
		appendRaw(railPrefix(detailsDepth) + line)
	}
	appendBlank := func() {
		if len(out) == 0 {
			return
		}
		if detailsDepth == 0 {
			if out[len(out)-1] != "" {
				out = append(out, "")
			}
			return
		}
		railOnly := strings.TrimRight(railPrefix(detailsDepth), " ")
		if r.NoColor {
			railOnly = StripANSI(railOnly)
		}
		if out[len(out)-1] != railOnly && out[len(out)-1] != "" {
			appendRaw(railOnly)
		}
	}

	boldStyle := lipgloss.NewStyle().Bold(true)
	italicStyle := lipgloss.NewStyle().Italic(true)
	strikeStyle := lipgloss.NewStyle().Strikethrough(true)
	linkLabelStyle := lipgloss.NewStyle().Underline(true).Foreground(r.Slots.Cyan)
	codeSpanStyle := lipgloss.NewStyle().Foreground(r.Slots.Cyan)
	subHeadingStyle := lipgloss.NewStyle().Bold(true)
	if r.NoColor {
		boldStyle = lipgloss.NewStyle()
		italicStyle = lipgloss.NewStyle()
		strikeStyle = lipgloss.NewStyle()
		linkLabelStyle = lipgloss.NewStyle()
		codeSpanStyle = lipgloss.NewStyle()
		subHeadingStyle = lipgloss.NewStyle()
	}

	inFence := false
	fenceDelim := ""
	for i := 0; i < len(rawLines); i++ {
		line := rawLines[i]
		trimmed := strings.TrimSpace(line)

		if !inFence {
			switch {
			case trimmed == wsDetailsOpenSentinel:
				appendBlank()
				detailsDepth++
				detailsHasSummary = append(detailsHasSummary, false)
				continue
			case strings.HasPrefix(trimmed, wsSummaryPrefix):
				if detailsDepth == 0 {
					detailsDepth = 1
					detailsHasSummary = append(detailsHasSummary, false)
				}
				title := strings.TrimSpace(strings.TrimPrefix(trimmed, wsSummaryPrefix))
				if title == "" {
					title = "Details"
				}
				detailsHasSummary[len(detailsHasSummary)-1] = true
				parentW := max(10, width-(detailsDepth-1)*2)
				sumTokens := tokenizeInlineMarkdown(title, r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle)
				sumRendered := renderInlinePlainMarkdownFromTokens(sumTokens, boldStyle)
				hdrText := r.Accent.Render("▾ ") + sumRendered
				if r.NoColor {
					hdrText = "▾ " + StripANSI(sumRendered)
				}
				if ansi.StringWidth(hdrText) > parentW {
					hdrText = TruncateDisplay(hdrText, parentW)
				}
				appendRaw(railPrefix(detailsDepth-1) + hdrText)
				continue
			case trimmed == wsDetailsCloseSentinel:
				if detailsDepth > 0 {
					detailsHasSummary = detailsHasSummary[:len(detailsHasSummary)-1]
					detailsDepth--
					appendBlank()
				}
				continue
			}
		}

		if !inFence {
			if delim, lang, ok := parseCodeFenceOpen(trimmed); ok {
				inFence = true
				fenceDelim = delim
				if lang != "" {
					appendLine(r.Muted.Render("[" + lang + "]"))
				}
				continue
			}
		} else {
			if strings.HasPrefix(trimmed, fenceDelim) && strings.Trim(trimmed, string(fenceDelim[0])) == "" {
				inFence = false
				fenceDelim = ""
				appendBlank()
				continue
			}
			prefix := r.Border.Render("│ ")
			codeText := strings.ReplaceAll(line, "\t", "  ")
			appendLine(prefix + TruncateDisplay(codeText, max(4, contentWidth()-2)))
			continue
		}

		if trimmed == "" {
			appendBlank()
			continue
		}

		effW := contentWidth()

		if isHorizontalRule(trimmed) {
			ruleW := min(effW, 40)
			appendLine(r.Border.Render(strings.Repeat("─", ruleW)))
			continue
		}

		if strings.Contains(trimmed, "|") && i+1 < len(rawLines) && isMarkdownTableSeparator(rawLines[i+1]) {
			headers := parsePipeRow(trimmed)
			var tableRows [][]string
			j := i + 2
			for ; j < len(rawLines); j++ {
				rowTrim := strings.TrimSpace(rawLines[j])
				if rowTrim == "" || !strings.Contains(rowTrim, "|") {
					break
				}
				cells := parsePipeRow(rowTrim)
				if len(cells) < len(headers) {
					padded := make([]string, len(headers))
					copy(padded, cells)
					cells = padded
				} else if len(cells) > len(headers) {
					cells = cells[:len(headers)]
				}
				for ci := range cells {
					cells[ci] = renderInlinePlainMarkdown(cells[ci], r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle)
				}
				tableRows = append(tableRows, cells)
			}
			for hi := range headers {
				headers[hi] = StripANSI(renderInlinePlainMarkdown(headers[hi], r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle))
			}
			renderedTable := renderMarkdownTable(headers, tableRows, effW, r)
			if renderedTable != "" {
				for _, tl := range strings.Split(renderedTable, "\n") {
					appendLine(tl)
				}
			}
			i = j - 1
			continue
		}

		if level, heading, ok := parseATXHeading(trimmed); ok {
			appendBlank()
			st := subHeadingStyle
			switch level {
			case 1:
				st = r.Accent
			case 2:
				st = r.Header
			}
			wrapped := wrapStyledTokens(tokenizeInlineMarkdown(heading, r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle), effW, func(tok inlineToken) string {
				if tok.styled != "" {
					return tok.styled
				}
				return st.Render(tok.raw)
			})
			for _, wl := range wrapped {
				appendLine(wl)
			}
			appendBlank()
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			quoteBody := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			prefix := r.Border.Render("▎ ")
			contPrefix := r.Border.Render("▎ ")
			if quoteBody == "" {
				appendLine(strings.TrimRight(prefix, " "))
				continue
			}
			tokens := tokenizeInlineMarkdown(quoteBody, r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle)
			wrapped := wrapStyledTokens(tokens, max(6, effW-2), func(tok inlineToken) string {
				if tok.styled != "" {
					return tok.styled
				}
				return r.Muted.Render(tok.raw)
			})
			for idx, wl := range wrapped {
				if idx == 0 {
					appendLine(prefix + wl)
				} else {
					appendLine(contPrefix + wl)
				}
			}
			continue
		}

		if indentSpaces, marker, itemBody, ok := parseListItem(line, r); ok {
			indent := strings.Repeat(" ", min(indentSpaces/2, 3)*2)
			markerW := lipgloss.Width(StripANSI(marker))
			firstPrefix := indent + marker
			contPrefix := strings.Repeat(" ", len(indent)+markerW)
			availW := max(8, effW-len(indent)-markerW)
			tokens := tokenizeInlineMarkdown(itemBody, r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle)
			wrapped := wrapStyledTokens(tokens, availW, func(tok inlineToken) string {
				if tok.styled != "" {
					return tok.styled
				}
				return tok.raw
			})
			if len(wrapped) == 0 {
				appendLine(strings.TrimRight(firstPrefix, " "))
				continue
			}
			for idx, wl := range wrapped {
				if idx == 0 {
					appendLine(firstPrefix + wl)
				} else {
					appendLine(contPrefix + wl)
				}
			}
			continue
		}

		tokens := tokenizeInlineMarkdown(trimmed, r, codeSpanStyle, boldStyle, italicStyle, strikeStyle, linkLabelStyle)
		wrapped := wrapStyledTokens(tokens, effW, func(tok inlineToken) string {
			if tok.styled != "" {
				return tok.styled
			}
			return tok.raw
		})
		for _, wl := range wrapped {
			appendLine(wl)
		}
	}

	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func normalizeGitHubHTMLOutsideFences(md string) string {
	lines := strings.Split(md, "\n")
	var prose strings.Builder
	var result []string
	inFence := false
	fenceDelim := ""

	flushProse := func() {
		if prose.Len() == 0 {
			return
		}
		norm := normalizeGitHubHTMLProse(prose.String())
		norm = strings.TrimSuffix(norm, "\n")
		result = append(result, strings.Split(norm, "\n")...)
		prose.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if delim, _, ok := parseCodeFenceOpen(trimmed); ok {
				flushProse()
				inFence = true
				fenceDelim = delim
				result = append(result, line)
				continue
			}
			prose.WriteString(line)
			prose.WriteByte('\n')
		} else {
			result = append(result, line)
			if strings.HasPrefix(trimmed, fenceDelim) && strings.Trim(trimmed, string(fenceDelim[0])) == "" {
				inFence = false
				fenceDelim = ""
			}
		}
	}
	flushProse()
	return strings.Join(result, "\n")
}

func normalizeGitHubHTMLProse(s string) string {

	var inlineCode []string
	var protected strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '`' {
			end := strings.IndexByte(s[i+1:], '`')
			if end >= 0 && !strings.ContainsRune(s[i+1:i+1+end], '\n') {
				token := fmt.Sprintf("\x00WS_IC_%d\x00", len(inlineCode))
				inlineCode = append(inlineCode, s[i:i+1+end+1])
				protected.WriteString(token)
				i = i + 1 + end + 1
				continue
			}
		}
		protected.WriteByte(s[i])
		i++
	}
	cur := protected.String()

	cur = reHTMLDetailsOpen.ReplaceAllString(cur, "\n"+wsDetailsOpenSentinel+"\n")
	cur = reHTMLSummaryBlock.ReplaceAllStringFunc(cur, func(m string) string {
		sub := reHTMLSummaryBlock.FindStringSubmatch(m)
		title := ""
		if len(sub) > 1 {
			title = sub[1]
		}
		title = reHTMLAnyTag.ReplaceAllString(title, " ")
		title = html.UnescapeString(strings.Join(strings.Fields(title), " "))
		if title == "" {
			title = "Details"
		}
		return "\n" + wsSummaryPrefix + title + "\n"
	})
	cur = reHTMLDetailsClose.ReplaceAllString(cur, "\n"+wsDetailsCloseSentinel+"\n")

	cur = reHTMLBr.ReplaceAllString(cur, "\n")
	cur = reHTMLHr.ReplaceAllString(cur, "\n---\n")
	cur = reHTMLHeading.ReplaceAllStringFunc(cur, func(m string) string {
		sub := reHTMLHeading.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		lvl := int(sub[1][0] - '0')
		if lvl < 1 || lvl > 6 {
			lvl = 2
		}
		txt := strings.Join(strings.Fields(reHTMLAnyTag.ReplaceAllString(sub[2], " ")), " ")
		return "\n" + strings.Repeat("#", lvl) + " " + txt + "\n"
	})
	cur = reHTMLListItem.ReplaceAllStringFunc(cur, func(m string) string {
		sub := reHTMLListItem.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		return "\n- " + strings.TrimSpace(sub[1]) + "\n"
	})
	cur = reHTMLAnchor.ReplaceAllStringFunc(cur, func(m string) string {
		sub := reHTMLAnchor.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		href := strings.TrimSpace(sub[1])
		if href == "" {
			href = strings.TrimSpace(sub[2])
		}
		label := strings.TrimSpace(reHTMLAnyTag.ReplaceAllString(sub[3], ""))
		if label == "" {
			label = href
		}
		if href == "" {
			return label
		}
		return "[" + label + "](" + href + ")"
	})
	cur = reHTMLImg.ReplaceAllStringFunc(cur, func(m string) string {
		sub := reHTMLImgAlt.FindStringSubmatch(m)
		alt := ""
		if len(sub) >= 3 {
			alt = strings.TrimSpace(sub[1])
			if alt == "" {
				alt = strings.TrimSpace(sub[2])
			}
		}
		if alt != "" {
			return "[image: " + alt + "]"
		}
		return "[image]"
	})
	cur = reHTMLStrong.ReplaceAllString(cur, "**$1**")
	cur = reHTMLEm.ReplaceAllString(cur, "*$1*")
	cur = reHTMLCode.ReplaceAllString(cur, "`$1`")
	cur = reHTMLDel.ReplaceAllString(cur, "~~$1~~")
	cur = reHTMLBlockBreak.ReplaceAllString(cur, "\n")
	cur = reHTMLStripTags.ReplaceAllString(cur, "")

	outLines := strings.Split(cur, "\n")
	for idx, ln := range outLines {
		t := strings.TrimSpace(ln)
		if t == wsDetailsOpenSentinel || t == wsDetailsCloseSentinel || strings.HasPrefix(t, wsSummaryPrefix) {
			continue
		}
		decoded := html.UnescapeString(ln)
		decoded = strings.ReplaceAll(decoded, "\u00a0", " ")
		outLines[idx] = decoded
	}
	cur = strings.Join(outLines, "\n")

	for idx, span := range inlineCode {
		cur = strings.ReplaceAll(cur, fmt.Sprintf("\x00WS_IC_%d\x00", idx), span)
	}
	return cur
}

func stripHTMLCommentsOutsideFences(md string) string {
	lines := strings.Split(md, "\n")
	var prose strings.Builder
	var result []string
	inFence := false
	fenceDelim := ""

	flushProse := func() {
		if prose.Len() == 0 {
			return
		}
		stripped := stripHTMLComments(prose.String())
		stripped = strings.TrimSuffix(stripped, "\n")
		result = append(result, strings.Split(stripped, "\n")...)
		prose.Reset()
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if delim, _, ok := parseCodeFenceOpen(trimmed); ok {
				flushProse()
				inFence = true
				fenceDelim = delim
				result = append(result, line)
				continue
			}
			prose.WriteString(line)
			prose.WriteByte('\n')
		} else {
			result = append(result, line)
			if strings.HasPrefix(trimmed, fenceDelim) && strings.Trim(trimmed, string(fenceDelim[0])) == "" {
				inFence = false
				fenceDelim = ""
			}
		}
	}
	flushProse()
	return strings.Join(result, "\n")
}

func stripHTMLComments(s string) string {
	for {
		start := strings.Index(s, "<!--")
		if start < 0 {
			return s
		}
		end := strings.Index(s[start+4:], "-->")
		if end < 0 {
			return s[:start]
		}
		s = s[:start] + s[start+4+end+3:]
	}
}

func parseCodeFenceOpen(trimmed string) (delim string, lang string, ok bool) {
	for _, ch := range []byte{'`', '~'} {
		prefix := strings.Repeat(string(ch), 3)
		if strings.HasPrefix(trimmed, prefix) {
			i := 0
			for i < len(trimmed) && trimmed[i] == ch {
				i++
			}
			lang = strings.TrimSpace(trimmed[i:])
			return trimmed[:i], lang, true
		}
	}
	return "", "", false
}

func isHorizontalRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	var marker rune
	count := 0
	for _, r := range trimmed {
		if unicode.IsSpace(r) {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if marker == 0 {
			marker = r
		} else if marker != r {
			return false
		}
		count++
	}
	return count >= 3
}

func parseATXHeading(trimmed string) (level int, heading string, ok bool) {
	if !strings.HasPrefix(trimmed, "#") {
		return 0, "", false
	}
	for level < len(trimmed) && level < 6 && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) || trimmed[level] != ' ' {
		return 0, "", false
	}
	heading = strings.TrimSpace(trimmed[level+1:])
	heading = strings.TrimRight(heading, " #")
	return level, heading, heading != ""
}

func parseListItem(line string, r Roles) (indent int, marker string, body string, ok bool) {
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	rest := line[indent:]
	switch {
	case strings.HasPrefix(rest, "- [x] "), strings.HasPrefix(rest, "- [X] "),
		strings.HasPrefix(rest, "* [x] "), strings.HasPrefix(rest, "* [X] "):
		return indent, r.OK.Render("☑ "), strings.TrimSpace(rest[6:]), true
	case strings.HasPrefix(rest, "- [ ] "), strings.HasPrefix(rest, "* [ ] "):
		return indent, r.Muted.Render("☐ "), strings.TrimSpace(rest[6:]), true
	case strings.HasPrefix(rest, "- "), strings.HasPrefix(rest, "* "), strings.HasPrefix(rest, "+ "):
		return indent, r.Key.Render("• "), strings.TrimSpace(rest[2:]), true
	}

	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(rest) && (rest[digits] == '.' || rest[digits] == ')') && rest[digits+1] == ' ' {
		num := rest[:digits+1]
		return indent, r.Key.Render(num + " "), strings.TrimSpace(rest[digits+2:]), true
	}
	return 0, "", "", false
}

func parsePipeRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func isMarkdownTableSeparator(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.Contains(trimmed, "|") || !strings.Contains(trimmed, "-") {
		return false
	}
	cells := parsePipeRow(trimmed)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" {
			return false
		}
		dashes := 0
		for _, ch := range c {
			switch ch {
			case '-':
				dashes++
			case ':', ' ':
			default:
				return false
			}
		}
		if dashes == 0 {
			return false
		}
	}
	return true
}

func renderMarkdownTable(headers []string, rows [][]string, width int, r Roles) string {
	if len(headers) == 0 {
		return ""
	}
	tbl := lgtable.New().
		Headers(headers...).
		Rows(rows...).
		Width(width).
		Border(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderHeader(true).
		BorderColumn(true).
		BorderRow(false).
		BorderStyle(r.Border).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == lgtable.HeaderRow {
				if r.NoColor {
					return lipgloss.NewStyle()
				}
				return r.Header.Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
	return strings.TrimRight(tbl.Render(), "\n")
}

type inlineToken struct {
	raw    string
	styled string
}

func tokenizeInlineMarkdown(s string, r Roles, codeSt, boldSt, italicSt, strikeSt, linkSt lipgloss.Style) []inlineToken {
	var tokens []inlineToken
	appendWordSplits := func(raw string, styleFn func(string) string) {
		parts := splitKeepingSpaces(raw)
		for _, part := range parts {
			if part == "" {
				continue
			}
			if strings.TrimSpace(part) == "" {
				tokens = append(tokens, inlineToken{raw: part, styled: part})
				continue
			}
			if styleFn != nil {
				tokens = append(tokens, inlineToken{raw: part, styled: styleFn(part)})
			} else {
				tokens = append(tokens, inlineToken{raw: part})
			}
		}
	}

	i := 0
	var plainBuf strings.Builder
	flushPlain := func() {
		if plainBuf.Len() > 0 {
			appendWordSplits(plainBuf.String(), nil)
			plainBuf.Reset()
		}
	}

	for i < len(s) {

		if s[i] == '`' {
			if end := strings.IndexByte(s[i+1:], '`'); end >= 0 {
				flushPlain()
				inner := s[i+1 : i+1+end]
				appendWordSplits(inner, func(w string) string { return codeSt.Render(w) })
				i = i + 1 + end + 1
				continue
			}
		}

		if s[i] == '[' {
			if closeBr := strings.IndexByte(s[i+1:], ']'); closeBr >= 0 {
				after := i + 1 + closeBr + 1
				if after < len(s) && s[after] == '(' {
					if closePar := strings.IndexByte(s[after+1:], ')'); closePar >= 0 {
						flushPlain()
						label := s[i+1 : i+1+closeBr]
						linkURL := strings.TrimSpace(s[after+1 : after+1+closePar])
						if label != "" {
							appendWordSplits(label, func(w string) string { return linkSt.Render(w) })
						}
						if linkURL != "" && linkURL != label && !strings.HasPrefix(linkURL, "#") {
							tokens = append(tokens, inlineToken{raw: " ", styled: " "})
							tokens = append(tokens, inlineToken{
								raw:    fmt.Sprintf("(%s)", linkURL),
								styled: r.Muted.Render(fmt.Sprintf("(%s)", linkURL)),
							})
						}
						i = after + 1 + closePar + 1
						continue
					}
				}
			}
		}

		if i+1 < len(s) && ((s[i] == '*' && s[i+1] == '*') || (s[i] == '_' && s[i+1] == '_')) {
			delim := s[i : i+2]
			if end := strings.Index(s[i+2:], delim); end > 0 {
				flushPlain()
				inner := s[i+2 : i+2+end]
				appendWordSplits(inner, func(w string) string { return boldSt.Render(w) })
				i = i + 2 + end + 2
				continue
			}
		}

		if i+1 < len(s) && s[i] == '~' && s[i+1] == '~' {
			if end := strings.Index(s[i+2:], "~~"); end > 0 {
				flushPlain()
				inner := s[i+2 : i+2+end]
				appendWordSplits(inner, func(w string) string { return strikeSt.Render(w) })
				i = i + 2 + end + 2
				continue
			}
		}

		if s[i] == '*' && i+1 < len(s) && !unicode.IsSpace(rune(s[i+1])) {
			if end := strings.IndexByte(s[i+1:], '*'); end > 0 {
				flushPlain()
				inner := s[i+1 : i+1+end]
				appendWordSplits(inner, func(w string) string { return italicSt.Render(w) })
				i = i + 1 + end + 1
				continue
			}
		}
		plainBuf.WriteByte(s[i])
		i++
	}
	flushPlain()
	return tokens
}

func renderInlinePlainMarkdownFromTokens(tokens []inlineToken, defaultSt lipgloss.Style) string {
	var b strings.Builder
	for _, tok := range tokens {
		if tok.styled != "" {
			b.WriteString(tok.styled)
		} else {
			b.WriteString(defaultSt.Render(tok.raw))
		}
	}
	return b.String()
}

func renderInlinePlainMarkdown(s string, r Roles, codeSt, boldSt, italicSt, strikeSt, linkSt lipgloss.Style) string {
	tokens := tokenizeInlineMarkdown(s, r, codeSt, boldSt, italicSt, strikeSt, linkSt)
	var b strings.Builder
	for _, tok := range tokens {
		if tok.styled != "" {
			b.WriteString(tok.styled)
		} else {
			b.WriteString(tok.raw)
		}
	}
	return b.String()
}

func splitKeepingSpaces(s string) []string {
	var parts []string
	var cur strings.Builder
	inSpace := false
	for idx, r := range s {
		sp := unicode.IsSpace(r)
		if idx == 0 {
			inSpace = sp
		} else if sp != inSpace {
			parts = append(parts, cur.String())
			cur.Reset()
			inSpace = sp
		}
		if sp {
			cur.WriteRune(' ')
		} else {
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func wrapStyledTokens(tokens []inlineToken, width int, renderTok func(inlineToken) string) []string {
	if width <= 0 {
		width = 40
	}
	var lines []string
	var cur strings.Builder
	curW := 0

	flushLine := func() {
		lines = append(lines, strings.TrimRight(cur.String(), " "))
		cur.Reset()
		curW = 0
	}

	for _, tok := range tokens {
		isSpace := strings.TrimSpace(tok.raw) == ""
		if isSpace {
			if curW == 0 {
				continue
			}
			sw := lipgloss.Width(tok.raw)
			if curW+sw > width {
				flushLine()
				continue
			}
			cur.WriteString(" ")
			curW++
			continue
		}

		rendered := renderTok(tok)
		tw := lipgloss.Width(rendered)
		if curW > 0 && curW+tw > width {
			flushLine()
		}
		if tw > width && curW == 0 {
			lines = append(lines, TruncateDisplay(tok.raw, width))
			continue
		}
		cur.WriteString(rendered)
		curW += tw
	}
	if curW > 0 || len(lines) == 0 {
		flushLine()
	}
	return lines
}
