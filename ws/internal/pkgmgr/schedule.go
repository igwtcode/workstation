package pkgmgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
)

const (
	launchdLabel           = "dev.workstation.ws-digest"
	launchdDBBackupLabel   = "dev.workstation.ws-db-backup"
	systemdService         = "ws-digest.service"
	systemdTimer           = "ws-digest.timer"
	systemdDBBackupService = "ws-db-backup.service"
	systemdDBBackupTimer   = "ws-db-backup.timer"
)

type ScheduleOptions struct {
	IntervalHours int
	WithDBBackup  bool
}

type ScheduleFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ScheduleSpec struct {
	Backend       string         `json:"backend"`
	Label         string         `json:"label"`
	IntervalHours int            `json:"intervalHours"`
	WithDBBackup  bool           `json:"withDbBackup,omitempty"`
	Command       []string       `json:"command"`
	Files         []ScheduleFile `json:"files"`
}

type ScheduleStatus struct {
	Backend       string   `json:"backend"`
	Installed     bool     `json:"installed"`
	Active        bool     `json:"active"`
	IntervalHours int      `json:"intervalHours,omitempty"`
	Files         []string `json:"files,omitempty"`
	Detail        string   `json:"detail,omitempty"`
}

const defaultScheduleIntervalHours = 12

func RenderScheduleFiles(ctx *app.Context, intervalHours int) (ScheduleSpec, error) {
	return RenderScheduleFilesWithOptions(ctx, ScheduleOptions{IntervalHours: intervalHours})
}

func RenderScheduleFilesWithOptions(ctx *app.Context, opts ScheduleOptions) (ScheduleSpec, error) {
	intervalHours := opts.IntervalHours
	if intervalHours <= 0 {
		intervalHours = defaultScheduleIntervalHours
	}
	outPath := ctx.WSDigestPath()
	cmd := []string{"ws", "update", "--digest", "--json", "--output", outPath}
	execPrefix := resolveWSCommand()
	var repoArgs []string
	if ctx != nil && strings.TrimSpace(ctx.RepoRoot) != "" {
		repoArgs = []string{"--repo", ctx.RepoRoot}
	}
	fullArgs := append(append([]string{}, execPrefix...), append(slices.Clone(repoArgs), "update", "--digest", "--json", "--output", outPath)...)
	backupArgs := append(append([]string{}, execPrefix...), append(slices.Clone(repoArgs), "db", "backup", "--if-changed")...)
	envPairs := scheduleEnvPairs(ctx)

	switch ctx.Host.OS {
	case "darwin":
		plistPath := filepath.Join(ctx.HomeDir, "Library", "LaunchAgents", launchdLabel+".plist")
		errPath := filepath.Join(ctx.WSLogsDir(), "digest.err.log")
		intervalSec := intervalHours * 3600

		files := []ScheduleFile{
			{
				Path:    plistPath,
				Content: renderLaunchdPlist(launchdLabel, fullArgs, envPairs, intervalSec, errPath),
			},
		}
		if opts.WithDBBackup {
			backupPlist := filepath.Join(ctx.HomeDir, "Library", "LaunchAgents", launchdDBBackupLabel+".plist")
			backupErr := filepath.Join(ctx.WSLogsDir(), "db-backup.err.log")
			files = append(files, ScheduleFile{
				Path:    backupPlist,
				Content: renderLaunchdPlist(launchdDBBackupLabel, backupArgs, envPairs, intervalSec, backupErr),
			})
		}

		return ScheduleSpec{
			Backend:       "launchd",
			Label:         launchdLabel,
			IntervalHours: intervalHours,
			WithDBBackup:  opts.WithDBBackup,
			Command:       cmd,
			Files:         files,
		}, nil

	case "linux":
		unitDir := filepath.Join(ctx.ConfigDir, "systemd", "user")
		servicePath := filepath.Join(unitDir, systemdService)
		timerPath := filepath.Join(unitDir, systemdTimer)

		serviceContent := renderSystemdService("Workstation read-only update digest", fullArgs, envPairs)
		timerContent := renderSystemdTimer("Periodic workstation read-only update digest", "15m", intervalHours)

		files := []ScheduleFile{
			{Path: servicePath, Content: serviceContent},
			{Path: timerPath, Content: timerContent},
		}
		if opts.WithDBBackup {
			files = append(files,
				ScheduleFile{
					Path:    filepath.Join(unitDir, systemdDBBackupService),
					Content: renderSystemdService("Workstation diff-gated encrypted ws.db backup", backupArgs, envPairs),
				},
				ScheduleFile{
					Path:    filepath.Join(unitDir, systemdDBBackupTimer),
					Content: renderSystemdTimer("Periodic workstation diff-gated ws.db backup", "20m", intervalHours),
				},
			)
		}

		return ScheduleSpec{
			Backend:       "systemd",
			Label:         systemdTimer,
			IntervalHours: intervalHours,
			WithDBBackup:  opts.WithDBBackup,
			Command:       cmd,
			Files:         files,
		}, nil

	default:
		return ScheduleSpec{}, fmt.Errorf("unsupported OS for scheduling: %q", ctx.Host.OS)
	}
}

func scheduleEnvPairs(ctx *app.Context) [][2]string {
	pathEntries := []string{}
	if ctx != nil && ctx.HomeDir != "" {
		pathEntries = append(pathEntries, filepath.Join(ctx.HomeDir, ".local", "bin"))
	}
	pathEntries = append(pathEntries, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin")
	pairs := [][2]string{
		{"PATH", strings.Join(pathEntries, ":")},
	}
	if ctx == nil {
		return pairs
	}
	if ctx.HomeDir != "" {
		pairs = append(pairs, [2]string{"HOME", ctx.HomeDir})
	}
	if wsBase := ctx.WSBaseDir(); wsBase != "" {
		pairs = append(pairs, [2]string{"WS_HOME", wsBase})
	}
	if ctx.RepoRoot != "" {
		pairs = append(pairs, [2]string{"WS_DIR", ctx.RepoRoot})
	}
	if ctx.ConfigDir != "" {
		pairs = append(pairs, [2]string{"XDG_CONFIG_HOME", ctx.ConfigDir})
	}
	if ctx.DataDir != "" {
		pairs = append(pairs, [2]string{"XDG_DATA_HOME", ctx.DataDir})
	}
	if ctx.StateDir != "" {
		pairs = append(pairs, [2]string{"XDG_STATE_HOME", ctx.StateDir})
	}
	if ctx.CacheDir != "" {
		pairs = append(pairs, [2]string{"XDG_CACHE_HOME", ctx.CacheDir})
	}
	return pairs
}

func renderLaunchdPlist(label string, args []string, envPairs [][2]string, intervalSec int, errPath string) string {
	var argLines strings.Builder
	for _, a := range args {
		fmt.Fprintf(&argLines, "    <string>%s</string>\n", xmlEscape(a))
	}
	var envLines strings.Builder
	for _, kv := range envPairs {
		fmt.Fprintf(&envLines, "    <key>%s</key>\n    <string>%s</string>\n", xmlEscape(kv[0]), xmlEscape(kv[1]))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>EnvironmentVariables</key>
  <dict>
%s  </dict>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <false/>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(label), argLines.String(), envLines.String(), intervalSec, xmlEscape(errPath))
}

func renderSystemdService(description string, args []string, envPairs [][2]string) string {
	quotedArgs := make([]string, len(args))
	for i, a := range args {
		quotedArgs[i] = systemdQuoteArg(a)
	}
	var envLines strings.Builder
	for _, kv := range envPairs {
		fmt.Fprintf(&envLines, "Environment=%s\n", systemdQuoteArg(kv[0]+"="+kv[1]))
	}
	return fmt.Sprintf(`[Unit]
Description=%s

[Service]
Type=oneshot
%sExecStart=%s
`, description, envLines.String(), strings.Join(quotedArgs, " "))
}

func renderSystemdTimer(description, onBoot string, intervalHours int) string {
	return fmt.Sprintf(`[Unit]
Description=%s

[Timer]
OnBootSec=%s
OnUnitActiveSec=%dh
Persistent=true

[Install]
WantedBy=timers.target
`, description, onBoot, intervalHours)
}

func resolveWSCommand() []string {
	if exe, err := os.Executable(); err == nil && filepath.IsAbs(exe) {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil && filepath.IsAbs(resolved) {
			exe = resolved
		}
		if filepath.Base(exe) == "ws" {
			return []string{exe}
		}
	}
	return []string{"/usr/bin/env", "ws"}
}

func systemdQuoteArg(arg string) string {
	escaped := strings.NewReplacer("%", "%%", "$", "$$").Replace(arg)
	if strings.ContainsAny(escaped, " \t\"\\") {
		escaped = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(escaped)
		return `"` + escaped + `"`
	}
	return escaped
}

func GetScheduleStatus(ctx *app.Context, runner CommandRunner) (ScheduleStatus, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	spec, err := RenderScheduleFiles(ctx, defaultScheduleIntervalHours)
	if err != nil {
		return ScheduleStatus{}, err
	}

	status := ScheduleStatus{
		Backend: spec.Backend,
	}
	allExist := true
	for _, f := range spec.Files {
		raw, rerr := os.ReadFile(f.Path)
		if rerr != nil {
			allExist = false
			continue
		}
		status.Files = append(status.Files, f.Path)
		if h := parseIntervalHours(spec.Backend, string(raw)); h > 0 {
			status.IntervalHours = h
		}
	}
	status.Installed = allExist && len(spec.Files) > 0

	if fullSpec, ferr := RenderScheduleFilesWithOptions(ctx, ScheduleOptions{
		IntervalHours: defaultScheduleIntervalHours,
		WithDBBackup:  true,
	}); ferr == nil && len(fullSpec.Files) > len(spec.Files) {
		for _, f := range fullSpec.Files[len(spec.Files):] {
			if _, serr := os.Stat(f.Path); serr == nil {
				status.Files = append(status.Files, f.Path)
			}
		}
	}

	if !status.Installed {
		status.Detail = "not installed"
		return status, nil
	}

	switch spec.Backend {
	case "launchd":
		if hasBin(runner, "launchctl") {
			uidDomain := fmt.Sprintf("gui/%d", os.Getuid())
			if _, _, err := runner.Run([]string{"launchctl", "list", launchdLabel}, nil, "", false); err == nil {
				status.Active = true
				status.Detail = "loaded in launchd"
			} else if _, _, err := runner.Run([]string{"launchctl", "print", uidDomain + "/" + launchdLabel}, nil, "", false); err == nil {
				status.Active = true
				status.Detail = "loaded in launchd"
			} else {
				status.Detail = "plist installed but not loaded"
			}
			if len(status.Files) > len(spec.Files) {
				if _, _, err := runner.Run([]string{"launchctl", "list", launchdDBBackupLabel}, nil, "", false); err == nil {
					status.Detail += "; db-backup active"
				} else if _, _, err := runner.Run([]string{"launchctl", "print", uidDomain + "/" + launchdDBBackupLabel}, nil, "", false); err == nil {
					status.Detail += "; db-backup active"
				} else {
					status.Detail += "; db-backup inactive"
				}
			}
		}
	case "systemd":
		if hasBin(runner, "systemctl") {
			stdout, _, err := runner.Run([]string{"systemctl", "--user", "is-active", systemdTimer}, nil, "", false)
			if err == nil && strings.TrimSpace(stdout) == "active" {
				status.Active = true
				status.Detail = "timer active"
			} else {
				status.Detail = "unit installed but timer inactive"
			}
			if len(status.Files) > len(spec.Files) {
				if bOut, _, bErr := runner.Run([]string{"systemctl", "--user", "is-active", systemdDBBackupTimer}, nil, "", false); bErr == nil && strings.TrimSpace(bOut) == "active" {
					status.Detail += "; db-backup timer active"
				} else {
					status.Detail += "; db-backup timer inactive"
				}
			}
		}
	}

	return status, nil
}

func InstallScheduleWithOptions(ctx *app.Context, opts ScheduleOptions, runner CommandRunner) (ScheduleStatus, error) {
	if runner == nil {
		runner = ExecRunner{}
	}
	spec, err := RenderScheduleFilesWithOptions(ctx, opts)
	if err != nil {
		return ScheduleStatus{}, err
	}

	status := ScheduleStatus{
		Backend:       spec.Backend,
		IntervalHours: spec.IntervalHours,
	}
	for _, f := range spec.Files {
		status.Files = append(status.Files, f.Path)
	}
	if ctx.DryRun {
		status.Detail = "dry-run: schedule files not written"
		return status, nil
	}

	if err := os.MkdirAll(ctx.WSCacheDir(), 0o700); err != nil {
		return status, err
	}
	if err := os.MkdirAll(ctx.WSLogsDir(), 0o700); err != nil {
		return status, err
	}
	for _, f := range spec.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return status, err
		}
		if err := os.WriteFile(f.Path, []byte(f.Content), 0o644); err != nil {
			return status, err
		}
	}
	status.Installed = true

	switch spec.Backend {
	case "launchd":
		if hasBin(runner, "launchctl") {
			uidDomain := fmt.Sprintf("gui/%d", os.Getuid())
			for _, f := range spec.Files {
				_, _, _ = runner.Run([]string{"launchctl", "bootout", uidDomain, f.Path}, nil, "", false)
				if _, _, bErr := runner.Run([]string{"launchctl", "bootstrap", uidDomain, f.Path}, nil, "", false); bErr != nil {
					_, _, _ = runner.Run([]string{"launchctl", "unload", f.Path}, nil, "", false)
					if _, stderr, err := runner.Run([]string{"launchctl", "load", "-w", f.Path}, nil, "", false); err != nil {
						return status, fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(stderr))
					}
				}
			}
			status.Active = true
			status.Detail = "installed and loaded"
		}
	case "systemd":
		if hasBin(runner, "systemctl") {
			if _, stderr, err := runner.Run([]string{"systemctl", "--user", "daemon-reload"}, nil, "", false); err != nil {
				return status, fmt.Errorf("systemctl daemon-reload failed: %w: %s", err, strings.TrimSpace(stderr))
			}
			if _, stderr, err := runner.Run([]string{"systemctl", "--user", "enable", "--now", systemdTimer}, nil, "", false); err != nil {
				return status, fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(stderr))
			}
			if opts.WithDBBackup {
				if _, stderr, err := runner.Run([]string{"systemctl", "--user", "enable", "--now", systemdDBBackupTimer}, nil, "", false); err != nil {
					return status, fmt.Errorf("systemctl enable %s failed: %w: %s", systemdDBBackupTimer, err, strings.TrimSpace(stderr))
				}
			}
			status.Active = true
			status.Detail = "installed and enabled"
		}
	}

	return status, nil
}

func RemoveSchedule(ctx *app.Context, runner CommandRunner) error {
	if runner == nil {
		runner = ExecRunner{}
	}
	baseSpec, err := RenderScheduleFiles(ctx, defaultScheduleIntervalHours)
	if err != nil {
		return err
	}
	fullSpec, err := RenderScheduleFilesWithOptions(ctx, ScheduleOptions{
		IntervalHours: defaultScheduleIntervalHours,
		WithDBBackup:  true,
	})
	if err != nil {
		return err
	}
	if ctx.DryRun {
		return nil
	}

	extraFiles := fullSpec.Files[len(baseSpec.Files):]
	hasExtra := false
	for _, f := range extraFiles {
		if _, serr := os.Stat(f.Path); serr == nil {
			hasExtra = true
			break
		}
	}

	switch baseSpec.Backend {
	case "launchd":
		if hasBin(runner, "launchctl") {
			uidDomain := fmt.Sprintf("gui/%d", os.Getuid())
			for _, f := range baseSpec.Files {
				_, _, _ = runner.Run([]string{"launchctl", "bootout", uidDomain, f.Path}, nil, "", false)
				_, _, _ = runner.Run([]string{"launchctl", "unload", f.Path}, nil, "", false)
			}
			if hasExtra {
				for _, f := range extraFiles {
					_, _, _ = runner.Run([]string{"launchctl", "bootout", uidDomain, f.Path}, nil, "", false)
					_, _, _ = runner.Run([]string{"launchctl", "unload", f.Path}, nil, "", false)
				}
			}
		}
	case "systemd":
		if hasBin(runner, "systemctl") {
			_, _, _ = runner.Run([]string{"systemctl", "--user", "disable", "--now", systemdTimer}, nil, "", false)
			if hasExtra {
				_, _, _ = runner.Run([]string{"systemctl", "--user", "disable", "--now", systemdDBBackupTimer}, nil, "", false)
			}
		}
	}

	for _, f := range fullSpec.Files {
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	if baseSpec.Backend == "systemd" && hasBin(runner, "systemctl") {
		_, _, _ = runner.Run([]string{"systemctl", "--user", "daemon-reload"}, nil, "", false)
	}
	return nil
}

var (
	reLaunchdInterval = regexp.MustCompile(`<key>StartInterval</key>\s*<integer>(\d+)</integer>`)
	reSystemdInterval = regexp.MustCompile(`OnUnitActiveSec=(\d+)h`)
)

func parseIntervalHours(backend, content string) int {
	switch backend {
	case "launchd":
		if m := reLaunchdInterval.FindStringSubmatch(content); len(m) == 2 {
			if sec, err := strconv.Atoi(m[1]); err == nil && sec > 0 {
				return sec / 3600
			}
		}
	case "systemd":
		if m := reSystemdInterval.FindStringSubmatch(content); len(m) == 2 {
			if h, err := strconv.Atoi(m[1]); err == nil && h > 0 {
				return h
			}
		}
	}
	return 0
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
