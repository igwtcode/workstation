package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

const (
	PanelOverview = iota
	PanelProjects
	PanelPackages
	PanelEnv
	PanelFleet
	PanelWorkstation
	PanelOutput
	panelCount
	workspaceTabCount = 6
)

type SplitMode int

const (
	SplitOff SplitMode = iota
	SplitVertical
	SplitHorizontal
)

func (m SplitMode) String() string {
	switch m {
	case SplitVertical:
		return "V"
	case SplitHorizontal:
		return "H"
	default:
		return "off"
	}
}

type ResourcePanel interface {
	ID() string
	Title() string
	ShortTitle() string
	Badge() string
	SubViews() []string
	ActiveSubView() int
	SetSubView(idx int) ResourcePanel
	OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd)
	SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd)
	IsZoned() bool
	Attention() AttentionLevel
	PaneStatus() string
	FilterQuery() string
	FilterTarget() string
	SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd)
	OnBlur() ResourcePanel
	Breadcrumbs() []string
	StatusChips() []string
	KeyHints(split SplitMode, focusPreview bool) [][2]string
	SelectedCount() int
	ClearSelection() ResourcePanel
	SupportsPreview() bool
	DefaultSplit(width int) SplitMode
	Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd)
	ViewMain(width, height int, focused bool, st Styles) string
	ViewPreview(width, height int, focused bool, borderless bool, st Styles) string
	ViewModal(width, height int, st Styles) (string, bool)
	CLIEquivalent() string
	CapturingInput() bool
	HasOpenModal() bool
	MutationInFlight() bool
	CanPop() bool
	Pop() ResourcePanel
}

type SelectionSet struct {
	items map[string]bool
}

func (s SelectionSet) Has(key string) bool {
	return s.items != nil && s.items[key]
}

func (s SelectionSet) Count() int {
	return len(s.items)
}

func (s SelectionSet) clone() map[string]bool {
	m := make(map[string]bool, len(s.items)+4)
	for k, v := range s.items {
		if v {
			m[k] = true
		}
	}
	return m
}

func (s SelectionSet) Toggle(key string) SelectionSet {
	if key == "" {
		return s
	}
	m := s.clone()
	if m[key] {
		delete(m, key)
	} else {
		m[key] = true
	}
	return SelectionSet{items: m}
}

func (s SelectionSet) SetAll(keys []string) SelectionSet {
	if len(keys) == 0 {
		return SelectionSet{}
	}
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k != "" {
			m[k] = true
		}
	}
	return SelectionSet{items: m}
}

func (s SelectionSet) ToggleAll(keys []string) SelectionSet {
	if len(keys) == 0 {
		return s
	}
	allMarked := true
	for _, k := range keys {
		if k != "" && !s.Has(k) {
			allMarked = false
			break
		}
	}
	m := s.clone()
	if allMarked {
		for _, k := range keys {
			delete(m, k)
		}
	} else {
		for _, k := range keys {
			if k != "" {
				m[k] = true
			}
		}
	}
	return SelectionSet{items: m}
}

func (s SelectionSet) Clear() SelectionSet {
	return SelectionSet{}
}

func renderSplitStage(width, height int, mode SplitMode, focusPreview bool, renderMain, renderPreview func(w, h int, focused bool) string) string {
	if mode == SplitOff || height < 8 || width < 32 {
		return renderMain(width, height, true)
	}
	effective := mode
	if effective == SplitVertical && width < 84 {
		effective = SplitHorizontal
	}
	if effective == SplitVertical {
		wMain := clamp((width*55)/100, 36, max(36, width-28))
		wPrev := max(24, width-wMain-1)
		if wMain+1+wPrev > width {
			wPrev = max(18, width-wMain-1)
		}
		left := renderMain(wMain, height, !focusPreview)
		right := renderPreview(wPrev, height, focusPreview)
		spLines := make([]string, max(1, height))
		for i := range spLines {
			spLines[i] = " "
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Join(spLines, "\n"), right)
	}
	hMain := clamp((height*54)/100, 6, max(6, height-6))
	hPrev := max(5, height-hMain)
	top := renderMain(width, hMain, !focusPreview)
	bot := renderPreview(width, hPrev, focusPreview)
	return lipgloss.JoinVertical(lipgloss.Left, top, bot)
}

func renderK9sHeader(width, lines int, ctxRows [][2]string, globalHints [][2]string, viewHints [][2]string, st Styles) string {
	if width <= 10 || lines <= 0 {
		return ""
	}
	lines = min(lines, 5)

	visCtx := ctxRows
	if len(visCtx) > lines {
		visCtx = visCtx[:lines]
	}

	lblW := 6
	valNatW := 8
	for _, r := range visCtx {
		if w := len(r[0]) + 1; w > lblW {
			lblW = w
		}
		if w := lipgloss.Width(r[1]); w > valNatW {
			valNatW = w
		}
	}
	maxCtxW := clamp((width*28)/100, 20, 34)
	ctxW := min(maxCtxW, lblW+1+valNatW+1)
	if ctxW < 18 {
		ctxW = min(maxCtxW, 18)
	}

	ctxLines := make([]string, lines)
	for row := 0; row < lines; row++ {
		if row < len(visCtx) {
			lbl := padRight(visCtx[row][0]+":", lblW) + " "
			valMax := max(4, ctxW-lipgloss.Width(lbl))
			val := truncate(visCtx[row][1], valMax)
			if st.Roles.NoColor {
				ctxLines[row] = padRight(lbl+val, ctxW)
			} else {
				ctxLines[row] = fitStyledWidth(st.Muted.Render(lbl)+st.RowNormal.Render(val), ctxW, st)
			}
		} else {
			ctxLines[row] = strings.Repeat(" ", ctxW)
		}
	}

	type hintCol struct {
		rows  []string
		w     int
		isSep bool
	}
	packHintCols := func(src [][2]string, keyStyle lipgloss.Style) []hintCol {
		var cols []hintCol
		for i := 0; i < len(src); i += lines {
			chunk := src[i:min(i+lines, len(src))]
			maxK := 4
			maxD := 6
			for _, h := range chunk {
				if kw := len("<" + h[0] + ">"); kw > maxK {
					maxK = kw
				}
				if dw := lipgloss.Width(h[1]); dw > maxD {
					maxD = dw
				}
			}
			keyW := min(maxK, 12)
			descW := min(maxD, 21)
			colW := keyW + 1 + descW
			colRows := make([]string, lines)
			for row := 0; row < lines; row++ {
				if row >= len(chunk) {
					colRows[row] = strings.Repeat(" ", colW)
					continue
				}
				kText := truncate("<"+chunk[row][0]+">", keyW)
				dText := truncate(chunk[row][1], descW)
				if st.Roles.NoColor {
					colRows[row] = padRight(padRight(kText, keyW)+" "+dText, colW)
				} else {
					colRows[row] = fitStyledWidth(keyStyle.Render(padRight(kText, keyW))+" "+st.Muted.Render(dText), colW, st)
				}
			}
			cols = append(cols, hintCol{rows: colRows, w: colW})
		}
		return cols
	}
	trimHintCol := func(chunk [][2]string, availW int, keyStyle lipgloss.Style) hintCol {
		trimmedRows := make([]string, lines)
		keyW := min(10, max(5, availW/2-1))
		descW := max(3, availW-keyW-1)
		for row := 0; row < lines; row++ {
			if row >= len(chunk) {
				trimmedRows[row] = strings.Repeat(" ", availW)
				continue
			}
			kText := truncate("<"+chunk[row][0]+">", keyW)
			dText := truncate(chunk[row][1], descW)
			if st.Roles.NoColor {
				trimmedRows[row] = padRight(padRight(kText, keyW)+" "+dText, availW)
			} else {
				trimmedRows[row] = fitStyledWidth(keyStyle.Render(padRight(kText, keyW))+" "+st.Muted.Render(dText), availW, st)
			}
		}
		return hintCol{rows: trimmedRows, w: availW}
	}

	gCols := packHintCols(globalHints, st.InfoText)
	vCols := packHintCols(viewHints, st.AccentText)

	rem := width - ctxW
	firstGutter := "  "
	colGutter := "  "

	sepChar := "│"
	if st.Roles.NoColor {
		sepChar = "|"
	} else {
		sepChar = st.Muted.Render("│")
	}
	sepRows := make([]string, lines)
	for row := 0; row < lines; row++ {
		sepRows[row] = sepChar
	}

	var fitted []hintCol

	minViewReserve := 0
	if len(vCols) > 0 {
		minViewReserve = len(colGutter) + 1 + len(colGutter) + min(vCols[0].w, 20)
	}
	maxGlobalCols := len(gCols)
	if width < 108 && maxGlobalCols > 1 {
		maxGlobalCols = 1
	}

	for ci := 0; ci < maxGlobalCols; ci++ {
		gc := gCols[ci]
		sepW := len(colGutter)
		if ci == 0 {
			sepW = len(firstGutter)
		}
		needed := sepW + gc.w
		if ci > 0 && rem-needed < minViewReserve {
			break
		}
		if rem-needed < minViewReserve && ci == 0 && len(vCols) > 0 {

			break
		}
		if rem >= needed {
			fitted = append(fitted, gc)
			rem -= needed
		} else {
			break
		}
	}

	if len(fitted) > 0 && len(vCols) > 0 && rem >= len(colGutter)+1+len(colGutter)+8 {
		fitted = append(fitted, hintCol{rows: sepRows, w: 1, isSep: true})
		rem -= len(colGutter) + 1
	}

	for ci, vc := range vCols {
		sepW := len(colGutter)
		if len(fitted) == 0 && ci == 0 {
			sepW = len(firstGutter)
		}
		if rem <= sepW+8 {
			break
		}
		if rem >= sepW+vc.w {
			fitted = append(fitted, vc)
			rem -= sepW + vc.w
		} else {
			start := ci * lines
			chunk := viewHints[start:min(start+lines, len(viewHints))]
			fitted = append(fitted, trimHintCol(chunk, rem-sepW, st.AccentText))
			rem = 0
			break
		}
	}

	out := make([]string, 0, lines)
	for row := 0; row < lines; row++ {
		var b strings.Builder
		b.WriteString(ctxLines[row])
		for ci, fc := range fitted {
			if ci == 0 {
				b.WriteString(firstGutter)
			} else {
				b.WriteString(colGutter)
			}
			b.WriteString(fc.rows[row])
		}
		out = append(out, fitStyledWidth(b.String(), width, st))
	}
	return strings.Join(out, "\n")
}

type hostLoadedMsg struct {
	badge HostBadge
	err   error
}

type statusToastMsg struct {
	text  string
	isErr bool
	id    int
}

type toastClearMsg struct {
	id int
}

type ActivityStatus int

const (
	ActivityRunning ActivityStatus = iota
	ActivityDone
	ActivityError
)

type ActivityEntry struct {
	ID         int
	Text       string
	Status     ActivityStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

type activityMsg struct {
	id    int
	label string
	done  bool
	isErr bool
}

type repoSelectedMsg struct {
	path string
}

type requestThemePreviewMsg struct {
	id string
}

type previewOpenedMsg struct{}

type previewFocusedMsg struct{}

type focusPreviewMsg struct {
	focus       bool
	ensureSplit bool
}

type ghHourlyTickMsg struct{}

type ghRepoCacheLoadedMsg struct {
	repo      string
	state     string
	items     []GHItemRow
	fetchedAt int64
	err       error
}

type confirmRequestMsg struct {
	Title        string
	Body         string
	Warning      string
	ConfirmLabel string
	Dangerous    bool
	StartPanel   int
	StartMsg     tea.Msg
	OnConfirm    func(Backend) tea.Cmd
}

type commandRunRecordedMsg struct {
	Run        CommandRun
	OpenOutput bool
}

type wsCommandFinishedMsg struct {
	RunID  int
	Result CommandRunResult
	Err    error
}

type envVarsLoadedMsg struct {
	query string
	zone  string
	vars  []EnvVarRow
	err   error
}

type envFilesLoadedMsg struct {
	zone  string
	files []EnvFileRow
	err   error
}

type envDetailLoadedMsg struct {
	varKey string
	detail EnvDetail
	err    error
}

type envRevealedMsg struct {
	sourcePath string
	varKey     string
	value      string
	err        error
}

type envRotatePreviewMsg struct {
	preview EnvRotatePreview
	err     error
}

type envRotatedMsg struct {
	result EnvRotateResult
	err    error
}

type envScannedMsg struct {
	err error
}

type projectsCWDMsg struct {
	badge HostBadge
}

type reposLoadedMsg struct {
	zone  string
	repos []RepoRow
	err   error
}

type ghItemsLoadedMsg struct {
	zone  string
	items []GHItemRow
	err   error
}

type ghRepoItemsLoadedMsg struct {
	repo  string
	zone  string
	state string
	items []GHItemRow
	err   error
}

type ghItemDetailLoadedMsg struct {
	kind   string
	repo   string
	number int
	detail GHItemDetail
	err    error
}

type reposPulledMsg struct {
	result RepoPullResult
	cmd    string
	err    error
}

type interactivePullFinishedMsg struct {
	repo      string
	remaining []RepoRow
	err       error
}

type repoClonedMsg struct {
	summary string
	err     error
}

type fleetNodesLoadedMsg struct {
	nodes []FleetNodeRow
	err   error
}

type fleetJoinCmdLoadedMsg struct {
	node string
	cmd  string
	err  error
}

type fleetSyncPreviewMsg struct {
	node    string
	preview FleetSyncPreview
	err     error
}

type fleetSyncedMsg struct {
	node    string
	summary string
	err     error
}

type fleetConnectFinishedMsg struct {
	node string
	err  error
}

type fleetFileDiffLoadedMsg struct {
	node string
	view FleetFileDiffView
	err  error
}

type fleetFileTextDiffMsg struct {
	node    string
	relPath string
	diff    string
	err     error
}

type fleetFileSyncedMsg struct {
	runID  int
	node   string
	dir    string
	res    FleetFileSyncResult
	result CommandRunResult
	err    error
}

type presetsLoadedMsg struct {
	presets []PresetSummary
	err     error
}

type presetResolvedMsg struct {
	id   string
	rows []PresetToolRow
	err  error
}

type pkgDetailLoadedMsg struct {
	key    string
	detail PackageDetail
	err    error
}

type pkgActionDoneMsg struct {
	runID  int
	action string
	result CommandRunResult
	err    error
}

type themesLoadedMsg struct {
	ids      []string
	palettes map[string]config.Palette
	active   string
	err      error
}

type palettePreviewLoadedMsg struct {
	id      string
	palette config.Palette
	err     error
}

type previewThemeSyncMsg struct {
	id string
}

type themeChangedMsg struct {
	id      string
	app     string
	palette config.Palette
	result  *engine.ApplyResult
	err     error
}

type templatesLoadedMsg struct {
	rows []TemplateStatusRow
	err  error
}

type editFinishedMsg struct {
	app string
	err error
}

type cleanupScannedMsg struct {
	items []CleanItem
	err   error
}

type cleanupDoneMsg struct {
	result *engine.ApplyResult
	dryRun bool
	ids    []string
	err    error
}

type cleanupTTYFinishedMsg struct {
	ids        []string
	nonSudoIDs []string
	err        error
}

type historyLoadedMsg struct {
	source string
	rows   []HistoryRow
	err    error
}

type doctorLoadedMsg struct {
	summary DoctorSummary
	err     error
}

type planAppliedMsg struct {
	result *engine.ApplyResult
	cmd    string
	err    error
}

type dbStatusLoadedMsg struct {
	status DBStatusSummary
	err    error
}

type dbBackedUpMsg struct {
	result DBBackupResult
	err    error
}

type undoDoneMsg struct {
	summary string
	err     error
}

type vaultInventoryLoadedMsg struct {
	inv VaultInventoryView
	err error
}

type vaultMutationStartMsg struct {
	action string
}

type vaultActionDoneMsg struct {
	runID  int
	action string
	result CommandRunResult
	err    error
}

type miseLoadedMsg struct {
	inv             MiseInventoryView
	checkedOutdated bool
	err             error
}

type miseEditFinishedMsg struct {
	path string
	err  error
}

type miseMutationStartMsg struct {
	kind string
}

type miseUpgradedMsg struct {
	runID  int
	result CommandRunResult
	err    error
}

type pkgOutdatedLoadedMsg struct {
	items map[string]PackageOutdatedInfo
	err   error
}

type settingsLoadedMsg struct {
	view SettingsView
	err  error
}

type settingSavedMsg struct {
	key  string
	val  string
	view SettingsView
	err  error
}

type configWatchTickMsg struct{}

type configWatchCheckedMsg struct {
	res ConfigWatchResult
	err error
}

type configReloadedMsg struct {
	reason       string
	changedFiles []string
	view         SettingsView
	err          error
}

type configEditFinishedMsg struct {
	path string
	err  error
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncate(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	if ansi.StringWidth(s) <= maxW {
		return s
	}
	return ansi.Truncate(s, maxW, "…")
}

func padRight(s string, width int) string {
	w := ansi.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func fitStyledWidth(s string, width int, st Styles) string {
	if width <= 0 {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if st.Roles.NoColor {
		s = ui.StripANSI(s)
	}
	if ansi.StringWidth(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	if rem := width - ansi.StringWidth(s); rem > 0 {
		s += strings.Repeat(" ", rem)
	}
	return s
}

func fitLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n")
}

func clampViewport(cursor, total, visible int) (int, int) {
	if total <= 0 || visible <= 0 {
		return 0, 0
	}
	if total <= visible {
		return 0, total
	}
	start := cursor - visible/2
	if start < 0 {
		start = 0
	}
	if start+visible > total {
		start = total - visible
	}
	return start, start + visible
}

func formatBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func formatDurationMs(ms int64) string {
	if ms < 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	sec := float64(ms) / 1000.0
	if sec < 60 {
		return fmt.Sprintf("%.1fs", sec)
	}
	mins := int(sec) / 60
	rem := int(sec) % 60
	return fmt.Sprintf("%dm%02ds", mins, rem)
}

func renderProgressBar(ratio float64, width int, st Styles) string {
	if width < 4 {
		width = 4
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	empty := width - filled
	return st.ProgressBar.Render(strings.Repeat("█", filled)) +
		st.ProgressRest.Render(strings.Repeat("░", empty))
}

func severityBadge(sev string, st Styles) string {
	switch strings.ToLower(sev) {
	case "ok":
		return st.BadgeOK.Render("OK")
	case "warn", "warning":
		return st.BadgeWarn.Render("WARN")
	case "err", "error":
		return st.BadgeErr.Render("ERR")
	default:
		return st.BadgeInfo.Render("INFO")
	}
}

func isSpaceKey(msg tea.KeyPressMsg) bool {
	s := msg.String()
	return s == "space" || s == " " || msg.Code == tea.KeySpace
}

func renderTitledBox(title, rightBadge string, bodyLines []string, width, height int, focused bool, st Styles) string {
	if width < 6 {
		width = 6
	}
	if height < 2 {
		height = 2
	}

	bStyle := lipgloss.NewStyle().Faint(true)
	tStyle := lipgloss.NewStyle().Bold(true)
	badgeStyle := lipgloss.NewStyle().Faint(true)
	if focused {
		bStyle = lipgloss.NewStyle().Foreground(st.Accent)
		tStyle = lipgloss.NewStyle().Bold(true).Foreground(st.Accent)
		badgeStyle = lipgloss.NewStyle()
	}
	if st.Roles.NoColor {
		bStyle = lipgloss.NewStyle()
		tStyle = lipgloss.NewStyle()
		badgeStyle = lipgloss.NewStyle()
	}

	avail := width - 2
	var topMid string
	if title != "" {
		badgePlain := ""
		if rightBadge != "" && avail >= 14 {
			maxBadge := max(4, avail/3)
			badgePlain = " " + truncate(rightBadge, maxBadge) + " "
		}
		maxTitle := avail - 2 - lipgloss.Width(badgePlain) - 2
		if badgePlain != "" && maxTitle < 6 {
			badgePlain = ""
			maxTitle = avail - 3
		}
		titlePlain := " " + truncate(title, max(1, maxTitle)) + " "
		fillW := avail - 1 - lipgloss.Width(titlePlain) - lipgloss.Width(badgePlain)
		if badgePlain != "" {
			fillW--
		}
		if fillW < 0 {
			fillW = 0
		}
		if badgePlain != "" {
			topMid = bStyle.Render("─") + tStyle.Render(titlePlain) + bStyle.Render(strings.Repeat("─", fillW)) + badgeStyle.Render(badgePlain) + bStyle.Render("─")
		} else if title == "Help" && avail > lipgloss.Width(titlePlain)+4 {
			leftDash := (avail - lipgloss.Width(titlePlain)) / 2
			rightDash := max(0, avail-lipgloss.Width(titlePlain)-leftDash)
			topMid = bStyle.Render(strings.Repeat("─", leftDash)) + tStyle.Render(titlePlain) + bStyle.Render(strings.Repeat("─", rightDash))
		} else {
			topMid = bStyle.Render("─") + tStyle.Render(titlePlain) + bStyle.Render(strings.Repeat("─", fillW))
		}
		if mw := ansi.StringWidth(topMid); mw > avail {
			topMid = ansi.Truncate(topMid, avail, "")
		}
		if mw := ansi.StringWidth(topMid); mw < avail {
			topMid += bStyle.Render(strings.Repeat("─", avail-mw))
		}
	} else {
		topMid = bStyle.Render(strings.Repeat("─", avail))
	}
	topLine := bStyle.Render("╭") + topMid + bStyle.Render("╮")
	botLine := bStyle.Render("╰") + bStyle.Render(strings.Repeat("─", avail)) + bStyle.Render("╯")

	pad := 1
	if width < 10 {
		pad = 0
	}
	innerW := max(1, width-2-2*pad)
	innerH := max(0, height-2)

	padStr := ""
	if pad > 0 {
		padStr = " "
	}
	vBar := bStyle.Render("│")

	out := make([]string, 0, height)
	out = append(out, topLine)
	for i := 0; i < innerH; i++ {
		raw := ""
		if i < len(bodyLines) {
			raw = bodyLines[i]
		}
		cell := fitStyledWidth(raw, innerW, st)
		out = append(out, vBar+padStr+cell+padStr+vBar)
	}
	out = append(out, botLine)
	return strings.Join(out, "\n")
}

func styleTextInput(ti textinput.Model, width int, st Styles) textinput.Model {
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = st.AccentText
	tiStyles.Focused.Text = st.RowNormal
	tiStyles.Focused.Placeholder = st.Muted
	tiStyles.Blurred.Prompt = st.Muted
	tiStyles.Blurred.Text = st.RowNormal
	tiStyles.Blurred.Placeholder = st.Muted
	ti.SetStyles(tiStyles)
	ti.SetWidth(max(4, width))
	return ti
}

func wrapPlainText(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	rawLines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	out := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		if raw == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(raw)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		var cur strings.Builder
		curW := 0
		for _, w := range words {
			ww := ansi.StringWidth(w)
			if ww > width {
				if curW > 0 {
					out = append(out, cur.String())
					cur.Reset()
					curW = 0
				}
				rem := w
				for ansi.StringWidth(rem) > width {
					chunk := ansi.Truncate(rem, width, "")
					if chunk == "" {
						break
					}
					out = append(out, chunk)
					rem = strings.TrimPrefix(rem, chunk)
				}
				if rem != "" {
					cur.WriteString(rem)
					curW = ansi.StringWidth(rem)
				}
				continue
			}
			if curW == 0 {
				cur.WriteString(w)
				curW = ww
			} else if curW+1+ww <= width {
				cur.WriteByte(' ')
				cur.WriteString(w)
				curW += 1 + ww
			} else {
				out = append(out, cur.String())
				cur.Reset()
				cur.WriteString(w)
				curW = ww
			}
		}
		if curW > 0 {
			out = append(out, cur.String())
		}
	}
	return out
}

func renderModalActionButton(label string, focused, danger bool, st Styles) string {
	if st.Roles.NoColor {
		if focused {
			return "[ " + label + " ]"
		}
		return "  " + label + "  "
	}
	if focused {
		if danger {
			return st.ButtonDanger.Render(label)
		}
		return st.ButtonActive.Render(label)
	}
	return st.ButtonInactive.Render(label)
}

func renderConfirmModal(title, body, warning, confirmLabel string, danger, focusConfirm bool, width, height int, st Styles) string {
	boxW := min(max(36, width-10), 72)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(10, boxW-st.ModalBox.GetHorizontalFrameSize())

	var parts []string
	parts = append(parts, st.ModalTitle.Render(truncate(title, innerW)))
	parts = append(parts, "")
	for _, wl := range wrapPlainText(body, innerW) {
		parts = append(parts, st.RowNormal.Render(wl))
	}
	if warning != "" {
		parts = append(parts, "")
		for _, wl := range wrapPlainText("WARNING: "+warning, innerW) {
			parts = append(parts, st.ErrText.Render(wl))
		}
	}
	parts = append(parts, "")

	cancelBtn := renderModalActionButton("Cancel", !focusConfirm, false, st)
	confirmBtn := renderModalActionButton(confirmLabel, focusConfirm, danger, st)
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, cancelBtn, "  ", confirmBtn)
	if ansi.StringWidth(buttons) > innerW {
		buttons = fitStyledWidth(buttons, innerW, st)
	}
	hint := st.Muted.Render(truncate("left/right/tab switch · enter/y confirm · esc cancel", innerW))
	parts = append(parts, buttons, "", hint)

	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	if height > 0 && lipgloss.Height(modal) > height {
		modal = fitLines(modal, height)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func renderInputField(label string, ti textinput.Model, width int, focused bool, st Styles) string {
	styled := styleTextInput(ti, max(8, width-lipgloss.Width(label)-4), st)
	lblStyle := st.Muted
	if focused {
		lblStyle = st.AccentText
	}
	return lblStyle.Render(label) + " " + styled.View()
}

type navHitSpan struct {
	ID    int
	Start int
	End   int
}

func hitTestSplitStage(x, y, width, stageTop, stageH int, mode SplitMode, supportsPreview bool) (inStage, inPreview bool, relY int) {
	if x < 0 || x >= width || y < stageTop || y >= stageTop+stageH {
		return false, false, 0
	}
	localY := y - stageTop
	if mode == SplitOff || !supportsPreview || stageH < 8 || width < 32 {
		return true, false, localY
	}
	effective := mode
	if effective == SplitVertical && width < 84 {
		effective = SplitHorizontal
	}
	if effective == SplitVertical {
		wMain := clamp((width*55)/100, 36, max(36, width-28))
		if x > wMain {
			return true, true, localY
		}
		return true, false, localY
	}
	hMain := clamp((stageH*54)/100, 6, max(6, stageH-6))
	if localY >= hMain {
		return true, true, localY - hMain
	}
	return true, false, localY
}

func trimModalCanvas(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(ui.StripANSI(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(ui.StripANSI(lines[end-1])) == "" {
		end--
	}
	if start >= end {
		return ""
	}
	lines = lines[start:end]

	minLead := -1
	for _, ln := range lines {
		if strings.TrimSpace(ui.StripANSI(ln)) == "" {
			continue
		}
		lead := 0
		for lead < len(ln) && ln[lead] == ' ' {
			lead++
		}
		if minLead < 0 || lead < minLead {
			minLead = lead
		}
	}
	if minLead > 0 {
		for i, ln := range lines {
			if len(ln) >= minLead && strings.TrimSpace(ln[:minLead]) == "" {
				lines[i] = ln[minLead:]
			} else {
				lines[i] = ""
			}
		}
	}
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " ")
	}
	return strings.Join(lines, "\n")
}

func overlayModalOnStage(baseStage, modal string, width, height int, st Styles) string {
	box := trimModalCanvas(modal)
	if box == "" {
		return baseStage
	}
	if width < 42 || height < 10 || strings.TrimSpace(baseStage) == "" {
		return fitLines(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box), height)
	}
	bw := lipgloss.Width(box)
	bh := strings.Count(box, "\n") + 1
	if bh > height {
		box = fitLines(box, height)
		bh = height
	}
	dimBase := dimStageLines(baseStage, width, height, st)
	px := max(0, (width-bw)/2)
	py := max(0, (height-bh)/2)
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(dimBase),
		lipgloss.NewLayer(box).X(px).Y(py).Z(1),
	)
	out := fitLines(comp.Render(), height)
	if st.Roles.NoColor {
		out = ui.StripANSI(out)
	}
	return out
}
