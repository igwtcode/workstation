package cleanup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

type mockCleanRunner struct {
	bins     map[string]string
	handlers map[string]func(cmd []string) (string, string, error)
	calls    [][]string
}

func newMockCleanRunner(bins ...string) *mockCleanRunner {
	m := &mockCleanRunner{
		bins:     map[string]string{},
		handlers: map[string]func([]string) (string, string, error){},
	}
	for _, b := range bins {
		m.bins[b] = "/usr/bin/" + b
	}
	return m
}

func (m *mockCleanRunner) on(key string, fn func(cmd []string) (string, string, error)) {
	m.handlers[key] = fn
}

func (m *mockCleanRunner) Run(cmd []string, _ map[string]string, _ string) (string, string, error) {
	m.calls = append(m.calls, slices.Clone(cmd))
	if fn, ok := m.handlers[strings.Join(cmd, " ")]; ok {
		return fn(cmd)
	}
	return "", "", nil
}

func (m *mockCleanRunner) LookPath(bin string) (string, error) {
	if p, ok := m.bins[bin]; ok {
		return p, nil
	}
	return "", errors.New("not found: " + bin)
}

func makeTestContext(t *testing.T) *app.Context {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(home, "personal", "code", "workstation")
	for _, d := range []string{
		home,
		filepath.Join(home, ".config"),
		filepath.Join(home, ".cache"),
		filepath.Join(home, ".local", "state"),
		filepath.Join(repo, "config", "zsh"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &app.Context{
		RepoRoot:  repo,
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		Host:      app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64"},
		LocalEnv:  map[string]string{},
	}
}

func writeBytes(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetHardRefusalsAndSymlinkDefense(t *testing.T) {
	ctx := makeTestContext(t)
	roots := AllowlistedRoots(ctx)

	if _, err := ValidateTarget(ctx, "/", []string{"/"}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for /, got %v", err)
	}

	if _, err := ValidateTarget(ctx, ctx.HomeDir, []string{ctx.HomeDir}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for HomeDir, got %v", err)
	}

	repoFile := filepath.Join(ctx.RepoRoot, "config", "zsh", "zshrc")
	writeBytes(t, repoFile, 16)
	if _, err := ValidateTarget(ctx, ctx.RepoRoot, []string{ctx.RepoRoot}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for RepoRoot, got %v", err)
	}
	if _, err := ValidateTarget(ctx, repoFile, []string{ctx.RepoRoot}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for file inside RepoRoot, got %v", err)
	}

	outside := filepath.Join(ctx.HomeDir, "Documents", "important.txt")
	writeBytes(t, outside, 16)
	if _, err := ValidateTarget(ctx, outside, roots); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist for %s, got %v", outside, err)
	}

	if _, err := ValidateTarget(ctx, ".cache/bat", roots); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for relative path, got %v", err)
	}

	evilLink := filepath.Join(ctx.CacheDir, "evil-repo-link")
	if err := os.Symlink(ctx.RepoRoot, evilLink); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(ctx, evilLink, roots); !errors.Is(err, ErrSymlinkTarget) {
		t.Errorf("expected ErrSymlinkTarget for symlink pointing to RepoRoot, got %v", err)
	}

	nestedThroughLink := filepath.Join(evilLink, "config", "zsh")
	if _, err := ValidateTarget(ctx, nestedThroughLink, roots); !errors.Is(err, ErrSymlinkTarget) {
		t.Errorf("expected ErrSymlinkTarget for symlink ancestor, got %v", err)
	}

	dangling := filepath.Join(ctx.CacheDir, "dangling-link")
	if err := os.Symlink(filepath.Join(ctx.HomeDir, "does-not-exist"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTarget(ctx, dangling, roots); !errors.Is(err, ErrSymlinkTarget) {
		t.Errorf("expected ErrSymlinkTarget for dangling symlink, got %v", err)
	}

	goodCache := filepath.Join(ctx.CacheDir, "bat")
	writeBytes(t, filepath.Join(goodCache, "syntaxes.bin"), 128)
	valid, err := ValidateTarget(ctx, goodCache, roots)
	if err != nil || valid != goodCache {
		t.Errorf("expected valid target %s, got %q err=%v", goodCache, valid, err)
	}

	homeParent := filepath.Dir(ctx.HomeDir)
	if _, err := ValidateTarget(ctx, homeParent, []string{homeParent}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for ancestor of HomeDir, got %v", err)
	}
	repoParent := filepath.Dir(ctx.RepoRoot)
	if _, err := ValidateTarget(ctx, repoParent, []string{repoParent}); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected ErrUnsafeTarget for ancestor of RepoRoot, got %v", err)
	}

	siblingUnderHomeParent := filepath.Join(homeParent, "other-dir")
	writeBytes(t, filepath.Join(siblingUnderHomeParent, "f.txt"), 16)
	if _, err := ValidateTarget(ctx, siblingUnderHomeParent, []string{homeParent}); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist when allowlisted root contains HomeDir, got %v", err)
	}
	docFile := filepath.Join(ctx.HomeDir, "Documents", "keep.txt")
	if _, err := ValidateTarget(ctx, docFile, []string{ctx.HomeDir}); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist when allowlisted root equals HomeDir, got %v", err)
	}

	codeFile := filepath.Join(ctx.HomeDir, "personal", "code", "other-project", "README.md")
	writeBytes(t, codeFile, 16)
	if _, err := ValidateTarget(ctx, codeFile, roots); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist for arbitrary file in code root, got %v", err)
	}
}

func TestSafeDirSizeNeverFollowsSymlinks(t *testing.T) {
	ctx := makeTestContext(t)
	cacheDir := filepath.Join(ctx.CacheDir, "bat")
	writeBytes(t, filepath.Join(cacheDir, "real.bin"), 100)

	hugeFile := filepath.Join(ctx.RepoRoot, "huge.bin")
	writeBytes(t, hugeFile, 50_000)

	if err := os.Symlink(hugeFile, filepath.Join(cacheDir, "link-to-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ctx.RepoRoot, filepath.Join(cacheDir, "link-to-repo")); err != nil {
		t.Fatal(err)
	}

	sz, err := SafeDirSize(cacheDir, 10)
	if err != nil {
		t.Fatalf("SafeDirSize failed: %v", err)
	}
	if sz != 100 {
		t.Errorf("SafeDirSize = %d, want 100 (must not follow symlinks)", sz)
	}
}

func TestScanAllTiersAndPreWSVerification(t *testing.T) {
	ctx := makeTestContext(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	writeBytes(t, filepath.Join(ctx.CacheDir, "bat", "cache.bin"), 512)
	writeBytes(t, filepath.Join(ctx.CacheDir, "uv", "pkg.whl"), 1024)
	writeBytes(t, filepath.Join(ctx.HomeDir, "Library", "Caches", "Homebrew", "foo.bottle.tar.gz"), 2048)

	repoZshrc := filepath.Join(ctx.RepoRoot, "config", "zsh", "zshrc")
	writeBytes(t, repoZshrc, 32)
	zshrcLink := filepath.Join(ctx.HomeDir, ".zshrc")
	if err := os.Symlink(repoZshrc, zshrcLink); err != nil {
		t.Fatal(err)
	}
	goodBackup := zshrcLink + ".pre-ws"
	writeBytes(t, goodBackup, 256)

	unverifiedTarget := filepath.Join(ctx.HomeDir, ".bashrc")
	writeBytes(t, unverifiedTarget, 32)
	badBackup := unverifiedTarget + ".pre-ws"
	writeBytes(t, badBackup, 256)

	staleProj := filepath.Join(ctx.HomeDir, "personal", "code", "old-crate")
	writeBytes(t, filepath.Join(staleProj, "Cargo.toml"), 32)
	staleTarget := filepath.Join(staleProj, "target")
	staleArtifact := filepath.Join(staleTarget, "debug", "app")
	staleTag := filepath.Join(staleTarget, "CACHEDIR.TAG")
	writeBytes(t, staleArtifact, 4096)
	writeBytes(t, staleTag, 48)
	oldTime := now.Add(-45 * 24 * time.Hour)
	_ = os.Chtimes(staleArtifact, oldTime, oldTime)
	_ = os.Chtimes(staleTag, oldTime, oldTime)
	_ = os.Chtimes(filepath.Join(staleTarget, "debug"), oldTime, oldTime)
	_ = os.Chtimes(staleTarget, oldTime, oldTime)

	freshProj := filepath.Join(ctx.HomeDir, "personal", "code", "fresh-crate")
	writeBytes(t, filepath.Join(freshProj, "Cargo.toml"), 32)
	freshTarget := filepath.Join(freshProj, "target")
	writeBytes(t, filepath.Join(freshTarget, "CACHEDIR.TAG"), 48)
	writeBytes(t, filepath.Join(freshTarget, "debug", "app"), 4096)

	writeBytes(t, filepath.Join(ctx.HomeDir, ".gradle", "caches", "mod.jar"), 3000)

	writeBytes(t, filepath.Join(ctx.HomeDir, ".android", "avd", "Pixel.avd", "userdata.img"), 8192)

	r := newMockCleanRunner("brew", "uv", "docker")
	r.on("docker builder du", func([]string) (string, string, error) {
		return "ID\tSIZE\nTotal:\t2.5GB\nReclaimable:\t1.5GB\n", "", nil
	})
	r.on("docker system df --format {{.Type}}\t{{.Reclaimable}}", func([]string) (string, string, error) {
		return "Images\t0B\nLocal Volumes\t500MB (100%)\n", "", nil
	})

	cands, err := ScanWithOptions(ctx, r, now)
	if err != nil {
		t.Fatalf("ScanWithOptions failed: %v", err)
	}

	byID := map[string]Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}

	if c, ok := byID["safe:homebrew-cache"]; !ok || !c.SelectedByDefault || len(c.CleanerCmd) == 0 {
		t.Errorf("expected safe:homebrew-cache with native cleaner, got %+v", c)
	}
	if c, ok := byID["safe:pre-ws-backups"]; !ok || !slices.Equal(c.Paths, []string{goodBackup}) || c.SelectedByDefault {
		t.Errorf("expected only verified .pre-ws backup %q with SelectedByDefault=false, got %+v", goodBackup, c)
	}
	if c, ok := byID["costly:cargo-targets"]; !ok || !slices.Equal(c.Paths, []string{staleTarget}) || c.SelectedByDefault {
		t.Errorf("expected only stale Cargo target %q (SelectedByDefault=false), got %+v", staleTarget, c)
	}
	if c, ok := byID["costly:docker-buildx"]; !ok || c.Bytes != 1_500_000_000 || c.SelectedByDefault {
		t.Errorf("expected costly:docker-buildx with 1.5GB, got %+v", c)
	}
	if c, ok := byID["dangerous:android-avd"]; !ok || c.Tier != TierDangerous || c.SelectedByDefault {
		t.Errorf("expected dangerous:android-avd not selected by default, got %+v", c)
	}
	if c, ok := byID["dangerous:docker-volumes"]; !ok || c.Tier != TierDangerous || c.SelectedByDefault {
		t.Errorf("expected dangerous:docker-volumes not selected by default, got %+v", c)
	}

	for _, p := range []string{goodBackup, badBackup, staleArtifact} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Scan must be read-only, file missing: %s", p)
		}
	}
}

func TestDangerousTierBlockedUnderNonInteractiveAndWithoutAllowRemove(t *testing.T) {
	ctx := makeTestContext(t)
	avdDir := filepath.Join(ctx.HomeDir, ".android", "avd")
	avdImg := filepath.Join(avdDir, "Pixel.avd", "disk.img")
	writeBytes(t, avdImg, 1024)

	cands := []Candidate{
		{
			ID:                "dangerous:android-avd",
			Tier:              TierDangerous,
			Category:          "vm",
			Title:             "Android Emulator AVDs",
			Paths:             []string{avdDir},
			Bytes:             1024,
			Warning:           "Destroys AVDs",
			SelectedByDefault: false,
		},
	}

	defPlan, err := PlanClean(ctx, cands, nil, nil)
	if err != nil {
		t.Fatalf("default PlanClean failed: %v", err)
	}
	if len(defPlan.Actions) != 0 {
		t.Fatalf("default PlanClean must never select dangerous tier, got %+v", defPlan.Actions)
	}

	ctx.NonInteractive = true
	ctx.Allow.Remove = false
	if _, err := PlanClean(ctx, cands, []string{"dangerous:android-avd"}, nil); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("expected ErrDangerousBlocked in non-interactive without allow.remove, got %v", err)
	}

	ctx.NonInteractive = true
	ctx.Allow.Remove = true
	if _, err := PlanClean(ctx, cands, []string{"dangerous:android-avd"}, nil); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("expected ErrDangerousBlocked in non-interactive even with allow.remove, got %v", err)
	}

	ctx.NonInteractive = false
	ctx.Allow.Remove = false
	if _, err := PlanClean(ctx, cands, []string{"dangerous:android-avd"}, nil); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("expected ErrDangerousBlocked interactively without allow.remove, got %v", err)
	}

	crafted := &engine.Plan{
		Title: "Crafted dangerous plan",
		Actions: []engine.Action{
			{
				ID:       "clean:dangerous:android-avd",
				Kind:     engine.ActionClean,
				Target:   avdDir,
				Risk:     engine.RiskDangerous,
				Metadata: map[string]string{"tier": "dangerous", "category": "vm"},
			},
		},
	}
	ctx.NonInteractive = true
	ctx.Allow.Remove = true
	if _, err := ExecClean(ctx, crafted, newMockCleanRunner()); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("ExecClean must reject dangerous tier under --non-interactive, got %v", err)
	}
	if _, err := os.Stat(avdImg); err != nil {
		t.Errorf("blocked dangerous execution must leave AVD intact")
	}

	ctx.NonInteractive = false
	ctx.Allow.Remove = true
	plan, err := PlanClean(ctx, cands, []string{"dangerous:android-avd"}, nil)
	if err != nil {
		t.Fatalf("expected interactive + allow.remove PlanClean to succeed, got %v", err)
	}
	res, err := ExecClean(ctx, plan, newMockCleanRunner())
	if err != nil || res.Applied != 1 {
		t.Fatalf("expected ExecClean to succeed, got res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(avdDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected AVD dir removed after consented interactive clean")
	}
}

func TestExecCleanBlocksSymlinkSwapTOCTOUAndRespectsDryRun(t *testing.T) {
	ctx := makeTestContext(t)
	batCache := filepath.Join(ctx.CacheDir, "bat")
	writeBytes(t, filepath.Join(batCache, "data.bin"), 256)
	repoFile := filepath.Join(ctx.RepoRoot, "config", "zsh", "keep.zsh")
	writeBytes(t, repoFile, 64)

	cands := []Candidate{
		{
			ID:                "safe:cli-bat",
			Tier:              TierSafe,
			Category:          "cli-cache",
			Title:             "bat syntax cache",
			Paths:             []string{batCache},
			Bytes:             256,
			SelectedByDefault: true,
		},
	}

	plan, err := PlanClean(ctx, cands, nil, nil)
	if err != nil {
		t.Fatalf("PlanClean failed: %v", err)
	}

	ctx.DryRun = true
	resDry, err := ExecClean(ctx, plan, newMockCleanRunner())
	if err != nil || resDry.Applied != 0 || resDry.Skipped != 1 {
		t.Fatalf("unexpected DryRun result: res=%+v err=%v", resDry, err)
	}
	if _, err := os.Stat(batCache); err != nil {
		t.Fatalf("DryRun must not remove files")
	}

	ctx.DryRun = false
	if err := os.RemoveAll(batCache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ctx.RepoRoot, batCache); err != nil {
		t.Fatal(err)
	}

	if _, err := ExecClean(ctx, plan, newMockCleanRunner()); !errors.Is(err, ErrSymlinkTarget) {
		t.Fatalf("ExecClean must catch symlink swap with ErrSymlinkTarget, got %v", err)
	}
	if _, err := os.Stat(repoFile); err != nil {
		t.Fatalf("RepoRoot file must remain untouched after blocked symlink swap")
	}
}

func TestExecCleanConsentForgedPlanAndCorruptLedger(t *testing.T) {
	ctx := makeTestContext(t)

	batCache := filepath.Join(ctx.CacheDir, "bat")
	writeBytes(t, filepath.Join(batCache, "a.bin"), 64)
	repoZshrc := filepath.Join(ctx.RepoRoot, "config", "zsh", "zshrc")
	writeBytes(t, repoZshrc, 16)
	zshrcLink := filepath.Join(ctx.HomeDir, ".zshrc")
	if err := os.Symlink(repoZshrc, zshrcLink); err != nil {
		t.Fatal(err)
	}
	preWS := zshrcLink + ".pre-ws"
	writeBytes(t, preWS, 32)

	cands, err := ScanWithRunner(ctx, newMockCleanRunner())
	if err != nil {
		t.Fatal(err)
	}
	defPlan, err := PlanClean(ctx, cands, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defPlan.Verb != "clean" || defPlan.Host == "" {
		t.Fatalf("expected Verb=clean and Host on PlanClean, got verb=%q host=%q", defPlan.Verb, defPlan.Host)
	}
	for _, a := range defPlan.Actions {
		if strings.Contains(a.ID, "pre-ws") {
			t.Fatalf("default PlanClean must not select .pre-ws backups: %+v", a)
		}
	}

	if _, err := ExecClean(ctx, &engine.Plan{Verb: "apply", Title: "wrong"}, newMockCleanRunner()); err == nil {
		t.Errorf("expected ExecClean to reject Verb=apply")
	}

	badCmds := [][]string{
		{"rm", "-rf", "/"},
		{"bash", "-c", "echo pwn"},
		{"sudo", "rm", "-rf", "/"},
		{"go", "run", "evil.go"},
		{"docker", "run", "alpine"},
	}
	for _, cmd := range badCmds {
		r := newMockCleanRunner()
		forged := &engine.Plan{
			Verb: "clean",
			Actions: []engine.Action{
				{ID: "clean:forged", Kind: engine.ActionClean, Command: cmd},
			},
		}
		if _, err := ExecClean(ctx, forged, r); !errors.Is(err, ErrUnsafeTarget) {
			t.Errorf("expected ErrUnsafeTarget for forged command %v, got %v", cmd, err)
		}
		if len(r.calls) != 0 {
			t.Errorf("forged command %v executed", cmd)
		}
	}

	avdDir := filepath.Join(ctx.HomeDir, ".android", "avd")
	writeBytes(t, filepath.Join(avdDir, "Pixel.avd", "disk.img"), 64)
	forgedAVD := &engine.Plan{
		Verb: "clean",
		Actions: []engine.Action{
			{
				ID:       "clean:safe:disguised-avd",
				Kind:     engine.ActionClean,
				Target:   avdDir,
				Risk:     engine.RiskSafe,
				Metadata: map[string]string{"tier": "safe", "category": "cli-cache"},
			},
		},
	}
	ctx.NonInteractive = false
	ctx.Allow.Remove = false
	if _, err := ExecClean(ctx, forgedAVD, newMockCleanRunner()); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("expected forged safe AVD plan to be blocked as dangerous, got %v", err)
	}
	forgedVol := &engine.Plan{
		Verb: "clean",
		Actions: []engine.Action{
			{
				ID:       "clean:safe:disguised-vol",
				Kind:     engine.ActionClean,
				Risk:     engine.RiskSafe,
				Command:  []string{"docker", "volume", "prune", "-f"},
				Metadata: map[string]string{"tier": "safe", "category": "containers"},
			},
		},
	}
	if _, err := ExecClean(ctx, forgedVol, newMockCleanRunner()); !errors.Is(err, ErrDangerousBlocked) {
		t.Errorf("expected forged safe docker volume prune to be blocked as dangerous, got %v", err)
	}

	for _, cmd := range [][]string{{"paccache", "-rk2"}, {"sudo", "pacman", "-Sc", "--noconfirm"}} {
		for _, nonInt := range []bool{false, true} {
			ctxNoSudo := makeTestContext(t)
			ctxNoSudo.NonInteractive = nonInt
			ctxNoSudo.Allow.Sudo = false
			p := &engine.Plan{
				Verb: "clean",
				Actions: []engine.Action{
					{ID: "clean:safe:pacman-cache", Kind: engine.ActionClean, Command: cmd},
				},
			}
			r := newMockCleanRunner()
			if _, err := ExecClean(ctxNoSudo, p, r); err == nil || !strings.Contains(err.Error(), "--allow sudo") {
				t.Errorf("cmd=%v nonInt=%v: expected --allow sudo error, got %v", cmd, nonInt, err)
			}
			if len(r.calls) != 0 {
				t.Errorf("cmd=%v nonInt=%v: ran without --allow sudo", cmd, nonInt)
			}
		}
		ctxSudo := makeTestContext(t)
		ctxSudo.Allow.Sudo = true
		p := &engine.Plan{
			Verb: "clean",
			Actions: []engine.Action{
				{ID: "clean:safe:pacman-cache", Kind: engine.ActionClean, Command: cmd},
			},
		}
		r := newMockCleanRunner()
		if res, err := ExecClean(ctxSudo, p, r); err != nil || res.Applied != 1 {
			t.Errorf("cmd=%v with --allow sudo: expected success, got res=%+v err=%v", cmd, res, err)
		}
	}

	ctxCorrupt := makeTestContext(t)
	cacheDir := filepath.Join(ctxCorrupt.CacheDir, "bat")
	cacheFile := filepath.Join(cacheDir, "keep.bin")
	writeBytes(t, cacheFile, 64)
	ledgerPath := app.LedgerPath(ctxCorrupt.WSBaseDir())
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const corruptJSON = "{broken-ledger"
	if err := os.WriteFile(ledgerPath, []byte(corruptJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanPlan := &engine.Plan{
		Verb: "clean",
		Actions: []engine.Action{
			{ID: "clean:safe:cli-bat", Kind: engine.ActionClean, Target: cacheDir, Metadata: map[string]string{"category": "cli-cache"}},
		},
	}
	if _, err := ExecClean(ctxCorrupt, cleanPlan, newMockCleanRunner()); err == nil {
		t.Fatalf("expected ExecClean to fail on corrupt ledger")
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("cache file should not be removed when ledger is corrupt: %v", err)
	}
	if raw, _ := os.ReadFile(ledgerPath); string(raw) != corruptJSON {
		t.Fatalf("corrupt ledger was overwritten: got %q", string(raw))
	}
}

func TestPhase3CleanupAuditRegressions(t *testing.T) {
	ctx := makeTestContext(t)
	roots := AllowlistedRoots(ctx)

	if _, err := ValidateTarget(ctx, ctx.CacheDir, roots); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist for ~/.cache itself, got %v", err)
	}
	macCaches := filepath.Join(ctx.HomeDir, "Library", "Caches")
	writeBytes(t, filepath.Join(macCaches, "dummy"), 8)
	if _, err := ValidateTarget(ctx, macCaches, roots); !errors.Is(err, ErrOutsideAllowlist) {
		t.Errorf("expected ErrOutsideAllowlist for ~/Library/Caches itself, got %v", err)
	}

	if !isOSHomeRootAncestor("/home", "/home/alice/.cache/bat", "/home/alice/.cache", "/home/alice") {
		t.Errorf("expected /home to qualify as OS root ancestor above /home/alice")
	}
	if isOSHomeRootAncestor("/home/alice", "/home/alice/.cache/bat", "/home/alice/.cache", "/home/alice") {
		t.Errorf("HomeDir itself must never be tolerated as symlink ancestor")
	}
	if isOSHomeRootAncestor("/home/alice/.cache", "/home/alice/.cache/bat", "/home/alice/.cache", "/home/alice") {
		t.Errorf("matchedRoot itself must never be tolerated as symlink ancestor")
	}

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	oldTime := now.Add(-45 * 24 * time.Hour)

	noTagProj := filepath.Join(ctx.HomeDir, "personal", "code", "non-cargo-target")
	writeBytes(t, filepath.Join(noTagProj, "Cargo.toml"), 16)
	noTagArt := filepath.Join(noTagProj, "target", "artifact.bin")
	writeBytes(t, noTagArt, 2048)
	_ = os.Chtimes(noTagArt, oldTime, oldTime)
	_ = os.Chtimes(filepath.Join(noTagProj, "target"), oldTime, oldTime)

	nestedFreshProj := filepath.Join(ctx.HomeDir, "personal", "code", "active-deep-crate")
	writeBytes(t, filepath.Join(nestedFreshProj, "Cargo.toml"), 16)
	deepTarget := filepath.Join(nestedFreshProj, "target")
	deepTag := filepath.Join(deepTarget, "CACHEDIR.TAG")
	deepFile := filepath.Join(deepTarget, "debug", "deps", "libfoo.rlib")
	writeBytes(t, deepTag, 48)
	writeBytes(t, deepFile, 4096)
	_ = os.Chtimes(deepTag, oldTime, oldTime)
	_ = os.Chtimes(deepTarget, oldTime, oldTime)
	_ = os.Chtimes(filepath.Join(deepTarget, "debug"), oldTime, oldTime)
	_ = os.Chtimes(deepFile, now.Add(-2*time.Hour), now.Add(-2*time.Hour))

	if cand := scanStaleCargoTargets(ctx, now); cand != nil {
		t.Errorf("expected no stale cargo targets when CACHEDIR.TAG absent or nested dep recent, got %+v", cand)
	}

	danglingTarget := filepath.Join(ctx.HomeDir, ".dangling-rc")
	_ = os.Symlink(filepath.Join(ctx.RepoRoot, "config", "zsh", "missing-file"), danglingTarget)
	danglingBackup := danglingTarget + ".pre-ws"
	writeBytes(t, danglingBackup, 32)
	if _, err := VerifyPreWSBackup(ctx, danglingBackup); !errors.Is(err, ErrUnsafeTarget) {
		t.Errorf("expected VerifyPreWSBackup to reject dangling target symlink, got %v", err)
	}

	ctxDup := makeTestContext(t)
	ctxDup.CacheDir = filepath.Join(ctxDup.HomeDir, "Library", "Caches")
	writeBytes(t, filepath.Join(ctxDup.CacheDir, "Homebrew", "pkg.tar.gz"), 1024)
	writeBytes(t, filepath.Join(ctxDup.CacheDir, "go-build", "00", "obj"), 512)
	cands, err := ScanWithOptions(ctxDup, newMockCleanRunner("brew", "go"), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.ID == "safe:homebrew-cache" && c.Bytes != 1024 {
			t.Errorf("expected deduplicated homebrew bytes 1024, got %d (paths=%v)", c.Bytes, c.Paths)
		}
		if c.ID == "safe:cli-go-build" && c.Bytes != 512 {
			t.Errorf("expected deduplicated go-build bytes 512, got %d (paths=%v)", c.Bytes, c.Paths)
		}
	}
	if got := parseDockerBuilderDU("ID\tSIZE\nTotal:\t2.5GB\n"); got != 0 {
		t.Errorf("expected 0 when Reclaimable line absent, got %d", got)
	}

	for _, bad := range [][]string{
		{"sudo", "paccache", "-c", "/etc", "-rk0"},
		{"go", "clean", "-toolexec=id"},
		{"brew", "cleanup", "--force-unlisted"},
	} {
		if err := validateCleanerCommand(bad); !errors.Is(err, ErrUnsafeTarget) {
			t.Errorf("expected validateCleanerCommand to reject %v, got %v", bad, err)
		}
	}
}

func TestExecCleanPreservesConcurrentLedgerWrites(t *testing.T) {
	ctx := makeTestContext(t)
	r := newMockCleanRunner("go")
	r.handlers["go clean -cache"] = func([]string) (string, string, error) {
		err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
			l, err := app.LoadLedger(ctx.WSBaseDir())
			if err != nil {
				return err
			}
			l.UndoLog = append(l.UndoLog, app.UndoTx{ID: "tx-concurrent", Summary: "concurrent apply"})
			return app.SaveLedger(ctx.WSBaseDir(), l)
		})
		return "", "", err
	}
	plan := &engine.Plan{
		Verb:  "clean",
		Title: "Clean caches",
		Actions: []engine.Action{
			{ID: "clean:go", Kind: engine.ActionClean, Command: []string{"go", "clean", "-cache"}},
		},
	}
	if _, err := ExecClean(ctx, plan, r); err != nil {
		t.Fatalf("ExecClean: %v", err)
	}
	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(ledger.UndoLog) != 1 || ledger.UndoLog[0].ID != "tx-concurrent" {
		t.Fatalf("ExecClean clobbered concurrent ledger UndoLog: %+v", ledger.UndoLog)
	}
	if len(ledger.RunJournal) == 0 || ledger.RunJournal[len(ledger.RunJournal)-1].Command != "clean" {
		t.Fatalf("expected clean RunJournal entry, got %+v", ledger.RunJournal)
	}
}

func TestPreWSBackupProtectedWhileReferencedByUndo(t *testing.T) {
	ctx := makeTestContext(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	repoZshrc := filepath.Join(ctx.RepoRoot, "config", "zsh", "zshrc")
	repoZshenv := filepath.Join(ctx.RepoRoot, "config", "zsh", "zshenv")
	writeBytes(t, repoZshrc, 32)
	writeBytes(t, repoZshenv, 32)

	liveLink := filepath.Join(ctx.HomeDir, ".zshrc")
	if err := os.Symlink(repoZshrc, liveLink); err != nil {
		t.Fatal(err)
	}
	liveBackup := liveLink + ".pre-ws"
	writeBytes(t, liveBackup, 128)

	orphanLink := filepath.Join(ctx.HomeDir, ".zshenv")
	if err := os.Symlink(repoZshenv, orphanLink); err != nil {
		t.Fatal(err)
	}
	orphanBackup := orphanLink + ".pre-ws"
	writeBytes(t, orphanBackup, 128)

	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		t.Fatal(err)
	}
	ledger.UndoLog = append(ledger.UndoLog, app.UndoTx{
		ID:      "tx-live",
		Summary: "sync",
		Ops: []app.UndoOp{
			{
				Kind:       "restore_backup",
				Target:     liveLink,
				BackupPath: liveBackup,
			},
		},
	})
	if err := app.SaveLedger(ctx.WSBaseDir(), ledger); err != nil {
		t.Fatal(err)
	}

	if _, err := VerifyPreWSBackup(ctx, liveBackup); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("expected VerifyPreWSBackup to reject live undo backup, got %v", err)
	}

	cands, err := ScanWithOptions(ctx, newMockCleanRunner(), now)
	if err != nil {
		t.Fatalf("ScanWithOptions: %v", err)
	}
	var backupCand *Candidate
	for i := range cands {
		if cands[i].ID == "safe:pre-ws-backups" {
			backupCand = &cands[i]
			break
		}
	}
	if backupCand == nil || !slices.Equal(backupCand.Paths, []string{orphanBackup}) {
		t.Fatalf("expected only orphan backup %q, got %+v", orphanBackup, backupCand)
	}

	plan, err := PlanClean(ctx, cands, []string{"safe:pre-ws-backups"}, nil)
	if err != nil {
		t.Fatalf("PlanClean: %v", err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	doneLock := make(chan error, 1)
	go func() {
		doneLock <- app.WithLedgerLock(ctx.WSBaseDir(), func() error {
			close(locked)
			<-release
			l, lerr := app.LoadLedger(ctx.WSBaseDir())
			if lerr != nil {
				return lerr
			}
			l.UndoLog = append(l.UndoLog, app.UndoTx{
				ID:      "tx-raced",
				Summary: "concurrent apply",
				Ops: []app.UndoOp{
					{
						Kind:       "restore_backup",
						Target:     orphanLink,
						BackupPath: orphanBackup,
					},
				},
			})
			return app.SaveLedger(ctx.WSBaseDir(), l)
		})
	}()

	<-locked
	execDone := make(chan error, 1)
	go func() {
		_, err := ExecClean(ctx, plan, newMockCleanRunner())
		execDone <- err
	}()

	time.Sleep(50 * time.Millisecond)
	close(release)
	if err := <-doneLock; err != nil {
		t.Fatalf("concurrent ledger writer: %v", err)
	}
	if err := <-execDone; !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("expected ExecClean to reject backup newly referenced under lock, got %v", err)
	}
	if _, err := os.Stat(orphanBackup); err != nil {
		t.Fatalf("expected newly referenced backup %s to remain on disk: %v", orphanBackup, err)
	}
	if _, err := os.Stat(liveBackup); err != nil {
		t.Fatalf("expected live backup %s to remain on disk: %v", liveBackup, err)
	}
}
