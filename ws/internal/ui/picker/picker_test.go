package picker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

func sendPickerMsg(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("expected picker.Model, got %T", next)
	}
	return updated, cmd
}

func keyRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func sampleItems() []Item {
	items := []Item{
		{Title: "api-service", Subtitle: "work · main*", Value: "/tmp/work/code/api-service", Filter: "github"},
		{Title: "demo-web", Subtitle: "personal · feat/ui", Value: "/tmp/personal/code/demo-web", Filter: "github"},
		{Title: "infra-terraform", Subtitle: "work · main", Value: "/tmp/work/code/infra-terraform", Filter: "codecommit"},
		{Title: "notes-cli", Subtitle: "personal · main", Value: "/tmp/personal/code/notes-cli", Filter: "github"},
	}
	for i := 0; i < 24; i++ {
		items = append(items, Item{
			Title:    "service-" + string(rune('a'+i%26)),
			Subtitle: "work · main",
			Value:    "/tmp/work/code/service",
		})
	}
	return items
}

func TestFuzzyAndSubstringFiltering(t *testing.T) {
	m := NewModel(Options{
		Title: "Select repository",
		Items: sampleItems(),
	})

	if len(m.Filtered()) != 28 {
		t.Fatalf("expected 28 initial items, got %d", len(m.Filtered()))
	}

	for _, r := range "personal" {
		m, _ = sendPickerMsg(t, m, keyRune(r))
	}
	got := m.Filtered()
	if len(got) != 2 || got[0].Title != "demo-web" || got[1].Title != "notes-cli" {
		t.Fatalf("unexpected substring filter results for personal: %+v", got)
	}

	for range "personal" {
		m, _ = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "itf" {
		m, _ = sendPickerMsg(t, m, keyRune(r))
	}
	got = m.Filtered()
	if len(got) == 0 || got[0].Title != "infra-terraform" {
		t.Fatalf("expected fuzzy match infra-terraform first for 'itf', got %+v", got)
	}

	rendered := m.Render()
	if !strings.Contains(rendered, "/28") || !strings.Contains(rendered, "type to filter · ↑/↓ move · enter select · esc cancel") {
		t.Fatalf("expected match count and keybind hints in rendered picker:\n%s", rendered)
	}
}

func TestNavigationSelectAndCancel(t *testing.T) {
	items := sampleItems()[:4]
	m := NewModel(Options{
		Title: "Select repository",
		Items: items,
	})

	m, _ = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.Cursor() != 1 {
		t.Fatalf("expected cursor=1, got %d", m.Cursor())
	}

	var cmd tea.Cmd
	m, cmd = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected quit cmd on enter")
	}
	sel, ok := m.Selected()
	if !ok || sel.Value != "/tmp/personal/code/demo-web" {
		t.Fatalf("unexpected selection: ok=%v sel=%+v", ok, sel)
	}

	m2 := NewModel(Options{Title: "Cancel test", Items: items})
	m2, cmd = sendPickerMsg(t, m2, tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil || !m2.Cancelled() {
		t.Fatalf("expected cancelled=true and quit cmd on esc")
	}
	if _, ok := m2.Selected(); ok {
		t.Fatalf("expected no selected item after cancel")
	}
}

func TestEmptyResultsAndNoMatchEnter(t *testing.T) {
	if _, err := Pick(context.Background(), Options{Title: "Empty"}); !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty for 0 items, got %v", err)
	}

	m := NewModel(Options{
		Title: "Select AWS profile",
		Items: []Item{{Title: "prod", Subtitle: "work · us-east-1", Value: "prod"}},
	})
	for _, r := range "zzzzz" {
		m, _ = sendPickerMsg(t, m, keyRune(r))
	}
	if len(m.Filtered()) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(m.Filtered()))
	}

	var cmd tea.Cmd
	m, cmd = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected enter on empty matches to be a no-op")
	}
	out := m.Render()
	if !strings.Contains(out, "0/1") || !strings.Contains(out, "No matching items.") {
		t.Fatalf("expected empty state and 0/1 match count:\n%s", out)
	}
}

func TestBoundedHeightAndInlineView(t *testing.T) {
	if got := VisibleRows(10); got != 6 {
		t.Fatalf("VisibleRows(10) = %d, want 6", got)
	}
	if got := VisibleRows(16); got != 10 {
		t.Fatalf("VisibleRows(16) = %d, want 10", got)
	}
	if got := VisibleRows(45); got != 14 {
		t.Fatalf("VisibleRows(45) = %d, want 14", got)
	}

	m := NewModel(Options{
		Title:  "Many items",
		Items:  sampleItems(),
		Width:  80,
		Height: 40,
	})
	if m.View().AltScreen {
		t.Fatalf("picker must be inline/overlay, never full-screen AltScreen")
	}

	m, _ = sendPickerMsg(t, m, tea.WindowSizeMsg{Width: 80, Height: 16})
	if m.VisibleLimit() != 10 {
		t.Fatalf("expected visible limit 10 for termH=16, got %d", m.VisibleLimit())
	}
	out := m.Render()

	if lines := strings.Split(out, "\n"); len(lines) > 15 {
		t.Fatalf("expected bounded height <= 15 lines at termH=16, got %d:\n%s", len(lines), out)
	}
}

func TestNoColorZeroANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	m := NewModel(Options{
		Title:   "Select profile",
		Items:   sampleItems()[:4],
		Palette: config.Palette{ID: "gruvbox-dark", Mode: "dark"},
		Width:   80,
		Height:  24,
	})
	out := m.Render()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("expected zero ANSI escapes under NO_COLOR=1, got %q", out)
	}
	if !strings.Contains(out, "4/4") || !strings.Contains(out, "api-service") {
		t.Fatalf("expected picker content under NO_COLOR=1:\n%s", out)
	}
}

func TestPickWritesUIOnlyToOutputNotStdout(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var uiOut bytes.Buffer
	sel, err := Pick(ctx, Options{
		Title:  "Pick AWS profile",
		Items:  []Item{{Title: "dev", Subtitle: "work", Value: "dev"}, {Title: "prod", Subtitle: "work · eu-west-1", Value: "prod"}},
		Input:  strings.NewReader("prod\r"),
		Output: &uiOut,
		Width:  80,
		Height: 24,
		Roles:  &ui.Roles{NoColor: true},
	})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if sel.Value != "prod" {
		t.Fatalf("expected selected prod, got %+v", sel)
	}
	if uiOut.Len() == 0 {
		t.Fatalf("expected picker chrome to render to Output writer")
	}

	cancelCtx, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	var cancelOut bytes.Buffer
	_, err = Pick(cancelCtx, Options{
		Title:  "Pick AWS profile",
		Items:  []Item{{Title: "dev", Subtitle: "work", Value: "dev"}},
		Input:  strings.NewReader("\x03"),
		Output: &cancelOut,
		Width:  80,
		Height: 24,
	})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("expected ErrCancelled on Ctrl+C, got %v", err)
	}
}

func TestTTYDetectionWhenStdoutPiped(t *testing.T) {
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rPipe.Close()
	defer wPipe.Close()

	if hasInteractiveTTY(rPipe, wPipe, func() bool { return false }) {
		t.Fatalf("expected false when stdin/stderr are pipes and /dev/tty unavailable")
	}

	if hasInteractiveTTY(rPipe, wPipe, func() bool { return true }) {
		t.Fatalf("expected false when both stdin and stderr are non-TTY pipes")
	}
}

func TestPickerTinyHeightAndWideStyledRowClamping(t *testing.T) {
	roles := ui.NewRoles(config.Palette{Mode: "dark"}, false)
	items := []Item{{
		Title:    roles.OK.Render(strings.Repeat("wide-styled-title-", 4)),
		Subtitle: "very-long-subtitle-that-must-truncate",
		Value:    "v1",
	}}
	m := NewModel(Options{
		Title:  "Tiny",
		Items:  items,
		Width:  32,
		Height: 9,
		Roles:  &roles,
	})
	if got := m.VisibleLimit(); got != 3 {
		t.Fatalf("expected visible limit=3 for tiny termH=9, got %d", got)
	}
	out := m.Render()
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 32 && i < len(strings.Split(out, "\n"))-1 {
			t.Fatalf("picker line %d overflowed width 32 (%d): %q", i, w, line)
		}
	}
}

func TestResponsiveWidthAndNoArtificial92ColTruncation(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	roles := ui.NewRoles(config.Palette{Mode: "dark"}, true)

	longPath := "~/personal/code/org/very-long-repository-path-name-that-previously-truncated-at-92-columns/app-service"
	longSub := "personal · feat/responsive-width"
	m := NewModel(Options{
		Title: "Select repository",
		Items: []Item{{
			Title:    longPath,
			Subtitle: longSub,
			Value:    "/tmp/repo",
		}},
		Width:  180,
		Height: 24,
		Roles:  &roles,
	})

	wideOut := m.Render()
	wideLines := strings.Split(wideOut, "\n")
	if gotW := lipgloss.Width(wideLines[0]); gotW != 180 {
		t.Fatalf("expected wide picker to span 180 cols instead of 92 cap, got %d:\n%s", gotW, wideOut)
	}
	for i, line := range wideLines {
		if w := lipgloss.Width(line); w > 180 {
			t.Fatalf("wide line %d overflowed 180 (%d): %q", i, w, line)
		}
	}
	if !strings.Contains(wideOut, longPath) || !strings.Contains(wideOut, longSub) {
		t.Fatalf("expected wide picker to display full path and subtitle untruncated:\n%s", wideOut)
	}

	m, _ = sendPickerMsg(t, m, tea.WindowSizeMsg{Width: 72, Height: 24})
	narrowOut := m.Render()
	narrowLines := strings.Split(narrowOut, "\n")
	if gotW := lipgloss.Width(narrowLines[0]); gotW != 72 {
		t.Fatalf("expected resized picker to match 72 cols, got %d", gotW)
	}
	for i, line := range narrowLines {
		if w := lipgloss.Width(line); w > 72 {
			t.Fatalf("narrow line %d overflowed 72 (%d): %q", i, w, line)
		}
	}
	titleW, subW := m.columnWidths(72 - 4)
	if titleW < (72-6)*60/100 {
		t.Fatalf("expected title column to receive >=60%% of usable width on tight screen, got titleW=%d subW=%d", titleW, subW)
	}
}

func TestFixedColumnsStayStableAcrossScrollAndFilter(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	roles := ui.NewRoles(config.Palette{Mode: "dark"}, true)

	longPath := "~/personal/code/org/very-long-repository-path-name-that-gets-full-flex-width/app-service"
	items := []Item{
		{
			Title:    "~/work/code/short",
			Subtitle: "work · main",
			Columns:  []string{"work", "main", "~/work/code/short"},
			Value:    "/tmp/work/code/short",
			Filter:   "short work main",
		},
		{
			Title:    longPath,
			Subtitle: "personal · feat/very-long-branch-name-that-clips-to-24-chars",
			Columns:  []string{"personal", "feat/very-long-branch-name-that-clips-to-24-chars", longPath},
			Value:    "/tmp/personal/code/long",
			Filter:   "app-service personal",
		},
	}

	m := NewModel(Options{
		Title: "Select repository",
		Items: items,
		Columns: []ColumnSpec{
			{Title: "ZONE", Width: 10, Accent: true},
			{Title: "BRANCH", Width: 24, Muted: true},
			{Title: "REPOSITORY", Width: 0},
		},
		Width:  160,
		Height: 24,
		Roles:  &roles,
	})

	initial := ui.StripANSI(m.Render())
	if strings.Contains(initial, "\x1b") {
		t.Fatalf("expected zero ANSI under NO_COLOR=1")
	}
	var hdrLine, row0, row1 string
	for _, ln := range strings.Split(initial, "\n") {
		if strings.Contains(ln, "ZONE") && strings.Contains(ln, "BRANCH") && strings.Contains(ln, "REPOSITORY") {
			hdrLine = ln
		} else if strings.Contains(ln, "~/work/code/short") {
			row0 = ln
		} else if strings.Contains(ln, longPath) {
			row1 = ln
		}
	}
	if hdrLine == "" || row0 == "" || row1 == "" {
		t.Fatalf("expected ZONE/BRANCH/REPOSITORY header and both rows:\n%s", initial)
	}
	displayColOf := func(line, sub string) int {
		idx := strings.Index(line, sub)
		if idx < 0 {
			return -1
		}
		return lipgloss.Width(line[:idx])
	}
	branchCol := displayColOf(hdrLine, "BRANCH")
	repoCol := displayColOf(hdrLine, "REPOSITORY")
	if displayColOf(row0, "main") != branchCol || displayColOf(row0, "~/work/code/short") != repoCol {
		t.Fatalf("row 0 columns misaligned (want branch@%d repo@%d): %q", branchCol, repoCol, row0)
	}
	if displayColOf(row1, "feat/") != branchCol || displayColOf(row1, longPath) != repoCol {
		t.Fatalf("row 1 columns misaligned (want branch@%d repo@%d): %q", branchCol, repoCol, row1)
	}

	m, _ = sendPickerMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	scrolled := ui.StripANSI(m.Render())
	for _, ln := range strings.Split(scrolled, "\n") {
		if strings.Contains(ln, "~/work/code/short") && displayColOf(ln, "~/work/code/short") != repoCol {
			t.Fatalf("repository column shifted after scroll (want %d): %q", repoCol, ln)
		}
	}

	for _, r := range "short" {
		m, _ = sendPickerMsg(t, m, keyRune(r))
	}
	filtered := ui.StripANSI(m.Render())
	for _, ln := range strings.Split(filtered, "\n") {
		if strings.Contains(ln, "~/work/code/short") {
			if displayColOf(ln, "main") != branchCol || displayColOf(ln, "~/work/code/short") != repoCol {
				t.Fatalf("columns shifted after filter (want branch@%d repo@%d): %q", branchCol, repoCol, ln)
			}
		}
	}

	m, _ = sendPickerMsg(t, m, tea.WindowSizeMsg{Width: 68, Height: 24})
	narrow := ui.StripANSI(m.Render())
	for i, ln := range strings.Split(narrow, "\n") {
		if w := lipgloss.Width(ln); w > 68 {
			t.Fatalf("narrow line %d overflowed 68 (%d): %q", i, w, ln)
		}
	}
}

func TestPickerClearsInlineViewOnSelectAndCancel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	items := sampleItems()[:3]

	mEnter := NewModel(Options{Title: "Select", Items: items, Width: 80, Height: 24})
	if mEnter.View().Content == "" {
		t.Fatalf("expected non-empty View before exit")
	}
	mEnter, _ = sendPickerMsg(t, mEnter, tea.KeyPressMsg{Code: tea.KeyEnter})
	if mEnter.View().Content != "" {
		t.Fatalf("expected empty View on Enter select, got %q", mEnter.View().Content)
	}
	if _, ok := mEnter.Selected(); !ok {
		t.Fatalf("expected item selected on Enter")
	}

	mEsc := NewModel(Options{Title: "CancelEsc", Items: items, Width: 80, Height: 24})
	mEsc, _ = sendPickerMsg(t, mEsc, tea.KeyPressMsg{Code: tea.KeyEsc})
	if mEsc.View().Content != "" || !mEsc.Cancelled() {
		t.Fatalf("expected empty View and Cancelled=true on Esc, got view=%q cancelled=%v", mEsc.View().Content, mEsc.Cancelled())
	}

	mCtrlC := NewModel(Options{Title: "CancelCtrlC", Items: items, Width: 80, Height: 24})
	mCtrlC, _ = sendPickerMsg(t, mCtrlC, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if mCtrlC.View().Content != "" || !mCtrlC.Cancelled() {
		t.Fatalf("expected empty View and Cancelled=true on Ctrl+C, got view=%q cancelled=%v", mCtrlC.View().Content, mCtrlC.Cancelled())
	}

	mQuit := NewModel(Options{Title: "QuitMsg", Items: items, Width: 80, Height: 24})
	mQuit, _ = sendPickerMsg(t, mQuit, tea.QuitMsg{})
	if mQuit.View().Content != "" || !mQuit.Cancelled() {
		t.Fatalf("expected empty View and Cancelled=true on QuitMsg, got view=%q cancelled=%v", mQuit.View().Content, mQuit.Cancelled())
	}

	for _, tc := range []struct {
		name      string
		input     string
		wantValue string
		wantErr   error
	}{
		{name: "select", input: "prod\r", wantValue: "prod"},
		{name: "esc", input: "\x1b", wantErr: ErrCancelled},
		{name: "ctrl-c", input: "\x03", wantErr: ErrCancelled},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var outBuf bytes.Buffer
		sel, err := Pick(ctx, Options{
			Title:  "Pick profile",
			Items:  []Item{{Title: "dev", Subtitle: "work", Value: "dev"}, {Title: "prod", Subtitle: "work", Value: "prod"}},
			Input:  strings.NewReader(tc.input),
			Output: &outBuf,
			Width:  80,
			Height: 24,
			Roles:  &ui.Roles{NoColor: true},
		})
		cancel()
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: expected err %v, got %v", tc.name, tc.wantErr, err)
			}
		} else if err != nil || sel.Value != tc.wantValue {
			t.Fatalf("%s: expected sel=%q err=nil, got sel=%+v err=%v", tc.name, tc.wantValue, sel, err)
		}

		raw := outBuf.String()
		idx := strings.LastIndex(raw, ansi.EraseScreenBelow)
		if idx < 0 {
			t.Fatalf("%s: expected final render to emit EraseScreenBelow (%q), got %q", tc.name, ansi.EraseScreenBelow, raw)
		}
		tail := raw[idx+len(ansi.EraseScreenBelow):]
		for _, forbidden := range []string{"╭", "│", "╰", "Pick profile", "dev", "prod"} {
			if strings.Contains(tail, forbidden) {
				t.Fatalf("%s: expected zero leftover picker chrome %q after final EraseScreenBelow, got tail=%q", tc.name, forbidden, tail)
			}
		}
	}
}

func TestFzfV2RankingPrimaryOverPathAndExtendedQuery(t *testing.T) {
	items := []Item{
		{
			Title:    "~/work/code/acme/api-service",
			Subtitle: "work · main",
			Primary:  "api-service",
			Columns:  []string{"work", "main", "~/work/code/acme/api-service"},
			Value:    "/tmp/work/code/acme/api-service",
			Filter:   "work main ~/work/code/acme/api-service api-service",
		},
		{
			Title:    "~/work/code/acme/auth-billing",
			Subtitle: "work · feat/payments",
			Primary:  "auth-billing",
			Columns:  []string{"work", "feat/payments", "~/work/code/acme/auth-billing"},
			Value:    "/tmp/work/code/acme/auth-billing",
			Filter:   "work feat/payments ~/work/code/acme/auth-billing auth-billing",
		},
		{
			Title:    "~/personal/code/igwtcode/workstation",
			Subtitle: "personal · main",
			Primary:  "workstation",
			Columns:  []string{"personal", "main", "~/personal/code/igwtcode/workstation"},
			Value:    "/tmp/personal/code/igwtcode/workstation",
			Filter:   "personal main ~/personal/code/igwtcode/workstation workstation",
		},
		{
			Title:    "~/personal/code/igwtcode/work-notes",
			Subtitle: "personal · main",
			Primary:  "work-notes",
			Columns:  []string{"personal", "main", "~/personal/code/igwtcode/work-notes"},
			Value:    "/tmp/personal/code/igwtcode/work-notes",
			Filter:   "personal main ~/personal/code/igwtcode/work-notes work-notes",
		},
		{
			Title:    "~/work/code/acme/billing-api",
			Subtitle: "work · feat/auth",
			Primary:  "billing-api",
			Columns:  []string{"work", "feat/auth", "~/work/code/acme/billing-api"},
			Value:    "/tmp/work/code/acme/billing-api",
			Filter:   "work feat/auth ~/work/code/acme/billing-api billing-api",
		},
		{
			Title:    "~/personal/code/org/auth-service",
			Subtitle: "personal · main",
			Primary:  "auth-service",
			Columns:  []string{"personal", "main", "~/personal/code/org/auth-service"},
			Value:    "/tmp/personal/code/org/auth-service",
			Filter:   "personal main ~/personal/code/org/auth-service auth-service",
		},
	}

	workGot := FilterItems(items, "work")
	if len(workGot) < 4 {
		t.Fatalf("expected >=4 matches for 'work', got %+v", workGot)
	}
	if workGot[0].Primary != "work-notes" || workGot[1].Primary != "workstation" {
		t.Fatalf("expected Primary work-notes & workstation before ~/work/code/* repos, got %s, %s", workGot[0].Primary, workGot[1].Primary)
	}

	wstatGot := FilterItems(items, "wstat")
	if len(wstatGot) == 0 || wstatGot[0].Primary != "workstation" {
		t.Fatalf("expected workstation #1 for 'wstat', got %+v", wstatGot)
	}

	authGot := FilterItems(items, "auth")
	if len(authGot) != 3 || authGot[0].Primary != "auth-billing" || authGot[1].Primary != "auth-service" || authGot[2].Primary != "billing-api" {
		t.Fatalf("expected Primary auth-* items before feat/auth billing-api for 'auth', got %+v", authGot)
	}

	negGot := FilterItems(items, "work notes$")
	if len(negGot) != 1 || negGot[0].Primary != "work-notes" {
		t.Fatalf("expected [work-notes] for 'work notes$', got %+v", negGot)
	}
	exclGot := FilterItems(items, "work !notes !api")
	for _, g := range exclGot {
		if strings.Contains(g.Primary, "notes") || strings.Contains(g.Primary, "api") {
			t.Fatalf("expected negated terms excluded, got %+v", g)
		}
	}

	awsItems := []Item{
		{Title: "acme-preprod-admin", Subtitle: "work · eu-central-1", Primary: "acme-preprod-admin", Columns: []string{"work", "eu-central-1", "acme-preprod-admin"}},
		{Title: "acme-prod-admin-ro", Subtitle: "work · eu-central-1", Primary: "acme-prod-admin-ro", Columns: []string{"work", "eu-central-1", "acme-prod-admin-ro"}},
		{Title: "acme-prod-admin", Subtitle: "work · eu-central-1", Primary: "acme-prod-admin", Columns: []string{"work", "eu-central-1", "acme-prod-admin"}},
		{Title: "personal-prod-admin", Subtitle: "personal · us-east-1", Primary: "personal-prod-admin", Columns: []string{"personal", "us-east-1", "personal-prod-admin"}},
	}
	prodaGot := FilterItems(awsItems, "proda")
	if len(prodaGot) < 3 || prodaGot[0].Primary != "acme-prod-admin" || prodaGot[1].Primary != "acme-prod-admin-ro" {
		t.Fatalf("expected acme-prod-admin #1 and acme-prod-admin-ro #2 for 'proda', got %+v", prodaGot)
	}
	wprodGot := FilterItems(awsItems, "wprod")
	if len(wprodGot) == 0 || wprodGot[0].Primary == "personal-prod-admin" {
		t.Fatalf("expected cross-column 'wprod' to prefer work profile, got %+v", wprodGot)
	}
}

func TestPickerHighlightRespectsNoColorAndWidth(t *testing.T) {
	items := []Item{
		{
			Title:    "~/personal/code/workstation",
			Subtitle: "personal · main",
			Primary:  "workstation",
			Columns:  []string{"personal", "main", "~/personal/code/workstation"},
			Value:    "/tmp/personal/code/workstation",
		},
		{
			Title:    "~/work/code/api-service",
			Subtitle: "work · main",
			Primary:  "api-service",
			Columns:  []string{"work", "main", "~/work/code/api-service"},
			Value:    "/tmp/work/code/api-service",
		},
	}
	cols := []ColumnSpec{
		{Title: "ZONE", Width: 10, Accent: true},
		{Title: "BRANCH", Width: 16, Muted: true},
		{Title: "REPOSITORY", Width: 0},
	}

	t.Run("no color emits zero ANSI", func(t *testing.T) {
		noColor := ui.NewRoles(config.Palette{Mode: "dark"}, true)
		m := NewModel(Options{
			Title:   "Select repository",
			Query:   "wstat",
			Items:   items,
			Columns: cols,
			Width:   90,
			Height:  20,
			Roles:   &noColor,
		})
		out := m.Render()
		if strings.Contains(out, "\x1b") {
			t.Fatalf("expected zero ANSI under NoColor with query highlight, got %q", out)
		}
	})

	t.Run("color highlights without width overflow", func(t *testing.T) {
		colored := ui.NewRoles(config.Palette{Mode: "dark"}, false)
		m := NewModel(Options{
			Title:   "Select repository",
			Query:   "work",
			Items:   items,
			Columns: cols,
			Width:   90,
			Height:  20,
			Roles:   &colored,
		})
		out := m.Render()
		if !strings.Contains(out, "\x1b[") {
			t.Fatalf("expected ANSI highlighting when color is enabled")
		}
		for i, ln := range strings.Split(out, "\n") {
			if w := lipgloss.Width(ln); w > 90 {
				t.Fatalf("highlighted line %d overflowed width 90 (%d): %q", i, w, ln)
			}
		}
		plain := ui.StripANSI(out)
		if !strings.Contains(plain, "workstation") || !strings.Contains(plain, "api-service") {
			t.Fatalf("expected full plain labels preserved after highlight, got:\n%s", plain)
		}
	})
}

func TestPickerMouseWheelAndMouseMode(t *testing.T) {
	noColor := ui.NewRoles(config.Palette{Mode: "dark"}, true)
	items := []Item{
		{Title: "alpha", Value: "a"},
		{Title: "beta", Value: "b"},
		{Title: "gamma", Value: "c"},
	}
	m := NewModel(Options{
		Title:  "Pick item",
		Items:  items,
		Width:  60,
		Height: 16,
		Roles:  &noColor,
	})
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("expected active picker View().MouseMode == MouseModeCellMotion, got %v", v.MouseMode)
	}

	m, _ = sendPickerMsg(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5, Y: 5})
	if m.cursor != 1 {
		t.Fatalf("expected MouseWheelDown to move cursor to 1, got %d", m.cursor)
	}
	m, _ = sendPickerMsg(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: 5})
	if m.cursor != 0 {
		t.Fatalf("expected MouseWheelUp to move cursor back to 0, got %d", m.cursor)
	}
}

func hasReverseSGR(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] != '\x1b' || i+1 >= len(line) || line[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
			j++
		}
		if j < len(line) && line[j] == 'm' {
			for _, tok := range strings.Split(line[i+2:j], ";") {
				if tok == "7" {
					return true
				}
			}
		}
		i = j
	}
	return false
}

func TestPickerSelectionUsesCalmThemeSurfaceNotReverse(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	tokyoPal := config.Palette{
		ID:   "tokyonight-dark",
		Mode: "dark",
		Tokens: map[string]string{
			"bg":           "#1a1b26",
			"fg":           "#c0caf5",
			"accent":       "#7aa2f7",
			"selection_bg": "#283457",
			"selection_fg": "#c0caf5",
			"color1":       "#f7768e",
			"color2":       "#9ece6a",
			"color3":       "#e0af68",
			"color4":       "#7aa2f7",
			"color5":       "#bb9af7",
			"color6":       "#7dcfff",
			"color9":       "#f7768e",
			"color10":      "#9ece6a",
			"color11":      "#e0af68",
			"color12":      "#7aa2f7",
			"color13":      "#bb9af7",
			"color14":      "#7dcfff",
		},
	}

	items := []Item{
		{
			Title:    "~/personal/code/workstation",
			Subtitle: "personal · main",
			Primary:  "workstation",
			Columns:  []string{"personal", "main", "~/personal/code/workstation"},
			Value:    "/tmp/workstation",
		},
		{
			Title:    "~/work/code/api-service",
			Subtitle: "work · main",
			Primary:  "api-service",
			Columns:  []string{"work", "main", "~/work/code/api-service"},
			Value:    "/tmp/api",
		},
	}

	coloredRoles := ui.NewRoles(tokyoPal, false)

	mCols := NewModel(Options{
		Title:   "Select repository",
		Query:   "work",
		Items:   items,
		Palette: tokyoPal,
		Roles:   &coloredRoles,
		Columns: []ColumnSpec{
			{Title: "ZONE", Width: 10, Accent: true},
			{Title: "BRANCH", Width: 12, Muted: true},
			{Title: "REPOSITORY", Width: 0},
		},
		Width:  80,
		Height: 20,
	})
	outCols := mCols.Render()
	var selColLine string
	for i, ln := range strings.Split(outCols, "\n") {
		if w := lipgloss.Width(ln); w > 80 {
			t.Fatalf("column picker line %d overflowed width 80 (%d): %q", i, w, ln)
		}
		if strings.Contains(ui.StripANSI(ln), "> personal") {
			selColLine = ln
		}
	}
	if selColLine == "" {
		t.Fatalf("expected selected column row in output:\n%s", ui.StripANSI(outCols))
	}
	if !strings.Contains(selColLine, "48;2;40;52;87") {
		t.Fatalf("expected tokyonight-dark selection_bg (48;2;40;52;87) on selected row, got %q", selColLine)
	}
	if hasReverseSGR(selColLine) {
		t.Fatalf("selected column row unexpectedly used reverse-video SGR 7: %q", selColLine)
	}

	mRows := NewModel(Options{
		Title:   "Select item",
		Query:   "work",
		Items:   items,
		Palette: tokyoPal,
		Roles:   &coloredRoles,
		Width:   80,
		Height:  20,
	})
	outRows := mRows.Render()
	var selPlainRow string
	for _, ln := range strings.Split(outRows, "\n") {
		if strings.Contains(ui.StripANSI(ln), "> ~/personal/code/workstation") {
			selPlainRow = ln
			break
		}
	}
	if selPlainRow == "" {
		t.Fatalf("expected selected title+subtitle row in output:\n%s", ui.StripANSI(outRows))
	}
	if !strings.Contains(selPlainRow, "48;2;40;52;87") {
		t.Fatalf("expected selection_bg on selected title+subtitle row, got %q", selPlainRow)
	}
	if hasReverseSGR(selPlainRow) {
		t.Fatalf("selected title+subtitle row unexpectedly used reverse-video SGR 7: %q", selPlainRow)
	}

	noColorRoles := ui.NewRoles(tokyoPal, true)
	mNoColor := NewModel(Options{
		Title:   "Select repository",
		Query:   "work",
		Items:   items,
		Palette: tokyoPal,
		Roles:   &noColorRoles,
		Width:   80,
		Height:  20,
	})
	if got := mNoColor.Render(); strings.Contains(got, "\x1b") {
		t.Fatalf("expected zero ANSI under NoColor, got %q", got)
	}
}
