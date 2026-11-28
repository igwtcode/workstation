package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

type triggerProjectsPullMsg struct {
	pullAll bool
}

var nextActivitySeq atomic.Int64

func newActivityID() int {
	return int(nextActivitySeq.Add(1))
}

func startActivityCmd(id int, label string) tea.Cmd {
	return func() tea.Msg {
		return activityMsg{id: id, label: label, done: false}
	}
}

func finishActivityCmd(id int, label string, isErr bool) tea.Cmd {
	return func() tea.Msg {
		return activityMsg{id: id, label: label, done: true, isErr: isErr}
	}
}

func batchNonNil(cmds ...tea.Cmd) tea.Cmd {
	var out []tea.Cmd
	for _, c := range cmds {
		if c != nil {
			out = append(out, c)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	default:
		return tea.Batch(out...)
	}
}

type Options struct {
	StartTab          string
	Version           string
	Zones             []string
	DefaultZone       string
	HasCDFile         bool
	GHRepo            string
	GHAllMine         bool
	GHState           string
	GHRefresh         bool
	PkgSearchQuery    string
	PkgSearchProvider string
}

type RunResult struct {
	SelectedRepoPath string
}

type confirmModalState struct {
	Title        string
	Body         string
	Warning      string
	ConfirmLabel string
	Dangerous    bool
	FocusConfirm bool
	StartPanel   int
	StartMsg     tea.Msg
	OnConfirm    func(Backend) tea.Cmd
}

type jumpEntry struct {
	panel   int
	subView int
}

type Model struct {
	backend           Backend
	version           string
	panels            []ResourcePanel
	activePanel       int
	prevPanel         int
	jumpStack         []jumpEntry
	pendingKey        string
	splitMode         map[int]SplitMode
	focusPreview      bool
	zoomed            bool
	mouseEnabled      bool
	cmdBar            commandBar
	confirmModal      *confirmModalState
	actionMenu        *actionMenuState
	nextRunID         int
	host              HostBadge
	zones             []string
	scopeZone         string
	hasCDFile         bool
	themeIDs          []string
	presetIDs         []string
	repoSlugs         []string
	palettes          map[string]config.Palette
	activeTheme       string
	previewTheme      string
	styles            Styles
	spinner           spinner.Model
	pendingLoads      int
	activities        []ActivityEntry
	width             int
	height            int
	showHelp          bool
	helpScroll        int
	toast             string
	toastErr          bool
	toastID           int
	findingsWarn      int
	findingsErr       int
	selectedRepoPath  string
	autoReloadEnabled bool
	autoReloadSecs    int
	ghRefreshMins     int
	defaultSplitPref  string
	borderedBarPref   string
	defaultZonePref   string
	configFingerprint string
	explicitScopeZone bool
	explicitGHRepo    string
}

func normalizeZones(raw []string, defaultZone string) ([]string, string) {
	scope := strings.TrimSpace(defaultZone)
	if strings.EqualFold(scope, "all") {
		scope = ""
	}
	zones := []string{"all"}
	seen := map[string]bool{"all": true}
	for _, z := range raw {
		z = strings.TrimSpace(z)
		if z == "" || strings.EqualFold(z, "all") || seen[z] {
			continue
		}
		seen[z] = true
		zones = append(zones, z)
	}
	if len(zones) == 1 {
		for _, fallback := range []string{"personal", "work"} {
			if !seen[fallback] {
				seen[fallback] = true
				zones = append(zones, fallback)
			}
		}
	}
	if scope != "" && !seen[scope] {
		zones = append(zones, scope)
	}
	return zones, scope
}

func NewModel(backend Backend, opts Options) Model {
	pal := DefaultPalette()
	st := NewStyles(pal)
	sp := spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(st.Accent)),
	)

	zones, scopeZone := normalizeZones(opts.Zones, opts.DefaultZone)
	startPanel, startSub, startFocusGH, startGHKind := resolveStartTab(opts.StartTab)

	outPanel := newOutputPanel()
	if hp, ok := backend.(OutputHistoryPathProvider); ok {
		path, writable := hp.OutputHistoryPath()
		outPanel = outPanel.WithHistory(path, writable)
	}

	panels := []ResourcePanel{
		newOverviewPanel(),
		newProjectsPanel(opts.HasCDFile),
		newPackagesPanel(0),
		newEnvPanel(),
		newFleetPanel(),
		newWorkstationPanel(),
		outPanel,
	}
	if scopeZone != "" {
		for i := range panels {
			if panels[i].IsZoned() {
				panels[i], _ = panels[i].SetScope(scopeZone, nil)
			}
		}
	}
	if startSub > 0 && startPanel >= 0 && startPanel < len(panels) {
		panels[startPanel] = panels[startPanel].SetSubView(startSub)
	}
	if startPanel == PanelProjects {
		if pp, ok := panels[PanelProjects].(projectsPanel); ok {
			if startGHKind != "" {
				pp.ghKindFilter = startGHKind
			}
			if startFocusGH {
				pp.ghFocused = true
			}
			if opts.GHRepo != "" {
				pp.userPickedRepo = true
				pp.ghAllMine = false
				pp.ghRepoOverride = opts.GHRepo
				if backend != nil {
					if pp.repoLoading == nil {
						pp.repoLoading = map[string]bool{}
					}
					pp.repoLoading[repoCacheKey(opts.GHRepo, nonEmpty(strings.ToLower(strings.TrimSpace(opts.GHState)), "open"))] = true
				}
			} else if opts.GHAllMine {
				pp.userPickedRepo = true
				pp.ghAllMine = true
				pp.ghRepoOverride = ""
			}
			if st := strings.ToLower(strings.TrimSpace(opts.GHState)); st != "" {
				pp.ghStateFilter = st
			}
			pp.ghForceRefresh = opts.GHRefresh
			panels[PanelProjects] = pp
		}
	}
	if startPanel == PanelPackages && startSub == pkgSubSearch {
		if sp, ok := panels[PanelPackages].(packagesPanel); ok {
			sp.setSearchProvider(opts.PkgSearchProvider)
			if q := strings.TrimSpace(opts.PkgSearchQuery); q != "" {
				sp.searchQuery = q
				sp.searchInput.SetValue(q)
				sp.searchInputFocused = false
			} else {
				sp.searchInputFocused = true
			}
			panels[PanelPackages] = sp
		}
	}

	m := Model{
		backend:           backend,
		version:           opts.Version,
		panels:            panels,
		activePanel:       startPanel,
		prevPanel:         PanelReposDefault(startPanel),
		splitMode:         map[int]SplitMode{},
		focusPreview:      startFocusGH,
		mouseEnabled:      true,
		cmdBar:            newCommandBar(),
		nextRunID:         outPanel.MaxRunID(),
		zones:             zones,
		scopeZone:         scopeZone,
		explicitScopeZone: strings.TrimSpace(opts.DefaultZone) != "",
		explicitGHRepo:    strings.TrimSpace(opts.GHRepo),
		hasCDFile:         opts.HasCDFile,
		palettes:          map[string]config.Palette{pal.ID: pal},
		activeTheme:       pal.ID,
		styles:            st,
		spinner:           sp,
		pendingLoads:      15,
		width:             120,
		height:            34,
		autoReloadEnabled: true,
		autoReloadSecs:    2,
		ghRefreshMins:     60,
		defaultSplitPref:  "auto",
		borderedBarPref:   "auto",
	}
	m.syncCommandBarOptions()
	return m
}

func buildSearchProvidersForHost(badge HostBadge) []string {
	out := []string{"all"}
	seen := map[string]bool{"all": true}
	add := func(p string) {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if badge.OS == "darwin" {
		add("brew")
		add("cask")
	}
	for _, pm := range badge.PkgManagers {
		add(pm)
		if pm == "brew" {
			add("cask")
		}
	}
	add("mise")
	if len(out) <= 2 {
		return nil
	}
	return out
}

func PanelReposDefault(start int) int {
	if start == PanelOutput {
		return PanelOverview
	}
	return start
}

func Run(ctx context.Context, backend Backend, opts Options) error {
	_, err := RunWithResult(ctx, backend, opts)
	return err
}

func RunWithResult(ctx context.Context, backend Backend, opts Options) (RunResult, error) {
	p := tea.NewProgram(NewModel(backend, opts), tea.WithContext(ctx))
	finalModel, err := p.Run()
	var res RunResult
	if fm, ok := finalModel.(Model); ok {
		res.SelectedRepoPath = fm.SelectedRepoPath()
	}
	return res, err
}

func resolveStartTab(start string) (panel int, subView int, focusGH bool, ghKind string) {
	switch strings.ToLower(strings.TrimSpace(start)) {
	case "", "overview", "status", "home", "1", "8":
		return PanelOverview, 0, false, ""
	case "projects", "project", "repos", "repo", "git", "gg", "2":
		return PanelProjects, projSubRepos, false, ""
	case "gh":
		return PanelProjects, projSubRepos, true, "all"
	case "gh-prs":
		return PanelProjects, projSubPRs, false, "pr"
	case "gh-issues":
		return PanelProjects, projSubIssues, false, "issue"
	case "prs", "pr":
		return PanelProjects, projSubRepos, true, "pr"
	case "issues", "issue":
		return PanelProjects, projSubRepos, true, "issue"
	case "packages", "pkg", "pkgs", "installed", "system", "sys", "3":
		return PanelPackages, pkgSubInstalled, false, ""
	case "pkg-search", "search", "browse", "pkg-browse":
		return PanelPackages, pkgSubSearch, false, ""
	case "mise-tools", "mise-outdated":
		return PanelPackages, pkgSubMiseTools, false, ""
	case "mise", "mise-configs":
		return PanelPackages, pkgSubMiseConfigs, false, ""
	case "cleanup", "clean":
		return PanelPackages, pkgSubCleanup, false, ""
	case "env", "secrets", "tokens", "vars", "4":
		return PanelEnv, envSubVars, false, ""
	case "envfiles", "files":
		return PanelEnv, envSubFiles, false, ""
	case "vault", "db", "1password", "op":
		return PanelEnv, envSubVault, false, ""
	case "fleet", "nodes", "tailscale", "vm", "5":
		return PanelFleet, fleetSubNodes, false, ""
	case "filesync", "sync-files", "diff-files", "fleet-diff":
		return PanelFleet, fleetSubFileSync, false, ""
	case "workstation", "ws", "doctor", "doc", "6":
		return PanelWorkstation, wsSubDoctor, false, ""
	case "themes", "theme":
		return PanelWorkstation, wsSubThemes, false, ""
	case "presets", "preset":
		return PanelWorkstation, wsSubPresets, false, ""
	case "settings", "prefs", "config", "7":
		return PanelWorkstation, wsSubSettings, false, ""
	case "watched", "watched-files":
		return PanelWorkstation, wsSubWatchedFiles, false, ""
	case "history", "hist":
		return PanelWorkstation, wsSubHistory, false, ""
	case "output", "out", "jobs", "0":
		return PanelOutput, 0, false, ""
	default:
		return PanelOverview, 0, false, ""
	}
}

func (m Model) ActivePanel() int            { return m.activePanel }
func (m Model) ActiveTab() int              { return m.activePanel }
func (m Model) FocusPreview() bool          { return m.focusPreview }
func (m Model) Zoomed() bool                { return m.zoomed }
func (m Model) MouseEnabled() bool          { return m.mouseEnabled }
func (m Model) HasConfirmModal() bool       { return m.confirmModal != nil }
func (m Model) HasActionMenu() bool         { return m.actionMenu != nil }
func (m Model) ShowHelp() bool              { return m.showHelp }
func (m Model) HelpScroll() int             { return m.helpScroll }
func (m Model) CommandBarActive() bool      { return m.cmdBar.Active() }
func (m Model) SelectedRepoPath() string    { return m.selectedRepoPath }
func (m Model) ActiveTheme() string         { return m.activeTheme }
func (m Model) PreviewTheme() string        { return m.previewTheme }
func (m Model) Styles() Styles              { return m.styles }
func (m Model) ScopeZone() string           { return m.scopeZone }
func (m Model) Zones() []string             { return append([]string(nil), m.zones...) }
func (m Model) Toast() string               { return m.toast }
func (m Model) Activities() []ActivityEntry { return append([]ActivityEntry(nil), m.activities...) }

func (m Model) OutputPanel() outputPanel {
	if op, ok := m.panels[PanelOutput].(outputPanel); ok {
		return op
	}
	return newOutputPanel()
}

func (m Model) activeSplitKey() int {
	return m.activePanel*16 + m.panels[m.activePanel].ActiveSubView()
}

func (m Model) ActiveSplit() SplitMode {
	p := m.panels[m.activePanel]
	if !p.SupportsPreview() {
		return SplitOff
	}
	if mode, ok := m.splitMode[m.activeSplitKey()]; ok {
		return mode
	}
	switch m.defaultSplitPref {
	case "off":
		return SplitOff
	case "vertical":
		return SplitVertical
	case "horizontal":
		return SplitHorizontal
	}
	w := m.width
	if w <= 0 {
		w = 120
	}
	return p.DefaultSplit(w)
}

func (m *Model) allocRunID() int {
	m.nextRunID++
	return m.nextRunID
}

func scheduleGHTickCmd(mins int) tea.Cmd {
	if mins <= 0 {
		mins = 60
	}
	return tea.Tick(time.Duration(mins)*time.Minute, func(time.Time) tea.Msg {
		return ghHourlyTickMsg{}
	})
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.spinner.Tick,
		scheduleGHTickCmd(m.ghRefreshMins),
		scheduleConfigWatchTickCmd(2 * time.Second),
		loadHostCmd(m.backend),
		loadEnvVarsCmd(m.backend, "", m.scopeZone),
		loadReposCmd(m.backend, false, m.scopeZone),
		loadGHItemsCmd(m.backend, false, m.scopeZone),
		loadMiseInventoryCmd(m.backend, m.scopeZone, false, false),
		loadMiseInventoryCmd(m.backend, m.scopeZone, true, false),
		loadFleetNodesCmd(m.backend),
		loadPresetsCmd(m.backend),
		loadPkgOutdatedCmd(m.backend),
		loadThemesCmd(m.backend),
		loadTemplatesCmd(m.backend),
		scanCleanupCmd(m.backend),
		loadHistoryCmd(m.backend, "", 2000),
		loadDoctorCmd(m.backend),
		loadDBStatusCmd(m.backend),
		loadVaultInventoryCmd(m.backend),
		loadSettingsCmd(m.backend),
	}
	if m.explicitGHRepo != "" && m.backend != nil {
		state := "open"
		refresh := false
		if pp, ok := m.panels[PanelProjects].(projectsPanel); ok {
			state = nonEmpty(pp.ghStateFilter, "open")
			refresh = pp.ghForceRefresh
		}
		cmds = append(cmds,
			loadCachedRepoGHItemsCmd(m.backend, m.explicitGHRepo, state),
			loadRepoGHItemsCmd(m.backend, m.explicitGHRepo, m.scopeZone, state, refresh),
		)
	}
	if m.activePanel == PanelPackages && m.panels[PanelPackages].ActiveSubView() == pkgSubSearch && m.backend != nil {
		if sp, ok := m.panels[PanelPackages].(packagesPanel); ok {
			q := sp.searchQuery
			prov := sp.searchProvider()
			focus := q == ""
			cmds = append(cmds, func() tea.Msg {
				return systemPkgSearchMsg{query: q, provider: prov, focusInput: focus}
			})
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.activeOperations() <= 0 {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case activityMsg:
		wasIdle := m.activeOperations() == 0
		m.applyActivityMsg(msg)
		if wasIdle && m.activeOperations() > 0 {
			return m, m.spinner.Tick
		}
		return m, nil

	case hostLoadedMsg:
		m.doneLoad()
		if msg.err != nil {
			return m, m.setToast("Host summary failed: "+msg.err.Error(), true)
		}
		m.host = msg.badge
		if msg.badge.Theme != "" && m.activeTheme == DefaultPalette().ID {
			m.activeTheme = msg.badge.Theme
		}
		m.syncOverview(msg)
		nextM, projCmd := m.routeToPanel(PanelProjects, projectsCWDMsg{badge: msg.badge})
		if provs := buildSearchProvidersForHost(msg.badge); len(provs) > 0 {
			var pkgCmd tea.Cmd
			nextM, pkgCmd = nextM.(Model).routeToPanel(PanelPackages, systemHostProvidersMsg{providers: provs})
			return nextM, batchNonNil(projCmd, pkgCmd)
		}
		return nextM, projCmd

	case ghHourlyTickMsg:
		return m, tea.Batch(
			scheduleGHTickCmd(m.ghRefreshMins),
			loadGHItemsCmd(m.backend, false, m.scopeZone),
		)

	case configWatchTickMsg:
		if !m.autoReloadEnabled {
			return m, nil
		}
		interval := time.Duration(max(1, m.autoReloadSecs)) * time.Second
		return m, tea.Batch(
			scheduleConfigWatchTickCmd(interval),
			checkConfigWatchCmd(m.backend, m.configFingerprint),
		)

	case configWatchCheckedMsg:
		if msg.err != nil {
			return m, nil
		}
		if m.configFingerprint == "" {
			m.configFingerprint = msg.res.Fingerprint
			return m, nil
		}
		if msg.res.Changed {
			m.configFingerprint = msg.res.Fingerprint
			reason := strings.Join(msg.res.ChangedFiles, ", ")
			return m, reloadConfigCmd(m.backend, reason)
		}
		if msg.res.Fingerprint != "" {
			m.configFingerprint = msg.res.Fingerprint
		}
		return m, nil

	case settingsLoadedMsg:
		m.doneLoad()
		var liveCmd tea.Cmd
		if msg.err == nil {
			liveCmd = m.applyLiveSettings(msg.view, false)
		}
		updated, pCmd := m.routeToPanel(PanelWorkstation, msg)
		return updated, batchNonNil(liveCmd, pCmd)

	case settingSavedMsg:
		var liveCmd tea.Cmd
		if msg.err == nil {
			liveCmd = m.applyLiveSettings(msg.view, true)
		}
		updated, pCmd := m.routeToPanel(PanelWorkstation, msg)
		return updated, batchNonNil(liveCmd, pCmd)

	case configReloadedMsg:
		var liveCmd tea.Cmd
		var toastCmd tea.Cmd
		if msg.err == nil {
			liveCmd = m.applyLiveSettings(msg.view, true)
			label := "Config reloaded"
			if len(msg.changedFiles) > 0 {
				label = "Auto-reloaded: " + strings.Join(msg.changedFiles, ", ")
			} else if msg.reason != "" && msg.reason != "manual" && msg.reason != "command" {
				label = "Auto-reloaded: " + msg.reason
			}
			toastCmd = m.setToast(label, false)
		}
		updated, pCmd := m.routeToPanel(PanelWorkstation, msg)
		return updated, batchNonNil(liveCmd, pCmd, toastCmd)

	case configEditFinishedMsg:
		return m.routeToPanel(PanelWorkstation, msg)

	case focusPreviewMsg:
		if msg.ensureSplit && m.panels[m.activePanel].SupportsPreview() && m.ActiveSplit() == SplitOff {
			if m.width >= 96 {
				m.splitMode[m.activeSplitKey()] = SplitVertical
			} else {
				m.splitMode[m.activeSplitKey()] = SplitHorizontal
			}
		}
		m.focusPreview = msg.focus && m.ActiveSplit() != SplitOff
		if m.focusPreview {
			return m.routeToPanel(m.activePanel, previewFocusedMsg{})
		}
		return m.routeToPanel(m.activePanel, msg)

	case statusToastMsg:
		if msg.text == "" {
			m.toastID++
			m.toast = ""
			m.toastErr = false
			return m, nil
		}
		return m, m.setToast(msg.text, msg.isErr)

	case toastClearMsg:
		if msg.id == m.toastID {
			m.toast = ""
			m.toastErr = false
		}
		return m, nil

	case repoSelectedMsg:
		m.selectedRepoPath = msg.path
		return m, tea.Quit

	case requestThemePreviewMsg:
		return m.toggleThemePreview(msg.id)

	case confirmRequestMsg:
		m.confirmModal = &confirmModalState{
			Title:        msg.Title,
			Body:         msg.Body,
			Warning:      msg.Warning,
			ConfirmLabel: nonEmpty(msg.ConfirmLabel, "Confirm"),
			Dangerous:    msg.Dangerous,
			FocusConfirm: false,
			StartPanel:   msg.StartPanel,
			StartMsg:     msg.StartMsg,
			OnConfirm:    msg.OnConfirm,
		}
		return m, nil

	case commandRunRecordedMsg:
		m.recordOutputRun(msg.Run)
		if msg.OpenOutput {
			if m.activePanel != PanelOutput {
				m.prevPanel = m.activePanel
			}
			m.activePanel = PanelOutput
			m.focusPreview = false
		}
		return m, nil

	case overviewJumpMsg:
		subCmd := m.switchTo(msg.panel, msg.subView)
		return m, subCmd

	case jumpToPresetsMsg:
		subCmd := m.switchTo(PanelWorkstation, wsSubPresets)
		return m, subCmd

	case presetActivatedMsg:
		sw := systemSwitchPresetMsg{preset: msg.preset}
		m1, cmdPkg := m.routeToPanel(PanelPackages, sw)
		m2, cmdWs := m1.(Model).routeToPanel(PanelWorkstation, sw)
		nextM := m2.(Model)
		if msg.jumpToPackages {
			subCmd := nextM.switchTo(PanelPackages, pkgSubInstalled)
			return nextM, batchNonNil(cmdPkg, cmdWs, subCmd)
		}
		toastCmd := nextM.setToast("Active preset: "+msg.preset, false)
		return nextM, batchNonNil(cmdPkg, cmdWs, toastCmd)

	case launchStreamCmdMsg:
		return m.launchWSCommandWithOpts(msg.Args, msg.OpenOutput, msg.Dangerous)

	case cancelOutputRunMsg:
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			cmdStr, cancelled, nextOp := op.CancelRun(msg.RunID)
			m.panels[PanelOutput] = nextOp
			if cancelled {
				return m, m.setToast("Cancelling "+cmdStr+"...", false)
			}
		}
		return m, nil

	case outputStreamLineMsg:
		var nextCmd tea.Cmd
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			nextOp, step := op.AppendLine(msg.RunID, msg.Line)
			m.panels[PanelOutput] = nextOp
			if h, ok := nextOp.StreamHandle(msg.RunID); ok {
				if step != "" && h.actID != 0 {
					m.applyActivityMsg(activityMsg{
						id:    h.actID,
						label: h.cmdStr + " · " + step,
						done:  false,
					})
				}
				nextCmd = waitOutputStreamCmd(h)
			}
		}
		return m, nextCmd

	case outputStreamDoneMsg:
		var actCmd tea.Cmd
		var wasCancelled bool
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			h, nextOp := op.RemoveStream(msg.RunID)
			nextOp = nextOp.CompleteRun(msg.RunID, msg.Result, msg.Err)
			m.panels[PanelOutput] = nextOp
			for _, r := range nextOp.runs {
				if r.ID == msg.RunID {
					wasCancelled = r.Cancelled
					break
				}
			}
			if h != nil && h.actID != 0 {
				actCmd = finishActivityCmd(h.actID, h.cmdStr, msg.Err != nil || msg.Result.IsErr)
			}
		}
		if wasCancelled || errors.Is(msg.Err, context.Canceled) {
			return m, batchNonNil(actCmd, m.setToast(fmt.Sprintf("Cancelled: %s", msg.Result.Command), true))
		}
		if msg.Err != nil || msg.Result.IsErr {
			return m, batchNonNil(actCmd, m.setToast(fmt.Sprintf("%s failed (exit %d)", msg.Result.Command, msg.Result.ExitCode), true))
		}
		return m, batchNonNil(actCmd, m.setToast(fmt.Sprintf("Completed: %s", msg.Result.Command), false))

	case wsCommandFinishedMsg:
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			m.panels[PanelOutput] = op.CompleteRun(msg.RunID, msg.Result, msg.Err)
		}
		if msg.Err != nil {
			return m, m.setToast(fmt.Sprintf("%s failed (exit %d)", msg.Result.Command, msg.Result.ExitCode), true)
		}
		return m, m.setToast(fmt.Sprintf("Completed: %s", msg.Result.Command), false)

	case envVarsLoadedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelEnv, msg)
	case envFilesLoadedMsg, envDetailLoadedMsg, envRevealedMsg, envRotatePreviewMsg, envRotatedMsg, envScannedMsg:
		if rot, ok := msg.(envRotatedMsg); ok {
			m.recordSimpleRun("ws env rotate", rot.result.Summary, rot.err, true)
		}
		return m.routeToPanel(PanelEnv, msg)

	case reposLoadedMsg:
		m.doneLoad()
		m.syncOverview(msg)
		if msg.err == nil && len(msg.repos) > 0 {
			seen := map[string]bool{}
			var slugs []string
			for _, r := range msg.repos {
				if slug := strings.TrimSpace(extractGHSlugFromRemote(r.RemoteURL)); slug != "" && !seen[strings.ToLower(slug)] {
					seen[strings.ToLower(slug)] = true
					slugs = append(slugs, slug)
				}
			}
			m.repoSlugs = slugs
			m.syncCommandBarOptions()
		}
		return m.routeToPanel(PanelProjects, msg)
	case ghItemsLoadedMsg:
		m.doneLoad()
		m.syncOverview(msg)
		return m.routeToPanel(PanelProjects, msg)
	case reposPulledMsg:
		if msg.err == nil && len(msg.result.NeedsInteractive) == 0 {
			lines := msg.result.Lines
			if len(lines) == 0 && msg.result.Summary != "" {
				lines = []string{msg.result.Summary}
			}
			cmdStr := nonEmpty(msg.cmd, "ws repo pull")
			m.recordOutputRun(CommandRun{
				ID:      m.allocRunID(),
				Command: cmdStr,
				Lines:   lines,
				IsErr:   msg.result.Failed > 0,
				ExitCode: func() int {
					if msg.result.Failed > 0 {
						return 1
					}
					return 0
				}(),
			})
		} else if msg.err != nil {
			m.recordSimpleRun(nonEmpty(msg.cmd, "ws repo pull"), msg.err.Error(), msg.err, false)
		}
		return m.routeToPanel(PanelProjects, msg)
	case interactivePullFinishedMsg, repoClonedMsg, repoEditorFinishedMsg, repoLazygitFinishedMsg, ghRepoCacheLoadedMsg, ghRepoItemsLoadedMsg, ghItemDetailLoadedMsg, ghDashActionDoneMsg:
		return m.routeToPanel(PanelProjects, msg)

	case miseLoadedMsg:
		if !msg.checkedOutdated {
			m.doneLoad()
		}
		return m.routeToPanel(PanelPackages, msg)
	case miseEditFinishedMsg:
		return m.routeToPanel(PanelPackages, msg)
	case miseUpgradedMsg:
		m.recordSimpleRun(msg.result.Command, msg.result.Output, msg.err, false)
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			id := msg.runID
			if id == 0 {
				id = m.allocRunID()
			}
			m.panels[PanelOutput] = op.CompleteRun(id, msg.result, msg.err)
		}
		return m.routeToPanel(PanelPackages, msg)

	case fleetNodesLoadedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelFleet, msg)
	case fleetSyncedMsg:
		m.recordSimpleRun("ws fleet sync "+msg.node, msg.summary, msg.err, true)
		return m.routeToPanel(PanelFleet, msg)
	case fleetJoinCmdLoadedMsg, fleetSyncPreviewMsg, fleetConnectFinishedMsg,
		fleetFileDiffLoadedMsg, fleetFileTextDiffMsg:
		return m.routeToPanel(PanelFleet, msg)
	case fleetFileSyncedMsg:
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			id := msg.runID
			if id == 0 {
				id = m.allocRunID()
			}
			m.panels[PanelOutput] = op.CompleteRun(id, msg.result, msg.err)
		}
		return m.routeToPanel(PanelFleet, msg)

	case presetsLoadedMsg:
		m.doneLoad()
		m.syncOverview(msg)
		if msg.err == nil {
			var pids []string
			for _, pr := range msg.presets {
				if pr.ID != "" {
					pids = append(pids, pr.ID)
				}
			}
			m.presetIDs = pids
			m.syncCommandBarOptions()
		}
		m1, cmdPkg := m.routeToPanel(PanelPackages, msg)
		m2, cmdWs := m1.(Model).routeToPanel(PanelWorkstation, msg)
		return m2, batchNonNil(cmdPkg, cmdWs)
	case presetResolvedMsg:
		m1, cmdPkg := m.routeToPanel(PanelPackages, msg)
		m2, cmdWs := m1.(Model).routeToPanel(PanelWorkstation, msg)
		return m2, batchNonNil(cmdPkg, cmdWs)
	case pkgOutdatedLoadedMsg, pkgUntrackedLoadedMsg:
		m.syncOverview(msg)
		return m.routeToPanel(PanelPackages, msg)
	case pkgDetailLoadedMsg, pkgListMutatedMsg, updateAllPreviewReadyMsg, pkgSearchResultsMsg, systemPkgSearchMsg, systemHostProvidersMsg:
		return m.routeToPanel(PanelPackages, msg)
	case editFinishedMsg:
		return m.routeToPanel(PanelWorkstation, msg)
	case pkgTTYFinishedMsg:
		summary := fmt.Sprintf("Completed in terminal: %s", nonEmpty(msg.command, "pkg "+msg.action))
		if msg.err != nil {
			summary = msg.err.Error()
		}
		m.recordSimpleRun(nonEmpty(msg.command, "ws pkg "+msg.action), summary, msg.err, false)
		next, pCmd := m.routeToPanel(PanelPackages, msg)
		if msg.action == "update-all" && m.backend != nil {
			return next, batchNonNil(pCmd, loadDoctorCmd(m.backend), loadHostCmd(m.backend), loadTemplatesCmd(m.backend), loadThemesCmd(m.backend))
		}
		return next, pCmd
	case pkgActionDoneMsg:
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			id := msg.runID
			if id == 0 {
				id = m.allocRunID()
			}
			m.panels[PanelOutput] = op.CompleteRun(id, msg.result, msg.err)
		}
		return m.routeToPanel(PanelPackages, msg)
	case templatesLoadedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelWorkstation, msg)
	case cleanupScannedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelPackages, msg)
	case cleanupDoneMsg:
		if !msg.dryRun {
			summary := "Cleanup complete"
			if msg.result != nil {
				summary = fmt.Sprintf("Cleaned %d item(s)", msg.result.Applied)
			}
			m.recordSimpleRun("ws clean", summary, msg.err, true)
		} else if msg.result != nil {
			m.recordOutputRun(CommandRun{
				ID:      m.allocRunID(),
				Command: "ws clean --dry-run",
				Lines:   []string{fmt.Sprintf("Dry-run: %d cleanup item(s) evaluated (no files deleted)", len(msg.ids))},
			})
		}
		return m.routeToPanel(PanelPackages, msg)
	case cleanupTTYFinishedMsg:
		return m.routeToPanel(PanelPackages, msg)
	case historyLoadedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelWorkstation, msg)

	case themesLoadedMsg:
		m.doneLoad()
		if msg.err == nil {
			m.themeIDs = msg.ids
			m.syncCommandBarOptions()
			if m.palettes == nil {
				m.palettes = map[string]config.Palette{}
			}
			for id, pal := range msg.palettes {
				m.palettes[id] = pal
			}
			if msg.active != "" {
				m.activeTheme = msg.active
				m.host.Theme = msg.active
				m.previewTheme = ""
				if pal, ok := m.palettes[msg.active]; ok {
					m.applyLocalStyles(pal)
				}
			}
		}
		return m.routeToPanel(PanelWorkstation, msg)

	case palettePreviewLoadedMsg:
		if msg.err != nil {
			toastCmd := m.setToast("Preview failed: "+msg.err.Error(), true)
			if msg.id != "" && m.previewTheme == msg.id {
				m.previewTheme = ""
				if cached, ok := m.palettes[m.activeTheme]; ok && len(cached.Tokens) > 0 {
					m.applyLocalStyles(cached)
				}
				synced, syncCmd := m.routeToPanel(PanelWorkstation, previewThemeSyncMsg{id: ""})
				return synced, batchNonNil(syncCmd, toastCmd)
			}
			return m, toastCmd
		}
		if msg.id != "" && len(msg.palette.Tokens) > 0 {
			if m.palettes == nil {
				m.palettes = map[string]config.Palette{}
			}
			m.palettes[msg.id] = msg.palette
			if nonEmpty(m.previewTheme, m.activeTheme) == msg.id {
				m.applyLocalStyles(msg.palette)
			}
		}
		return m, nil

	case themeChangedMsg:
		if msg.err != nil {
			cmdTxt := "ws theme set " + msg.id
			if msg.app != "" {
				cmdTxt += " --app " + msg.app
			}
			m.recordSimpleRun(cmdTxt, msg.err.Error(), msg.err, false)
			toastCmd := m.setToast("Theme failed: "+msg.err.Error(), true)
			updated, cmd := m.routeToPanel(PanelWorkstation, msg)
			return updated, batchNonNil(toastCmd, cmd)
		}
		var toastCmd tea.Cmd
		if msg.id != "" {
			if msg.app != "" {
				m.recordSimpleRun("ws theme set "+msg.id+" --app "+msg.app, "Applied theme "+msg.id+" to "+msg.app, nil, false)
				toastCmd = m.setToast("Applied theme "+msg.id+" to "+msg.app, false)
			} else {
				m.activeTheme = msg.id
				m.host.Theme = msg.id
				m.previewTheme = ""
				if len(msg.palette.Tokens) > 0 {
					if m.palettes == nil {
						m.palettes = map[string]config.Palette{}
					}
					m.palettes[msg.id] = msg.palette
					m.applyLocalStyles(msg.palette)
				}
				m.recordSimpleRun("ws theme set "+msg.id, "Applied theme "+msg.id, nil, false)
				toastCmd = m.setToast("Theme active: "+msg.id, false)
			}
		}
		wasIdle := m.activeOperations() == 0
		m.pendingLoads++
		var spinCmd tea.Cmd
		if wasIdle && m.activeOperations() > 0 {
			spinCmd = m.spinner.Tick
		}
		updated, cmd := m.routeToPanel(PanelWorkstation, msg)
		return updated, batchNonNil(toastCmd, cmd, spinCmd, loadTemplatesCmd(m.backend))

	case doctorLoadedMsg:
		m.doneLoad()
		if msg.err == nil {
			m.findingsWarn, m.findingsErr = countFindings(msg.summary.Findings)
		}
		m.syncOverview(msg)
		return m.routeToPanel(PanelWorkstation, msg)
	case dbStatusLoadedMsg:
		m.doneLoad()
		m.syncOverview(msg)
		m1, cmdWs := m.routeToPanel(PanelWorkstation, msg)
		m2, cmdEnv := m1.(Model).routeToPanel(PanelEnv, msg)
		return m2, batchNonNil(cmdWs, cmdEnv)
	case planAppliedMsg:
		var lines []string
		if msg.err != nil {
			lines = []string{"Apply error: " + msg.err.Error()}
		} else if msg.result != nil {
			lines = []string{fmt.Sprintf("Applied %d step(s), skipped %d", msg.result.Applied, msg.result.Skipped)}
		}
		m.recordOutputRun(CommandRun{
			ID:      m.allocRunID(),
			Command: nonEmpty(msg.cmd, "ws apply"),
			Lines:   lines,
			IsErr:   msg.err != nil,
		})
		return m.routeToPanel(PanelWorkstation, msg)
	case undoDoneMsg:
		m.recordSimpleRun("ws undo", msg.summary, msg.err, true)
		m1, cmdWs := m.routeToPanel(PanelWorkstation, msg)
		m2, cmdEnv := m1.(Model).routeToPanel(PanelEnv, msg)
		return m2, batchNonNil(cmdWs, cmdEnv)
	case vaultInventoryLoadedMsg:
		m.doneLoad()
		return m.routeToPanel(PanelEnv, msg)
	case vaultActionDoneMsg:
		if op, ok := m.panels[PanelOutput].(outputPanel); ok {
			id := msg.runID
			if id == 0 {
				id = m.allocRunID()
			}
			m.panels[PanelOutput] = op.CompleteRun(id, msg.result, msg.err)
		}
		return m.routeToPanel(PanelEnv, msg)
	case dbBackedUpMsg:
		m.recordSimpleRun("ws db backup", msg.result.Summary, msg.err, false)
		m1, cmdEnv := m.routeToPanel(PanelEnv, msg)
		m2, cmdWs := m1.(Model).routeToPanel(PanelWorkstation, msg)
		return m2, batchNonNil(cmdEnv, cmdWs)

	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)
	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m.routeToPanel(m.activePanel, msg)
}

func (m *Model) syncOverview(msg tea.Msg) {
	if PanelOverview >= 0 && PanelOverview < len(m.panels) && m.panels[PanelOverview] != nil {
		nextP, _ := m.panels[PanelOverview].Update(msg, m.backend, SplitOff, false)
		m.panels[PanelOverview] = nextP
	}
}

func (m *Model) cancelAllStreams() {
	if op, ok := m.panels[PanelOutput].(outputPanel); ok {
		op.CancelAll()
	}
}

func (m *Model) recordOutputRun(run CommandRun) {
	if run.ID <= 0 {
		run.ID = m.allocRunID()
	}
	if op, ok := m.panels[PanelOutput].(outputPanel); ok {
		m.panels[PanelOutput] = op.RecordRun(run)
	}
}

func (m *Model) recordSimpleRun(cmdStr, summary string, err error, dangerous bool) {
	lines := splitOutputLines(summary)
	exitCode := 0
	if err != nil {
		exitCode = 1
		if len(lines) == 0 {
			lines = []string{"error: " + err.Error()}
		}
	}
	m.recordOutputRun(CommandRun{
		ID:        m.allocRunID(),
		Command:   cmdStr,
		Lines:     lines,
		ExitCode:  exitCode,
		IsErr:     err != nil,
		Dangerous: dangerous,
	})
}

func (m *Model) setToast(text string, isErr bool) tea.Cmd {
	m.toastID++
	id := m.toastID
	m.toast = text
	m.toastErr = isErr
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg {
		return toastClearMsg{id: id}
	})
}

func (m Model) activeOperations() int {
	n := m.pendingLoads
	for _, a := range m.activities {
		if a.Status == ActivityRunning {
			n++
		}
	}
	return n
}

func (m Model) latestRunningActivity() (ActivityEntry, bool) {
	for i := len(m.activities) - 1; i >= 0; i-- {
		if m.activities[i].Status == ActivityRunning {
			return m.activities[i], true
		}
	}
	return ActivityEntry{}, false
}

func (m *Model) applyActivityMsg(msg activityMsg) {
	now := time.Now()
	status := ActivityRunning
	if msg.done {
		if msg.isErr {
			status = ActivityError
		} else {
			status = ActivityDone
		}
	}

	if !msg.done {
		if msg.id != 0 {
			for i := len(m.activities) - 1; i >= 0; i-- {
				if m.activities[i].ID == msg.id {
					m.activities[i].Text = msg.label
					m.activities[i].Status = ActivityRunning
					return
				}
			}
		}
		m.activities = append(m.activities, ActivityEntry{
			ID:        msg.id,
			Text:      msg.label,
			Status:    ActivityRunning,
			StartedAt: now,
		})
		if len(m.activities) > 20 {
			m.activities = append([]ActivityEntry(nil), m.activities[len(m.activities)-20:]...)
		}
		return
	}

	matched := false
	for i := len(m.activities) - 1; i >= 0; i-- {
		a := &m.activities[i]
		if a.Status != ActivityRunning {
			continue
		}
		if msg.id != 0 && a.ID == msg.id {
			a.Status = status
			if msg.label != "" {
				a.Text = msg.label
			}
			a.FinishedAt = now
			matched = true
			break
		}
		if msg.id == 0 && msg.label != "" && a.Text == msg.label {
			a.Status = status
			a.FinishedAt = now
			matched = true
			break
		}
		if msg.id == 0 && msg.label == "" {
			a.Status = status
			a.FinishedAt = now
			matched = true
			break
		}
	}
	if !matched && msg.id == 0 && msg.label != "" {
		for i := len(m.activities) - 1; i >= 0; i-- {
			a := &m.activities[i]
			if a.Status == ActivityRunning && a.ID == 0 {
				a.Status = status
				a.Text = msg.label
				a.FinishedAt = now
				matched = true
				break
			}
		}
	}
	if !matched && msg.label != "" {
		m.activities = append(m.activities, ActivityEntry{
			ID:         msg.id,
			Text:       msg.label,
			Status:     status,
			StartedAt:  now,
			FinishedAt: now,
		})
		if len(m.activities) > 20 {
			m.activities = append([]ActivityEntry(nil), m.activities[len(m.activities)-20:]...)
		}
	}
}

func (m *Model) doneLoad() {
	if m.pendingLoads > 0 {
		m.pendingLoads--
	}
}

func (m *Model) applyLocalStyles(pal config.Palette) {
	m.styles = NewStyles(pal)
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.styles.Accent)
}

func (m *Model) applyLiveSettings(sv SettingsView, fromReload bool) tea.Cmd {
	wasAuto := m.autoReloadEnabled
	if sv.Fingerprint != "" {
		m.configFingerprint = sv.Fingerprint
	}
	m.autoReloadEnabled = sv.AutoReload
	if sv.Mouse != nil {
		m.mouseEnabled = *sv.Mouse
	} else {
		for _, it := range sv.Items {
			if it.Key == "tui.mouse" {
				m.mouseEnabled = !strings.EqualFold(strings.TrimSpace(it.Value), "false")
				break
			}
		}
	}
	if sv.AutoReloadSecs > 0 {
		m.autoReloadSecs = sv.AutoReloadSecs
	}
	if sv.GHRefreshMins > 0 {
		m.ghRefreshMins = sv.GHRefreshMins
	}
	if sv.DefaultSplit != "" {
		m.defaultSplitPref = sv.DefaultSplit
	}
	if sv.BorderedBar != "" {
		m.borderedBarPref = sv.BorderedBar
	}

	if fp, ok := m.panels[PanelFleet].(fleetPanel); ok && !fp.fileLoading {
		if !fromReload && len(fp.fileDiff.Entries) == 0 {
			fp.fileIncludeEnv = sv.FleetIncludeEnv
			fp.fileIncludeDB = sv.FleetIncludeDB
			m.panels[PanelFleet] = fp
		}
	}

	var cmds []tea.Cmd
	if !wasAuto && sv.AutoReload {
		cmds = append(cmds, scheduleConfigWatchTickCmd(time.Duration(max(1, m.autoReloadSecs))*time.Second))
	}

	if sv.Theme != "" && sv.Theme != m.activeTheme {
		m.activeTheme = sv.Theme
		m.host.Theme = sv.Theme
		m.previewTheme = ""
		if len(m.panels) > PanelWorkstation && m.panels[PanelWorkstation] != nil {
			np, syncCmd := m.panels[PanelWorkstation].Update(previewThemeSyncMsg{id: ""}, m.backend, m.ActiveSplit(), m.focusPreview)
			m.panels[PanelWorkstation] = np
			if syncCmd != nil {
				cmds = append(cmds, syncCmd)
			}
		}
		if pal, ok := m.palettes[sv.Theme]; ok {
			m.applyLocalStyles(pal)
		} else if m.backend != nil {
			cmds = append(cmds, loadPalettePreviewCmd(m.backend, sv.Theme))
		}
	}

	if sv.DefaultZone != m.defaultZonePref {
		m.defaultZonePref = sv.DefaultZone
		if strings.TrimSpace(sv.DefaultZone) != "" && (fromReload || (!m.explicitScopeZone && m.explicitGHRepo == "")) {
			nextZone := strings.TrimSpace(sv.DefaultZone)
			if strings.EqualFold(nextZone, "all") {
				nextZone = ""
			}
			if nextZone != m.scopeZone {
				m.scopeZone = nextZone
				for i := range m.panels {
					if m.panels[i].IsZoned() {
						var scCmd tea.Cmd
						m.panels[i], scCmd = m.panels[i].SetScope(m.scopeZone, m.backend)
						if scCmd != nil {
							cmds = append(cmds, scCmd)
						}
					}
				}
			}
		}
	}

	if fromReload && m.backend != nil {
		cmds = append(cmds,
			loadHostCmd(m.backend),
			loadVaultInventoryCmd(m.backend),
			loadDoctorCmd(m.backend),
			loadPresetsCmd(m.backend),
			loadThemesCmd(m.backend),
			loadTemplatesCmd(m.backend),
			loadEnvVarsCmd(m.backend, "", m.scopeZone),
			loadReposCmd(m.backend, false, m.scopeZone),
			loadMiseInventoryCmd(m.backend, m.scopeZone, false, true),
		)
	}
	m.syncCommandBarOptions()
	return batchNonNil(cmds...)
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.cancelAllStreams()
		return m, tea.Quit
	}
	if key == "ctrl+x" {
		if op, ok := m.panels[PanelOutput].(outputPanel); ok && op.HasRunningStream() {
			cmdStr, cancelled, nextOp := op.CancelRun(0)
			m.panels[PanelOutput] = nextOp
			if cancelled {
				return m, m.setToast("Cancelling "+cmdStr+"...", false)
			}
		}
	}

	if m.confirmModal != nil {
		switch key {
		case "left", "right", "tab", "shift+tab", "h", "l":
			m.confirmModal.FocusConfirm = !m.confirmModal.FocusConfirm
			return m, nil
		case "y":
			cm := m.confirmModal
			m.confirmModal = nil
			var cmds []tea.Cmd
			if cm.StartMsg != nil && cm.StartPanel >= 0 && cm.StartPanel < len(m.panels) {
				nextP, sc := m.panels[cm.StartPanel].Update(cm.StartMsg, m.backend, m.ActiveSplit(), false)
				m.panels[cm.StartPanel] = nextP
				if sc != nil {
					cmds = append(cmds, sc)
				}
			}
			if cm.OnConfirm != nil {
				if cc := cm.OnConfirm(m.backend); cc != nil {
					cmds = append(cmds, cc)
				}
			}
			return m, batchNonNil(cmds...)
		case "enter":
			if m.confirmModal.FocusConfirm {
				cm := m.confirmModal
				m.confirmModal = nil
				var cmds []tea.Cmd
				if cm.StartMsg != nil && cm.StartPanel >= 0 && cm.StartPanel < len(m.panels) {
					nextP, sc := m.panels[cm.StartPanel].Update(cm.StartMsg, m.backend, m.ActiveSplit(), false)
					m.panels[cm.StartPanel] = nextP
					if sc != nil {
						cmds = append(cmds, sc)
					}
				}
				if cm.OnConfirm != nil {
					if cc := cm.OnConfirm(m.backend); cc != nil {
						cmds = append(cmds, cc)
					}
				}
				return m, batchNonNil(cmds...)
			}
			m.confirmModal = nil
			return m, m.setToast("Cancelled", false)
		case "n", "esc", "q":
			m.confirmModal = nil
			return m, m.setToast("Cancelled", false)
		}
		return m, nil
	}

	if m.actionMenu != nil {
		chosen, closed := m.actionMenu.updateKey(msg)
		if closed {
			m.actionMenu = nil
		}
		if chosen != nil {
			if ap, ok := m.panels[m.activePanel].(ActionMenuProvider); ok {
				nextPanel, cmd := ap.RunRowAction(chosen.Key, m.backend, m.ActiveSplit(), m.focusPreview)
				m.panels[m.activePanel] = nextPanel
				return m, cmd
			}
		}
		return m, nil
	}

	if m.cmdBar.Active() {
		nextBar, barCmd, submitted, filterChanged, _ := m.cmdBar.Update(msg)
		m.cmdBar = nextBar
		var cmds []tea.Cmd
		if barCmd != nil {
			cmds = append(cmds, barCmd)
		}
		if filterChanged {
			var fCmd tea.Cmd
			m.panels[m.activePanel], fCmd = m.panels[m.activePanel].SetFilterQuery(m.cmdBar.input.Value(), m.backend)
			if fCmd != nil {
				cmds = append(cmds, fCmd)
			}
		}
		if submitted != nil {
			execModel, execCmd := m.executeCommand(*submitted)
			if execCmd != nil {
				cmds = append(cmds, execCmd)
			}
			return execModel, batchNonNil(cmds...)
		}
		return m, batchNonNil(cmds...)
	}

	if m.showHelp {
		switch key {
		case "?", "esc", "q":
			m.pendingKey = ""
			m.showHelp = false
			m.helpScroll = 0
			return m, nil
		case "g":
			if m.pendingKey == "g" {
				m.pendingKey = ""
				m.helpScroll = 0
			} else {
				m.pendingKey = "g"
			}
			return m, nil
		}
		m.pendingKey = ""
		if mot := classifyVimMotion(key); mot != motionNone {
			w := m.width
			if w <= 0 {
				w = 80
			}
			h := m.height
			if h <= 0 {
				h = 24
			}
			stageH := max(5, h-(m.headerLines(h)+m.cmdBarLines(h)+2))
			maxSc := m.helpMaxScroll(w, stageH)
			m.helpScroll, _ = applyScrollMotion(m.helpScroll, maxSc, max(4, stageH-4), mot)
		}
		return m, nil
	}

	active := m.panels[m.activePanel]
	if active.CapturingInput() || active.HasOpenModal() {
		m.pendingKey = ""
		return m.routeToPanel(m.activePanel, msg)
	}

	if m.pendingKey != "" {
		prefix := m.pendingKey
		m.pendingKey = ""
		if prefix == "g" {
			if key == "g" {
				return m.routeToPanel(m.activePanel, tea.KeyPressMsg{Code: tea.KeyHome})
			}
			if key == "esc" {
				return m, nil
			}
		}
	}

	switch key {
	case "g":
		m.pendingKey = "g"
		return m, nil
	case "ctrl+o":
		if n := len(m.jumpStack); n > 0 {
			prev := m.jumpStack[n-1]
			m.jumpStack = m.jumpStack[:n-1]
			if prev.panel >= 0 && prev.panel < len(m.panels) {
				oldPanel := m.activePanel
				if m.activePanel != PanelOutput {
					m.prevPanel = m.activePanel
				}
				if oldPanel != prev.panel {
					m.panels[oldPanel] = m.panels[oldPanel].OnBlur()
				}
				m.activePanel = prev.panel
				m.focusPreview = false
				p := m.panels[prev.panel]
				if prev.subView >= 0 && prev.subView < len(p.SubViews()) {
					p = p.SetSubView(prev.subView)
					var subCmd tea.Cmd
					p, subCmd = p.OnSubViewChanged(m.backend)
					m.panels[prev.panel] = p
					return m, subCmd
				}
			}
		}
		return m, nil
	case "q", "esc":
		return m.handleBackOrQuit()
	case "?":
		m.showHelp = true
		m.helpScroll = 0
		return m, nil
	case "a":
		if ap, ok := active.(ActionMenuProvider); ok {
			if actions := ap.RowActions(m.ActiveSplit(), m.focusPreview); len(actions) > 0 {
				title := active.ShortTitle()
				if subs := active.SubViews(); len(subs) > 0 {
					idx := clamp(active.ActiveSubView(), 0, len(subs)-1)
					title += " · " + subs[idx]
				}
				m.actionMenu = &actionMenuState{
					Title:   title,
					Actions: actions,
				}
				return m, nil
			}
		}
		return m, m.setToast("No extra actions in this view", false)
	case ":":
		var cmd tea.Cmd
		m.cmdBar, cmd = m.cmdBar.OpenCommand("")
		return m, cmd
	case "/":
		var cmd tea.Cmd
		m.cmdBar, cmd = m.cmdBar.OpenFilter(active.FilterQuery())
		return m, cmd
	case "ctrl+y":
		return m, copyCmd(active.CLIEquivalent(), "CLI command")
	case "0":
		m.pushJumpPoint()
		oldPanel := m.activePanel
		if m.activePanel == PanelOutput {
			m.activePanel = m.prevPanel
		} else {
			m.prevPanel = m.activePanel
			m.activePanel = PanelOutput
		}
		if oldPanel != m.activePanel {
			m.panels[oldPanel] = m.panels[oldPanel].OnBlur()
		}
		m.focusPreview = false
		m.zoomed = false
		return m, nil
	case "1", "2", "3", "4", "5", "6":
		idx := int(key[0] - '1')
		m.switchPanel(idx)
		return m, nil
	case "P":
		subCmd := m.switchTo(PanelWorkstation, wsSubPresets)
		return m, subCmd
	case "p":
		if active.SupportsPreview() {
			sk := m.activeSplitKey()
			cur := m.ActiveSplit()
			switch cur {
			case SplitOff:
				if m.width >= 84 {
					m.splitMode[sk] = SplitVertical
				} else {
					m.splitMode[sk] = SplitHorizontal
				}
				return m.routeToPanel(m.activePanel, previewOpenedMsg{})
			case SplitVertical:
				m.splitMode[sk] = SplitHorizontal
				return m.routeToPanel(m.activePanel, previewOpenedMsg{})
			default:
				m.splitMode[sk] = SplitOff
				m.focusPreview = false
				m.zoomed = false
				return m.routeToPanel(m.activePanel, focusPreviewMsg{focus: false})
			}
		}
		return m, nil
	case "=":
		return m.toggleZoom()
	case "tab", "shift+tab":
		if m.ActiveSplit() != SplitOff {
			m.focusPreview = !m.focusPreview
			if m.focusPreview {
				return m.routeToPanel(m.activePanel, previewFocusedMsg{})
			}
			return m.routeToPanel(m.activePanel, focusPreviewMsg{focus: false})
		}
		return m.routeToPanel(m.activePanel, msg)
	case "[", "]":
		if m.activePanel == PanelOutput {
			return m.routeToPanel(PanelOutput, msg)
		}
		subViews := active.SubViews()
		if len(subViews) > 1 {
			delta := 1
			if key == "[" {
				delta = -1
			}
			m.pushJumpPoint()
			nextIdx := (active.ActiveSubView() + delta + len(subViews)) % len(subViews)
			nextPanel := active.SetSubView(nextIdx)
			nextPanel, viewCmd := nextPanel.OnSubViewChanged(m.backend)
			m.panels[m.activePanel] = nextPanel
			m.focusPreview = false
			m.zoomed = false
			toastCmd := m.setToast("View: "+subViews[nextIdx], false)
			return m, batchNonNil(toastCmd, viewCmd)
		}
		return m, nil
	case "z":
		return m.cycleScopeZone()
	}

	return m.routeToPanel(m.activePanel, msg)
}

func (m Model) handleBackOrQuit() (tea.Model, tea.Cmd) {
	active := m.panels[m.activePanel]
	if m.zoomed {
		m.zoomed = false
		return m, nil
	}
	if active.CanPop() {
		popRepoFromList := false
		if pp, ok := active.(projectsPanel); ok && pp.repoInfoMode && pp.repoInfoFromRepos {
			popRepoFromList = true
		}
		m.panels[m.activePanel] = active.Pop()
		if m.ActiveSplit() == SplitOff || popRepoFromList {
			m.focusPreview = false
			return m.routeToPanel(m.activePanel, focusPreviewMsg{focus: false})
		}
		return m, nil
	}
	if active.FilterQuery() != "" {
		var fCmd tea.Cmd
		m.panels[m.activePanel], fCmd = active.SetFilterQuery("", m.backend)
		return m, fCmd
	}
	if m.activePanel == PanelOutput {
		m.activePanel = m.prevPanel
		m.focusPreview = false
		return m, nil
	}
	if m.focusPreview && m.ActiveSplit() != SplitOff {
		m.focusPreview = false
		return m.routeToPanel(m.activePanel, focusPreviewMsg{focus: false})
	}
	if active.SelectedCount() > 0 {
		m.panels[m.activePanel] = active.ClearSelection()
		return m, m.setToast("Selection cleared", false)
	}
	if m.previewTheme != "" {
		return m.clearThemePreview()
	}
	if m.hasMutationInFlight() {
		return m, m.setToast("Mutation in progress (press ctrl+c to quit)", true)
	}
	return m, tea.Quit
}

func (m *Model) pushJumpPoint() {
	entry := jumpEntry{
		panel:   m.activePanel,
		subView: m.panels[m.activePanel].ActiveSubView(),
	}
	if n := len(m.jumpStack); n > 0 && m.jumpStack[n-1] == entry {
		return
	}
	m.jumpStack = append(m.jumpStack, entry)
	if len(m.jumpStack) > 32 {
		m.jumpStack = m.jumpStack[len(m.jumpStack)-32:]
	}
}

func (m *Model) switchPanel(idx int) {
	if idx < 0 || idx >= len(m.panels) {
		return
	}
	oldPanel := m.activePanel
	curSub := m.panels[m.activePanel].ActiveSubView()
	if idx != m.activePanel {
		m.pushJumpPoint()
		m.panels[oldPanel] = m.panels[oldPanel].OnBlur()
	}
	_ = curSub
	if m.activePanel != PanelOutput {
		m.prevPanel = m.activePanel
	}
	m.activePanel = idx
	m.focusPreview = false
	m.zoomed = false
	m.actionMenu = nil
	m.helpScroll = 0
	m.pendingKey = ""
}

func (m *Model) switchTo(panelIdx, subView int) tea.Cmd {
	oldPanel := m.activePanel
	if panelIdx != m.activePanel || (subView >= 0 && subView != m.panels[panelIdx].ActiveSubView()) {
		m.pushJumpPoint()
	}
	if oldPanel != panelIdx {
		m.panels[oldPanel] = m.panels[oldPanel].OnBlur()
	}
	if m.activePanel != PanelOutput {
		m.prevPanel = m.activePanel
	}
	m.activePanel = panelIdx
	m.focusPreview = false
	m.zoomed = false
	m.actionMenu = nil
	m.helpScroll = 0
	m.pendingKey = ""
	p := m.panels[panelIdx]
	if subView >= 0 && subView < len(p.SubViews()) {
		p = p.SetSubView(subView)
		var cmd tea.Cmd
		p, cmd = p.OnSubViewChanged(m.backend)
		m.panels[panelIdx] = p
		return cmd
	}
	return nil
}

func (m Model) executeCommand(inv commandInvocation) (tea.Model, tea.Cmd) {
	cmdName := strings.ToLower(strings.TrimSpace(inv.Cmd))
	if cmdName == "" {
		return m, nil
	}

	freeQuery := func() string {
		var parts []string
		for _, a := range inv.Args {
			if strings.Contains(a, ":") || strings.Contains(a, "=") {
				continue
			}
			parts = append(parts, a)
		}
		return strings.Join(parts, " ")
	}

	switch cmdName {
	case "q", "quit":
		if m.hasMutationInFlight() {
			return m, m.setToast("Mutation in progress (press ctrl+c to quit)", true)
		}
		return m, tea.Quit
	case "overview", "home", "1", "8":
		m.switchPanel(PanelOverview)
		return m, nil
	case "repo", "repos", "projects", "2":
		c := m.switchTo(PanelProjects, projSubRepos)
		m.focusPreview = false
		nextP, fCmd := m.panels[PanelProjects].Update(focusPreviewMsg{focus: false}, m.backend, m.ActiveSplit(), false)
		m.panels[PanelProjects] = nextP
		var qCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelProjects], qCmd = m.panels[PanelProjects].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd, qCmd)
	case "gh", "prs", "pr", "issues", "issue":
		c := m.switchTo(PanelProjects, projSubRepos)
		if m.ActiveSplit() == SplitOff && m.panels[PanelProjects].SupportsPreview() {
			if m.width >= 96 {
				m.splitMode[m.activeSplitKey()] = SplitVertical
			} else {
				m.splitMode[m.activeSplitKey()] = SplitHorizontal
			}
		}
		m.focusPreview = m.ActiveSplit() != SplitOff
		kind := "all"
		if cmdName == "prs" || cmdName == "pr" {
			kind = "pr"
		} else if cmdName == "issues" || cmdName == "issue" {
			kind = "issue"
		}
		var ghCmd tea.Cmd
		var nextP ResourcePanel
		nextP, ghCmd = m.panels[PanelProjects].Update(ghCommandMsg{
			kind:    kind,
			repo:    inv.KV["repo"],
			state:   inv.KV["state"],
			allMine: inv.KV["all"] != "",
			query:   freeQuery(),
		}, m.backend, m.ActiveSplit(), m.focusPreview)
		m.panels[PanelProjects] = nextP
		return m, batchNonNil(c, ghCmd)
	case "packages", "pkg", "pkgs", "installed", "system", "sys", "3":
		c := m.switchTo(PanelPackages, pkgSubInstalled)
		var pCmd, wCmd, fCmd tea.Cmd
		if preset := inv.KV["preset"]; preset != "" {
			sw := systemSwitchPresetMsg{preset: preset}
			var np, nw ResourcePanel
			np, pCmd = m.panels[PanelPackages].Update(sw, m.backend, m.ActiveSplit(), false)
			m.panels[PanelPackages] = np
			nw, wCmd = m.panels[PanelWorkstation].Update(sw, m.backend, m.ActiveSplit(), false)
			m.panels[PanelWorkstation] = nw
		}
		if q := freeQuery(); q != "" {
			m.panels[PanelPackages], fCmd = m.panels[PanelPackages].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, pCmd, wCmd, fCmd)
	case "pkg-search", "search", "browse", "psearch":
		c := m.switchTo(PanelPackages, pkgSubSearch)
		q := freeQuery()
		prov := inv.KV["provider"]
		var np ResourcePanel
		var sCmd tea.Cmd
		np, sCmd = m.panels[PanelPackages].Update(systemPkgSearchMsg{
			query:      q,
			provider:   prov,
			focusInput: q == "",
		}, m.backend, m.ActiveSplit(), false)
		m.panels[PanelPackages] = np
		return m, batchNonNil(c, sCmd)
	case "mise-tools", "mise-outdated":
		c := m.switchTo(PanelPackages, pkgSubMiseTools)
		var fCmd tea.Cmd
		if q := freeQuery(); q != "" {
			m.panels[PanelPackages], fCmd = m.panels[PanelPackages].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "mise", "mise-configs":
		c := m.switchTo(PanelPackages, pkgSubMiseConfigs)
		var fCmd tea.Cmd
		if q := freeQuery(); q != "" {
			m.panels[PanelPackages], fCmd = m.panels[PanelPackages].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "clean", "cleanup":
		c := m.switchTo(PanelPackages, pkgSubCleanup)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelPackages], fCmd = m.panels[PanelPackages].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "env", "secrets", "vars", "tokens", "4":
		c := m.switchTo(PanelEnv, envSubVars)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelEnv], fCmd = m.panels[PanelEnv].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "envfiles", "files":
		c := m.switchTo(PanelEnv, envSubFiles)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelEnv], fCmd = m.panels[PanelEnv].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "vault", "db", "1password", "op":
		return m, m.switchTo(PanelEnv, envSubVault)
	case "fleet", "nodes", "5":
		c := m.switchTo(PanelFleet, fleetSubNodes)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelFleet], fCmd = m.panels[PanelFleet].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "filesync", "sync-files", "diff-files":
		c := m.switchTo(PanelFleet, fleetSubFileSync)
		return m, c
	case "workstation", "ws", "doctor", "doc", "6":
		return m, m.switchTo(PanelWorkstation, wsSubDoctor)
	case "themes", "theme":
		c := m.switchTo(PanelWorkstation, wsSubThemes)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelWorkstation], fCmd = m.panels[PanelWorkstation].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "presets", "preset":
		c := m.switchTo(PanelWorkstation, wsSubPresets)
		var pCmd, wCmd tea.Cmd
		if len(inv.Args) > 0 && !strings.Contains(inv.Args[0], ":") {
			sw := systemSwitchPresetMsg{preset: inv.Args[0]}
			var np, nw ResourcePanel
			np, pCmd = m.panels[PanelPackages].Update(sw, m.backend, m.ActiveSplit(), false)
			m.panels[PanelPackages] = np
			nw, wCmd = m.panels[PanelWorkstation].Update(sw, m.backend, m.ActiveSplit(), false)
			m.panels[PanelWorkstation] = nw
		}
		return m, batchNonNil(c, pCmd, wCmd)
	case "settings", "prefs", "config", "7":
		c := m.switchTo(PanelWorkstation, wsSubSettings)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelWorkstation], fCmd = m.panels[PanelWorkstation].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "watched", "watched-files":
		c := m.switchTo(PanelWorkstation, wsSubWatchedFiles)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelWorkstation], fCmd = m.panels[PanelWorkstation].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "history", "hist":
		c := m.switchTo(PanelWorkstation, wsSubHistory)
		var fCmd tea.Cmd
		if q := strings.Join(inv.Args, " "); q != "" {
			m.panels[PanelWorkstation], fCmd = m.panels[PanelWorkstation].SetFilterQuery(q, m.backend)
		}
		return m, batchNonNil(c, fCmd)
	case "reload":
		return m, reloadConfigCmd(m.backend, "command")
	case "out", "output", "jobs", "0":
		m.switchPanel(PanelOutput)
		return m, nil
	case "zone", "z":
		target := ""
		if len(inv.Args) > 0 {
			target = inv.Args[0]
		}
		return m.setScopeZone(target)
	case "split":
		if !m.panels[m.activePanel].SupportsPreview() {
			return m, m.setToast("Active view does not support split preview", false)
		}
		arg := "v"
		if len(inv.Args) > 0 {
			arg = strings.ToLower(inv.Args[0])
		}
		switch arg {
		case "v", "vert", "vertical":
			m.splitMode[m.activeSplitKey()] = SplitVertical
			return m.routeToPanel(m.activePanel, previewOpenedMsg{})
		case "h", "horiz", "horizontal":
			m.splitMode[m.activeSplitKey()] = SplitHorizontal
			return m.routeToPanel(m.activePanel, previewOpenedMsg{})
		case "off", "none", "0":
			m.splitMode[m.activeSplitKey()] = SplitOff
			m.focusPreview = false
		default:
			return m, m.setToast("Usage: :split <v|h|off>", true)
		}
		return m, nil
	case "clear":
		np, fCmd := m.panels[m.activePanel].SetFilterQuery("", m.backend)
		m.panels[m.activePanel] = np.ClearSelection()
		return m, batchNonNil(fCmd, m.setToast("Cleared filter & selection", false))
	case "zoom", "zpane":
		return m.toggleZoom()
	case "update", "upgrade-all":
		allFlag := false
		for _, a := range inv.Args {
			switch strings.ToLower(strings.TrimSpace(a)) {
			case "all", "--all":
				allFlag = true
			}
		}
		subCmd := m.switchTo(PanelPackages, pkgSubInstalled)
		return m, batchNonNil(subCmd, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Confirm Workstation Update (ws update)",
				Body:         fmt.Sprintf("Run full workstation update (all=%v) across repo pull, missing preset packages, OS upgrades, and parallel rustup/tldr/gh-ext/mise/cleanup + symlink/theme sync.", allFlag),
				ConfirmLabel: "Update All",
				StartPanel:   PanelPackages,
				StartMsg:     systemMutationStartMsg{kind: "pkg"},
				OnConfirm: func(b Backend) tea.Cmd {
					return execUpdateAllOrFallback(b, allFlag)
				},
			}
		})
	case "scan":
		scope := "all"
		if len(inv.Args) > 0 {
			scope = strings.ToLower(strings.TrimSpace(inv.Args[0]))
		}
		if scope != "all" && scope != "env" && scope != "repos" {
			return m, m.setToast("Usage: :scan [env|repos|all]", true)
		}
		actID := newActivityID()
		runID := m.allocRunID()
		cmdStr := "ws env scan && ws repo list --refresh"
		if scope == "env" {
			cmdStr = "ws env scan"
		} else if scope == "repos" {
			cmdStr = "ws repo list --refresh"
		}
		m.recordOutputRun(CommandRun{
			ID:      runID,
			Command: cmdStr,
			Running: true,
		})
		label := "Scanning " + scope
		backend := m.backend
		zone := m.scopeZone
		return m, tea.Batch(
			startActivityCmd(actID, label),
			func() tea.Msg {
				if backend == nil {
					err := fmt.Errorf("backend unavailable")
					return tea.BatchMsg{
						func() tea.Msg { return finishActivityCmd(actID, label, true)() },
						func() tea.Msg {
							return wsCommandFinishedMsg{RunID: runID, Result: CommandRunResult{Command: cmdStr, Output: err.Error(), ExitCode: 1, IsErr: true}, Err: err}
						},
					}
				}
				var lines []string
				var msgs []tea.Msg
				var scanErr error
				if scope == "all" || scope == "env" {
					err := backend.ScanEnv()
					msgs = append(msgs, envScannedMsg{err: err})
					if err != nil {
						scanErr = err
						lines = append(lines, "env scan error: "+err.Error())
					} else {
						vars, vErr := backend.LoadEnvVars("", zone)
						msgs = append(msgs, envVarsLoadedMsg{zone: zone, vars: vars, err: vErr})
						if vErr != nil {
							scanErr = vErr
						}
						lines = append(lines, fmt.Sprintf("Scanned env vars (%d visible in zone %q)", len(vars), nonEmpty(zone, "all")))
					}
				}
				if scope == "all" || scope == "repos" {
					repos, rErr := backend.LoadRepos(true, zone)
					msgs = append(msgs, reposLoadedMsg{zone: zone, repos: repos, err: rErr})
					if rErr != nil {
						if scanErr == nil {
							scanErr = rErr
						}
						lines = append(lines, "repo scan error: "+rErr.Error())
					} else {
						lines = append(lines, fmt.Sprintf("Scanned repositories (%d visible in zone %q)", len(repos), nonEmpty(zone, "all")))
					}
				}
				res := CommandRunResult{
					Command: cmdStr,
					Output:  strings.Join(lines, "\n"),
					IsErr:   scanErr != nil,
				}
				if scanErr != nil {
					res.ExitCode = 1
				}
				msgs = append(msgs,
					wsCommandFinishedMsg{RunID: runID, Result: res, Err: scanErr},
					activityMsg{id: actID, label: label, done: true, isErr: scanErr != nil},
				)
				batch := make(tea.BatchMsg, 0, len(msgs))
				for _, mItem := range msgs {
					mm := mItem
					batch = append(batch, func() tea.Msg { return mm })
				}
				return batch
			},
		)
	case "status":
		return m.launchWSCommand([]string{"status"}, true)
	case "backup":
		return m, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Backing up ws.db..."} },
			backupDBCmd(m.backend, false),
		)
	case "pull":
		if len(inv.Args) > 1 || (len(inv.Args) == 1 && !strings.EqualFold(inv.Args[0], "all")) {
			return m, m.setToast("Usage: :pull [all]", true)
		}
		pullAll := len(inv.Args) == 1 && strings.EqualFold(inv.Args[0], "all")
		var np ResourcePanel
		var pCmd tea.Cmd
		np, pCmd = m.panels[PanelProjects].Update(triggerProjectsPullMsg{pullAll: pullAll}, m.backend, m.ActiveSplit(), false)
		m.panels[PanelProjects] = np
		return m, pCmd
	case "clean-exec":
		var np ResourcePanel
		var cCmd tea.Cmd
		np, cCmd = m.panels[PanelPackages].Update(triggerCleanExecMsg{}, m.backend, m.ActiveSplit(), false)
		m.panels[PanelPackages] = np
		return m, batchNonNil(m.switchTo(PanelPackages, pkgSubCleanup), cCmd)
	case "fix":
		var np ResourcePanel
		var aCmd tea.Cmd
		np, aCmd = m.panels[PanelWorkstation].Update(triggerDoctorApplyMsg{}, m.backend, m.ActiveSplit(), false)
		m.panels[PanelWorkstation] = np
		return m, batchNonNil(m.switchTo(PanelWorkstation, wsSubDoctor), aCmd)
	case "apply":
		args := append([]string{"apply"}, inv.Args...)
		if isSafeWSSubcommand(args) {
			return m.launchWSCommand(args, true)
		}
		m.confirmModal = &confirmModalState{
			Title:        "Confirm Workstation Apply (ws apply)",
			Body:         "Execute:\n$ ws " + strings.Join(args, " "),
			Warning:      "Renders templates, updates symlinks, and runs post-apply hooks.",
			ConfirmLabel: "Apply",
			Dangerous:    true,
			OnConfirm: func(Backend) tea.Cmd {
				argsCopy := append([]string(nil), args...)
				return func() tea.Msg {
					return launchStreamCmdMsg{
						Args:       argsCopy,
						Dangerous:  true,
						OpenOutput: true,
					}
				}
			},
		}
		return m, nil
	case "undo":
		m.confirmModal = &confirmModalState{
			Title:        "Confirm Undo Transaction",
			Body:         "Roll back the latest transaction recorded in ws.db.\n$ ws undo",
			Warning:      "Restores prior file/config state on disk.",
			ConfirmLabel: "Undo",
			Dangerous:    true,
			StartPanel:   PanelWorkstation,
			StartMsg:     doctorMutationStartMsg{kind: "undo"},
			OnConfirm: func(b Backend) tea.Cmd {
				return undoLastCmd(b)
			},
		}
		return m, nil
	case "!ws":
		if len(inv.Args) == 0 {
			return m, m.setToast("Usage: :!ws <subcommand...>", true)
		}
		args := append([]string(nil), inv.Args...)
		if isSafeWSSubcommand(args) {
			return m.launchWSCommand(args, true)
		}
		m.confirmModal = &confirmModalState{
			Title:        "Confirm CLI Execution",
			Body:         "Execute mutating CLI command:\n$ ws " + strings.Join(args, " "),
			Warning:      "This subcommand may modify workstation config, packages, or files.",
			ConfirmLabel: "Run Command",
			Dangerous:    true,
			OnConfirm: func(Backend) tea.Cmd {
				argsCopy := append([]string(nil), args...)
				return func() tea.Msg {
					return launchStreamCmdMsg{
						Args:       argsCopy,
						Dangerous:  true,
						OpenOutput: true,
					}
				}
			},
		}
		return m, nil
	}

	return m, m.setToast(fmt.Sprintf("Unknown command :%s (try :!ws %s)", cmdName, inv.Raw), true)
}

func (m Model) launchWSCommand(args []string, openOutput bool) (Model, tea.Cmd) {
	return m.launchWSCommandWithOpts(args, openOutput, false)
}

func (m Model) launchWSCommandWithOpts(args []string, openOutput, dangerous bool) (Model, tea.Cmd) {
	runID := m.allocRunID()
	actID := newActivityID()
	argsCopy := append([]string(nil), args...)
	cmdStr := "ws " + strings.Join(argsCopy, " ")
	m.recordOutputRun(CommandRun{
		ID:          runID,
		Command:     cmdStr,
		Running:     true,
		CurrentStep: "starting...",
		Dangerous:   dangerous,
		Rerun: func(Backend) tea.Cmd {
			return func() tea.Msg {
				return launchStreamCmdMsg{
					Args:       argsCopy,
					Dangerous:  dangerous,
					OpenOutput: true,
				}
			}
		},
	})
	handle, waitCmd := startWSCommandStream(m.backend, runID, actID, argsCopy)
	if op, ok := m.panels[PanelOutput].(outputPanel); ok {
		m.panels[PanelOutput] = op.RegisterStream(handle)
	}
	if openOutput {
		if m.activePanel != PanelOutput {
			m.prevPanel = m.activePanel
		}
		m.activePanel = PanelOutput
		m.focusPreview = false
	}
	return m, tea.Batch(
		startActivityCmd(actID, cmdStr),
		func() tea.Msg { return statusToastMsg{text: "Running " + cmdStr + "..."} },
		waitCmd,
	)
}

func (m Model) setScopeZone(target string) (tea.Model, tea.Cmd) {
	clean := strings.TrimSpace(target)
	if strings.EqualFold(clean, "all") {
		clean = ""
	} else if clean != "" && len(m.zones) > 0 {
		matched := ""
		for _, z := range m.zones {
			if strings.EqualFold(z, clean) {
				matched = z
				break
			}
		}
		if matched == "" {
			return m, m.setToast(fmt.Sprintf("Unknown zone %q (valid: %s)", clean, strings.Join(m.zones, ", ")), true)
		}
		clean = matched
	}
	m.scopeZone = clean
	label := nonEmpty(m.scopeZone, "all")
	cmds := []tea.Cmd{
		m.setToast("Zone scope: "+label, false),
	}
	for i := range m.panels {
		if m.panels[i].IsZoned() {
			nextP, cmd := m.panels[i].SetScope(m.scopeZone, m.backend)
			m.panels[i] = nextP
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return m, batchNonNil(cmds...)
}

func (m Model) cycleScopeZone() (tea.Model, tea.Cmd) {
	if len(m.zones) == 0 {
		return m, nil
	}
	cur := m.scopeZone
	if cur == "" {
		cur = "all"
	}
	idx := 0
	for i, z := range m.zones {
		if z == cur {
			idx = i
			break
		}
	}
	next := m.zones[(idx+1)%len(m.zones)]
	if next == "all" {
		m.scopeZone = ""
	} else {
		m.scopeZone = next
	}
	return m.setScopeZone(m.scopeZone)
}

func (m Model) hasMutationInFlight() bool {
	for _, p := range m.panels {
		if p.MutationInFlight() {
			return true
		}
	}
	return false
}

func (m Model) toggleThemePreview(id string) (tea.Model, tea.Cmd) {
	if len(m.themeIDs) == 0 {
		wasIdle := m.activeOperations() == 0
		m.pendingLoads++
		var spinCmd tea.Cmd
		if wasIdle && m.activeOperations() > 0 {
			spinCmd = m.spinner.Tick
		}
		return m, batchNonNil(spinCmd, loadThemesCmd(m.backend))
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return m, m.setToast("No theme selected to preview", false)
	}
	if m.previewTheme != "" && (id == m.previewTheme || id == m.activeTheme) {
		return m.clearThemePreview()
	}
	if id == m.activeTheme {
		return m, m.setToast(fmt.Sprintf("Already active theme: %s (select another theme to preview)", id), false)
	}
	m.previewTheme = id
	toastCmd := m.setToast(fmt.Sprintf("Previewing theme: %s (press t or esc to revert, Enter to apply)", id), false)

	var loadCmd tea.Cmd
	if cached, hasCached := m.palettes[id]; hasCached && len(cached.Tokens) > 0 {
		m.applyLocalStyles(cached)
	} else {
		loadCmd = loadPalettePreviewCmd(m.backend, id)
	}

	synced, syncCmd := m.routeToPanel(PanelWorkstation, previewThemeSyncMsg{id: id})
	return synced, batchNonNil(toastCmd, syncCmd, loadCmd)
}

func (m Model) clearThemePreview() (tea.Model, tea.Cmd) {
	m.previewTheme = ""

	var loadCmd tea.Cmd
	if cached, ok := m.palettes[m.activeTheme]; ok && len(cached.Tokens) > 0 {
		m.applyLocalStyles(cached)
	} else if m.activeTheme != "" && m.backend != nil {
		loadCmd = loadPalettePreviewCmd(m.backend, m.activeTheme)
	} else {
		m.applyLocalStyles(DefaultPalette())
	}

	toastCmd := m.setToast(fmt.Sprintf("Theme preview off (active: %s)", nonEmpty(m.activeTheme, "default")), false)
	synced, syncCmd := m.routeToPanel(PanelWorkstation, previewThemeSyncMsg{id: ""})
	return synced, batchNonNil(toastCmd, syncCmd, loadCmd)
}

func loadPalettePreviewCmd(backend Backend, id string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return palettePreviewLoadedMsg{id: id, err: fmt.Errorf("backend unavailable")}
		}
		pal, err := backend.LoadPalette(id)
		return palettePreviewLoadedMsg{id: id, palette: pal, err: err}
	}
}

func (m Model) routeToPanel(idx int, msg tea.Msg) (tea.Model, tea.Cmd) {
	if idx < 0 || idx >= len(m.panels) {
		return m, nil
	}
	nextPanel, cmd := m.panels[idx].Update(msg, m.backend, m.ActiveSplit(), m.focusPreview)
	m.panels[idx] = nextPanel
	return m, cmd
}

func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}
	e := msg.Mouse()
	var keyMsg tea.KeyPressMsg
	switch e.Button {
	case tea.MouseWheelUp:
		keyMsg = tea.KeyPressMsg{Code: tea.KeyUp}
	case tea.MouseWheelDown:
		keyMsg = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		return m, nil
	}

	if m.cmdBar.Mode() == barCommand || m.showHelp || m.actionMenu != nil {
		return m.handleKey(keyMsg)
	}
	if m.confirmModal != nil || m.cmdBar.Active() {
		return m, nil
	}

	active := m.panels[m.activePanel]
	if active.HasOpenModal() || active.CapturingInput() {
		return m.routeToPanel(m.activePanel, keyMsg)
	}

	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	headLines := m.headerLines(h)
	barLines := m.cmdBarLines(h)
	stageTop := headLines + barLines
	stageH := max(5, h-(headLines+barLines+2))
	split := m.ActiveSplit()

	inStage, inPreview, _ := hitTestSplitStage(e.X, e.Y, w, stageTop, stageH, split, active.SupportsPreview())
	targetPreview := m.focusPreview
	if inStage && (!m.zoomed || split == SplitOff || !active.SupportsPreview()) {
		targetPreview = inPreview
	}
	nextPanel, cmd := active.Update(keyMsg, m.backend, split, targetPreview)
	m.panels[m.activePanel] = nextPanel
	return m, cmd
}

func (m Model) toggleZoom() (tea.Model, tea.Cmd) {
	active := m.panels[m.activePanel]
	if !active.SupportsPreview() || m.ActiveSplit() == SplitOff {
		return m, m.setToast("Split view required to zoom pane (press p to open preview)", false)
	}
	m.zoomed = !m.zoomed
	if m.zoomed {
		target := "main"
		if m.focusPreview {
			target = "preview"
		}
		return m, m.setToast("Zoomed "+target+" pane (= or esc to restore)", false)
	}
	return m, m.setToast("Restored split view", false)
}

func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}
	e := msg.Mouse()
	if e.Button != tea.MouseLeft {
		return m, nil
	}
	if m.confirmModal != nil || m.actionMenu != nil || m.showHelp || m.cmdBar.Active() {
		return m, nil
	}
	active := m.panels[m.activePanel]
	if active.HasOpenModal() || active.CapturingInput() {
		return m, nil
	}

	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	headLines := m.headerLines(h)
	barLines := m.cmdBarLines(h)
	stageTop := headLines + barLines
	stageH := max(5, h-(headLines+barLines+2))
	navY := stageTop + stageH

	if e.Y == navY {
		_, tabSpans, subSpans := m.layoutBottomNav(w)
		for _, sp := range subSpans {
			if e.X >= sp.Start && e.X < sp.End {
				subViews := active.SubViews()
				if sp.ID >= 0 && sp.ID < len(subViews) && sp.ID != active.ActiveSubView() {
					m.pushJumpPoint()
					nextPanel := active.SetSubView(sp.ID)
					nextPanel, viewCmd := nextPanel.OnSubViewChanged(m.backend)
					m.panels[m.activePanel] = nextPanel
					m.focusPreview = false
					m.zoomed = false
					toastCmd := m.setToast("View: "+subViews[sp.ID], false)
					return m, batchNonNil(toastCmd, viewCmd)
				}
				return m, nil
			}
		}
		for _, sp := range tabSpans {
			if e.X >= sp.Start && e.X < sp.End {
				if sp.ID == PanelOutput {
					m.pushJumpPoint()
					oldPanel := m.activePanel
					if m.activePanel == PanelOutput {
						m.activePanel = m.prevPanel
					} else {
						m.prevPanel = m.activePanel
						m.activePanel = PanelOutput
					}
					if oldPanel != m.activePanel {
						m.panels[oldPanel] = m.panels[oldPanel].OnBlur()
					}
					m.focusPreview = false
					return m, nil
				}
				m.switchPanel(sp.ID)
				return m, nil
			}
		}
		return m, nil
	}

	split := m.ActiveSplit()
	if m.zoomed && split != SplitOff && active.SupportsPreview() {
		return m, nil
	}
	inStage, inPreview, _ := hitTestSplitStage(e.X, e.Y, w, stageTop, stageH, split, active.SupportsPreview())
	if inStage && split != SplitOff && active.SupportsPreview() && inPreview != m.focusPreview {
		m.focusPreview = inPreview
		if inPreview {
			return m.routeToPanel(m.activePanel, previewFocusedMsg{})
		}
		return m.routeToPanel(m.activePanel, focusPreviewMsg{focus: false})
	}
	return m, nil
}

func (m Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	if m.mouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m Model) Render() string {
	w := m.width
	h := m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}

	active := m.panels[m.activePanel]
	split := m.ActiveSplit()

	headLines := m.headerLines(h)
	globalHints := m.headerGlobalHints(headLines, w)
	viewHints := active.KeyHints(split, m.focusPreview)
	topHeader := renderK9sHeader(w, headLines, m.buildContextRows(headLines), globalHints, viewHints, m.styles)

	chips := append([]string{}, active.StatusChips()...)
	if split != SplitOff && active.SupportsPreview() {
		chips = append(chips, "split:"+split.String())
		if m.zoomed {
			zoomSide := "main"
			if m.focusPreview {
				zoomSide = "preview"
			}
			chips = append(chips, "ZOOM:"+zoomSide)
		}
	}
	if n := active.SelectedCount(); n > 0 {
		chips = append(chips, fmt.Sprintf("%d sel", n))
	}
	barLines := m.cmdBarLines(h)
	cmdLine := m.cmdBar.View(w, barLines, active.FilterTarget(), active.Breadcrumbs(), chips, m.styles)

	navStrip := m.renderBottomNav(w)
	cliBar := m.renderCLIBar(w)

	chromeLines := headLines + barLines + 2
	stageH := max(5, h-chromeLines)

	baseStage := m.renderStage(active, w, stageH, split)
	stage := baseStage
	switch {
	case m.confirmModal != nil:
		modal := renderConfirmModal(
			m.confirmModal.Title,
			m.confirmModal.Body,
			m.confirmModal.Warning,
			m.confirmModal.ConfirmLabel,
			m.confirmModal.Dangerous,
			m.confirmModal.FocusConfirm,
			w, stageH, m.styles,
		)
		stage = overlayModalOnStage(baseStage, modal, w, stageH, m.styles)
	case m.actionMenu != nil:
		modal := renderActionMenuModal(m.actionMenu, w, stageH, m.styles)
		stage = overlayModalOnStage(baseStage, modal, w, stageH, m.styles)
	case m.showHelp:
		modal := m.renderHelpModal(w, stageH)
		stage = overlayModalOnStage(baseStage, modal, w, stageH, m.styles)
	case m.cmdBar.Mode() == barCommand:
		palW := min(max(36, w-6), 94)
		if palW > w {
			palW = w
		}
		palH := min(20, max(7, stageH-2))
		if pal, ok := m.cmdBar.RenderPalette(palW, palH, m.styles); ok {
			stage = overlayModalOnStage(baseStage, pal, w, stageH, m.styles)
		}
	default:
		if modal, open := active.ViewModal(w, stageH, m.styles); open {
			stage = overlayModalOnStage(baseStage, modal, w, stageH, m.styles)
		}
	}

	stage = fitLines(stage, stageH)
	out := lipgloss.JoinVertical(lipgloss.Left, topHeader, cmdLine, stage, navStrip, cliBar)
	return fitLines(out, h)
}

func (m Model) renderStage(active ResourcePanel, width, height int, split SplitMode) string {
	if split == SplitOff || !active.SupportsPreview() {
		return active.ViewMain(width, height, true, m.styles)
	}
	if m.zoomed {
		if m.focusPreview {
			return active.ViewPreview(width, height, true, false, m.styles)
		}
		return active.ViewMain(width, height, true, m.styles)
	}
	return renderSplitStage(
		width, height, split, m.focusPreview,
		func(mw, mh int, mf bool) string {
			return active.ViewMain(mw, mh, mf, m.styles)
		},
		func(pw, ph int, pf bool) string {
			return active.ViewPreview(pw, ph, pf, false, m.styles)
		},
	)
}

func dimStageLines(base string, width, height int, st Styles) string {
	lines := strings.Split(fitLines(base, height), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	out := make([]string, height)
	for i := 0; i < height; i++ {
		plain := ui.StripANSI(lines[i])
		if ansi.StringWidth(plain) > width {
			plain = ansi.Truncate(plain, width, "")
		}
		if rem := width - ansi.StringWidth(plain); rem > 0 {
			plain += strings.Repeat(" ", rem)
		}
		if st.Roles.NoColor {
			out[i] = plain
		} else {
			out[i] = st.Muted.Render(plain)
		}
	}
	return strings.Join(out, "\n")
}

func (m Model) headerLines(h int) int {
	switch {
	case h <= 25:
		return 2
	case h <= 29:
		return 3
	default:
		return 4
	}
}

func (m Model) cmdBarLines(h int) int {
	switch m.borderedBarPref {
	case "compact":
		return 1
	case "always":
		if h >= 14 {
			return 3
		}
		return 1
	default:
		if h <= 25 {
			return 1
		}
		return 3
	}
}

func (m Model) headerGlobalHints(lines, width int) [][2]string {
	core := [][2]string{
		{":", "Command bar"},
		{"/", "Filter list"},
		{"a", "Row actions"},
		{"?", "Help"},
		{"1-6/0", "Workspaces"},
		{"[/]", "Sub-view"},
		{"p/tab/=", "Layout/Tab/Zoom"},
		{"q/esc", "Back / Quit"},
	}
	if width < 108 || lines <= 2 {
		return core[:4]
	}
	return core
}

func (m *Model) syncCommandBarOptions() {
	zoneOpts := make([]paletteOption, 0, len(m.zones))
	for _, z := range m.zones {
		desc := "Switch zone scope to " + z
		if z == "all" {
			desc = "Show resources across all configured zones"
		}
		zoneOpts = append(zoneOpts, paletteOption{Value: z, Description: desc})
	}
	if len(zoneOpts) > 0 {
		m.cmdBar.SetArgOptions("zone", zoneOpts)
	}
	if len(m.themeIDs) > 0 {
		themeOpts := make([]paletteOption, 0, len(m.themeIDs))
		for _, id := range m.themeIDs {
			desc := "Inspect or apply palette " + id
			if id == m.activeTheme {
				desc += " (active)"
			}
			themeOpts = append(themeOpts, paletteOption{Value: id, Description: desc})
		}
		m.cmdBar.SetArgOptions("themes", themeOpts)
	}
	if len(m.presetIDs) > 0 {
		presetOpts := make([]paletteOption, 0, len(m.presetIDs))
		pkgOpts := make([]paletteOption, 0, len(m.presetIDs))
		for _, id := range m.presetIDs {
			presetOpts = append(presetOpts, paletteOption{Value: id, Description: "Inspect preset " + id})
			pkgOpts = append(pkgOpts, paletteOption{Value: "preset:" + id, Description: "Switch packages view to preset " + id})
		}
		m.cmdBar.SetArgOptions("presets", presetOpts)
		m.cmdBar.SetArgOptions("packages", pkgOpts)
		m.cmdBar.SetArgOptions("pkg", pkgOpts)
		m.cmdBar.SetArgOptions("pkgs", pkgOpts)
	}
	ghOpts := append([]paletteOption(nil), defaultArgOptions()["gh"]...)
	for _, slug := range m.repoSlugs {
		ghOpts = append(ghOpts, paletteOption{Value: "repo:" + slug, Description: "Inspect PRs & issues for " + slug})
	}
	m.cmdBar.SetArgOptions("gh", ghOpts)
	m.cmdBar.SetArgOptions("prs", ghOpts)
	m.cmdBar.SetArgOptions("issues", ghOpts)
}

func extractGHSlugFromRemote(remote string) string {

	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	if idx := strings.Index(r, "github.com:"); idx >= 0 {
		slug := strings.TrimSuffix(strings.TrimPrefix(r[idx+len("github.com:"):], "/"), ".git")
		if strings.Count(slug, "/") == 1 {
			return slug
		}
	}
	if idx := strings.Index(r, "github.com/"); idx >= 0 {
		parts := strings.Split(strings.TrimSuffix(r[idx+len("github.com/"):], ".git"), "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return ""
}

func (m Model) buildContextRows(lines int) [][2]string {
	osArch := "unknown"
	if m.host.OS != "" || m.host.Arch != "" {
		osArch = strings.Trim(m.host.OS+"/"+m.host.Arch, "/")
		if m.host.Distro != "" && m.host.Distro != m.host.OS {
			osArch = m.host.Distro + " " + osArch
		}
	}
	zoneLabel := nonEmpty(m.scopeZone, "all")
	themeLabel := nonEmpty(m.activeTheme, "default")
	if m.previewTheme != "" && m.previewTheme != m.activeTheme {
		themeLabel = m.previewTheme + " (preview)"
	}

	findingsStatus := "OK"
	switch {
	case m.findingsErr > 0:
		findingsStatus = fmt.Sprintf("%d err", m.findingsErr)
	case m.findingsWarn > 0:
		findingsStatus = fmt.Sprintf("%d warn", m.findingsWarn)
	}

	if lines >= 4 {
		return [][2]string{
			{"Host", osArch},
			{"Zone", zoneLabel},
			{"Theme", themeLabel},
			{"Health", findingsStatus},
		}
	}
	if lines == 3 {
		return [][2]string{
			{"Host", osArch},
			{"Zone", fmt.Sprintf("%s · %s", zoneLabel, themeLabel)},
			{"Health", findingsStatus},
		}
	}
	return [][2]string{
		{"Host", osArch},
		{"Zone", fmt.Sprintf("%s · %s", zoneLabel, themeLabel)},
	}
}

func (m Model) renderBottomNav(width int) string {
	rendered, _, _ := m.layoutBottomNav(width)
	return rendered
}

func (m Model) layoutBottomNav(width int) (string, []navHitSpan, []navHitSpan) {
	st := m.styles
	tabs := make([]string, 0, len(m.panels))
	tabSpans := make([]navHitSpan, 0, len(m.panels))
	curX := 0
	for i := 0; i < workspaceTabCount; i++ {
		num := fmt.Sprintf("%d", i+1)
		lbl := m.panels[i].ShortTitle()
		var rendered string
		if i == m.activePanel {
			rendered = st.TabActive.Render(num + " " + lbl)
		} else {
			rendered = st.InfoText.Render(num) + " " + st.Muted.Render(lbl)
		}
		rw := lipgloss.Width(rendered)
		tabSpans = append(tabSpans, navHitSpan{ID: i, Start: curX, End: curX + rw})
		curX += rw + 2
		tabs = append(tabs, rendered)
	}

	outBadge := "Out(0)"
	outAttn := AttentionNone
	if op, ok := m.panels[PanelOutput].(outputPanel); ok {
		outBadge, outAttn = op.TabBadge()
	}
	outLbl := "0 " + outBadge
	var outRendered string
	if m.activePanel == PanelOutput {
		outRendered = st.TabActive.Render(outLbl)
	} else {
		badgeStyled := st.Muted.Render(outBadge)
		switch outAttn {
		case AttentionInfo:
			badgeStyled = st.InfoText.Render(outBadge)
		case AttentionErr:
			badgeStyled = st.ErrText.Render(outBadge)
		}
		outRendered = st.InfoText.Render("0") + " " + badgeStyled
	}
	outW := lipgloss.Width(outRendered)
	tabSpans = append(tabSpans, navHitSpan{ID: PanelOutput, Start: curX, End: curX + outW})
	tabs = append(tabs, outRendered)
	left := strings.Join(tabs, "  ")

	var right string
	var relSubSpans []navHitSpan
	subViews := m.panels[m.activePanel].SubViews()
	if len(subViews) > 1 {
		curSub := m.panels[m.activePanel].ActiveSubView()
		var subParts []string
		curRX := 0
		for si, sv := range subViews {
			var rendered string
			if si == curSub {
				rendered = st.TabActive.Render(sv)
			} else {
				rendered = st.TabInactive.Render(sv)
			}
			rw := lipgloss.Width(rendered)
			relSubSpans = append(relSubSpans, navHitSpan{ID: si, Start: curRX, End: curRX + rw})
			curRX += rw + 1
			subParts = append(subParts, rendered)
		}
		right = strings.Join(subParts, " ") + " " + st.Muted.Render("[/]")
	}

	shiftSpans := func(spans []navHitSpan, dx int) []navHitSpan {
		out := make([]navHitSpan, len(spans))
		for i, sp := range spans {
			out[i] = navHitSpan{ID: sp.ID, Start: sp.Start + dx, End: sp.End + dx}
		}
		return out
	}

	leftW := lipgloss.Width(left)
	if right == "" && leftW <= width {
		return fitStyledWidth(left, width, st), tabSpans, nil
	}
	gap := width - leftW - lipgloss.Width(right)
	if right != "" && gap >= 2 {
		return fitStyledWidth(left+strings.Repeat(" ", gap)+right, width, st), tabSpans, shiftSpans(relSubSpans, leftW+gap)
	}

	var compactTabs []string
	compactSpans := make([]navHitSpan, 0, len(m.panels))
	compX := 0
	for i := 0; i < workspaceTabCount; i++ {
		num := fmt.Sprintf("%d", i+1)
		var rendered string
		if i == m.activePanel {
			rendered = st.TabActive.Render(num + ":" + m.panels[i].ShortTitle())
		} else {
			rendered = st.InfoText.Render(num)
		}
		rw := lipgloss.Width(rendered)
		compactSpans = append(compactSpans, navHitSpan{ID: i, Start: compX, End: compX + rw})
		compX += rw + 1
		compactTabs = append(compactTabs, rendered)
	}
	var compactOut string
	outSuffix := ""
	if outAttn == AttentionInfo {
		outSuffix = "…"
	} else if outAttn == AttentionErr {
		outSuffix = "!"
	}
	if m.activePanel == PanelOutput {
		compactOut = st.TabActive.Render("0:Out" + outSuffix)
	} else {
		switch outAttn {
		case AttentionInfo:
			compactOut = st.InfoText.Render("0" + outSuffix)
		case AttentionErr:
			compactOut = st.ErrText.Render("0" + outSuffix)
		default:
			compactOut = st.InfoText.Render("0")
		}
	}
	cow := lipgloss.Width(compactOut)
	compactSpans = append(compactSpans, navHitSpan{ID: PanelOutput, Start: compX, End: compX + cow})
	compactTabs = append(compactTabs, compactOut)
	compactLeft := strings.Join(compactTabs, " ")
	if right == "" {
		return fitStyledWidth(compactLeft, width, st), compactSpans, nil
	}
	compactLeftW := lipgloss.Width(compactLeft)
	gap = width - compactLeftW - lipgloss.Width(right)
	if gap >= 1 {
		return fitStyledWidth(compactLeft+strings.Repeat(" ", gap)+right, width, st), compactSpans, shiftSpans(relSubSpans, compactLeftW+gap)
	}
	return fitStyledWidth(compactLeft, width, st), compactSpans, nil
}

func (m Model) renderCLIBar(width int) string {
	st := m.styles
	innerW := max(4, width-st.CLIBar.GetHorizontalFrameSize())
	cli := "$ " + m.panels[m.activePanel].CLIEquivalent()

	var rightStyled string
	if m.toast != "" {
		toastMax := max(10, innerW/2)
		toastStr := truncate(m.toast, toastMax)
		rightStyled = st.OKText.Render(toastStr)
		if m.toastErr {
			rightStyled = st.ErrText.Render(toastStr)
		}
	} else if m.pendingKey != "" {
		rightStyled = st.InfoText.Render("pending: " + m.pendingKey)
	} else if op, ok := m.panels[PanelOutput].(outputPanel); ok && op.ActiveStep() != "" {
		spin := m.spinner.View()
		stepText := op.ActiveStep()
		maxStepW := max(8, innerW/2-lipgloss.Width(spin)-1)
		rightStyled = spin + " " + st.InfoText.Render(truncate(stepText, maxStepW))
	} else if m.activeOperations() > 0 {
		actText := "Loading..."
		if act, ok := m.latestRunningActivity(); ok && act.Text != "" {
			actText = act.Text
		}
		spin := m.spinner.View()
		maxActW := max(8, innerW/2-lipgloss.Width(spin)-1)
		rightStyled = spin + " " + st.InfoText.Render(truncate(actText, maxActW))
	} else if stText := m.panels[m.activePanel].PaneStatus(); stText != "" {
		rightStyled = st.InfoText.Render(truncate(stText, max(8, innerW/3)))
	}

	if rightStyled == "" {
		return st.CLIBar.Width(width).MaxWidth(width).MaxHeight(1).Render(truncate(cli, innerW))
	}

	cliMax := max(4, innerW-lipgloss.Width(rightStyled)-2)
	cliTrunc := truncate(cli, cliMax)
	gap := max(1, innerW-lipgloss.Width(cliTrunc)-lipgloss.Width(rightStyled))
	line := cliTrunc + strings.Repeat(" ", gap) + rightStyled
	return st.CLIBar.Width(width).MaxWidth(width).MaxHeight(1).Render(line)
}

type ghCommandMsg struct {
	kind    string
	repo    string
	state   string
	allMine bool
	query   string
}
type systemSwitchPresetMsg struct{ preset string }
type triggerCleanExecMsg struct{}
type triggerDoctorApplyMsg struct{}

func countFindings(findings []DoctorFinding) (warns, errs int) {
	for _, f := range findings {
		switch strings.ToLower(f.Severity) {
		case "warn", "warning":
			warns++
		case "err", "error":
			errs++
		}
	}
	return warns, errs
}

func loadHostCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return hostLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		badge, err := backend.HostSummary()
		return hostLoadedMsg{badge: badge, err: err}
	}
}
