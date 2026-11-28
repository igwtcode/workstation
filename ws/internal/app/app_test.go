package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseAllow(t *testing.T) {
	got, err := ParseAllowStrict("replace, sudo")
	if err != nil || !got.Replace || !got.Sudo || got.Remove || got.Script {
		t.Fatalf("unexpected allow parse: %+v err=%v", got, err)
	}
	all, err := ParseAllowStrict("all")
	if err != nil || !all.Replace || !all.Sudo || !all.Remove || !all.Script || !all.Yes {
		t.Fatalf("expected all scopes enabled with Yes: %+v err=%v", all, err)
	}
	listed, err := ParseAllowStrict("replace,sudo,remove,script")
	if err != nil || !listed.Replace || !listed.Sudo || !listed.Remove || !listed.Script || listed.Yes {
		t.Fatalf("expected listing every scope to grant them without Yes: %+v err=%v", listed, err)
	}
	if got.String() != "replace,sudo" {
		t.Fatalf("unexpected allow string: %q", got.String())
	}
	if _, err := ParseAllowStrict("replce"); err == nil {
		t.Fatalf("expected ParseAllowStrict to reject unknown scope")
	}
	if !got.Satisfies("replace,sudo") || got.Satisfies("replace,remove") || got.Satisfies("unknown") {
		t.Fatalf("unexpected Satisfies results on %+v", got)
	}
}

func TestDetectHostFrom(t *testing.T) {
	tmp := t.TempDir()
	ubuntuRel := filepath.Join(tmp, "os-release")
	if err := os.WriteFile(ubuntuRel, []byte("ID=ubuntu\nID_LIKE=debian\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := DetectHostFromEnv(ubuntuRel, "linux", "arm64", true, func(c string) bool {
		return c == "apt-get" || c == "mise"
	})
	if h.Distro != "ubuntu" || !h.Headless || h.DefaultPreset != "ubuntu-runner" {
		t.Fatalf("unexpected ubuntu host detection: %+v", h)
	}

	cachyRel := filepath.Join(tmp, "cachy-release")
	if err := os.WriteFile(cachyRel, []byte("ID=cachyos\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := DetectHostFromEnv(cachyRel, "linux", "amd64", true, func(cmd string) bool {
		return cmd == "paru" || cmd == "pacman"
	})
	if c.DefaultPreset != "cachyos-desktop" {
		t.Fatalf("expected cachyos-desktop, got %+v", c)
	}
}

func TestLoadMiseLocalEnvAndLedger(t *testing.T) {
	tmp := t.TempDir()
	misePath := filepath.Join(tmp, "mise.local.toml")
	content := `
[env]
WS_OP_VAULT = "Private"
WS_ZONES = """
personal|Sample User|user@example.invalid
"""
`
	if err := os.WriteFile(misePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env, err := LoadMiseLocalEnv(misePath)
	if err != nil {
		t.Fatal(err)
	}
	if env["WS_OP_VAULT"] != "Private" {
		t.Fatalf("unexpected vault: %q", env["WS_OP_VAULT"])
	}
	rows := ParsePipeLines(env["WS_ZONES"])
	if len(rows) != 1 || rows[0][0] != "personal" {
		t.Fatalf("unexpected parsed rows: %+v", rows)
	}

	stateDir := filepath.Join(tmp, "state")
	l, err := LoadLedger(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	l.ActiveTheme = "gruvbox-dark"
	l.ActivePreset = "mac-workstation"
	if err := SaveLedger(stateDir, l); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadLedger(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ActiveTheme != "gruvbox-dark" || ReadLegacyTheme(stateDir) != "gruvbox-dark" {
		t.Fatalf("theme did not persist: %+v", reloaded)
	}
}

func makeValidRepoFixture(t *testing.T, dir string) {
	t.Helper()
	for _, rel := range []string{
		filepath.Join("themes", "registry"),
		filepath.Join("links", "common.links"),
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNewContextStrictRepoAndAbsoluteXDG(t *testing.T) {
	tmp := t.TempDir()
	validRepo := filepath.Join(tmp, "valid-repo")
	invalidRepo := filepath.Join(tmp, "not-a-repo")
	home := filepath.Join(tmp, "home")
	makeValidRepoFixture(t, validRepo)
	if err := os.MkdirAll(invalidRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_DIR", validRepo)
	if _, err := NewContext(Options{RepoRoot: invalidRepo, HomeDir: home}); err == nil {
		t.Fatalf("expected error for invalid explicit RepoRoot")
	}
	if got := FindRepoRoot(invalidRepo, home); got != "" {
		t.Fatalf("FindRepoRoot(invalid) = %q, want empty", got)
	}

	ctx, err := NewContext(Options{RepoRoot: validRepo, HomeDir: home})
	if err != nil {
		t.Fatalf("NewContext(validRepo) failed: %v", err)
	}
	if ctx.RepoRoot != validRepo {
		t.Fatalf("ctx.RepoRoot = %q, want %q", ctx.RepoRoot, validRepo)
	}

	absState := filepath.Join(tmp, "custom-state")
	t.Setenv("WS_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	t.Setenv("XDG_DATA_HOME", "../relative/data")
	t.Setenv("XDG_STATE_HOME", absState)
	t.Setenv("XDG_CACHE_HOME", "relcache")
	ctxXDG, err := NewContext(Options{RepoRoot: validRepo})
	if err != nil {
		t.Fatalf("NewContext(XDG) failed: %v", err)
	}
	if !filepath.IsAbs(ctxXDG.ConfigDir) || strings.Contains(ctxXDG.ConfigDir, "relative/config") {
		t.Fatalf("expected relative XDG_CONFIG_HOME ignored, got %q", ctxXDG.ConfigDir)
	}
	if !filepath.IsAbs(ctxXDG.DataDir) || strings.Contains(ctxXDG.DataDir, "relative/data") {
		t.Fatalf("expected relative XDG_DATA_HOME ignored, got %q", ctxXDG.DataDir)
	}
	if ctxXDG.StateDir != absState {
		t.Fatalf("expected absolute XDG_STATE_HOME %q, got %q", absState, ctxXDG.StateDir)
	}
	if !filepath.IsAbs(ctxXDG.CacheDir) || strings.Contains(ctxXDG.CacheDir, "relcache") {
		t.Fatalf("expected relative XDG_CACHE_HOME ignored, got %q", ctxXDG.CacheDir)
	}
}

func TestPinnedMachineEnvStripsZoneKeys(t *testing.T) {
	base := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/tmp/home",
		"WS_ZONE=work",
		"WS_CODE_ROOT=/tmp/home/work/code",
		"GH_CONFIG_DIR=/tmp/home/.config/gh-work",
		"CLAUDE_CONFIG_DIR=/tmp/home/.config/claude-work",
		"CODEX_HOME=/tmp/home/.codex-work",
		"AWS_CONFIG_FILE=/tmp/home/.aws/work-config",
		"AWS_SHARED_CREDENTIALS_FILE=/tmp/home/.aws/work-creds",
		"AWS_PROFILE=work-prod",
		"KUBECONFIG=/tmp/home/.kube/work",
		"TERM=xterm-256color",
	}

	pinned := PinnedMachineEnv(base)
	for _, stripped := range []string{
		"WS_ZONE=",
		"WS_CODE_ROOT=",
		"GH_CONFIG_DIR=",
		"CLAUDE_CONFIG_DIR=",
		"CODEX_HOME=",
		"AWS_CONFIG_FILE=",
		"AWS_SHARED_CREDENTIALS_FILE=",
		"AWS_PROFILE=",
		"KUBECONFIG=",
	} {
		for _, kv := range pinned {
			if strings.HasPrefix(kv, stripped) {
				t.Errorf("PinnedMachineEnv leaked zone key %q", kv)
			}
		}
	}
	for _, kept := range []string{"PATH=/usr/bin:/bin", "HOME=/tmp/home", "TERM=xterm-256color"} {
		if !slices.Contains(pinned, kept) {
			t.Errorf("PinnedMachineEnv dropped non-zone env %q", kept)
		}
	}
}

func TestLedgerAdoptsNewerLegacyThemeAndInlineMiseEnv(t *testing.T) {
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	l := &Ledger{
		ActiveTheme:    "gruvbox-dark",
		ActivePreset:   "mac-workstation",
		RenderedHashes: map[string]string{},
		ManagedLinks:   map[string]string{},
		ManagedTools:   map[string]ManagedToolRecord{},
	}
	if err := SaveLedger(stateDir, l); err != nil {
		t.Fatal(err)
	}
	legacyPath := LegacyThemePath(stateDir)
	if err := os.WriteFile(legacyPath, []byte("nord-dark\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(legacyPath, future, future); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadLedger(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ActiveTheme != "nord-dark" {
		t.Fatalf("expected newer legacy theme nord-dark, got %q", reloaded.ActiveTheme)
	}

	misePath := filepath.Join(tmp, "mise.local.toml")
	if err := os.WriteFile(misePath, []byte("[env]\nSECRET = { value = \"s3cr3t\", redact = true }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envMap, err := LoadMiseLocalEnv(misePath)
	if err != nil {
		t.Fatal(err)
	}
	if envMap["SECRET"] != "s3cr3t" {
		t.Fatalf("expected inline table env value s3cr3t, got %q", envMap["SECRET"])
	}
}

func TestLookupEnvAndLookupEnvExact(t *testing.T) {
	ctx := &Context{
		LocalEnv: map[string]string{
			"WS_ZONES":         "personal|Demo|demo@example.com",
			"WS_EXCLUDE_PATHS": "foo,bar",
		},
	}
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_EXCLUDE_PATHS", "")

	if got := ctx.LookupEnv("WS_ZONES"); got != "personal|Demo|demo@example.com" {
		t.Fatalf("LookupEnv fell back mismatch: got %q", got)
	}
	if v, ok := ctx.LookupEnvExact("WS_EXCLUDE_PATHS"); !ok || v != "" {
		t.Fatalf("LookupEnvExact empty override = (%q, %v), want (\"\", true)", v, ok)
	}
}

func TestWSHomeAndLegacyMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("WS_HOME", "")

	tmp := t.TempDir()
	validRepo := filepath.Join(tmp, "repo")
	home := filepath.Join(tmp, "home")
	makeValidRepoFixture(t, validRepo)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("WS_HOME", "relative-ws")
	ctxDef, err := NewContext(Options{RepoRoot: validRepo})
	if err != nil {
		t.Fatalf("NewContext(default): %v", err)
	}
	wantDef := filepath.Join(home, ".ws")
	if ctxDef.WSHome != wantDef || ctxDef.WSBaseDir() != wantDef {
		t.Fatalf("WSHome = %q (base=%q), want %q", ctxDef.WSHome, ctxDef.WSBaseDir(), wantDef)
	}

	customWS := filepath.Join(tmp, "custom-ws-home")
	t.Setenv("WS_HOME", customWS)
	ctxCustom, err := NewContext(Options{RepoRoot: validRepo})
	if err != nil {
		t.Fatalf("NewContext(custom WS_HOME): %v", err)
	}
	if ctxCustom.WSHome != customWS || ctxCustom.WSDBPath() != filepath.Join(customWS, "ws.db") {
		t.Fatalf("custom WS_HOME = %q db=%q, want %q", ctxCustom.WSHome, ctxCustom.WSDBPath(), customWS)
	}

	t.Setenv("WS_HOME", "")
	migHome := filepath.Join(tmp, "mig-home")
	legacyWS := filepath.Join(migHome, ".local", "state", "ws")
	legacyCfgWS := filepath.Join(migHome, ".config", "ws")
	for _, d := range []string{
		filepath.Join(legacyWS, "backups"),
		filepath.Join(legacyWS, "exports"),
		filepath.Join(legacyCfgWS, "claude"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite := func(p, content string, perm os.FileMode) {
		if err := os.WriteFile(p, []byte(content), perm); err != nil {
			t.Fatal(err)
		}
	}
	seedSQLiteDB(t, filepath.Join(legacyWS, "ws.db"), []string{"legacy-row"})
	mustWrite(filepath.Join(legacyWS, "ledger.json"), `{"activeTheme":"gruvbox-dark"}`, 0o600)
	mustWrite(filepath.Join(legacyWS, "theme"), "gruvbox-dark\n", 0o644)
	mustWrite(filepath.Join(legacyWS, "digest.json"), `{"items":[]}`, 0o600)
	mustWrite(filepath.Join(legacyWS, "digest.err.log"), "ok\n", 0o644)
	mustWrite(filepath.Join(legacyWS, "backups", "ws-backup-latest.enc"), "enc-bak", 0o600)
	mustWrite(filepath.Join(legacyWS, "exports", "mise-1.json"), "{}\n", 0o600)
	mustWrite(filepath.Join(legacyWS, "ledger.lock"), "", 0o600)
	mustWrite(filepath.Join(legacyCfgWS, "db.key"), "0123456789abcdef\n", 0o600)
	mustWrite(filepath.Join(legacyCfgWS, "claude", "statusline.sh"), "#!/bin/sh\n", 0o755)

	if _, err := NewContext(Options{RepoRoot: validRepo, HomeDir: migHome, DryRun: true}); err != nil {
		t.Fatalf("NewContext(DryRun): %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacyWS, "ws.db")); err != nil {
		t.Fatalf("expected DryRun to leave legacy ws.db untouched: %v", err)
	}

	migCtx, err := NewContext(Options{RepoRoot: validRepo, HomeDir: migHome})
	if err != nil {
		t.Fatalf("NewContext(migrate): %v", err)
	}
	for _, rel := range []string{
		"ws.db",
		"db.key",
		"ledger.json",
		"theme",
		filepath.Join("cache", "digest.json"),
		filepath.Join("logs", "digest.err.log"),
		filepath.Join("backups", "ws-backup-latest.enc"),
		filepath.Join("exports", "mise-1.json"),
	} {
		if _, err := os.Stat(filepath.Join(migCtx.WSHome, rel)); err != nil {
			t.Fatalf("expected migrated file %s in ~/.ws: %v", rel, err)
		}
	}
	if fi, err := os.Stat(migCtx.WSKeyPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected migrated db.key 0600, err=%v mode=%v", err, fi.Mode().Perm())
	}
	if _, err := os.Stat(legacyWS); !os.IsNotExist(err) {
		t.Fatalf("expected empty legacy state dir %s removed, err=%v", legacyWS, err)
	}
	if _, err := os.Stat(filepath.Join(legacyCfgWS, "claude", "statusline.sh")); err != nil {
		t.Fatalf("expected ~/.config/ws/claude preserved: %v", err)
	}

	if err := os.MkdirAll(legacyWS, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(legacyWS, "theme"), "old-theme\n", 0o644)
	mustWrite(migCtx.WSThemePath(), "new-theme\n", 0o644)
	if _, err := NewContext(Options{RepoRoot: validRepo, HomeDir: migHome}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(migCtx.WSThemePath()); err != nil || strings.TrimSpace(string(got)) != "new-theme" {
		t.Fatalf("expected existing ~/.ws/theme preserved, got %q err=%v", string(got), err)
	}

	externalState := filepath.Join(tmp, "external-state")
	if err := os.MkdirAll(filepath.Join(externalState, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(externalState, "ws", "ws.db"), "keep-me", 0o600)
	isolatedHome := filepath.Join(tmp, "isolated-home")
	if err := os.MkdirAll(isolatedHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WS_HOME", "")
	t.Setenv("HOME", isolatedHome)
	t.Setenv("XDG_STATE_HOME", externalState)
	if _, err := NewContext(Options{RepoRoot: validRepo}); err != nil {
		t.Fatalf("NewContext(external state): %v", err)
	}
	if _, err := os.Stat(filepath.Join(externalState, "ws", "ws.db")); err != nil {
		t.Fatalf("expected external XDG_STATE_HOME ws.db untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(isolatedHome, ".ws", "ws.db")); !os.IsNotExist(err) {
		t.Fatalf("expected external XDG_STATE_HOME not migrated into %s/.ws, err=%v", isolatedHome, err)
	}
	t.Setenv("XDG_STATE_HOME", "")

	customHome := filepath.Join(tmp, "custom-home")
	if err := os.MkdirAll(filepath.Join(customHome, ".local", "state", "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(customHome, ".local", "state", "ws", "ws.db"), "keep-legacy", 0o600)
	altWS := filepath.Join(tmp, "alt-ws")
	if _, err := NewContext(Options{RepoRoot: validRepo, HomeDir: customHome, WSHome: altWS}); err != nil {
		t.Fatalf("NewContext(custom WSHome): %v", err)
	}
	if _, err := os.Stat(filepath.Join(customHome, ".local", "state", "ws", "ws.db")); err != nil {
		t.Fatalf("expected custom WSHome to leave legacy ws.db untouched: %v", err)
	}
}

func TestFindRepoRootFromHomeSymlinkZoneAndConfig(t *testing.T) {
	tmp := t.TempDir()
	if realTmp, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = realTmp
	}
	unrelatedDir := filepath.Join(tmp, "unrelated")
	if err := os.MkdirAll(unrelatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(unrelatedDir)
	t.Setenv("WS_DIR", "")
	t.Setenv("WS_REPO", "")
	t.Setenv("MISE_PROJECT_ROOT", "")
	t.Setenv("WS_CODE_ROOT", "")
	t.Setenv("WS_ZONES", "")

	homeSym := filepath.Join(tmp, "home-symlink")
	repoSym := filepath.Join(tmp, "custom-checkout")
	makeValidRepoFixture(t, repoSym)
	zshTarget := filepath.Join(repoSym, "config", "zsh", "zshrc")
	if err := os.MkdirAll(filepath.Dir(zshTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zshTarget, []byte("# zshrc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(homeSym, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(zshTarget, filepath.Join(homeSym, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	if got := FindRepoRoot("", homeSym); got != repoSym {
		t.Fatalf("FindRepoRoot(symlink ~/.zshrc) = %q, want %q", got, repoSym)
	}

	homeZone := filepath.Join(tmp, "home-zone")
	repoZone := filepath.Join(homeZone, "personal", "code", "someorg", "workstation")
	makeValidRepoFixture(t, repoZone)
	if err := os.MkdirAll(homeZone, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindRepoRoot("", homeZone); got != repoZone {
		t.Fatalf("FindRepoRoot(personal/code/*/workstation) = %q, want %q", got, repoZone)
	}

	repoCfg := filepath.Join(tmp, "config-checkout")
	makeValidRepoFixture(t, repoCfg)
	if err := os.WriteFile(filepath.Join(repoCfg, "mise.local.toml"), []byte("[env]\nWS_CUSTOM_MARKER = \"from-cfg\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeZone, ".ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgToml := "[workspace]\nrepo_root = '" + repoCfg + "'\n"
	if err := os.WriteFile(filepath.Join(homeZone, ".ws", "config.toml"), []byte(cfgToml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, err := NewContext(Options{HomeDir: homeZone})
	if err != nil {
		t.Fatalf("NewContext(workspace.repo_root): %v", err)
	}
	if ctx.RepoRoot != repoCfg {
		t.Fatalf("ctx.RepoRoot = %q, want %q", ctx.RepoRoot, repoCfg)
	}
	if got := ctx.LookupEnv("WS_CUSTOM_MARKER"); got != "from-cfg" {
		t.Fatalf("expected mise.local.toml loaded from workspace.repo_root, got %q", got)
	}
}

func seedSQLiteDB(t *testing.T, dbPath string, rows []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, "_pragma=busy_timeout(5000)"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, val TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO items(val) VALUES (?)`, r); err != nil {
			t.Fatal(err)
		}
	}
}

func readSQLiteItems(t *testing.T, dbPath string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, "_pragma=busy_timeout(5000)&mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT val FROM items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func seedWALUncheckpointedCopy(t *testing.T, dstDB string, rows []string) {
	t.Helper()
	srcDir := t.TempDir()
	srcDB := filepath.Join(srcDir, "ws.db")
	db, err := sql.Open("sqlite", sqliteFileDSN(srcDB, "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, val TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO items(val) VALUES (?)`, r); err != nil {
			t.Fatal(err)
		}
	}
	walInfo, err := os.Stat(srcDB + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("expected non-empty WAL file before copy, err=%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dstDB), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, rerr := os.ReadFile(srcDB + suffix)
		if rerr != nil {
			if os.IsNotExist(rerr) && suffix == "-shm" {
				continue
			}
			t.Fatal(rerr)
		}
		if err := os.WriteFile(dstDB+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateLegacyWSHomeWALInterruptedAndErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("WS_HOME", "")

	tmp := t.TempDir()
	validRepo := filepath.Join(tmp, "repo")
	makeValidRepoFixture(t, validRepo)

	walHome := filepath.Join(tmp, "wal-home")
	legacyWS := filepath.Join(walHome, ".local", "state", "ws")
	wsHome := filepath.Join(walHome, ".ws")
	wantRows := []string{"alpha", "beta", "gamma"}
	seedWALUncheckpointedCopy(t, filepath.Join(legacyWS, "ws.db"), wantRows)

	if err := os.MkdirAll(wsHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsHome, "ws.db.migrate.tmp"), []byte("partial-garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsHome, "ws.db.migrate.cleanup"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := &Context{
		HomeDir:   walHome,
		WSHome:    wsHome,
		StateDir:  filepath.Join(walHome, ".local", "state"),
		ConfigDir: filepath.Join(walHome, ".config"),
	}

	const workers = 4
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = MigrateLegacyWSHome(ctx)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent MigrateLegacyWSHome[%d] failed: %v", i, err)
		}
	}
	if got := readSQLiteItems(t, ctx.WSDBPath()); !slices.Equal(got, wantRows) {
		t.Fatalf("migrated WAL rows = %v, want %v", got, wantRows)
	}
	for _, leftover := range []string{
		filepath.Join(legacyWS, "ws.db"),
		filepath.Join(legacyWS, "ws.db-wal"),
		filepath.Join(legacyWS, "ws.db-shm"),
		ctx.WSDBPath() + ".migrate.tmp",
		ctx.WSDBPath() + ".migrate.cleanup",
	} {
		if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
			t.Errorf("expected leftover %s removed, err=%v", leftover, err)
		}
	}

	resumeHome := filepath.Join(tmp, "resume-home")
	resumeLegacy := filepath.Join(resumeHome, ".local", "state", "ws")
	resumeWSHome := filepath.Join(resumeHome, ".ws")
	seedSQLiteDB(t, filepath.Join(resumeWSHome, "ws.db"), wantRows)
	seedWALUncheckpointedCopy(t, filepath.Join(resumeLegacy, "ws.db"), []string{"stale-old"})
	if err := os.WriteFile(filepath.Join(resumeWSHome, "ws.db.migrate.cleanup"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resumeCtx := &Context{
		HomeDir:   resumeHome,
		WSHome:    resumeWSHome,
		StateDir:  filepath.Join(resumeHome, ".local", "state"),
		ConfigDir: filepath.Join(resumeHome, ".config"),
	}
	for attempt := range 2 {
		if err := MigrateLegacyWSHome(resumeCtx); err != nil {
			t.Fatalf("resumed MigrateLegacyWSHome attempt %d: %v", attempt, err)
		}
	}
	if got := readSQLiteItems(t, resumeCtx.WSDBPath()); !slices.Equal(got, wantRows) {
		t.Fatalf("resumed migration altered dst rows = %v, want %v", got, wantRows)
	}
	if _, err := os.Lstat(resumeLegacy); !os.IsNotExist(err) {
		t.Fatalf("expected legacy dir removed after resumed cleanup, err=%v", err)
	}

	corruptHome := filepath.Join(tmp, "corrupt-home")
	corruptLegacy := filepath.Join(corruptHome, ".local", "state", "ws")
	if err := os.MkdirAll(corruptLegacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptLegacy, "ws.db"), []byte("not-a-sqlite-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewContext(Options{RepoRoot: validRepo, HomeDir: corruptHome}); err == nil {
		t.Fatalf("expected NewContext to propagate legacy ws.db migration error")
	}
	if _, err := os.Stat(filepath.Join(corruptLegacy, "ws.db")); err != nil {
		t.Fatalf("expected corrupt legacy ws.db preserved on failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(corruptHome, ".ws", "ws.db")); !os.IsNotExist(err) {
		t.Fatalf("expected no dst ws.db after failed migration, err=%v", err)
	}
}

func TestRepoRootLinkProbesMatchLinksManifest(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", ".."))
	commonLinksPath := filepath.Join(repoRoot, "links", "common.links")
	raw, err := os.ReadFile(commonLinksPath)
	if err != nil {
		t.Fatalf("read %s: %v", commonLinksPath, err)
	}

	shippedTargets := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		target := strings.TrimPrefix(fields[1], "~/")
		shippedTargets[filepath.Clean(target)] = true
	}

	for _, stale := range []string{
		".gitconfig",
		".tmux.conf",
		filepath.Join(".config", "mise", "config.toml"),
		filepath.Join(".config", "starship.toml"),
	} {
		if slices.Contains(repoRootLinkProbes, stale) {
			t.Fatalf("repoRootLinkProbes still contains unshipped symlink %q", stale)
		}
	}
	for _, probe := range repoRootLinkProbes {
		if !shippedTargets[filepath.Clean(probe)] {
			t.Errorf("repoRootLinkProbes entry %q is not in links/common.links", probe)
		}
	}

	tmp := t.TempDir()
	if realTmp, rerr := filepath.EvalSymlinks(tmp); rerr == nil {
		tmp = realTmp
	}
	unrelated := filepath.Join(tmp, "unrelated")
	if err := os.MkdirAll(unrelated, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(unrelated)
	t.Setenv("WS_DIR", "")
	t.Setenv("WS_REPO", "")
	t.Setenv("MISE_PROJECT_ROOT", "")
	t.Setenv("WS_CODE_ROOT", "")
	t.Setenv("WS_ZONES", "")

	repo := filepath.Join(tmp, "custom-repo")
	makeValidRepoFixture(t, repo)
	tmuxDir := filepath.Join(repo, "config", "tmux")
	if err := os.MkdirAll(tmuxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tmuxDir, filepath.Join(home, ".config", "tmux")); err != nil {
		t.Fatal(err)
	}
	if got := FindRepoRoot("", home); got != repo {
		t.Fatalf("FindRepoRoot via ~/.config/tmux = %q, want %q", got, repo)
	}
}

func TestProbeEnvDisablesEveryMiseAutoInstallSwitch(t *testing.T) {
	got := ProbeEnv([]string{"PATH=/bin", "MISE_AUTO_INSTALL=1", "MISE_NOT_FOUND_AUTO_INSTALL=1", "MISE_EXEC_AUTO_INSTALL=true", "MISE_OTHER=1"})
	want := []string{"PATH=/bin", "MISE_OTHER=1", "MISE_AUTO_INSTALL=0", "MISE_NOT_FOUND_AUTO_INSTALL=0", "MISE_EXEC_AUTO_INSTALL=0"}
	if !slices.Equal(got, want) {
		t.Fatalf("ProbeEnv = %v, want %v", got, want)
	}
}
