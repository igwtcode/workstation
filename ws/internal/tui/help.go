package tui

import (
	"fmt"
	"strings"
)

type helpGroup struct {
	title string
	rows  [][2]string
}

func (m Model) buildHelpGroups() []helpGroup {
	active := m.panels[m.activePanel]
	split := m.ActiveSplit()

	viewTitle := strings.ToUpper(active.ShortTitle())
	if subs := active.SubViews(); len(subs) > 0 {
		idx := clamp(active.ActiveSubView(), 0, len(subs)-1)
		viewTitle = strings.ToUpper(active.ShortTitle() + " · " + subs[idx])
	}

	var groups []helpGroup
	if hints := active.KeyHints(split, m.focusPreview); len(hints) > 0 {
		viewRows := make([][2]string, 0, len(hints))
		for _, h := range hints {
			viewRows = append(viewRows, [2]string{"<" + h[0] + ">", h[1]})
		}
		groups = append(groups, helpGroup{
			title: viewTitle,
			rows:  viewRows,
		})
	}

	if ap, ok := active.(ActionMenuProvider); ok {
		if actions := ap.RowActions(split, m.focusPreview); len(actions) > 0 {
			actRows := make([][2]string, 0, len(actions))
			for _, act := range actions {
				lbl := act.Label
				if act.Dangerous {
					lbl += " (confirm)"
				}
				actRows = append(actRows, [2]string{"a -> " + act.Key, lbl})
			}
			groups = append(groups, helpGroup{
				title: "ROW ACTIONS (a)",
				rows:  actRows,
			})
		}
	}

	groups = append(groups,
		helpGroup{
			title: "GLOBAL VERBS",
			rows: [][2]string{
				{"<a>", "Row actions menu"},
				{"<r>", "Refresh active view"},
				{"<y / Y>", "Copy primary / secondary"},
				{"<e>", "Edit in $VISUAL/$EDITOR"},
				{"<o>", "Open external / browser"},
				{"<d>", "Diff"},
				{"<i>", "Install"},
				{"<u>", "Update / pull / upgrade"},
				{"<x>", "Remove / delete / reset"},
				{"</>", "Fuzzy filter list"},
			},
		},
		helpGroup{
			title: "GENERAL",
			rows: [][2]string{
				{"<1-6>", "Switch workspace"},
				{"<0>", "Output console"},
				{"<P>", "Presets"},
				{"<ctrl+x>", "Cancel running command"},
				{"<[ / ]>", "Cycle sub-view"},
				{"<:cmd>", "Command palette"},
				{"<z>", "Cycle zone scope"},
				{"<space/*>", "Mark / toggle all"},
				{"<\\>", "Clear marks / filters"},
				{"<F / S>", "Filter / sort selector"},
				{"<ctrl+y>", "Copy CLI command"},
				{"<?>", "Toggle help"},
				{"<esc / q>", "Back one level / quit"},
				{"<ctrl+c>", "Force quit"},
			},
		},
		helpGroup{
			title: "NAVIGATION & LAYOUT",
			rows: [][2]string{
				{"<j / k>", "Down / Up"},
				{"<ctrl+d/u>", "Half-page down / up"},
				{"<ctrl+f/b>", "Page down / up"},
				{"<gg / G>", "Jump top / bottom"},
				{"<ctrl+o>", "Jump history back"},
				{"<p>", "Cycle split layout"},
				{"<tab>", "Switch pane focus"},
				{"<=>", "Zoom focused pane"},
			},
		},
	)
	return groups
}

func (m Model) buildHelpBodyLines(innerW int) []string {
	st := m.styles
	groups := m.buildHelpGroups()

	colCount := 2
	if innerW < 68 {
		colCount = 1
	} else if innerW >= 120 {
		colCount = 3
	}
	colGutter := 2
	colW := max(18, (innerW-(colCount-1)*colGutter)/colCount)
	keyW := min(14, max(9, colW/2-2))
	descW := max(8, colW-keyW-1)

	renderCol := func(g helpGroup) []string {
		out := []string{st.InfoText.Render(truncate(g.title, colW))}
		for _, r := range g.rows {
			kStr := padRight(truncate(r[0], keyW), keyW)
			dStr := truncate(r[1], descW)
			line := st.AccentText.Render(kStr) + " " + st.RowNormal.Render(dStr)
			out = append(out, fitStyledWidth(line, colW, st))
		}
		return out
	}

	var rendered [][]string
	for _, g := range groups {
		rendered = append(rendered, renderCol(g))
	}

	var body []string
	if colCount == 1 {
		for gi, colLines := range rendered {
			if gi > 0 {
				body = append(body, "")
			}
			body = append(body, colLines...)
		}
		return body
	}

	for start := 0; start < len(rendered); start += colCount {
		end := min(len(rendered), start+colCount)
		maxRows := 0
		for ci := start; ci < end; ci++ {
			if len(rendered[ci]) > maxRows {
				maxRows = len(rendered[ci])
			}
		}
		for r := 0; r < maxRows; r++ {
			var rowBuf strings.Builder
			for ci := start; ci < end; ci++ {
				if ci > start {
					rowBuf.WriteString(strings.Repeat(" ", colGutter))
				}
				if r < len(rendered[ci]) {
					rowBuf.WriteString(rendered[ci][r])
				} else {
					rowBuf.WriteString(strings.Repeat(" ", colW))
				}
			}
			body = append(body, fitStyledWidth(rowBuf.String(), innerW, st))
		}
		if end < len(rendered) {
			body = append(body, "")
		}
	}
	return body
}

func (m Model) helpMaxScroll(width, height int) int {
	innerW := max(16, width-4)
	innerH := max(4, height-2)
	all := m.buildHelpBodyLines(innerW)
	return max(0, len(all)-innerH)
}

func (m Model) renderHelpModal(width, height int) string {
	st := m.styles
	innerW := max(16, width-4)
	innerH := max(4, height-2)

	all := m.buildHelpBodyLines(innerW)
	maxScroll := max(0, len(all)-innerH)
	sc := clamp(m.helpScroll, 0, maxScroll)
	end := min(len(all), sc+innerH)
	vis := all[sc:end]

	badge := "esc/q close"
	if maxScroll > 0 {
		badge = fmt.Sprintf("%d-%d/%d · j/k scroll · esc/q close", sc+1, end, len(all))
	}
	title := "Help · " + m.panels[m.activePanel].ShortTitle()
	return renderTitledBox(title, badge, vis, width, height, true, st)
}
