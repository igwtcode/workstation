package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

type overviewJumpMsg struct {
	panel        int
	subView      int
	focusPreview bool
	ghKind       string
}

type overviewSectionID string

const (
	ovSecOutdated  overviewSectionID = "outdated"
	ovSecDoctor    overviewSectionID = "doctor"
	ovSecRepos     overviewSectionID = "repos"
	ovSecPRs       overviewSectionID = "prs"
	ovSecUntracked overviewSectionID = "untracked"
	ovSecBackup    overviewSectionID = "backup"
)

type overviewRow struct {
	ID       overviewSectionID
	Section  string
	Status   string
	Severity string
	Summary  string
	Target   string
	Panel    int
	SubView  int
}

type overviewPanel struct {
	host            HostBadge
	scopeZone       string
	filterQuery     string
	cursor          int
	previewScroll   int
	nowFn           func() time.Time
	outdatedLoaded  bool
	outdatedErr     string
	outdatedItems   []PackageOutdatedInfo
	digestOutdated  int
	digestLoaded    bool
	doctorLoaded    bool
	doctorErr       string
	doctor          DoctorSummary
	reposLoaded     bool
	reposErr        string
	repos           []RepoRow
	ghLoaded        bool
	ghErr           string
	ghItems         []GHItemRow
	untrackedLoaded bool
	untrackedErr    string
	untracked       []PresetToolRow
	dbLoaded        bool
	dbErr           string
	dbStatus        DBStatusSummary
	refreshInFlight bool
}

func newOverviewPanel() overviewPanel {
	return overviewPanel{
		nowFn: time.Now,
	}
}

func (p overviewPanel) ID() string                                        { return "overview" }
func (p overviewPanel) Title() string                                     { return "Workstation Overview" }
func (p overviewPanel) ShortTitle() string                                { return "Overview" }
func (p overviewPanel) SubViews() []string                                { return nil }
func (p overviewPanel) ActiveSubView() int                                { return 0 }
func (p overviewPanel) SetSubView(int) ResourcePanel                      { return p }
func (p overviewPanel) OnSubViewChanged(Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p overviewPanel) IsZoned() bool                                     { return true }
func (p overviewPanel) FilterQuery() string                               { return p.filterQuery }
func (p overviewPanel) FilterTarget() string                              { return "Overview" }
func (p overviewPanel) OnBlur() ResourcePanel                             { return p }
func (p overviewPanel) SelectedCount() int                                { return 0 }
func (p overviewPanel) ClearSelection() ResourcePanel                     { return p }
func (p overviewPanel) SupportsPreview() bool                             { return true }
func (p overviewPanel) DefaultSplit(width int) SplitMode {
	if width >= 96 {
		return SplitVertical
	}
	return SplitOff
}
func (p overviewPanel) CapturingInput() bool                      { return false }
func (p overviewPanel) HasOpenModal() bool                        { return false }
func (p overviewPanel) MutationInFlight() bool                    { return false }
func (p overviewPanel) CanPop() bool                              { return false }
func (p overviewPanel) Pop() ResourcePanel                        { return p }
func (p overviewPanel) ViewModal(int, int, Styles) (string, bool) { return "", false }
func (p overviewPanel) CLIEquivalent() string                     { return "ws status" }

func (p overviewPanel) SetScope(zone string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.scopeZone = strings.TrimSpace(zone)
	return p, nil
}

func (p overviewPanel) SetFilterQuery(q string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.cursor = 0
	p.previewScroll = 0
	return p, nil
}

func (p overviewPanel) now() time.Time {
	if p.nowFn != nil {
		return p.nowFn()
	}
	return time.Now()
}

func (p overviewPanel) dirtyAndBehindRepos() (dirty, behind int, flagged []RepoRow) {
	for _, r := range p.repos {
		if r.Dirty {
			dirty++
		}
		if r.Behind > 0 {
			behind++
		}
		if r.Dirty || r.Behind > 0 {
			flagged = append(flagged, r)
		}
	}
	return dirty, behind, flagged
}

func (p overviewPanel) reviewPRs() []GHItemRow {
	var out []GHItemRow
	for _, it := range p.ghItems {
		if !strings.EqualFold(it.Kind, "pr") {
			continue
		}
		if it.State != "" && !strings.EqualFold(it.State, "open") {
			continue
		}
		if it.IsDraft {
			continue
		}
		out = append(out, it)
	}
	return out
}

func (p overviewPanel) outdatedCount() (int, bool, string) {
	if p.outdatedLoaded {
		if p.outdatedErr != "" {
			if p.digestLoaded {
				return p.digestOutdated, true, "cached"
			}
			return 0, false, ""
		}
		return len(p.outdatedItems), true, "live"
	}
	if p.digestLoaded {
		return p.digestOutdated, true, "cached"
	}
	return 0, false, ""
}

func (p overviewPanel) doctorCounts() (errs, warns, infos int) {
	for _, f := range p.doctor.Findings {
		switch strings.ToLower(strings.TrimSpace(f.Severity)) {
		case "err", "error":
			errs++
		case "warn", "warning":
			warns++
		default:
			infos++
		}
	}
	return errs, warns, infos
}

func (p overviewPanel) backupAge() (age string, hasTime bool, stale bool) {
	raw := strings.TrimSpace(p.dbStatus.LastBackupAt)
	if raw == "" {
		return "never", false, true
	}
	var parsed time.Time
	var err error
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		parsed, err = time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			break
		}
	}
	if err != nil {
		return raw, true, false
	}
	d := p.now().Sub(parsed)
	return formatAgeDuration(d), true, d > 7*24*time.Hour
}

func formatAgeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (p overviewPanel) buildRows() []overviewRow {
	rows := make([]overviewRow, 0, 6)

	outCnt, outAvail, outSrc := p.outdatedCount()
	outRow := overviewRow{
		ID:      ovSecOutdated,
		Section: "Outdated packages",
		Target:  "3 Packages → Installed",
		Panel:   PanelPackages,
		SubView: pkgSubInstalled,
	}
	if !outAvail {
		if p.outdatedErr != "" {
			outRow.Status = "N/A"
			outRow.Severity = "info"
			outRow.Summary = "unavailable (" + truncate(p.outdatedErr, 34) + ")"
		} else {
			outRow.Status = "…"
			outRow.Severity = "info"
			outRow.Summary = "checking outdated packages..."
		}
	} else if outCnt == 0 {
		outRow.Status = "OK"
		outRow.Severity = "ok"
		outRow.Summary = fmt.Sprintf("0 outdated (%s)", outSrc)
	} else {
		outRow.Status = fmt.Sprintf("%d", outCnt)
		outRow.Severity = "warn"
		outRow.Summary = fmt.Sprintf("%d outdated (%s)", outCnt, outSrc)
	}
	rows = append(rows, outRow)

	docRow := overviewRow{
		ID:      ovSecDoctor,
		Section: "Doctor findings",
		Target:  "6 Workstation → Doctor",
		Panel:   PanelWorkstation,
		SubView: wsSubDoctor,
	}
	if !p.doctorLoaded {
		docRow.Status = "…"
		docRow.Severity = "info"
		docRow.Summary = "running diagnostics..."
	} else if p.doctorErr != "" {
		docRow.Status = "N/A"
		docRow.Severity = "info"
		docRow.Summary = "unavailable (" + truncate(p.doctorErr, 34) + ")"
	} else {
		errs, warns, infos := p.doctorCounts()
		switch {
		case errs > 0:
			docRow.Status = fmt.Sprintf("%d ERR", errs)
			docRow.Severity = "err"
		case warns > 0:
			docRow.Status = fmt.Sprintf("%d WARN", warns)
			docRow.Severity = "warn"
		default:
			docRow.Status = "OK"
			docRow.Severity = "ok"
		}
		docRow.Summary = fmt.Sprintf("%d err · %d warn · %d info", errs, warns, infos)
	}
	rows = append(rows, docRow)

	repoRow := overviewRow{
		ID:      ovSecRepos,
		Section: "Dirty / behind repos",
		Target:  "2 Projects → Repos",
		Panel:   PanelProjects,
		SubView: projSubRepos,
	}
	if !p.reposLoaded {
		repoRow.Status = "…"
		repoRow.Severity = "info"
		repoRow.Summary = "scanning repositories..."
	} else if p.reposErr != "" {
		repoRow.Status = "N/A"
		repoRow.Severity = "info"
		repoRow.Summary = "unavailable (" + truncate(p.reposErr, 34) + ")"
	} else {
		dirty, behind, flagged := p.dirtyAndBehindRepos()
		if len(flagged) == 0 {
			repoRow.Status = "OK"
			repoRow.Severity = "ok"
			repoRow.Summary = fmt.Sprintf("all %d repos clean & synced", len(p.repos))
		} else {
			repoRow.Status = fmt.Sprintf("%d", len(flagged))
			repoRow.Severity = "warn"
			repoRow.Summary = fmt.Sprintf("%d dirty · %d behind (of %d)", dirty, behind, len(p.repos))
		}
	}
	rows = append(rows, repoRow)

	prRow := overviewRow{
		ID:      ovSecPRs,
		Section: "PRs awaiting review",
		Target:  "2 Projects → Pull Requests",
		Panel:   PanelProjects,
		SubView: projSubPRs,
	}
	if !p.ghLoaded {
		prRow.Status = "…"
		prRow.Severity = "info"
		prRow.Summary = "loading cached GitHub PRs..."
	} else if p.ghErr != "" {
		prRow.Status = "N/A"
		prRow.Severity = "info"
		prRow.Summary = "unavailable (" + truncate(p.ghErr, 34) + ")"
	} else {
		prs := p.reviewPRs()
		if len(prs) == 0 {
			prRow.Status = "0"
			prRow.Severity = "ok"
			prRow.Summary = "no open PRs awaiting review"
		} else {
			prRow.Status = fmt.Sprintf("%d", len(prs))
			prRow.Severity = "info"
			prRow.Summary = fmt.Sprintf("%d open PR(s) in queue", len(prs))
		}
	}
	rows = append(rows, prRow)

	untrRow := overviewRow{
		ID:      ovSecUntracked,
		Section: "Untracked packages",
		Target:  "3 Packages → Installed",
		Panel:   PanelPackages,
		SubView: pkgSubInstalled,
	}
	if !p.untrackedLoaded {
		untrRow.Status = "…"
		untrRow.Severity = "info"
		untrRow.Summary = "checking untracked packages..."
	} else if p.untrackedErr != "" {
		untrRow.Status = "N/A"
		untrRow.Severity = "info"
		untrRow.Summary = "unavailable (" + truncate(p.untrackedErr, 34) + ")"
	} else if len(p.untracked) == 0 {
		untrRow.Status = "OK"
		untrRow.Severity = "ok"
		untrRow.Summary = "0 untracked OS packages"
	} else {
		untrRow.Status = fmt.Sprintf("%d", len(p.untracked))
		untrRow.Severity = "warn"
		untrRow.Summary = fmt.Sprintf("%d installed outside pkg/*.txt", len(p.untracked))
	}
	rows = append(rows, untrRow)

	bkRow := overviewRow{
		ID:      ovSecBackup,
		Section: "Last backup age",
		Target:  "4 Env & Secrets → Vault",
		Panel:   PanelEnv,
		SubView: envSubVault,
	}
	if !p.dbLoaded {
		bkRow.Status = "…"
		bkRow.Severity = "info"
		bkRow.Summary = "checking ws.db backup..."
	} else if p.dbErr != "" {
		bkRow.Status = "N/A"
		bkRow.Severity = "info"
		bkRow.Summary = "unavailable (" + truncate(p.dbErr, 34) + ")"
	} else {
		age, hasTime, stale := p.backupAge()
		if !hasTime {
			bkRow.Status = "NEVER"
			bkRow.Severity = "warn"
			bkRow.Summary = "no ws.db backup recorded"
		} else if p.dbStatus.DirtySinceBackup {
			bkRow.Status = age
			bkRow.Severity = "warn"
			bkRow.Summary = fmt.Sprintf("%s · dirty since backup", age)
		} else if stale {
			bkRow.Status = age
			bkRow.Severity = "warn"
			bkRow.Summary = fmt.Sprintf("%s · backup older than 7d", age)
		} else {
			bkRow.Status = age
			bkRow.Severity = "ok"
			bkRow.Summary = fmt.Sprintf("%s · clean", age)
		}
	}
	rows = append(rows, bkRow)

	return rows
}

func (p overviewPanel) visibleRows() []overviewRow {
	all := p.buildRows()
	if strings.TrimSpace(p.filterQuery) == "" {
		return all
	}
	return fuzzy.Filter(all, p.filterQuery, func(r overviewRow) ([]string, map[string]string) {
		return []string{r.Section, r.Summary, r.Target, r.Status}, map[string]string{
			"sev": r.Severity,
			"id":  string(r.ID),
		}
	})
}

func (p overviewPanel) selectedRow() (overviewRow, bool) {
	rows := p.visibleRows()
	if len(rows) == 0 {
		return overviewRow{}, false
	}
	idx := clamp(p.cursor, 0, len(rows)-1)
	return rows[idx], true
}

func (p overviewPanel) Attention() AttentionLevel {
	errs, warns, _ := p.doctorCounts()
	if errs > 0 {
		return AttentionErr
	}
	if warns > 0 {
		return AttentionWarn
	}
	if _, _, flagged := p.dirtyAndBehindRepos(); len(flagged) > 0 {
		return AttentionWarn
	}
	if cnt, ok, _ := p.outdatedCount(); ok && cnt > 0 {
		return AttentionWarn
	}
	if len(p.untracked) > 0 {
		return AttentionWarn
	}
	return AttentionNone
}

func (p overviewPanel) Badge() string {
	errs, warns, _ := p.doctorCounts()
	if errs > 0 {
		return fmt.Sprintf("%d err", errs)
	}
	if warns > 0 {
		return fmt.Sprintf("%d warn", warns)
	}
	return "OK"
}

func (p overviewPanel) PaneStatus() string {
	loading := 0
	if !p.outdatedLoaded && !p.digestLoaded {
		loading++
	}
	if !p.doctorLoaded {
		loading++
	}
	if !p.reposLoaded {
		loading++
	}
	if !p.ghLoaded {
		loading++
	}
	if !p.untrackedLoaded {
		loading++
	}
	if !p.dbLoaded {
		loading++
	}
	if loading > 0 {
		return fmt.Sprintf("loading %d section(s)...", loading)
	}
	return ""
}

func (p overviewPanel) Breadcrumbs() []string {
	if r, ok := p.selectedRow(); ok {
		return []string{"Overview", r.Section}
	}
	return []string{"Overview"}
}

func (p overviewPanel) StatusChips() []string {
	var chips []string
	if p.host.Preset != "" {
		chips = append(chips, "preset:"+p.host.Preset)
	}
	if p.scopeZone != "" {
		chips = append(chips, "zone:"+p.scopeZone)
	}
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	return chips
}

func (p overviewPanel) KeyHints(SplitMode, bool) [][2]string {
	return [][2]string{
		{"enter", "Jump to tab"},
		{"j/k", "Select section"},
		{"r", "Refresh all"},
		{"y", "Copy ws status"},
		{"1-6/0", "Switch tab"},
	}
}

func (p overviewPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case hostLoadedMsg:
		if msg.err == nil {
			p.host = msg.badge
		}
		return p, nil
	case doctorLoadedMsg:
		p.doctorLoaded = true
		p.refreshInFlight = false
		if msg.err != nil {
			p.doctorErr = msg.err.Error()
		} else {
			p.doctorErr = ""
			p.doctor = msg.summary
			p.digestLoaded = true
			p.digestOutdated = msg.summary.PackagesOutdated
		}
		return p, nil
	case pkgOutdatedLoadedMsg:
		p.outdatedLoaded = true
		if msg.err != nil {
			p.outdatedErr = msg.err.Error()
		} else {
			p.outdatedErr = ""
			seen := map[string]bool{}
			items := make([]PackageOutdatedInfo, 0, len(msg.items))
			for _, it := range msg.items {
				key := strings.ToLower(it.Provider + ":" + it.Name)
				if seen[key] {
					continue
				}
				seen[key] = true
				items = append(items, it)
			}
			sort.Slice(items, func(i, j int) bool {
				if items[i].Provider != items[j].Provider {
					return items[i].Provider < items[j].Provider
				}
				return items[i].Name < items[j].Name
			})
			p.outdatedItems = items
		}
		return p, nil
	case reposLoadedMsg:
		p.reposLoaded = true
		if msg.err != nil {
			p.reposErr = msg.err.Error()
		} else {
			p.reposErr = ""
			p.repos = append([]RepoRow(nil), msg.repos...)
		}
		return p, nil
	case ghItemsLoadedMsg:
		p.ghLoaded = true
		if msg.err != nil {
			p.ghErr = msg.err.Error()
		} else {
			p.ghErr = ""
			p.ghItems = append([]GHItemRow(nil), msg.items...)
		}
		return p, nil
	case pkgUntrackedLoadedMsg:
		p.untrackedLoaded = true
		if msg.err != nil {
			p.untrackedErr = msg.err.Error()
		} else {
			p.untrackedErr = ""
			p.untracked = append([]PresetToolRow(nil), msg.rows...)
		}
		return p, nil
	case presetsLoadedMsg:
		if msg.err != nil && !p.untrackedLoaded {
			p.untrackedLoaded = true
			p.untrackedErr = msg.err.Error()
		}
		return p, nil
	case dbStatusLoadedMsg:
		p.dbLoaded = true
		if msg.err != nil {
			p.dbErr = msg.err.Error()
		} else {
			p.dbErr = ""
			p.dbStatus = msg.status
		}
		return p, nil
	case tea.KeyPressMsg:
		rows := p.visibleRows()
		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview && split != SplitOff {
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 200, 12, m)
				return p, nil
			}
			nextCursor, moved := applyListMotion(p.cursor, len(rows), 10, m)
			if moved {
				p.cursor = nextCursor
				p.previewScroll = 0
			}
			return p, nil
		}
		switch msg.String() {
		case "enter":
			if sel, ok := p.selectedRow(); ok {
				return p, func() tea.Msg {
					return overviewJumpMsg{
						panel:   sel.Panel,
						subView: sel.SubView,
					}
				}
			}
		case "r":
			if backend == nil {
				return p, nil
			}
			p.refreshInFlight = true
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Refreshing overview..."} },
				loadDoctorCmd(backend),
				loadPkgOutdatedCmd(backend),
				loadReposCmd(backend, false, p.scopeZone),
				loadGHItemsCmd(backend, false, p.scopeZone),
				loadUntrackedPkgsCmd(backend, p.host.Preset),
				loadDBStatusCmd(backend),
			)
		case "y":
			return p, copyCmd("ws status", "CLI command")
		}
	}
	return p, nil
}

func (p overviewPanel) ViewMain(width, height int, focused bool, st Styles) string {
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	rows := p.visibleRows()
	if p.cursor >= len(rows) {
		p.cursor = max(0, len(rows)-1)
	}

	showTarget := innerW >= 96
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		statusCell := r.Status
		switch r.Severity {
		case "ok":
			statusCell = st.OKText.Render(r.Status)
		case "warn":
			statusCell = st.WarnText.Render(r.Status)
		case "err":
			statusCell = st.ErrText.Render(r.Status)
		default:
			statusCell = st.InfoText.Render(r.Status)
		}
		if showTarget {
			tableRows = append(tableRows, []string{
				r.Section,
				statusCell,
				r.Summary,
				st.Muted.Render(r.Target),
			})
		} else {
			tableRows = append(tableRows, []string{
				r.Section,
				statusCell,
				r.Summary,
			})
		}
	}

	headers := []string{"SECTION", "STATUS", "SUMMARY"}
	if showTarget {
		headers = append(headers, "DETAILS")
	}
	body := renderDataTable(headers, tableRows, p.cursor, focused, innerW, max(1, innerH-2), st)
	if len(rows) == 0 {
		body = []string{st.Muted.Render("No overview sections match filter.")}
	}
	if innerH > len(body)+1 {
		body = append(body, "", st.Muted.Render("Press Enter on any row to jump to its details tab · r to refresh"))
	}
	return renderTitledBox("Workstation Overview", p.Badge(), body, width, height, focused, st)
}

func (p overviewPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := max(12, width-4)
	innerH := max(1, height-2)
	if borderless {
		innerW = max(12, width)
		innerH = max(1, height)
	}

	sel, ok := p.selectedRow()
	if !ok {
		lines := []string{st.Muted.Render("Select an overview section to inspect.")}
		if borderless {
			return fitLines(strings.Join(lines, "\n"), height)
		}
		return renderTitledBox("Section Details", "", lines, width, height, focused, st)
	}

	var lines []string
	lines = append(lines, st.SectionHeader.Render(sel.Section))
	lines = append(lines, st.Muted.Render("Jump target: "+sel.Target+" (Enter)"))
	lines = append(lines, "")

	switch sel.ID {
	case ovSecOutdated:
		cnt, avail, src := p.outdatedCount()
		if !avail {
			if p.outdatedErr != "" {
				lines = append(lines, st.WarnText.Render("Unavailable: "+p.outdatedErr))
			} else {
				lines = append(lines, st.Muted.Render("Loading outdated packages..."))
			}
		} else {
			lines = append(lines, fmt.Sprintf("Outdated packages: %d (%s)", cnt, src))
			if len(p.outdatedItems) > 0 {
				lines = append(lines, "")
				for _, it := range p.outdatedItems {
					lines = append(lines, fmt.Sprintf("  [%s] %s  %s -> %s", it.Provider, it.Name, nonEmpty(it.Current, "?"), nonEmpty(it.Latest, "?")))
				}
			} else if cnt == 0 {
				lines = append(lines, st.OKText.Render("All checked packages are up to date."))
			}
		}

	case ovSecDoctor:
		if !p.doctorLoaded {
			lines = append(lines, st.Muted.Render("Running workstation diagnostics..."))
		} else if p.doctorErr != "" {
			lines = append(lines, st.WarnText.Render("Unavailable: "+p.doctorErr))
		} else {
			errs, warns, infos := p.doctorCounts()
			lines = append(lines, fmt.Sprintf("Errors: %d   Warnings: %d   Info: %d", errs, warns, infos))
			lines = append(lines, fmt.Sprintf("Links: %d/%d ok   Templates: %d/%d ok", p.doctor.LinksOK, p.doctor.LinksTotal, p.doctor.TemplatesOK, p.doctor.TemplatesTotal))
			lines = append(lines, "")
			if len(p.doctor.Findings) == 0 {
				lines = append(lines, st.OKText.Render("All workstation checks are healthy."))
			} else {
				for _, f := range p.doctor.Findings {
					lines = append(lines, fmt.Sprintf("  %s [%s] %s", severityBadge(f.Severity, st), f.Category, f.Title))
					if f.FixCLI != "" {
						lines = append(lines, "     "+st.Muted.Render("fix: "+f.FixCLI))
					}
				}
			}
		}

	case ovSecRepos:
		if !p.reposLoaded {
			lines = append(lines, st.Muted.Render("Scanning repositories..."))
		} else if p.reposErr != "" {
			lines = append(lines, st.WarnText.Render("Unavailable: "+p.reposErr))
		} else {
			dirty, behind, flagged := p.dirtyAndBehindRepos()
			lines = append(lines, fmt.Sprintf("Total repos: %d   Dirty: %d   Behind: %d", len(p.repos), dirty, behind))
			lines = append(lines, "")
			if len(flagged) == 0 {
				lines = append(lines, st.OKText.Render("Every repository is clean and up to date."))
			} else {
				for _, r := range flagged {
					var flags []string
					if r.Dirty {
						flags = append(flags, "dirty")
					}
					if r.Behind > 0 {
						flags = append(flags, fmt.Sprintf("-%d behind", r.Behind))
					}
					if r.Ahead > 0 {
						flags = append(flags, fmt.Sprintf("+%d ahead", r.Ahead))
					}
					zoneTag := ""
					if r.Zone != "" {
						zoneTag = "[" + r.Zone + "] "
					}
					lines = append(lines, fmt.Sprintf("  %s%s (%s) · %s", zoneTag, r.Name, nonEmpty(r.Branch, "HEAD"), strings.Join(flags, ", ")))
				}
			}
		}

	case ovSecPRs:
		if !p.ghLoaded {
			lines = append(lines, st.Muted.Render("Loading cached GitHub PRs..."))
		} else if p.ghErr != "" {
			lines = append(lines, st.WarnText.Render("Unavailable: "+p.ghErr))
			lines = append(lines, st.Muted.Render("Verify gh auth status or network connectivity."))
		} else {
			prs := p.reviewPRs()
			lines = append(lines, fmt.Sprintf("Open PRs awaiting review: %d", len(prs)))
			lines = append(lines, "")
			if len(prs) == 0 {
				lines = append(lines, st.OKText.Render("No open PRs waiting in your review queue."))
			} else {
				for _, pr := range prs {
					lines = append(lines, fmt.Sprintf("  #%d %s · %s", pr.Number, pr.Repo, truncate(pr.Title, max(12, innerW-24))))
					meta := strings.TrimSpace(strings.Join([]string{pr.Author, pr.UpdatedAt}, " · "))
					if meta != "" {
						lines = append(lines, "     "+st.Muted.Render(meta))
					}
				}
			}
		}

	case ovSecUntracked:
		if !p.untrackedLoaded {
			lines = append(lines, st.Muted.Render("Checking untracked OS packages..."))
		} else if p.untrackedErr != "" {
			lines = append(lines, st.WarnText.Render("Unavailable: "+p.untrackedErr))
		} else {
			lines = append(lines, fmt.Sprintf("Untracked packages: %d", len(p.untracked)))
			lines = append(lines, "")
			if len(p.untracked) == 0 {
				lines = append(lines, st.OKText.Render("All explicitly installed OS packages are tracked."))
			} else {
				for _, u := range p.untracked {
					name := nonEmpty(u.PackageName, u.ID)
					lines = append(lines, fmt.Sprintf("  [%s] %s -> %s", u.Provider, name, nonEmpty(u.OriginFile, "pkg/*.txt")))
				}
			}
		}

	case ovSecBackup:
		if !p.dbLoaded {
			lines = append(lines, st.Muted.Render("Checking ws.db backup status..."))
		} else if p.dbErr != "" {
			lines = append(lines, st.WarnText.Render("Unavailable: "+p.dbErr))
		} else {
			age, hasTime, _ := p.backupAge()
			lines = append(lines, fmt.Sprintf("Last backup: %s", nonEmpty(p.dbStatus.LastBackupAt, "never")))
			if hasTime {
				lines = append(lines, fmt.Sprintf("Backup age:  %s", age))
			}
			dirtyStr := "clean"
			if p.dbStatus.DirtySinceBackup {
				dirtyStr = "dirty (changes since last backup)"
			}
			lines = append(lines, fmt.Sprintf("State:       %s", dirtyStr))
			lines = append(lines, fmt.Sprintf("Key source:  %s", nonEmpty(p.dbStatus.KeySource, "none")))
			lines = append(lines, fmt.Sprintf("Records:     %d env · %d blobs · %d repos", p.dbStatus.EnvCount, p.dbStatus.BlobCount, p.dbStatus.RepoCount))
		}
	}

	maxScroll := max(0, len(lines)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	end := min(len(lines), sc+innerH)
	visible := lines[sc:end]

	if borderless {
		out := make([]string, 0, height)
		for _, ln := range visible {
			out = append(out, truncate(ln, width))
		}
		for len(out) < height {
			out = append(out, "")
		}
		return strings.Join(out, "\n")
	}
	return renderTitledBox(sel.Section, sel.Status, visible, width, height, focused, st)
}
