package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/cli"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

var Version = "dev"

func makeFangErrorHandler(root *cobra.Command, rawArgs []string) fang.ErrorHandler {
	return func(w io.Writer, _ fang.Styles, err error) {
		if cli.IsCancelledError(err) {
			return
		}
		prefix := "error:"
		if !ui.ColorDisabled() && root != nil && ui.IsTTY(root.ErrOrStderr()) {
			prefix = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(9)).Render("error:")
		}
		_, _ = fmt.Fprintf(w, "%s %s\n", prefix, err.Error())
		if cli.IsUsageError(err) && root != nil {
			target := cli.ResolveCommandForArgs(root, rawArgs)
			if target != nil {
				_, _ = fmt.Fprintf(w, "usage: %s\n", target.UseLine())
				_, _ = fmt.Fprintf(w, "run '%s --help'\n", target.CommandPath())
			}
		}
	}
}

func main() {
	opts := &cli.GlobalOpts{Version: Version}
	rootCmd := cli.NewRootCmd(opts)

	if handled, err := cli.TryPreDispatchPlugin(context.Background(), rootCmd, opts, os.Args[1:]); handled {
		if err != nil {
			if !cli.IsCancelledError(err) {
				fmt.Fprintf(os.Stderr, "error: %s\n", err)
			}
			os.Exit(cli.ExitCode(err))
		}
		return
	}

	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithVersion(Version),
		fang.WithColorSchemeFunc(ui.FangScheme),
		fang.WithErrorHandler(makeFangErrorHandler(rootCmd, os.Args[1:])),
		fang.WithNotifySignal(os.Interrupt),
	); err != nil {
		os.Exit(cli.ExitCode(err))
	}
}

var _ = errors.As
var _ = exec.ErrNotFound
