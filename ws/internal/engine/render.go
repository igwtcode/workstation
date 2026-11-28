package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

var hexTokenRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

type TemplateStatus struct {
	App          string `json:"app"`
	Template     string `json:"template"`
	Output       string `json:"output"`
	PostHook     string `json:"postHook,omitempty"`
	State        string `json:"state"`
	ExpectedHash string `json:"expectedHash"`
	ActualHash   string `json:"actualHash,omitempty"`
	LedgerHash   string `json:"ledgerHash,omitempty"`
	Diff         string `json:"diff,omitempty"`
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func RenderTemplateString(tmpl string, pal config.Palette) (string, error) {
	var b strings.Builder
	b.Grow(len(tmpl))

	rest := tmpl
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		rest = rest[start+2:]

		end := strings.Index(rest, "}}")
		if end < 0 {
			return "", fmt.Errorf("unterminated {{ in template")
		}
		token := rest[:end]
		rest = rest[end+2:]

		val, ok := resolvePaletteToken(pal, token)
		if !ok {
			return "", fmt.Errorf("unknown token '{{%s}}'", token)
		}
		b.WriteString(val)
	}

	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func resolvePaletteToken(pal config.Palette, token string) (string, bool) {
	if token == "" {
		return "", false
	}
	if v, ok := pal.Tokens[token]; ok && v != "" {
		return v, true
	}
	if strings.HasSuffix(token, "_strip") {
		base := strings.TrimSuffix(token, "_strip")
		if v, ok := pal.Tokens[base]; ok && hexTokenRe.MatchString(v) {
			return strings.TrimPrefix(v, "#"), true
		}
	}
	return "", false
}

func CheckTemplates(ctx *app.Context, themeID string) ([]TemplateStatus, error) {
	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		return nil, err
	}
	resolvedTheme, pal, err := loadActivePalette(ctx.RepoRoot, themeID, ledger)
	if err != nil {
		return nil, err
	}
	_ = resolvedTheme

	entries, err := config.LoadThemeRegistry(ctx.RepoRoot)
	if err != nil {
		return nil, err
	}
	if excludes := parseExcludeFilters(ctx); len(excludes) > 0 {
		var links []config.LinkEntry
		if presets, perr := config.LoadPresets(ctx.RepoRoot); perr == nil {
			if resolved, rerr := config.ResolvePreset(ctx.RepoRoot, presets, ctx.EffectivePreset()); rerr == nil {
				links, _ = config.LoadLinksForPreset(ctx.RepoRoot, resolved)
			}
		}
		outputTargets := buildOutputTargetMap(entries, links)
		entries, _, _ = applyExcludeFilters(ctx, excludes, entries, nil, outputTargets)
	}

	statuses := make([]TemplateStatus, 0, len(entries))
	for _, entry := range entries {
		rendered, err := renderRegistryEntry(ctx.RepoRoot, entry, pal)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", entry.Template, err)
		}
		expectedHash := SHA256Hex([]byte(rendered))
		ledgerHash := ledger.RenderedHashes[entry.Output]

		st := TemplateStatus{
			App:          entry.App,
			Template:     entry.Template,
			Output:       entry.Output,
			PostHook:     entry.PostHook,
			ExpectedHash: expectedHash,
			LedgerHash:   ledgerHash,
		}

		outAbs := filepath.Join(ctx.RepoRoot, entry.Output)
		diskBytes, err := os.ReadFile(outAbs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				st.State = "missing"
				st.Diff = UnifiedDiff("/dev/null", entry.Output, "", rendered)
				statuses = append(statuses, st)
				continue
			}
			return nil, err
		}

		actualHash := SHA256Hex(diskBytes)
		st.ActualHash = actualHash
		if actualHash == expectedHash {
			st.State = "ok"
		} else {
			if ledgerHash != "" && actualHash != ledgerHash {
				st.State = "modified"
			} else {
				st.State = "stale"
			}
			st.Diff = UnifiedDiff(entry.Output, entry.Output+" (rendered)", string(diskBytes), rendered)
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

func renderRegistryEntry(repoRoot string, entry config.RegistryEntry, pal config.Palette) (string, error) {
	tmplPath := filepath.Join(repoRoot, entry.Template)
	raw, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", err
	}
	return RenderTemplateString(string(raw), pal)
}

func loadActivePalette(repoRoot, override string, ledger *app.Ledger) (string, config.Palette, error) {
	if override != "" {
		pal, err := config.LoadPalette(repoRoot, override)
		return override, pal, err
	}
	if ledger != nil && ledger.ActiveTheme != "" {
		pal, err := config.LoadPalette(repoRoot, ledger.ActiveTheme)
		return ledger.ActiveTheme, pal, err
	}
	pal, err := config.LoadPalette(repoRoot, "gruvbox-dark")
	if err == nil {
		return "gruvbox-dark", pal, nil
	}
	themes, listErr := config.ListThemes(repoRoot)
	if listErr == nil && len(themes) > 0 {
		pal, loadErr := config.LoadPalette(repoRoot, themes[0])
		if loadErr == nil {
			return themes[0], pal, nil
		}
	}
	return "gruvbox-dark", config.Palette{}, err
}

type diffOpKind uint8

const (
	diffEqual diffOpKind = iota
	diffDelete
	diffInsert
)

type diffOp struct {
	kind diffOpKind
	line string
}

func UnifiedDiff(labelA, labelB, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	a := splitLines(oldText)
	b := splitLines(newText)
	ops := diffLines(a, b)

	const ctxLines = 3
	type hunkRange struct {
		start int
		end   int
	}
	var ranges []hunkRange
	for i, op := range ops {
		if op.kind == diffEqual {
			continue
		}
		s := i - ctxLines
		if s < 0 {
			s = 0
		}
		e := i + ctxLines + 1
		if e > len(ops) {
			e = len(ops)
		}
		if len(ranges) > 0 && s <= ranges[len(ranges)-1].end {
			if e > ranges[len(ranges)-1].end {
				ranges[len(ranges)-1].end = e
			}
		} else {
			ranges = append(ranges, hunkRange{start: s, end: e})
		}
	}
	if len(ranges) == 0 {
		if strings.HasSuffix(oldText, "\n") != strings.HasSuffix(newText, "\n") {
			var out strings.Builder
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", labelA, labelB)
			if !strings.HasSuffix(oldText, "\n") && strings.HasSuffix(newText, "\n") {
				fmt.Fprintf(&out, "@@ -%d,0 +%d,0 @@\n\\ No newline at end of file (added trailing newline)\n", len(a), len(b))
			} else {
				fmt.Fprintf(&out, "@@ -%d,0 +%d,0 @@\n\\ No newline at end of file (removed trailing newline)\n", len(a), len(b))
			}
			return out.String()
		}
		return ""
	}

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", labelA, labelB)

	aPos := make([]int, len(ops)+1)
	bPos := make([]int, len(ops)+1)
	ai, bi := 1, 1
	for i, op := range ops {
		aPos[i] = ai
		bPos[i] = bi
		switch op.kind {
		case diffEqual:
			ai++
			bi++
		case diffDelete:
			ai++
		case diffInsert:
			bi++
		}
	}
	aPos[len(ops)] = ai
	bPos[len(ops)] = bi

	for _, r := range ranges {
		aStart := aPos[r.start]
		bStart := bPos[r.start]
		aCount, bCount := 0, 0
		for i := r.start; i < r.end; i++ {
			switch ops[i].kind {
			case diffEqual:
				aCount++
				bCount++
			case diffDelete:
				aCount++
			case diffInsert:
				bCount++
			}
		}
		if aCount == 0 && aStart > 0 {
			aStart--
		}
		if bCount == 0 && bStart > 0 {
			bStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
		for i := r.start; i < r.end; i++ {
			switch ops[i].kind {
			case diffEqual:
				out.WriteByte(' ')
			case diffDelete:
				out.WriteByte('-')
			case diffInsert:
				out.WriteByte('+')
			}
			out.WriteString(ops[i].line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func diffLines(a, b []string) []diffOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for i := 0; i < prefix; i++ {
		ops = append(ops, diffOp{kind: diffEqual, line: a[i]})
	}

	midA := a[prefix:aEnd]
	midB := b[prefix:bEnd]
	if len(midA)*len(midB) > 2_000_000 {
		for _, l := range midA {
			ops = append(ops, diffOp{kind: diffDelete, line: l})
		}
		for _, l := range midB {
			ops = append(ops, diffOp{kind: diffInsert, line: l})
		}
	} else {
		ops = append(ops, lcsDiff(midA, midB)...)
	}

	for i := aEnd; i < len(a); i++ {
		ops = append(ops, diffOp{kind: diffEqual, line: a[i]})
	}
	return ops
}

func lcsDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{kind: diffEqual, line: a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{kind: diffDelete, line: a[i]})
			i++
		} else {
			ops = append(ops, diffOp{kind: diffInsert, line: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: diffDelete, line: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: diffInsert, line: b[j]})
	}
	return ops
}
