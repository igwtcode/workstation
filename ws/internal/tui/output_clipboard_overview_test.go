package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

type streamingMockBackend struct {
	*mockBackend
	historyPath     string
	historyWritable bool
	streamFn        func(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error)
}

func (s *streamingMockBackend) OutputHistoryPath() (string, bool) {
	return s.historyPath, s.historyWritable
}

func (s *streamingMockBackend) RunWSCommandStream(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error) {
	if s.streamFn != nil {
		return s.streamFn(ctx, args, onLine)
	}
	return s.mockBackend.RunWSCommand(args)
}

func TestLiveStreamingOutputAndStatusBarStep(t *testing.T) {
	gate1 := make(chan struct{})
	gate2 := make(chan struct{})
	doneGate := make(chan struct{})

	sb := &streamingMockBackend{
		mockBackend: newMockBackend(),
		streamFn: func(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error) {
			onLine("==> Phase 1: updating packages")
			<-gate1
			onLine("  ✓ brew upgraded 3 formulae")
			<-gate2
			<-doneGate
			return CommandRunResult{
				Command:  "ws " + strings.Join(args, " "),
				ExitCode: 0,
				Duration: 42 * time.Millisecond,
				Output:   "==> Phase 1: updating packages\n  ✓ brew upgraded 3 formulae",
			}, nil
		},
	}

	m := NewModel(sb, Options{StartTab: "overview"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 34})
	m.pendingLoads = 0

	var cmd tea.Cmd
	m, cmd = m.launchWSCommand([]string{"update", "--digest"}, true)
	if m.ActivePanel() != PanelOutput {
		t.Fatalf("expected active panel PanelOutput, got %d", m.ActivePanel())
	}

	var waitCmd tea.Cmd
	if cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sub == nil {
					continue
				}
				subMsg := sub()
				switch v := subMsg.(type) {
				case outputStreamLineMsg:
					m, waitCmd = sendMsg(t, m, v)
				case activityMsg:
					m, _ = sendMsg(t, m, v)
				case statusToastMsg:
					m, _ = sendMsg(t, m, v)
				}
			}
		}
	}
	if waitCmd == nil {
		t.Fatalf("expected streaming line to re-subscribe waitCmd")
	}

	m.toast = ""
	outMid := ui.StripANSI(m.Render())
	if !strings.Contains(outMid, "Phase 1: updating packages") {
		t.Fatalf("expected streamed line 1 in Output panel before command exit:\n%s", outMid)
	}
	if !strings.Contains(outMid, "Out(1·run)") {
		t.Fatalf("expected active running badge Out(1·run) in bottom nav:\n%s", outMid)
	}
	lines := strings.Split(outMid, "\n")
	statusBar := lines[len(lines)-1]
	if !strings.Contains(statusBar, "Phase 1: updating packages") {
		t.Fatalf("expected current step in status bar, got %q", statusBar)
	}

	close(gate1)
	msg2 := waitCmd()
	line2, ok := msg2.(outputStreamLineMsg)
	if !ok || !strings.Contains(line2.Line, "brew upgraded") {
		t.Fatalf("expected second streamed line, got %#v", msg2)
	}
	m, waitCmd = sendMsg(t, m, line2)
	if waitCmd == nil {
		t.Fatalf("expected second waitCmd while still running")
	}

	close(gate2)
	close(doneGate)
	doneMsg := waitCmd()
	if _, ok := doneMsg.(outputStreamDoneMsg); !ok {
		t.Fatalf("expected outputStreamDoneMsg, got %#v", doneMsg)
	}
	m, _ = sendMsg(t, m, doneMsg)

	op := m.OutputPanel()
	if len(op.runs) != 1 || op.runs[0].Running || op.runs[0].IsErr {
		t.Fatalf("expected completed non-error run, got %+v", op.runs)
	}
	if badge, attn := op.TabBadge(); badge != "Out(1)" || attn != AttentionNone {
		t.Fatalf("expected settled badge Out(1)/AttentionNone, got %q/%v", badge, attn)
	}
}

func TestCancelRunningStreamKeyWithoutQuitting(t *testing.T) {
	started := make(chan struct{})
	cancelledCh := make(chan struct{})

	sb := &streamingMockBackend{
		mockBackend: newMockBackend(),
		streamFn: func(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error) {
			onLine("starting long job")
			close(started)
			<-ctx.Done()
			close(cancelledCh)
			return CommandRunResult{
				Command:  "ws " + strings.Join(args, " "),
				ExitCode: 130,
				IsErr:    true,
			}, context.Canceled
		},
	}

	m := NewModel(sb, Options{StartTab: "overview"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m.pendingLoads = 0

	var cmd tea.Cmd
	m, cmd = m.launchWSCommand([]string{"update"}, true)

	<-started
	var waitCmd tea.Cmd
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			subMsg := sub()
			if lm, ok := subMsg.(outputStreamLineMsg); ok {
				m, waitCmd = sendMsg(t, m, lm)
			} else {
				m, _ = sendMsg(t, m, subMsg)
			}
		}
	}

	var cancelCmd tea.Cmd
	m, cancelCmd = sendMsg(t, m, keyRune('x'))
	if cancelCmd == nil {
		t.Fatalf("expected 'x' in Output panel to emit cancel command")
	}
	m, _ = sendMsg(t, m, cancelCmd())

	select {
	case <-cancelledCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected running stream context to be cancelled by 'x'")
	}

	doneMsg := waitCmd()
	if _, ok := doneMsg.(outputStreamDoneMsg); !ok {
		t.Fatalf("expected outputStreamDoneMsg after cancel, got %#v", doneMsg)
	}
	var postCmd tea.Cmd
	m, postCmd = sendMsg(t, m, doneMsg)
	if postCmd != nil {
		if _, isQuit := postCmd().(tea.QuitMsg); isQuit {
			t.Fatalf("cancelling a run must not quit the TUI")
		}
	}

	op := m.OutputPanel()
	if len(op.runs) != 1 || !op.runs[0].Cancelled || op.runs[0].ExitCode != 130 || !op.runs[0].IsErr {
		t.Fatalf("expected cancelled run with exit 130, got %+v", op.runs)
	}
	if badge, attn := op.TabBadge(); badge != "Out(1!)" || attn != AttentionErr {
		t.Fatalf("expected failed badge Out(1!)/AttentionErr, got %q/%v", badge, attn)
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "CANCELLED") {
		t.Fatalf("expected CANCELLED status in Output view:\n%s", ui.StripANSI(m.Render()))
	}

	started2 := make(chan struct{})
	cancelled2 := make(chan struct{})
	sb.streamFn = func(ctx context.Context, args []string, onLine func(string)) (CommandRunResult, error) {
		close(started2)
		<-ctx.Done()
		close(cancelled2)
		return CommandRunResult{Command: "ws update", ExitCode: 130, IsErr: true}, context.Canceled
	}
	m, _ = m.launchWSCommand([]string{"update"}, true)
	<-started2
	m.switchPanel(PanelOverview)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	select {
	case <-cancelled2:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected global ctrl+x to cancel running stream from another panel")
	}
}

func TestOutputHistoryPersistenceRedactionAndReload(t *testing.T) {
	wsHome := t.TempDir()
	histPath := filepath.Join(wsHome, "output_history.json")

	sb := &streamingMockBackend{
		mockBackend:     newMockBackend(),
		historyPath:     histPath,
		historyWritable: true,
	}

	m := NewModel(sb, Options{StartTab: "output"})
	for i := 1; i <= 55; i++ {
		run := CommandRun{
			ID:       i,
			Command:  fmt.Sprintf("ws env check --token=ghp_1234567890abcdefghijklmno%02d", i),
			Duration: time.Duration(i) * time.Millisecond,
			ExitCode: 0,
			Lines: []string{
				fmt.Sprintf("run %d ok", i),
				"API_TOKEN=supersecretvalue9876543210",
				"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
				"dsn=postgres://app:hunter2supersecret@db.internal:5432/prod",
				"token=sk-live-secret1234567890abcdef",
				"-----BEGIN RSA PRIVATE KEY-----",
				"MIIEowIBAAKCAQEAsecretkeymaterial",
				"-----END RSA PRIVATE KEY-----",
			},
			IsErr: i == 55,
		}
		if i == 55 {
			run.ExitCode = 2
		}
		m.recordOutputRun(run)
	}

	fi, err := os.Stat(histPath)
	if err != nil {
		t.Fatalf("expected history file written at %s: %v", histPath, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected history file mode 0600, got %o", perm)
	}
	if fi.Size() > maxPersistedTotalBytes {
		t.Fatalf("expected history file size <= %d, got %d", maxPersistedTotalBytes, fi.Size())
	}

	rawBytes, err := os.ReadFile(histPath)
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	rawStr := string(rawBytes)
	for _, secret := range []string{
		"ghp_1234567890abcdefghijklmno",
		"supersecretvalue9876543210",
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"hunter2supersecret",
		"sk-live-secret1234567890abcdef",
		"MIIEowIBAAKCAQEAsecretkeymaterial",
	} {
		if strings.Contains(rawStr, secret) {
			t.Fatalf("persisted output history leaked plaintext secret %q:\n%s", secret, rawStr)
		}
	}

	m2 := NewModel(sb, Options{StartTab: "overview"})
	m2, _ = sendMsg(t, m2, tea.WindowSizeMsg{Width: 140, Height: 34})
	op2 := m2.OutputPanel()
	if len(op2.runs) != maxOutputRuns {
		t.Fatalf("expected %d reloaded runs, got %d", maxOutputRuns, len(op2.runs))
	}
	if op2.runs[0].ID != 6 || op2.runs[len(op2.runs)-1].ID != 55 {
		t.Fatalf("expected reloaded runs #6..#55, got #%d..#%d", op2.runs[0].ID, op2.runs[len(op2.runs)-1].ID)
	}
	if nextID := m2.allocRunID(); nextID != 56 {
		t.Fatalf("expected nextRunID after reload to be 56, got %d", nextID)
	}
	if badge, attn := op2.TabBadge(); badge != "Out(50!)" || attn != AttentionErr {
		t.Fatalf("expected reloaded failed badge Out(50!)/AttentionErr, got %q/%v", badge, attn)
	}
	if !strings.Contains(ui.StripANSI(m2.Render()), "Out(50!)") {
		t.Fatalf("expected bottom nav to show failed output badge Out(50!):\n%s", ui.StripANSI(m2.Render()))
	}
}

func TestClipboardOSC52FallbackSSHAndOpenURL(t *testing.T) {
	oldClip := clipboardWrite
	oldOSC := osc52Write
	oldEnv := clipGetenv
	oldLook := clipLookPath
	oldBuilder := openURLCmdBuilder
	oldWait := openURLWaitTimeout
	defer func() {
		clipboardWrite = oldClip
		osc52Write = oldOSC
		clipGetenv = oldEnv
		clipLookPath = oldLook
		openURLCmdBuilder = oldBuilder
		openURLWaitTimeout = oldWait
	}()

	var mu sync.Mutex
	var oscCalls []string
	var clipCalls []string
	envMap := map[string]string{}

	clipGetenv = func(k string) string { return envMap[k] }
	osc52Write = func(s string) error {
		mu.Lock()
		oscCalls = append(oscCalls, s)
		mu.Unlock()
		return nil
	}
	clipboardWrite = func(s string) error {
		mu.Lock()
		clipCalls = append(clipCalls, s)
		mu.Unlock()
		return errors.New("xclip/wl-copy unavailable")
	}

	if err := copyToClipboard("hello-fallback"); err != nil {
		t.Fatalf("expected OSC 52 fallback to succeed when clipboard fails: %v", err)
	}
	if len(clipCalls) != 1 || len(oscCalls) != 1 || oscCalls[0] != "hello-fallback" {
		t.Fatalf("expected clipboard attempt then OSC 52 fallback, clip=%v osc=%v", clipCalls, oscCalls)
	}

	clipCalls = nil
	oscCalls = nil
	envMap["SSH_TTY"] = "/dev/pts/1"
	clipboardWrite = func(s string) error {
		clipCalls = append(clipCalls, s)
		return nil
	}
	if err := copyToClipboard("hello-ssh"); err != nil {
		t.Fatalf("expected SSH_TTY to use OSC 52 directly: %v", err)
	}
	if len(clipCalls) != 0 || len(oscCalls) != 1 || oscCalls[0] != "hello-ssh" {
		t.Fatalf("expected SSH_TTY to bypass local clipboardWrite and use OSC 52, clip=%v osc=%v", clipCalls, oscCalls)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("abc"))
	envMap = map[string]string{}
	if got, want := formatOSC52Sequence("abc"), "\x1b]52;c;"+encoded+"\x07"; got != want {
		t.Fatalf("plain OSC 52 = %q, want %q", got, want)
	}
	envMap = map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0"}
	if got, want := formatOSC52Sequence("abc"), "\x1bPtmux;\x1b\x1b]52;c;"+encoded+"\x07\x1b\\"; got != want {
		t.Fatalf("tmux OSC 52 = %q, want %q", got, want)
	}
	envMap = map[string]string{"TERM": "screen-256color"}
	if got, want := formatOSC52Sequence("abc"), "\x1bP\x1b]52;c;"+encoded+"\x07\x1b\\"; got != want {
		t.Fatalf("screen OSC 52 = %q, want %q", got, want)
	}

	openURLWaitTimeout = 120 * time.Millisecond
	openURLCmdBuilder = func(u string) (*exec.Cmd, string, error) {
		return nil, "xdg-open", errors.New("xdg-open not found on PATH")
	}
	if err := openExternalURLImpl("https://example.com"); err == nil || !strings.Contains(err.Error(), "xdg-open not found") {
		t.Fatalf("expected missing opener error, got %v", err)
	}

	openURLCmdBuilder = func(u string) (*exec.Cmd, string, error) {
		return exec.Command("sh", "-c", "echo 'no graphical display' >&2; exit 3"), "xdg-open", nil
	}
	if err := openExternalURLImpl("https://example.com"); err == nil || !strings.Contains(err.Error(), "no graphical display") {
		t.Fatalf("expected early non-zero exit error with stderr, got %v", err)
	}

	openURLCmdBuilder = func(u string) (*exec.Cmd, string, error) {
		return exec.Command("sh", "-c", "exit 0"), "open", nil
	}
	if err := openExternalURLImpl("https://example.com"); err != nil {
		t.Fatalf("expected clean exit 0 to succeed, got %v", err)
	}

	openURLCmdBuilder = func(u string) (*exec.Cmd, string, error) {
		return exec.Command("sh", "-c", "sleep 0.4"), "xdg-open", nil
	}
	start := time.Now()
	if err := openExternalURLImpl("https://example.com"); err != nil {
		t.Fatalf("expected long-running browser process to return nil after brief wait, got %v", err)
	}
	if time.Since(start) > 350*time.Millisecond {
		t.Fatalf("expected openExternalURLImpl not to hang on long-running browser, took %s", time.Since(start))
	}
}

func TestOverviewTabLandingSectionsDegradationAndJump(t *testing.T) {
	b := newMockBackend()
	b.untrackedPkgs = []PresetToolRow{
		{ID: "untracked:brew:jq", PackageName: "jq", Provider: "brew", OriginFile: "pkg/mac-brew.txt"},
	}
	m := initLoadedModel(t, b, Options{})
	if m.ActivePanel() != PanelOverview {
		t.Fatalf("expected Overview tab as default landing view, got panel %d", m.ActivePanel())
	}

	for _, sz := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 160, Height: 45},
		{Width: 220, Height: 55},
	} {
		m, _ = sendMsg(t, m, sz)
		out := m.View().Content
		if h := lipgloss.Height(out); h != sz.Height {
			t.Fatalf("overview at %dx%d height mismatch: %d != %d", sz.Width, sz.Height, h, sz.Height)
		}
		for i, ln := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(ln); lw > sz.Width {
				t.Fatalf("overview at %dx%d line %d overflowed width: %d > %d", sz.Width, sz.Height, i+1, lw, sz.Width)
			}
		}
	}

	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 38})
	rendered := ui.StripANSI(m.Render())
	for _, want := range []string{
		"Workstation Overview",
		"Outdated packages",
		"Doctor findings",
		"Dirty / behind repos",
		"PRs awaiting review",
		"Untracked packages",
		"Last backup age",
		"1 Overview",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("overview missing %q:\n%s", want, rendered)
		}
	}

	jumpCases := []struct {
		rowIdx    int
		wantPanel int
		wantSub   int
	}{
		{0, PanelPackages, pkgSubInstalled},
		{1, PanelWorkstation, wsSubDoctor},
		{2, PanelProjects, projSubRepos},
		{3, PanelProjects, projSubPRs},
		{4, PanelPackages, pkgSubInstalled},
		{5, PanelEnv, envSubVault},
	}
	for _, tc := range jumpCases {
		m, _ = sendMsg(t, m, keyRune('1'))
		if m.ActivePanel() != PanelOverview {
			t.Fatalf("expected key '1' to switch to PanelOverview, got %d", m.ActivePanel())
		}
		m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
		for step := 0; step < tc.rowIdx; step++ {
			m, _ = sendMsg(t, m, keyRune('j'))
		}
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.wantPanel {
			t.Fatalf("row %d jump panel = %d, want %d", tc.rowIdx, m.ActivePanel(), tc.wantPanel)
		}
		if len(m.panels[tc.wantPanel].SubViews()) > 0 && m.panels[tc.wantPanel].ActiveSubView() != tc.wantSub {
			t.Fatalf("row %d jump subview = %d, want %d", tc.rowIdx, m.panels[tc.wantPanel].ActiveSubView(), tc.wantSub)
		}
		m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != PanelOverview {
			t.Fatalf("expected ctrl+o to jump back to Overview from row %d, got %d", tc.rowIdx, m.ActivePanel())
		}
	}

	degraded := NewModel(nil, Options{StartTab: "overview"})
	degraded, _ = sendMsg(t, degraded, tea.WindowSizeMsg{Width: 120, Height: 34})
	degraded, _ = sendMsg(t, degraded, doctorLoadedMsg{err: errors.New("doctor offline")})
	degraded, _ = sendMsg(t, degraded, pkgOutdatedLoadedMsg{err: errors.New("no network")})
	degraded, _ = sendMsg(t, degraded, reposLoadedMsg{err: errors.New("git unavailable")})
	degraded, _ = sendMsg(t, degraded, ghItemsLoadedMsg{err: errors.New("gh not authenticated")})
	degraded, _ = sendMsg(t, degraded, pkgUntrackedLoadedMsg{err: errors.New("pkg manager missing")})
	degraded, _ = sendMsg(t, degraded, dbStatusLoadedMsg{err: errors.New("ws.db missing")})
	degOut := ui.StripANSI(degraded.Render())
	if strings.Count(degOut, "N/A") < 6 {
		t.Fatalf("expected all 6 overview sections to degrade gracefully to N/A:\n%s", degOut)
	}
}
