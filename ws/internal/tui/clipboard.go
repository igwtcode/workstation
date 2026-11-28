package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

var (
	clipboardWrite     = clipboard.WriteAll
	osc52Write         = writeOSC52
	urlOpener          = openExternalURLImpl
	clipGetenv         = os.Getenv
	clipLookPath       = exec.LookPath
	openURLCmdBuilder  = defaultOpenURLCmd
	openURLWaitTimeout = 500 * time.Millisecond
)

func formatOSC52Sequence(text string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := "\x1b]52;c;" + encoded + "\x07"
	if strings.TrimSpace(clipGetenv("TMUX")) != "" {
		return "\x1bPtmux;\x1b" + seq + "\x1b\\"
	}
	if strings.TrimSpace(clipGetenv("STY")) != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(clipGetenv("TERM"))), "screen") {
		return "\x1bP" + seq + "\x1b\\"
	}
	return seq
}

func writeOSC52(text string) error {
	seq := formatOSC52Sequence(text)
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		_, werr := io.WriteString(tty, seq)
		return werr
	}
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		_, werr := io.WriteString(os.Stdout, seq)
		return werr
	}
	return fmt.Errorf("no terminal available for OSC 52 clipboard")
}

func copyToClipboard(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}
	if strings.TrimSpace(clipGetenv("SSH_TTY")) != "" {
		if oscErr := osc52Write(text); oscErr == nil {
			return nil
		} else if clipErr := clipboardWrite(text); clipErr == nil {
			return nil
		} else {
			return fmt.Errorf("OSC 52 failed (%v); clipboard failed (%v)", oscErr, clipErr)
		}
	}
	clipErr := clipboardWrite(text)
	if clipErr == nil {
		return nil
	}
	if oscErr := osc52Write(text); oscErr == nil {
		return nil
	} else {
		return fmt.Errorf("%v (OSC 52 fallback failed: %v)", clipErr, oscErr)
	}
}

func copyCmd(text, label string) tea.Cmd {
	return func() tea.Msg {
		if err := copyToClipboard(text); err != nil {
			return statusToastMsg{text: "Copy failed: " + err.Error(), isErr: true}
		}
		if label == "" {
			return statusToastMsg{text: "Copied to clipboard"}
		}
		return statusToastMsg{text: "Copied " + label}
	}
}

func openURLCmd(rawURL string) tea.Cmd {
	return func() tea.Msg {
		if err := openExternalURL(rawURL); err != nil {
			return statusToastMsg{text: "Open URL failed: " + err.Error(), isErr: true}
		}
		return statusToastMsg{text: "Opened in browser"}
	}
}

func openExternalURL(rawURL string) error {
	clean := strings.TrimSpace(rawURL)
	if clean == "" {
		return fmt.Errorf("URL is empty")
	}
	for _, r := range clean {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid URL")
		}
	}
	u, err := url.ParseRequestURI(clean)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("refusing non-http(s) URL")
	}
	return urlOpener(u.String())
}

func defaultOpenURLCmd(cleanURL string) (*exec.Cmd, string, error) {
	cmdName := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmdName = "open"
	}
	bin, err := clipLookPath(cmdName)
	if err != nil {
		return nil, cmdName, fmt.Errorf("%s not found on PATH", cmdName)
	}
	return exec.Command(bin, cleanURL), cmdName, nil
}

func openExternalURLImpl(cleanURL string) error {
	cmd, cmdName, err := openURLCmdBuilder(cleanURL)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", cmdName, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	waitFor := openURLWaitTimeout
	if waitFor <= 0 {
		waitFor = 500 * time.Millisecond
	}
	select {
	case waitErr := <-done:
		if waitErr != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				if idx := strings.IndexByte(detail, '\n'); idx >= 0 {
					detail = strings.TrimSpace(detail[:idx])
				}
				return fmt.Errorf("%s failed: %s", cmdName, detail)
			}
			return fmt.Errorf("%s failed: %w", cmdName, waitErr)
		}
		return nil
	case <-time.After(waitFor):
		return nil
	}
}
