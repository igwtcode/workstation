package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/fang/v2"
	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/cli"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

func TestFangErrorHandlerSuppressesCancel(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	cancelCases := []struct {
		name string
		err  error
	}{
		{name: "picker cancelled", err: picker.ErrCancelled},
		{name: "wrapped picker cancelled", err: fmt.Errorf("repo pick: %w", picker.ErrCancelled)},
		{name: "huh user aborted", err: huh.ErrUserAborted},
		{name: "context canceled", err: context.Canceled},
	}

	for _, tc := range cancelCases {
		t.Run(tc.name, func(t *testing.T) {
			var errBuf bytes.Buffer
			cmd := &cobra.Command{
				Use:           "ws-test",
				SilenceErrors: true,
				SilenceUsage:  true,
				RunE: func(_ *cobra.Command, _ []string) error {
					return tc.err
				},
			}
			cmd.SetArgs([]string{})
			cmd.SetErr(&errBuf)

			err := fang.Execute(
				context.Background(),
				cmd,
				fang.WithErrorHandler(makeFangErrorHandler(cmd, nil)),
			)
			if err == nil {
				t.Fatalf("expected non-nil error for %s", tc.name)
			}
			if got := cli.ExitCode(err); got != 130 {
				t.Fatalf("expected exit code 130 for %s, got %d", tc.name, got)
			}
			if errBuf.Len() != 0 {
				t.Fatalf("expected zero stderr bytes for %s, got %q", tc.name, errBuf.String())
			}
		})
	}

	t.Run("real error prints single unix line", func(t *testing.T) {
		var errBuf bytes.Buffer
		cmd := &cobra.Command{
			Use:           "ws-test",
			SilenceErrors: true,
			SilenceUsage:  true,
			RunE: func(_ *cobra.Command, _ []string) error {
				return errors.New("disk full")
			},
		}
		cmd.SetArgs([]string{})
		cmd.SetErr(&errBuf)

		err := fang.Execute(
			context.Background(),
			cmd,
			fang.WithErrorHandler(makeFangErrorHandler(cmd, nil)),
		)
		if err == nil {
			t.Fatalf("expected error")
		}
		if got := cli.ExitCode(err); got != 1 {
			t.Fatalf("expected exit code 1, got %d", got)
		}
		if got := errBuf.String(); got != "error: disk full\n" {
			t.Fatalf("expected concise Unix error line %q, got %q", "error: disk full\n", got)
		}
	})
}

func TestFangErrorHandlerPrintsUsageOnArgError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var errBuf bytes.Buffer
	root := &cobra.Command{Use: "ws", SilenceErrors: true, SilenceUsage: true}
	sub := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, _ []string) error {
			return nil
		},
	}
	root.AddCommand(sub)
	args := []string{"set"}
	root.SetArgs(args)
	root.SetErr(&errBuf)

	err := fang.Execute(
		context.Background(),
		root,
		fang.WithErrorHandler(makeFangErrorHandler(root, args)),
	)
	if err == nil {
		t.Fatalf("expected arg error")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("expected exit code 2 for arg error, got %d", got)
	}
	out := errBuf.String()
	for _, want := range []string{
		"error: accepts 2 arg(s), received 0",
		"usage: ws set <key> <value>",
		"run 'ws set --help'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in usage error output:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"Available Commands:", "Flags:", "Set a config value"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("expected concise usage error without %q:\n%s", forbidden, out)
		}
	}
}

func TestFangErrorHandlerConciseOnUnknownCommand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var errBuf bytes.Buffer
	root := cli.NewRootCmd(&cli.GlobalOpts{Repo: t.TempDir(), Home: t.TempDir()})
	args := []string{"pkg", "lst"}
	root.SetArgs(args)
	root.SetErr(&errBuf)

	err := fang.Execute(
		context.Background(),
		root,
		fang.WithErrorHandler(makeFangErrorHandler(root, args)),
	)
	if err == nil {
		t.Fatalf("expected unknown subcommand error")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
	out := errBuf.String()
	for _, want := range []string{
		`unknown command "lst" for "ws pkg"`,
		"Did you mean this?",
		"list",
		"usage: ws pkg",
		"run 'ws pkg --help'",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Workstation") || strings.Contains(out, "Global Flags") {
		t.Fatalf("must not dump full root help on usage error:\n%s", out)
	}
}
