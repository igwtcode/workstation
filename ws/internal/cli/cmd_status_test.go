package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/igwtcode/workstation/ws/internal/store"
)

func seedStatusFixtureRepo(t *testing.T) (repoDir, homeDir, wsHome string) {
	t.Helper()
	base := t.TempDir()
	repoDir = filepath.Join(base, "repo")
	homeDir = filepath.Join(base, "home")
	wsHome = filepath.Join(homeDir, ".ws")

	for _, dir := range []string{
		filepath.Join(repoDir, "themes"),
		filepath.Join(repoDir, "links"),
		filepath.Join(repoDir, "pkg"),
		filepath.Join(repoDir, "config", "demo"),
		homeDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mustWriteStatusFile(t, filepath.Join(repoDir, "themes", "registry"), "config/demo/colors-template.conf config/demo/colors.conf -\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "themes", "gruvbox-dark.palette"), "name=gruvbox-dark\nmode=dark\nbg=#282828\nfg=#ebdbb2\naccent=#fabd2f\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "config", "demo", "colors-template.conf"), "bg={{bg}}\nfg={{fg}}\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "config", "demo", "colors.conf"), "bg=#282828\nfg=#ebdbb2\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "links", "common.links"), "config/demo/colors.conf ~/.config/demo/colors.conf\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "presets.toml"), "[preset.minimal]\ndescription = \"Minimal test preset\"\nlinks = [\"links/common.links\"]\n")
	mustWriteStatusFile(t, filepath.Join(repoDir, "tools.toml"), "")

	return repoDir, homeDir, wsHome
}

func mustWriteStatusFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWSStatusJSONWithSeededCaches(t *testing.T) {
	repoDir, homeDir, wsHome := seedStatusFixtureRepo(t)
	t.Setenv("HOME", homeDir)
	t.Setenv("WS_HOME", wsHome)
	t.Setenv("GH_CONFIG_DIR", filepath.Join(homeDir, ".config", "gh"))
	t.Setenv("PATH", "")

	mustWriteStatusFile(t, filepath.Join(homeDir, ".config", "gh", "hosts.yml"), "github.com:\n    user: me-user\n")
	mustWriteStatusFile(t, filepath.Join(wsHome, "cache", "digest.json"), `{"preset":"minimal","items":[{"name":"ripgrep","provider":"brew","current":"14.0.0","latest":"14.1.0","risk":"low"},{"name":"fd","provider":"brew","current":"9.0.0","latest":"10.0.0","risk":"low"}]}`)

	db, err := store.Open(store.DefaultPath(wsHome))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.SQL().Exec(
		`INSERT INTO repos(id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"r1", "dirty-api", "work", "github", "https://github.com/org/dirty-api.git", filepath.Join(homeDir, "work", "code", "dirty-api"), "main", 1, 0, 2, "[]", now,
		"r2", "behind-web", "personal", "github", "https://github.com/me/behind-web.git", filepath.Join(homeDir, "personal", "code", "behind-web"), "main", 0, 0, 3, "[]", now,
		"r3", "clean-cli", "personal", "github", "https://github.com/me/clean-cli.git", filepath.Join(homeDir, "personal", "code", "clean-cli"), "main", 0, 1, 0, "[]", now,
	); err != nil {
		t.Fatalf("seed repos: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO gh_cache(kind, repo, number, title, state, author, url, zone, updated_at, fetched_at, body, labels_json, assignees_json, is_draft, comments_count, created_at, source, head_ref, milestone)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '[]', 0, 0, ?, 'involved', 'feat/a', ''),
		        (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '[]', 0, 0, ?, 'involved', 'feat/b', ''),
		        (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '[]', '[]', 1, 0, ?, 'involved', 'feat/c', '')`,
		"pr", "org/dirty-api", 42, "Review needed PR", "open", "teammate", "https://github.com/org/dirty-api/pull/42", "work", now, now, now,
		"pr", "org/dirty-api", 43, "My own PR", "open", "me-user", "https://github.com/org/dirty-api/pull/43", "work", now, now, now,
		"pr", "org/dirty-api", 44, "Draft PR", "open", "teammate", "https://github.com/org/dirty-api/pull/44", "work", now, now, now,
	); err != nil {
		t.Fatalf("seed gh_cache: %v", err)
	}
	if err := db.SetMeta("gh_cache_format", "2"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta("gh_cache_fetched_at", strconv.FormatInt(now, 10)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta("last_backup_at", strconv.FormatInt(now-3*3600, 10)); err != nil {
		t.Fatal(err)
	}
	hash, err := db.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta("last_backup_hash", hash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	opts := &GlobalOpts{
		Repo:           repoDir,
		Home:           homeDir,
		Preset:         "minimal",
		NonInteractive: true,
	}
	cmd := NewRootCmd(opts)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"status", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ws status --json failed: %v\noutput:\n%s", err, stdout.String())
	}

	var rep StatusReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal status JSON: %v\nraw:\n%s", err, stdout.String())
	}

	if !rep.Outdated.Available || rep.Outdated.Source != "cache" || rep.Outdated.Count != 2 {
		t.Fatalf("unexpected outdated section: %+v", rep.Outdated)
	}
	if !rep.Doctor.Available || rep.Doctor.LinksTotal != 1 || rep.Doctor.TotalCount < 1 {
		t.Fatalf("unexpected doctor section: %+v", rep.Doctor)
	}
	if !rep.Repos.Available || rep.Repos.TotalCount != 3 || rep.Repos.DirtyCount != 1 || rep.Repos.BehindCount != 2 || rep.Repos.AttentionCount != 2 {
		t.Fatalf("unexpected repos section: %+v", rep.Repos)
	}
	if !rep.ReviewPRs.Available || rep.ReviewPRs.Count != 1 || len(rep.ReviewPRs.Items) != 1 || rep.ReviewPRs.Items[0].Number != 42 {
		t.Fatalf("unexpected reviewPRs section: %+v", rep.ReviewPRs)
	}
	if !rep.Untracked.Available || rep.Untracked.Count != 0 {
		t.Fatalf("unexpected untracked section: %+v", rep.Untracked)
	}
	if !rep.Backup.Available || rep.Backup.DirtySinceBackup || !strings.Contains(rep.Backup.Age, "3h ago") {
		t.Fatalf("unexpected backup section: %+v", rep.Backup)
	}
}

func TestWSStatusGracefulDegradationAndReadOnly(t *testing.T) {
	repoDir, homeDir, wsHome := seedStatusFixtureRepo(t)
	t.Setenv("HOME", homeDir)
	t.Setenv("WS_HOME", wsHome)
	t.Setenv("GH_CONFIG_DIR", filepath.Join(homeDir, ".config", "gh-missing"))
	t.Setenv("PATH", "")

	var jsonOut bytes.Buffer
	cmd := NewRootCmd(&GlobalOpts{
		Repo:           repoDir,
		Home:           homeDir,
		Preset:         "minimal",
		NonInteractive: true,
	})
	cmd.SetOut(&jsonOut)
	cmd.SetErr(&jsonOut)
	cmd.SetArgs([]string{"status", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ws status --json failed in empty/offline env: %v\n%s", err, jsonOut.String())
	}

	var rep StatusReport
	if err := json.Unmarshal(jsonOut.Bytes(), &rep); err != nil {
		t.Fatalf("invalid JSON from degraded ws status: %v\n%s", err, jsonOut.String())
	}
	if rep.Outdated.Available || rep.ReviewPRs.Available || rep.Backup.Available {
		t.Fatalf("expected missing cache/gh/db sections to degrade gracefully: outdated=%+v prs=%+v backup=%+v", rep.Outdated, rep.ReviewPRs, rep.Backup)
	}
	if !rep.Doctor.Available {
		t.Fatalf("expected doctor section available offline: %+v", rep.Doctor)
	}
	if _, err := os.Stat(store.DefaultPath(wsHome)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only ws status must not create ws.db (stat err=%v)", err)
	}

	var humanOut bytes.Buffer
	cmdHuman := NewRootCmd(&GlobalOpts{
		Repo:           repoDir,
		Home:           homeDir,
		Preset:         "minimal",
		NonInteractive: true,
	})
	cmdHuman.SetOut(&humanOut)
	cmdHuman.SetErr(&humanOut)
	cmdHuman.SetArgs([]string{"status"})
	if err := cmdHuman.Execute(); err != nil {
		t.Fatalf("ws status human output failed: %v", err)
	}
	text := humanOut.String()
	for _, want := range []string{"Outdated", "Doctor", "Repos", "Review PRs", "Untracked", "Last Backup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("human ws status missing %q:\n%s", want, text)
		}
	}
}

func TestRunCommandStreamingLiveAndProcessGroupKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var seenLines []string
	gotChildPID := make(chan int, 1)

	script := "sleep 60 &\nchild=$!\necho CHILD:$child\necho STEP:running\nwait $child\n"
	c := exec.CommandContext(ctx, "sh", "-c", script)

	done := make(chan struct{})
	var exitCode int
	var runErr error
	go func() {
		defer close(done)
		_, exitCode, runErr = runCommandStreaming(ctx, c, func(ln string) {
			mu.Lock()
			seenLines = append(seenLines, ln)
			mu.Unlock()
			if strings.HasPrefix(ln, "CHILD:") {
				if pid, err := strconv.Atoi(strings.TrimPrefix(ln, "CHILD:")); err == nil {
					select {
					case gotChildPID <- pid:
					default:
					}
				}
			}
		})
	}()

	var childPID int
	select {
	case childPID = <-gotChildPID:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for streamed CHILD pid line")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("runCommandStreaming did not exit promptly after cancel")
	}

	if !errors.Is(runErr, context.Canceled) || exitCode != 130 {
		t.Fatalf("expected context.Canceled and exit 130, got err=%v code=%d", runErr, exitCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := syscall.Kill(childPID, 0); err == nil {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("expected grandchild process %d in process group to be killed on cancel", childPID)
	}

	mu.Lock()
	lines := append([]string(nil), seenLines...)
	mu.Unlock()
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "CHILD:") {
		t.Fatalf("expected live streamed lines before cancel, got %v", lines)
	}
}
