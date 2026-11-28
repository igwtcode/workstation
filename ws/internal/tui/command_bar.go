package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

type barMode int

const (
	barIdle barMode = iota
	barFilter
	barCommand
)

type commandSpec struct {
	Name        string
	Aliases     []string
	Kind        string
	Usage       string
	Description string
	Dangerous   bool
}

type commandInvocation struct {
	Raw  string
	Cmd  string
	Args []string
	KV   map[string]string
}

type paletteOption struct {
	Value       string
	Description string
}

type commandBar struct {
	mode          barMode
	input         textinput.Model
	specs         []commandSpec
	matches       []commandSpec
	matchIdx      int
	argOptions    map[string][]paletteOption
	argMode       bool
	argCmd        string
	argPrefix     string
	argMatches    []paletteOption
	argIdx        int
	cmdHistory    []string
	cmdHistPos    int
	filterHistory []string
	filterHistPos int
	draft         string
	openDraft     string
}

func defaultArgOptions() map[string][]paletteOption {
	return map[string][]paletteOption{
		"zone": {
			{Value: "all", Description: "Show resources from all configured zones"},
			{Value: "personal", Description: "Filter to personal identity zone"},
			{Value: "work", Description: "Filter to work identity zone"},
		},
		"split": {
			{Value: "v", Description: "Side-by-side vertical preview split"},
			{Value: "h", Description: "Stacked horizontal preview split"},
			{Value: "off", Description: "Full-width main pane (hide split preview)"},
		},
		"scan": {
			{Value: "all", Description: "Rescan indexed env files and git repositories"},
			{Value: "env", Description: "Rescan encrypted env files only"},
			{Value: "repos", Description: "Rescan git repositories only"},
		},
		"pull": {
			{Value: "all", Description: "Pull all clean repositories across zones"},
		},
		"gh": {
			{Value: "state:open", Description: "Show open PRs and issues"},
			{Value: "state:closed", Description: "Show closed / merged PRs and issues"},
			{Value: "state:all", Description: "Show all states"},
			{Value: "all:true", Description: "Query all your GitHub PRs/issues across repos"},
		},
		"update": {
			{Value: "all", Description: "Full update including greedy casks (--all)"},
			{Value: "pkg", Description: "Upgrade OS/mise packages only"},
		},
		"apply": {
			{Value: "--dry-run", Description: "Preview template renders, symlinks & hooks without writing"},
		},
		"pkg-search": {
			{Value: "provider:all", Description: "Search all available package repositories"},
			{Value: "provider:brew", Description: "Search Homebrew formulae"},
			{Value: "provider:cask", Description: "Search Homebrew casks"},
			{Value: "provider:paru", Description: "Search Arch official + AUR via paru"},
			{Value: "provider:pacman", Description: "Search Arch official repositories"},
			{Value: "provider:mise", Description: "Search mise tool registry"},
			{Value: "provider:apt", Description: "Search Debian/Ubuntu apt repositories"},
		},
	}
}

func newCommandBar() commandBar {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 256
	ti.Blur()
	cb := commandBar{
		mode:          barIdle,
		input:         ti,
		specs:         defaultCommandSpecs(),
		argOptions:    defaultArgOptions(),
		cmdHistPos:    -1,
		filterHistPos: -1,
	}
	cb.recomputeMatches()
	return cb
}

func (cb *commandBar) SetArgOptions(cmdName string, opts []paletteOption) {
	if cb.argOptions == nil {
		cb.argOptions = map[string][]paletteOption{}
	}
	cp := make([]paletteOption, len(opts))
	copy(cp, opts)
	cb.argOptions[strings.ToLower(strings.TrimSpace(cmdName))] = cp
	if cb.mode == barCommand {
		cb.recomputeMatches()
	}
}

func defaultCommandSpecs() []commandSpec {
	return []commandSpec{
		{Name: "overview", Aliases: []string{"home", "1", "8"}, Kind: "view", Usage: ":overview (or :1)", Description: "1 · Workstation overview dashboard"},
		{Name: "repo", Aliases: []string{"repos", "projects", "2"}, Kind: "view", Usage: ":repo [query] (or :2)", Description: "2 · Git repositories across zones"},
		{Name: "gh", Kind: "view", Usage: ":gh [repo:<slug>] [state:open|closed|all]", Description: "2 · Unified GitHub PRs & Issues"},
		{Name: "prs", Aliases: []string{"pr"}, Kind: "view", Usage: ":prs [repo:<slug>] [state:open|closed|all]", Description: "2 · GitHub pull requests"},
		{Name: "issues", Aliases: []string{"issue"}, Kind: "view", Usage: ":issues [repo:<slug>] [state:open|closed|all]", Description: "2 · GitHub issues"},
		{Name: "packages", Aliases: []string{"pkg", "pkgs", "installed", "system", "sys", "3"}, Kind: "view", Usage: ":packages [preset:<id>] [query] (or :3)", Description: "3 · Installed preset packages & inspector"},
		{Name: "pkg-search", Aliases: []string{"search", "browse", "psearch"}, Kind: "view", Usage: ":search [provider:<name>] <query>", Description: "3 · Search brew, cask, paru/pacman & mise repos"},
		{Name: "mise-tools", Aliases: []string{"mise-outdated"}, Kind: "view", Usage: ":mise-tools [query]", Description: "3 · Mise packages & outdated versions"},
		{Name: "mise", Aliases: []string{"mise-configs"}, Kind: "view", Usage: ":mise [query]", Description: "3 · Global, zone & project mise configs"},
		{Name: "clean", Aliases: []string{"cleanup"}, Kind: "view", Usage: ":clean [query]", Description: "3 · Reclaimable disk caches"},
		{Name: "env", Aliases: []string{"secrets", "vars", "tokens", "4"}, Kind: "view", Usage: ":env [query] (or :4)", Description: "4 · Environment variables & secrets"},
		{Name: "envfiles", Aliases: []string{"files"}, Kind: "view", Usage: ":envfiles", Description: "4 · Indexed env/mise config files"},
		{Name: "vault", Aliases: []string{"db", "1password", "op"}, Kind: "view", Usage: ":vault", Description: "4 · ws.db & 1Password backup/restore manager"},
		{Name: "fleet", Aliases: []string{"nodes", "5"}, Kind: "view", Usage: ":fleet [query] (or :5)", Description: "5 · Tailscale & cloud fleet nodes"},
		{Name: "filesync", Aliases: []string{"sync-files", "diff-files"}, Kind: "view", Usage: ":filesync", Description: "5 · Diff & sync config/secret files over Tailscale"},
		{Name: "workstation", Aliases: []string{"ws", "6"}, Kind: "view", Usage: ":workstation (or :6)", Description: "6 · Workstation diagnostics, themes, presets & settings"},
		{Name: "doctor", Aliases: []string{"doc"}, Kind: "view", Usage: ":doctor", Description: "6 · Diagnostics, links & fixes"},
		{Name: "themes", Aliases: []string{"theme"}, Kind: "view", Usage: ":themes [id]", Description: "6 · Palettes & themed app templates"},
		{Name: "presets", Aliases: []string{"preset"}, Kind: "view", Usage: ":presets [id]", Description: "6 · Switch or inspect machine preset"},
		{Name: "config", Aliases: []string{"settings", "prefs", "7"}, Kind: "view", Usage: ":config [query]", Description: "6 · TUI & workstation settings, overrides & auto-reload"},
		{Name: "watched", Aliases: []string{"watched-files"}, Kind: "view", Usage: ":watched [query]", Description: "6 · Watched config files & auto-reload status"},
		{Name: "hist", Aliases: []string{"history"}, Kind: "view", Usage: ":hist [query]", Description: "6 · Unified zsh/bash/atuin shell history"},
		{Name: "out", Aliases: []string{"0", "output", "jobs"}, Kind: "view", Usage: ":out (or :0)", Description: "0 · Command output history console"},
		{Name: "zone", Aliases: []string{"z"}, Kind: "context", Usage: ":zone <all|personal|work>", Description: "Set active zone scope"},
		{Name: "split", Kind: "context", Usage: ":split <v|h|off>", Description: "Set vertical/horizontal/off preview split"},
		{Name: "zoom", Aliases: []string{"zpane"}, Kind: "view", Usage: ":zoom", Description: "Toggle full-width zoom on focused split pane"},
		{Name: "clear", Kind: "context", Usage: ":clear", Description: "Clear active filter and selection"},
		{Name: "reload", Kind: "action", Usage: ":reload", Description: "Reload ~/.ws/config.toml, mise.local.toml & theme now"},
		{Name: "scan", Kind: "action", Usage: ":scan [env|repos|all]", Description: "Rescan indexed env files & repos"},
		{Name: "update", Aliases: []string{"upgrade-all"}, Kind: "action", Usage: ":update [all|pkg]", Description: "Run parallel workstation update across packages, tools & dotfiles", Dangerous: true},
		{Name: "status", Kind: "action", Usage: ":status", Description: "Run workstation diagnostics into Output"},
		{Name: "backup", Kind: "action", Usage: ":backup", Description: "Encrypt & back up ws.db into Output"},
		{Name: "pull", Kind: "action", Usage: ":pull [all]", Description: "Pull selected or all repos into Output", Dangerous: true},
		{Name: "clean-exec", Kind: "action", Usage: ":clean-exec", Description: "Execute marked cleanup items", Dangerous: true},
		{Name: "fix", Kind: "action", Usage: ":fix", Description: "Apply selected Doctor fix into Output", Dangerous: true},
		{Name: "apply", Kind: "action", Usage: ":apply [--dry-run]", Description: "Run ws apply (templates, symlinks & hooks) into Output", Dangerous: true},
		{Name: "undo", Kind: "action", Usage: ":undo", Description: "Roll back last transaction", Dangerous: true},
		{Name: "!ws", Aliases: []string{"!"}, Kind: "action", Usage: ":!ws <subcommand...>", Description: "Run arbitrary ws subcommand into Output"},
		{Name: "quit", Aliases: []string{"q"}, Kind: "context", Usage: ":quit", Description: "Exit ws TUI"},
	}
}

func (cb commandBar) Active() bool { return cb.mode != barIdle }
func (cb commandBar) Mode() barMode {
	return cb.mode
}

func (cb commandBar) OpenCommand(initial string) (commandBar, tea.Cmd) {
	cb.mode = barCommand
	cb.cmdHistPos = -1
	cb.draft = ""
	cb.openDraft = ""
	cb.matchIdx = 0
	cb.input.Prompt = ":"
	cb.input.SetValue(initial)
	cb.input.CursorEnd()
	cb.recomputeMatches()
	return cb, cb.input.Focus()
}

func (cb commandBar) OpenFilter(current string) (commandBar, tea.Cmd) {
	cb.mode = barFilter
	cb.filterHistPos = -1
	cb.draft = current
	cb.openDraft = current
	cb.input.Prompt = "/"
	cb.input.SetValue(current)
	cb.input.CursorEnd()
	return cb, cb.input.Focus()
}

func (cb commandBar) Close() commandBar {
	cb.mode = barIdle
	cb.cmdHistPos = -1
	cb.filterHistPos = -1
	cb.input.Blur()
	cb.input.SetValue("")
	return cb
}

func (cb *commandBar) recordCommand(raw string) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, ":"))
	if raw == "" {
		return
	}
	cb.cmdHistory = appendDedupMRU(cb.cmdHistory, raw, 50)
}

func (cb *commandBar) recordFilter(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	cb.filterHistory = appendDedupMRU(cb.filterHistory, raw, 50)
}

func appendDedupMRU(hist []string, val string, capSize int) []string {
	out := make([]string, 0, len(hist)+1)
	for _, h := range hist {
		if h != val {
			out = append(out, h)
		}
	}
	out = append(out, val)
	if capSize > 0 && len(out) > capSize {
		out = out[len(out)-capSize:]
	}
	return out
}

func (cb commandBar) resolveCommandSpec(head string) (commandSpec, bool) {
	lower := strings.ToLower(strings.TrimSpace(head))
	if lower == "" {
		return commandSpec{}, false
	}
	for _, sp := range cb.specs {
		if strings.EqualFold(sp.Name, lower) || containsFold(sp.Aliases, lower) {
			return sp, true
		}
	}
	return commandSpec{}, false
}

func filterArgOptions(opts []paletteOption, prefix string) []paletteOption {
	if len(opts) == 0 {
		return nil
	}
	q := strings.TrimSpace(prefix)
	if q == "" {
		return append([]paletteOption(nil), opts...)
	}
	qLower := strings.ToLower(q)
	type scoredOpt struct {
		opt   paletteOption
		score int
		idx   int
	}
	var matched []scoredOpt
	for i, o := range opts {
		vLower := strings.ToLower(o.Value)
		best := -1
		if vLower == qLower {
			best = 2000
		} else if strings.HasPrefix(vLower, qLower) {
			best = 1000 - len(vLower)
		} else if r := fuzzy.Match(o.Value, q, fuzzy.SchemeDefault, false); r.Matched {
			best = 200 + r.Score
		} else if r := fuzzy.Match(o.Description, q, fuzzy.SchemeDefault, false); r.Matched {
			best = r.Score
		}
		if best >= 0 {
			matched = append(matched, scoredOpt{opt: o, score: best, idx: i})
		}
	}
	if len(matched) == 0 {
		return nil
	}
	for i := 0; i < len(matched); i++ {
		for j := i + 1; j < len(matched); j++ {
			if matched[j].score > matched[i].score || (matched[j].score == matched[i].score && matched[j].idx < matched[i].idx) {
				matched[i], matched[j] = matched[j], matched[i]
			}
		}
	}
	out := make([]paletteOption, len(matched))
	for i, m := range matched {
		out[i] = m.opt
	}
	return out
}

func (cb *commandBar) recomputeMatches() {
	raw := strings.TrimLeft(strings.TrimPrefix(cb.input.Value(), ":"), " \t")
	cb.argMode = false
	cb.argCmd = ""
	cb.argPrefix = ""
	cb.argMatches = nil

	if spIdx := strings.IndexAny(raw, " \t"); spIdx >= 0 {
		head := raw[:spIdx]
		rest := raw[spIdx+1:]
		if spec, ok := cb.resolveCommandSpec(head); ok && !strings.HasPrefix(head, "!") {
			if opts := cb.argOptions[spec.Name]; len(opts) > 0 {
				argPref := rest
				if lastSp := strings.LastIndexAny(rest, " \t"); lastSp >= 0 {
					argPref = rest[lastSp+1:]
				}
				argMatches := filterArgOptions(opts, argPref)
				cb.matches = []commandSpec{spec}
				cb.matchIdx = 0
				if len(argMatches) > 0 {
					cb.argMode = true
					cb.argCmd = spec.Name
					cb.argPrefix = argPref
					cb.argMatches = argMatches
					cb.argIdx = clamp(cb.argIdx, 0, len(argMatches)-1)
				} else {
					cb.argIdx = 0
				}
				return
			}
		}
	}

	head := strings.TrimSpace(raw)
	if sp := strings.IndexFunc(head, unicode.IsSpace); sp >= 0 {
		head = head[:sp]
	}
	if head == "" {
		cb.matches = append([]commandSpec(nil), cb.specs...)
		cb.matchIdx = clamp(cb.matchIdx, 0, max(0, len(cb.matches)-1))
		return
	}
	if strings.HasPrefix(head, "!") {
		cb.matches = nil
		for _, sp := range cb.specs {
			if sp.Name == "!ws" {
				cb.matches = append(cb.matches, sp)
			}
		}
		cb.matchIdx = 0
		return
	}

	cb.matches = fuzzy.Filter(cb.specs, head, func(sp commandSpec) ([]string, map[string]string) {
		fields := append([]string{sp.Name}, sp.Aliases...)
		fields = append(fields, sp.Description)
		return fields, map[string]string{"kind": sp.Kind}
	})
	cb.matchIdx = clamp(cb.matchIdx, 0, max(0, len(cb.matches)-1))
}

func (cb commandBar) ghostSuggestion() string {
	cur := cb.input.Value()
	if cb.mode == barFilter {
		if cur == "" {
			if len(cb.filterHistory) > 0 {
				return cb.filterHistory[len(cb.filterHistory)-1]
			}
			return ""
		}
		if sfx := runePrefixGhostSuffix(cb.filterHistory, cur); sfx != "" {
			return sfx
		}
		return ""
	}
	if cb.mode != barCommand {
		return ""
	}
	rawNoColon := strings.TrimPrefix(cur, ":")
	if strings.TrimSpace(rawNoColon) == "" {
		return ""
	}
	if cb.argMode && len(cb.argMatches) > 0 {
		optVal := cb.argMatches[clamp(cb.argIdx, 0, len(cb.argMatches)-1)].Value
		if cb.argPrefix == "" {
			return optVal
		}
		if sfx := runePrefixGhostSuffix([]string{optVal}, cb.argPrefix); sfx != "" {
			return sfx
		}
		return ""
	}
	trimmed := strings.TrimSpace(rawNoColon)
	if !strings.ContainsAny(rawNoColon, " \t") && len(cb.matches) > 0 {
		idx := clamp(cb.matchIdx, 0, len(cb.matches)-1)
		cand := cb.matches[idx].Name
		return runePrefixGhostSuffix([]string{cand}, trimmed)
	}
	return ""
}

func runePrefixGhostSuffix(candidates []string, prefix string) string {
	prefRunes := []rune(strings.ToLower(prefix))
	if len(prefRunes) == 0 {
		return ""
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		cand := candidates[i]
		cRunes := []rune(cand)
		lowerRunes := []rune(strings.ToLower(cand))
		if len(cRunes) != len(lowerRunes) || len(cRunes) <= len(prefRunes) {
			continue
		}
		matched := true
		for j, pr := range prefRunes {
			if lowerRunes[j] != pr {
				matched = false
				break
			}
		}
		if matched {
			return string(cRunes[len(prefRunes):])
		}
	}
	return ""
}

func commandAcceptsArg(name string) bool {
	switch name {
	case "!ws", "zone", "themes", "presets", "packages", "pkg", "pkgs", "gh", "prs", "issues", "split", "scan", "pull", "update", "apply":
		return true
	default:
		return false
	}
}

func (cb *commandBar) applyAutocomplete() {
	if cb.mode == barCommand && cb.argMode && len(cb.argMatches) > 0 {
		opt := cb.argMatches[clamp(cb.argIdx, 0, len(cb.argMatches)-1)].Value
		cur := cb.input.Value()
		if cb.argPrefix == "" {
			cb.input.SetValue(cur + opt)
		} else if strings.HasSuffix(cur, cb.argPrefix) {
			cb.input.SetValue(cur[:len(cur)-len(cb.argPrefix)] + opt)
		} else {
			cb.input.SetValue(cur + opt)
		}
		cb.input.CursorEnd()
		cb.recomputeMatches()
		return
	}
	if cb.mode == barFilter {
		if suffix := cb.ghostSuggestion(); suffix != "" {
			cb.input.SetValue(cb.input.Value() + suffix)
			cb.input.CursorEnd()
		}
		return
	}
	rawNoColon := strings.TrimPrefix(cb.input.Value(), ":")
	if cb.mode == barCommand && !strings.ContainsAny(rawNoColon, " \t") && len(cb.matches) > 0 {
		idx := clamp(cb.matchIdx, 0, len(cb.matches)-1)
		name := cb.matches[idx].Name
		if len(cb.argOptions[name]) > 0 || commandAcceptsArg(name) {
			name += " "
		}
		cb.input.SetValue(name)
		cb.input.CursorEnd()
		cb.recomputeMatches()
	}
}

func (cb *commandBar) recallHistory(delta int) bool {
	if cb.mode == barFilter {
		if len(cb.filterHistory) == 0 {
			return false
		}
		if cb.filterHistPos == -1 {
			if delta >= 0 {
				return false
			}
			cb.draft = cb.input.Value()
			cb.filterHistPos = len(cb.filterHistory) - 1
		} else {
			next := cb.filterHistPos + delta
			if next < 0 {
				next = 0
			}
			if next >= len(cb.filterHistory) {
				cb.filterHistPos = -1
				cb.input.SetValue(cb.draft)
				cb.input.CursorEnd()
				return true
			}
			cb.filterHistPos = next
		}
		cb.input.SetValue(cb.filterHistory[cb.filterHistPos])
		cb.input.CursorEnd()
		return true
	}

	if cb.mode == barCommand {
		if len(cb.cmdHistory) == 0 {
			return false
		}
		if cb.cmdHistPos == -1 {
			if delta >= 0 {
				return false
			}
			cb.draft = cb.input.Value()
			cb.cmdHistPos = len(cb.cmdHistory) - 1
		} else {
			next := cb.cmdHistPos + delta
			if next < 0 {
				next = 0
			}
			if next >= len(cb.cmdHistory) {
				cb.cmdHistPos = -1
				cb.input.SetValue(cb.draft)
				cb.input.CursorEnd()
				cb.recomputeMatches()
				return true
			}
			cb.cmdHistPos = next
		}
		cb.input.SetValue(cb.cmdHistory[cb.cmdHistPos])
		cb.input.CursorEnd()
		cb.recomputeMatches()
		return true
	}
	return false
}

func (cb commandBar) Update(msg tea.KeyPressMsg) (next commandBar, cmd tea.Cmd, submitted *commandInvocation, filterChanged bool, closed bool) {
	switch msg.String() {
	case "esc":
		if cb.mode == barFilter {
			restored := cb.openDraft
			changed := cb.input.Value() != restored
			nextBar := cb.Close()
			nextBar.input.SetValue(restored)
			return nextBar, nil, nil, changed, true
		}
		return cb.Close(), nil, nil, false, true
	case "tab":
		cb.applyAutocomplete()
		return cb, nil, nil, cb.mode == barFilter, false
	case "ctrl+r", "alt+up":
		if cb.recallHistory(-1) {
			return cb, nil, nil, cb.mode == barFilter, false
		}
		return cb, nil, nil, false, false
	case "alt+down":
		if cb.recallHistory(1) {
			return cb, nil, nil, cb.mode == barFilter, false
		}
		return cb, nil, nil, false, false
	case "up", "ctrl+p":
		if cb.mode == barFilter {
			if cb.recallHistory(-1) {
				return cb, nil, nil, true, false
			}
			return cb, nil, nil, false, false
		}
		if cb.mode == barCommand {
			if cb.cmdHistPos != -1 {
				if cb.recallHistory(-1) {
					return cb, nil, nil, false, false
				}
			}
			if cb.argMode && len(cb.argMatches) > 0 {
				if cb.argIdx > 0 {
					cb.argIdx--
				}
				return cb, nil, nil, false, false
			}
			if cb.matchIdx > 0 {
				cb.matchIdx--
			} else if msg.String() == "up" && cb.input.Value() == "" && len(cb.cmdHistory) > 0 {
				cb.recallHistory(-1)
			}
		}
		return cb, nil, nil, false, false
	case "down", "ctrl+n":
		if cb.mode == barFilter {
			if cb.recallHistory(1) {
				return cb, nil, nil, true, false
			}
			return cb, nil, nil, false, false
		}
		if cb.mode == barCommand {
			if cb.cmdHistPos != -1 {
				if cb.recallHistory(1) {
					return cb, nil, nil, false, false
				}
			}
			if cb.argMode && len(cb.argMatches) > 0 {
				if cb.argIdx < len(cb.argMatches)-1 {
					cb.argIdx++
				}
				return cb, nil, nil, false, false
			}
			if len(cb.matches) > 0 && cb.matchIdx < len(cb.matches)-1 {
				cb.matchIdx++
			}
		}
		return cb, nil, nil, false, false
	case "enter":
		rawInput := cb.input.Value()
		val := strings.TrimSpace(rawInput)
		if cb.mode == barFilter {
			if val != "" {
				cb.recordFilter(val)
			}
			return cb.Close(), nil, nil, false, true
		}
		if val == "" && len(cb.matches) > 0 {
			val = cb.matches[clamp(cb.matchIdx, 0, len(cb.matches)-1)].Name
		} else if cb.argMode && len(cb.argMatches) > 0 {
			selOpt := cb.argMatches[clamp(cb.argIdx, 0, len(cb.argMatches)-1)].Value
			if cb.argPrefix == "" {
				val = strings.TrimSpace(rawInput) + " " + selOpt
			} else {
				exactOpt := false
				for _, o := range cb.argMatches {
					if strings.EqualFold(o.Value, cb.argPrefix) {
						exactOpt = true
						break
					}
				}
				if !exactOpt && strings.HasSuffix(val, cb.argPrefix) {
					val = val[:len(val)-len(cb.argPrefix)] + selOpt
				}
			}
		} else if val != "" && !strings.ContainsAny(val, " \t") && !strings.HasPrefix(val, "!") && len(cb.matches) > 0 {

			exact := false
			lowerVal := strings.ToLower(val)
			for _, sp := range cb.specs {
				if sp.Name == lowerVal || containsFold(sp.Aliases, lowerVal) {
					exact = true
					break
				}
			}
			if !exact {
				sel := cb.matches[clamp(cb.matchIdx, 0, len(cb.matches)-1)]
				val = sel.Name
			}
		}
		cb.recordCommand(val)
		inv := parseCommandInput(val)
		return cb.Close(), nil, &inv, false, true
	}

	prev := cb.input.Value()
	var ucmd tea.Cmd
	cb.input, ucmd = cb.input.Update(msg)
	cur := cb.input.Value()
	if cur != prev {
		if cb.mode == barCommand {
			cb.cmdHistPos = -1
			cb.recomputeMatches()
		} else if cb.mode == barFilter {
			cb.filterHistPos = -1
			filterChanged = true
		}
	}
	return cb, ucmd, nil, filterChanged, false
}

func containsFold(slice []string, target string) bool {
	for _, s := range slice {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}

func parseCommandInput(raw string) commandInvocation {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, ":"))
	inv := commandInvocation{
		Raw: trimmed,
		KV:  map[string]string{},
	}
	if trimmed == "" {
		return inv
	}
	if strings.HasPrefix(trimmed, "!") {
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
		parts := fuzzy.SplitFields(rest)
		if len(parts) > 0 && strings.EqualFold(parts[0], "ws") {
			parts = parts[1:]
		}
		inv.Cmd = "!ws"
		inv.Args = parts
		return inv
	}

	fields := fuzzy.SplitFields(trimmed)
	if len(fields) == 0 {
		return inv
	}
	inv.Cmd = strings.ToLower(fields[0])
	for _, arg := range fields[1:] {
		if k, v, ok := strings.Cut(arg, ":"); ok && k != "" && v != "" {
			inv.KV[strings.ToLower(k)] = v
		} else if k, v, ok := strings.Cut(arg, "="); ok && k != "" && v != "" {
			inv.KV[strings.ToLower(k)] = v
		}
		inv.Args = append(inv.Args, arg)
	}
	return inv
}

func isSafeWSSubcommand(args []string) bool {
	if len(args) == 0 {
		return true
	}
	hasHelp := false
	hasDryRun := false
	var pos []string
	skipNext := false
	for _, raw := range args {
		a := strings.TrimSpace(raw)
		if a == "" {
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--help" || a == "-h" {
			hasHelp = true
			continue
		}
		if a == "--dry-run" || a == "-n" {
			hasDryRun = true
			continue
		}
		if strings.HasPrefix(a, "-") {

			switch strings.ToLower(a) {
			case "--zone", "-z", "--preset", "--theme", "--format":
				skipNext = true
			}
			continue
		}
		pos = append(pos, strings.ToLower(a))
	}
	if hasHelp || len(pos) == 0 {
		return true
	}

	sub := pos[0]
	sub2 := ""
	if len(pos) > 1 {
		sub2 = pos[1]
	}
	switch sub {
	case "doctor", "whoami", "status", "diff", "hist", "history":
		return true
	case "env":
		switch sub2 {
		case "", "list", "show", "diff", "files":
			return true
		}
	case "repo":
		switch sub2 {
		case "", "list", "prs", "issues", "pick", "status":
			return true
		}
	case "preset", "presets":
		switch sub2 {
		case "", "list", "show", "status":
			return true
		}
	case "theme", "themes":
		switch sub2 {
		case "", "list", "status":
			return true
		}
	case "pkg":
		switch sub2 {
		case "", "list", "check", "outdated":
			return true
		}
	case "fleet", "db", "vault", "zone", "schedule":
		switch sub2 {
		case "", "list", "status", "diff":
			return true
		}
	case "config", "settings":
		switch sub2 {
		case "", "show", "path":
			return true
		}
	case "mise":
		switch sub2 {
		case "", "list", "configs", "outdated":
			return true
		}
	case "update":
		for _, a := range args {
			if a == "--digest" {
				return true
			}
		}
	case "apply", "bootstrap", "clean", "android":
		return hasDryRun
	}
	return false
}

func (cb commandBar) View(width, lines int, filterTarget string, breadcrumbs, chips []string, st Styles) string {
	if width <= 4 {
		return ""
	}
	if lines >= 3 && width >= 20 {
		return cb.viewBorderedPanel(width, filterTarget, breadcrumbs, chips, st)
	}
	if cb.mode != barFilter {
		var crumbParts []string
		crumbParts = append(crumbParts, st.Muted.Render("ws"))
		for i, c := range breadcrumbs {
			crumbParts = append(crumbParts, st.Muted.Render("›"))
			if i == len(breadcrumbs)-1 {
				crumbParts = append(crumbParts, st.AccentText.Render(c))
			} else {
				crumbParts = append(crumbParts, st.RowNormal.Render(c))
			}
		}
		left := strings.Join(crumbParts, " ")

		var rightParts []string
		for _, ch := range chips {
			if ch != "" {
				rightParts = append(rightParts, st.InfoText.Render("["+ch+"]"))
			}
		}
		if cb.mode == barCommand {
			rightParts = append(rightParts, st.AccentText.Render("[palette · esc]"))
		}
		right := strings.Join(rightParts, " ")

		innerW := width - 2
		gap := innerW - ansi.StringWidth(left) - ansi.StringWidth(right)
		line := left
		if gap >= 2 {
			line = left + strings.Repeat(" ", gap) + right
		} else if ansi.StringWidth(left) > innerW {
			line = truncate(strings.Join(append([]string{"ws"}, breadcrumbs...), " › "), innerW)
		}
		return fitStyledWidth("  "+fitStyledWidth(line, innerW, st), width, st)
	}

	ghost := cb.ghostSuggestion()
	val := cb.input.Value()
	target := nonEmpty(filterTarget, "List")
	targetPrefix := "Filter " + target + " ▸ "
	if width < 82 {
		targetPrefix = target + " "
	}
	rightHint := st.Muted.Render("fuzzy / key:val · tab fill · ctrl+r/alt+↑ hist · esc restore")
	if width < 82 {
		rightHint = st.Muted.Render("tab fill · alt+↑ hist · esc")
	}

	promptPart := st.InfoText.Render(targetPrefix) + st.AccentText.Render("/") + st.RowNormal.Render(val) + st.AccentText.Render("█")
	if st.Roles.NoColor {
		promptPart = targetPrefix + "/" + val + "_"
	}
	if ghost != "" {
		promptPart += st.Muted.Render(ghost)
	}
	innerW := width - 2
	gap := innerW - ansi.StringWidth(promptPart) - ansi.StringWidth(rightHint)
	content := promptPart
	if gap >= 2 {
		content = promptPart + strings.Repeat(" ", gap) + rightHint
	}
	accentBar := lipgloss.NewStyle().Foreground(st.Accent).Render("▌")
	if st.Roles.NoColor {
		accentBar = ">"
	}
	return fitStyledWidth(accentBar+" "+fitStyledWidth(content, innerW, st), width, st)
}

func (cb commandBar) viewBorderedPanel(width int, filterTarget string, breadcrumbs, chips []string, st Styles) string {
	innerW := max(12, width-4)
	if cb.mode != barFilter {
		var crumbParts []string
		crumbParts = append(crumbParts, st.Muted.Render("ws"))
		for i, c := range breadcrumbs {
			crumbParts = append(crumbParts, st.Muted.Render("›"))
			if i == len(breadcrumbs)-1 {
				crumbParts = append(crumbParts, st.AccentText.Render(c))
			} else {
				crumbParts = append(crumbParts, st.RowNormal.Render(c))
			}
		}
		left := strings.Join(crumbParts, " ")

		var badgeParts []string
		for _, ch := range chips {
			if ch != "" {
				badgeParts = append(badgeParts, "["+ch+"]")
			}
		}
		if cb.mode == barCommand {
			badgeParts = append(badgeParts, "[palette · esc]")
		}
		rightBadge := strings.Join(badgeParts, " ")

		var opts string
		if cb.mode == barCommand {
			opts = st.AccentText.Render("tab fill · ↑/↓ pick · enter run · esc close")
		}
		line := left
		if opts != "" {
			if gap := innerW - ansi.StringWidth(left) - ansi.StringWidth(opts); gap >= 2 {
				line = left + strings.Repeat(" ", gap) + opts
			}
		}
		boxTitle := "Context"
		if cb.mode == barCommand {
			boxTitle = "Command Palette"
		}
		return renderTitledBox(boxTitle, rightBadge, []string{fitStyledWidth(line, innerW, st)}, width, 3, cb.mode == barCommand, st)
	}

	target := nonEmpty(filterTarget, "List")
	boxTitle := "Filter · " + target
	ghost := cb.ghostSuggestion()
	val := cb.input.Value()
	targetPrefix := "Filter " + target + " ▸ "
	if innerW < 80 {
		targetPrefix = target + " "
	}
	promptPart := st.InfoText.Render(targetPrefix) + st.AccentText.Render("/") + st.RowNormal.Render(val) + st.AccentText.Render("█")
	if st.Roles.NoColor {
		promptPart = targetPrefix + "/" + val + "_"
	}
	if ghost != "" {
		promptPart += st.Muted.Render(ghost)
	}

	var opts string
	switch {
	case innerW >= 104:
		opts = st.Muted.Render("fuzzy / key:val · tab fill · ctrl+r/alt+↑ hist · esc restore")
	case innerW >= 68:
		opts = st.Muted.Render("tab fill · alt+↑ hist · esc")
	case innerW >= 42:
		opts = st.Muted.Render("enter apply · esc")
	}
	content := promptPart
	if opts != "" {
		if gap := innerW - ansi.StringWidth(promptPart) - ansi.StringWidth(opts); gap >= 2 {
			content = promptPart + strings.Repeat(" ", gap) + opts
		}
	}
	var badgeParts []string
	for _, ch := range chips {
		if ch != "" {
			badgeParts = append(badgeParts, "["+ch+"]")
		}
	}
	return renderTitledBox(boxTitle, strings.Join(badgeParts, " "), []string{fitStyledWidth(content, innerW, st)}, width, 3, true, st)
}

func commandKindBadge(sp commandSpec, st Styles) (plain string, styled string) {
	switch {
	case sp.Dangerous:
		plain = "[confirm]"
		if st.Roles.NoColor {
			return plain, plain
		}
		return plain, st.WarnText.Render(plain)
	case sp.Kind == "view":
		plain = "[view]"
		if st.Roles.NoColor {
			return plain, plain
		}
		return plain, st.InfoText.Render(plain)
	case sp.Kind == "context":
		plain = "[ctx]"
		if st.Roles.NoColor {
			return plain, plain
		}
		return plain, st.Muted.Render(plain)
	default:
		plain = "[run]"
		if st.Roles.NoColor {
			return plain, plain
		}
		return plain, st.OKText.Render(plain)
	}
}

func (cb commandBar) RenderPalette(maxW, maxH int, st Styles) (string, bool) {
	if cb.mode != barCommand || maxH < 5 || maxW < 24 {
		return "", false
	}

	innerW := max(12, maxW-4)
	matchChipPlain := fmt.Sprintf("%d/%d", min(cb.matchIdx+1, max(1, len(cb.matches))), len(cb.matches))
	if cb.argMode && len(cb.argMatches) > 0 {
		matchChipPlain = fmt.Sprintf("%d/%d opts", clamp(cb.argIdx, 0, len(cb.argMatches)-1)+1, len(cb.argMatches))
	} else if len(cb.matches) == 0 {
		matchChipPlain = "raw :!ws"
	}
	chipStyled := st.Muted.Render(matchChipPlain)
	if st.Roles.NoColor {
		chipStyled = matchChipPlain
	}

	ti := cb.input
	ti.Prompt = ": "
	ti.Placeholder = "type command or !ws subcommand..."
	promptW := innerW - ansi.StringWidth(matchChipPlain) - 3
	if promptW < 10 {
		promptW = innerW - 2
	}
	ti = styleTextInput(ti, max(4, promptW), st)
	inputRendered := ti.View()
	if st.Roles.NoColor {
		inputRendered = ui.StripANSI(inputRendered)
	}
	ghost := cb.ghostSuggestion()
	atEnd := ti.Position() >= utf8.RuneCountInString(ti.Value())
	if ghost != "" && atEnd && innerW-ansi.StringWidth(inputRendered)-ansi.StringWidth(matchChipPlain) > 4 {
		availGhost := innerW - ansi.StringWidth(inputRendered) - ansi.StringWidth(matchChipPlain) - 2
		gTrunc := truncate(ghost, availGhost)
		if gTrunc != "" {
			if st.Roles.NoColor {
				inputRendered += gTrunc
			} else {
				inputRendered += st.Muted.Render(gTrunc)
			}
		}
	}
	promptLine := inputRendered
	if gap := innerW - ansi.StringWidth(inputRendered) - ansi.StringWidth(matchChipPlain); gap >= 2 {
		promptLine = inputRendered + strings.Repeat(" ", gap) + chipStyled
	}
	promptLine = fitStyledWidth(promptLine, innerW, st)

	innerH := max(3, maxH-2)
	showTopRule := innerH >= 5
	showFooter := innerH >= 6

	var footerDetail string
	var isDangerous bool
	if cb.argMode && len(cb.argMatches) > 0 {
		selOpt := cb.argMatches[clamp(cb.argIdx, 0, len(cb.argMatches)-1)]
		footerDetail = fmt.Sprintf("option: %s  ·  %s  ·  keys: tab fill · ↑/↓ select · enter apply · esc cancel", selOpt.Value, nonEmpty(selOpt.Description, "argument value"))
	} else if len(cb.matches) == 0 {
		footerDetail = "Tip: :1 repo · :2 mise · :3 pkg · :4 fleet · :5 env · :6 doctor · :7 config · :0 out · :!ws <cmd>"
	} else {
		sel := cb.matches[clamp(cb.matchIdx, 0, len(cb.matches)-1)]
		isDangerous = sel.Dangerous
		var fParts []string
		fParts = append(fParts, sel.Usage)
		if len(sel.Aliases) > 0 {
			fParts = append(fParts, "aliases: "+strings.Join(sel.Aliases, ","))
		}
		if sel.Dangerous {
			fParts = append(fParts, "prompts before running")
		}
		fParts = append(fParts, "keys: tab fill · ↑/↓ move · ctrl+r hist · enter run")
		footerDetail = strings.Join(fParts, "  ·  ")
	}

	var footerLines []string
	if showFooter {
		maxFooterLines := max(1, min(4, innerH-4))
		footerLines = wrapPlainText(footerDetail, innerW)
		if len(footerLines) > maxFooterLines {
			footerLines = footerLines[:maxFooterLines]
		}
	}

	overhead := 1
	if showTopRule {
		overhead++
	}
	if showFooter {
		overhead += 1 + len(footerLines)
	}
	listCap := max(1, innerH-overhead)

	body := make([]string, 0, innerH)
	body = append(body, promptLine)
	if showTopRule {
		body = append(body, st.Muted.Render(strings.Repeat("─", innerW)))
	}

	if cb.argMode && len(cb.argMatches) > 0 {
		showN := min(len(cb.argMatches), listCap)
		start, end := clampViewport(cb.argIdx, len(cb.argMatches), showN)
		valW := 16
		for i := start; i < end; i++ {
			if vw := ansi.StringWidth(cb.argMatches[i].Value) + 2; vw > valW && vw <= max(18, innerW/2) {
				valW = vw
			}
		}
		kindW := 8
		if innerW < 42 {
			valW = 12
			kindW = 7
		}
		optPlain := "[opt]"
		optStyled := st.InfoText.Render(optPlain)
		if st.Roles.NoColor {
			optStyled = optPlain
		}
		for i := start; i < end; i++ {
			opt := cb.argMatches[i]
			isSel := i == cb.argIdx
			marker := "  "
			if isSel {
				if st.Roles.NoColor {
					marker = "> "
				} else {
					marker = "▸ "
				}
			}
			valCol := padRight(truncate(opt.Value, valW), valW)
			kindColPlain := padRight(optPlain, kindW)
			kindColStyled := padRight(optStyled, kindW)
			descMax := max(6, innerW-2-valW-kindW-1)
			descTxt := truncate(opt.Description, descMax)

			var row string
			if st.Roles.NoColor {
				row = padRight(marker+valCol+" "+kindColPlain+descTxt, innerW)
			} else if isSel {
				styledRow := st.AccentText.Render(marker) + st.AccentText.Render(valCol) + " " + kindColStyled + st.RowNormal.Render(descTxt)
				if rem := innerW - ansi.StringWidth(styledRow); rem > 0 {
					styledRow += strings.Repeat(" ", rem)
				}
				if st.SelBgHex != "" {
					row = applyRowHighlightBG(styledRow, st.SelBgHex, st.SelFgHex)
				} else {
					row = st.RowSelected.Render(padRight(marker+valCol+" "+kindColPlain+descTxt, innerW))
				}
			} else {
				row = marker + st.AccentText.Render(valCol) + " " + kindColStyled + st.RowNormal.Render(descTxt)
			}
			body = append(body, fitStyledWidth(row, innerW, st))
		}
	} else if len(cb.matches) == 0 {
		rawTrim := strings.TrimSpace(strings.TrimPrefix(cb.input.Value(), ":"))
		msg1 := st.WarnText.Render("No built-in palette command matches this input.")
		msg2 := st.Muted.Render("Enter runs: ws " + nonEmpty(strings.TrimPrefix(rawTrim, "!"), "<subcommand>"))
		if st.Roles.NoColor {
			msg1 = "No built-in palette command matches this input."
			msg2 = "Enter runs: ws " + nonEmpty(strings.TrimPrefix(rawTrim, "!"), "<subcommand>")
		}
		body = append(body, fitStyledWidth(msg1, innerW, st))
		if listCap >= 2 {
			body = append(body, fitStyledWidth(msg2, innerW, st))
		}
	} else {
		showN := min(len(cb.matches), listCap)
		start, end := clampViewport(cb.matchIdx, len(cb.matches), showN)
		nameW := 11
		kindW := 10
		if innerW < 42 {
			nameW = 9
			kindW = 8
		}
		for i := start; i < end; i++ {
			sp := cb.matches[i]
			isSel := i == cb.matchIdx
			marker := "  "
			if isSel {
				if st.Roles.NoColor {
					marker = "> "
				} else {
					marker = "▸ "
				}
			}
			nameCol := padRight(truncate(":"+sp.Name, nameW), nameW)
			kindPlain, kindStyled := commandKindBadge(sp, st)
			kindColPlain := padRight(kindPlain, kindW)
			kindColStyled := padRight(kindStyled, kindW)
			descMax := max(6, innerW-2-nameW-kindW-1)
			descTxt := truncate(sp.Description, descMax)

			var row string
			if st.Roles.NoColor {
				row = padRight(marker+nameCol+" "+kindColPlain+descTxt, innerW)
			} else if isSel {
				styledRow := st.AccentText.Render(marker) + st.AccentText.Render(nameCol) + " " + kindColStyled + st.RowNormal.Render(descTxt)
				if rem := innerW - ansi.StringWidth(styledRow); rem > 0 {
					styledRow += strings.Repeat(" ", rem)
				}
				if st.SelBgHex != "" {
					row = applyRowHighlightBG(styledRow, st.SelBgHex, st.SelFgHex)
				} else {
					row = st.RowSelected.Render(padRight(marker+nameCol+" "+kindColPlain+descTxt, innerW))
				}
			} else {
				row = marker + st.AccentText.Render(nameCol) + " " + kindColStyled + st.RowNormal.Render(descTxt)
			}
			body = append(body, fitStyledWidth(row, innerW, st))
		}
	}

	for len(body) < innerH-(1+len(footerLines)) && showFooter {
		body = append(body, "")
	}
	if showFooter {
		body = append(body, st.Muted.Render(strings.Repeat("─", innerW)))
		fStyle := st.Muted
		if isDangerous && !st.Roles.NoColor {
			fStyle = st.WarnText
		}
		for _, fl := range footerLines {
			line := fl
			if !st.Roles.NoColor {
				line = fStyle.Render(fl)
			}
			body = append(body, fitStyledWidth(line, innerW, st))
		}
	}

	boxH := len(body) + 2
	return renderTitledBox("Command Palette", matchChipPlain, body, maxW, boxH, true, st), true
}
