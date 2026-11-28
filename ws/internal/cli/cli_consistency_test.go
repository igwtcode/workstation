package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

func TestGroupCommandsRejectUnknownSubcommandsWithSuggestions(t *testing.T) {
	fx := setupFixture(t)

	cases := []struct {
		args       []string
		wantSub    string
		wantSug    string
		wantParent string
	}{
		{args: []string{"pkg", "lst"}, wantSub: "lst", wantSug: "list", wantParent: "ws pkg"},
		{args: []string{"pkg", "definitely-bogus"}, wantSub: "definitely-bogus", wantParent: "ws pkg"},
		{args: []string{"env", "lst"}, wantSub: "lst", wantSug: "list", wantParent: "ws env"},
		{args: []string{"repo", "clne"}, wantSub: "clne", wantSug: "clone", wantParent: "ws repo"},
		{args: []string{"fleet", "stats"}, wantSub: "stats", wantSug: "status", wantParent: "ws fleet"},
		{args: []string{"db", "stats"}, wantSub: "stats", wantSug: "status", wantParent: "ws db"},
		{args: []string{"db", "key", "ini"}, wantSub: "ini", wantSug: "init", wantParent: "ws db key"},
		{args: []string{"mise", "lst"}, wantSub: "lst", wantSug: "list", wantParent: "ws mise"},
		{args: []string{"config", "shw"}, wantSub: "shw", wantSug: "show", wantParent: "ws config"},
		{args: []string{"zone", "stats"}, wantSub: "stats", wantSug: "status", wantParent: "ws zone"},
		{args: []string{"preset", "lst"}, wantSub: "lst", wantSug: "list", wantParent: "ws preset"},
		{args: []string{"os-env", "prnt"}, wantSub: "prnt", wantSug: "print", wantParent: "ws os-env"},
		{args: []string{"plugin", "lst"}, wantSub: "lst", wantSug: "list", wantParent: "ws plugin"},
		{args: []string{"update", "schdule"}, wantSub: "schdule", wantSug: "schedule", wantParent: "ws update"},
		{args: []string{"update", "schedule", "prnt"}, wantSub: "prnt", wantSug: "print", wantParent: "ws update schedule"},
		{args: []string{"schedule", "prnt"}, wantSub: "prnt", wantSug: "print", wantParent: "ws schedule"},
		{args: []string{"vault", "screts"}, wantSub: "screts", wantSug: "secrets", wantParent: "ws vault"},
		{args: []string{"vault", "secrets", "pul"}, wantSub: "pul", wantSug: "pull", wantParent: "ws vault secrets"},
		{args: []string{"vault", "files", "psh"}, wantSub: "psh", wantSug: "push", wantParent: "ws vault files"},
		{args: []string{"doctr"}, wantSub: "doctr", wantSug: "doctor", wantParent: "ws"},
	}

	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			_, err := execWS(t, fx, tc.args...)
			if err == nil {
				t.Fatalf("expected error for ws %s", strings.Join(tc.args, " "))
			}
			if got := ExitCode(err); got != 2 {
				t.Fatalf("expected exit code 2 for ws %s, got %d (err=%v)", strings.Join(tc.args, " "), got, err)
			}
			if !IsUsageError(err) {
				t.Fatalf("expected IsUsageError=true for %v", err)
			}
			msg := err.Error()
			wantUnknown := `unknown command "` + tc.wantSub + `" for "` + tc.wantParent + `"`
			if !strings.Contains(msg, wantUnknown) {
				t.Fatalf("expected %q in error, got:\n%s", wantUnknown, msg)
			}
			if tc.wantSug != "" && (!strings.Contains(msg, "Did you mean this?") || !strings.Contains(msg, tc.wantSug)) {
				t.Fatalf("expected suggestion %q in error, got:\n%s", tc.wantSug, msg)
			}
		})
	}
}

func TestPkgVerbsAndAliases(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	pkgHelp, err := execWS(t, fx, "pkg", "--help")
	if err != nil {
		t.Fatalf("pkg --help failed: %v", err)
	}
	for _, hidden := range []string{"\n  check ", "\n  untracked ", "\n  untrack "} {
		if strings.Contains(pkgHelp, hidden) {
			t.Fatalf("expected %q hidden from ws pkg --help:\n%s", hidden, pkgHelp)
		}
	}

	rmOut, err := execWS(t, fx, "pkg", "rm", "--dry-run", "--provider", "brew", "fd", "--json")
	if err != nil || !strings.Contains(rmOut, "brew uninstall fd") {
		t.Fatalf("expected pkg rm to alias remove, got err=%v out=%s", err, rmOut)
	}

	iOut, err := execWS(t, fx, "pkg", "i", "--dry-run", "--provider", "brew", "fd", "--json")
	if err != nil || !strings.Contains(iOut, "brew install fd") {
		t.Fatalf("expected pkg i to alias install, got err=%v out=%s", err, iOut)
	}

	if _, err := execWS(t, fx, "pkg", "add", "--no-install", "--file", "pkg/server.txt", "bat", "--json"); err != nil {
		t.Fatalf("pkg add --no-install failed: %v", err)
	}
	keepOut, err := execWS(t, fx, "pkg", "remove", "--keep-installed", "--file", "pkg/server.txt", "bat", "--json")
	if err != nil || !strings.Contains(keepOut, `"removed": true`) {
		t.Fatalf("pkg remove --keep-installed failed: err=%v out=%s", err, keepOut)
	}
	rawList, err := os.ReadFile(filepath.Join(fx.repo, "pkg", "server.txt"))
	if err != nil || strings.Contains(string(rawList), "bat") {
		t.Fatalf("expected bat removed from pkg/server.txt, err=%v raw=%q", err, string(rawList))
	}

	if _, err := execWS(t, fx, "pkg", "list", "--missing", "--untracked"); err == nil || ExitCode(err) != 2 {
		t.Fatalf("expected --missing + --untracked to exit 2, got err=%v code=%d", err, ExitCode(err))
	}

	t.Setenv("PATH", t.TempDir())
	missOut, err := execWS(t, fx, "pkg", "list", "--missing", "--json")
	if err != nil {
		t.Fatalf("pkg list --missing --json failed: %v\n%s", err, missOut)
	}
	var missPayload struct {
		Preset  string `json:"preset"`
		Missing int    `json:"missing"`
		Total   int    `json:"total"`
		Tools   []struct {
			ID        string `json:"id"`
			Installed bool   `json:"installed"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(missOut), &missPayload); err != nil {
		t.Fatalf("unmarshal pkg list --missing: %v\n%s", err, missOut)
	}
	if missPayload.Total == 0 || missPayload.Missing != len(missPayload.Tools) {
		t.Fatalf("expected only missing tools in pkg list --missing, got %+v", missPayload)
	}

	untrackedOut, err := execWS(t, fx, "pkg", "list", "--untracked", "--json")
	if err != nil {
		t.Fatalf("pkg list --untracked --json failed: %v\n%s", err, untrackedOut)
	}
	if _, err := execWS(t, fx, "pkg", "check", "--json"); err != nil {
		t.Fatalf("hidden pkg check alias failed: %v", err)
	}
	if _, err := execWS(t, fx, "pkg", "untracked", "--json"); err != nil {
		t.Fatalf("hidden pkg untracked alias failed: %v", err)
	}
}

func TestDoctorExitCodeOnErrorFindings(t *testing.T) {
	fx := setupFixture(t)

	if out, err := execWS(t, fx, "doctor", "--json"); err != nil {
		t.Fatalf("expected doctor with only warnings to exit 0, got err=%v\n%s", err, out)
	}

	brokenTarget := filepath.Join(fx.home, ".config", "tmux")
	if err := os.MkdirAll(filepath.Dir(brokenTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fx.home, "nonexistent-target"), brokenTarget); err != nil {
		t.Fatal(err)
	}

	out, err := execWS(t, fx, "doctor", "--json")
	if err == nil {
		t.Fatalf("expected doctor with broken symlink error finding to return error:\n%s", out)
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("expected doctor error finding exit code 1, got %d (err=%v)", got, err)
	}
	var rep engine.DoctorReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("expected valid doctor JSON on stdout even when exiting 1: %v\n%s", jerr, out)
	}
	if doctorErrorCount(&rep) == 0 {
		t.Fatalf("expected >=1 error finding in report: %+v", rep.Findings)
	}
}

func TestLocalPlanFlagsRegroupedAliasesAndVaultSubcommands(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	root := NewRootCmd(&GlobalOpts{Repo: fx.repo, Home: fx.home})
	if root.PersistentFlags().Lookup("plan") != nil || root.PersistentFlags().Lookup("save-plan") != nil {
		t.Fatalf("--plan and --save-plan must not be persistent root flags")
	}

	planPaths := [][]string{
		{"apply"},
		{"bootstrap"},
		{"clean"},
		{"pkg", "sync"},
		{"theme"},
		{"theme", "set"},
		{"update"},
	}
	for _, p := range planPaths {
		cmd, _, err := root.Find(p)
		if err != nil {
			t.Fatalf("Find(%v): %v", p, err)
		}
		if cmd.Flags().Lookup("plan") == nil || cmd.Flags().Lookup("save-plan") == nil {
			t.Fatalf("expected local --plan/--save-plan on ws %s", strings.Join(p, " "))
		}
	}

	noPlanPaths := [][]string{
		{"doctor"},
		{"repo"},
		{"env"},
		{"db"},
		{"fleet"},
		{"config"},
		{"pkg", "list"},
		{"update", "schedule"},
	}
	for _, p := range noPlanPaths {
		cmd, _, err := root.Find(p)
		if err != nil {
			t.Fatalf("Find(%v): %v", p, err)
		}
		if cmd.Flags().Lookup("plan") != nil || cmd.Flags().Lookup("save-plan") != nil {
			t.Fatalf("did not expect --plan/--save-plan on ws %s", strings.Join(p, " "))
		}
	}

	if _, err := execWS(t, fx, "doctor", "--plan", "x.json"); err == nil || ExitCode(err) != 2 {
		t.Fatalf("expected doctor --plan to exit 2, got err=%v code=%d", err, ExitCode(err))
	}

	if out, err := execWS(t, fx, "theme", "edit", "tmux", "--dry-run", "--json"); err != nil || !strings.Contains(out, `"dryRun": true`) {
		t.Fatalf("theme edit --dry-run failed: err=%v out=%s", err, out)
	}
	if out, err := execWS(t, fx, "edit", "tmux", "--dry-run", "--json"); err != nil || !strings.Contains(out, `"dryRun": true`) {
		t.Fatalf("hidden edit alias failed: err=%v out=%s", err, out)
	}

	if out, err := execWS(t, fx, "zone", "run", "--zone", "work", "--dry-run", "--", "env"); err != nil || !strings.Contains(out, "zone=work") {
		t.Fatalf("zone run --dry-run failed: err=%v out=%s", err, out)
	}
	if out, err := execWS(t, fx, "run", "--zone", "work", "--dry-run", "--", "env"); err != nil || !strings.Contains(out, "zone=work") {
		t.Fatalf("hidden run alias failed: err=%v out=%s", err, out)
	}

	if out, err := execWS(t, fx, "update", "schedule", "print"); err != nil || !strings.Contains(out, "Backend:") {
		t.Fatalf("update schedule print failed: err=%v out=%s", err, out)
	}
	if out, err := execWS(t, fx, "schedule", "print"); err != nil || !strings.Contains(out, "Backend:") {
		t.Fatalf("hidden schedule alias failed: err=%v out=%s", err, out)
	}

	repoHelp, err := execWS(t, fx, "repo", "--help")
	if err != nil {
		t.Fatalf("repo --help: %v", err)
	}
	if strings.Contains(repoHelp, "\n  prs ") || strings.Contains(repoHelp, "\n  issues ") {
		t.Fatalf("expected repo prs/issues hidden from repo --help:\n%s", repoHelp)
	}

	themeHelp, err := execWS(t, fx, "theme", "--help")
	if err != nil {
		t.Fatalf("theme --help: %v", err)
	}
	if strings.Contains(themeHelp, "--list") {
		t.Fatalf("expected --list hidden from theme --help:\n%s", themeHelp)
	}
	if !strings.Contains(themeHelp, "edit") || !strings.Contains(themeHelp, "list") {
		t.Fatalf("expected edit and list in theme --help:\n%s", themeHelp)
	}

	for _, args := range [][]string{
		{"vault", "secrets", "pull", "--json"},
		{"vault", "secrets", "push", "--json"},
		{"vault", "secrets", "load", "--json"},
		{"vault", "secrets", "save", "--json"},
		{"vault", "ssh", "pull", "--json"},
		{"vault", "files", "push", "~/.ssh/config", "--json"},
		{"vault", "envfiles", "pull", "--all", "--json"},
	} {
		out, err := execWS(t, fx, args...)
		if err != nil {
			t.Fatalf("ws %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		if !strings.Contains(out, `"dryRun": true`) {
			t.Fatalf("expected default dryRun preview for ws %s, got:\n%s", strings.Join(args, " "), out)
		}
	}

	if out, err := execWS(t, fx, "db", "key", "init", "--json"); err != nil {
		t.Fatalf("db key init: %v\n%s", err, out)
	}
	if out, err := execWS(t, fx, "db", "key", "backup", "--json"); err != nil || !strings.Contains(out, `"dryRun": true`) {
		t.Fatalf("expected db key backup to preview by default, got err=%v out=%s", err, out)
	}
	if out, err := execWS(t, fx, "db", "key", "restore", "--json"); err != nil || !strings.Contains(out, `"dryRun": true`) {
		t.Fatalf("expected db key restore to preview by default, got err=%v out=%s", err, out)
	}

	if out, err := execWS(t, fx, "config", "list", "--json"); err != nil || !strings.Contains(out, "settings") {
		t.Fatalf("expected config list alias to work, got err=%v out=%s", err, out)
	}

	if out, err := execWS(t, fx, "repo", "list", "--refresh", "--json"); err != nil {
		t.Fatalf("repo list --refresh failed: %v\n%s", err, out)
	}
	if out, err := execWS(t, fx, "repo", "pull", "--all", "--dry-run", "--json"); err != nil {
		t.Fatalf("repo pull --all --dry-run failed: %v\n%s", err, out)
	}
	if _, err := execWS(t, fx, "repo", "pull", "--all", fx.home); err == nil || ExitCode(err) != 2 {
		t.Fatalf("expected repo pull --all <dir> to exit 2, got err=%v code=%d", err, ExitCode(err))
	}
}

func TestHistClipboardOnlyWithCopyFlag(t *testing.T) {
	fx := setupFixture(t)

	origTTY := histIsPickerTTY
	origPick := histPick
	origClip := writeClipboard
	t.Cleanup(func() {
		histIsPickerTTY = origTTY
		histPick = origPick
		writeClipboard = origClip
	})

	histIsPickerTTY = func(_ *cobra.Command, _ *app.Context) bool { return true }
	histPick = func(_ context.Context, opts picker.Options) (picker.Item, error) {
		if len(opts.Items) == 0 {
			return picker.Item{}, errors.New("empty items")
		}
		return opts.Items[0], nil
	}

	clipCalls := 0
	var lastClip string
	writeClipboard = func(text string) error {
		clipCalls++
		lastClip = text
		return nil
	}

	out, err := execWS(t, fx, "hist")
	if err != nil {
		t.Fatalf("ws hist failed: %v\n%s", err, out)
	}
	if clipCalls != 0 {
		t.Fatalf("expected ws hist without --copy not to write clipboard, got %d call(s)", clipCalls)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected chosen command on stdout")
	}

	outCopy, err := execWS(t, fx, "hist", "--copy")
	if err != nil {
		t.Fatalf("ws hist --copy failed: %v\n%s", err, outCopy)
	}
	if clipCalls != 1 || lastClip != strings.TrimSpace(outCopy) {
		t.Fatalf("expected 1 clipboard write with %q, got calls=%d text=%q", strings.TrimSpace(outCopy), clipCalls, lastClip)
	}

	writeClipboard = func(_ string) error {
		return errors.New("pbcopy unavailable")
	}
	_, err = execWS(t, fx, "hist", "--copy")
	if err == nil || !strings.Contains(err.Error(), "pbcopy unavailable") {
		t.Fatalf("expected clipboard failure to be reported, got err=%v", err)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("expected exit code 1 on clipboard error, got %d", ExitCode(err))
	}
}

type fakeCLITestBackend struct{}

func (fakeCLITestBackend) HostSummary() (tui.HostBadge, error) {
	return tui.HostBadge{
		OS:          "linux",
		Distro:      "ubuntu",
		Arch:        "amd64",
		Preset:      "ubuntu-runner",
		Zone:        "work",
		Theme:       "gruvbox-dark",
		PkgManagers: []string{"apt", "mise"},
		CWDRepoSlug: "example/demo-web",
	}, nil
}

func (fakeCLITestBackend) ScanEnv() error { return nil }
func (fakeCLITestBackend) LoadEnvVars(_, _ string) ([]tui.EnvVarRow, error) {
	return []tui.EnvVarRow{{
		VarKey:           "API_TOKEN",
		Zone:             "work",
		ActiveSourceKind: "dotenv",
		ActiveSourcePath: "~/work/code/svc/.env",
		ActiveMasked:     "sk-***",
		Fingerprint:      "fp:0123456789abcdef",
		IsSecret:         true,
		SourceCount:      1,
	}}, nil
}
func (fakeCLITestBackend) LoadEnvFiles(_ string) ([]tui.EnvFileRow, error) {
	return []tui.EnvFileRow{{
		Path:        "~/work/code/svc/.env",
		SourceKind:  "dotenv",
		Zone:        "work",
		VarCount:    2,
		SecretCount: 1,
	}}, nil
}
func (fakeCLITestBackend) LoadEnvDetail(varKey, _ string) (tui.EnvDetail, error) {
	return tui.EnvDetail{VarKey: varKey, ActiveMasked: "sk-***", Fingerprint: "fp:0123456789abcdef", IsSecret: true}, nil
}
func (fakeCLITestBackend) RevealEnvValue(_, _ string) (string, error) { return "secret", nil }
func (fakeCLITestBackend) PreviewEnvRotate(req tui.EnvRotateReq) (tui.EnvRotatePreview, error) {
	return tui.EnvRotatePreview{VarKey: req.VarKey}, nil
}
func (fakeCLITestBackend) ExecEnvRotate(_ tui.EnvRotateReq) (tui.EnvRotateResult, error) {
	return tui.EnvRotateResult{TxID: "tx-1", UpdatedCount: 1, Summary: "ok"}, nil
}
func (fakeCLITestBackend) LoadRepos(_ bool, _ string) ([]tui.RepoRow, error) {
	return []tui.RepoRow{
		{
			Name:      "api-service",
			Zone:      "work",
			Forge:     "github",
			RemoteURL: "https://github.com/example/api-service.git",
			LocalPath: "/tmp/ws/api-service",
			Branch:    "main",
		},
		{
			Name:      "web-app",
			Zone:      "work",
			Forge:     "github",
			RemoteURL: "https://github.com/example/web-app.git",
			LocalPath: "/tmp/ws/web-app",
			Branch:    "main",
		},
	}, nil
}
func (fakeCLITestBackend) LoadGHItems(_ bool, _ string) ([]tui.GHItemRow, error) {
	return []tui.GHItemRow{{
		Kind:   "pr",
		Repo:   "example/api-service",
		Number: 42,
		Title:  "Fix CLI",
		State:  "open",
		Zone:   "work",
	}}, nil
}
func (fakeCLITestBackend) LoadCachedRepoGHItems(_, _ string) ([]tui.GHItemRow, int64, error) {
	return nil, 0, nil
}
func (fakeCLITestBackend) LoadRepoGHItems(_, _, _ string, _ bool) ([]tui.GHItemRow, error) {
	return nil, nil
}
func (fakeCLITestBackend) LoadGHItemDetail(kind, repo, zone string, number int, _ bool) (tui.GHItemDetail, error) {
	return tui.GHItemDetail{GHItemRow: tui.GHItemRow{Kind: kind, Repo: repo, Zone: zone, Number: number}}, nil
}
func (fakeCLITestBackend) PullRepos() (tui.RepoPullResult, error) {
	return tui.RepoPullResult{Summary: "ok"}, nil
}
func (fakeCLITestBackend) PullSelectedRepos(_ []string) (tui.RepoPullResult, error) {
	return tui.RepoPullResult{Summary: "ok"}, nil
}
func (fakeCLITestBackend) InteractivePullCmd(_ tui.RepoRow) (*exec.Cmd, error) { return nil, nil }
func (fakeCLITestBackend) CloneRepo(_, _ string) (string, error)               { return "ok", nil }
func (fakeCLITestBackend) ApproveGHPR(_, _ string, _ int, _ string) (string, error) {
	return "ok", nil
}
func (fakeCLITestBackend) MergeGHPR(_, _ string, _ int, _ string) (string, error) {
	return "ok", nil
}
func (fakeCLITestBackend) SetGHItemState(_, _, _ string, _ int, _ bool) (string, error) {
	return "ok", nil
}
func (fakeCLITestBackend) CommentGHItem(_, _, _ string, _ int, _ string) (string, error) {
	return "ok", nil
}
func (fakeCLITestBackend) CheckoutGHPR(_, _, _ string, _ int) (string, error) { return "ok", nil }
func (fakeCLITestBackend) LoadMiseInventory(_ string, _, _ bool) (tui.MiseInventoryView, error) {
	return tui.MiseInventoryView{
		MiseVersion: "2026.1.0",
		Configs: []tui.MiseConfigRow{{
			Path:        "/tmp/example/.config/mise/config.toml",
			Kind:        "mise.toml",
			Scope:       "global",
			ProjectName: "global",
		}},
		Tools: []tui.MiseToolRow{{
			Name:       "go",
			Requested:  "1.24",
			Version:    "1.24.0",
			Latest:     "1.24.1",
			Scope:      "global",
			SourcePath: "/tmp/example/.config/mise/config.toml",
			Installed:  true,
			Active:     true,
			Outdated:   true,
			Status:     "outdated",
		}},
	}, nil
}
func (fakeCLITestBackend) EditMiseFileCmd(_ string) (*exec.Cmd, error) { return nil, nil }
func (fakeCLITestBackend) UpgradeMiseTools(_ []tui.MiseToolRow) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) LoadFleetNodes() ([]tui.FleetNodeRow, error) {
	return []tui.FleetNodeRow{{
		Name:       "dev-vm-01",
		DNSName:    "dev-vm-01.tail.ts.net",
		OS:         "linux",
		Arch:       "arm64",
		Online:     true,
		SSHEnabled: true,
	}}, nil
}
func (fakeCLITestBackend) TailscaleJoinCmd(_ string, _ []string) (string, error) {
	return "tailscale up", nil
}
func (fakeCLITestBackend) PreviewFleetSync(_ tui.FleetSyncReq) (tui.FleetSyncPreview, error) {
	return tui.FleetSyncPreview{Summary: "ok"}, nil
}
func (fakeCLITestBackend) ExecFleetSync(_ tui.FleetSyncReq) (string, error) { return "ok", nil }
func (fakeCLITestBackend) FleetConnectCmd(_ tui.FleetNodeRow) (*exec.Cmd, error) {
	return nil, nil
}
func (fakeCLITestBackend) DiffFleetFiles(node, targetID string, includeEnv, includeDB bool) (tui.FleetFileDiffView, error) {
	return tui.FleetFileDiffView{
		Node:       node,
		TargetID:   targetID,
		IncludeEnv: includeEnv,
		IncludeDB:  includeDB,
		Entries: []tui.FleetFileDiffRow{
			{
				RelPath:  ".ssh/config",
				Category: "ssh",
				Status:   "local-newer",
			},
			{
				RelPath:  "work/code/api-service/.env",
				Category: "envfiles",
				Status:   "local-only",
			},
		},
	}, nil
}
func (fakeCLITestBackend) PreviewFleetFileDiff(_, _, _ string) (string, error) { return "", nil }
func (fakeCLITestBackend) SyncFleetFiles(_ tui.FleetFileSyncReq) (tui.FleetFileSyncResult, error) {
	return tui.FleetFileSyncResult{Summary: "ok"}, nil
}
func (fakeCLITestBackend) ListPresets() ([]tui.PresetSummary, error) {
	return []tui.PresetSummary{{ID: "ubuntu-runner", Description: "Ubuntu runner", Active: true}}, nil
}
func (fakeCLITestBackend) ResolvePreset(_ string) ([]tui.PresetToolRow, error) {
	return []tui.PresetToolRow{
		{
			ID:          "ripgrep",
			Provider:    "apt",
			PackageName: "ripgrep",
			Status:      "installed",
			InstallCmd:  "ws pkg install --provider apt ripgrep",
		},
		{
			ID:          "fd",
			Provider:    "apt",
			PackageName: "fd",
			Status:      "installed",
			InstallCmd:  "ws pkg install --provider apt fd",
		},
	}, nil
}
func (fakeCLITestBackend) LoadPackageOutdated() (map[string]tui.PackageOutdatedInfo, error) {
	return nil, nil
}
func (fakeCLITestBackend) LoadUntrackedPackages(_ string) ([]tui.PresetToolRow, error) {
	return nil, nil
}
func (fakeCLITestBackend) SearchPackages(_, _ string, _ int) ([]tui.PackageSearchRow, error) {
	return []tui.PackageSearchRow{{
		Name:       "ripgrep",
		Provider:   "apt",
		Version:    "14.1.0",
		InstallCmd: "ws pkg install --provider apt ripgrep",
	}}, nil
}
func (fakeCLITestBackend) LoadPackageDetail(pkg tui.PresetToolRow) (tui.PackageDetail, error) {
	return tui.PackageDetail{ID: pkg.ID, PackageName: pkg.PackageName, Provider: pkg.Provider}, nil
}
func (fakeCLITestBackend) PackageActionExecCmd(_ string, _ []tui.PresetToolRow) (*exec.Cmd, error) {
	return nil, nil
}
func (fakeCLITestBackend) ExecPackageAction(_ string, _ []tui.PresetToolRow) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) TrackPackages(_ string, _ []tui.PresetToolRow) (string, error) {
	return "ok", nil
}
func (fakeCLITestBackend) AddAndTrackPackageExecCmd(_, _, _ string, _ bool) (*exec.Cmd, error) {
	return nil, nil
}
func (fakeCLITestBackend) AddAndTrackPackage(_, _, _ string, _ bool) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) UntrackPackages(_ []tui.PresetToolRow) (string, error) { return "ok", nil }
func (fakeCLITestBackend) PreviewUpdateAll(_ bool) (tui.UpdatePlanPreview, error) {
	return tui.UpdatePlanPreview{Summary: "ok"}, nil
}
func (fakeCLITestBackend) UpdateAllExecCmd(_ bool) (*exec.Cmd, error) { return nil, nil }
func (fakeCLITestBackend) ExecUpdateAll(_ bool) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) ListThemes() ([]string, error) {
	return []string{"gruvbox-dark", "catppuccin-dark"}, nil
}
func (fakeCLITestBackend) LoadPalette(_ string) (config.Palette, error) {
	return config.Palette{Name: "Gruvbox Dark", Mode: "dark"}, nil
}
func (fakeCLITestBackend) ActiveTheme() (string, error) { return "gruvbox-dark", nil }
func (fakeCLITestBackend) ApplyTheme(_ string) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{}, nil
}
func (fakeCLITestBackend) ApplyThemeForApp(_, _ string) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{}, nil
}
func (fakeCLITestBackend) TemplateStatuses() ([]tui.TemplateStatusRow, error) {
	return []tui.TemplateStatusRow{{App: "tmux", Status: "ok"}}, nil
}
func (fakeCLITestBackend) EditAppCmd(_ string) (*exec.Cmd, error) { return nil, nil }
func (fakeCLITestBackend) FinishAppEdit(_ string) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{}, nil
}
func (fakeCLITestBackend) ScanCleanup() ([]tui.CleanItem, error) {
	return []tui.CleanItem{{
		ID:                "safe:cli-bat",
		Tier:              "safe",
		Category:          "cli-cache",
		Title:             "bat cache",
		Bytes:             1024,
		SelectedByDefault: true,
	}}, nil
}
func (fakeCLITestBackend) CleanupExecCmd(_ []string) (*exec.Cmd, error) { return nil, nil }
func (fakeCLITestBackend) RunCleanup(_ []string, _ bool) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{}, nil
}
func (fakeCLITestBackend) LoadHistory(_ string, _ int) ([]tui.HistoryRow, string, error) {
	return []tui.HistoryRow{{
		Timestamp: time.Now(),
		Shell:     "zsh",
		Zone:      "work",
		Command:   "ws doctor",
	}}, "zsh", nil
}
func (fakeCLITestBackend) LoadDoctor() (tui.DoctorSummary, error) {
	return tui.DoctorSummary{
		Host:   "linux/amd64",
		Preset: "ubuntu-runner",
		Theme:  "gruvbox-dark",
		Findings: []tui.DoctorFinding{{
			ID:       "tmpl:tmux",
			Severity: "warn",
			Category: "template",
			Title:    "tmux colors out of date",
			FixCLI:   "ws apply --only tmux",
		}},
	}, nil
}
func (fakeCLITestBackend) ApplyPlan(_ *engine.Plan) (*engine.ApplyResult, error) {
	return &engine.ApplyResult{}, nil
}
func (fakeCLITestBackend) LoadDBStatus() (tui.DBStatusSummary, error) {
	return tui.DBStatusSummary{Path: "~/.ws/ws.db", SchemaVersion: 1, HasKey: true}, nil
}
func (fakeCLITestBackend) BackupDB(_ bool) (tui.DBBackupResult, error) {
	return tui.DBBackupResult{Summary: "ok"}, nil
}
func (fakeCLITestBackend) UndoLast() (string, error) { return "undid", nil }
func (fakeCLITestBackend) LoadVaultInventory() (tui.VaultInventoryView, error) {
	return tui.VaultInventoryView{
		HasOPCLI: true,
		Items: []tui.VaultItemRow{
			{ID: "db", Scope: "db", Label: "ws.db", DocTitle: "ws-db-backup.enc"},
			{ID: "db-key", Scope: "db-key", Label: "db.key", DocTitle: "ws-db.key"},
			{ID: "secrets", Scope: "secrets", Label: "Private Layer", DocTitle: "workstation-secrets.zip"},
			{ID: "ssh", Scope: "ssh", Label: "SSH", DocTitle: "~/.ssh.zip"},
			{ID: "file-aws", Scope: "files", Label: "~/.aws/config", TargetArg: "~/.aws/config", DocTitle: "~/.aws/config"},
			{ID: "env-work", Scope: "envfiles", Label: "work .env*", TargetArg: "/tmp/example/work/code", DocTitle: "workstation-envfiles-work.zip"},
		},
	}, nil
}
func (fakeCLITestBackend) RunVaultAction(_ string, _ []tui.VaultItemRow) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) RunWSCommand(_ []string) (tui.CommandRunResult, error) {
	return tui.CommandRunResult{Output: "ok"}, nil
}
func (fakeCLITestBackend) LoadSettings() (tui.SettingsView, error) {
	return tui.SettingsView{
		ConfigPath: "~/.ws/config.toml",
		Items: []tui.SettingRow{{
			Key:      "WS_OP_VAULT",
			Category: "vault",
			Kind:     "string",
			Value:    "Personal",
			Source:   "override",
		}},
	}, nil
}
func (fakeCLITestBackend) SetSetting(_, _ string) (tui.SettingsView, error) {
	return tui.SettingsView{}, nil
}
func (fakeCLITestBackend) ResetSetting(_ string) (tui.SettingsView, error) {
	return tui.SettingsView{}, nil
}
func (fakeCLITestBackend) CheckConfigWatch(prev string) (tui.ConfigWatchResult, error) {
	return tui.ConfigWatchResult{Fingerprint: prev}, nil
}
func (fakeCLITestBackend) ReloadConfig() (tui.SettingsView, []string, error) {
	return tui.SettingsView{}, nil, nil
}
func (fakeCLITestBackend) EditConfigFileCmd(_ string) (*exec.Cmd, error) { return nil, nil }

func drainTUICmds(m tui.Model, cmd tea.Cmd) tui.Model {
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
			updated, nextCmd := m.Update(msg)
			m = updated.(tui.Model)
			if nextCmd != nil {
				queue = append(queue, nextCmd)
			}
		}
	}
	return m
}

func sendTUIMsg(m tui.Model, msg tea.Msg) tui.Model {
	updated, cmd := m.Update(msg)
	m = updated.(tui.Model)
	return drainTUICmds(m, cmd)
}

func extractTUICLIBarCommand(rendered string) string {
	clean := ui.StripANSI(rendered)
	for _, line := range strings.Split(clean, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "$ ") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "$ "))
			if idx := strings.Index(rest, "  "); idx > 0 {
				rest = strings.TrimSpace(rest[:idx])
			}
			return rest
		}
	}
	return ""
}

func splitShellArgs(raw string) []string {
	var args []string
	var cur strings.Builder
	inQuote := rune(0)
	escaped := false
	for _, r := range raw {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && inQuote != '\'' {
			escaped = true
			continue
		}
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		if r == '"' || r == '\'' {
			inQuote = r
			continue
		}
		if r == '#' && cur.Len() == 0 {
			break
		}
		if r == ' ' || r == '\t' {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func parseWSCommandAgainstCobra(cmdLine string) error {
	for _, part := range strings.Split(cmdLine, "&&") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "ws ") && part != "ws" {
			continue
		}
		tokens := splitShellArgs(part)
		if len(tokens) == 0 || tokens[0] != "ws" {
			continue
		}
		argv := tokens[1:]
		root := NewRootCmd(&GlobalOpts{})
		target, rem, err := root.Find(argv)
		if err != nil {
			return err
		}
		if err := target.ParseFlags(rem); err != nil {
			return err
		}
		posArgs := target.Flags().Args()
		if target.Args != nil {
			if err := target.Args(target, posArgs); err != nil {
				return err
			}
		}
		if err := target.ValidateRequiredFlags(); err != nil {
			return err
		}
	}
	return nil
}

func TestTUICLIEquivalentsParseAgainstRealCobraTree(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	backend := fakeCLITestBackend{}

	tabs := []string{
		"overview",
		"projects",
		"gh",
		"prs",
		"issues",
		"packages",
		"pkgs",
		"pkg-search",
		"mise-tools",
		"mise",
		"cleanup",
		"env",
		"secrets",
		"envfiles",
		"vault",
		"fleet",
		"filesync",
		"workstation",
		"doctor",
		"themes",
		"presets",
		"settings",
		"watched",
		"history",
		"output",
	}

	collected := map[string]bool{}
	openOutputCLI := func(m tui.Model) string {
		m = sendTUIMsg(m, tea.KeyPressMsg{Code: ':', Text: ":"})
		for _, r := range "output" {
			m = sendTUIMsg(m, tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		m = sendTUIMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		return extractTUICLIBarCommand(m.Render())
	}

	for _, tab := range tabs {
		m := tui.NewModel(backend, tui.Options{
			StartTab:    tab,
			DefaultZone: "work",
			Zones:       []string{"work", "personal"},
			Version:     "test",
		})
		m = drainTUICmds(m, m.Init())
		m = sendTUIMsg(m, tea.WindowSizeMsg{Width: 240, Height: 48})
		if cliCmd := extractTUICLIBarCommand(m.Render()); strings.HasPrefix(cliCmd, "ws") {
			collected[cliCmd] = true
		}

		switch tab {
		case "vault":
			for i := 0; i < 6; i++ {
				m = sendTUIMsg(m, tea.KeyPressMsg{Code: 'j', Text: "j"})
				if cliCmd := extractTUICLIBarCommand(m.Render()); strings.HasPrefix(cliCmd, "ws") {
					collected[cliCmd] = true
				}
			}
		case "projects":
			mSel := sendTUIMsg(m, tea.KeyPressMsg{Code: '*', Text: "*"})
			selCLI := extractTUICLIBarCommand(mSel.Render())
			if !strings.Contains(selCLI, "/tmp/ws/api-service") || !strings.Contains(selCLI, "/tmp/ws/web-app") {
				t.Fatalf("expected projects multi-select CLI to list selected repo paths, got %q", selCLI)
			}
			collected[selCLI] = true

			mPull := sendTUIMsg(mSel, tea.KeyPressMsg{Code: 'u', Text: "u"})
			pullOutCLI := openOutputCLI(mPull)
			if !strings.Contains(pullOutCLI, "/tmp/ws/api-service") || !strings.Contains(pullOutCLI, "/tmp/ws/web-app") {
				t.Fatalf("expected recorded repo pull CLI to list selected repo paths, got %q", pullOutCLI)
			}
			collected[pullOutCLI] = true

			mClone := sendTUIMsg(m, tea.KeyPressMsg{Code: 'a', Text: "a"})
			mClone = sendTUIMsg(mClone, tea.KeyPressMsg{Code: 'c', Text: "c"})
			cloneEmptyCLI := extractTUICLIBarCommand(mClone.Render())
			if !strings.HasPrefix(cloneEmptyCLI, "ws repo clone") || strings.Contains(cloneEmptyCLI, "<url>") {
				t.Fatalf("expected runnable clone CLI without template placeholders, got %q", cloneEmptyCLI)
			}
			collected[cloneEmptyCLI] = true

			for _, r := range "https://github.com/example/demo" {
				mClone = sendTUIMsg(mClone, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			cloneTypedCLI := extractTUICLIBarCommand(mClone.Render())
			if !strings.Contains(cloneTypedCLI, "https://github.com/example/demo") {
				t.Fatalf("expected clone CLI to include typed source URL, got %q", cloneTypedCLI)
			}
			collected[cloneTypedCLI] = true

		case "pkgs":
			mSel := sendTUIMsg(m, tea.KeyPressMsg{Code: '*', Text: "*"})
			pkgSelCLI := extractTUICLIBarCommand(mSel.Render())
			if !strings.Contains(pkgSelCLI, "ripgrep") || !strings.Contains(pkgSelCLI, "fd") {
				t.Fatalf("expected pkgs multi-select CLI to list package names, got %q", pkgSelCLI)
			}
			collected[pkgSelCLI] = true

			mAct := sendTUIMsg(mSel, tea.KeyPressMsg{Code: 'u', Text: "u"})
			mAct = sendTUIMsg(mAct, tea.KeyPressMsg{Code: 'y', Text: "y"})
			pkgOutCLI := extractTUICLIBarCommand(mAct.Render())
			if !strings.Contains(pkgOutCLI, "ripgrep") || !strings.Contains(pkgOutCLI, "fd") {
				t.Fatalf("expected recorded pkg action CLI to list package names, got %q", pkgOutCLI)
			}
			collected[pkgOutCLI] = true

		case "filesync":
			m = sendTUIMsg(m, tea.KeyPressMsg{Code: 'r', Text: "r"})
			mSel := sendTUIMsg(m, tea.KeyPressMsg{Code: '*', Text: "*"})
			fileSelCLI := extractTUICLIBarCommand(mSel.Render())
			if !strings.Contains(fileSelCLI, ".ssh/config") || !strings.Contains(fileSelCLI, "work/code/api-service/.env") {
				t.Fatalf("expected filesync multi-select CLI to list selected paths, got %q", fileSelCLI)
			}
			collected[fileSelCLI] = true

			mSync := sendTUIMsg(mSel, tea.KeyPressMsg{Code: 'a', Text: "a"})
			mSync = sendTUIMsg(mSync, tea.KeyPressMsg{Code: 'P', Text: "P"})
			mSync = sendTUIMsg(mSync, tea.KeyPressMsg{Code: 'y', Text: "y"})
			syncOutCLI := openOutputCLI(mSync)
			if !strings.Contains(syncOutCLI, "ws fleet sync-files dev-vm-01 --push") ||
				!strings.Contains(syncOutCLI, ".ssh/config") ||
				!strings.Contains(syncOutCLI, "work/code/api-service/.env") ||
				!strings.Contains(syncOutCLI, "--execute") {
				t.Fatalf("expected recorded fleet sync-files CLI to include selected paths and --execute, got %q", syncOutCLI)
			}
			collected[syncOutCLI] = true
		}
	}

	for _, extra := range []string{
		"ws repo pull --all",
		"ws repo pull /tmp/ws/api-service /tmp/ws/web-app",
		"ws repo list --refresh",
		"ws env scan && ws repo list --refresh",
		"ws clean --dry-run --tier safe",
		"ws clean --execute --tier safe --allow remove",
		"ws db restore --from-1password",
		"ws db backup --1password --force",
		"ws db key init && ws db key backup",
		"ws db key restore",
		"ws vault secrets load --doc workstation-secrets.zip",
		"ws vault secrets save --doc workstation-secrets.zip",
		"ws vault files save --doc ~/.aws/config ~/.aws/config",
		"ws vault envfiles load --doc workstation-envfiles-work.zip /tmp/example/work/code",
		"ws theme set gruvbox-dark --app tmux",
		"ws fleet sync-files dev-vm-01 --push .ssh/config --execute",
		"ws update --all",
		"ws pkg install --provider apt --track --file pkg/server.txt ripgrep",
		"ws pkg remove --provider apt ripgrep",
		"ws pkg add --provider apt --file pkg/server.txt ripgrep",
	} {
		collected[extra] = true
	}

	if len(collected) < 15 {
		t.Fatalf("expected to collect >=15 TUI CLI equivalents, got %d: %v", len(collected), collected)
	}

	for cmdLine := range collected {
		for _, bad := range []string{"<url>", "<node>", "selected)", "repos)", "(2)"} {
			if strings.Contains(cmdLine, bad) {
				t.Errorf("TUI CLI equivalent %q contains non-runnable display token %q", cmdLine, bad)
			}
		}
		if err := parseWSCommandAgainstCobra(cmdLine); err != nil {
			t.Errorf("TUI CLI equivalent %q failed to parse against cobra tree: %v", cmdLine, err)
		}
	}
}

func TestEnvRestoreRequiresExecute(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	envFile := filepath.Join(fx.home, "work", "code", "svc-restore", ".env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	origContent := "RESTORE_SECRET=sk-env-restore-test-123456\n"
	if err := os.WriteFile(envFile, []byte(origContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := execWS(t, fx, "db", "key", "init", "--json"); err != nil {
		t.Fatalf("db key init failed: %v\n%s", err, out)
	}
	if out, err := execWS(t, fx, "env", "scan", "--json"); err != nil {
		t.Fatalf("env scan failed: %v\n%s", err, out)
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}

	previewText, err := execWS(t, fx, "env", "restore")
	if err != nil {
		t.Fatalf("env restore preview (text) failed: %v\n%s", err, previewText)
	}
	if !strings.Contains(previewText, "[dry-run]") || !strings.Contains(previewText, "--execute") {
		t.Fatalf("expected dry-run preview hint from bare ws env restore, got:\n%s", previewText)
	}
	if _, statErr := os.Stat(envFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected bare ws env restore not to write file on disk, stat err=%v", statErr)
	}

	previewJSON, err := execWS(t, fx, "env", "restore", "--json")
	if err != nil {
		t.Fatalf("env restore preview (json) failed: %v\n%s", err, previewJSON)
	}
	var previewPayload struct {
		DryRun bool     `json:"dryRun"`
		Files  []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(previewJSON), &previewPayload); err != nil || !previewPayload.DryRun || len(previewPayload.Files) != 1 {
		t.Fatalf("unexpected env restore preview JSON: err=%v out=%s", err, previewJSON)
	}
	if _, statErr := os.Stat(envFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected json preview not to write file on disk, stat err=%v", statErr)
	}

	dryExecOut, err := execWS(t, fx, "env", "restore", "--execute", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("env restore --execute --dry-run failed: %v\n%s", err, dryExecOut)
	}
	if !strings.Contains(dryExecOut, `"dryRun": true`) {
		t.Fatalf("expected --dry-run to keep dryRun=true even with --execute, got:\n%s", dryExecOut)
	}
	if _, statErr := os.Stat(envFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected --execute --dry-run not to write file on disk, stat err=%v", statErr)
	}

	execJSON, err := execWS(t, fx, "env", "restore", "--execute", "--json")
	if err != nil {
		t.Fatalf("env restore --execute failed: %v\n%s", err, execJSON)
	}
	var execPayload struct {
		DryRun bool     `json:"dryRun"`
		Files  []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(execJSON), &execPayload); err != nil || execPayload.DryRun || len(execPayload.Files) != 1 {
		t.Fatalf("unexpected env restore --execute JSON: err=%v out=%s", err, execJSON)
	}
	got, err := os.ReadFile(envFile)
	if err != nil || string(got) != origContent {
		t.Fatalf("restored env file mismatch: got=%q err=%v", string(got), err)
	}
	if fi, err := os.Stat(envFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 on restored env file, got %04o err=%v", fi.Mode().Perm(), err)
	}

	if err := os.WriteFile(envFile, []byte("RESTORE_SECRET=modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := execWS(t, fx, "env", "restore", "--execute", "--json"); err == nil {
		t.Fatalf("expected env restore --execute without --allow replace to refuse overwriting existing file")
	}
	if out, err := execWS(t, fx, "env", "restore", "--execute", "--allow", "replace", "--json"); err != nil {
		t.Fatalf("expected env restore --execute --allow replace to overwrite existing file: %v\n%s", err, out)
	}
	got2, err := os.ReadFile(envFile)
	if err != nil || string(got2) != origContent {
		t.Fatalf("overwritten env file mismatch: got=%q err=%v", string(got2), err)
	}
}

func TestEnvRestorePreviewDoesNotCreateDB(t *testing.T) {
	fx := setupFixture(t)
	dbPath := filepath.Join(fx.home, ".ws", "ws.db")
	_ = os.Remove(dbPath)
	out, err := execWS(t, fx, "env", "restore", "--json")
	if err != nil {
		t.Fatalf("env restore preview: %v\n%s", err, out)
	}
	if _, serr := os.Stat(dbPath); !errors.Is(serr, os.ErrNotExist) {
		t.Fatalf("preview created ws.db (stat err=%v)", serr)
	}
	if !strings.Contains(out, "dryRun") {
		t.Fatalf("expected dry-run JSON, got %s", out)
	}
}

func TestIsValidTUITabCoversOldAndNewNames(t *testing.T) {
	valid := []string{
		"", "overview", "status", "home",
		"projects", "project", "repos", "repo", "git", "gg", "gh", "prs", "pr", "issues", "issue", "gh-prs", "gh-issues",
		"packages", "pkg", "pkgs", "installed", "system", "sys",
		"pkg-search", "search", "browse", "pkg-browse",
		"mise", "mise-configs", "mise-tools", "mise-outdated",
		"cleanup", "clean",
		"env", "secrets", "tokens", "vars", "envfiles", "files", "vault", "db", "1password", "op",
		"fleet", "nodes", "tailscale", "vm", "filesync", "sync-files", "diff-files", "fleet-diff",
		"workstation", "ws", "doctor", "doc", "themes", "theme", "presets", "preset", "settings", "prefs", "config", "watched", "watched-files", "history", "hist",
		"output", "out", "jobs",
		"0", "1", "2", "3", "4", "5", "6", "7", "8",
	}
	for _, tab := range valid {
		if !isValidTUITab(tab) {
			t.Errorf("isValidTUITab(%q) = false, want true", tab)
		}
	}
	for _, invalid := range []string{"unknown-tab", "9", "dashboard"} {
		if isValidTUITab(invalid) {
			t.Errorf("isValidTUITab(%q) = true, want false", invalid)
		}
	}
}
