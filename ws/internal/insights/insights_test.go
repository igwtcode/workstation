package insights

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func newInsightsTestContext(t *testing.T) *app.Context {
	t.Helper()
	home := t.TempDir()
	return &app.Context{
		RepoRoot:  t.TempDir(),
		HomeDir:   home,
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		Host: app.HostInfo{
			OS:   "linux",
			Arch: "amd64",
		},
		LocalEnv: map[string]string{},
	}
}

func TestSearchHistoryFilesFallback(t *testing.T) {
	ctx := newInsightsTestContext(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WS_ZONES", "")

	zshHist := strings.Join([]string{
		": 1700000100:2;git status",
		": 1700000200:5;git commit -m 'multi\\",
		"line'",
		": 1700000050:0;echo hello",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, ".zsh_history"), []byte(zshHist), 0o600); err != nil {
		t.Fatal(err)
	}

	bashHist := strings.Join([]string{
		"#1700000300",
		"GIT diff --stat",
		"#1700000010",
		"ls -la",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, ".bash_history"), []byte(bashHist), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, src, err := SearchHistory(ctx, "git", 10)
	if err != nil {
		t.Fatal(err)
	}
	if src != "history-files" {
		t.Fatalf("expected history-files source, got %q", src)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 git entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Shell != "bash" || entries[0].Command != "GIT diff --stat" {
		t.Fatalf("expected newest entry from bash first, got %+v", entries[0])
	}
	if entries[1].Shell != "zsh" || entries[1].DurationMs != 5000 || !strings.Contains(entries[1].Command, "line'") {
		t.Fatalf("expected multiline zsh entry second, got %+v", entries[1])
	}
	if entries[2].Command != "git status" || entries[2].DurationMs != 2000 {
		t.Fatalf("expected oldest git entry third, got %+v", entries[2])
	}

	limited, _, err := SearchHistory(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected limit 2, got %d", len(limited))
	}
}

func TestSearchHistoryViaAtuin(t *testing.T) {
	ctx := newInsightsTestContext(t)
	t.Setenv("WS_ZONES", "")

	workDir := filepath.Join(ctx.HomeDir, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbDir := filepath.Join(ctx.DataDir, "atuin")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "history.db"), []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	mockAtuin := "#!/bin/sh\nprintf '2026-01-02 10:00:00\\t120ms\\t0\\t" + workDir + "/code/api\\tgo test ./...\\n'\n" +
		"printf '2026-01-01 09:00:00\\t2s\\t1\\t" + ctx.HomeDir + "\\tgo build ./...\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "atuin"), []byte(mockAtuin), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	entries, src, err := SearchHistory(ctx, "go ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if src != "atuin" {
		t.Fatalf("expected atuin source, got %q", src)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 atuin entries, got %+v", entries)
	}
	if entries[0].Zone != "work" || entries[0].DurationMs != 120 || entries[0].ExitCode != 0 {
		t.Fatalf("unexpected first atuin entry: %+v", entries[0])
	}
	if entries[1].ExitCode != 1 || entries[1].DurationMs != 2000 {
		t.Fatalf("unexpected second atuin entry: %+v", entries[1])
	}
}

func TestSearchHistoryViaAtuinBlocksFlagInjection(t *testing.T) {
	ctx := newInsightsTestContext(t)
	t.Setenv("WS_ZONES", "")

	dbDir := filepath.Join(ctx.DataDir, "atuin")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "history.db"), []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(t.TempDir(), "atuin-args.txt")
	binDir := t.TempDir()
	mockAtuin := "#!/bin/sh\n: > \"$ATUIN_ARGS_FILE\"\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> \"$ATUIN_ARGS_FILE\"; done\n" +
		"printf '2026-01-02 10:00:00\\t50ms\\t0\\t" + ctx.HomeDir + "\\techo --delete-it-all\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "atuin"), []byte(mockAtuin), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATUIN_ARGS_FILE", argsFile)
	t.Setenv("PATH", binDir)

	entries, src, err := SearchHistory(ctx, "--delete-it-all", 5)
	if err != nil {
		t.Fatal(err)
	}
	if src != "atuin" || len(entries) != 1 {
		t.Fatalf("expected 1 atuin entry, got src=%q entries=%+v", src, entries)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	if len(argv) < 2 || argv[len(argv)-2] != "--" || argv[len(argv)-1] != "--delete-it-all" {
		t.Fatalf("expected '--' separator immediately before query, got argv=%q", argv)
	}
}

func TestHistoryCustomEnvAndBackslashEdgeCases(t *testing.T) {
	ctx := newInsightsTestContext(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WS_ZONES", "")
	t.Setenv("HISTFILE", "")

	customHist := filepath.Join(ctx.HomeDir, ".custom_zsh_history")
	ctx.LocalEnv["HISTFILE"] = customHist
	zshRaw := strings.Join([]string{
		`: 1700000100:0;echo done\\`,
		`: 1700000200:0;ls -la`,
	}, "\n") + "\n"
	if err := os.WriteFile(customHist, []byte(zshRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	hEntries, _, err := SearchHistory(ctx, "", 10)
	if err != nil || len(hEntries) != 2 {
		t.Fatalf("expected 2 separate zsh entries (even \\\\ is NOT continuation), got len=%d entries=%+v err=%v", len(hEntries), hEntries, err)
	}
	if localT := parseFlexibleTime("2026-01-02 10:00:00"); localT.IsZero() {
		t.Fatal("expected parseFlexibleTime local datetime to parse")
	}
}
