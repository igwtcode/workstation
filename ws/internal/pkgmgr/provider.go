package pkgmgr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

var ErrScriptNotAllowed = errors.New("script install requires explicit consent (--allow script)")

type CommandRunner interface {
	Run(cmd []string, env map[string]string, dir string, needsTTY bool) (stdout string, stderr string, err error)
	LookPath(bin string) (string, error)
}

type StreamingCommandRunner interface {
	RunStreaming(cmd []string, env map[string]string, dir string, needsTTY bool, stdout, stderr io.Writer) (capturedStderr string, err error)
}

type ExecRunner struct {
	Probe bool
}

func (r ExecRunner) env() []string {
	if r.Probe {
		return app.ProbeEnv(app.PinnedMachineEnv(os.Environ()))
	}
	return app.PinnedMachineEnv(os.Environ())
}

func (r ExecRunner) Run(cmd []string, env map[string]string, dir string, needsTTY bool) (string, string, error) {
	if len(cmd) == 0 {
		return "", "", errors.New("empty command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = r.env()
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	if needsTTY {
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		err := c.Run()
		return "", "", err
	}
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	return outBuf.String(), errBuf.String(), err
}

func (r ExecRunner) RunStreaming(cmd []string, env map[string]string, dir string, needsTTY bool, stdout, stderr io.Writer) (string, error) {
	if len(cmd) == 0 {
		return "", errors.New("empty command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	if dir != "" {
		c.Dir = dir
	}
	c.Env = r.env()
	for k, v := range env {
		c.Env = append(c.Env, k+"="+v)
	}
	if needsTTY {
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		err := c.Run()
		return "", err
	}
	if stdout != nil {
		c.Stdout = stdout
	}
	var errBuf bytes.Buffer
	if stderr != nil {
		c.Stderr = io.MultiWriter(stderr, &errBuf)
	} else {
		c.Stderr = &errBuf
	}
	err := c.Run()
	return errBuf.String(), err
}

func (ExecRunner) LookPath(bin string) (string, error) {
	return exec.LookPath(bin)
}

type Provider interface {
	Name() string
	Available() bool
	IsInstalled(pkg config.ResolvedTool) bool
	InstallAction(pkg config.ResolvedTool) engine.Action
	UpgradeAction(all bool) *engine.Action
}

func SelfUpdatedByProvider(tools []config.ResolvedTool) map[string][]string {
	allProviders := []string{"brew", "cask", "pacman", "paru", "apt", "dnf", "mise", "gh-ext"}
	sets := map[string]map[string]bool{}
	add := func(via, name string) {
		via = strings.TrimSpace(via)
		name = strings.TrimSpace(name)
		if via == "" || name == "" {
			return
		}
		if sets[via] == nil {
			sets[via] = map[string]bool{}
		}
		sets[via][name] = true
	}
	addAllProviders := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, prov := range allProviders {
			add(prov, name)
		}
	}

	for _, t := range tools {
		if t.Updates != "self" {
			continue
		}
		addAllProviders(t.ID)
		addAllProviders(t.Bin)
		if t.Spec.Name != "" {
			addAllProviders(t.Spec.Name)
		}
		if t.Spec.Ref != "" {
			add("mise", t.Spec.Ref)
			if t.Spec.Via != "" {
				add(t.Spec.Via, t.Spec.Ref)
			}
		}
		if name := pkgNameOf(t); name != "" && t.Spec.Via != "" {
			add(t.Spec.Via, name)
		}
		for _, conflict := range t.Conflicts {
			if via, pkg, ok := strings.Cut(conflict, ":"); ok {
				add(via, pkg)
			}
		}
	}

	out := make(map[string][]string, len(sets))
	for via, m := range sets {
		list := make([]string, 0, len(m))
		for k := range m {
			list = append(list, k)
		}
		sort.Strings(list)
		out[via] = list
	}
	return out
}

func AllProviders(ctx *app.Context, runner CommandRunner, tools []config.ResolvedTool) map[string]Provider {
	if runner == nil {
		runner = ExecRunner{}
	}
	selfMap := SelfUpdatedByProvider(tools)
	archSelf := unionSorted(selfMap["pacman"], selfMap["paru"])

	return map[string]Provider{
		"brew":   &brewProvider{ctx: ctx, runner: runner, skipSelf: selfMap["brew"]},
		"cask":   &caskProvider{ctx: ctx, runner: runner, skipSelf: selfMap["cask"]},
		"pacman": &pacmanProvider{ctx: ctx, runner: runner, skipSelf: archSelf},
		"paru":   &paruProvider{ctx: ctx, runner: runner, skipSelf: archSelf},
		"apt":    &aptProvider{ctx: ctx, runner: runner, skipSelf: selfMap["apt"]},
		"dnf":    &dnfProvider{ctx: ctx, runner: runner, skipSelf: selfMap["dnf"]},
		"mise":   &miseProvider{ctx: ctx, runner: runner, skipSelf: selfMap["mise"]},
		"gh-ext": &ghExtProvider{ctx: ctx, runner: runner, skipSelf: selfMap["gh-ext"]},
		"script": &scriptProvider{ctx: ctx, runner: runner},
	}
}

func pkgNameOf(pkg config.ResolvedTool) string {
	if pkg.Spec.Name != "" {
		return pkg.Spec.Name
	}
	if pkg.Spec.Ref != "" {
		return pkg.Spec.Ref
	}
	return pkg.ID
}

func unionSorted(a, b []string) []string {
	m := map[string]bool{}
	for _, s := range a {
		if s != "" {
			m[s] = true
		}
	}
	for _, s := range b {
		if s != "" {
			m[s] = true
		}
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func hasBin(runner CommandRunner, bin string) bool {
	if bin == "" {
		return false
	}
	_, err := runner.LookPath(bin)
	return err == nil
}

func filterOutSelf(candidates []string, skip []string) (kept []string, excluded []string) {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[strings.ToLower(s)] = true
	}
	for _, raw := range candidates {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if skipSet[strings.ToLower(name)] {
			excluded = append(excluded, name)
			continue
		}
		kept = append(kept, name)
	}
	return kept, excluded
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type brewProvider struct {
	ctx           *app.Context
	runner        CommandRunner
	skipSelf      []string
	installedOnce bool
	installedSet  map[string]bool
	batchOK       bool
}

func (p *brewProvider) Name() string    { return "brew" }
func (p *brewProvider) Available() bool { return hasBin(p.runner, "brew") }

func (p *brewProvider) ensureInstalled() {
	if p.installedOnce {
		return
	}
	p.installedOnce = true
	p.installedSet, p.batchOK = LoadBrewInstalledSet(p.runner, false)
}

func (p *brewProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	p.ensureInstalled()
	var installed bool
	if p.batchOK {
		installed = matchBrewInstalled(p.installedSet, name, "homebrew/core/")
	} else {
		_, _, err := p.runner.Run([]string{"brew", "list", "--formula", name}, nil, "", false)
		installed = err == nil
	}
	if !installed && p.ctx != nil && p.ctx.Host.OS == "darwin" && strings.EqualFold(name, "zsh") {
		bin := pkg.Bin
		if bin == "" {
			bin = "zsh"
		}
		if hasBin(p.runner, bin) {
			return true
		}
	}
	return installed
}

func (p *brewProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	meta := map[string]string{
		"provider":            "brew",
		"tool":                pkg.ID,
		"pkg":                 name,
		"updates":             pkg.Updates,
		"env.HOMEBREW_NO_ASK": "1",
	}
	if strings.Count(name, "/") == 2 {
		parts := strings.Split(name, "/")
		meta["tap"] = parts[0] + "/" + parts[1]
	}
	return engine.Action{
		ID:       fmt.Sprintf("install:brew:%s", pkg.ID),
		Kind:     engine.ActionInstall,
		Target:   name,
		Summary:  fmt.Sprintf("Install %s via Homebrew formula", name),
		Risk:     engine.RiskSafe,
		Command:  []string{"brew", "install", "--formula", name},
		Metadata: meta,
	}
}

func (p *brewProvider) upgradeActionFromNames(outdated []string, alreadyQueried bool) *engine.Action {
	meta := map[string]string{
		"provider":            "brew",
		"env.HOMEBREW_NO_ASK": "1",
	}
	if len(p.skipSelf) == 0 && !alreadyQueried {
		return &engine.Action{
			ID:       "upgrade:brew",
			Kind:     engine.ActionUpgrade,
			Target:   "brew",
			Summary:  "Upgrade Homebrew formulae",
			Risk:     engine.RiskSafe,
			Command:  []string{"brew", "upgrade", "--formula"},
			Metadata: meta,
		}
	}

	if !alreadyQueried {
		stdout, _, err := p.runner.Run([]string{"brew", "outdated", "--formula", "--quiet"}, nil, "", false)
		if err != nil {

			return nil
		}
		outdated = splitNonEmptyLines(stdout)
	}
	kept := outdated
	var excluded []string
	if len(p.skipSelf) > 0 {
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
		kept, excluded = filterOutSelf(outdated, p.skipSelf)
	}
	if len(excluded) > 0 {
		meta["excluded"] = strings.Join(excluded, ",")
	}
	if len(kept) == 0 {
		return nil
	}
	summary := fmt.Sprintf("Upgrade %d Homebrew formulae", len(kept))
	if len(p.skipSelf) > 0 {
		summary = fmt.Sprintf("Upgrade %d Homebrew formulae (excluding self-updaters)", len(kept))
	}
	return &engine.Action{
		ID:       "upgrade:brew",
		Kind:     engine.ActionUpgrade,
		Target:   "brew",
		Summary:  summary,
		Risk:     engine.RiskSafe,
		Command:  append([]string{"brew", "upgrade", "--formula"}, kept...),
		Metadata: meta,
	}
}

func (p *brewProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	return p.upgradeActionFromNames(nil, false)
}

type caskProvider struct {
	ctx           *app.Context
	runner        CommandRunner
	skipSelf      []string
	installedOnce bool
	installedSet  map[string]bool
	batchOK       bool
}

func (p *caskProvider) Name() string    { return "cask" }
func (p *caskProvider) Available() bool { return hasBin(p.runner, "brew") }

func (p *caskProvider) ensureInstalled() {
	if p.installedOnce {
		return
	}
	p.installedOnce = true
	p.installedSet, p.batchOK = LoadBrewInstalledSet(p.runner, true)
}

func (p *caskProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	p.ensureInstalled()
	if p.batchOK {
		return matchBrewInstalled(p.installedSet, name, "homebrew/cask/")
	}
	_, _, err := p.runner.Run([]string{"brew", "list", "--cask", name}, nil, "", false)
	return err == nil
}

func (p *caskProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	meta := map[string]string{
		"provider":            "cask",
		"tool":                pkg.ID,
		"pkg":                 name,
		"updates":             pkg.Updates,
		"env.HOMEBREW_NO_ASK": "1",
	}
	if strings.Count(name, "/") == 2 {
		parts := strings.Split(name, "/")
		meta["tap"] = parts[0] + "/" + parts[1]
	}
	return engine.Action{
		ID:       fmt.Sprintf("install:cask:%s", pkg.ID),
		Kind:     engine.ActionInstall,
		Target:   name,
		Summary:  fmt.Sprintf("Install %s via Homebrew cask", name),
		Risk:     engine.RiskMedium,
		NeedsTTY: true,
		Command:  []string{"brew", "install", "--cask", name},
		Metadata: meta,
	}
}

func (p *caskProvider) upgradeActionFromNames(all bool, outdated []string, alreadyQueried bool) *engine.Action {
	meta := map[string]string{
		"provider":            "cask",
		"env.HOMEBREW_NO_ASK": "1",
	}
	if len(p.skipSelf) > 0 {
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
	}

	if len(p.skipSelf) == 0 && !alreadyQueried {
		cmd := []string{"brew", "upgrade", "--cask"}
		summary := "Upgrade Homebrew casks"
		if all {
			cmd = append(cmd, "--greedy")
			summary = "Upgrade all Homebrew casks (--greedy)"
		}
		return &engine.Action{
			ID:       "upgrade:cask",
			Kind:     engine.ActionUpgrade,
			Target:   "cask",
			Summary:  summary,
			Risk:     engine.RiskMedium,
			NeedsTTY: true,
			Command:  cmd,
			Metadata: meta,
		}
	}

	if !alreadyQueried {
		outdatedCmd := []string{"brew", "outdated", "--cask", "--quiet"}
		if all {
			outdatedCmd = []string{"brew", "outdated", "--cask", "--greedy", "--quiet"}
		}
		stdout, _, err := p.runner.Run(outdatedCmd, nil, "", false)
		if err != nil {

			return nil
		}
		outdated = splitNonEmptyLines(stdout)
	}

	kept := outdated
	var excluded []string
	if len(p.skipSelf) > 0 {
		kept, excluded = filterOutSelf(outdated, p.skipSelf)
	}
	if len(excluded) > 0 {
		meta["excluded"] = strings.Join(excluded, ",")
	}
	if len(kept) == 0 {
		return nil
	}
	cmd := []string{"brew", "upgrade", "--cask"}
	if all {
		cmd = append(cmd, "--greedy")
	}
	cmd = append(cmd, kept...)
	summary := fmt.Sprintf("Upgrade %d Homebrew casks", len(kept))
	if len(p.skipSelf) > 0 {
		summary = fmt.Sprintf("Upgrade %d Homebrew casks (self-updaters excluded)", len(kept))
	}
	return &engine.Action{
		ID:       "upgrade:cask",
		Kind:     engine.ActionUpgrade,
		Target:   "cask",
		Summary:  summary,
		Risk:     engine.RiskMedium,
		NeedsTTY: true,
		Command:  cmd,
		Metadata: meta,
	}
}

func (p *caskProvider) UpgradeAction(all bool) *engine.Action {
	return p.upgradeActionFromNames(all, nil, false)
}

type pacmanProvider struct {
	ctx           *app.Context
	runner        CommandRunner
	skipSelf      []string
	installedOnce bool
	installedSet  map[string]bool
	batchOK       bool
}

func (p *pacmanProvider) Name() string    { return "pacman" }
func (p *pacmanProvider) Available() bool { return hasBin(p.runner, "pacman") }

func (p *pacmanProvider) ensureInstalled() {
	if p.installedOnce {
		return
	}
	p.installedOnce = true
	p.installedSet, p.batchOK = LoadArchInstalledSet(p.runner, "pacman")
}

func (p *pacmanProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	p.ensureInstalled()
	if p.batchOK {
		return p.installedSet[name]
	}
	_, _, err := p.runner.Run([]string{"pacman", "-Qi", name}, nil, "", false)
	return err == nil
}

func (p *pacmanProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	return engine.Action{
		ID:            fmt.Sprintf("install:pacman:%s", pkg.ID),
		Kind:          engine.ActionInstall,
		Target:        name,
		Summary:       fmt.Sprintf("Install %s via pacman", name),
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"sudo", "pacman", "-S", "--needed", "--noconfirm", name},
		Metadata: map[string]string{
			"provider": "pacman",
			"tool":     pkg.ID,
			"pkg":      name,
			"updates":  pkg.Updates,
		},
	}
}

func (p *pacmanProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	cmd := []string{"sudo", "pacman", "-Syu", "--noconfirm"}
	meta := map[string]string{"provider": "pacman"}
	if len(p.skipSelf) > 0 {
		for _, pkg := range p.skipSelf {
			cmd = append(cmd, "--ignore", pkg)
		}
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
	}
	return &engine.Action{
		ID:            "upgrade:pacman",
		Kind:          engine.ActionUpgrade,
		Target:        "pacman",
		Summary:       "Upgrade system packages via pacman",
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       cmd,
		Metadata:      meta,
	}
}

type paruProvider struct {
	ctx           *app.Context
	runner        CommandRunner
	skipSelf      []string
	installedOnce bool
	installedSet  map[string]bool
	batchOK       bool
}

func (p *paruProvider) Name() string    { return "paru" }
func (p *paruProvider) Available() bool { return hasBin(p.runner, "paru") }

func (p *paruProvider) ensureInstalled() {
	if p.installedOnce {
		return
	}
	p.installedOnce = true
	p.installedSet, p.batchOK = LoadArchInstalledSet(p.runner, "paru")
}

func (p *paruProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	p.ensureInstalled()
	if p.batchOK {
		return p.installedSet[name]
	}
	_, _, err := p.runner.Run([]string{"paru", "-Qi", name}, nil, "", false)
	return err == nil
}

func (p *paruProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	return engine.Action{
		ID:            fmt.Sprintf("install:paru:%s", pkg.ID),
		Kind:          engine.ActionInstall,
		Target:        name,
		Summary:       fmt.Sprintf("Install %s via paru", name),
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"paru", "-S", "--needed", "--noconfirm", name},
		Metadata: map[string]string{
			"provider": "paru",
			"tool":     pkg.ID,
			"pkg":      name,
			"updates":  pkg.Updates,
		},
	}
}

func (p *paruProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	cmd := []string{"paru", "-Syu", "--noconfirm"}
	meta := map[string]string{"provider": "paru"}
	if len(p.skipSelf) > 0 {
		for _, pkg := range p.skipSelf {
			cmd = append(cmd, "--ignore", pkg)
		}
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
	}
	return &engine.Action{
		ID:            "upgrade:paru",
		Kind:          engine.ActionUpgrade,
		Target:        "paru",
		Summary:       "Upgrade official and AUR packages via paru",
		Risk:          engine.RiskDangerous,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       cmd,
		Metadata:      meta,
	}
}

type aptProvider struct {
	ctx           *app.Context
	runner        CommandRunner
	skipSelf      []string
	installedOnce bool
	installedSet  map[string]bool
	batchOK       bool
}

func (p *aptProvider) Name() string    { return "apt" }
func (p *aptProvider) Available() bool { return hasBin(p.runner, "apt-get") }

func (p *aptProvider) ensureInstalled() {
	if p.installedOnce {
		return
	}
	p.installedOnce = true
	p.installedSet, p.batchOK = LoadDpkgInstalledSet(p.runner)
}

func (p *aptProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	p.ensureInstalled()
	if p.batchOK {
		return p.installedSet[name]
	}
	stdout, _, err := p.runner.Run([]string{"dpkg-query", "-W", "-f=${Status}", name}, nil, "", false)
	return err == nil && strings.TrimSpace(stdout) == "install ok installed"
}

func (p *aptProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	meta := map[string]string{
		"provider":            "apt",
		"tool":                pkg.ID,
		"pkg":                 name,
		"updates":             pkg.Updates,
		"env.DEBIAN_FRONTEND": "noninteractive",
	}
	if pkg.Spec.Shim != "" {
		meta["shim"] = pkg.Spec.Shim
	}
	return engine.Action{
		ID:            fmt.Sprintf("install:apt:%s", pkg.ID),
		Kind:          engine.ActionInstall,
		Target:        name,
		Summary:       fmt.Sprintf("Install %s via apt-get", name),
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"sudo", "-E", "apt-get", "install", "-y", name},
		Metadata:      meta,
	}
}

func (p *aptProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	return p.upgradeActionFromNames(nil, false)
}

func (p *aptProvider) upgradeActionFromNames(upgradable []string, alreadyQueried bool) *engine.Action {
	meta := map[string]string{
		"provider":            "apt",
		"env.DEBIAN_FRONTEND": "noninteractive",
	}
	if len(p.skipSelf) == 0 && !alreadyQueried {
		return &engine.Action{
			ID:            "upgrade:apt",
			Kind:          engine.ActionUpgrade,
			Target:        "apt",
			Summary:       "Upgrade packages via apt-get",
			Risk:          engine.RiskMedium,
			RequiresAllow: "sudo",
			NeedsTTY:      true,
			Command:       []string{"sudo", "-E", "apt-get", "upgrade", "-y"},
			Metadata:      meta,
		}
	}

	if !alreadyQueried {
		stdout, _, err := p.runner.Run([]string{"apt", "list", "--upgradable"}, nil, "", false)
		if err != nil {
			return nil
		}
		for _, item := range parseAptUpgradable(stdout) {
			upgradable = append(upgradable, item.Name)
		}
	}
	kept := upgradable
	var excluded []string
	if len(p.skipSelf) > 0 {
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
		kept, excluded = filterOutSelf(upgradable, p.skipSelf)
	}
	if len(excluded) > 0 {
		meta["excluded"] = strings.Join(excluded, ",")
	}
	if len(kept) == 0 {
		return nil
	}
	summary := fmt.Sprintf("Upgrade %d apt packages", len(kept))
	if len(p.skipSelf) > 0 {
		summary = fmt.Sprintf("Upgrade %d apt packages (excluding self-updaters)", len(kept))
	}
	return &engine.Action{
		ID:            "upgrade:apt",
		Kind:          engine.ActionUpgrade,
		Target:        "apt",
		Summary:       summary,
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       append([]string{"sudo", "-E", "apt-get", "install", "--only-upgrade", "-y"}, kept...),
		Metadata:      meta,
	}
}

func ParseShim(spec string) (srcBin, dstBin string, err error) {
	src, dst, ok := strings.Cut(strings.TrimSpace(spec), ":")
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if !ok || src == "" || dst == "" || strings.ContainsAny(src, "/\\") || strings.ContainsAny(dst, "/\\") {
		return "", "", fmt.Errorf("invalid shim spec %q (expected source:target)", spec)
	}
	return src, dst, nil
}

func EnsureShim(ctx *app.Context, shimSpec string, runner CommandRunner) (string, string, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	srcBin, dstBin, err := ParseShim(shimSpec)
	if err != nil {
		return "", "", err
	}
	srcPath, err := runner.LookPath(srcBin)
	if err != nil {
		return "", "", fmt.Errorf("shim source binary %q not found on PATH: %w", srcBin, err)
	}

	binDir := filepath.Join(ctx.HomeDir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", "", err
	}
	linkPath := filepath.Join(binDir, dstBin)

	if fi, lerr := os.Lstat(linkPath); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if cur, rerr := os.Readlink(linkPath); rerr == nil && cur == srcPath {
				return srcPath, linkPath, nil
			}
			if err := os.Remove(linkPath); err != nil {
				return "", "", err
			}
		} else {
			if !ctx.Allow.Replace {
				return "", "", fmt.Errorf("refusing to overwrite non-symlink %s without --allow replace", linkPath)
			}
			if err := os.Remove(linkPath); err != nil {
				return "", "", err
			}
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return "", "", lerr
	}

	if err := os.Symlink(srcPath, linkPath); err != nil {
		return "", "", err
	}
	return srcPath, linkPath, nil
}

type dnfProvider struct {
	ctx      *app.Context
	runner   CommandRunner
	skipSelf []string
}

func (p *dnfProvider) Name() string    { return "dnf" }
func (p *dnfProvider) Available() bool { return hasBin(p.runner, "dnf") }

func (p *dnfProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	_, _, err := p.runner.Run([]string{"rpm", "-q", name}, nil, "", false)
	return err == nil
}

func (p *dnfProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	return engine.Action{
		ID:            fmt.Sprintf("install:dnf:%s", pkg.ID),
		Kind:          engine.ActionInstall,
		Target:        name,
		Summary:       fmt.Sprintf("Install %s via dnf", name),
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"sudo", "dnf", "install", "-y", name},
		Metadata: map[string]string{
			"provider": "dnf",
			"tool":     pkg.ID,
			"pkg":      name,
			"updates":  pkg.Updates,
		},
	}
}

func (p *dnfProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	cmd := []string{"sudo", "dnf", "upgrade", "-y"}
	meta := map[string]string{"provider": "dnf"}
	if len(p.skipSelf) > 0 {
		joined := strings.Join(p.skipSelf, ",")
		cmd = append(cmd, "--exclude="+joined)
		meta["skip_self_updated"] = joined
	}
	return &engine.Action{
		ID:            "upgrade:dnf",
		Kind:          engine.ActionUpgrade,
		Target:        "dnf",
		Summary:       "Upgrade packages via dnf",
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       cmd,
		Metadata:      meta,
	}
}

type miseProvider struct {
	ctx      *app.Context
	runner   CommandRunner
	skipSelf []string
}

func ValidateMiseRef(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return errors.New("empty mise ref")
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "ubi:") {
		return fmt.Errorf("deprecated mise backend ubi: is forbidden (%q); use github:, aqua:, or npm:", trimmed)
	}
	return nil
}

func (p *miseProvider) Name() string    { return "mise" }
func (p *miseProvider) Available() bool { return hasBin(p.runner, "mise") }

func (p *miseProvider) IsInstalled(pkg config.ResolvedTool) bool {
	if pkg.Bin != "" && hasBin(p.runner, pkg.Bin) {
		return true
	}
	if pkg.Bin != "" {
		_, _, err := p.runner.Run([]string{"mise", "which", pkg.Bin}, nil, miseWorkDir(p.ctx), false)
		return err == nil
	}
	return false
}

func (p *miseProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	ref := pkg.Spec.Ref
	if ref == "" {
		ref = pkgNameOf(pkg)
	}
	detail := ""
	if err := ValidateMiseRef(ref); err != nil {
		detail = err.Error()
	}
	return engine.Action{
		ID:      fmt.Sprintf("install:mise:%s", pkg.ID),
		Kind:    engine.ActionInstall,
		Target:  ref,
		Summary: fmt.Sprintf("Install %s globally via mise (%s)", pkg.ID, ref),
		Detail:  detail,
		Risk:    engine.RiskSafe,
		Command: []string{"mise", "use", "-g", ref},
		Metadata: map[string]string{
			"provider": "mise",
			"tool":     pkg.ID,
			"ref":      ref,
			"updates":  pkg.Updates,
		},
	}
}

func (p *miseProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	return p.upgradeActionFromNames(nil, false)
}

func (p *miseProvider) upgradeActionFromNames(outdated []string, alreadyQueried bool) *engine.Action {
	meta := map[string]string{"provider": "mise"}
	if len(p.skipSelf) == 0 && !alreadyQueried {
		return &engine.Action{
			ID:       "upgrade:mise",
			Kind:     engine.ActionUpgrade,
			Target:   "mise",
			Summary:  "Upgrade mise-managed tools",
			Risk:     engine.RiskSafe,
			Command:  []string{"mise", "upgrade"},
			Metadata: meta,
		}
	}
	if !alreadyQueried {
		items, ok := queryMiseOutdated(p.ctx, p.runner)
		if !ok {
			return nil
		}
		for _, item := range items {
			outdated = append(outdated, item.Name)
		}
	}
	kept := outdated
	var excluded []string
	if len(p.skipSelf) > 0 {
		meta["skip_self_updated"] = strings.Join(p.skipSelf, ",")
		kept, excluded = filterOutSelf(outdated, p.skipSelf)
	}
	if len(excluded) > 0 {
		meta["excluded"] = strings.Join(excluded, ",")
	}
	if len(kept) == 0 {
		return nil
	}
	summary := fmt.Sprintf("Upgrade %d mise tools", len(kept))
	if len(p.skipSelf) > 0 {
		summary = fmt.Sprintf("Upgrade %d mise tools (excluding self-updaters)", len(kept))
	}
	return &engine.Action{
		ID:       "upgrade:mise",
		Kind:     engine.ActionUpgrade,
		Target:   "mise",
		Summary:  summary,
		Risk:     engine.RiskSafe,
		Command:  append([]string{"mise", "upgrade"}, kept...),
		Metadata: meta,
	}
}

type ghExtProvider struct {
	ctx      *app.Context
	runner   CommandRunner
	skipSelf []string
}

func (p *ghExtProvider) Name() string    { return "gh-ext" }
func (p *ghExtProvider) Available() bool { return hasBin(p.runner, "gh") }

func (p *ghExtProvider) IsInstalled(pkg config.ResolvedTool) bool {
	name := pkgNameOf(pkg)
	stdout, _, err := p.runner.Run([]string{"gh", "extension", "list"}, nil, "", false)
	if err != nil {
		return false
	}
	for _, line := range splitNonEmptyLines(stdout) {
		fields := strings.Fields(line)
		if slices.Contains(fields, name) {
			return true
		}
		if _, short, ok := strings.Cut(name, "/"); ok && slices.Contains(fields, short) {
			return true
		}
	}
	return false
}

func (p *ghExtProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	name := pkgNameOf(pkg)
	return engine.Action{
		ID:      fmt.Sprintf("install:gh-ext:%s", pkg.ID),
		Kind:    engine.ActionInstall,
		Target:  name,
		Summary: fmt.Sprintf("Install GitHub CLI extension %s", name),
		Risk:    engine.RiskSafe,
		Command: []string{"gh", "extension", "install", name},
		Metadata: map[string]string{
			"provider": "gh-ext",
			"tool":     pkg.ID,
			"pkg":      name,
			"updates":  pkg.Updates,
		},
	}
}

func (p *ghExtProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	return &engine.Action{
		ID:       "upgrade:gh-ext",
		Kind:     engine.ActionUpgrade,
		Target:   "gh-ext",
		Summary:  "Upgrade GitHub CLI extensions",
		Risk:     engine.RiskSafe,
		Command:  []string{"gh", "extension", "upgrade", "--all"},
		Metadata: map[string]string{"provider": "gh-ext"},
	}
}

type scriptProvider struct {
	ctx    *app.Context
	runner CommandRunner
}

func (p *scriptProvider) Name() string    { return "script" }
func (p *scriptProvider) Available() bool { return hasBin(p.runner, "bash") }

func (p *scriptProvider) IsInstalled(pkg config.ResolvedTool) bool {
	return hasBin(p.runner, pkg.Bin)
}

func (p *scriptProvider) InstallAction(pkg config.ResolvedTool) engine.Action {
	risk := engine.RiskMedium
	detail := "Downloads installer to a temporary file and verifies SHA256 before execution"
	if pkg.Spec.SHA256 == "" {
		risk = engine.RiskDangerous
		detail = "Unpinned TOFU script installer; downloads to a temporary file and computes SHA256 before execution"
	}
	return engine.Action{
		ID:            fmt.Sprintf("install:script:%s", pkg.ID),
		Kind:          engine.ActionInstall,
		Target:        pkg.ID,
		Source:        pkg.Spec.URL,
		Summary:       fmt.Sprintf("Install %s via verified script (%s)", pkg.ID, pkg.Spec.URL),
		Detail:        detail,
		Risk:          risk,
		RequiresAllow: "script",
		Metadata: map[string]string{
			"provider": "script",
			"tool":     pkg.ID,
			"url":      pkg.Spec.URL,
			"sha256":   pkg.Spec.SHA256,
			"tofu":     strconv.FormatBool(pkg.Spec.TOFU),
			"updates":  pkg.Updates,
		},
	}
}

func (p *scriptProvider) UpgradeAction(all bool) *engine.Action {
	_ = all
	return nil
}

func isAllowedScriptURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	isLocalhost := host == "127.0.0.1" || host == "localhost" || host == "::1"
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && isLocalhost)
}

func hardenedHTTPClient(client *http.Client) *http.Client {
	var c http.Client
	if client != nil {
		c = *client
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	prevCheck := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !isAllowedScriptURL(req.URL) {
			return fmt.Errorf("refusing redirect to non-https URL %q", req.URL.String())
		}
		if prevCheck != nil {
			return prevCheck(req, via)
		}
		return nil
	}
	return &c
}

func DownloadAndVerifyScript(rawURL, expectedSHA256 string, client *http.Client) (tmpPath string, actualSHA256 string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid script URL %q", rawURL)
	}
	if !isAllowedScriptURL(parsed) {
		return "", "", fmt.Errorf("script URL must use https: %q", rawURL)
	}
	httpClient := hardenedHTTPClient(client)

	resp, err := httpClient.Get(parsed.String())
	if err != nil {
		return "", "", fmt.Errorf("download script %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("download script %s: HTTP %d", rawURL, resp.StatusCode)
	}

	f, err := os.CreateTemp("", "ws-script-*.sh")
	if err != nil {
		return "", "", err
	}
	tmpPath = f.Name()
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}

	if err := f.Chmod(0o600); err != nil {
		cleanup()
		return "", "", err
	}

	const maxScriptBytes = 10 << 20
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxScriptBytes+1))
	if err != nil {
		cleanup()
		return "", "", fmt.Errorf("save script %s: %w", rawURL, err)
	}
	if n > maxScriptBytes {
		cleanup()
		return "", "", fmt.Errorf("script %s exceeds 10 MiB limit", rawURL)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", "", err
	}

	actualSHA256 = hex.EncodeToString(h.Sum(nil))
	if want := strings.TrimSpace(expectedSHA256); want != "" {
		if !strings.EqualFold(actualSHA256, want) {
			_ = os.Remove(tmpPath)
			return "", actualSHA256, fmt.Errorf("script SHA256 mismatch for %s: expected %s, got %s", rawURL, want, actualSHA256)
		}
	}
	return tmpPath, actualSHA256, nil
}

func verifyAndRunScriptWithPrior(ctx *app.Context, spec config.InstallSpec, toolID, priorSHA256 string, client *http.Client, runner CommandRunner) (sha256Hex string, skipped bool, warning string, err error) {
	if ctx == nil {
		return "", false, "", errors.New("nil context")
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	unpinned := strings.TrimSpace(spec.SHA256) == ""
	if unpinned && !spec.TOFU {
		return "", false, "", fmt.Errorf("script installer for %s requires sha256 pin or tofu=true", toolID)
	}
	if !ctx.Allow.Script {
		if unpinned && spec.TOFU {
			return "", true, fmt.Sprintf("skipped unpinned TOFU script for %s (requires --allow script)", toolID), nil
		}
		return "", true, fmt.Sprintf("skipped script installer for %s (requires --allow script)", toolID), nil
	}

	tmpPath, sum, err := DownloadAndVerifyScript(spec.URL, spec.SHA256, client)
	if err != nil {
		return sum, false, "", err
	}
	defer os.Remove(tmpPath)

	if prior := strings.TrimSpace(priorSHA256); unpinned && prior != "" && !strings.EqualFold(sum, prior) {
		return sum, false, "", fmt.Errorf("TOFU SHA256 mismatch for %s: ledger recorded %s, got %s", toolID, prior, sum)
	}

	if ctx.DryRun {
		return sum, true, fmt.Sprintf("dry-run: downloaded and verified %s (sha256=%s)", toolID, sum), nil
	}

	if _, stderr, runErr := runner.Run([]string{"bash", tmpPath}, nil, "", ctx.ChildTTY()); runErr != nil {
		msg := strings.TrimSpace(stderr)
		if msg != "" {
			return sum, false, "", fmt.Errorf("script install %s failed: %w: %s", toolID, runErr, msg)
		}
		return sum, false, "", fmt.Errorf("script install %s failed: %w", toolID, runErr)
	}
	return sum, false, "", nil
}
