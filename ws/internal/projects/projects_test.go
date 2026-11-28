package projects

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/shell"
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

func runTestGit(t *testing.T, home, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = shell.MergeEnv(os.Environ(), []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Tester",
		"GIT_AUTHOR_EMAIL=tester@example.invalid",
		"GIT_COMMITTER_NAME=Tester",
		"GIT_COMMITTER_EMAIL=tester@example.invalid",
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
}

func TestDetectForge(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/repo.git", "github"},
		{"git@github.com:org/repo.git", "github"},
		{"git@github.com-work:org/repo.git", "github"},
		{"org/repo", "github"},
		{"codecommit://us-east-1/service", "codecommit"},
		{"codecommit::eu-west-1://profile@service", "codecommit"},
		{"https://git-codecommit.us-east-1.amazonaws.com/v1/repos/service", "codecommit"},
		{"ssh://git-codecommit.eu-central-1.amazonaws.com/v1/repos/service", "codecommit"},
		{"https://gitlab.com/team/app.git", "gitlab"},
		{"git@gitlab.example.invalid:team/app.git", "gitlab"},
		{"https://bitbucket.org/team/app.git", "other"},
		{"/tmp/local-bare.git", "other"},
		{"", "other"},
	}

	for _, tc := range cases {
		if got := DetectForge(tc.url); got != tc.want {
			t.Errorf("DetectForge(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestScanReposUntrackedLocalsAndBackupCrossRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "work")

	ctx := testContext(t)
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	workRoot := filepath.Join(ctx.HomeDir, "work")
	workCode := filepath.Join(workRoot, "code")
	if err := os.MkdirAll(workCode, 0o755); err != nil {
		t.Fatal(err)
	}
	miseWork := "[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\n"
	if err := os.WriteFile(filepath.Join(workRoot, "mise.toml"), []byte(miseWork), 0o600); err != nil {
		t.Fatal(err)
	}

	apiRepo := filepath.Join(workCode, "api-service")
	if err := os.MkdirAll(apiRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, apiRepo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(apiRepo, "README.md"), []byte("api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, apiRepo, "add", "README.md")
	runTestGit(t, ctx.HomeDir, apiRepo, "commit", "-m", "init")
	runTestGit(t, ctx.HomeDir, apiRepo, "remote", "add", "origin", "git@github.com:acme/api-service.git")

	writeFile := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	envPath := filepath.Join(apiRepo, ".env")
	writeFile(envPath, "API_KEY=secret-123\n")
	writeFile(filepath.Join(apiRepo, ".env.local"), "DEBUG=1\n")
	writeFile(filepath.Join(apiRepo, ".env.example"), "API_KEY=changeme\n")
	writeFile(filepath.Join(apiRepo, ".env.sample"), "API_KEY=changeme\n")
	writeFile(filepath.Join(apiRepo, ".env.template"), "API_KEY=changeme\n")
	writeFile(filepath.Join(apiRepo, "mise.local.toml"), "[env]\nTOKEN=\"abc\"\n")
	writeFile(filepath.Join(apiRepo, ".local", "override.json"), "{}\n")
	writeFile(filepath.Join(apiRepo, "infra", "terraform.tfvars"), "region = \"us-east-1\"\n")
	writeFile(filepath.Join(apiRepo, "config.local.json"), "{}\n")
	writeFile(filepath.Join(apiRepo, "settings.local.yaml"), "a: 1\n")
	writeFile(filepath.Join(apiRepo, "node_modules", "pkg", ".env"), "IGNORED=1\n")

	if err := db.PutSecretBlob(store.SecretBlob{
		ID:          "envfile:work/code/api-service/.env",
		Scope:       "envfile",
		Label:       "api-service/.env",
		SourcePath:  ctx.CollapseHome(envPath),
		Nonce:       []byte("123456789012"),
		Ciphertext:  []byte("cipher"),
		Fingerprint: "fp:test",
		Perm:        0o600,
	}); err != nil {
		t.Fatalf("PutSecretBlob: %v", err)
	}

	flatRepo := filepath.Join(ctx.HomeDir, "code", "flat-app")
	if err := os.MkdirAll(flatRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, flatRepo, "init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(flatRepo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, flatRepo, "add", "main.go")
	runTestGit(t, ctx.HomeDir, flatRepo, "commit", "-m", "init")
	runTestGit(t, ctx.HomeDir, flatRepo, "remote", "add", "origin", "codecommit://us-east-1/flat-app")

	records, err := ScanRepos(ctx, db)
	if err != nil {
		t.Fatalf("ScanRepos: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 repos (zoned + flat), got %d: %+v", len(records), records)
	}

	var apiRec, flatRec *RepoRecord
	for i := range records {
		switch records[i].Name {
		case "api-service":
			apiRec = &records[i]
		case "flat-app":
			flatRec = &records[i]
		}
	}
	if apiRec == nil || flatRec == nil {
		t.Fatalf("missing expected repos in scan result: %+v", records)
	}

	if apiRec.Zone != "work" || apiRec.Forge != "github" || apiRec.Branch != "main" || !apiRec.Dirty {
		t.Fatalf("unexpected api-service record: %+v", apiRec)
	}
	if flatRec.Zone != "" || flatRec.Forge != "codecommit" || flatRec.Branch != "dev" || flatRec.Dirty {
		t.Fatalf("unexpected flat-app record: %+v", flatRec)
	}

	gotPaths := map[string]LocalFileEntry{}
	for _, lf := range apiRec.UntrackedLocals {
		gotPaths[lf.RelPath] = lf
	}
	wantFiles := []string{
		".env",
		".env.local",
		".local/override.json",
		"config.local.json",
		"infra/terraform.tfvars",
		"mise.local.toml",
		"settings.local.yaml",
	}
	for _, w := range wantFiles {
		if _, ok := gotPaths[w]; !ok {
			t.Errorf("expected untracked local file %q, got %+v", w, apiRec.UntrackedLocals)
		}
	}
	for _, excluded := range []string{".env.example", ".env.sample", ".env.template", "node_modules/pkg/.env"} {
		if _, ok := gotPaths[excluded]; ok {
			t.Errorf("did not expect excluded file %q in untracked locals", excluded)
		}
	}
	if !gotPaths[".env"].BackedUp || !gotPaths[".env"].IsSecret {
		t.Fatalf("expected .env to be marked BackedUp=true and IsSecret=true, got %+v", gotPaths[".env"])
	}
	if gotPaths[".env.local"].BackedUp {
		t.Fatalf("expected .env.local BackedUp=false, got %+v", gotPaths[".env.local"])
	}

	cached, err := ListCachedRepos(db)
	if err != nil {
		t.Fatalf("ListCachedRepos: %v", err)
	}
	if len(cached) != 2 {
		t.Fatalf("expected 2 cached repos in ws.db, got %d", len(cached))
	}
}

func TestFuzzyMatchRepos(t *testing.T) {
	repos := []RepoRecord{
		{Name: "workstation", Zone: "personal", Forge: "github", LocalPath: "/home/u/personal/code/workstation"},
		{Name: "billing-service", Zone: "work", Forge: "codecommit", LocalPath: "/home/u/work/code/billing-service"},
		{Name: "web-portal", Zone: "work", Forge: "github", LocalPath: "/home/u/work/code/web-portal"},
		{Name: "work-notes", Zone: "personal", Forge: "gitlab", LocalPath: "/home/u/personal/code/work-notes"},
	}

	all := FuzzyMatchRepos(repos, "")
	if len(all) != len(repos) {
		t.Fatalf("empty query should return all %d repos, got %d", len(repos), len(all))
	}

	got := FuzzyMatchRepos(repos, "bsvc")
	if len(got) != 1 || got[0].Name != "billing-service" {
		t.Fatalf("expected subsequence match billing-service, got %+v", got)
	}

	gotPrefix := FuzzyMatchRepos(repos, "work")
	if len(gotPrefix) < 3 {
		t.Fatalf("expected at least 3 matches for 'work', got %+v", gotPrefix)
	}
	if gotPrefix[0].Name != "work-notes" && gotPrefix[0].Name != "workstation" {
		t.Fatalf("expected name-prefix match ranked first, got %+v", gotPrefix[0])
	}

	gotMulti := FuzzyMatchRepos(repos, "work portal")
	if len(gotMulti) != 1 || gotMulti[0].Name != "web-portal" {
		t.Fatalf("expected multi-term zone+name match web-portal, got %+v", gotMulti)
	}

	if gotWstat := FuzzyMatchRepos(repos, "wstat"); len(gotWstat) == 0 || gotWstat[0].Name != "workstation" {
		t.Fatalf("expected workstation #1 for 'wstat', got %+v", gotWstat)
	}

	if gotPrefix[0].Name != "work-notes" || gotPrefix[1].Name != "workstation" {
		t.Fatalf("expected both work* repos ranked ahead of ~/work/* repos, got %+v", gotPrefix)
	}

	gotNeg := FuzzyMatchRepos(repos, "work !portal !billing")
	if len(gotNeg) != 2 {
		t.Fatalf("expected 2 repos after !portal !billing, got %+v", gotNeg)
	}
}

type fakeCmdCall struct {
	env  []string
	dir  string
	name string
	args []string
}

type fakeCommandRunner struct {
	calls   []fakeCmdCall
	handler func(env []string, dir, name string, args ...string) ([]byte, error)
}

func (f *fakeCommandRunner) Output(env []string, dir, name string, args ...string) ([]byte, error) {
	cpEnv := append([]string(nil), env...)
	cpArgs := append([]string(nil), args...)
	f.calls = append(f.calls, fakeCmdCall{env: cpEnv, dir: dir, name: name, args: cpArgs})
	if f.handler != nil {
		return f.handler(env, dir, name, args...)
	}
	return nil, nil
}

func TestPlanCloneAndExecClone(t *testing.T) {
	t.Setenv("WS_ZONES", "")
	ctx := testContext(t)
	ctx.LocalEnv["WS_ZONES"] = "work|Worker|worker@example.invalid|ssh-ed25519 AAAA|work-prod\n"

	ghPlan, err := PlanClone(ctx, CloneRequest{
		Source: "acme/platform",
		Zone:   "work",
	})
	if err != nil {
		t.Fatalf("PlanClone github: %v", err)
	}
	if ghPlan.Forge != "github" || !ghPlan.UseGH {
		t.Fatalf("expected github UseGH plan, got %+v", ghPlan)
	}
	wantDest := filepath.Join(ctx.HomeDir, "work", "code", "platform")
	if ghPlan.DestDir != wantDest {
		t.Fatalf("DestDir = %q, want %q", ghPlan.DestDir, wantDest)
	}
	if !slices.Equal(ghPlan.Command, []string{"gh", "repo", "clone", "acme/platform", wantDest}) {
		t.Fatalf("unexpected gh clone command: %v", ghPlan.Command)
	}

	ccPlan, err := PlanClone(ctx, CloneRequest{
		Source:  "codecommit::us-east-1://work-prod@infra-core",
		Zone:    "work",
		NoZones: true,
	})
	if err != nil {
		t.Fatalf("PlanClone codecommit: %v", err)
	}
	if ccPlan.Forge != "codecommit" || ccPlan.UseGH {
		t.Fatalf("expected codecommit git clone plan, got %+v", ccPlan)
	}
	wantFlat := filepath.Join(ctx.HomeDir, "code", "infra-core")
	if ccPlan.DestDir != wantFlat {
		t.Fatalf("DestDir = %q, want %q", ccPlan.DestDir, wantFlat)
	}
	if !slices.Equal(ccPlan.Command, []string{"git", "clone", "--", "codecommit::us-east-1://work-prod@infra-core", wantFlat}) {
		t.Fatalf("unexpected codecommit clone command: %v", ccPlan.Command)
	}

	badRequests := []CloneRequest{
		{Source: ""},
		{Source: "--upload-pack=evil"},
		{Source: "acme/repo\nwhoami"},
		{Source: "acme/../../escape"},
		{Source: "acme/repo", DestName: "../escape"},
		{Source: "acme/repo", DestName: "-bad"},
		{Source: "acme/repo", Zone: "../badzone"},
	}
	for _, bad := range badRequests {
		if _, err := PlanClone(ctx, bad); err == nil {
			t.Errorf("expected PlanClone(%+v) to fail", bad)
		}
	}

	ctx.DryRun = true
	runner := &fakeCommandRunner{}
	if err := ExecClone(ctx, ccPlan, runner); err != nil {
		t.Fatalf("ExecClone dry-run: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected zero calls in dry-run, got %d", len(runner.calls))
	}

	ctx.DryRun = false
	if err := ExecClone(ctx, ccPlan, runner); err != nil {
		t.Fatalf("ExecClone: %v", err)
	}
	if len(runner.calls) != 7 {
		t.Fatalf("expected clone + 6 git config calls, got %d: %+v", len(runner.calls), runner.calls)
	}
	if runner.calls[0].name != "git" || runner.calls[1].args[2] != "user.name" || runner.calls[2].args[2] != "user.email" || runner.calls[4].args[2] != "gpg.format" {
		t.Fatalf("unexpected ExecClone calls: %+v", runner.calls)
	}
}

func TestBatchPullSeparatesCodeCommitAndInteractiveCmd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := testContext(t)
	codeDir := filepath.Join(ctx.HomeDir, "code")
	remoteDir := filepath.Join(ctx.HomeDir, "remote.git")
	if err := os.MkdirAll(codeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runTestGit(t, ctx.HomeDir, ctx.HomeDir, "init", "--bare", remoteDir)

	repoBehind := filepath.Join(codeDir, "repo-behind")
	runTestGit(t, ctx.HomeDir, codeDir, "clone", remoteDir, repoBehind)
	if err := os.WriteFile(filepath.Join(repoBehind, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, repoBehind, "add", "f.txt")
	runTestGit(t, ctx.HomeDir, repoBehind, "commit", "-m", "v1")
	runTestGit(t, ctx.HomeDir, repoBehind, "push", "-u", "origin", "HEAD")

	repoAhead := filepath.Join(codeDir, "repo-ahead")
	runTestGit(t, ctx.HomeDir, codeDir, "clone", remoteDir, repoAhead)
	if err := os.WriteFile(filepath.Join(repoAhead, "f.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, repoAhead, "add", "f.txt")
	runTestGit(t, ctx.HomeDir, repoAhead, "commit", "-m", "v2")
	runTestGit(t, ctx.HomeDir, repoAhead, "push")

	ccRepo := RepoRecord{
		ID:        "~/work/code/cc-svc",
		Name:      "cc-svc",
		Zone:      "work",
		Forge:     "codecommit",
		RemoteURL: "codecommit://us-east-1/cc-svc",
		LocalPath: filepath.Join(ctx.HomeDir, "work", "code", "cc-svc"),
	}

	res, err := BatchPull(ctx, []RepoRecord{
		{Name: "repo-behind", Forge: "github", LocalPath: repoBehind},
		{Name: "repo-ahead", Forge: "github", LocalPath: repoAhead},
		ccRepo,
	})
	if err != nil {
		t.Fatalf("BatchPull: %v", err)
	}
	if len(res.NeedsInteractive) != 1 || res.NeedsInteractive[0].Name != "cc-svc" {
		t.Fatalf("expected codecommit repo separated into NeedsInteractive, got %+v", res.NeedsInteractive)
	}
	if len(res.Updated) != 1 || len(res.UpToDate) != 1 || len(res.Failed) != 0 {
		t.Fatalf("unexpected batch pull counts: %+v", res)
	}

	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	cmd := BuildInteractivePullCmd(ctx, ccRepo)
	if !slices.Equal(cmd.Args, []string{"git", "-C", ccRepo.LocalPath, "pull", "--ff-only"}) {
		t.Fatalf("unexpected interactive pull args: %v", cmd.Args)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") {
			t.Fatalf("interactive pull must not set GIT_TERMINAL_PROMPT: %s", kv)
		}
	}
}

func TestFetchGitHubItemsCachingAndGracefulNotes(t *testing.T) {
	t.Setenv("WS_ZONES", "")
	t.Setenv("GH_CONFIG_DIR", "")
	ctx := testContext(t)
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	for _, z := range []string{"work", "personal"} {
		zroot := filepath.Join(ctx.HomeDir, z)
		if err := os.MkdirAll(zroot, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := fmt.Sprintf("[env]\nWS_ZONE = %q\nGH_CONFIG_DIR = \"{{env.HOME}}/.config/gh-%s\"\n", z, z)
		if err := os.WriteFile(filepath.Join(zroot, "mise.toml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	runner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			ghDir := ""
			for _, kv := range env {
				if strings.HasPrefix(kv, "GH_CONFIG_DIR=") {
					ghDir = strings.TrimPrefix(kv, "GH_CONFIG_DIR=")
				}
			}
			if strings.HasSuffix(ghDir, "gh-personal") {
				return nil, errors.New("not logged into any GitHub hosts; run gh auth login")
			}
			if slices.Contains(args, "prs") && slices.Contains(args, "--involves=@me") {
				return []byte(fmt.Sprintf(`[{"repository":{"nameWithOwner":"acme/api"},"number":12,"title":"Add feature","state":"open","author":{"login":"dev"},"url":"https://github.com/acme/api/pull/12","body":"Implements **feature**","labels":[{"name":"enhancement"}],"assignees":[{"login":"dev"}],"isDraft":true,"commentsCount":3,"updatedAt":%q}]`, nowStr)), nil
			}
			if slices.Contains(args, "prs") && slices.Contains(args, "--review-requested=@me") {
				return []byte(fmt.Sprintf(`[{"repository":{"nameWithOwner":"acme/api"},"number":12,"title":"Add feature","state":"open","author":{"login":"dev"},"url":"https://github.com/acme/api/pull/12","body":"Implements **feature**","labels":[{"name":"enhancement"}],"isDraft":true,"commentsCount":3,"updatedAt":%q}]`, nowStr)), nil
			}
			if slices.Contains(args, "issues") {
				return []byte(fmt.Sprintf(`[{"repository":{"nameWithOwner":"acme/api"},"number":7,"title":"Fix bug","state":"open","author":{"login":"ops"},"url":"https://github.com/acme/api/issues/7","body":"Repro steps","labels":[{"name":"bug"}],"commentsCount":2,"updatedAt":%q}]`, nowStr)), nil
			}
			return []byte(`[]`), nil
		},
	}

	items, notes, err := FetchGitHubItems(ctx, db, true, runner)
	if err != nil {
		t.Fatalf("FetchGitHubItems refresh: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 deduplicated items (1 PR + 1 issue), got %d: %+v", len(items), items)
	}
	for _, it := range items {
		if it.Kind == "pr" && (!it.IsDraft || it.CommentsCount != 3 || it.Body == "" || len(it.Labels) != 1) {
			t.Fatalf("expected enriched PR metadata from search & cache, got %+v", it)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "personal") {
		t.Fatalf("expected 1 unauth note for personal zone, got %+v", notes)
	}
	callsAfterFirst := len(runner.calls)
	if callsAfterFirst != 4 {
		t.Fatalf("expected 3 calls for work + 1 short-circuited call for personal, got %d", callsAfterFirst)
	}

	cachedItems, cachedNotes, err := FetchGitHubItems(ctx, db, false, runner)
	if err != nil {
		t.Fatalf("FetchGitHubItems cached: %v", err)
	}
	if len(runner.calls) != callsAfterFirst {
		t.Fatalf("expected cached call not to invoke runner, got %d calls", len(runner.calls)-callsAfterFirst)
	}
	if len(cachedItems) != 2 || len(cachedNotes) != 0 {
		t.Fatalf("unexpected cached result: items=%+v notes=%+v", cachedItems, cachedNotes)
	}
	for _, it := range cachedItems {
		if it.Kind == "pr" && (!it.IsDraft || it.CommentsCount != 3 || len(it.Labels) != 1) {
			t.Fatalf("expected enriched PR metadata preserved in SQLite gh_cache, got %+v", it)
		}
	}
}

func TestExtractGitHubRepoSlugAndRepoDetailFetch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://github.com/acme/api-service.git", "acme/api-service"},
		{"git@github.com:acme/api-service.git", "acme/api-service"},
		{"git@github.com-work:acme/worker", "acme/worker"},
		{"git@github-work:org/repo.git", "org/repo"},
		{"git@github-personal:org/repo.git", "org/repo"},
		{"ssh://git@github.com/acme/tools.git", "acme/tools"},
		{"acme/cli", "acme/cli"},
		{"codecommit::us-east-1://profile@repo", ""},
		{"https://gitlab.com/org/repo.git", ""},
	}
	for _, tc := range cases {
		if got := ExtractGitHubRepoSlug(tc.in); got != tc.want {
			t.Errorf("ExtractGitHubRepoSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	t.Setenv("WS_ZONES", "")
	ctx := testContext(t)
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	if _, err := FetchRepoGitHubItems(ctx, db, "--flag", "", "open", true, nil); err == nil {
		t.Fatalf("expected invalid slug rejection")
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	runner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
				return []byte(fmt.Sprintf(`[{"number":91,"title":"Repo PR","state":"OPEN","author":{"login":"a1"},"url":"https://github.com/acme/api/pull/91","body":"PR markdown","labels":[{"name":"ready"}],"isDraft":false,"comments":[{"author":{"login":"rev"},"body":"LGTM","createdAt":%q}],"updatedAt":%q}]`, nowStr, nowStr)), nil
			}
			if len(args) >= 2 && args[0] == "issue" && args[1] == "list" {
				return []byte(fmt.Sprintf(`[{"number":55,"title":"Open issue","state":"OPEN","author":{"login":"a2"},"url":"https://github.com/acme/api/issues/55","body":"Issue body","labels":[{"name":"bug"}],"comments":[],"updatedAt":%q}]`, nowStr)), nil
			}
			if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return []byte(fmt.Sprintf(`{"number":91,"title":"Repo PR","state":"OPEN","author":{"login":"a1"},"url":"https://github.com/acme/api/pull/91","body":"## Summary\nFull body","labels":[{"name":"ready"}],"isDraft":false,"headRefName":"feat/x","baseRefName":"main","reviewDecision":"APPROVED","mergeable":"MERGEABLE","additions":12,"deletions":3,"changedFiles":2,"comments":[{"author":{"login":"rev"},"body":"Looks good","createdAt":%q}],"statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}],"updatedAt":%q}`, nowStr, nowStr)), nil
			}
			return []byte(`[]`), nil
		},
	}

	items, err := FetchRepoGitHubItems(ctx, db, "acme/api", "work", "open", true, runner)
	if err != nil {
		t.Fatalf("FetchRepoGitHubItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 repo items (1 PR + 1 issue), got %d: %+v", len(items), items)
	}

	prevCalls := len(runner.calls)
	cached, err := FetchRepoGitHubItems(ctx, db, "acme/api", "work", "open", false, runner)
	if err != nil {
		t.Fatalf("FetchRepoGitHubItems cached: %v", err)
	}
	if len(runner.calls) != prevCalls || len(cached) != 2 {
		t.Fatalf("expected cache hit for repo items (calls diff=%d, len=%d)", len(runner.calls)-prevCalls, len(cached))
	}

	det, err := FetchGHItemDetail(ctx, "pr", "acme/api", "work", 91, runner)
	if err != nil {
		t.Fatalf("FetchGHItemDetail: %v", err)
	}
	if det.HeadRef != "feat/x" || det.BaseRef != "main" || det.ReviewDecision != "APPROVED" || det.ChecksSummary != "1 passed" || len(det.Comments) != 1 {
		t.Fatalf("unexpected PR detail: %+v", det)
	}
}

func TestStripURLCredentials(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "https://x-access-token:secret-tok@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			in:   "https://user@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			in:   "git@github.com:org/repo.git",
			want: "git@github.com:org/repo.git",
		},
		{
			in:   "https://token:secret-value@gitlab.com/org/repo.git",
			want: "https://gitlab.com/org/repo.git",
		},
	}
	for _, tc := range cases {
		if got := StripURLCredentials(tc.in); got != tc.want {
			t.Errorf("StripURLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScanReposGitignoredSecretsAndDotEnvFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "work")

	ctx := testContext(t)
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	workRoot := filepath.Join(ctx.HomeDir, "work")
	workCode := filepath.Join(workRoot, "code")
	if err := os.MkdirAll(workCode, 0o755); err != nil {
		t.Fatal(err)
	}
	miseWork := "[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\n"
	if err := os.WriteFile(filepath.Join(workRoot, "mise.toml"), []byte(miseWork), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(workCode, "payments")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".secrets/\n*.env\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, repo, "add", ".gitignore")
	runTestGit(t, ctx.HomeDir, repo, "commit", "-m", "init")

	writeFile := func(rel, data string) {
		t.Helper()
		full := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(".secrets/prod.env", "API_KEY=prod-val\n")
	writeFile(".secrets/shared.env", "SHARED_KEY=shared-val\n")
	writeFile(".secrets/prod.sample.env", "API_KEY=changeme\n")
	writeFile("staging.env", "STAGE_KEY=stage-val\n")
	writeFile("nested/a/b/c/deep.env", "DEEP_KEY=deep-val\n")
	writeFile(".env.local", "LOCAL_KEY=local-val\n")
	writeFile("node_modules/pkg/ignored.env", "SKIP=1\n")

	prodFull := filepath.Join(repo, ".secrets", "prod.env")
	if err := db.PutSecretBlob(store.SecretBlob{
		ID:          "envfile:work/code/payments/.secrets/prod.env",
		Scope:       "envfile",
		Label:       "work/code/payments/.secrets/prod.env",
		SourcePath:  ctx.CollapseHome(prodFull),
		Nonce:       []byte("123456789012"),
		Ciphertext:  []byte("cipher"),
		Fingerprint: "fp:test",
		Perm:        0o600,
	}); err != nil {
		t.Fatalf("PutSecretBlob: %v", err)
	}

	records, err := ScanRepos(ctx, db)
	if err != nil {
		t.Fatalf("ScanRepos: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 repo, got %d: %+v", len(records), records)
	}

	got := map[string]LocalFileEntry{}
	for _, lf := range records[0].UntrackedLocals {
		got[lf.RelPath] = lf
	}
	for _, want := range []string{".env.local", ".secrets/prod.env", ".secrets/shared.env", "nested/a/b/c/deep.env", "staging.env"} {
		entry, ok := got[want]
		if !ok {
			t.Errorf("expected %q in UntrackedLocals, got %+v", want, records[0].UntrackedLocals)
			continue
		}
		if !entry.IsSecret {
			t.Errorf("expected %q marked IsSecret=true, got %+v", want, entry)
		}
	}
	if !got[".secrets/prod.env"].BackedUp {
		t.Errorf("expected .secrets/prod.env BackedUp=true, got %+v", got[".secrets/prod.env"])
	}
	for _, excluded := range []string{".secrets/prod.sample.env", "node_modules/pkg/ignored.env"} {
		if _, ok := got[excluded]; ok {
			t.Errorf("did not expect excluded file %q in UntrackedLocals", excluded)
		}
	}
}

func TestGHConfigDirEnvRespected(t *testing.T) {
	t.Setenv("WS_ZONES", "")
	ctx := testContext(t)

	zroot := filepath.Join(ctx.HomeDir, "work")
	if err := os.MkdirAll(zroot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zroot, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nGH_CONFIG_DIR = \"{{env.HOME}}/.config/gh-work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	envGHDir := filepath.Join(ctx.HomeDir, ".config", "gh-active-override")
	t.Setenv("GH_CONFIG_DIR", envGHDir)
	t.Setenv("WS_ZONE", "")

	var seenDirs []string
	runner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			for _, kv := range env {
				if strings.HasPrefix(kv, "GH_CONFIG_DIR=") {
					seenDirs = append(seenDirs, strings.TrimPrefix(kv, "GH_CONFIG_DIR="))
				}
			}
			if slices.Contains(args, "view") {
				return []byte(`{"number":12,"title":"PR 12","state":"open","author":{"login":"dev"},"url":"https://github.com/acme/api/pull/12","headRefName":"feat/12"}`), nil
			}
			return []byte(`[{"repository":{"nameWithOwner":"acme/api"},"number":12,"title":"PR 12","state":"open","author":{"login":"dev"},"url":"https://github.com/acme/api/pull/12","headRefName":"feat/12","updatedAt":"2026-11-24T12:00:00Z"}]`), nil
		},
	}

	items, err := FetchRepoGitHubItems(ctx, nil, "acme/api", "", "open", true, runner)
	if err != nil || len(items) == 0 || items[0].HeadRef != "feat/12" {
		t.Fatalf("FetchRepoGitHubItems: items=%+v err=%v", items, err)
	}
	if _, err := FetchGHItemDetail(ctx, "pr", "acme/api", "", 12, runner); err != nil {
		t.Fatalf("FetchGHItemDetail: %v", err)
	}
	if _, _, err := FetchGitHubItems(ctx, nil, true, runner); err != nil {
		t.Fatalf("FetchGitHubItems: %v", err)
	}
	if len(seenDirs) < 3 || seenDirs[0] != envGHDir {
		t.Fatalf("expected active GH_CONFIG_DIR=%q used first, got seenDirs=%v", envGHDir, seenDirs)
	}

	seenDirs = nil
	if _, err := FetchRepoGitHubItems(ctx, nil, "acme/api", "work", "open", true, runner); err != nil {
		t.Fatalf("FetchRepoGitHubItems matching zone: %v", err)
	}
	if len(seenDirs) == 0 || !strings.HasSuffix(seenDirs[0], "gh-work") {
		t.Fatalf("expected matching zone gh-work before fallback env, got seenDirs=%v", seenDirs)
	}
}

func TestParseIssueNumberFromBranchAndDetectCWD(t *testing.T) {
	cases := []struct {
		branch string
		want   int
	}{
		{"feat/123-fix-header", 123},
		{"fix/42", 42},
		{"issue-42", 42},
		{"gh-42-auth", 42},
		{"pr-104", 104},
		{"123-desc", 123},
		{"feat/#77-k9s", 77},
		{"release/v1.2.3", 0},
		{"backup-2026-11-24", 0},
		{"main", 0},
		{"master", 0},
	}
	for _, tc := range cases {
		if got := ParseIssueNumberFromBranch(tc.branch); got != tc.want {
			t.Errorf("ParseIssueNumberFromBranch(%q) = %d, want %d", tc.branch, got, tc.want)
		}
	}

	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "feat/42-inline-repo")
	runGit("remote", "add", "origin", "https://github.com/example/demo-app.git")

	cwdCtx := DetectCWDContext(repo)
	if cwdCtx.RepoName != filepath.Base(repo) || cwdCtx.RepoSlug != "example/demo-app" || cwdCtx.Branch != "feat/42-inline-repo" || cwdCtx.IssueHint != 42 {
		t.Fatalf("unexpected DetectCWDContext result: %+v", cwdCtx)
	}
}

func TestGHDetailSQLiteCachingAndHourlyTTL(t *testing.T) {
	if ghCacheTTLSeconds != 3600 {
		t.Fatalf("expected ghCacheTTLSeconds=3600 (1 hour), got %d", ghCacheTTLSeconds)
	}
	if strings.Contains(ghListPRFields, "comments") || strings.Contains(ghListIssueFields, "comments") {
		t.Fatalf("list field sets must omit expensive comments connection: pr=%q issue=%q", ghListPRFields, ghListIssueFields)
	}

	t.Setenv("WS_ZONES", "")
	ctx := testContext(t)
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()

	nowStr := time.Now().UTC().Format(time.RFC3339)
	runner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
				return []byte(fmt.Sprintf(`[{"number":91,"title":"Repo PR","state":"OPEN","author":{"login":"a1"},"url":"https://github.com/acme/api/pull/91","body":"Body","labels":[],"isDraft":false,"headRefName":"feat/cache","updatedAt":%q}]`, nowStr)), nil
			}
			if len(args) >= 2 && args[0] == "issue" && args[1] == "list" {
				return []byte(`[]`), nil
			}
			if len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return []byte(fmt.Sprintf(`{"number":91,"title":"Repo PR","state":"OPEN","author":{"login":"a1"},"url":"https://github.com/acme/api/pull/91","body":"## Cached Markdown","labels":[{"name":"ready"}],"isDraft":false,"headRefName":"feat/cache","baseRefName":"main","reviewDecision":"APPROVED","mergeable":"MERGEABLE","additions":5,"deletions":1,"changedFiles":1,"comments":[{"author":{"login":"rev"},"body":"Cached comment","createdAt":%q}],"statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}],"updatedAt":%q}`, nowStr, nowStr)), nil
			}
			return []byte(`[]`), nil
		},
	}

	if _, err := FetchRepoGitHubItems(ctx, db, "acme/api", "work", "open", true, runner); err != nil {
		t.Fatalf("FetchRepoGitHubItems: %v", err)
	}
	callsBeforeCacheOnly := len(runner.calls)
	cachedItems, fetchedAt, err := LoadCachedRepoGHItems(db, "acme/api", "open")
	if err != nil {
		t.Fatalf("LoadCachedRepoGHItems: %v", err)
	}
	if len(runner.calls) != callsBeforeCacheOnly || len(cachedItems) != 1 || fetchedAt == 0 {
		t.Fatalf("expected SQLite LoadCachedRepoGHItems hit with 0 runner calls, callsDiff=%d len=%d fetchedAt=%d", len(runner.calls)-callsBeforeCacheOnly, len(cachedItems), fetchedAt)
	}

	det1, err := FetchGHItemDetailCached(ctx, db, "pr", "acme/api", "work", 91, false, runner)
	if err != nil {
		t.Fatalf("FetchGHItemDetailCached first call: %v", err)
	}
	if det1.HeadRef != "feat/cache" || len(det1.Comments) != 1 {
		t.Fatalf("unexpected detail: %+v", det1)
	}
	callsAfterFirstDetail := len(runner.calls)

	det2, err := FetchGHItemDetailCached(ctx, db, "pr", "acme/api", "work", 91, false, runner)
	if err != nil {
		t.Fatalf("FetchGHItemDetailCached second call: %v", err)
	}
	if len(runner.calls) != callsAfterFirstDetail || det2.Comments[0].Body != "Cached comment" {
		t.Fatalf("expected second detail call served from gh_detail_cache without runner invocation, callsDiff=%d", len(runner.calls)-callsAfterFirstDetail)
	}

	if _, err := FetchGHItemDetailCached(ctx, db, "pr", "acme/api", "work", 91, true, runner); err != nil {
		t.Fatalf("FetchGHItemDetailCached refresh=true: %v", err)
	}
	if len(runner.calls) != callsAfterFirstDetail+1 {
		t.Fatalf("expected refresh=true to invoke runner once, got diff=%d", len(runner.calls)-callsAfterFirstDetail)
	}
}

func TestPhase4ProjectsHardening(t *testing.T) {
	if !isEnvTemplateFilename("prod.example.env") || !isEnvTemplateFilename("dev.sample.json") {
		t.Fatal("expected middle-segment template names to be detected")
	}

	ctx := testContext(t)
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_CODE_ROOT", "/should/be/ignored/in/isolated/home")
	plan, err := PlanClone(ctx, CloneRequest{Source: "acme/app"})
	if err != nil {
		t.Fatalf("PlanClone isolated home: %v", err)
	}
	wantDir := filepath.Join(ctx.HomeDir, "code", "app")
	if plan.DestDir != wantDir {
		t.Fatalf("DestDir = %q, want %q (host WS_CODE_ROOT should be ignored when HomeDir is isolated)", plan.DestDir, wantDir)
	}

	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("WS_ZONE", "")
	ctx.LocalEnv["GH_CONFIG_DIR"] = filepath.Join(ctx.HomeDir, ".config", "gh-localenv")
	ctx.LocalEnv["WS_ZONE"] = "localzone"
	targets := resolveGHZoneTargets(ctx)
	if len(targets) == 0 || targets[0].zone != "localzone" || !strings.HasSuffix(targets[0].ghConfigDir, "gh-localenv") {
		t.Fatalf("expected LocalEnv GH_CONFIG_DIR target first, got %+v", targets)
	}

	if got := StripURLCredentials("ssh://git@github.com/org/repo.git"); got != "ssh://git@github.com/org/repo.git" {
		t.Fatalf("StripURLCredentials ssh altered username: %q", got)
	}
	if got := StripURLCredentials("codecommit://work-prod@service"); got != "codecommit://work-prod@service" {
		t.Fatalf("StripURLCredentials codecommit altered profile: %q", got)
	}
	if got := StripURLCredentials("codecommit::eu-west-1://work-prod@service"); got != "codecommit::eu-west-1://work-prod@service" {
		t.Fatalf("StripURLCredentials codecommit:: altered profile: %q", got)
	}

	trackedRepo := filepath.Join(ctx.HomeDir, "work", "code", "tracked-env")
	if err := os.MkdirAll(filepath.Join(ctx.HomeDir, "work", "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, "work", "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(trackedRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, trackedRepo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(trackedRepo, ".env"), []byte("PUBLIC=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, ctx.HomeDir, trackedRepo, "add", "-f", ".env")
	runTestGit(t, ctx.HomeDir, trackedRepo, "commit", "-m", "track env")
	if err := os.WriteFile(filepath.Join(trackedRepo, ".env.local"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trackedRecs, err := ScanRepos(ctx, nil)
	if err != nil {
		t.Fatalf("ScanRepos tracked check: %v", err)
	}
	if len(trackedRecs) != 1 || len(trackedRecs[0].UntrackedLocals) != 1 || trackedRecs[0].UntrackedLocals[0].RelPath != ".env.local" {
		t.Fatalf("expected only untracked .env.local (excluding git-tracked .env), got %+v", trackedRecs)
	}

	t.Setenv("WS_FORGES", "")
	t.Setenv("WS_ZONES", "")
	ctx.LocalEnv["WS_FORGES"] = "github|acme-org|~/work/code\ncodecommit|work-prof|~/work/code/cc\n"
	forges, err := ParseForges(ctx)
	if err != nil || len(forges) != 2 {
		t.Fatalf("ParseForges: forges=%+v err=%v", forges, err)
	}
	if z := DeriveZoneForPath(ctx, forges[0].Dest); z != "work" {
		t.Fatalf("DeriveZoneForPath(%s) = %q, want work", forges[0].Dest, z)
	}
	forgeRunner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			if name == "gh" && len(args) >= 2 && args[1] == "list" {
				return []byte(`[{"nameWithOwner":"acme-org/svc-one"}]`), nil
			}
			return []byte("{}"), nil
		},
	}
	listed, err := ListForgeRepos(ctx, forges[0], "work", forgeRunner)
	if err != nil || len(listed) != 1 || listed[0].CloneRef != "acme-org/svc-one" {
		t.Fatalf("ListForgeRepos: listed=%+v err=%v", listed, err)
	}
	bulkRes, err := BulkCloneForgeRepos(ctx, forges[0], "work", listed, forgeRunner)
	if err != nil || len(bulkRes.Cloned) != 1 {
		t.Fatalf("BulkCloneForgeRepos: res=%+v err=%v", bulkRes, err)
	}
}

func TestDiscoverReposFastReadsHeadWithoutGitAndMergesCache(t *testing.T) {
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "")

	ctx := testContext(t)
	ctx.LocalEnv["WS_ZONES"] = "personal|Demo|demo@example.invalid\nwork|Work|work@example.invalid"

	pMise := `WS_ZONE = "personal"` + "\n" + `WS_CODE_ROOT = "{{env.HOME}}/personal/code"` + "\n"
	wMise := `WS_ZONE = "work"` + "\n" + `WS_CODE_ROOT = "{{env.HOME}}/work/code"` + "\n"
	for zone, body := range map[string]string{"personal": pMise, "work": wMise} {
		zdir := filepath.Join(ctx.HomeDir, zone)
		if err := os.MkdirAll(zdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(zdir, "mise.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	repoNormal := filepath.Join(ctx.HomeDir, "personal", "code", "demo-web")
	if err := os.MkdirAll(filepath.Join(repoNormal, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoNormal, ".git", "HEAD"), []byte("ref: refs/heads/feat/instant-gg\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoDetached := filepath.Join(ctx.HomeDir, "work", "code", "billing-api")
	if err := os.MkdirAll(filepath.Join(repoDetached, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDetached, ".git", "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoWorktree := filepath.Join(ctx.HomeDir, "work", "code", "wt-service")
	wtGitDir := filepath.Join(ctx.HomeDir, "work", "code", ".wt-meta", "wt-service")
	if err := os.MkdirAll(repoWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "HEAD"), []byte("ref: refs/heads/fix/worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoWorktree, ".git"), []byte("gitdir: ../.wt-meta/wt-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recs, err := DiscoverReposFast(ctx, nil)
	if err != nil {
		t.Fatalf("DiscoverReposFast(nil): %v", err)
	}
	byName := map[string]RepoRecord{}
	for _, r := range recs {
		byName[r.Name] = r
	}
	if got := byName["demo-web"]; got.Branch != "feat/instant-gg" || got.Zone != "personal" {
		t.Fatalf("expected demo-web branch=feat/instant-gg zone=personal, got %+v", got)
	}
	if got := byName["billing-api"]; got.Branch != "0123456" || got.Zone != "work" {
		t.Fatalf("expected detached billing-api branch=0123456 zone=work, got %+v", got)
	}
	if got := byName["wt-service"]; got.Branch != "fix/worktree" || got.Zone != "work" {
		t.Fatalf("expected worktree wt-service branch=fix/worktree zone=work, got %+v", got)
	}

	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	defer db.Close()
	if err := replaceCachedRepos(db, []RepoRecord{{
		ID:        "~/work/code/billing-api",
		Name:      "billing-api",
		Zone:      "work",
		Forge:     "codecommit",
		RemoteURL: "codecommit://eu-central-1/billing-api",
		LocalPath: repoDetached,
		Branch:    "stale-cached-branch",
		Dirty:     true,
		ScannedAt: 100,
	}}); err != nil {
		t.Fatalf("replaceCachedRepos: %v", err)
	}

	repoBrandNew := filepath.Join(ctx.HomeDir, "personal", "code", "brand-new")
	if err := os.MkdirAll(filepath.Join(repoBrandNew, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoBrandNew, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, err := DiscoverReposFast(ctx, db)
	if err != nil {
		t.Fatalf("DiscoverReposFast(db): %v", err)
	}
	mergedByName := map[string]RepoRecord{}
	for _, r := range merged {
		mergedByName[r.Name] = r
	}
	if got := mergedByName["billing-api"]; !got.Dirty || got.Forge != "codecommit" || got.Branch != "0123456" {
		t.Fatalf("expected billing-api to merge cached Dirty/Forge while keeping live HEAD branch, got %+v", got)
	}
	if got := mergedByName["brand-new"]; got.Branch != "main" || got.LocalPath != repoBrandNew {
		t.Fatalf("expected brand-new uncached repo to be discovered immediately, got %+v", got)
	}
}

func TestGHZoneSwitchingIsolationAndWriteActions(t *testing.T) {
	ctx := testContext(t)
	t.Setenv("WS_ZONES", "work|Worker|work@example.invalid\npersonal|Personal|personal@example.invalid")

	workDir := filepath.Join(ctx.HomeDir, "work")
	persDir := filepath.Join(ctx.HomeDir, "personal")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(persDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workGHDir := filepath.Join(ctx.HomeDir, ".config", "gh-work")
	persGHDir := filepath.Join(ctx.HomeDir, ".config", "gh-personal")
	if err := os.WriteFile(filepath.Join(workDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nGH_CONFIG_DIR = \"{{env.HOME}}/.config/gh-work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(persDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"personal\"\nGH_CONFIG_DIR = \"{{env.HOME}}/.config/gh-personal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_ZONE", "work")
	t.Setenv("GH_CONFIG_DIR", workGHDir)
	t.Setenv("GH_TOKEN", "work-secret-token")
	t.Setenv("GITHUB_TOKEN", "work-github-token")

	var seenEnvs [][]string
	var seenDirs []string
	var calls [][]string
	runner := &fakeCommandRunner{
		handler: func(env []string, dir, name string, args ...string) ([]byte, error) {
			seenEnvs = append(seenEnvs, env)
			seenDirs = append(seenDirs, dir)
			calls = append(calls, args)
			if slices.Contains(args, "list") {
				return []byte(`[{"number":42,"title":"Personal fix","state":"OPEN","author":{"login":"me"},"url":"https://github.com/personal/repo/pull/42","milestone":{"title":"v1.2"},"headRefName":"feat/42","updatedAt":"2026-11-27T10:00:00Z"}]`), nil
			}
			return []byte("ok"), nil
		},
	}

	items, err := FetchRepoGitHubItems(ctx, nil, "personal/repo", "personal", "open", true, runner)
	if err != nil {
		t.Fatalf("FetchRepoGitHubItems(personal): %v", err)
	}
	if len(items) != 2 || items[0].Milestone != "v1.2" {
		t.Fatalf("expected milestone v1.2 parsed for personal PR/issue, got %+v", items)
	}
	for i, env := range seenEnvs {
		var ghDir, ghTok, ghHubTok, wsZone string
		for _, kv := range env {
			switch {
			case strings.HasPrefix(kv, "GH_CONFIG_DIR="):
				ghDir = strings.TrimPrefix(kv, "GH_CONFIG_DIR=")
			case strings.HasPrefix(kv, "GH_TOKEN="):
				ghTok = strings.TrimPrefix(kv, "GH_TOKEN=")
			case strings.HasPrefix(kv, "GITHUB_TOKEN="):
				ghHubTok = strings.TrimPrefix(kv, "GITHUB_TOKEN=")
			case strings.HasPrefix(kv, "WS_ZONE="):
				wsZone = strings.TrimPrefix(kv, "WS_ZONE=")
			}
		}
		if ghDir != persGHDir || wsZone != "personal" {
			t.Fatalf("call %d used wrong zone env: GH_CONFIG_DIR=%q WS_ZONE=%q (want %q / personal)", i, ghDir, wsZone, persGHDir)
		}
		if ghTok != "" || ghHubTok != "" {
			t.Fatalf("call %d leaked work token into personal zone: GH_TOKEN=%q GITHUB_TOKEN=%q", i, ghTok, ghHubTok)
		}
		if filepath.Clean(seenDirs[i]) != filepath.Clean(persDir) {
			t.Fatalf("call %d workdir=%q, want %q", i, seenDirs[i], persDir)
		}
	}

	if _, err := GHApprovePR(ctx, "personal/repo", "personal", 42, "LGTM", runner); err != nil {
		t.Fatalf("GHApprovePR: %v", err)
	}
	if _, err := GHCommentItem(ctx, "pr", "personal/repo", "personal", 42, "Looks clean", runner); err != nil {
		t.Fatalf("GHCommentItem: %v", err)
	}
	if _, err := GHSetItemState(ctx, "issue", "personal/repo", "personal", 7, true, runner); err != nil {
		t.Fatalf("GHSetItemState close: %v", err)
	}
	if _, err := GHMergePR(ctx, "personal/repo", "personal", 42, "squash", runner); err != nil {
		t.Fatalf("GHMergePR: %v", err)
	}
	if len(calls) < 6 {
		t.Fatalf("expected 4 mutation calls recorded, got %d", len(calls))
	}
	dryCtx := *ctx
	dryCtx.DryRun = true
	callsBeforeDry := len(calls)
	msg, err := GHApprovePR(&dryCtx, "personal/repo", "personal", 42, "", runner)
	if err != nil || !strings.Contains(msg, "[dry-run]") || len(calls) != callsBeforeDry {
		t.Fatalf("expected DryRun GHApprovePR to avoid executing runner, got msg=%q err=%v diff=%d", msg, err, len(calls)-callsBeforeDry)
	}
}
