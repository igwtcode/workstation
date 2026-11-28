package ui

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func TestDurationFormatting(t *testing.T) {
	if got := FormatDuration(-5 * time.Millisecond); got != "0ms" {
		t.Fatalf("FormatDuration(-5ms) = %q, want 0ms", got)
	}
	if got := FormatDuration(420 * time.Millisecond); got != "420ms" {
		t.Fatalf("FormatDuration(420ms) = %q, want 420ms", got)
	}
	if got := FormatDuration(1900 * time.Millisecond); got != "1.9s" {
		t.Fatalf("FormatDuration(1.9s) = %q, want 1.9s", got)
	}
	if got := FormatDuration(72 * time.Second); got != "1m12s" {
		t.Fatalf("FormatDuration(72s) = %q, want 1m12s", got)
	}
	if got := FormatDuration(119*time.Second + 700*time.Millisecond); got != "2m0s" {
		t.Fatalf("FormatDuration(119.7s) = %q, want 2m0s", got)
	}
	if got := FormatSpinnerElapsed(14200 * time.Millisecond); got != "14.2s" {
		t.Fatalf("FormatSpinnerElapsed(14.2s) = %q, want 14.2s", got)
	}
	if got := FormatSpinnerElapsed(62 * time.Second); got != "1m02s" {
		t.Fatalf("FormatSpinnerElapsed(62s) = %q, want 1m02s", got)
	}
}

func TestLoggerLevelsAndNoColor(t *testing.T) {
	var plain bytes.Buffer
	l := NewLogger(&plain, NewRoles(config.Palette{Mode: "dark"}, true))
	l.Info("Checking updates", "preset", "mac-workstation", "note", "has spaces", "empty", "")
	l.Done("Completed", "applied", 9)
	l.Warn("Uncommitted changes")
	l.Error("Command failed", "exit", 1)
	l.Skip("Skipped step", "reason", "policy")
	l.Plan("Plan title", "actions", 3)
	l.Section("Prep")
	l.StepStart("Refresh index")
	l.StepDone("Refresh index", 1500*time.Millisecond, "ok")
	l.StepSkip("Cleanup", "up to date")
	l.StepWarn("Hook", 250*time.Millisecond, "timed out")
	l.StepFail("Upgrade", 2*time.Minute+5*time.Second, "exit 1")
	l.PlanItem("finalize", "Re-sync symlinks")
	l.ChangeItem("config/zsh/colors.sh")
	l.ChildLine("Removing cache")
	l.KV("Host", 8, "darwin/arm64")

	out := plain.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in NoColor logger output:\n%s", out)
	}
	for _, want := range []string{
		"INFO  Checking updates  preset=mac-workstation  note=\"has spaces\"  empty=\"\"",
		"DONE  Completed  applied=9",
		"WARN  Uncommitted changes",
		"ERRO  Command failed  exit=1",
		"SKIP  Skipped step  reason=policy",
		"PLAN  Plan title  actions=3",
		"\nPrep\n",
		"  › Refresh index",
		"  ✓ Refresh index  1.5s · ok",
		"  – Cleanup  up to date",
		"  ! Hook  250ms: timed out",
		"  ✗ Upgrade  2m5s: exit 1",
		"  · [finalize] Re-sync symlinks",
		"  + config/zsh/colors.sh",
		"    │ Removing cache",
		"Host:    darwin/arm64",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in logger output:\n%s", want, out)
		}
	}

	var styled bytes.Buffer
	sl := NewLogger(&styled, NewRoles(config.Palette{Mode: "dark"}, false))
	sl.Info("Styled message", "k", "v")
	sl.Done("Done message")
	if !strings.Contains(styled.String(), "\x1b[") {
		t.Fatalf("expected ANSI styling when NoColor=false, got %q", styled.String())
	}
	if stripped := StripANSI(styled.String()); !strings.Contains(stripped, "INFO  Styled message  k=v") {
		t.Fatalf("unexpected stripped styled output: %q", stripped)
	}
}

func TestProgressNonTTYAndChildGutter(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf, NewRoles(config.Palette{Mode: "dark"}, true))
	defer p.Close()

	p.Section("OS packages & cleanup")
	p.Start("brew-up", "Upgrade Homebrew")
	cw := p.ChildWriter()
	_, _ = cw.Write([]byte("step 10%\rstep 100%\n   \n\t\nline two"))
	p.Done("brew-up", "Upgrade Homebrew", 650*time.Millisecond, "")

	p.Section("Parallel tools")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "t" + string(rune('1'+idx))
			p.Start(id, "Task "+id)
			time.Sleep(5 * time.Millisecond)
			p.Done(id, "Task "+id, 10*time.Millisecond, "")
		}(i)
	}
	wg.Wait()
	p.Close()

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("non-TTY progress emitted ANSI control sequences: %q", out)
	}
	if strings.Contains(out, "step 10%") {
		t.Fatalf("expected carriage-return update to collapse step 10%%:\n%s", out)
	}
	for _, want := range []string{
		"OS packages & cleanup",
		"  › Upgrade Homebrew",
		"    │ step 100%",
		"    │ line two",
		"  ✓ Upgrade Homebrew  650ms",
		"Parallel tools",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in non-TTY progress output:\n%s", want, out)
		}
	}
}

func TestProgressLiveTTYSpinnerAndParallelUpdates(t *testing.T) {
	var sb syncBuffer
	p := NewProgressWithOptions(&sb, NewRoles(config.Palette{Mode: "dark"}, true), ProgressOptions{
		ForceTTY:     true,
		Width:        60,
		TickInterval: 15 * time.Millisecond,
	})
	defer p.Close()

	p.Section("User tools (parallel)")
	p.Start("mise", "Upgrade mise tools")
	p.Start("tldr", "Update tldr page cache")
	p.Start("rustup", "Update Rust toolchains via rustup and extra long label exceeding width")

	time.Sleep(60 * time.Millisecond)
	p.Done("mise", "Upgrade mise tools", 20*time.Millisecond, "")
	p.Done("rustup", "Update Rust toolchains via rustup and extra long label exceeding width", 35*time.Millisecond, "")

	mid := sb.String()
	hasFrame := false
	for _, fr := range spinnerFrames {
		if strings.Contains(mid, fr) {
			hasFrame = true
			break
		}
	}
	if !hasFrame {
		t.Fatalf("expected Braille spinner frame during TTY parallel tasks, got %q", mid)
	}
	if !strings.Contains(mid, "Running 3 tasks:") {
		t.Fatalf("expected multi-task spinner summary in TTY output, got %q", mid)
	}

	snapBefore := len(mid)
	time.Sleep(60 * time.Millisecond)
	after := sb.String()[snapBefore:]
	if !strings.Contains(after, "Update tldr page cache") {
		t.Fatalf("expected remaining task spinner to display Update tldr page cache, got %q", after)
	}
	if strings.Contains(after, "Running 3 tasks:") {
		t.Fatalf("did not expect finished tasks in subsequent spinner redraw, got %q", after)
	}

	_, _ = p.ChildWriter().Write([]byte("downloading pages\n"))
	p.Done("tldr", "Update tldr page cache", 120*time.Millisecond, "")

	p.StartWithTTY("paru", "Upgrade Arch packages via paru", true)
	p.Done("paru", "Upgrade Arch packages via paru", 50*time.Millisecond, "")

	p.Close()

	final := sb.String()
	if !strings.Contains(final, "  › Update tldr page cache\n    │ downloading pages\n") {
		t.Fatalf("expected single › header before child output line, got %q", final)
	}
	if !strings.Contains(final, "  › Upgrade Arch packages via paru\n") {
		t.Fatalf("expected interactive TTY task to print › header immediately, got %q", final)
	}

	checkedSpinnerSegments := 0
	for _, seg := range strings.Split(final, "\r\x1b[2K") {
		line := strings.Split(seg, "\n")[0]
		trimmed := strings.TrimSpace(StripANSI(line))
		isSpinner := false
		for _, fr := range spinnerFrames {
			if strings.HasPrefix(trimmed, fr) {
				isSpinner = true
				break
			}
		}
		if !isSpinner {
			continue
		}
		checkedSpinnerSegments++
		if w := ansi.StringWidth(StripANSI(line)); w >= 60 {
			t.Fatalf("spinner segment exceeded terminal width (%d >= 60): %q", w, line)
		}
	}
	if checkedSpinnerSegments == 0 {
		t.Fatalf("expected at least one spinner segment to validate width")
	}
}

func TestSpinClearsLineOnTTYAndSilentOnNonTTY(t *testing.T) {
	var nonTTY bytes.Buffer
	wantErr := errors.New("boom")
	err := SpinWithOptions(&nonTTY, NewRoles(config.Palette{Mode: "dark"}, true), "Checking updates", ProgressOptions{}, func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("SpinWithOptions error = %v, want %v", err, wantErr)
	}
	if nonTTY.Len() != 0 {
		t.Fatalf("expected zero bytes written on non-TTY Spin, got %q", nonTTY.String())
	}

	var ttyBuf syncBuffer
	if err := SpinWithOptions(&ttyBuf, NewRoles(config.Palette{Mode: "dark"}, true), "Checking workstation updates", ProgressOptions{
		ForceTTY:     true,
		Width:        80,
		TickInterval: 15 * time.Millisecond,
	}, func() error {
		time.Sleep(60 * time.Millisecond)
		return nil
	}); err != nil {
		t.Fatalf("SpinWithOptions ForceTTY failed: %v", err)
	}
	out := ttyBuf.String()
	if !strings.Contains(out, "Checking workstation updates") {
		t.Fatalf("expected spinner label in ForceTTY output, got %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[2K") {
		t.Fatalf("expected SpinWithOptions to clear line with \\r\\x1b[2K on Close, got suffix %q", out)
	}
}
