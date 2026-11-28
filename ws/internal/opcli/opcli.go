package opcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/term"
)

var ErrDocumentNotFound = errors.New("1Password document not found")

const (
	DefaultTimeout     = 30 * time.Second
	InteractiveTimeout = 3 * time.Minute
)

var (
	ttyDisabled atomic.Bool
	isTerminal  = term.IsTerminal
)

func DisableTTY() func() {
	prev := ttyDisabled.Swap(true)
	return func() { ttyDisabled.Store(prev) }
}

type docMeta struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func LookPath() (string, error) {
	return exec.LookPath("op")
}

func RequireSignedIn(ctx context.Context) (string, error) {
	opBin, err := LookPath()
	if err != nil {
		return "", fmt.Errorf("1Password CLI (op) not found on PATH")
	}
	out, err := runCombined(ctx, opBin, nil, true, "whoami")
	if err != nil {
		return "", fmt.Errorf("not signed in to 1Password (op whoami failed): %s", strings.TrimSpace(string(out)))
	}
	return opBin, nil
}

func IsSignedIn(ctx context.Context) (string, bool) {
	opBin, err := LookPath()
	if err != nil {
		return "", false
	}
	if _, err := runCombined(ctx, opBin, nil, false, "whoami"); err != nil {
		return "", false
	}
	return opBin, true
}

func FindDocument(ctx context.Context, opBin, vault, title string) (string, error) {
	args := append([]string{"document", "list", "--format", "json"}, vaultFlags(vault)...)
	out, _, err := runStdout(ctx, opBin, nil, true, args...)
	if err != nil {
		return "", err
	}
	var docs []docMeta
	if err := json.Unmarshal(out, &docs); err != nil {
		return "", err
	}
	var matches []string
	for _, d := range docs {
		if d.Title == title {
			matches = append(matches, d.ID)
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous 1Password document %q (%d matches); set WS_OP_VAULT", title, len(matches))
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", nil
}

func GetDocument(ctx context.Context, opBin, vault, title, dest string) error {
	id, err := FindDocument(ctx, opBin, vault, title)
	if err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: %q", ErrDocumentNotFound, title)
	}
	_ = os.Remove(dest)
	args := append([]string{"document", "get", id, "--out-file", dest}, vaultFlags(vault)...)
	if out, err := runCombined(ctx, opBin, nil, true, args...); err != nil {
		return fmt.Errorf("op document get %q: %w (%s)", title, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func GetDocumentBytes(ctx context.Context, opBin, vault, title string) ([]byte, error) {
	id, err := FindDocument(ctx, opBin, vault, title)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("%w: %q", ErrDocumentNotFound, title)
	}
	args := append([]string{"document", "get", id}, vaultFlags(vault)...)
	out, stderr, err := runStdout(ctx, opBin, nil, true, args...)
	if err != nil {
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return nil, fmt.Errorf("op document get %q: %w (%s)", title, err, msg)
		}
		return nil, fmt.Errorf("op document get %q: %w", title, err)
	}
	return out, nil
}

func PutDocument(ctx context.Context, opBin, vault, title, srcFile string) error {
	id, err := FindDocument(ctx, opBin, vault, title)
	if err != nil {
		return err
	}
	if id != "" {
		args := append([]string{"document", "edit", id, srcFile}, vaultFlags(vault)...)
		if out, err := runCombined(ctx, opBin, nil, true, args...); err != nil {
			return fmt.Errorf("op document edit %q: %w (%s)", title, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	args := append([]string{"document", "create", srcFile, "--title", title, "--file-name", filepath.Base(srcFile)}, vaultFlags(vault)...)
	if out, err := runCombined(ctx, opBin, nil, true, args...); err != nil {
		return fmt.Errorf("op document create %q: %w (%s)", title, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func PutDocumentBytes(ctx context.Context, opBin, vault, title string, payload []byte) error {
	id, err := FindDocument(ctx, opBin, vault, title)
	if err != nil {
		return err
	}
	var args []string
	if id != "" {
		args = append([]string{"document", "edit", id, "-", "--file-name", title}, vaultFlags(vault)...)
	} else {
		args = append([]string{"document", "create", "-", "--title", title, "--file-name", title}, vaultFlags(vault)...)
	}
	if out, err := runCombined(ctx, opBin, bytes.NewReader(payload), true, args...); err != nil {
		return fmt.Errorf("op document upload %q failed: %w (%s)", title, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func vaultFlags(vault string) []string {
	if vault == "" {
		return nil
	}
	return []string{"--vault", vault}
}

func isInteractiveTTY(stdin io.Reader, allowTTY bool) bool {
	if !allowTTY || ttyDisabled.Load() {
		return false
	}
	if stdin == nil && isTerminal(os.Stdin.Fd()) {
		return true
	}
	return isTerminal(os.Stderr.Fd())
}

func timeoutFor(stdin io.Reader, allowTTY bool) time.Duration {
	if isInteractiveTTY(stdin, allowTTY) {
		return InteractiveTimeout
	}
	return DefaultTimeout
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func runStdout(ctx context.Context, opBin string, stdin io.Reader, allowTTY bool, args ...string) ([]byte, []byte, error) {
	cctx, cancel := withTimeout(ctx, timeoutFor(stdin, allowTTY))
	defer cancel()
	cmd := exec.CommandContext(cctx, opBin, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	wireIO(cmd, stdin, &stderrBuf, allowTTY)
	err := cmd.Run()
	if cctx.Err() != nil {
		return stdoutBuf.Bytes(), stderrBuf.Bytes(), cctx.Err()
	}
	return stdoutBuf.Bytes(), stderrBuf.Bytes(), err
}

func runCombined(ctx context.Context, opBin string, stdin io.Reader, allowTTY bool, args ...string) ([]byte, error) {
	cctx, cancel := withTimeout(ctx, timeoutFor(stdin, allowTTY))
	defer cancel()
	cmd := exec.CommandContext(cctx, opBin, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	var combined bytes.Buffer
	cmd.Stdout = &combined
	wireIO(cmd, stdin, &combined, allowTTY)
	err := cmd.Run()
	if cctx.Err() != nil {
		return combined.Bytes(), cctx.Err()
	}
	return combined.Bytes(), err
}

func wireIO(cmd *exec.Cmd, stdin io.Reader, stderrCapture io.Writer, allowTTY bool) {
	allowTTY = allowTTY && !ttyDisabled.Load()
	if stdin != nil {
		cmd.Stdin = stdin
	} else if allowTTY && isTerminal(os.Stdin.Fd()) {
		cmd.Stdin = os.Stdin
	}
	if allowTTY && isTerminal(os.Stderr.Fd()) {
		cmd.Stderr = io.MultiWriter(os.Stderr, stderrCapture)
	} else {
		cmd.Stderr = stderrCapture
	}
}
