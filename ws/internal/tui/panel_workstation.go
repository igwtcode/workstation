package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

const (
	wsSubDoctor = iota
	wsSubThemes
	wsSubPresets
	wsSubSettings
	wsSubWatchedFiles
	wsSubHistory
	wsSubCount
)

type presetActivatedMsg struct {
	preset         string
	jumpToPackages bool
}

type workstationPanel struct {
	subTab   int
	doctor   doctorPanel
	settings settingsPanel

	themeIDs        []string
	filteredThemes  []string
	themeQuery      string
	palettes        map[string]config.Palette
	activeTheme     string
	previewTheme    string
	templates       []TemplateStatusRow
	themesLoaded    bool
	templatesLoaded bool
	themesErr       string
	tplErr          string
	themeIdx        int
	tplIdx          int
	themeFocusTpl   bool
	themeApplying   bool
	themesActID     int
	tplActID        int
	themeApplyActID int

	presets         []PresetSummary
	filteredPresets []PresetSummary
	presetPkgCache  map[string][]PresetToolRow
	presetQuery     string
	presetCursor    int
	activePresetID  string
	presetsLoaded   bool
	presetsErr      string
	presetsActID    int

	histRows      []HistoryRow
	filteredHist  []HistoryRow
	histSource    string
	histQuery     string
	histSel       SelectionSet
	histCursor    int
	histLoaded    bool
	histErr       string
	histActID     int
	histDrillOpen bool

	previewScroll int
}

func newWorkstationPanel() workstationPanel {
	return workstationPanel{
		doctor:         newDoctorPanel(),
		settings:       newSettingsPanel(),
		palettes:       map[string]config.Palette{},
		presetPkgCache: map[string][]PresetToolRow{},
	}
}

func (p workstationPanel) isSettingsSubView() bool {
	return p.subTab == wsSubSettings || p.subTab == wsSubWatchedFiles
}

func (p workstationPanel) ID() string         { return "workstation" }
func (p workstationPanel) Title() string      { return "Workstation" }
func (p workstationPanel) ShortTitle() string { return "Workstation" }
func (p workstationPanel) SubViews() []string {
	return []string{"Doctor", "Themes", "Presets", "Settings", "Watched Files", "History"}
}
func (p workstationPanel) ActiveSubView() int { return p.subTab }

func (p workstationPanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < wsSubCount {
		p.subTab = idx
		p.histDrillOpen = false
		p.previewScroll = 0
		p.doctor = p.doctor.Pop().(doctorPanel)
		if idx == wsSubSettings {
			p.settings = p.settings.SetSubView(settingsSubAll).(settingsPanel)
		} else if idx == wsSubWatchedFiles {
			p.settings = p.settings.SetSubView(settingsSubFiles).(settingsPanel)
		}
	}
	return p
}

func (p workstationPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case wsSubDoctor:
		nextDoc, cmd := p.doctor.OnSubViewChanged(backend)
		p.doctor = nextDoc.(doctorPanel)
		return p, cmd
	case wsSubSettings, wsSubWatchedFiles:
		nextSet, cmd := p.settings.OnSubViewChanged(backend)
		p.settings = nextSet.(settingsPanel)
		return p, cmd
	case wsSubPresets:
		if pr, ok := p.selectedPresetSummary(); ok && backend != nil {
			if _, cached := p.presetPkgCache[pr.ID]; !cached {
				return p, resolvePresetCmd(backend, pr.ID)
			}
		}
	case wsSubHistory:
		if !p.histLoaded && backend != nil {
			p.histActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.histActID, "Loading shell history"),
				loadHistoryCmd(backend, "", 2000),
			)
		}
	}
	return p, nil
}

func (p workstationPanel) SetScope(string, Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p workstationPanel) IsZoned() bool                                     { return false }

func (p workstationPanel) SupportsPreview() bool {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.SupportsPreview()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.SupportsPreview()
	case wsSubHistory:
		return !p.histDrillOpen
	default:
		return true
	}
}

func (p workstationPanel) DefaultSplit(width int) SplitMode {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.DefaultSplit(width)
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.DefaultSplit(width)
	case wsSubPresets:
		if width >= 96 {
			return SplitVertical
		}
		return SplitOff
	default:
		return SplitOff
	}
}

func (p workstationPanel) CanPop() bool {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.CanPop()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.CanPop()
	case wsSubHistory:
		return p.histDrillOpen
	default:
		return false
	}
}

func (p workstationPanel) Pop() ResourcePanel {
	switch p.subTab {
	case wsSubDoctor:
		p.doctor = p.doctor.Pop().(doctorPanel)
	case wsSubSettings, wsSubWatchedFiles:
		p.settings = p.settings.Pop().(settingsPanel)
	case wsSubHistory:
		p.histDrillOpen = false
		p.previewScroll = 0
	}
	return p
}

func (p workstationPanel) CapturingInput() bool {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.CapturingInput()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.CapturingInput()
	default:
		return false
	}
}

func (p workstationPanel) HasOpenModal() bool {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.HasOpenModal()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.HasOpenModal()
	default:
		return false
	}
}

func (p workstationPanel) MutationInFlight() bool {
	return p.themeApplying || p.doctor.MutationInFlight() || p.settings.MutationInFlight()
}

func (p workstationPanel) FilterTarget() string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.FilterTarget()
	case wsSubThemes:
		return "Themes"
	case wsSubPresets:
		return "Presets"
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.FilterTarget()
	case wsSubHistory:
		return "History"
	default:
		return "Workstation"
	}
}

func (p workstationPanel) FilterQuery() string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.FilterQuery()
	case wsSubThemes:
		return p.themeQuery
	case wsSubPresets:
		return p.presetQuery
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.FilterQuery()
	case wsSubHistory:
		return p.histQuery
	default:
		return ""
	}
}

func (p workstationPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case wsSubDoctor:
		nextDoc, cmd := p.doctor.SetFilterQuery(q, backend)
		p.doctor = nextDoc.(doctorPanel)
		return p, cmd
	case wsSubThemes:
		p.themeQuery = q
		p.recomputeFilteredThemes()
		p.themeIdx = 0
	case wsSubPresets:
		p.presetQuery = q
		p.recomputeFilteredPresets()
		p.presetCursor = 0
	case wsSubSettings, wsSubWatchedFiles:
		nextSet, cmd := p.settings.SetFilterQuery(q, backend)
		p.settings = nextSet.(settingsPanel)
		return p, cmd
	case wsSubHistory:
		p.histQuery = q
		p.recomputeFilteredHistory()
		p.histCursor = 0
	}
	return p, nil
}

func (p workstationPanel) OnBlur() ResourcePanel {
	p.doctor = p.doctor.OnBlur().(doctorPanel)
	p.settings = p.settings.OnBlur().(settingsPanel)
	return p
}

func (p workstationPanel) SelectedCount() int {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.SelectedCount()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.SelectedCount()
	case wsSubHistory:
		return p.histSel.Count()
	default:
		return 0
	}
}

func (p workstationPanel) ClearSelection() ResourcePanel {
	switch p.subTab {
	case wsSubDoctor:
		p.doctor = p.doctor.ClearSelection().(doctorPanel)
	case wsSubSettings, wsSubWatchedFiles:
		p.settings = p.settings.ClearSelection().(settingsPanel)
	case wsSubHistory:
		p.histSel = p.histSel.Clear()
	}
	return p
}

func (p workstationPanel) Attention() AttentionLevel {
	if p.themesErr != "" || p.tplErr != "" || p.presetsErr != "" || p.histErr != "" {
		return AttentionErr
	}
	docAttn := p.doctor.Attention()
	setAttn := p.settings.Attention()
	if docAttn >= setAttn {
		return docAttn
	}
	return setAttn
}

func (p workstationPanel) PaneStatus() string {
	switch {
	case p.themeApplying:
		return "applying theme..."
	case p.subTab == wsSubDoctor:
		return p.doctor.PaneStatus()
	case p.isSettingsSubView():
		return p.settings.PaneStatus()
	}
	if st := p.doctor.PaneStatus(); st != "" {
		return st
	}
	return p.settings.PaneStatus()
}

func (p workstationPanel) Badge() string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.Badge()
	case wsSubThemes:
		return nonEmpty(p.activeTheme, fmt.Sprintf("%d themes", len(p.themeIDs)))
	case wsSubPresets:
		return nonEmpty(p.activePresetID, fmt.Sprintf("%d presets", len(p.filteredPresets)))
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.Badge()
	case wsSubHistory:
		return fmt.Sprintf("%d cmds", len(p.filteredHist))
	default:
		return p.doctor.Badge()
	}
}

func (p workstationPanel) Breadcrumbs() []string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.Breadcrumbs()
	case wsSubThemes:
		return []string{"Workstation", fmt.Sprintf("Themes (%d)", len(p.filteredThemes))}
	case wsSubPresets:
		return []string{"Workstation", fmt.Sprintf("Presets (%d)", len(p.filteredPresets))}
	case wsSubSettings, wsSubWatchedFiles:
		bc := p.settings.Breadcrumbs()
		if len(bc) > 0 {
			return append([]string{"Workstation"}, bc[1:]...)
		}
		return []string{"Workstation", "Settings"}
	case wsSubHistory:
		if p.histDrillOpen {
			return []string{"Workstation", "History", "Command"}
		}
		return []string{"Workstation", fmt.Sprintf("History (%d)", len(p.filteredHist))}
	default:
		return []string{"Workstation"}
	}
}

func (p workstationPanel) StatusChips() []string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.StatusChips()
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.StatusChips()
	}
	var chips []string
	if q := p.FilterQuery(); q != "" {
		chips = append(chips, "/"+truncate(q, 18))
	}
	switch p.subTab {
	case wsSubThemes:
		if p.previewTheme != "" && p.previewTheme != p.activeTheme {
			chips = append(chips, "preview:"+p.previewTheme)
		}
	case wsSubPresets:
		if p.activePresetID != "" {
			chips = append(chips, "active:"+p.activePresetID)
		}
	case wsSubHistory:
		if p.histSource != "" {
			chips = append(chips, "src:"+p.histSource)
		}
	}
	return chips
}

func (p workstationPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.KeyHints(split, focusPreview)
	case wsSubThemes:
		return [][2]string{
			{"enter", "Apply theme"},
			{"e", "Edit template"},
			{"y", "Copy ID / path"},
			{"r", "Refresh"},
			{"tab", "Theme / Tpl"},
			{"a", "Theme actions"},
		}
	case wsSubPresets:
		return [][2]string{
			{"enter", "Switch preset"},
			{"o", "Open in Packages"},
			{"y", "Copy CLI"},
			{"r", "Refresh"},
			{"a", "Preset actions"},
		}
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.KeyHints(split, focusPreview)
	case wsSubHistory:
		return [][2]string{
			{"enter", "Inspect cmd"},
			{"y/Y", "Copy marked / cmd"},
			{"r", "Reload"},
			{"a", "History actions"},
			{"space/*", "Mark cmds"},
		}
	default:
		return p.doctor.KeyHints(split, focusPreview)
	}
}

func (p workstationPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.RowActions(split, focusPreview)
	case wsSubThemes:
		return []RowAction{
			{Key: "t", Label: "Preview theme", Description: "Live-preview selected palette in TUI"},
			{Key: "1", Label: "Apply single app", Description: "Render and apply selected palette to focused template only"},
			{Key: "A", Label: "Apply all apps", Description: "Render and apply selected palette across all themed apps"},
		}
	case wsSubPresets:
		return []RowAction{
			{Key: "s", Label: "Switch preset", Description: "Use selected preset in Packages view"},
			{Key: "o", Label: "Open in Packages", Description: "Switch preset and jump to Packages → Installed"},
			{Key: "y", Label: "Copy preset CLI", Description: "Copy ws preset show command"},
			{Key: "r", Label: "Refresh presets", Description: "Reload presets.toml"},
		}
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.RowActions(split, focusPreview)
	case wsSubHistory:
		return []RowAction{
			{Key: "y", Label: "Copy marked / cmd", Description: "Copy marked or highlighted history command(s)"},
			{Key: "r", Label: "Reload history", Description: "Reload shell history entries"},
		}
	default:
		return nil
	}
}

func (p workstationPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case wsSubDoctor:
		nextDoc, cmd := p.doctor.RunRowAction(actionKey, backend, split, focusPreview)
		p.doctor = nextDoc.(doctorPanel)
		return p, cmd
	case wsSubThemes:
		switch actionKey {
		case "t":
			id := p.selectedThemeID()
			return p, func() tea.Msg { return requestThemePreviewMsg{id: id} }
		case "1":
			if p.themeApplying {
				return p, nil
			}
			tpl, ok := p.selectedTemplate()
			if !ok || tpl.App == "" {
				return p, func() tea.Msg { return statusToastMsg{text: "No template selected to apply", isErr: true} }
			}
			return p.requestThemeApplyConfirm(tpl.App)
		case "A":
			if p.themeApplying {
				return p, nil
			}
			return p.requestThemeApplyConfirm("")
		}
	case wsSubPresets:
		switch actionKey {
		case "s":
			if pr, ok := p.selectedPresetSummary(); ok {
				p.activePresetID = pr.ID
				return p, func() tea.Msg { return presetActivatedMsg{preset: pr.ID, jumpToPackages: false} }
			}
		case "o":
			if pr, ok := p.selectedPresetSummary(); ok {
				p.activePresetID = pr.ID
				return p, func() tea.Msg { return presetActivatedMsg{preset: pr.ID, jumpToPackages: true} }
			}
		case "y":
			if pr, ok := p.selectedPresetSummary(); ok {
				return p, copyCmd("ws preset show "+pr.ID, "preset CLI")
			}
		case "r":
			p.presetsActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.presetsActID, "Loading presets"),
				loadPresetsCmd(backend),
			)
		}
	case wsSubSettings, wsSubWatchedFiles:
		nextSet, cmd := p.settings.RunRowAction(actionKey, backend, split, focusPreview)
		p.settings = nextSet.(settingsPanel)
		return p, cmd
	case wsSubHistory:
		switch actionKey {
		case "y":
			return p.copySelectedHistory()
		case "r":
			p.histActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.histActID, "Reloading shell history"),
				loadHistoryCmd(backend, "", 2000),
			)
		}
	}
	return p, nil
}

func (p workstationPanel) CLIEquivalent() string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.CLIEquivalent()
	case wsSubThemes:
		if id := p.selectedThemeID(); id != "" {
			return fmt.Sprintf("ws theme set %s", id)
		}
		return "ws theme list"
	case wsSubPresets:
		if pr, ok := p.selectedPresetSummary(); ok {
			return fmt.Sprintf("ws preset show %s", pr.ID)
		}
		return "ws preset list"
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.CLIEquivalent()
	case wsSubHistory:
		if strings.TrimSpace(p.histQuery) != "" {
			return fmt.Sprintf("ws hist %q", p.histQuery)
		}
		return "ws hist"
	default:
		return "ws doctor"
	}
}

func (p workstationPanel) selectedThemeID() string {
	if len(p.filteredThemes) == 0 || p.themeIdx < 0 || p.themeIdx >= len(p.filteredThemes) {
		return ""
	}
	return p.filteredThemes[p.themeIdx]
}

func (p workstationPanel) selectedTemplate() (TemplateStatusRow, bool) {
	if len(p.templates) == 0 || p.tplIdx < 0 || p.tplIdx >= len(p.templates) {
		return TemplateStatusRow{}, false
	}
	return p.templates[p.tplIdx], true
}

func (p workstationPanel) selectedPresetSummary() (PresetSummary, bool) {
	if len(p.filteredPresets) == 0 || p.presetCursor < 0 || p.presetCursor >= len(p.filteredPresets) {
		return PresetSummary{}, false
	}
	return p.filteredPresets[p.presetCursor], true
}

func (p workstationPanel) selectedHistoryRow() (HistoryRow, bool) {
	if len(p.filteredHist) == 0 || p.histCursor < 0 || p.histCursor >= len(p.filteredHist) {
		return HistoryRow{}, false
	}
	return p.filteredHist[p.histCursor], true
}

func (p *workstationPanel) recomputeFilteredThemes() {
	p.filteredThemes = fuzzy.Filter(p.themeIDs, p.themeQuery, func(id string) ([]string, map[string]string) {
		active := "false"
		if id == p.activeTheme {
			active = "true"
		}
		return []string{id}, map[string]string{"active": active}
	})
	if p.themeIdx >= len(p.filteredThemes) {
		p.themeIdx = max(0, len(p.filteredThemes)-1)
	}
}

func (p *workstationPanel) recomputeFilteredPresets() {
	p.filteredPresets = fuzzy.Filter(p.presets, p.presetQuery, func(pr PresetSummary) ([]string, map[string]string) {
		active := "false"
		if pr.ID == p.activePresetID || (p.activePresetID == "" && pr.Active) {
			active = "true"
		}
		match := "false"
		if pr.MatchesHost {
			match = "true"
		}
		return []string{pr.ID, pr.Description, strings.Join(pr.Include, " "), strings.Join(pr.Tags, " ")},
			map[string]string{"active": active, "match": match}
	})
	if p.presetCursor >= len(p.filteredPresets) {
		p.presetCursor = max(0, len(p.filteredPresets)-1)
	}
}

func (p *workstationPanel) recomputeFilteredHistory() {
	p.filteredHist = fuzzy.Filter(p.histRows, p.histQuery, func(h HistoryRow) ([]string, map[string]string) {
		return []string{h.Command, h.Shell, h.Zone},
			map[string]string{
				"shell": strings.ToLower(h.Shell),
				"zone":  strings.ToLower(nonEmpty(h.Zone, "global")),
				"exit":  fmt.Sprintf("%d", h.ExitCode),
			}
	})
	if p.histCursor >= len(p.filteredHist) {
		p.histCursor = max(0, len(p.filteredHist)-1)
	}
}

func histRowKey(h HistoryRow) string {
	return fmt.Sprintf("%d|%s|%s", h.Timestamp.UnixNano(), h.Shell, h.Command)
}

func (p workstationPanel) copySelectedHistory() (ResourcePanel, tea.Cmd) {
	if p.histSel.Count() > 0 {
		var picked []string
		for _, h := range p.filteredHist {
			if p.histSel.Has(histRowKey(h)) && strings.TrimSpace(h.Command) != "" {
				picked = append(picked, h.Command)
			}
		}
		if len(picked) > 0 {
			return p, copyCmd(strings.Join(picked, "\n"), fmt.Sprintf("%d history command(s)", len(picked)))
		}
	}
	if h, ok := p.selectedHistoryRow(); ok && h.Command != "" {
		return p, copyCmd(h.Command, "history command")
	}
	return p, nil
}

func (p workstationPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case doctorMutationStartMsg, triggerDoctorApplyMsg, doctorLoadedMsg, dbStatusLoadedMsg, planAppliedMsg, dbBackedUpMsg, undoDoneMsg:
		nextDoc, cmd := p.doctor.Update(msg, backend, split, focusPreview)
		p.doctor = nextDoc.(doctorPanel)
		return p, cmd

	case settingsLoadedMsg, settingSavedMsg, configReloadedMsg, configEditFinishedMsg:
		nextSet, cmd := p.settings.Update(msg, backend, split, focusPreview)
		p.settings = nextSet.(settingsPanel)
		return p, cmd

	case systemMutationStartMsg:
		if msg.kind == "theme" {
			p.themeApplying = true
		}
		return p, nil

	case presetsLoadedMsg:
		p.presetsLoaded = true
		var cmds []tea.Cmd
		if p.presetsActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.presetsActID, "Loading presets", msg.err != nil))
			p.presetsActID = 0
		}
		if msg.err != nil {
			p.presetsErr = msg.err.Error()
			return p, batchNonNil(cmds...)
		}
		p.presetsErr = ""
		p.presets = msg.presets
		if p.activePresetID == "" {
			for _, pr := range p.presets {
				if pr.Active {
					p.activePresetID = pr.ID
					break
				}
			}
		}
		p.recomputeFilteredPresets()
		for i, pr := range p.filteredPresets {
			if pr.ID == p.activePresetID {
				p.presetCursor = i
				break
			}
		}
		return p, batchNonNil(cmds...)

	case presetResolvedMsg:
		if msg.err == nil && msg.id != "" {
			if p.presetPkgCache == nil {
				p.presetPkgCache = map[string][]PresetToolRow{}
			}
			p.presetPkgCache[msg.id] = msg.rows
		}
		return p, nil

	case systemSwitchPresetMsg:
		for i, pr := range p.filteredPresets {
			if strings.EqualFold(pr.ID, msg.preset) {
				p.activePresetID = pr.ID
				p.presetCursor = i
				return p, nil
			}
		}
		for _, pr := range p.presets {
			if strings.EqualFold(pr.ID, msg.preset) {
				p.activePresetID = pr.ID
				return p, nil
			}
		}
		return p, nil

	case themesLoadedMsg:
		p.themesLoaded = true
		var cmds []tea.Cmd
		if p.themesActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.themesActID, "Loading themes", msg.err != nil))
			p.themesActID = 0
		}
		if msg.err != nil {
			p.themesErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Themes load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.themesErr = ""
		p.themeIDs = msg.ids
		p.palettes = map[string]config.Palette{}
		for k, v := range msg.palettes {
			p.palettes[k] = v
		}
		if msg.active != "" {
			p.activeTheme = msg.active
			p.previewTheme = ""
		}
		p.recomputeFilteredThemes()
		for i, id := range p.filteredThemes {
			if id == p.activeTheme {
				p.themeIdx = i
				break
			}
		}
		return p, batchNonNil(cmds...)

	case previewThemeSyncMsg:
		p.previewTheme = msg.id
		return p, nil

	case themeChangedMsg:
		p.themeApplying = false
		var actCmd tea.Cmd
		if p.themeApplyActID != 0 {
			lbl := "Applying theme " + msg.id
			if msg.app != "" {
				lbl += " (" + msg.app + ")"
			}
			actCmd = finishActivityCmd(p.themeApplyActID, lbl, msg.err != nil)
			p.themeApplyActID = 0
		}
		if msg.err == nil && msg.id != "" {
			if msg.app == "" {
				p.activeTheme = msg.id
				p.previewTheme = ""
			}
			if p.palettes == nil {
				p.palettes = map[string]config.Palette{}
			}
			if len(msg.palette.Tokens) > 0 {
				p.palettes[msg.id] = msg.palette
			}
		}
		return p, actCmd

	case templatesLoadedMsg:
		p.templatesLoaded = true
		var cmds []tea.Cmd
		if p.tplActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.tplActID, "Checking templates", msg.err != nil))
			p.tplActID = 0
		}
		if msg.err != nil {
			p.tplErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Templates check failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.tplErr = ""
		p.templates = msg.rows
		if p.tplIdx >= len(p.templates) {
			p.tplIdx = max(0, len(p.templates)-1)
		}
		return p, batchNonNil(cmds...)

	case editFinishedMsg:
		p.tplActID = newActivityID()
		var finishErr error
		if msg.err == nil && backend != nil {
			_, finishErr = backend.FinishAppEdit(msg.app)
		}
		errOut := msg.err
		if errOut == nil {
			errOut = finishErr
		}
		return p, tea.Batch(
			func() tea.Msg {
				if errOut != nil {
					return statusToastMsg{text: fmt.Sprintf("Edit %s failed: %v", msg.app, errOut), isErr: true}
				}
				return statusToastMsg{text: fmt.Sprintf("Edited %s — templates refreshed", msg.app)}
			},
			startActivityCmd(p.tplActID, "Checking templates"),
			loadTemplatesCmd(backend),
		)

	case historyLoadedMsg:
		p.histLoaded = true
		var cmds []tea.Cmd
		if p.histActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.histActID, "Loading shell history", msg.err != nil))
			p.histActID = 0
		}
		if msg.err != nil {
			p.histErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "History load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.histErr = ""
		p.histSource = msg.source
		p.histRows = msg.rows
		p.recomputeFilteredHistory()
		return p, batchNonNil(cmds...)

	case tea.KeyPressMsg:
		switch p.subTab {
		case wsSubDoctor:
			nextDoc, cmd := p.doctor.Update(msg, backend, split, focusPreview)
			p.doctor = nextDoc.(doctorPanel)
			return p, cmd
		case wsSubThemes:
			return p.updateThemesKey(msg, backend, focusPreview)
		case wsSubPresets:
			return p.updatePresetsKey(msg, backend, split, focusPreview)
		case wsSubSettings, wsSubWatchedFiles:
			nextSet, cmd := p.settings.Update(msg, backend, split, focusPreview)
			p.settings = nextSet.(settingsPanel)
			return p, cmd
		case wsSubHistory:
			return p.updateHistoryKey(msg, backend, focusPreview)
		}
	}

	switch p.subTab {
	case wsSubDoctor:
		nextDoc, cmd := p.doctor.Update(msg, backend, split, focusPreview)
		p.doctor = nextDoc.(doctorPanel)
		return p, cmd
	case wsSubSettings, wsSubWatchedFiles:
		nextSet, cmd := p.settings.Update(msg, backend, split, focusPreview)
		p.settings = nextSet.(settingsPanel)
		return p, cmd
	}
	return p, nil
}

func (p workstationPanel) updateThemesKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if p.themeFocusTpl {
			p.tplIdx, _ = applyListMotion(p.tplIdx, len(p.templates), defaultMotionPage, m)
		} else {
			p.themeIdx, _ = applyListMotion(p.themeIdx, len(p.filteredThemes), defaultMotionPage, m)
		}
		return p, nil
	}
	switch msg.String() {
	case "tab", "shift+tab":
		p.themeFocusTpl = !p.themeFocusTpl
	case "enter":
		if p.themeApplying {
			return p, nil
		}
		if p.themeFocusTpl {
			if tpl, ok := p.selectedTemplate(); ok && tpl.App != "" {
				return p.requestThemeApplyConfirm(tpl.App)
			}
		}
		return p.requestThemeApplyConfirm("")
	case "e":
		return p, p.launchEditor(backend)
	case "y":
		if p.themeFocusTpl {
			if tpl, ok := p.selectedTemplate(); ok && tpl.Output != "" {
				return p, copyCmd(tpl.Output, "template output path")
			}
		} else if id := p.selectedThemeID(); id != "" {
			return p, copyCmd(id, "theme ID")
		}
	case "r":
		p.themesActID = newActivityID()
		p.tplActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.themesActID, "Loading themes"),
			startActivityCmd(p.tplActID, "Checking templates"),
			loadThemesCmd(backend),
			loadTemplatesCmd(backend),
		)
	}
	return p, nil
}

func (p workstationPanel) updatePresetsKey(msg tea.KeyPressMsg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.presetCursor, len(p.filteredPresets), defaultMotionPage, m); changed {
			p.presetCursor = next
			p.previewScroll = 0
			if pr, ok := p.selectedPresetSummary(); ok && backend != nil && split != SplitOff {
				if _, cached := p.presetPkgCache[pr.ID]; !cached {
					return p, resolvePresetCmd(backend, pr.ID)
				}
			}
		}
		return p, nil
	}
	switch msg.String() {
	case "enter":
		if pr, ok := p.selectedPresetSummary(); ok {
			p.activePresetID = pr.ID
			return p, func() tea.Msg { return presetActivatedMsg{preset: pr.ID, jumpToPackages: false} }
		}
	case "o":
		if pr, ok := p.selectedPresetSummary(); ok {
			p.activePresetID = pr.ID
			return p, func() tea.Msg { return presetActivatedMsg{preset: pr.ID, jumpToPackages: true} }
		}
	case "y":
		if pr, ok := p.selectedPresetSummary(); ok {
			return p, copyCmd("ws preset show "+pr.ID, "preset CLI")
		}
	case "r":
		p.presetsActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.presetsActID, "Loading presets"),
			loadPresetsCmd(backend),
		)
	}
	return p, nil
}

func (p workstationPanel) updateHistoryKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if isSpaceKey(msg) {
		if h, ok := p.selectedHistoryRow(); ok {
			p.histSel = p.histSel.Toggle(histRowKey(h))
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview || p.histDrillOpen {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.histCursor, len(p.filteredHist), defaultMotionPage, m); changed {
			p.histCursor = next
			p.previewScroll = 0
		}
		return p, nil
	}
	switch msg.String() {
	case "*":
		keys := make([]string, len(p.filteredHist))
		for i, h := range p.filteredHist {
			keys[i] = histRowKey(h)
		}
		p.histSel = p.histSel.ToggleAll(keys)
	case `\`:
		p.histSel = p.histSel.Clear()
	case "enter":
		if _, ok := p.selectedHistoryRow(); ok {
			p.histDrillOpen = !p.histDrillOpen
			p.previewScroll = 0
		}
	case "y":
		return p.copySelectedHistory()
	case "Y":
		if h, ok := p.selectedHistoryRow(); ok {
			return p, copyCmd(h.Command, "command")
		}
	case "r":
		p.histActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.histActID, "Reloading shell history"),
			loadHistoryCmd(backend, "", 2000),
		)
	}
	return p, nil
}

func (p workstationPanel) requestThemeApplyConfirm(app string) (ResourcePanel, tea.Cmd) {
	id := p.selectedThemeID()
	if id == "" {
		return p, nil
	}
	cached := p.palettes[id]
	if app != "" {
		targetOut := app
		if tpl, ok := p.selectedTemplate(); ok && tpl.App == app && tpl.Output != "" {
			targetOut = collapseHomePath(tpl.Output)
		}
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Apply Theme to " + app,
				Body:         fmt.Sprintf("Theme: %s\nTarget app: %s\nOutput: %s\nCommand: ws theme set %s --app %s", id, app, targetOut, id, app),
				ConfirmLabel: "Apply " + app,
				Dangerous:    false,
				StartPanel:   PanelWorkstation,
				StartMsg:     systemMutationStartMsg{kind: "theme"},
				OnConfirm: func(b Backend) tea.Cmd {
					return applyThemeForAppCmd(b, id, app, cached)
				},
			}
		}
	}
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Apply Theme Palette (All Apps)",
			Body:         fmt.Sprintf("Theme: %s\nTemplates: %d registered config(s)\nCommand: ws theme set %s", id, len(p.templates), id),
			ConfirmLabel: "Apply All",
			Dangerous:    false,
			StartPanel:   PanelWorkstation,
			StartMsg:     systemMutationStartMsg{kind: "theme"},
			OnConfirm: func(b Backend) tea.Cmd {
				return applyThemeCmd(b, id, cached)
			},
		}
	}
}

func (p workstationPanel) launchEditor(backend Backend) tea.Cmd {
	tpl, ok := p.selectedTemplate()
	if !ok || tpl.App == "" {
		return func() tea.Msg { return statusToastMsg{text: "No template selected to edit"} }
	}
	if backend == nil {
		return func() tea.Msg { return statusToastMsg{text: "Backend unavailable", isErr: true} }
	}
	cmd, err := backend.EditAppCmd(tpl.App)
	if err != nil {
		return func() tea.Msg { return statusToastMsg{text: "Edit command failed: " + err.Error(), isErr: true} }
	}
	if cmd == nil {
		return func() tea.Msg { return statusToastMsg{text: "No editor configured for " + tpl.App, isErr: true} }
	}
	appName := tpl.App
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editFinishedMsg{app: appName, err: err}
	})
}

func (p workstationPanel) ViewMain(width, height int, focused bool, st Styles) string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.ViewMain(width, height, focused, st)
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.ViewMain(width, height, focused, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	switch p.subTab {
	case wsSubThemes:
		return p.renderThemesMain(width, height, innerW, innerH, focused, st)
	case wsSubPresets:
		return p.renderPresetsMain(width, height, innerW, innerH, focused, st)
	case wsSubHistory:
		if p.histDrillOpen {
			return p.ViewPreview(width, height, focused, false, st)
		}
		return p.renderHistoryMain(width, height, innerW, innerH, focused, st)
	default:
		return p.doctor.ViewMain(width, height, focused, st)
	}
}

func (p workstationPanel) renderThemesMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Themes & App Templates (%d themes · %d templates)", len(p.filteredThemes), len(p.templates))
	topH := clamp((height*48)/100, 6, max(6, height-6))
	botH := max(5, height-topH)

	var tRows [][]string
	for _, id := range p.filteredThemes {
		state := "palette"
		if id == p.activeTheme {
			state = st.OKText.Render("● active")
		} else if id == p.previewTheme {
			state = st.WarnText.Render("○ preview")
		}
		swatch := ""
		if pal, ok := p.palettes[id]; ok {
			swatch = RenderSwatch(pal, false)
		}
		tRows = append(tRows, []string{id, state, swatch})
	}
	tLines := renderDataTable([]string{"Theme ID", "State", "Swatches"}, tRows, p.themeIdx, focused && !p.themeFocusTpl, innerW, max(3, topH-2), st)
	topBox := renderTitledBox(title, "", tLines, width, topH, focused && !p.themeFocusTpl, st)

	var tplLines []string
	if !p.templatesLoaded {
		tplLines = []string{st.Muted.Render("Checking themes/registry...")}
	} else if p.tplErr != "" {
		tplLines = []string{st.ErrText.Render(truncate(p.tplErr, innerW))}
	} else {
		rows := make([][]string, 0, len(p.templates))
		for _, r := range p.templates {
			hookLabel := "—"
			if strings.TrimSpace(r.PostHook) != "" {
				hookLabel = st.InfoText.Render("reload")
			}
			statSt := st.OKText
			switch strings.ToLower(r.Status) {
			case "stale", "modified":
				statSt = st.WarnText
			case "missing", "error":
				statSt = st.ErrText
			}
			rows = append(rows, []string{r.App, statSt.Render(r.Status), hookLabel, collapseHomePath(r.Output)})
		}
		tplLines = renderDataTable([]string{"App", "Status", "Hook", "Rendered Output"}, rows, p.tplIdx, focused && p.themeFocusTpl, innerW, max(3, botH-2), st)
	}
	botBox := renderTitledBox(fmt.Sprintf("Templates & Hooks (%d)", len(p.templates)), "", tplLines, width, botH, focused && p.themeFocusTpl, st)
	return lipgloss.JoinVertical(lipgloss.Left, topBox, botBox)
}

func (p workstationPanel) renderPresetsMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Workstation Presets (%d)", len(p.filteredPresets))
	if !p.presetsLoaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Loading presets.toml...")}, width, height, focused, st)
	}
	if p.presetsErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.presetsErr, innerW))}, width, height, focused, st)
	}
	if len(p.filteredPresets) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No presets matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredPresets))
	for _, pr := range p.filteredPresets {
		match := "—"
		isActive := pr.ID == p.activePresetID || (p.activePresetID == "" && pr.Active)
		if isActive {
			match = st.OKText.Render("● active")
		} else if pr.MatchesHost {
			match = st.InfoText.Render("match")
		}
		rows = append(rows, []string{
			pr.ID,
			match,
			fmt.Sprintf("%d", pr.PackageCount),
			fmt.Sprintf("%d", pr.LinksCount),
			pr.Description,
		})
	}
	lines := renderDataTable([]string{"Preset", "Match", "Pkgs", "Links", "Description"}, rows, p.presetCursor, focused, innerW, innerH, st)
	return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
}

func (p workstationPanel) renderHistoryMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Unified Shell History (%d commands)", len(p.filteredHist))
	if p.histSource != "" {
		title += " · " + p.histSource
	}
	if !p.histLoaded && len(p.histRows) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Loading zsh / bash / atuin history...")}, width, height, focused, st)
	}
	if p.histErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.histErr, innerW))}, width, height, focused, st)
	}
	if len(p.filteredHist) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No history entries matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredHist))
	marked := make(map[int]bool, p.histSel.Count())
	for i, h := range p.filteredHist {
		if p.histSel.Has(histRowKey(h)) {
			marked[i] = true
		}
		ts := "—"
		if !h.Timestamp.IsZero() {
			ts = h.Timestamp.Local().Format("01-02 15:04")
		}
		exitCell := st.OKText.Render("0")
		if h.ExitCode != 0 {
			exitCell = st.ErrText.Render(fmt.Sprintf("%d", h.ExitCode))
		}
		rows = append(rows, []string{
			ts,
			nonEmpty(h.Shell, "—"),
			nonEmpty(h.Zone, "—"),
			exitCell,
			formatDurationMs(h.DurationMs),
			strings.ReplaceAll(h.Command, "\n", " ↵ "),
		})
	}
	lines := renderSelectableTable([]string{"Time", "Shell", "Zone", "Exit", "Dur", "Command"}, rows, p.histCursor, marked, focused, innerW, innerH, st)
	badge := ""
	if p.histSel.Count() > 0 {
		badge = fmt.Sprintf("%d sel", p.histSel.Count())
	}
	return renderTitledBox(title, badge, lines, width, height, focused, st)
}

func (p workstationPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.ViewPreview(width, height, focused, borderless, st)
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.ViewPreview(width, height, focused, borderless, st)
	}
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	switch p.subTab {
	case wsSubThemes:
		return p.renderThemePreview(width, height, innerW, innerH, focused, borderless, st)
	case wsSubPresets:
		return p.renderPresetPreview(width, height, innerW, innerH, focused, borderless, st)
	case wsSubHistory:
		return p.renderHistoryPreview(width, height, innerW, innerH, focused, borderless, st)
	default:
		return p.doctor.ViewPreview(width, height, focused, borderless, st)
	}
}

func (p workstationPanel) renderThemePreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	id := p.selectedThemeID()
	var all []string
	if id != "" {
		pal, ok := p.palettes[id]
		if !ok {
			pal = DefaultPalette()
			pal.ID = id
		}
		all = append(all, st.AccentText.Render("Palette: "+id)+"  "+RenderSwatch(pal, false))
		switch {
		case id == p.activeTheme:
			all = append(all, st.OKText.Render("● active theme"))
		case id == p.previewTheme:
			all = append(all, st.WarnText.Render("○ previewing live"))
		default:
			all = append(all, st.Muted.Render("inactive palette"))
		}
		all = append(all, "")
	}
	if tpl, ok := p.selectedTemplate(); ok {
		all = append(all, st.SectionHeader.Render("Template: "+tpl.App+" ("+tpl.Status+")"))
		all = append(all, st.Muted.Render("Output: "+collapseHomePath(tpl.Output)))
		if tpl.Diff != "" {
			all = append(all, "")
			for _, dl := range strings.Split(tpl.Diff, "\n") {
				all = append(all, truncate(dl, innerW))
			}
		}
	}
	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Theme & Diff Preview", "", vis, width, height, focused, st)
}

func (p workstationPanel) renderPresetPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	pr, ok := p.selectedPresetSummary()
	if !ok {
		return renderTitledBox("Preset Inspector", "", []string{st.Muted.Render("Select a preset to inspect.")}, width, height, focused, st)
	}
	state := "available"
	if pr.ID == p.activePresetID || (p.activePresetID == "" && pr.Active) {
		state = st.OKText.Render("● active")
	} else if pr.MatchesHost {
		state = st.InfoText.Render("matches host")
	}
	pairs := [][2]string{
		{"Preset", st.AccentText.Render(pr.ID)},
		{"Status", state},
		{"Packages", fmt.Sprintf("%d", pr.PackageCount)},
		{"Links", fmt.Sprintf("%d", pr.LinksCount)},
	}
	if pr.Secrets != "" {
		pairs = append(pairs, [2]string{"Secrets", pr.Secrets})
	}
	if len(pr.Include) > 0 {
		pairs = append(pairs, [2]string{"Include", strings.Join(pr.Include, ", ")})
	}
	if len(pr.Exclude) > 0 {
		pairs = append(pairs, [2]string{"Exclude", strings.Join(pr.Exclude, ", ")})
	}
	if len(pr.OSHooks) > 0 {
		pairs = append(pairs, [2]string{"OS Hooks", strings.Join(pr.OSHooks, ", ")})
	}
	if len(pr.Tags) > 0 {
		pairs = append(pairs, [2]string{"Tags", strings.Join(pr.Tags, ", ")})
	}

	var all []string
	all = append(all, renderKVTable(pairs, innerW, st)...)
	if pr.Description != "" {
		all = append(all, "", st.SectionHeader.Render("Description"))
		for _, ln := range wrapPlainText(pr.Description, innerW) {
			all = append(all, st.RowNormal.Render(ln))
		}
	}
	if resolved, okR := p.presetPkgCache[pr.ID]; okR && len(resolved) > 0 {
		all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Resolved Packages (%d)", len(resolved))))
		limit := min(12, len(resolved))
		for i := 0; i < limit; i++ {
			t := resolved[i]
			all = append(all, fmt.Sprintf("  • %s (%s) · %s", nonEmpty(t.PackageName, t.ID), t.Provider, nonEmpty(t.Status, "unknown")))
		}
		if len(resolved) > limit {
			all = append(all, st.Muted.Render(fmt.Sprintf("  … +%d more (press o to open in Packages)", len(resolved)-limit)))
		}
	}
	all = append(all, "", st.Muted.Render("Enter switches preset · o opens in Packages · ctrl+o jumps back"))

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Preset Inspector · "+pr.ID, "", vis, width, height, focused, st)
}

func (p workstationPanel) renderHistoryPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	h, ok := p.selectedHistoryRow()
	if !ok {
		return renderTitledBox("Command Detail", "", []string{st.Muted.Render("Select a history command.")}, width, height, focused, st)
	}
	ts := "—"
	if !h.Timestamp.IsZero() {
		ts = h.Timestamp.Local().Format("2006-01-02 15:04:05")
	}
	var all []string
	all = append(all, renderKVTable([][2]string{
		{"Time", ts},
		{"Shell / Zone", nonEmpty(h.Shell, "—") + " · " + nonEmpty(h.Zone, "—")},
		{"Exit / Dur", fmt.Sprintf("exit %d · %s", h.ExitCode, formatDurationMs(h.DurationMs))},
	}, innerW, st)...)
	all = append(all, "", st.SectionHeader.Render("Full Command"))
	all = append(all, strings.Split(h.Command, "\n")...)

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Shell Command", "", vis, width, height, focused, st)
}

func (p workstationPanel) ViewModal(width, height int, st Styles) (string, bool) {
	switch p.subTab {
	case wsSubDoctor:
		return p.doctor.ViewModal(width, height, st)
	case wsSubSettings, wsSubWatchedFiles:
		return p.settings.ViewModal(width, height, st)
	default:
		return "", false
	}
}

func loadThemesCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return themesLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		ids, err := backend.ListThemes()
		if err != nil {
			return themesLoadedMsg{err: err}
		}
		active, _ := backend.ActiveTheme()
		palettes := make(map[string]config.Palette, len(ids))
		for _, id := range ids {
			if pal, err := backend.LoadPalette(id); err == nil {
				palettes[id] = pal
			}
		}
		return themesLoadedMsg{ids: ids, palettes: palettes, active: active}
	}
}

func applyThemeCmd(backend Backend, id string, cached config.Palette) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return themeChangedMsg{id: id, err: fmt.Errorf("backend unavailable")}
		}
		pal := cached
		if len(pal.Tokens) == 0 {
			loaded, err := backend.LoadPalette(id)
			if err != nil {
				return themeChangedMsg{id: id, err: err}
			}
			pal = loaded
		}
		res, err := backend.ApplyTheme(id)
		return themeChangedMsg{id: id, palette: pal, result: res, err: err}
	}
}

func applyThemeForAppCmd(backend Backend, id, app string, cached config.Palette) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return themeChangedMsg{id: id, app: app, err: fmt.Errorf("backend unavailable")}
		}
		pal := cached
		if len(pal.Tokens) == 0 {
			loaded, err := backend.LoadPalette(id)
			if err != nil {
				return themeChangedMsg{id: id, app: app, err: err}
			}
			pal = loaded
		}
		res, err := backend.ApplyThemeForApp(id, app)
		return themeChangedMsg{id: id, app: app, palette: pal, result: res, err: err}
	}
}

func loadTemplatesCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return templatesLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		rows, err := backend.TemplateStatuses()
		return templatesLoadedMsg{rows: rows, err: err}
	}
}

func loadHistoryCmd(backend Backend, query string, limit int) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return historyLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		rows, src, err := backend.LoadHistory(query, limit)
		return historyLoadedMsg{source: src, rows: rows, err: err}
	}
}
