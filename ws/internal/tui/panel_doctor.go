package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

type doctorPanel struct {
	summary       DoctorSummary
	filtered      []DoctorFinding
	dbStatus      DBStatusSummary
	filterQuery   string
	loaded        bool
	dbLoaded      bool
	loadErr       string
	dbErr         string
	cursor        int
	sel           SelectionSet
	drillOpen     bool
	previewScroll int
	applying      bool
	backingUp     bool
	undoing       bool
	docActID      int
	dbActID       int
	fixActID      int
	bkActID       int
	undoActID     int
}

func newDoctorPanel() doctorPanel {
	return doctorPanel{}
}

func (p doctorPanel) ID() string                                        { return "doctor" }
func (p doctorPanel) Title() string                                     { return "Doctor Diagnostics" }
func (p doctorPanel) ShortTitle() string                                { return "Doctor" }
func (p doctorPanel) SubViews() []string                                { return nil }
func (p doctorPanel) ActiveSubView() int                                { return 0 }
func (p doctorPanel) SetSubView(int) ResourcePanel                      { return p }
func (p doctorPanel) OnSubViewChanged(Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p doctorPanel) SetScope(string, Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p doctorPanel) IsZoned() bool                                     { return false }
func (p doctorPanel) FilterQuery() string                               { return p.filterQuery }
func (p doctorPanel) FilterTarget() string                              { return "Findings" }
func (p doctorPanel) SelectedCount() int                                { return p.sel.Count() }
func (p doctorPanel) ClearSelection() ResourcePanel {
	p.sel = p.sel.Clear()
	return p
}
func (p doctorPanel) SupportsPreview() bool { return !p.drillOpen }
func (p doctorPanel) DefaultSplit(width int) SplitMode {
	if width >= 112 {
		return SplitVertical
	}
	return SplitOff
}
func (p doctorPanel) CanPop() bool         { return p.drillOpen }
func (p doctorPanel) Pop() ResourcePanel   { p.drillOpen = false; p.previewScroll = 0; return p }
func (p doctorPanel) CapturingInput() bool { return false }
func (p doctorPanel) HasOpenModal() bool   { return false }
func (p doctorPanel) MutationInFlight() bool {
	return p.applying || p.backingUp || p.undoing
}
func (p doctorPanel) ViewModal(int, int, Styles) (string, bool) { return "", false }

func (p doctorPanel) SetFilterQuery(q string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.recomputeFiltered()
	p.cursor = 0
	p.previewScroll = 0
	return p, nil
}

func (p doctorPanel) OnBlur() ResourcePanel { return p }

func (p *doctorPanel) recomputeFiltered() {
	p.filtered = fuzzy.Filter(p.summary.Findings, p.filterQuery, func(f DoctorFinding) ([]string, map[string]string) {
		fix := "false"
		if f.FixPlan != nil && len(f.FixPlan.Actions) > 0 {
			fix = "true"
		}
		return []string{f.Title, f.Category, f.Detail, f.FixCLI, f.Severity},
			map[string]string{
				"sev": f.Severity,
				"cat": f.Category,
				"fix": fix,
			}
	})
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
}

func (p doctorPanel) Attention() AttentionLevel {
	if p.loadErr != "" || p.dbErr != "" {
		return AttentionErr
	}
	warns, errs := countFindings(p.summary.Findings)
	if errs > 0 {
		return AttentionErr
	}
	if warns > 0 {
		return AttentionWarn
	}
	return AttentionNone
}

func (p doctorPanel) PaneStatus() string {
	switch {
	case p.applying:
		return "applying fix..."
	case p.backingUp:
		return "backing up ws.db..."
	case p.undoing:
		return "undoing..."
	default:
		return ""
	}
}

func (p doctorPanel) Badge() string {
	if !p.loaded {
		return "…"
	}
	warns, errs := countFindings(p.summary.Findings)
	base := "OK"
	if errs > 0 {
		base = fmt.Sprintf("%d err", errs)
	} else if warns > 0 {
		base = fmt.Sprintf("%d warn", warns)
	}
	if p.dbStatus.DirtySinceBackup {
		base += " · db*"
	}
	return base
}

func (p doctorPanel) Breadcrumbs() []string {
	if p.drillOpen {
		if f, ok := p.selectedFinding(); ok {
			return []string{"Workstation", "Doctor", truncate(f.Title, 32)}
		}
	}
	return []string{"Workstation", fmt.Sprintf("Doctor (%d)", len(p.filtered))}
}

func (p doctorPanel) StatusChips() []string {
	var chips []string
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	if p.dbStatus.DirtySinceBackup {
		chips = append(chips, "db:dirty")
	} else if p.dbLoaded && p.dbErr == "" {
		chips = append(chips, "db:synced")
	}
	return chips
}

func (p doctorPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.drillOpen {
		return [][2]string{
			{"j/k", "Scroll diff"},
			{"a", "Doctor actions"},
			{"y", "Copy Fix CLI"},
			{"r", "Refresh"},
			{"esc", "Back to list"},
		}
	}
	return [][2]string{
		{"enter", "Full-screen diff"},
		{"a", "Doctor actions"},
		{"y", "Copy Fix CLI"},
		{"r", "Refresh"},
		{"space/*", "Mark findings"},
	}
}

func (p doctorPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	return []RowAction{
		{Key: "f", Label: "Apply fix plan", Description: "Apply fix for selected/marked finding(s)", Dangerous: true},
		{Key: "b", Label: "Backup ws.db", Description: "Write encrypted ws.db backup if changed"},
		{Key: "U", Label: "Undo last transaction", Description: "Roll back last workstation transaction (ws undo)", Dangerous: true},
	}
}

func (p doctorPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch actionKey {
	case "f":
		return p.requestApplyFix()
	case "b":
		return p.startBackup(backend)
	case "U":
		return p.requestUndo()
	}
	return p, nil
}

func (p doctorPanel) requestUndo() (ResourcePanel, tea.Cmd) {
	if p.undoing {
		return p, nil
	}
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Undo Last Transaction",
			Body:         "Roll back the most recent recorded workstation transaction from ws.db / ledger snapshots?\n$ ws undo",
			Warning:      "Files modified by that transaction will be restored or removed.",
			ConfirmLabel: "Undo",
			Dangerous:    true,
			StartPanel:   PanelWorkstation,
			StartMsg:     doctorMutationStartMsg{kind: "undo"},
			OnConfirm: func(b Backend) tea.Cmd {
				return undoLastCmd(b)
			},
		}
	}
}

func (p doctorPanel) selectedFinding() (DoctorFinding, bool) {
	if len(p.filtered) == 0 || p.cursor < 0 || p.cursor >= len(p.filtered) {
		return DoctorFinding{}, false
	}
	return p.filtered[p.cursor], true
}

func (p doctorPanel) targetFixFindings() []DoctorFinding {
	if p.sel.Count() > 0 {
		var out []DoctorFinding
		for _, f := range p.filtered {
			if p.sel.Has(f.ID) && f.FixPlan != nil && len(f.FixPlan.Actions) > 0 {
				out = append(out, f)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if f, ok := p.selectedFinding(); ok && f.FixPlan != nil && len(f.FixPlan.Actions) > 0 {
		return []DoctorFinding{f}
	}
	return nil
}

func (p doctorPanel) CLIEquivalent() string {
	if f, ok := p.selectedFinding(); ok && strings.TrimSpace(f.FixCLI) != "" {
		return f.FixCLI
	}
	return "ws doctor"
}

type doctorMutationStartMsg struct {
	kind string
}

func (p doctorPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case doctorMutationStartMsg:
		switch msg.kind {
		case "apply":
			p.applying = true
		case "undo":
			p.undoing = true
		}
		return p, nil

	case triggerDoctorApplyMsg:
		return p.requestApplyFix()

	case doctorLoadedMsg:
		p.loaded = true
		p.applying = false
		var cmds []tea.Cmd
		if p.docActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.docActID, "Running diagnostics", msg.err != nil))
			p.docActID = 0
		}
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Doctor failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.loadErr = ""
		p.summary = msg.summary
		p.recomputeFiltered()
		return p, batchNonNil(cmds...)

	case dbStatusLoadedMsg:
		p.dbLoaded = true
		var cmds []tea.Cmd
		if p.dbActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.dbActID, "Checking ws.db status", msg.err != nil))
			p.dbActID = 0
		}
		if msg.err != nil {
			p.dbErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "ws.db status failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.dbErr = ""
		p.dbStatus = msg.status
		return p, batchNonNil(cmds...)

	case planAppliedMsg:
		p.applying = false
		p.sel = p.sel.Clear()
		var actCmd tea.Cmd
		if p.fixActID != 0 {
			actCmd = finishActivityCmd(p.fixActID, "Applying doctor fix", msg.err != nil)
			p.fixActID = 0
		}
		if msg.err != nil {
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "Apply failed: " + msg.err.Error(), isErr: true}
			})
		}
		applied := 0
		if msg.result != nil {
			applied = msg.result.Applied
		}
		p.docActID = newActivityID()
		p.dbActID = newActivityID()
		return p, batchNonNil(
			actCmd,
			func() tea.Msg { return statusToastMsg{text: fmt.Sprintf("Applied %d action(s)", applied)} },
			startActivityCmd(p.docActID, "Running diagnostics"),
			startActivityCmd(p.dbActID, "Checking ws.db status"),
			loadDoctorCmd(backend),
			loadDBStatusCmd(backend),
		)

	case dbBackedUpMsg:
		p.backingUp = false
		var actCmd tea.Cmd
		if p.bkActID != 0 {
			actCmd = finishActivityCmd(p.bkActID, "Backing up ws.db", msg.err != nil)
			p.bkActID = 0
		}
		if msg.err != nil {
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "ws.db backup failed: " + msg.err.Error(), isErr: true}
			})
		}
		summary := msg.result.Summary
		if summary == "" {
			if msg.result.Skipped {
				summary = "ws.db unchanged since last backup"
			} else {
				summary = "ws.db backup completed"
			}
		}
		p.dbActID = newActivityID()
		return p, batchNonNil(
			actCmd,
			func() tea.Msg { return statusToastMsg{text: summary} },
			startActivityCmd(p.dbActID, "Checking ws.db status"),
			loadDBStatusCmd(backend),
		)

	case undoDoneMsg:
		p.undoing = false
		var actCmd tea.Cmd
		if p.undoActID != 0 {
			actCmd = finishActivityCmd(p.undoActID, "Undoing transaction", msg.err != nil)
			p.undoActID = 0
		}
		if msg.err != nil {
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "Undo failed: " + msg.err.Error(), isErr: true}
			})
		}
		summary := nonEmpty(msg.summary, "Undid last transaction")
		p.docActID = newActivityID()
		p.dbActID = newActivityID()
		return p, batchNonNil(
			actCmd,
			func() tea.Msg { return statusToastMsg{text: summary} },
			startActivityCmd(p.docActID, "Running diagnostics"),
			startActivityCmd(p.dbActID, "Checking ws.db status"),
			loadDoctorCmd(backend),
			loadDBStatusCmd(backend),
		)

	case tea.KeyPressMsg:
		if !p.drillOpen && isSpaceKey(msg) {
			if f, ok := p.selectedFinding(); ok {
				p.sel = p.sel.Toggle(f.ID)
			}
			return p, nil
		}
		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview || p.drillOpen {
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
				return p, nil
			}
			if next, changed := applyListMotion(p.cursor, len(p.filtered), defaultMotionPage, m); changed {
				p.cursor = next
				p.previewScroll = 0
			}
			return p, nil
		}
		switch msg.String() {
		case "*":
			keys := make([]string, len(p.filtered))
			for i, f := range p.filtered {
				keys[i] = f.ID
			}
			p.sel = p.sel.ToggleAll(keys)
		case `\`:
			p.sel = p.sel.Clear()
		case "enter":
			if _, ok := p.selectedFinding(); ok {
				p.drillOpen = !p.drillOpen
				p.previewScroll = 0
			}
		case "y":
			if f, ok := p.selectedFinding(); ok && f.FixCLI != "" {
				return p, copyCmd(f.FixCLI, "fix command")
			}
		case "r":
			p.docActID = newActivityID()
			p.dbActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.docActID, "Running diagnostics"),
				startActivityCmd(p.dbActID, "Checking ws.db status"),
				loadDoctorCmd(backend),
				loadDBStatusCmd(backend),
			)
		}
	}
	return p, nil
}

func (p doctorPanel) startBackup(backend Backend) (ResourcePanel, tea.Cmd) {
	if p.backingUp {
		return p, nil
	}
	p.backingUp = true
	p.bkActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.bkActID, "Backing up ws.db"),
		backupDBCmd(backend, true),
	)
}

func (p doctorPanel) requestApplyFix() (ResourcePanel, tea.Cmd) {
	if p.applying {
		return p, func() tea.Msg { return statusToastMsg{text: "Fix apply already in progress"} }
	}
	targets := p.targetFixFindings()
	if len(targets) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "No runnable fix plan for selected item(s)"} }
	}
	totalActions := 0
	hasDanger := false
	var titles []string
	var warnParts []string
	for _, f := range targets {
		titles = append(titles, f.Title)
		for _, a := range f.FixPlan.Actions {
			totalActions++
			if a.Risk == engine.RiskDangerous {
				hasDanger = true
			}
			if a.RequiresAllow != "" {
				warnParts = append(warnParts, fmt.Sprintf("%s requires --allow %s", a.Target, a.RequiresAllow))
			}
		}
	}
	body := fmt.Sprintf("Findings (%d): %s\nTotal Planned Actions: %d\nCommand: %s",
		len(targets), strings.Join(titles, ", "), totalActions, p.CLIEquivalent())
	warn := strings.Join(warnParts, "; ")
	targetsCopy := append([]DoctorFinding(nil), targets...)
	cliCmd := p.CLIEquivalent()

	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Confirm Apply Fix Plan",
			Body:         body,
			Warning:      warn,
			ConfirmLabel: fmt.Sprintf("Apply (%d)", totalActions),
			Dangerous:    hasDanger || len(warnParts) > 0,
			StartPanel:   PanelWorkstation,
			StartMsg:     doctorMutationStartMsg{kind: "apply"},
			OnConfirm: func(b Backend) tea.Cmd {
				actID := newActivityID()
				return tea.Batch(
					startActivityCmd(actID, "Applying doctor fix"),
					func() tea.Msg {
						if b == nil {
							return planAppliedMsg{cmd: cliCmd, err: fmt.Errorf("backend unavailable")}
						}
						combined := &engine.ApplyResult{}
						for _, tf := range targetsCopy {
							res, err := b.ApplyPlan(tf.FixPlan)
							if err != nil {
								return planAppliedMsg{result: combined, cmd: cliCmd, err: err}
							}
							if res != nil {
								combined.Applied += res.Applied
								combined.Skipped += res.Skipped
							}
						}
						return planAppliedMsg{result: combined, cmd: cliCmd}
					},
				)
			},
		}
	}
}

func (p doctorPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.drillOpen {
		return p.ViewPreview(width, height, focused, false, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	s := p.summary
	dbTag := "sync"
	if p.dbStatus.DirtySinceBackup {
		dbTag = "dirty"
	}
	kpiLine := fmt.Sprintf("Tpl/Link: %d/%d  ·  Pkgs: %d (%d outdated)  ·  Zones: %d/%d  ·  Reclaim: %s  ·  ws.db: %s",
		s.TemplatesOK+s.LinksOK, s.TemplatesTotal+s.LinksTotal,
		s.PackagesManaged, s.PackagesOutdated,
		s.ZonesOK, s.ZonesTotal,
		formatBytes(s.ReclaimableBytes), dbTag)

	lines := []string{st.InfoText.Render(truncate(kpiLine, innerW))}
	if p.dbErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("ws.db error: "+p.dbErr, innerW)))
	}
	lines = append(lines, "")

	if !p.loaded {
		lines = append(lines, st.Muted.Render("Running diagnostics..."))
		return renderTitledBox("Doctor Diagnostics", p.Badge(), lines, width, height, focused, st)
	}
	if p.loadErr != "" {
		lines = append(lines, st.ErrText.Render(truncate(p.loadErr, innerW)))
		return renderTitledBox("Doctor Diagnostics", p.Badge(), lines, width, height, focused, st)
	}
	if len(p.filtered) == 0 {
		lines = append(lines, st.OKText.Render("All diagnostics healthy."))
		return renderTitledBox("Doctor Diagnostics (0)", p.Badge(), lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filtered))
	marked := make(map[int]bool, p.sel.Count())
	for i, f := range p.filtered {
		if p.sel.Has(f.ID) {
			marked[i] = true
		}
		fix := nonEmpty(f.FixCLI, "—")
		if f.FixPlan != nil && len(f.FixPlan.Actions) > 0 {
			fix = "✦ " + fix
		}
		rows = append(rows, []string{
			severityBadge(f.Severity, st),
			nonEmpty(f.Category, "—"),
			f.Title,
			fix,
		})
	}
	tableH := max(4, innerH-len(lines))
	lines = append(lines, renderSelectableTable([]string{"Sev", "Category", "Finding", "Fix CLI"}, rows, p.cursor, marked, focused, innerW, tableH, st)...)
	return renderTitledBox(fmt.Sprintf("Doctor Findings (%d)", len(p.filtered)), p.Badge(), lines, width, height, focused, st)
}

func (p doctorPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	f, ok := p.selectedFinding()
	if !ok {
		msg := []string{st.OKText.Render("All diagnostics healthy — select a finding to inspect its Plan & Diff.")}
		if p.loadErr != "" {
			msg = []string{st.ErrText.Render(truncate("Doctor error: "+p.loadErr, innerW))}
		}
		if borderless {
			return fitLines(strings.Join(msg, "\n"), height)
		}
		return renderTitledBox("Plan & Diff Preview", "", msg, width, height, focused, st)
	}

	var all []string
	all = append(all, severityBadge(f.Severity, st)+" "+st.AccentText.Render(truncate(f.Title, max(6, innerW-8))))
	if f.Detail != "" {
		all = append(all, st.Muted.Render(truncate(f.Detail, innerW)))
	}
	if f.FixCLI != "" {
		all = append(all, st.InfoText.Render(truncate("$ "+f.FixCLI, innerW)))
	}
	all = append(all, "")

	if f.FixPlan != nil && len(f.FixPlan.Actions) > 0 {
		all = append(all, st.SectionHeader.Render(truncate(fmt.Sprintf("Plan & Diff Preview: %s (%d action)", f.FixPlan.Title, len(f.FixPlan.Actions)), innerW)))
		for _, act := range f.FixPlan.Actions {
			riskSt := st.OKText
			switch act.Risk {
			case engine.RiskMedium, engine.RiskCostly:
				riskSt = st.WarnText
			case engine.RiskDangerous:
				riskSt = st.ErrText
			}
			summary := fmt.Sprintf("[%s] %s: %s", act.Kind, act.Target, act.Summary)
			all = append(all, riskSt.Render("• ")+st.RowNormal.Render(truncate(summary, max(4, innerW-2))))
			if act.Diff != "" {
				for _, dl := range strings.Split(strings.TrimSpace(act.Diff), "\n") {
					t := truncate(dl, innerW)
					switch {
					case strings.HasPrefix(dl, "+") && !strings.HasPrefix(dl, "+++"):
						all = append(all, st.DiffAdd.Render(t))
					case strings.HasPrefix(dl, "-") && !strings.HasPrefix(dl, "---"):
						all = append(all, st.DiffDel.Render(t))
					case strings.HasPrefix(dl, "@@") || strings.HasPrefix(dl, "---") || strings.HasPrefix(dl, "+++"):
						all = append(all, st.DiffHeader.Render(t))
					default:
						all = append(all, st.Muted.Render(t))
					}
				}
			}
		}
	} else {
		all = append(all, st.Muted.Render("No mutations queued for this finding."))
	}
	if p.dbErr != "" {
		all = append(all, "", st.ErrText.Render(truncate("ws.db error: "+p.dbErr, innerW)))
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Plan & Diff Preview · "+truncate(f.Title, 24), "", vis, width, height, focused, st)
}

func loadDoctorCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return doctorLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.LoadDoctor()
		return doctorLoadedMsg{summary: summary, err: err}
	}
}

func loadDBStatusCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return dbStatusLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		status, err := backend.LoadDBStatus()
		return dbStatusLoadedMsg{status: status, err: err}
	}
}

func backupDBCmd(backend Backend, ifChanged bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return dbBackedUpMsg{err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.BackupDB(ifChanged)
		return dbBackedUpMsg{result: res, err: err}
	}
}

func undoLastCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return undoDoneMsg{err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.UndoLast()
		return undoDoneMsg{summary: summary, err: err}
	}
}
