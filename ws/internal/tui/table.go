package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

func renderDataTable(headers []string, rows [][]string, selectedIdx int, focused bool, width, maxHeight int, st Styles) []string {
	return renderSelectableTable(headers, rows, selectedIdx, nil, focused, width, maxHeight, st)
}

func renderSelectableTable(headers []string, rows [][]string, selectedIdx int, marked map[int]bool, focused bool, width, maxHeight int, st Styles) []string {
	if width < 6 || len(headers) == 0 {
		return nil
	}

	total := len(rows)
	start, end := 0, total
	if maxHeight > 0 {
		availData := max(1, maxHeight-1)
		if total > availData {
			sel := clamp(selectedIdx, 0, max(0, total-1))
			start, end = clampViewport(sel, total, availData)
		}
	}

	hasSelectable := marked != nil
	showCursorPad := hasSelectable || selectedIdx >= 0

	dispHeaders := append([]string(nil), headers...)
	if showCursorPad && len(dispHeaders) > 0 {
		if hasSelectable {
			dispHeaders[0] = "   " + dispHeaders[0]
		} else {
			dispHeaders[0] = "  " + dispHeaders[0]
		}
	}

	visRows := make([][]string, 0, max(0, end-start))
	relMarked := map[int]bool{}
	relSelected := -1
	if selectedIdx >= start && selectedIdx < end {
		relSelected = selectedIdx - start
	}

	for idx := start; idx < end; idx++ {
		relIdx := idx - start
		isSel := relIdx == relSelected
		isMarked := marked != nil && marked[idx]
		if isMarked {
			relMarked[relIdx] = true
		}

		src := rows[idx]
		clean := make([]string, len(headers))
		for c := 0; c < len(headers); c++ {
			val := ""
			if c < len(src) {
				val = src[c]
			}
			val = strings.ReplaceAll(val, "\r", "")
			val = strings.ReplaceAll(val, "\n", " ")

			if isMarked || st.Roles.NoColor {
				val = ui.StripANSI(val)
			}
			clean[c] = val
		}
		if len(clean) > 0 && showCursorPad {
			if hasSelectable {
				prefix := "   "
				switch {
				case isSel && isMarked && focused:
					prefix = ">* "
				case isSel && isMarked:
					prefix = "▸* "
				case isSel && focused:
					prefix = ">  "
				case isSel:
					prefix = "▸  "
				case isMarked:
					prefix = " * "
				}
				clean[0] = prefix + clean[0]
			} else {
				prefix := "  "
				switch {
				case isSel && focused:
					prefix = "> "
				case isSel:
					prefix = "▸ "
				}
				clean[0] = prefix + clean[0]
			}
		}
		visRows = append(visRows, clean)
	}

	gap := 2
	if width < 44 {
		gap = 1
	}
	colW := computeTableColWidths(dispHeaders, visRows, width, gap)
	sep := strings.Repeat(" ", gap)

	hdrSt := st.TableHeader
	markedSt := st.RowMarked
	selMarkedSt := st.RowSelectedMarked
	unfocusSelSt := lipgloss.NewStyle().Bold(true)
	if st.Roles.NoColor {
		hdrSt = lipgloss.NewStyle()
		markedSt = lipgloss.NewStyle()
		selMarkedSt = lipgloss.NewStyle()
		unfocusSelSt = lipgloss.NewStyle()
	}

	hdrParts := make([]string, len(dispHeaders))
	for c, h := range dispHeaders {
		cell := fitCellDisplay(h, colW[c])
		if st.Roles.NoColor {
			hdrParts[c] = ui.StripANSI(cell)
		} else {
			hdrParts[c] = hdrSt.Render(cell)
		}
	}
	hdrLine := strings.Join(hdrParts, sep)
	if lipgloss.Width(hdrLine) > width {
		hdrLine = fitStyledWidth(hdrLine, width, st)
	}

	lines := make([]string, 0, 1+len(visRows))
	lines = append(lines, hdrLine)

	for relIdx, r := range visRows {
		rowParts := make([]string, len(dispHeaders))
		for c := 0; c < len(dispHeaders); c++ {
			val := ""
			if c < len(r) {
				val = r[c]
			}
			rowParts[c] = fitCellDisplay(val, colW[c])
		}
		line := strings.Join(rowParts, sep)
		if w := lipgloss.Width(line); w < width {
			line += strings.Repeat(" ", width-w)
		} else if w > width {
			line = fitStyledWidth(line, width, st)
		}

		isSel := relIdx == relSelected
		isMarked := relMarked[relIdx]

		switch {
		case st.Roles.NoColor:
			line = ui.StripANSI(line)
		case isSel && isMarked && focused:
			line = selMarkedSt.Render(ui.StripANSI(line))
		case isSel && focused:
			line = applyRowHighlightBG(line, st.SelBgHex, st.SelFgHex)
		case isMarked:
			line = markedSt.Render(ui.StripANSI(line))
		case isSel && !focused:
			line = unfocusSelSt.Render(line)
		}
		lines = append(lines, line)
	}

	if maxHeight > 0 && len(lines) > maxHeight {
		lines = lines[:maxHeight]
	}
	return lines
}

func fitCellDisplay(s string, cellW int) string {
	if cellW <= 0 {
		return ""
	}
	curW := lipgloss.Width(s)
	if curW > cellW {
		s = ui.TruncateDisplay(s, cellW)
		curW = lipgloss.Width(s)
	}
	if rem := cellW - curW; rem > 0 {
		return s + strings.Repeat(" ", rem)
	}
	return s
}

func applyRowHighlightBG(line, bgHex, fgHex string) string {
	br, bg, bb, okBG := parseHexRGB(bgHex)
	if !okBG {
		return line
	}
	bgSeq := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", br, bg, bb)
	fgSeq := ""
	if fr, fg, fb, okFG := parseHexRGB(fgHex); okFG {
		fgSeq = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", fr, fg, fb)
	}

	var b strings.Builder
	b.Grow(len(line) + 64)
	b.WriteString(bgSeq)
	b.WriteString(fgSeq)

	i := 0
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j < len(line) {
				term := line[j]
				params := line[i+2 : j]
				b.WriteString(line[i : j+1])
				if term == 'm' {
					needBG, needFG := inspectSGRResets(params)
					if needBG {
						b.WriteString(bgSeq)
					}
					if needFG && fgSeq != "" {
						b.WriteString(fgSeq)
					}
				}
				i = j + 1
				continue
			}
		}
		b.WriteByte(line[i])
		i++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

func inspectSGRResets(params string) (needBG, needFG bool) {
	if params == "" {
		return true, true
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		rawPart := parts[i]
		p := rawPart
		hasColon := false
		if c := strings.IndexByte(rawPart, ':'); c >= 0 {
			p = rawPart[:c]
			hasColon = true
		}
		switch p {
		case "", "0":
			needBG = true
			needFG = true
		case "49":
			needBG = true
		case "39":
			needFG = true
		case "38", "48", "58":

			if !hasColon && i+1 < len(parts) {
				switch parts[i+1] {
				case "5":
					i += 2
				case "2":
					i += 4
				}
			}
		}
	}
	return needBG, needFG
}

func columnFlexTier(header string, natW int) int {
	h := strings.ToLower(strings.TrimSpace(ui.StripANSI(header)))
	switch h {
	case "on", "kind", "state", "sync", "zone", "forge", "plan", "exit", "dur", "size",
		"sec", "vars", "shad", "locs", "tier", "shell", "done", "use", "val", "#",
		"match", "status", "updated", "time", "severity", "os/arch", "trans", "flags",
		"synced", "locals", "ws.db", "gh", "cost", "tokens", "pkgs", "hook", "provider",
		"category", "app":
		return 0
	case "title", "path", "paths", "command", "location", "active source", "description",
		"finding", "message", "rendered output", "value", "step", "origin",
		"swatches", "change", "file", "target", "model":
		return 2
	}
	if natW <= 6 {
		return 0
	}
	return 1
}

func computeTableColWidths(headers []string, rows [][]string, totalWidth, gap int) []int {
	n := len(headers)
	if n == 0 || totalWidth <= 0 {
		return nil
	}
	budget := max(n*2, totalWidth-(n-1)*gap)
	natW := make([]int, n)
	tiers := make([]int, n)
	sum := 0
	for c := 0; c < n; c++ {
		w := lipgloss.Width(ui.StripANSI(headers[c]))
		for _, r := range rows {
			if c < len(r) {
				if cw := lipgloss.Width(ui.StripANSI(r[c])); cw > w {
					w = cw
				}
			}
		}
		if w < 2 {
			w = 2
		}
		natW[c] = w
		tiers[c] = columnFlexTier(headers[c], w)
		sum += w
	}

	colW := append([]int(nil), natW...)
	if sum < budget {
		extra := budget - sum

		if extra > n*2 {
			for c := 0; c < n && extra > 0; c++ {
				if tiers[c] == 0 {
					add := min(2, extra)
					colW[c] += add
					extra -= add
				}
			}
		}

		for c := 0; c < n && extra > 0; c++ {
			if tiers[c] == 1 {
				add := min(8, extra)
				colW[c] += add
				extra -= add
			}
		}

		var t2Idx []int
		for c := 0; c < n; c++ {
			if tiers[c] == 2 {
				t2Idx = append(t2Idx, c)
			}
		}
		if len(t2Idx) > 0 {
			for pass := 0; pass < 40 && extra > 0; pass++ {
				grew := false
				for _, c := range t2Idx {
					if extra <= 0 {
						break
					}
					capW := max(natW[c]+28, 48)
					if c == n-1 {
						capW = max(natW[c]+48, 72)
					}
					if colW[c] < capW {
						colW[c]++
						extra--
						grew = true
					}
				}
				if !grew {
					break
				}
			}
		} else {

			targetCol := n - 1
			for c := n - 1; c >= 0; c-- {
				if tiers[c] > 0 {
					targetCol = c
					break
				}
			}
			add := min(extra, 16)
			colW[targetCol] += add
		}
		return colW
	}

	shrinkTier := func(targetTier, minKeep int) {
		for sum > budget {
			widest := -1
			widestW := 0
			for c, w := range colW {
				if tiers[c] == targetTier && w > minKeep && w > widestW {
					widestW = w
					widest = c
				}
			}
			if widest < 0 {
				break
			}
			colW[widest]--
			sum--
		}
	}
	shrinkTier(2, 12)
	shrinkTier(1, 10)
	shrinkTier(2, 8)
	shrinkTier(1, 6)
	shrinkTier(0, 4)
	for sum > budget {
		widest := -1
		widestW := 0
		for c, w := range colW {
			if w > 2 && w > widestW {
				widestW = w
				widest = c
			}
		}
		if widest < 0 {
			break
		}
		colW[widest]--
		sum--
	}
	return colW
}

func renderKVTable(pairs [][2]string, width int, st Styles) []string {
	if width < 8 || len(pairs) == 0 {
		return nil
	}

	keyW := 6
	for _, p := range pairs {
		if kw := lipgloss.Width(ui.StripANSI(p[0])); kw > keyW {
			keyW = kw
		}
	}
	maxKeyW := max(6, min(18, width/3))
	if keyW > maxKeyW {
		keyW = maxKeyW
	}
	valW := max(8, width-keyW-2)
	indent := strings.Repeat(" ", keyW+2)

	var out []string
	for _, p := range pairs {
		kPlain := padRight(truncate(ui.StripANSI(p[0]), keyW), keyW)
		kStyled := kPlain
		if !st.Roles.NoColor {
			kStyled = st.Muted.Render(kPlain)
		}
		valLines := wrapKVValue(p[1], valW, st.Roles.NoColor)
		for li, vl := range valLines {
			if st.Roles.NoColor {
				vl = ui.StripANSI(vl)
			}
			if li == 0 {
				out = append(out, fitStyledWidth(kStyled+"  "+vl, width, st))
			} else {
				out = append(out, fitStyledWidth(indent+vl, width, st))
			}
		}
	}
	return out
}

func wrapKVValue(val string, maxW int, noColor bool) []string {
	val = strings.ReplaceAll(val, "\r", "")
	val = strings.ReplaceAll(val, "\n", " ")
	if noColor {
		val = ui.StripANSI(val)
	}
	if maxW <= 0 || lipgloss.Width(val) <= maxW {
		return []string{val}
	}

	if strings.Contains(val, "\x1b") {
		plain := ui.StripANSI(val)
		if lipgloss.Width(plain) <= maxW {
			return []string{val}
		}
		val = plain
	}
	var lines []string
	words := strings.Fields(val)
	if len(words) == 0 {
		return []string{""}
	}
	cur := ""
	for _, w := range words {
		if lipgloss.Width(w) > maxW {
			if cur != "" {
				lines = append(lines, cur)
				cur = ""
			}
			lines = append(lines, ui.TruncateDisplay(w, maxW))
			continue
		}
		if cur == "" {
			cur = w
		} else if lipgloss.Width(cur)+1+lipgloss.Width(w) <= maxW {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
