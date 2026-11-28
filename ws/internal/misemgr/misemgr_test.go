package misemgr

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/projects"
)

type fakeRunner struct {
	handlers map[string]func(dir string) (string, string, error)
}

func (f *fakeRunner) Run(cmd []string, _ map[string]string, workDir string) (string, string, error) {
	key := strings.Join(cmd, " ")
	if fn, ok := f.handlers[key]; ok {
		return fn(workDir)
	}
	return "", "", nil
}

func TestScanDiscoversGlobalZoneProjectAndOutdatedToolsWithoutLeakingSecrets(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	cfgDir := filepath.Join(home, ".config")
	wsHome := filepath.Join(home, ".ws")
	repoDir := filepath.Join(home, "work", "code", "workstation-repo")
	ghProj := filepath.Join(home, "personal", "code", "app-web")
	ccProj := filepath.Join(home, "work", "code", "billing-svc")
	zoneDir := filepath.Join(home, "work")

	for _, d := range []string{
		filepath.Join(cfgDir, "mise"),
		wsHome,
		repoDir,
		filepath.Join(ghProj, ".mise"),
		ccProj,
		zoneDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	secretSentinel := "super-secret-value-never-export"
	if err := os.WriteFile(filepath.Join(cfgDir, "mise", "config.toml"), []byte("[tools]\ngo = \"1.27.4\"\nnode = \"22.0.0\"\n[env]\nSHARED_KEY = \""+secretSentinel+"\"\n[tasks.check]\nrun = \"echo ok\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "mise.local.toml"), []byte("[env]\nZONE_TOKEN = \""+secretSentinel+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghProj, ".mise", "config.toml"), []byte("[tools]\nnode = \"22.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghProj, "mise.local.toml"), []byte("[env]\nLOCAL_DEV_SECRET = \""+secretSentinel+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccProj, ".tool-versions"), []byte("# runtime versions\nterraform 1.9.8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &app.Context{
		RepoRoot:  repoDir,
		HomeDir:   home,
		WSHome:    wsHome,
		ConfigDir: cfgDir,
		StateDir:  filepath.Join(home, ".local", "state"),
		Host:      app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64"},
		LocalEnv:  map[string]string{"WS_ZONES": "work|Example User|work@example.invalid"},
	}
	repos := []projects.RepoRecord{
		{Name: "app-web", Zone: "personal", Forge: "github", RemoteURL: "https://token@github.com/example/app-web.git", LocalPath: ghProj, Branch: "main", Dirty: true},
		{Name: "billing-svc", Zone: "work", Forge: "codecommit", RemoteURL: "codecommit::us-east-1://prof@billing-svc", LocalPath: ccProj, Branch: "release"},
	}

	runner := &fakeRunner{
		handlers: map[string]func(dir string) (string, string, error){
			"mise --version": func(string) (string, string, error) { return "2026.11.14 macos-arm64", "", nil },
			"mise ls --json": func(string) (string, string, error) {
				return `{
  "go": [{"version": "1.27.4", "installed": true, "active": true, "install_path": "/mise/go/1.27.4"}],
  "node": [{"version": "22.0.0", "installed": true, "active": true, "install_path": "/mise/node/22.0.0"}],
  "terraform": [{"version": "1.9.8", "installed": true, "active": false, "install_path": "/mise/tf/1.9.8"}]
}`, "", nil
			},
			"mise outdated --json": func(dir string) (string, string, error) {
				if filepath.Clean(dir) == filepath.Clean(ghProj) {
					return `{"node": {"name": "node", "requested": "22.0.0", "current": "22.0.0", "latest": "24.1.0"}}`, "", nil
				}
				return `{}`, "", nil
			},
		},
	}

	inv, err := ScanWithRunner(ctx, runner, repos, "", true)
	if err != nil {
		t.Fatalf("ScanWithRunner failed: %v", err)
	}
	if inv.MiseVersion != "2026.11.14" {
		t.Fatalf("MiseVersion = %q, want 2026.11.14", inv.MiseVersion)
	}
	if len(inv.Configs) < 5 {
		t.Fatalf("expected >= 5 configs (global, zone, gh .mise/config.toml, gh mise.local.toml, cc .tool-versions), got %d", len(inv.Configs))
	}
	if inv.OutdatedCount == 0 {
		t.Fatalf("expected OutdatedCount >= 1 for node in ghProj")
	}

	var sawGH, sawCC bool
	for _, cfg := range inv.Configs {
		if filepath.Clean(cfg.Path) == filepath.Clean(filepath.Join(ghProj, "mise.local.toml")) {
			if !cfg.LocalOverride || !slices.Equal(cfg.EnvKeys, []string{"LOCAL_DEV_SECRET"}) {
				t.Fatalf("unexpected local override metadata: %+v", cfg)
			}
		}
		if filepath.Clean(cfg.Path) == filepath.Clean(filepath.Join(ghProj, ".mise", "config.toml")) {
			sawGH = true
			if cfg.Forge != "github" || cfg.GitHubSlug != "example/app-web" || strings.Contains(cfg.RemoteURL, "token@") || cfg.OutdatedCount != 1 {
				t.Fatalf("unexpected gh config metadata: %+v", cfg)
			}
		}
		if filepath.Clean(cfg.Path) == filepath.Clean(filepath.Join(ccProj, ".tool-versions")) {
			sawCC = true
			if cfg.Forge != "codecommit" || cfg.GitHubSlug != "" || len(cfg.Tools) != 1 || cfg.Tools[0].Name != "terraform" {
				t.Fatalf("unexpected codecommit .tool-versions metadata: %+v", cfg)
			}
		}
	}
	if !sawGH || !sawCC {
		t.Fatalf("missing expected gh (%v) or cc (%v) config in %+v", sawGH, sawCC, inv.Configs)
	}

	if _, err := ValidateEditableMisePath(ctx, filepath.Join(ghProj, "mise.local.toml")); err != nil {
		t.Fatalf("ValidateEditableMisePath rejected valid mise.local.toml: %v", err)
	}
	if _, err := ValidateEditableMisePath(ctx, "/etc/passwd"); err == nil {
		t.Fatalf("ValidateEditableMisePath allowed non-mise /etc/passwd")
	}
}

func TestScanDiscoversZonesWithoutWSZones(t *testing.T) {
	t.Setenv("WS_ZONES", "")

	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config")
	zoneDir := filepath.Join(home, "work")
	projDir := filepath.Join(zoneDir, "code", "svc-api")

	for _, d := range []string{
		filepath.Join(cfgDir, "mise"),
		zoneDir,
		projDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(cfgDir, "mise", "config.toml"), []byte("[tools]\ngo = \"1.24\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zoneDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "mise.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &app.Context{
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: cfgDir,
		StateDir:  filepath.Join(home, ".local", "state"),
		LocalEnv:  map[string]string{},
	}

	runner := &fakeRunner{
		handlers: map[string]func(dir string) (string, string, error){
			"mise --version": func(string) (string, string, error) { return "2026.11.14", "", nil },
			"mise ls --json": func(string) (string, string, error) { return `{}`, "", nil },
		},
	}

	inv, err := ScanWithRunner(ctx, runner, nil, "", false)
	if err != nil {
		t.Fatalf("ScanWithRunner: %v", err)
	}

	var sawGlobal, sawZone, sawProj bool
	for _, cfg := range inv.Configs {
		switch filepath.Clean(cfg.Path) {
		case filepath.Clean(filepath.Join(cfgDir, "mise", "config.toml")):
			sawGlobal = true
		case filepath.Clean(filepath.Join(zoneDir, "mise.toml")):
			if cfg.Scope == "zone" && cfg.Zone == "work" {
				sawZone = true
			}
		case filepath.Clean(filepath.Join(projDir, "mise.toml")):
			if cfg.Scope == "project" && cfg.Zone == "work" && cfg.ProjectName == "svc-api" {
				sawProj = true
			}
		}
	}
	if !sawGlobal || !sawZone || !sawProj {
		t.Fatalf("expected global (%v), zone (%v), and project (%v) configs without WS_ZONES, got %+v", sawGlobal, sawZone, sawProj, inv.Configs)
	}
}
