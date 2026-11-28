package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type RowAction struct {
	Key         string
	Label       string
	Description string
	Dangerous   bool
}

type ActionMenuProvider interface {
	RowActions(split SplitMode, focusPreview bool) []RowAction
	RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd)
}

type actionMenuState struct {
	Title   string
	Actions []RowAction
	Cursor  int
}

func (a *actionMenuState) updateKey(msg tea.KeyPressMsg) (chosen *RowAction, closed bool) {
	key := msg.String()
	switch key {
	case "esc", "q":
		return nil, true
	case "j", "down", "ctrl+n":
		if len(a.Actions) > 0 {
			a.Cursor = (a.Cursor + 1) % len(a.Actions)
		}
		return nil, false
	case "k", "up", "ctrl+p":
		if len(a.Actions) > 0 {
			a.Cursor = (a.Cursor - 1 + len(a.Actions)) % len(a.Actions)
		}
		return nil, false
	case "home":
		a.Cursor = 0
		return nil, false
	case "end", "G":
		if len(a.Actions) > 0 {
			a.Cursor = len(a.Actions) - 1
		}
		return nil, false
	case "enter":
		if len(a.Actions) > 0 && a.Cursor >= 0 && a.Cursor < len(a.Actions) {
			act := a.Actions[a.Cursor]
			return &act, true
		}
		return nil, true
	}
	for i := range a.Actions {
		if a.Actions[i].Key == key {
			act := a.Actions[i]
			return &act, true
		}
	}
	return nil, false
}

func renderActionMenuModal(menu *actionMenuState, width, height int, st Styles) string {
	if menu == nil {
		return ""
	}
	boxW := min(max(44, width-14), 76)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(16, boxW-st.ModalBox.GetHorizontalFrameSize())

	title := nonEmpty(menu.Title, "Actions")
	parts := []string{
		st.ModalTitle.Render(truncate("Actions · "+title, innerW)),
		"",
	}

	maxItems := max(3, height-8)
	start, end := clampViewport(menu.Cursor, len(menu.Actions), min(len(menu.Actions), maxItems))

	keyW := 5
	for _, act := range menu.Actions {
		if w := len("<"+act.Key+">") + 1; w > keyW && w <= 12 {
			keyW = w
		}
	}
	lblW := 20
	for _, act := range menu.Actions {
		if w := lipgloss.Width(act.Label) + 2; w > lblW && w <= max(20, innerW/2) {
			lblW = w
		}
	}

	for i := start; i < end; i++ {
		act := menu.Actions[i]
		isSel := i == menu.Cursor
		prefix := "  "
		if isSel {
			prefix = "▸ "
			if st.Roles.NoColor {
				prefix = "> "
			}
		}
		kCol := padRight("<"+act.Key+">", keyW)
		lCol := padRight(truncate(act.Label, lblW), lblW)
		descMax := max(6, innerW-2-keyW-lblW-1)
		dCol := truncate(act.Description, descMax)
		if act.Dangerous && descMax > 10 {
			dCol = truncate(act.Description+" [confirm]", descMax)
		}

		if st.Roles.NoColor {
			parts = append(parts, fitStyledWidth(prefix+kCol+lCol+" "+dCol, innerW, st))
			continue
		}
		kStyle := st.AccentText
		lStyle := st.RowNormal
		if act.Dangerous {
			kStyle = st.WarnText
		}
		if isSel {
			lStyle = st.AccentText
			row := prefix + kStyle.Render(kCol) + lStyle.Render(lCol) + " " + st.Muted.Render(dCol)
			parts = append(parts, fitStyledWidth(st.RowSelected.Render(prefix+kCol+lCol+" "+dCol), innerW, st))
			_ = row
			continue
		}
		row := prefix + kStyle.Render(kCol) + lStyle.Render(lCol) + " " + st.Muted.Render(dCol)
		parts = append(parts, fitStyledWidth(row, innerW, st))
	}

	if len(menu.Actions) > maxItems {
		parts = append(parts, st.Muted.Render(fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(menu.Actions))))
	}
	parts = append(parts, "", st.Muted.Render(truncate("press key or j/k + enter · esc/q close", innerW)))

	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	if height > 0 && lipgloss.Height(modal) > height {
		modal = fitLines(modal, height)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}
