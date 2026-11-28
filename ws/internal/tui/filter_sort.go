package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type FacetOption struct {
	Value string
	Label string
	Count int
}

type FacetGroup struct {
	Key     string
	Label   string
	Options []FacetOption
}

type SortOption struct {
	Key   string
	Label string
}

type FacetFilterState struct {
	Active    map[string]string
	SortKey   string
	SortDesc  bool
	Open      bool
	FocusSort bool
	GroupIdx  int
	OptIdx    int
	SortIdx   int
}

func (f *FacetFilterState) OpenFilters(groups []FacetGroup, sorts []SortOption) {
	f.Open = true
	f.FocusSort = false
	if len(groups) == 0 && len(sorts) > 0 {
		f.FocusSort = true
	}
	f.clampIndices(groups, sorts)
}

func (f *FacetFilterState) OpenSort(groups []FacetGroup, sorts []SortOption) {
	f.Open = true
	f.FocusSort = len(sorts) > 0
	f.syncSortIdx(sorts)
	f.clampIndices(groups, sorts)
}

func (f *FacetFilterState) Close() {
	f.Open = false
}

func (f *FacetFilterState) Clear() {
	f.Active = nil
	f.SortKey = ""
	f.SortDesc = false
}

func (f FacetFilterState) HasActive() bool {
	return len(f.Active) > 0 || f.SortKey != "" || f.SortDesc
}

func (f FacetFilterState) ActiveCount() int {
	n := len(f.Active)
	if f.SortKey != "" {
		n++
	}
	return n
}

func (f FacetFilterState) Get(groupKey string) string {
	if len(f.Active) == 0 {
		return ""
	}
	return f.Active[groupKey]
}

func (f FacetFilterState) Matches(groupKey, val string) bool {
	want := strings.TrimSpace(f.Get(groupKey))
	if want == "" {
		return true
	}
	return strings.EqualFold(want, strings.TrimSpace(val))
}

func (f FacetFilterState) Chips() []string {
	if len(f.Active) == 0 && f.SortKey == "" {
		return nil
	}
	var out []string

	keys := make([]string, 0, len(f.Active))
	for k, v := range f.Active {
		if v != "" {
			keys = append(keys, k)
		}
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		out = append(out, k+":"+f.Active[k])
	}
	if f.SortKey != "" {
		dir := "↑"
		if f.SortDesc {
			dir = "↓"
		}
		out = append(out, "sort:"+f.SortKey+dir)
	}
	return out
}

func (f *FacetFilterState) syncSortIdx(sorts []SortOption) {
	for i, s := range sorts {
		if s.Key == f.SortKey {
			f.SortIdx = i
			return
		}
	}
	if len(sorts) > 0 && f.SortIdx >= len(sorts) {
		f.SortIdx = 0
	}
}

func (f *FacetFilterState) clampIndices(groups []FacetGroup, sorts []SortOption) {
	if len(groups) == 0 {
		f.GroupIdx = 0
		f.OptIdx = 0
	} else {
		f.GroupIdx = clamp(f.GroupIdx, 0, len(groups)-1)
		opts := groups[f.GroupIdx].Options
		if len(opts) == 0 {
			f.OptIdx = 0
		} else {
			f.OptIdx = clamp(f.OptIdx, 0, len(opts)-1)
		}
	}
	if len(sorts) == 0 {
		f.SortIdx = 0
		f.FocusSort = false
	} else {
		f.SortIdx = clamp(f.SortIdx, 0, len(sorts)-1)
	}
}

func (f *FacetFilterState) Update(msg tea.KeyPressMsg, groups []FacetGroup, sorts []SortOption) (handled bool, changed bool) {
	if !f.Open {
		return false, false
	}
	f.clampIndices(groups, sorts)

	switch msg.String() {
	case "esc", "q":
		f.Open = false
		return true, false
	case "tab", "shift+tab":
		if len(groups) > 0 && len(sorts) > 0 {
			f.FocusSort = !f.FocusSort
		}
		return true, false
	case "c", `\`:
		wasActive := f.HasActive()
		f.Clear()
		return true, wasActive
	case "d":
		if len(sorts) > 0 {
			if f.SortKey == "" {
				f.SortKey = sorts[f.SortIdx].Key
			}
			f.SortDesc = !f.SortDesc
			return true, true
		}
		return true, false
	}

	if f.FocusSort {
		if isSpaceKey(msg) {
			if len(sorts) > 0 {
				f.applySortIdx(sorts)
				return true, true
			}
			return true, false
		}
		switch msg.String() {
		case "S":
			f.Open = false
			return true, false
		case "j", "down":
			if f.SortIdx < len(sorts)-1 {
				f.SortIdx++
			}
		case "k", "up":
			if f.SortIdx > 0 {
				f.SortIdx--
			}
		case "enter":
			f.Open = false
			return true, false
		}
		return true, false
	}

	if isSpaceKey(msg) {
		if f.toggleCurrentOption(groups) {
			return true, true
		}
		return true, false
	}

	switch msg.String() {
	case "F":
		f.Open = false
		return true, false
	case "h", "left":
		if len(groups) > 1 && f.GroupIdx > 0 {
			f.GroupIdx--
			f.OptIdx = 0
		}
	case "l", "right":
		if len(groups) > 1 && f.GroupIdx < len(groups)-1 {
			f.GroupIdx++
			f.OptIdx = 0
		}
	case "j", "down":
		if len(groups) > 0 {
			opts := groups[f.GroupIdx].Options
			if f.OptIdx < len(opts)-1 {
				f.OptIdx++
			} else if f.GroupIdx < len(groups)-1 {
				f.GroupIdx++
				f.OptIdx = 0
			}
		}
	case "k", "up":
		if len(groups) > 0 {
			if f.OptIdx > 0 {
				f.OptIdx--
			} else if f.GroupIdx > 0 {
				f.GroupIdx--
				f.OptIdx = max(0, len(groups[f.GroupIdx].Options)-1)
			}
		}
	case "enter":
		f.Open = false
		return true, false
	}
	return true, false
}

func (f *FacetFilterState) toggleCurrentOption(groups []FacetGroup) bool {
	if len(groups) == 0 || f.GroupIdx < 0 || f.GroupIdx >= len(groups) {
		return false
	}
	grp := groups[f.GroupIdx]
	if len(grp.Options) == 0 || f.OptIdx < 0 || f.OptIdx >= len(grp.Options) {
		return false
	}
	opt := grp.Options[f.OptIdx]
	if f.Active == nil {
		f.Active = map[string]string{}
	}
	if strings.EqualFold(f.Active[grp.Key], opt.Value) {
		delete(f.Active, grp.Key)
	} else {
		f.Active[grp.Key] = opt.Value
	}
	return true
}

func (f *FacetFilterState) applySortIdx(sorts []SortOption) {
	if len(sorts) == 0 || f.SortIdx < 0 || f.SortIdx >= len(sorts) {
		return
	}
	chosen := sorts[f.SortIdx].Key
	if f.SortKey == chosen {
		f.SortDesc = !f.SortDesc
		return
	}
	f.SortKey = chosen
	f.SortDesc = false
}

func (f FacetFilterState) ViewModal(title string, groups []FacetGroup, sorts []SortOption, width, height int, st Styles) (string, bool) {
	if !f.Open {
		return "", false
	}
	fCopy := f
	fCopy.clampIndices(groups, sorts)

	boxW := min(max(48, width-16), 82)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(18, boxW-st.ModalBox.GetHorizontalFrameSize())

	var parts []string
	for _, wl := range wrapPlainText("Filter & Sort Selector · "+title, innerW) {
		parts = append(parts, st.ModalTitle.Render(wl))
	}
	activeChips := fCopy.Chips()
	statusTxt := "Active: none (showing all)"
	if len(activeChips) > 0 {
		statusTxt = "Active: " + strings.Join(activeChips, " · ")
	}
	for _, wl := range wrapPlainText(statusTxt, innerW) {
		parts = append(parts, st.InfoText.Render(wl))
	}
	parts = append(parts, "")

	if len(groups) > 0 {
		parts = append(parts, st.SectionHeader.Render("1. Filter Criteria (←/→ group · space toggle · enter done)"))
		for gi, grp := range groups {
			isCurGrp := !fCopy.FocusSort && gi == fCopy.GroupIdx
			activeVal := fCopy.Get(grp.Key)
			hdrPrefix := "  "
			if isCurGrp {
				hdrPrefix = "▸ "
			}
			grpHdr := fmt.Sprintf("%s%s", hdrPrefix, grp.Label)
			if activeVal != "" {
				grpHdr += " [" + activeVal + "]"
			}
			if isCurGrp {
				parts = append(parts, st.AccentText.Render(grpHdr))
			} else {
				parts = append(parts, st.RowNormal.Render(grpHdr))
			}

			for oi, opt := range grp.Options {
				hovered := isCurGrp && oi == fCopy.OptIdx
				selected := strings.EqualFold(activeVal, opt.Value)
				mark := "[ ]"
				if selected {
					mark = "[x]"
				}
				cursor := "   "
				if hovered {
					cursor = " ▸ "
				}
				lineTxt := fmt.Sprintf("%s%s %-18s (%d)", cursor, mark, opt.Label, opt.Count)
				switch {
				case hovered:
					parts = append(parts, fitStyledWidth(st.RowSelected.Render(lineTxt), innerW, st))
				case selected:
					parts = append(parts, st.OKText.Render(lineTxt))
				default:
					parts = append(parts, st.Muted.Render(lineTxt))
				}
			}
		}
		parts = append(parts, "")
	}

	if len(sorts) > 0 {
		dirLabel := "ASC ↑"
		if fCopy.SortDesc {
			dirLabel = "DESC ↓"
		}
		parts = append(parts, st.SectionHeader.Render(fmt.Sprintf("2. Sort Order (%s — space select · d flip)", dirLabel)))
		for si, s := range sorts {
			hovered := fCopy.FocusSort && si == fCopy.SortIdx
			selected := fCopy.SortKey == s.Key
			mark := "( )"
			if selected {
				mark = "(●)"
			}
			cursor := "   "
			if hovered {
				cursor = " ▸ "
			}
			lineTxt := fmt.Sprintf("%s%s %s", cursor, mark, s.Label)
			switch {
			case hovered:
				parts = append(parts, fitStyledWidth(st.RowSelected.Render(lineTxt), innerW, st))
			case selected:
				parts = append(parts, st.OKText.Render(lineTxt))
			default:
				parts = append(parts, st.Muted.Render(lineTxt))
			}
		}
		parts = append(parts, "")
	}

	for _, wl := range wrapPlainText("j/k move · ←/→ group · space toggle/select · enter apply & close · tab filters/sort · c clear · esc close", innerW) {
		parts = append(parts, st.Muted.Render(wl))
	}

	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	if height > 0 && lipgloss.Height(modal) > height {
		modal = fitLines(modal, height)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal), true
}
