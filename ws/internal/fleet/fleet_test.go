package fleet

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/store"
)

func testContext(t *testing.T) *app.Context {
	t.Helper()
	home := t.TempDir()
	return &app.Context{
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		LocalEnv:  map[string]string{},
	}
}

type fakeTSRunner struct {
	out []byte
	err error
}

func (f fakeTSRunner) Output(name string, args ...string) ([]byte, error) {
	return f.out, f.err
}

func TestListTailscaleNodes(t *testing.T) {
	sampleJSON := `{
		"Self": {
			"HostName": "laptop-dev",
			"DNSName": "laptop-dev.tailnet.ts.net.",
			"TailscaleIPs": ["100.64.0.10", "fd7a:115c:a1e0::1"],
			"OS": "macOS",
			"Online": true,
			"Tags": ["tag:workstation"],
			"Capabilities": ["https://tailscale.com/cap/ssh"]
		},
		"Peer": {
			"nodekey:1": {
				"HostName": "eu-runner",
				"DNSName": "eu-runner.tailnet.ts.net.",
				"TailscaleIPs": ["100.64.0.20"],
				"OS": "linux",
				"Arch": "amd64",
				"Online": true,
				"Tags": ["tag:hetzner", "tag:dev-vm"],
				"sshHostKeys": ["ssh-ed25519 AAAA"],
				"LastSeen": "2026-11-22T12:00:00Z"
			},
			"nodekey:2": {
				"HostName": "aws-box",
				"DNSName": "aws-box.tailnet.ts.net.",
				"TailscaleIPs": ["100.64.0.30"],
				"OS": "linux",
				"Online": false,
				"Tags": ["tag:aws"]
			}
		}
	}`

	nodes, err := ListTailscaleNodes(testContext(t), fakeTSRunner{out: []byte(sampleJSON)})
	if err != nil {
		t.Fatalf("ListTailscaleNodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(nodes), nodes)
	}
	if !nodes[0].IsSelf || nodes[0].Name != "laptop-dev" || nodes[0].TailscaleIP != "100.64.0.10" || !nodes[0].SSHEnabled {
		t.Fatalf("unexpected self node: %+v", nodes[0])
	}
	if nodes[1].Name != "eu-runner" || !nodes[1].Online || !nodes[1].SSHEnabled || nodes[1].Provider != "hetzner" || nodes[1].DNSName != "eu-runner.tailnet.ts.net" {
		t.Fatalf("unexpected online peer: %+v", nodes[1])
	}
	if nodes[2].Name != "aws-box" || nodes[2].Online || nodes[2].Provider != "aws" {
		t.Fatalf("unexpected offline peer: %+v", nodes[2])
	}

	if _, err := ListTailscaleNodes(testContext(t), fakeTSRunner{err: exec.ErrNotFound}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected descriptive missing binary error, got %v", err)
	}
}

func TestRenderJoinAndTagsCommandValidation(t *testing.T) {
	cmd, err := RenderJoinCommand(JoinOptions{
		AuthKeyEnv: "TS_AUTHKEY",
		Hostname:   "dev-vm-01",
		Tags:       []string{"tag:dev", "tag:hetzner"},
		SSH:        true,
	})
	if err != nil {
		t.Fatalf("RenderJoinCommand: %v", err)
	}
	want := `tailscale up --ssh --authkey="$TS_AUTHKEY" --hostname=dev-vm-01 --advertise-tags=tag:dev,tag:hetzner`
	if cmd != want {
		t.Fatalf("RenderJoinCommand = %q, want %q", cmd, want)
	}

	tagsCmd, err := RenderTagsCommand([]string{"tag:runner", "tag:linux"})
	if err != nil {
		t.Fatalf("RenderTagsCommand: %v", err)
	}
	if tagsCmd != "tailscale set --advertise-tags=tag:runner,tag:linux" {
		t.Fatalf("unexpected tags command: %q", tagsCmd)
	}

	badOpts := []JoinOptions{
		{Hostname: "bad;whoami"},
		{Hostname: "$(id)"},
		{Hostname: "-leading"},
		{AuthKeyEnv: "TS_KEY;rm -rf /"},
		{AuthKeyEnv: "tskey-raw-secret"},
		{Tags: []string{"not-prefixed"}},
		{Tags: []string{"tag:bad|pipe"}},
		{Tags: []string{"tag:bad`tick`"}},
	}
	for _, bo := range badOpts {
		if _, err := RenderJoinCommand(bo); err == nil {
			t.Errorf("expected RenderJoinCommand(%+v) to reject unsafe input", bo)
		}
	}
	if _, err := RenderTagsCommand(nil); err == nil {
		t.Fatalf("expected error for empty tags")
	}
}

type recordedExec struct {
	name  string
	args  []string
	stdin []byte
}

type fakeExecRunner struct {
	calls []recordedExec
	err   error
}

func (f *fakeExecRunner) Run(stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
	var data []byte
	if stdin != nil {
		var err error
		data, err = io.ReadAll(stdin)
		if err != nil {
			return nil, nil, err
		}
	}
	f.calls = append(f.calls, recordedExec{
		name:  name,
		args:  append([]string(nil), args...),
		stdin: data,
	})
	return []byte("ok"), nil, f.err
}

func TestPlanAndExecFleetSyncStdinPipelineAndDryRun(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")
	ctx := testContext(t)
	kr, err := store.InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	repoPath := filepath.Join(ctx.HomeDir, "work", "code", "svc-api")
	if _, err := db.SQL().Exec(
		`INSERT INTO repos(id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at)
		 VALUES('~/work/code/svc-api', 'svc-api', 'work', 'codecommit', 'codecommit://us-east-1/svc-api', ?, 'main', 0, 0, 0, '[]', 1000)`,
		repoPath,
	); err != nil {
		t.Fatalf("insert repo: %v", err)
	}

	secretPlain := []byte("DB_PASSWORD=super-secret-value-987654\n")
	blobID := "envfile:work/code/svc-api/.env"
	nonce, ct, err := store.Encrypt(kr.Key, secretPlain, []byte(blobID))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := db.PutSecretBlob(store.SecretBlob{
		ID:          blobID,
		Scope:       "envfile",
		Label:       "svc-api/.env",
		SourcePath:  "~/work/code/svc-api/.env",
		Nonce:       nonce,
		Ciphertext:  ct,
		Fingerprint: store.Fingerprint(kr.Key, string(secretPlain)),
		Perm:        0o600,
	}); err != nil {
		t.Fatalf("PutSecretBlob: %v", err)
	}

	dummyBin := filepath.Join(t.TempDir(), "ws-linux-amd64")
	if err := os.WriteFile(dummyBin, []byte("ELF-FAKE-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := SyncSpec{
		Node:          "eu-vm-1",
		Transport:     "tailscale-ssh",
		Preset:        "ubuntu-runner",
		Arch:          "amd64",
		BinaryPath:    dummyBin,
		NoZones:       true,
		PushEnvFiles:  true,
		SelectedRepos: []string{"svc-api"},
	}

	ctx.DryRun = true
	dryPlan, err := PlanFleetSync(ctx, db, kr, spec)
	if err != nil {
		t.Fatalf("PlanFleetSync dry-run: %v", err)
	}
	var fleetCount int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM fleet_nodes`).Scan(&fleetCount); err != nil {
		t.Fatal(err)
	}
	if fleetCount != 0 {
		t.Fatalf("DryRun PlanFleetSync must not mutate fleet_nodes, got %d rows", fleetCount)
	}
	dryRunner := &fakeExecRunner{}
	if err := ExecFleetSync(ctx, db, kr, dryPlan, dryRunner); err != nil {
		t.Fatalf("ExecFleetSync dry-run: %v", err)
	}
	if len(dryRunner.calls) != 0 {
		t.Fatalf("DryRun ExecFleetSync must execute zero commands, got %d", len(dryRunner.calls))
	}

	ctx.DryRun = false
	plan, err := PlanFleetSync(ctx, db, kr, spec)
	if err != nil {
		t.Fatalf("PlanFleetSync: %v", err)
	}

	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM fleet_nodes`).Scan(&fleetCount); err != nil {
		t.Fatal(err)
	}
	if fleetCount != 0 {
		t.Fatalf("PlanFleetSync must not mutate fleet_nodes before ExecFleetSync, got %d rows", fleetCount)
	}
	if !slices.Equal(plan.SkippedConfigs, DefaultVMExcludes()) {
		t.Fatalf("expected DefaultVMExcludes in SkippedConfigs, got %v", plan.SkippedConfigs)
	}
	if plan.EnvFilesCount != 1 || !slices.Equal(plan.EnvBlobIDs, []string{blobID}) {
		t.Fatalf("unexpected EnvBlobIDs/Count: count=%d ids=%v", plan.EnvFilesCount, plan.EnvBlobIDs)
	}

	checklistCmds := make([]string, 0, len(plan.ManualChecklist))
	for _, c := range plan.ManualChecklist {
		checklistCmds = append(checklistCmds, c.Command)
	}
	for _, wantSub := range []string{
		"claude auth login",
		"gh auth login",
		"aws sso login",
		"tailscale status",
	} {
		found := false
		for _, cmdStr := range checklistCmds {
			if strings.Contains(cmdStr, wantSub) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q in manual checklist, got %v", wantSub, checklistCmds)
		}
	}

	runner := &fakeExecRunner{}
	if err := ExecFleetSync(ctx, db, kr, plan, runner); err != nil {
		t.Fatalf("ExecFleetSync: %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("expected 5 ssh calls (bin, apply, layout, clone, env), got %d", len(runner.calls))
	}

	envCall := runner.calls[4]
	joinedArgs := strings.Join(envCall.args, " ")
	if strings.Contains(joinedArgs, "super-secret-value-987654") {
		t.Fatalf("decrypted secret leaked into SSH argv: %s", joinedArgs)
	}
	if !strings.Contains(joinedArgs, "umask 077") || !strings.Contains(joinedArgs, "chmod 0600") || !strings.Contains(joinedArgs, "code/svc-api/.env") {
		t.Fatalf("expected umask 077 + chmod 0600 + --no-zones remapped path in env push command, got: %s", joinedArgs)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(envCall.stdin)))
	if err != nil {
		t.Fatalf("decode stdin base64 payload: %v", err)
	}
	if !bytes.Equal(decoded, secretPlain) {
		t.Fatalf("stdin payload mismatch: got %q want %q", decoded, secretPlain)
	}

	var syncedAt int64
	if err := db.SQL().QueryRow(`SELECT last_synced_at FROM fleet_nodes WHERE name = 'eu-vm-1'`).Scan(&syncedAt); err != nil {
		t.Fatalf("query fleet_nodes: %v", err)
	}
	if syncedAt == 0 {
		t.Fatalf("expected last_synced_at to be updated after ExecFleetSync")
	}

	failRunner := &fakeExecRunner{err: errors.New("ssh unreachable")}
	if err := ExecFleetSync(ctx, db, kr, plan, failRunner); err == nil {
		t.Fatalf("expected ExecFleetSync error when runner fails")
	}

	noRepoSpec := spec
	noRepoSpec.SelectedRepos = nil
	if _, err := PlanFleetSync(ctx, db, kr, noRepoSpec); err == nil || !strings.Contains(err.Error(), "--repos") {
		t.Fatalf("expected PlanFleetSync --push-env without --repos error, got %v", err)
	}

	personalRepo := filepath.Join(ctx.HomeDir, "personal", "code", "svc-api")
	if _, err := db.SQL().Exec(
		`INSERT INTO repos(id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at)
		 VALUES('~/personal/code/svc-api', 'svc-api', 'personal', 'github', 'https://user:secret-pat@github.com/acme/svc-api.git', ?, 'main', 0, 0, 0, '[]', 1000)`,
		personalRepo,
	); err != nil {
		t.Fatalf("insert personal repo: %v", err)
	}
	pPlain := []byte("PERSONAL_VAR=personal-only\n")
	pID := "envfile:personal/code/svc-api/.env"
	pNonce, pCT, err := store.Encrypt(kr.Key, pPlain, []byte(pID))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSecretBlob(store.SecretBlob{
		ID:          pID,
		Scope:       "envfile",
		Label:       "svc-api/.env",
		SourcePath:  "~/personal/code/svc-api/.env",
		Nonce:       pNonce,
		Ciphertext:  pCT,
		Fingerprint: store.Fingerprint(kr.Key, string(pPlain)),
		Perm:        0o600,
	}); err != nil {
		t.Fatal(err)
	}

	workOnlySpec := spec
	workOnlySpec.SelectedRepos = []string{"work/svc-api"}
	workOnlyPlan, err := PlanFleetSync(ctx, db, kr, workOnlySpec)
	if err != nil {
		t.Fatalf("PlanFleetSync work/svc-api: %v", err)
	}
	if !slices.Equal(workOnlyPlan.EnvBlobIDs, []string{blobID}) {
		t.Fatalf("expected only work blob %q, got %v", blobID, workOnlyPlan.EnvBlobIDs)
	}
	if len(workOnlyPlan.EnvFiles) != 1 || !strings.Contains(workOnlyPlan.EnvFiles[0], "~/code/svc-api/.env") {
		t.Fatalf("expected explicit EnvFiles preview mapping, got %v", workOnlyPlan.EnvFiles)
	}

	collideSpec := spec
	collideSpec.SelectedRepos = []string{"work/svc-api", "personal/svc-api"}
	if _, err := PlanFleetSync(ctx, db, kr, collideSpec); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected --no-zones collision error, got %v", err)
	}

	credSpec := spec
	credSpec.PushEnvFiles = false
	credSpec.SelectedRepos = []string{"personal/svc-api"}
	credPlan, err := PlanFleetSync(ctx, db, kr, credSpec)
	if err != nil {
		t.Fatalf("PlanFleetSync cred strip: %v", err)
	}
	for _, st := range credPlan.Steps {
		if st.ID == "clone-repos" && strings.Contains(strings.Join(st.Command, " "), "secret-pat") {
			t.Fatalf("clone step leaked URL credentials: %v", st.Command)
		}
	}

	ambigSpec := spec
	ambigSpec.SelectedRepos = []string{"svc-api"}
	if _, err := PlanFleetSync(ctx, db, kr, ambigSpec); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous bare repo error across two zones, got %v", err)
	}

	aadPlain := []byte("BOUND_AAD_KEY=ok-123456\n")
	aadSrc := "~/personal/code/svc-api/.env.local"
	aadID := "envfile:personal/code/svc-api/.env.local"
	nAad, ctAad, err := store.Encrypt(kr.Key, aadPlain, store.EnvBlobAAD(aadID, "envfile", aadSrc, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSecretBlob(store.SecretBlob{
		ID:          aadID,
		Scope:       "envfile",
		Label:       "personal/code/svc-api/.env.local",
		SourcePath:  aadSrc,
		Nonce:       nAad,
		Ciphertext:  ctAad,
		Fingerprint: store.Fingerprint(kr.Key, string(aadPlain)),
		Perm:        0o600,
	}); err != nil {
		t.Fatal(err)
	}
	aadPlan, err := PlanFleetSync(ctx, db, kr, SyncSpec{
		Node:          "eu-vm-1",
		Transport:     "tailscale-ssh",
		BinaryPath:    dummyBin,
		PushEnvFiles:  true,
		SelectedRepos: []string{"personal/svc-api"},
	})
	if err != nil {
		t.Fatalf("PlanFleetSync EnvBlobAAD: %v", err)
	}
	aadRunner := &fakeExecRunner{}
	if err := ExecFleetSync(ctx, db, kr, aadPlan, aadRunner); err != nil {
		t.Fatalf("ExecFleetSync EnvBlobAAD: %v", err)
	}

	badID := "envfile:work/code/svc-api/.bashrc"
	nBad, ctBad, _ := store.Encrypt(kr.Key, []byte("evil\n"), store.EnvBlobAAD(badID, "envfile", "~/work/code/svc-api/.bashrc", 0o600))
	if err := db.PutSecretBlob(store.SecretBlob{
		ID:         badID,
		Scope:      "envfile",
		Label:      "work/code/svc-api/.bashrc",
		SourcePath: "~/work/code/svc-api/.bashrc",
		Nonce:      nBad,
		Ciphertext: ctBad,
		Perm:       0o600,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanFleetSync(ctx, db, kr, SyncSpec{
		Node:          "eu-vm-1",
		Transport:     "tailscale-ssh",
		BinaryPath:    dummyBin,
		PushEnvFiles:  true,
		SelectedRepos: []string{"work/svc-api"},
	}); err == nil || !strings.Contains(err.Error(), "non-writable") {
		t.Fatalf("expected non-writable remote env filename rejection for .bashrc, got %v", err)
	}
}

func TestStep4BFleetSecurity(t *testing.T) {

	if err := ValidateNodeTarget("ubuntu@100.64.0.12"); err != nil {
		t.Fatalf("ValidateNodeTarget user@ip: %v", err)
	}
	if err := ValidateNodeTarget("bad user@host"); err == nil {
		t.Fatal("expected space in SSH user to fail")
	}
	nodes, err := ListTailscaleNodes(testContext(t), fakeTSRunner{out: []byte(`{
		"Peer": {
			"nodekey:1": {
				"HostName": "Owner's MacBook Pro",
				"DNSName": "owners-macbook.tailnet.ts.net.",
				"TailscaleIPs": ["100.64.0.55"],
				"Online": true
			}
		}
	}`)})
	if err != nil || len(nodes) != 1 {
		t.Fatalf("ListTailscaleNodes: nodes=%+v err=%v", nodes, err)
	}
	if nodes[0].Name != "owners-macbook" || ValidateHostname(nodes[0].Name) != nil {
		t.Fatalf("expected MagicDNS machine name 'owners-macbook', got %q", nodes[0].Name)
	}
	if err := ValidateNodeTarget(nodes[0].TargetID); err != nil || strings.Contains(nodes[0].TargetID, "'") {
		t.Fatalf("expected sanitized TargetID, got %q (err=%v)", nodes[0].TargetID, err)
	}
}

type funcExecRunner func(stdin io.Reader, name string, args ...string) ([]byte, []byte, error)

func (f funcExecRunner) Run(stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
	return f(stdin, name, args...)
}

func TestDefaultVMExcludesAlignWithShippedConfigs(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(repoRoot, "links", "common.links")); err != nil {
		t.Fatalf("locate repo root from %s: %v", cwd, err)
	}

	linkMatches, err := filepath.Glob(filepath.Join(repoRoot, "links", "*.links"))
	if err != nil || len(linkMatches) == 0 {
		t.Fatalf("glob links/*.links: %v", err)
	}
	var allLinks strings.Builder
	for _, lf := range linkMatches {
		raw, rerr := os.ReadFile(lf)
		if rerr != nil {
			t.Fatal(rerr)
		}
		allLinks.Write(raw)
		allLinks.WriteByte('\n')
	}
	linksText := allLinks.String()

	excludes := DefaultVMExcludes()
	if len(excludes) == 0 {
		t.Fatal("DefaultVMExcludes returned empty slice")
	}
	for _, stale := range []string{"aerospace", "raycast", "sketchybar", "fonts"} {
		if slices.Contains(excludes, stale) {
			t.Fatalf("DefaultVMExcludes still contains unshipped tool %q: %v", stale, excludes)
		}
	}
	for _, name := range excludes {
		cfgDir := filepath.Join(repoRoot, "config", name)
		fi, serr := os.Stat(cfgDir)
		if serr != nil || !fi.IsDir() {
			t.Errorf("DefaultVMExcludes entry %q has no config/%s directory", name, name)
		}
		if !strings.Contains(linksText, "config/"+name) {
			t.Errorf("DefaultVMExcludes entry %q is not referenced in links/*.links", name)
		}
	}
}
