package insights

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/shell"
)

type HistoryEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	DurationMs int64     `json:"durationMs"`
	ExitCode   int       `json:"exitCode"`
	Shell      string    `json:"shell"`
	Zone       string    `json:"zone,omitempty"`
	Command    string    `json:"command"`
}

func SearchHistory(ctx *app.Context, query string, limit int) ([]HistoryEntry, string, error) {
	if limit <= 0 {
		limit = 100
	}
	if ctx == nil || ctx.HomeDir == "" {
		return nil, "none", nil
	}

	zoneRoots := buildZoneRootMap(ctx)

	if entries, ok := searchAtuin(ctx, query, limit, zoneRoots); ok {
		return entries, "atuin", nil
	}

	type rankedEntry struct {
		entry HistoryEntry
		order int
	}

	var all []rankedEntry
	seq := 0

	for _, e := range parseZshHistory(resolveZshHistoryPath(ctx)) {
		seq++
		all = append(all, rankedEntry{entry: e, order: seq})
	}
	for _, e := range parseBashHistory(filepath.Join(ctx.HomeDir, ".bash_history")) {
		seq++
		all = append(all, rankedEntry{entry: e, order: seq})
	}

	q := strings.ToLower(strings.TrimSpace(query))
	filtered := make([]rankedEntry, 0, len(all))
	for _, item := range all {
		if q != "" && !strings.Contains(strings.ToLower(item.entry.Command), q) {
			continue
		}
		filtered = append(filtered, item)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		ti, tj := filtered[i].entry.Timestamp, filtered[j].entry.Timestamp
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return filtered[i].order > filtered[j].order
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	result := make([]HistoryEntry, len(filtered))
	for i, item := range filtered {
		result[i] = item.entry
	}
	return result, "history-files", nil
}

func buildZoneRootMap(ctx *app.Context) map[string]string {
	out := map[string]string{}
	if specs, err := shell.ParseZones(ctx); err == nil {
		for _, s := range specs {
			out[s.Name] = filepath.Clean(filepath.Join(ctx.HomeDir, s.Name))
		}
	}
	if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			zroot := filepath.Join(ctx.HomeDir, e.Name())
			if envMap, err := shell.ReadZoneMiseEnv(zroot); err == nil {
				if z := envMap["WS_ZONE"]; z != "" {
					out[z] = filepath.Clean(zroot)
				}
			}
		}
	}
	return out
}

func matchZoneForDir(dir string, zoneRoots map[string]string) string {
	if dir == "" {
		return ""
	}
	clean := filepath.Clean(dir)
	for z, root := range zoneRoots {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return z
		}
	}
	return ""
}

func searchAtuin(ctx *app.Context, query string, limit int, zoneRoots map[string]string) ([]HistoryEntry, bool) {
	atuinBin, err := exec.LookPath("atuin")
	if err != nil {
		return nil, false
	}

	dbPath := ctx.LocalEnv["ATUIN_DB_PATH"]
	if dbPath == "" {
		dataDir := ctx.DataDir
		if dataDir == "" {
			dataDir = filepath.Join(ctx.HomeDir, ".local", "share")
		}
		dbPath = filepath.Join(dataDir, "atuin", "history.db")
	}

	if _, err := os.Stat(dbPath); err != nil {
		return nil, false
	}

	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	args := []string{
		"search",
		"--reverse",
		"--search-mode", "full-text",
		"--format", "{time}\t{duration}\t{exit}\t{directory}\t{command}",
		"--limit", strconv.Itoa(limit * 2),
	}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "--", q)
	}

	cmd := exec.CommandContext(cctx, atuinBin, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = shell.MergeEnv(os.Environ(), []string{
		"HOME=" + ctx.HomeDir,
		"XDG_CONFIG_HOME=" + ctx.ConfigDir,
		"XDG_DATA_HOME=" + ctx.DataDir,
		"ATUIN_DB_PATH=" + dbPath,
	})
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}

	q := strings.ToLower(strings.TrimSpace(query))
	var entries []HistoryEntry
	sc := bufio.NewScanner(bytes.NewReader(out))
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 2*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 5 {
			continue
		}
		cmdText := strings.TrimSpace(parts[4])
		if cmdText == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(cmdText), q) {
			continue
		}
		ts := parseFlexibleTime(parts[0])
		durMs := parseFlexibleDurationMs(parts[1])
		exitCode, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		zone := matchZoneForDir(strings.TrimSpace(parts[3]), zoneRoots)

		entries = append(entries, HistoryEntry{
			Timestamp:  ts,
			DurationMs: durMs,
			ExitCode:   exitCode,
			Shell:      "atuin",
			Zone:       zone,
			Command:    cmdText,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, true
}

func parseFlexibleTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	if epoch, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(epoch, 0).UTC()
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func resolveZshHistoryPath(ctx *app.Context) string {
	fallback := filepath.Join(ctx.HomeDir, ".zsh_history")
	realHome, _ := os.UserHomeDir()
	cleanHome := filepath.Clean(ctx.HomeDir)
	sameAsRealHome := cleanHome != "" && realHome != "" && cleanHome == filepath.Clean(realHome)

	if envHist := strings.TrimSpace(os.Getenv("HISTFILE")); envHist != "" {
		expanded := filepath.Clean(ctx.ExpandHome(envHist))
		withinCtxHome := cleanHome != "" && (expanded == cleanHome || strings.HasPrefix(expanded, cleanHome+string(filepath.Separator)))
		if sameAsRealHome || withinCtxHome {
			return expanded
		}
	}
	if ctx.LocalEnv != nil {
		if localHist := strings.TrimSpace(ctx.LocalEnv["HISTFILE"]); localHist != "" {
			return filepath.Clean(ctx.ExpandHome(localHist))
		}
	}
	return fallback
}

func parseFlexibleDurationMs(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" || s == "-" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d.Milliseconds()
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return 0
}

func parseZshHistory(path string) []HistoryEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []HistoryEntry
	reader := bufio.NewReaderSize(f, 64*1024)
	var pending *HistoryEntry

	flush := func() {
		if pending != nil {
			pending.Command = strings.TrimSpace(pending.Command)
			if pending.Command != "" {
				entries = append(entries, *pending)
			}
			pending = nil
		}
	}

	for {
		rawLine, err := reader.ReadBytes('\n')
		if len(rawLine) > 0 {
			line := string(unmetafyZsh(bytes.TrimRight(rawLine, "\r\n")))
			if pending != nil {
				if hasOddTrailingBackslashes(line) {
					pending.Command += "\n" + strings.TrimSuffix(line, `\`)
				} else {
					pending.Command += "\n" + line
					flush()
				}
			} else if ts, durMs, cmd, ok := parseZshExtendedLine(line); ok {
				e := HistoryEntry{
					Timestamp:  ts,
					DurationMs: durMs,
					Shell:      "zsh",
					Command:    cmd,
				}
				if hasOddTrailingBackslashes(cmd) {
					e.Command = strings.TrimSuffix(cmd, `\`)
					pending = &e
				} else if strings.TrimSpace(e.Command) != "" {
					entries = append(entries, e)
				}
			} else if strings.TrimSpace(line) != "" {
				e := HistoryEntry{
					Shell:   "zsh",
					Command: line,
				}
				if hasOddTrailingBackslashes(line) {
					e.Command = strings.TrimSuffix(line, `\`)
					pending = &e
				} else {
					entries = append(entries, e)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	flush()
	return entries
}

func hasOddTrailingBackslashes(s string) bool {
	cnt := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		cnt++
	}
	return cnt%2 == 1
}

func parseZshExtendedLine(line string) (time.Time, int64, string, bool) {
	if !strings.HasPrefix(line, ": ") {
		return time.Time{}, 0, "", false
	}
	rest := line[2:]
	semi := strings.IndexByte(rest, ';')
	if semi <= 0 {
		return time.Time{}, 0, "", false
	}
	meta := rest[:semi]
	cmd := rest[semi+1:]
	epochStr, durStr, ok := strings.Cut(meta, ":")
	if !ok {
		return time.Time{}, 0, "", false
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(epochStr), 10, 64)
	if err != nil {
		return time.Time{}, 0, "", false
	}
	durSec, err := strconv.ParseInt(strings.TrimSpace(durStr), 10, 64)
	if err != nil {
		durSec = 0
	}
	return time.Unix(epoch, 0).UTC(), durSec * 1000, cmd, true
}

func unmetafyZsh(in []byte) []byte {
	if bytes.IndexByte(in, 0x83) < 0 {
		return in
	}
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		if in[i] == 0x83 && i+1 < len(in) {
			i++
			out = append(out, in[i]^0x20)
		} else {
			out = append(out, in[i])
		}
	}
	return out
}

func parseBashHistory(path string) []HistoryEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []HistoryEntry
	var pendingTS time.Time
	reader := bufio.NewReaderSize(f, 64*1024)

	for {
		rawLine, err := reader.ReadBytes('\n')
		if len(rawLine) > 0 {
			line := string(bytes.TrimRight(rawLine, "\r\n"))
			if strings.HasPrefix(line, "#") && len(line) > 1 {
				if epoch, perr := strconv.ParseInt(strings.TrimSpace(line[1:]), 10, 64); perr == nil && epoch > 0 {
					pendingTS = time.Unix(epoch, 0).UTC()
					goto next
				}
			}
			if cmd := strings.TrimSpace(line); cmd != "" {
				entries = append(entries, HistoryEntry{
					Timestamp: pendingTS,
					Shell:     "bash",
					Command:   cmd,
				})
			}
			pendingTS = time.Time{}
		}
	next:
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return entries
}
