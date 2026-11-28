package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/spf13/cobra"
)

type vmTargetSpec struct {
	Distro string
	Image  string
	Preset string
}

func resolveVMTarget(distro, imageOverride string) (vmTargetSpec, error) {
	d := strings.ToLower(strings.TrimSpace(distro))
	var spec vmTargetSpec
	switch d {
	case "ubuntu":
		spec = vmTargetSpec{Distro: "ubuntu", Image: "ubuntu:24.04", Preset: "ubuntu-runner"}
	case "debian":
		spec = vmTargetSpec{Distro: "debian", Image: "debian:bookworm-slim", Preset: "debian-server"}
	case "arch":
		spec = vmTargetSpec{Distro: "arch", Image: "archlinux:latest", Preset: "base"}
	default:
		return vmTargetSpec{}, fmt.Errorf("unsupported test target %q (expected ubuntu, debian, or arch)", distro)
	}
	if strings.TrimSpace(imageOverride) != "" {
		spec.Image = strings.TrimSpace(imageOverride)
	}
	return spec, nil
}

func newTestVMCmd(opts *GlobalOpts) *cobra.Command {
	var targetDistro string
	var imageOverride string

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Verify workstation setup in Docker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if ctx.RepoRoot == "" {
				return fmt.Errorf("workstation repository root not found (pass --repo)")
			}

			spec, err := resolveVMTarget(targetDistro, imageOverride)
			if err != nil {
				return err
			}

			dockerArch := detectDockerArch(cmdContext(cmd), !ctx.DryRun)
			prebuilt := filepath.Join(ctx.RepoRoot, "ws", "dist", "ws-linux-"+dockerArch)
			script := buildVMTestScript(spec.Preset)

			if ctx.DryRun {
				payload := map[string]any{
					"distro":     spec.Distro,
					"image":      spec.Image,
					"preset":     spec.Preset,
					"dockerArch": dockerArch,
					"prebuilt":   prebuilt,
					"script":     script,
					"dryRun":     true,
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), payload)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] docker VM test: distro=%s image=%s preset=%s arch=%s\n",
					spec.Distro, spec.Image, spec.Preset, dockerArch)
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] binary lookup: %s (or GOOS=linux GOARCH=%s CGO_ENABLED=0 go build ./cmd/ws)\n",
					prebuilt, dockerArch)
				return nil
			}

			if _, err := exec.LookPath("docker"); err != nil {
				return fmt.Errorf("docker not found on PATH: %w", err)
			}

			binPath := prebuilt
			cleanupBin := ""
			if !isExecutable(binPath) {
				tmpDir, err := os.MkdirTemp("", "ws-vm-bin-*")
				if err != nil {
					return err
				}
				cleanupBin = tmpDir
				defer os.RemoveAll(cleanupBin)

				binPath = filepath.Join(tmpDir, "ws")
				buildCmd := exec.CommandContext(cmdContext(cmd), "go", "build", "-o", binPath, "./cmd/ws")
				buildCmd.Dir = filepath.Join(ctx.RepoRoot, "ws")
				buildCmd.Env = shell.MergeEnv(os.Environ(), []string{
					"GOOS=linux",
					"GOARCH=" + dockerArch,
					"CGO_ENABLED=0",
				})
				if out, err := buildCmd.CombinedOutput(); err != nil {
					return fmt.Errorf("cross-build linux/%s ws binary failed: %w\n%s", dockerArch, err, strings.TrimSpace(string(out)))
				}
			}

			dockerArgs := []string{
				"run", "--rm",
				"-v", ctx.RepoRoot + ":/repo:ro",
				"-v", binPath + ":/usr/local/bin/ws:ro",
				spec.Image,
				"bash", "-euo", "pipefail", "-c", script,
			}
			runCmd := exec.CommandContext(cmdContext(cmd), "docker", dockerArgs...)
			var outBuf bytes.Buffer
			if ctx.JSON {
				runCmd.Stdout = &outBuf
				runCmd.Stderr = &outBuf
			} else {
				runCmd.Stdout = cmd.OutOrStdout()
				runCmd.Stderr = cmd.ErrOrStderr()
			}
			if err := runCmd.Run(); err != nil {
				if ctx.JSON && outBuf.Len() > 0 {
					return wrapCmdExitErr(fmt.Errorf("container verification (%s) failed: %w\n%s", spec.Image, err, strings.TrimSpace(outBuf.String())))
				}
				return wrapCmdExitErr(fmt.Errorf("container verification (%s) failed: %w", spec.Image, err))
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"distro": spec.Distro,
					"image":  spec.Image,
					"preset": spec.Preset,
					"output": strings.TrimRight(outBuf.String(), "\r\n"),
					"passed": true,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Container verification passed (%s / %s).\n", spec.Image, spec.Preset)
			return nil
		},
	}

	cmd.Flags().StringVar(&targetDistro, "in", "ubuntu", "Container distro target: ubuntu, debian, or arch")
	cmd.Flags().StringVar(&imageOverride, "image", "", "Override container image tag")
	_ = cmd.RegisterFlagCompletionFunc("in", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"ubuntu", "debian", "arch"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func detectDockerArch(parent context.Context, probeDaemon bool) string {
	if !probeDaemon {
		return runtime.GOARCH
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return runtime.GOARCH
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Architecture}}")
	c.WaitDelay = 500 * time.Millisecond
	if out, err := c.Output(); err == nil {
		raw := strings.ToLower(strings.TrimSpace(string(out)))
		switch raw {
		case "aarch64", "arm64":
			return "arm64"
		case "x86_64", "amd64":
			return "amd64"
		}
	}
	return runtime.GOARCH
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}

func buildVMTestScript(preset string) string {
	return fmt.Sprintf(`set -euo pipefail
cp -a /repo /tmp/repo
export HOME=/tmp/home
export XDG_CONFIG_HOME=/tmp/home/.config
export XDG_DATA_HOME=/tmp/home/.local/share
export XDG_STATE_HOME=/tmp/home/.local/state
export XDG_CACHE_HOME=/tmp/home/.cache
mkdir -p "$HOME"

echo "==> 1/4 ws doctor"
ws --repo /tmp/repo doctor

echo "==> 2/4 ws apply --preset %s"
ws --repo /tmp/repo apply --preset %s --non-interactive --allow replace

echo "==> 3/4 idempotency check (2nd ws apply --dry-run --json == 0 actions)"
plan_json="$(ws --repo /tmp/repo apply --preset %s --dry-run --json)"
if ! printf '%%s\n' "$plan_json" | grep -q '"actions": \[\]'; then
  echo "Expected 0 actions on second apply, got:"
  printf '%%s\n' "$plan_json"
  exit 1
fi

echo "==> 4/4 ws theme catppuccin-dark"
ws --repo /tmp/repo theme catppuccin-dark --non-interactive
`, preset, preset, preset)
}
