package tui

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image/color"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

var _ Backend = (*mockBackend)(nil)

type mockBackend struct {
	mu sync.Mutex

	host          HostBadge
	envVars       []EnvVarRow
	envFiles      []EnvFileRow
	envDetails    map[string]EnvDetail
	envSecrets    map[string]string
	repos         []RepoRow
	ghItems       []GHItemRow
	ghRepoItems   map[string][]GHItemRow
	ghDetails     map[string]GHItemDetail
	fleetNodes    []FleetNodeRow
	miseInv       MiseInventoryView
	presets       []PresetSummary
	presetTools   map[string][]PresetToolRow
	pkgOutdated   map[string]PackageOutdatedInfo
	pkgDetails    map[string]PackageDetail
	themeIDs      []string
	palettes      map[string]config.Palette
	activeTheme   string
	templates     []TemplateStatusRow
	cleanup       []CleanItem
	historyRows   []HistoryRow
	historySrc    string
	doctor        DoctorSummary
	dbStatus      DBStatusSummary
	vaultInv      VaultInventoryView
	fileDiff      FleetFileDiffView
	fileTextDiffs map[string]string
	settingsView  SettingsView
	watchChanged  []string

	envZones           []string
	repoZones          []string
	ghZones            []string
	ghCachedCalls      []string
	ghRepoCalls        []string
	ghRepoZones        []string
	ghRepoRefreshes    []bool
	ghDetailCalls      []string
	ghDetailRefresh    []bool
	rotatePreviews     []EnvRotateReq
	rotateExecs        []EnvRotateReq
	pulledPaths        [][]string
	clonedRepos        []string
	fleetSyncCalls     []FleetSyncReq
	fleetConnects      []string
	pkgActions         []string
	pkgActionExecCalls []string
	wsCmdCalls         [][]string
	appliedPlans       int
	appliedThemes      []string
	appliedThemeApps   [][2]string
	applyThemeErr      error
	cleanupCalls       int
	cleanupExecCalls   int
	lastCleanIDs       []string
	lastExecCleanIDs   []string
	lastCleanDry       bool
	editedApps         []string
	miseZones          []string
	editedMiseFiles    []string
	upgradedMiseTools  []MiseToolRow
	backupCalls        int
	undoCalls          int
	vaultActions       []string
	fileDiffCalls      []string
	fileTextCalls      []string
	fileSyncCalls      []FleetFileSyncReq
	setSettingCalls    [][2]string
	resetSettingCalls  []string
	reloadConfigCalls  int
	editedConfigPaths  []string
	untrackedPkgs      []PresetToolRow
	ghApprovedPRs      []int
	ghMergedPRs        [][2]string
	ghStateChanges     [][2]string
	ghComments         []string
	ghCheckedOutPRs    []int
	trackedCalls       []string
	addedPkgCalls      []string
	addTrackExecCalls  []string
	untrackedCalls     []string
	updatePreviewCnt   int
	updateAllCalls     []bool
	updateAllExecCalls []bool
	doctorLoadCalls    int
	outdatedLoadCalls  int
}

func newMockBackend() *mockBackend {
	gruvDark := DefaultPalette()
	gruvLight := config.Palette{
		ID:     "gruvbox-light",
		Name:   "gruvbox-light",
		Mode:   "light",
		Tokens: defaultLightTokens(),
	}
	catppuccin := config.Palette{
		ID:   "catppuccin-dark",
		Name: "catppuccin-dark",
		Mode: "dark",
		Tokens: map[string]string{
			"name":         "catppuccin-dark",
			"mode":         "dark",
			"bg":           "#1e1e2e",
			"bg_alt":       "#181825",
			"fg":           "#cdd6f4",
			"fg_dim":       "#9399b2",
			"accent":       "#cba6f7",
			"selection_bg": "#45475a",
			"selection_fg": "#cdd6f4",
			"border":       "#6c7086",
			"color1":       "#f38ba8",
			"color2":       "#a6e3a1",
			"color3":       "#f9e2af",
			"color4":       "#89b4fa",
			"color5":       "#f5c2e7",
			"color6":       "#94e2d5",
			"color9":       "#f38ba8",
			"color10":      "#a6e3a1",
			"color11":      "#f9e2af",
			"color12":      "#89b4fa",
			"color13":      "#f5c2e7",
			"color14":      "#94e2d5",
		},
	}

	fixPlan := &engine.Plan{
		Title:  "Re-render stale tmux colors",
		Preset: "mac-workstation",
		Theme:  "gruvbox-dark",
		Actions: []engine.Action{
			{
				ID:      "render-tmux",
				Kind:    engine.ActionRender,
				Target:  "config/tmux/colors.conf",
				Summary: "Render tmux palette from colors-template.conf",
				Risk:    engine.RiskSafe,
				Diff:    "--- a/config/tmux/colors.conf\n+++ b/config/tmux/colors.conf\n@@ -1,2 +1,2 @@\n-set -g @accent '#cba6f7'\n+set -g @accent '#fe8019'",
			},
		},
	}

	return &mockBackend{
		host: HostBadge{
			OS:          "darwin",
			Distro:      "macos",
			Arch:        "arm64",
			Preset:      "mac-workstation",
			Zone:        "personal",
			Theme:       "gruvbox-dark",
			PkgManagers: []string{"brew", "mise", "gh"},
		},
		envVars: []EnvVarRow{
			{VarKey: "very-long-service-api-key", Zone: "work", ActiveSourceKind: "dotenv-local", ActiveSourcePath: "~/work/code/api-service/.env.local", ActiveMasked: "sk-****9f2a", Fingerprint: "fp-aaa111", IsSecret: true, SourceCount: 3, ShadowedCount: 2},
			{VarKey: "DATABASE_URL", Zone: "personal", ActiveSourceKind: "dotenv", ActiveSourcePath: "~/personal/code/demo-web/.env", ActiveMasked: "postgres://****", Fingerprint: "fp-bbb222", IsSecret: true, SourceCount: 1, ShadowedCount: 0},
		},
		envFiles: []EnvFileRow{
			{Path: "~/work/code/api-service/.env.local", Zone: "work", SourceKind: "dotenv-local", VarCount: 8, SecretCount: 3},
			{Path: "~/personal/code/demo-web/.env", Zone: "personal", SourceKind: "dotenv", VarCount: 4, SecretCount: 1},
		},
		envDetails: map[string]EnvDetail{
			"very-long-service-api-key": {
				VarKey:       "very-long-service-api-key",
				ActiveMasked: "sk-****9f2a",
				Fingerprint:  "fp-aaa111",
				IsSecret:     true,
				Layers: []EnvLayerRow{
					{Status: "ACTIVE", VarKey: "very-long-service-api-key", Zone: "work", SourceKind: "dotenv-local", SourcePath: "~/work/code/api-service/.env.local", LineNum: 12, MaskedValue: "sk-****9f2a", Fingerprint: "fp-aaa111", IsSecret: true},
					{Status: "SHADOWED", VarKey: "very-long-service-api-key", Zone: "work", SourceKind: "zone-mise", SourcePath: "~/work/mise.toml", LineNum: 8, MaskedValue: "sk-****old1", Fingerprint: "fp-old111", IsSecret: true},
				},
				BlastRadius: []EnvLayerRow{
					{Status: "MATCH", VarKey: "very-long-service-api-key", Zone: "work", SourceKind: "dotenv-local", SourcePath: "~/work/code/api-service/.env.local", LineNum: 12, MaskedValue: "sk-****9f2a", Fingerprint: "fp-aaa111", IsSecret: true},
					{Status: "MATCH", VarKey: "very-long-service-api-key", Zone: "work", SourceKind: "dotenv", SourcePath: "~/work/code/worker/.env", LineNum: 4, MaskedValue: "sk-****9f2a", Fingerprint: "fp-aaa111", IsSecret: true},
				},
			},
		},
		envSecrets: map[string]string{
			"~/work/code/api-service/.env.local\x00very-long-service-api-key": "sk-live-revealed-9f2a",
			"~/work/code/worker/.env\x00very-long-service-api-key":            "sk-live-revealed-9f2a",
		},
		repos: []RepoRow{
			{
				Name: "api-service", Zone: "work", Forge: "github", RemoteURL: "https://github.com/example/api-service",
				LocalPath: "/tmp/example/work/code/api-service", Branch: "main", Dirty: true, Ahead: 1, Behind: 0,
				Locals: []LocalManifestEntry{
					{RelPath: ".env.local", SizeBytes: 420, BackedUp: true},
					{RelPath: "mise.local.toml", SizeBytes: 180, BackedUp: false},
				},
			},
			{
				Name: "demo-web", Zone: "personal", Forge: "github", RemoteURL: "https://github.com/example/demo-web",
				LocalPath: "/tmp/example/personal/code/demo-web", Branch: "feat/ui", Dirty: false, Ahead: 0, Behind: 2,
			},
		},
		ghItems: []GHItemRow{
			{Kind: "pr", Repo: "example/api-service", Number: 104, Title: "Add structured health checks", State: "open", Author: "dev1", URL: "https://github.com/example/api-service/pull/104", Zone: "work", Body: "## Health Checks\n- [x] HTTP /readyz endpoint\n- [ ] Timeout guard", Labels: []string{"api", "ops"}, UpdatedAt: "2026-11-21"},
			{Kind: "issue", Repo: "example/demo-web", Number: 22, Title: "Improve mobile spacing", State: "open", Author: "dev2", URL: "https://github.com/example/demo-web/issues/22", Zone: "personal", Body: "Spacing overflows on narrow viewports.\n\nRepro:\n1. Resize to 80x24", Labels: []string{"ui"}, UpdatedAt: "2026-11-20"},
		},
		ghRepoItems: map[string][]GHItemRow{
			"example/demo-web|open": {
				{Kind: "issue", Repo: "example/demo-web", Number: 22, Title: "Improve mobile spacing", State: "open", Author: "dev2", URL: "https://github.com/example/demo-web/issues/22", Zone: "personal", Body: "Spacing overflows on narrow viewports.", Labels: []string{"ui"}, UpdatedAt: "2026-11-20"},
				{Kind: "issue", Repo: "example/demo-web", Number: 45, Title: "Add dark mode docs", State: "open", Author: "dev3", URL: "https://github.com/example/demo-web/issues/45", Zone: "personal", Body: "Document light/dark role mapping.", Labels: []string{"docs"}, UpdatedAt: "2026-11-19"},
			},
			"example/demo-web|closed": {
				{Kind: "pr", Repo: "example/demo-web", Number: 18, Title: "Initial scaffold", State: "closed", Author: "dev1", URL: "https://github.com/example/demo-web/pull/18", Zone: "personal", Body: "Merged initial project layout.", UpdatedAt: "2026-11-10"},
			},
		},
		ghDetails: map[string]GHItemDetail{
			"pr|example/api-service#104": {
				GHItemRow: GHItemRow{
					Kind: "pr", Repo: "example/api-service", Number: 104, Title: "Add structured health checks",
					State: "open", Author: "dev1", URL: "https://github.com/example/api-service/pull/104", Zone: "work",
					Body: "## Health Checks\n- [x] HTTP /readyz endpoint\n- [ ] Timeout guard\n\n`curl localhost:8080/readyz`", Labels: []string{"api", "ops"}, UpdatedAt: "2026-11-21",
				},
				HeadRef: "feat/health", BaseRef: "main", ReviewDecision: "APPROVED", ChecksSummary: "3 passed",
				Additions: 84, Deletions: 12, ChangedFiles: 4,
				Comments: []GHCommentRow{{Author: "reviewer1", Body: "Looks clean!", CreatedAt: "2026-11-21 14:00"}},
			},
		},
		fleetNodes: []FleetNodeRow{
			{
				Name: "dev-vm-01", DNSName: "dev-vm-01.tailnet.ts.net", TailscaleIP: "100.64.0.12",
				OS: "linux", Arch: "arm64", Provider: "hetzner", TargetID: "dev-vm-01", Preset: "ubuntu-vm",
				Online: true, SSHEnabled: true, Tags: []string{"tag:dev"}, NoZones: true, PushEnv: true,
				Checklist: []FleetChecklistItem{
					{Done: false, Title: "Authenticate Claude CLI", Command: "claude auth login"},
					{Done: true, Title: "Verify Tailscale status", Command: "tailscale status"},
				},
				LastSynced: "2026-11-21 18:00",
			},
			{
				Name: "aws-runner-01", DNSName: "", TailscaleIP: "",
				OS: "linux", Arch: "amd64", Provider: "aws", TargetID: "i-0123456789abcdef0", Preset: "ubuntu-runner",
				Online: false, SSHEnabled: false, Tags: []string{"tag:ci"},
			},
		},
		presets: []PresetSummary{
			{ID: "mac-workstation", Description: "Full macOS workstation profile", Include: []string{"dev-cli"}, Exclude: []string{"claude-code@latest"}, Secrets: "1password", MatchesHost: true, Active: true, PackageCount: 42},
			{ID: "ubuntu-runner", Description: "Headless Ubuntu CI worker", Include: []string{"base"}, Exclude: []string{"desktop", "gui"}, Secrets: "none", PackageCount: 14},
		},
		presetTools: map[string][]PresetToolRow{
			"mac-workstation": {
				{
					ID: "zsh-autosuggestions", Bin: "", Provider: "brew", PackageName: "zsh-autosuggestions",
					Updates: "manager", Source: "pkg-list", OriginPreset: "base-cli", OriginFile: "packages.toml",
					AllProviders: []string{"brew"}, InstallCmd: "brew install zsh-autosuggestions",
					UpdateCmd: "brew upgrade zsh-autosuggestions", RemoveCmd: "brew uninstall zsh-autosuggestions",
					Status: "installed",
				},
				{
					ID: "font-caskaydia-cove-nerd-font", Bin: "", Provider: "cask", PackageName: "font-caskaydia-cove-nerd-font",
					Updates: "manager", Source: "pkg-list", OriginPreset: "mac-desktop", OriginFile: "packages.toml",
					AllProviders: []string{"cask"}, InstallCmd: "brew install --cask font-caskaydia-cove-nerd-font",
					UpdateCmd: "brew upgrade --cask font-caskaydia-cove-nerd-font", RemoveCmd: "brew uninstall --cask font-caskaydia-cove-nerd-font",
					Status: "installed",
				},
				{
					ID: "session-manager-plugin", Bin: "session-manager-plugin", Provider: "cask", PackageName: "session-manager-plugin",
					Updates: "manager", Source: "tools.toml", OriginPreset: "cloud-tools", OriginFile: "tools.toml",
					AllProviders: []string{"cask", "apt"}, InstallCmd: "brew install --cask session-manager-plugin",
					UpdateCmd: "brew upgrade --cask session-manager-plugin", RemoveCmd: "brew uninstall --cask session-manager-plugin",
					Status: "installed",
				},
				{
					ID: "claude-code", Bin: "claude", Provider: "script", PackageName: "claude-code",
					Updates: "self", Source: "tools.toml", OriginPreset: "ai-tools", OriginFile: "tools.toml",
					AllProviders: []string{"script"}, InstallCmd: "curl -fsSL https://example.com/install.sh | sh",
					UpdateCmd: "claude update", Status: "installed",
				},
			},
			"ubuntu-runner": {
				{ID: "fd", Bin: "fd", Provider: "apt", PackageName: "fd-find", Updates: "manager", Source: "tools.toml", OriginPreset: "base", OriginFile: "tools.toml", InstallCmd: "sudo apt-get install -y fd-find", Status: "installed"},
			},
		},
		pkgDetails: map[string]PackageDetail{
			"brew:zsh-autosuggestions": {
				ID:            "zsh-autosuggestions",
				Provider:      "brew",
				PackageName:   "zsh-autosuggestions",
				Version:       "0.7.1",
				LatestVersion: "0.7.2",
				Description:   "Fish-like fast/unobtrusive autosuggestions for zsh",
				Homepage:      "https://github.com/zsh-users/zsh-autosuggestions",
				OriginPreset:  "base-cli",
				OriginFile:    "packages.toml",
				Dependencies:  []string{"zsh"},
				Artifacts:     []string{"/opt/homebrew/share/zsh-autosuggestions/zsh-autosuggestions.zsh"},
				Caveats:       "Source zsh-autosuggestions.zsh from your .zshrc",
			},
		},
		themeIDs: []string{"catppuccin-dark", "gruvbox-dark", "gruvbox-light"},
		palettes: map[string]config.Palette{
			"catppuccin-dark": catppuccin,
			"gruvbox-dark":    gruvDark,
			"gruvbox-light":   gruvLight,
		},
		activeTheme: "gruvbox-dark",
		templates: []TemplateStatusRow{
			{App: "tmux", Template: "config/tmux/colors-template.conf", Output: "config/tmux/colors.conf", PostHook: "tmux source-file ~/.config/tmux/tmux.conf", Status: "stale"},
			{App: "nvim", Template: "config/nvim/lua/colors-template.lua", Output: "config/nvim/lua/colors.lua", PostHook: "pkill -USR1 -x nvim || true", Status: "ok"},
		},
		cleanup: []CleanItem{
			{ID: "brew-cache", Tier: "safe", Category: "pkg", Title: "Homebrew download cache", Paths: []string{"~/Library/Caches/Homebrew"}, Bytes: 620_000_000, SelectedByDefault: true},
			{ID: "go-build", Tier: "safe", Category: "cli", Title: "Go build cache", Paths: []string{"~/.cache/go-build"}, Bytes: 240_000_000, SelectedByDefault: true},
			{ID: "cargo-target", Tier: "costly", Category: "dev", Title: "Stale Cargo target/ dirs (>30d)", Paths: []string{"~/personal/code/demo/target"}, Bytes: 990_000_000, SelectedByDefault: false},
			{ID: "docker-vols", Tier: "dangerous", Category: "containers", Title: "Unused container volumes", Paths: []string{"docker://volumes"}, Bytes: 2_100_000_000, Warning: "Deletes persistent container data", SelectedByDefault: true},
		},
		historySrc: "zsh+atuin",
		historyRows: []HistoryRow{
			{Timestamp: time.Date(2026, 11, 22, 10, 15, 0, 0, time.UTC), Shell: "zsh", Zone: "work", ExitCode: 0, DurationMs: 420, Command: "git status -sb"},
			{Timestamp: time.Date(2026, 11, 22, 10, 16, 0, 0, time.UTC), Shell: "atuin", Zone: "work", ExitCode: 1, DurationMs: 1850, Command: "go test ./internal/api"},
			{Timestamp: time.Date(2026, 11, 22, 10, 18, 0, 0, time.UTC), Shell: "atuin", Zone: "personal", ExitCode: 2, DurationMs: 910, Command: "npm run lint"},
		},
		doctor: DoctorSummary{
			Host:             "darwin/arm64",
			Preset:           "mac-workstation",
			Theme:            "gruvbox-dark",
			LinksOK:          18,
			LinksTotal:       19,
			TemplatesOK:      15,
			TemplatesTotal:   16,
			PackagesManaged:  42,
			PackagesOutdated: 3,
			ZonesOK:          2,
			ZonesTotal:       2,
			ReclaimableBytes: 1_850_000_000,
			Findings: []DoctorFinding{
				{
					ID:       "stale-tmux",
					Severity: "warn",
					Category: "templates",
					Title:    "tmux colors.conf is stale for active theme",
					Detail:   "Disk SHA256 differs from expected gruvbox-dark render",
					FixCLI:   "ws apply --only tmux",
					FixPlan:  fixPlan,
				},
				{
					ID:       "zones-ok",
					Severity: "ok",
					Category: "zones",
					Title:    "Identity zones verified (personal, work)",
					Detail:   "All zone mise.toml and .gitconfig files present",
					FixCLI:   "ws doctor",
				},
			},
		},
		dbStatus: DBStatusSummary{
			Path:             "~/.ws/ws.db",
			SchemaVersion:    2,
			KeySource:        "1password",
			HasKey:           true,
			DirtySinceBackup: true,
			LastBackupAt:     "2026-11-21 12:00",
			CurrentHash:      "sha256:abcdef0123456789",
			EnvCount:         24,
			BlobCount:        6,
			RepoCount:        2,
		},
		vaultInv: VaultInventoryView{
			HasOPCLI: true,
			OPVault:  "Private",
			Items: []VaultItemRow{
				{
					ID: "ws-db", Scope: "db", Label: "ws.db (Encrypted Snapshot Vault)",
					LocalPath: "~/.ws/ws.db", DocTitle: "ws-db-backup.enc", Category: "vault",
					ExistsLocal: true, FileCount: 1, SizeBytes: 4096,
					Note: "Recommended: AES-256-GCM snapshot; excludes ~/.ws/cache, logs, exports, and ledger.lock",
				},
				{
					ID: "ws-db-key", Scope: "db-key", Label: "ws.db Master Key (db.key)",
					LocalPath: "~/.ws/db.key", DocTitle: "ws-db.key", Category: "key",
					ExistsLocal: true, FileCount: 1, SizeBytes: 64,
				},
				{
					ID: "ssh", Scope: "ssh", Label: "~/.ssh (SSH Keys & Config)",
					LocalPath: "~/.ssh", DocTitle: "~/.ssh.zip", Category: "ssh",
					ExistsLocal: true, IsDir: true, FileCount: 3, SizeBytes: 2048,
				},
				{
					ID: "secrets", Scope: "secrets", Label: "Private Layer (mise.local.toml + local tasks)",
					LocalPath: "~/personal/code/workstation/mise.local.toml", DocTitle: "workstation-secrets.zip", Category: "secrets",
					ExistsLocal: true, FileCount: 1, SizeBytes: 512,
				},
			},
		},
		fileDiff: FleetFileDiffView{
			Node:      "dev-vm-01",
			TargetID:  "dev-vm-01",
			Roots:     []string{".ssh", ".aws/credentials"},
			DiffCount: 1, LocalOnlyCount: 1, RemoteOnlyCount: 1, SameCount: 1,
			Entries: []FleetFileDiffRow{
				{
					RelPath: ".ssh/config", Root: ".ssh", Category: "ssh", Status: "local-newer", IsSecret: false,
					Local:  FleetFileMeta{Exists: true, SHA256: "aaaa111122223333", SizeBytes: 128, ModTime: 1760000100, Mode: "0644"},
					Remote: FleetFileMeta{Exists: true, SHA256: "bbbb444455556666", SizeBytes: 96, ModTime: 1760000000, Mode: "0644"},
				},
				{
					RelPath: ".ssh/id_ed25519", Root: ".ssh", Category: "ssh", Status: "same", IsSecret: true,
					Local:  FleetFileMeta{Exists: true, SHA256: "cccc777788889999", SizeBytes: 411, ModTime: 1760000000, Mode: "0600"},
					Remote: FleetFileMeta{Exists: true, SHA256: "cccc777788889999", SizeBytes: 411, ModTime: 1760000000, Mode: "0600"},
				},
				{
					RelPath: ".aws/credentials", Root: ".aws/credentials", Category: "files", Status: "local-only", IsSecret: true,
					Local: FleetFileMeta{Exists: true, SHA256: "dddd000011112222", SizeBytes: 256, ModTime: 1760000050, Mode: "0600"},
				},
				{
					RelPath: "personal/code/workstation/mise.local.toml", Root: "personal/code/workstation/mise.local.toml", Category: "secrets", Status: "remote-only", IsSecret: true,
					Remote: FleetFileMeta{Exists: true, SHA256: "eeee333344445555", SizeBytes: 310, ModTime: 1760000080, Mode: "0600"},
				},
			},
		},
		fileTextDiffs: map[string]string{
			".ssh/config": "--- local/.ssh/config\n+++ dev-vm-01/.ssh/config\n@@ -1,2 +1,2 @@\n+  Port 2222\n-  Port 22",
		},
		settingsView: SettingsView{
			ConfigPath:     "~/.ws/config.toml",
			AutoReload:     true,
			AutoReloadSecs: 2,
			GHRefreshMins:  60,
			DefaultSplit:   "auto",
			BorderedBar:    "auto",
			Theme:          "gruvbox-dark",
			StartTab:       "projects",
			Fingerprint:    "fp-init-001",
			Items: []SettingRow{
				{Key: "tui.auto_reload", Category: "tui", Kind: "bool", Value: "true", DefaultValue: "true", Source: "default", Options: []string{"true", "false"}, Description: "Automatically watch and reload configs"},
				{Key: "tui.theme", Category: "tui", Kind: "enum", Value: "gruvbox-dark", DefaultValue: "gruvbox-dark", Source: "ledger", Options: []string{"gruvbox-dark", "catppuccin-dark", "gruvbox-light"}, Description: "Active palette ID"},
				{Key: "WS_OP_VAULT", Category: "vault", Kind: "string", Value: "", DefaultValue: "", Source: "unset", Description: "Target 1Password vault name"},
				{Key: "path.ws_home", Category: "paths", Kind: "readonly", Value: "~/.ws", DefaultValue: "~/.ws", Source: "readonly", ReadOnly: true, Description: "Unified workstation storage root"},
			},
			Watched: []WatchedFileRow{
				{Label: "config.toml", Path: "/tmp/example/.ws/config.toml", Exists: true, SizeBytes: 128, ModTime: 1760000100, Fingerprint: "128:1"},
				{Label: "mise.local.toml", Path: "/tmp/example/mise.local.toml", Exists: true, SizeBytes: 256, ModTime: 1760000100, Fingerprint: "256:1"},
			},
		},
		miseInv: MiseInventoryView{
			MiseVersion:   "2026.11.14",
			OutdatedKnown: true,
			OutdatedCount: 1,
			MissingCount:  1,
			Configs: []MiseConfigRow{
				{
					Path:  "/tmp/example/.config/mise/config.toml",
					Kind:  "config.toml",
					Scope: "global",
					Forge: "local",
					Tools: []MiseToolDecl{{Name: "go", Requested: "1.24"}},
				},
				{
					Path:          "/tmp/example/personal/code/demo-web/.mise/config.toml",
					Kind:          ".mise/config.toml",
					Scope:         "project",
					Zone:          "personal",
					ProjectName:   "demo-web",
					ProjectPath:   "/tmp/example/personal/code/demo-web",
					Forge:         "github",
					GitHubSlug:    "example/demo-web",
					Branch:        "feat/ui",
					Tools:         []MiseToolDecl{{Name: "node", Requested: "20"}},
					Tasks:         []string{"test", "build"},
					EnvKeys:       []string{"API_TOKEN"},
					OutdatedCount: 1,
				},
				{
					Path:         "/tmp/example/work/code/internal-svc/mise.toml",
					Kind:         "mise.toml",
					Scope:        "project",
					Zone:         "work",
					ProjectName:  "internal-svc",
					ProjectPath:  "/tmp/example/work/code/internal-svc",
					Forge:        "codecommit",
					Branch:       "main",
					Tools:        []MiseToolDecl{{Name: "python", Requested: "3.12"}},
					MissingCount: 1,
				},
			},
			Tools: []MiseToolRow{
				{
					Name: "go", Requested: "1.24", Version: "1.24.0", Latest: "1.24.0",
					SourcePath: "/tmp/example/.config/mise/config.toml",
					Scope:      "global", Forge: "local", Installed: true, Active: true, Status: "installed",
				},
				{
					Name: "node", Requested: "20", Version: "20.11.0", Latest: "20.18.0",
					SourcePath: "/tmp/example/personal/code/demo-web/.mise/config.toml",
					Scope:      "project", Zone: "personal", ProjectName: "demo-web", ProjectPath: "/tmp/example/personal/code/demo-web",
					Forge: "github", GitHubSlug: "example/demo-web", Installed: true, Active: true, Outdated: true, Status: "outdated",
				},
				{
					Name: "python", Requested: "3.12", Version: "", Latest: "3.12.7",
					SourcePath: "/tmp/example/work/code/internal-svc/mise.toml",
					Scope:      "project", Zone: "work", ProjectName: "internal-svc", ProjectPath: "/tmp/example/work/code/internal-svc",
					Forge: "codecommit", Installed: false, Active: false, Status: "missing",
				},
			},
		},
		pkgOutdated: map[string]PackageOutdatedInfo{
			"cask:session-manager-plugin": {
				Provider: "cask",
				Name:     "session-manager-plugin",
				Current:  "1.2.650",
				Latest:   "1.2.690",
				Risk:     "safe",
			},
		},
	}
}

func (b *mockBackend) HostSummary() (HostBadge, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.host, nil
}

func (b *mockBackend) ScanEnv() error { return nil }

func (b *mockBackend) LoadEnvVars(query, zone string) ([]EnvVarRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.envZones = append(b.envZones, zone)
	var rows []EnvVarRow
	for _, r := range b.envVars {
		if zone != "" && r.Zone != zone {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(r.VarKey), strings.ToLower(query)) {
			rows = append(rows, r)
		}
	}
	return rows, nil
}

func (b *mockBackend) LoadEnvFiles(zone string) ([]EnvFileRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if zone == "" {
		return append([]EnvFileRow(nil), b.envFiles...), nil
	}
	var scoped []EnvFileRow
	for _, f := range b.envFiles {
		if f.Zone == zone {
			scoped = append(scoped, f)
		}
	}
	return scoped, nil
}

func (b *mockBackend) LoadEnvDetail(varKey, targetDir string) (EnvDetail, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d, ok := b.envDetails[varKey]; ok {
		return d, nil
	}
	return EnvDetail{VarKey: varKey}, nil
}

func (b *mockBackend) RevealEnvValue(sourcePath, varKey string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if val, ok := b.envSecrets[revealKey(sourcePath, varKey)]; ok {
		return val, nil
	}
	return "revealed-placeholder", nil
}

func (b *mockBackend) PreviewEnvRotate(req EnvRotateReq) (EnvRotatePreview, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rotatePreviews = append(b.rotatePreviews, req)
	return EnvRotatePreview{
		VarKey:    req.VarKey,
		NewMasked: "****new",
		Targets: []EnvRotateTargetRow{
			{SourcePath: "~/work/code/api-service/.env.local", SourceKind: "dotenv-local", Zone: "work", LineNum: 12, OldMasked: "sk-****9f2a", NewMasked: "****new", Fingerprint: "fp-aaa111"},
			{SourcePath: "~/work/code/worker/.env", SourceKind: "dotenv", Zone: "work", LineNum: 4, OldMasked: "sk-****9f2a", NewMasked: "****new", Fingerprint: "fp-aaa111"},
		},
	}, nil
}

func (b *mockBackend) ExecEnvRotate(req EnvRotateReq) (EnvRotateResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rotateExecs = append(b.rotateExecs, req)
	return EnvRotateResult{TxID: "tx-001", UpdatedCount: len(req.Paths), Summary: fmt.Sprintf("Rotated %d file(s)", len(req.Paths))}, nil
}

func (b *mockBackend) LoadRepos(refresh bool, zone string) ([]RepoRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.repoZones = append(b.repoZones, zone)
	if zone == "" {
		return append([]RepoRow(nil), b.repos...), nil
	}
	var out []RepoRow
	for _, r := range b.repos {
		if r.Zone == zone {
			out = append(out, r)
		}
	}
	return out, nil
}

func (b *mockBackend) LoadGHItems(refresh bool, zone string) ([]GHItemRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ghZones = append(b.ghZones, zone)
	if zone == "" {
		return append([]GHItemRow(nil), b.ghItems...), nil
	}
	var out []GHItemRow
	for _, it := range b.ghItems {
		if it.Zone == zone {
			out = append(out, it)
		}
	}
	return out, nil
}

func (b *mockBackend) LoadCachedRepoGHItems(repo, state string) ([]GHItemRow, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ck := strings.ToLower(strings.TrimSpace(repo)) + "|" + strings.ToLower(strings.TrimSpace(state))
	b.ghCachedCalls = append(b.ghCachedCalls, ck)
	if items, ok := b.ghRepoItems[ck]; ok {
		return append([]GHItemRow(nil), items...), time.Now().Add(-5 * time.Minute).Unix(), nil
	}
	var out []GHItemRow
	for _, it := range b.ghItems {
		if strings.EqualFold(it.Repo, repo) {
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		return nil, 0, nil
	}
	return out, time.Now().Add(-5 * time.Minute).Unix(), nil
}

func (b *mockBackend) LoadRepoGHItems(repo, zone, state string, refresh bool) ([]GHItemRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ck := strings.ToLower(strings.TrimSpace(repo)) + "|" + strings.ToLower(strings.TrimSpace(state))
	b.ghRepoCalls = append(b.ghRepoCalls, ck)
	b.ghRepoZones = append(b.ghRepoZones, zone)
	b.ghRepoRefreshes = append(b.ghRepoRefreshes, refresh)
	if items, ok := b.ghRepoItems[ck]; ok {
		return append([]GHItemRow(nil), items...), nil
	}
	var out []GHItemRow
	for _, it := range b.ghItems {
		if strings.EqualFold(it.Repo, repo) {
			out = append(out, it)
		}
	}
	return out, nil
}

func (b *mockBackend) LoadGHItemDetail(kind, repo, zone string, number int, refresh bool) (GHItemDetail, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dk := fmt.Sprintf("%s|%s#%d", strings.ToLower(kind), strings.ToLower(repo), number)
	b.ghDetailCalls = append(b.ghDetailCalls, dk)
	b.ghDetailRefresh = append(b.ghDetailRefresh, refresh)
	if det, ok := b.ghDetails[dk]; ok {
		return det, nil
	}
	for _, it := range b.ghItems {
		if strings.EqualFold(it.Repo, repo) && it.Number == number {
			return GHItemDetail{GHItemRow: it}, nil
		}
	}
	return GHItemDetail{}, fmt.Errorf("detail not found for %s", dk)
}

func (b *mockBackend) PullRepos() (RepoPullResult, error) {
	return b.PullSelectedRepos(nil)
}

func (b *mockBackend) PullSelectedRepos(paths []string) (RepoPullResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cp := append([]string(nil), paths...)
	b.pulledPaths = append(b.pulledPaths, cp)
	lines := make([]string, 0, max(1, len(cp)))
	for _, p := range cp {
		lines = append(lines, "pulled "+filepath.Base(p)+" -> fast-forward")
	}
	if len(lines) == 0 {
		lines = []string{"pulled all clean repositories"}
	}
	return RepoPullResult{
		Summary: fmt.Sprintf("Pull: %d updated", max(1, len(cp))),
		Updated: max(1, len(cp)),
		Lines:   lines,
	}, nil
}

func (b *mockBackend) InteractivePullCmd(repo RepoRow) (*exec.Cmd, error) {
	return exec.Command("true"), nil
}

func (b *mockBackend) CloneRepo(source, zone string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clonedRepos = append(b.clonedRepos, source+"@"+zone)
	return "Cloned " + source, nil
}

func (b *mockBackend) LoadFleetNodes() ([]FleetNodeRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]FleetNodeRow(nil), b.fleetNodes...), nil
}

func (b *mockBackend) TailscaleJoinCmd(hostname string, tags []string) (string, error) {
	return fmt.Sprintf(`tailscale up --ssh --authkey="$TS_AUTHKEY" --hostname=%s`, hostname), nil
}

func (b *mockBackend) PreviewFleetSync(req FleetSyncReq) (FleetSyncPreview, error) {
	envCnt := 0
	if req.PushEnvFiles {
		envCnt = 3
	}
	return FleetSyncPreview{
		Summary:        fmt.Sprintf("Sync %s with preset %s", req.Node, req.Preset),
		SkippedConfigs: []string{"aerospace", "ghostty", "raycast"},
		EnvFilesCount:  envCnt,
		Steps:          []string{"Push static ws-linux binary", "Apply preset config", "Deliver encrypted .env files over stdin"},
		Checklist: []FleetChecklistItem{
			{Done: false, Title: "Authenticate Claude CLI", Command: "claude auth login"},
		},
	}, nil
}

func (b *mockBackend) ExecFleetSync(req FleetSyncReq) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fleetSyncCalls = append(b.fleetSyncCalls, req)
	return "Synced " + req.Node, nil
}

func (b *mockBackend) FleetConnectCmd(node FleetNodeRow) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fleetConnects = append(b.fleetConnects, node.Name)
	return exec.Command("true"), nil
}

func (b *mockBackend) ListPresets() ([]PresetSummary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]PresetSummary(nil), b.presets...), nil
}

func (b *mockBackend) ResolvePreset(id string) ([]PresetToolRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]PresetToolRow(nil), b.presetTools[id]...), nil
}

func (b *mockBackend) SearchPackages(query, provider string, limit int) ([]PackageSearchRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	prov := nonEmpty(provider, "brew")
	if prov == "all" {
		prov = "brew"
	}
	return []PackageSearchRow{
		{
			Name:          query + "-cli",
			Provider:      prov,
			Repo:          "homebrew/core",
			Version:       "1.4.0",
			Description:   "Search result for " + query,
			SuggestedFile: "pkg/mac-brew.txt",
			InstallCmd:    "brew install " + query + "-cli",
			RemoveCmd:     "brew uninstall " + query + "-cli",
		},
		{
			Name:          "bat",
			Provider:      prov,
			Repo:          "homebrew/core",
			Version:       "0.25.0",
			Description:   "Cat clone matching " + query,
			Installed:     true,
			Tracked:       true,
			SuggestedFile: "pkg/mac-brew.txt",
			InstallCmd:    "brew install bat",
			RemoveCmd:     "brew uninstall bat",
		},
	}, nil
}

func (b *mockBackend) LoadPackageDetail(pkg PresetToolRow) (PackageDetail, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if det, ok := b.pkgDetails[pkg.Provider+":"+pkg.ID]; ok {
		return det, nil
	}
	return PackageDetail{
		ID:          pkg.ID,
		Provider:    pkg.Provider,
		PackageName: nonEmpty(pkg.PackageName, pkg.ID),
		Version:     "1.0.0",
		Description: nonEmpty(pkg.Description, "Managed package "+nonEmpty(pkg.PackageName, pkg.ID)),
	}, nil
}

func (b *mockBackend) PackageActionExecCmd(action string, pkgs []PresetToolRow) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for _, p := range pkgs {
		ids = append(ids, nonEmpty(p.PackageName, p.ID))
	}
	b.pkgActionExecCalls = append(b.pkgActionExecCalls, action+":"+strings.Join(ids, ","))
	return exec.Command("true"), nil
}

func (b *mockBackend) ExecPackageAction(action string, pkgs []PresetToolRow) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids, lines []string
	for _, p := range pkgs {
		ids = append(ids, nonEmpty(p.PackageName, p.ID))
		lines = append(lines, fmt.Sprintf("%s %s (%s): ok", action, nonEmpty(p.PackageName, p.ID), p.Provider))
	}
	b.pkgActions = append(b.pkgActions, action+":"+strings.Join(ids, ","))
	return CommandRunResult{
		Command: fmt.Sprintf("ws pkg %s %s", action, strings.Join(ids, " ")),
		Output:  strings.Join(lines, "\n"),
	}, nil
}

func (b *mockBackend) ListThemes() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.themeIDs...), nil
}

func (b *mockBackend) LoadPalette(id string) (config.Palette, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.palettes[id]; ok {
		return p, nil
	}
	return DefaultPalette(), nil
}

func (b *mockBackend) ActiveTheme() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.activeTheme, nil
}

func (b *mockBackend) ApplyTheme(id string) (*engine.ApplyResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appliedThemes = append(b.appliedThemes, id)
	if b.applyThemeErr != nil {
		return nil, b.applyThemeErr
	}
	b.activeTheme = id
	return &engine.ApplyResult{Applied: len(b.templates)}, nil
}

func (b *mockBackend) ApplyThemeForApp(id, app string) (*engine.ApplyResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appliedThemeApps = append(b.appliedThemeApps, [2]string{id, app})
	if b.applyThemeErr != nil {
		return nil, b.applyThemeErr
	}
	return &engine.ApplyResult{Applied: 1}, nil
}

func (b *mockBackend) TemplateStatuses() ([]TemplateStatusRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]TemplateStatusRow(nil), b.templates...), nil
}

func (b *mockBackend) EditAppCmd(app string) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.editedApps = append(b.editedApps, app)
	return exec.Command("true"), nil
}

func (b *mockBackend) FinishAppEdit(app string) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{Applied: 1}, nil
}

func (b *mockBackend) ScanCleanup() ([]CleanItem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]CleanItem(nil), b.cleanup...), nil
}

func (b *mockBackend) CleanupExecCmd(ids []string) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.cleanup {
		if it.NeedsSudo && slices.Contains(ids, it.ID) {
			b.cleanupExecCalls++
			b.lastExecCleanIDs = append([]string(nil), ids...)
			return exec.Command("true"), nil
		}
	}
	return nil, nil
}

func (b *mockBackend) RunCleanup(ids []string, dryRun bool) (*engine.ApplyResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupCalls++
	b.lastCleanIDs = append([]string(nil), ids...)
	b.lastCleanDry = dryRun
	return &engine.ApplyResult{Applied: len(ids)}, nil
}

func (b *mockBackend) LoadHistory(query string, limit int) ([]HistoryRow, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []HistoryRow
	for _, r := range b.historyRows {
		if query == "" || strings.Contains(strings.ToLower(r.Command), strings.ToLower(query)) {
			out = append(out, r)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, b.historySrc, nil
}

func (b *mockBackend) RunWSCommand(args []string) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.wsCmdCalls = append(b.wsCmdCalls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "tui" {
		return CommandRunResult{Command: "ws tui", ExitCode: 2, IsErr: true, Output: "nested tui refused"}, fmt.Errorf("refusing nested ws tui")
	}
	cmdStr := "ws " + strings.Join(args, " ")
	out := "ok: executed " + cmdStr + "\nline-2: healthy"
	return CommandRunResult{
		Command: cmdStr,
		Output:  out,
	}, nil
}

func (b *mockBackend) LoadDoctor() (DoctorSummary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.doctorLoadCalls++
	return b.doctor, nil
}

func (b *mockBackend) ApplyPlan(plan *engine.Plan) (*engine.ApplyResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.appliedPlans++
	return &engine.ApplyResult{Applied: len(plan.Actions)}, nil
}

func (b *mockBackend) LoadDBStatus() (DBStatusSummary, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dbStatus, nil
}

func (b *mockBackend) BackupDB(ifChanged bool) (DBBackupResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.backupCalls++
	b.dbStatus.DirtySinceBackup = false
	return DBBackupResult{Changed: true, LocalPath: "~/.ws/backups/ws-backup-latest.enc", OnePassword: true, Summary: "Backed up ws.db"}, nil
}

func (b *mockBackend) UndoLast() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.undoCalls++
	return "Undid last transaction", nil
}

func (b *mockBackend) LoadMiseInventory(zone string, checkOutdated, _ bool) (MiseInventoryView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.miseZones = append(b.miseZones, zone)
	z := strings.TrimSpace(zone)
	var cfgs []MiseConfigRow
	for _, c := range b.miseInv.Configs {
		if z == "" || c.Scope == "global" || strings.EqualFold(c.Zone, z) {
			cfgs = append(cfgs, c)
		}
	}
	var tools []MiseToolRow
	outCnt, missCnt := 0, 0
	for _, t := range b.miseInv.Tools {
		if z == "" || t.Scope == "global" || strings.EqualFold(t.Zone, z) {
			tools = append(tools, t)
			if t.Outdated {
				outCnt++
			}
			if !t.Installed {
				missCnt++
			}
		}
	}
	return MiseInventoryView{
		MiseVersion:   b.miseInv.MiseVersion,
		OutdatedKnown: checkOutdated || b.miseInv.OutdatedKnown,
		OutdatedCount: outCnt,
		MissingCount:  missCnt,
		Configs:       cfgs,
		Tools:         tools,
		Warnings:      append([]string(nil), b.miseInv.Warnings...),
	}, nil
}

func (b *mockBackend) EditMiseFileCmd(path string) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.editedMiseFiles = append(b.editedMiseFiles, path)
	return exec.Command("true"), nil
}

func (b *mockBackend) UpgradeMiseTools(tools []MiseToolRow) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.upgradedMiseTools = append(b.upgradedMiseTools, tools...)
	return CommandRunResult{
		Command: fmt.Sprintf("mise upgrade (%d tool(s))", len(tools)),
		Output:  "upgraded mise tools",
	}, nil
}

func (b *mockBackend) LoadPackageOutdated() (map[string]PackageOutdatedInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outdatedLoadCalls++
	out := make(map[string]PackageOutdatedInfo, len(b.pkgOutdated))
	for k, v := range b.pkgOutdated {
		out[k] = v
	}
	return out, nil
}

func (b *mockBackend) DiffFleetFiles(node, targetID string, includeEnv, includeDB bool) (FleetFileDiffView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fileDiffCalls = append(b.fileDiffCalls, fmt.Sprintf("%s|%v|%v", node, includeEnv, includeDB))
	view := b.fileDiff
	view.Node = node
	view.TargetID = targetID
	view.IncludeEnv = includeEnv
	view.IncludeDB = includeDB
	return view, nil
}

func (b *mockBackend) PreviewFleetFileDiff(node, targetID, relPath string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fileTextCalls = append(b.fileTextCalls, node+":"+relPath)
	if txt, ok := b.fileTextDiffs[relPath]; ok {
		return txt, nil
	}
	if strings.Contains(relPath, "id_ed25519") || strings.Contains(relPath, ".env") || strings.Contains(relPath, "credentials") {
		return "Redacted: " + relPath + " is sensitive; text diff preview disabled.", nil
	}
	return relPath + " is byte-identical.", nil
}

func (b *mockBackend) SyncFleetFiles(req FleetFileSyncReq) (FleetFileSyncResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fileSyncCalls = append(b.fileSyncCalls, req)
	return FleetFileSyncResult{
		Node:        req.Node,
		Direction:   req.Direction,
		Transferred: len(req.RelPaths),
		Summary:     fmt.Sprintf("%s %s: %d transferred, 0 skipped, 0 failed", strings.ToUpper(req.Direction), req.Node, len(req.RelPaths)),
		Lines:       []string{fmt.Sprintf("%s %s complete", req.Direction, strings.Join(req.RelPaths, ", "))},
	}, nil
}

func (b *mockBackend) LoadVaultInventory() (VaultInventoryView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.vaultInv, nil
}

func (b *mockBackend) RunVaultAction(action string, items []VaultItemRow) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var scopes []string
	for _, it := range items {
		scopes = append(scopes, it.Scope)
	}
	b.vaultActions = append(b.vaultActions, action+":"+strings.Join(scopes, ","))
	return CommandRunResult{
		Command: fmt.Sprintf("ws vault (%s: %s)", action, strings.Join(scopes, ",")),
		Output:  fmt.Sprintf("completed %s on %d scope(s)", action, len(items)),
	}, nil
}

func (b *mockBackend) LoadSettings() (SettingsView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settingsView, nil
}

func (b *mockBackend) SetSetting(key, value string) (SettingsView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.setSettingCalls = append(b.setSettingCalls, [2]string{key, value})
	for i := range b.settingsView.Items {
		if b.settingsView.Items[i].Key == key {
			b.settingsView.Items[i].Value = value
			b.settingsView.Items[i].Source = "override"
			b.settingsView.Items[i].Overridden = true
		}
	}
	switch key {
	case "tui.auto_reload":
		b.settingsView.AutoReload = value == "true"
	case "tui.theme":
		b.settingsView.Theme = value
		b.activeTheme = value
	case "tui.bordered_bar":
		b.settingsView.BorderedBar = value
	}
	b.settingsView.Fingerprint = fmt.Sprintf("fp-set-%d", len(b.setSettingCalls))
	return b.settingsView, nil
}

func (b *mockBackend) ResetSetting(key string) (SettingsView, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetSettingCalls = append(b.resetSettingCalls, key)
	for i := range b.settingsView.Items {
		if b.settingsView.Items[i].Key == key {
			b.settingsView.Items[i].Value = b.settingsView.Items[i].DefaultValue
			b.settingsView.Items[i].Source = "default"
			b.settingsView.Items[i].Overridden = false
		}
	}
	if key == "tui.auto_reload" {
		b.settingsView.AutoReload = true
	}
	return b.settingsView, nil
}

func (b *mockBackend) CheckConfigWatch(prevFingerprint string) (ConfigWatchResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.watchChanged) > 0 || (prevFingerprint != "" && prevFingerprint != b.settingsView.Fingerprint) {
		ch := append([]string(nil), b.watchChanged...)
		b.watchChanged = nil
		return ConfigWatchResult{
			Changed:      true,
			Fingerprint:  b.settingsView.Fingerprint,
			ChangedFiles: ch,
		}, nil
	}
	return ConfigWatchResult{
		Changed:     false,
		Fingerprint: b.settingsView.Fingerprint,
	}, nil
}

func (b *mockBackend) ReloadConfig() (SettingsView, []string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reloadConfigCalls++
	if b.settingsView.Theme != "" {
		b.activeTheme = b.settingsView.Theme
	}
	return b.settingsView, []string{"config.toml"}, nil
}

func (b *mockBackend) EditConfigFileCmd(path string) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.editedConfigPaths = append(b.editedConfigPaths, path)
	return exec.Command("true"), nil
}

func (b *mockBackend) ApproveGHPR(repo, zone string, number int, body string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ghApprovedPRs = append(b.ghApprovedPRs, number)
	return fmt.Sprintf("Approved %s#%d", repo, number), nil
}

func (b *mockBackend) MergeGHPR(repo, zone string, number int, method string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ghMergedPRs = append(b.ghMergedPRs, [2]string{fmt.Sprintf("%s#%d", repo, number), method})
	return fmt.Sprintf("Merged %s#%d (%s)", repo, number, method), nil
}

func (b *mockBackend) SetGHItemState(kind, repo, zone string, number int, closeItem bool) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	act := "reopen"
	if closeItem {
		act = "close"
	}
	b.ghStateChanges = append(b.ghStateChanges, [2]string{fmt.Sprintf("%s#%d", kind, number), act})
	return fmt.Sprintf("%s %s #%d", strings.ToUpper(act), kind, number), nil
}

func (b *mockBackend) CommentGHItem(kind, repo, zone string, number int, body string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ghComments = append(b.ghComments, fmt.Sprintf("%s#%d:%s", kind, number, body))
	return fmt.Sprintf("Commented on #%d", number), nil
}

func (b *mockBackend) CheckoutGHPR(repoDir, repo, zone string, number int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ghCheckedOutPRs = append(b.ghCheckedOutPRs, number)
	return fmt.Sprintf("Checked out PR #%d", number), nil
}

func (b *mockBackend) LoadUntrackedPackages(presetID string) ([]PresetToolRow, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]PresetToolRow(nil), b.untrackedPkgs...), nil
}

func (b *mockBackend) TrackPackages(relFile string, pkgs []PresetToolRow) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	removedNames := map[string]bool{}
	for _, p := range pkgs {
		name := nonEmpty(p.PackageName, p.ID)
		b.trackedCalls = append(b.trackedCalls, name)
		removedNames[strings.ToLower(name)] = true
		p.ID = name
		p.Status = "installed"
		p.OriginPreset = "mac-workstation"
		p.OriginFile = nonEmpty(relFile, nonEmpty(p.OriginFile, "pkg/mac-brew.txt"))
		b.presetTools["mac-workstation"] = append(b.presetTools["mac-workstation"], p)
	}
	var kept []PresetToolRow
	for _, u := range b.untrackedPkgs {
		if !removedNames[strings.ToLower(nonEmpty(u.PackageName, u.ID))] {
			kept = append(kept, u)
		}
	}
	b.untrackedPkgs = kept
	return fmt.Sprintf("Tracked %d package(s)", len(pkgs)), nil
}

func (b *mockBackend) AddAndTrackPackageExecCmd(name, provider, relFile string, install bool) (*exec.Cmd, error) {
	if !install {
		return nil, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addTrackExecCalls = append(b.addTrackExecCalls, fmt.Sprintf("%s|%s|%s|%v", name, provider, relFile, install))
	return exec.Command("true"), nil
}

func (b *mockBackend) AddAndTrackPackage(name, provider, relFile string, install bool) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.addedPkgCalls = append(b.addedPkgCalls, fmt.Sprintf("%s|%s|%s|%v", name, provider, relFile, install))
	b.presetTools["mac-workstation"] = append(b.presetTools["mac-workstation"], PresetToolRow{
		ID:           name,
		PackageName:  name,
		Provider:     provider,
		Status:       "installed",
		Updates:      "manager",
		OriginPreset: "mac-workstation",
		OriginFile:   relFile,
	})
	return CommandRunResult{
		Command: fmt.Sprintf("ws pkg add --provider %s --file %s %s", provider, relFile, name),
		Output:  fmt.Sprintf("Added %s to %s", name, relFile),
	}, nil
}

func (b *mockBackend) UntrackPackages(pkgs []PresetToolRow) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	targets := map[string]bool{}
	for _, p := range pkgs {
		name := nonEmpty(p.PackageName, p.ID)
		b.untrackedCalls = append(b.untrackedCalls, name)
		targets[strings.ToLower(name)] = true
	}
	for pid, list := range b.presetTools {
		var kept []PresetToolRow
		for _, item := range list {
			if !targets[strings.ToLower(nonEmpty(item.PackageName, item.ID))] && !targets[strings.ToLower(item.ID)] {
				kept = append(kept, item)
			}
		}
		b.presetTools[pid] = kept
	}
	return fmt.Sprintf("Untracked %d package(s)", len(pkgs)), nil
}

func (b *mockBackend) PreviewUpdateAll(all bool) (UpdatePlanPreview, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updatePreviewCnt++
	return UpdatePlanPreview{
		Summary:      "3 package update(s), 4 planned step(s)",
		PendingCount: 3,
		Steps: []string{
			"[prep] Fast-forward workstation repository (git pull --ff-only)",
			"[os-pkg] Upgrade formulae via brew (brew upgrade)",
			"[post-parallel] Update Rust, tldr, gh extensions & symlinks",
		},
	}, nil
}

func (b *mockBackend) UpdateAllExecCmd(all bool) (*exec.Cmd, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updateAllExecCalls = append(b.updateAllExecCalls, all)
	return exec.Command("true"), nil
}

func (b *mockBackend) ExecUpdateAll(all bool) (CommandRunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updateAllCalls = append(b.updateAllCalls, all)
	return CommandRunResult{
		Command: "ws update --all",
		Output:  "Summary: 5 applied, 0 skipped",
	}, nil
}

func keyRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func typeString(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, keyRune(r))
		m = drainCmds(t, m, cmd)
	}
	return m
}

func sendMsg(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	updated, ok := next.(Model)
	if !ok {
		t.Fatalf("expected Model from Update, got %T", next)
	}
	return updated, cmd
}

func drainCmds(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{}
	if cmd != nil {
		queue = append(queue, cmd)
	}
	for steps := 0; len(queue) > 0 && steps < 128; steps++ {
		cur := queue[0]
		queue = queue[1:]
		if cur == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func(fn tea.Cmd) {
			ch <- fn()
		}(cur)
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(35 * time.Millisecond):
			continue
		}
		if msg == nil {
			continue
		}
		switch v := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, v...)
		case spinner.TickMsg:
			continue
		case tea.QuitMsg:
			return m
		default:
			var nextCmd tea.Cmd
			m, nextCmd = sendMsg(t, m, msg)
			if nextCmd != nil {
				queue = append(queue, nextCmd)
			}
		}
	}
	return m
}

func initLoadedModel(t *testing.T, backend *mockBackend, opts Options) Model {
	t.Helper()
	m := NewModel(backend, opts)
	m = drainCmds(t, m, m.Init())
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

func TestPanels1To6Output0AndStartTabAliases(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{Version: "v2.0"})

	v := m.View()
	if !v.AltScreen {
		t.Fatalf("expected View.AltScreen to be true")
	}

	cases := []struct {
		key       rune
		wantPanel int
		wantText  string
		wantCLI   string
	}{
		{'1', PanelOverview, "Workstation Overview", "$ ws status"},
		{'2', PanelProjects, "Repositories (2)", "$ ws repo pick api-service"},
		{'3', PanelPackages, "Resolved Packages", "$ brew install zsh-autosuggestions"},
		{'4', PanelEnv, "Environment Variables", "$ ws env show very-long-service-api-key"},
		{'5', PanelFleet, "Fleet & Tailscale Nodes (2)", "$ ws fleet sync dev-vm-01"},
		{'6', PanelWorkstation, "Plan & Diff Preview", "$ ws apply --only tmux"},
	}

	for _, tc := range cases {
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, keyRune(tc.key))
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.wantPanel {
			t.Fatalf("key %q: active panel = %d, want %d", tc.key, m.ActivePanel(), tc.wantPanel)
		}
		out := ui.StripANSI(m.Render())
		if !strings.Contains(out, tc.wantText) {
			t.Fatalf("panel %d missing expected text %q\nview:\n%s", tc.wantPanel+1, tc.wantText, out)
		}
		if !strings.Contains(out, tc.wantCLI) {
			t.Fatalf("panel %d missing expected CLI bar %q\nview:\n%s", tc.wantPanel+1, tc.wantCLI, out)
		}
	}

	m, _ = sendMsg(t, m, keyRune('0'))
	if m.ActivePanel() != PanelOutput || !strings.Contains(ui.StripANSI(m.Render()), "Output Console") {
		t.Fatalf("expected '0' to open Output Console")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.ActivePanel() != PanelWorkstation {
		t.Fatalf("expected Esc on Output Console to return to prevPanel (Workstation), got %d", m.ActivePanel())
	}

	m, _ = sendMsg(t, m, keyRune('4'))
	m, _ = sendMsg(t, m, keyRune(']'))
	if m.panels[PanelEnv].ActiveSubView() != envSubFiles {
		t.Fatalf("expected ']' on Env to switch to Files (1), got %d", m.panels[PanelEnv].ActiveSubView())
	}
	m, _ = sendMsg(t, m, keyRune(']'))
	if m.panels[PanelEnv].ActiveSubView() != envSubVault {
		t.Fatalf("expected second ']' on Env to switch to Vault (2), got %d", m.panels[PanelEnv].ActiveSubView())
	}
	m, _ = sendMsg(t, m, keyRune('['))
	if m.panels[PanelEnv].ActiveSubView() != envSubFiles {
		t.Fatalf("expected '[' on Env to switch back to Files (1), got %d", m.panels[PanelEnv].ActiveSubView())
	}
	m, _ = sendMsg(t, m, keyRune('['))
	if m.panels[PanelEnv].ActiveSubView() != envSubVars {
		t.Fatalf("expected '[' on Env to switch back to Vars (0), got %d", m.panels[PanelEnv].ActiveSubView())
	}

	m, _ = sendMsg(t, m, keyRune('3'))
	if !slices.Equal(m.panels[PanelPackages].SubViews(), []string{"Installed", "Search", "Mise Tools", "Mise Configs", "Cleanup"}) {
		t.Fatalf("unexpected Packages SubViews: %v", m.panels[PanelPackages].SubViews())
	}
	if !slices.Equal(m.panels[PanelEnv].SubViews(), []string{"Vars", "Files", "Vault"}) {
		t.Fatalf("unexpected Env & Secrets SubViews: %v", m.panels[PanelEnv].SubViews())
	}
	if !slices.Equal(m.panels[PanelFleet].SubViews(), []string{"Nodes", "File Sync"}) {
		t.Fatalf("unexpected Fleet SubViews: %v", m.panels[PanelFleet].SubViews())
	}
	if !slices.Equal(m.panels[PanelWorkstation].SubViews(), []string{"Doctor", "Themes", "Presets", "Settings", "Watched Files", "History"}) {
		t.Fatalf("unexpected Workstation SubViews: %v", m.panels[PanelWorkstation].SubViews())
	}

	aliases := []struct {
		start       string
		wantPanel   int
		wantSub     int
		wantPreview bool
	}{
		{"", PanelOverview, 0, false},
		{"overview", PanelOverview, 0, false},
		{"status", PanelOverview, 0, false},
		{"home", PanelOverview, 0, false},
		{"1", PanelOverview, 0, false},
		{"8", PanelOverview, 0, false},
		{"projects", PanelProjects, projSubRepos, false},
		{"repos", PanelProjects, projSubRepos, false},
		{"2", PanelProjects, projSubRepos, false},
		{"gh", PanelProjects, projSubRepos, true},
		{"gh-prs", PanelProjects, projSubPRs, false},
		{"gh-issues", PanelProjects, projSubIssues, false},
		{"prs", PanelProjects, projSubRepos, true},
		{"issues", PanelProjects, projSubRepos, true},
		{"packages", PanelPackages, pkgSubInstalled, false},
		{"installed", PanelPackages, pkgSubInstalled, false},
		{"pkgs", PanelPackages, pkgSubInstalled, false},
		{"system", PanelPackages, pkgSubInstalled, false},
		{"3", PanelPackages, pkgSubInstalled, false},
		{"pkg-search", PanelPackages, pkgSubSearch, false},
		{"search", PanelPackages, pkgSubSearch, false},
		{"mise-tools", PanelPackages, pkgSubMiseTools, false},
		{"mise-outdated", PanelPackages, pkgSubMiseTools, false},
		{"mise", PanelPackages, pkgSubMiseConfigs, false},
		{"mise-configs", PanelPackages, pkgSubMiseConfigs, false},
		{"cleanup", PanelPackages, pkgSubCleanup, false},
		{"clean", PanelPackages, pkgSubCleanup, false},
		{"env", PanelEnv, envSubVars, false},
		{"secrets", PanelEnv, envSubVars, false},
		{"vars", PanelEnv, envSubVars, false},
		{"4", PanelEnv, envSubVars, false},
		{"envfiles", PanelEnv, envSubFiles, false},
		{"files", PanelEnv, envSubFiles, false},
		{"vault", PanelEnv, envSubVault, false},
		{"db", PanelEnv, envSubVault, false},
		{"fleet", PanelFleet, fleetSubNodes, false},
		{"nodes", PanelFleet, fleetSubNodes, false},
		{"5", PanelFleet, fleetSubNodes, false},
		{"filesync", PanelFleet, fleetSubFileSync, false},
		{"workstation", PanelWorkstation, wsSubDoctor, false},
		{"doctor", PanelWorkstation, wsSubDoctor, false},
		{"6", PanelWorkstation, wsSubDoctor, false},
		{"themes", PanelWorkstation, wsSubThemes, false},
		{"presets", PanelWorkstation, wsSubPresets, false},
		{"preset", PanelWorkstation, wsSubPresets, false},
		{"settings", PanelWorkstation, wsSubSettings, false},
		{"config", PanelWorkstation, wsSubSettings, false},
		{"7", PanelWorkstation, wsSubSettings, false},
		{"watched", PanelWorkstation, wsSubWatchedFiles, false},
		{"watched-files", PanelWorkstation, wsSubWatchedFiles, false},
		{"history", PanelWorkstation, wsSubHistory, false},
		{"hist", PanelWorkstation, wsSubHistory, false},
		{"output", PanelOutput, 0, false},
		{"0", PanelOutput, 0, false},
	}
	for _, a := range aliases {
		am := NewModel(b, Options{StartTab: a.start})
		if am.ActivePanel() != a.wantPanel {
			t.Fatalf("StartTab=%q: ActivePanel=%d, want %d", a.start, am.ActivePanel(), a.wantPanel)
		}
		if len(am.panels[a.wantPanel].SubViews()) > 0 && am.panels[a.wantPanel].ActiveSubView() != a.wantSub {
			t.Fatalf("StartTab=%q: ActiveSubView=%d, want %d", a.start, am.panels[a.wantPanel].ActiveSubView(), a.wantSub)
		}
		if a.wantPanel == PanelProjects && am.FocusPreview() != a.wantPreview {
			t.Fatalf("StartTab=%q: FocusPreview=%v, want %v", a.start, am.FocusPreview(), a.wantPreview)
		}
	}
}

func TestWideTerminalZeroBogusTruncation220x55(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 220, Height: 55})

	pkgOut := ui.StripANSI(m.Render())
	for _, full := range []string{
		"zsh-autosuggestions",
		"font-caskaydia-cove-nerd-font",
		"session-manager-plugin",
		"base-cli:packages.toml",
	} {
		if !strings.Contains(pkgOut, full) {
			t.Fatalf("wide 220x55 Packages view missing %q:\n%s", full, pkgOut)
		}
	}

	m, _ = sendMsg(t, m, keyRune('4'))
	envOut := ui.StripANSI(m.Render())
	if !strings.Contains(envOut, "very-long-service-api-key") {
		t.Fatalf("wide 220x55 Env view truncated very-long-service-api-key:\n%s", envOut)
	}
	if strings.Contains(envOut, ": cmd  / filter") || !strings.Contains(envOut, "Env") {
		t.Fatalf("expected compact K9s breadcrumb/filter prompt in wide frame:\n%s", envOut)
	}
}

func TestResponsive80x24AndWideLineCountAndWidthFit(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{})

	sizes := []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 160, Height: 45},
		{Width: 220, Height: 55},
	}

	checkFrame := func(label string, sz tea.WindowSizeMsg, out string) {
		t.Helper()
		if strings.TrimSpace(out) == "" {
			t.Fatalf("%s at %dx%d rendered empty output", label, sz.Width, sz.Height)
		}
		if h := lipgloss.Height(out); h != sz.Height {
			t.Fatalf("%s at %dx%d height mismatch: got %d != %d", label, sz.Width, sz.Height, h, sz.Height)
		}
		for i, line := range strings.Split(out, "\n") {
			if lw := lipgloss.Width(line); lw > sz.Width {
				t.Fatalf("%s at %dx%d line %d overflowed width: got %d > %d (%q)", label, sz.Width, sz.Height, i+1, lw, sz.Width, line)
			}
		}
	}

	for _, sz := range sizes {
		m, _ = sendMsg(t, m, sz)
		for _, k := range []rune{'1', '2', '3', '4', '5', '6', '7', '0'} {
			m, _ = sendMsg(t, m, keyRune(k))
			out := m.View().Content
			checkFrame(fmt.Sprintf("panel %c", k), sz, out)

			if k == '2' {
				m, _ = sendMsg(t, m, keyRune(']'))
				checkFrame("mise sub tools", sz, m.View().Content)
				m, _ = sendMsg(t, m, keyRune(']'))

				m, _ = sendMsg(t, m, keyRune('F'))
				checkFrame("mise filter/sort modal", sz, m.View().Content)
				m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

				m, _ = sendMsg(t, m, keyRune('E'))
				checkFrame("export modal", sz, m.View().Content)
				m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
			}

			if k == '3' {
				for si := 0; si < 3; si++ {
					m, _ = sendMsg(t, m, keyRune(']'))
					checkFrame(fmt.Sprintf("system sub %d", si+1), sz, m.View().Content)
				}

				m, _ = sendMsg(t, m, keyRune(']'))
			}

			if k == '4' {
				var cmd tea.Cmd
				m, cmd = sendMsg(t, m, keyRune(']'))
				m = drainCmds(t, m, cmd)
				checkFrame("fleet file sync", sz, m.View().Content)
				m, _ = sendMsg(t, m, keyRune(']'))
			}
		}

		m, _ = sendMsg(t, m, keyRune(':'))
		checkFrame("command palette dropdown", sz, m.View().Content)
		m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

		m, _ = sendMsg(t, m, keyRune('?'))
		checkFrame("help overlay", sz, m.View().Content)
		m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	}
}

func TestEnvRotateModalCancelDefaultAndExecution(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})

	m, _ = sendMsg(t, m, keyRune('/'))
	if !m.CommandBarActive() {
		t.Fatalf("expected command bar filter to capture input after '/'")
	}
	m, _ = sendMsg(t, m, keyRune('q'))
	if m.ActivePanel() != PanelEnv {
		t.Fatalf("typing into filter switched panels")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	var cmd tea.Cmd
	m, cmd = sendAction(t, m, 'v')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "sk-live-revealed") {
		t.Fatalf("expected revealed value after pressing a+v:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendAction(t, m, 'v')
	m = drainCmds(t, m, cmd)
	if strings.Contains(ui.StripANSI(m.Render()), "sk-live-revealed") {
		t.Fatalf("expected value to hide again after second a+v")
	}

	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "Selective Token Update") {
		t.Fatalf("expected Rotate modal to open after pressing a+R")
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	execCount := len(b.rotateExecs)
	b.mu.Unlock()
	if execCount != 0 {
		t.Fatalf("expected 0 ExecEnvRotate calls on immediate Enter (Cancel default), got %d", execCount)
	}

	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	secretVal := "sk-brand-new-secret-999"
	m = typeString(t, m, secretVal)
	modalOut := ui.StripANSI(m.Render())
	if strings.Contains(modalOut, secretVal) {
		t.Fatalf("raw secret value leaked in rendered Rotate modal!")
	}
	if !strings.Contains(modalOut, "Target Files (1 checked of 2)") {
		t.Fatalf("expected ONLY 1 anchor file checked by default in selective Rotate modal, got:\n%s", modalOut)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	execCount = len(b.rotateExecs)
	b.mu.Unlock()
	if execCount != 0 {
		t.Fatalf("expected Tab from input to land on Cancel (0 ExecEnvRotate calls), got %d", execCount)
	}

	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	m = typeString(t, m, secretVal)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	execs := append([]EnvRotateReq(nil), b.rotateExecs...)
	b.mu.Unlock()
	if len(execs) != 1 || execs[0].NewValue != secretVal || len(execs[0].Paths) != 1 || execs[0].Paths[0] != "~/work/code/api-service/.env.local" {
		t.Fatalf("expected 1 selective ExecEnvRotate with only anchor path, got %+v", execs)
	}

	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	m = typeString(t, m, secretVal)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	if !strings.Contains(ui.StripANSI(m.Render()), "Target Files (2 checked of 2)") {
		t.Fatalf("expected 2 checked after toggling second file with space:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	execs = append([]EnvRotateReq(nil), b.rotateExecs...)
	b.mu.Unlock()
	if len(execs) != 2 || len(execs[1].Paths) != 2 {
		t.Fatalf("expected second ExecEnvRotate to target both checked paths, got %+v", execs)
	}
}

func sendAction(t *testing.T, m Model, key rune) (Model, tea.Cmd) {
	t.Helper()
	m, _ = sendMsg(t, m, keyRune('a'))
	if !m.HasActionMenu() {
		t.Fatalf("expected action menu open after 'a'")
	}
	return sendMsg(t, m, keyRune(key))
}

func TestThemePreviewOnlyAndConfirmApply(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "themes"})

	beforeActive := m.ActiveTheme()
	beforeThemeID := m.Styles().ThemeID

	var cmd tea.Cmd
	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.PreviewTheme() != "" {
		t.Fatalf("expected pressing a+t on active theme to keep PreviewTheme empty, got %q", m.PreviewTheme())
	}
	if m.Styles().ThemeID != beforeThemeID {
		t.Fatalf("expected Styles.ThemeID to remain %q, got %q", beforeThemeID, m.Styles().ThemeID)
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "Already active theme") {
		t.Fatalf("expected active-theme notice on a+t, got:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)

	if m.ActiveTheme() != beforeActive {
		t.Fatalf("pressing a+t mutated ActiveTheme from %q to %q", beforeActive, m.ActiveTheme())
	}
	if m.PreviewTheme() != "gruvbox-light" {
		t.Fatalf("expected PreviewTheme to be %q, got %q", "gruvbox-light", m.PreviewTheme())
	}
	if m.Styles().ThemeID != "gruvbox-light" || m.Styles().ThemeID == beforeThemeID {
		t.Fatalf("expected in-memory Styles.ThemeID to become gruvbox-light during theme preview, got %q", m.Styles().ThemeID)
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "gruvbox-light (preview)") {
		t.Fatalf("expected header badge to show gruvbox-light (preview):\n%s", ui.StripANSI(m.Render()))
	}
	b.mu.Lock()
	applied := len(b.appliedThemes)
	b.mu.Unlock()
	if applied != 0 {
		t.Fatalf("expected 0 backend.ApplyTheme calls on preview, got %d", applied)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.PreviewTheme() != "" || m.Styles().ThemeID != beforeThemeID {
		t.Fatalf("expected second a+t to revert preview to %q, got preview=%q styles=%q", beforeThemeID, m.PreviewTheme(), m.Styles().ThemeID)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	previewed := m.PreviewTheme()
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Apply Theme Palette") {
		t.Fatalf("expected Apply Theme confirmation modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	applied = len(b.appliedThemes)
	b.mu.Unlock()
	if applied != 0 {
		t.Fatalf("expected Cancel default on Apply Theme modal, got %d ApplyTheme calls", applied)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	applied = len(b.appliedThemes)
	b.mu.Unlock()
	if applied != 1 || m.ActiveTheme() != previewed {
		t.Fatalf("expected 1 ApplyTheme call setting ActiveTheme=%q, got calls=%d active=%q", previewed, applied, m.ActiveTheme())
	}

	m, cmd = sendMsg(t, m, keyRune('e'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on 'e' in Themes sub-view")
	}
	m, cmd = sendMsg(t, m, editFinishedMsg{app: "tmux"})
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "Edited tmux") {
		t.Fatalf("expected post-edit toast after editFinishedMsg")
	}
}

func TestThemeApplyErrorRollback(t *testing.T) {
	b := newMockBackend()
	b.applyThemeErr = errors.New("disk write blocked")
	m := initLoadedModel(t, b, Options{StartTab: "themes"})

	beforeActive := m.ActiveTheme()
	m, _ = sendMsg(t, m, keyRune('j'))
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.panels[PanelWorkstation].MutationInFlight() {
		t.Fatalf("expected MutationInFlight while applying theme")
	}
	m = drainCmds(t, m, cmd)
	if m.ActiveTheme() != beforeActive {
		t.Fatalf("failed theme apply mutated ActiveTheme to %q", m.ActiveTheme())
	}
	if m.panels[PanelWorkstation].MutationInFlight() {
		t.Fatalf("expected MutationInFlight cleared after error")
	}
}

func TestCleanupDangerousUnselectedAndCancelDefault(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "cleanup"})

	sp := m.panels[PanelPackages].(packagesPanel)
	if sp.cleanSelected.Has("docker-vols") {
		t.Fatalf("dangerous cleanup item must never start selected")
	}
	if !sp.cleanSelected.Has("brew-cache") {
		t.Fatalf("expected safe default item brew-cache to start selected")
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.panels[PanelPackages].(packagesPanel).cleanSelected.Has("brew-cache") {
		t.Fatalf("expected space to toggle brew-cache off")
	}

	m, _ = sendAction(t, m, 'm')
	if !strings.Contains(ui.StripANSI(m.Render()), "EXECUTE") {
		t.Fatalf("expected EXECUTE badge after a+m")
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('x'))
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Confirm Cleanup Execution") {
		t.Fatalf("expected Confirm Cleanup Execution modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	calls := b.cleanupCalls
	b.mu.Unlock()
	if calls != 0 {
		t.Fatalf("expected 0 RunCleanup calls on Cancel default, got %d", calls)
	}

	m, cmd = sendMsg(t, m, keyRune('x'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	calls = b.cleanupCalls
	gotIDs := append([]string(nil), b.lastCleanIDs...)
	gotDry := b.lastCleanDry
	b.mu.Unlock()
	if calls != 1 || gotDry || len(gotIDs) != 1 || gotIDs[0] != "go-build" {
		t.Fatalf("expected 1 execute RunCleanup([go-build]), got calls=%d dry=%v ids=%v", calls, gotDry, gotIDs)
	}
}

func TestCleanupSudoUsesExecProcess(t *testing.T) {
	b := newMockBackend()
	b.cleanup = append(b.cleanup, CleanItem{
		ID: "safe:pacman-cache", Tier: "safe", Category: "pkg", Title: "Pacman cache", Bytes: 150_000_000, NeedsSudo: true,
	})
	m := initLoadedModel(t, b, Options{StartTab: "cleanup"})

	m, _ = sendMsg(t, m, keyRune('\\'))
	sp := m.panels[PanelPackages].(packagesPanel)
	for i, idx := range sp.cleanFiltered {
		if sp.cleanItems[idx].ID == "safe:pacman-cache" {
			sp.cleanCursor = i
			break
		}
	}
	m.panels[PanelPackages] = sp
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = sendAction(t, m, 'm')
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('x'))
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd for sudo cleanup target")
	}
	b.mu.Lock()
	execCalls := b.cleanupExecCalls
	bgCalls := b.cleanupCalls
	b.mu.Unlock()
	if execCalls != 1 || bgCalls != 0 {
		t.Fatalf("expected CleanupExecCmd=1 RunCleanup=0, got %d/%d", execCalls, bgCalls)
	}
}

func TestProjectsEnterSelectsRepoPath(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects", HasCDFile: true})

	if !strings.Contains(ui.StripANSI(m.Render()), "Add structured health checks") {
		t.Fatalf("expected unified split to show active repo PRs/Issues directly:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendAction(t, m, 'L')
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "✓ in ws.db") {
		t.Fatalf("expected local files manifest backup badge after a+L:\n%s", out)
	}
	m, _ = sendAction(t, m, 'L')

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('j'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected repo selection cmd on Enter")
	}
	selMsg := cmd()
	m, cmd = sendMsg(t, m, selMsg)
	if m.SelectedRepoPath() != "/tmp/example/personal/code/demo-web" {
		t.Fatalf("SelectedRepoPath = %q, want /tmp/example/personal/code/demo-web", m.SelectedRepoPath())
	}
	if cmd == nil {
		t.Fatalf("expected tea.Quit cmd after selecting repo")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("expected tea.QuitMsg after repo selection")
	}
}

func TestFleetAndDoctorAndQuitGuard(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "fleet"})

	var cmd tea.Cmd
	m, cmd = sendAction(t, m, 'Z')
	m = drainCmds(t, m, cmd)
	m, _ = sendAction(t, m, 's')
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	syncCalls := len(b.fleetSyncCalls)
	b.mu.Unlock()
	if syncCalls != 0 {
		t.Fatalf("expected Cancel default on Fleet sync modal, got %d calls", syncCalls)
	}
	m, _ = sendAction(t, m, 's')
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	syncCalls = len(b.fleetSyncCalls)
	b.mu.Unlock()
	if syncCalls != 1 {
		t.Fatalf("expected 1 ExecFleetSync call after confirm, got %d", syncCalls)
	}

	m, cmd = sendMsg(t, m, keyRune('o'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on 'o' in Fleet panel")
	}
	b.mu.Lock()
	connects := append([]string(nil), b.fleetConnects...)
	b.mu.Unlock()
	if len(connects) != 1 || connects[0] != "dev-vm-01" {
		t.Fatalf("expected FleetConnectCmd(dev-vm-01), got %v", connects)
	}

	m, _ = sendMsg(t, m, keyRune('6'))
	m, cmd = sendAction(t, m, 'b')
	m = drainCmds(t, m, cmd)
	m, cmd = sendAction(t, m, 'U')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Undo Last Transaction") {
		t.Fatalf("expected Undo confirmation modal after pressing a+U:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	uCalls := b.undoCalls
	b.mu.Unlock()
	if uCalls != 0 {
		t.Fatalf("expected Cancel default on Doctor Undo modal, got %d calls", uCalls)
	}
	m, cmd = sendAction(t, m, 'U')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	bkCalls := b.backupCalls
	uCalls = b.undoCalls
	b.mu.Unlock()
	if bkCalls != 1 || uCalls != 1 {
		t.Fatalf("expected 1 BackupDB and 1 UndoLast call, got %d/%d", bkCalls, uCalls)
	}

	m, cmd = sendAction(t, m, 'f')
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !m.hasMutationInFlight() {
		t.Fatalf("expected Doctor apply mutation in flight")
	}
	var qCmd tea.Cmd
	m, qCmd = sendMsg(t, m, keyRune('q'))
	if qCmd != nil {
		if _, isQuit := qCmd().(tea.QuitMsg); isQuit {
			t.Fatalf("expected 'q' blocked while mutation in flight")
		}
	}
	var ctrlCCmd tea.Cmd
	m, ctrlCCmd = sendMsg(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if ctrlCCmd == nil {
		t.Fatalf("expected ctrl+c to quit immediately")
	}
	if _, isQuit := ctrlCCmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("expected tea.QuitMsg on ctrl+c")
	}
}

func TestStylesWorkForAllRepoPalettesAndFallbacks(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := config.ListThemes(repoRoot)
	if err != nil {
		t.Fatalf("ListThemes: %v", err)
	}
	for _, id := range ids {
		pal, err := config.LoadPalette(repoRoot, id)
		if err != nil {
			t.Fatalf("LoadPalette(%s): %v", id, err)
		}
		st := NewStyles(pal)
		if st.ThemeID != id {
			t.Fatalf("NewStyles(%s).ThemeID = %q", id, st.ThemeID)
		}
		if sw := RenderSwatch(pal, false); lipgloss.Width(sw) == 0 {
			t.Fatalf("RenderSwatch(%s) returned empty output", id)
		}
	}

	partial := NewStyles(config.Palette{ID: "partial-light", Mode: "light", Tokens: map[string]string{}})
	if !partial.IsLight || partial.HeaderBrand.Render("ws") == "" {
		t.Fatalf("expected partial light palette fallback to render cleanly")
	}
}

func TestLightPaletteContrastSanity(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := config.ListThemes(repoRoot)
	if err != nil {
		t.Fatalf("ListThemes: %v", err)
	}

	requiredLight := map[string]bool{
		"everforest-light": false,
		"catppuccin-light": false,
		"tokyonight-light": false,
		"gruvbox-light":    false,
	}

	for _, id := range ids {
		if _, tracked := requiredLight[id]; tracked {
			requiredLight[id] = true
		}
		pal, err := config.LoadPalette(repoRoot, id)
		if err != nil {
			t.Fatalf("LoadPalette(%s): %v", id, err)
		}
		st := NewStyles(pal)

		hasColor := func(c color.Color) bool {
			if c == nil {
				return false
			}
			_, isNo := c.(lipgloss.NoColor)
			return !isNo
		}
		if hasColor(st.Panel.GetBackground()) || hasColor(st.PanelFocused.GetBackground()) || hasColor(st.KPIBox.GetBackground()) || hasColor(st.HeaderBar.GetBackground()) {
			t.Fatalf("%s: expected terminal-native transparent panel/chrome backgrounds (NoColor), got non-empty background", id)
		}
		for _, idx := range []int{
			st.Roles.Slots.RedIdx,
			st.Roles.Slots.GreenIdx,
			st.Roles.Slots.YellowIdx,
			st.Roles.Slots.BlueIdx,
			st.Roles.Slots.MagentaIdx,
			st.Roles.Slots.CyanIdx,
			st.Roles.Slots.AccentIdx,
		} {
			if idx == 0 || idx == 7 || idx == 8 || idx == 15 {
				t.Fatalf("%s: chose forbidden foreground ANSI slot %d", id, idx)
			}
		}
	}

	for id, seen := range requiredLight {
		if !seen {
			t.Fatalf("expected light palette %q in repo themes", id)
		}
	}
}

func TestToastAutoClearAndMonotonicID(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, statusToastMsg{text: "First toast"})
	if cmd == nil {
		t.Fatalf("expected auto-clear tick cmd on statusToastMsg")
	}
	id1 := m.toastID
	if m.Toast() != "First toast" {
		t.Fatalf("Toast() = %q, want First toast", m.Toast())
	}

	m, cmd = sendMsg(t, m, statusToastMsg{text: "Second toast", isErr: true})
	if cmd == nil {
		t.Fatalf("expected auto-clear tick cmd on second statusToastMsg")
	}
	id2 := m.toastID
	if id2 <= id1 {
		t.Fatalf("expected monotonic toastID: id2=%d <= id1=%d", id2, id1)
	}

	m, _ = sendMsg(t, m, toastClearMsg{id: id1})
	if m.Toast() != "Second toast" {
		t.Fatalf("stale toastClearMsg cleared active toast: %q", m.Toast())
	}

	m, _ = sendMsg(t, m, toastClearMsg{id: id2})
	if m.Toast() != "" || m.toastErr {
		t.Fatalf("matching toastClearMsg did not clear toast: text=%q err=%v", m.Toast(), m.toastErr)
	}
	if strings.Contains(ui.StripANSI(m.Render()), "Second toast") {
		t.Fatalf("cleared toast still rendered in view")
	}
}

type scopeSpyPanel struct {
	ResourcePanel
	scopes []string
}

func (s scopeSpyPanel) SetScope(zone string, backend Backend) (ResourcePanel, tea.Cmd) {
	s.scopes = append(s.scopes, zone)
	next, cmd := s.ResourcePanel.SetScope(zone, backend)
	s.ResourcePanel = next
	return s, cmd
}

func (s scopeSpyPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	next, cmd := s.ResourcePanel.Update(msg, backend, split, focusPreview)
	s.ResourcePanel = next
	return s, cmd
}

func (s scopeSpyPanel) SetSubView(idx int) ResourcePanel {
	s.ResourcePanel = s.ResourcePanel.SetSubView(idx)
	return s
}

func (s scopeSpyPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	next, cmd := s.ResourcePanel.OnSubViewChanged(backend)
	s.ResourcePanel = next
	return s, cmd
}

func (s scopeSpyPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	next, cmd := s.ResourcePanel.SetFilterQuery(q, backend)
	s.ResourcePanel = next
	return s, cmd
}

func (s scopeSpyPanel) OnBlur() ResourcePanel {
	s.ResourcePanel = s.ResourcePanel.OnBlur()
	return s
}

func (s scopeSpyPanel) ClearSelection() ResourcePanel {
	s.ResourcePanel = s.ResourcePanel.ClearSelection()
	return s
}

func (s scopeSpyPanel) Pop() ResourcePanel {
	s.ResourcePanel = s.ResourcePanel.Pop()
	return s
}

func TestGlobalZoneSwitchingCallsSetScopeAcrossPanels(t *testing.T) {
	b := newMockBackend()
	m := NewModel(b, Options{
		StartTab:    "projects",
		Zones:       []string{"personal", "work"},
		DefaultZone: "work",
	})

	m = drainCmds(t, m, m.Init())
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	b.mu.Lock()
	initRepos := append([]string(nil), b.repoZones...)
	initGH := append([]string(nil), b.ghZones...)
	initEnv := append([]string(nil), b.envZones...)
	b.mu.Unlock()
	if !slices.Contains(initRepos, "work") || !slices.Contains(initGH, "work") || !slices.Contains(initEnv, "work") {
		t.Fatalf("expected initial DefaultZone=work in Init loads; repos=%v gh=%v env=%v", initRepos, initGH, initEnv)
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "work") {
		t.Fatalf("expected active zone in K9s context header")
	}

	for i := range m.panels {
		m.panels[i] = scopeSpyPanel{ResourcePanel: m.panels[i]}
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('z'))
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "" {
		t.Fatalf("ScopeZone after cycling from work = %q, want \"\"", m.ScopeZone())
	}

	for i, p := range m.panels {
		spy := p.(scopeSpyPanel)
		if spy.IsZoned() {
			if len(spy.scopes) != 1 || spy.scopes[0] != "" {
				t.Fatalf("zoned panel %d expected SetScope(\"\"), got %v", i, spy.scopes)
			}
		} else if len(spy.scopes) != 0 {
			t.Fatalf("non-zoned panel %d should not receive SetScope, got %v", i, spy.scopes)
		}
	}

	m, cmd = sendMsg(t, m, keyRune('z'))
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "personal" {
		t.Fatalf("ScopeZone = %q, want personal", m.ScopeZone())
	}
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "personal") || !strings.Contains(out, "demo-web") || strings.Contains(out, "api-service") {
		t.Fatalf("expected personal scope to filter Projects to demo-web:\n%s", out)
	}

	m, _ = sendMsg(t, m, keyRune('/'))
	m, _ = sendMsg(t, m, keyRune('z'))
	if m.ScopeZone() != "personal" {
		t.Fatalf("pressing z inside filter input unexpectedly switched ScopeZone to %q", m.ScopeZone())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
}

func TestFleetUpperZTogglesNoZonesAndLowerZCyclesScope(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{
		StartTab: "fleet",
		Zones:    []string{"personal", "work"},
	})

	fpBefore := m.panels[PanelFleet].(fleetPanel)
	if !fpBefore.noZones {
		t.Fatalf("expected dev-vm-01 fixture to start with noZones=true")
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('z'))
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "personal" {
		t.Fatalf("expected lowercase z on Fleet to switch global zone to personal, got %q", m.ScopeZone())
	}
	if !m.panels[PanelFleet].(fleetPanel).noZones {
		t.Fatalf("lowercase z collided with Fleet --no-zones toggle")
	}

	m, cmd = sendAction(t, m, 'Z')
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "personal" {
		t.Fatalf("a+Z changed global ScopeZone to %q", m.ScopeZone())
	}
	if m.panels[PanelFleet].(fleetPanel).noZones {
		t.Fatalf("expected a+Z to toggle Fleet --no-zones to false")
	}
}

func TestPanelErrorSurfacing(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, ghItemsLoadedMsg{err: errors.New("gh auth expired")})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelProjects].Attention() != AttentionErr {
		t.Fatalf("expected PanelProjects AttentionErr on ghItemsLoadedMsg error")
	}
	if !strings.Contains(m.Toast(), "gh auth expired") || !m.toastErr {
		t.Fatalf("expected gh error toast, got %q (isErr=%v)", m.Toast(), m.toastErr)
	}
	m, cmd = sendMsg(t, m, keyRune(']'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendAction(t, m, 'g')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "gh auth expired") {
		t.Fatalf("expected ghItemsLoadedMsg error rendered in GH All Mine view:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('5'))
	m, cmd = sendMsg(t, m, fleetNodesLoadedMsg{err: errors.New("tailscale daemon down")})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelFleet].Attention() != AttentionErr {
		t.Fatalf("expected PanelFleet AttentionErr on fleetNodesLoadedMsg error")
	}
	if !strings.Contains(m.Toast(), "tailscale daemon down") || !m.toastErr {
		t.Fatalf("expected fleet load error toast, got %q", m.Toast())
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "tailscale daemon down") {
		t.Fatalf("expected fleetNodesLoadedMsg error rendered in Fleet panel")
	}

	m, cmd = sendMsg(t, m, fleetNodesLoadedMsg{nodes: b.fleetNodes})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, fleetJoinCmdLoadedMsg{node: "dev-vm-01", err: errors.New("invalid node tag")})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelFleet].Attention() != AttentionErr {
		t.Fatalf("expected PanelFleet AttentionErr on fleetJoinCmdLoadedMsg error")
	}
	if !strings.Contains(m.Toast(), "invalid node tag") || !m.toastErr {
		t.Fatalf("expected fleetJoinCmdLoadedMsg error toast, got %q", m.Toast())
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(ui.StripANSI(m.Render()), "invalid node tag") {
		t.Fatalf("expected fleetJoinCmdLoadedMsg error rendered in Fleet node detail:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('6'))
	m, cmd = sendMsg(t, m, dbStatusLoadedMsg{err: errors.New("ws.db key locked")})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelWorkstation].Attention() != AttentionErr {
		t.Fatalf("expected PanelWorkstation AttentionErr on dbStatusLoadedMsg error")
	}
	if m.panels[PanelEnv].Attention() != AttentionErr {
		t.Fatalf("expected PanelEnv AttentionErr on dbStatusLoadedMsg error")
	}
	if !strings.Contains(m.Toast(), "ws.db key locked") || !m.toastErr {
		t.Fatalf("expected dbStatusLoadedMsg error toast, got %q", m.Toast())
	}
	m, _ = sendMsg(t, m, keyRune('4'))
	m, _ = sendMsg(t, m, keyRune(']'))
	m, _ = sendMsg(t, m, keyRune(']'))
	if !strings.Contains(ui.StripANSI(m.Render()), "ws.db key locked") {
		t.Fatalf("expected dbStatusLoadedMsg error rendered in Env & Secrets Vault view:\n%s", ui.StripANSI(m.Render()))
	}
}

func TestSystemEnterPredictableAndSwatchPreview(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "themes"})

	m, cmd := sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected confirmRequestMsg cmd on Enter")
	}
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Apply Theme Palette (All Apps)") {
		t.Fatalf("expected Enter on Themes to open Apply Theme Palette (All Apps) confirmation modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected confirmRequestMsg cmd on Enter in Templates")
	}
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Apply Theme to ") {
		t.Fatalf("expected Enter in Templates to open single-app confirmation modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune('p'))
	prevOut := ui.StripANSI(m.panels[PanelWorkstation].ViewPreview(60, 18, true, false, m.Styles()))
	if !strings.Contains(prevOut, "██") || !strings.Contains(prevOut, "Palette:") {
		t.Fatalf("expected Themes preview pane to render swatches:\n%s", prevOut)
	}

	m, _ = sendMsg(t, m, keyRune(']'))
	if m.panels[PanelWorkstation].ActiveSubView() != wsSubPresets {
		t.Fatalf("expected ] to cycle Themes -> Presets, got %d", m.panels[PanelWorkstation].ActiveSubView())
	}
	m, _ = sendMsg(t, m, keyRune('['))
	if m.panels[PanelWorkstation].ActiveSubView() != wsSubThemes {
		t.Fatalf("expected [ to cycle Presets -> Themes, got %d", m.panels[PanelWorkstation].ActiveSubView())
	}
}

func TestActivityTrackingSpinnerAndPaneStatus(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	if m.activeOperations() != 0 {
		t.Fatalf("expected 0 activeOperations after init, got %d", m.activeOperations())
	}
	_, tickCmd := sendMsg(t, m, spinner.TickMsg{})
	if tickCmd != nil {
		t.Fatalf("expected spinner tick to stop when idle")
	}

	m, tickCmd = sendMsg(t, m, activityMsg{id: 42, label: "Refreshing index", done: false})
	if tickCmd == nil || m.activeOperations() != 1 {
		t.Fatalf("expected spinner tick and activeOperations=1 on activity start")
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "Refreshing index") {
		t.Fatalf("expected active activity label in footer CLI bar")
	}

	m, _ = sendMsg(t, m, activityMsg{id: 42, label: "Refreshing index", done: true})
	if m.activeOperations() != 0 {
		t.Fatalf("expected 0 activeOperations after activity done, got %d", m.activeOperations())
	}
	_, tickCmd = sendMsg(t, m, spinner.TickMsg{})
	if tickCmd != nil {
		t.Fatalf("expected spinner tick to stop once activity finishes")
	}

	wp := m.panels[PanelWorkstation].(workstationPanel)
	wp.doctor.applying = true
	m.panels[PanelWorkstation] = wp
	m.activePanel = PanelWorkstation
	if !strings.Contains(ui.StripANSI(m.Render()), "applying fix...") {
		t.Fatalf("expected active panel PaneStatus in frame:\n%s", ui.StripANSI(m.Render()))
	}
}

func TestProjectsGitHubHubFiltersPreviewAndCopy(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	var copied []string
	var opened []string
	oldClip := clipboardWrite
	oldOpen := urlOpener
	clipboardWrite = func(s string) error {
		copied = append(copied, s)
		return nil
	}
	urlOpener = func(u string) error {
		opened = append(opened, u)
		return nil
	}
	defer func() {
		clipboardWrite = oldClip
		urlOpener = oldOpen
	}()

	var cmd tea.Cmd
	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	if m.ActiveSplit() != SplitVertical || !m.FocusPreview() {
		t.Fatalf("expected a+P to focus GitHub Pull Requests in SplitVertical, got split=%v focus=%v", m.ActiveSplit(), m.FocusPreview())
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "example/api-service") || !strings.Contains(out, "#104") || !strings.Contains(out, "feat/health -> main") {
		t.Fatalf("expected enriched PR reader in right pane, got:\n%s", out)
	}
	if !strings.Contains(out, "☑ HTTP /readyz endpoint") {
		t.Fatalf("expected rendered Markdown task list in PR reader:\n%s", out)
	}

	for i := 0; i < 6; i++ {
		m, _ = sendMsg(t, m, keyRune('j'))
	}
	scrolled := ui.StripANSI(m.Render())
	if !strings.Contains(scrolled, "Recent Comments (1)") || !strings.Contains(scrolled, "Looks clean!") {
		t.Fatalf("expected scrolling PR reader to reveal comments section & body:\n%s", scrolled)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if strings.Contains(ui.StripANSI(m.Render()), "Preview ·") {
		t.Fatalf("expected Esc from PR reader to return to PRs & Issues table")
	}

	for _, k := range []rune{'y', 'Y'} {
		m, cmd = sendMsg(t, m, keyRune(k))
		m = drainCmds(t, m, cmd)
	}
	m, cmd = sendAction(t, m, 'c')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('o'))
	m = drainCmds(t, m, cmd)
	wantCopied := []string{
		"https://github.com/example/api-service/pull/104",
		"#104",
		"gh pr checkout 104 -R example/api-service",
	}
	if !slices.Equal(copied, wantCopied) {
		t.Fatalf("copied=%v, want %v", copied, wantCopied)
	}
	if len(opened) != 1 || opened[0] != "https://github.com/example/api-service/pull/104" {
		t.Fatalf("opened=%v, want [#104 URL]", opened)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if strings.Contains(ui.StripANSI(m.Render()), "#104") {
		t.Fatalf("expected a+t -> issue filter to hide PR #104:\n%s", ui.StripANSI(m.Render()))
	}

	m, cmd = sendAction(t, m, 'g')
	m = drainCmds(t, m, cmd)
	if out = ui.StripANSI(m.Render()); !strings.Contains(out, "All Mine") || !strings.Contains(out, "#22") {
		t.Fatalf("expected a+g to switch to All Mine and surface issue #22:\n%s", out)
	}

	m, cmd = sendAction(t, m, 'g')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('j'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	m, cmd = sendAction(t, m, 's')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "#18") {
		t.Fatalf("expected state=closed on demo-web to load PR #18:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitHorizontal {
		t.Fatalf("expected first 'p' from SplitVertical to switch to SplitHorizontal, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitOff || strings.Contains(ui.StripANSI(m.Render()), "Repo:") {
		t.Fatalf("expected second 'p' to hide preview split")
	}
}

func TestEnvDrillDownAndNoDuplicatePaths(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})

	out := ui.StripANSI(m.Render())
	if strings.Contains(out, "Blast Radius") {
		t.Fatalf("confusing 'Blast Radius' label must not appear in Env UI:\n%s", out)
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	drillOut := ui.StripANSI(m.Render())
	if !strings.Contains(drillOut, "Env Detail · very-long-service-api-key") ||
		!strings.Contains(drillOut, "Precedence Chain (2)") ||
		!strings.Contains(drillOut, "Other Checkouts Using very-long-service-api-key (1)") {
		t.Fatalf("expected Env Drill-Down with Precedence Chain and deduplicated Other Checkouts:\n%s", drillOut)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, cmd = sendAction(t, m, 'v')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "sk-live-revealed-9f2a") {
		t.Fatalf("expected a+v inside Other Checkouts to reveal its value:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, cmd = sendMsg(t, m, keyRune(']'))
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "Indexed Environment Files") {
		t.Fatalf("expected ']' on Env to switch to Files view:\n%s", ui.StripANSI(m.Render()))
	}
}

func TestFleetStructuredPreviewAndCopyJoinCmd(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "fleet"})

	var copied []string
	oldClip := clipboardWrite
	clipboardWrite = func(s string) error {
		copied = append(copied, s)
		return nil
	}
	defer func() { clipboardWrite = oldClip }()

	m, _ = sendMsg(t, m, keyRune('p'))
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "Node · dev-vm-01") ||
		!strings.Contains(out, "Tailscale Join Command") ||
		!strings.Contains(out, "Sync Plan") ||
		!strings.Contains(out, "Post-Sync Checklist") {
		t.Fatalf("expected Fleet preview split with join cmd, sync plan, and checklist:\n%s", out)
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('Y'))
	m = drainCmds(t, m, cmd)
	if len(copied) != 1 || !strings.Contains(copied[0], "tailscale up") {
		t.Fatalf("expected join command copied with 'Y', got %v", copied)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	if len(copied) != 2 || copied[1] != "claude auth login" {
		t.Fatalf("expected checklist command copied when preview focused, got %v", copied)
	}
}

func TestCommandPaletteNavigationAutocompleteHistoryAndExecution(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{
		StartTab: "env",
		Zones:    []string{"personal", "work"},
	})

	m, _ = sendMsg(t, m, keyRune(':'))
	if !m.CommandBarActive() {
		t.Fatalf("expected CommandBarActive after ':'")
	}
	m = typeString(t, m, "hi")
	if !strings.Contains(ui.StripANSI(m.Render()), ":hist") {
		t.Fatalf("expected command dropdown to match :hist:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.cmdBar.input.Value() != "hist" {
		t.Fatalf("expected Tab autocomplete to fill %q, got %q", "hist", m.cmdBar.input.Value())
	}
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubHistory {
		t.Fatalf("expected :history to open Workstation -> History, got panel=%d sub=%d", m.ActivePanel(), m.panels[PanelWorkstation].ActiveSubView())
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "prs repo:example/demo-web state:closed")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelProjects || !m.FocusPreview() {
		t.Fatalf("expected :prs to open unified Projects GH pane focused, got panel=%d focus=%v", m.ActivePanel(), m.FocusPreview())
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "#18") {
		t.Fatalf("expected :prs repo:example/demo-web state:closed to render PR #18:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "zone personal")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "personal" {
		t.Fatalf("expected :zone personal to set ScopeZone=personal, got %q", m.ScopeZone())
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "themes")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubThemes {
		t.Fatalf("expected :themes to switch to Workstation -> Themes, got panel=%d sub=%d", m.ActivePanel(), m.panels[PanelWorkstation].ActiveSubView())
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cmdBar.input.Value() != "themes" {
		t.Fatalf("expected Up in command bar to recall MRU %q, got %q", "themes", m.cmdBar.input.Value())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "!ws doctor")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelOutput {
		t.Fatalf("expected safe :!ws doctor to open Output Console, got panel %d", m.ActivePanel())
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "ok: executed ws doctor") {
		t.Fatalf("expected Output Console to show RunWSCommand output:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "!ws apply")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Confirm CLI Execution") {
		t.Fatalf("expected confirm modal for dangerous :!ws apply")
	}
	b.mu.Lock()
	callsBefore := len(b.wsCmdCalls)
	b.mu.Unlock()
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	callsAfterCancel := len(b.wsCmdCalls)
	b.mu.Unlock()
	if callsAfterCancel != callsBefore {
		t.Fatalf("expected bare Enter to cancel dangerous :!ws apply")
	}
	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "!ws apply")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	callsAfterYes := len(b.wsCmdCalls)
	b.mu.Unlock()
	if callsAfterYes != callsBefore+1 {
		t.Fatalf("expected 'y' to execute :!ws apply once, got delta %d", callsAfterYes-callsBefore)
	}
}

func TestFuzzyFilterLiveAndHistoryRecall(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "cask")
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "font-caskaydia-cove-nerd-font") || strings.Contains(out, "zsh-autosuggestions") {
		t.Fatalf("expected live fuzzy filter 'cask' to narrow packages:\n%s", out)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.CommandBarActive() {
		t.Fatalf("expected filter bar to close on Enter")
	}
	if m.panels[PanelPackages].FilterQuery() != "cask" {
		t.Fatalf("expected filter query 'cask' preserved after Enter")
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.panels[PanelPackages].FilterQuery() != "" {
		t.Fatalf("expected Esc to clear panel filter query")
	}
	m, _ = sendMsg(t, m, keyRune('/'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.panels[PanelPackages].FilterQuery() != "cask" {
		t.Fatalf("expected Up in filter bar to recall previous query 'cask', got %q", m.panels[PanelPackages].FilterQuery())
	}
}

func TestFlexiblePreviewSplitsFocusAndBorderlessCopy(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})

	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected Packages to start with SplitOff")
	}
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('p'))
	m = drainCmds(t, m, cmd)
	if m.ActiveSplit() != SplitVertical {
		t.Fatalf("expected 'p' at 120 cols to open SplitVertical, got %v", m.ActiveSplit())
	}
	splitOut := ui.StripANSI(m.Render())
	for _, want := range []string{"Package Inspector", "base-cli (packages.toml)", "zsh", "Source zsh-autosuggestions.zsh"} {
		if !strings.Contains(splitOut, want) {
			t.Fatalf("expected Package Inspector preview to include %q:\n%s", want, splitOut)
		}
	}

	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitHorizontal {
		t.Fatalf("expected second 'p' to cycle to SplitHorizontal, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected third 'p' to cycle to SplitOff, got %v", m.ActiveSplit())
	}
	m, cmd = sendMsg(t, m, keyRune('p'))
	m = drainCmds(t, m, cmd)
	if m.ActiveSplit() != SplitVertical {
		t.Fatalf("expected 'p' to reopen SplitVertical, got %v", m.ActiveSplit())
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.FocusPreview() {
		t.Fatalf("expected Tab to switch focus to preview")
	}

	m, _ = sendMsg(t, m, keyRune('='))
	if !m.Zoomed() {
		t.Fatalf("expected '=' to enable zoom")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.Zoomed() {
		t.Fatalf("expected Esc to exit zoom")
	}
}

func TestMultiSelectBatchActions(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if m.panels[PanelProjects].SelectedCount() != 2 {
		t.Fatalf("expected 2 repos selected, got %d", m.panels[PanelProjects].SelectedCount())
	}
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('u'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	pulls := append([][]string(nil), b.pulledPaths...)
	b.mu.Unlock()
	if len(pulls) != 1 || len(pulls[0]) != 2 ||
		pulls[0][0] != "/tmp/example/work/code/api-service" ||
		pulls[0][1] != "/tmp/example/personal/code/demo-web" {
		t.Fatalf("expected PullSelectedRepos called with both selected paths, got %+v", pulls)
	}
	if len(m.OutputPanel().runs) == 0 {
		t.Fatalf("expected repo pull to record run in Output Console")
	}

	m, _ = sendMsg(t, m, keyRune('3'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, cmd = sendMsg(t, m, keyRune('x'))
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Confirm Package REMOVE") {
		t.Fatalf("expected Confirm Package REMOVE modal")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd for batch package remove")
	}
	b.mu.Lock()
	execActions := append([]string(nil), b.pkgActionExecCalls...)
	bgActions := append([]string(nil), b.pkgActions...)
	b.mu.Unlock()
	if len(execActions) != 1 || execActions[0] != "remove:zsh-autosuggestions,font-caskaydia-cove-nerd-font" || len(bgActions) != 0 {
		t.Fatalf("expected batch remove PackageActionExecCmd with 0 background calls, got exec=%v bg=%v", execActions, bgActions)
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "remove", command: "ws pkg remove"})
	m = drainCmds(t, m, cmd)

	m, _ = sendMsg(t, m, keyRune('4'))
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	m = typeString(t, m, "sk-rotated-worker-only")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	rotates := append([]EnvRotateReq(nil), b.rotateExecs...)
	b.mu.Unlock()
	if len(rotates) != 1 || len(rotates[0].Paths) != 1 || rotates[0].Paths[0] != "~/work/code/worker/.env" {
		t.Fatalf("expected marked worker path in ExecEnvRotate, got %+v", rotates)
	}

	var copied []string
	oldClip := clipboardWrite
	clipboardWrite = func(s string) error {
		copied = append(copied, s)
		return nil
	}
	defer func() { clipboardWrite = oldClip }()

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "history")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "exit:err")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = sendMsg(t, m, keyRune('*'))
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	wantJoined := "go test ./internal/api\nnpm run lint"
	if len(copied) != 1 || copied[0] != wantJoined {
		t.Fatalf("expected history multi-copy %q, got %v", wantJoined, copied)
	}
}

func TestOutputConsoleRunSwitchingSearchWrapAndCopy(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	var copied []string
	oldClip := clipboardWrite
	clipboardWrite = func(s string) error {
		copied = append(copied, s)
		return nil
	}
	defer func() { clipboardWrite = oldClip }()

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "!ws status")
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "!ws doctor")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	op := m.OutputPanel()
	if len(op.runs) != 2 || op.activeIdx != 1 {
		t.Fatalf("expected 2 runs with activeIdx=1, got len=%d idx=%d", len(op.runs), op.activeIdx)
	}

	m, _ = sendMsg(t, m, keyRune('['))
	if !strings.Contains(ui.StripANSI(m.Render()), "ws status") {
		t.Fatalf("expected '[' to switch Output Console to first run")
	}
	m, _ = sendMsg(t, m, keyRune(']'))
	if !strings.Contains(ui.StripANSI(m.Render()), "ws doctor") {
		t.Fatalf("expected ']' to switch Output Console to second run")
	}

	if !m.OutputPanel().wrap {
		t.Fatalf("expected Output Console to start with line wrapping enabled")
	}
	m, _ = sendMsg(t, m, keyRune('w'))
	if m.OutputPanel().wrap {
		t.Fatalf("expected 'w' to disable line wrapping in Output Console")
	}
	m, _ = sendMsg(t, m, keyRune('w'))
	if !m.OutputPanel().wrap {
		t.Fatalf("expected second 'w' to re-enable line wrapping in Output Console")
	}
	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "healthy")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('Y'))
	m = drainCmds(t, m, cmd)
	wantCopied := []string{"line-2: healthy", "ws doctor"}
	if !slices.Equal(copied, wantCopied) {
		t.Fatalf("copied=%v, want %v", copied, wantCopied)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.ActivePanel() != PanelProjects {
		t.Fatalf("expected hierarchical Esc on Output Console to return to prevPanel (Projects), got %d", m.ActivePanel())
	}
}

func TestProjectsEnterWithoutCDFileOpensRepoDetail(t *testing.T) {
	t.Setenv("HOME", "/tmp/example")
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects", HasCDFile: false})

	m, _ = sendMsg(t, m, keyRune('j'))
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		msg := cmd()
		if _, isSel := msg.(repoSelectedMsg); isSel {
			t.Fatalf("expected no repoSelectedMsg when HasCDFile=false")
		}
		if _, isQuit := msg.(tea.QuitMsg); isQuit {
			t.Fatalf("expected no tea.QuitMsg when HasCDFile=false")
		}
		m, _ = sendMsg(t, m, msg)
	}
	if m.SelectedRepoPath() != "" {
		t.Fatalf("SelectedRepoPath = %q, want empty when HasCDFile=false", m.SelectedRepoPath())
	}
	pp := m.panels[PanelProjects].(projectsPanel)
	if !pp.repoInfoMode || !m.FocusPreview() {
		t.Fatalf("expected Enter without HasCDFile to open Repo Detail and focus preview, repoInfoMode=%v focusPreview=%v", pp.repoInfoMode, m.FocusPreview())
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "Repo · demo-web") {
		t.Fatalf("expected Repo Detail for demo-web, got:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatalf("expected Esc from Repo Detail to pop back without quitting")
		}
	}
	pp = m.panels[PanelProjects].(projectsPanel)
	if pp.repoInfoMode || m.FocusPreview() {
		t.Fatalf("expected Esc to return from Repo Detail to Repos list, repoInfoMode=%v focusPreview=%v", pp.repoInfoMode, m.FocusPreview())
	}
}

func TestNewTablesAndMarkdownNoColorAndTransparentBG(t *testing.T) {
	b := newMockBackend()
	tabs := []string{"ai", "env", "projects", "fleet", "system", "history", "doctor", "output"}

	renderView := func(start string) (Model, string) {
		m := initLoadedModel(t, b, Options{StartTab: start})
		if start == "projects" {
			var cmd tea.Cmd
			m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			m = drainCmds(t, m, cmd)
		}
		if start == "output" {
			m, _ = sendMsg(t, m, commandRunRecordedMsg{
				OpenOutput: true,
				Run: CommandRun{
					ID:      1,
					Command: "ws doctor",
					Lines:   []string{"ok: all checks passed"},
				},
			})
		}
		return m, m.Render()
	}

	for _, start := range tabs {
		t.Run("color/"+start, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			m, out := renderView(start)
			st := m.Styles()
			if st.RowSelected.GetReverse() || st.RowSelectedMarked.GetReverse() || st.TabActive.GetReverse() {
				t.Fatalf("%s unexpectedly uses harsh Reverse(true) highlight", start)
			}
			if _, ok := st.Panel.GetBackground().(lipgloss.NoColor); !ok {
				t.Fatalf("%s panel chrome unexpectedly paints background", start)
			}
			if _, ok := st.RowMarked.GetBackground().(lipgloss.NoColor); !ok {
				t.Fatalf("%s marked row unexpectedly paints background", start)
			}
			if strings.Contains(out, "\x1b[7m") {
				t.Fatalf("%s emitted forbidden reverse-video ANSI sequence \\x1b[7m", start)
			}
		})
		t.Run("nocolor/"+start, func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			m := initLoadedModel(t, b, Options{StartTab: start})
			if start == "projects" {
				var cmd tea.Cmd
				m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
				m = drainCmds(t, m, cmd)
			}
			if start == "output" {
				m, _ = sendMsg(t, m, commandRunRecordedMsg{
					OpenOutput: true,
					Run: CommandRun{
						ID:      1,
						Command: "ws doctor",
						Lines:   []string{"ok: all checks passed"},
					},
				})
			}
			if out := m.Render(); strings.Contains(out, "\x1b") {
				t.Fatalf("%s under NO_COLOR=1 emitted ANSI escape: %q", start, out)
			}

			m, _ = sendMsg(t, m, keyRune(':'))
			if cmdOut := m.Render(); strings.Contains(cmdOut, "\x1b") {
				t.Fatalf("%s command palette under NO_COLOR=1 emitted ANSI escape: %q", start, cmdOut)
			}
		})
	}
}

func TestK9sRowHighlightSelectionNoBracketColumn(t *testing.T) {
	b := newMockBackend()
	for _, start := range []string{"projects", "env", "pkgs", "cleanup", "history", "doctor"} {
		t.Run(start, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			m := initLoadedModel(t, b, Options{StartTab: start})
			if start == "cleanup" {

				m, _ = sendMsg(t, m, keyRune('\\'))
			}
			m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			raw := m.Render()
			out := ui.StripANSI(raw)
			if strings.Contains(out, "[ ]") || strings.Contains(out, "[x]") {
				t.Fatalf("%s unexpectedly rendered bracket checkbox artifacts:\n%s", start, out)
			}
			if strings.Contains(out, "│ Sel ") || strings.Contains(out, "───┼───") {
				t.Fatalf("%s unexpectedly rendered table borders or Sel column:\n%s", start, out)
			}
			if !strings.Contains(out, ">* ") {
				t.Fatalf("%s expected selected+marked row indicator '>* ':\n%s", start, out)
			}

			m, _ = sendMsg(t, m, keyRune('j'))
			movedOut := ui.StripANSI(m.Render())
			if !strings.Contains(movedOut, "* ") {
				t.Fatalf("%s expected marked row indicator '* ' after moving cursor away:\n%s", start, movedOut)
			}
			if strings.Contains(m.Render(), "\x1b[7m") {
				t.Fatalf("%s rendered forbidden reverse-video highlight", start)
			}
		})
	}
}

func TestK9sHeaderNoTextOverlapOrCollision(t *testing.T) {
	b := newMockBackend()
	keyDescCollisionRe := regexp.MustCompile(`<[^>]+>[^ \n│]`)
	sizes := []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 100, Height: 30},
		{Width: 120, Height: 40},
		{Width: 160, Height: 45},
	}

	for _, sz := range sizes {
		for _, k := range []rune{'1', '2', '3', '4', '5', '6', '7', '0'} {
			m := initLoadedModel(t, b, Options{})
			m, _ = sendMsg(t, m, sz)
			m, _ = sendMsg(t, m, keyRune(k))
			out := ui.StripANSI(m.Render())
			lines := strings.Split(out, "\n")
			headCount := m.headerLines(sz.Height)
			header := strings.Join(lines[:min(headCount, len(lines))], "\n")
			if strings.Contains(header, "Health:") && !strings.Contains(header, "Health: ") {
				t.Fatalf("panel %c at %dx%d smashed Health: value without space:\n%s", k, sz.Width, sz.Height, header)
			}
			if loc := keyDescCollisionRe.FindString(header); loc != "" {
				t.Fatalf("panel %c at %dx%d collided hint key and description (%q):\n%s", k, sz.Width, sz.Height, loc, header)
			}
		}
	}
}

func TestProjectsCWDAutoSelectAndInlineRepoSwitcher(t *testing.T) {
	b := newMockBackend()
	b.host.CWDRepoPath = "/tmp/example/personal/code/demo-web"
	b.host.CWDRepoSlug = "example/demo-web"
	b.host.CWDBranch = "feat/18-scaffold"
	b.host.CWDIssueHint = 18
	b.ghRepoItems["example/demo-web|open"] = []GHItemRow{
		{Kind: "issue", Repo: "example/demo-web", Number: 22, Title: "Improve mobile spacing", State: "open", Zone: "personal"},
		{Kind: "pr", Repo: "example/demo-web", Number: 18, Title: "Scaffold header fix", State: "open", HeadRef: "feat/18-scaffold", Zone: "personal"},
	}

	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "Repo: example/demo-web (cwd)") || !strings.Contains(out, "Scaffold header fix") || !strings.Contains(out, "★ #18") {
		t.Fatalf("expected CWD auto-selected demo-web unified split with branch PR ★ #18:\n%s", out)
	}

	m, _ = sendAction(t, m, 'L')
	if !strings.Contains(ui.StripANSI(m.Render()), "Repo · demo-web") || !strings.Contains(ui.StripANSI(m.Render()), "Untracked Local Files Manifest") {
		t.Fatalf("expected a+L to open demo-web local files manifest:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendAction(t, m, 'L')

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('R'))
	if m.panels[PanelProjects].CapturingInput() || strings.Contains(ui.StripANSI(m.Render()), "Repo>") {
		t.Fatalf("expected 'R' not to open removed inline Repo Switcher")
	}
	m, cmd = sendAction(t, m, 'g')
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "Repo: All Mine") {
		t.Fatalf("expected All Mine after pressing a+g:\n%s", ui.StripANSI(m.Render()))
	}

	m, cmd = sendMsg(t, m, keyRune('<'))
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "example/demo-web") && !strings.Contains(ui.StripANSI(m.Render()), "example/api-service") {
		t.Fatalf("expected '<' to cycle repo scope directly inside unified GitHub pane")
	}
}

func TestNeovimKeyBindingsAcrossPanels(t *testing.T) {
	b := newMockBackend()

	b.historyRows = nil
	for i := 0; i < 30; i++ {
		b.historyRows = append(b.historyRows, HistoryRow{
			Timestamp: time.Date(2026, 11, 22, 10, i, 0, 0, time.UTC),
			Shell:     "zsh",
			Zone:      "work",
			Command:   fmt.Sprintf("cmd-%02d", i),
		})
	}
	m := initLoadedModel(t, b, Options{StartTab: "history"})

	sp := m.panels[PanelWorkstation].(workstationPanel)
	if sp.histCursor != 0 {
		t.Fatalf("expected initial histCursor=0")
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	afterHalf := m.panels[PanelWorkstation].(workstationPanel).histCursor
	if afterHalf <= 1 {
		t.Fatalf("expected ctrl+d to advance half-page, got cursor=%d", afterHalf)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := m.panels[PanelWorkstation].(workstationPanel).histCursor; got >= afterHalf {
		t.Fatalf("expected ctrl+u to move back up from %d, got %d", afterHalf, got)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if got := m.panels[PanelWorkstation].(workstationPanel).histCursor; got < 10 {
		t.Fatalf("expected ctrl+f to advance full page, got %d", got)
	}
	m, _ = sendMsg(t, m, keyRune('G'))
	if got := m.panels[PanelWorkstation].(workstationPanel).histCursor; got != 29 {
		t.Fatalf("expected G to jump to last row 29, got %d", got)
	}
	m, _ = sendMsg(t, m, keyRune('g'))
	m, _ = sendMsg(t, m, keyRune('g'))
	if got := m.panels[PanelWorkstation].(workstationPanel).histCursor; got != 0 {
		t.Fatalf("expected gg to jump back to top row 0, got %d", got)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	m, _ = sendMsg(t, m, keyRune('v'))
	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected removed Ctrl+w v to be a no-op, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitVertical {
		t.Fatalf("expected first 'p' to open SplitVertical, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.FocusPreview() {
		t.Fatalf("expected Tab to focus preview pane")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.FocusPreview() {
		t.Fatalf("expected second Tab to focus main pane")
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitHorizontal {
		t.Fatalf("expected second 'p' to cycle to SplitHorizontal, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected third 'p' to close split preview, got %v", m.ActiveSplit())
	}

	m, _ = sendMsg(t, m, keyRune('2'))
	if m.ActivePanel() != PanelProjects {
		t.Fatalf("expected switch to PanelProjects")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.ActivePanel() != PanelWorkstation {
		t.Fatalf("expected Ctrl+o to jump back to PanelWorkstation, got %d", m.ActivePanel())
	}
}

func TestBorderlessTablesAndWeightedColumnWidths(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	st := NewStyles(DefaultPalette())

	tblHeaders := []string{"On", "Tool", "Kind", "Plan", "Zone", "Version", "Updated", "Status"}
	tblRows := [][]string{
		{"●", "neovim", "brew", "stable", "personal", "0.10.2", "today", "ok"},
		{"●", "ghostty-terminal", "cask", "latest", "personal", "1.1.0", "2d ago", "ok"},
	}
	tblLines := renderSelectableTable(tblHeaders, tblRows, 0, nil, true, 240, 10, st)
	for i, ln := range tblLines {
		plain := ui.StripANSI(ln)
		if strings.Contains(plain, " │ ") || strings.Contains(plain, "───") {
			t.Fatalf("table line %d rendered forbidden border divider: %q", i, plain)
		}
	}
	colW := computeTableColWidths(append([]string{"  " + tblHeaders[0]}, tblHeaders[1:]...), tblRows, 240, 2)

	for _, ci := range []int{0, 2, 3, 4, 7} {
		if colW[ci] > 16 {
			t.Fatalf("compact column %q bloated to width %d on ultrawide 240-col table (colW=%v)", tblHeaders[ci], colW[ci], colW)
		}
	}

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 240, Height: 40})

	m, _ = sendMsg(t, m, keyRune('p'))
	out := ui.StripANSI(m.Render())
	for _, ln := range strings.Split(out, "\n") {
		if strings.HasPrefix(ln, "│ ") && strings.HasSuffix(ln, " │") {
			inner := strings.TrimSuffix(strings.TrimPrefix(ln, "│ "), " │")
			if strings.Contains(inner, "│") {
				t.Fatalf("expected borderless Repos table inside panel walls, found pipe in: %q", ln)
			}
		}
	}
	var tableHdrLine string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "Repo") && strings.Contains(ln, "Zone") && strings.Contains(ln, "Branch") && strings.Contains(ln, "Path") {
			tableHdrLine = ln
			break
		}
	}
	if tableHdrLine == "" {
		t.Fatalf("expected Repos table header line at 240x40:\n%s", out)
	}
	zoneIdx := strings.Index(tableHdrLine, "Zone")
	branchIdx := strings.Index(tableHdrLine, "Branch")
	if branchIdx <= zoneIdx || branchIdx-zoneIdx > 16 {
		t.Fatalf("Zone column stretched excessively at 240 cols (gap=%d, line=%q)", branchIdx-zoneIdx, tableHdrLine)
	}
}

func TestK9sHeaderCompactWithoutUltrawideDeadSpace(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 240, Height: 45})
	out := ui.StripANSI(m.Render())
	lines := strings.Split(out, "\n")
	headN := m.headerLines(45)
	headerLines := lines[:min(headN, len(lines))]
	header := strings.Join(headerLines, "\n")

	for _, want := range []string{"Host:", "Zone:", "Theme:"} {
		if !strings.Contains(header, want) {
			t.Fatalf("expected separate %q label in ultrawide K9s header:\n%s", want, header)
		}
	}

	firstLine := strings.TrimRight(headerLines[0], " ")
	if strings.Contains(firstLine, strings.Repeat(" ", 20)) {
		t.Fatalf("expected compact adjacent header columns without dead space gap in row 0: %q", firstLine)
	}
}

func TestUnifiedProjectsZeroAPIFloodOnRepoScroll(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	b.mu.Lock()
	b.ghRepoCalls = nil
	b.ghCachedCalls = nil
	b.mu.Unlock()

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('j'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('k'))
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	repoCallsAfterScroll := len(b.ghRepoCalls)
	cachedCallsAfterScroll := len(b.ghCachedCalls)
	b.mu.Unlock()
	if repoCallsAfterScroll != 0 {
		t.Fatalf("expected scrolling Repos to trigger 0 GH API calls (cached-only), got %d: %v", repoCallsAfterScroll, b.ghRepoCalls)
	}
	if cachedCallsAfterScroll < 1 {
		t.Fatalf("expected scrolling Repos to read SQLite cache on unvisited repo, got %d calls", cachedCallsAfterScroll)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('r'))
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	refreshes := append([]bool(nil), b.ghRepoRefreshes...)
	b.mu.Unlock()
	foundManualRefresh := false
	for _, ref := range refreshes {
		if ref {
			foundManualRefresh = true
			break
		}
	}
	if !foundManualRefresh {
		t.Fatalf("expected manual 'r' on GH pane to invoke LoadRepoGHItems with refresh=true, got %v", refreshes)
	}
}

func TestCommandPaletteCenteredOverlayDimmingTinyFallbackAndControls(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{
		StartTab: "env",
		Zones:    []string{"personal", "work"},
	})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m, _ = sendMsg(t, m, keyRune(':'))
	raw := m.Render()
	lines := strings.Split(raw, "\n")
	if len(lines) > 40 {
		t.Fatalf("rendered height=%d > 40", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w > 120 {
			t.Fatalf("line %d width=%d > 120: %q", i, w, ln)
		}
	}
	plain := ui.StripANSI(raw)
	for _, want := range []string{"Command Palette", "[view]", ":overview (or :1)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected centered Command Palette to contain %q:\n%s", want, plain)
		}
	}

	foundCenteredOverlay := false
	for _, ln := range strings.Split(plain, "\n") {
		if strings.HasPrefix(ln, "│") {
			if idx := strings.Index(ln, "╭─ Command Palette"); idx > 4 {
				foundCenteredOverlay = true
				break
			}
		}
	}
	if !foundCenteredOverlay {
		t.Fatalf("expected horizontally centered Command Palette over dimmed stage backdrop:\n%s", plain)
	}

	m = typeString(t, m, "pull")
	pullPlain := ui.StripANSI(m.Render())
	if !strings.Contains(pullPlain, "[confirm]") || !strings.Contains(pullPlain, "prompts before running") {
		t.Fatalf("expected filtered :pull row to show [confirm] badge and safety footer:\n%s", pullPlain)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = sendMsg(t, m, keyRune(':'))

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cmdBar.matchIdx != 1 {
		t.Fatalf("expected Down to advance matchIdx=1, got %d", m.cmdBar.matchIdx)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.cmdBar.matchIdx != 2 {
		t.Fatalf("expected Ctrl+N to advance matchIdx=2, got %d", m.cmdBar.matchIdx)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.cmdBar.matchIdx != 1 {
		t.Fatalf("expected Ctrl+P to move matchIdx=1, got %d", m.cmdBar.matchIdx)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.cmdBar.matchIdx != 0 {
		t.Fatalf("expected Up to return matchIdx=0, got %d", m.cmdBar.matchIdx)
	}

	m.cmdBar.recordCommand("themes gruvbox-dark")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if m.cmdBar.input.Value() != "themes gruvbox-dark" {
		t.Fatalf("expected Ctrl+R to recall MRU command, got %q", m.cmdBar.input.Value())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt})
	if m.cmdBar.input.Value() != "" {
		t.Fatalf("expected Alt+Down past newest history to restore draft, got %q", m.cmdBar.input.Value())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt})
	if m.cmdBar.input.Value() != "themes gruvbox-dark" {
		t.Fatalf("expected Alt+Up to recall MRU command, got %q", m.cmdBar.input.Value())
	}

	if got := runePrefixGhostSuffix([]string{"café-list"}, "caf"); got != "é-list" {
		t.Fatalf("expected UTF-8 ghost suffix %q, got %q", "é-list", got)
	}
	_ = runePrefixGhostSuffix([]string{"İnfo"}, "i")

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 36, Height: 14})
	m, _ = sendMsg(t, m, keyRune(':'))
	tinyRaw := m.Render()
	for i, ln := range strings.Split(tinyRaw, "\n") {
		if w := ansi.StringWidth(ln); w > 36 {
			t.Fatalf("tiny rendered line %d overflowed width 36 (%d): %q", i, w, ln)
		}
	}
	if !strings.Contains(ui.StripANSI(tinyRaw), "Command Palette") {
		t.Fatalf("expected Command Palette visible on tiny 36x14 terminal:\n%s", ui.StripANSI(tinyRaw))
	}
}

func TestFilterBarEscRestoresPriorDraft(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "fzf")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "cask")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.panels[PanelPackages].FilterQuery(); got != "cask" {
		t.Fatalf("expected committed filter %q, got %q", "cask", got)
	}

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "-extra")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.CommandBarActive() {
		t.Fatalf("expected filter bar closed on Esc")
	}
	if got := m.panels[PanelPackages].FilterQuery(); got != "cask" {
		t.Fatalf("expected Esc in '/' bar to restore openDraft %q, got %q", "cask", got)
	}
}

func TestTableSGRTrueColorAndUniformSelectableAlignment(t *testing.T) {
	sgrCases := []struct {
		params string
		wantBG bool
		wantFG bool
	}{
		{"38;2;255;0;128", false, false},
		{"38;5;49", false, false},
		{"48;2;0;39;0", false, false},
		{"38:2::0:39:49", false, false},
		{"0", true, true},
		{"49", true, false},
		{"39", false, true},
		{"1;38;2;0;49;39;49", true, false},
	}
	for _, tc := range sgrCases {
		gotBG, gotFG := inspectSGRResets(tc.params)
		if gotBG != tc.wantBG || gotFG != tc.wantFG {
			t.Fatalf("inspectSGRResets(%q) = (%v,%v), want (%v,%v)", tc.params, gotBG, gotFG, tc.wantBG, tc.wantFG)
		}
	}

	st := NewStyles(DefaultPalette())
	rows := [][]string{
		{"alpha", "1"},
		{"beta", "2"},
		{"gamma", "3"},
		{"delta", "4"},
	}
	out := renderSelectableTable([]string{"Name", "Val"}, rows, 0, map[int]bool{0: true, 1: true}, true, 40, 10, st)
	if len(out) != 5 {
		t.Fatalf("expected 5 table lines, got %d", len(out))
	}
	expectedCol := 3
	for i, name := range []string{"Name", "alpha", "beta", "gamma", "delta"} {
		plain := ui.StripANSI(out[i])
		if idx := strings.Index(plain, name); idx != expectedCol {
			t.Fatalf("line %d (%q) aligned %q at col %d, want %d", i, plain, name, idx, expectedCol)
		}
	}

	out2 := renderSelectableTable([]string{"Name", "Val"}, rows, 2, map[int]bool{0: true, 1: true}, true, 40, 10, st)
	for i, name := range []string{"Name", "alpha", "beta", "gamma", "delta"} {
		plain := ui.StripANSI(out2[i])
		if idx := strings.Index(plain, name); idx != expectedCol {
			t.Fatalf("line %d (%q) aligned %q at col %d, want %d", i, plain, name, idx, expectedCol)
		}
	}
}

func TestCommandHardeningBorderlessIsolationAndPreviewIdentity(t *testing.T) {
	safeCases := []struct {
		args []string
		want bool
	}{
		{[]string{"doctor"}, true},
		{[]string{"--zone", "work", "apply"}, false},
		{[]string{"apply", "--dry-run"}, true},
		{[]string{"unknown-plugin", "--dry-run"}, false},
		{[]string{"pkg", "check"}, true},
		{[]string{"env", "rotate"}, false},
	}
	for _, tc := range safeCases {
		if got := isSafeWSSubcommand(tc.args); got != tc.want {
			t.Fatalf("isSafeWSSubcommand(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{
		StartTab: "projects",
		Zones:    []string{"personal", "work"},
	})
	var cmd tea.Cmd

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "zone nonexistent")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ScopeZone() != "" || !strings.Contains(m.Toast(), "Unknown zone") {
		t.Fatalf("expected invalid zone rejected with toast, got scope=%q toast=%q", m.ScopeZone(), m.Toast())
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "scan env")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	op := m.panels[PanelOutput].(outputPanel)
	if len(op.runs) == 0 || op.runs[len(op.runs)-1].Running {
		t.Fatalf("expected :scan env to complete its Output Console run: %+v", op.runs)
	}

	b.mu.Lock()
	b.pulledPaths = nil
	b.mu.Unlock()
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "pull")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() {
		t.Fatalf("expected :pull to open confirmation modal")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	pulls := append([][]string(nil), b.pulledPaths...)
	b.mu.Unlock()
	if len(pulls) != 1 || len(pulls[0]) != 1 || pulls[0][0] != b.repos[0].LocalPath {
		t.Fatalf("expected :pull with 1 selected repo to pull %q, got %+v", b.repos[0].LocalPath, pulls)
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	if !strings.Contains(ui.StripANSI(m.Render()), "Command Palette") {
		t.Fatalf("expected Command Palette visible")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = sendMsg(t, m, keyRune('?'))
	if !strings.Contains(ui.StripANSI(m.Render()), "GLOBAL VERBS") {
		t.Fatalf("expected Help modal visible")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	st := NewStyles(DefaultPalette())
	var fpR ResourcePanel = newFleetPanel()
	fpR, _ = fpR.Update(fleetNodesLoadedMsg{nodes: b.fleetNodes}, nil, SplitVertical, false)
	fpR, _ = fpR.Update(fleetJoinCmdLoadedMsg{node: "other-node", cmd: "stale-join-cmd"}, nil, SplitVertical, false)
	fpR, _ = fpR.Update(fleetSyncPreviewMsg{node: "other-node", preview: FleetSyncPreview{Summary: "STALE SYNC SUMMARY"}}, nil, SplitVertical, false)
	fPrev := ui.StripANSI(fpR.ViewPreview(80, 24, true, false, st))
	if strings.Contains(fPrev, "stale-join-cmd") || strings.Contains(fPrev, "STALE SYNC SUMMARY") {
		t.Fatalf("expected fleet ViewPreview to reject stale node messages:\n%s", fPrev)
	}

	var epR ResourcePanel = newEnvPanel()
	epR, _ = epR.Update(envVarsLoadedMsg{vars: b.envVars}, nil, SplitVertical, false)
	epR, _ = epR.Update(envDetailLoadedMsg{
		varKey: "WRONG_OTHER_KEY",
		detail: EnvDetail{VarKey: "WRONG_OTHER_KEY", Layers: []EnvLayerRow{{SourcePath: "/tmp/stale-other.env"}}},
	}, nil, SplitVertical, false)
	ePrev := ui.StripANSI(epR.ViewPreview(80, 24, true, false, st))
	if strings.Contains(ePrev, "/tmp/stale-other.env") {
		t.Fatalf("expected env ViewPreview to hide mismatched VarKey layers:\n%s", ePrev)
	}
}

func TestCommandPaletteNumberedViewsAndWrappedFooter(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 88, Height: 30})

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "pull")
	palOut := ui.StripANSI(m.Render())
	if !strings.Contains(palOut, "prompts before running") || !strings.Contains(palOut, "keys: tab fill") || !strings.Contains(palOut, "enter run") {
		t.Fatalf("expected wrapped Command Palette footer notes completely visible:\n%s", palOut)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	viewCases := []struct {
		cmd       string
		wantPanel int
	}{
		{"1", PanelOverview},
		{"2", PanelProjects},
		{"3", PanelPackages},
		{"4", PanelEnv},
		{"5", PanelFleet},
		{"6", PanelWorkstation},
		{"0", PanelOutput},
	}
	for _, tc := range viewCases {
		m, _ = sendMsg(t, m, keyRune(':'))
		m = typeString(t, m, tc.cmd)
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.wantPanel {
			t.Fatalf("command :%s switched to panel %d, want %d", tc.cmd, m.ActivePanel(), tc.wantPanel)
		}
	}
}

func TestSystemThemesSingleAndAllAppsAndCleanupWrappedPaths(t *testing.T) {
	b := newMockBackend()
	b.cleanup = []CleanItem{
		{
			ID:                "backups",
			Tier:              "safe",
			Category:          "Backups",
			Title:             "Old configuration backups",
			Bytes:             4096,
			SelectedByDefault: true,
			Paths: []string{
				"/var/backups/workstation/very/deeply/nested/config/archive/2026-01-01/tmux.conf.bak",
				"/var/backups/workstation/very/deeply/nested/config/archive/2026-01-02/zshrc.bak",
			},
		},
	}
	m := initLoadedModel(t, b, Options{StartTab: "themes"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 38})

	prevActive := m.ActiveTheme()
	var cmd tea.Cmd
	m, cmd = sendAction(t, m, '1')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "--app ") {
		t.Fatalf("expected a+1 on Themes to confirm single-app apply:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	appApplies := append([][2]string(nil), b.appliedThemeApps...)
	allApplies := len(b.appliedThemes)
	b.mu.Unlock()
	if len(appApplies) != 1 || allApplies != 0 {
		t.Fatalf("expected 1 single-app apply and 0 all-app applies, got apps=%v all=%d", appApplies, allApplies)
	}
	if m.ActiveTheme() != prevActive {
		t.Fatalf("single-app apply mutated global ActiveTheme from %q to %q", prevActive, m.ActiveTheme())
	}

	m, cmd = sendAction(t, m, 'A')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "All Apps") {
		t.Fatalf("expected 'A' to confirm all-apps theme apply")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	allApplies = len(b.appliedThemes)
	b.mu.Unlock()
	if allApplies != 1 {
		t.Fatalf("expected 'A' to invoke ApplyTheme once, got %d", allApplies)
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "cleanup")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelPackages || m.panels[PanelPackages].ActiveSubView() != pkgSubCleanup || m.ActiveSplit() != SplitVertical {
		t.Fatalf("expected Cleanup at width 120 to auto-open SplitVertical, got panel=%d sub=%d split=%v", m.ActivePanel(), m.panels[PanelPackages].ActiveSubView(), m.ActiveSplit())
	}
	cleanOut := ui.StripANSI(m.Render())
	if !strings.Contains(cleanOut, "tmux.conf.bak") || !strings.Contains(cleanOut, "zshrc.bak") {
		t.Fatalf("expected Cleanup split preview to render wrapped full backup filenames:\n%s", cleanOut)
	}
}

func TestProjectsFilterTargetIndicatorAndUnifiedScope(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})

	m, _ = sendMsg(t, m, keyRune('/'))
	if out := ui.StripANSI(m.Render()); !strings.Contains(out, "Filter Repos ▸ /") {
		t.Fatalf("expected left pane filter bar to show 'Filter Repos ▸ /':\n%s", out)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('/'))
	if out := ui.StripANSI(m.Render()); !strings.Contains(out, "Filter PRs & Issues ▸ /") {
		t.Fatalf("expected right GH pane filter bar to show 'Filter PRs & Issues ▸ /':\n%s", out)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune('R'))
	if m.panels[PanelProjects].CapturingInput() || strings.Contains(ui.StripANSI(m.Render()), "Repo>") {
		t.Fatalf("expected 'R' not to capture input or render removed inline picker")
	}
}

func TestEnvEscPreservesFilterBatchRevealDotEnvCopyAndWrappedModal(t *testing.T) {
	b := newMockBackend()
	b.envSecrets["~/personal/code/demo-web/.env\x00DATABASE_URL"] = "postgres://user:pass@host/db?sslmode=require&opt=a b"
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 36})

	var copied []string
	oldClip := clipboardWrite
	clipboardWrite = func(s string) error {
		copied = append(copied, s)
		return nil
	}
	defer func() { clipboardWrite = oldClip }()

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "very-long")
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.panels[PanelEnv].CanPop() {
		t.Fatalf("expected Enter in Env Vars to open Env Detail (CanPop=true)")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.panels[PanelEnv].CanPop() {
		t.Fatalf("expected first Esc to exit Env Detail")
	}
	if got := m.panels[PanelEnv].FilterQuery(); got != "very-long" {
		t.Fatalf("expected Esc from Env Detail to preserve active filter %q, got %q", "very-long", got)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	if got := m.panels[PanelEnv].FilterQuery(); got != "" {
		t.Fatalf("expected second Esc to clear active filter, got %q", got)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, cmd = sendAction(t, m, 'v')
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('k'))
	revOut := ui.StripANSI(m.Render())
	if !strings.Contains(revOut, "sk-live-revea") || !strings.Contains(revOut, "postgres://us") {
		t.Fatalf("expected batch a+v to reveal both marked secrets and persist across j/k:\n%s", revOut)
	}

	for _, k := range []rune{'y', 'Y'} {
		m, cmd = sendMsg(t, m, keyRune(k))
		m = drainCmds(t, m, cmd)
	}
	m, cmd = sendAction(t, m, 'c')
	m = drainCmds(t, m, cmd)
	if len(copied) != 3 {
		t.Fatalf("expected 3 clipboard writes for y, Y, a+c; got %d: %v", len(copied), copied)
	}
	if !strings.Contains(copied[0], "very-long-service-api-key=sk-live-revealed-9f2a") ||
		!strings.Contains(copied[0], `DATABASE_URL="postgres://user:pass@host/db?sslmode=require&opt=a b"`) {
		t.Fatalf("expected 'y' to copy .env KEY=VALUE lines, got %q", copied[0])
	}
	if !strings.Contains(copied[1], "sk-live-revealed-9f2a") || !strings.Contains(copied[1], "postgres://user:pass@host/db?sslmode=require&opt=a b") {
		t.Fatalf("expected 'Y' to copy raw values, got %q", copied[1])
	}
	if copied[2] != "~/work/code/api-service/.env.local" {
		t.Fatalf("expected a+c to copy active source path, got %q", copied[2])
	}

	m, cmd = sendAction(t, m, 'v')
	m = drainCmds(t, m, cmd)
	if strings.Contains(ui.StripANSI(m.Render()), "sk-live-revealed-9f2a") {
		t.Fatalf("expected second a+v to mask all revealed batch entries")
	}

	m, cmd = sendAction(t, m, 'R')
	m = drainCmds(t, m, cmd)
	modalOut := ui.StripANSI(m.Render())
	for _, want := range []string{"1. Replacement Value", "2. Target Files (1 checked of 2)", "3. Confirm Update", "Deliberately different checkouts stay untouched", "tab switch focus"} {
		if !strings.Contains(modalOut, want) {
			t.Fatalf("expected wrapped Selective Token Update modal to contain %q:\n%s", want, modalOut)
		}
	}
}

func TestMisePanelConfigsToolsAndEdit(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "mise"})

	out := ui.StripANSI(m.Render())
	for _, want := range []string{"Mise Configs (3)", "config.toml", "demo-web", "github", "codecommit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected Mise Configs view to contain %q:\n%s", want, out)
		}
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	demoOut := ui.StripANSI(m.Render())
	for _, want := range []string{"example/demo-web", "(Issue/PR eligible)", "API_TOKEN"} {
		if !strings.Contains(demoOut, want) {
			t.Fatalf("expected demo-web preview to contain %q:\n%s", want, demoOut)
		}
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('e'))
	if cmd == nil {
		t.Fatalf("expected 'e' on Mise config to return tea.ExecProcess cmd")
	}
	b.mu.Lock()
	edited := append([]string(nil), b.editedMiseFiles...)
	b.mu.Unlock()
	if len(edited) != 1 || edited[0] != "/tmp/example/personal/code/demo-web/.mise/config.toml" {
		t.Fatalf("expected EditMiseFileCmd for demo-web config, got %v", edited)
	}

	m, _ = sendMsg(t, m, keyRune('['))
	toolsOut := ui.StripANSI(m.Render())
	for _, want := range []string{"Mise Packages (3)", "node", "outdated", "python", "missing"} {
		if !strings.Contains(toolsOut, want) {
			t.Fatalf("expected Mise Tools view to contain %q:\n%s", want, toolsOut)
		}
	}
	m, _ = sendMsg(t, m, keyRune(']'))

	m, _ = sendMsg(t, m, keyRune('j'))
	ccOut := ui.StripANSI(m.Render())
	if !strings.Contains(ccOut, "internal-svc · project") || !strings.Contains(ccOut, "codecommit (local") || !strings.Contains(ccOut, "research/branch only)") {
		t.Fatalf("expected internal-svc preview to show full project name & CodeCommit policy:\n%s", ccOut)
	}
}

func TestFilterAndSortSelectorAcrossPackagesMiseProjectsAndEnv(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})

	sp := m.panels[PanelPackages].(packagesPanel)
	if len(sp.filteredPkgs) < 4 {
		t.Fatalf("expected at least 4 packages before filter, got %d", len(sp.filteredPkgs))
	}

	m, _ = sendMsg(t, m, keyRune('F'))
	if !m.panels[PanelPackages].HasOpenModal() {
		t.Fatalf("expected 'F' on Packages to open Filter & Sort selector modal")
	}
	boxOut := ui.StripANSI(m.Render())
	if !strings.Contains(boxOut, "Filter & Sort Selector") || !strings.Contains(boxOut, "Outdated") || !strings.Contains(boxOut, "(1)") {
		t.Fatalf("expected Packages Filter & Sort box to show Outdated (1):\n%s", boxOut)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	sp = m.panels[PanelPackages].(packagesPanel)
	if len(sp.filteredPkgs) != 1 || sp.filteredPkgs[0].ID != "session-manager-plugin" || !sp.filteredPkgs[0].Outdated {
		t.Fatalf("expected Space toggle on Status=Outdated to filter live to session-manager-plugin, got %+v", sp.filteredPkgs)
	}
	if toggledOut := ui.StripANSI(m.Render()); !strings.Contains(toggledOut, "[x] Outdated") {
		t.Fatalf("expected Space to check [x] Outdated inside selector modal:\n%s", toggledOut)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = sendMsg(t, m, keyRune('S'))
	m, _ = sendMsg(t, m, keyRune('c'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	sp = m.panels[PanelPackages].(packagesPanel)
	if len(sp.filteredPkgs) < 4 || sp.pkgFilters.HasActive() {
		t.Fatalf("expected clearing Filter/Sort to restore all packages, got len=%d active=%v", len(sp.filteredPkgs), sp.pkgFilters.HasActive())
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "mise")
	var miseCmd tea.Cmd
	m, miseCmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, miseCmd)
	m, _ = sendMsg(t, m, keyRune('F'))
	if !m.panels[PanelPackages].HasOpenModal() {
		t.Fatalf("mise: expected 'F' to open Filter & Sort modal")
	}
	if modalOut := ui.StripANSI(m.Render()); !strings.Contains(modalOut, "Filter & Sort Selector") || !strings.Contains(modalOut, "Mise Configs") {
		t.Fatalf("mise: Filter & Sort modal missing Mise Configs:\n%s", modalOut)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	for _, tc := range []struct {
		key       rune
		panelIdx  int
		wantTitle string
	}{
		{'2', PanelProjects, "Repos"},
		{'4', PanelEnv, "Env Vars"},
	} {
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, keyRune(tc.key))
		m = drainCmds(t, m, cmd)
		m, _ = sendMsg(t, m, keyRune('F'))
		if !m.panels[tc.panelIdx].HasOpenModal() {
			t.Fatalf("panel %c: expected 'F' to open Filter & Sort modal", tc.key)
		}
		if modalOut := ui.StripANSI(m.Render()); !strings.Contains(modalOut, "Filter & Sort Selector") || !strings.Contains(modalOut, tc.wantTitle) {
			t.Fatalf("panel %c: Filter & Sort modal missing %q:\n%s", tc.key, tc.wantTitle, modalOut)
		}
		m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
		if m.panels[tc.panelIdx].HasOpenModal() {
			t.Fatalf("panel %c: expected Esc to close Filter & Sort modal", tc.key)
		}
	}
}

func TestBorderedBreadcrumbAndActiveFilterBar(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 34})

	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "╭") || !strings.Contains(out, "Projects") || strings.Contains(out, ": cmd  / filter") {
		t.Fatalf("expected bordered 3-line breadcrumb panel at 120x34:\n%s", out)
	}

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "demo")
	filterOut := ui.StripANSI(m.Render())
	if !strings.Contains(filterOut, "Filter · Repos") || !strings.Contains(filterOut, "esc restore") {
		t.Fatalf("expected accented bordered Filter bar when '/' active:\n%s", filterOut)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	compact := m.View().Content
	if got := lipgloss.Height(compact); got != 24 {
		t.Fatalf("expected 80x24 height=24, got %d", got)
	}
}

func TestDoctorVaultOnePasswordSaveRestoreAndDBKey(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "vault"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 128, Height: 36})

	out := ui.StripANSI(m.Render())
	for _, want := range []string{
		"Vault & 1Password Manager",
		"ws.db (Encrypted Snapshot Vault)",
		"ws-db-backup.enc",
		"ws-db.key",
		"~/.ssh.zip",
		"excludes",
		"~/.ws/cache, logs",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected Doctor Vault view to include %q:\n%s", want, out)
		}
	}

	var cmd tea.Cmd
	m, cmd = sendAction(t, m, 's')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Save Selected Items to 1Password") {
		t.Fatalf("expected Save to 1Password confirm modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendAction(t, m, 'l')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Load / Restore Selected Items from 1Password") {
		t.Fatalf("expected Load from 1Password confirm modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	m, cmd = sendAction(t, m, 'l')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	m, cmd = sendAction(t, m, 'K')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Initialize & Back Up ws.db Key") {
		t.Fatalf("expected db.key modal on 'K':\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	actions := append([]string(nil), b.vaultActions...)
	b.mu.Unlock()
	if !slices.Equal(actions, []string{"save:db", "load:ssh", "key:db-key"}) {
		t.Fatalf("unexpected RunVaultAction calls: %v", actions)
	}
	if len(m.OutputPanel().runs) < 3 {
		t.Fatalf("expected Vault actions recorded in Output Console, got %d runs", len(m.OutputPanel().runs))
	}
}

func TestFleetFileSyncDiffPushPullAndSecretRedaction(t *testing.T) {
	b := newMockBackend()
	b.fleetNodes = append([]FleetNodeRow{
		{Name: "local-mac", DNSName: "local-mac.tailnet.ts.net", TailscaleIP: "100.64.0.1", OS: "darwin", Arch: "arm64", Provider: "tailscale", Online: true, IsSelf: true},
	}, b.fleetNodes...)
	m := initLoadedModel(t, b, Options{StartTab: "fleet"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 160, Height: 38})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	if m.panels[PanelFleet].ActiveSubView() != fleetSubNodes || !strings.Contains(m.Toast(), "itself") {
		t.Fatalf("expected self-diff guard toast on local node, got sub=%d toast=%q", m.panels[PanelFleet].ActiveSubView(), m.Toast())
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	if m.panels[PanelFleet].ActiveSubView() != fleetSubFileSync {
		t.Fatalf("expected 'd' on peer to open File Sync sub-view")
	}
	out := ui.StripANSI(m.Render())
	for _, want := range []string{"Machine File Diff · dev-vm-01", ".ssh/config", ".aws/credentials", "secret"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected File Sync diff table to include %q:\n%s", want, out)
		}
	}

	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "+  Port 2222") {
		t.Fatalf("expected unified diff for .ssh/config:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "Redacted:") {
		t.Fatalf("expected secret diff preview redacted on .ssh/id_ed25519:\n%s", ui.StripANSI(m.Render()))
	}

	m, _ = sendMsg(t, m, keyRune('F'))
	if !m.panels[PanelFleet].HasOpenModal() {
		t.Fatalf("expected 'F' on File Sync to open Filter & Sort selector")
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	fp := m.panels[PanelFleet].(fleetPanel)
	if len(fp.filteredFiles) != 1 || fp.filteredFiles[0].RelPath != ".ssh/config" {
		t.Fatalf("expected Modified facet filter to narrow to .ssh/config, got %+v", fp.filteredFiles)
	}
	m, _ = sendMsg(t, m, keyRune('c'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Push Files: Local -> dev-vm-01") {
		t.Fatalf("expected Push confirm modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	b.mu.Lock()
	syncCntAfterCancel := len(b.fileSyncCalls)
	b.mu.Unlock()
	if syncCntAfterCancel != 0 {
		t.Fatalf("expected Cancel default on File Sync Push modal, got %d calls", syncCntAfterCancel)
	}

	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	m, cmd = sendAction(t, m, 'L')
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Pull Files: dev-vm-01 -> Local") {
		t.Fatalf("expected Pull confirm modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)

	b.mu.Lock()
	calls := append([]FleetFileSyncReq(nil), b.fileSyncCalls...)
	b.mu.Unlock()
	if len(calls) != 2 || calls[0].Direction != "push" || calls[1].Direction != "pull" {
		t.Fatalf("expected push + pull SyncFleetFiles calls, got %+v", calls)
	}
}

func TestSettingsPanelOverridesResetAndLiveAutoReload(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "settings"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	out := ui.StripANSI(m.Render())
	for _, want := range []string{"Workstation & TUI Settings", "tui.auto_reload", "WS_OP_VAULT", "AUTO-RELOAD 2s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected Settings view to render %q:\n%s", want, out)
		}
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	m = drainCmds(t, m, cmd)
	if m.autoReloadEnabled {
		t.Fatalf("expected Space on tui.auto_reload to toggle autoReloadEnabled=false")
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.panels[PanelWorkstation].HasOpenModal() || !strings.Contains(ui.StripANSI(m.Render()), "Override Setting: WS_OP_VAULT") {
		t.Fatalf("expected inline override modal for WS_OP_VAULT:\n%s", ui.StripANSI(m.Render()))
	}
	m = typeString(t, m, "TeamVault")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !strings.Contains(ui.StripANSI(m.Render()), "✦ override") {
		t.Fatalf("expected saved override source badge:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('x'))
	m = drainCmds(t, m, cmd)

	m, cmd = sendAction(t, m, 'A')
	m = drainCmds(t, m, cmd)
	if !m.autoReloadEnabled {
		t.Fatalf("expected a+A to re-enable auto-reload")
	}

	m, cmd = sendMsg(t, m, keyRune(']'))
	m = drainCmds(t, m, cmd)
	if m.panels[PanelWorkstation].ActiveSubView() != wsSubWatchedFiles || !strings.Contains(ui.StripANSI(m.Render()), "config.toml") {
		t.Fatalf("expected Watched Files sub-view with config.toml:\n%s", ui.StripANSI(m.Render()))
	}
	m, cmd = sendMsg(t, m, keyRune('e'))
	if cmd == nil {
		t.Fatalf("expected 'e' on Watched Files to return EditConfigFileCmd ExecProcess")
	}
	m, _ = sendMsg(t, m, keyRune('['))

	b.mu.Lock()
	b.settingsView.Theme = "catppuccin-dark"
	b.settingsView.BorderedBar = "compact"
	b.settingsView.Fingerprint = "fp-external-999"
	b.watchChanged = []string{"config.toml"}
	b.mu.Unlock()

	m, cmd = sendMsg(t, m, configWatchTickMsg{})
	m = drainCmds(t, m, cmd)
	if m.activeTheme != "catppuccin-dark" || m.borderedBarPref != "compact" {
		t.Fatalf("expected live auto-reload to apply theme=catppuccin-dark and bordered_bar=compact, got theme=%q bar=%q", m.activeTheme, m.borderedBarPref)
	}
	if !strings.Contains(m.Toast(), "Auto-reloaded") {
		t.Fatalf("expected auto-reload toast, got %q", m.Toast())
	}

	b.mu.Lock()
	setCalls := append([][2]string(nil), b.setSettingCalls...)
	resetCalls := append([]string(nil), b.resetSettingCalls...)
	reloadCnt := b.reloadConfigCalls
	editPaths := append([]string(nil), b.editedConfigPaths...)
	b.mu.Unlock()
	if len(setCalls) != 3 || setCalls[0] != [2]string{"tui.auto_reload", "false"} || setCalls[1] != [2]string{"WS_OP_VAULT", "TeamVault"} || setCalls[2] != [2]string{"tui.auto_reload", "true"} {
		t.Fatalf("unexpected SetSetting calls: %+v", setCalls)
	}
	if !slices.Equal(resetCalls, []string{"WS_OP_VAULT"}) || reloadCnt == 0 || len(editPaths) != 1 {
		t.Fatalf("unexpected reset=%v reload=%d edits=%v", resetCalls, reloadCnt, editPaths)
	}
}

func TestProjectsFilterNarrowsReposAndKeepsGHSynced(t *testing.T) {
	b := newMockBackend()
	b.host.CWDRepoPath = "/tmp/outside/other"
	b.host.CWDRepoSlug = "example/outside-repo"
	b.repos = []RepoRow{
		{
			Name:      "demo-web",
			Zone:      "personal",
			Forge:     "github",
			RemoteURL: "https://github.com/example/demo-web",
			LocalPath: "/tmp/example/personal/code/demo-web",
			Branch:    "feat/ui",
		},
		{
			Name:      "api-service",
			Zone:      "work",
			Forge:     "github",
			RemoteURL: "https://github.com/example/api-service",
			LocalPath: "/tmp/example/work/code/api-service",
			Branch:    "main",
		},
		{
			Name:      "internal-svc",
			Zone:      "work",
			Forge:     "codecommit",
			RemoteURL: "codecommit::eu-central-1://internal-svc",
			LocalPath: "/tmp/example/work/code/internal-svc",
			Branch:    "main",
		},
	}

	m := initLoadedModel(t, b, Options{StartTab: "projects"})

	initOut := ui.StripANSI(m.Render())
	if !strings.Contains(initOut, "Repo: example/demo-web") || strings.Contains(initOut, "example/outside-repo") {
		t.Fatalf("expected right pane synced to highlighted demo-web instead of absent outside CWD repo:\n%s", initOut)
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('/'))
	m = drainCmds(t, m, cmd)
	for _, ch := range "api" {
		m, cmd = sendMsg(t, m, keyRune(ch))
		m = drainCmds(t, m, cmd)
	}
	pp := m.panels[PanelProjects].(projectsPanel)
	if len(pp.filteredRepos) != 1 || pp.filteredRepos[0].Name != "api-service" {
		t.Fatalf("expected '/api' filter to narrow repos strictly to [api-service], got %+v", pp.filteredRepos)
	}
	apiOut := ui.StripANSI(m.Render())
	if !strings.Contains(apiOut, "Repo: example/api-service") || !strings.Contains(apiOut, "#104") {
		t.Fatalf("expected right pane synced to filtered api-service PRs:\n%s", apiOut)
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('j'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('j'))
	m = drainCmds(t, m, cmd)
	ccOut := ui.StripANSI(m.Render())
	if !strings.Contains(ccOut, "internal-svc (codecommit") || !strings.Contains(ccOut, "not GitHub") || strings.Contains(ccOut, "#104") || strings.Contains(ccOut, "#22") {
		t.Fatalf("expected CodeCommit repo selection to show non-GitHub empty state without leaking unrelated PRs:\n%s", ccOut)
	}

	m, _ = sendMsg(t, m, keyRune('/'))
	for _, ch := range "zzznotfound" {
		m, cmd = sendMsg(t, m, keyRune(ch))
		m = drainCmds(t, m, cmd)
	}
	pp = m.panels[PanelProjects].(projectsPanel)
	if len(pp.filteredRepos) != 0 {
		t.Fatalf("expected 0 matching repos for zzznotfound, got %d", len(pp.filteredRepos))
	}
	emptyOut := ui.StripANSI(m.Render())
	if !strings.Contains(emptyOut, "No matching repository selected") || strings.Contains(emptyOut, "example/outside-repo") || strings.Contains(emptyOut, "#104") {
		t.Fatalf("expected empty right pane when repo filter has 0 matches:\n%s", emptyOut)
	}
}

func TestFilterSortModalEnterDoesNotRetoggle(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "packages"})

	m, _ = sendMsg(t, m, keyRune('F'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	sp := m.panels[PanelPackages].(packagesPanel)
	if sp.pkgFilters.Get("status") != "outdated" {
		t.Fatalf("expected first Space to set status=outdated, got %q", sp.pkgFilters.Get("status"))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	sp = m.panels[PanelPackages].(packagesPanel)
	if sp.pkgFilters.Get("status") != "" {
		t.Fatalf("expected second Space to clear status filter, got %q", sp.pkgFilters.Get("status"))
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sp = m.panels[PanelPackages].(packagesPanel)
	if m.panels[PanelPackages].HasOpenModal() {
		t.Fatalf("expected Enter to close Filter & Sort modal")
	}
	if sp.pkgFilters.Get("status") != "" {
		t.Fatalf("expected Enter NOT to re-toggle OFF filter option back ON, got %q", sp.pkgFilters.Get("status"))
	}
	if len(sp.filteredPkgs) < 4 {
		t.Fatalf("expected full package list preserved when filter was toggled OFF before Enter, got %d", len(sp.filteredPkgs))
	}

	m, _ = sendMsg(t, m, keyRune('S'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace})
	sp = m.panels[PanelPackages].(packagesPanel)
	if sp.pkgFilters.SortKey == "" || sp.pkgFilters.SortDesc {
		t.Fatalf("expected Space to select ASC sort, got key=%q desc=%v", sp.pkgFilters.SortKey, sp.pkgFilters.SortDesc)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sp = m.panels[PanelPackages].(packagesPanel)
	if m.panels[PanelPackages].HasOpenModal() || sp.pkgFilters.SortDesc {
		t.Fatalf("expected Enter to close Sort modal without flipping ASC->DESC, got open=%v desc=%v",
			m.panels[PanelPackages].HasOpenModal(), sp.pkgFilters.SortDesc)
	}
}

func TestCommandPaletteArgumentAutocompleteGhostAndTab(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	m, _ = sendMsg(t, m, keyRune(':'))
	for _, r := range "zone " {
		m, _ = sendMsg(t, m, keyRune(r))
	}
	if !m.cmdBar.argMode || len(m.cmdBar.argMatches) < 2 {
		t.Fatalf("expected command palette argMode for 'zone ', got argMode=%v matches=%d", m.cmdBar.argMode, len(m.cmdBar.argMatches))
	}
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "[opt]") || !strings.Contains(out, "personal") {
		t.Fatalf("expected [opt] zone completions in palette render:\n%s", out)
	}

	m, _ = sendMsg(t, m, keyRune('p'))
	if ghost := m.cmdBar.ghostSuggestion(); !strings.Contains(ghost, "ersonal") {
		t.Fatalf("expected ghost suggestion 'ersonal' for ':zone p', got %q", ghost)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.cmdBar.input.Value(); got != "zone personal" {
		t.Fatalf("expected Tab to complete 'zone personal', got %q", got)
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.scopeZone != "personal" {
		t.Fatalf("expected :zone personal to switch scopeZone='personal', got %q", m.scopeZone)
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	for _, r := range "themes cat" {
		m, _ = sendMsg(t, m, keyRune(r))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.cmdBar.input.Value(); got != "themes catppuccin-dark" {
		t.Fatalf("expected Tab to complete 'themes catppuccin-dark', got %q", got)
	}
}

func TestSettingsOptionPickerAndHeaderViewHintsColumns(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "settings"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 150, Height: 36})

	out := ui.StripANSI(m.Render())

	if !strings.Contains(out, "Pick / Toggle") || !strings.Contains(out, "<?>") || !strings.Contains(out, "Help") {
		t.Fatalf("expected Settings view hints and global hints in header:\n%s", out)
	}
	if strings.Contains(out, "enter/space Toggle/Edit · c Text edit") {
		t.Fatalf("expected redundant inline Settings bottom footer to be removed:\n%s", out)
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	sp := m.panels[PanelWorkstation].(workstationPanel).settings
	if !sp.pickOpen {
		t.Fatalf("expected Enter on tui.theme to open Option Picker modal")
	}
	modalOut := ui.StripANSI(m.Render())
	if !strings.Contains(modalOut, "Select tui.theme") || !strings.Contains(modalOut, "catppuccin-dark") {
		t.Fatalf("expected Option Picker modal with theme choices:\n%s", modalOut)
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	sp = m.panels[PanelWorkstation].(workstationPanel).settings
	if sp.pickOpen || m.activeTheme != "catppuccin-dark" {
		t.Fatalf("expected Option Picker to save activeTheme='catppuccin-dark' and close, pickOpen=%v theme=%q", sp.pickOpen, m.activeTheme)
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = sendMsg(t, m, keyRune('c'))
	sp = m.panels[PanelWorkstation].(workstationPanel).settings
	if !sp.editOpen || sp.pickOpen {
		t.Fatalf("expected 'c' inside Option Picker to switch to text override modal, editOpen=%v pickOpen=%v", sp.editOpen, sp.pickOpen)
	}
}

func TestUnifiedGHSubViewsActionsAndZoom(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	b := newMockBackend()
	b.ghRepoItems["example/app|open"] = []GHItemRow{
		{
			Kind:      "pr",
			Repo:      "example/app",
			Number:    42,
			Title:     "Add ws gh dashboard",
			State:     "open",
			Author:    "alice",
			URL:       "https://github.com/example/app/pull/42",
			Zone:      "personal",
			Milestone: "v2.0",
			HeadRef:   "feat/gh-dash",
			UpdatedAt: "2026-11-27",
		},
		{
			Kind:      "issue",
			Repo:      "example/app",
			Number:    17,
			Title:     "Track release checklist",
			State:     "open",
			Author:    "bob",
			URL:       "https://github.com/example/app/issues/17",
			Zone:      "personal",
			Milestone: "v1.9",
			UpdatedAt: "2026-11-26",
		},
	}
	b.ghDetails["pr|example/app#42"] = GHItemDetail{
		GHItemRow: GHItemRow{
			Kind:      "pr",
			Repo:      "example/app",
			Number:    42,
			Title:     "Add ws gh dashboard",
			State:     "open",
			Author:    "alice",
			URL:       "https://github.com/example/app/pull/42",
			Zone:      "personal",
			Milestone: "v2.0",
			Body:      "Implements **gh-dash** workflow.",
		},
		HeadRef:        "feat/gh-dash",
		BaseRef:        "main",
		ReviewDecision: "REVIEW_REQUIRED",
		ChecksSummary:  "3 passed",
		Comments: []GHCommentRow{
			{Author: "reviewer", Body: "Looks clean!", CreatedAt: "2026-11-27 12:00"},
		},
	}

	m := initLoadedModel(t, b, Options{
		StartTab:    "gh-prs",
		GHRepo:      "example/app",
		DefaultZone: "personal",
		GHState:     "open",
	})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 165, Height: 38})

	if m.ActivePanel() != PanelProjects || m.panels[PanelProjects].ActiveSubView() != projSubPRs {
		t.Fatalf("expected StartTab='gh-prs' in PanelProjects projSubPRs, got panel=%d sub=%d", m.ActivePanel(), m.panels[PanelProjects].ActiveSubView())
	}

	out := m.Render()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("expected zero ANSI escapes under NO_COLOR=1:\n%q", out)
	}
	for _, want := range []string{"#42", "Add ws gh dashboard", "v2.0", "feat/gh-dash", "3 passed", "Looks clean!"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in unified PR view:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api-service") && strings.Contains(out, "demo-web") {
		t.Fatalf("expected Repos table hidden in dedicated Pull Requests subview:\n%s", out)
	}

	var cmd tea.Cmd
	m, _ = sendMsg(t, m, keyRune('='))
	if !m.Zoomed() {
		t.Fatalf("expected '=' to enable zoom")
	}
	zoomMain := m.Render()
	if !strings.Contains(zoomMain, "ZOOM:main") || strings.Contains(zoomMain, "Preview · PR #42") {
		t.Fatalf("expected ZOOM:main hiding preview pane:\n%s", zoomMain)
	}
	m, _ = sendMsg(t, m, keyRune('='))
	if m.Zoomed() {
		t.Fatalf("expected second '=' to restore split")
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('W'))
	if m.Zoomed() {
		t.Fatalf("expected removed 'W' key to be a no-op")
	}
	m, _ = sendMsg(t, m, keyRune('='))
	if !m.Zoomed() {
		t.Fatalf("expected '=' to zoom preview pane")
	}
	zoomPrev := m.Render()
	if !strings.Contains(zoomPrev, "ZOOM:preview") || !strings.Contains(zoomPrev, "Looks clean!") || strings.Contains(zoomPrev, "Pull Requests · example/app (1)") {
		t.Fatalf("expected ZOOM:preview showing full detail reader:\n%s", zoomPrev)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Zoomed() {
		t.Fatalf("expected Esc to restore zoom before popping subview")
	}

	m = typeString(t, m, ":zoom")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.Zoomed() {
		t.Fatalf("expected :zoom to enable zoom")
	}
	m, _ = sendMsg(t, m, keyRune('='))
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = drainCmds(t, m, cmd)

	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	copyModal := m.Render()
	for _, want := range []string{"Copy Field", "Milestone", "v2.0", "Checkout command"} {
		if !strings.Contains(copyModal, want) {
			t.Fatalf("expected %q in Copy Field modal:\n%s", want, copyModal)
		}
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m, cmd = sendAction(t, m, 'A')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	if len(b.ghApprovedPRs) != 1 || b.ghApprovedPRs[0] != 42 {
		t.Fatalf("expected PR #42 approved, got %v", b.ghApprovedPRs)
	}

	m, cmd = sendAction(t, m, 'M')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if len(b.ghMergedPRs) != 1 || b.ghMergedPRs[0][1] != "squash" {
		t.Fatalf("expected squash merge of #42, got %v", b.ghMergedPRs)
	}

	m, cmd = sendAction(t, m, 'X')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	if len(b.ghStateChanges) != 1 || b.ghStateChanges[0][1] != "close" {
		t.Fatalf("expected close state action, got %v", b.ghStateChanges)
	}

	m, cmd = sendAction(t, m, 'm')
	m = drainCmds(t, m, cmd)
	m = typeString(t, m, "LGTM")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if len(b.ghComments) != 1 || !strings.Contains(b.ghComments[0], "LGTM") {
		t.Fatalf("expected LGTM comment recorded, got %v", b.ghComments)
	}

	m, cmd = sendAction(t, m, 'K')
	m = drainCmds(t, m, cmd)
	if len(b.ghCheckedOutPRs) != 1 || b.ghCheckedOutPRs[0] != 42 {
		t.Fatalf("expected checkout PR #42, got %v", b.ghCheckedOutPRs)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.panels[PanelProjects].ActiveSubView() != projSubIssues {
		t.Fatalf("expected 't' to switch to projSubIssues, got %d", m.panels[PanelProjects].ActiveSubView())
	}
	issueOut := m.Render()
	if !strings.Contains(issueOut, "#17") || !strings.Contains(issueOut, "Track release checklist") {
		t.Fatalf("expected issue #17 in Issues subview:\n%s", issueOut)
	}
}

func TestSystemPackagesUpdateAllAndUntrackedWorkflow(t *testing.T) {
	b := newMockBackend()
	b.untrackedPkgs = []PresetToolRow{
		{
			ID:           "untracked:brew:jq",
			PackageName:  "jq",
			Provider:     "brew",
			Status:       "untracked",
			Updates:      "manager",
			OriginPreset: "untracked",
			OriginFile:   "pkg/mac-brew.txt",
			Source:       "untracked",
		},
	}

	m := initLoadedModel(t, b, Options{StartTab: "pkgs"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 38})

	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "1 untracked") || !strings.Contains(out, "jq") || !strings.Contains(out, "untracked") {
		t.Fatalf("expected untracked row and status chip in System Packages:\n%s", out)
	}

	sp := m.panels[PanelPackages].(packagesPanel)
	var cmd tea.Cmd
	for i, row := range sp.filteredPkgs {
		if row.PackageName == "jq" {
			sp.pkgIdx = i
			m.panels[PanelPackages] = sp
			break
		}
	}
	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	if len(b.trackedCalls) != 1 || b.trackedCalls[0] != "jq" {
		t.Fatalf("expected 'jq' tracked via a+t, got %v", b.trackedCalls)
	}

	m, cmd = sendAction(t, m, 'n')
	m = drainCmds(t, m, cmd)
	if !m.panels[PanelPackages].(packagesPanel).pkgAddModalOpen {
		t.Fatalf("expected a+n to open pkgAddModalOpen")
	}
	m = typeString(t, m, "hyperfine")
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if len(b.addedPkgCalls) != 1 || b.addedPkgCalls[0] != "hyperfine|brew|pkg/mac-brew.txt|false" {
		t.Fatalf("expected hyperfine tracked without install, got %v", b.addedPkgCalls)
	}

	sp = m.panels[PanelPackages].(packagesPanel)
	sp.pkgIdx = 0
	m.panels[PanelPackages] = sp
	m, cmd = sendAction(t, m, 'D')
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	m = drainCmds(t, m, cmd)
	if len(b.untrackedCalls) != 1 {
		t.Fatalf("expected tracked package untracked on a+D, got %v", b.untrackedCalls)
	}

	m, cmd = sendMsg(t, m, keyRune('U'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd for 'U'")
	}
	if b.updatePreviewCnt != 1 || len(b.updateAllExecCalls) != 1 || b.updateAllExecCalls[0] || len(b.updateAllCalls) != 0 {
		t.Fatalf("expected 'U' to preview & exec ws update (all=false), preview=%d exec=%v bg=%v", b.updatePreviewCnt, b.updateAllExecCalls, b.updateAllCalls)
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update", command: "ws update"})
	m = drainCmds(t, m, cmd)

	m = typeString(t, m, ":update all")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd for :update all")
	}
	if len(b.updateAllExecCalls) != 2 || !b.updateAllExecCalls[1] || len(b.updateAllCalls) != 0 {
		t.Fatalf("expected :update all to trigger UpdateAllExecCmd(true) with 0 background calls, exec=%v bg=%v", b.updateAllExecCalls, b.updateAllCalls)
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update-all", command: "ws update --all"})
	m = drainCmds(t, m, cmd)
}

func TestCommandPaletteTwoStageCompletionNeverLeaksHistory(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	m, _ = sendMsg(t, m, keyRune(':'))
	for _, r := range "zone personal" {
		m, _ = sendMsg(t, m, keyRune(r))
	}
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.scopeZone != "personal" {
		t.Fatalf("expected :zone personal recorded, got scopeZone=%q", m.scopeZone)
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	for _, r := range "zon" {
		m, _ = sendMsg(t, m, keyRune(r))
	}
	if ghost := m.cmdBar.ghostSuggestion(); ghost != "e" {
		t.Fatalf("expected stage-1 ghost 'e' (only command name), got %q", ghost)
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.cmdBar.input.Value(); got != "zone " {
		t.Fatalf("expected Tab after 'zon' to complete ONLY command 'zone ', got %q", got)
	}
	if !m.cmdBar.argMode || len(m.cmdBar.argMatches) < 2 {
		t.Fatalf("expected Stage-2 arg options active after completing 'zone ', argMode=%v matches=%d", m.cmdBar.argMode, len(m.cmdBar.argMatches))
	}

	m, _ = sendMsg(t, m, keyRune('w'))
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.cmdBar.input.Value(); got != "zone work" {
		t.Fatalf("expected Stage-2 Tab after 'w' to complete 'zone work', got %q", got)
	}
}

func TestFloatingFilterSortModalPreservesBackgroundContent(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "settings"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	m, _ = sendMsg(t, m, keyRune('F'))
	if !m.panels[PanelWorkstation].HasOpenModal() {
		t.Fatalf("expected Filter & Sort modal open")
	}

	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "Filter & Sort Selector") {
		t.Fatalf("expected Filter & Sort dialog rendered:\n%s", out)
	}

	if !strings.Contains(out, "Workstation & TUI Settings") || !strings.Contains(out, "Setting Inspector") {
		t.Fatalf("expected underlying Settings panels preserved behind floating modal:\n%s", out)
	}
}

func TestFleetDiffEscBackToNodesNavigation(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "fleet"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	fp := m.panels[PanelFleet].(fleetPanel)
	if n, ok := fp.selectedNode(); ok && n.IsSelf {
		m, _ = sendMsg(t, m, keyRune('j'))
	}

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	fp = m.panels[PanelFleet].(fleetPanel)
	if fp.subView != fleetSubFileSync {
		t.Fatalf("expected 'd' to open fleetSubFileSync, got subView=%d", fp.subView)
	}
	if !strings.Contains(ui.StripANSI(m.Render()), "Diff preview") {
		t.Fatalf("expected header hint 'Diff preview' in fleetSubFileSync")
	}

	m, cmd = sendMsg(t, m, keyRune('d'))
	m = drainCmds(t, m, cmd)
	fp = m.panels[PanelFleet].(fleetPanel)
	if fp.drillOpen {
		m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
		fp = m.panels[PanelFleet].(fleetPanel)
		if fp.drillOpen || fp.subView != fleetSubFileSync {
			t.Fatalf("expected first Esc to pop diff drill back to File Sync list, drillOpen=%v subView=%d", fp.drillOpen, fp.subView)
		}
	}

	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	fp = m.panels[PanelFleet].(fleetPanel)
	if fp.subView != fleetSubNodes {
		t.Fatalf("expected Esc from File Sync to return to Nodes (fleetSubNodes=%d), got subView=%d", fleetSubNodes, fp.subView)
	}
}

func TestMouseSupportMainTUIAndGHDash(t *testing.T) {

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "settings"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("expected main View().MouseMode == MouseModeCellMotion, got %v", v.MouseMode)
	}

	mouseOff := false
	svOff := b.settingsView
	svOff.Mouse = &mouseOff
	m, _ = sendMsg(t, m, settingSavedMsg{key: "tui.mouse", val: "false", view: svOff})
	if m.MouseEnabled() {
		t.Fatalf("expected MouseEnabled=false after tui.mouse=false")
	}
	if v := m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("expected MouseModeNone when tui.mouse=false, got %v", v.MouseMode)
	}
	mouseOn := true
	svOn := b.settingsView
	svOn.Mouse = &mouseOn
	m, _ = sendMsg(t, m, settingSavedMsg{key: "tui.mouse", val: "true", view: svOn})
	if !m.MouseEnabled() || m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("expected MouseModeCellMotion after re-enabling tui.mouse=true")
	}

	m, _ = sendMsg(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 15, Y: 12})
	sp := m.panels[PanelWorkstation].(workstationPanel).settings
	if sp.cursor != 1 {
		t.Fatalf("expected MouseWheelDown on Settings list to move cursor to 1, got %d", sp.cursor)
	}

	m, _ = sendMsg(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 110, Y: 12})
	sp = m.panels[PanelWorkstation].(workstationPanel).settings
	if sp.cursor != 1 || sp.previewScroll == 0 {
		t.Fatalf("expected MouseWheelDown over preview pane to scroll preview (scroll=%d cursor=%d)", sp.previewScroll, sp.cursor)
	}

	navY := m.headerLines(36) + m.cmdBarLines(36) + max(5, 36-(m.headerLines(36)+m.cmdBarLines(36)+2))
	_, tabSpans, _ := m.layoutBottomNav(140)
	var pkgSpan navHitSpan
	for _, ts := range tabSpans {
		if ts.ID == PanelPackages {
			pkgSpan = ts
			break
		}
	}
	m, _ = sendMsg(t, m, tea.MouseClickMsg{Button: tea.MouseLeft, X: pkgSpan.Start, Y: navY})
	if m.activePanel != PanelPackages {
		t.Fatalf("expected click on Packages bottom tab to switch activePanel=%d, got %d", PanelPackages, m.activePanel)
	}

	_, _, subSpans := m.layoutBottomNav(140)
	if len(subSpans) < 2 {
		t.Fatalf("expected multiple subview hit spans on Packages panel, got %d", len(subSpans))
	}
	m, _ = sendMsg(t, m, tea.MouseClickMsg{Button: tea.MouseLeft, X: subSpans[1].Start + 1, Y: navY})
	if got := m.panels[PanelPackages].ActiveSubView(); got != 1 {
		t.Fatalf("expected click on subview[1] pill to activate subview 1, got %d", got)
	}

	gb := newMockBackend()
	gb.ghRepoItems["example/app|open"] = []GHItemRow{
		{Kind: "pr", Repo: "example/app", Number: 42, Title: "First PR", State: "open", Author: "alice", Zone: "personal"},
		{Kind: "pr", Repo: "example/app", Number: 43, Title: "Second PR", State: "open", Author: "bob", Zone: "personal"},
		{Kind: "issue", Repo: "example/app", Number: 17, Title: "Open issue", State: "open", Author: "carol", Zone: "personal"},
	}
	gb.ghDetails["pr|example/app#42"] = GHItemDetail{
		GHItemRow: GHItemRow{
			Kind: "pr", Repo: "example/app", Number: 42, Title: "First PR", State: "open", Author: "alice", Zone: "personal",
			Body: strings.Repeat("Line of description text\n", 40),
		},
	}
	gm := initLoadedModel(t, gb, Options{StartTab: "gh-prs", GHRepo: "example/app", DefaultZone: "personal"})
	gm, _ = sendMsg(t, gm, tea.WindowSizeMsg{Width: 140, Height: 36})

	if v := gm.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("expected unified PRs View().MouseMode == MouseModeCellMotion, got %v", v.MouseMode)
	}

	gm, _ = sendMsg(t, gm, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 110, Y: 12})
	gp := gm.panels[PanelProjects].(projectsPanel)
	if gp.previewScroll == 0 {
		t.Fatalf("expected MouseWheelDown in GH detail preview pane to increment previewScroll")
	}

	gNavY := gm.headerLines(36) + gm.cmdBarLines(36) + max(5, 36-(gm.headerLines(36)+gm.cmdBarLines(36)+2))
	_, _, gSubSpans := gm.layoutBottomNav(140)
	if len(gSubSpans) < 3 {
		t.Fatalf("expected Repos / Pull Requests / Issues subview spans, got %d", len(gSubSpans))
	}
	gm, _ = sendMsg(t, gm, tea.MouseClickMsg{Button: tea.MouseLeft, X: gSubSpans[projSubIssues].Start + 1, Y: gNavY})
	if gm.panels[PanelProjects].ActiveSubView() != projSubIssues {
		t.Fatalf("expected click on Issues subview pill to switch to projSubIssues (%d), got %d", projSubIssues, gm.panels[PanelProjects].ActiveSubView())
	}
}

func TestWSGHPreservesRepoAndZoneWhenSettingsHasDefaultZone(t *testing.T) {

	ba := newMockBackend()
	ba.settingsView.DefaultZone = "personal"
	ba.ghRepoItems["work-org/backend-api|open"] = []GHItemRow{
		{
			Kind: "pr", Repo: "work-org/backend-api", Number: 99, Title: "Fix production queue backoff",
			State: "open", Author: "alice", URL: "https://github.com/work-org/backend-api/pull/99", Zone: "work",
			UpdatedAt: "2026-11-27",
		},
	}
	ba.ghDetails["pr|work-org/backend-api#99"] = GHItemDetail{
		GHItemRow: GHItemRow{
			Kind: "pr", Repo: "work-org/backend-api", Number: 99, Title: "Fix production queue backoff",
			State: "open", Author: "alice", URL: "https://github.com/work-org/backend-api/pull/99", Zone: "work",
			Body: "Exponential backoff prevents storm retries.",
		},
		HeadRef: "fix/queue-backoff", BaseRef: "main", ChecksSummary: "4 passed",
	}

	ma := initLoadedModel(t, ba, Options{
		StartTab:    "gh-prs",
		Zones:       []string{"personal", "work"},
		DefaultZone: "work",
		GHRepo:      "work-org/backend-api",
		GHState:     "open",
	})
	ma, _ = sendMsg(t, ma, tea.WindowSizeMsg{Width: 165, Height: 38})

	if ma.ScopeZone() != "work" {
		t.Fatalf("expected ScopeZone to remain 'work' despite tui.default_zone='personal', got %q", ma.ScopeZone())
	}
	ppA := ma.panels[PanelProjects].(projectsPanel)
	if ppA.ghRepoOverride != "work-org/backend-api" {
		t.Fatalf("expected ghRepoOverride='work-org/backend-api', got %q", ppA.ghRepoOverride)
	}
	ba.mu.Lock()
	calledWork := false
	for i, ck := range ba.ghRepoCalls {
		if ck == "work-org/backend-api|open" && i < len(ba.ghRepoZones) && ba.ghRepoZones[i] == "work" {
			calledWork = true
			break
		}
	}
	ba.mu.Unlock()
	if !calledWork {
		t.Fatalf("expected LoadRepoGHItems called for work-org/backend-api in zone 'work'; calls=%v zones=%v", ba.ghRepoCalls, ba.ghRepoZones)
	}
	outA := ui.StripANSI(ma.Render())
	for _, want := range []string{"work-org/backend-api", "#99", "Fix production queue backoff", "Exponential backoff prevents storm retries."} {
		if !strings.Contains(outA, want) {
			t.Fatalf("expected %q in cross-zone ws gh view:\n%s", want, outA)
		}
	}

	bb := newMockBackend()
	bb.settingsView.DefaultZone = "personal"
	bb.ghRepoItems["example/app|open"] = []GHItemRow{
		{
			Kind: "pr", Repo: "example/app", Number: 42, Title: "Keep CWD repository scoped",
			State: "open", Author: "bob", URL: "https://github.com/example/app/pull/42", Zone: "personal",
			UpdatedAt: "2026-11-27",
		},
	}
	bb.ghDetails["pr|example/app#42"] = GHItemDetail{
		GHItemRow: GHItemRow{
			Kind: "pr", Repo: "example/app", Number: 42, Title: "Keep CWD repository scoped",
			State: "open", Author: "bob", URL: "https://github.com/example/app/pull/42", Zone: "personal",
			Body: "Preserves target repository even when tui.default_zone matches active zone.",
		},
	}

	mb := initLoadedModel(t, bb, Options{
		StartTab:    "gh-prs",
		Zones:       []string{"personal", "work"},
		DefaultZone: "personal",
		GHRepo:      "example/app",
		GHState:     "open",
	})
	mb, _ = sendMsg(t, mb, tea.WindowSizeMsg{Width: 165, Height: 38})

	ppB := mb.panels[PanelProjects].(projectsPanel)
	if ppB.ghRepoOverride != "example/app" {
		t.Fatalf("expected ghRepoOverride='example/app' preserved instead of wiped onto repos[0], got %q", ppB.ghRepoOverride)
	}
	outB := ui.StripANSI(mb.Render())
	for _, want := range []string{"example/app", "#42", "Keep CWD repository scoped", "Preserves target repository"} {
		if !strings.Contains(outB, want) {
			t.Fatalf("expected %q in same-zone ws gh view:\n%s", want, outB)
		}
	}
}

func TestSystemPackageSearchSubViewPreviewZoomAndActions(t *testing.T) {
	b := newMockBackend()
	b.pkgDetails["brew:rip-cli"] = PackageDetail{
		ID:           "rip-cli",
		PackageName:  "rip-cli",
		Provider:     "brew",
		Repo:         "homebrew/core",
		Version:      "1.4.0",
		Homepage:     "https://example.invalid/rip-cli",
		License:      "MIT",
		Description:  "Detailed search preview description for rip-cli",
		Dependencies: []string{"pcre2"},
		Caveats:      "Run rip-cli --help to begin",
	}

	m := initLoadedModel(t, b, Options{StartTab: "pkgs"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 150, Height: 38})

	m, cmd := sendAction(t, m, 's')
	m = drainCmds(t, m, cmd)
	if m.panels[PanelPackages].ActiveSubView() != pkgSubSearch {
		t.Fatalf("expected a+s to switch to pkgSubSearch, got %d", m.panels[PanelPackages].ActiveSubView())
	}
	if !m.panels[PanelPackages].CapturingInput() {
		t.Fatalf("expected Search SubView to focus search input when empty")
	}

	m = typeString(t, m, "rip")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	out := ui.StripANSI(m.Render())
	for _, want := range []string{"rip-cli", "1.4.0", "Detailed search preview description for rip-cli", "https://example.invalid/rip-cli", "pcre2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in Search results & live preview:\n%s", want, out)
		}
	}

	m, _ = sendMsg(t, m, keyRune('='))
	if !m.Zoomed() {
		t.Fatalf("expected '=' to zoom Search pane")
	}
	m, _ = sendMsg(t, m, keyRune('='))
	if m.Zoomed() {
		t.Fatalf("expected second '=' to unzoom Search pane")
	}

	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	sp := m.panels[PanelPackages].(packagesPanel)
	if sp.searchProvider() == "all" {
		t.Fatalf("expected a+P to cycle search provider off 'all', got %q", sp.searchProvider())
	}

	m, cmd = sendMsg(t, m, keyRune('i'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd for Search Install & Track")
	}
	if len(b.addTrackExecCalls) == 0 || !strings.HasPrefix(b.addTrackExecCalls[len(b.addTrackExecCalls)-1], "rip-cli|") || !strings.HasSuffix(b.addTrackExecCalls[len(b.addTrackExecCalls)-1], "|true") || len(b.addedPkgCalls) != 0 {
		t.Fatalf("expected Install & Track exec handoff for rip-cli, exec=%v bg=%v", b.addTrackExecCalls, b.addedPkgCalls)
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "add", command: "ws pkg install --track rip-cli"})
	m = drainCmds(t, m, cmd)

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "search provider:brew hyperfine")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	sp = m.panels[PanelPackages].(packagesPanel)
	if m.ActivePanel() != PanelPackages || m.panels[PanelPackages].ActiveSubView() != pkgSubSearch {
		t.Fatalf("expected :search to open Packages -> Search")
	}
	if sp.searchQuery != "hyperfine" || sp.searchProvider() != "brew" {
		t.Fatalf("unexpected palette :search state: query=%q prov=%q", sp.searchQuery, sp.searchProvider())
	}
}

func cloneTokensWithOverrides(src map[string]string, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(src)+len(overrides))
	for k, v := range src {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func TestThemeFilterPreviewSelectedAndRevert(t *testing.T) {
	b := newMockBackend()
	baseDark := DefaultPalette().Tokens
	baseLight := defaultLightTokens()
	b.themeIDs = append(b.themeIDs, "tokyonight-dark", "tokyonight-light")
	b.palettes["tokyonight-dark"] = config.Palette{
		ID:     "tokyonight-dark",
		Name:   "tokyonight-dark",
		Mode:   "dark",
		Tokens: cloneTokensWithOverrides(baseDark, map[string]string{"name": "tokyonight-dark", "mode": "dark", "accent": "#7aa2f7"}),
	}
	b.palettes["tokyonight-light"] = config.Palette{
		ID:     "tokyonight-light",
		Name:   "tokyonight-light",
		Mode:   "light",
		Tokens: cloneTokensWithOverrides(baseLight, map[string]string{"name": "tokyonight-light", "mode": "light", "accent": "#2e7de9"}),
	}

	m := initLoadedModel(t, b, Options{})

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "theme tokyonight")
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubThemes {
		t.Fatalf("expected :theme tokyonight to open Workstation -> Themes, got panel=%d sub=%d", m.ActivePanel(), m.panels[PanelWorkstation].ActiveSubView())
	}
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "tokyonight-dark") {
		t.Fatalf("expected filtered Themes list to show tokyonight-dark:\n%s", out)
	}
	if strings.Contains(out, "gruvbox-light") {
		t.Fatalf("expected filtered Themes list to exclude gruvbox-light:\n%s", out)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.ActiveTheme() != "gruvbox-dark" {
		t.Fatalf("expected ActiveTheme to remain gruvbox-dark, got %q", m.ActiveTheme())
	}
	if m.PreviewTheme() != "tokyonight-dark" || m.Styles().ThemeID != "tokyonight-dark" {
		t.Fatalf("expected preview of tokyonight-dark, got preview=%q styles=%q", m.PreviewTheme(), m.Styles().ThemeID)
	}

	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.PreviewTheme() != "" || m.Styles().ThemeID != "gruvbox-dark" {
		t.Fatalf("expected a+t toggle off to restore gruvbox-dark, got preview=%q styles=%q", m.PreviewTheme(), m.Styles().ThemeID)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "tokyonight")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	out = ui.StripANSI(m.Render())
	if !strings.Contains(out, "tokyonight-dark") || !strings.Contains(out, "tokyonight-light") {
		t.Fatalf("expected '/' filter for tokyonight to show both variants:\n%s", out)
	}

	m, _ = sendMsg(t, m, keyRune('j'))
	m, cmd = sendAction(t, m, 't')
	m = drainCmds(t, m, cmd)
	if m.PreviewTheme() != "tokyonight-light" || m.Styles().ThemeID != "tokyonight-light" {
		t.Fatalf("expected preview of tokyonight-light, got preview=%q styles=%q", m.PreviewTheme(), m.Styles().ThemeID)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelWorkstation].FilterQuery() != "" {
		t.Fatalf("expected first Esc to clear Themes filter, got %q", m.panels[PanelWorkstation].FilterQuery())
	}
	if m.PreviewTheme() != "tokyonight-light" {
		t.Fatalf("expected first Esc to preserve active preview tokyonight-light, got %q", m.PreviewTheme())
	}
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	if m.PreviewTheme() != "" || m.Styles().ThemeID != "gruvbox-dark" {
		t.Fatalf("expected second Esc to clear preview back to gruvbox-dark, got preview=%q styles=%q", m.PreviewTheme(), m.Styles().ThemeID)
	}
}

func TestPanelsKeepKeybindingHintsInHeadingBar(t *testing.T) {
	forbidden := []string{
		": cmd  / filter",
		"F/S filter/sort  E export",
		"enter jump · tab GH · L locals",
		"Repo [</>/tab]:",
		"[a] All  ·  [t]",
		"[/] filter PRs",
		"not fetched · r sync",
		"from cache · r sync",
		"cached/idle · r fetch",
		"tab focus · r sync · L locals",
		"tab detail · y copy · A/M/X/m/K act",
		"enter read · </> repo · r refresh",
		"L/esc PRs & Issues · y path",
		"U update all · a/A add/track · D untrack",
		"t preview/off · enter/A all",
		"enter/a apply app · A apply all",
		"space mark · * all · tab preview · x exec",
		"/ fuzzy · space multi · y copy",
		"y copy · C borderless",
		"Full Command [y copy]",
		"enter detail · v reveal · y KEY=VAL · R rotate",
		"Press enter to open Env Detail",
		"Keys: v reveal/mask",
		"enter Env Detail · y KEY=VAL · v reveal",
		"enter detail · d diff files · s sync · t ssh",
		"Tailscale Join Command [Y copy]",
		"Content Diff Preview [v load]",
		"Press v to fetch unified line diff",
		"F filter · S sort · e edit · E export",
		"File Path [e edit · y copy]",
		"Declared In [e edit]",
		"s Save->1P · l Load<-1P · K Key",
		"a apply · C borderless",
		"A auto · R reload · x reset",
		"esc return",
	}

	assertCleanRender := func(label string, out string, wantHeading ...string) {
		t.Helper()
		for _, bad := range forbidden {
			if strings.Contains(out, bad) {
				t.Fatalf("%s unexpectedly rendered in-panel hint %q:\n%s", label, bad, out)
			}
		}
		header := strings.Join(strings.Split(out, "\n")[:6], "\n")
		for _, want := range wantHeading {
			if !strings.Contains(header, want) {
				t.Fatalf("%s expected %q in heading bar, got:\n%s", label, want, header)
			}
		}
	}

	tabs := []struct {
		tab  string
		want []string
	}{
		{"projects", []string{"Repo detail", "Pull repo"}},
		{"system", []string{"Pkg actions", "Refresh"}},
		{"env", []string{"Env Detail", "Env actions"}},
		{"fleet", []string{"Node detail", "Diff files"}},
		{"mise", []string{"Config detail", "Edit config"}},
		{"doctor", []string{"Full-screen diff", "Doctor actions"}},
		{"settings", []string{"Pick / Toggle"}},
	}

	for _, tc := range tabs {
		b := newMockBackend()
		m := initLoadedModel(t, b, Options{StartTab: tc.tab})
		m, cmd := sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
		m = drainCmds(t, m, cmd)
		assertCleanRender("tab:"+tc.tab, ui.StripANSI(m.Render()), tc.want...)
	}

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, cmd := sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drainCmds(t, m, cmd)
	out := ui.StripANSI(m.Render())
	if !strings.Contains(out, "Repo:") {
		t.Fatalf("expected factual Repo: scope line in GH preview:\n%s", out)
	}
	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	assertCleanRender("projects:gh-dedicated", ui.StripANSI(m.Render()), "PR/Issue actions")

	mSys := initLoadedModel(t, b, Options{StartTab: "system"})
	mSys, cmd = sendMsg(t, mSys, tea.WindowSizeMsg{Width: 140, Height: 40})
	mSys = drainCmds(t, mSys, cmd)
	for i := 0; i < 4; i++ {
		mSys, cmd = sendMsg(t, mSys, keyRune(']'))
		mSys = drainCmds(t, mSys, cmd)
		assertCleanRender(fmt.Sprintf("system:sub-%d", i+1), ui.StripANSI(mSys.Render()))
	}

	mFleet := initLoadedModel(t, b, Options{StartTab: "fleet"})
	mFleet, cmd = sendMsg(t, mFleet, tea.WindowSizeMsg{Width: 140, Height: 40})
	mFleet = drainCmds(t, mFleet, cmd)
	mFleet, cmd = sendMsg(t, mFleet, keyRune(']'))
	mFleet = drainCmds(t, mFleet, cmd)
	assertCleanRender("fleet:filesync", ui.StripANSI(mFleet.Render()), "Sync actions")

	mDoc := initLoadedModel(t, b, Options{StartTab: "workstation"})
	mDoc, cmd = sendMsg(t, mDoc, tea.WindowSizeMsg{Width: 140, Height: 40})
	mDoc = drainCmds(t, mDoc, cmd)
	for i := 0; i < 5; i++ {
		mDoc, cmd = sendMsg(t, mDoc, keyRune(']'))
		mDoc = drainCmds(t, mDoc, cmd)
		assertCleanRender(fmt.Sprintf("workstation:sub-%d", i+1), ui.StripANSI(mDoc.Render()))
	}

	mVault := initLoadedModel(t, b, Options{StartTab: "vault"})
	mVault, cmd = sendMsg(t, mVault, tea.WindowSizeMsg{Width: 140, Height: 40})
	mVault = drainCmds(t, mVault, cmd)
	assertCleanRender("env:vault", ui.StripANSI(mVault.Render()), "Vault actions")
}

func TestPackageActionsAndUpdateAllUseExecProcessHandoff(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "pkgs"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('u'))
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() {
		t.Fatalf("expected confirm modal on 'u'")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on confirmed package update")
	}
	if len(b.pkgActionExecCalls) != 1 || !strings.HasPrefix(b.pkgActionExecCalls[0], "update:") || len(b.pkgActions) != 0 {
		t.Fatalf("expected PackageActionExecCmd=1 ExecPackageAction=0, got exec=%v bg=%v", b.pkgActionExecCalls, b.pkgActions)
	}
	outdatedBefore := b.outdatedLoadCalls
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update", command: "ws pkg update"})
	m = drainCmds(t, m, cmd)
	if m.panels[PanelPackages].(packagesPanel).pkgRunning {
		t.Fatalf("expected pkgRunning cleared after pkgTTYFinishedMsg")
	}
	if b.outdatedLoadCalls <= outdatedBefore {
		t.Fatalf("expected outdated packages refreshed after pkgTTYFinishedMsg")
	}

	m, cmd = sendMsg(t, m, keyRune('U'))
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() {
		t.Fatalf("expected confirm modal on 'U'")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on confirmed 'U'")
	}
	if len(b.updateAllExecCalls) != 1 || len(b.updateAllCalls) != 0 {
		t.Fatalf("expected UpdateAllExecCmd=1 ExecUpdateAll=0, got exec=%v bg=%v", b.updateAllExecCalls, b.updateAllCalls)
	}
	doctorBefore := b.doctorLoadCalls
	outdatedBefore = b.outdatedLoadCalls
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update-all", command: "ws update --all"})
	m = drainCmds(t, m, cmd)
	if b.doctorLoadCalls <= doctorBefore || b.outdatedLoadCalls <= outdatedBefore {
		t.Fatalf("expected update-all return to refresh doctor (%d->%d) and outdated (%d->%d)",
			doctorBefore, b.doctorLoadCalls, outdatedBefore, b.outdatedLoadCalls)
	}

	m = typeString(t, m, ":update")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() {
		t.Fatalf("expected confirm modal on :update")
	}
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on confirmed :update")
	}
	if len(b.updateAllExecCalls) != 2 || b.updateAllExecCalls[1] || len(b.updateAllCalls) != 0 {
		t.Fatalf("expected :update to call UpdateAllExecCmd(false) with 0 background calls, exec=%v bg=%v", b.updateAllExecCalls, b.updateAllCalls)
	}
}

func extractBoundKeysFromPanelFile(t *testing.T, filename string) (directKeys []string, actionKeys []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	directSet := map[string]bool{}
	actionSet := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		isDirect := name == "Update" || (strings.HasPrefix(name, "update") && strings.HasSuffix(name, "Key") && !strings.Contains(name, "Modal"))
		isAction := name == "RunRowAction"
		if !isDirect && !isAction {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || sw.Tag == nil {
				return true
			}
			tagMatch := false
			if ident, ok := sw.Tag.(*ast.Ident); ok {
				if isDirect && ident.Name == "key" {
					tagMatch = true
				}
				if isAction && ident.Name == "actionKey" {
					tagMatch = true
				}
			}
			if call, ok := sw.Tag.(*ast.CallExpr); ok && isDirect {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "String" {
					tagMatch = true
				}
			}
			if !tagMatch {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					bl, ok := expr.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					val, err := strconv.Unquote(bl.Value)
					if err != nil || val == "" {
						continue
					}
					if isDirect {
						directSet[val] = true
					} else {
						actionSet[val] = true
					}
				}
			}
			return true
		})
	}
	for k := range directSet {
		directKeys = append(directKeys, k)
	}
	for k := range actionSet {
		actionKeys = append(actionKeys, k)
	}
	slices.Sort(directKeys)
	slices.Sort(actionKeys)
	return directKeys, actionKeys
}

func helpGroupKeyTokens(groups []helpGroup) (allKeys map[string]bool, rowActionKeys map[string]bool) {
	allKeys = map[string]bool{}
	rowActionKeys = map[string]bool{}
	addSpec := func(spec string) {
		s := strings.TrimSpace(spec)
		s = strings.TrimPrefix(s, "<")
		s = strings.TrimSuffix(s, ">")
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if s == "/" {
			allKeys["/"] = true
			return
		}
		if s == "</>" || s == "< / >" {
			allKeys["<"] = true
			allKeys[">"] = true
			return
		}
		if strings.HasPrefix(s, "ctrl+") && strings.Contains(s, "/") {
			parts := strings.Split(strings.TrimPrefix(s, "ctrl+"), "/")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					allKeys["ctrl+"+p] = true
				}
			}
			return
		}
		for _, tok := range strings.Split(s, "/") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if tok == "gg" {
				allKeys["g"] = true
			}
			allKeys[tok] = true
		}
	}
	for _, g := range groups {
		for _, r := range g.rows {
			if strings.HasPrefix(r[0], "a -> ") {
				k := strings.TrimSpace(strings.TrimPrefix(r[0], "a -> "))
				rowActionKeys[k] = true
				allKeys[k] = true
				continue
			}
			addSpec(r[0])
		}
	}
	return allKeys, rowActionKeys
}

func TestHelpDynamicPerPanelScrollableAndCoversAllBoundKeys(t *testing.T) {
	oldLookPath := execLookPath
	execLookPath = func(file string) (string, error) {
		return "/usr/local/bin/" + file, nil
	}
	defer func() { execLookPath = oldLookPath }()

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m, _ = sendMsg(t, m, keyRune('?'))
	if !m.ShowHelp() {
		t.Fatalf("expected '?' to open help modal")
	}
	envHelp := ui.StripANSI(m.Render())
	for _, want := range []string{"Help · Env & Secrets", "ENV & SECRETS · VARS", "ROW ACTIONS (a)", "a -> R", "Rotate", "GLOBAL VERBS"} {
		if !strings.Contains(envHelp, want) {
			t.Fatalf("expected Env help to contain %q:\n%s", want, envHelp)
		}
	}
	for _, bad := range []string{"Pull repo", "Force refresh", "Checkout"} {
		if strings.Contains(envHelp, bad) {
			t.Fatalf("expected Env help NOT to contain hard-coded Projects/wrong label %q:\n%s", bad, envHelp)
		}
	}

	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 72, Height: 16})
	if m.HelpScroll() != 0 {
		t.Fatalf("expected HelpScroll=0 initially, got %d", m.HelpScroll())
	}
	beforeScroll := ui.StripANSI(m.Render())
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, keyRune('j'))
	if m.HelpScroll() == 0 {
		t.Fatalf("expected 'j' in short terminal to scroll help modal")
	}
	afterScroll := ui.StripANSI(m.Render())
	if afterScroll == beforeScroll {
		t.Fatalf("expected rendered help modal content to change after scrolling")
	}
	m, _ = sendMsg(t, m, keyRune('G'))
	bottomScroll := m.HelpScroll()
	if bottomScroll == 0 {
		t.Fatalf("expected 'G' to scroll to bottom of help")
	}
	m, _ = sendMsg(t, m, keyRune('g'))
	m, _ = sendMsg(t, m, keyRune('g'))
	if m.HelpScroll() != 0 {
		t.Fatalf("expected 'gg' to scroll back to top of help, got %d", m.HelpScroll())
	}
	m, _ = sendMsg(t, m, keyRune('q'))
	if m.ShowHelp() {
		t.Fatalf("expected 'q' to close help modal")
	}

	ignoredDirect := map[string]bool{
		"up": true, "down": true, "left": true, "right": true,
		"pgup": true, "pgdown": true, "home": true, "end": true,
		"ctrl+n": true, "ctrl+p": true, " ": true, "shift+tab": true,
		"h": true,
	}

	panelFiles := []struct {
		panelID int
		file    string
	}{
		{PanelOverview, "panel_overview.go"},
		{PanelProjects, "panel_projects.go"},
		{PanelPackages, "panel_packages.go"},
		{PanelPackages, "panel_mise.go"},
		{PanelEnv, "panel_env.go"},
		{PanelEnv, "panel_vault.go"},
		{PanelFleet, "panel_fleet.go"},
		{PanelWorkstation, "panel_workstation.go"},
		{PanelWorkstation, "panel_doctor.go"},
		{PanelWorkstation, "panel_settings.go"},
	}

	for _, pf := range panelFiles {
		directKeys, actionKeys := extractBoundKeysFromPanelFile(t, pf.file)
		mPanel := initLoadedModel(t, b, Options{})
		mPanel.activePanel = pf.panelID
		mPanel.splitMode[pf.panelID] = SplitVertical

		seenDirect := map[string]bool{}
		seenActions := map[string]bool{}
		collect := func(mod Model) {
			dk, ak := helpGroupKeyTokens(mod.buildHelpGroups())
			for k := range dk {
				seenDirect[k] = true
			}
			for k := range ak {
				seenActions[k] = true
			}
		}

		subCount := max(1, len(mPanel.panels[pf.panelID].SubViews()))
		for sub := 0; sub < subCount; sub++ {
			mPanel.panels[pf.panelID] = mPanel.panels[pf.panelID].SetSubView(sub)
			for _, fp := range []bool{false, true} {
				mPanel.focusPreview = fp
				collect(mPanel)
			}
		}
		if pf.panelID == PanelProjects {
			pp := mPanel.panels[PanelProjects].(projectsPanel)
			pp.subView = projSubRepos
			pp.repoInfoMode = true
			mPanel.panels[PanelProjects] = pp
			mPanel.focusPreview = true
			collect(mPanel)
		}
		if pf.panelID == PanelEnv {
			ep := mPanel.panels[PanelEnv].(envPanel)
			ep.drillKey = "API_KEY"
			mPanel.panels[PanelEnv] = ep
			mPanel.focusPreview = false
			collect(mPanel)
		}

		for _, k := range directKeys {
			if ignoredDirect[k] {
				continue
			}
			if !seenDirect[k] {
				t.Errorf("%s: bound direct key %q missing from panel help", pf.file, k)
			}
		}
		for _, k := range actionKeys {
			if !seenActions[k] {
				t.Errorf("%s: RowAction key %q missing from panel ROW ACTIONS help", pf.file, k)
			}
		}
	}
}

func TestActionMenuExecutionAndConfirmations(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "pkgs"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 38})

	m, _ = sendMsg(t, m, keyRune('a'))
	if !m.HasActionMenu() {
		t.Fatalf("expected 'a' to open per-row action menu")
	}
	menuOut := ui.StripANSI(m.Render())
	for _, want := range []string{"Actions ·", "<t>", "Track untracked", "<n>", "Add & track package", "<D>", "Untrack package"} {
		if !strings.Contains(menuOut, want) {
			t.Fatalf("expected Packages action menu to contain %q:\n%s", want, menuOut)
		}
	}

	m, _ = sendMsg(t, m, keyRune('q'))
	if m.HasActionMenu() {
		t.Fatalf("expected 'q' to close action menu without running action")
	}

	m, _ = sendMsg(t, m, keyRune('a'))
	m, _ = sendMsg(t, m, keyRune('j'))
	m, _ = sendMsg(t, m, keyRune('j'))
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.HasActionMenu() {
		t.Fatalf("expected action menu closed after Enter")
	}
	if !m.HasConfirmModal() {
		t.Fatalf("expected Untrack package via action menu Enter to open confirmation modal")
	}
	m, _ = sendMsg(t, m, keyRune('q'))
	if m.HasConfirmModal() {
		t.Fatalf("expected 'q' to cancel confirmation modal")
	}
	if len(b.untrackedCalls) != 0 {
		t.Fatalf("expected 0 untrackedCalls after cancelling modal, got %v", b.untrackedCalls)
	}
}

func TestCommandPaletteCLINamesFixApplyAndUpdateConsistency(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 38})

	navCases := []struct {
		cmd     string
		panel   int
		subView int
	}{
		{"pkg", PanelPackages, pkgSubInstalled},
		{"pkgs", PanelPackages, pkgSubInstalled},
		{"packages", PanelPackages, pkgSubInstalled},
		{"repo", PanelProjects, projSubRepos},
		{"repos", PanelProjects, projSubRepos},
		{"hist", PanelWorkstation, wsSubHistory},
		{"history", PanelWorkstation, wsSubHistory},
		{"config", PanelWorkstation, wsSubSettings},
		{"settings", PanelWorkstation, wsSubSettings},
	}
	for _, tc := range navCases {
		m, _ = sendMsg(t, m, keyRune(':'))
		m = typeString(t, m, tc.cmd)
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.panel || m.panels[tc.panel].ActiveSubView() != tc.subView {
			t.Fatalf(":%s landed on panel=%d sub=%d, want panel=%d sub=%d", tc.cmd, m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView(), tc.panel, tc.subView)
		}
	}

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "fix")
	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "Apply") {
		t.Fatalf("expected :fix to open Doctor fix confirmation modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "apply")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if !m.HasConfirmModal() || !strings.Contains(ui.StripANSI(m.Render()), "ws apply") {
		t.Fatalf("expected :apply to open ws apply confirmation modal:\n%s", ui.StripANSI(m.Render()))
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "apply --dry-run")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.HasConfirmModal() {
		t.Fatalf("expected :apply --dry-run to run without confirmation modal")
	}
	if m.ActivePanel() != PanelOutput {
		t.Fatalf("expected :apply --dry-run to open Output console, got panel=%d", m.ActivePanel())
	}

	m, _ = sendMsg(t, m, keyRune('3'))
	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "pkg")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)

	m, cmd = sendMsg(t, m, keyRune('U'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected ExecProcess cmd for U")
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update", command: "ws update"})
	m = drainCmds(t, m, cmd)

	m, _ = sendMsg(t, m, keyRune(':'))
	m = typeString(t, m, "update")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('y'))
	if cmd == nil {
		t.Fatalf("expected ExecProcess cmd for :update")
	}
	m, cmd = sendMsg(t, m, pkgTTYFinishedMsg{action: "update", command: "ws update"})
	m = drainCmds(t, m, cmd)

	if len(b.updateAllExecCalls) != 2 || b.updateAllExecCalls[0] != b.updateAllExecCalls[1] || b.updateAllExecCalls[0] {
		t.Fatalf("expected U and :update to both run with all=false, got %v", b.updateAllExecCalls)
	}
}

func TestSimplifiedPaneBindingsAndTUIMouseSetting(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected Env to start SplitOff, got %v", m.ActiveSplit())
	}
	for _, removed := range []rune{'|', '-', '+', 'W', 'C'} {
		m, _ = sendMsg(t, m, keyRune(removed))
		if m.ActiveSplit() != SplitOff || m.Zoomed() {
			t.Fatalf("expected removed pane key %q to be a no-op, split=%v zoom=%v", removed, m.ActiveSplit(), m.Zoomed())
		}
	}

	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitVertical {
		t.Fatalf("expected 1st 'p' -> SplitVertical, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if !m.FocusPreview() {
		t.Fatalf("expected Tab to focus preview")
	}
	m, _ = sendMsg(t, m, keyRune('='))
	if !m.Zoomed() {
		t.Fatalf("expected '=' to zoom focused pane")
	}
	m, _ = sendMsg(t, m, keyRune('='))
	if m.Zoomed() {
		t.Fatalf("expected second '=' to unzoom")
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitHorizontal {
		t.Fatalf("expected 2nd 'p' -> SplitHorizontal, got %v", m.ActiveSplit())
	}
	m, _ = sendMsg(t, m, keyRune('p'))
	if m.ActiveSplit() != SplitOff {
		t.Fatalf("expected 3rd 'p' -> SplitOff, got %v", m.ActiveSplit())
	}

	if !m.MouseEnabled() || m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("expected tui.mouse enabled by default")
	}
	mouseOff := false
	svOff := b.settingsView
	svOff.Mouse = &mouseOff
	m, _ = sendMsg(t, m, settingSavedMsg{key: "tui.mouse", val: "false", view: svOff})
	if m.MouseEnabled() || m.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("expected tui.mouse=false to disable mouse capture")
	}
	m, _ = sendMsg(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 15, Y: 12})
	if m.panels[PanelEnv].(envPanel).cursor != 0 {
		t.Fatalf("expected mouse wheel ignored when tui.mouse=false")
	}
}

func TestEscAndQBackOneLevelAndQuitAtTopLevel(t *testing.T) {
	isQuitCmd := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}

	for _, backKey := range []tea.KeyPressMsg{
		{Code: tea.KeyEsc},
		keyRune('q'),
	} {
		name := backKey.String()
		t.Run(name, func(t *testing.T) {
			b := newMockBackend()
			m := initLoadedModel(t, b, Options{StartTab: "env"})
			m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 36})

			m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
			m, _ = sendMsg(t, m, keyRune('/'))
			m = typeString(t, m, "very-long")
			var cmd tea.Cmd
			m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = drainCmds(t, m, cmd)

			m, _ = sendMsg(t, m, keyRune('p'))
			m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
			m, _ = sendMsg(t, m, keyRune('='))
			m, _ = sendMsg(t, m, keyRune('?'))

			m, cmd = sendMsg(t, m, backKey)
			if m.ShowHelp() || isQuitCmd(cmd) {
				t.Fatalf("%s step 1 should close help without quitting", name)
			}

			m, cmd = sendMsg(t, m, backKey)
			if m.Zoomed() || isQuitCmd(cmd) {
				t.Fatalf("%s step 2 should exit zoom without quitting", name)
			}

			m, cmd = sendMsg(t, m, backKey)
			m = drainCmds(t, m, cmd)
			if m.panels[PanelEnv].FilterQuery() != "" || isQuitCmd(cmd) {
				t.Fatalf("%s step 3 should clear active filter without quitting", name)
			}

			m, cmd = sendMsg(t, m, backKey)
			if m.FocusPreview() || isQuitCmd(cmd) {
				t.Fatalf("%s step 4 should unfocus split preview without quitting", name)
			}

			m, cmd = sendMsg(t, m, backKey)
			if m.panels[PanelEnv].SelectedCount() != 0 || isQuitCmd(cmd) {
				t.Fatalf("%s step 5 should clear selection without quitting", name)
			}

			m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = drainCmds(t, m, cmd)
			if !m.panels[PanelEnv].CanPop() {
				t.Fatalf("%s expected Env drill-down active", name)
			}
			m, cmd = sendMsg(t, m, backKey)
			if m.panels[PanelEnv].CanPop() || isQuitCmd(cmd) {
				t.Fatalf("%s step 6 should exit Env drill-down without quitting", name)
			}

			m, cmd = sendMsg(t, m, backKey)
			if !isQuitCmd(cmd) {
				t.Fatalf("%s at top level should quit", name)
			}
		})
	}

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "env"})
	m, _ = sendMsg(t, m, keyRune('/'))
	m, _ = sendMsg(t, m, keyRune('q'))
	if !m.CommandBarActive() || m.cmdBar.input.Value() != "q" {
		t.Fatalf("expected 'q' inside '/' filter input to type 'q', active=%v val=%q", m.CommandBarActive(), m.cmdBar.input.Value())
	}
	var ctrlCCmd tea.Cmd
	m, ctrlCCmd = sendMsg(t, m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !isQuitCmd(ctrlCCmd) {
		t.Fatalf("expected ctrl+c to always quit even inside filter input")
	}
}

func TestRepoRowActionsBrowserEditorAndLazygit(t *testing.T) {
	urlCases := []struct {
		remote string
		want   string
	}{
		{"git@github.com:example/api-service.git", "https://github.com/example/api-service"},
		{"https://github.com/example/api-service.git", "https://github.com/example/api-service"},
		{"ssh://git@gitlab.example.com/team/repo.git", "https://gitlab.example.com/team/repo"},
		{"example/demo-web", "https://github.com/example/demo-web"},
		{"codecommit::us-east-1://my-repo", ""},
		{"/local/path/repo", ""},
	}
	for _, tc := range urlCases {
		if got := repoWebURL(tc.remote); got != tc.want {
			t.Fatalf("repoWebURL(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}

	var opened []string
	oldOpen := urlOpener
	urlOpener = func(u string) error {
		opened = append(opened, u)
		return nil
	}
	defer func() { urlOpener = oldOpen }()

	oldLookPath := execLookPath
	execLookPath = func(file string) (string, error) {
		if file == "lazygit" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + file, nil
	}
	defer func() { execLookPath = oldLookPath }()

	b := newMockBackend()
	m := initLoadedModel(t, b, Options{StartTab: "projects"})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 38})

	for _, h := range m.panels[PanelProjects].KeyHints(SplitVertical, false) {
		if h[0] == "l" {
			t.Fatalf("expected 'l' hidden from Repos KeyHints when lazygit is not installed")
		}
	}
	m, cmd := sendMsg(t, m, keyRune('l'))
	if cmd != nil {
		t.Fatalf("expected 'l' to be a no-op when lazygit is not installed")
	}

	execLookPath = func(file string) (string, error) {
		return "/usr/local/bin/" + file, nil
	}
	foundLazygitHint := false
	for _, h := range m.panels[PanelProjects].KeyHints(SplitVertical, false) {
		if h[0] == "l" {
			foundLazygitHint = true
		}
	}
	if !foundLazygitHint {
		t.Fatalf("expected 'l' shown in Repos KeyHints when lazygit is installed")
	}

	m, cmd = sendMsg(t, m, keyRune('o'))
	m = drainCmds(t, m, cmd)
	if len(opened) != 1 || opened[0] != "https://github.com/example/api-service" {
		t.Fatalf("expected 'o' on repo row to open remote URL in browser, got %v", opened)
	}

	t.Setenv("VISUAL", "nvim -u NONE")
	t.Setenv("EDITOR", "vi")
	edCmd, err := buildRepoEditorCmd("/tmp/example/work/code/api-service")
	if err != nil || edCmd == nil || filepath.Base(edCmd.Path) != "nvim" || edCmd.Dir != "/tmp/example/work/code/api-service" {
		t.Fatalf("expected VISUAL precedence for repo editor cmd, got cmd=%+v err=%v", edCmd, err)
	}
	m, cmd = sendMsg(t, m, keyRune('e'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on 'e' in Repos view")
	}
	m, cmd = sendMsg(t, m, repoEditorFinishedMsg{repo: "api-service"})
	m = drainCmds(t, m, cmd)

	lgCmd, err := buildRepoLazygitCmd("/tmp/example/work/code/api-service")
	if err != nil || lgCmd == nil || lgCmd.Dir != "/tmp/example/work/code/api-service" {
		t.Fatalf("expected lazygit cmd with repo Dir, got cmd=%+v err=%v", lgCmd, err)
	}
	m, cmd = sendMsg(t, m, keyRune('l'))
	if cmd == nil {
		t.Fatalf("expected tea.ExecProcess cmd on 'l' in Repos view when lazygit installed")
	}
	m, cmd = sendMsg(t, m, repoLazygitFinishedMsg{repo: "api-service"})
	m = drainCmds(t, m, cmd)
}

func TestTabReorgNumberKeysAndSubViewOrder(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})

	if m.ActivePanel() != PanelOverview {
		t.Fatalf("expected default landing panel PanelOverview (%d), got %d", PanelOverview, m.ActivePanel())
	}

	wantTabs := []struct {
		key        rune
		panel      int
		shortTitle string
		subViews   []string
	}{
		{'1', PanelOverview, "Overview", nil},
		{'2', PanelProjects, "Projects", []string{"Repos", "Pull Requests", "Issues"}},
		{'3', PanelPackages, "Packages", []string{"Installed", "Search", "Mise Tools", "Mise Configs", "Cleanup"}},
		{'4', PanelEnv, "Env & Secrets", []string{"Vars", "Files", "Vault"}},
		{'5', PanelFleet, "Fleet", []string{"Nodes", "File Sync"}},
		{'6', PanelWorkstation, "Workstation", []string{"Doctor", "Themes", "Presets", "Settings", "Watched Files", "History"}},
		{'0', PanelOutput, "Output", nil},
	}

	for _, tc := range wantTabs {
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, keyRune(tc.key))
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.panel {
			t.Fatalf("key %q landed on panel %d, want %d", tc.key, m.ActivePanel(), tc.panel)
		}
		gotTitle := m.panels[tc.panel].ShortTitle()
		if gotTitle != tc.shortTitle {
			t.Fatalf("panel %d ShortTitle = %q, want %q", tc.panel, gotTitle, tc.shortTitle)
		}
		gotSubs := m.panels[tc.panel].SubViews()
		if !slices.Equal(gotSubs, tc.subViews) {
			t.Fatalf("panel %d (%s) SubViews = %v, want %v", tc.panel, gotTitle, gotSubs, tc.subViews)
		}
		if len(tc.subViews) > 1 {
			startSub := m.panels[tc.panel].ActiveSubView()
			for step := 1; step <= len(tc.subViews); step++ {
				if m.panels[tc.panel].CapturingInput() {
					m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
					m = drainCmds(t, m, cmd)
				}
				m, cmd = sendMsg(t, m, keyRune(']'))
				m = drainCmds(t, m, cmd)
				wantIdx := (startSub + step) % len(tc.subViews)
				if got := m.panels[tc.panel].ActiveSubView(); got != wantIdx {
					t.Fatalf("panel %s ']' step %d: subView=%d, want %d", gotTitle, step, got, wantIdx)
				}
			}
			if m.panels[tc.panel].CapturingInput() {
				m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
				m = drainCmds(t, m, cmd)
			}
			m, cmd = sendMsg(t, m, keyRune('['))
			m = drainCmds(t, m, cmd)
			wantPrev := (startSub + len(tc.subViews) - 1) % len(tc.subViews)
			if got := m.panels[tc.panel].ActiveSubView(); got != wantPrev {
				t.Fatalf("panel %s '[': subView=%d, want %d", gotTitle, got, wantPrev)
			}
		}
	}

	m, _ = sendMsg(t, m, keyRune('2'))
	for _, unused := range []rune{'7', '8'} {
		m, _ = sendMsg(t, m, keyRune(unused))
		if m.ActivePanel() != PanelProjects {
			t.Fatalf("expected unused key %q not to switch panels, got %d", unused, m.ActivePanel())
		}
	}
}

func TestAllOldAndNewStartTabViewNames(t *testing.T) {
	b := newMockBackend()
	cases := []struct {
		name    string
		panel   int
		subView int
		focusGH bool
	}{
		{"", PanelOverview, 0, false},
		{"overview", PanelOverview, 0, false},
		{"status", PanelOverview, 0, false},
		{"home", PanelOverview, 0, false},
		{"1", PanelOverview, 0, false},
		{"8", PanelOverview, 0, false},
		{"projects", PanelProjects, projSubRepos, false},
		{"project", PanelProjects, projSubRepos, false},
		{"repos", PanelProjects, projSubRepos, false},
		{"repo", PanelProjects, projSubRepos, false},
		{"git", PanelProjects, projSubRepos, false},
		{"gg", PanelProjects, projSubRepos, false},
		{"2", PanelProjects, projSubRepos, false},
		{"gh", PanelProjects, projSubRepos, true},
		{"prs", PanelProjects, projSubRepos, true},
		{"pr", PanelProjects, projSubRepos, true},
		{"issues", PanelProjects, projSubRepos, true},
		{"issue", PanelProjects, projSubRepos, true},
		{"gh-prs", PanelProjects, projSubPRs, false},
		{"gh-issues", PanelProjects, projSubIssues, false},
		{"packages", PanelPackages, pkgSubInstalled, false},
		{"pkg", PanelPackages, pkgSubInstalled, false},
		{"pkgs", PanelPackages, pkgSubInstalled, false},
		{"installed", PanelPackages, pkgSubInstalled, false},
		{"system", PanelPackages, pkgSubInstalled, false},
		{"sys", PanelPackages, pkgSubInstalled, false},
		{"3", PanelPackages, pkgSubInstalled, false},
		{"pkg-search", PanelPackages, pkgSubSearch, false},
		{"search", PanelPackages, pkgSubSearch, false},
		{"browse", PanelPackages, pkgSubSearch, false},
		{"pkg-browse", PanelPackages, pkgSubSearch, false},
		{"mise-tools", PanelPackages, pkgSubMiseTools, false},
		{"mise-outdated", PanelPackages, pkgSubMiseTools, false},
		{"mise", PanelPackages, pkgSubMiseConfigs, false},
		{"mise-configs", PanelPackages, pkgSubMiseConfigs, false},
		{"cleanup", PanelPackages, pkgSubCleanup, false},
		{"clean", PanelPackages, pkgSubCleanup, false},
		{"env", PanelEnv, envSubVars, false},
		{"secrets", PanelEnv, envSubVars, false},
		{"tokens", PanelEnv, envSubVars, false},
		{"vars", PanelEnv, envSubVars, false},
		{"4", PanelEnv, envSubVars, false},
		{"envfiles", PanelEnv, envSubFiles, false},
		{"files", PanelEnv, envSubFiles, false},
		{"vault", PanelEnv, envSubVault, false},
		{"db", PanelEnv, envSubVault, false},
		{"1password", PanelEnv, envSubVault, false},
		{"op", PanelEnv, envSubVault, false},
		{"fleet", PanelFleet, fleetSubNodes, false},
		{"nodes", PanelFleet, fleetSubNodes, false},
		{"tailscale", PanelFleet, fleetSubNodes, false},
		{"vm", PanelFleet, fleetSubNodes, false},
		{"5", PanelFleet, fleetSubNodes, false},
		{"filesync", PanelFleet, fleetSubFileSync, false},
		{"sync-files", PanelFleet, fleetSubFileSync, false},
		{"diff-files", PanelFleet, fleetSubFileSync, false},
		{"fleet-diff", PanelFleet, fleetSubFileSync, false},
		{"workstation", PanelWorkstation, wsSubDoctor, false},
		{"ws", PanelWorkstation, wsSubDoctor, false},
		{"doctor", PanelWorkstation, wsSubDoctor, false},
		{"doc", PanelWorkstation, wsSubDoctor, false},
		{"6", PanelWorkstation, wsSubDoctor, false},
		{"themes", PanelWorkstation, wsSubThemes, false},
		{"theme", PanelWorkstation, wsSubThemes, false},
		{"presets", PanelWorkstation, wsSubPresets, false},
		{"preset", PanelWorkstation, wsSubPresets, false},
		{"settings", PanelWorkstation, wsSubSettings, false},
		{"prefs", PanelWorkstation, wsSubSettings, false},
		{"config", PanelWorkstation, wsSubSettings, false},
		{"7", PanelWorkstation, wsSubSettings, false},
		{"watched", PanelWorkstation, wsSubWatchedFiles, false},
		{"watched-files", PanelWorkstation, wsSubWatchedFiles, false},
		{"history", PanelWorkstation, wsSubHistory, false},
		{"hist", PanelWorkstation, wsSubHistory, false},
		{"output", PanelOutput, 0, false},
		{"out", PanelOutput, 0, false},
		{"jobs", PanelOutput, 0, false},
		{"0", PanelOutput, 0, false},
	}

	for _, tc := range cases {
		m := NewModel(b, Options{StartTab: tc.name})
		if m.ActivePanel() != tc.panel || m.panels[tc.panel].ActiveSubView() != tc.subView || m.FocusPreview() != tc.focusGH {
			t.Errorf("StartTab=%q -> panel=%d sub=%d focusGH=%v, want panel=%d sub=%d focusGH=%v",
				tc.name, m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView(), m.FocusPreview(),
				tc.panel, tc.subView, tc.focusGH)
		}
	}
}

func TestAllOldAndNewCommandPaletteViewNames(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})

	cases := []struct {
		cmd     string
		panel   int
		subView int
	}{
		{"overview", PanelOverview, 0},
		{"home", PanelOverview, 0},
		{"1", PanelOverview, 0},
		{"8", PanelOverview, 0},
		{"repo", PanelProjects, projSubRepos},
		{"repos", PanelProjects, projSubRepos},
		{"projects", PanelProjects, projSubRepos},
		{"2", PanelProjects, projSubRepos},
		{"gh", PanelProjects, projSubRepos},
		{"prs", PanelProjects, projSubRepos},
		{"pr", PanelProjects, projSubRepos},
		{"issues", PanelProjects, projSubRepos},
		{"issue", PanelProjects, projSubRepos},
		{"packages", PanelPackages, pkgSubInstalled},
		{"pkg", PanelPackages, pkgSubInstalled},
		{"pkgs", PanelPackages, pkgSubInstalled},
		{"installed", PanelPackages, pkgSubInstalled},
		{"system", PanelPackages, pkgSubInstalled},
		{"sys", PanelPackages, pkgSubInstalled},
		{"3", PanelPackages, pkgSubInstalled},
		{"pkg-search", PanelPackages, pkgSubSearch},
		{"search", PanelPackages, pkgSubSearch},
		{"browse", PanelPackages, pkgSubSearch},
		{"psearch", PanelPackages, pkgSubSearch},
		{"mise-tools", PanelPackages, pkgSubMiseTools},
		{"mise-outdated", PanelPackages, pkgSubMiseTools},
		{"mise", PanelPackages, pkgSubMiseConfigs},
		{"mise-configs", PanelPackages, pkgSubMiseConfigs},
		{"clean", PanelPackages, pkgSubCleanup},
		{"cleanup", PanelPackages, pkgSubCleanup},
		{"env", PanelEnv, envSubVars},
		{"secrets", PanelEnv, envSubVars},
		{"vars", PanelEnv, envSubVars},
		{"tokens", PanelEnv, envSubVars},
		{"4", PanelEnv, envSubVars},
		{"envfiles", PanelEnv, envSubFiles},
		{"files", PanelEnv, envSubFiles},
		{"vault", PanelEnv, envSubVault},
		{"db", PanelEnv, envSubVault},
		{"1password", PanelEnv, envSubVault},
		{"op", PanelEnv, envSubVault},
		{"fleet", PanelFleet, fleetSubNodes},
		{"nodes", PanelFleet, fleetSubNodes},
		{"5", PanelFleet, fleetSubNodes},
		{"filesync", PanelFleet, fleetSubFileSync},
		{"sync-files", PanelFleet, fleetSubFileSync},
		{"diff-files", PanelFleet, fleetSubFileSync},
		{"workstation", PanelWorkstation, wsSubDoctor},
		{"ws", PanelWorkstation, wsSubDoctor},
		{"doctor", PanelWorkstation, wsSubDoctor},
		{"doc", PanelWorkstation, wsSubDoctor},
		{"6", PanelWorkstation, wsSubDoctor},
		{"themes", PanelWorkstation, wsSubThemes},
		{"theme", PanelWorkstation, wsSubThemes},
		{"presets", PanelWorkstation, wsSubPresets},
		{"preset", PanelWorkstation, wsSubPresets},
		{"config", PanelWorkstation, wsSubSettings},
		{"settings", PanelWorkstation, wsSubSettings},
		{"prefs", PanelWorkstation, wsSubSettings},
		{"7", PanelWorkstation, wsSubSettings},
		{"watched", PanelWorkstation, wsSubWatchedFiles},
		{"watched-files", PanelWorkstation, wsSubWatchedFiles},
		{"hist", PanelWorkstation, wsSubHistory},
		{"history", PanelWorkstation, wsSubHistory},
		{"out", PanelOutput, 0},
		{"output", PanelOutput, 0},
		{"jobs", PanelOutput, 0},
		{"0", PanelOutput, 0},
	}

	for _, tc := range cases {
		if m.panels[m.ActivePanel()].CapturingInput() {
			m, _ = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
		}
		m, _ = sendMsg(t, m, keyRune(':'))
		m = typeString(t, m, tc.cmd)
		var cmd tea.Cmd
		m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
		m = drainCmds(t, m, cmd)
		if m.ActivePanel() != tc.panel || m.panels[tc.panel].ActiveSubView() != tc.subView {
			t.Errorf(":%s -> panel=%d sub=%d, want panel=%d sub=%d",
				tc.cmd, m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView(), tc.panel, tc.subView)
		}
	}
}

func TestWorkstationPresetsSubViewAndGlobalShortcut(t *testing.T) {
	b := newMockBackend()
	m := initLoadedModel(t, b, Options{})
	m, _ = sendMsg(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})

	var cmd tea.Cmd
	m, cmd = sendMsg(t, m, keyRune('P'))
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubPresets {
		t.Fatalf("expected 'P' to jump to Workstation -> Presets, got panel=%d sub=%d", m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView())
	}
	out := ui.StripANSI(m.Render())
	for _, want := range []string{"mac-workstation", "ubuntu-runner", "Presets"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in Workstation -> Presets view:\n%s", want, out)
		}
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelOverview {
		t.Fatalf("expected ctrl+o to jump back to Overview, got panel=%d", m.ActivePanel())
	}

	m, cmd = sendMsg(t, m, keyRune('3'))
	m = drainCmds(t, m, cmd)
	m, cmd = sendAction(t, m, 'P')
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubPresets {
		t.Fatalf("expected Packages a+P to jump to Workstation -> Presets, got panel=%d sub=%d", m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView())
	}

	m, _ = sendMsg(t, m, keyRune('/'))
	m = typeString(t, m, "ubuntu")
	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	filteredOut := ui.StripANSI(m.Render())
	if !strings.Contains(filteredOut, "ubuntu-runner") {
		t.Fatalf("expected filtered presets to show ubuntu-runner:\n%s", filteredOut)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelWorkstation || m.panels[PanelWorkstation].ActiveSubView() != wsSubPresets {
		t.Fatalf("expected Enter in Presets to stay on Workstation -> Presets, got panel=%d sub=%d", m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView())
	}
	if cur, ok := m.panels[PanelPackages].(packagesPanel).selectedPreset(); !ok || cur.ID != "ubuntu-runner" {
		t.Fatalf("expected Enter in Presets to activate ubuntu-runner in Packages, got %+v ok=%v", cur, ok)
	}

	m, cmd = sendMsg(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m = drainCmds(t, m, cmd)
	m, cmd = sendMsg(t, m, keyRune('o'))
	m = drainCmds(t, m, cmd)
	if m.ActivePanel() != PanelPackages || m.panels[PanelPackages].ActiveSubView() != pkgSubInstalled {
		t.Fatalf("expected 'o' in Presets to jump to Packages -> Installed, got panel=%d sub=%d", m.ActivePanel(), m.panels[m.ActivePanel()].ActiveSubView())
	}
	if cur, ok := m.panels[PanelPackages].(packagesPanel).selectedPreset(); !ok || cur.ID != "mac-workstation" {
		t.Fatalf("expected 'o' on first preset to activate mac-workstation in Packages, got %+v ok=%v", cur, ok)
	}
}
