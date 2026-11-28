package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

type CommandRunner interface {
	Run(cmd []string, env map[string]string, dir string) (stdout string, stderr string, err error)
	LookPath(bin string) (string, error)
}

type TimeoutCommandRunner interface {
	RunWithTimeout(timeout time.Duration, cmd []string, env map[string]string, dir string) (stdout string, stderr string, err error)
}

type InteractiveCommandRunner interface {
	RunInteractive(cmd []string, env map[string]string, dir string, needsTTY bool) (stdout string, stderr string, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(cmd []string, env map[string]string, dir string) (string, string, error) {
	return ExecRunner{}.run(context.Background(), cmd, env, dir, false)
}

func (ExecRunner) RunWithTimeout(timeout time.Duration, cmd []string, env map[string]string, dir string) (string, string, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return ExecRunner{}.run(ctx, cmd, env, dir, false)
}

func (ExecRunner) RunInteractive(cmd []string, env map[string]string, dir string, needsTTY bool) (string, string, error) {
	return ExecRunner{}.run(context.Background(), cmd, env, dir, needsTTY)
}

func (ExecRunner) run(ctx context.Context, cmd []string, env map[string]string, dir string, needsTTY bool) (string, string, error) {
	if len(cmd) == 0 {
		return "", "", errors.New("empty command")
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = app.PinnedMachineEnv(os.Environ())
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	if needsTTY {
		c.Stdin = os.Stdin
	}
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	return outBuf.String(), errBuf.String(), err
}

func (ExecRunner) LookPath(bin string) (string, error) {
	return exec.LookPath(bin)
}

func Scan(ctx *app.Context) ([]Candidate, error) {
	return ScanWithRunner(ctx, ExecRunner{})
}

func ScanWithRunner(ctx *app.Context, runner CommandRunner) ([]Candidate, error) {
	return ScanWithOptions(ctx, runner, time.Now())
}

func ScanWithOptions(ctx *app.Context, runner CommandRunner, now time.Time) ([]Candidate, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	return scanAllCandidates(ctx, runner, now), nil
}

func PlanClean(ctx *app.Context, candidates []Candidate, selectedIDs []string, tiers []Tier) (*engine.Plan, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	chosen, err := selectCandidates(candidates, selectedIDs, tiers)
	if err != nil {
		return nil, err
	}

	plan := &engine.Plan{
		Verb:      "clean",
		Host:      engine.FormatPlanHost(ctx),
		Title:     "Cleanup reclaimable caches and artifacts",
		Preset:    ctx.EffectivePreset(),
		CreatedAt: time.Now().UTC(),
	}

	roots := AllowlistedRoots(ctx)

	for _, c := range chosen {
		if c.Tier == TierDangerous {
			if ctx.NonInteractive {
				return nil, fmt.Errorf("%w: %s cannot run in --non-interactive mode", ErrDangerousBlocked, c.ID)
			}
			if !ctx.Allow.Remove {
				return nil, fmt.Errorf("%w: %s requires --allow remove", ErrDangerousBlocked, c.ID)
			}
		}

		var validatedPaths []string
		if len(c.CleanerCmd) == 0 {
			for _, p := range c.Paths {
				valid, verr := validateCandidatePath(ctx, c.Category, p, roots)
				if verr != nil {
					return nil, fmt.Errorf("candidate %s: %w", c.ID, verr)
				}
				validatedPaths = append(validatedPaths, valid)
			}
			if len(validatedPaths) == 0 {
				plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: no paths to remove", c.ID))
				continue
			}
		} else if verr := validateCleanerCommand(c.CleanerCmd); verr != nil {
			return nil, fmt.Errorf("candidate %s: %w", c.ID, verr)
		}

		risk := tierToRisk(c.Tier)
		var reqScopes []string
		if c.Tier == TierDangerous {
			reqScopes = append(reqScopes, "remove")
		}
		if commandRequiresSudo(c.CleanerCmd) {
			reqScopes = append(reqScopes, "sudo")
		}
		reqAllow := strings.Join(reqScopes, ",")

		meta := map[string]string{
			"tier":     string(c.Tier),
			"category": c.Category,
			"bytes":    strconv.FormatInt(c.Bytes, 10),
		}
		if len(validatedPaths) > 0 {
			rawPaths, err := json.Marshal(validatedPaths)
			if err != nil {
				return nil, err
			}
			meta["paths"] = string(rawPaths)
		}

		target := c.ID
		if len(validatedPaths) == 1 {
			target = validatedPaths[0]
		} else if len(c.CleanerCmd) > 0 {
			target = strings.Join(c.CleanerCmd, " ")
		}

		if c.Warning != "" {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s: %s", c.ID, c.Warning))
		}

		plan.Actions = append(plan.Actions, engine.Action{
			ID:            "clean:" + c.ID,
			Kind:          engine.ActionClean,
			Target:        target,
			Summary:       c.Title,
			Detail:        c.Warning,
			Risk:          risk,
			RequiresAllow: reqAllow,
			Command:       slices.Clone(c.CleanerCmd),
			Metadata:      meta,
		})
	}

	return plan, nil
}

func ExecClean(ctx *app.Context, plan *engine.Plan, runner CommandRunner) (*engine.ApplyResult, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if plan == nil {
		return nil, errors.New("nil plan")
	}
	if plan.Verb != "" && plan.Verb != "clean" {
		return nil, fmt.Errorf("refusing plan with verb %q in clean", plan.Verb)
	}
	if runner == nil {
		runner = ExecRunner{}
	}

	res := &engine.ApplyResult{
		Warnings: slices.Clone(plan.Warnings),
	}
	roots := AllowlistedRoots(ctx)

	type preparedAction struct {
		action engine.Action
		paths  []string
	}
	prepared := make([]preparedAction, 0, len(plan.Actions))

	for _, act := range plan.Actions {
		if act.Kind != "" && act.Kind != engine.ActionClean {
			return res, fmt.Errorf("%w: action %s has unsupported kind %q in clean", ErrUnsafeTarget, act.ID, act.Kind)
		}

		var paths []string
		if len(act.Command) > 0 {
			if err := validateCleanerCommand(act.Command); err != nil {
				return res, fmt.Errorf("%s: %w", act.ID, err)
			}
		} else {
			rawPaths, err := extractActionPaths(act)
			if err != nil {
				return res, fmt.Errorf("%s: %w", act.ID, err)
			}
			category := act.Metadata["category"]
			for _, p := range rawPaths {
				valid, verr := validateCandidatePath(ctx, category, p, roots)
				if verr != nil {
					return res, fmt.Errorf("%s: %w", act.ID, verr)
				}
				paths = append(paths, valid)
			}
		}

		if isDangerousAction(ctx, act, paths) {
			if ctx.NonInteractive {
				return res, fmt.Errorf("%w: %s cannot execute in --non-interactive mode", ErrDangerousBlocked, act.ID)
			}
			if !ctx.Allow.Remove {
				return res, fmt.Errorf("%w: %s requires --allow remove", ErrDangerousBlocked, act.ID)
			}
		}

		for _, scope := range requiredCleanAllows(ctx, act, paths) {
			if !allowSatisfied(ctx.Allow, scope) {
				if ctx.DryRun {
					res.Warnings = append(res.Warnings, fmt.Sprintf("[dry-run] %s requires --allow %s", act.ID, scope))
					continue
				}
				return res, fmt.Errorf("action %s requires --allow %s", act.ID, scope)
			}
		}

		prepared = append(prepared, preparedAction{action: act, paths: paths})
	}

	if ctx.DryRun {
		res.Skipped = len(prepared)
		return res, nil
	}

	if ctx.WSBaseDir() != "" {
		if _, lerr := app.LoadLedger(ctx.WSBaseDir()); lerr != nil {
			return res, fmt.Errorf("load ledger: %w", lerr)
		}
	}

	for _, item := range prepared {
		act := item.action

		if len(act.Command) > 0 {
			var cmdEnv map[string]string
			if len(act.Command) >= 2 && act.Command[0] == "brew" && act.Command[1] == "cleanup" {
				cmdEnv = map[string]string{
					"HOMEBREW_NO_AUTO_UPDATE": "1",
					"HOMEBREW_NO_ASK":         "1",
				}
			}
			needsTTY := commandRequiresSudo(act.Command) && ctx.ChildTTY()
			var (
				stderr string
				err    error
			)
			if ir, ok := runner.(InteractiveCommandRunner); ok {
				_, stderr, err = ir.RunInteractive(act.Command, cmdEnv, ctx.HomeDir, needsTTY)
			} else {
				_, stderr, err = runner.Run(act.Command, cmdEnv, ctx.HomeDir)
			}
			if err != nil {
				msg := strings.TrimSpace(stderr)
				if msg != "" {
					return res, fmt.Errorf("%s failed: %w: %s", act.ID, err, msg)
				}
				return res, fmt.Errorf("%s failed: %w", act.ID, err)
			}
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			continue
		}

		category := act.Metadata["category"]
		if category == "backups" && ctx.WSBaseDir() != "" {
			if err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
				ledger, lerr := app.LoadLedger(ctx.WSBaseDir())
				if lerr != nil {
					return fmt.Errorf("load ledger: %w", lerr)
				}
				refs := referencedUndoBackups(ledger)
				for _, p := range item.paths {
					valid, verr := verifyPreWSBackupWithRefs(ctx, p, refs)
					if verr != nil {
						return fmt.Errorf("%s: %w", act.ID, verr)
					}
					if err := os.RemoveAll(valid); err != nil {
						return fmt.Errorf("%s: remove %s: %w", act.ID, valid, err)
					}
				}
				return nil
			}); err != nil {
				return res, err
			}
		} else {
			for _, p := range item.paths {
				valid, err := validateCandidatePath(ctx, category, p, roots)
				if err != nil {
					return res, fmt.Errorf("%s: %w", act.ID, err)
				}
				if err := os.RemoveAll(valid); err != nil {
					return res, fmt.Errorf("%s: remove %s: %w", act.ID, valid, err)
				}
			}
		}
		res.Applied++
		res.Changed = append(res.Changed, act.ID)
	}

	if ctx.WSBaseDir() != "" {
		if err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
			ledger, lerr := app.LoadLedger(ctx.WSBaseDir())
			if lerr != nil {
				return fmt.Errorf("load ledger: %w", lerr)
			}
			ledger.RunJournal = append(ledger.RunJournal, app.JournalEntry{
				Timestamp:    time.Now().UTC(),
				Command:      "clean",
				Preset:       plan.Preset,
				Summary:      plan.Title,
				ActionsCount: res.Applied,
				Warnings:     res.Warnings,
			})
			if err := app.SaveLedger(ctx.WSBaseDir(), ledger); err != nil {
				return fmt.Errorf("save ledger: %w", err)
			}
			return nil
		}); err != nil {
			return res, err
		}
	}

	return res, nil
}

func selectCandidates(candidates []Candidate, selectedIDs []string, tiers []Tier) ([]Candidate, error) {
	if len(selectedIDs) > 0 {
		byID := make(map[string]Candidate, len(candidates))
		for _, c := range candidates {
			byID[c.ID] = c
		}
		var out []Candidate
		seen := map[string]bool{}
		for _, id := range selectedIDs {
			if seen[id] {
				continue
			}
			c, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("unknown cleanup candidate %q", id)
			}
			seen[id] = true
			out = append(out, c)
		}
		return out, nil
	}

	if len(tiers) > 0 {
		var out []Candidate
		for _, c := range candidates {
			if slices.Contains(tiers, c.Tier) {
				out = append(out, c)
			}
		}
		return out, nil
	}

	var out []Candidate
	for _, c := range candidates {
		if c.SelectedByDefault && c.Tier != TierDangerous {
			out = append(out, c)
		}
	}
	return out, nil
}

func validateCandidatePath(ctx *app.Context, category, p string, roots []string) (string, error) {
	switch category {
	case "backups":
		return VerifyPreWSBackup(ctx, p)
	case "cargo":
		valid, err := ValidateTarget(ctx, p, codeRoots(ctx))
		if err != nil {
			return "", err
		}
		if filepath.Base(valid) != "target" {
			return "", fmt.Errorf("%w: cargo cleanup path must be a target directory: %s", ErrUnsafeTarget, valid)
		}
		cargoToml := filepath.Join(filepath.Dir(valid), "Cargo.toml")
		fi, err := os.Lstat(cargoToml)
		if err != nil || fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: missing sibling Cargo.toml for %s", ErrUnsafeTarget, valid)
		}
		return valid, nil
	default:
		return ValidateTarget(ctx, p, roots)
	}
}

func extractActionPaths(act engine.Action) ([]string, error) {
	if raw, ok := act.Metadata["paths"]; ok && strings.TrimSpace(raw) != "" {
		var paths []string
		if err := json.Unmarshal([]byte(raw), &paths); err != nil {
			return nil, fmt.Errorf("parse action paths: %w", err)
		}
		return paths, nil
	}
	if act.Target != "" {
		return []string{act.Target}, nil
	}
	return nil, fmt.Errorf("%w: clean action has neither command nor paths", ErrUnsafeTarget)
}

var allowlistedCleanerCommands = [][]string{
	{"brew", "cleanup", "--prune=all", "-s"},
	{"sudo", "paccache", "-rk2"},
	{"paccache", "-rk2"},
	{"sudo", "pacman", "-Sc", "--noconfirm"},
	{"go", "clean", "-cache"},
	{"npm", "cache", "clean", "--force"},
	{"uv", "cache", "clean"},
	{"docker", "builder", "prune", "-f"},
	{"docker", "volume", "prune", "-f"},
}

func validateCleanerCommand(cmd []string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("%w: empty cleaner command", ErrUnsafeTarget)
	}
	for _, allowed := range allowlistedCleanerCommands {
		if slices.Equal(cmd, allowed) {
			return nil
		}
	}
	return fmt.Errorf("%w: cleaner command not allowlisted: %s", ErrUnsafeTarget, strings.Join(cmd, " "))
}

func commandRequiresSudo(cmd []string) bool {
	return len(cmd) > 0 && (cmd[0] == "sudo" || cmd[0] == "paccache")
}

func isDangerousAction(ctx *app.Context, act engine.Action, paths []string) bool {
	if act.Risk == engine.RiskDangerous ||
		strings.EqualFold(act.Metadata["tier"], string(TierDangerous)) ||
		strings.EqualFold(act.Metadata["category"], "vm") ||
		strings.HasPrefix(act.ID, "clean:dangerous:") ||
		strings.HasPrefix(act.ID, "dangerous:") {
		return true
	}
	if len(act.Command) >= 2 && act.Command[0] == "docker" && act.Command[1] == "volume" {
		return true
	}
	if ctx != nil && ctx.HomeDir != "" {
		avdRoot := filepath.Clean(filepath.Join(ctx.HomeDir, ".android", "avd"))
		realAVD := evalExisting(avdRoot)
		for _, p := range paths {
			clean := filepath.Clean(p)
			realP := evalExisting(clean)
			if isWithinOrEqual(clean, avdRoot) || isWithinOrEqual(avdRoot, clean) ||
				isWithinOrEqual(realP, realAVD) || isWithinOrEqual(realAVD, realP) {
				return true
			}
		}
	}
	return false
}

func requiredCleanAllows(ctx *app.Context, act engine.Action, paths []string) []string {
	var scopes []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || slices.Contains(scopes, s) {
			return
		}
		scopes = append(scopes, s)
	}
	for _, part := range strings.Split(act.RequiresAllow, ",") {
		add(part)
	}
	if isDangerousAction(ctx, act, paths) {
		add("remove")
	}
	if commandRequiresSudo(act.Command) {
		add("sudo")
	}
	return scopes
}

func tierToRisk(t Tier) engine.RiskLevel {
	switch t {
	case TierSafe:
		return engine.RiskSafe
	case TierCostly:
		return engine.RiskCostly
	case TierDangerous:
		return engine.RiskDangerous
	default:
		return engine.RiskMedium
	}
}

func allowSatisfied(a app.AllowSet, scope string) bool {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return true
	}
	for _, part := range strings.Split(trimmed, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "":
			continue
		case "replace":
			if !a.Replace {
				return false
			}
		case "sudo":
			if !a.Sudo {
				return false
			}
		case "remove":
			if !a.Remove {
				return false
			}
		case "script":
			if !a.Script {
				return false
			}
		default:
			return false
		}
	}
	return true
}
