package cli

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

var confirmAllowScope = defaultConfirmAllowScope

func defaultConfirmAllowScope(cmd *cobra.Command, ctx *app.Context, scope string, actions []string) (bool, error) {
	if !isInteractiveTTY(cmd, ctx) {
		return false, nil
	}
	stepWord := "step"
	if len(actions) != 1 {
		stepWord = "steps"
	}
	preview := strings.Join(actions, ", ")
	if len(actions) > 3 {
		preview = fmt.Sprintf("%s, +%d more", strings.Join(actions[:3], ", "), len(actions)-3)
	}
	var confirmed bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Allow %s for %d planned %s?", scope, len(actions), stepWord)).
			Description(preview).
			Affirmative("Allow").
			Negative("Skip").
			Value(&confirmed),
	)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
	if err := form.Run(); err != nil {
		return false, err
	}
	return confirmed, nil
}

func promptMissingAllows(cmd *cobra.Command, ctx *app.Context, plan *engine.Plan) error {
	if ctx == nil || ctx.DryRun || ctx.NonInteractive || ctx.JSON || plan == nil {
		return nil
	}
	order, byScope := pkgmgr.MissingPlanScopes(ctx.Allow, plan)
	if len(order) == 0 {
		return nil
	}
	for _, scope := range order {
		ok, err := confirmAllowScope(cmd, ctx, scope, byScope[scope])
		if err != nil {
			return err
		}
		if ok {
			grantAllowScope(&ctx.Allow, scope)
		}
	}
	return nil
}

func grantAllowScope(a *app.AllowSet, scope string) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "replace":
		a.Replace = true
	case "sudo":
		a.Sudo = true
	case "remove":
		a.Remove = true
	case "script":
		a.Script = true
	}
}

func consentSkippedError(res *engine.ApplyResult) error {
	if res == nil || res.ConsentSkipped <= 0 {
		return nil
	}
	stepWord := "steps"
	if res.ConsentSkipped == 1 {
		stepWord = "step"
	}
	scopes := make([]string, 0, len(res.MissingAllows))
	for _, sc := range res.MissingAllows {
		sc = strings.TrimSpace(sc)
		if sc != "" && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	var hint string
	switch {
	case len(scopes) > 0 && res.Declined > 0:
		hint = fmt.Sprintf(", rerun with --allow %s (declined hooks need --yes)", strings.Join(scopes, ","))
	case len(scopes) > 0:
		hint = ", rerun with --allow " + strings.Join(scopes, ",")
	case res.Declined > 0:
		hint = ", rerun with --yes to accept declined hooks"
	default:
		hint = " due to missing consent"
	}
	return &ExitCodeError{
		Code: 1,
		Err:  fmt.Errorf("%d %s skipped%s", res.ConsentSkipped, stepWord, hint),
	}
}
