package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

const (
	envSubVars = iota
	envSubFiles
	envSubVault
	envSubCount
)

const (
	rotateFocusInput = iota
	rotateFocusTargets
	rotateFocusButtons
)

type envPanel struct {
	subView         int
	vault           vaultPanel
	zone            string
	allVars         []EnvVarRow
	vars            []EnvVarRow
	allFiles        []EnvFileRow
	files           []EnvFileRow
	detail          EnvDetail
	detailKey       string
	drillKey        string
	drillFocusOther bool
	revealed        map[string]string
	query           string
	varFilters      FacetFilterState
	cursor          int
	fileCursor      int
	layerCursor     int
	otherCursor     int
	previewScroll   int
	varSel          SelectionSet
	layerSel        SelectionSet
	loaded          bool
	filesLoaded     bool
	loadingVars     bool
	loadingFiles    bool
	loadErr         string
	filesErr        string
	detailErr       string
	scanning        bool
	rotating        bool
	backingUp       bool

	rotateOpen         bool
	rotateInput        textinput.Model
	rotatePreview      EnvRotatePreview
	rotatePreviewErr   string
	rotateAnchorPath   string
	rotatePrechecked   []string
	rotateInitialized  bool
	rotateChecked      map[string]bool
	rotateTargetCursor int
	rotateFocus        int
	rotateConfirmFocus bool
}

func newEnvPanel() envPanel {
	ri := textinput.New()
	ri.Prompt = "New value: "
	ri.Placeholder = "type replacement secret..."
	ri.CharLimit = 512
	ri.EchoMode = textinput.EchoPassword
	ri.EchoCharacter = '*'
	ri.SetWidth(36)

	return envPanel{
		vault:         newVaultPanel(),
		revealed:      map[string]string{},
		rotateChecked: map[string]bool{},
		rotateInput:   ri,
	}
}

func (p envPanel) ID() string         { return "env" }
func (p envPanel) Title() string      { return "Env & Secrets" }
func (p envPanel) ShortTitle() string { return "Env & Secrets" }
func (p envPanel) SubViews() []string { return []string{"Vars", "Files", "Vault"} }
func (p envPanel) ActiveSubView() int { return p.subView }

func (p envPanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < envSubCount {
		p.subView = idx
		p.drillKey = ""
		p.drillFocusOther = false
		p.previewScroll = 0
		p.vault = p.vault.Pop()
	}
	return p
}

func (p envPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	if p.subView == envSubFiles && !p.filesLoaded && backend != nil {
		p.loadingFiles = true
		return p, tea.Batch(
			func() tea.Msg { return activityMsg{label: "Loading env files..."} },
			loadEnvFilesCmd(backend, p.zone),
		)
	}
	return p, nil
}

func (p envPanel) SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd) {
	p.zone = normalizeEnvScopeZone(zone)
	p.cursor = 0
	p.fileCursor = 0
	p.layerCursor = 0
	p.otherCursor = 0
	p.previewScroll = 0
	p.drillKey = ""
	p.varSel = p.varSel.Clear()
	p.layerSel = p.layerSel.Clear()
	p.revealed = map[string]string{}
	p.detail = EnvDetail{}
	p.detailKey = ""
	p.detailErr = ""
	p.loadingVars = true
	p.loadingFiles = true
	if backend == nil {
		p.vars = p.applyVarFilter(p.allVars)
		p.files = p.applyFileFilter(p.allFiles)
		return p, nil
	}
	return p, tea.Batch(
		func() tea.Msg { return activityMsg{label: "Loading env vars..."} },
		loadEnvVarsCmd(backend, "", p.zone),
		loadEnvFilesCmd(backend, p.zone),
	)
}

func (p envPanel) IsZoned() bool { return true }
func (p envPanel) FilterQuery() string {
	if p.subView == envSubVault {
		return p.vault.FilterQuery()
	}
	return p.query
}
func (p envPanel) FilterTarget() string {
	if p.subView == envSubVault {
		return p.vault.FilterTarget()
	}
	if p.subView == envSubFiles {
		return "Env Files"
	}
	return "Env Vars"
}
func (p envPanel) SupportsPreview() bool {
	if p.subView == envSubVault {
		return p.vault.SupportsPreview()
	}
	return p.drillKey == ""
}
func (p envPanel) DefaultSplit(width int) SplitMode {
	if p.subView == envSubVault {
		return p.vault.DefaultSplit(width)
	}
	return SplitOff
}

func (p envPanel) CanPop() bool {
	if p.subView == envSubVault {
		return p.vault.CanPop()
	}
	return p.drillKey != ""
}
func (p envPanel) Pop() ResourcePanel {
	if p.subView == envSubVault {
		p.vault = p.vault.Pop()
		return p
	}
	p.drillKey = ""
	p.drillFocusOther = false
	p.layerSel = p.layerSel.Clear()
	p.previewScroll = 0
	return p
}

func (p envPanel) SelectedCount() int {
	if p.subView == envSubVault {
		return p.vault.SelectedCount()
	}
	if p.drillKey != "" {
		return p.layerSel.Count()
	}
	return p.varSel.Count()
}

func (p envPanel) ClearSelection() ResourcePanel {
	if p.subView == envSubVault {
		p.vault = p.vault.ClearSelection()
		return p
	}
	p.varSel = p.varSel.Clear()
	p.layerSel = p.layerSel.Clear()
	return p
}

func (p envPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	if p.subView == envSubVault {
		p.vault = p.vault.SetFilterQuery(q)
		return p, nil
	}
	p.query = q
	p.vars = p.applyVarFilter(p.allVars)
	p.files = p.applyFileFilter(p.allFiles)
	p.cursor = 0
	p.fileCursor = 0
	if p.subView == envSubVars {
		if v, ok := p.selectedVar(); ok && !strings.EqualFold(p.detailKey, v.VarKey) {
			p.detailKey = v.VarKey
			p.detail = EnvDetail{}
			p.detailErr = ""
			if backend != nil {
				return p, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath)
			}
		}
	}
	return p, nil
}

func (p envPanel) OnBlur() ResourcePanel {
	p.rotateOpen = false
	p.rotateConfirmFocus = false
	p.rotateInput.Blur()
	p.varFilters.Close()
	p.revealed = map[string]string{}
	return p
}

func (p envPanel) Attention() AttentionLevel {
	if p.loadErr != "" || p.filesErr != "" || p.detailErr != "" || p.vault.Attention() == AttentionErr {
		return AttentionErr
	}
	for _, f := range p.files {
		if f.Missing {
			return AttentionWarn
		}
	}
	return p.vault.Attention()
}

func (p envPanel) PaneStatus() string {
	if p.subView == envSubVault {
		if st := p.vault.PaneStatus(); st != "" {
			return st
		}
	}
	switch {
	case p.scanning:
		return "scanning..."
	case p.rotating:
		return "rotating..."
	case p.backingUp:
		return "backing up..."
	case p.subView == envSubFiles && p.loadingFiles && !p.filesLoaded:
		return "loading files..."
	case p.loadingVars && !p.loaded:
		return "loading vars..."
	}
	return p.vault.PaneStatus()
}

func (p envPanel) Breadcrumbs() []string {
	if p.subView == envSubVault {
		return p.vault.Breadcrumbs()
	}
	if p.drillKey != "" {
		return []string{"Env & Secrets", "Vars", p.drillKey}
	}
	if p.subView == envSubFiles {
		return []string{"Env & Secrets", fmt.Sprintf("Files (%d)", len(p.files))}
	}
	return []string{"Env & Secrets", fmt.Sprintf("Vars (%d)", len(p.vars))}
}

func (p envPanel) StatusChips() []string {
	if p.subView == envSubVault {
		return p.vault.StatusChips()
	}
	var chips []string
	if p.query != "" {
		chips = append(chips, "/"+truncate(p.query, 18))
	}
	if p.subView == envSubFiles {
		chips = append(chips, "files")
	} else {
		chips = append(chips, p.varFilters.Chips()...)
	}
	return chips
}

func (p envPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.subView == envSubVault {
		return p.vault.KeyHints(split, focusPreview)
	}
	if p.drillKey != "" {
		return [][2]string{
			{"space/*", "Mark files"},
			{"tab", "Chain / Checkouts"},
			{"y/Y", "Copy ENV / Val"},
			{"a", "Env actions"},
			{"esc", "Back (keeps /)"},
		}
	}
	if p.subView == envSubFiles {
		return [][2]string{
			{"enter", "Filter file vars"},
			{"y", "Copy file path"},
			{"r", "Rescan env"},
			{"a", "File actions"},
		}
	}
	return [][2]string{
		{"enter", "Env Detail"},
		{"y/Y", "Copy ENV / Val"},
		{"r", "Rescan env"},
		{"a", "Env actions"},
		{"F/S", "Filter / Sort"},
		{"space/*", "Mark vars"},
	}
}

func (p envPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	if p.subView == envSubVault {
		return p.vault.RowActions(split, focusPreview)
	}
	if p.subView == envSubFiles {
		return []RowAction{
			{Key: "y", Label: "Copy file path", Description: "Copy selected env file path"},
			{Key: "b", Label: "Backup ws.db", Description: "Write encrypted ws.db backup if changed"},
		}
	}
	return []RowAction{
		{Key: "v", Label: "Reveal / Mask value", Description: "Toggle secret value visibility"},
		{Key: "R", Label: "Rotate secret", Description: "Selectively rotate value across matching files", Dangerous: true},
		{Key: "c", Label: "Copy source path", Description: "Copy active source file path (and line in detail)"},
		{Key: "b", Label: "Backup ws.db", Description: "Write encrypted ws.db backup if changed"},
	}
}

func (p envPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if p.subView == envSubVault {
		nextVault, cmd := p.vault.RunRowAction(actionKey, backend, split, focusPreview)
		p.vault = nextVault
		return p, cmd
	}
	switch actionKey {
	case "v":
		return p.toggleReveal(backend)
	case "R":
		return p.openRotateModal(backend)
	case "c":
		if p.drillKey != "" {
			if row, ok := p.focusedLayerRow(); ok && row.SourcePath != "" {
				loc := row.SourcePath
				if row.LineNum > 0 {
					loc = fmt.Sprintf("%s:%d", row.SourcePath, row.LineNum)
				}
				return p, copyCmd(loc, "file location")
			}
			return p, nil
		}
		if v, ok := p.selectedVar(); ok && v.ActiveSourcePath != "" {
			return p, copyCmd(v.ActiveSourcePath, "active source path")
		}
	case "b":
		if p.backingUp {
			return p, nil
		}
		p.backingUp = true
		return p, backupDBCmd(backend, true)
	case "y":
		if p.subView == envSubFiles {
			if f, ok := p.selectedFile(); ok && f.Path != "" {
				return p, copyCmd(f.Path, "file path")
			}
		}
	}
	return p, nil
}

func (p envPanel) Badge() string {
	if p.subView == envSubVault {
		return p.vault.Badge()
	}
	if !p.loaded {
		return "…"
	}
	return fmt.Sprintf("%d vars", len(p.vars))
}

func (p envPanel) CapturingInput() bool {
	if p.subView == envSubVault {
		return p.vault.CapturingInput()
	}
	return (p.rotateOpen && p.rotateFocus == rotateFocusInput) || (p.subView == envSubVars && p.varFilters.Open)
}
func (p envPanel) HasOpenModal() bool {
	if p.subView == envSubVault {
		return p.vault.HasOpenModal()
	}
	return p.rotateOpen || (p.subView == envSubVars && p.varFilters.Open)
}
func (p envPanel) MutationInFlight() bool {
	return p.rotating || p.backingUp || p.scanning || p.vault.MutationInFlight()
}

func (p envPanel) selectedVar() (EnvVarRow, bool) {
	if len(p.vars) == 0 || p.cursor < 0 || p.cursor >= len(p.vars) {
		return EnvVarRow{}, false
	}
	return p.vars[p.cursor], true
}

func (p envPanel) selectedFile() (EnvFileRow, bool) {
	if len(p.files) == 0 || p.fileCursor < 0 || p.fileCursor >= len(p.files) {
		return EnvFileRow{}, false
	}
	return p.files[p.fileCursor], true
}

func envVarSelectionKey(v EnvVarRow) string {
	return v.Zone + "\x00" + v.VarKey + "\x00" + v.ActiveSourcePath
}

func (p envPanel) otherLocations() []EnvLayerRow {
	if len(p.detail.BlastRadius) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(p.detail.Layers))
	for _, l := range p.detail.Layers {
		seen[layerIdentityKey(l.SourcePath, l.LineNum, l.VarKey)] = true
	}
	var out []EnvLayerRow
	for _, b := range p.detail.BlastRadius {
		k := layerIdentityKey(b.SourcePath, b.LineNum, b.VarKey)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, b)
	}
	return out
}

func (p envPanel) activeLayerIndex() int {
	for i, l := range p.detail.Layers {
		if strings.EqualFold(l.Status, "ACTIVE") {
			return i
		}
	}
	if len(p.detail.Layers) > 0 {
		return len(p.detail.Layers) - 1
	}
	return 0
}

func (p envPanel) focusedLayerRow() (EnvLayerRow, bool) {
	if p.drillKey != "" {
		if p.drillFocusOther {
			others := p.otherLocations()
			if len(others) > 0 && p.otherCursor >= 0 && p.otherCursor < len(others) {
				return others[p.otherCursor], true
			}
			return EnvLayerRow{}, false
		}
		if len(p.detail.Layers) > 0 && p.layerCursor >= 0 && p.layerCursor < len(p.detail.Layers) {
			return p.detail.Layers[p.layerCursor], true
		}
	}
	if v, ok := p.selectedVar(); ok && v.ActiveSourcePath != "" {
		return EnvLayerRow{
			Status:      "ACTIVE",
			VarKey:      v.VarKey,
			Zone:        v.Zone,
			SourceKind:  v.ActiveSourceKind,
			SourcePath:  v.ActiveSourcePath,
			MaskedValue: v.ActiveMasked,
			Fingerprint: v.Fingerprint,
			IsSecret:    v.IsSecret,
		}, true
	}
	if len(p.detail.Layers) > 0 {
		return p.detail.Layers[p.activeLayerIndex()], true
	}
	return EnvLayerRow{}, false
}

func (p envPanel) candidateRotatePaths(anchor EnvLayerRow) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || seen[trimmed] {
			return
		}
		seen[trimmed] = true
		paths = append(paths, trimmed)
	}
	for _, pr := range p.rotatePrechecked {
		add(pr)
	}
	add(anchor.SourcePath)
	for _, l := range p.detail.Layers {
		add(l.SourcePath)
	}
	for _, o := range p.otherLocations() {
		add(o.SourcePath)
	}
	return paths
}

func (p envPanel) CLIEquivalent() string {
	if p.subView == envSubVault {
		return p.vault.CLIEquivalent()
	}
	if p.subView == envSubFiles {
		if p.zone != "" {
			return fmt.Sprintf("ws env files --zone %s", p.zone)
		}
		return "ws env files"
	}
	if v, ok := p.selectedVar(); ok {
		if p.rotateOpen {
			return fmt.Sprintf("ws env rotate %s", v.VarKey)
		}
		return fmt.Sprintf("ws env show %s", v.VarKey)
	}
	if q := strings.TrimSpace(p.query); q != "" {
		if p.zone != "" {
			return fmt.Sprintf("ws env list --zone %s --filter %q", p.zone, q)
		}
		return fmt.Sprintf("ws env list --filter %q", q)
	}
	if p.zone != "" {
		return fmt.Sprintf("ws env list --zone %s", p.zone)
	}
	return "ws env list"
}

func (p envPanel) varFacetSpec() ([]FacetGroup, []SortOption) {
	z := normalizeEnvScopeZone(p.zone)
	kindCnt := map[string]int{}
	secCnt, plainCnt, shadCnt, cleanCnt := 0, 0, 0, 0
	for _, r := range p.allVars {
		if z != "" && !strings.EqualFold(r.Zone, z) {
			continue
		}
		if r.ActiveSourceKind != "" {
			kindCnt[strings.ToLower(r.ActiveSourceKind)]++
		}
		if r.IsSecret {
			secCnt++
		} else {
			plainCnt++
		}
		if r.ShadowedCount > 0 {
			shadCnt++
		} else {
			cleanCnt++
		}
	}
	var kinds []string
	for k := range kindCnt {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var kindOpts []FacetOption
	for _, k := range kinds {
		kindOpts = append(kindOpts, FacetOption{Value: k, Label: k, Count: kindCnt[k]})
	}
	groups := []FacetGroup{
		{
			Key:   "visibility",
			Label: "Visibility",
			Options: []FacetOption{
				{Value: "secret", Label: "Secret vars", Count: secCnt},
				{Value: "plain", Label: "Plaintext vars", Count: plainCnt},
			},
		},
		{
			Key:   "shadowed",
			Label: "Shadowing",
			Options: []FacetOption{
				{Value: "shadowed", Label: "Shadowed (>1 src)", Count: shadCnt},
				{Value: "clean", Label: "Single-layer", Count: cleanCnt},
			},
		},
	}
	if len(kindOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "kind", Label: "Source Kind", Options: kindOpts})
	}
	sorts := []SortOption{
		{Key: "key", Label: "Variable Key (A–Z)"},
		{Key: "shadowed", Label: "Shadowed Count"},
		{Key: "locs", Label: "Location Count"},
		{Key: "kind", Label: "Source Kind"},
	}
	return groups, sorts
}

func (p envPanel) applyVarFilter(rows []EnvVarRow) []EnvVarRow {
	var scoped []EnvVarRow
	z := normalizeEnvScopeZone(p.zone)
	for _, r := range rows {
		if z != "" && !strings.EqualFold(r.Zone, z) {
			continue
		}
		scoped = append(scoped, r)
	}
	filtered := fuzzy.Filter(scoped, p.query, func(r EnvVarRow) ([]string, map[string]string) {
		sec := "false"
		if r.IsSecret {
			sec = "true"
		}
		return []string{r.VarKey, r.ActiveSourcePath, r.ActiveSourceKind, r.Zone},
			map[string]string{
				"zone":   nonEmpty(r.Zone, "global"),
				"kind":   r.ActiveSourceKind,
				"secret": sec,
			}
	})
	var kept []EnvVarRow
	for _, r := range filtered {
		vis := "plain"
		if r.IsSecret {
			vis = "secret"
		}
		if !p.varFilters.Matches("visibility", vis) {
			continue
		}
		sh := "clean"
		if r.ShadowedCount > 0 {
			sh = "shadowed"
		}
		if !p.varFilters.Matches("shadowed", sh) {
			continue
		}
		if !p.varFilters.Matches("kind", r.ActiveSourceKind) {
			continue
		}
		kept = append(kept, r)
	}
	if p.varFilters.SortKey != "" || p.varFilters.SortDesc {
		sort.SliceStable(kept, func(i, j int) bool {
			var cmp int
			switch p.varFilters.SortKey {
			case "shadowed":
				cmp = kept[j].ShadowedCount - kept[i].ShadowedCount
			case "locs":
				cmp = kept[j].SourceCount - kept[i].SourceCount
			case "kind":
				cmp = strings.Compare(strings.ToLower(kept[i].ActiveSourceKind), strings.ToLower(kept[j].ActiveSourceKind))
			default:
				cmp = strings.Compare(strings.ToLower(kept[i].VarKey), strings.ToLower(kept[j].VarKey))
			}
			if p.varFilters.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	return kept
}

func (p envPanel) applyFileFilter(rows []EnvFileRow) []EnvFileRow {
	var scoped []EnvFileRow
	z := normalizeEnvScopeZone(p.zone)
	for _, r := range rows {
		if z != "" && !strings.EqualFold(r.Zone, z) {
			continue
		}
		scoped = append(scoped, r)
	}
	return fuzzy.Filter(scoped, p.query, func(r EnvFileRow) ([]string, map[string]string) {
		st := "indexed"
		if r.Missing {
			st = "missing"
		} else if r.Ignored {
			st = "ignored"
		}
		return []string{r.Path, r.SourceKind, r.Zone, st},
			map[string]string{
				"zone":   nonEmpty(r.Zone, "global"),
				"kind":   r.SourceKind,
				"status": st,
			}
	})
}

func (p envPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case envVarsLoadedMsg:
		p.loaded = true
		p.loadingVars = false
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Env load failed: " + msg.err.Error(), isErr: true} },
				func() tea.Msg { return activityMsg{label: "Env load failed", done: true, isErr: true} },
			)
		}
		if normalizeEnvScopeZone(msg.zone) != p.zone {
			return p, func() tea.Msg { return activityMsg{done: true} }
		}
		p.loadErr = ""
		if msg.query == "" {
			p.allVars = msg.vars
		}
		src := p.allVars
		if len(msg.vars) > 0 && msg.query != "" {
			src = msg.vars
		}
		p.vars = p.applyVarFilter(src)
		if p.cursor >= len(p.vars) {
			p.cursor = max(0, len(p.vars)-1)
		}
		cmds := []tea.Cmd{func() tea.Msg { return activityMsg{done: true} }}
		if v, ok := p.selectedVar(); ok {
			p.detailKey = v.VarKey
			p.detailErr = ""
			cmds = append(cmds, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath))
		} else {
			p.detail = EnvDetail{}
			p.detailKey = ""
			p.detailErr = ""
		}
		return p, tea.Batch(cmds...)

	case envFilesLoadedMsg:
		p.filesLoaded = true
		p.loadingFiles = false
		if msg.err != nil {
			p.filesErr = msg.err.Error()
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Env files load failed: " + msg.err.Error(), isErr: true} },
				func() tea.Msg { return activityMsg{label: "Env files load failed", done: true, isErr: true} },
			)
		}
		if normalizeEnvScopeZone(msg.zone) != p.zone {
			return p, func() tea.Msg { return activityMsg{done: true} }
		}
		p.filesErr = ""
		p.allFiles = msg.files
		p.files = p.applyFileFilter(p.allFiles)
		if p.fileCursor >= len(p.files) {
			p.fileCursor = max(0, len(p.files)-1)
		}
		return p, func() tea.Msg { return activityMsg{done: true} }

	case envDetailLoadedMsg:
		if msg.err != nil {
			if msg.varKey == p.detailKey || p.detailKey == "" {
				p.detailErr = msg.err.Error()
			}
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Env detail failed: " + msg.err.Error(), isErr: true} },
				func() tea.Msg { return activityMsg{label: "Env detail failed", done: true, isErr: true} },
			)
		}
		if msg.varKey == p.detailKey || p.detailKey == "" {
			p.detailErr = ""
			p.detail = msg.detail
			p.detailKey = msg.varKey
			if len(p.detail.Layers) > 0 && p.layerCursor >= len(p.detail.Layers) {
				p.layerCursor = p.activeLayerIndex()
			}
			if others := p.otherLocations(); p.otherCursor >= len(others) {
				p.otherCursor = max(0, len(others)-1)
			}
		}
		return p, func() tea.Msg { return activityMsg{done: true} }

	case envRevealedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: "Reveal failed: " + msg.err.Error(), isErr: true}
			}
		}
		if p.revealed == nil {
			p.revealed = map[string]string{}
		}
		p.revealed[revealKey(msg.sourcePath, msg.varKey)] = msg.value
		return p, func() tea.Msg {
			return statusToastMsg{text: fmt.Sprintf("Revealed %s (press v again to hide)", msg.varKey)}
		}

	case envRotatePreviewMsg:
		if msg.err != nil {
			p.rotatePreviewErr = msg.err.Error()
			return p, nil
		}
		p.rotatePreviewErr = ""
		p.rotatePreview = msg.preview
		p.syncRotateChecked()
		return p, nil

	case envRotatedMsg:
		p.rotating = false
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: "Rotate failed: " + msg.err.Error(), isErr: true}
			}
		}
		summary := msg.result.Summary
		if summary == "" {
			summary = fmt.Sprintf("Rotated %d file(s) (tx %s)", msg.result.UpdatedCount, msg.result.TxID)
		}
		cmds := []tea.Cmd{
			func() tea.Msg { return statusToastMsg{text: summary} },
			loadEnvVarsCmd(backend, "", p.zone),
			loadEnvFilesCmd(backend, p.zone),
		}
		if v, ok := p.selectedVar(); ok {
			cmds = append(cmds, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath))
		}
		return p, tea.Batch(cmds...)

	case envScannedMsg:
		p.scanning = false
		if msg.err != nil {
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Env scan failed: " + msg.err.Error(), isErr: true} },
				func() tea.Msg { return activityMsg{label: "Env scan failed", done: true, isErr: true} },
			)
		}
		p.loadingVars = true
		p.loadingFiles = true
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Environment re-scanned"} },
			func() tea.Msg { return activityMsg{done: true} },
			loadEnvVarsCmd(backend, "", p.zone),
			loadEnvFilesCmd(backend, p.zone),
		)

	case vaultInventoryLoadedMsg, vaultActionDoneMsg, vaultMutationStartMsg, dbStatusLoadedMsg, undoDoneMsg:
		nextVault, cmd := p.vault.Update(msg, backend, split, focusPreview)
		p.vault = nextVault
		return p, cmd

	case dbBackedUpMsg:
		p.backingUp = false
		nextVault, cmd := p.vault.Update(msg, backend, split, focusPreview)
		p.vault = nextVault
		return p, cmd

	case tea.KeyPressMsg:
		if p.subView == envSubVault {
			nextVault, cmd := p.vault.Update(msg, backend, split, focusPreview)
			p.vault = nextVault
			return p, cmd
		}
		if p.rotateOpen {
			return p.updateRotateModal(msg, backend)
		}
		if p.drillKey == "" && p.subView == envSubVars && p.varFilters.Open {
			groups, sorts := p.varFacetSpec()
			if handled, changed := p.varFilters.Update(msg, groups, sorts); handled {
				if changed {
					p.vars = p.applyVarFilter(p.allVars)
					p.cursor = 0
				}
				return p, nil
			}
		}
		if p.drillKey != "" {
			return p.updateDrillKey(msg, backend)
		}
		if p.subView == envSubFiles {
			return p.updateFilesKey(msg, backend, focusPreview)
		}
		return p.updateVarsKey(msg, backend, focusPreview)
	}

	if p.rotateOpen && p.rotateFocus == rotateFocusInput {
		var cmd tea.Cmd
		p.rotateInput, cmd = p.rotateInput.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p envPanel) updateVarsKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if isSpaceKey(msg) {
		if v, ok := p.selectedVar(); ok {
			p.varSel = p.varSel.Toggle(envVarSelectionKey(v))
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.cursor, len(p.vars), defaultMotionPage, m); changed {
			p.cursor = next
			p.layerCursor = 0
			p.otherCursor = 0
			p.previewScroll = 0
			if v, ok := p.selectedVar(); ok {
				p.detailKey = v.VarKey
				p.detailErr = ""
				return p, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath)
			}
		}
		return p, nil
	}
	switch msg.String() {
	case "F":
		groups, sorts := p.varFacetSpec()
		p.varFilters.OpenFilters(groups, sorts)
		return p, nil
	case "S":
		groups, sorts := p.varFacetSpec()
		p.varFilters.OpenSort(groups, sorts)
		return p, nil
	case "*":
		keys := make([]string, len(p.vars))
		for i, v := range p.vars {
			keys[i] = envVarSelectionKey(v)
		}
		p.varSel = p.varSel.ToggleAll(keys)
	case `\`:
		p.varSel = p.varSel.Clear()
		if p.varFilters.HasActive() {
			p.varFilters.Clear()
			p.vars = p.applyVarFilter(p.allVars)
			p.cursor = 0
		}
	case "enter":
		if v, ok := p.selectedVar(); ok {
			p.drillKey = v.VarKey
			p.drillFocusOther = false
			p.layerSel = p.layerSel.Clear()
			p.layerCursor = p.activeLayerIndex()
			p.otherCursor = 0
			p.previewScroll = 0
			if p.detailKey != v.VarKey {
				p.detailKey = v.VarKey
				return p, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath)
			}
		}
	case "y":
		if targets := p.revealTargets(); len(targets) > 0 {
			return p, copyEnvTargetsCmd(backend, targets, cloneRevealedMap(p.revealed), true)
		}
	case "Y":
		if targets := p.revealTargets(); len(targets) > 0 {
			return p, copyEnvTargetsCmd(backend, targets, cloneRevealedMap(p.revealed), false)
		}
	case "r":
		if p.scanning {
			return p, nil
		}
		p.scanning = true
		return p, tea.Batch(
			func() tea.Msg { return activityMsg{label: "Scanning env files..."} },
			scanEnvCmd(backend),
		)
	}
	return p, nil
}

func (p envPanel) updateDrillKey(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	others := p.otherLocations()
	if isSpaceKey(msg) {
		if row, ok := p.focusedLayerRow(); ok && row.SourcePath != "" {
			p.layerSel = p.layerSel.Toggle(row.SourcePath)
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if p.drillFocusOther {
			if next, changed := applyListMotion(p.otherCursor, len(others), defaultMotionPage, m); changed {
				p.otherCursor = next
			}
		} else if next, changed := applyListMotion(p.layerCursor, len(p.detail.Layers), defaultMotionPage, m); changed {
			p.layerCursor = next
		}
		return p, nil
	}
	switch msg.String() {
	case "tab", "shift+tab":
		if len(others) > 0 || p.drillFocusOther {
			p.drillFocusOther = !p.drillFocusOther
		}
	case "*":
		var paths []string
		if p.drillFocusOther {
			for _, o := range others {
				paths = append(paths, o.SourcePath)
			}
		} else {
			for _, l := range p.detail.Layers {
				paths = append(paths, l.SourcePath)
			}
		}
		p.layerSel = p.layerSel.ToggleAll(paths)
	case `\`:
		p.layerSel = p.layerSel.Clear()
	case "y":
		if targets := p.revealTargets(); len(targets) > 0 {
			return p, copyEnvTargetsCmd(backend, targets, cloneRevealedMap(p.revealed), true)
		}
	case "Y":
		if targets := p.revealTargets(); len(targets) > 0 {
			return p, copyEnvTargetsCmd(backend, targets, cloneRevealedMap(p.revealed), false)
		}
	}
	return p, nil
}

func (p envPanel) updateFilesKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.fileCursor, len(p.files), defaultMotionPage, m); changed {
			p.fileCursor = next
		}
		return p, nil
	}
	switch msg.String() {
	case "enter":
		if f, ok := p.selectedFile(); ok && f.Path != "" {
			p.subView = envSubVars
			p.query = f.Path
			p.vars = p.applyVarFilter(p.allVars)
			p.cursor = 0
			p.previewScroll = 0
			if v, okV := p.selectedVar(); okV {
				p.detailKey = v.VarKey
				return p, loadEnvDetailCmd(backend, v.VarKey, v.ActiveSourcePath)
			}
		}
	case "y":
		if f, ok := p.selectedFile(); ok && f.Path != "" {
			return p, copyCmd(f.Path, "file path")
		}
	case "r":
		if p.scanning {
			return p, nil
		}
		p.scanning = true
		return p, tea.Batch(
			func() tea.Msg { return activityMsg{label: "Scanning env files..."} },
			scanEnvCmd(backend),
		)
	}
	return p, nil
}

func (p envPanel) openRotateModal(backend Backend) (ResourcePanel, tea.Cmd) {
	v, ok := p.selectedVar()
	if !ok {
		return p, func() tea.Msg { return statusToastMsg{text: "Select a variable to update"} }
	}
	anchor, _ := p.focusedLayerRow()
	p.rotateOpen = true
	p.rotateAnchorPath = anchor.SourcePath
	p.rotatePrechecked = nil
	p.rotateChecked = map[string]bool{}
	if p.layerSel.Count() > 0 {
		for path := range p.layerSel.items {
			p.rotatePrechecked = append(p.rotatePrechecked, path)
			p.rotateChecked[path] = true
		}
	} else if anchor.SourcePath != "" {
		p.rotateChecked[anchor.SourcePath] = true
	}
	p.rotateInitialized = false
	p.rotateTargetCursor = 0
	p.rotateFocus = rotateFocusInput
	p.rotateConfirmFocus = false
	p.rotatePreview = EnvRotatePreview{VarKey: v.VarKey}
	p.rotatePreviewErr = ""
	p.rotateInput.SetValue("")
	focusCmd := p.rotateInput.Focus()
	paths := p.candidateRotatePaths(anchor)
	req := EnvRotateReq{
		VarKey: v.VarKey,
		Paths:  paths,
	}
	if len(req.Paths) == 0 {
		req.MatchFingerprint = v.Fingerprint
		req.Zone = v.Zone
	}
	return p, tea.Batch(focusCmd, previewEnvRotateCmd(backend, req))
}

func (p *envPanel) syncRotateChecked() {
	if p.rotateChecked == nil {
		p.rotateChecked = map[string]bool{}
	}
	if len(p.rotatePreview.Targets) == 0 {
		p.rotateTargetCursor = 0
		return
	}
	if !p.rotateInitialized {
		p.rotateInitialized = true
		if len(p.rotatePrechecked) > 0 {
			for _, sp := range p.rotatePrechecked {
				p.rotateChecked[sp] = true
			}
		} else {
			matchedAnchor := false
			for i, t := range p.rotatePreview.Targets {
				if t.SourcePath == p.rotateAnchorPath {
					p.rotateChecked[t.SourcePath] = true
					p.rotateTargetCursor = i
					matchedAnchor = true
					break
				}
			}
			if !matchedAnchor {
				p.rotateChecked[p.rotatePreview.Targets[0].SourcePath] = true
				p.rotateTargetCursor = 0
			}
		}
	}
	if p.rotateTargetCursor >= len(p.rotatePreview.Targets) {
		p.rotateTargetCursor = max(0, len(p.rotatePreview.Targets)-1)
	}
}

type envRevealTarget struct {
	sourcePath     string
	varKey         string
	fallbackMasked string
}

func (p envPanel) revealTargets() []envRevealTarget {
	seen := map[string]bool{}
	var out []envRevealTarget
	addRow := func(sourcePath, varKey, fallback string) {
		if sourcePath == "" || varKey == "" {
			return
		}
		rk := revealKey(sourcePath, varKey)
		if seen[rk] {
			return
		}
		seen[rk] = true
		out = append(out, envRevealTarget{
			sourcePath:     sourcePath,
			varKey:         varKey,
			fallbackMasked: fallback,
		})
	}

	if p.drillKey != "" {
		if p.layerSel.Count() > 0 {
			for _, l := range p.detail.Layers {
				if p.layerSel.Has(l.SourcePath) {
					addRow(l.SourcePath, nonEmpty(l.VarKey, p.drillKey), l.MaskedValue)
				}
			}
			for _, o := range p.otherLocations() {
				if p.layerSel.Has(o.SourcePath) {
					addRow(o.SourcePath, nonEmpty(o.VarKey, p.drillKey), o.MaskedValue)
				}
			}
		}
		if len(out) == 0 {
			if row, ok := p.focusedLayerRow(); ok {
				addRow(row.SourcePath, nonEmpty(row.VarKey, p.drillKey), row.MaskedValue)
			}
		}
		return out
	}

	if p.varSel.Count() > 0 {
		for _, v := range p.vars {
			if p.varSel.Has(envVarSelectionKey(v)) {
				addRow(v.ActiveSourcePath, v.VarKey, v.ActiveMasked)
			}
		}
	}
	if len(out) == 0 {
		if v, ok := p.selectedVar(); ok {
			addRow(v.ActiveSourcePath, v.VarKey, v.ActiveMasked)
		}
	}
	return out
}

func cloneRevealedMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func formatDotEnvLine(key, val string) string {
	if val == "" {
		return key + "="
	}
	if strings.ContainsAny(val, " \t\r\n#\"'$") {
		return key + "=" + strconv.Quote(val)
	}
	return key + "=" + val
}

func copyEnvTargetsCmd(backend Backend, targets []envRevealTarget, revSnap map[string]string, formatDotEnv bool) tea.Cmd {
	targetsCopy := append([]envRevealTarget(nil), targets...)
	return func() tea.Msg {
		if len(targetsCopy) == 0 {
			return nil
		}
		lines := make([]string, 0, len(targetsCopy))
		for _, t := range targetsCopy {
			val, ok := revSnap[revealKey(t.sourcePath, t.varKey)]
			if !ok {
				if backend != nil {
					loaded, err := backend.RevealEnvValue(t.sourcePath, t.varKey)
					if err != nil {
						return statusToastMsg{text: fmt.Sprintf("Reveal %s failed: %v", t.varKey, err), isErr: true}
					}
					val = loaded
				} else {
					val = t.fallbackMasked
				}
			}
			if formatDotEnv {
				lines = append(lines, formatDotEnvLine(t.varKey, val))
			} else {
				lines = append(lines, val)
			}
		}
		var label string
		if formatDotEnv {
			if len(targetsCopy) == 1 {
				label = targetsCopy[0].varKey + " (.env line)"
			} else {
				label = fmt.Sprintf("%d vars (.env)", len(targetsCopy))
			}
		} else {
			if len(targetsCopy) == 1 {
				label = targetsCopy[0].varKey + " value"
			} else {
				label = fmt.Sprintf("%d values", len(targetsCopy))
			}
		}
		return copyCmd(strings.Join(lines, "\n"), label)()
	}
}

func (p envPanel) toggleReveal(backend Backend) (ResourcePanel, tea.Cmd) {
	targets := p.revealTargets()
	if len(targets) == 0 {
		return p, nil
	}
	if p.revealed == nil {
		p.revealed = map[string]string{}
	}
	allRevealed := true
	for _, t := range targets {
		if _, exists := p.revealed[revealKey(t.sourcePath, t.varKey)]; !exists {
			allRevealed = false
			break
		}
	}
	if allRevealed {
		for _, t := range targets {
			delete(p.revealed, revealKey(t.sourcePath, t.varKey))
		}
		lbl := "Value masked"
		if len(targets) > 1 {
			lbl = fmt.Sprintf("Masked %d values", len(targets))
		}
		return p, func() tea.Msg { return statusToastMsg{text: lbl} }
	}
	var cmds []tea.Cmd
	for _, t := range targets {
		rk := revealKey(t.sourcePath, t.varKey)
		if _, exists := p.revealed[rk]; !exists {
			cmds = append(cmds, revealEnvValueCmd(backend, t.sourcePath, t.varKey))
		}
	}
	return p, batchNonNil(cmds...)
}

func (p envPanel) updateRotateModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	if key == "esc" || (key == "q" && p.rotateFocus != rotateFocusInput) {
		p.closeRotateModal()
		return p, func() tea.Msg { return statusToastMsg{text: "Token rotation cancelled"} }
	}

	v, _ := p.selectedVar()
	paths := p.candidateRotatePaths(EnvLayerRow{SourcePath: p.rotateAnchorPath})

	switch p.rotateFocus {
	case rotateFocusInput:
		switch key {
		case "enter":
			p.closeRotateModal()
			return p, func() tea.Msg { return statusToastMsg{text: "Token rotation cancelled"} }
		case "down":
			p.rotateInput.Blur()
			if len(p.rotatePreview.Targets) > 0 {
				p.rotateFocus = rotateFocusTargets
			} else {
				p.rotateFocus = rotateFocusButtons
				p.rotateConfirmFocus = false
			}
			return p, nil
		case "tab":
			p.rotateInput.Blur()
			p.rotateFocus = rotateFocusButtons
			p.rotateConfirmFocus = false
			return p, nil
		default:
			prev := p.rotateInput.Value()
			var tiCmd tea.Cmd
			p.rotateInput, tiCmd = p.rotateInput.Update(msg)
			cur := p.rotateInput.Value()
			if cur != prev {
				req := EnvRotateReq{
					VarKey:   v.VarKey,
					Paths:    paths,
					NewValue: cur,
				}
				if len(req.Paths) == 0 {
					req.MatchFingerprint = v.Fingerprint
					req.Zone = v.Zone
				}
				return p, tea.Batch(tiCmd, previewEnvRotateCmd(backend, req))
			}
			return p, tiCmd
		}

	case rotateFocusTargets:
		targets := p.rotatePreview.Targets
		switch {
		case isSpaceKey(msg):
			if p.rotateTargetCursor >= 0 && p.rotateTargetCursor < len(targets) {
				sp := targets[p.rotateTargetCursor].SourcePath
				p.rotateChecked[sp] = !p.rotateChecked[sp]
			}
			return p, nil
		case key == "a":
			sameCount := 0
			sameChecked := 0
			for _, t := range targets {
				if v.Fingerprint == "" || t.Fingerprint == "" || t.Fingerprint == v.Fingerprint {
					sameCount++
					if p.rotateChecked[t.SourcePath] {
						sameChecked++
					}
				}
			}
			targetVal := sameChecked < sameCount
			for _, t := range targets {
				if v.Fingerprint == "" || t.Fingerprint == "" || t.Fingerprint == v.Fingerprint {
					p.rotateChecked[t.SourcePath] = targetVal
				}
			}
			return p, nil
		case key == "j" || key == "down":
			if p.rotateTargetCursor < len(targets)-1 {
				p.rotateTargetCursor++
			} else {
				p.rotateFocus = rotateFocusButtons
				p.rotateConfirmFocus = false
			}
			return p, nil
		case key == "k" || key == "up":
			if p.rotateTargetCursor > 0 {
				p.rotateTargetCursor--
			} else {
				p.rotateFocus = rotateFocusInput
				return p, p.rotateInput.Focus()
			}
			return p, nil
		case key == "tab":
			p.rotateFocus = rotateFocusButtons
			p.rotateConfirmFocus = false
			return p, nil
		case key == "shift+tab":
			p.rotateFocus = rotateFocusInput
			return p, p.rotateInput.Focus()
		}
		return p, nil

	default:
		switch key {
		case "left", "right", "tab", "h", "l":
			p.rotateConfirmFocus = !p.rotateConfirmFocus
		case "up", "shift+tab":
			if len(p.rotatePreview.Targets) > 0 {
				p.rotateFocus = rotateFocusTargets
				p.rotateConfirmFocus = false
				return p, nil
			}
			p.rotateFocus = rotateFocusInput
			p.rotateConfirmFocus = false
			return p, p.rotateInput.Focus()
		case "enter":
			if !p.rotateConfirmFocus {
				p.closeRotateModal()
				return p, func() tea.Msg { return statusToastMsg{text: "Token rotation cancelled"} }
			}
			newVal := p.rotateInput.Value()
			if strings.TrimSpace(newVal) == "" {
				p.rotateFocus = rotateFocusInput
				p.rotateConfirmFocus = false
				focusCmd := p.rotateInput.Focus()
				return p, tea.Batch(focusCmd, func() tea.Msg {
					return statusToastMsg{text: "Enter a new token value before updating", isErr: true}
				})
			}
			var selectedPaths []string
			for _, t := range p.rotatePreview.Targets {
				if p.rotateChecked[t.SourcePath] {
					selectedPaths = append(selectedPaths, t.SourcePath)
				}
			}
			if len(selectedPaths) == 0 && len(p.rotatePreview.Targets) > 0 {
				return p, func() tea.Msg {
					return statusToastMsg{text: "Select at least one target file (down -> space)", isErr: true}
				}
			}
			req := EnvRotateReq{
				VarKey:   v.VarKey,
				Paths:    selectedPaths,
				NewValue: newVal,
			}
			p.closeRotateModal()
			p.rotating = true
			return p, execEnvRotateCmd(backend, req)
		}
	}
	return p, nil
}

func (p *envPanel) closeRotateModal() {
	p.rotateInput.Blur()
	p.rotateInput.SetValue("")
	p.rotateOpen = false
	p.rotateFocus = rotateFocusInput
	p.rotateConfirmFocus = false
	p.rotateInitialized = false
	p.rotatePrechecked = nil
	p.rotateChecked = map[string]bool{}
	p.rotateAnchorPath = ""
	p.rotateTargetCursor = 0
}

func (p envPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.subView == envSubVault {
		return p.vault.ViewMain(width, height, focused, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	if p.drillKey != "" {
		return p.renderDrillView(width, height, focused, st)
	}
	if p.subView == envSubFiles {
		return p.renderFilesView(width, height, innerW, innerH, focused, st)
	}
	return p.renderVarsView(width, height, innerW, innerH, focused, st)
}

func (p envPanel) renderVarsView(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Environment Variables (%d)", len(p.vars))
	if !p.loaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Indexing environment vars...")}, width, height, focused, st)
	}
	if p.loadErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.loadErr, innerW))}, width, height, focused, st)
	}
	if len(p.vars) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No matching environment variables.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.vars))
	marked := make(map[int]bool, p.varSel.Count())
	for i, v := range p.vars {
		if p.varSel.Has(envVarSelectionKey(v)) {
			marked[i] = true
		}
		keyStr := v.VarKey
		if v.IsSecret {
			keyStr = st.AccentText.Render(v.VarKey)
		}
		val := v.ActiveMasked
		if rev, ok := p.revealed[revealKey(v.ActiveSourcePath, v.VarKey)]; ok {
			val = st.OKText.Render(rev)
		}
		shadowed := "—"
		if v.ShadowedCount > 0 {
			shadowed = st.WarnText.Render(fmt.Sprintf("%d", v.ShadowedCount))
		}
		rows = append(rows, []string{
			keyStr,
			nonEmpty(v.Zone, "global"),
			val,
			st.InfoText.Render(v.ActiveSourceKind),
			nonEmpty(collapseHomePath(v.ActiveSourcePath), "—"),
			shadowed,
			fmt.Sprintf("%d", max(1, v.SourceCount)),
		})
	}
	lines := renderSelectableTable([]string{"Key", "Zone", "Value", "Kind", "Active Source", "Shad", "Locs"}, rows, p.cursor, marked, focused, innerW, innerH, st)
	return renderTitledBox(title, "", lines, width, height, focused, st)
}

func (p envPanel) renderDrillView(width, height int, focused bool, st Styles) string {
	v, ok := p.selectedVar()
	if !ok {
		return renderTitledBox("Variable Drill-Down", "", []string{st.Muted.Render("No variable selected.")}, width, height, focused, st)
	}
	innerW := max(12, width-4)
	others := p.otherLocations()

	topH := clamp((height*50)/100, 6, max(6, height-6))
	if len(others) == 0 {
		topH = height
	}
	botH := max(0, height-topH)

	topInnerH := max(2, topH-2)
	activeVal := nonEmpty(p.detail.ActiveMasked, v.ActiveMasked)
	if row, okR := p.focusedLayerRow(); okR && row.SourcePath != "" {
		fk := nonEmpty(row.VarKey, v.VarKey)
		if rev, hasRev := p.revealed[revealKey(row.SourcePath, fk)]; hasRev {
			activeVal = rev + " (revealed)"
		}
	}
	hdr := fmt.Sprintf("%s  ·  Value: %s  ·  Zone: %s  ·  fp:%s",
		st.AccentText.Render(v.VarKey),
		st.OKText.Render(activeVal),
		nonEmpty(v.Zone, "global"),
		nonEmpty(truncate(v.Fingerprint, 12), "—"))
	chainLines := []string{fitStyledWidth(hdr, innerW, st)}
	if p.detailErr != "" {
		chainLines = append(chainLines, st.ErrText.Render(truncate("Detail error: "+p.detailErr, innerW)))
	}
	chainLines = append(chainLines, "")

	rows := make([][]string, 0, len(p.detail.Layers))
	layerMarked := make(map[int]bool, p.layerSel.Count())
	for i, l := range p.detail.Layers {
		if p.layerSel.Has(l.SourcePath) {
			layerMarked[i] = true
		}
		statSt := st.Muted
		switch strings.ToUpper(l.Status) {
		case "ACTIVE":
			statSt = st.OKText
		case "SHADOWED":
			statSt = st.WarnText
		}
		loc := collapseHomePath(l.SourcePath)
		if l.LineNum > 0 {
			loc = fmt.Sprintf("%s:%d", loc, l.LineNum)
		}
		val := l.MaskedValue
		if rev, hasRev := p.revealed[revealKey(l.SourcePath, nonEmpty(l.VarKey, v.VarKey))]; hasRev {
			val = rev
		}
		rows = append(rows, []string{
			statSt.Render(l.Status),
			st.InfoText.Render(l.SourceKind),
			loc,
			val,
		})
	}
	availTop := max(3, topInnerH-len(chainLines))
	if len(rows) == 0 {
		chainLines = append(chainLines, st.Muted.Render("No precedence layers recorded."))
	} else {
		chainLines = append(chainLines, renderSelectableTable([]string{"Status", "Kind", "Location", "Value"}, rows, p.layerCursor, layerMarked, focused && !p.drillFocusOther, innerW, availTop, st)...)
	}
	topBox := renderTitledBox(fmt.Sprintf("Env Detail · %s — Precedence Chain (%d)", v.VarKey, len(p.detail.Layers)), "", chainLines, width, topH, focused && !p.drillFocusOther, st)
	if botH < 4 {
		return topBox
	}

	otherRows := make([][]string, 0, len(others))
	otherMarked := make(map[int]bool, p.layerSel.Count())
	for i, o := range others {
		if p.layerSel.Has(o.SourcePath) {
			otherMarked[i] = true
		}
		relTag := st.WarnText.Render("diff value")
		if o.VarKey != "" && o.VarKey != v.VarKey {
			relTag = st.InfoText.Render("alias:" + o.VarKey)
		} else if o.Fingerprint != "" && o.Fingerprint == v.Fingerprint {
			relTag = st.OKText.Render("same hash")
		}
		loc := collapseHomePath(o.SourcePath)
		if o.LineNum > 0 {
			loc = fmt.Sprintf("%s:%d", loc, o.LineNum)
		}
		val := o.MaskedValue
		fk := nonEmpty(o.VarKey, v.VarKey)
		if rev, hasRev := p.revealed[revealKey(o.SourcePath, fk)]; hasRev {
			val = rev
		}
		otherRows = append(otherRows, []string{
			relTag,
			nonEmpty(o.Zone, "—"),
			loc,
			val,
		})
	}
	otherLines := renderSelectableTable([]string{"Match", "Zone", "Location", "Value"}, otherRows, p.otherCursor, otherMarked, focused && p.drillFocusOther, innerW, max(3, botH-2), st)
	botBox := renderTitledBox(fmt.Sprintf("Other Checkouts Using %s (%d)", v.VarKey, len(others)), "", otherLines, width, botH, focused && p.drillFocusOther, st)
	return lipgloss.JoinVertical(lipgloss.Left, topBox, botBox)
}

func (p envPanel) renderFilesView(width, height, innerW, innerH int, focused bool, st Styles) string {
	title := fmt.Sprintf("Indexed Environment Files (%d)", len(p.files))
	if !p.filesLoaded {
		return renderTitledBox(title, p.PaneStatus(), []string{st.Muted.Render("Loading indexed env files...")}, width, height, focused, st)
	}
	if p.filesErr != "" {
		return renderTitledBox(title, p.PaneStatus(), []string{st.ErrText.Render(truncate(p.filesErr, innerW))}, width, height, focused, st)
	}
	if len(p.files) == 0 {
		return renderTitledBox(title, p.PaneStatus(), []string{st.Muted.Render("No indexed env files matched.")}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.files))
	for _, f := range p.files {
		stTag := st.OKText.Render("INDEXED")
		if f.Missing {
			stTag = st.ErrText.Render("MISSING")
		} else if f.Ignored {
			stTag = st.WarnText.Render("IGNORED")
		}
		rows = append(rows, []string{
			nonEmpty(f.Zone, "—"),
			f.SourceKind,
			fmt.Sprintf("%d", f.VarCount),
			fmt.Sprintf("%d", f.SecretCount),
			stTag,
			collapseHomePath(f.Path),
		})
	}
	lines := renderDataTable([]string{"Zone", "Kind", "Vars", "Sec", "Status", "Path"}, rows, p.fileCursor, focused, innerW, innerH, st)
	return renderTitledBox(title, "", lines, width, height, focused, st)
}

func (p envPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	if p.subView == envSubVault {
		return p.vault.ViewPreview(width, height, focused, borderless, st)
	}
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	if p.subView == envSubFiles {
		f, ok := p.selectedFile()
		if !ok {
			return renderTitledBox("File Preview", "", []string{st.Muted.Render("Select an env file to inspect.")}, width, height, focused, st)
		}
		lines := renderKVTable([][2]string{
			{"Path", collapseHomePath(f.Path)},
			{"Kind", f.SourceKind},
			{"Zone", nonEmpty(f.Zone, "global")},
			{"Counts", fmt.Sprintf("%d vars (%d secrets)", f.VarCount, f.SecretCount)},
			{"Missing / Ign", fmt.Sprintf("%v / %v", f.Missing, f.Ignored)},
		}, innerW, st)
		if borderless {
			return fitLines(strings.Join(lines, "\n"), height)
		}
		return renderTitledBox("File · "+shortEnvFileLabel(f.Path), "", lines, width, height, focused, st)
	}

	v, ok := p.selectedVar()
	if !ok {
		msg := []string{st.Muted.Render("Select an environment variable to view its summary.")}
		if borderless {
			return fitLines(strings.Join(msg, "\n"), height)
		}
		return renderTitledBox("Env Summary", "", msg, width, height, focused, st)
	}

	val := v.ActiveMasked
	if rev, okR := p.revealed[revealKey(v.ActiveSourcePath, v.VarKey)]; okR {
		val = rev + " (revealed)"
	}
	chainCount := fmt.Sprintf("%d layer(s)", max(1, v.SourceCount))
	otherCount := "0 other checkout(s)"
	if strings.EqualFold(p.detail.VarKey, v.VarKey) {
		if len(p.detail.Layers) > 0 {
			chainCount = fmt.Sprintf("%d layer(s)", len(p.detail.Layers))
		}
		otherCount = fmt.Sprintf("%d other checkout(s)", len(p.otherLocations()))
	}
	var all []string
	all = append(all, renderKVTable([][2]string{
		{"Key", st.AccentText.Render(v.VarKey)},
		{"Value", st.OKText.Render(val)},
		{"Zone / Kind", nonEmpty(v.Zone, "global") + " · " + v.ActiveSourceKind},
		{"Active File", nonEmpty(collapseHomePath(v.ActiveSourcePath), "—")},
		{"Fingerprint", nonEmpty(v.Fingerprint, "—")},
		{"Precedence", chainCount},
		{"Cross-Repo", otherCount},
	}, innerW, st)...)

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Summary · "+v.VarKey, "", vis, width, height, focused, st)
}

func (p envPanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.subView == envSubVault {
		return "", false
	}
	if p.subView == envSubVars && p.varFilters.Open {
		groups, sorts := p.varFacetSpec()
		return p.varFilters.ViewModal("Env Vars", groups, sorts, width, height, st)
	}
	if !p.rotateOpen {
		return "", false
	}
	boxW := min(max(58, width-6), 96)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(18, boxW-st.ModalBox.GetHorizontalFrameSize())

	v, _ := p.selectedVar()
	styledIn := styleTextInput(p.rotateInput, max(12, innerW-4), st)

	checkedCount := 0
	for _, t := range p.rotatePreview.Targets {
		if p.rotateChecked[t.SourcePath] {
			checkedCount++
		}
	}

	var parts []string
	for _, wl := range wrapPlainText("Selective Token Update · "+v.VarKey, innerW) {
		parts = append(parts, st.ModalTitle.Render(wl))
	}
	for _, wl := range wrapPlainText("Only checked files will be updated. Deliberately different checkouts stay untouched.", innerW) {
		parts = append(parts, st.Muted.Render(wl))
	}
	parts = append(parts, "", st.SectionHeader.Render("1. Replacement Value"), styledIn.View(), "")

	if p.rotatePreviewErr != "" {
		for _, wl := range wrapPlainText("Preview error: "+p.rotatePreviewErr, innerW) {
			parts = append(parts, st.ErrText.Render(wl))
		}
	} else {
		targetCount := len(p.rotatePreview.Targets)
		parts = append(parts, st.SectionHeader.Render(fmt.Sprintf("2. Target Files (%d checked of %d)", checkedCount, targetCount)))
		for _, wl := range wrapPlainText("space toggle file · a toggle same-value matches", innerW) {
			parts = append(parts, st.Muted.Render(wl))
		}
		if targetCount == 0 {
			parts = append(parts, st.Muted.Render("No writable .env or mise.local.toml targets found."))
		} else {
			rows := make([][]string, 0, targetCount)
			marked := make(map[int]bool, targetCount)
			for i, t := range p.rotatePreview.Targets {
				chk := "·"
				if p.rotateChecked[t.SourcePath] {
					chk = st.OKText.Render("✓")
					marked[i] = true
				}
				matchLbl := "same"
				if v.Fingerprint != "" && t.Fingerprint != "" && t.Fingerprint != v.Fingerprint {
					matchLbl = st.WarnText.Render("diff")
				}
				loc := collapseHomePath(t.SourcePath)
				if t.LineNum > 0 {
					loc = fmt.Sprintf("%s:%d", loc, t.LineNum)
				}
				rows = append(rows, []string{
					chk,
					matchLbl,
					nonEmpty(t.Zone, "—"),
					loc,
					fmt.Sprintf("%s->%s", nonEmpty(t.OldMasked, "****"), nonEmpty(t.NewMasked, "****")),
				})
			}
			tLines := renderSelectableTable([]string{"Use", "Val", "Zone", "File", "Change"}, rows, p.rotateTargetCursor, marked, p.rotateFocus == rotateFocusTargets, innerW, min(8, targetCount+2), st)
			parts = append(parts, tLines...)
		}
	}
	parts = append(parts, "")

	confirmLbl := fmt.Sprintf("Update %d file(s)", checkedCount)
	focusRotate := p.rotateFocus == rotateFocusButtons && p.rotateConfirmFocus
	cancelBtn := renderModalActionButton("Cancel", !focusRotate, false, st)
	rotateBtn := renderModalActionButton(confirmLbl, focusRotate, true, st)
	parts = append(parts,
		st.SectionHeader.Render("3. Confirm Update"),
		lipgloss.JoinHorizontal(lipgloss.Center, cancelBtn, "  ", rotateBtn),
		"",
	)
	for _, wl := range wrapPlainText("↑/↓ or j/k pick files · space toggle · a toggle matching · tab switch focus · enter confirm · esc cancel", innerW) {
		parts = append(parts, st.Muted.Render(wl))
	}

	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal), true
}

func layerIdentityKey(sourcePath string, lineNum int, varKey string) string {
	return fmt.Sprintf("%s:%d\x00%s", strings.TrimSpace(sourcePath), lineNum, strings.TrimSpace(varKey))
}

func shortEnvFileLabel(path string) string {
	slash := filepath.ToSlash(strings.TrimSpace(path))
	if slash == "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(slash, "~/"), "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return slash
}

func normalizeEnvScopeZone(zone string) string {
	z := strings.TrimSpace(zone)
	if strings.EqualFold(z, "all") {
		return ""
	}
	return z
}

func revealKey(sourcePath, varKey string) string {
	return sourcePath + "\x00" + varKey
}

func scanEnvCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envScannedMsg{err: fmt.Errorf("backend unavailable")}
		}
		return envScannedMsg{err: backend.ScanEnv()}
	}
}

func loadEnvVarsCmd(backend Backend, query, zone string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envVarsLoadedMsg{query: query, zone: zone, err: fmt.Errorf("backend unavailable")}
		}
		vars, err := backend.LoadEnvVars(query, zone)
		return envVarsLoadedMsg{query: query, zone: zone, vars: vars, err: err}
	}
}

func loadEnvFilesCmd(backend Backend, zone string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envFilesLoadedMsg{zone: zone, err: fmt.Errorf("backend unavailable")}
		}
		files, err := backend.LoadEnvFiles(zone)
		return envFilesLoadedMsg{zone: zone, files: files, err: err}
	}
}

func loadEnvDetailCmd(backend Backend, varKey, targetDir string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envDetailLoadedMsg{varKey: varKey, err: fmt.Errorf("backend unavailable")}
		}
		d, err := backend.LoadEnvDetail(varKey, targetDir)
		return envDetailLoadedMsg{varKey: varKey, detail: d, err: err}
	}
}

func revealEnvValueCmd(backend Backend, sourcePath, varKey string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envRevealedMsg{sourcePath: sourcePath, varKey: varKey, err: fmt.Errorf("backend unavailable")}
		}
		val, err := backend.RevealEnvValue(sourcePath, varKey)
		return envRevealedMsg{sourcePath: sourcePath, varKey: varKey, value: val, err: err}
	}
}

func previewEnvRotateCmd(backend Backend, req EnvRotateReq) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envRotatePreviewMsg{err: fmt.Errorf("backend unavailable")}
		}
		prev, err := backend.PreviewEnvRotate(req)
		return envRotatePreviewMsg{preview: prev, err: err}
	}
}

func execEnvRotateCmd(backend Backend, req EnvRotateReq) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return envRotatedMsg{err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.ExecEnvRotate(req)
		return envRotatedMsg{result: res, err: err}
	}
}
