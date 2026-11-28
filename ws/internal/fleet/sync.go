package fleet

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/store"
)

type SyncSpec struct {
	Node           string   `json:"node"`
	Transport      string   `json:"transport"`
	TargetID       string   `json:"targetId,omitempty"`
	Preset         string   `json:"preset"`
	Arch           string   `json:"arch"`
	BinaryPath     string   `json:"binaryPath,omitempty"`
	NoZones        bool     `json:"noZones,omitempty"`
	PushEnvFiles   bool     `json:"pushEnvFiles,omitempty"`
	ExcludePaths   []string `json:"excludePaths,omitempty"`
	SelectedRepos  []string `json:"selectedRepos,omitempty"`
	CodeCommitHost bool     `json:"codeCommitHost,omitempty"`
}

type SyncStep struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Detail  string   `json:"detail"`
	Command []string `json:"command,omitempty"`
}

type ChecklistItem struct {
	Done    bool   `json:"done"`
	Title   string `json:"title"`
	Command string `json:"command"`
}

type SyncPlan struct {
	Spec            SyncSpec        `json:"spec"`
	Steps           []SyncStep      `json:"steps"`
	EnvBlobIDs      []string        `json:"envBlobIds,omitempty"`
	EnvFiles        []string        `json:"envFiles,omitempty"`
	EnvFilesCount   int             `json:"envFilesCount"`
	SkippedConfigs  []string        `json:"skippedConfigs,omitempty"`
	ManualChecklist []ChecklistItem `json:"manualChecklist,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"`
}

type ExecRunner interface {
	Run(stdin io.Reader, name string, args ...string) (stdout, stderr []byte, err error)
}

const (
	fleetProbeTimeout      = 15 * time.Second
	fleetExecTimeout       = 2 * time.Minute
	fleetApplyTimeout      = 10 * time.Minute
	maxRunnerStderrCapture = 64 * 1024
	maxRunnerErrorStderr   = 2048
)

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if rem := b.max - b.buf.Len(); rem > 0 {
		if len(p) > rem {
			b.buf.Write(p[:rem])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func trimBoundedStderr(stderr []byte) string {
	trimmed := strings.TrimSpace(string(stderr))
	if len(trimmed) <= maxRunnerErrorStderr {
		return trimmed
	}
	cut := maxRunnerErrorStderr
	for cut > 0 && !utf8.RuneStart(trimmed[cut]) {
		cut--
	}
	return strings.TrimSpace(trimmed[:cut]) + "... (truncated)"
}

func formatStepExecError(stepID string, err error, stderr []byte) error {
	if detail := trimBoundedStderr(stderr); detail != "" {
		return fmt.Errorf("step %s failed: %w (%s)", stepID, err, detail)
	}
	return fmt.Errorf("step %s failed: %w", stepID, err)
}

type osExecRunner struct{}

func (osExecRunner) Run(stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
	timeout := fleetExecTimeout
	if name == "tailscale" {
		timeout = 30 * time.Second
	} else if name == "ssh" && len(args) > 0 && strings.Contains(args[len(args)-1], "/.local/bin/ws") && strings.Contains(args[len(args)-1], " apply ") {
		timeout = fleetApplyTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout bytes.Buffer
	stderr := limitedBuffer{max: maxRunnerStderrCapture}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stdout.Bytes(), stderr.buf.Bytes(), fmt.Errorf("%s timed out after %s: %w", name, timeout, ctx.Err())
	}
	return stdout.Bytes(), stderr.buf.Bytes(), err
}

func DefaultVMExcludes() []string {
	return []string{
		"ghostty",
		"herdr-app",
		"hypr",
		"kitty",
		"niri",
		"noctalia",
		"vscode",
		"zed",
	}
}

type cachedRepoMeta struct {
	ID        string
	Name      string
	Zone      string
	Forge     string
	RemoteURL string
	LocalPath string
}

func buildFleetSSHArgs(connectTimeoutSec int, target, remoteScript string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if connectTimeoutSec > 0 {
		args = append(args, "-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeoutSec))
	}
	args = append(args, "--", target, remoteScript)
	return args
}

func PlanFleetSync(ctx *app.Context, db *store.DB, kr *store.Keyring, spec SyncSpec) (*SyncPlan, error) {
	norm, err := normalizeAndValidateSyncSpec(ctx, spec)
	if err != nil {
		return nil, err
	}

	plan := &SyncPlan{
		Spec:           norm,
		SkippedConfigs: append([]string(nil), norm.ExcludePaths...),
	}

	wrapRemote := func(_, script string) ([]string, error) {
		return append([]string{"ssh"}, buildFleetSSHArgs(0, norm.TargetID, script)...), nil
	}

	binScript := "mkdir -p \"$HOME/.local/bin\" && umask 022 && tmp=\"$HOME/.local/bin/ws.tmp.$$\" && trap 'rm -f \"$tmp\"' EXIT && cat > \"$tmp\" && chmod 0755 \"$tmp\" && mv -f \"$tmp\" \"$HOME/.local/bin/ws\""
	binCmd, err := wrapRemote("ws push binary", binScript)
	if err != nil {
		return nil, err
	}
	plan.Steps = append(plan.Steps, SyncStep{
		ID:      "push-binary",
		Title:   fmt.Sprintf("Push static ws binary (%s)", norm.Arch),
		Detail:  fmt.Sprintf("Install %s to ~/.local/bin/ws on %s", norm.BinaryPath, norm.Node),
		Command: binCmd,
	})

	excludeCSV := strings.Join(norm.ExcludePaths, ",")
	var applyScript string
	if wsOrigin := detectWorkstationOriginURL(ctx); wsOrigin != "" {
		applyScript = fmt.Sprintf(
			"if [ -d \"$HOME/workstation/.git\" ]; then git -C \"$HOME/workstation\" pull --ff-only; else git clone -- %s \"$HOME/workstation\"; fi && WS_EXCLUDE_PATHS=%s \"$HOME/.local/bin/ws\" apply --repo \"$HOME/workstation\" --non-interactive --preset %s --allow replace",
			shellSingleQuote(wsOrigin),
			shellSingleQuote(excludeCSV),
			shellSingleQuote(norm.Preset),
		)
	} else {
		applyScript = fmt.Sprintf(
			"mkdir -p \"$HOME/workstation\" && WS_EXCLUDE_PATHS=%s \"$HOME/.local/bin/ws\" apply --non-interactive --preset %s --allow replace",
			shellSingleQuote(excludeCSV),
			shellSingleQuote(norm.Preset),
		)
		plan.Warnings = append(plan.Warnings, "could not detect workstation repository origin URL; apply assumes ~/workstation is already present on target")
	}
	applyCmd, err := wrapRemote("ws apply workstation", applyScript)
	if err != nil {
		return nil, err
	}
	plan.Steps = append(plan.Steps, SyncStep{
		ID:      "apply-workstation",
		Title:   fmt.Sprintf("Apply preset %s", norm.Preset),
		Detail:  fmt.Sprintf("Run ws apply --non-interactive --preset %s --allow replace (skipping %d GUI/macOS configs)", norm.Preset, len(norm.ExcludePaths)),
		Command: applyCmd,
	})

	if norm.NoZones {
		dirs := []string{`"$HOME/code"`, `"$HOME/.codex"`, `"$HOME/.local/share/gh"`, `"$HOME/.config/claude"`}
		layoutScript := "mkdir -p " + strings.Join(dirs, " ")
		layoutCmd, err := wrapRemote("ws flat layout", layoutScript)
		if err != nil {
			return nil, err
		}
		plan.Steps = append(plan.Steps, SyncStep{
			ID:      "layout-no-zones",
			Title:   "Create flat ~/code layout",
			Detail:  "Initialize ~/code, ~/.config/claude, ~/.codex, and ~/.local/share/gh without zone hierarchy",
			Command: layoutCmd,
		})
	}

	repoCatalog := loadRepoCatalog(db)
	hasCodeCommit := norm.CodeCommitHost
	if len(norm.SelectedRepos) > 0 {
		var cloneLines []string
		seenCloneDest := map[string]string{}
		for _, sel := range norm.SelectedRepos {
			meta, ok, ambigErr := lookupSelectedRepoStrict(sel, repoCatalog)
			if ambigErr != nil {
				return nil, ambigErr
			}
			if !ok {
				if looksLikeRemoteURL(sel) {
					name := safeBaseRepoName(sel)
					if name == "" {
						plan.Warnings = append(plan.Warnings, fmt.Sprintf("could not derive directory name for repo %q", sel))
						continue
					}
					meta = cachedRepoMeta{Name: name, RemoteURL: projects.StripURLCredentials(sel)}
					if strings.Contains(strings.ToLower(sel), "codecommit") {
						meta.Forge = "codecommit"
					}
				} else {
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("selected repo %q not found in ws.db repos cache", sel))
					continue
				}
			}
			meta.RemoteURL = projects.StripURLCredentials(meta.RemoteURL)
			if meta.Forge == "codecommit" {
				hasCodeCommit = true
			}
			if meta.RemoteURL == "" {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("repo %q has no origin remote URL; skipping clone", meta.Name))
				continue
			}
			if !isAllowedRemoteURL(meta.RemoteURL) {
				return nil, fmt.Errorf("disallowed remote URL scheme for repo %q: %q", meta.Name, meta.RemoteURL)
			}
			if !isSafeRelSegment(meta.Name) {
				return nil, fmt.Errorf("unsafe repo directory name %q", meta.Name)
			}
			if meta.Zone != "" && !isSafeRelSegment(meta.Zone) {
				return nil, fmt.Errorf("unsafe zone directory name %q", meta.Zone)
			}
			destSub := "code/" + meta.Name
			if !norm.NoZones && meta.Zone != "" {
				destSub = meta.Zone + "/code/" + meta.Name
			}
			if prev, dup := seenCloneDest[destSub]; dup {
				return nil, fmt.Errorf("duplicate remote clone destination %q (%s and %s); omit --no-zones or disambiguate --repos", destSub, prev, meta.Name)
			}
			seenCloneDest[destSub] = meta.Name
			cloneLines = append(cloneLines, fmt.Sprintf(
				"{ mkdir -p \"$HOME\"/%s && { [ -d \"$HOME\"/%s ] || git clone -- %s \"$HOME\"/%s; }; }",
				shellSingleQuote(path.Dir(destSub)),
				shellSingleQuote(destSub),
				shellSingleQuote(meta.RemoteURL),
				shellSingleQuote(destSub),
			))
		}
		if len(cloneLines) > 0 {
			cloneCmd, err := wrapRemote("ws clone repos", strings.Join(cloneLines, " && "))
			if err != nil {
				return nil, err
			}
			plan.Steps = append(plan.Steps, SyncStep{
				ID:      "clone-repos",
				Title:   fmt.Sprintf("Clone %d selected repositories", len(cloneLines)),
				Detail:  "Idempotently clone selected repositories from ws.db catalog",
				Command: cloneCmd,
			})
		}
	}

	if norm.PushEnvFiles {
		if db == nil {
			plan.Warnings = append(plan.Warnings, "ws.db is not open; cannot inspect encrypted .env blobs")
		} else {
			blobs, berr := db.ListSecretBlobs("envfile")
			if berr != nil {
				return nil, berr
			}
			seenEnvDest := map[string]string{}
			for _, b := range blobs {
				matched, merr := matchesSelectedReposStrict(ctx, b, norm.SelectedRepos, repoCatalog)
				if merr != nil {
					return nil, merr
				}
				if !matched {
					continue
				}
				relDest, rerr := resolveRemoteEnvRelPath(ctx, b.SourcePath, norm.NoZones)
				if rerr != nil {
					return nil, fmt.Errorf("unsafe remote path for blob %s: %w", b.ID, rerr)
				}
				if !store.IsWritableEnvFilename(path.Base(relDest)) {
					return nil, fmt.Errorf("refusing non-writable remote env filename %s for blob %s", relDest, b.ID)
				}
				if prev, dup := seenEnvDest[relDest]; dup {
					return nil, fmt.Errorf("multiple selected .env files map to remote ~/%s (%s and %s); omit --no-zones or select distinct repos", relDest, prev, b.SourcePath)
				}
				seenEnvDest[relDest] = b.SourcePath
				plan.EnvBlobIDs = append(plan.EnvBlobIDs, b.ID)
				plan.EnvFiles = append(plan.EnvFiles, fmt.Sprintf("%s -> ~/%s", b.SourcePath, relDest))
			}
			plan.EnvFilesCount = len(plan.EnvBlobIDs)
			if plan.EnvFilesCount > 0 {
				if kr == nil || !kr.HasKey() {
					plan.Warnings = append(plan.Warnings, "database encryption key is locked; unlock key before executing --push-env")
				}
				previewCmd := append([]string{"ssh"}, buildFleetSSHArgs(0, norm.TargetID, "umask 077 && base64 -d > <target>.ws-tmp && chmod 0600 <target>.ws-tmp && mv -f <target>.ws-tmp <target>")...)
				plan.Steps = append(plan.Steps, SyncStep{
					ID:      "push-env-files",
					Title:   fmt.Sprintf("Push %d decrypted .env file(s)", plan.EnvFilesCount),
					Detail:  "Stream via SSH stdin (umask 077, 0600): " + strings.Join(plan.EnvFiles, ", "),
					Command: previewCmd,
				})
			}
		}
	}

	plan.ManualChecklist = buildManualChecklist(norm, hasCodeCommit)

	return plan, nil
}

func ExecFleetSync(ctx *app.Context, db *store.DB, kr *store.Keyring, plan *SyncPlan, runner ExecRunner) error {
	if plan == nil {
		return fmt.Errorf("sync plan is required")
	}
	if ctx != nil && ctx.DryRun {
		return nil
	}
	if runner == nil {
		runner = osExecRunner{}
	}

	for _, step := range plan.Steps {
		switch step.ID {
		case "push-binary":
			if len(step.Command) == 0 {
				continue
			}
			var stdin io.Reader
			if plan.Spec.Transport == "tailscale-ssh" && plan.Spec.BinaryPath != "" {
				raw, err := os.ReadFile(plan.Spec.BinaryPath)
				if err != nil {
					return fmt.Errorf("read binary %s: %w", plan.Spec.BinaryPath, err)
				}
				stdin = bytes.NewReader(raw)
			}
			_, stderr, err := runner.Run(stdin, step.Command[0], step.Command[1:]...)
			if err != nil {
				return formatStepExecError(step.ID, err, stderr)
			}

		case "push-env-files":
			if err := execPushEnvFiles(ctx, db, kr, plan, runner); err != nil {
				return err
			}

		default:
			if len(step.Command) == 0 {
				continue
			}
			_, stderr, err := runner.Run(nil, step.Command[0], step.Command[1:]...)
			if err != nil {
				return formatStepExecError(step.ID, err, stderr)
			}
		}
	}

	if db != nil {
		if err := saveFleetNodePlan(db, plan, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}

func execPushEnvFiles(ctx *app.Context, db *store.DB, kr *store.Keyring, plan *SyncPlan, runner ExecRunner) error {
	if len(plan.EnvBlobIDs) == 0 {
		return nil
	}
	if db == nil {
		return fmt.Errorf("open ws.db is required to push .env files")
	}
	if kr == nil || !kr.HasKey() {
		return fmt.Errorf("database encryption key is required to decrypt .env blobs")
	}

	for _, blobID := range plan.EnvBlobIDs {
		blob, err := db.GetSecretBlob(blobID)
		if err != nil {
			return fmt.Errorf("load secret blob %s: %w", blobID, err)
		}
		if blob == nil {
			return fmt.Errorf("secret blob %s not found", blobID)
		}
		plain, err := store.DecryptSecretBlob(kr, *blob)
		if err != nil {
			return fmt.Errorf("decrypt secret blob %s: %w", blobID, err)
		}
		relDest, err := resolveRemoteEnvRelPath(ctx, blob.SourcePath, plan.Spec.NoZones)
		if err != nil {
			return fmt.Errorf("unsafe remote path for blob %s: %w", blobID, err)
		}
		if !store.IsWritableEnvFilename(path.Base(relDest)) {
			return fmt.Errorf("refusing non-writable remote env filename %s for blob %s", relDest, blobID)
		}

		b64 := base64.StdEncoding.EncodeToString(plain)
		replaceGuard := ""
		if ctx == nil || !ctx.Allow.Replace {
			replaceGuard = "if [ -e \"$target\" ]; then echo \"remote env target exists (pass --allow replace to overwrite)\" >&2; exit 1; fi && "
		}

		remoteScript := fmt.Sprintf(
			"umask 077 && %s && target=\"$HOME\"/%s && mkdir -p \"$(dirname \"$target\")\" && [ ! -L \"$target\" ] && %stmp=$(mktemp \"$target.ws-tmp.XXXXXX\") && trap 'rm -f \"$tmp\"' EXIT && base64 -d > \"$tmp\" && chmod 0600 \"$tmp\" && mv -f \"$tmp\" \"$target\"",
			remoteSafePathGuard(relDest),
			shellSingleQuote(relDest),
			replaceGuard,
		)
		args := buildFleetSSHArgs(0, plan.Spec.TargetID, remoteScript)
		if _, stderr, err := runner.Run(strings.NewReader(b64+"\n"), "ssh", args...); err != nil {
			if detail := trimBoundedStderr(stderr); detail != "" {
				return fmt.Errorf("push env blob %s via ssh: %w (%s)", blobID, err, detail)
			}
			return fmt.Errorf("push env blob %s via ssh: %w", blobID, err)
		}
	}
	return nil
}

func resolveRemoteEnvRelPath(ctx *app.Context, sourcePath string, noZones bool) (string, error) {
	raw := strings.TrimSpace(sourcePath)
	if raw == "" || strings.ContainsAny(raw, "\n\r\x00") {
		return "", fmt.Errorf("empty or unsafe source path")
	}

	rel := filepath.ToSlash(raw)
	if strings.HasPrefix(rel, "~/") {
		rel = strings.TrimPrefix(rel, "~/")
	} else if ctx != nil && ctx.HomeDir != "" {
		homeSlash := strings.TrimRight(filepath.ToSlash(filepath.Clean(ctx.HomeDir)), "/")
		if strings.HasPrefix(rel, homeSlash+"/") {
			rel = strings.TrimPrefix(rel, homeSlash+"/")
		}
	}

	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path %q is outside home directory", sourcePath)
	}

	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("path traversal rejected: %q", sourcePath)
	}

	if noZones {
		parts := strings.Split(clean, "/")
		if len(parts) >= 3 && parts[1] == "code" {
			clean = path.Join(parts[1:]...)
		}
	}
	return clean, nil
}

func normalizeAndValidateSyncSpec(ctx *app.Context, spec SyncSpec) (SyncSpec, error) {
	out := spec
	out.Node = strings.TrimSpace(out.Node)
	out.Transport = strings.ToLower(strings.TrimSpace(out.Transport))
	out.TargetID = strings.TrimSpace(out.TargetID)
	out.Preset = strings.TrimSpace(out.Preset)
	out.Arch = strings.ToLower(strings.TrimSpace(out.Arch))
	out.BinaryPath = strings.TrimSpace(out.BinaryPath)

	if err := ValidateNodeTarget(out.Node); err != nil {
		return SyncSpec{}, err
	}
	if out.Transport == "" {
		out.Transport = "tailscale-ssh"
	}
	switch out.Transport {
	case "tailscale-ssh":
		if out.TargetID == "" {
			out.TargetID = out.Node
		}
		if err := ValidateNodeTarget(out.TargetID); err != nil {
			return SyncSpec{}, err
		}
	default:
		return SyncSpec{}, fmt.Errorf("unsupported transport %q (expected tailscale-ssh)", out.Transport)
	}

	if out.PushEnvFiles {
		if len(out.SelectedRepos) == 0 {
			return SyncSpec{}, fmt.Errorf("--push-env requires --repos to specify which repository .env files to push")
		}
	}

	if out.Preset == "" {
		out.Preset = "ubuntu-runner"
	}
	if err := ValidatePresetID(out.Preset); err != nil {
		return SyncSpec{}, err
	}

	if out.Arch == "" {
		out.Arch = "amd64"
	}
	if out.Arch != "amd64" && out.Arch != "arm64" {
		return SyncSpec{}, fmt.Errorf("unsupported arch %q (expected amd64 or arm64)", out.Arch)
	}

	if out.BinaryPath == "" {
		out.BinaryPath = "ws-linux-" + out.Arch
		if ctx != nil && strings.TrimSpace(ctx.RepoRoot) != "" {
			distBin := filepath.Join(ctx.RepoRoot, "ws", "dist", "ws-linux-"+out.Arch)
			if fi, err := os.Stat(distBin); err == nil && !fi.IsDir() {
				out.BinaryPath = distBin
			}
		}
	} else if strings.ContainsAny(out.BinaryPath, "\n\r\x00") || strings.HasPrefix(out.BinaryPath, "-") {
		return SyncSpec{}, fmt.Errorf("invalid binary path %q", out.BinaryPath)
	}

	if len(out.ExcludePaths) == 0 {
		out.ExcludePaths = DefaultVMExcludes()
	} else {
		cleaned := make([]string, 0, len(out.ExcludePaths))
		for _, ex := range out.ExcludePaths {
			ex = strings.TrimSpace(ex)
			if ex == "" {
				continue
			}
			if strings.ContainsAny(ex, "\n\r\x00,") || strings.Contains(ex, "..") || strings.HasPrefix(ex, "-") {
				return SyncSpec{}, fmt.Errorf("invalid exclude path %q", ex)
			}
			cleaned = append(cleaned, ex)
		}
		out.ExcludePaths = cleaned
	}

	if len(out.SelectedRepos) > 0 {
		repos := make([]string, 0, len(out.SelectedRepos))
		for _, r := range out.SelectedRepos {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if strings.ContainsAny(r, " \t\n\r\x00") || strings.HasPrefix(r, "-") || strings.Contains(r, "..") {
				return SyncSpec{}, fmt.Errorf("invalid selected repo %q", r)
			}
			repos = append(repos, r)
		}
		out.SelectedRepos = repos
	}

	return out, nil
}

func buildManualChecklist(_ SyncSpec, hasCodeCommit bool) []ChecklistItem {
	items := []ChecklistItem{
		{
			Title:   "Authenticate Claude CLI",
			Command: "claude auth login",
		},
	}

	items = append(items, ChecklistItem{
		Title:   "Authenticate GitHub CLI",
		Command: "gh auth login",
	})

	if hasCodeCommit {
		items = append(items, ChecklistItem{
			Title:   "Authenticate AWS SSO for CodeCommit",
			Command: "aws sso login",
		})
	}

	items = append(items, ChecklistItem{
		Title:   "Verify Tailscale status",
		Command: "tailscale status",
	})
	return items
}

func loadRepoCatalog(db *store.DB) []cachedRepoMeta {
	if db == nil || db.SQL() == nil {
		return nil
	}
	rows, err := db.SQL().Query(`SELECT id, name, zone, forge, remote_url, local_path FROM repos ORDER BY zone ASC, name ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []cachedRepoMeta
	for rows.Next() {
		var m cachedRepoMeta
		if err := rows.Scan(&m.ID, &m.Name, &m.Zone, &m.Forge, &m.RemoteURL, &m.LocalPath); err != nil {
			return out
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

func lookupSelectedRepoStrict(sel string, catalog []cachedRepoMeta) (cachedRepoMeta, bool, error) {
	var exact []cachedRepoMeta
	var byName []cachedRepoMeta
	for _, r := range catalog {
		if r.ID == sel || r.RemoteURL == sel || (r.Zone != "" && r.Zone+"/"+r.Name == sel) {
			exact = append(exact, r)
			continue
		}
		if r.Name == sel {
			byName = append(byName, r)
		}
	}
	if len(exact) == 1 {
		return exact[0], true, nil
	}
	if len(exact) > 1 {
		return cachedRepoMeta{}, false, fmt.Errorf("selected repo %q matches %d catalog entries; disambiguate with zone/name", sel, len(exact))
	}
	if len(byName) == 1 {
		return byName[0], true, nil
	}
	if len(byName) > 1 {
		zones := make([]string, 0, len(byName))
		for _, m := range byName {
			if m.Zone != "" {
				zones = append(zones, m.Zone+"/"+m.Name)
			} else {
				zones = append(zones, m.ID)
			}
		}
		return cachedRepoMeta{}, false, fmt.Errorf("selected repo %q is ambiguous across zones (%s); specify zone/name", sel, strings.Join(zones, ", "))
	}
	return cachedRepoMeta{}, false, nil
}

func detectWorkstationOriginURL(ctx *app.Context) string {
	if ctx == nil || strings.TrimSpace(ctx.RepoRoot) == "" {
		return ""
	}
	cmdCtx, cancel := context.WithTimeout(context.Background(), fleetProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", "-C", ctx.RepoRoot, "remote", "get-url", "origin")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	u := projects.StripURLCredentials(string(out))
	if u == "" || strings.ContainsAny(u, "\n\r\x00") {
		return ""
	}
	return u
}

func matchesSelectedReposStrict(ctx *app.Context, blob store.SecretBlob, selected []string, catalog []cachedRepoMeta) (bool, error) {
	if len(selected) == 0 {
		return false, nil
	}
	srcNorm := filepath.ToSlash(blob.SourcePath)
	expanded := srcNorm
	if ctx != nil {
		expanded = filepath.ToSlash(filepath.Clean(ctx.ExpandHome(blob.SourcePath)))
	}
	for _, sel := range selected {
		meta, ok, err := lookupSelectedRepoStrict(sel, catalog)
		if err != nil {
			return false, err
		}
		if ok && meta.LocalPath != "" {
			repoSlash := filepath.ToSlash(filepath.Clean(meta.LocalPath))
			if strings.HasPrefix(expanded, repoSlash+"/") {
				return true, nil
			}
			if ctx != nil {
				collapsed := filepath.ToSlash(ctx.CollapseHome(meta.LocalPath))
				if strings.HasPrefix(srcNorm, collapsed+"/") {
					return true, nil
				}
			}
		}
		if strings.Contains(sel, "/") || strings.HasPrefix(sel, "~") {
			selClean := filepath.ToSlash(filepath.Clean(sel))
			if strings.HasPrefix(srcNorm, selClean+"/") || strings.HasPrefix(expanded, selClean+"/") {
				return true, nil
			}
			if ctx != nil {
				selExp := filepath.ToSlash(filepath.Clean(ctx.ExpandHome(sel)))
				if strings.HasPrefix(expanded, selExp+"/") {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func isAllowedRemoteURL(raw string) bool {
	u := strings.TrimSpace(raw)
	if u == "" || strings.HasPrefix(u, "-") || strings.ContainsAny(u, " \t\n\r\x00") {
		return false
	}
	lower := strings.ToLower(u)
	for _, prefix := range []string{"https://", "http://", "ssh://", "git@", "codecommit://", "codecommit::"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isSafeRelSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\n\r\x00") && !strings.Contains(s, "..") && !strings.HasPrefix(s, "-")
}

func looksLikeRemoteURL(s string) bool {
	return strings.Contains(s, "://") || strings.Contains(s, "@")
}

func safeBaseRepoName(s string) string {
	trimmed := strings.TrimRight(s, "/")
	if idx := strings.LastIndexAny(trimmed, "/:"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	if at := strings.LastIndexByte(trimmed, '@'); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if !safeTokenArgRe.MatchString(trimmed) || strings.Contains(trimmed, "..") {
		return ""
	}
	return trimmed
}

func saveFleetNodePlan(db *store.DB, plan *SyncPlan, syncedAt int64) error {
	if db == nil || db.SQL() == nil {
		return fmt.Errorf("database is not open")
	}
	spec := plan.Spec
	provider := "tailscale"
	noZonesInt := 0
	if spec.NoZones {
		noZonesInt = 1
	}

	excJSON, err := marshalSliceJSON(spec.ExcludePaths)
	if err != nil {
		return err
	}
	repoJSON, err := marshalSliceJSON(spec.SelectedRepos)
	if err != nil {
		return err
	}
	aiJSON := "[]"
	chk := plan.ManualChecklist
	if chk == nil {
		chk = []ChecklistItem{}
	}
	chkBytes, err := json.Marshal(chk)
	if err != nil {
		return err
	}

	_, err = db.SQL().Exec(
		`INSERT INTO fleet_nodes(name, provider, tailscale_ip, os, arch, tags, preset, no_zones, exclude_paths_json, selected_repos_json, ai_accounts_json, checklist_json, last_synced_at)
		 VALUES(?, ?, '', 'linux', ?, '', ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
			provider=excluded.provider,
			arch=excluded.arch,
			preset=excluded.preset,
			no_zones=excluded.no_zones,
			exclude_paths_json=excluded.exclude_paths_json,
			selected_repos_json=excluded.selected_repos_json,
			ai_accounts_json=excluded.ai_accounts_json,
			checklist_json=excluded.checklist_json,
			last_synced_at=CASE WHEN excluded.last_synced_at > 0 THEN excluded.last_synced_at ELSE fleet_nodes.last_synced_at END`,
		spec.Node, provider, spec.Arch, spec.Preset, noZonesInt,
		excJSON, repoJSON, aiJSON, string(chkBytes), syncedAt,
	)
	return err
}

func marshalSliceJSON(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
