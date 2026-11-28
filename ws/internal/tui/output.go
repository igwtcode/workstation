package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

const (
	maxOutputRuns          = 50
	maxPersistedLinesPerOp = 500
	maxPersistedRunBytes   = 32 * 1024
	maxPersistedTotalBytes = 512 * 1024
	maxLiveLinesPerRun     = 4000
)

type StreamingWSBackend interface {
	RunWSCommandStream(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error)
}

type OutputHistoryPathProvider interface {
	OutputHistoryPath() (string, bool)
}

type outputStreamEvent struct {
	line string
	done bool
	res  CommandRunResult
	err  error
}

type outputStreamHandle struct {
	runID  int
	actID  int
	cmdStr string
	ch     chan outputStreamEvent
	cancel context.CancelFunc
}

type outputStreamLineMsg struct {
	RunID int
	Line  string
}

type outputStreamDoneMsg struct {
	RunID  int
	Result CommandRunResult
	Err    error
}

type launchStreamCmdMsg struct {
	Args       []string
	Dangerous  bool
	OpenOutput bool
}

type cancelOutputRunMsg struct {
	RunID int
}

type CommandRun struct {
	ID          int
	Command     string
	StartedAt   time.Time
	Duration    time.Duration
	ExitCode    int
	Lines       []string
	IsErr       bool
	Running     bool
	Cancelling  bool
	Cancelled   bool
	CurrentStep string
	Dangerous   bool
	Rerun       func(Backend) tea.Cmd
}

type persistedCommandRun struct {
	ID        int           `json:"id"`
	Command   string        `json:"command"`
	StartedAt time.Time     `json:"startedAt"`
	Duration  time.Duration `json:"duration"`
	ExitCode  int           `json:"exitCode"`
	Lines     []string      `json:"lines,omitempty"`
	IsErr     bool          `json:"isErr,omitempty"`
	Cancelled bool          `json:"cancelled,omitempty"`
	Dangerous bool          `json:"dangerous,omitempty"`
}

type persistedOutputHistory struct {
	Version int                   `json:"version"`
	Runs    []persistedCommandRun `json:"runs"`
}

type outputPanel struct {
	runs           []CommandRun
	activeIdx      int
	scroll         int
	wrap           bool
	filterQuery    string
	historyPath    string
	historyCanSave bool
	streams        map[int]*outputStreamHandle
}

func newOutputPanel() outputPanel {
	return outputPanel{
		wrap:    true,
		streams: map[int]*outputStreamHandle{},
	}
}

func (p outputPanel) WithHistory(path string, writable bool) outputPanel {
	p.historyPath = strings.TrimSpace(path)
	p.historyCanSave = writable && p.historyPath != ""
	if p.historyPath != "" {
		if loaded, err := loadOutputHistory(p.historyPath); err == nil && len(loaded) > 0 {
			p.runs = loaded
			p.activeIdx = len(p.runs) - 1
			p.scroll = 0
		}
	}
	return p
}

func (p outputPanel) MaxRunID() int {
	maxID := 0
	for _, r := range p.runs {
		if r.ID > maxID {
			maxID = r.ID
		}
	}
	return maxID
}

func (p outputPanel) ID() string         { return "output" }
func (p outputPanel) Title() string      { return "Command Output" }
func (p outputPanel) ShortTitle() string { return "Output" }
func (p outputPanel) SubViews() []string { return nil }
func (p outputPanel) ActiveSubView() int { return 0 }
func (p outputPanel) SetSubView(int) ResourcePanel {
	return p
}
func (p outputPanel) OnSubViewChanged(Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p outputPanel) SetScope(string, Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p outputPanel) IsZoned() bool                                     { return false }
func (p outputPanel) FilterQuery() string                               { return p.filterQuery }
func (p outputPanel) FilterTarget() string                              { return "Output" }
func (p outputPanel) SetFilterQuery(q string, _ Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.scroll = 0
	return p, nil
}
func (p outputPanel) OnBlur() ResourcePanel                     { return p }
func (p outputPanel) SelectedCount() int                        { return 0 }
func (p outputPanel) ClearSelection() ResourcePanel             { return p }
func (p outputPanel) SupportsPreview() bool                     { return false }
func (p outputPanel) DefaultSplit(int) SplitMode                { return SplitOff }
func (p outputPanel) CapturingInput() bool                      { return false }
func (p outputPanel) HasOpenModal() bool                        { return false }
func (p outputPanel) CanPop() bool                              { return false }
func (p outputPanel) Pop() ResourcePanel                        { return p }
func (p outputPanel) ViewModal(int, int, Styles) (string, bool) { return "", false }

func (p outputPanel) Badge() string {
	lbl, _ := p.TabBadge()
	return lbl
}

func (p outputPanel) TabBadge() (string, AttentionLevel) {
	running := 0
	for _, r := range p.runs {
		if r.Running {
			running++
		}
	}
	if running > 0 {
		return fmt.Sprintf("Out(%d·run)", len(p.runs)), AttentionInfo
	}
	if len(p.runs) > 0 && p.runs[len(p.runs)-1].IsErr {
		return fmt.Sprintf("Out(%d!)", len(p.runs)), AttentionErr
	}
	return fmt.Sprintf("Out(%d)", len(p.runs)), AttentionNone
}

func (p outputPanel) Attention() AttentionLevel {
	for _, r := range p.runs {
		if r.Running {
			return AttentionInfo
		}
	}
	if len(p.runs) > 0 && p.runs[len(p.runs)-1].IsErr {
		return AttentionErr
	}
	return AttentionNone
}

func (p outputPanel) ActiveStep() string {
	for i := len(p.runs) - 1; i >= 0; i-- {
		r := p.runs[i]
		if !r.Running {
			continue
		}
		if r.Cancelling {
			return "cancelling " + truncate(r.Command, 32)
		}
		if strings.TrimSpace(r.CurrentStep) != "" {
			return r.CurrentStep
		}
		if r.Command != "" {
			return "running " + truncate(r.Command, 36)
		}
		return "running..."
	}
	return ""
}

func (p outputPanel) PaneStatus() string {
	if step := p.ActiveStep(); step != "" {
		return step
	}
	return ""
}

func (p outputPanel) MutationInFlight() bool {
	for _, r := range p.runs {
		if r.Running {
			return true
		}
	}
	return false
}

func (p outputPanel) HasRunningStream() bool {
	return len(p.streams) > 0
}

func (p outputPanel) Breadcrumbs() []string {
	if r, ok := p.activeRun(); ok {
		return []string{"Output", fmt.Sprintf("#%d %s", r.ID, truncate(r.Command, 36))}
	}
	return []string{"Output"}
}

func (p outputPanel) StatusChips() []string {
	var chips []string
	if r, ok := p.activeRun(); ok {
		if r.Running {
			if r.Cancelling {
				chips = append(chips, "cancelling")
			} else {
				chips = append(chips, "running")
			}
		} else if r.Cancelled {
			chips = append(chips, "cancelled")
		} else if r.IsErr {
			chips = append(chips, fmt.Sprintf("exit:%d", r.ExitCode))
		} else {
			chips = append(chips, "ok")
		}
	}
	if p.wrap {
		chips = append(chips, "wrap:on")
	} else {
		chips = append(chips, "wrap:off")
	}
	if p.filterQuery != "" {
		chips = append(chips, "filter:"+truncate(p.filterQuery, 18))
	}
	return chips
}

func (p outputPanel) KeyHints(SplitMode, bool) [][2]string {
	hints := [][2]string{
		{"j/k/C-d", "Scroll output"},
		{"[/]", "Prev / Next run"},
		{"y/Y", "Copy output/CLI"},
		{"w", "Toggle wrap"},
	}
	if p.HasRunningStream() || p.MutationInFlight() {
		hints = append(hints, [2]string{"x/C-x", "Cancel running"})
	} else {
		hints = append(hints, [2]string{"r/esc", "Re-run / Return"})
	}
	return hints
}

func (p outputPanel) CLIEquivalent() string {
	if r, ok := p.activeRun(); ok && r.Command != "" {
		return r.Command
	}
	return "ws status"
}

func (p outputPanel) activeRun() (CommandRun, bool) {
	if len(p.runs) == 0 {
		return CommandRun{}, false
	}
	idx := clamp(p.activeIdx, 0, len(p.runs)-1)
	return p.runs[idx], true
}

func (p outputPanel) RegisterStream(h *outputStreamHandle) outputPanel {
	if h == nil {
		return p
	}
	if p.streams == nil {
		p.streams = map[int]*outputStreamHandle{}
	}
	p.streams[h.runID] = h
	return p
}

func (p outputPanel) StreamHandle(runID int) (*outputStreamHandle, bool) {
	if p.streams == nil {
		return nil, false
	}
	h, ok := p.streams[runID]
	return h, ok
}

func (p outputPanel) RemoveStream(runID int) (*outputStreamHandle, outputPanel) {
	if p.streams == nil {
		return nil, p
	}
	h := p.streams[runID]
	delete(p.streams, runID)
	return h, p
}

func (p outputPanel) CancelRun(runID int) (string, bool, outputPanel) {
	if p.streams == nil {
		return "", false, p
	}
	if runID <= 0 {
		if r, ok := p.activeRun(); ok && r.Running {
			if _, ok := p.streams[r.ID]; ok {
				runID = r.ID
			}
		}
		if runID <= 0 {
			for i := len(p.runs) - 1; i >= 0; i-- {
				if p.runs[i].Running {
					if _, ok := p.streams[p.runs[i].ID]; ok {
						runID = p.runs[i].ID
						break
					}
				}
			}
		}
	}
	h, ok := p.streams[runID]
	if !ok || h == nil {
		return "", false, p
	}
	if h.cancel != nil {
		h.cancel()
	}
	cmdStr := h.cmdStr
	for i := range p.runs {
		if p.runs[i].ID == runID {
			p.runs[i].Cancelling = true
			p.runs[i].CurrentStep = "cancelling..."
			if cmdStr == "" {
				cmdStr = p.runs[i].Command
			}
			break
		}
	}
	return cmdStr, true, p
}

func (p outputPanel) CancelAll() {
	for _, h := range p.streams {
		if h != nil && h.cancel != nil {
			h.cancel()
		}
	}
}

func (p outputPanel) RecordRun(run CommandRun) outputPanel {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	run.Command = redactOutputLine(run.Command)
	run.Lines = redactOutputLines(run.Lines)
	if run.ID <= 0 {
		run.ID = p.MaxRunID() + 1
	}
	for i := range p.runs {
		if p.runs[i].ID == run.ID {
			p.runs[i] = run
			p.activeIdx = i
			p.scroll = 0
			if !run.Running {
				p.saveHistoryBestEffort()
			}
			return p
		}
	}
	p.runs = append(p.runs, run)
	if len(p.runs) > maxOutputRuns {
		p.runs = p.runs[len(p.runs)-maxOutputRuns:]
	}
	p.activeIdx = len(p.runs) - 1
	p.scroll = 0
	if !run.Running {
		p.saveHistoryBestEffort()
	}
	return p
}

func (p outputPanel) AppendLine(id int, rawLine string) (outputPanel, string) {
	line := redactOutputLine(strings.TrimRight(rawLine, "\r\n"))
	step := extractStepFromLine(line)
	for i := range p.runs {
		if p.runs[i].ID != id {
			continue
		}
		wasAtTail := i == p.activeIdx && p.scroll >= max(0, len(p.runs[i].Lines)-1)
		p.runs[i].Lines = append(p.runs[i].Lines, line)
		if len(p.runs[i].Lines) > maxLiveLinesPerRun {
			p.runs[i].Lines = p.runs[i].Lines[len(p.runs[i].Lines)-maxLiveLinesPerRun:]
		}
		if step != "" {
			p.runs[i].CurrentStep = step
		}
		if wasAtTail {
			p.scroll = max(0, len(p.runs[i].Lines)-1)
		}
		return p, step
	}
	return p, step
}

func (p outputPanel) CompleteRun(id int, res CommandRunResult, err error) outputPanel {
	resLines := redactOutputLines(splitOutputLines(res.Output))
	cancelled := errors.Is(err, context.Canceled)
	isErr := res.IsErr || err != nil || res.ExitCode != 0
	code := res.ExitCode
	if cancelled {
		isErr = true
		if code == 0 {
			code = 130
		}
	} else if isErr && code == 0 {
		code = 1
	}
	for i := range p.runs {
		if p.runs[i].ID == id {
			wasCancelling := p.runs[i].Cancelling
			if wasCancelling && err != nil {
				cancelled = true
				if code == 0 || code == 1 {
					code = 130
				}
			}
			p.runs[i].Running = false
			p.runs[i].Cancelling = false
			p.runs[i].Cancelled = cancelled
			p.runs[i].CurrentStep = ""
			if res.Duration > 0 {
				p.runs[i].Duration = res.Duration
			} else if !p.runs[i].StartedAt.IsZero() {
				p.runs[i].Duration = time.Since(p.runs[i].StartedAt)
			}
			p.runs[i].ExitCode = code
			p.runs[i].IsErr = isErr
			if res.Command != "" {
				p.runs[i].Command = redactOutputLine(res.Command)
			}
			if len(resLines) > 0 {
				p.runs[i].Lines = resLines
			} else if cancelled {
				p.runs[i].Lines = append(p.runs[i].Lines, "cancelled by user")
			} else if err != nil && len(p.runs[i].Lines) == 0 {
				p.runs[i].Lines = []string{"error: " + redactOutputLine(err.Error())}
			}
			p.activeIdx = i
			p.saveHistoryBestEffort()
			return p
		}
	}
	if err != nil && len(resLines) == 0 {
		if cancelled {
			resLines = []string{"cancelled by user"}
		} else {
			resLines = []string{"error: " + redactOutputLine(err.Error())}
		}
	}
	return p.RecordRun(CommandRun{
		ID:        id,
		Command:   redactOutputLine(res.Command),
		Duration:  res.Duration,
		ExitCode:  code,
		Lines:     resLines,
		IsErr:     isErr,
		Cancelled: cancelled,
	})
}

func (p outputPanel) saveHistoryBestEffort() {
	if !p.historyCanSave || p.historyPath == "" {
		return
	}
	_ = saveOutputHistory(p.historyPath, p.runs)
}

func extractStepFromLine(line string) string {
	clean := strings.TrimSpace(ui.StripANSI(line))
	if clean == "" {
		return ""
	}
	clean = strings.TrimPrefix(clean, "==>")
	clean = strings.TrimSpace(clean)
	if len(clean) > 64 {
		clean = truncate(clean, 64)
	}
	return clean
}

func splitOutputLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func waitOutputStreamCmd(h *outputStreamHandle) tea.Cmd {
	if h == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-h.ch
		if !ok {
			return nil
		}
		if ev.done {
			return outputStreamDoneMsg{
				RunID:  h.runID,
				Result: ev.res,
				Err:    ev.err,
			}
		}
		return outputStreamLineMsg{
			RunID: h.runID,
			Line:  ev.line,
		}
	}
}

func startWSCommandStream(backend Backend, runID, actID int, args []string) (*outputStreamHandle, tea.Cmd) {
	cmdStr := "ws " + strings.Join(args, " ")
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan outputStreamEvent, 128)
	h := &outputStreamHandle{
		runID:  runID,
		actID:  actID,
		cmdStr: cmdStr,
		ch:     ch,
		cancel: cancel,
	}
	argsCopy := append([]string(nil), args...)
	go func() {
		defer close(ch)
		onLine := func(line string) {
			select {
			case ch <- outputStreamEvent{line: line}:
			case <-ctx.Done():
			}
		}
		var res CommandRunResult
		var err error
		if sb, ok := backend.(StreamingWSBackend); ok {
			res, err = sb.RunWSCommandStream(ctx, argsCopy, onLine)
		} else if backend != nil {
			res, err = backend.RunWSCommand(argsCopy)
		} else {
			err = fmt.Errorf("backend unavailable")
			res = CommandRunResult{Command: cmdStr, ExitCode: 1, IsErr: true}
		}
		if res.Command == "" {
			res.Command = cmdStr
		}
		select {
		case ch <- outputStreamEvent{done: true, res: res, err: err}:
		case <-time.After(2 * time.Second):
		}
	}()
	return h, waitOutputStreamCmd(h)
}

func (p outputPanel) filteredLines(r CommandRun) []string {
	if strings.TrimSpace(p.filterQuery) == "" {
		return r.Lines
	}
	return fuzzy.Filter(r.Lines, p.filterQuery, func(line string) ([]string, map[string]string) {
		return []string{ui.StripANSI(line)}, nil
	})
}

func (p outputPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	kmsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return p, nil
	}
	if m := classifyVimMotion(kmsg.String()); m != motionNone {
		maxScroll := 0
		if r, ok := p.activeRun(); ok {
			maxScroll = max(0, len(p.filteredLines(r))-1)
		}
		p.scroll, _ = applyScrollMotion(p.scroll, maxScroll, 20, m)
		return p, nil
	}
	switch kmsg.String() {
	case "[":
		if p.activeIdx > 0 {
			p.activeIdx--
			p.scroll = 0
		}
	case "]":
		if p.activeIdx < len(p.runs)-1 {
			p.activeIdx++
			p.scroll = 0
		}
	case "w":
		p.wrap = !p.wrap
	case "x", "ctrl+x":
		if r, ok := p.activeRun(); ok && r.Running {
			id := r.ID
			return p, func() tea.Msg { return cancelOutputRunMsg{RunID: id} }
		}
		if p.HasRunningStream() {
			return p, func() tea.Msg { return cancelOutputRunMsg{} }
		}
	case "y":
		if r, ok := p.activeRun(); ok {
			lines := p.filteredLines(r)
			if len(lines) == 0 {
				return p, func() tea.Msg { return statusToastMsg{text: "No output lines to copy"} }
			}
			return p, copyCmd(strings.Join(lines, "\n"), fmt.Sprintf("%d output line(s)", len(lines)))
		}
	case "Y":
		if r, ok := p.activeRun(); ok && r.Command != "" {
			return p, copyCmd(r.Command, "command")
		}
	case "r":
		if r, ok := p.activeRun(); ok && r.Rerun != nil {
			if r.Dangerous {
				runCopy := r
				return p, func() tea.Msg {
					return confirmRequestMsg{
						Title:        "Confirm Re-Run Command",
						Body:         "Execute command again:\n$ " + runCopy.Command,
						Warning:      "This action may modify files or installed packages.",
						ConfirmLabel: "Re-Run",
						Dangerous:    true,
						OnConfirm:    runCopy.Rerun,
					}
				}
			}
			return p, r.Rerun(backend)
		}
	}
	return p, nil
}

func (p outputPanel) ViewMain(width, height int, focused bool, st Styles) string {
	return p.renderOutput(width, height, focused, false, st)
}

func (p outputPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	return p.renderOutput(width, height, focused, borderless, st)
}

func (p outputPanel) renderOutput(width, height int, focused bool, borderless bool, st Styles) string {
	if len(p.runs) == 0 {
		msgLines := []string{
			st.SectionHeader.Render("Output Console is idle"),
			"",
			st.Muted.Render("Run an action (pull repos, update/remove packages, apply fixes, backup ws.db)"),
			st.Muted.Render("or execute `:status`, `:pull`, or `:!ws <cmd>` to capture stdout/stderr here."),
		}
		if borderless {
			return fitLines(strings.Join(msgLines, "\n"), height)
		}
		return renderTitledBox("Command Output (0 runs)", "", msgLines, width, height, focused, st)
	}

	r, _ := p.activeRun()
	filtered := p.filteredLines(r)

	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	if innerW < 8 {
		innerW = 8
	}

	var body []string
	if !borderless {
		var pills []string
		startRun := max(0, len(p.runs)-5)
		for i := startRun; i < len(p.runs); i++ {
			rr := p.runs[i]
			mark := "✓"
			if rr.Running {
				mark = "…"
			} else if rr.Cancelled {
				mark = "⊘"
			} else if rr.IsErr {
				mark = "✗"
			}
			lbl := fmt.Sprintf("#%d %s %s", rr.ID, mark, truncate(rr.Command, 24))
			if i == clamp(p.activeIdx, 0, len(p.runs)-1) {
				pills = append(pills, st.TabActive.Render(lbl))
			} else {
				pills = append(pills, st.TabInactive.Render(lbl))
			}
		}
		body = append(body, fitStyledWidth(strings.Join(pills, " "), innerW, st))

		statusStr := st.OKText.Render(fmt.Sprintf("OK (exit 0) · %s", r.Duration.Round(10*time.Millisecond)))
		if r.Running {
			if r.Cancelling {
				statusStr = st.WarnText.Render("Cancelling...")
			} else if r.CurrentStep != "" {
				statusStr = st.InfoText.Render("Running · " + truncate(r.CurrentStep, 28) + " (x cancel)")
			} else {
				statusStr = st.InfoText.Render("Running... (x cancel)")
			}
		} else if r.Cancelled {
			statusStr = st.WarnText.Render(fmt.Sprintf("CANCELLED (exit %d) · %s", r.ExitCode, r.Duration.Round(10*time.Millisecond)))
		} else if r.IsErr {
			statusStr = st.ErrText.Render(fmt.Sprintf("FAILED (exit %d) · %s", r.ExitCode, r.Duration.Round(10*time.Millisecond)))
		}
		hdr := st.AccentText.Render("$ "+truncate(r.Command, max(10, innerW-lipgloss.Width(statusStr)-4))) + "  " + statusStr
		body = append(body, fitStyledWidth(hdr, innerW, st))
		body = append(body, st.Muted.Render(strings.Repeat("─", innerW)))
	}

	availLog := max(1, innerH-len(body))
	var displayLines []string
	numW := len(fmt.Sprintf("%d", max(1, len(filtered))))
	for idx, rawLine := range filtered {
		if borderless {
			if p.wrap && lipgloss.Width(rawLine) > innerW {
				displayLines = append(displayLines, wrapTextLines(rawLine, innerW)...)
			} else {
				displayLines = append(displayLines, rawLine)
			}
			continue
		}
		lnPrefix := st.Muted.Render(fmt.Sprintf("%*d ", numW, idx+1))
		textW := max(8, innerW-numW-1)
		if p.wrap && lipgloss.Width(ui.StripANSI(rawLine)) > textW {
			wrapped := wrapTextLines(ui.StripANSI(rawLine), textW)
			for wi, wl := range wrapped {
				if wi == 0 {
					displayLines = append(displayLines, lnPrefix+st.RowNormal.Render(wl))
				} else {
					displayLines = append(displayLines, strings.Repeat(" ", numW+1)+st.RowNormal.Render(wl))
				}
			}
		} else {
			displayLines = append(displayLines, lnPrefix+st.RowNormal.Render(rawLine))
		}
	}
	if len(displayLines) == 0 {
		if r.Running {
			displayLines = []string{st.InfoText.Render("Executing command...")}
		} else if p.filterQuery != "" {
			displayLines = []string{st.Muted.Render("No output lines match filter.")}
		} else {
			displayLines = []string{st.Muted.Render("(command completed with no output)")}
		}
	}

	maxScroll := max(0, len(displayLines)-availLog)
	sc := clamp(p.scroll, 0, maxScroll)
	end := min(len(displayLines), sc+availLog)
	visible := displayLines[sc:end]

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

	body = append(body, visible...)
	badge := fmt.Sprintf("#%d/%d", clamp(p.activeIdx, 0, len(p.runs)-1)+1, len(p.runs))
	return renderTitledBox(fmt.Sprintf("Command Output · #%d", r.ID), badge, body, width, height, focused, st)
}

func wrapTextLines(s string, width int) []string {
	if width <= 1 || lipgloss.Width(s) <= width {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	cw := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if cw+rw > width && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			cw = 0
		}
		cur.WriteRune(r)
		cw += rw
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var (
	reSecretToken = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{16,}|sk-(?:proj-|live-|test-)?[A-Za-z0-9_-]{12,}|xox[baprs]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[0-9A-Z]{16}|tskey-[A-Za-z0-9_-]{12,}|ops_[A-Za-z0-9_-]{16,})\b`)
	reAuthHeader  = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+([A-Za-z0-9._~+/=-]{8,})`)
	reURLCreds    = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:)([^/\s@]+)(@)`)
	reKVSecret    = regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:SECRET|TOKEN|PASSWORD|PASSWD|API_?KEY|PRIVATE_KEY|ACCESS_KEY|AUTH_?KEY|CREDENTIAL|SIGNING_KEY|ENCRYPTION_KEY|WS_DB_KEY|TAILSCALE_AUTHKEY)\b\s*[:=]\s*)(?:"([^"]*)"|'([^']*)'|([^\s,;]+))`)
	rePEMLine     = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
)

func redactOutputLine(line string) string {
	if line == "" {
		return ""
	}
	if rePEMLine.MatchString(line) {
		return "[REDACTED PRIVATE KEY]"
	}
	out := reURLCreds.ReplaceAllStringFunc(line, func(m string) string {
		parts := reURLCreds.FindStringSubmatch(m)
		if len(parts) != 4 {
			return m
		}
		return parts[1] + store.MaskValue(parts[2], true) + parts[3]
	})
	out = reAuthHeader.ReplaceAllStringFunc(out, func(m string) string {
		parts := reAuthHeader.FindStringSubmatch(m)
		if len(parts) != 3 {
			return m
		}
		return parts[1] + " " + store.MaskValue(parts[2], true)
	})
	out = reKVSecret.ReplaceAllStringFunc(out, func(m string) string {
		parts := reKVSecret.FindStringSubmatch(m)
		if len(parts) != 5 {
			return m
		}
		val := parts[2]
		quote := ""
		if parts[2] != "" {
			val = parts[2]
			quote = `"`
		} else if parts[3] != "" {
			val = parts[3]
			quote = `'`
		} else {
			val = parts[4]
		}
		if val == "" || strings.Contains(val, "••••") || strings.Contains(val, "****") {
			return m
		}
		return parts[1] + quote + store.MaskValue(val, true) + quote
	})
	out = reSecretToken.ReplaceAllStringFunc(out, func(m string) string {
		return store.MaskValue(m, true)
	})
	return out
}

func redactOutputLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	inPEM := false
	for _, ln := range lines {
		if rePEMLine.MatchString(ln) {
			inPEM = true
			out = append(out, "[REDACTED PRIVATE KEY]")
			continue
		}
		if inPEM {
			if strings.Contains(ln, "-----END ") {
				inPEM = false
			}
			continue
		}
		out = append(out, redactOutputLine(ln))
	}
	return out
}

func capPersistedLines(lines []string) []string {
	redacted := redactOutputLines(lines)
	if len(redacted) == 0 {
		return nil
	}
	if len(redacted) > maxPersistedLinesPerOp {
		redacted = redacted[len(redacted)-maxPersistedLinesPerOp:]
	}
	totalBytes := 0
	start := len(redacted)
	for i := len(redacted) - 1; i >= 0; i-- {
		ln := redacted[i]
		if len(ln) > 2048 {
			ln = ln[:2048] + "…"
			redacted[i] = ln
		}
		if totalBytes+len(ln)+1 > maxPersistedRunBytes && start < len(redacted) {
			break
		}
		totalBytes += len(ln) + 1
		start = i
	}
	return append([]string(nil), redacted[start:]...)
}

func saveOutputHistory(path string, runs []CommandRun) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	persisted := make([]persistedCommandRun, 0, min(len(runs), maxOutputRuns))
	start := max(0, len(runs)-maxOutputRuns)
	for _, r := range runs[start:] {
		if r.Running {
			continue
		}
		persisted = append(persisted, persistedCommandRun{
			ID:        r.ID,
			Command:   redactOutputLine(r.Command),
			StartedAt: r.StartedAt,
			Duration:  r.Duration,
			ExitCode:  r.ExitCode,
			Lines:     capPersistedLines(r.Lines),
			IsErr:     r.IsErr,
			Cancelled: r.Cancelled,
			Dangerous: r.Dangerous,
		})
	}
	for {
		payload := persistedOutputHistory{
			Version: 1,
			Runs:    persisted,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if len(raw) <= maxPersistedTotalBytes || len(persisted) <= 1 {
			if len(raw) > maxPersistedTotalBytes && len(persisted) == 1 {
				persisted[0].Lines = nil
				raw, err = json.Marshal(persistedOutputHistory{Version: 1, Runs: persisted})
				if err != nil {
					return err
				}
			}
			return app.WriteAtomicFile(path, raw, 0o600)
		}
		persisted = persisted[1:]
	}
}

func loadOutputHistory(path string) ([]CommandRun, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var payload persistedOutputHistory
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	start := max(0, len(payload.Runs)-maxOutputRuns)
	out := make([]CommandRun, 0, len(payload.Runs)-start)
	for _, pr := range payload.Runs[start:] {
		out = append(out, CommandRun{
			ID:        pr.ID,
			Command:   redactOutputLine(pr.Command),
			StartedAt: pr.StartedAt,
			Duration:  pr.Duration,
			ExitCode:  pr.ExitCode,
			Lines:     redactOutputLines(pr.Lines),
			IsErr:     pr.IsErr,
			Cancelled: pr.Cancelled,
			Dangerous: pr.Dangerous,
		})
	}
	return out, nil
}
