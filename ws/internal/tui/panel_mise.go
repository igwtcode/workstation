package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

const (
	miseSubConfigs = iota
	miseSubTools
	miseSubCount
)

type misePanel struct {
	subView         int
	zone            string
	inv             MiseInventoryView
	filteredConfigs []MiseConfigRow
	filteredTools   []MiseToolRow
	filterQuery     string
	filters         FacetFilterState

	cfgSel        SelectionSet
	toolSel       SelectionSet
	cfgCursor     int
	toolCursor    int
	previewScroll int
	drillOpen     bool

	loaded          bool
	loading         bool
	checkingOut     bool
	upgrading       bool
	loadErr         string
	loadActID       int
	outdatedActID   int
	upgradeActID    int
	lastOutdatedRun string
}

func newMisePanel() misePanel {
	return misePanel{loading: true}
}

func (p misePanel) ID() string         { return "mise" }
func (p misePanel) Title() string      { return "Mise Manager" }
func (p misePanel) ShortTitle() string { return "Mise" }
func (p misePanel) SubViews() []string { return []string{"Configs", "Tools"} }
func (p misePanel) ActiveSubView() int { return p.subView }
func (p misePanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < miseSubCount {
		p.subView = idx
		p.drillOpen = false
		p.previewScroll = 0
		p.filters.Close()
		p.recomputeFiltered()
	}
	return p
}

func (p misePanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	if !p.inv.OutdatedKnown && backend != nil && !p.checkingOut {
		p.checkingOut = true
		p.outdatedActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.outdatedActID, "Checking mise outdated tools"),
			loadMiseInventoryCmd(backend, p.zone, true, false),
		)
	}
	return p, nil
}

func (p misePanel) SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd) {
	p.zone = zone
	p.loading = true
	p.loadErr = ""
	p.cfgCursor = 0
	p.toolCursor = 0
	p.previewScroll = 0
	p.cfgSel = p.cfgSel.Clear()
	p.toolSel = p.toolSel.Clear()
	p.drillOpen = false
	if backend == nil {
		return p, nil
	}
	p.loadActID = newActivityID()
	p.checkingOut = true
	p.outdatedActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.loadActID, "Scanning mise configs"),
		startActivityCmd(p.outdatedActID, "Checking mise outdated tools"),
		loadMiseInventoryCmd(backend, zone, false, false),
		loadMiseInventoryCmd(backend, zone, true, false),
	)
}

func (p misePanel) IsZoned() bool       { return true }
func (p misePanel) FilterQuery() string { return p.filterQuery }
func (p misePanel) FilterTarget() string {
	if p.subView == miseSubTools {
		return "Mise Tools"
	}
	return "Mise Configs"
}

func (p misePanel) SetFilterQuery(q string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.cfgCursor = 0
	p.toolCursor = 0
	p.previewScroll = 0
	p.recomputeFiltered()
	return p, nil
}

func (p misePanel) OnBlur() ResourcePanel {
	p.filters.Close()
	return p
}

func (p misePanel) Attention() AttentionLevel {
	if p.loadErr != "" {
		return AttentionErr
	}
	if p.inv.MissingCount > 0 || p.inv.OutdatedCount > 0 {
		return AttentionWarn
	}
	return AttentionNone
}

func (p misePanel) PaneStatus() string {
	switch {
	case p.upgrading:
		return "upgrading mise tools..."
	case p.checkingOut:
		return "checking outdated..."
	case p.loading && !p.loaded:
		return "scanning mise..."
	default:
		return ""
	}
}

func (p misePanel) Badge() string {
	if !p.loaded {
		return "…"
	}
	if p.subView == miseSubTools {
		if p.inv.OutdatedCount > 0 {
			return fmt.Sprintf("%d tools · %d out", len(p.filteredTools), p.inv.OutdatedCount)
		}
		return fmt.Sprintf("%d tools", len(p.filteredTools))
	}
	if p.inv.OutdatedCount > 0 {
		return fmt.Sprintf("%d cfgs · %d out", len(p.filteredConfigs), p.inv.OutdatedCount)
	}
	return fmt.Sprintf("%d cfgs", len(p.filteredConfigs))
}

func (p misePanel) Breadcrumbs() []string {
	sub := "Configs"
	count := len(p.filteredConfigs)
	if p.subView == miseSubTools {
		sub = "Tools"
		count = len(p.filteredTools)
	}
	if p.drillOpen {
		if p.subView == miseSubConfigs {
			if cfg, ok := p.selectedConfig(); ok {
				return []string{"Mise", sub, nonEmpty(cfg.ProjectName, cfg.Kind)}
			}
		} else if t, ok := p.selectedTool(); ok {
			return []string{"Mise", sub, t.Name}
		}
	}
	return []string{"Mise", fmt.Sprintf("%s (%d)", sub, count)}
}

func (p misePanel) StatusChips() []string {
	var chips []string
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	chips = append(chips, p.filters.Chips()...)
	if p.inv.MiseVersion != "" {
		chips = append(chips, "mise:"+p.inv.MiseVersion)
	}
	return chips
}

func (p misePanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.subView == miseSubTools {
		return [][2]string{
			{"enter", "Tool detail"},
			{"u", "Upgrade outdated"},
			{"e", "Edit config"},
			{"y/Y", "Copy path / cmd"},
			{"r", "Refresh"},
			{"a", "Actions"},
			{"F/S", "Filter / Sort"},
			{"space/*", "Mark tools"},
		}
	}
	return [][2]string{
		{"enter", "Config detail"},
		{"e", "Edit config"},
		{"u", "Upgrade outdated"},
		{"y/Y", "Copy path / dir"},
		{"r", "Refresh"},
		{"a", "Actions"},
		{"F/S", "Filter / Sort"},
		{"space/*", "Mark configs"},
	}
}

func (p misePanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	return []RowAction{
		{Key: "e", Label: "Edit mise config", Description: "Open selected mise config in $EDITOR"},
		{Key: "u", Label: "Upgrade outdated", Description: "Upgrade selected or outdated mise tools"},
		{Key: "y", Label: "Copy config path", Description: "Copy mise config file path"},
		{Key: "r", Label: "Refresh mise", Description: "Rescan mise configs and outdated versions"},
	}
}

func (p misePanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch actionKey {
	case "e":
		return p, p.launchEditCmd(backend)
	case "u":
		return p.requestUpgradeConfirm(backend)
	case "y":
		if p.subView == miseSubTools {
			if t, ok := p.selectedTool(); ok && t.SourcePath != "" {
				return p, copyCmd(t.SourcePath, "mise config path")
			}
		} else if cfg, ok := p.selectedConfig(); ok && cfg.Path != "" {
			return p, copyCmd(cfg.Path, "mise config path")
		}
	case "r":
		p.checkingOut = true
		p.outdatedActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.outdatedActID, "Refreshing mise configs & outdated tools"),
			loadMiseInventoryCmd(backend, p.zone, true, true),
		)
	}
	return p, nil
}

func (p misePanel) SelectedCount() int {
	if p.subView == miseSubTools {
		return p.toolSel.Count()
	}
	return p.cfgSel.Count()
}

func (p misePanel) ClearSelection() ResourcePanel {
	p.cfgSel = p.cfgSel.Clear()
	p.toolSel = p.toolSel.Clear()
	return p
}

func (p misePanel) SupportsPreview() bool { return !p.drillOpen }
func (p misePanel) DefaultSplit(width int) SplitMode {
	if width >= 104 {
		return SplitVertical
	}
	return SplitHorizontal
}
func (p misePanel) CanPop() bool { return p.drillOpen }
func (p misePanel) Pop() ResourcePanel {
	p.drillOpen = false
	p.previewScroll = 0
	return p
}
func (p misePanel) CapturingInput() bool { return p.filters.Open }
func (p misePanel) HasOpenModal() bool   { return p.filters.Open }
func (p misePanel) MutationInFlight() bool {
	return p.upgrading
}

func (p misePanel) CLIEquivalent() string {
	if p.subView == miseSubTools {
		if p.filters.Matches("status", "outdated") && p.filters.Get("status") != "" {
			return "ws mise outdated"
		}
		return "ws mise list"
	}
	if cfg, ok := p.selectedConfig(); ok {
		return "ws mise edit " + collapseHomePath(cfg.Path)
	}
	return "ws mise configs"
}

func miseToolSelKey(t MiseToolRow) string {
	return t.Name + "\x00" + filepath.Clean(t.SourcePath)
}

func (p misePanel) selectedConfig() (MiseConfigRow, bool) {
	if len(p.filteredConfigs) == 0 || p.cfgCursor < 0 || p.cfgCursor >= len(p.filteredConfigs) {
		return MiseConfigRow{}, false
	}
	return p.filteredConfigs[p.cfgCursor], true
}

func (p misePanel) selectedTool() (MiseToolRow, bool) {
	if len(p.filteredTools) == 0 || p.toolCursor < 0 || p.toolCursor >= len(p.filteredTools) {
		return MiseToolRow{}, false
	}
	return p.filteredTools[p.toolCursor], true
}

func (p misePanel) activeFacetSpec() ([]FacetGroup, []SortOption) {
	if p.subView == miseSubTools {
		countStatus := map[string]int{}
		countScope := map[string]int{}
		countForge := map[string]int{}
		for _, t := range p.inv.Tools {
			countStatus[strings.ToLower(t.Status)]++
			countScope[strings.ToLower(t.Scope)]++
			fg := nonEmpty(strings.ToLower(t.Forge), "local")
			countForge[fg]++
		}
		groups := []FacetGroup{
			{
				Key:   "status",
				Label: "Status",
				Options: []FacetOption{
					{Value: "outdated", Label: "Outdated", Count: countStatus["outdated"]},
					{Value: "missing", Label: "Missing", Count: countStatus["missing"]},
					{Value: "installed", Label: "Installed", Count: countStatus["installed"]},
				},
			},
			{
				Key:   "scope",
				Label: "Scope",
				Options: []FacetOption{
					{Value: "project", Label: "Project", Count: countScope["project"]},
					{Value: "zone", Label: "Zone", Count: countScope["zone"]},
					{Value: "global", Label: "Global", Count: countScope["global"]},
				},
			},
			{
				Key:   "forge",
				Label: "Forge",
				Options: []FacetOption{
					{Value: "github", Label: "GitHub", Count: countForge["github"]},
					{Value: "codecommit", Label: "CodeCommit", Count: countForge["codecommit"]},
					{Value: "local", Label: "Local / Other", Count: countForge["local"] + countForge["other"]},
				},
			},
		}
		sorts := []SortOption{
			{Key: "status", Label: "Status (Outdated / Missing first)"},
			{Key: "name", Label: "Tool Name (A–Z)"},
			{Key: "project", Label: "Project / Scope"},
		}
		return groups, sorts
	}

	countScope := map[string]int{}
	countForge := map[string]int{}
	outCnt, missCnt, localCnt := 0, 0, 0
	for _, c := range p.inv.Configs {
		countScope[strings.ToLower(c.Scope)]++
		fg := nonEmpty(strings.ToLower(c.Forge), "local")
		countForge[fg]++
		if c.OutdatedCount > 0 {
			outCnt++
		}
		if c.MissingCount > 0 {
			missCnt++
		}
		if c.LocalOverride {
			localCnt++
		}
	}
	groups := []FacetGroup{
		{
			Key:   "scope",
			Label: "Scope",
			Options: []FacetOption{
				{Value: "project", Label: "Project", Count: countScope["project"]},
				{Value: "zone", Label: "Zone", Count: countScope["zone"]},
				{Value: "global", Label: "Global", Count: countScope["global"]},
			},
		},
		{
			Key:   "forge",
			Label: "Forge",
			Options: []FacetOption{
				{Value: "github", Label: "GitHub", Count: countForge["github"]},
				{Value: "codecommit", Label: "CodeCommit", Count: countForge["codecommit"]},
				{Value: "local", Label: "Local / Other", Count: countForge["local"] + countForge["other"]},
			},
		},
		{
			Key:   "health",
			Label: "Health / Type",
			Options: []FacetOption{
				{Value: "outdated", Label: "Has outdated", Count: outCnt},
				{Value: "missing", Label: "Has missing", Count: missCnt},
				{Value: "local", Label: "*.local override", Count: localCnt},
			},
		},
	}
	sorts := []SortOption{
		{Key: "scope", Label: "Scope (Global -> Zone -> Project)"},
		{Key: "name", Label: "Project / Path"},
		{Key: "outdated", Label: "Most Outdated First"},
		{Key: "tools", Label: "Tool Count"},
	}
	return groups, sorts
}

func (p *misePanel) recomputeFiltered() {

	cfgs := fuzzy.Filter(p.inv.Configs, p.filterQuery, func(c MiseConfigRow) ([]string, map[string]string) {
		var toolNames []string
		for _, td := range c.Tools {
			toolNames = append(toolNames, td.Name)
		}
		fields := []string{c.ProjectName, c.Path, c.Kind, c.Zone, c.Forge, c.GitHubSlug, strings.Join(toolNames, " ")}
		meta := map[string]string{
			"scope": c.Scope,
			"zone":  c.Zone,
			"forge": c.Forge,
			"kind":  c.Kind,
		}
		return fields, meta
	})
	var keptCfgs []MiseConfigRow
	for _, c := range cfgs {
		if !p.filters.Matches("scope", c.Scope) {
			continue
		}
		fg := nonEmpty(strings.ToLower(c.Forge), "local")
		if wantFg := p.filters.Get("forge"); wantFg != "" {
			if wantFg == "local" {
				if fg != "local" && fg != "other" {
					continue
				}
			} else if !strings.EqualFold(fg, wantFg) {
				continue
			}
		}
		switch p.filters.Get("health") {
		case "outdated":
			if c.OutdatedCount == 0 {
				continue
			}
		case "missing":
			if c.MissingCount == 0 {
				continue
			}
		case "local":
			if !c.LocalOverride {
				continue
			}
		}
		keptCfgs = append(keptCfgs, c)
	}
	sort.SliceStable(keptCfgs, func(i, j int) bool {
		var cmp int
		switch p.filters.SortKey {
		case "name":
			cmp = strings.Compare(strings.ToLower(nonEmpty(keptCfgs[i].ProjectName, keptCfgs[i].Path)), strings.ToLower(nonEmpty(keptCfgs[j].ProjectName, keptCfgs[j].Path)))
		case "outdated":
			cmp = keptCfgs[j].OutdatedCount - keptCfgs[i].OutdatedCount
		case "tools":
			cmp = len(keptCfgs[j].Tools) - len(keptCfgs[i].Tools)
		default:
			cmp = miseScopeOrder(keptCfgs[i].Scope) - miseScopeOrder(keptCfgs[j].Scope)
			if cmp == 0 {
				cmp = strings.Compare(keptCfgs[i].Path, keptCfgs[j].Path)
			}
		}
		if p.filters.SortDesc {
			return cmp > 0
		}
		return cmp < 0
	})
	p.filteredConfigs = keptCfgs
	if p.cfgCursor >= len(p.filteredConfigs) {
		p.cfgCursor = max(0, len(p.filteredConfigs)-1)
	}

	tools := fuzzy.Filter(p.inv.Tools, p.filterQuery, func(t MiseToolRow) ([]string, map[string]string) {
		fields := []string{t.Name, t.Requested, t.Version, t.Latest, t.ProjectName, t.SourcePath, t.Zone, t.Forge}
		meta := map[string]string{
			"status": t.Status,
			"scope":  t.Scope,
			"zone":   t.Zone,
			"forge":  t.Forge,
		}
		return fields, meta
	})
	var keptTools []MiseToolRow
	for _, t := range tools {
		if !p.filters.Matches("status", t.Status) {
			continue
		}
		if !p.filters.Matches("scope", t.Scope) {
			continue
		}
		fg := nonEmpty(strings.ToLower(t.Forge), "local")
		if wantFg := p.filters.Get("forge"); wantFg != "" {
			if wantFg == "local" {
				if fg != "local" && fg != "other" {
					continue
				}
			} else if !strings.EqualFold(fg, wantFg) {
				continue
			}
		}
		keptTools = append(keptTools, t)
	}
	sort.SliceStable(keptTools, func(i, j int) bool {
		var cmp int
		switch p.filters.SortKey {
		case "name":
			cmp = strings.Compare(strings.ToLower(keptTools[i].Name), strings.ToLower(keptTools[j].Name))
		case "project":
			cmp = strings.Compare(strings.ToLower(nonEmpty(keptTools[i].ProjectName, keptTools[i].Scope)), strings.ToLower(nonEmpty(keptTools[j].ProjectName, keptTools[j].Scope)))
		default:
			cmp = miseStatusOrder(keptTools[i].Status) - miseStatusOrder(keptTools[j].Status)
			if cmp == 0 {
				cmp = strings.Compare(strings.ToLower(keptTools[i].Name), strings.ToLower(keptTools[j].Name))
			}
		}
		if p.filters.SortDesc {
			return cmp > 0
		}
		return cmp < 0
	})
	p.filteredTools = keptTools
	if p.toolCursor >= len(p.filteredTools) {
		p.toolCursor = max(0, len(p.filteredTools)-1)
	}
}

func miseScopeOrder(s string) int {
	switch s {
	case "global":
		return 0
	case "zone":
		return 1
	default:
		return 2
	}
}

func miseStatusOrder(s string) int {
	switch s {
	case "outdated":
		return 0
	case "missing":
		return 1
	default:
		return 2
	}
}

func (p misePanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case miseMutationStartMsg:
		if msg.kind == "upgrade" {
			p.upgrading = true
		}
		return p, nil

	case miseLoadedMsg:
		p.loaded = true
		p.loading = false
		var cmds []tea.Cmd
		if msg.checkedOutdated {
			p.checkingOut = false
			if p.outdatedActID != 0 {
				cmds = append(cmds, finishActivityCmd(p.outdatedActID, "Checking mise outdated tools", msg.err != nil))
				p.outdatedActID = 0
			}
		} else {
			if p.loadActID != 0 {
				cmds = append(cmds, finishActivityCmd(p.loadActID, "Scanning mise configs", msg.err != nil))
				p.loadActID = 0
			}
		}
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Mise scan failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.loadErr = ""
		if msg.checkedOutdated || !p.inv.OutdatedKnown {
			p.inv = msg.inv
			p.recomputeFiltered()
		}
		return p, batchNonNil(cmds...)

	case miseEditFinishedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: "Edit failed: " + msg.err.Error(), isErr: true}
			}
		}
		p.checkingOut = true
		p.outdatedActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg {
				return statusToastMsg{text: "Saved " + collapseHomePath(msg.path) + " — refreshing mise"}
			},
			startActivityCmd(p.outdatedActID, "Checking mise outdated tools"),
			loadMiseInventoryCmd(backend, p.zone, true, true),
		)

	case miseUpgradedMsg:
		p.upgrading = false
		var cmds []tea.Cmd
		if msg.runID != 0 {
			cmds = append(cmds, finishActivityCmd(msg.runID, "Upgrading mise tools", msg.err != nil))
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Mise upgrade failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.toolSel = p.toolSel.Clear()
		p.checkingOut = true
		p.outdatedActID = newActivityID()
		cmds = append(cmds,
			func() tea.Msg { return statusToastMsg{text: "Mise tools upgraded"} },
			startActivityCmd(p.outdatedActID, "Checking mise outdated tools"),
			loadMiseInventoryCmd(backend, p.zone, true, true),
		)
		return p, batchNonNil(cmds...)

	case tea.KeyPressMsg:
		if p.filters.Open {
			groups, sorts := p.activeFacetSpec()
			if handled, changed := p.filters.Update(msg, groups, sorts); handled {
				if changed {
					p.recomputeFiltered()
				}
				return p, nil
			}
		}

		switch msg.String() {
		case "F":
			groups, sorts := p.activeFacetSpec()
			p.filters.OpenFilters(groups, sorts)
			return p, nil
		case "S":
			groups, sorts := p.activeFacetSpec()
			p.filters.OpenSort(groups, sorts)
			return p, nil
		}

		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview || p.drillOpen {
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
				return p, nil
			}
			if p.subView == miseSubTools {
				if next, changed := applyListMotion(p.toolCursor, len(p.filteredTools), defaultMotionPage, m); changed {
					p.toolCursor = next
					p.previewScroll = 0
				}
			} else {
				if next, changed := applyListMotion(p.cfgCursor, len(p.filteredConfigs), defaultMotionPage, m); changed {
					p.cfgCursor = next
					p.previewScroll = 0
				}
			}
			return p, nil
		}

		if isSpaceKey(msg) {
			if p.subView == miseSubTools {
				if t, ok := p.selectedTool(); ok {
					p.toolSel = p.toolSel.Toggle(miseToolSelKey(t))
				}
			} else if cfg, ok := p.selectedConfig(); ok {
				p.cfgSel = p.cfgSel.Toggle(filepath.Clean(cfg.Path))
			}
			return p, nil
		}

		switch msg.String() {
		case "*":
			if p.subView == miseSubTools {
				keys := make([]string, len(p.filteredTools))
				for i, t := range p.filteredTools {
					keys[i] = miseToolSelKey(t)
				}
				p.toolSel = p.toolSel.ToggleAll(keys)
			} else {
				keys := make([]string, len(p.filteredConfigs))
				for i, c := range p.filteredConfigs {
					keys[i] = filepath.Clean(c.Path)
				}
				p.cfgSel = p.cfgSel.ToggleAll(keys)
			}
		case `\`:
			p.cfgSel = p.cfgSel.Clear()
			p.toolSel = p.toolSel.Clear()
			if p.filters.HasActive() {
				p.filters.Clear()
				p.recomputeFiltered()
			}
		case "enter":
			p.drillOpen = !p.drillOpen
			p.previewScroll = 0
		case "e":
			return p, p.launchEditCmd(backend)
		case "u":
			return p.requestUpgradeConfirm(backend)
		case "r":
			p.checkingOut = true
			p.outdatedActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.outdatedActID, "Refreshing mise configs & outdated tools"),
				loadMiseInventoryCmd(backend, p.zone, true, true),
			)
		case "y":
			if p.subView == miseSubTools {
				if t, ok := p.selectedTool(); ok && t.SourcePath != "" {
					return p, copyCmd(t.SourcePath, "mise config path")
				}
			} else if cfg, ok := p.selectedConfig(); ok && cfg.Path != "" {
				return p, copyCmd(cfg.Path, "mise config path")
			}
		case "Y":
			if p.subView == miseSubTools {
				if t, ok := p.selectedTool(); ok {
					return p, copyCmd("mise upgrade "+t.Name, "mise upgrade cmd")
				}
			} else if cfg, ok := p.selectedConfig(); ok && cfg.ProjectPath != "" {
				return p, copyCmd(cfg.ProjectPath, "project path")
			}
		}
	}
	return p, nil
}

func (p misePanel) launchEditCmd(backend Backend) tea.Cmd {
	if backend == nil {
		return func() tea.Msg { return statusToastMsg{text: "Backend unavailable", isErr: true} }
	}
	target := ""
	if p.subView == miseSubTools {
		if t, ok := p.selectedTool(); ok {
			target = t.SourcePath
		}
	} else if c, ok := p.selectedConfig(); ok {
		target = c.Path
	}
	if target == "" {
		return func() tea.Msg { return statusToastMsg{text: "No mise config selected"} }
	}
	cmd, err := backend.EditMiseFileCmd(target)
	if err != nil {
		return func() tea.Msg { return statusToastMsg{text: "Edit unavailable: " + err.Error(), isErr: true} }
	}
	if cmd == nil {
		return func() tea.Msg { return statusToastMsg{text: "No editor configured", isErr: true} }
	}
	cleanPath := target
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return miseEditFinishedMsg{path: cleanPath, err: err}
	})
}

func (p misePanel) requestUpgradeConfirm(backend Backend) (ResourcePanel, tea.Cmd) {
	if p.upgrading {
		return p, func() tea.Msg { return statusToastMsg{text: "Mise upgrade already in progress"} }
	}
	var targets []MiseToolRow
	if p.subView == miseSubTools {
		if p.toolSel.Count() > 0 {
			for _, t := range p.filteredTools {
				if p.toolSel.Has(miseToolSelKey(t)) {
					targets = append(targets, t)
				}
			}
		} else if t, ok := p.selectedTool(); ok {
			targets = append(targets, t)
		}
	} else {
		selectedPaths := map[string]bool{}
		if p.cfgSel.Count() > 0 {
			for _, c := range p.filteredConfigs {
				if p.cfgSel.Has(filepath.Clean(c.Path)) {
					selectedPaths[filepath.Clean(c.Path)] = true
				}
			}
		} else if c, ok := p.selectedConfig(); ok {
			selectedPaths[filepath.Clean(c.Path)] = true
		}
		for _, t := range p.inv.Tools {
			if selectedPaths[filepath.Clean(t.SourcePath)] && (t.Outdated || !t.Installed) {
				targets = append(targets, t)
			}
		}
	}
	if len(targets) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "No outdated or missing mise tools selected"} }
	}

	var names []string
	for i, t := range targets {
		if i >= 6 {
			names = append(names, fmt.Sprintf("+%d more", len(targets)-i))
			break
		}
		names = append(names, fmt.Sprintf("%s (%s -> %s)", t.Name, nonEmpty(t.Version, "missing"), nonEmpty(t.Latest, "latest")))
	}
	body := fmt.Sprintf("Action: Upgrade %d mise tool(s)\nTargets: %s\nCommand: mise upgrade ...", len(targets), strings.Join(names, ", "))
	targetsCopy := append([]MiseToolRow(nil), targets...)
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Confirm Mise Tool Upgrade",
			Body:         body,
			ConfirmLabel: fmt.Sprintf("Upgrade %d Tool(s)", len(targetsCopy)),
			Dangerous:    false,
			StartPanel:   PanelPackages,
			StartMsg:     miseMutationStartMsg{kind: "upgrade"},
			OnConfirm: func(b Backend) tea.Cmd {
				actID := newActivityID()
				cmdTitle := fmt.Sprintf("mise upgrade (%d tool(s))", len(targetsCopy))
				return tea.Batch(
					func() tea.Msg {
						return commandRunRecordedMsg{
							OpenOutput: true,
							Run: CommandRun{
								ID:      actID,
								Command: cmdTitle,
								Running: true,
							},
						}
					},
					func() tea.Msg {
						if b == nil {
							return miseUpgradedMsg{
								runID:  actID,
								result: CommandRunResult{Command: cmdTitle, ExitCode: 1, IsErr: true},
								err:    fmt.Errorf("backend unavailable"),
							}
						}
						res, err := b.UpgradeMiseTools(targetsCopy)
						if res.Command == "" {
							res.Command = cmdTitle
						}
						return miseUpgradedMsg{runID: actID, result: res, err: err}
					},
				)
			},
		}
	}
}

func (p misePanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.drillOpen {
		return p.ViewPreview(width, height, focused, false, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	if p.subView == miseSubTools {
		return p.renderToolsMain(width, height, innerW, innerH, focused, st)
	}
	return p.renderConfigsMain(width, height, innerW, innerH, focused, st)
}

func (p misePanel) renderConfigsMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Mise Configs (%d)", len(p.filteredConfigs))
	if !p.loaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Scanning mise.toml & .tool-versions...")}, width, height, focused, st)
	}
	if p.loadErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.loadErr, innerW))}, width, height, focused, st)
	}
	if len(p.filteredConfigs) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No mise config files matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredConfigs))
	marked := make(map[int]bool, p.cfgSel.Count())
	for i, c := range p.filteredConfigs {
		if p.cfgSel.Has(filepath.Clean(c.Path)) {
			marked[i] = true
		}
		scopeSt := st.InfoText
		if c.Scope == "global" {
			scopeSt = st.AccentText
		}
		forgeCell := nonEmpty(c.Forge, "local")
		if c.Forge == "github" {
			forgeCell = st.OKText.Render("github")
		} else if c.Forge == "codecommit" {
			forgeCell = st.WarnText.Render("codecommit")
		}
		statusCell := st.OKText.Render("ok")
		if c.OutdatedCount > 0 {
			statusCell = st.WarnText.Render(fmt.Sprintf("%d out", c.OutdatedCount))
		} else if c.MissingCount > 0 {
			statusCell = st.ErrText.Render(fmt.Sprintf("%d miss", c.MissingCount))
		}
		kindLbl := c.Kind
		if c.LocalOverride {
			kindLbl += " *"
		}
		row := []string{
			scopeSt.Render(c.Scope),
			nonEmpty(c.ProjectName, "global"),
			kindLbl,
			forgeCell,
			fmt.Sprintf("%d", len(c.Tools)),
			statusCell,
		}
		if innerW >= 92 {
			row = append(row, collapseHomePath(c.Path))
		}
		rows = append(rows, row)
	}
	cols := []string{"Scope", "Project", "File", "Forge", "Tools", "Status"}
	if innerW >= 92 {
		cols = append(cols, "Path")
	}
	lines := renderSelectableTable(cols, rows, p.cfgCursor, marked, focused, innerW, innerH, st)
	badge := ""
	if p.cfgSel.Count() > 0 {
		badge = fmt.Sprintf("%d marked", p.cfgSel.Count())
	}
	return renderTitledBox(title, badge, lines, width, height, focused, st)
}

func (p misePanel) renderToolsMain(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Mise Packages (%d)", len(p.filteredTools))
	if !p.loaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Loading installed mise packages...")}, width, height, focused, st)
	}
	if p.loadErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.loadErr, innerW))}, width, height, focused, st)
	}
	if len(p.filteredTools) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No mise packages matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredTools))
	marked := make(map[int]bool, p.toolSel.Count())
	for i, t := range p.filteredTools {
		if p.toolSel.Has(miseToolSelKey(t)) {
			marked[i] = true
		}
		statSt := st.OKText
		switch strings.ToLower(t.Status) {
		case "outdated":
			statSt = st.WarnText
		case "missing":
			statSt = st.ErrText
		}
		row := []string{
			t.Name,
			nonEmpty(t.Version, "—"),
			nonEmpty(t.Latest, "—"),
			statSt.Render(t.Status),
			nonEmpty(t.ProjectName, "global"),
		}
		if innerW >= 92 {
			row = []string{
				t.Name,
				nonEmpty(t.Requested, "—"),
				nonEmpty(t.Version, "—"),
				nonEmpty(t.Latest, "—"),
				statSt.Render(t.Status),
				t.Scope,
				nonEmpty(t.ProjectName, "global"),
			}
		}
		rows = append(rows, row)
	}
	cols := []string{"Tool", "Version", "Latest", "Status", "Project"}
	if innerW >= 92 {
		cols = []string{"Tool", "Requested", "Installed", "Latest", "Status", "Scope", "Project"}
	}
	lines := renderSelectableTable(cols, rows, p.toolCursor, marked, focused, innerW, innerH, st)
	badge := ""
	if p.toolSel.Count() > 0 {
		badge = fmt.Sprintf("%d marked", p.toolSel.Count())
	}
	return renderTitledBox(title, badge, lines, width, height, focused, st)
}

func (p misePanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	if p.subView == miseSubTools {
		return p.renderToolPreview(width, height, innerW, innerH, focused, borderless, st)
	}
	return p.renderConfigPreview(width, height, innerW, innerH, focused, borderless, st)
}

func (p misePanel) renderConfigPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	cfg, ok := p.selectedConfig()
	if !ok {
		return renderTitledBox("Mise Config Inspector", "", []string{st.Muted.Render("Select a mise config file.")}, width, height, focused, st)
	}

	forgeInfo := nonEmpty(cfg.Forge, "local")
	if cfg.Forge == "github" && cfg.GitHubSlug != "" {
		forgeInfo = "github · " + cfg.GitHubSlug + " (Issue/PR eligible)"
	} else if cfg.Forge == "codecommit" {
		forgeInfo = "codecommit (local research/branch only)"
	}
	overrideLbl := "standard"
	if cfg.LocalOverride {
		overrideLbl = "local override (.local)"
	}

	var all []string
	pairs := [][2]string{
		{"Project / Scope", nonEmpty(cfg.ProjectName, "Global") + " · " + cfg.Scope},
		{"Kind", cfg.Kind + " (" + overrideLbl + ")"},
		{"Zone", nonEmpty(cfg.Zone, "all/global")},
		{"Forge", forgeInfo},
	}
	if cfg.Branch != "" {
		br := cfg.Branch
		if cfg.Dirty {
			br += " (dirty)"
		}
		pairs = append(pairs, [2]string{"Git Branch", br})
	}
	all = append(all, renderKVTable(pairs, innerW, st)...)
	all = append(all, "", st.SectionHeader.Render("File Path"))
	for _, wl := range wrapPlainText(collapseHomePath(cfg.Path), max(10, innerW-2)) {
		all = append(all, "  "+st.AccentText.Render(wl))
	}

	all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Declared Tools (%d)", len(cfg.Tools))))
	if len(cfg.Tools) == 0 {
		all = append(all, "  "+st.Muted.Render("No [tools] declared in this file."))
	} else {
		toolByName := map[string]MiseToolRow{}
		for _, t := range p.inv.Tools {
			if filepath.Clean(t.SourcePath) == filepath.Clean(cfg.Path) {
				toolByName[t.Name] = t
			}
		}
		for _, td := range cfg.Tools {
			stStr := st.Muted.Render("declared")
			verDetail := nonEmpty(td.Requested, "latest")
			if tr, found := toolByName[td.Name]; found {
				switch tr.Status {
				case "outdated":
					stStr = st.WarnText.Render("outdated")
					verDetail = fmt.Sprintf("%s -> %s", nonEmpty(tr.Version, tr.Requested), nonEmpty(tr.Latest, "?"))
				case "missing":
					stStr = st.ErrText.Render("missing")
					verDetail = "req " + nonEmpty(td.Requested, "latest")
				default:
					stStr = st.OKText.Render("installed")
					verDetail = nonEmpty(tr.Version, td.Requested)
				}
			}
			all = append(all, fitStyledWidth(fmt.Sprintf("  %-16s %-18s %s", td.Name, verDetail, stStr), innerW, st))
		}
	}

	if len(cfg.Tasks) > 0 {
		all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Tasks (%d)", len(cfg.Tasks))))
		for _, wl := range wrapPlainText(strings.Join(cfg.Tasks, ", "), max(10, innerW-2)) {
			all = append(all, "  "+st.InfoText.Render(wl))
		}
	}
	if len(cfg.EnvKeys) > 0 {
		all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Env Key Names Only (%d — values redacted)", len(cfg.EnvKeys))))
		for _, wl := range wrapPlainText(strings.Join(cfg.EnvKeys, ", "), max(10, innerW-2)) {
			all = append(all, "  "+st.Muted.Render(wl))
		}
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Mise Config · "+cfg.Kind, "", vis, width, height, focused, st)
}

func (p misePanel) renderToolPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	t, ok := p.selectedTool()
	if !ok {
		return renderTitledBox("Mise Tool Inspector", "", []string{st.Muted.Render("Select a mise package.")}, width, height, focused, st)
	}
	stRender := st.OKText.Render(t.Status)
	if t.Outdated {
		stRender = st.WarnText.Render("outdated")
	} else if !t.Installed {
		stRender = st.ErrText.Render("missing")
	}

	var all []string
	all = append(all, renderKVTable([][2]string{
		{"Tool", st.AccentText.Render(t.Name)},
		{"Status", stRender},
		{"Requested", nonEmpty(t.Requested, "—")},
		{"Installed", nonEmpty(t.Version, "missing")},
		{"Latest", nonEmpty(t.Latest, "up to date")},
		{"Scope / Zone", t.Scope + " · " + nonEmpty(t.Zone, "all/global")},
		{"Project", nonEmpty(t.ProjectName, "Global")},
		{"Forge", nonEmpty(t.Forge, "local")},
	}, innerW, st)...)

	all = append(all, "", st.SectionHeader.Render("Declared In"))
	for _, wl := range wrapPlainText(collapseHomePath(t.SourcePath), max(10, innerW-2)) {
		all = append(all, "  "+st.AccentText.Render(wl))
	}
	if t.InstallPath != "" {
		all = append(all, "", st.SectionHeader.Render("Install Path"))
		for _, wl := range wrapPlainText(collapseHomePath(t.InstallPath), max(10, innerW-2)) {
			all = append(all, "  "+st.Muted.Render(wl))
		}
	}
	all = append(all, "", st.SectionHeader.Render("CLI Upgrade"))
	all = append(all, "  "+st.OKText.Render("mise upgrade "+t.Name))

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Mise Package · "+t.Name, "", vis, width, height, focused, st)
}

func (p misePanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.filters.Open {
		groups, sorts := p.activeFacetSpec()
		return p.filters.ViewModal(p.FilterTarget(), groups, sorts, width, height, st)
	}
	return "", false
}

func loadMiseInventoryCmd(backend Backend, zone string, checkOutdated, force bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return miseLoadedMsg{checkedOutdated: checkOutdated, err: fmt.Errorf("backend unavailable")}
		}
		inv, err := backend.LoadMiseInventory(zone, checkOutdated, force)
		return miseLoadedMsg{inv: inv, checkedOutdated: checkOutdated, err: err}
	}
}
