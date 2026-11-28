package ui

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/spf13/cobra"
)

type ContrastIssue struct {
	Token   string
	Against string
	Ratio   float64
	Minimum float64
}

func LintPalette(pal config.Palette) []ContrastIssue {
	var issues []ContrastIssue
	if pal.Tokens == nil {
		return issues
	}
	bg := strings.TrimSpace(pal.Tokens["bg"])
	if bg == "" {
		return issues
	}

	check := func(tok, against string, minRatio float64) {
		val := strings.TrimSpace(pal.Tokens[tok])
		bgVal := strings.TrimSpace(pal.Tokens[against])
		if val == "" || bgVal == "" {
			return
		}
		if r := ContrastRatioHex(val, bgVal); r < minRatio {
			issues = append(issues, ContrastIssue{
				Token:   tok,
				Against: against,
				Ratio:   r,
				Minimum: minRatio,
			})
		}
	}

	check("fg", "bg", 4.5)
	check("fg_dim", "bg", 2.0)
	check("accent", "bg", 3.0)
	check("selection_fg", "selection_bg", 3.0)

	slots := ChooseSemanticSlots(pal)
	slotChecks := []struct {
		name string
		idx  int
	}{
		{"accent", slots.AccentIdx},
		{"blue", slots.BlueIdx},
		{"cyan", slots.CyanIdx},
		{"green", slots.GreenIdx},
		{"magenta", slots.MagentaIdx},
		{"red", slots.RedIdx},
		{"yellow", slots.YellowIdx},
	}
	for _, sc := range slotChecks {
		idx := sc.idx
		if idx == 0 || idx == 7 || idx == 8 || idx == 15 {
			issues = append(issues, ContrastIssue{
				Token:   fmt.Sprintf("slot(%s)=%d", sc.name, idx),
				Against: "forbidden-fg-slot",
				Ratio:   0,
				Minimum: 1,
			})
			continue
		}
		tok := fmt.Sprintf("color%d", idx)
		if val := strings.TrimSpace(pal.Tokens[tok]); val != "" {
			if r := ContrastRatioHex(val, bg); r < 3.0 {
				issues = append(issues, ContrastIssue{
					Token:   fmt.Sprintf("%s(%s)", sc.name, tok),
					Against: "bg",
					Ratio:   r,
					Minimum: 3.0,
				})
			}
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Token != issues[j].Token {
			return issues[i].Token < issues[j].Token
		}
		return issues[i].Against < issues[j].Against
	})
	return issues
}

func forbiddenFGSlot(c any) (int, bool) {
	ac, ok := c.(lipgloss.ANSIColor)
	if !ok {
		return 0, false
	}
	idx := int(ac)
	return idx, idx == 0 || idx == 7 || idx == 8 || idx == 15
}

func TestRolesNoForbiddenSlotsOrChromeBackgrounds(t *testing.T) {
	roles := NewRoles(config.Palette{
		ID:   "gruvbox-dark",
		Mode: "dark",
		Tokens: map[string]string{
			"bg":      "#282828",
			"fg":      "#ebdbb2",
			"accent":  "#fe8019",
			"color1":  "#cc241d",
			"color9":  "#fb4934",
			"color2":  "#98971a",
			"color10": "#b8bb26",
			"color3":  "#d79921",
			"color11": "#fabd2f",
			"color4":  "#458588",
			"color12": "#83a598",
			"color5":  "#b16286",
			"color13": "#d3869b",
			"color6":  "#689d6a",
			"color14": "#8ec07c",
		},
	}, false)

	styles := map[string]lipgloss.Style{
		"Accent":         roles.Accent,
		"Muted":          roles.Muted,
		"OK":             roles.OK,
		"Warn":           roles.Warn,
		"Err":            roles.Err,
		"Info":           roles.Info,
		"Header":         roles.Header,
		"Key":            roles.Key,
		"Border":         roles.Border,
		"Selected":       roles.Selected,
		"ButtonActive":   roles.ButtonActive,
		"ButtonInactive": roles.ButtonInactive,
		"ButtonDanger":   roles.ButtonDanger,
		"BarFill":        roles.BarFill,
		"BarTrack":       roles.BarTrack,
	}

	for name, st := range styles {
		if bg := st.GetBackground(); bg != nil && !reflect.DeepEqual(bg, lipgloss.NoColor{}) {
			t.Fatalf("role %s sets background %v; terminal-native chrome must not set backgrounds", name, bg)
		}
		if idx, forbidden := forbiddenFGSlot(st.GetForeground()); forbidden {
			t.Fatalf("role %s uses forbidden ANSI foreground slot %d", name, idx)
		}
	}
}

func TestFangSchemeUsesNoCodeblockBGAndSafeSlots(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	for _, isDark := range []bool{true, false} {
		cs := FangScheme(lipgloss.LightDark(isDark))
		if cs.Codeblock != nil && !reflect.DeepEqual(cs.Codeblock, lipgloss.NoColor{}) {
			t.Fatalf("FangScheme(isDark=%v) Codeblock must be NoColor, got %v", isDark, cs.Codeblock)
		}
		fgFields := map[string]any{
			"Base":           cs.Base,
			"Title":          cs.Title,
			"Description":    cs.Description,
			"Program":        cs.Program,
			"DimmedArgument": cs.DimmedArgument,
			"Comment":        cs.Comment,
			"Flag":           cs.Flag,
			"FlagDefault":    cs.FlagDefault,
			"Command":        cs.Command,
			"QuotedString":   cs.QuotedString,
			"Argument":       cs.Argument,
			"Help":           cs.Help,
			"Dash":           cs.Dash,
			"ErrorDetails":   cs.ErrorDetails,
		}
		for name, c := range fgFields {
			if idx, forbidden := forbiddenFGSlot(c); forbidden {
				t.Fatalf("FangScheme(isDark=%v).%s uses forbidden ANSI foreground slot %d", isDark, name, idx)
			}
		}
	}
}

func TestFangExecuteRespectsNoColorAndPipedHelp(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")

	var out bytes.Buffer
	cmd := &cobra.Command{
		Use:     "ws-test",
		Short:   "Workstation test CLI",
		Example: "ws-test --zone work",
	}
	cmd.Flags().String("zone", "personal", "zone name")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	if err := fang.Execute(
		context.Background(),
		cmd,
		fang.WithVersion("dev"),
		fang.WithColorSchemeFunc(FangScheme),
	); err != nil {
		t.Fatalf("fang.Execute(--help): %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "ws-test") {
		t.Fatalf("expected help output, got %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in piped NO_COLOR help, got %q", got)
	}
}

func TestHuhThemeHasNoChromeBackgroundsOrForbiddenFG(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	roles := NewRoles(config.Palette{Mode: "dark"}, false)
	theme := HuhTheme(roles)
	for _, isDark := range []bool{true, false} {
		hs := theme.Theme(isDark)
		check := map[string]lipgloss.Style{
			"Focused.Title":          hs.Focused.Title,
			"Focused.Description":    hs.Focused.Description,
			"Focused.SelectSelector": hs.Focused.SelectSelector,
			"Focused.Option":         hs.Focused.Option,
			"Focused.SelectedOption": hs.Focused.SelectedOption,
			"Focused.FocusedButton":  hs.Focused.FocusedButton,
			"Focused.BlurredButton":  hs.Focused.BlurredButton,
			"Focused.Placeholder":    hs.Focused.TextInput.Placeholder,
			"Blurred.Title":          hs.Blurred.Title,
			"Blurred.BlurredButton":  hs.Blurred.BlurredButton,
		}
		for name, st := range check {
			if bg := st.GetBackground(); bg != nil && !reflect.DeepEqual(bg, lipgloss.NoColor{}) {
				t.Fatalf("HuhTheme(isDark=%v) %s has background %v", isDark, name, bg)
			}
			if idx, forbidden := forbiddenFGSlot(st.GetForeground()); forbidden {
				t.Fatalf("HuhTheme(isDark=%v) %s uses forbidden ANSI fg %d", isDark, name, idx)
			}
		}
	}
}

func TestNoColorAndDumbTermStripEscapes(t *testing.T) {
	for _, env := range []struct {
		k, v string
	}{
		{"NO_COLOR", "1"},
		{"TERM", "dumb"},
	} {
		t.Run(env.k+"="+env.v, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			t.Setenv(env.k, env.v)

			if !ColorDisabled() {
				t.Fatalf("expected ColorDisabled=true for %s=%s", env.k, env.v)
			}
			r := DefaultRoles(config.Palette{Mode: "dark"})
			if !r.NoColor {
				t.Fatalf("expected Roles.NoColor=true")
			}

			var buf bytes.Buffer
			if err := RenderTable(&buf, []TableColumn{{Title: "KEY"}, {Title: "VALUE"}}, [][]string{{"API_KEY", r.OK.Render("ok")}}, r); err != nil {
				t.Fatalf("RenderTable: %v", err)
			}
			if strings.Contains(buf.String(), "\x1b") {
				t.Fatalf("expected zero ANSI escapes in output, got %q", buf.String())
			}

			cs := FangScheme(lipgloss.LightDark(true))
			if !reflect.DeepEqual(cs.Title, lipgloss.NoColor{}) {
				t.Fatalf("expected NoColor FangScheme Title under %s=%s, got %v", env.k, env.v, cs.Title)
			}
		})
	}
}

func TestAllWorkspacePalettesPassContrastLint(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repoRoot: %v", err)
	}
	ids, err := config.ListThemes(repoRoot)
	if err != nil {
		t.Fatalf("ListThemes(%s): %v", repoRoot, err)
	}
	if len(ids) == 0 {
		t.Fatalf("expected themes in %s/themes", repoRoot)
	}
	for _, id := range ids {
		pal, err := config.LoadPalette(repoRoot, id)
		if err != nil {
			t.Fatalf("LoadPalette(%s): %v", id, err)
		}
		slots := ChooseSemanticSlots(pal)
		for _, idx := range []int{slots.RedIdx, slots.GreenIdx, slots.YellowIdx, slots.BlueIdx, slots.MagentaIdx, slots.CyanIdx, slots.AccentIdx} {
			if idx == 0 || idx == 7 || idx == 8 || idx == 15 {
				t.Fatalf("theme %s chose forbidden foreground ANSI slot %d", id, idx)
			}
		}

		bg := pal.Tokens["bg"]
		accentColor := pal.Tokens["color"+strconvItoa(slots.AccentIdx)]
		if accentColor != "" {
			if cr := ContrastRatioHex(accentColor, bg); cr < 3.0 {
				t.Fatalf("theme %s chosen AccentIdx=%d (%s) contrast vs bg (%s) = %.2f < 3.0", id, slots.AccentIdx, accentColor, bg, cr)
			}
		}

		for _, iss := range LintPalette(pal) {
			if iss.Token == "fg" && iss.Ratio < 4.5 {
				t.Fatalf("theme %s fg contrast %.2f < 4.5", id, iss.Ratio)
			}
			if iss.Token == "selection_fg" && iss.Ratio < 3.0 {
				t.Fatalf("theme %s selection_fg contrast %.2f < 3.0", id, iss.Ratio)
			}
			if iss.Against == "forbidden-fg-slot" {
				t.Fatalf("theme %s: %+v", id, iss)
			}
			if iss.Ratio < 1.8 {
				t.Fatalf("theme %s unreadable token %+v", id, iss)
			}
		}
	}
}

func TestStripANSIAndDeterministicLintPalette(t *testing.T) {

	hyperlink := "\x1b]8;;https://example.invalid\x07click\x1b]8;;\x07 \x1b[38;2;255;0;0mok\x1b[0m"
	if got := StripANSI(hyperlink); got != "click ok" {
		t.Fatalf("StripANSI OSC/CSI failed: got %q", got)
	}

	roles := NewRoles(config.Palette{Mode: "dark"}, false)
	var tblBuf bytes.Buffer
	if err := RenderTable(&tblBuf, []TableColumn{{Title: "STATUS", MaxWidth: 8}}, [][]string{{roles.OK.Render("very-long-status-value")}}, roles); err != nil {
		t.Fatalf("RenderTable styled truncate: %v", err)
	}
	if !strings.Contains(StripANSI(tblBuf.String()), "…") {
		t.Fatalf("expected truncated ellipsis in table cell, got %q", tblBuf.String())
	}

	lowPal := config.Palette{
		Mode: "dark",
		Tokens: map[string]string{
			"bg": "#282828", "fg": "#292929", "accent": "#2a2a2a",
			"color1": "#292929", "color2": "#292929", "color3": "#292929",
			"color4": "#292929", "color5": "#292929", "color6": "#292929",
		},
	}
	first := LintPalette(lowPal)
	for i := 0; i < 10; i++ {
		if got := LintPalette(lowPal); !reflect.DeepEqual(first, got) {
			t.Fatalf("LintPalette non-deterministic on iter %d: %+v vs %+v", i, first, got)
		}
	}
}

func TestResolveSelectionColorsAllPalettes(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repoRoot: %v", err)
	}
	ids, err := config.ListThemes(repoRoot)
	if err != nil {
		t.Fatalf("ListThemes(%s): %v", repoRoot, err)
	}
	for _, id := range ids {
		pal, err := config.LoadPalette(repoRoot, id)
		if err != nil {
			t.Fatalf("LoadPalette(%s): %v", id, err)
		}
		sel := ResolveSelectionColors(pal)
		if want := strings.TrimSpace(pal.Tokens["selection_bg"]); want != "" && sel.BgHex != want {
			t.Fatalf("theme %s ResolveSelectionColors.BgHex = %q, want %q", id, sel.BgHex, want)
		}
		if cr := ContrastRatioHex(sel.FgHex, sel.BgHex); cr < 4.5 {
			t.Fatalf("theme %s selection contrast (%s on %s) = %.2f < 4.5", id, sel.FgHex, sel.BgHex, cr)
		}
	}

	darkFallback := ResolveSelectionColors(config.Palette{Mode: "dark"})
	if darkFallback.BgHex != "#45475a" || ContrastRatioHex(darkFallback.FgHex, darkFallback.BgHex) < 4.5 {
		t.Fatalf("unexpected dark fallback selection colors: %+v", darkFallback)
	}
	lightFallback := ResolveSelectionColors(config.Palette{Mode: "light"})
	if lightFallback.BgHex != "#bdae93" || ContrastRatioHex(lightFallback.FgHex, lightFallback.BgHex) < 4.5 {
		t.Fatalf("unexpected light fallback selection colors: %+v", lightFallback)
	}
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
