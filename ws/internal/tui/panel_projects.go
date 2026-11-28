package tui

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

const (
	projSubRepos = iota
	projSubPRs
	projSubIssues
	projSubCount
)

type ghModalKind int

const (
	ghModalNone ghModalKind = iota
	ghModalCopy
	ghModalMerge
	ghModalComment
)

type copyFieldChoice struct {
	Label string
	Value string
}

type ghDashActionDoneMsg struct {
	label   string
	summary string
	err     error
}

type repoEditorFinishedMsg struct {
	repo string
	err  error
}

type repoLazygitFinishedMsg struct {
	repo string
	err  error
}

var execLookPath = exec.LookPath

func lazygitAvailable() bool {
	_, err := execLookPath("lazygit")
	return err == nil
}

func repoWebURL(remoteURL string) string {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "codecommit://") || strings.HasPrefix(lower, "codecommit::") {
		return ""
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, ".") {
		return ""
	}
	if slug := projects.ExtractGitHubRepoSlug(raw); slug != "" {
		return "https://github.com/" + slug
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(raw)
		if err != nil || strings.TrimSpace(u.Host) == "" {
			return ""
		}
		u.User = nil
		u.Path = strings.TrimSuffix(u.Path, ".git")
		return u.String()
	}
	if strings.HasPrefix(lower, "ssh://") {
		u, err := url.Parse(raw)
		if err != nil || strings.TrimSpace(u.Hostname()) == "" {
			return ""
		}
		host := u.Hostname()
		if strings.Contains(strings.ToLower(host), "codecommit") {
			return ""
		}
		p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		if p == "" {
			return ""
		}
		return "https://" + host + "/" + p
	}
	if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.Contains(raw, "://") {
		hostPart := raw[:colon]
		pathPart := strings.TrimSuffix(strings.TrimPrefix(raw[colon+1:], "/"), ".git")
		if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		if hostPart == "" || pathPart == "" || strings.Contains(strings.ToLower(hostPart), "codecommit") {
			return ""
		}
		return "https://" + hostPart + "/" + pathPart
	}
	return ""
}

func buildRepoEditorCmd(dir string) (*exec.Cmd, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("repository path is empty")
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	var parts []string
	if editor != "" {
		parts = strings.Fields(editor)
	} else if _, err := execLookPath("nvim"); err == nil {
		parts = []string{"nvim"}
	} else if _, err := execLookPath("vi"); err == nil {
		parts = []string{"vi"}
	} else {
		return nil, fmt.Errorf("no $VISUAL or $EDITOR configured")
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("no $VISUAL or $EDITOR configured")
	}
	cmdArgs := append(parts[1:], dir)
	c := exec.Command(parts[0], cmdArgs...)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c, nil
}

func buildRepoLazygitCmd(dir string) (*exec.Cmd, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("repository path is empty")
	}
	bin, err := execLookPath("lazygit")
	if err != nil {
		return nil, fmt.Errorf("lazygit is not installed")
	}
	c := exec.Command(bin)
	c.Dir = dir
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c, nil
}

type projectsPanel struct {
	subView       int
	repos         []RepoRow
	filteredRepos []RepoRow
	repoSel       SelectionSet
	ghItems       []GHItemRow
	filteredGH    []GHItemRow

	cwdRepoPath    string
	cwdRepoSlug    string
	cwdBranch      string
	cwdIssueHint   int
	userPickedRepo bool
	userMovedGH    bool

	ghFocused         bool
	ghDetailOpen      bool
	repoInfoMode      bool
	repoInfoFromRepos bool
	ghForceRefresh    bool
	ghActing          bool
	ghActionID        int

	repoItems      map[string][]GHItemRow
	repoFetchedAt  map[string]int64
	repoLoading    map[string]bool
	repoErrs       map[string]string
	detailCache    map[string]GHItemDetail
	detailLoading  map[string]bool
	detailErrs     map[string]string
	previewScroll  int
	ghKindFilter   string
	ghAllMine      bool
	ghRepoOverride string
	ghStateFilter  string
	repoQuery      string
	ghQuery        string
	repoFilters    FacetFilterState
	ghFilters      FacetFilterState

	hasCDFile   bool
	zone        string
	repoCursor  int
	ghCursor    int
	reposLoaded bool
	ghLoaded    bool
	loadErr     string
	ghErr       string
	pulling     bool
	cloning     bool
	repoActID   int
	ghActID     int
	pullActID   int
	cloneActID  int

	cloneOpen         bool
	cloneURL          textinput.Model
	cloneZone         textinput.Model
	cloneFocus        int
	cloneConfirmFocus bool

	modalKind    ghModalKind
	modalTarget  GHItemRow
	copyChoices  []copyFieldChoice
	copyCursor   int
	mergeMethods []string
	mergeCursor  int
	commentInput textinput.Model
}

func newProjectsPanel(hasCDFile bool) projectsPanel {
	urlIn := textinput.New()
	urlIn.Placeholder = "owner/repo or https://... or codecommit://..."
	urlIn.CharLimit = 256
	urlIn.SetWidth(40)

	zoneIn := textinput.New()
	zoneIn.Placeholder = "personal | work (optional)"
	zoneIn.CharLimit = 64
	zoneIn.SetWidth(28)

	cmIn := textinput.New()
	cmIn.Placeholder = "Write your GitHub comment..."
	cmIn.CharLimit = 2000
	cmIn.SetWidth(54)

	return projectsPanel{
		hasCDFile:     hasCDFile,
		cloneURL:      urlIn,
		cloneZone:     zoneIn,
		commentInput:  cmIn,
		mergeMethods:  []string{"squash", "merge", "rebase"},
		repoItems:     map[string][]GHItemRow{},
		repoFetchedAt: map[string]int64{},
		repoLoading:   map[string]bool{},
		repoErrs:      map[string]string{},
		detailCache:   map[string]GHItemDetail{},
		detailLoading: map[string]bool{},
		detailErrs:    map[string]string{},
		ghKindFilter:  "all",
		ghStateFilter: "open",
	}
}

func (p projectsPanel) isGHDedicatedSubView() bool {
	return p.subView == projSubPRs || p.subView == projSubIssues
}

func (p projectsPanel) ID() string         { return "projects" }
func (p projectsPanel) Title() string      { return "Projects & GitHub" }
func (p projectsPanel) ShortTitle() string { return "Projects" }
func (p projectsPanel) SubViews() []string { return []string{"Repos", "Pull Requests", "Issues"} }
func (p projectsPanel) ActiveSubView() int { return p.subView }
func (p projectsPanel) SetSubView(idx int) ResourcePanel {
	if idx < 0 || idx >= projSubCount {
		idx = 0
	}
	p.subView = idx
	switch idx {
	case projSubPRs:
		p.ghKindFilter = "pr"
		p.ghFocused = true
		p.repoInfoMode = false
		p.ghDetailOpen = false
	case projSubIssues:
		p.ghKindFilter = "issue"
		p.ghFocused = true
		p.repoInfoMode = false
		p.ghDetailOpen = false
	default:
		p.ghKindFilter = "all"
		p.ghDetailOpen = false
	}
	p.ghCursor = 0
	p.previewScroll = 0
	p.userMovedGH = false
	p.recomputeFilteredGH()
	return p
}
func (p projectsPanel) IsZoned() bool         { return true }
func (p projectsPanel) SupportsPreview() bool { return true }
func (p projectsPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	if p.isGHDedicatedSubView() {
		p.ghFocused = true
		p.repoInfoMode = false
		p.ghDetailOpen = false
		force := p.ghForceRefresh
		p.ghForceRefresh = false
		return p, p.ensureActiveRepoAndDetailCmds(backend, force, false)
	}
	return p, nil
}

func (p projectsPanel) DefaultSplit(width int) SplitMode {
	if width >= 96 {
		return SplitVertical
	}
	return SplitHorizontal
}

func (p projectsPanel) CanPop() bool {
	if p.modalKind != ghModalNone {
		return true
	}
	if p.isGHDedicatedSubView() {
		return p.repoInfoMode
	}
	return p.ghDetailOpen || p.repoInfoMode || p.ghRepoOverride != ""
}

func (p projectsPanel) Pop() ResourcePanel {
	if p.modalKind != ghModalNone {
		p.closeGHModal()
		return p
	}
	if p.ghDetailOpen && !p.isGHDedicatedSubView() {
		p.ghDetailOpen = false
		p.previewScroll = 0
		return p
	}
	if p.repoInfoMode {
		p.repoInfoMode = false
		p.repoInfoFromRepos = false
		p.previewScroll = 0
		return p
	}
	if p.ghRepoOverride != "" {
		p.ghRepoOverride = ""
		p.recomputeFilteredGH()
	}
	return p
}

func (p projectsPanel) SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd) {
	p.zone = zone
	p.reposLoaded = false
	p.ghLoaded = false
	p.loadErr = ""
	p.ghErr = ""
	p.repoCursor = 0
	p.ghCursor = 0
	p.previewScroll = 0
	p.ghDetailOpen = false
	p.repoInfoMode = false
	p.ghRepoOverride = ""
	p.userPickedRepo = false
	p.userMovedGH = false
	p.repoSel = p.repoSel.Clear()
	p.repoItems = map[string][]GHItemRow{}
	p.repoFetchedAt = map[string]int64{}
	p.repoLoading = map[string]bool{}
	p.repoErrs = map[string]string{}
	p.detailCache = map[string]GHItemDetail{}
	p.detailLoading = map[string]bool{}
	p.detailErrs = map[string]string{}
	if backend == nil {
		return p, nil
	}
	p.repoActID = newActivityID()
	p.ghActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.repoActID, "Scanning repositories"),
		startActivityCmd(p.ghActID, "Loading GitHub PRs/Issues"),
		loadReposCmd(backend, false, zone),
		loadGHItemsCmd(backend, false, zone),
	)
}

func (p projectsPanel) isGHActiveMode() bool {
	return (p.ghFocused || p.isGHDedicatedSubView()) && !p.repoInfoMode
}

func (p projectsPanel) FilterQuery() string {
	if p.isGHActiveMode() {
		return p.ghQuery
	}
	return p.repoQuery
}

func (p projectsPanel) FilterTarget() string {
	if p.isGHActiveMode() {
		if p.subView == projSubPRs {
			return "Pull Requests"
		}
		if p.subView == projSubIssues {
			return "Issues"
		}
		return "PRs & Issues"
	}
	return "Repos"
}

func (p projectsPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	if p.isGHActiveMode() {
		p.ghQuery = q
		p.ghCursor = 0
		p.previewScroll = 0
		p.recomputeFilteredGH()
		if (p.ghDetailOpen || p.isGHDedicatedSubView()) && backend != nil {
			return p, p.ensureSelectedGHDetailCmd(backend, true, false)
		}
		return p, nil
	}
	p.repoQuery = q
	p.userPickedRepo = true
	p.ghRepoOverride = ""
	p.ghAllMine = false
	p.ghDetailOpen = false
	p.previewScroll = 0
	p.recomputeFilteredRepos()
	p.repoCursor = 0
	p.userMovedGH = false
	p.ghCursor = 0
	p.recomputeFilteredGH()
	if backend != nil {
		return p, p.loadSelectedRepoCacheOnlyCmd(backend)
	}
	return p, nil
}

func (p projectsPanel) OnBlur() ResourcePanel {
	p.cloneOpen = false
	p.cloneConfirmFocus = false
	p.cloneFocus = 0
	p.cloneURL.Blur()
	p.cloneZone.Blur()
	p.closeGHModal()
	p.repoFilters.Close()
	p.ghFilters.Close()
	return p
}

func (p projectsPanel) SelectedCount() int {
	return p.repoSel.Count()
}

func (p projectsPanel) ClearSelection() ResourcePanel {
	p.repoSel = p.repoSel.Clear()
	return p
}

func (p projectsPanel) Attention() AttentionLevel {
	if p.loadErr != "" || p.ghErr != "" {
		return AttentionErr
	}
	return AttentionNone
}

func (p projectsPanel) PaneStatus() string {
	slug, _, allMine, _ := p.effectiveGHScope()
	switch {
	case p.pulling:
		return "pulling..."
	case p.cloning:
		return "cloning..."
	case !allMine && slug != "" && p.repoLoading[repoCacheKey(slug, p.ghStateFilter)]:
		return "syncing gh..."
	case p.repoActID != 0 || p.ghActID != 0:
		return "scanning..."
	default:
		return ""
	}
}

func (p projectsPanel) Badge() string {
	if p.loadErr != "" && len(p.filteredRepos) == 0 {
		return "err"
	}
	if !p.reposLoaded {
		return "…"
	}
	return fmt.Sprintf("%d repos", len(p.filteredRepos))
}

func (p projectsPanel) Breadcrumbs() []string {
	slug, _, allMine, nonGHRepo := p.effectiveGHScope()
	scope := "None"
	switch {
	case allMine:
		scope = "All Mine"
	case slug != "":
		scope = slug
	case nonGHRepo:
		if r, ok := p.selectedRepo(); ok && r.Name != "" {
			scope = r.Name
		} else {
			scope = "Non-GitHub"
		}
	}
	if p.ghDetailOpen {
		if it, ok := p.selectedGHItem(); ok {
			return []string{"Projects", scope, fmt.Sprintf("#%d", it.Number)}
		}
	}
	if p.repoInfoMode {
		if r, ok := p.selectedRepo(); ok {
			return []string{"Projects", r.Name, "Local Files"}
		}
	}
	if p.subView == projSubPRs {
		return []string{"Projects", "Pull Requests", scope, fmt.Sprintf("(%d)", len(p.filteredGH))}
	}
	if p.subView == projSubIssues {
		return []string{"Projects", "Issues", scope, fmt.Sprintf("(%d)", len(p.filteredGH))}
	}
	if p.ghFocused {
		kind := "PRs & Issues"
		if p.ghKindFilter == "pr" {
			kind = "PRs"
		} else if p.ghKindFilter == "issue" {
			kind = "Issues"
		}
		return []string{"Projects", scope, fmt.Sprintf("%s (%d)", kind, len(p.filteredGH))}
	}
	if !allMine && (slug != "" || nonGHRepo) {
		return []string{"Projects", fmt.Sprintf("Repos (%d)", len(p.filteredRepos)), scope}
	}
	return []string{"Projects", fmt.Sprintf("Repos (%d)", len(p.filteredRepos))}
}

func (p projectsPanel) StatusChips() []string {
	var chips []string
	if p.repoQuery != "" {
		chips = append(chips, "repo:/"+truncate(p.repoQuery, 14))
	}
	if p.ghQuery != "" {
		chips = append(chips, "gh:/"+truncate(p.ghQuery, 14))
	}
	if p.ghAllMine {
		chips = append(chips, "scope:all")
	} else if slug, _, _, _ := p.effectiveGHScope(); slug != "" {
		chips = append(chips, "gh:"+slug)
	}
	if p.ghKindFilter != "all" {
		chips = append(chips, "type:"+p.ghKindFilter)
	}
	if p.ghStateFilter != "open" {
		chips = append(chips, "state:"+p.ghStateFilter)
	}
	if p.isGHActiveMode() {
		chips = append(chips, p.ghFilters.Chips()...)
	} else {
		chips = append(chips, p.repoFilters.Chips()...)
	}
	return chips
}

func (p projectsPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.isGHDedicatedSubView() {
		if focusPreview {
			return [][2]string{
				{"j/k", "Scroll reader"},
				{"o", "Open in browser"},
				{"y/Y", "Copy field / #"},
				{"r", "Refresh detail"},
				{"a", "PR/Issue actions"},
				{"esc", "Back"},
			}
		}
		return [][2]string{
			{"enter", "Focus detail"},
			{"o", "Open in browser"},
			{"y/Y", "Copy field / #"},
			{"r", "Refresh GH"},
			{"</>", "Prev / Next repo"},
			{"a", "PR/Issue actions"},
			{"F/S", "Filter / Sort"},
		}
	}
	if focusPreview && p.ghDetailOpen {
		return [][2]string{
			{"j/k", "Scroll reader"},
			{"o", "Open in browser"},
			{"y/Y", "Copy field / #"},
			{"r", "Refresh detail"},
			{"a", "PR/Issue actions"},
			{"esc", "Back to list"},
		}
	}
	if focusPreview && p.repoInfoMode {
		hints := [][2]string{
			{"j/k", "Scroll locals"},
			{"o", "Open remote"},
			{"e", "Open in editor"},
		}
		if lazygitAvailable() {
			hints = append(hints, [2]string{"l", "Lazygit"})
		}
		hints = append(hints,
			[2]string{"y/Y", "Copy path / URL"},
			[2]string{"a", "Repo actions"},
			[2]string{"esc", "Back"},
		)
		return hints
	}
	if focusPreview {
		return [][2]string{
			{"enter", "Read PR/Issue"},
			{"o", "Open in browser"},
			{"y/Y", "Copy URL / #"},
			{"r", "Refresh GH"},
			{"</>", "Prev / Next repo"},
			{"a", "PR/Issue actions"},
			{"F/S", "Filter / Sort"},
		}
	}
	cdVerb := "Repo detail"
	if p.hasCDFile {
		cdVerb = "Jump (cd)"
	}
	hints := [][2]string{
		{"enter", cdVerb},
		{"u", "Pull repo"},
		{"o", "Open remote"},
		{"e", "Open in editor"},
	}
	if lazygitAvailable() {
		hints = append(hints, [2]string{"l", "Lazygit"})
	}
	hints = append(hints,
		[2]string{"y/Y", "Copy path / URL"},
		[2]string{"r", "Refresh"},
		[2]string{"a", "Repo actions"},
		[2]string{"F/S", "Filter / Sort"},
		[2]string{"space/*", "Mark repos"},
	)
	return hints
}

func (p projectsPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	if p.repoInfoMode && focusPreview {
		actions := []RowAction{
			{Key: "P", Label: "Pull Requests", Description: "View GitHub pull requests for selected repo"},
			{Key: "I", Label: "Issues", Description: "View GitHub issues for selected repo"},
			{Key: "c", Label: "Clone repository", Description: "Clone a repository into a zone"},
		}
		return actions
	}
	if focusPreview || p.isGHDedicatedSubView() {
		actions := []RowAction{
			{Key: "A", Label: "Approve PR", Description: "Submit approving review via gh pr review --approve"},
			{Key: "M", Label: "Merge PR", Description: "Choose squash/merge/rebase and merge PR", Dangerous: true},
			{Key: "X", Label: "Close / Reopen", Description: "Toggle PR or issue open/closed state", Dangerous: true},
			{Key: "m", Label: "Add comment", Description: "Post a comment on selected PR or issue"},
			{Key: "K", Label: "Checkout PR", Description: "Check out selected PR branch locally"},
			{Key: "c", Label: "Copy gh command", Description: "Copy gh pr checkout / gh issue view command"},
			{Key: "t", Label: "Toggle PR / Issue", Description: "Cycle type filter (all / PRs / issues)"},
			{Key: "g", Label: "Toggle Repo / All Mine", Description: "Switch between current repo and all your items"},
			{Key: "s", Label: "Cycle state filter", Description: "Cycle open / closed / merged / all state"},
		}
		if !p.isGHDedicatedSubView() {
			actions = append(actions, RowAction{Key: "L", Label: "Repo detail & locals", Description: "Inspect selected repo metadata and local files"})
		}
		return actions
	}
	return []RowAction{
		{Key: "c", Label: "Clone repository", Description: "Clone a repository into a zone"},
		{Key: "L", Label: "Repo detail & locals", Description: "Inspect selected repo metadata and local files"},
		{Key: "P", Label: "Pull Requests", Description: "Focus GitHub pull requests for selected repo"},
		{Key: "I", Label: "Issues", Description: "Focus GitHub issues for selected repo"},
	}
}

func (p projectsPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if p.repoInfoMode && focusPreview {
		switch actionKey {
		case "P":
			p.repoInfoMode = false
			p.repoInfoFromRepos = false
			p.ghKindFilter = "pr"
			p.recomputeFilteredGH()
			return p, p.ensureActiveRepoAndDetailCmds(backend, false, false)
		case "I":
			p.repoInfoMode = false
			p.repoInfoFromRepos = false
			p.ghKindFilter = "issue"
			p.recomputeFilteredGH()
			return p, p.ensureActiveRepoAndDetailCmds(backend, false, false)
		case "c":
			return p.openCloneModal()
		}
		return p, nil
	}
	if focusPreview || p.isGHDedicatedSubView() {
		return p.runGHRowAction(actionKey, backend)
	}
	switch actionKey {
	case "c":
		return p.openCloneModal()
	case "L":
		p.repoInfoMode = !p.repoInfoMode
		p.repoInfoFromRepos = p.repoInfoMode
		p.ghDetailOpen = false
		p.previewScroll = 0
		if split == SplitOff {
			return p, func() tea.Msg { return focusPreviewMsg{focus: true, ensureSplit: true} }
		}
		return p, nil
	case "P":
		return p.focusRepoGHKind(backend, "pr")
	case "I":
		return p.focusRepoGHKind(backend, "issue")
	}
	return p, nil
}

func (p projectsPanel) openCloneModal() (ResourcePanel, tea.Cmd) {
	p.cloneOpen = true
	p.cloneURL.SetValue("")
	p.cloneZone.SetValue(p.zone)
	p.cloneFocus = 0
	p.cloneConfirmFocus = false
	return p, p.cloneURL.Focus()
}

func (p projectsPanel) focusRepoGHKind(backend Backend, kind string) (ResourcePanel, tea.Cmd) {
	p.userPickedRepo = true
	p.ghKindFilter = kind
	p.repoInfoMode = false
	p.repoInfoFromRepos = false
	p.ghDetailOpen = false
	p.ghRepoOverride = ""
	p.ghAllMine = false
	p.userMovedGH = false
	p.ghCursor = 0
	p.previewScroll = 0
	p.ghFocused = true
	p.recomputeFilteredGH()
	return p, batchNonNil(
		func() tea.Msg { return focusPreviewMsg{focus: true, ensureSplit: true} },
		p.ensureActiveRepoAndDetailCmds(backend, false, false),
	)
}

func (p projectsPanel) openSelectedRepoInBrowser() tea.Cmd {
	r, ok := p.selectedRepo()
	if !ok {
		return nil
	}
	u := repoWebURL(r.RemoteURL)
	if u == "" {
		return func() tea.Msg {
			return statusToastMsg{text: "No browser URL for " + nonEmpty(r.Name, "repo"), isErr: true}
		}
	}
	return openURLCmd(u)
}

func (p projectsPanel) launchRepoEditorCmd() tea.Cmd {
	r, ok := p.selectedRepo()
	if !ok || r.LocalPath == "" {
		return nil
	}
	cmd, err := buildRepoEditorCmd(r.LocalPath)
	if err != nil {
		return func() tea.Msg { return statusToastMsg{text: "Editor failed: " + err.Error(), isErr: true} }
	}
	repoName := r.Name
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return repoEditorFinishedMsg{repo: repoName, err: err}
	})
}

func (p projectsPanel) launchRepoLazygitCmd() tea.Cmd {
	r, ok := p.selectedRepo()
	if !ok || r.LocalPath == "" {
		return nil
	}
	cmd, err := buildRepoLazygitCmd(r.LocalPath)
	if err != nil {
		return func() tea.Msg { return statusToastMsg{text: "Lazygit failed: " + err.Error(), isErr: true} }
	}
	repoName := r.Name
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return repoLazygitFinishedMsg{repo: repoName, err: err}
	})
}

func (p projectsPanel) CapturingInput() bool {
	return (p.cloneOpen && p.cloneFocus < 2) || p.modalKind == ghModalComment || p.repoFilters.Open || p.ghFilters.Open
}
func (p projectsPanel) HasOpenModal() bool {
	return p.cloneOpen || p.modalKind != ghModalNone || p.repoFilters.Open || p.ghFilters.Open
}
func (p projectsPanel) MutationInFlight() bool { return p.pulling || p.cloning || p.ghActing }

func (p projectsPanel) selectedRepo() (RepoRow, bool) {
	if len(p.filteredRepos) == 0 || p.repoCursor < 0 || p.repoCursor >= len(p.filteredRepos) {
		return RepoRow{}, false
	}
	return p.filteredRepos[p.repoCursor], true
}

func (p projectsPanel) selectedGHItem() (GHItemRow, bool) {
	if len(p.filteredGH) == 0 || p.ghCursor < 0 || p.ghCursor >= len(p.filteredGH) {
		return GHItemRow{}, false
	}
	return p.filteredGH[p.ghCursor], true
}

func (p projectsPanel) effectiveGHScope() (repoSlug, zone string, allMine, nonGHRepo bool) {
	if p.ghAllMine {
		return "", p.zone, true, false
	}
	if p.ghRepoOverride != "" {
		return p.ghRepoOverride, p.zone, false, false
	}
	if r, ok := p.selectedRepo(); ok {
		slug := projects.ExtractGitHubRepoSlug(r.RemoteURL)
		if slug == "" {
			return "", nonEmpty(r.Zone, p.zone), false, true
		}
		return slug, nonEmpty(r.Zone, p.zone), false, false
	}
	if !p.reposLoaded && p.cwdRepoSlug != "" {
		return p.cwdRepoSlug, p.zone, false, false
	}
	return "", p.zone, false, false
}

func (p projectsPanel) repoMatchesCWD(r RepoRow) bool {
	if p.cwdRepoPath != "" && r.LocalPath != "" && filepath.Clean(r.LocalPath) == filepath.Clean(p.cwdRepoPath) {
		return true
	}
	if p.cwdRepoSlug != "" {
		if slug := projects.ExtractGitHubRepoSlug(r.RemoteURL); slug != "" && strings.EqualFold(slug, p.cwdRepoSlug) {
			return true
		}
	}
	return false
}

func (p *projectsPanel) autoSelectCWDRepo() {
	if p.userPickedRepo || (p.cwdRepoPath == "" && p.cwdRepoSlug == "") {
		return
	}
	for i, r := range p.filteredRepos {
		if p.repoMatchesCWD(r) {
			p.repoCursor = i
			return
		}
	}
}

func isDefaultBranchName(b string) bool {
	switch strings.ToLower(strings.TrimSpace(b)) {
	case "", "main", "master", "develop", "dev", "trunk", "head":
		return true
	}
	return false
}

func (p projectsPanel) isCurrentBranchGHItem(it GHItemRow) bool {
	if !isDefaultBranchName(p.cwdBranch) && it.HeadRef != "" && strings.EqualFold(it.HeadRef, p.cwdBranch) {
		return true
	}
	if p.cwdIssueHint > 0 && it.Number == p.cwdIssueHint {
		return true
	}
	return false
}

func buildRepoPullCLI(paths []string) string {
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		if s := strings.TrimSpace(p); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return "ws repo pull --all"
	}
	return "ws repo pull " + joinShellArgs(clean)
}

func (p projectsPanel) selectedRepoPaths() []string {
	var paths []string
	for _, r := range p.filteredRepos {
		if r.LocalPath != "" && p.repoSel.Has(r.LocalPath) {
			paths = append(paths, r.LocalPath)
		}
	}
	return paths
}

func (p projectsPanel) CLIEquivalent() string {
	if p.cloneOpen {
		parts := []string{"ws", "repo", "clone"}
		if src := strings.TrimSpace(p.cloneURL.Value()); src != "" {
			parts = append(parts, shellQuoteArg(src))
		}
		if z := strings.TrimSpace(p.cloneZone.Value()); z != "" {
			parts = append(parts, "--zone", shellQuoteArg(z))
		}
		return strings.Join(parts, " ")
	}
	if p.isGHActiveMode() {
		if it, ok := p.selectedGHItem(); ok && it.Repo != "" {
			kind := strings.ToLower(nonEmpty(it.Kind, "pr"))
			return fmt.Sprintf("gh %s view %d -R %s", kind, it.Number, it.Repo)
		}
		slug, _, allMine, _ := p.effectiveGHScope()
		kindFlag := "prs"
		if p.ghKindFilter == "issue" {
			kindFlag = "issues"
		}
		if !allMine && slug != "" {
			return fmt.Sprintf("gh %s list -R %s --state %s", strings.TrimSuffix(kindFlag, "s"), slug, p.ghStateFilter)
		}
		return "ws gh"
	}
	if p.repoSel.Count() > 0 {
		return buildRepoPullCLI(p.selectedRepoPaths())
	}
	if r, ok := p.selectedRepo(); ok {
		return fmt.Sprintf("ws repo pick %s", r.Name)
	}
	return "ws repo list"
}

func (p projectsPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case focusPreviewMsg:
		p.ghFocused = msg.focus || p.isGHDedicatedSubView()
		if !msg.focus {
			p.repoInfoFromRepos = false
		}
		return p, nil

	case previewFocusedMsg:
		if p.repoInfoMode && p.repoInfoFromRepos {
			p.ghFocused = false
			return p, nil
		}
		p.ghFocused = true
		p.repoInfoMode = false
		return p, batchNonNil(
			p.loadSelectedRepoCacheOnlyCmd(backend),
			p.ensureActiveRepoAndDetailCmds(backend, false, false),
		)

	case previewOpenedMsg:
		p.ghFocused = focusPreview
		return p, p.loadSelectedRepoCacheOnlyCmd(backend)

	case triggerProjectsPullMsg:
		if p.pulling {
			return p, nil
		}
		var paths []string
		cmdStr := "ws repo pull --all"
		body := "Pull selected clean repositories across active scope and record full output in Output Console."
		if msg.pullAll {
			cmdStr = "ws repo pull --all"
			body = "Pull ALL repositories across configured zones into Output Console."
		} else if p.repoSel.Count() > 0 {
			paths = p.selectedRepoPaths()
			if len(paths) == 0 {
				return p, func() tea.Msg {
					return statusToastMsg{text: "No marked repos match the active view/zone", isErr: true}
				}
			}
			cmdStr = buildRepoPullCLI(paths)
			body = fmt.Sprintf("Pull %d selected clean repository(s) into Output Console.", len(paths))
		} else if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
			paths = []string{r.LocalPath}
			cmdStr = buildRepoPullCLI(paths)
			body = fmt.Sprintf("Pull clean repository %s into Output Console.", r.Name)
		} else {
			return p, func() tea.Msg {
				return statusToastMsg{text: "No repositories selected to pull (use :pull all)", isErr: true}
			}
		}
		targets := append([]string(nil), paths...)
		return p, func() tea.Msg {
			return confirmRequestMsg{
				Title:        "Confirm Repository Pull",
				Body:         body + "\n$ " + cmdStr,
				Warning:      "Updates clean local working branches from upstream.",
				ConfirmLabel: "Pull",
				Dangerous:    true,
				OnConfirm: func(b Backend) tea.Cmd {
					return pullSelectedReposCmd(b, targets, cmdStr)
				},
			}
		}

	case projectsCWDMsg:
		p.cwdRepoPath = strings.TrimSpace(msg.badge.CWDRepoPath)
		p.cwdRepoSlug = strings.TrimSpace(msg.badge.CWDRepoSlug)
		p.cwdBranch = strings.TrimSpace(msg.badge.CWDBranch)
		p.cwdIssueHint = msg.badge.CWDIssueHint
		if !p.userPickedRepo {
			p.autoSelectCWDRepo()
		}
		p.recomputeFilteredGH()
		var cmds []tea.Cmd
		if cmd := p.loadSelectedRepoCacheOnlyCmd(backend); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if p.cwdRepoSlug != "" {
			selectedMatchesCWD := !p.reposLoaded
			if r, ok := p.selectedRepo(); ok && p.repoMatchesCWD(r) {
				selectedMatchesCWD = true
			}
			if selectedMatchesCWD {

				if cmd := p.ensureRepoLoadedCmd(backend, p.cwdRepoSlug, p.zone, false); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		if p.ghFocused || p.isGHDedicatedSubView() {
			if cmd := p.ensureActiveRepoAndDetailCmds(backend, false, false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return p, batchNonNil(cmds...)

	case ghCommandMsg:
		p.ghFocused = true
		p.repoInfoMode = false
		p.ghDetailOpen = false
		p.ghCursor = 0
		p.previewScroll = 0
		if msg.kind != "" {
			p.ghKindFilter = strings.ToLower(msg.kind)
		}
		if msg.state != "" {
			p.ghStateFilter = strings.ToLower(msg.state)
		}
		if msg.allMine {
			p.ghAllMine = true
			p.ghRepoOverride = ""
		} else if msg.repo != "" {
			p.userPickedRepo = true
			p.ghAllMine = false
			p.ghRepoOverride = msg.repo
			for i, r := range p.filteredRepos {
				if strings.EqualFold(r.Name, msg.repo) || strings.EqualFold(projects.ExtractGitHubRepoSlug(r.RemoteURL), msg.repo) {
					p.repoCursor = i
					p.ghRepoOverride = ""
					break
				}
			}
		}
		if msg.query != "" {
			p.ghQuery = msg.query
		}
		p.recomputeFilteredGH()
		return p, p.ensureActiveRepoAndDetailCmds(backend, false, false)

	case reposLoadedMsg:
		if msg.zone != "" && msg.zone != p.zone {
			return p, nil
		}
		p.reposLoaded = true
		var cmds []tea.Cmd
		if p.repoActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.repoActID, "Scanning repositories", msg.err != nil))
			p.repoActID = 0
		}
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Repo scan failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		var prevPath string
		if p.userPickedRepo {
			if prev, ok := p.selectedRepo(); ok {
				prevPath = prev.LocalPath
			}
		}
		p.loadErr = ""
		p.repos = msg.repos
		p.recomputeFilteredRepos()
		if p.userPickedRepo && prevPath != "" {
			for i, r := range p.filteredRepos {
				if r.LocalPath == prevPath {
					p.repoCursor = i
					break
				}
			}
		} else {
			p.autoSelectCWDRepo()
		}
		if p.ghRepoOverride != "" {
			for i, r := range p.filteredRepos {
				if strings.EqualFold(projects.ExtractGitHubRepoSlug(r.RemoteURL), p.ghRepoOverride) {
					p.repoCursor = i
					break
				}
			}
		}
		if p.repoCursor >= len(p.filteredRepos) {
			p.repoCursor = max(0, len(p.filteredRepos)-1)
		}
		p.recomputeFilteredGH()
		if cmd := p.loadSelectedRepoCacheOnlyCmd(backend); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if p.ghFocused || p.isGHDedicatedSubView() {
			force := p.ghForceRefresh
			p.ghForceRefresh = false
			if slug, _, allMine, _ := p.effectiveGHScope(); !allMine && slug != "" {
				if _, already := p.repoItems[repoCacheKey(slug, nonEmpty(p.ghStateFilter, "open"))]; already {
					force = false
				}
			}
			if cmd := p.ensureActiveRepoAndDetailCmds(backend, force, false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else if r, ok := p.selectedRepo(); ok && p.cwdRepoSlug != "" && p.repoMatchesCWD(r) {
			if cmd := p.ensureRepoLoadedCmd(backend, p.cwdRepoSlug, nonEmpty(r.Zone, p.zone), false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return p, batchNonNil(cmds...)

	case ghItemsLoadedMsg:
		if msg.zone != "" && msg.zone != p.zone {
			return p, nil
		}
		p.ghLoaded = true
		var cmds []tea.Cmd
		if p.ghActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.ghActID, "Loading GitHub PRs/Issues", msg.err != nil))
			p.ghActID = 0
		}
		if msg.err != nil {
			p.ghErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "GitHub load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.ghErr = ""
		p.ghItems = msg.items
		p.recomputeFilteredGH()
		if (p.ghFocused && p.ghDetailOpen) || p.isGHDedicatedSubView() {
			if cmd := p.ensureSelectedGHDetailCmd(backend, true, false); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return p, batchNonNil(cmds...)

	case ghRepoCacheLoadedMsg:
		ck := repoCacheKey(msg.repo, msg.state)
		if msg.err != nil || msg.fetchedAt == 0 {
			return p, nil
		}
		if _, already := p.repoItems[ck]; !already || len(msg.items) > 0 {
			p.repoItems[ck] = msg.items
			p.repoFetchedAt[ck] = msg.fetchedAt
			p.recomputeFilteredGH()
			if p.isGHDedicatedSubView() {
				return p, p.ensureSelectedGHDetailCmd(backend, true, false)
			}
		}
		return p, nil

	case ghRepoItemsLoadedMsg:
		ck := repoCacheKey(msg.repo, msg.state)
		delete(p.repoLoading, ck)
		if msg.err != nil {
			p.repoErrs[ck] = msg.err.Error()
			return p, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("GitHub (%s): %v", msg.repo, msg.err), isErr: true}
			}
		}
		delete(p.repoErrs, ck)
		p.repoItems[ck] = msg.items
		var maxTS int64
		for _, it := range msg.items {
			if it.FetchedAt > maxTS {
				maxTS = it.FetchedAt
			}
		}
		if maxTS == 0 {
			maxTS = time.Now().Unix()
		}
		p.repoFetchedAt[ck] = maxTS
		p.recomputeFilteredGH()
		if (p.ghFocused && p.ghDetailOpen) || p.isGHDedicatedSubView() {
			return p, p.ensureSelectedGHDetailCmd(backend, true, false)
		}
		return p, nil

	case ghItemDetailLoadedMsg:
		dk := ghDetailKey(msg.kind, msg.repo, msg.number)
		delete(p.detailLoading, dk)
		if msg.err != nil {
			p.detailErrs[dk] = msg.err.Error()
			return p, nil
		}
		delete(p.detailErrs, dk)
		p.detailCache[dk] = msg.detail
		return p, nil

	case ghDashActionDoneMsg:
		p.ghActing = false
		var cmds []tea.Cmd
		if p.ghActionID != 0 {
			cmds = append(cmds, finishActivityCmd(p.ghActionID, nonEmpty(msg.label, "GitHub action"), msg.err != nil))
			p.ghActionID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("%s failed: %v", nonEmpty(msg.label, "GitHub action"), msg.err), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		cmds = append(cmds, func() tea.Msg {
			return statusToastMsg{text: nonEmpty(msg.summary, msg.label+" complete")}
		})
		if backend != nil {
			slug, zone, allMine, _ := p.effectiveGHScope()
			if !allMine && slug != "" {
				delete(p.repoItems, repoCacheKey(slug, p.ghStateFilter))
				if cmd := p.ensureRepoLoadedCmd(backend, slug, zone, true); cmd != nil {
					cmds = append(cmds, cmd)
				}
			} else {
				p.ghActID = newActivityID()
				cmds = append(cmds,
					startActivityCmd(p.ghActID, "Refreshing GitHub PRs/Issues"),
					loadGHItemsCmd(backend, true, p.zone),
				)
			}
			if cmd := p.ensureSelectedGHDetailCmd(backend, true, true); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return p, batchNonNil(cmds...)

	case reposPulledMsg:
		if msg.err != nil {
			p.pulling = false
			var actCmd tea.Cmd
			if p.pullActID != 0 {
				actCmd = finishActivityCmd(p.pullActID, "Pulling repositories", true)
				p.pullActID = 0
			}
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "Pull failed: " + msg.err.Error(), isErr: true}
			})
		}
		if len(msg.result.NeedsInteractive) > 0 && backend != nil {
			first := msg.result.NeedsInteractive[0]
			rest := append([]RepoRow(nil), msg.result.NeedsInteractive[1:]...)
			execCmd, err := backend.InteractivePullCmd(first)
			if err == nil && execCmd != nil {
				repoName := first.Name
				return p, tea.ExecProcess(execCmd, func(err error) tea.Msg {
					return interactivePullFinishedMsg{repo: repoName, remaining: rest, err: err}
				})
			}
		}
		p.pulling = false
		p.repoSel = p.repoSel.Clear()
		var cmds []tea.Cmd
		if p.pullActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.pullActID, "Pulling repositories", false))
			p.pullActID = 0
		}
		summary := nonEmpty(msg.result.Summary, fmt.Sprintf("Pull: %d updated, %d up-to-date, %d dirty", msg.result.Updated, msg.result.UpToDate, msg.result.Dirty))
		p.repoActID = newActivityID()
		cmds = append(cmds,
			func() tea.Msg { return statusToastMsg{text: summary} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)
		return p, batchNonNil(cmds...)

	case interactivePullFinishedMsg:
		if msg.err != nil {
			p.pulling = false
			var cmds []tea.Cmd
			if p.pullActID != 0 {
				cmds = append(cmds, finishActivityCmd(p.pullActID, "Pulling repositories", true))
				p.pullActID = 0
			}
			p.repoActID = newActivityID()
			cmds = append(cmds,
				func() tea.Msg {
					return statusToastMsg{text: fmt.Sprintf("Interactive pull %s failed: %v", msg.repo, msg.err), isErr: true}
				},
				startActivityCmd(p.repoActID, "Scanning repositories"),
				loadReposCmd(backend, true, p.zone),
			)
			return p, batchNonNil(cmds...)
		}
		if len(msg.remaining) > 0 && backend != nil {
			next := msg.remaining[0]
			rest := append([]RepoRow(nil), msg.remaining[1:]...)
			execCmd, err := backend.InteractivePullCmd(next)
			if err == nil && execCmd != nil {
				repoName := next.Name
				return p, tea.ExecProcess(execCmd, func(err error) tea.Msg {
					return interactivePullFinishedMsg{repo: repoName, remaining: rest, err: err}
				})
			}
		}
		p.pulling = false
		p.repoSel = p.repoSel.Clear()
		var cmds []tea.Cmd
		if p.pullActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.pullActID, "Pulling repositories", false))
			p.pullActID = 0
		}
		p.repoActID = newActivityID()
		cmds = append(cmds,
			func() tea.Msg { return statusToastMsg{text: "Batch pull complete"} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)
		return p, batchNonNil(cmds...)

	case repoEditorFinishedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Editor (%s) failed: %v", msg.repo, msg.err), isErr: true}
			}
		}
		p.repoActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Closed editor for " + msg.repo} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)

	case repoLazygitFinishedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Lazygit (%s) failed: %v", msg.repo, msg.err), isErr: true}
			}
		}
		p.repoActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Closed lazygit for " + msg.repo} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)

	case repoClonedMsg:
		p.cloning = false
		var cmds []tea.Cmd
		if p.cloneActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.cloneActID, "Cloning repository", msg.err != nil))
			p.cloneActID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Clone failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.repoActID = newActivityID()
		cmds = append(cmds,
			func() tea.Msg { return statusToastMsg{text: nonEmpty(msg.summary, "Repository cloned")} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)
		return p, batchNonNil(cmds...)

	case tea.KeyPressMsg:
		p.ghFocused = focusPreview || p.isGHDedicatedSubView()
		if p.cloneOpen {
			return p.updateCloneModal(msg, backend)
		}
		if p.modalKind != ghModalNone {
			return p.updateGHModalKey(msg, backend)
		}
		if (focusPreview || p.isGHDedicatedSubView()) && p.ghFilters.Open {
			groups, sorts := p.ghFacetSpec()
			if handled, changed := p.ghFilters.Update(msg, groups, sorts); handled {
				if changed {
					p.ghCursor = 0
					p.previewScroll = 0
					p.recomputeFilteredGH()
					if p.isGHDedicatedSubView() {
						return p, p.ensureSelectedGHDetailCmd(backend, true, false)
					}
				}
				return p, nil
			}
		}
		if !p.isGHDedicatedSubView() && !focusPreview && p.repoFilters.Open {
			groups, sorts := p.repoFacetSpec()
			if handled, changed := p.repoFilters.Update(msg, groups, sorts); handled {
				if changed {
					p.userPickedRepo = true
					p.ghRepoOverride = ""
					p.ghAllMine = false
					p.ghDetailOpen = false
					p.userMovedGH = false
					p.ghCursor = 0
					p.previewScroll = 0
					p.repoCursor = 0
					p.recomputeFilteredRepos()
					p.recomputeFilteredGH()
					return p, p.loadSelectedRepoCacheOnlyCmd(backend)
				}
				return p, nil
			}
		}
		if focusPreview || p.isGHDedicatedSubView() {
			return p.updateGHKey(msg, backend, split, focusPreview)
		}
		return p.updateReposKey(msg, backend, split)
	}

	if p.cloneOpen {
		var cmd tea.Cmd
		if p.cloneFocus == 0 {
			p.cloneURL, cmd = p.cloneURL.Update(msg)
		} else if p.cloneFocus == 1 {
			p.cloneZone, cmd = p.cloneZone.Update(msg)
		}
		return p, cmd
	}
	if p.modalKind == ghModalComment {
		var cmd tea.Cmd
		p.commentInput, cmd = p.commentInput.Update(msg)
		return p, cmd
	}
	return p, nil
}

func (p projectsPanel) updateReposKey(msg tea.KeyPressMsg, backend Backend, split SplitMode) (ResourcePanel, tea.Cmd) {
	if isSpaceKey(msg) {
		if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
			p.repoSel = p.repoSel.Toggle(r.LocalPath)
		}
		return p, nil
	}
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if next, changed := applyListMotion(p.repoCursor, len(p.filteredRepos), defaultMotionPage, m); changed {
			p.repoCursor = next
			p.userPickedRepo = true
			p.ghRepoOverride = ""
			p.ghAllMine = false
			p.userMovedGH = false
			p.ghCursor = 0
			p.previewScroll = 0
			p.ghDetailOpen = false
			p.recomputeFilteredGH()

			return p, p.loadSelectedRepoCacheOnlyCmd(backend)
		}
		return p, nil
	}
	switch msg.String() {
	case "F":
		groups, sorts := p.repoFacetSpec()
		p.repoFilters.OpenFilters(groups, sorts)
		return p, nil
	case "S":
		groups, sorts := p.repoFacetSpec()
		p.repoFilters.OpenSort(groups, sorts)
		return p, nil
	case "*":
		paths := make([]string, len(p.filteredRepos))
		for i, r := range p.filteredRepos {
			paths[i] = r.LocalPath
		}
		p.repoSel = p.repoSel.ToggleAll(paths)
	case `\`:
		p.repoSel = p.repoSel.Clear()
		if p.repoFilters.HasActive() {
			p.repoFilters.Clear()
			p.userPickedRepo = true
			p.ghRepoOverride = ""
			p.ghAllMine = false
			p.ghDetailOpen = false
			p.userMovedGH = false
			p.ghCursor = 0
			p.previewScroll = 0
			p.repoCursor = 0
			p.recomputeFilteredRepos()
			p.recomputeFilteredGH()
			return p, p.loadSelectedRepoCacheOnlyCmd(backend)
		}
	case "y":
		if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
			return p, copyCmd(r.LocalPath, "repo path")
		}
	case "Y":
		if r, ok := p.selectedRepo(); ok && r.RemoteURL != "" {
			return p, copyCmd(r.RemoteURL, "remote URL")
		}
	case "o":
		return p, p.openSelectedRepoInBrowser()
	case "e":
		return p, p.launchRepoEditorCmd()
	case "l":
		if !lazygitAvailable() {
			return p, nil
		}
		return p, p.launchRepoLazygitCmd()
	case "enter":
		if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
			if p.hasCDFile {
				path := r.LocalPath
				return p, func() tea.Msg { return repoSelectedMsg{path: path} }
			}
			p.repoInfoMode = true
			p.repoInfoFromRepos = true
			p.ghDetailOpen = false
			p.previewScroll = 0
			return p, func() tea.Msg { return focusPreviewMsg{focus: true, ensureSplit: true} }
		}
	case "u":
		if p.pulling {
			return p, nil
		}
		var paths []string
		if p.repoSel.Count() > 0 {
			paths = p.selectedRepoPaths()
		} else if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
			paths = []string{r.LocalPath}
		}
		cmdStr := buildRepoPullCLI(paths)
		p.pulling = true
		p.pullActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Pulling repositories..."} },
			startActivityCmd(p.pullActID, "Pulling repositories"),
			pullSelectedReposCmd(backend, paths, cmdStr),
		)
	case "r":
		p.repoActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Refreshing repositories..."} },
			startActivityCmd(p.repoActID, "Scanning repositories"),
			loadReposCmd(backend, true, p.zone),
		)
	}
	return p, nil
}

func (p *projectsPanel) cycleGHRepo(delta int) bool {
	type ghRepoCand struct {
		filteredIdx int
		slug        string
	}
	var cands []ghRepoCand
	for i, r := range p.filteredRepos {
		if slug := projects.ExtractGitHubRepoSlug(r.RemoteURL); slug != "" {
			cands = append(cands, ghRepoCand{filteredIdx: i, slug: slug})
		}
	}
	if len(cands) == 0 {
		return false
	}
	curSlug, _, _, _ := p.effectiveGHScope()
	pos := 0
	for ci, c := range cands {
		if strings.EqualFold(c.slug, curSlug) {
			pos = ci
			break
		}
	}
	next := (pos + delta + len(cands)) % len(cands)
	p.userPickedRepo = true
	p.ghAllMine = false
	p.ghRepoOverride = ""
	p.repoInfoMode = false
	p.ghDetailOpen = false
	p.repoCursor = cands[next].filteredIdx
	p.userMovedGH = false
	p.ghCursor = 0
	p.previewScroll = 0
	return true
}

func (p projectsPanel) updateGHKey(msg tea.KeyPressMsg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if p.repoInfoMode {
		if m := classifyVimMotion(msg.String()); m != motionNone {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		switch msg.String() {
		case "o":
			return p, p.openSelectedRepoInBrowser()
		case "e":
			return p, p.launchRepoEditorCmd()
		case "l":
			if !lazygitAvailable() {
				return p, nil
			}
			return p, p.launchRepoLazygitCmd()
		case "y":
			if r, ok := p.selectedRepo(); ok && r.LocalPath != "" {
				return p, copyCmd(r.LocalPath, "repo path")
			}
		case "Y":
			if r, ok := p.selectedRepo(); ok && r.RemoteURL != "" {
				return p, copyCmd(r.RemoteURL, "remote URL")
			}
		}
		return p, nil
	}

	detailFocus := p.ghDetailOpen || (p.isGHDedicatedSubView() && focusPreview)
	if m := classifyVimMotion(msg.String()); m != motionNone {
		if detailFocus {
			step := 2
			switch m {
			case motionDown:
				p.previewScroll += step
			case motionUp:
				p.previewScroll = max(0, p.previewScroll-step)
			default:
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			}
			return p, nil
		}
		if next, changed := applyListMotion(p.ghCursor, len(p.filteredGH), defaultMotionPage, m); changed {
			p.ghCursor = next
			p.userMovedGH = true
			p.previewScroll = 0
			if p.isGHDedicatedSubView() {
				return p, p.ensureSelectedGHDetailCmd(backend, true, false)
			}
		}
		return p, nil
	}

	switch msg.String() {
	case "F":
		groups, sorts := p.ghFacetSpec()
		p.ghFilters.OpenFilters(groups, sorts)
		return p, nil
	case "S":
		groups, sorts := p.ghFacetSpec()
		p.ghFilters.OpenSort(groups, sorts)
		return p, nil
	case "<", ">":
		delta := 1
		if msg.String() == "<" {
			delta = -1
		}
		if p.cycleGHRepo(delta) {
			p.recomputeFilteredGH()
			return p, p.ensureActiveRepoAndDetailCmds(backend, false, false)
		}
		return p, nil
	case "enter":
		if p.isGHDedicatedSubView() {
			if _, ok := p.selectedGHItem(); ok {
				return p, batchNonNil(
					func() tea.Msg { return focusPreviewMsg{focus: true, ensureSplit: true} },
					p.ensureSelectedGHDetailCmd(backend, true, false),
				)
			}
			return p, nil
		}
		if it, ok := p.selectedGHItem(); ok {
			p.ghDetailOpen = !p.ghDetailOpen
			p.previewScroll = 0
			if p.ghDetailOpen {
				return p, p.ensureSelectedGHDetailCmd(backend, true, false)
			}
			_ = it
		}
		return p, nil
	case "y":
		if it, ok := p.selectedGHItem(); ok {
			if p.ghDetailOpen || p.isGHDedicatedSubView() {
				return p.openCopyFieldModal(it), nil
			}
			return p, copyCmd(it.URL, fmt.Sprintf("%s #%d URL", strings.ToUpper(it.Kind), it.Number))
		}
	case "Y":
		if it, ok := p.selectedGHItem(); ok {
			num := fmt.Sprintf("#%d", it.Number)
			return p, copyCmd(num, num)
		}
	case "o":
		if it, ok := p.selectedGHItem(); ok {
			return p, openURLCmd(it.URL)
		}
	case "r":
		if p.ghDetailOpen || (p.isGHDedicatedSubView() && focusPreview) {
			if it, ok := p.selectedGHItem(); ok {
				return p, batchNonNil(
					func() tea.Msg { return statusToastMsg{text: fmt.Sprintf("Refreshing #%d...", it.Number)} },
					p.ensureSelectedGHDetailCmd(backend, true, true),
				)
			}
		}
		slug, zone, allMine, _ := p.effectiveGHScope()
		if !allMine && slug != "" {
			delete(p.repoItems, repoCacheKey(slug, p.ghStateFilter))
			return p, batchNonNil(
				func() tea.Msg { return statusToastMsg{text: "Refreshing " + slug + "..."} },
				p.ensureRepoLoadedCmd(backend, slug, zone, true),
			)
		}
		p.ghActID = newActivityID()
		return p, tea.Batch(
			func() tea.Msg { return statusToastMsg{text: "Refreshing GitHub PRs & Issues..."} },
			startActivityCmd(p.ghActID, "Loading GitHub PRs/Issues"),
			loadGHItemsCmd(backend, true, p.zone),
		)
	}
	return p, nil
}

func (p projectsPanel) runGHRowAction(actionKey string, backend Backend) (ResourcePanel, tea.Cmd) {
	switch actionKey {
	case "L":
		if !p.isGHDedicatedSubView() {
			p.repoInfoMode = true
			p.repoInfoFromRepos = false
			p.ghDetailOpen = false
			p.previewScroll = 0
		}
		return p, nil
	case "t":
		if p.isGHDedicatedSubView() {
			if p.subView == projSubPRs {
				p.subView = projSubIssues
				p.ghKindFilter = "issue"
			} else {
				p.subView = projSubPRs
				p.ghKindFilter = "pr"
			}
			p.ghCursor = 0
			p.userMovedGH = false
			p.previewScroll = 0
			p.recomputeFilteredGH()
			toast := "Pull Requests"
			if p.subView == projSubIssues {
				toast = "Issues"
			}
			return p, batchNonNil(
				func() tea.Msg { return statusToastMsg{text: "View: " + toast} },
				p.ensureSelectedGHDetailCmd(backend, true, false),
			)
		}
		switch p.ghKindFilter {
		case "all":
			p.ghKindFilter = "pr"
		case "pr":
			p.ghKindFilter = "issue"
		default:
			p.ghKindFilter = "all"
		}
		p.ghCursor = 0
		p.ghDetailOpen = false
		p.userMovedGH = false
		p.previewScroll = 0
		p.recomputeFilteredGH()
		return p, func() tea.Msg { return statusToastMsg{text: "Filter type: " + p.ghKindFilter} }
	case "g":
		p.userPickedRepo = true
		p.ghAllMine = !p.ghAllMine
		p.ghRepoOverride = ""
		p.ghDetailOpen = false
		p.ghCursor = 0
		p.userMovedGH = false
		p.previewScroll = 0
		p.recomputeFilteredGH()
		slug, _, allMine, _ := p.effectiveGHScope()
		scopeToast := "Scope: All Mine"
		if !allMine && slug != "" {
			scopeToast = "Scope: " + slug
		}
		return p, batchNonNil(
			func() tea.Msg { return statusToastMsg{text: scopeToast} },
			p.ensureActiveRepoAndDetailCmds(backend, false, false),
		)
	case "s":
		switch p.ghStateFilter {
		case "open":
			p.ghStateFilter = "closed"
		case "closed":
			if p.ghKindFilter == "pr" {
				p.ghStateFilter = "merged"
			} else {
				p.ghStateFilter = "all"
			}
		case "merged":
			p.ghStateFilter = "all"
		default:
			p.ghStateFilter = "open"
		}
		p.ghCursor = 0
		p.ghDetailOpen = false
		p.userMovedGH = false
		p.previewScroll = 0
		p.recomputeFilteredGH()
		return p, batchNonNil(
			func() tea.Msg { return statusToastMsg{text: "State filter: " + p.ghStateFilter} },
			p.ensureActiveRepoAndDetailCmds(backend, false, false),
		)
	case "c":
		if it, ok := p.selectedGHItem(); ok {
			var cmdText, lbl string
			if strings.EqualFold(it.Kind, "pr") {
				cmdText = fmt.Sprintf("gh pr checkout %d -R %s", it.Number, it.Repo)
				lbl = fmt.Sprintf("gh pr checkout %d", it.Number)
			} else {
				cmdText = fmt.Sprintf("gh issue view %d -R %s", it.Number, it.Repo)
				lbl = fmt.Sprintf("gh issue view %d", it.Number)
			}
			return p, copyCmd(cmdText, lbl)
		}
	case "A":
		if it, ok := p.selectedGHItem(); ok {
			if !strings.EqualFold(it.Kind, "pr") {
				return p, func() tea.Msg { return statusToastMsg{text: "Only pull requests can be approved", isErr: true} }
			}
			target := it
			zone := nonEmpty(target.Zone, p.zone)
			return p, func() tea.Msg {
				return confirmRequestMsg{
					Title:        fmt.Sprintf("Approve PR #%d?", target.Number),
					Body:         fmt.Sprintf("%s\nRepo: %s", target.Title, target.Repo),
					Warning:      "Submits an approving review via gh pr review --approve.",
					ConfirmLabel: "Approve",
					OnConfirm: func(b Backend) tea.Cmd {
						return approveGHPRCmd(b, target.Repo, zone, target.Number, "")
					},
				}
			}
		}
	case "M":
		if it, ok := p.selectedGHItem(); ok {
			if !strings.EqualFold(it.Kind, "pr") {
				return p, func() tea.Msg { return statusToastMsg{text: "Only pull requests can be merged", isErr: true} }
			}
			p.modalKind = ghModalMerge
			p.modalTarget = it
			p.mergeCursor = 0
			return p, nil
		}
	case "X":
		if it, ok := p.selectedGHItem(); ok {
			target := it
			zone := nonEmpty(target.Zone, p.zone)
			closeItem := strings.EqualFold(nonEmpty(target.State, "open"), "open")
			verb := "Reopen"
			if closeItem {
				verb = "Close"
			}
			kindUpper := strings.ToUpper(nonEmpty(target.Kind, "pr"))
			return p, func() tea.Msg {
				return confirmRequestMsg{
					Title:        fmt.Sprintf("%s %s #%d?", verb, kindUpper, target.Number),
					Body:         fmt.Sprintf("%s\nRepo: %s", target.Title, target.Repo),
					Warning:      fmt.Sprintf("Runs gh %s %s %d.", strings.ToLower(kindUpper), strings.ToLower(verb), target.Number),
					ConfirmLabel: verb,
					Dangerous:    closeItem,
					OnConfirm: func(b Backend) tea.Cmd {
						return setGHItemStateCmd(b, target.Kind, target.Repo, zone, target.Number, closeItem)
					},
				}
			}
		}
	case "m":
		if it, ok := p.selectedGHItem(); ok {
			p.modalKind = ghModalComment
			p.modalTarget = it
			p.commentInput.SetValue("")
			return p, p.commentInput.Focus()
		}
	case "K":
		if it, ok := p.selectedGHItem(); ok {
			if !strings.EqualFold(it.Kind, "pr") {
				return p, func() tea.Msg { return statusToastMsg{text: "Only pull requests can be checked out", isErr: true} }
			}
			if p.ghActing {
				return p, nil
			}
			p.ghActing = true
			p.ghActionID = newActivityID()
			repoDir := p.repoDirForGHItem(it)
			zone := nonEmpty(it.Zone, p.zone)
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: fmt.Sprintf("Checking out PR #%d...", it.Number)} },
				startActivityCmd(p.ghActionID, fmt.Sprintf("Checkout PR #%d", it.Number)),
				checkoutGHPRCmd(backend, repoDir, it.Repo, zone, it.Number),
			)
		}
	}
	return p, nil
}

func (p *projectsPanel) recomputeFilteredGH() {
	slug, _, allMine, _ := p.effectiveGHScope()
	var base []GHItemRow
	switch {
	case allMine:
		base = p.ghItems
	case slug != "":
		ck := repoCacheKey(slug, p.ghStateFilter)
		if cached, ok := p.repoItems[ck]; ok {
			base = cached
		} else {
			for _, it := range p.ghItems {
				if strings.EqualFold(it.Repo, slug) {
					base = append(base, it)
				}
			}
		}
	default:
		base = nil
	}

	baseFiltered := filterGHItemsAdvanced(base, p.ghQuery, p.ghKindFilter, p.ghStateFilter, true)
	var kept []GHItemRow
	for _, it := range baseFiltered {
		if !p.ghFilters.Matches("kind", it.Kind) {
			continue
		}
		if !p.ghFilters.Matches("state", it.State) {
			continue
		}
		if !p.ghFilters.Matches("zone", nonEmpty(it.Zone, "global")) {
			continue
		}
		kept = append(kept, it)
	}
	if p.ghFilters.SortKey != "" || p.ghFilters.SortDesc {
		sort.SliceStable(kept, func(i, j int) bool {
			var cmp int
			switch p.ghFilters.SortKey {
			case "number":
				cmp = kept[j].Number - kept[i].Number
			case "title":
				cmp = strings.Compare(strings.ToLower(kept[i].Title), strings.ToLower(kept[j].Title))
			case "state":
				cmp = strings.Compare(strings.ToLower(kept[i].State), strings.ToLower(kept[j].State))
			default:
				cmp = strings.Compare(kept[j].UpdatedAt, kept[i].UpdatedAt)
			}
			if p.ghFilters.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	p.filteredGH = kept
	if !p.userMovedGH {
		p.autoSelectCurrentGHItem()
	}
	if p.ghCursor >= len(p.filteredGH) {
		p.ghCursor = max(0, len(p.filteredGH)-1)
	}
}

func (p *projectsPanel) recomputeFilteredRepos() {
	filtered := filterRepoRows(p.repos, p.repoQuery)
	var kept []RepoRow
	for _, r := range filtered {
		if st := p.repoFilters.Get("state"); st != "" {
			switch st {
			case "dirty":
				if !r.Dirty {
					continue
				}
			case "clean":
				if r.Dirty {
					continue
				}
			case "sync":
				if r.Ahead == 0 && r.Behind == 0 {
					continue
				}
			}
		}
		fg := nonEmpty(strings.ToLower(r.Forge), "other")
		if !p.repoFilters.Matches("forge", fg) {
			continue
		}
		if !p.repoFilters.Matches("zone", nonEmpty(r.Zone, "global")) {
			continue
		}
		kept = append(kept, r)
	}
	if p.repoFilters.SortKey != "" || p.repoFilters.SortDesc {
		sort.SliceStable(kept, func(i, j int) bool {
			var cmp int
			switch p.repoFilters.SortKey {
			case "dirty":
				di, dj := 1, 1
				if kept[i].Dirty {
					di = 0
				}
				if kept[j].Dirty {
					dj = 0
				}
				cmp = di - dj
				if cmp == 0 {
					cmp = strings.Compare(strings.ToLower(kept[i].Name), strings.ToLower(kept[j].Name))
				}
			case "branch":
				cmp = strings.Compare(strings.ToLower(kept[i].Branch), strings.ToLower(kept[j].Branch))
			case "locals":
				cmp = len(kept[j].Locals) - len(kept[i].Locals)
			default:
				cmp = strings.Compare(strings.ToLower(kept[i].Name), strings.ToLower(kept[j].Name))
			}
			if p.repoFilters.SortDesc {
				return cmp > 0
			}
			return cmp < 0
		})
	}
	p.filteredRepos = kept
	if p.repoCursor >= len(p.filteredRepos) {
		p.repoCursor = max(0, len(p.filteredRepos)-1)
	}
}

func (p projectsPanel) repoFacetSpec() ([]FacetGroup, []SortOption) {
	dirtyCnt, cleanCnt, syncCnt := 0, 0, 0
	forgeCnt := map[string]int{}
	zoneCnt := map[string]int{}
	for _, r := range p.repos {
		if r.Dirty {
			dirtyCnt++
		} else {
			cleanCnt++
		}
		if r.Ahead > 0 || r.Behind > 0 {
			syncCnt++
		}
		fg := nonEmpty(strings.ToLower(r.Forge), "other")
		forgeCnt[fg]++
		zoneCnt[strings.ToLower(nonEmpty(r.Zone, "global"))]++
	}
	var fgKeys []string
	for k := range forgeCnt {
		fgKeys = append(fgKeys, k)
	}
	sort.Strings(fgKeys)
	var fgOpts []FacetOption
	for _, k := range fgKeys {
		fgOpts = append(fgOpts, FacetOption{Value: k, Label: k, Count: forgeCnt[k]})
	}
	var znKeys []string
	for k := range zoneCnt {
		znKeys = append(znKeys, k)
	}
	sort.Strings(znKeys)
	var znOpts []FacetOption
	for _, k := range znKeys {
		znOpts = append(znOpts, FacetOption{Value: k, Label: k, Count: zoneCnt[k]})
	}
	groups := []FacetGroup{
		{
			Key:   "state",
			Label: "Working Tree",
			Options: []FacetOption{
				{Value: "dirty", Label: "Dirty", Count: dirtyCnt},
				{Value: "clean", Label: "Clean", Count: cleanCnt},
				{Value: "sync", Label: "Ahead / Behind", Count: syncCnt},
			},
		},
		{Key: "forge", Label: "Forge", Options: fgOpts},
	}
	if len(znOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "zone", Label: "Zone", Options: znOpts})
	}
	sorts := []SortOption{
		{Key: "name", Label: "Repo Name (A–Z)"},
		{Key: "dirty", Label: "Dirty First"},
		{Key: "branch", Label: "Branch"},
		{Key: "locals", Label: "Local Untracked Files"},
	}
	return groups, sorts
}

func (p projectsPanel) ghFacetSpec() ([]FacetGroup, []SortOption) {
	kindCnt := map[string]int{}
	stateCnt := map[string]int{}
	zoneCnt := map[string]int{}
	for _, it := range p.ghItems {
		kindCnt[strings.ToLower(it.Kind)]++
		stateCnt[strings.ToLower(it.State)]++
		zoneCnt[strings.ToLower(nonEmpty(it.Zone, "global"))]++
	}
	var znKeys []string
	for k := range zoneCnt {
		znKeys = append(znKeys, k)
	}
	sort.Strings(znKeys)
	var znOpts []FacetOption
	for _, k := range znKeys {
		znOpts = append(znOpts, FacetOption{Value: k, Label: k, Count: zoneCnt[k]})
	}
	groups := []FacetGroup{
		{
			Key:   "kind",
			Label: "Type",
			Options: []FacetOption{
				{Value: "pr", Label: "Pull Requests", Count: kindCnt["pr"]},
				{Value: "issue", Label: "Issues", Count: kindCnt["issue"]},
			},
		},
		{
			Key:   "state",
			Label: "State",
			Options: []FacetOption{
				{Value: "open", Label: "Open", Count: stateCnt["open"]},
				{Value: "closed", Label: "Closed / Merged", Count: stateCnt["closed"] + stateCnt["merged"]},
			},
		},
	}
	if len(znOpts) > 0 {
		groups = append(groups, FacetGroup{Key: "zone", Label: "Zone", Options: znOpts})
	}
	sorts := []SortOption{
		{Key: "updated", Label: "Updated Date"},
		{Key: "number", Label: "PR / Issue #"},
		{Key: "title", Label: "Title (A–Z)"},
		{Key: "state", Label: "State"},
	}
	return groups, sorts
}

func (p *projectsPanel) autoSelectCurrentGHItem() {
	if len(p.filteredGH) == 0 {
		return
	}
	if !isDefaultBranchName(p.cwdBranch) {
		for i, it := range p.filteredGH {
			if strings.EqualFold(it.Kind, "pr") && it.HeadRef != "" && strings.EqualFold(it.HeadRef, p.cwdBranch) {
				p.ghCursor = i
				return
			}
		}
	}
	if p.cwdIssueHint > 0 {
		for i, it := range p.filteredGH {
			if it.Number == p.cwdIssueHint {
				p.ghCursor = i
				return
			}
		}
	}
}

func (p *projectsPanel) loadSelectedRepoCacheOnlyCmd(backend Backend) tea.Cmd {
	if backend == nil {
		return nil
	}
	slug, _, allMine, _ := p.effectiveGHScope()
	if allMine || slug == "" {
		return nil
	}
	state := nonEmpty(p.ghStateFilter, "open")
	ck := repoCacheKey(slug, state)
	if _, ok := p.repoItems[ck]; ok {
		return nil
	}
	return loadCachedRepoGHItemsCmd(backend, slug, state)
}

func (p *projectsPanel) ensureActiveRepoAndDetailCmds(backend Backend, forceList, forceDetail bool) tea.Cmd {
	if backend == nil {
		return nil
	}
	var cmds []tea.Cmd
	slug, zone, allMine, _ := p.effectiveGHScope()
	if (allMine || slug == "") && (!p.ghLoaded || forceList) {
		p.ghActID = newActivityID()
		cmds = append(cmds,
			startActivityCmd(p.ghActID, "Loading GitHub PRs/Issues"),
			loadGHItemsCmd(backend, forceList && p.ghAllMine, p.zone),
		)
	}
	if !allMine && slug != "" {
		if cmd := p.ensureRepoLoadedCmd(backend, slug, zone, forceList); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if p.ghDetailOpen || p.isGHDedicatedSubView() {
		if cmd := p.ensureSelectedGHDetailCmd(backend, true, forceDetail); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return batchNonNil(cmds...)
}

func (p *projectsPanel) ensureRepoLoadedCmd(backend Backend, repo, zone string, force bool) tea.Cmd {
	if backend == nil || strings.TrimSpace(repo) == "" {
		return nil
	}
	state := nonEmpty(p.ghStateFilter, "open")
	ck := repoCacheKey(repo, state)
	if !force {
		if _, ok := p.repoItems[ck]; ok {
			return nil
		}
	}
	if p.repoLoading[ck] {
		return nil
	}
	p.repoLoading[ck] = true
	return loadRepoGHItemsCmd(backend, repo, zone, state, force)
}

func (p *projectsPanel) ensureSelectedGHDetailCmd(backend Backend, needDetail, force bool) tea.Cmd {
	if backend == nil || !needDetail {
		return nil
	}
	it, ok := p.selectedGHItem()
	if !ok || it.Repo == "" || it.Number <= 0 {
		return nil
	}
	dk := ghDetailKey(it.Kind, it.Repo, it.Number)
	if !force {
		if _, ok := p.detailCache[dk]; ok {
			return nil
		}
	}
	if p.detailLoading[dk] {
		return nil
	}
	p.detailLoading[dk] = true
	return loadGHItemDetailCmd(backend, it.Kind, it.Repo, nonEmpty(it.Zone, p.zone), it.Number, force)
}

func (p projectsPanel) updateCloneModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	if key == "esc" || (key == "q" && p.cloneFocus == 2) {
		p.closeCloneModal()
		return p, func() tea.Msg { return statusToastMsg{text: "Clone cancelled"} }
	}
	switch p.cloneFocus {
	case 0:
		switch key {
		case "tab", "down", "enter":
			p.cloneURL.Blur()
			p.cloneFocus = 1
			return p, p.cloneZone.Focus()
		default:
			var cmd tea.Cmd
			p.cloneURL, cmd = p.cloneURL.Update(msg)
			return p, cmd
		}
	case 1:
		switch key {
		case "tab", "down", "enter":
			p.cloneZone.Blur()
			p.cloneFocus = 2
			p.cloneConfirmFocus = false
			return p, nil
		case "shift+tab", "up":
			p.cloneZone.Blur()
			p.cloneFocus = 0
			return p, p.cloneURL.Focus()
		default:
			var cmd tea.Cmd
			p.cloneZone, cmd = p.cloneZone.Update(msg)
			return p, cmd
		}
	default:
		switch key {
		case "left", "right", "tab", "h", "l":
			p.cloneConfirmFocus = !p.cloneConfirmFocus
		case "shift+tab", "up":
			p.cloneFocus = 1
			return p, p.cloneZone.Focus()
		case "enter":
			if !p.cloneConfirmFocus {
				p.closeCloneModal()
				return p, func() tea.Msg { return statusToastMsg{text: "Clone cancelled"} }
			}
			src := strings.TrimSpace(p.cloneURL.Value())
			zone := strings.TrimSpace(p.cloneZone.Value())
			if src == "" {
				return p, func() tea.Msg { return statusToastMsg{text: "Repository URL or owner/repo is required", isErr: true} }
			}
			p.closeCloneModal()
			p.cloning = true
			p.cloneActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.cloneActID, "Cloning "+src),
				cloneRepoCmd(backend, src, zone),
			)
		}
	}
	return p, nil
}

func (p *projectsPanel) closeCloneModal() {
	p.cloneURL.Blur()
	p.cloneZone.Blur()
	p.cloneOpen = false
	p.cloneFocus = 0
	p.cloneConfirmFocus = false
}

func (p projectsPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.isGHDedicatedSubView() {
		innerW := max(10, width-4)
		innerH := max(1, height-2)
		return p.renderUnifiedGHPreview(width, height, innerW, innerH, focused, false, st)
	}
	return p.renderReposTable(width, height, focused, st)
}

func (p projectsPanel) renderRepoGHBadge(r RepoRow, st Styles) string {
	slug := projects.ExtractGitHubRepoSlug(r.RemoteURL)
	if slug == "" {
		return st.Muted.Render("—")
	}
	var items []GHItemRow
	ck := repoCacheKey(slug, "open")
	if cached, ok := p.repoItems[ck]; ok {
		items = cached
	} else {
		for _, it := range p.ghItems {
			if strings.EqualFold(it.Repo, slug) {
				items = append(items, it)
			}
		}
	}
	if len(items) == 0 {
		return st.Muted.Render("—")
	}
	prs, issues := 0, 0
	for _, it := range items {
		if strings.EqualFold(it.Kind, "pr") {
			prs++
		} else {
			issues++
		}
	}
	var parts []string
	if prs > 0 {
		parts = append(parts, fmt.Sprintf("%dp", prs))
	}
	if issues > 0 {
		parts = append(parts, fmt.Sprintf("%di", issues))
	}
	return st.InfoText.Render(strings.Join(parts, " "))
}

func (p projectsPanel) renderReposTable(width, height int, focused bool, st Styles) string {
	innerW := max(12, width-4)
	innerH := max(1, height-2)
	title := fmt.Sprintf("Repositories (%d)", len(p.filteredRepos))

	if !p.reposLoaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Scanning repositories...")}, width, height, focused, st)
	}
	if p.loadErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.loadErr, innerW))}, width, height, focused, st)
	}
	if len(p.filteredRepos) == 0 {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("No matching repositories.")}, width, height, focused, st)
	}

	compactRepos := innerW < 84
	rows := make([][]string, 0, len(p.filteredRepos))
	marked := make(map[int]bool, p.repoSel.Count())
	for i, r := range p.filteredRepos {
		if p.repoSel.Has(r.LocalPath) {
			marked[i] = true
		}
		state := st.OKText.Render("✓ clean")
		if r.Dirty {
			state = st.WarnText.Render("● dirty")
		}
		sync := "—"
		if r.Ahead > 0 || r.Behind > 0 {
			sync = fmt.Sprintf("↑%d ↓%d", r.Ahead, r.Behind)
		}
		locals := "—"
		if len(r.Locals) > 0 {
			locals = fmt.Sprintf("%d file(s)", len(r.Locals))
		}
		if compactRepos {
			rows = append(rows, []string{
				r.Name,
				nonEmpty(r.Zone, "—"),
				nonEmpty(r.Branch, "HEAD"),
				state,
				p.renderRepoGHBadge(r, st),
				locals,
			})
		} else {
			rows = append(rows, []string{
				r.Name,
				nonEmpty(r.Zone, "—"),
				nonEmpty(r.Branch, "HEAD"),
				sync,
				state,
				p.renderRepoGHBadge(r, st),
				locals,
				collapseHomePath(r.LocalPath),
			})
		}
	}
	headers := []string{"Repo", "Zone", "Branch", "Sync", "State", "GH", "Locals", "Path"}
	if compactRepos {
		headers = []string{"Repo", "Zone", "Branch", "State", "GH", "Locals"}
	}
	lines := renderSelectableTable(headers, rows, p.repoCursor, marked, focused, innerW, innerH, st)
	badge := ""
	if p.repoSel.Count() > 0 {
		badge = fmt.Sprintf("%d sel", p.repoSel.Count())
	}
	return renderTitledBox(title, badge, lines, width, height, focused, st)
}

func (p projectsPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(10, innerW)
	innerH = max(1, innerH)

	if p.repoInfoMode {
		return p.renderRepoPreview(width, height, innerW, innerH, focused, borderless, st)
	}
	if p.ghDetailOpen || p.isGHDedicatedSubView() {
		return p.renderGHDetailPreview(width, height, innerW, innerH, focused, borderless, st)
	}
	return p.renderUnifiedGHPreview(width, height, innerW, innerH, focused, borderless, st)
}

func formatCacheAge(unixTS int64) string {
	if unixTS <= 0 {
		return "not fetched"
	}
	d := time.Since(time.Unix(unixTS, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "cached just now"
	case d < time.Hour:
		return fmt.Sprintf("cached %dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("cached %dh ago", int(d.Hours()))
	}
}

func (p projectsPanel) renderUnifiedGHPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	slug, _, allMine, nonGHRepo := p.effectiveGHScope()
	selectedRepo, hasRepo := p.selectedRepo()
	scopeLabel := "None"
	switch {
	case allMine:
		scopeLabel = "All Mine"
	case slug != "":
		scopeLabel = slug
	case nonGHRepo && hasRepo:
		scopeLabel = nonEmpty(selectedRepo.Name, "Non-GitHub")
	}
	kindTitle := "PRs & Issues"
	if p.ghKindFilter == "pr" {
		kindTitle = "Pull Requests"
	} else if p.ghKindFilter == "issue" {
		kindTitle = "Issues"
	}
	boxTitle := fmt.Sprintf("%s · %s (%d)", kindTitle, scopeLabel, len(p.filteredGH))
	if nonGHRepo && hasRepo {
		boxTitle = fmt.Sprintf("%s · %s (%s)", kindTitle, nonEmpty(selectedRepo.Name, "Non-GitHub"), nonEmpty(selectedRepo.Forge, "local"))
	}

	var lines []string
	if !allMine && hasRepo && (nonGHRepo || strings.EqualFold(projects.ExtractGitHubRepoSlug(selectedRepo.RemoteURL), slug) || (!p.userPickedRepo && p.repoMatchesCWD(selectedRepo))) {
		stateWord := st.OKText.Render("CLEAN")
		if selectedRepo.Dirty {
			stateWord = st.WarnText.Render("DIRTY")
		}
		syncStr := ""
		if selectedRepo.Ahead > 0 || selectedRepo.Behind > 0 {
			syncStr = fmt.Sprintf(" ↑%d/↓%d", selectedRepo.Ahead, selectedRepo.Behind)
		}
		infoLine := fmt.Sprintf("%s · %s%s · %s · %d locals",
			nonEmpty(slug, selectedRepo.Name), nonEmpty(selectedRepo.Branch, "HEAD"), syncStr, stateWord, len(selectedRepo.Locals))
		lines = append(lines, fitStyledWidth(infoLine, innerW, st))
	}

	scopeCtrl := fmt.Sprintf("Repo: %s", scopeLabel)
	if !allMine && slug != "" && p.cwdRepoSlug != "" && strings.EqualFold(slug, p.cwdRepoSlug) {
		scopeCtrl += " (cwd)"
	}
	if nonGHRepo {
		forgeLabel := "non-GitHub"
		if hasRepo && selectedRepo.Forge != "" {
			forgeLabel = selectedRepo.Forge
		}
		scopeCtrl = fmt.Sprintf("Repo: %s (%s — not GitHub)", nonEmpty(selectedRepo.Name, "repo"), forgeLabel)
	} else if !allMine && slug == "" {
		scopeCtrl = "Repo: none"
	}

	ck := ""
	var cacheNote string
	if !allMine && slug != "" {
		ck = repoCacheKey(slug, p.ghStateFilter)
		if ts := p.repoFetchedAt[ck]; ts > 0 {
			cacheNote = formatCacheAge(ts)
		} else if len(p.filteredGH) > 0 {
			cacheNote = "cached"
		} else {
			cacheNote = "not fetched"
		}
	}
	ctrl := fmt.Sprintf("%s  ·  Type: %s  ·  State: %s", scopeCtrl, strings.ToUpper(p.ghKindFilter), strings.ToUpper(p.ghStateFilter))
	if cacheNote != "" {
		ctrl += "  ·  " + cacheNote
	}
	lines = append(lines, st.Muted.Render(truncate(ctrl, innerW)))
	lines = append(lines, "")

	switch {
	case !allMine && slug != "" && p.repoLoading[ck] && len(p.filteredGH) == 0:
		lines = append(lines, st.Muted.Render(fmt.Sprintf("Loading GitHub items for %s...", slug)))
	case !allMine && slug != "" && p.repoErrs[ck] != "" && len(p.filteredGH) == 0:
		lines = append(lines, st.ErrText.Render(truncate(p.repoErrs[ck], innerW)))
	case allMine && !p.ghLoaded && len(p.filteredGH) == 0:
		lines = append(lines, st.Muted.Render("Loading GitHub PRs & Issues..."))
	case len(p.filteredGH) == 0 && p.ghErr != "":
		lines = append(lines, st.ErrText.Render(truncate(p.ghErr, innerW)))
	case len(p.filteredGH) == 0:
		msg := "No cached PRs/Issues for this scope."
		switch {
		case nonGHRepo:
			forgeLabel := "non-GitHub"
			repoName := "This repository"
			if hasRepo {
				repoName = nonEmpty(selectedRepo.Name, repoName)
				forgeLabel = nonEmpty(selectedRepo.Forge, forgeLabel)
			}
			msg = fmt.Sprintf("%s uses %s (not GitHub).", repoName, forgeLabel)
		case !allMine && slug == "":
			msg = "No matching repository selected."
		case !allMine && slug != "" && p.repoFetchedAt[ck] > 0:
			oppHint := ""
			if p.ghKindFilter == "pr" || p.ghKindFilter == "issue" {
				wantOpp := "issue"
				oppLabel := "Issues"
				curLabel := "Pull Requests"
				if p.ghKindFilter == "issue" {
					wantOpp = "pr"
					oppLabel = "Pull Requests"
					curLabel = "Issues"
				}
				oppItems := filterGHItemsAdvanced(p.repoItems[ck], "", wantOpp, p.ghStateFilter, true)
				if len(oppItems) > 0 {
					oppHint = fmt.Sprintf("No %s %s in %s (%d %s cached).", p.ghStateFilter, curLabel, slug, len(oppItems), oppLabel)
				} else {
					oppHint = fmt.Sprintf("No %s %s in %s.", p.ghStateFilter, curLabel, slug)
				}
			}
			if oppHint != "" {
				msg = oppHint
			} else {
				msg = "No matching items."
			}
		}
		lines = append(lines, st.Muted.Render(truncate(msg, innerW)))
	default:
		if allMine && p.ghErr != "" {
			lines = append(lines, st.ErrText.Render(truncate("Refresh error: "+p.ghErr, innerW)))
		} else if !allMine && slug != "" && p.repoErrs[ck] != "" {
			lines = append(lines, st.ErrText.Render(truncate("Refresh error: "+p.repoErrs[ck], innerW)))
		}

		scopedSingle := !allMine && slug != ""
		var headers []string
		switch {
		case scopedSingle && innerW < 64:
			headers = []string{"Kind", "State", "#", "Title"}
		case scopedSingle && innerW < 80:
			headers = []string{"Kind", "State", "#", "Title", "Updated"}
		case scopedSingle && innerW < 96:
			headers = []string{"Kind", "State", "#", "Title", "Author", "Updated"}
		case scopedSingle:
			headers = []string{"Kind", "State", "#", "Title", "Milestone", "Author", "Updated"}
		case innerW < 66:
			headers = []string{"Kind", "Repo", "#", "Title"}
		case innerW < 104:
			headers = []string{"Kind", "State", "Repo", "#", "Title", "Author", "Updated"}
		default:
			headers = []string{"Kind", "State", "Repo", "#", "Title", "Milestone", "Author", "Updated"}
		}
		rows := make([][]string, 0, len(p.filteredGH))
		for _, it := range p.filteredGH {
			kindTag := st.InfoText.Render("ISSUE")
			if strings.EqualFold(it.Kind, "pr") {
				kindTag = st.AccentText.Render("PR")
			}
			stateTag := st.OKText.Render(strings.ToUpper(nonEmpty(it.State, "OPEN")))
			if it.IsDraft {
				stateTag = st.Muted.Render("DRAFT")
			} else if strings.EqualFold(it.State, "closed") || strings.EqualFold(it.State, "merged") {
				stateTag = st.Muted.Render(strings.ToUpper(it.State))
			}
			numStr := fmt.Sprintf("#%d", it.Number)
			if p.isCurrentBranchGHItem(it) {
				numStr = "★ " + numStr
			}
			ms := nonEmpty(it.Milestone, "—")
			switch {
			case scopedSingle && innerW < 64:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.AccentText.Render(numStr),
					it.Title,
				})
			case scopedSingle && innerW < 80:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.AccentText.Render(numStr),
					it.Title,
					nonEmpty(it.UpdatedAt, "—"),
				})
			case scopedSingle && innerW < 96:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.AccentText.Render(numStr),
					it.Title,
					nonEmpty(it.Author, "—"),
					nonEmpty(it.UpdatedAt, "—"),
				})
			case scopedSingle:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.AccentText.Render(numStr),
					it.Title,
					ms,
					nonEmpty(it.Author, "—"),
					nonEmpty(it.UpdatedAt, "—"),
				})
			case innerW < 66:
				rows = append(rows, []string{
					kindTag,
					st.InfoText.Render(it.Repo),
					st.AccentText.Render(numStr),
					it.Title,
				})
			case innerW < 104:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.InfoText.Render(it.Repo),
					st.AccentText.Render(numStr),
					it.Title,
					nonEmpty(it.Author, "—"),
					nonEmpty(it.UpdatedAt, "—"),
				})
			default:
				rows = append(rows, []string{
					kindTag,
					stateTag,
					st.InfoText.Render(it.Repo),
					st.AccentText.Render(numStr),
					it.Title,
					ms,
					nonEmpty(it.Author, "—"),
					nonEmpty(it.UpdatedAt, "—"),
				})
			}
		}
		tableH := max(4, innerH-len(lines))
		lines = append(lines, renderDataTable(headers, rows, p.ghCursor, focused, innerW, tableH, st)...)
	}

	if borderless {
		plain := make([]string, 0, len(lines))
		for _, ln := range lines {
			plain = append(plain, ui.StripANSI(ln))
		}
		return fitLines(strings.Join(plain, "\n"), height)
	}
	return renderTitledBox(boxTitle, "", lines, width, height, focused, st)
}

func (p projectsPanel) renderRepoPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	r, ok := p.selectedRepo()
	if !ok {
		return renderTitledBox("Repository Info", "", []string{st.Muted.Render("Select a repository.")}, width, height, focused, st)
	}
	branchInfo := nonEmpty(r.Branch, "HEAD")
	if r.Ahead > 0 || r.Behind > 0 {
		branchInfo += fmt.Sprintf(" (↑%d / ↓%d)", r.Ahead, r.Behind)
	}
	statusBadge := st.OKText.Render("CLEAN")
	if r.Dirty {
		statusBadge = st.WarnText.Render("DIRTY")
	}
	kvPairs := [][2]string{
		{"Repo", st.AccentText.Render(r.Name) + " · " + statusBadge},
		{"Zone / Forge", nonEmpty(r.Zone, "—") + " · " + nonEmpty(r.Forge, "git")},
		{"Branch", branchInfo},
		{"Path", r.LocalPath},
	}
	if r.RemoteURL != "" {
		kvPairs = append(kvPairs, [2]string{"Remote", r.RemoteURL})
	}
	var all []string
	all = append(all, renderKVTable(kvPairs, innerW, st)...)
	all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Untracked Local Files Manifest (%d)", len(r.Locals))))
	if len(r.Locals) == 0 {
		all = append(all, st.Muted.Render("No untracked local env/config files detected."))
	} else {
		rows := make([][]string, 0, len(r.Locals))
		for _, loc := range r.Locals {
			bk := st.WarnText.Render("not backed up")
			if loc.BackedUp {
				bk = st.OKText.Render("✓ in ws.db")
			}
			rows = append(rows, []string{loc.RelPath, formatBytes(loc.SizeBytes), bk})
		}
		all = append(all, renderDataTable([]string{"File", "Size", "ws.db"}, rows, -1, false, innerW, max(3, min(10, len(rows)+1)), st)...)
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		plain := make([]string, 0, len(vis))
		for _, ln := range vis {
			plain = append(plain, ui.StripANSI(ln))
		}
		return fitLines(strings.Join(plain, "\n"), height)
	}
	return renderTitledBox("Repo · "+r.Name, "", vis, width, height, focused, st)
}

func (p projectsPanel) renderGHDetailPreview(width, height, innerW, innerH int, focused bool, borderless bool, st Styles) string {
	it, ok := p.selectedGHItem()
	if !ok {
		msg := []string{st.Muted.Render("Select a PR or Issue to inspect metadata and Markdown body.")}
		if borderless {
			return fitLines(strings.Join(msg, "\n"), height)
		}
		return renderTitledBox("Preview", "", msg, width, height, focused, st)
	}

	dk := ghDetailKey(it.Kind, it.Repo, it.Number)
	det, hasDetail := p.detailCache[dk]
	viewItem := it
	if hasDetail {
		viewItem = det.GHItemRow
		if viewItem.Repo == "" {
			viewItem.Repo = it.Repo
		}
	}

	kindUpper := "ISSUE"
	if strings.EqualFold(viewItem.Kind, "pr") {
		kindUpper = "PR"
		if viewItem.IsDraft {
			kindUpper = "DRAFT PR"
		}
	}
	boxTitle := fmt.Sprintf("Preview · %s #%d", kindUpper, viewItem.Number)

	pairs := [][2]string{
		{"Title", st.AccentText.Render(viewItem.Title)},
		{"Repo", st.InfoText.Render(nonEmpty(viewItem.Repo, "—")) + "  " + st.Muted.Render("zone:"+nonEmpty(viewItem.Zone, "—"))},
		{"State", strings.ToUpper(nonEmpty(viewItem.State, "open")) + " · author @" + nonEmpty(viewItem.Author, "—")},
	}
	if len(viewItem.Labels) > 0 {
		pairs = append(pairs, [2]string{"Labels", st.WarnText.Render(strings.Join(viewItem.Labels, ", "))})
	}
	if viewItem.Milestone != "" {
		pairs = append(pairs, [2]string{"Milestone", st.InfoText.Render(viewItem.Milestone)})
	}
	if hasDetail && strings.EqualFold(viewItem.Kind, "pr") && (det.HeadRef != "" || det.ReviewDecision != "" || det.ChecksSummary != "") {
		branch := nonEmpty(det.HeadRef, "?") + " -> " + nonEmpty(det.BaseRef, "?")
		var extra []string
		if det.ReviewDecision != "" {
			extra = append(extra, det.ReviewDecision)
		}
		if det.ChecksSummary != "" {
			extra = append(extra, det.ChecksSummary)
		}
		if det.ChangedFiles > 0 || det.Additions > 0 || det.Deletions > 0 {
			extra = append(extra, fmt.Sprintf("+%d/-%d (%d files)", det.Additions, det.Deletions, det.ChangedFiles))
		}
		val := branch
		if len(extra) > 0 {
			val += "  ·  " + strings.Join(extra, " · ")
		}
		pairs = append(pairs, [2]string{"PR Info", st.InfoText.Render(val)})
	}
	pairs = append(pairs, [2]string{"URL", st.Muted.Render(viewItem.URL)})

	kvLines := renderKVTable(pairs, innerW, st)

	var mdBuf strings.Builder
	if strings.TrimSpace(viewItem.Body) != "" {
		mdBuf.WriteString(ui.RenderMarkdown(viewItem.Body, innerW, st.Roles))
	} else if p.detailLoading[dk] {
		mdBuf.WriteString(st.Muted.Render("Loading full description & comments..."))
	} else if errMsg := p.detailErrs[dk]; errMsg != "" {
		mdBuf.WriteString(st.ErrText.Render("Detail load failed: " + errMsg))
	} else {
		mdBuf.WriteString(st.Muted.Render("(No description provided.)"))
	}
	if hasDetail && len(det.Comments) > 0 {
		mdBuf.WriteString("\n\n")
		mdBuf.WriteString(st.SectionHeader.Render(fmt.Sprintf("Recent Comments (%d)", len(det.Comments))))
		for _, c := range det.Comments {
			mdBuf.WriteString("\n\n")
			mdBuf.WriteString(st.InfoText.Render("@"+nonEmpty(c.Author, "user")) + " " + st.Muted.Render(c.CreatedAt) + "\n")
			mdBuf.WriteString(ui.RenderMarkdown(c.Body, innerW, st.Roles))
		}
	}

	vpH := max(3, innerH-len(kvLines)-1)
	vp := viewport.New(viewport.WithWidth(innerW), viewport.WithHeight(vpH))
	vp.SoftWrap = true
	vp.SetContent(mdBuf.String())
	maxOff := max(0, vp.TotalLineCount()-vpH)
	vp.SetYOffset(clamp(p.previewScroll, 0, maxOff))

	var out []string
	out = append(out, kvLines...)
	out = append(out, st.Muted.Render(strings.Repeat("─", min(innerW, 48))))
	out = append(out, strings.Split(vp.View(), "\n")...)

	if borderless {
		plain := make([]string, 0, len(out))
		for _, ln := range out {
			plain = append(plain, ui.StripANSI(ln))
		}
		return fitLines(strings.Join(plain, "\n"), height)
	}
	pct := int(vp.ScrollPercent() * 100)
	badge := fmt.Sprintf("%d%%", pct)
	return renderTitledBox(boxTitle, badge, out, width, height, focused, st)
}

func (p projectsPanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.modalKind != ghModalNone {
		return p.renderGHModal(width, height, st), true
	}
	if (p.ghFocused || p.isGHDedicatedSubView()) && p.ghFilters.Open {
		groups, sorts := p.ghFacetSpec()
		return p.ghFilters.ViewModal("PRs & Issues", groups, sorts, width, height, st)
	}
	if !p.isGHDedicatedSubView() && !p.ghFocused && p.repoFilters.Open {
		groups, sorts := p.repoFacetSpec()
		return p.repoFilters.ViewModal("Repos", groups, sorts, width, height, st)
	}
	if !p.cloneOpen {
		return "", false
	}
	boxW := min(max(44, width-12), 70)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(14, boxW-st.ModalBox.GetHorizontalFrameSize())

	focusClone := p.cloneFocus == 2 && p.cloneConfirmFocus
	cancelBtn := renderModalActionButton("Cancel", !focusClone, false, st)
	cloneBtn := renderModalActionButton("Clone", focusClone, false, st)

	parts := []string{
		st.ModalTitle.Render(truncate("Clone Repository", innerW)),
		"",
		renderInputField("Source: ", p.cloneURL, innerW, p.cloneFocus == 0, st),
		renderInputField("Zone:   ", p.cloneZone, innerW, p.cloneFocus == 1, st),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center, cancelBtn, "  ", cloneBtn),
		"",
		st.Muted.Render(truncate("tab next · left/right switch button · enter select · esc cancel", innerW)),
	}
	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal), true
}

func collapseHomePath(p string) string {
	if p == "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = filepath.Clean(home)
		clean := filepath.Clean(p)
		if clean == home {
			return "~"
		}
		if strings.HasPrefix(clean, home+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(clean[len(home)+1:])
		}
	}
	return p
}

func filterRepoRows(rows []RepoRow, query string) []RepoRow {
	return fuzzy.Filter(rows, query, func(r RepoRow) ([]string, map[string]string) {
		dirty := "false"
		if r.Dirty {
			dirty = "true"
		}
		locals := "false"
		if len(r.Locals) > 0 {
			locals = "true"
		}
		slug := projects.ExtractGitHubRepoSlug(r.RemoteURL)
		return []string{r.Name, slug, r.Zone, r.Branch, r.Forge},
			map[string]string{
				"zone":   nonEmpty(r.Zone, "global"),
				"forge":  nonEmpty(r.Forge, "git"),
				"branch": r.Branch,
				"dirty":  dirty,
				"locals": locals,
			}
	})
}

func filterGHItemsAdvanced(rows []GHItemRow, query, kindFilter, stateFilter string, filterState bool) []GHItemRow {
	kf := strings.ToLower(strings.TrimSpace(kindFilter))
	sf := strings.ToLower(strings.TrimSpace(stateFilter))

	var stage []GHItemRow
	for _, r := range rows {
		if kf != "" && kf != "all" && !strings.EqualFold(r.Kind, kf) {
			continue
		}
		if filterState && sf != "" && sf != "all" {
			st := strings.ToLower(strings.TrimSpace(r.State))
			if sf == "open" && st != "" && st != "open" {
				continue
			}
			if sf == "closed" && st != "closed" && st != "merged" {
				continue
			}
			if sf == "merged" && st != "merged" {
				continue
			}
		}
		stage = append(stage, r)
	}
	return fuzzy.Filter(stage, query, func(r GHItemRow) ([]string, map[string]string) {
		num := fmt.Sprintf("#%d", r.Number)
		return []string{num, r.Title, r.Repo, r.Author, strings.Join(r.Labels, " "), r.Zone},
			map[string]string{
				"kind":   strings.ToLower(r.Kind),
				"state":  strings.ToLower(nonEmpty(r.State, "open")),
				"repo":   strings.ToLower(r.Repo),
				"author": strings.ToLower(r.Author),
				"zone":   strings.ToLower(r.Zone),
			}
	})
}

func repoCacheKey(repo, state string) string {
	return strings.ToLower(strings.TrimSpace(repo)) + "|" + strings.ToLower(strings.TrimSpace(state))
}

func ghDetailKey(kind, repo string, number int) string {
	return fmt.Sprintf("%s|%s#%d", strings.ToLower(strings.TrimSpace(kind)), strings.ToLower(strings.TrimSpace(repo)), number)
}

func loadReposCmd(backend Backend, refresh bool, zone string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return reposLoadedMsg{zone: zone, err: fmt.Errorf("backend unavailable")}
		}
		repos, err := backend.LoadRepos(refresh, zone)
		return reposLoadedMsg{zone: zone, repos: repos, err: err}
	}
}

func loadGHItemsCmd(backend Backend, refresh bool, zone string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghItemsLoadedMsg{zone: zone, err: fmt.Errorf("backend unavailable")}
		}
		items, err := backend.LoadGHItems(refresh, zone)
		return ghItemsLoadedMsg{zone: zone, items: items, err: err}
	}
}

func loadCachedRepoGHItemsCmd(backend Backend, repo, state string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghRepoCacheLoadedMsg{repo: repo, state: state, err: fmt.Errorf("backend unavailable")}
		}
		items, fetchedAt, err := backend.LoadCachedRepoGHItems(repo, state)
		return ghRepoCacheLoadedMsg{repo: repo, state: state, items: items, fetchedAt: fetchedAt, err: err}
	}
}

func loadRepoGHItemsCmd(backend Backend, repo, zone, state string, refresh bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghRepoItemsLoadedMsg{repo: repo, zone: zone, state: state, err: fmt.Errorf("backend unavailable")}
		}
		items, err := backend.LoadRepoGHItems(repo, zone, state, refresh)
		return ghRepoItemsLoadedMsg{repo: repo, zone: zone, state: state, items: items, err: err}
	}
}

func loadGHItemDetailCmd(backend Backend, kind, repo, zone string, number int, refresh bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghItemDetailLoadedMsg{kind: kind, repo: repo, number: number, err: fmt.Errorf("backend unavailable")}
		}
		det, err := backend.LoadGHItemDetail(kind, repo, zone, number, refresh)
		return ghItemDetailLoadedMsg{kind: kind, repo: repo, number: number, detail: det, err: err}
	}
}

func pullSelectedReposCmd(backend Backend, paths []string, cmdLabel string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return reposPulledMsg{cmd: cmdLabel, err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.PullSelectedRepos(paths)
		return reposPulledMsg{result: res, cmd: cmdLabel, err: err}
	}
}

func cloneRepoCmd(backend Backend, source, zone string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return repoClonedMsg{err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.CloneRepo(source, zone)
		return repoClonedMsg{summary: summary, err: err}
	}
}

func (p *projectsPanel) closeGHModal() {
	p.modalKind = ghModalNone
	p.copyChoices = nil
	p.copyCursor = 0
	p.mergeCursor = 0
	p.commentInput.Blur()
}

func (p projectsPanel) openCopyFieldModal(it GHItemRow) projectsPanel {
	numStr := fmt.Sprintf("#%d", it.Number)
	ms := it.Milestone
	det, hasDet := p.detailCache[ghDetailKey(it.Kind, it.Repo, it.Number)]
	if hasDet && ms == "" {
		ms = det.Milestone
	}
	choices := []copyFieldChoice{
		{Label: "Number", Value: numStr},
		{Label: "Title", Value: it.Title},
		{Label: "URL", Value: it.URL},
	}
	if ms != "" {
		choices = append(choices, copyFieldChoice{Label: "Milestone", Value: ms})
	}
	if it.Repo != "" {
		choices = append(choices, copyFieldChoice{Label: "Repo #", Value: fmt.Sprintf("%s#%d", it.Repo, it.Number)})
	}
	headRef := it.HeadRef
	if hasDet && headRef == "" {
		headRef = det.HeadRef
	}
	if headRef != "" {
		choices = append(choices, copyFieldChoice{Label: "Head Branch", Value: headRef})
	}
	if it.Repo != "" {
		if strings.EqualFold(it.Kind, "pr") {
			choices = append(choices, copyFieldChoice{
				Label: "Checkout command",
				Value: fmt.Sprintf("gh pr checkout %d -R %s", it.Number, it.Repo),
			})
		} else {
			choices = append(choices, copyFieldChoice{
				Label: "View command",
				Value: fmt.Sprintf("gh issue view %d -R %s", it.Number, it.Repo),
			})
		}
	}
	p.modalKind = ghModalCopy
	p.modalTarget = it
	p.copyChoices = choices
	p.copyCursor = 0
	return p
}

func (p projectsPanel) repoDirForGHItem(it GHItemRow) string {
	for _, r := range p.repos {
		if slug := projects.ExtractGitHubRepoSlug(r.RemoteURL); slug != "" && strings.EqualFold(slug, it.Repo) {
			return r.LocalPath
		}
	}
	if p.cwdRepoSlug != "" && strings.EqualFold(p.cwdRepoSlug, it.Repo) {
		return p.cwdRepoPath
	}
	return ""
}

func (p projectsPanel) updateGHModalKey(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	key := msg.String()
	switch p.modalKind {
	case ghModalCopy:
		switch key {
		case "esc", "q":
			p.closeGHModal()
			return p, nil
		case "j", "down":
			if len(p.copyChoices) > 0 {
				p.copyCursor = (p.copyCursor + 1) % len(p.copyChoices)
			}
		case "k", "up":
			if len(p.copyChoices) > 0 {
				p.copyCursor = (p.copyCursor - 1 + len(p.copyChoices)) % len(p.copyChoices)
			}
		case "1", "2", "3", "4", "5", "6", "7":
			idx := int(key[0] - '1')
			if idx >= 0 && idx < len(p.copyChoices) {
				ch := p.copyChoices[idx]
				p.closeGHModal()
				return p, copyCmd(ch.Value, ch.Label)
			}
		case "enter":
			if p.copyCursor >= 0 && p.copyCursor < len(p.copyChoices) {
				ch := p.copyChoices[p.copyCursor]
				p.closeGHModal()
				return p, copyCmd(ch.Value, ch.Label)
			}
		}
		return p, nil

	case ghModalMerge:
		switch key {
		case "esc", "q":
			p.closeGHModal()
			return p, nil
		case "j", "down", "tab", "right":
			if len(p.mergeMethods) > 0 {
				p.mergeCursor = (p.mergeCursor + 1) % len(p.mergeMethods)
			}
		case "k", "up", "shift+tab", "left":
			if len(p.mergeMethods) > 0 {
				p.mergeCursor = (p.mergeCursor - 1 + len(p.mergeMethods)) % len(p.mergeMethods)
			}
		case "enter":
			if p.ghActing {
				return p, nil
			}
			method := "squash"
			if p.mergeCursor >= 0 && p.mergeCursor < len(p.mergeMethods) {
				method = p.mergeMethods[p.mergeCursor]
			}
			target := p.modalTarget
			zone := nonEmpty(target.Zone, p.zone)
			p.closeGHModal()
			p.ghActing = true
			p.ghActionID = newActivityID()
			return p, tea.Batch(
				func() tea.Msg {
					return statusToastMsg{text: fmt.Sprintf("Merging PR #%d (%s)...", target.Number, method)}
				},
				startActivityCmd(p.ghActionID, fmt.Sprintf("Merge PR #%d", target.Number)),
				mergeGHPRCmd(backend, target.Repo, zone, target.Number, method),
			)
		}
		return p, nil

	case ghModalComment:
		switch key {
		case "esc":
			p.closeGHModal()
			return p, func() tea.Msg { return statusToastMsg{text: "Comment cancelled"} }
		case "enter", "ctrl+s":
			body := strings.TrimSpace(p.commentInput.Value())
			if body == "" {
				return p, func() tea.Msg { return statusToastMsg{text: "Comment body is empty", isErr: true} }
			}
			if p.ghActing {
				return p, nil
			}
			target := p.modalTarget
			zone := nonEmpty(target.Zone, p.zone)
			p.closeGHModal()
			p.ghActing = true
			p.ghActionID = newActivityID()
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: fmt.Sprintf("Posting comment to #%d...", target.Number)} },
				startActivityCmd(p.ghActionID, fmt.Sprintf("Comment #%d", target.Number)),
				commentGHItemCmd(backend, target.Kind, target.Repo, zone, target.Number, body),
			)
		default:
			var cmd tea.Cmd
			p.commentInput, cmd = p.commentInput.Update(msg)
			return p, cmd
		}
	}
	return p, nil
}

func (p projectsPanel) renderGHModal(width, height int, st Styles) string {
	boxW := min(max(46, width-12), 76)
	if boxW > width-2 && width > 4 {
		boxW = width - 2
	}
	innerW := max(16, boxW-st.ModalBox.GetHorizontalFrameSize())

	var parts []string
	switch p.modalKind {
	case ghModalCopy:
		parts = append(parts,
			st.ModalTitle.Render(truncate(fmt.Sprintf("Copy Field · #%d", p.modalTarget.Number), innerW)),
			st.Muted.Render(truncate(p.modalTarget.Title, innerW)),
			"",
		)
		for i, ch := range p.copyChoices {
			prefix := "  "
			lbl := st.RowNormal.Render(fmt.Sprintf("%d. %-16s", i+1, ch.Label))
			if i == p.copyCursor {
				prefix = st.AccentText.Render("> ")
				lbl = st.AccentText.Render(fmt.Sprintf("%d. %-16s", i+1, ch.Label))
			}
			val := st.Muted.Render(truncate(ch.Value, max(10, innerW-23)))
			parts = append(parts, prefix+lbl+" "+val)
		}
		parts = append(parts, "", st.Muted.Render(truncate("j/k move · 1-7/enter copy · esc cancel", innerW)))

	case ghModalMerge:
		parts = append(parts,
			st.ModalTitle.Render(truncate(fmt.Sprintf("Merge PR #%d", p.modalTarget.Number), innerW)),
			st.Muted.Render(truncate(p.modalTarget.Title, innerW)),
			"",
		)
		for i, method := range p.mergeMethods {
			marker := "( )"
			lineSt := st.RowNormal
			if i == p.mergeCursor {
				marker = "(•)"
				lineSt = st.AccentText
			}
			parts = append(parts, lineSt.Render(fmt.Sprintf("  %s %s", marker, strings.ToUpper(method))))
		}
		parts = append(parts, "", st.Muted.Render(truncate("j/k select method · enter merge · esc cancel", innerW)))

	case ghModalComment:
		parts = append(parts,
			st.ModalTitle.Render(truncate(fmt.Sprintf("Comment on %s #%d", strings.ToUpper(nonEmpty(p.modalTarget.Kind, "pr")), p.modalTarget.Number), innerW)),
			st.Muted.Render(truncate(p.modalTarget.Title, innerW)),
			"",
			renderInputField("Comment: ", p.commentInput, innerW, true, st),
			"",
			st.Muted.Render(truncate("enter submit · esc cancel", innerW)),
		)
	}

	modal := st.ModalBox.Width(boxW).MaxWidth(boxW).Render(strings.Join(parts, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, modal)
}

func approveGHPRCmd(backend Backend, repo, zone string, number int, body string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghDashActionDoneMsg{label: "Approve PR", err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.ApproveGHPR(repo, zone, number, body)
		return ghDashActionDoneMsg{label: fmt.Sprintf("Approve #%d", number), summary: summary, err: err}
	}
}

func mergeGHPRCmd(backend Backend, repo, zone string, number int, method string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghDashActionDoneMsg{label: "Merge PR", err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.MergeGHPR(repo, zone, number, method)
		return ghDashActionDoneMsg{label: fmt.Sprintf("Merge #%d", number), summary: summary, err: err}
	}
}

func setGHItemStateCmd(backend Backend, kind, repo, zone string, number int, closeItem bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghDashActionDoneMsg{label: "Update state", err: fmt.Errorf("backend unavailable")}
		}
		verb := "Reopen"
		if closeItem {
			verb = "Close"
		}
		summary, err := backend.SetGHItemState(kind, repo, zone, number, closeItem)
		return ghDashActionDoneMsg{label: fmt.Sprintf("%s #%d", verb, number), summary: summary, err: err}
	}
}

func commentGHItemCmd(backend Backend, kind, repo, zone string, number int, body string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghDashActionDoneMsg{label: "Comment", err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.CommentGHItem(kind, repo, zone, number, body)
		return ghDashActionDoneMsg{label: fmt.Sprintf("Comment #%d", number), summary: summary, err: err}
	}
}

func checkoutGHPRCmd(backend Backend, repoDir, repo, zone string, number int) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return ghDashActionDoneMsg{label: "Checkout PR", err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.CheckoutGHPR(repoDir, repo, zone, number)
		return ghDashActionDoneMsg{label: fmt.Sprintf("Checkout #%d", number), summary: summary, err: err}
	}
}
