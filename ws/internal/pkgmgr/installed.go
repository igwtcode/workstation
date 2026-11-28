package pkgmgr

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
)

type TimedExecRunner struct {
	Timeout time.Duration
}

func (r TimedExecRunner) Run(cmd []string, env map[string]string, dir string, needsTTY bool) (string, string, error) {
	if len(cmd) == 0 {
		return "", "", errors.New("empty command")
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.WaitDelay = 500 * time.Millisecond
	if dir != "" {
		c.Dir = dir
	}
	c.Env = app.ProbeEnv(app.PinnedMachineEnv(os.Environ()))
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

func (TimedExecRunner) LookPath(bin string) (string, error) {
	return exec.LookPath(bin)
}

func LoadBrewInstalledSet(runner CommandRunner, cask bool) (map[string]bool, bool) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	if !hasBin(runner, "brew") {
		return nil, false
	}
	flag := "--formula"
	corePrefix := "homebrew/core/"
	if cask {
		flag = "--cask"
		corePrefix = "homebrew/cask/"
	}
	stdout, _, err := runner.Run([]string{"brew", "list", flag, "--full-name"}, brewCommandEnv(), "", false)
	if err != nil {
		return nil, false
	}
	return buildBrewInstalledSet(stdout, corePrefix), true
}

func buildBrewInstalledSet(stdout, corePrefix string) map[string]bool {
	set := map[string]bool{}
	for _, line := range splitNonEmptyLines(stdout) {
		lower := strings.ToLower(strings.TrimSpace(line))
		if lower == "" {
			continue
		}
		set[lower] = true
		if strings.HasPrefix(lower, corePrefix) {
			set[strings.TrimPrefix(lower, corePrefix)] = true
		}
	}
	return set
}

func MatchBrewInstalled(set map[string]bool, name string, cask bool) bool {
	corePrefix := "homebrew/core/"
	if cask {
		corePrefix = "homebrew/cask/"
	}
	return matchBrewInstalled(set, name, corePrefix)
}

func matchBrewInstalled(set map[string]bool, name, corePrefix string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "/") {
		if set[lower] {
			return true
		}
		if strings.HasPrefix(lower, corePrefix) {
			return set[strings.TrimPrefix(lower, corePrefix)]
		}
		return false
	}
	return set[lower]
}

func LoadArchInstalledSet(runner CommandRunner, fallbackBin string) (map[string]bool, bool) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	bin := ""
	if hasBin(runner, "pacman") {
		bin = "pacman"
	} else if fallbackBin != "" && hasBin(runner, fallbackBin) {
		bin = fallbackBin
	}
	if bin == "" {
		return nil, false
	}
	stdout, _, err := runner.Run([]string{bin, "-Qq"}, nil, "", false)
	if err != nil {
		return nil, false
	}
	set := map[string]bool{}
	for _, line := range splitNonEmptyLines(stdout) {
		set[line] = true
		if low := strings.ToLower(line); low != line {
			set[low] = true
		}
	}
	return set, true
}

func LoadDpkgInstalledSet(runner CommandRunner) (map[string]bool, bool) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	if !hasBin(runner, "dpkg-query") {
		return nil, false
	}
	stdout, _, err := runner.Run([]string{"dpkg-query", "-W", "-f=${Status}\t${Package}\n"}, nil, "", false)
	if err != nil {
		return nil, false
	}
	set := map[string]bool{}
	for _, line := range splitNonEmptyLines(stdout) {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "install ok installed" {
			if name := strings.TrimSpace(parts[1]); name != "" {
				set[name] = true
				if low := strings.ToLower(name); low != name {
					set[low] = true
				}
			}
		}
	}
	return set, true
}
