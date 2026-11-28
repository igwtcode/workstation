package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

func ResolveAppTemplate(repoRoot, appName string) (config.RegistryEntry, error) {
	q := strings.ToLower(strings.TrimSpace(appName))
	if q == "" {
		return config.RegistryEntry{}, fmt.Errorf("app name is required")
	}

	entries, err := config.LoadThemeRegistry(repoRoot)
	if err != nil {
		return config.RegistryEntry{}, err
	}

	for _, e := range entries {
		if strings.ToLower(e.App) == q {
			return e, nil
		}
	}

	var exactPathMatches []config.RegistryEntry
	for _, e := range entries {
		if strings.EqualFold(e.Template, q) ||
			strings.EqualFold(e.Output, q) ||
			strings.EqualFold(filepath.Base(e.Template), q) ||
			strings.EqualFold(filepath.Base(e.Output), q) {
			exactPathMatches = append(exactPathMatches, e)
		}
	}
	if len(exactPathMatches) == 1 {
		return exactPathMatches[0], nil
	}
	if len(exactPathMatches) > 1 {
		return config.RegistryEntry{}, fmt.Errorf("ambiguous app %q matches: %s", appName, joinAppNames(exactPathMatches))
	}

	var prefixMatches []config.RegistryEntry
	for _, e := range entries {
		if strings.HasPrefix(strings.ToLower(e.App), q) {
			prefixMatches = append(prefixMatches, e)
		}
	}
	if len(prefixMatches) == 1 {
		return prefixMatches[0], nil
	}
	if len(prefixMatches) > 1 {
		return config.RegistryEntry{}, fmt.Errorf("ambiguous app %q matches: %s", appName, joinAppNames(prefixMatches))
	}

	var subMatches []config.RegistryEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.App), q) || strings.Contains(strings.ToLower(e.Template), q) {
			subMatches = append(subMatches, e)
		}
	}
	if len(subMatches) == 1 {
		return subMatches[0], nil
	}
	if len(subMatches) > 1 {
		return config.RegistryEntry{}, fmt.Errorf("ambiguous app %q matches: %s", appName, joinAppNames(subMatches))
	}

	return config.RegistryEntry{}, fmt.Errorf("no template found for %q (available: %s)", appName, joinAppNames(entries))
}

func joinAppNames(entries []config.RegistryEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.App)
	}
	return strings.Join(names, ", ")
}

func EditAppPlan(ctx *app.Context, entry config.RegistryEntry, preHash string) (*Plan, error) {
	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		return nil, err
	}
	themeID, pal, err := loadActivePalette(ctx.RepoRoot, "", ledger)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Verb:      "apply",
		Host:      FormatPlanHost(ctx),
		Title:     fmt.Sprintf("Re-render %s (%s)", entry.App, themeID),
		Theme:     themeID,
		CreatedAt: time.Now().UTC(),
		Actions:   []Action{},
		Metadata: map[string]string{
			"partial": "true",
			"only":    entry.App,
		},
	}

	act, changed, err := planRenderEntry(ctx.RepoRoot, entry, themeID, pal)
	if err != nil {
		return nil, err
	}
	if !changed {
		plan.Skipped = append(plan.Skipped, fmt.Sprintf("unchanged %s", entry.Output))
		return plan, nil
	}
	if preHash != "" && act.Metadata["sha256"] == preHash {
		plan.Skipped = append(plan.Skipped, fmt.Sprintf("unchanged %s", entry.Output))
		return plan, nil
	}

	plan.Actions = append(plan.Actions, act)
	if entry.PostHook != "" {
		plan.Actions = append(plan.Actions, Action{
			ID:      fmt.Sprintf("hook:%s", entry.App),
			Kind:    ActionHook,
			Target:  entry.App,
			Summary: fmt.Sprintf("post-hook %s: %s", entry.App, entry.PostHook),
			Detail:  entry.PostHook,
			Risk:    RiskSafe,
			Command: []string{"bash", "-c", entry.PostHook},
			Metadata: map[string]string{
				"app":  entry.App,
				"hook": entry.PostHook,
			},
		})
	}
	return plan, nil
}

func ExecEditAfterSave(ctx *app.Context, appName string) (*ApplyResult, error) {
	entry, err := ResolveAppTemplate(ctx.RepoRoot, appName)
	if err != nil {
		return nil, err
	}

	var preHash string
	outAbs := filepath.Join(ctx.RepoRoot, entry.Output)
	if existing, rerr := os.ReadFile(outAbs); rerr == nil {
		preHash = SHA256Hex(existing)
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return nil, rerr
	}

	plan, err := EditAppPlan(ctx, entry, preHash)
	if err != nil {
		return nil, err
	}
	if len(plan.Actions) == 0 {
		return &ApplyResult{
			Applied:  0,
			Skipped:  len(plan.Skipped),
			Warnings: append([]string{}, plan.Warnings...),
		}, nil
	}
	return ExecApply(ctx, plan)
}
