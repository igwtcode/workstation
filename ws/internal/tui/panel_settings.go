package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

const (
	settingsSubAll = iota
	settingsSubFiles
	settingsSubCount
)

type settingsPanel struct {
	subView          int
	view             SettingsView
	filtered         []SettingRow
	filteredWatched  []WatchedFileRow
	cursor           int
	watchedCursor    int
	filterQuery      string
	filters          FacetFilterState
	loaded           bool
	loadErr          string
	saving           bool
	reloading        bool
	previewScroll    int
	drillOpen        bool
	editOpen         bool
	editRow          SettingRow
	editInput        textinput.Model
	editErr          string
	pickOpen         bool
	pickRow          SettingRow
	pickInput        textinput.Model
	pickFiltered     []string
	pickCursor       int
	lastReloadAt     time.Time
	lastReloadReason string
	reloadCount      int
	loadActID        int
	saveActID        int
	reloadActID      int
}

func newSettingsPanel() settingsPanel {
	ti := textinput.New()
	ti.CharLimit = 512
	ti.SetWidth(48)
	pi := textinput.New()
	pi.CharLimit = 128
	pi.Prompt = ""
	pi.Placeholder = "Filter options..."
	pi.SetWidth(40)
	return settingsPanel{
		editInput: ti,
		pickInput: pi,
	}
}

func (p settingsPanel) ID() string         { return "settings" }
func (p settingsPanel) Title() string      { return "Settings & Auto-Reload" }
func (p settingsPanel) ShortTitle() string { return "Settings" }
func (p settingsPanel) SubViews() []string { return []string{"Settings", "Watched Files"} }
func (p settingsPanel) ActiveSubView() int { return p.subView }
func (p settingsPanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < settingsSubCount {
		p.subView = idx
		p.drillOpen = false
		p.previewScroll = 0
		p.filters.Close()
	}
	return p
}
func (p settingsPanel) OnSubViewChanged(Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p settingsPanel) SetScope(string, Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p settingsPanel) IsZoned() bool                                     { return false }
func (p settingsPanel) FilterQuery() string                               { return p.filterQuery }
func (p settingsPanel) FilterTarget() string {
	if p.subView == settingsSubFiles {
		return "Watched Files"
	}
	return "Settings"
}
func (p settingsPanel) SelectedCount() int            { return 0 }
func (p settingsPanel) ClearSelection() ResourcePanel { return p }
func (p settingsPanel) SupportsPreview() bool         { return !p.drillOpen }
func (p settingsPanel) DefaultSplit(width int) SplitMode {
	if width >= 112 {
		return SplitVertical
	}
	return SplitOff
}
func (p settingsPanel) CanPop() bool { return p.drillOpen }
func (p settingsPanel) Pop() ResourcePanel {
	p.drillOpen = false
	p.previewScroll = 0
	return p
}

func (p settingsPanel) SetFilterQuery(q string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.recomputeFiltered()
	p.cursor = 0
	p.watchedCursor = 0
	p.previewScroll = 0
	return p, nil
}

func (p settingsPanel) OnBlur() ResourcePanel {
	p.filters.Close()
	if p.editOpen {
		p.editInput.Blur()
		p.editOpen = false
	}
	return p
}

func (p *settingsPanel) recomputeFiltered() {
	pre := fuzzy.Filter(p.view.Items, p.filterQuery, func(r SettingRow) ([]string, map[string]string) {
		ov := "false"
		if r.Overridden {
			ov = "true"
		}
		return []string{r.Key, r.Category, r.Value, r.Source, r.Description},
			map[string]string{
				"cat":      r.Category,
				"source":   r.Source,
				"override": ov,
			}
	})

	out := make([]SettingRow, 0, len(pre))
	for _, r := range pre {
		if !p.filters.Matches("cat", r.Category) {
			continue
		}
		if !p.filters.Matches("source", r.Source) {
			continue
		}
		out = append(out, r)
	}
	if p.filters.SortKey != "" {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			var less bool
			switch p.filters.SortKey {
			case "key":
				less = a.Key < b.Key
			case "source":
				if a.Source != b.Source {
					less = a.Source < b.Source
				} else {
					less = a.Key < b.Key
				}
			default:
				if a.Category != b.Category {
					less = a.Category < b.Category
				} else {
					less = a.Key < b.Key
				}
			}
			if p.filters.SortDesc {
				return !less
			}
			return less
		})
	}
	p.filtered = out
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}

	p.filteredWatched = fuzzy.Filter(p.view.Watched, p.filterQuery, func(wf WatchedFileRow) ([]string, map[string]string) {
		st := "missing"
		if wf.Exists {
			st = "exists"
		}
		return []string{wf.Label, wf.Path, st},
			map[string]string{"state": st}
	})
	if p.watchedCursor >= len(p.filteredWatched) {
		p.watchedCursor = max(0, len(p.filteredWatched)-1)
	}
}

func (p settingsPanel) settingsFacetGroups() ([]FacetGroup, []SortOption) {
	catCounts := map[string]int{}
	srcCounts := map[string]int{}
	for _, r := range p.view.Items {
		catCounts[r.Category]++
		srcCounts[r.Source]++
	}
	var catOpts []FacetOption
	for _, c := range []string{"tui", "workspace", "vault", "fleet", "paths"} {
		if n := catCounts[c]; n > 0 {
			catOpts = append(catOpts, FacetOption{Value: c, Label: c, Count: n})
		}
	}
	var srcOpts []FacetOption
	for _, s := range []string{"override", "flag", "env", "mise.local", "ledger", "default", "unset", "readonly"} {
		if n := srcCounts[s]; n > 0 {
			srcOpts = append(srcOpts, FacetOption{Value: s, Label: s, Count: n})
		}
	}
	var groups []FacetGroup
	if len(catOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "cat", Label: "Category", Options: catOpts})
	}
	if len(srcOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "source", Label: "Source", Options: srcOpts})
	}
	sorts := []SortOption{
		{Key: "category", Label: "Category"},
		{Key: "key", Label: "Setting Key"},
		{Key: "source", Label: "Source"},
	}
	return groups, sorts
}

func (p settingsPanel) Attention() AttentionLevel {
	if p.loadErr != "" || p.view.ConfigErr != "" || p.view.MiseLocalErr != "" {
		return AttentionErr
	}
	return AttentionNone
}

func (p settingsPanel) PaneStatus() string {
	switch {
	case p.reloading:
		return "reloading config..."
	case p.saving:
		return "saving override..."
	case p.loadActID != 0:
		return "loading settings..."
	default:
		return ""
	}
}

func (p settingsPanel) Badge() string {
	if !p.loaded {
		return "…"
	}
	ov := 0
	for _, it := range p.view.Items {
		if it.Overridden {
			ov++
		}
	}
	rel := "manual"
	if p.view.AutoReload {
		rel = fmt.Sprintf("auto:%ds", max(1, p.view.AutoReloadSecs))
	}
	return fmt.Sprintf("%d ov · %s", ov, rel)
}

func (p settingsPanel) Breadcrumbs() []string {
	if p.subView == settingsSubFiles {
		if wf, ok := p.selectedWatched(); ok && p.drillOpen {
			return []string{"Settings", "Watched Files", wf.Label}
		}
		return []string{"Settings", fmt.Sprintf("Watched Files (%d)", len(p.filteredWatched))}
	}
	if p.drillOpen {
		if r, ok := p.selectedSetting(); ok {
			return []string{"Settings", r.Key}
		}
	}
	return []string{"Settings", fmt.Sprintf("Config (%d)", len(p.filtered))}
}

func (p settingsPanel) StatusChips() []string {
	var chips []string
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	if p.subView == settingsSubAll {
		chips = append(chips, p.filters.Chips()...)
	}
	if p.loaded {
		if p.view.AutoReload {
			chips = append(chips, fmt.Sprintf("reload:%ds", max(1, p.view.AutoReloadSecs)))
		} else {
			chips = append(chips, "reload:off")
		}
	}
	return chips
}

func (p settingsPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.subView == settingsSubFiles {
		return [][2]string{
			{"enter/e", "Edit in $EDITOR"},
			{"y", "Copy path"},
			{"r", "Refresh"},
			{"a", "Actions"},
		}
	}
	return [][2]string{
		{"enter/space", "Pick / Toggle"},
		{"x", "Reset override"},
		{"e", "Edit config.toml"},
		{"y", "Copy CLI"},
		{"r", "Refresh"},
		{"a", "Actions"},
		{"F/S", "Filter / Sort"},
	}
}

func (p settingsPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	if p.subView == settingsSubFiles {
		return []RowAction{
			{Key: "R", Label: "Reload all configs", Description: "Reload config.toml, mise.local.toml & theme now"},
			{Key: "A", Label: "Toggle auto-reload", Description: "Enable or disable automatic config file watching"},
		}
	}
	return []RowAction{
		{Key: "c", Label: "Custom text value", Description: "Edit selected setting with freeform text input"},
		{Key: "A", Label: "Toggle auto-reload", Description: "Enable or disable automatic config file watching"},
		{Key: "R", Label: "Reload all configs", Description: "Reload config.toml, mise.local.toml & theme now"},
	}
}

func (p settingsPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch actionKey {
	case "c":
		if p.subView == settingsSubAll {
			return p.activateSelectedSetting(backend, true)
		}
	case "A":
		next := "false"
		if !p.view.AutoReload {
			next = "true"
		}
		p.saving = true
		p.saveActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.saveActID, "Toggling auto-reload"),
			setSettingCmd(backend, "tui.auto_reload", next),
		)
	case "R":
		p.reloading = true
		p.reloadActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.reloadActID, "Reloading config"),
			reloadConfigCmd(backend, "manual"),
		)
	}
	return p, nil
}

func (p settingsPanel) CapturingInput() bool {
	return p.editOpen || (p.pickOpen && len(p.pickRow.Options) > 5)
}
func (p settingsPanel) HasOpenModal() bool {
	return p.editOpen || p.pickOpen || p.filters.Open
}
func (p settingsPanel) MutationInFlight() bool {
	return p.saving || p.reloading
}

func (p settingsPanel) selectedSetting() (SettingRow, bool) {
	if len(p.filtered) == 0 || p.cursor < 0 || p.cursor >= len(p.filtered) {
		return SettingRow{}, false
	}
	return p.filtered[p.cursor], true
}

func (p settingsPanel) selectedWatched() (WatchedFileRow, bool) {
	if len(p.filteredWatched) == 0 || p.watchedCursor < 0 || p.watchedCursor >= len(p.filteredWatched) {
		return WatchedFileRow{}, false
	}
	return p.filteredWatched[p.watchedCursor], true
}

func (p settingsPanel) CLIEquivalent() string {
	if p.subView == settingsSubFiles {
		if wf, ok := p.selectedWatched(); ok {
			return wf.Path
		}
		return "ws config path"
	}
	if r, ok := p.selectedSetting(); ok {
		if r.ReadOnly {
			return "ws config show"
		}
		return fmt.Sprintf("ws config set %s %q", r.Key, r.Value)
	}
	return "ws config show"
}

func (p settingsPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsLoadedMsg:
		p.loaded = true
		var cmds []tea.Cmd
		if p.loadActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.loadActID, "Loading settings", msg.err != nil))
			p.loadActID = 0
		}
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Settings load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.loadErr = ""
		p.view = msg.view
		p.recomputeFiltered()
		return p, batchNonNil(cmds...)

	case settingSavedMsg:
		p.saving = false
		var cmds []tea.Cmd
		if p.saveActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.saveActID, "Saving "+msg.key, msg.err != nil))
			p.saveActID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Save failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.view = msg.view
		p.recomputeFiltered()
		toast := fmt.Sprintf("Saved %s = %s", msg.key, nonEmpty(msg.val, "default"))
		if msg.val == "" {
			toast = fmt.Sprintf("Reset override for %s", msg.key)
		}
		cmds = append(cmds, func() tea.Msg { return statusToastMsg{text: toast} })
		return p, batchNonNil(cmds...)

	case configReloadedMsg:
		p.reloading = false
		var cmds []tea.Cmd
		if p.reloadActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.reloadActID, "Reloading config", msg.err != nil))
			p.reloadActID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Config reload error: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.loadErr = ""
		p.view = msg.view
		p.recomputeFiltered()
		p.lastReloadAt = time.Now()
		p.lastReloadReason = msg.reason
		if len(msg.changedFiles) > 0 {
			p.lastReloadReason = strings.Join(msg.changedFiles, ", ")
		}
		p.reloadCount++
		return p, batchNonNil(cmds...)

	case configEditFinishedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: "Editor failed: " + msg.err.Error(), isErr: true}
			}
		}
		p.reloading = true
		p.reloadActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.reloadActID, "Reloading after edit"),
			reloadConfigCmd(backend, "editor"),
		)

	case tea.KeyPressMsg:
		if p.editOpen {
			return p.updateEditModal(msg, backend)
		}
		if p.pickOpen {
			return p.updatePickModal(msg, backend)
		}
		if p.filters.Open && p.subView == settingsSubAll {
			groups, sorts := p.settingsFacetGroups()
			handled, changed := p.filters.Update(msg, groups, sorts)
			if changed {
				p.recomputeFiltered()
				p.cursor = 0
				p.previewScroll = 0
			}
			if handled {
				return p, nil
			}
		}

		if !p.drillOpen && isSpaceKey(msg) {
			if p.subView == settingsSubAll {
				return p.activateSelectedSetting(backend, false)
			}
			return p, nil
		}

		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview || p.drillOpen {
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
				return p, nil
			}
			if p.subView == settingsSubFiles {
				if next, changed := applyListMotion(p.watchedCursor, len(p.filteredWatched), defaultMotionPage, m); changed {
					p.watchedCursor = next
					p.previewScroll = 0
				}
				return p, nil
			}
			if next, changed := applyListMotion(p.cursor, len(p.filtered), defaultMotionPage, m); changed {
				p.cursor = next
				p.previewScroll = 0
			}
			return p, nil
		}

		switch msg.String() {
		case "F":
			if p.subView == settingsSubAll {
				groups, sorts := p.settingsFacetGroups()
				p.filters.OpenFilters(groups, sorts)
			}
		case "S":
			if p.subView == settingsSubAll {
				groups, sorts := p.settingsFacetGroups()
				p.filters.OpenSort(groups, sorts)
			}
		case "enter":
			if p.subView == settingsSubFiles {
				return p.editSelectedWatchedFile(backend)
			}
			return p.activateSelectedSetting(backend, false)
		case "x":
			if p.subView == settingsSubAll {
				return p.resetSelectedSetting(backend)
			}
		case "e":
			if p.subView == settingsSubFiles {
				return p.editSelectedWatchedFile(backend)
			}
			return p.editConfigFile(backend, p.view.ConfigPath)
		case "y":
			return p, copyCmd(p.CLIEquivalent(), "setting command/path")
		case "r":
			p.loadActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.loadActID, "Loading settings"),
				loadSettingsCmd(backend),
			)
		}
	}
	return p, nil
}

func (p settingsPanel) activateSelectedSetting(backend Backend, forceTextModal bool) (ResourcePanel, tea.Cmd) {
	r, ok := p.selectedSetting()
	if !ok {
		return p, nil
	}
	if r.ReadOnly {
		return p, func() tea.Msg {
			return statusToastMsg{text: "Read-only runtime path (press y to copy)"}
		}
	}
	if !forceTextModal {
		if r.Kind == "bool" {
			next := "true"
			if strings.EqualFold(r.Value, "true") {
				next = "false"
			}
			p.saving = true
			p.saveActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.saveActID, "Updating "+r.Key),
				setSettingCmd(backend, r.Key, next),
			)
		}
		if len(r.Options) > 0 {
			p.editOpen = false
			p.editInput.Blur()
			p.pickOpen = true
			p.pickRow = r
			p.pickInput.SetValue("")
			p.refilterPickOptions(true)
			if len(r.Options) > 5 {
				return p, p.pickInput.Focus()
			}
			p.pickInput.Blur()
			return p, nil
		}
	}

	p.pickOpen = false
	p.pickInput.Blur()
	p.editOpen = true
	p.editRow = r
	p.editErr = ""
	p.editInput.SetValue(r.Value)
	p.editInput.CursorEnd()
	return p, p.editInput.Focus()
}

func (p *settingsPanel) refilterPickOptions(selectActive bool) {
	q := strings.TrimSpace(p.pickInput.Value())
	opts := p.pickRow.Options
	if q == "" {
		p.pickFiltered = append([]string(nil), opts...)
		if selectActive {
			p.pickCursor = 0
			for i, opt := range p.pickFiltered {
				if opt == p.pickRow.Value {
					p.pickCursor = i
					break
				}
			}
		} else {
			p.pickCursor = clamp(p.pickCursor, 0, max(0, len(p.pickFiltered)-1))
		}
		return
	}

	toks := fuzzy.ParseQuery(q)
	type scored struct {
		opt   string
		score int
		idx   int
	}
	var matches []scored
	for i, opt := range opts {
		totalScore := 0
		ok := true
		for _, tok := range toks {
			res := fuzzy.MatchToken(opt, tok, fuzzy.SchemeDefault, false)
			if !res.Matched {
				ok = false
				break
			}
			totalScore += res.Score
		}
		if ok {
			matches = append(matches, scored{opt: opt, score: totalScore, idx: i})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].idx < matches[j].idx
	})
	p.pickFiltered = make([]string, len(matches))
	for i, m := range matches {
		p.pickFiltered[i] = m.opt
	}
	p.pickCursor = 0
}

func (p settingsPanel) updatePickModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	hasFilter := len(p.pickRow.Options) > 5
	switch key {
	case "esc":
		p.pickInput.Blur()
		p.pickOpen = false
		return p, nil
	case "q":
		if !hasFilter {
			p.pickInput.Blur()
			p.pickOpen = false
			return p, nil
		}
	case "up", "ctrl+p":
		if len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor-1, 0, len(p.pickFiltered)-1)
		}
		return p, nil
	case "down", "ctrl+n":
		if len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor+1, 0, len(p.pickFiltered)-1)
		}
		return p, nil
	case "pgup":
		if len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor-6, 0, len(p.pickFiltered)-1)
		}
		return p, nil
	case "pgdown":
		if len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor+6, 0, len(p.pickFiltered)-1)
		}
		return p, nil
	case "home":
		p.pickCursor = 0
		return p, nil
	case "end":
		if len(p.pickFiltered) > 0 {
			p.pickCursor = len(p.pickFiltered) - 1
		}
		return p, nil
	case "k":
		if !hasFilter && len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor-1, 0, len(p.pickFiltered)-1)
			return p, nil
		}
	case "j":
		if !hasFilter && len(p.pickFiltered) > 0 {
			p.pickCursor = clamp(p.pickCursor+1, 0, len(p.pickFiltered)-1)
			return p, nil
		}
	case "ctrl+e", "f2":
		p.pickInput.Blur()
		p.pickOpen = false
		return p.activateSelectedSetting(backend, true)
	case "c":
		if !hasFilter || strings.TrimSpace(p.pickInput.Value()) == "" {
			p.pickInput.Blur()
			p.pickOpen = false
			return p.activateSelectedSetting(backend, true)
		}
	case "enter":
		if len(p.pickFiltered) == 0 {
			return p, nil
		}
		idx := clamp(p.pickCursor, 0, len(p.pickFiltered)-1)
		val := p.pickFiltered[idx]
		keyName := p.pickRow.Key
		p.pickInput.Blur()
		p.pickOpen = false
		p.saving = true
		p.saveActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.saveActID, "Updating "+keyName),
			setSettingCmd(backend, keyName, val),
		)
	}
	if hasFilter {
		var cmd tea.Cmd
		p.pickInput, cmd = p.pickInput.Update(msg)
		p.refilterPickOptions(strings.TrimSpace(p.pickInput.Value()) == "")
		return p, cmd
	}
	return p, nil
}

func (p settingsPanel) resetSelectedSetting(backend Backend) (ResourcePanel, tea.Cmd) {
	r, ok := p.selectedSetting()
	if !ok {
		return p, nil
	}
	if r.ReadOnly {
		return p, func() tea.Msg {
			return statusToastMsg{text: "Read-only runtime path cannot be reset"}
		}
	}
	if !r.Overridden {
		return p, func() tea.Msg {
			return statusToastMsg{text: r.Key + " has no active override to clear"}
		}
	}
	p.saving = true
	p.saveActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.saveActID, "Resetting "+r.Key),
		resetSettingCmd(backend, r.Key),
	)
}

func (p settingsPanel) updateEditModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		p.editInput.Blur()
		p.editOpen = false
		p.editErr = ""
		return p, nil
	case "enter":
		val := strings.TrimSpace(p.editInput.Value())
		key := p.editRow.Key
		p.editInput.Blur()
		p.editOpen = false
		p.saving = true
		p.saveActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.saveActID, "Saving "+key),
			setSettingCmd(backend, key, val),
		)
	}
	var cmd tea.Cmd
	p.editInput, cmd = p.editInput.Update(msg)
	return p, cmd
}

func (p settingsPanel) editSelectedWatchedFile(backend Backend) (ResourcePanel, tea.Cmd) {
	wf, ok := p.selectedWatched()
	if !ok {
		return p, nil
	}
	return p.editConfigFile(backend, wf.Path)
}

func (p settingsPanel) editConfigFile(backend Backend, path string) (ResourcePanel, tea.Cmd) {
	if backend == nil {
		return p, func() tea.Msg { return statusToastMsg{text: "Backend unavailable", isErr: true} }
	}
	cmd, err := backend.EditConfigFileCmd(path)
	if err != nil {
		return p, func() tea.Msg { return statusToastMsg{text: "Edit failed: " + err.Error(), isErr: true} }
	}
	return p, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return configEditFinishedMsg{path: path, err: err}
	})
}

func (p settingsPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.drillOpen {
		return p.ViewPreview(width, height, focused, false, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	reloadTxt := st.BadgeWarn.Render("AUTO-RELOAD OFF")
	if p.view.AutoReload {
		reloadTxt = st.BadgeOK.Render(fmt.Sprintf("AUTO-RELOAD %ds", max(1, p.view.AutoReloadSecs)))
	}
	cfgLine := reloadTxt + "  " + st.Muted.Render(truncate("File: "+nonEmpty(p.view.ConfigPath, "~/.ws/config.toml"), max(10, innerW-22)))
	lines := []string{cfgLine}
	if p.loadErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("Error: "+p.loadErr, innerW)))
	}
	if p.view.ConfigErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("config.toml: "+p.view.ConfigErr, innerW)))
	}
	if p.view.MiseLocalErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("mise.local.toml: "+p.view.MiseLocalErr, innerW)))
	}
	lines = append(lines, "")

	if p.subView == settingsSubFiles {
		title := fmt.Sprintf("Watched Config Files (%d)", len(p.filteredWatched))
		if !p.loaded {
			lines = append(lines, st.Muted.Render("Loading watched config files..."))
			return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
		}
		if len(p.filteredWatched) == 0 {
			lines = append(lines, st.Muted.Render("No watched files match filter."))
			return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
		}
		rows := make([][]string, 0, len(p.filteredWatched))
		for _, wf := range p.filteredWatched {
			stStr := st.Muted.Render("missing")
			modStr := "—"
			sizeStr := "—"
			if wf.Exists {
				stStr = st.OKText.Render("watched")
				sizeStr = formatBytes(wf.SizeBytes)
				if wf.ModTime > 0 {
					modStr = time.Unix(wf.ModTime, 0).Local().Format("01-02 15:04:05")
				}
			}
			rows = append(rows, []string{wf.Label, stStr, sizeStr, modStr, wf.Path})
		}
		tableH := max(4, innerH-len(lines))
		lines = append(lines, renderDataTable([]string{"Config", "Status", "Size", "Modified", "Path"}, rows, p.watchedCursor, focused, innerW, tableH, st)...)
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	}

	title := fmt.Sprintf("Workstation & TUI Settings (%d)", len(p.filtered))
	if !p.loaded {
		lines = append(lines, st.Muted.Render("Loading configuration & overrides..."))
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	}
	if len(p.filtered) == 0 {
		lines = append(lines, st.Muted.Render("No settings match the active search or filter."))
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filtered))
	for _, r := range p.filtered {
		val := nonEmpty(r.Value, "—")
		src := renderSettingSource(r.Source, r.Overridden, st)
		rows = append(rows, []string{
			r.Key,
			r.Category,
			val,
			src,
		})
	}
	tableH := max(4, innerH-len(lines))
	lines = append(lines, renderDataTable([]string{"Setting Key", "Cat", "Effective Value", "Source"}, rows, p.cursor, focused, innerW, tableH, st)...)
	return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
}

func renderSettingSource(source string, overridden bool, st Styles) string {
	switch {
	case overridden || source == "override":
		return st.AccentText.Render("✦ override")
	case source == "flag":
		return st.InfoText.Render("flag")
	case source == "env":
		return st.InfoText.Render("env")
	case source == "mise.local":
		return st.OKText.Render("mise.local")
	case source == "ledger":
		return st.OKText.Render("ledger")
	case source == "readonly":
		return st.Muted.Render("readonly")
	default:
		return st.Muted.Render(nonEmpty(source, "default"))
	}
}

func (p settingsPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	var all []string
	if p.subView == settingsSubFiles {
		if wf, ok := p.selectedWatched(); ok {
			state := st.Muted.Render("MISSING")
			if wf.Exists {
				state = st.OKText.Render("WATCHED ON DISK")
			}
			all = append(all, state+" "+st.AccentText.Render(wf.Label), "")
			modStr := "—"
			if wf.ModTime > 0 {
				modStr = time.Unix(wf.ModTime, 0).Local().Format("2006-01-02 15:04:05")
			}
			all = append(all, renderKVTable([][2]string{
				{"Path", wf.Path},
				{"Size", formatBytes(wf.SizeBytes)},
				{"Modified", modStr},
				{"Fingerprint", nonEmpty(wf.Fingerprint, "missing")},
			}, innerW, st)...)
			all = append(all, "")
		}
	} else if r, ok := p.selectedSetting(); ok {
		modeBadge := st.InfoText.Render(strings.ToUpper(r.Kind))
		if r.Overridden {
			modeBadge = st.BadgeInfo.Render("OVERRIDDEN")
		}
		all = append(all, modeBadge+" "+st.AccentText.Render(truncate(r.Key, max(6, innerW-16))), "")
		all = append(all, renderKVTable([][2]string{
			{"Category", r.Category},
			{"Effective", nonEmpty(r.Value, "(unset)")},
			{"Source", r.Source},
			{"Default", nonEmpty(r.DefaultValue, "(none)")},
			{"CLI Override", nonEmpty(p.CLIEquivalent(), "—")},
		}, innerW, st)...)
		if len(r.Options) > 0 {
			all = append(all, st.InfoText.Render(truncate("Options: "+strings.Join(r.Options, " · "), innerW)))
		}
		if r.Description != "" {
			all = append(all, "")
			all = append(all, wrapPlainTextLines(r.Description, innerW, st.RowNormal)...)
		}
		all = append(all, "")
	}

	reloadState := "OFF"
	if p.view.AutoReload {
		reloadState = fmt.Sprintf("ON (every %ds)", max(1, p.view.AutoReloadSecs))
	}
	lastReloadStr := "none this session"
	if !p.lastReloadAt.IsZero() {
		lastReloadStr = fmt.Sprintf("%s (%s, #%d)", p.lastReloadAt.Local().Format("15:04:05"), nonEmpty(p.lastReloadReason, "watch"), p.reloadCount)
	}
	all = append(all, st.SectionHeader.Render("Live Config Auto-Reload"))
	all = append(all, renderKVTable([][2]string{
		{"Auto-Reload", reloadState},
		{"Last Reload", lastReloadStr},
		{"Fingerprint", nonEmpty(p.view.Fingerprint, "—")},
		{"Precedence", "override (~/.ws/config.toml) > env > mise.local > default"},
	}, innerW, st)...)

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Setting Inspector & Auto-Reload", "", vis, width, height, focused, st)
}

func (p settingsPanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.filters.Open && p.subView == settingsSubAll {
		groups, sorts := p.settingsFacetGroups()
		return p.filters.ViewModal("Settings", groups, sorts, width, height, st)
	}
	if p.pickOpen {
		boxW := min(max(52, width-10), 80)
		if boxW > width-2 && width > 4 {
			boxW = width - 2
		}
		innerW := max(16, boxW-st.ModalBox.GetHorizontalFrameSize())
		r := p.pickRow
		parts := []string{
			st.ModalTitle.Render(truncate("Select "+r.Key, innerW)),
			"",
		}
		if r.Description != "" {
			parts = append(parts, wrapPlainTextLines(r.Description, innerW, st.RowNormal)...)
		}
		parts = append(parts,
			st.Muted.Render(truncate(fmt.Sprintf("Current: %s · Default: %s · Category: %s", nonEmpty(r.Value, "(unset)"), nonEmpty(r.DefaultValue, "(none)"), r.Category), innerW)),
			"",
		)
		hasFilter := len(r.Options) > 5
		if hasFilter {
			p.pickInput.SetWidth(max(12, innerW-10))
			parts = append(parts, renderInputField("Filter: ", p.pickInput, innerW, true, st), "")
		}
		pickRows := make([][]string, 0, len(p.pickFiltered))
		for _, opt := range p.pickFiltered {
			var tags []string
			if opt == r.Value {
				tags = append(tags, "active")
			}
			if opt == r.DefaultValue && r.DefaultValue != "" {
				tags = append(tags, "default")
			}
			stateCol := strings.Join(tags, " · ")
			pickRows = append(pickRows, []string{opt, stateCol})
		}
		if len(pickRows) == 0 {
			parts = append(parts, st.WarnText.Render("No options match filter. Press ctrl+e to type a custom value."))
		} else {
			listH := min(max(4, len(pickRows)+1), max(5, height-16))
			parts = append(parts, renderDataTable([]string{"Option", "State"}, pickRows, p.pickCursor, true, innerW, listH, st)...)
		}
		parts = append(parts, "", st.Muted.Render(truncate("enter apply · ↑/↓ select · ctrl+e custom text · esc cancel", innerW)))
		modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal), true
	}
	if !p.editOpen {
		return "", false
	}

	boxW := min(max(50, width-10), 80)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(16, boxW-st.ModalBox.GetHorizontalFrameSize())
	r := p.editRow
	p.editInput.SetWidth(max(12, innerW-10))

	parts := []string{
		st.ModalTitle.Render(truncate("Override Setting: "+r.Key, innerW)),
		"",
	}
	if r.Description != "" {
		parts = append(parts, wrapPlainTextLines(r.Description, innerW, st.RowNormal)...)
	}
	parts = append(parts,
		st.Muted.Render(truncate(fmt.Sprintf("Category: %s · Source: %s · Default: %s", r.Category, r.Source, nonEmpty(r.DefaultValue, "(none)")), innerW)),
		"",
		renderInputField("Value: ", p.editInput, innerW, true, st),
		"",
		st.Muted.Render(truncate("Saves to ~/.ws/config.toml (0600) and live-applies across the TUI.", innerW)),
		st.Muted.Render(truncate("enter save override · esc cancel", innerW)),
	)
	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal), true
}

func loadSettingsCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return settingsLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		view, err := backend.LoadSettings()
		return settingsLoadedMsg{view: view, err: err}
	}
}

func setSettingCmd(backend Backend, key, val string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return settingSavedMsg{key: key, val: val, err: fmt.Errorf("backend unavailable")}
		}
		view, err := backend.SetSetting(key, val)
		return settingSavedMsg{key: key, val: val, view: view, err: err}
	}
}

func resetSettingCmd(backend Backend, key string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return settingSavedMsg{key: key, val: "", err: fmt.Errorf("backend unavailable")}
		}
		view, err := backend.ResetSetting(key)
		return settingSavedMsg{key: key, val: "", view: view, err: err}
	}
}

func scheduleConfigWatchTickCmd(interval time.Duration) tea.Cmd {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return configWatchTickMsg{}
	})
}

func checkConfigWatchCmd(backend Backend, prevFingerprint string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return nil
		}
		res, err := backend.CheckConfigWatch(prevFingerprint)
		return configWatchCheckedMsg{res: res, err: err}
	}
}

func reloadConfigCmd(backend Backend, reason string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return configReloadedMsg{reason: reason, err: fmt.Errorf("backend unavailable")}
		}
		view, changed, err := backend.ReloadConfig()
		return configReloadedMsg{
			reason:       reason,
			changedFiles: changed,
			view:         view,
			err:          err,
		}
	}
}
