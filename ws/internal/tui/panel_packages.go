package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

type pkgUntrackedLoadedMsg struct {
	rows []PresetToolRow
	err  error
}

type pkgListMutatedMsg struct {
	summary string
	err     error
}

type updateAllPreviewReadyMsg struct {
	preview UpdatePlanPreview
	all     bool
	err     error
}

type pkgSearchResultsMsg struct {
	seq      int
	query    string
	provider string
	rows     []PackageSearchRow
	err      error
}

type systemPkgSearchMsg struct {
	query      string
	provider   string
	focusInput bool
}

type systemHostProvidersMsg struct {
	providers []string
}

type pkgTTYFinishedMsg struct {
	action  string
	command string
	err     error
}

type jumpToPresetsMsg struct{}

const (
	pkgSubInstalled = iota
	pkgSubSearch
	pkgSubMiseTools
	pkgSubMiseConfigs
	pkgSubCleanup
	pkgSubCount
)

var cleanupTierOrder = []string{"safe", "costly", "dangerous"}

type packagesPanel struct {
	subTab int
	mise   misePanel

	presets           []PresetSummary
	pkgCache          map[string][]PresetToolRow
	untracked         []PresetToolRow
	untrackedLoaded   bool
	filteredPkgs      []PresetToolRow
	pkgDetails        map[string]PackageDetail
	pkgDetailBusy     map[string]bool
	pkgQuery          string
	pkgFilters        FacetFilterState
	outdatedMap       map[string]PackageOutdatedInfo
	pkgSel            SelectionSet
	presetsLoad       bool
	presetsErr        string
	resolveErr        string
	presetIdx         int
	pkgIdx            int
	resolvingID       string
	pkgRunning        bool
	pkgDrillOpen      bool
	presetsActID      int
	resolveActID      int
	pkgAddModalOpen   bool
	pkgAddNameInput   textinput.Model
	pkgAddProviders   []string
	pkgAddProviderIdx int
	pkgAddFiles       []string
	pkgAddFileIdx     int
	pkgAddFocus       int
	pkgAddBtnIdx      int

	searchInput        textinput.Model
	searchInputFocused bool
	searchProviders    []string
	searchProviderIdx  int
	searchQuery        string
	searchFilter       string
	searchResults      []PackageSearchRow
	filteredSearch     []PackageSearchRow
	searchIdx          int
	searchLoading      bool
	searchErr          string
	searchActID        int
	searchReqSeq       int

	cleanItems     []CleanItem
	cleanOrder     []int
	cleanFiltered  []int
	cleanSelected  SelectionSet
	cleanQuery     string
	cleanFilters   FacetFilterState
	cleanLoaded    bool
	cleanErr       string
	cleanCursor    int
	cleanDryRun    bool
	cleanRunning   bool
	cleanScanActID int
	cleanRunActID  int

	previewScroll int
}

func newPackagesPanel(initialSubTab int) packagesPanel {
	if initialSubTab < 0 || initialSubTab >= pkgSubCount {
		initialSubTab = pkgSubInstalled
	}
	pkgNameIn := textinput.New()
	pkgNameIn.Placeholder = "e.g. jq or anomalyco/tap/opencode"
	pkgNameIn.CharLimit = 128
	pkgNameIn.SetWidth(42)

	searchIn := textinput.New()
	searchIn.Placeholder = "Type package name and press Enter..."
	searchIn.CharLimit = 128
	searchIn.SetWidth(44)
	searchIn.Blur()

	mp := newMisePanel()
	if initialSubTab == pkgSubMiseTools {
		mp = mp.SetSubView(miseSubTools).(misePanel)
	} else if initialSubTab == pkgSubMiseConfigs {
		mp = mp.SetSubView(miseSubConfigs).(misePanel)
	}

	return packagesPanel{
		subTab:          initialSubTab,
		mise:            mp,
		pkgCache:        map[string][]PresetToolRow{},
		pkgDetails:      map[string]PackageDetail{},
		pkgDetailBusy:   map[string]bool{},
		cleanDryRun:     true,
		pkgAddNameInput: pkgNameIn,
		pkgAddProviders: []string{"brew", "cask", "pacman", "paru", "apt", "dnf", "mise", "gh-ext"},
		pkgAddFiles:     []string{"pkg/mac-brew.txt", "pkg/mac-cask.txt", "pkg/arch.txt", "pkg/server.txt"},
		searchInput:     searchIn,
		searchProviders: []string{"all", "brew", "cask", "paru", "pacman", "mise", "apt"},
	}
}

func (p packagesPanel) isMiseSubView() bool {
	return p.subTab == pkgSubMiseTools || p.subTab == pkgSubMiseConfigs
}

func (p packagesPanel) ID() string         { return "packages" }
func (p packagesPanel) Title() string      { return "Packages & Cleanup" }
func (p packagesPanel) ShortTitle() string { return "Packages" }
func (p packagesPanel) SubViews() []string {
	return []string{"Installed", "Search", "Mise Tools", "Mise Configs", "Cleanup"}
}
func (p packagesPanel) ActiveSubView() int { return p.subTab }

func (p packagesPanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < pkgSubCount {
		p.subTab = idx
		p.pkgDrillOpen = false
		p.pkgAddModalOpen = false
		p.pkgAddNameInput.Blur()
		p.searchInputFocused = false
		p.searchInput.Blur()
		p.pkgFilters.Close()
		p.cleanFilters.Close()
		p.previewScroll = 0
		if idx == pkgSubMiseTools {
			p.mise = p.mise.SetSubView(miseSubTools).(misePanel)
		} else if idx == pkgSubMiseConfigs {
			p.mise = p.mise.SetSubView(miseSubConfigs).(misePanel)
		}
	}
	return p
}

func (p packagesPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case pkgSubMiseTools, pkgSubMiseConfigs:
		nextMise, cmd := p.mise.OnSubViewChanged(backend)
		p.mise = nextMise.(misePanel)
		return p, cmd
	case pkgSubInstalled:
		var cmds []tea.Cmd
		if len(p.filteredPkgs) == 0 && len(p.presets) > 0 {
			var next ResourcePanel
			var cmd tea.Cmd
			next, cmd = p.ensurePresetResolved(backend)
			p = next.(packagesPanel)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else if !p.untrackedLoaded && backend != nil {
			if pr, ok := p.selectedPreset(); ok {
				cmds = append(cmds, loadUntrackedPkgsCmd(backend, pr.ID))
			}
		}
		if t, ok := p.selectedTool(); ok {
			cmds = append(cmds, p.ensurePackageDetailCmd(backend, t))
		}
		return p, batchNonNil(cmds...)
	case pkgSubSearch:
		if strings.TrimSpace(p.searchQuery) == "" && len(p.searchResults) == 0 && !p.searchLoading {
			p.searchInputFocused = true
			return p, p.searchInput.Focus()
		}
		if r, ok := p.selectedSearchRow(); ok {
			return p, p.ensurePackageDetailCmd(backend, searchRowToPresetTool(r))
		}
	}
	return p, nil
}

func (p packagesPanel) SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd) {
	nextMise, cmd := p.mise.SetScope(zone, backend)
	p.mise = nextMise.(misePanel)
	return p, cmd
}

func (p packagesPanel) IsZoned() bool { return true }

func (p packagesPanel) SupportsPreview() bool {
	if p.isMiseSubView() {
		return p.mise.SupportsPreview()
	}
	return !p.pkgDrillOpen
}

func (p packagesPanel) DefaultSplit(width int) SplitMode {
	if p.isMiseSubView() {
		return p.mise.DefaultSplit(width)
	}
	if p.subTab == pkgSubCleanup || p.subTab == pkgSubSearch {
		if width >= 96 {
			return SplitVertical
		}
		return SplitHorizontal
	}
	return SplitOff
}

func (p packagesPanel) CanPop() bool {
	if p.isMiseSubView() {
		return p.mise.CanPop()
	}
	return p.pkgDrillOpen || p.pkgAddModalOpen || (p.subTab == pkgSubSearch && p.searchInputFocused)
}

func (p packagesPanel) Pop() ResourcePanel {
	if p.isMiseSubView() {
		p.mise = p.mise.Pop().(misePanel)
		return p
	}
	p.pkgDrillOpen = false
	p.pkgAddModalOpen = false
	p.pkgAddNameInput.Blur()
	p.searchInputFocused = false
	p.searchInput.Blur()
	p.previewScroll = 0
	return p
}

func (p packagesPanel) activeFacetModalOpen() bool {
	return (p.subTab == pkgSubInstalled && p.pkgFilters.Open) ||
		(p.subTab == pkgSubCleanup && p.cleanFilters.Open)
}

func (p packagesPanel) CapturingInput() bool {
	if p.isMiseSubView() {
		return p.mise.CapturingInput()
	}
	return p.activeFacetModalOpen() || (p.pkgAddModalOpen && p.pkgAddFocus == 0) || (p.subTab == pkgSubSearch && p.searchInputFocused)
}

func (p packagesPanel) HasOpenModal() bool {
	if p.isMiseSubView() {
		return p.mise.HasOpenModal()
	}
	return p.pkgAddModalOpen || p.activeFacetModalOpen()
}

func (p packagesPanel) MutationInFlight() bool {
	return p.cleanRunning || p.pkgRunning || p.mise.MutationInFlight()
}

func (p packagesPanel) FilterTarget() string {
	switch p.subTab {
	case pkgSubSearch:
		return "Search Results"
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.FilterTarget()
	case pkgSubCleanup:
		return "Cleanup"
	default:
		return "Packages"
	}
}

func (p packagesPanel) FilterQuery() string {
	switch p.subTab {
	case pkgSubSearch:
		return p.searchFilter
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.FilterQuery()
	case pkgSubCleanup:
		return p.cleanQuery
	default:
		return p.pkgQuery
	}
}

func (p packagesPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case pkgSubSearch:
		p.searchFilter = q
		p.recomputeFilteredSearch()
		p.searchIdx = 0
		if r, ok := p.selectedSearchRow(); ok {
			return p, p.ensurePackageDetailCmd(backend, searchRowToPresetTool(r))
		}
	case pkgSubMiseTools, pkgSubMiseConfigs:
		nextMise, cmd := p.mise.SetFilterQuery(q, backend)
		p.mise = nextMise.(misePanel)
		return p, cmd
	case pkgSubCleanup:
		p.cleanQuery = q
		p.recomputeFilteredCleanup()
		p.cleanCursor = 0
	default:
		p.pkgQuery = q
		p.recomputeFilteredPkgs()
		p.pkgIdx = 0
		if t, ok := p.selectedTool(); ok {
			return p, p.ensurePackageDetailCmd(backend, t)
		}
	}
	return p, nil
}

func (p packagesPanel) OnBlur() ResourcePanel {
	p.searchInputFocused = false
	p.searchInput.Blur()
	p.pkgFilters.Close()
	p.cleanFilters.Close()
	p.mise = p.mise.OnBlur().(misePanel)
	return p
}

func (p packagesPanel) SelectedCount() int {
	switch p.subTab {
	case pkgSubInstalled:
		return p.pkgSel.Count()
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.SelectedCount()
	case pkgSubCleanup:
		return p.cleanSelected.Count()
	default:
		return 0
	}
}

func (p packagesPanel) ClearSelection() ResourcePanel {
	switch p.subTab {
	case pkgSubInstalled:
		p.pkgSel = p.pkgSel.Clear()
	case pkgSubMiseTools, pkgSubMiseConfigs:
		p.mise = p.mise.ClearSelection().(misePanel)
	case pkgSubCleanup:
		p.cleanSelected = p.cleanSelected.Clear()
	}
	return p
}

func (p packagesPanel) Attention() AttentionLevel {
	if p.presetsErr != "" || p.resolveErr != "" || p.searchErr != "" || p.cleanErr != "" {
		return AttentionErr
	}
	return p.mise.Attention()
}

func (p packagesPanel) PaneStatus() string {
	switch {
	case p.pkgRunning:
		return "running pkg action..."
	case p.searchLoading:
		return "searching repos..."
	case p.cleanRunning:
		return "running cleanup..."
	case p.resolvingID != "":
		return "resolving " + p.resolvingID + "..."
	default:
		return p.mise.PaneStatus()
	}
}

func (p packagesPanel) Badge() string {
	switch p.subTab {
	case pkgSubSearch:
		return fmt.Sprintf("%d hits", len(p.filteredSearch))
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.Badge()
	case pkgSubCleanup:
		_, selBytes, totBytes, _, _ := p.cleanupStats()
		if selBytes > 0 {
			return formatBytes(selBytes)
		}
		return formatBytes(totBytes)
	default:
		return fmt.Sprintf("%d pkgs", len(p.filteredPkgs))
	}
}

func (p packagesPanel) Breadcrumbs() []string {
	switch p.subTab {
	case pkgSubSearch:
		return []string{"Packages", "Search", fmt.Sprintf("%s (%d)", p.searchProvider(), len(p.filteredSearch))}
	case pkgSubMiseTools, pkgSubMiseConfigs:
		bc := p.mise.Breadcrumbs()
		if len(bc) > 0 {
			out := append([]string{"Packages"}, bc[1:]...)
			if len(out) > 1 {
				if p.subTab == pkgSubMiseTools {
					out[1] = "Mise " + out[1]
				} else {
					out[1] = "Mise " + out[1]
				}
			}
			return out
		}
		return []string{"Packages", "Mise"}
	case pkgSubCleanup:
		return []string{"Packages", fmt.Sprintf("Cleanup (%d)", len(p.cleanFiltered))}
	default:
		pr, ok := p.selectedPreset()
		pName := "Installed"
		if ok {
			pName = pr.ID
		}
		if p.pkgDrillOpen {
			if t, okT := p.selectedTool(); okT {
				return []string{"Packages", pName, t.ID}
			}
		}
		return []string{"Packages", pName, fmt.Sprintf("Installed (%d)", len(p.filteredPkgs))}
	}
}

func (p packagesPanel) StatusChips() []string {
	if p.isMiseSubView() {
		return p.mise.StatusChips()
	}
	var chips []string
	if q := p.FilterQuery(); q != "" {
		chips = append(chips, "/"+truncate(q, 18))
	}
	switch p.subTab {
	case pkgSubInstalled:
		if pr, ok := p.selectedPreset(); ok {
			chips = append(chips, "preset:"+pr.ID)
		}
		if len(p.untracked) > 0 {
			chips = append(chips, fmt.Sprintf("%d untracked", len(p.untracked)))
		}
		chips = append(chips, p.pkgFilters.Chips()...)
	case pkgSubSearch:
		chips = append(chips, "prov:"+p.searchProvider())
		if strings.TrimSpace(p.searchQuery) != "" {
			chips = append(chips, "q:"+truncate(p.searchQuery, 18))
		}
	case pkgSubCleanup:
		mode := "dry-run"
		if !p.cleanDryRun {
			mode = "exec"
		}
		chips = append(chips, "mode:"+mode)
		chips = append(chips, p.cleanFilters.Chips()...)
	}
	return chips
}

func (p packagesPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	switch p.subTab {
	case pkgSubSearch:
		if p.searchInputFocused {
			return [][2]string{
				{"enter", "Run search"},
				{"tab", "Cycle provider"},
				{"esc", "Browse results"},
			}
		}
		return [][2]string{
			{"enter/i", "Install & track"},
			{"e", "Edit query"},
			{"x", "Remove pkg"},
			{"y/Y", "Copy name / cmd"},
			{"r", "Re-run search"},
			{"a", "Search actions"},
		}
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.KeyHints(split, focusPreview)
	case pkgSubCleanup:
		return [][2]string{
			{"enter/x", "Run cleanup"},
			{"r", "Rescan"},
			{"a", "Cleanup actions"},
			{"F/S", "Filter / Sort"},
			{"space/*", "Mark items"},
		}
	default:
		return [][2]string{
			{"enter", "Package detail"},
			{"i/u/x", "Inst / Upd / Rm"},
			{"U", "Update system"},
			{"y/Y", "Copy name / cmd"},
			{"r", "Refresh"},
			{"a", "Pkg actions"},
			{"F/S", "Filter / Sort"},
			{"space/*", "Mark pkgs"},
		}
	}
}

func (p packagesPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	switch p.subTab {
	case pkgSubSearch:
		return []RowAction{
			{Key: "s", Label: "Edit search query", Description: "Focus package search input box"},
			{Key: "P", Label: "Cycle provider", Description: "Switch package search provider"},
			{Key: "I", Label: "Install only", Description: "Install selected package without tracking in pkg/*.txt"},
			{Key: "t", Label: "Track only", Description: "Append selected package to pkg/*.txt without installing"},
		}
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.RowActions(split, focusPreview)
	case pkgSubCleanup:
		return []RowAction{
			{Key: "d", Label: "Dry-run selected", Description: "Preview cleanup targets without deleting files"},
			{Key: "m", Label: "Toggle dry-run/exec", Description: "Switch default Enter/x mode between dry-run and execute"},
			{Key: "s", Label: "Select safe defaults", Description: "Mark default safe cleanup targets"},
		}
	default:
		return []RowAction{
			{Key: "t", Label: "Track untracked", Description: "Append selected untracked package(s) to pkg/*.txt"},
			{Key: "n", Label: "Add & track package", Description: "Open modal to add and track a new package"},
			{Key: "D", Label: "Untrack package", Description: "Remove from pkg/*.txt (keeps binary installed)", Dangerous: true},
			{Key: "P", Label: "Switch preset", Description: "Jump to Workstation → Presets to pick another preset"},
			{Key: "s", Label: "Search repositories", Description: "Switch to package repository search"},
			{Key: "U", Label: "Update workstation", Description: "Run full workstation update (ws update)"},
		}
	}
}

func (p packagesPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch p.subTab {
	case pkgSubSearch:
		switch actionKey {
		case "s":
			p.searchInputFocused = true
			p.searchInput.CursorEnd()
			return p, p.searchInput.Focus()
		case "P":
			p.cycleSearchProvider(1)
			if strings.TrimSpace(p.searchQuery) != "" {
				return p.runRepoSearch(backend, p.searchQuery)
			}
			return p, nil
		case "I", "t":
			r, ok := p.selectedSearchRow()
			if !ok {
				return p, func() tea.Msg { return statusToastMsg{text: "Select a search result first"} }
			}
			if actionKey != "t" && p.pkgRunning {
				return p, func() tea.Msg { return statusToastMsg{text: "Package operation already running"} }
			}
			mapped := actionKey
			if mapped == "t" {
				mapped = "a"
			}
			return p.requestSearchRowAction(r, mapped)
		}
	case pkgSubMiseTools, pkgSubMiseConfigs:
		nextMise, cmd := p.mise.RunRowAction(actionKey, backend, split, focusPreview)
		p.mise = nextMise.(misePanel)
		return p, cmd
	case pkgSubCleanup:
		switch actionKey {
		case "d":
			return p.requestCleanExecution(backend, true)
		case "m":
			p.cleanDryRun = !p.cleanDryRun
			return p, nil
		case "s":
			var safeKeys []string
			for _, it := range p.cleanItems {
				if normalizeTier(it.Tier) == "safe" && it.SelectedByDefault {
					safeKeys = append(safeKeys, it.ID)
				}
			}
			p.cleanSelected = SelectionSet{}.SetAll(safeKeys)
			return p, nil
		}
	default:
		switch actionKey {
		case "t":
			return p.trackOrOpenAddModal()
		case "n":
			p = p.openPkgAddModal()
			return p, p.pkgAddNameInput.Focus()
		case "D":
			return p.requestUntrackPackages()
		case "P":
			return p, func() tea.Msg { return jumpToPresetsMsg{} }
		case "s":
			p = p.SetSubView(pkgSubSearch).(packagesPanel)
			return p.OnSubViewChanged(backend)
		case "U":
			return p.triggerWorkstationUpdate(backend, false)
		}
	}
	return p, nil
}

func (p packagesPanel) triggerWorkstationUpdate(backend Backend, all bool) (ResourcePanel, tea.Cmd) {
	if p.pkgRunning {
		return p, func() tea.Msg { return statusToastMsg{text: "Package operation already running"} }
	}
	return p, tea.Batch(
		func() tea.Msg { return statusToastMsg{text: "Planning workstation update..."} },
		previewUpdateAllCmd(backend, all),
	)
}

func (p packagesPanel) trackOrOpenAddModal() (ResourcePanel, tea.Cmd) {
	targets := p.selectedOrHighlightedTools()
	var untracked []PresetToolRow
	for _, t := range targets {
		if strings.EqualFold(t.Status, "untracked") {
			untracked = append(untracked, t)
		}
	}
	if len(untracked) > 0 {
		var names []string
		for _, u := range untracked {
			names = append(names, fmt.Sprintf("%s (%s -> %s)", nonEmpty(u.PackageName, u.ID), u.Provider, nonEmpty(u.OriginFile, "pkg/*.txt")))
		}
		copyTargets := append([]PresetToolRow(nil), untracked...)
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        fmt.Sprintf("Track %d Untracked Package(s)", len(copyTargets)),
				Body:         "Append to workstation package list:\n  " + strings.Join(names, "\n  "),
				ConfirmLabel: "Track",
				OnConfirm: func(b Backend) tea.Cmd {
					return trackPackagesCmd(b, "", copyTargets)
				},
			}
		}
	}
	p = p.openPkgAddModal()
	return p, p.pkgAddNameInput.Focus()
}

func (p packagesPanel) requestUntrackPackages() (ResourcePanel, tea.Cmd) {
	targets := p.selectedOrHighlightedTools()
	var tracked []PresetToolRow
	var names []string
	for _, t := range targets {
		if strings.EqualFold(t.Status, "untracked") {
			continue
		}
		tracked = append(tracked, t)
		names = append(names, fmt.Sprintf("%s (%s)", nonEmpty(t.PackageName, t.ID), nonEmpty(t.OriginFile, t.Source)))
	}
	if len(tracked) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "Select a tracked package to untrack"} }
	}
	copyTargets := append([]PresetToolRow(nil), tracked...)
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        fmt.Sprintf("Untrack %d Package(s)?", len(copyTargets)),
			Body:         "Remove from pkg/*.txt (keeps binary installed on OS):\n  " + truncate(strings.Join(names, ", "), 64),
			ConfirmLabel: "Untrack",
			Dangerous:    true,
			OnConfirm: func(b Backend) tea.Cmd {
				return untrackPackagesCmd(b, copyTargets)
			},
		}
	}
}

func (p packagesPanel) selectedPreset() (PresetSummary, bool) {
	if len(p.presets) == 0 || p.presetIdx < 0 || p.presetIdx >= len(p.presets) {
		return PresetSummary{}, false
	}
	return p.presets[p.presetIdx], true
}

func (p packagesPanel) currentTools() []PresetToolRow {
	pr, ok := p.selectedPreset()
	var base []PresetToolRow
	if ok {
		base = p.pkgCache[pr.ID]
	}
	if len(p.untracked) == 0 {
		return base
	}
	out := make([]PresetToolRow, 0, len(base)+len(p.untracked))
	out = append(out, base...)
	out = append(out, p.untracked...)
	return out
}

func (p packagesPanel) selectedTool() (PresetToolRow, bool) {
	if len(p.filteredPkgs) == 0 || p.pkgIdx < 0 || p.pkgIdx >= len(p.filteredPkgs) {
		return PresetToolRow{}, false
	}
	return p.filteredPkgs[p.pkgIdx], true
}

func (p packagesPanel) selectedCleanItem() (CleanItem, bool) {
	if len(p.cleanFiltered) == 0 || p.cleanCursor < 0 || p.cleanCursor >= len(p.cleanFiltered) {
		return CleanItem{}, false
	}
	return p.cleanItems[p.cleanFiltered[p.cleanCursor]], true
}

func (p packagesPanel) selectedSearchRow() (PackageSearchRow, bool) {
	if len(p.filteredSearch) == 0 || p.searchIdx < 0 || p.searchIdx >= len(p.filteredSearch) {
		return PackageSearchRow{}, false
	}
	return p.filteredSearch[p.searchIdx], true
}

func (p packagesPanel) searchProvider() string {
	if len(p.searchProviders) == 0 {
		return "all"
	}
	idx := clamp(p.searchProviderIdx, 0, len(p.searchProviders)-1)
	return p.searchProviders[idx]
}

func (p *packagesPanel) setSearchProvider(prov string) {
	trimmed := strings.ToLower(strings.TrimSpace(prov))
	if trimmed == "" {
		return
	}
	for i, existing := range p.searchProviders {
		if strings.EqualFold(existing, trimmed) {
			p.searchProviderIdx = i
			return
		}
	}
	p.searchProviders = append(p.searchProviders, trimmed)
	p.searchProviderIdx = len(p.searchProviders) - 1
}

func (p *packagesPanel) cycleSearchProvider(delta int) {
	if len(p.searchProviders) == 0 {
		return
	}
	p.searchProviderIdx = (p.searchProviderIdx + delta + len(p.searchProviders)) % len(p.searchProviders)
}

func (p *packagesPanel) recomputeFilteredSearch() {
	p.filteredSearch = fuzzy.Filter(p.searchResults, p.searchFilter, func(r PackageSearchRow) ([]string, map[string]string) {
		status := "available"
		switch {
		case r.Installed && r.Tracked:
			status = "installed tracked"
		case r.Installed:
			status = "installed"
		case r.Tracked:
			status = "tracked"
		}
		return []string{r.Name, r.Provider, r.Repo, r.Version, r.Description},
			map[string]string{
				"provider": strings.ToLower(r.Provider),
				"repo":     strings.ToLower(r.Repo),
				"status":   status,
			}
	})
	if p.searchIdx >= len(p.filteredSearch) {
		p.searchIdx = max(0, len(p.filteredSearch)-1)
	}
}

func searchRowToPresetTool(r PackageSearchRow) PresetToolRow {
	status := "available"
	if r.Installed {
		status = "installed"
	}
	return PresetToolRow{
		ID:            r.Name,
		PackageName:   r.Name,
		Provider:      r.Provider,
		Updates:       "manager",
		Source:        nonEmpty(r.Repo, r.Provider),
		Status:        status,
		LatestVersion: r.Version,
		Description:   r.Description,
		OriginFile:    r.SuggestedFile,
		InstallCmd:    r.InstallCmd,
		RemoveCmd:     r.RemoveCmd,
	}
}

func overlayOutdatedOnTools(rows []PresetToolRow, outdated map[string]PackageOutdatedInfo) []PresetToolRow {
	if len(rows) == 0 {
		return rows
	}
	out := make([]PresetToolRow, len(rows))
	copy(out, rows)
	if len(outdated) == 0 {
		return out
	}
	for i := range out {
		t := &out[i]
		if t.Updates == "self" || !strings.EqualFold(t.Status, "installed") {
			continue
		}
		prov := strings.ToLower(strings.TrimSpace(t.Provider))
		pkg := strings.ToLower(strings.TrimSpace(t.PackageName))
		idLow := strings.ToLower(strings.TrimSpace(t.ID))
		var info PackageOutdatedInfo
		found := false
		for _, k := range []string{prov + ":" + pkg, prov + ":" + idLow, pkg, idLow} {
			if k == "" || k == ":" {
				continue
			}
			if inf, ok := outdated[k]; ok {
				info = inf
				found = true
				break
			}
		}
		if found {
			t.Outdated = true
			t.Status = "outdated"
			t.CurrentVersion = info.Current
			t.LatestVersion = info.Latest
		}
	}
	return out
}

func (p packagesPanel) pkgFacetSpec() ([]FacetGroup, []SortOption) {
	tools := p.currentTools()
	stCnt := map[string]int{}
	provCnt := map[string]int{}
	updCnt := map[string]int{}
	origCnt := map[string]int{}
	for _, t := range tools {
		stCnt[strings.ToLower(nonEmpty(t.Status, "unknown"))]++
		if t.Provider != "" {
			provCnt[strings.ToLower(t.Provider)]++
		}
		updCnt[strings.ToLower(nonEmpty(t.Updates, "manager"))]++
		if t.OriginPreset != "" {
			origCnt[strings.ToLower(t.OriginPreset)]++
		}
	}
	var provKeys []string
	for k := range provCnt {
		provKeys = append(provKeys, k)
	}
	sort.Strings(provKeys)
	var provOpts []FacetOption
	for _, k := range provKeys {
		provOpts = append(provOpts, FacetOption{Value: k, Label: k, Count: provCnt[k]})
	}
	var origKeys []string
	for k := range origCnt {
		origKeys = append(origKeys, k)
	}
	sort.Strings(origKeys)
	var origOpts []FacetOption
	for _, k := range origKeys {
		origOpts = append(origOpts, FacetOption{Value: k, Label: k, Count: origCnt[k]})
	}
	groups := []FacetGroup{
		{
			Key:   "status",
			Label: "Status",
			Options: []FacetOption{
				{Value: "outdated", Label: "Outdated", Count: stCnt["outdated"]},
				{Value: "missing", Label: "Missing", Count: stCnt["missing"]},
				{Value: "untracked", Label: "Untracked", Count: stCnt["untracked"]},
				{Value: "conflict", Label: "Conflict", Count: stCnt["conflict"]},
				{Value: "installed", Label: "Installed", Count: stCnt["installed"]},
			},
		},
		{Key: "provider", Label: "Provider", Options: provOpts},
		{
			Key:   "updates",
			Label: "Updates",
			Options: []FacetOption{
				{Value: "manager", Label: "Manager", Count: updCnt["manager"]},
				{Value: "self", Label: "Self-update", Count: updCnt["self"]},
			},
		},
	}
	if len(origOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "origin", Label: "Origin Preset", Options: origOpts})
	}
	sorts := []SortOption{
		{Key: "status", Label: "Status (Outdated / Missing first)"},
		{Key: "name", Label: "Package / Tool ID"},
		{Key: "provider", Label: "Provider"},
		{Key: "origin", Label: "Origin Preset"},
	}
	return groups, sorts
}

func pkgStatusRank(st string) int {
	switch strings.ToLower(st) {
	case "outdated":
		return 0
	case "missing":
		return 1
	case "untracked":
		return 2
	case "conflict":
		return 3
	case "installed":
		return 4
	default:
		return 5
	}
}

func (p *packagesPanel) recomputeFilteredPkgs() {
	tools := p.currentTools()
	filtered := fuzzy.Filter(tools, p.pkgQuery, func(t PresetToolRow) ([]string, map[string]string) {
		return []string{t.ID, t.PackageName, t.Bin, t.Provider, t.OriginPreset, t.OriginFile, t.Description},
			map[string]string{
				"provider": strings.ToLower(t.Provider),
				"via":      strings.ToLower(t.Provider),
				"status":   strings.ToLower(t.Status),
				"updates":  strings.ToLower(t.Updates),
				"origin":   strings.ToLower(t.OriginPreset),
				"source":   strings.ToLower(t.Source),
			}
	})
	var kept []PresetToolRow
	for _, t := range filtered {
		if !p.pkgFilters.Matches("status", t.Status) {
			continue
		}
		if !p.pkgFilters.Matches("provider", t.Provider) {
			continue
		}
		if !p.pkgFilters.Matches("updates", nonEmpty(t.Updates, "manager")) {
			continue
		}
		if !p.pkgFilters.Matches("origin", t.OriginPreset) {
			continue
		}
		kept = append(kept, t)
	}
	if p.pkgFilters.SortKey != "" || p.pkgFilters.SortDesc {
		sort.SliceStable(kept, func(i, j int) bool {
			var cmp int
			switch p.pkgFilters.SortKey {
			case "name":
				cmp = strings.Compare(strings.ToLower(nonEmpty(kept[i].PackageName, kept[i].ID)), strings.ToLower(nonEmpty(kept[j].PackageName, kept[j].ID)))
			case "provider":
				cmp = strings.Compare(strings.ToLower(kept[i].Provider), strings.ToLower(kept[j].Provider))
			case "origin":
				cmp = strings.Compare(strings.ToLower(kept[i].OriginPreset), strings.ToLower(kept[j].OriginPreset))
			default:
				cmp = pkgStatusRank(kept[i].Status) - pkgStatusRank(kept[j].Status)
				if cmp == 0 {
					cmp = strings.Compare(strings.ToLower(kept[i].ID), strings.ToLower(kept[j].ID))
				}
			}
			if p.pkgFilters.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	p.filteredPkgs = kept
	if p.pkgIdx >= len(p.filteredPkgs) {
		p.pkgIdx = max(0, len(p.filteredPkgs)-1)
	}
}

func (p packagesPanel) cleanFacetSpec() ([]FacetGroup, []SortOption) {
	tierCnt := map[string]int{}
	catCnt := map[string]int{}
	for _, it := range p.cleanItems {
		tierCnt[normalizeTier(it.Tier)]++
		if it.Category != "" {
			catCnt[strings.ToLower(it.Category)]++
		}
	}
	var catKeys []string
	for k := range catCnt {
		catKeys = append(catKeys, k)
	}
	sort.Strings(catKeys)
	var catOpts []FacetOption
	for _, k := range catKeys {
		catOpts = append(catOpts, FacetOption{Value: k, Label: k, Count: catCnt[k]})
	}
	groups := []FacetGroup{
		{
			Key:   "tier",
			Label: "Safety Tier",
			Options: []FacetOption{
				{Value: "safe", Label: "Safe", Count: tierCnt["safe"]},
				{Value: "costly", Label: "Costly", Count: tierCnt["costly"]},
				{Value: "dangerous", Label: "Dangerous", Count: tierCnt["dangerous"]},
			},
		},
	}
	if len(catOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "category", Label: "Category", Options: catOpts})
	}
	sorts := []SortOption{
		{Key: "tier", Label: "Safety Tier"},
		{Key: "size", Label: "Reclaimable Size"},
		{Key: "title", Label: "Title (A–Z)"},
	}
	return groups, sorts
}

func (p *packagesPanel) recomputeFilteredCleanup() {
	indices := append([]int(nil), p.cleanOrder...)
	if strings.TrimSpace(p.cleanQuery) != "" {
		indices = fuzzy.Filter(indices, p.cleanQuery, func(idx int) ([]string, map[string]string) {
			it := p.cleanItems[idx]
			return []string{it.Title, it.Category, it.ID, strings.Join(it.Paths, " ")},
				map[string]string{
					"tier":     normalizeTier(it.Tier),
					"category": strings.ToLower(it.Category),
					"cat":      strings.ToLower(it.Category),
				}
		})
	}
	var kept []int
	for _, idx := range indices {
		it := p.cleanItems[idx]
		if !p.cleanFilters.Matches("tier", normalizeTier(it.Tier)) {
			continue
		}
		if !p.cleanFilters.Matches("category", it.Category) {
			continue
		}
		kept = append(kept, idx)
	}
	if p.cleanFilters.SortKey != "" || p.cleanFilters.SortDesc {
		sort.SliceStable(kept, func(i, j int) bool {
			a, b := p.cleanItems[kept[i]], p.cleanItems[kept[j]]
			var cmp int
			switch p.cleanFilters.SortKey {
			case "size":
				if a.Bytes > b.Bytes {
					cmp = -1
				} else if a.Bytes < b.Bytes {
					cmp = 1
				}
			case "title":
				cmp = strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
			default:
				cmp = strings.Compare(normalizeTier(a.Tier), normalizeTier(b.Tier))
			}
			if p.cleanFilters.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	p.cleanFiltered = kept
	if p.cleanCursor >= len(p.cleanFiltered) {
		p.cleanCursor = max(0, len(p.cleanFiltered)-1)
	}
}

func (p packagesPanel) cleanupStats() (ids []string, selBytes, totBytes int64, tiers []string, hasDangerous bool) {
	tierSeen := map[string]bool{}
	for _, idx := range p.cleanOrder {
		it := p.cleanItems[idx]
		totBytes += it.Bytes
		if p.cleanSelected.Has(it.ID) {
			ids = append(ids, it.ID)
			selBytes += it.Bytes
			t := normalizeTier(it.Tier)
			if !tierSeen[t] {
				tierSeen[t] = true
				tiers = append(tiers, t)
			}
			if t == "dangerous" {
				hasDangerous = true
			}
		}
	}
	return ids, selBytes, totBytes, tiers, hasDangerous
}

func buildPkgActionCLI(action string, targets []PresetToolRow) string {
	act := strings.TrimSpace(action)
	if act == "" {
		act = "install"
	}
	var provOrder []string
	byProv := map[string][]string{}
	seenByProv := map[string]map[string]bool{}
	for _, t := range targets {
		name := nonEmpty(strings.TrimSpace(t.PackageName), strings.TrimSpace(t.ID))
		if name == "" {
			continue
		}
		prov := strings.TrimSpace(t.Provider)
		if _, ok := byProv[prov]; !ok {
			provOrder = append(provOrder, prov)
			seenByProv[prov] = map[string]bool{}
		}
		if !seenByProv[prov][name] {
			seenByProv[prov][name] = true
			byProv[prov] = append(byProv[prov], name)
		}
	}
	if len(provOrder) == 0 {
		return "ws pkg list"
	}
	cmds := make([]string, 0, len(provOrder))
	for _, prov := range provOrder {
		parts := []string{"ws", "pkg", act}
		if prov != "" {
			parts = append(parts, "--provider", shellQuoteArg(prov))
		}
		for _, name := range byProv[prov] {
			parts = append(parts, shellQuoteArg(name))
		}
		cmds = append(cmds, strings.Join(parts, " "))
	}
	return strings.Join(cmds, " && ")
}

func (p packagesPanel) CLIEquivalent() string {
	switch p.subTab {
	case pkgSubSearch:
		if r, ok := p.selectedSearchRow(); ok {
			if r.InstallCmd != "" {
				return r.InstallCmd
			}
			return fmt.Sprintf("ws pkg info --provider %s %s", r.Provider, r.Name)
		}
		if strings.TrimSpace(p.searchQuery) != "" {
			if prov := p.searchProvider(); prov != "" && prov != "all" {
				return fmt.Sprintf("ws pkg search --provider %s %q", prov, p.searchQuery)
			}
			return fmt.Sprintf("ws pkg search %q", p.searchQuery)
		}
		return "ws pkg search --tui"
	case pkgSubMiseTools, pkgSubMiseConfigs:
		return p.mise.CLIEquivalent()
	case pkgSubCleanup:
		_, _, _, tiers, hasDangerous := p.cleanupStats()
		mode := "--dry-run"
		if !p.cleanDryRun {
			mode = "--execute"
		}
		cmd := "ws clean " + mode
		if len(tiers) > 0 {
			cmd += " --tier " + strings.Join(tiers, ",")
		}
		if !p.cleanDryRun && hasDangerous {
			cmd += " --allow remove"
		}
		return cmd
	default:
		if p.pkgSel.Count() > 0 {
			if targets := p.selectedOrHighlightedTools(); len(targets) > 0 {
				action := "update"
				for _, t := range targets {
					if t.Status == "missing" {
						action = "install"
						break
					}
				}
				return buildPkgActionCLI(action, targets)
			}
		}
		if t, ok := p.selectedTool(); ok && t.InstallCmd != "" {
			return t.InstallCmd
		}
		if pr, ok := p.selectedPreset(); ok {
			return fmt.Sprintf("ws preset show %s", pr.ID)
		}
		return "ws preset list"
	}
}

type systemMutationStartMsg struct {
	kind string
}

func (p packagesPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case miseLoadedMsg, miseEditFinishedMsg, miseUpgradedMsg, miseMutationStartMsg:
		nextMise, cmd := p.mise.Update(msg, backend, split, focusPreview)
		p.mise = nextMise.(misePanel)
		return p, cmd

	case systemMutationStartMsg:
		switch msg.kind {
		case "pkg":
			p.pkgRunning = true
		case "clean":
			p.cleanRunning = true
		}
		return p, nil

	case previewOpenedMsg:
		switch p.subTab {
		case pkgSubMiseTools, pkgSubMiseConfigs:
			nextMise, cmd := p.mise.Update(msg, backend, split, focusPreview)
			p.mise = nextMise.(misePanel)
			return p, cmd
		case pkgSubInstalled:
			if t, ok := p.selectedTool(); ok {
				return p, p.ensurePackageDetailCmd(backend, t)
			}
		case pkgSubSearch:
			if r, ok := p.selectedSearchRow(); ok {
				return p, p.ensurePackageDetailCmd(backend, searchRowToPresetTool(r))
			}
		}
		return p, nil

	case systemHostProvidersMsg:
		if len(msg.providers) > 0 {
			cur := p.searchProvider()
			p.searchProviders = append([]string(nil), msg.providers...)
			p.searchProviderIdx = 0
			p.setSearchProvider(cur)
		}
		return p, nil

	case systemPkgSearchMsg:
		if msg.provider != "" {
			p.setSearchProvider(msg.provider)
		}
		q := strings.TrimSpace(msg.query)
		if q == "" {
			p.searchInputFocused = true
			if msg.focusInput || strings.TrimSpace(p.searchQuery) == "" {
				return p, p.searchInput.Focus()
			}
			return p, nil
		}
		p.searchInput.SetValue(q)
		p.searchInput.Blur()
		p.searchInputFocused = false
		return p.runRepoSearch(backend, q)

	case pkgSearchResultsMsg:
		if msg.seq != p.searchReqSeq {
			return p, nil
		}
		p.searchLoading = false
		var cmds []tea.Cmd
		if p.searchActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.searchActID, "Searching "+msg.query, msg.err != nil))
			p.searchActID = 0
		}
		if msg.err != nil {
			p.searchErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Package search failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.searchErr = ""
		p.searchResults = msg.rows
		p.recomputeFilteredSearch()
		p.searchIdx = 0
		p.previewScroll = 0
		if r, ok := p.selectedSearchRow(); ok {
			cmds = append(cmds, p.ensurePackageDetailCmd(backend, searchRowToPresetTool(r)))
		}
		return p, batchNonNil(cmds...)

	case systemSwitchPresetMsg:
		for i, pr := range p.presets {
			if strings.EqualFold(pr.ID, msg.preset) {
				p.presetIdx = i
				p.pkgIdx = 0
				p.pkgSel = p.pkgSel.Clear()
				p.recomputeFilteredPkgs()
				return p.ensurePresetResolved(backend)
			}
		}
		return p, func() tea.Msg { return statusToastMsg{text: "Unknown preset " + msg.preset, isErr: true} }

	case triggerCleanExecMsg:
		return p.requestCleanExecution(backend, false)

	case presetsLoadedMsg:
		p.presetsLoad = true
		var cmds []tea.Cmd
		if p.presetsActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.presetsActID, "Loading presets", msg.err != nil))
			p.presetsActID = 0
		}
		if msg.err != nil {
			p.presetsErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Presets load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.presetsErr = ""
		p.presets = msg.presets
		for i, pr := range p.presets {
			if pr.Active {
				p.presetIdx = i
				break
			}
		}
		if p.presetIdx >= len(p.presets) {
			p.presetIdx = max(0, len(p.presets)-1)
		}
		if pr, ok := p.selectedPreset(); ok {
			p.resolvingID = pr.ID
			cmds = append(cmds, resolvePresetCmd(backend, pr.ID))
		}
		return p, batchNonNil(cmds...)

	case presetResolvedMsg:
		if p.resolvingID == msg.id {
			p.resolvingID = ""
		}
		var cmds []tea.Cmd
		if p.resolveActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.resolveActID, "Resolving preset "+msg.id, msg.err != nil))
			p.resolveActID = 0
		}
		if msg.err != nil {
			p.resolveErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Resolve preset failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.resolveErr = ""
		if p.pkgCache == nil {
			p.pkgCache = map[string][]PresetToolRow{}
		}
		p.pkgCache[msg.id] = overlayOutdatedOnTools(msg.rows, p.outdatedMap)
		p.recomputeFilteredPkgs()
		if !p.untrackedLoaded && backend != nil {
			cmds = append(cmds, loadUntrackedPkgsCmd(backend, msg.id))
		}
		if t, ok := p.selectedTool(); ok && (split != SplitOff || p.pkgDrillOpen) {
			cmds = append(cmds, p.ensurePackageDetailCmd(backend, t))
		}
		return p, batchNonNil(cmds...)

	case pkgUntrackedLoadedMsg:
		p.untrackedLoaded = true
		if msg.err == nil {
			p.untracked = msg.rows
			p.recomputeFilteredPkgs()
		}
		return p, nil

	case pkgOutdatedLoadedMsg:
		if msg.err == nil {
			p.outdatedMap = msg.items
			for id, rows := range p.pkgCache {
				p.pkgCache[id] = overlayOutdatedOnTools(rows, p.outdatedMap)
			}
			p.recomputeFilteredPkgs()
		}
		return p, nil

	case pkgListMutatedMsg:
		var cmds []tea.Cmd
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		cmds = append(cmds, func() tea.Msg {
			return statusToastMsg{text: nonEmpty(msg.summary, "Package list updated")}
		})
		p.pkgSel = p.pkgSel.Clear()
		p.untrackedLoaded = false
		if pr, ok := p.selectedPreset(); ok && backend != nil {
			delete(p.pkgCache, pr.ID)
			p.resolvingID = pr.ID
			p.resolveActID = newActivityID()
			cmds = append(cmds,
				startActivityCmd(p.resolveActID, "Resolving preset "+pr.ID),
				resolvePresetCmd(backend, pr.ID),
				loadUntrackedPkgsCmd(backend, pr.ID),
			)
		}
		return p, batchNonNil(cmds...)

	case updateAllPreviewReadyMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: "Update preview failed: " + msg.err.Error(), isErr: true}
			}
		}
		prev := msg.preview
		var bodyParts []string
		bodyParts = append(bodyParts, prev.Summary)
		if prev.RepoDirty {
			bodyParts = append(bodyParts, "Note: Workstation repo has local edits (skipping git pull).")
		}
		if prev.SnapshotHint != "" {
			bodyParts = append(bodyParts, "Snapshot hint: "+prev.SnapshotHint)
		}
		maxSteps := min(8, len(prev.Steps))
		for i := 0; i < maxSteps; i++ {
			bodyParts = append(bodyParts, "  • "+truncate(prev.Steps[i], 72))
		}
		if len(prev.Steps) > maxSteps {
			bodyParts = append(bodyParts, fmt.Sprintf("  … +%d more steps", len(prev.Steps)-maxSteps))
		}
		warn := ""
		if len(prev.Warnings) > 0 {
			warn = strings.Join(prev.Warnings[:min(2, len(prev.Warnings))], "; ")
		}
		title := "Confirm Workstation Update (ws update)"
		if msg.all {
			title = "Confirm Full Workstation Update (ws update --all)"
		}
		allFlag := msg.all
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        title,
				Body:         strings.Join(bodyParts, "\n"),
				Warning:      warn,
				ConfirmLabel: "Update",
				StartPanel:   PanelPackages,
				StartMsg:     systemMutationStartMsg{kind: "pkg"},
				OnConfirm: func(b Backend) tea.Cmd {
					return execUpdateAllOrFallback(b, allFlag)
				},
			}
		}

	case pkgDetailLoadedMsg:
		delete(p.pkgDetailBusy, msg.key)
		if msg.err == nil && msg.key != "" {
			if p.pkgDetails == nil {
				p.pkgDetails = map[string]PackageDetail{}
			}
			p.pkgDetails[msg.key] = msg.detail
		}
		return p, nil

	case pkgTTYFinishedMsg:
		p.pkgRunning = false
		p.pkgSel = p.pkgSel.Clear()
		p.untrackedLoaded = false
		p.outdatedMap = nil
		p.pkgDetails = map[string]PackageDetail{}
		var cmds []tea.Cmd
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Package %s failed: %v", msg.action, msg.err), isErr: true}
			})
		} else {
			toast := fmt.Sprintf("Package %s complete", msg.action)
			if msg.action == "update-all" {
				toast = "Workstation update complete"
			}
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: toast}
			})
		}
		if pr, ok := p.selectedPreset(); ok && backend != nil {
			delete(p.pkgCache, pr.ID)
			p.resolvingID = pr.ID
			p.resolveActID = newActivityID()
			cmds = append(cmds,
				startActivityCmd(p.resolveActID, "Resolving preset "+pr.ID),
				resolvePresetCmd(backend, pr.ID),
				loadUntrackedPkgsCmd(backend, pr.ID),
				loadPkgOutdatedCmd(backend),
			)
		}
		if msg.action == "update-all" && backend != nil {
			cmds = append(cmds, loadTemplatesCmd(backend), loadThemesCmd(backend))
			if p.cleanLoaded {
				cmds = append(cmds, scanCleanupCmd(backend))
			}
		}
		if p.subTab == pkgSubSearch && strings.TrimSpace(p.searchQuery) != "" && backend != nil {
			var searchCmd tea.Cmd
			p, searchCmd = p.runRepoSearch(backend, p.searchQuery)
			if searchCmd != nil {
				cmds = append(cmds, searchCmd)
			}
		}
		return p, batchNonNil(cmds...)

	case pkgActionDoneMsg:
		p.pkgRunning = false
		p.pkgSel = p.pkgSel.Clear()
		p.untrackedLoaded = false
		var cmds []tea.Cmd
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Package %s failed: %v", msg.action, msg.err), isErr: true}
			})
		} else {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Package %s complete (see Output)", msg.action)}
			})
		}
		if pr, ok := p.selectedPreset(); ok {
			delete(p.pkgCache, pr.ID)
			p.resolvingID = pr.ID
			p.resolveActID = newActivityID()
			cmds = append(cmds,
				startActivityCmd(p.resolveActID, "Resolving preset "+pr.ID),
				resolvePresetCmd(backend, pr.ID),
				loadUntrackedPkgsCmd(backend, pr.ID),
				loadPkgOutdatedCmd(backend),
			)
		}
		if p.subTab == pkgSubSearch && strings.TrimSpace(p.searchQuery) != "" && backend != nil {
			var searchCmd tea.Cmd
			p, searchCmd = p.runRepoSearch(backend, p.searchQuery)
			if searchCmd != nil {
				cmds = append(cmds, searchCmd)
			}
		}
		return p, batchNonNil(cmds...)

	case cleanupScannedMsg:
		p.cleanLoaded = true
		p.cleanRunning = false
		var cmds []tea.Cmd
		if p.cleanScanActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.cleanScanActID, "Scanning cleanup targets", msg.err != nil))
			p.cleanScanActID = 0
		}
		if msg.err != nil {
			p.cleanErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Cleanup scan failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.cleanErr = ""
		p.cleanItems = msg.items
		p.cleanOrder = buildCleanupOrder(p.cleanItems)

		var defKeys []string
		for _, it := range p.cleanItems {
			if normalizeTier(it.Tier) == "dangerous" {
				continue
			}
			if it.SelectedByDefault {
				defKeys = append(defKeys, it.ID)
			}
		}
		p.cleanSelected = SelectionSet{}.SetAll(defKeys)
		p.recomputeFilteredCleanup()
		return p, batchNonNil(cmds...)

	case cleanupTTYFinishedMsg:
		if msg.err != nil {
			p.cleanRunning = false
			return p, func() tea.Msg {
				return statusToastMsg{text: "Cleanup failed: " + msg.err.Error(), isErr: true}
			}
		}
		if len(msg.nonSudoIDs) > 0 && backend != nil {
			nonSudo := append([]string(nil), msg.nonSudoIDs...)
			allIDs := append([]string(nil), msg.ids...)
			return p, func() tea.Msg {
				res, err := backend.RunCleanup(nonSudo, false)
				if err == nil && res != nil {
					res.Applied += len(allIDs) - len(nonSudo)
				}
				return cleanupDoneMsg{result: res, dryRun: false, ids: allIDs, err: err}
			}
		}
		p.cleanRunning = false
		p.cleanScanActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: fmt.Sprintf("Cleaned %d target(s)", len(msg.ids))} },
			startActivityCmd(p.cleanScanActID, "Scanning cleanup targets"),
			scanCleanupCmd(backend),
		)

	case cleanupDoneMsg:
		p.cleanRunning = false
		var actCmd tea.Cmd
		if p.cleanRunActID != 0 {
			actCmd = finishActivityCmd(p.cleanRunActID, "Cleanup", msg.err != nil)
			p.cleanRunActID = 0
		}
		if msg.err != nil {
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "Cleanup failed: " + msg.err.Error(), isErr: true}
			})
		}
		count := len(msg.ids)
		if msg.result != nil && msg.result.Applied > 0 {
			count = msg.result.Applied
		}
		verb := "Cleaned"
		if msg.dryRun {
			verb = "Dry-run verified"
		}
		toast := func() tea.Msg {
			return statusToastMsg{text: fmt.Sprintf("%s %d target(s)", verb, count)}
		}
		if !msg.dryRun {
			p.cleanScanActID = newActivityID()
			return p, batchNonNil(actCmd, toast, startActivityCmd(p.cleanScanActID, "Scanning cleanup targets"), scanCleanupCmd(backend))
		}
		return p, batchNonNil(actCmd, toast)

	case tea.KeyPressMsg:
		if p.isMiseSubView() {
			nextMise, cmd := p.mise.Update(msg, backend, split, focusPreview)
			p.mise = nextMise.(misePanel)
			return p, cmd
		}
		if p.pkgAddModalOpen {
			return p.updatePkgAddModal(msg, backend)
		}
		if p.subTab == pkgSubInstalled && p.pkgFilters.Open {
			groups, sorts := p.pkgFacetSpec()
			if handled, changed := p.pkgFilters.Update(msg, groups, sorts); handled {
				if changed {
					p.recomputeFilteredPkgs()
				}
				return p, nil
			}
		}
		if p.subTab == pkgSubCleanup && p.cleanFilters.Open {
			groups, sorts := p.cleanFacetSpec()
			if handled, changed := p.cleanFilters.Update(msg, groups, sorts); handled {
				if changed {
					p.recomputeFilteredCleanup()
				}
				return p, nil
			}
		}
		switch p.subTab {
		case pkgSubSearch:
			return p.updateSearchKey(msg, backend, split, focusPreview)
		case pkgSubCleanup:
			return p.updateCleanupKey(msg, backend, focusPreview)
		default:
			return p.updatePackagesKey(msg, backend, split, focusPreview)
		}
	}
	if p.isMiseSubView() {
		nextMise, cmd := p.mise.Update(msg, backend, split, focusPreview)
		p.mise = nextMise.(misePanel)
		return p, cmd
	}
	if p.pkgAddModalOpen && p.pkgAddFocus == 0 {
		var cmd tea.Cmd
		p.pkgAddNameInput, cmd = p.pkgAddNameInput.Update(msg)
		return p, cmd
	}
	if p.subTab == pkgSubSearch && p.searchInputFocused {
		var cmd tea.Cmd
		p.searchInput, cmd = p.searchInput.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p packagesPanel) ensurePresetResolved(backend Backend) (ResourcePanel, tea.Cmd) {
	pr, ok := p.selectedPreset()
	if !ok {
		return p, nil
	}
	if _, cached := p.pkgCache[pr.ID]; cached {
		p.recomputeFilteredPkgs()
		return p, nil
	}
	p.resolvingID = pr.ID
	p.resolveActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.resolveActID, "Resolving preset "+pr.ID),
		resolvePresetCmd(backend, pr.ID),
	)
}

func pkgDetailKey(t PresetToolRow) string {
	return t.ID + "|" + t.Provider
}

func (p packagesPanel) ensurePackageDetailCmd(backend Backend, t PresetToolRow) tea.Cmd {
	if backend == nil {
		return nil
	}
	dk := pkgDetailKey(t)
	if _, ok := p.pkgDetails[dk]; ok || p.pkgDetailBusy[dk] {
		return nil
	}
	p.pkgDetailBusy[dk] = true
	return loadPackageDetailCmd(backend, dk, t)
}

func (p packagesPanel) updatePackagesKey(msg tea.KeyPressMsg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if isSpaceKey(msg) {
		if t, ok := p.selectedTool(); ok {
			p.pkgSel = p.pkgSel.Toggle(t.ID)
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview || p.pkgDrillOpen {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.pkgIdx, len(p.filteredPkgs), defaultMotionPage, m); changed {
			p.pkgIdx = next
			p.previewScroll = 0
			if t, ok := p.selectedTool(); ok && split != SplitOff {
				return p, p.ensurePackageDetailCmd(backend, t)
			}
		}
		return p, nil
	}
	switch msg.String() {
	case "F":
		groups, sorts := p.pkgFacetSpec()
		p.pkgFilters.OpenFilters(groups, sorts)
		return p, nil
	case "S":
		groups, sorts := p.pkgFacetSpec()
		p.pkgFilters.OpenSort(groups, sorts)
		return p, nil
	case "*":
		keys := make([]string, len(p.filteredPkgs))
		for i, t := range p.filteredPkgs {
			keys[i] = t.ID
		}
		p.pkgSel = p.pkgSel.ToggleAll(keys)
	case `\`:
		p.pkgSel = p.pkgSel.Clear()
		if p.pkgFilters.HasActive() {
			p.pkgFilters.Clear()
			p.recomputeFilteredPkgs()
		}
	case "enter":
		if t, ok := p.selectedTool(); ok {
			p.pkgDrillOpen = !p.pkgDrillOpen
			p.previewScroll = 0
			if p.pkgDrillOpen {
				return p, p.ensurePackageDetailCmd(backend, t)
			}
		}
	case "u", "i":
		action := "update"
		if msg.String() == "i" {
			action = "install"
		}
		return p.requestPackageAction(action)
	case "U":
		return p.triggerWorkstationUpdate(backend, false)
	case "x":
		return p.requestPackageAction("remove")
	case "y":
		if t, ok := p.selectedTool(); ok {
			return p, copyCmd(nonEmpty(t.PackageName, t.ID), "package name")
		}
	case "Y":
		if t, ok := p.selectedTool(); ok && t.InstallCmd != "" {
			return p, copyCmd(t.InstallCmd, "install command")
		}
	case "r":
		p.presetsActID = newActivityID()
		p.untrackedLoaded = false
		if pr, ok := p.selectedPreset(); ok {
			delete(p.pkgCache, pr.ID)
			p.pkgDetails = map[string]PackageDetail{}
			p.resolvingID = pr.ID
			p.resolveActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.presetsActID, "Loading presets"),
				startActivityCmd(p.resolveActID, "Resolving preset "+pr.ID),
				loadPresetsCmd(backend),
				resolvePresetCmd(backend, pr.ID),
				loadUntrackedPkgsCmd(backend, pr.ID),
				loadPkgOutdatedCmd(backend),
			)
		}
		return p, tea.Batch(startActivityCmd(p.presetsActID, "Loading presets"), loadPresetsCmd(backend))
	}
	return p, nil
}

func (p packagesPanel) selectedOrHighlightedTools() []PresetToolRow {
	if p.pkgSel.Count() > 0 {
		var out []PresetToolRow
		for _, t := range p.filteredPkgs {
			if p.pkgSel.Has(t.ID) {
				out = append(out, t)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if t, ok := p.selectedTool(); ok {
		return []PresetToolRow{t}
	}
	return nil
}

func (p packagesPanel) requestPackageAction(action string) (ResourcePanel, tea.Cmd) {
	if p.pkgRunning {
		return p, func() tea.Msg { return statusToastMsg{text: "Package operation already running"} }
	}
	targets := p.selectedOrHighlightedTools()
	if len(targets) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "No package selected"} }
	}
	var names []string
	var previewCmds []string
	for _, t := range targets {
		names = append(names, nonEmpty(t.PackageName, t.ID)+" ("+t.Provider+")")
		var c string
		switch action {
		case "install":
			c = strings.TrimSpace(t.InstallCmd)
		case "remove":
			c = strings.TrimSpace(t.RemoveCmd)
		default:
			c = strings.TrimSpace(t.UpdateCmd)
		}
		if c != "" && !strings.HasPrefix(c, "(") {
			previewCmds = append(previewCmds, c)
		}
	}
	targetsCopy := append([]PresetToolRow(nil), targets...)
	cmdCLI := buildPkgActionCLI(action, targetsCopy)
	cmdPreview := nonEmpty(strings.Join(previewCmds, " && "), cmdCLI)
	body := fmt.Sprintf("Action: %s %d package(s)\nTargets: %s\nCommand: %s",
		strings.ToUpper(action), len(targets), truncate(strings.Join(names, ", "), 56), nonEmpty(truncate(cmdPreview, 58), action))
	warn := ""
	if action == "remove" {
		warn = "Removing packages uninstalls them from your system package manager."
	}

	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        fmt.Sprintf("Confirm Package %s", strings.ToUpper(action)),
			Body:         body,
			Warning:      warn,
			ConfirmLabel: strings.ToUpper(action),
			Dangerous:    action == "remove",
			StartPanel:   PanelPackages,
			StartMsg:     systemMutationStartMsg{kind: "pkg"},
			OnConfirm: func(b Backend) tea.Cmd {
				return execPackageActionOrFallback(b, action, targetsCopy, cmdPreview)
			},
		}
	}
}

func (p packagesPanel) updateCleanupKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if isSpaceKey(msg) {
		if it, ok := p.selectedCleanItem(); ok {
			p.cleanSelected = p.cleanSelected.Toggle(it.ID)
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.cleanCursor, len(p.cleanFiltered), defaultMotionPage, m); changed {
			p.cleanCursor = next
			p.previewScroll = 0
		}
		return p, nil
	}
	switch msg.String() {
	case "F":
		groups, sorts := p.cleanFacetSpec()
		p.cleanFilters.OpenFilters(groups, sorts)
		return p, nil
	case "S":
		groups, sorts := p.cleanFacetSpec()
		p.cleanFilters.OpenSort(groups, sorts)
		return p, nil
	case "*":
		keys := make([]string, len(p.cleanFiltered))
		for i, idx := range p.cleanFiltered {
			keys[i] = p.cleanItems[idx].ID
		}
		p.cleanSelected = p.cleanSelected.ToggleAll(keys)
	case `\`:
		p.cleanSelected = p.cleanSelected.Clear()
		if p.cleanFilters.HasActive() {
			p.cleanFilters.Clear()
			p.recomputeFilteredCleanup()
		}
	case "r":
		p.cleanLoaded = false
		p.cleanScanActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.cleanScanActID, "Scanning cleanup targets"),
			scanCleanupCmd(backend),
		)
	case "x", "enter":
		return p.requestCleanExecution(backend, p.cleanDryRun)
	}
	return p, nil
}

func (p packagesPanel) requestCleanExecution(backend Backend, dryRun bool) (ResourcePanel, tea.Cmd) {
	if p.cleanRunning {
		return p, func() tea.Msg { return statusToastMsg{text: "Cleanup already in progress"} }
	}
	ids, selBytes, _, tiers, hasDangerous := p.cleanupStats()
	if len(ids) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "Select at least one cleanup item with space"} }
	}
	idsCopy := append([]string(nil), ids...)
	if dryRun {
		return p, func() tea.Msg {
			if backend == nil {
				return cleanupDoneMsg{dryRun: true, ids: idsCopy, err: fmt.Errorf("backend unavailable")}
			}
			res, err := backend.RunCleanup(idsCopy, true)
			return cleanupDoneMsg{result: res, dryRun: true, ids: idsCopy, err: err}
		}
	}

	warn := ""
	if hasDangerous {
		warn = "Selection includes DANGEROUS targets (irreversible data/volumes)."
	}
	body := fmt.Sprintf("Mode: EXECUTE (deletes selected targets)\nSelected: %d item(s) · %s\nTiers: %s",
		len(idsCopy), formatBytes(selBytes), strings.ToUpper(strings.Join(tiers, ", ")))
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Confirm Cleanup Execution",
			Body:         body,
			Warning:      warn,
			ConfirmLabel: "Execute Cleanup",
			Dangerous:    hasDangerous || len(idsCopy) > 0,
			StartPanel:   PanelPackages,
			StartMsg:     systemMutationStartMsg{kind: "clean"},
			OnConfirm: func(b Backend) tea.Cmd {
				if b == nil {
					return func() tea.Msg {
						return cleanupDoneMsg{dryRun: false, ids: idsCopy, err: fmt.Errorf("backend unavailable")}
					}
				}
				execCmd, err := b.CleanupExecCmd(idsCopy)
				if err != nil {
					return func() tea.Msg { return cleanupDoneMsg{dryRun: false, ids: idsCopy, err: err} }
				}
				if execCmd != nil {
					var nonSudoIDs []string
					for _, it := range p.cleanItems {
						if p.cleanSelected.Has(it.ID) && !it.NeedsSudo {
							nonSudoIDs = append(nonSudoIDs, it.ID)
						}
					}
					return tea.ExecProcess(execCmd, func(err error) tea.Msg {
						return cleanupTTYFinishedMsg{ids: idsCopy, nonSudoIDs: nonSudoIDs, err: err}
					})
				}
				actID := newActivityID()
				return tea.Batch(
					startActivityCmd(actID, "Running cleanup"),
					func() tea.Msg {
						res, err := b.RunCleanup(idsCopy, false)
						return cleanupDoneMsg{result: res, dryRun: false, ids: idsCopy, err: err}
					},
				)
			},
		}
	}
}

func (p packagesPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.isMiseSubView() {
		return p.mise.ViewMain(width, height, focused, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	switch p.subTab {
	case pkgSubSearch:
		return p.renderSearchMain(width, height, innerW, innerH, focused, st)
	case pkgSubCleanup:
		return p.renderCleanupMain(width, height, innerW, innerH, focused, st)
	default:
		if p.pkgDrillOpen {
			return p.ViewPreview(width, height, focused, false, st)
		}
		return p.renderPackagesMain(width, height, innerW, innerH, focused, st)
	}
}

func (p packagesPanel) runRepoSearch(backend Backend, q string) (packagesPanel, tea.Cmd) {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		p.searchInputFocused = true
		return p, p.searchInput.Focus()
	}
	p.searchQuery = trimmed
	p.searchErr = ""
	p.searchLoading = true
	p.searchReqSeq++
	if p.searchActID != 0 {
		p.searchActID = 0
	}
	p.searchActID = newActivityID()
	prov := p.searchProvider()
	return p, tea.Batch(
		startActivityCmd(p.searchActID, "Searching "+trimmed),
		searchPackagesCmd(backend, p.searchReqSeq, trimmed, prov),
	)
}

func (p packagesPanel) updateSearchKey(msg tea.KeyPressMsg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	if p.searchInputFocused {
		switch key {
		case "esc":
			p.searchInputFocused = false
			p.searchInput.Blur()
			return p, nil
		case "tab", "ctrl+p":
			p.cycleSearchProvider(1)
			return p, nil
		case "shift+tab":
			p.cycleSearchProvider(-1)
			return p, nil
		case "enter":
			q := strings.TrimSpace(p.searchInput.Value())
			if q == "" {
				return p, func() tea.Msg { return statusToastMsg{text: "Type a package name to search", isErr: true} }
			}
			p.searchInputFocused = false
			p.searchInput.Blur()
			return p.runRepoSearch(backend, q)
		default:
			var cmd tea.Cmd
			p.searchInput, cmd = p.searchInput.Update(msg)
			return p, cmd
		}
	}

	if m := classifyVimMotion(key); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.searchIdx, len(p.filteredSearch), defaultMotionPage, m); changed {
			p.searchIdx = next
			p.previewScroll = 0
			if r, ok := p.selectedSearchRow(); ok && split != SplitOff {
				return p, p.ensurePackageDetailCmd(backend, searchRowToPresetTool(r))
			}
		}
		return p, nil
	}

	switch key {
	case "e":
		p.searchInputFocused = true
		p.searchInput.CursorEnd()
		return p, p.searchInput.Focus()
	case "r":
		if strings.TrimSpace(p.searchQuery) != "" {
			return p.runRepoSearch(backend, p.searchQuery)
		}
	case "i", "enter", "x":
		r, ok := p.selectedSearchRow()
		if !ok {
			return p, func() tea.Msg { return statusToastMsg{text: "Select a search result first"} }
		}
		if p.pkgRunning {
			return p, func() tea.Msg { return statusToastMsg{text: "Package operation already running"} }
		}
		return p.requestSearchRowAction(r, key)
	case "y":
		if r, ok := p.selectedSearchRow(); ok {
			return p, copyCmd(r.Name, "package name")
		}
	case "Y":
		if r, ok := p.selectedSearchRow(); ok && r.InstallCmd != "" {
			return p, copyCmd(r.InstallCmd, "install command")
		}
	}
	return p, nil
}

func (p packagesPanel) requestSearchRowAction(r PackageSearchRow, key string) (ResourcePanel, tea.Cmd) {
	targetFile := nonEmpty(r.SuggestedFile, "pkg/server.txt")
	row := searchRowToPresetTool(r)
	switch key {
	case "a":
		if r.Tracked {
			return p, func() tea.Msg { return statusToastMsg{text: r.Name + " is already tracked"} }
		}
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Track Package in List",
				Body:         fmt.Sprintf("Append %s (%s) to %s (without running installer)", r.Name, r.Provider, targetFile),
				ConfirmLabel: "Track Only",
				OnConfirm: func(b Backend) tea.Cmd {
					return addAndTrackPkgCmd(b, r.Name, r.Provider, targetFile, false)
				},
			}
		}
	case "I":
		cmdTitle := nonEmpty(r.InstallCmd, fmt.Sprintf("ws pkg install --provider %s %s", r.Provider, r.Name))
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Confirm Package Install",
				Body:         fmt.Sprintf("Package: %s (%s)\nCommand: %s\nNote: Does not add to pkg/*.txt (use [i] to also track).", r.Name, r.Provider, cmdTitle),
				ConfirmLabel: "Install",
				StartPanel:   PanelPackages,
				StartMsg:     systemMutationStartMsg{kind: "pkg"},
				OnConfirm: func(b Backend) tea.Cmd {
					return execPackageActionOrFallback(b, "install", []PresetToolRow{row}, cmdTitle)
				},
			}
		}
	case "x":
		cmdTitle := nonEmpty(r.RemoveCmd, fmt.Sprintf("ws pkg remove --provider %s %s", r.Provider, r.Name))
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Confirm Package REMOVE",
				Body:         fmt.Sprintf("Package: %s (%s)\nCommand: %s", r.Name, r.Provider, cmdTitle),
				Warning:      "Removing packages uninstalls them from your system package manager.",
				ConfirmLabel: "REMOVE",
				Dangerous:    true,
				StartPanel:   PanelPackages,
				StartMsg:     systemMutationStartMsg{kind: "pkg"},
				OnConfirm: func(b Backend) tea.Cmd {
					return execPackageActionOrFallback(b, "remove", []PresetToolRow{row}, cmdTitle)
				},
			}
		}
	default:
		cmdTitle := fmt.Sprintf("ws pkg install --provider %s --track --file %s %s", r.Provider, targetFile, r.Name)
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Install & Track Package",
				Body:         fmt.Sprintf("Package: %s (%s)\nTrack In: %s\nCommand: %s", r.Name, r.Provider, targetFile, nonEmpty(r.InstallCmd, r.Name)),
				ConfirmLabel: "Install & Track",
				StartPanel:   PanelPackages,
				StartMsg:     systemMutationStartMsg{kind: "pkg"},
				OnConfirm: func(b Backend) tea.Cmd {
					return execAddAndTrackOrFallback(b, r.Name, r.Provider, targetFile, cmdTitle)
				},
			}
		}
	}
}

func (p packagesPanel) renderSearchMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	prov := p.searchProvider()
	title := fmt.Sprintf("Package Repository Search · %s (%d)", prov, len(p.filteredSearch))

	in := p.searchInput
	in.SetWidth(max(12, innerW-len(prov)-14))
	prompt := fmt.Sprintf("Search [%s]: ", prov)
	searchLine := renderInputField(prompt, in, innerW, p.searchInputFocused, st)
	lines := []string{searchLine, ""}

	bodyH := max(3, innerH-len(lines))
	switch {
	case p.searchLoading && len(p.filteredSearch) == 0:
		lines = append(lines, st.Muted.Render(fmt.Sprintf("Searching %s repositories for %q...", prov, p.searchQuery)))
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	case p.searchErr != "":
		lines = append(lines, st.ErrText.Render(truncate(p.searchErr, innerW)))
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	case len(p.filteredSearch) == 0:
		msg := "Enter a package query to search."
		if strings.TrimSpace(p.searchQuery) != "" {
			msg = fmt.Sprintf("No packages matched %q.", p.searchQuery)
		}
		lines = append(lines, st.Muted.Render(truncate(msg, innerW)))
		return renderTitledBox(title, "", lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredSearch))
	for _, r := range p.filteredSearch {
		stText := st.Muted.Render("avail")
		switch {
		case r.Installed && r.Tracked:
			stText = st.OKText.Render("inst+trk")
		case r.Installed:
			stText = st.OKText.Render("installed")
		case r.Tracked:
			stText = st.InfoText.Render("tracked")
		}
		rows = append(rows, []string{
			r.Name,
			st.InfoText.Render(r.Provider),
			nonEmpty(r.Repo, "—"),
			nonEmpty(r.Version, "—"),
			stText,
			r.Description,
		})
	}
	lines = append(lines, renderDataTable([]string{"Package", "Prov", "Repo", "Version", "Status", "Description"}, rows, p.searchIdx, focused && !p.searchInputFocused, innerW, bodyH, st)...)
	return renderTitledBox(title, "", lines, width, height, focused, st)
}

func (p packagesPanel) renderPackagesMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	pr, ok := p.selectedPreset()
	title := fmt.Sprintf("Packages · %d resolved", len(p.filteredPkgs))
	if ok {
		title = fmt.Sprintf("Resolved Packages · %s (%d)", pr.ID, len(p.filteredPkgs))
	}
	if !p.presetsLoad {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Loading presets.toml...")}, width, height, focused, st)
	}
	if p.presetsErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.presetsErr, innerW))}, width, height, focused, st)
	}
	if p.resolveErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.resolveErr, innerW))}, width, height, focused, st)
	}
	if p.resolvingID != "" && len(p.filteredPkgs) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Resolving preset packages...")}, width, height, focused, st)
	}
	if len(p.filteredPkgs) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No packages matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredPkgs))
	marked := make(map[int]bool, p.pkgSel.Count())
	for i, t := range p.filteredPkgs {
		if p.pkgSel.Has(t.ID) {
			marked[i] = true
		}
		upd := nonEmpty(t.Updates, "manager")
		if t.Updates == "self" {
			upd = st.AccentText.Render("self-update")
		}
		statSt := st.RowNormal
		switch strings.ToLower(t.Status) {
		case "conflict":
			statSt = st.ErrText
		case "outdated", "missing", "untracked":
			statSt = st.WarnText
		case "installed":
			statSt = st.OKText
		}
		origin := "—"
		if t.OriginPreset != "" || t.OriginFile != "" {
			origin = strings.Trim(t.OriginPreset+":"+t.OriginFile, ":")
		} else if t.Source != "" {
			origin = t.Source
		}
		toolCol := t.ID
		if strings.EqualFold(t.Status, "untracked") {
			toolCol = nonEmpty(t.PackageName, t.ID)
		}
		rows = append(rows, []string{
			toolCol,
			st.InfoText.Render(t.Provider),
			nonEmpty(t.PackageName, t.ID),
			upd,
			statSt.Render(nonEmpty(t.Status, "unknown")),
			origin,
		})
	}
	lines := renderSelectableTable([]string{"Tool", "Provider", "Package", "Updates", "Status", "Origin"}, rows, p.pkgIdx, marked, focused, innerW, innerH, st)
	badge := ""
	if p.pkgSel.Count() > 0 {
		badge = fmt.Sprintf("%d marked", p.pkgSel.Count())
	}
	return renderTitledBox(title, badge, lines, width, height, focused, st)
}

func (p packagesPanel) renderCleanupMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	ids, selBytes, totBytes, _, hasDangerous := p.cleanupStats()
	title := fmt.Sprintf("Disk Cleanup (%d targets · %s / %s selected)", len(p.cleanFiltered), formatBytes(selBytes), formatBytes(totBytes))

	modeBadge := st.BadgeInfo.Render("DRY-RUN")
	if !p.cleanDryRun {
		if hasDangerous {
			modeBadge = st.BadgeErr.Render("EXECUTE!")
		} else {
			modeBadge = st.BadgeWarn.Render("EXECUTE")
		}
	}
	ratio := 0.0
	if totBytes > 0 {
		ratio = float64(selBytes) / float64(totBytes)
	}
	lines := []string{
		modeBadge + " " + renderProgressBar(ratio, clamp(innerW-28, 10, 32), st) + " " + st.Muted.Render(fmt.Sprintf("%d marked", len(ids))),
		"",
	}

	if !p.cleanLoaded {
		lines = append(lines, st.Muted.Render("Scanning reclaimable caches..."))
		return renderTitledBox(title, "", lines, width, height, focused, st)
	}
	if p.cleanErr != "" {
		lines = append(lines, st.ErrText.Render(truncate(p.cleanErr, innerW)))
		return renderTitledBox(title, "", lines, width, height, focused, st)
	}
	if len(p.cleanFiltered) == 0 {
		lines = append(lines, st.OKText.Render("No cleanup targets matched."))
		return renderTitledBox(title, "", lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.cleanFiltered))
	marked := make(map[int]bool, p.cleanSelected.Count())
	for i, idx := range p.cleanFiltered {
		it := p.cleanItems[idx]
		if p.cleanSelected.Has(it.ID) {
			marked[i] = true
		}
		tier := strings.ToUpper(normalizeTier(it.Tier))
		switch normalizeTier(it.Tier) {
		case "safe":
			tier = st.OKText.Render(tier)
		case "costly":
			tier = st.WarnText.Render(tier)
		default:
			tier = st.ErrText.Render(tier)
		}
		pathSummary := summarizeCleanupPaths(it.Paths, 2)
		rows = append(rows, []string{
			tier,
			it.Category,
			it.Title,
			formatBytes(it.Bytes),
			pathSummary,
		})
	}
	tableH := max(4, innerH-len(lines))
	lines = append(lines, renderSelectableTable([]string{"Tier", "Category", "Target", "Size", "Paths"}, rows, p.cleanCursor, marked, focused, innerW, tableH, st)...)
	return renderTitledBox(title, "", lines, width, height, focused, st)
}

func summarizeCleanupPaths(paths []string, limit int) string {
	if len(paths) == 0 {
		return "—"
	}
	if limit <= 0 || len(paths) <= limit {
		collapsed := make([]string, len(paths))
		for i, pth := range paths {
			collapsed[i] = collapseHomePath(pth)
		}
		return strings.Join(collapsed, ", ")
	}
	collapsed := make([]string, limit)
	for i := 0; i < limit; i++ {
		collapsed[i] = collapseHomePath(paths[i])
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(collapsed, ", "), len(paths)-limit)
}

func (p packagesPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	if p.isMiseSubView() {
		return p.mise.ViewPreview(width, height, focused, borderless, st)
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
	case pkgSubCleanup:
		return p.renderCleanupPreview(width, height, innerW, innerH, focused, borderless, st)
	default:
		return p.renderPackagePreview(width, height, innerW, innerH, focused, borderless, st)
	}
}

func (p packagesPanel) renderPackagePreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	isSearch := p.subTab == pkgSubSearch
	var (
		t  PresetToolRow
		ok bool
	)
	if isSearch {
		if r, okS := p.selectedSearchRow(); okS {
			t = searchRowToPresetTool(r)
			ok = true
		}
	} else {
		t, ok = p.selectedTool()
	}
	if !ok {
		return renderTitledBox("Package Inspector", "", []string{st.Muted.Render("Select a package to inspect.")}, width, height, focused, st)
	}
	dk := pkgDetailKey(t)
	det, hasDet := p.pkgDetails[dk]

	desc := nonEmpty(det.Description, nonEmpty(t.Description, "No description in catalog."))
	origin := "—"
	if isSearch {
		origin = nonEmpty(det.Repo, nonEmpty(t.Source, t.Provider))
		if t.OriginFile != "" {
			origin += " · target: " + t.OriginFile
		}
	} else if t.OriginPreset != "" || t.OriginFile != "" {
		origin = fmt.Sprintf("preset %s (%s)", nonEmpty(t.OriginPreset, "?"), nonEmpty(t.OriginFile, "?"))
	} else if t.Source != "" {
		origin = t.Source
	}
	verStr := nonEmpty(det.Version, nonEmpty(t.CurrentVersion, t.Status))
	latestVer := nonEmpty(det.LatestVersion, t.LatestVersion)
	if latestVer != "" && latestVer != det.Version && latestVer != t.CurrentVersion {
		verStr += " -> latest " + latestVer
	}
	if det.Installed && !strings.Contains(strings.ToLower(verStr), "installed") {
		verStr = nonEmpty(verStr, "installed")
		if det.Version != "" {
			verStr += " (installed)"
		}
	}
	provs := strings.Join(t.AllProviders, ", ")
	if provs == "" {
		provs = nonEmpty(det.Provider, t.Provider)
	}

	pairs := [][2]string{
		{"Package", st.AccentText.Render(nonEmpty(det.PackageName, nonEmpty(t.PackageName, t.ID)))},
		{"Repo / Source", st.InfoText.Render(origin)},
		{"Provider", provs},
		{"Version/Status", nonEmpty(verStr, "unknown")},
	}
	if det.Homepage != "" {
		pairs = append(pairs, [2]string{"Homepage", det.Homepage})
	}
	if det.License != "" {
		pairs = append(pairs, [2]string{"License", det.License})
	}
	if det.InstalledSize != "" {
		pairs = append(pairs, [2]string{"Size", det.InstalledSize})
	}
	if len(det.Artifacts) > 0 {
		pairs = append(pairs, [2]string{"Artifacts", strings.Join(det.Artifacts, " · ")})
	}
	if len(det.Dependencies) > 0 {
		pairs = append(pairs, [2]string{"Dependencies", strings.Join(det.Dependencies, ", ")})
	}
	if len(det.RequiredBy) > 0 {
		pairs = append(pairs, [2]string{"Required By", strings.Join(det.RequiredBy, ", ")})
	}
	conflicts := det.Conflicts
	if len(conflicts) == 0 {
		conflicts = t.Conflicts
	}
	if len(conflicts) > 0 {
		pairs = append(pairs, [2]string{"Conflicts", st.WarnText.Render(strings.Join(conflicts, ", "))})
	}

	var all []string
	all = append(all, renderKVTable(pairs, innerW, st)...)
	all = append(all, "", st.SectionHeader.Render("Description"))
	for _, ln := range wrapPlainText(desc, innerW) {
		all = append(all, st.RowNormal.Render(ln))
	}
	if len(det.OptionalDeps) > 0 {
		all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Optional Dependencies (%d)", len(det.OptionalDeps))))
		for _, opt := range det.OptionalDeps {
			for _, ln := range wrapPlainText(opt, max(10, innerW-2)) {
				all = append(all, "  "+st.Muted.Render(ln))
			}
		}
	}
	if hasDet && det.Caveats != "" {
		all = append(all, "", st.SectionHeader.Render("Caveats"))
		for _, ln := range strings.Split(det.Caveats, "\n") {
			for _, wln := range wrapPlainText(ln, innerW) {
				all = append(all, st.Muted.Render(wln))
			}
		}
	}
	if det.Homepage == "" && len(det.Dependencies) == 0 && det.RawInfo != "" {
		all = append(all, "", st.SectionHeader.Render("Raw Package Info"))
		for _, ln := range strings.Split(det.RawInfo, "\n") {
			all = append(all, st.Muted.Render(truncate(ln, innerW)))
		}
	}
	all = append(all, "", st.SectionHeader.Render("Commands"))
	if isSearch {
		if t.InstallCmd != "" {
			all = append(all, "  Install & Track: "+st.OKText.Render(truncate(t.InstallCmd+" -> "+nonEmpty(t.OriginFile, "pkg/*.txt"), max(8, innerW-19))))
			all = append(all, "  Install Only:    "+st.OKText.Render(truncate(t.InstallCmd, max(8, innerW-19))))
		}
		all = append(all, "  Track Target:    "+st.InfoText.Render(nonEmpty(t.OriginFile, "pkg/*.txt")))
		if t.RemoveCmd != "" {
			all = append(all, "  Remove:          "+st.ErrText.Render(truncate(t.RemoveCmd, max(8, innerW-19))))
		}
	} else {
		if t.UpdateCmd != "" {
			all = append(all, "  Update:  "+st.OKText.Render(truncate(t.UpdateCmd, max(8, innerW-11))))
		}
		if t.RemoveCmd != "" {
			all = append(all, "  Remove:  "+st.ErrText.Render(truncate(t.RemoveCmd, max(8, innerW-11))))
		}
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Package Inspector · "+t.ID, "", vis, width, height, focused, st)
}

func (p packagesPanel) renderCleanupPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	it, ok := p.selectedCleanItem()
	if !ok {
		return renderTitledBox("Cleanup Detail", "", []string{st.Muted.Render("Select a cleanup target.")}, width, height, focused, st)
	}
	var all []string
	all = append(all, renderKVTable([][2]string{
		{"Title", st.AccentText.Render(it.Title)},
		{"Tier / Cat", strings.ToUpper(normalizeTier(it.Tier)) + " · " + it.Category},
		{"Size", formatBytes(it.Bytes)},
		{"Needs Sudo", fmt.Sprintf("%v", it.NeedsSudo)},
	}, innerW, st)...)
	if it.Warning != "" {
		all = append(all, "")
		for _, wl := range wrapPlainText("WARNING: "+it.Warning, innerW) {
			all = append(all, st.ErrText.Render(wl))
		}
	}
	all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Paths (%d)", len(it.Paths))))
	for _, pth := range it.Paths {
		wrapped := wrapPlainText(collapseHomePath(pth), max(10, innerW-4))
		for i, wl := range wrapped {
			prefix := "    "
			if i == 0 {
				prefix = "  • "
			}
			all = append(all, prefix+st.RowNormal.Render(wl))
		}
	}
	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	badge := ""
	if maxScroll > 0 {
		badge = fmt.Sprintf("scroll %d/%d", sc+1, len(all))
	}
	return renderTitledBox("Cleanup Target · "+it.Category, badge, vis, width, height, focused, st)
}

func (p packagesPanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.isMiseSubView() {
		return p.mise.ViewModal(width, height, st)
	}
	if p.subTab == pkgSubInstalled && p.pkgFilters.Open {
		groups, sorts := p.pkgFacetSpec()
		return p.pkgFilters.ViewModal("Packages", groups, sorts, width, height, st)
	}
	if p.subTab == pkgSubCleanup && p.cleanFilters.Open {
		groups, sorts := p.cleanFacetSpec()
		return p.cleanFilters.ViewModal("Cleanup", groups, sorts, width, height, st)
	}
	if p.pkgAddModalOpen {
		return p.renderPkgAddModal(width, height, st), true
	}
	return "", false
}

func normalizeTier(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "safe":
		return "safe"
	case "costly":
		return "costly"
	case "dangerous":
		return "dangerous"
	default:
		return "costly"
	}
}

func buildCleanupOrder(items []CleanItem) []int {
	order := make([]int, 0, len(items))
	for _, tier := range cleanupTierOrder {
		for i, it := range items {
			if normalizeTier(it.Tier) == tier {
				order = append(order, i)
			}
		}
	}
	return order
}

func loadPresetsCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return presetsLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		list, err := backend.ListPresets()
		return presetsLoadedMsg{presets: list, err: err}
	}
}

func resolvePresetCmd(backend Backend, id string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return presetResolvedMsg{id: id, err: fmt.Errorf("backend unavailable")}
		}
		rows, err := backend.ResolvePreset(id)
		return presetResolvedMsg{id: id, rows: rows, err: err}
	}
}

func loadPackageDetailCmd(backend Backend, key string, pkg PresetToolRow) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgDetailLoadedMsg{key: key, err: fmt.Errorf("backend unavailable")}
		}
		det, err := backend.LoadPackageDetail(pkg)
		return pkgDetailLoadedMsg{key: key, detail: det, err: err}
	}
}

func scanCleanupCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return cleanupScannedMsg{err: fmt.Errorf("backend unavailable")}
		}
		items, err := backend.ScanCleanup()
		return cleanupScannedMsg{items: items, err: err}
	}
}

func loadPkgOutdatedCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgOutdatedLoadedMsg{}
		}
		items, err := backend.LoadPackageOutdated()
		return pkgOutdatedLoadedMsg{items: items, err: err}
	}
}

func (p packagesPanel) openPkgAddModal() packagesPanel {
	p.pkgAddModalOpen = true
	p.pkgAddFocus = 0
	p.pkgAddBtnIdx = 2
	p.pkgAddNameInput.SetValue("")
	if len(p.pkgAddProviders) == 0 {
		p.pkgAddProviders = []string{"brew", "cask", "pacman", "paru", "apt", "dnf", "mise", "gh-ext"}
	}
	if len(p.pkgAddFiles) == 0 {
		p.pkgAddFiles = []string{"pkg/mac-brew.txt", "pkg/mac-cask.txt", "pkg/arch.txt", "pkg/server.txt"}
	}
	defProv := "brew"
	if pr, ok := p.selectedPreset(); ok {
		id := strings.ToLower(pr.ID)
		switch {
		case strings.Contains(id, "arch"), strings.Contains(id, "cachy"):
			defProv = "paru"
		case strings.Contains(id, "ubuntu"), strings.Contains(id, "debian"), strings.Contains(id, "server"):
			defProv = "apt"
		}
	}
	for i, pv := range p.pkgAddProviders {
		if pv == defProv {
			p.pkgAddProviderIdx = i
			break
		}
	}
	p.syncPkgAddFileForProvider()
	return p
}

func (p *packagesPanel) syncPkgAddFileForProvider() {
	if p.pkgAddProviderIdx < 0 || p.pkgAddProviderIdx >= len(p.pkgAddProviders) {
		return
	}
	wantFile := "pkg/server.txt"
	switch p.pkgAddProviders[p.pkgAddProviderIdx] {
	case "brew":
		wantFile = "pkg/mac-brew.txt"
	case "cask":
		wantFile = "pkg/mac-cask.txt"
	case "pacman", "paru":
		wantFile = "pkg/arch.txt"
	}
	for i, f := range p.pkgAddFiles {
		if f == wantFile {
			p.pkgAddFileIdx = i
			return
		}
	}
}

func (p packagesPanel) updatePkgAddModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	if key == "esc" || (key == "q" && p.pkgAddFocus != 0) {
		p.pkgAddModalOpen = false
		p.pkgAddNameInput.Blur()
		return p, func() tea.Msg { return statusToastMsg{text: "Add package cancelled"} }
	}
	switch p.pkgAddFocus {
	case 0:
		switch key {
		case "tab", "down", "enter":
			p.pkgAddNameInput.Blur()
			p.pkgAddFocus = 1
			return p, nil
		default:
			var cmd tea.Cmd
			p.pkgAddNameInput, cmd = p.pkgAddNameInput.Update(msg)
			return p, cmd
		}
	case 1:
		switch key {
		case "left", "h":
			if len(p.pkgAddProviders) > 0 {
				p.pkgAddProviderIdx = (p.pkgAddProviderIdx - 1 + len(p.pkgAddProviders)) % len(p.pkgAddProviders)
				p.syncPkgAddFileForProvider()
			}
		case "right", "l":
			if len(p.pkgAddProviders) > 0 {
				p.pkgAddProviderIdx = (p.pkgAddProviderIdx + 1) % len(p.pkgAddProviders)
				p.syncPkgAddFileForProvider()
			}
		case "tab", "down", "enter":
			p.pkgAddFocus = 2
		case "shift+tab", "up":
			p.pkgAddFocus = 0
			return p, p.pkgAddNameInput.Focus()
		}
		return p, nil
	case 2:
		switch key {
		case "left", "h":
			if len(p.pkgAddFiles) > 0 {
				p.pkgAddFileIdx = (p.pkgAddFileIdx - 1 + len(p.pkgAddFiles)) % len(p.pkgAddFiles)
			}
		case "right", "l":
			if len(p.pkgAddFiles) > 0 {
				p.pkgAddFileIdx = (p.pkgAddFileIdx + 1) % len(p.pkgAddFiles)
			}
		case "tab", "down", "enter":
			p.pkgAddFocus = 3
		case "shift+tab", "up":
			p.pkgAddFocus = 1
		}
		return p, nil
	default:
		switch key {
		case "left", "h":
			p.pkgAddBtnIdx = (p.pkgAddBtnIdx + 2) % 3
		case "right", "l", "tab":
			p.pkgAddBtnIdx = (p.pkgAddBtnIdx + 1) % 3
		case "shift+tab", "up":
			p.pkgAddFocus = 2
		case "enter":
			if p.pkgAddBtnIdx == 0 {
				p.pkgAddModalOpen = false
				p.pkgAddNameInput.Blur()
				return p, func() tea.Msg { return statusToastMsg{text: "Add package cancelled"} }
			}
			name := strings.TrimSpace(p.pkgAddNameInput.Value())
			if name == "" {
				return p, func() tea.Msg { return statusToastMsg{text: "Package name is required", isErr: true} }
			}
			prov := p.pkgAddProviders[clamp(p.pkgAddProviderIdx, 0, max(0, len(p.pkgAddProviders)-1))]
			file := p.pkgAddFiles[clamp(p.pkgAddFileIdx, 0, max(0, len(p.pkgAddFiles)-1))]
			install := p.pkgAddBtnIdx == 2
			p.pkgAddModalOpen = false
			p.pkgAddNameInput.Blur()
			if !install {
				return p, addAndTrackPkgCmd(backend, name, prov, file, false)
			}
			if p.pkgRunning {
				return p, func() tea.Msg { return statusToastMsg{text: "Package operation already running"} }
			}
			p.pkgRunning = true
			cmdTitle := fmt.Sprintf("ws pkg add --provider %s --file %s %s", prov, file, name)
			return p, execAddAndTrackOrFallback(backend, name, prov, file, cmdTitle)
		}
	}
	return p, nil
}

func (p packagesPanel) renderPkgAddModal(width, height int, st Styles) string {
	boxW := min(max(50, width-12), 78)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(18, boxW-st.ModalBox.GetHorizontalFrameSize())

	prov := ""
	if p.pkgAddProviderIdx >= 0 && p.pkgAddProviderIdx < len(p.pkgAddProviders) {
		prov = p.pkgAddProviders[p.pkgAddProviderIdx]
	}
	file := ""
	if p.pkgAddFileIdx >= 0 && p.pkgAddFileIdx < len(p.pkgAddFiles) {
		file = p.pkgAddFiles[p.pkgAddFileIdx]
	}

	provLine := fmt.Sprintf("Provider:    < %s >", prov)
	fileLine := fmt.Sprintf("Target File: < %s >", file)
	if p.pkgAddFocus == 1 {
		provLine = st.AccentText.Render(provLine)
	}
	if p.pkgAddFocus == 2 {
		fileLine = st.AccentText.Render(fileLine)
	}

	cancelBtn := renderModalActionButton("Cancel", p.pkgAddFocus == 3 && p.pkgAddBtnIdx == 0, false, st)
	trackBtn := renderModalActionButton("Track Only", p.pkgAddFocus == 3 && p.pkgAddBtnIdx == 1, false, st)
	installBtn := renderModalActionButton("Install & Track", p.pkgAddFocus == 3 && p.pkgAddBtnIdx == 2, false, st)

	parts := []string{
		st.ModalTitle.Render(truncate("Add & Track Package", innerW)),
		"",
		renderInputField("Package:     ", p.pkgAddNameInput, innerW, p.pkgAddFocus == 0, st),
		provLine,
		fileLine,
		"",
		lipgloss.JoinHorizontal(lipgloss.Center, cancelBtn, "  ", trackBtn, "  ", installBtn),
		"",
		st.Muted.Render(truncate("tab next · left/right switch option · enter submit · esc cancel", innerW)),
	}
	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func loadUntrackedPkgsCmd(backend Backend, presetID string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgUntrackedLoadedMsg{}
		}
		rows, err := backend.LoadUntrackedPackages(presetID)
		return pkgUntrackedLoadedMsg{rows: rows, err: err}
	}
}

func trackPackagesCmd(backend Backend, relFile string, pkgs []PresetToolRow) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgListMutatedMsg{err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.TrackPackages(relFile, pkgs)
		return pkgListMutatedMsg{summary: summary, err: err}
	}
}

func untrackPackagesCmd(backend Backend, pkgs []PresetToolRow) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgListMutatedMsg{err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.UntrackPackages(pkgs)
		return pkgListMutatedMsg{summary: summary, err: err}
	}
}

func addAndTrackPkgCmd(backend Backend, name, provider, relFile string, install bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgListMutatedMsg{err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.AddAndTrackPackage(name, provider, relFile, install)
		summary := fmt.Sprintf("Added %s to %s", name, relFile)
		if strings.TrimSpace(res.Output) != "" {
			summary = strings.TrimSpace(strings.Split(res.Output, "\n")[0])
		}
		return pkgListMutatedMsg{summary: summary, err: err}
	}
}

func previewUpdateAllCmd(backend Backend, all bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return updateAllPreviewReadyMsg{all: all, err: fmt.Errorf("backend unavailable")}
		}
		prev, err := backend.PreviewUpdateAll(all)
		return updateAllPreviewReadyMsg{preview: prev, all: all, err: err}
	}
}

func execUpdateAllOrFallback(b Backend, all bool) tea.Cmd {
	cmdTitle := "ws update"
	if all {
		cmdTitle += " --all"
	}
	if b == nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: "update-all", command: cmdTitle, err: fmt.Errorf("backend unavailable")}
		}
	}
	execCmd, err := b.UpdateAllExecCmd(all)
	if err != nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: "update-all", command: cmdTitle, err: err}
		}
	}
	if execCmd != nil {
		return tea.ExecProcess(execCmd, func(err error) tea.Msg {
			return pkgTTYFinishedMsg{action: "update-all", command: cmdTitle, err: err}
		})
	}
	actID := newActivityID()
	return tea.Batch(
		func() tea.Msg {
			return commandRunRecordedMsg{
				OpenOutput: true,
				Run:        CommandRun{ID: actID, Command: cmdTitle, Running: true},
			}
		},
		func() tea.Msg {
			res, err := b.ExecUpdateAll(all)
			if res.Command == "" {
				res.Command = cmdTitle
			}
			return pkgActionDoneMsg{runID: actID, action: "update-all", result: res, err: err}
		},
	)
}

func execPackageActionOrFallback(b Backend, action string, targets []PresetToolRow, cmdTitle string) tea.Cmd {
	if b == nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: action, command: cmdTitle, err: fmt.Errorf("backend unavailable")}
		}
	}
	execCmd, err := b.PackageActionExecCmd(action, targets)
	if err != nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: action, command: cmdTitle, err: err}
		}
	}
	if execCmd != nil {
		return tea.ExecProcess(execCmd, func(err error) tea.Msg {
			return pkgTTYFinishedMsg{action: action, command: cmdTitle, err: err}
		})
	}
	actID := newActivityID()
	return tea.Batch(
		func() tea.Msg {
			return commandRunRecordedMsg{
				OpenOutput: true,
				Run: CommandRun{
					ID:        actID,
					Command:   cmdTitle,
					Running:   true,
					Dangerous: action == "remove",
				},
			}
		},
		func() tea.Msg {
			res, err := b.ExecPackageAction(action, targets)
			if res.Command == "" {
				res.Command = cmdTitle
			}
			return pkgActionDoneMsg{runID: actID, action: action, result: res, err: err}
		},
	)
}

func execAddAndTrackOrFallback(b Backend, name, provider, relFile, cmdTitle string) tea.Cmd {
	if b == nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: "add", command: cmdTitle, err: fmt.Errorf("backend unavailable")}
		}
	}
	execCmd, err := b.AddAndTrackPackageExecCmd(name, provider, relFile, true)
	if err != nil {
		return func() tea.Msg {
			return pkgTTYFinishedMsg{action: "add", command: cmdTitle, err: err}
		}
	}
	if execCmd != nil {
		return tea.ExecProcess(execCmd, func(err error) tea.Msg {
			return pkgTTYFinishedMsg{action: "add", command: cmdTitle, err: err}
		})
	}
	actID := newActivityID()
	return tea.Batch(
		func() tea.Msg {
			return commandRunRecordedMsg{
				OpenOutput: true,
				Run:        CommandRun{ID: actID, Command: cmdTitle, Running: true},
			}
		},
		func() tea.Msg {
			res, err := b.AddAndTrackPackage(name, provider, relFile, true)
			if res.Command == "" {
				res.Command = cmdTitle
			}
			return pkgActionDoneMsg{runID: actID, action: "add", result: res, err: err}
		},
	)
}

func searchPackagesCmd(backend Backend, seq int, query, provider string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return pkgSearchResultsMsg{seq: seq, query: query, provider: provider, err: fmt.Errorf("backend unavailable")}
		}
		rows, err := backend.SearchPackages(query, provider, 80)
		return pkgSearchResultsMsg{seq: seq, query: query, provider: provider, rows: rows, err: err}
	}
}
