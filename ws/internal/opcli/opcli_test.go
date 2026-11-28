package opcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpCLIWrapperDocumentOperationsAndTimeout(t *testing.T) {
	binDir := t.TempDir()
	storeDir := t.TempDir()
	mockScript := `#!/bin/sh
set -eu
case "${1:-}" in
  whoami)
    if [ "${MOCK_OP_SLEEP:-0}" != "0" ]; then
      sleep "$MOCK_OP_SLEEP"
    fi
    if [ "${MOCK_OP_SIGNED_IN:-1}" = "1" ]; then
      printf 'user@example.invalid\n'
      exit 0
    fi
    printf 'not signed in\n' >&2
    exit 1
    ;;
  document)
    sub="${2:-}"
    case "$sub" in
      list)
        if [ "${MOCK_OP_AMBIGUOUS:-0}" = "1" ]; then
          printf '[{"id":"a","title":"dup"},{"id":"b","title":"dup"}]\n'
          exit 0
        fi
        if [ -f "$MOCK_OP_STORE/doc1" ]; then
          printf '[{"id":"doc1","title":"sample.bin"}]\n'
        else
          printf '[]\n'
        fi
        ;;
      create)
        src="$3"
        if [ "$src" = "-" ]; then
          cat > "$MOCK_OP_STORE/doc1"
        else
          cp "$src" "$MOCK_OP_STORE/doc1"
        fi
        ;;
      edit)
        src="$4"
        if [ "$src" = "-" ]; then
          cat > "$MOCK_OP_STORE/doc1"
        else
          cp "$src" "$MOCK_OP_STORE/doc1"
        fi
        ;;
      get)
        if [ "${4:-}" = "--out-file" ]; then
          cp "$MOCK_OP_STORE/doc1" "$5"
        else
          cat "$MOCK_OP_STORE/doc1"
        fi
        ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "op"), []byte(mockScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":/usr/bin:/bin")
	t.Setenv("MOCK_OP_STORE", storeDir)

	opBin, err := RequireSignedIn(context.Background())
	if err != nil {
		t.Fatalf("RequireSignedIn: %v", err)
	}
	if _, ok := IsSignedIn(context.Background()); !ok {
		t.Fatalf("expected IsSignedIn true")
	}

	if err := PutDocumentBytes(context.Background(), opBin, "Private", "sample.bin", []byte("hello-bytes")); err != nil {
		t.Fatalf("PutDocumentBytes create: %v", err)
	}
	gotBytes, err := GetDocumentBytes(context.Background(), opBin, "Private", "sample.bin")
	if err != nil || string(gotBytes) != "hello-bytes" {
		t.Fatalf("GetDocumentBytes: got=%q err=%v", string(gotBytes), err)
	}

	srcFile := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(srcFile, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PutDocument(context.Background(), opBin, "Private", "sample.bin", srcFile); err != nil {
		t.Fatalf("PutDocument edit: %v", err)
	}
	dstFile := filepath.Join(t.TempDir(), "download.bin")
	if err := GetDocument(context.Background(), opBin, "Private", "sample.bin", dstFile); err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	gotFile, err := os.ReadFile(dstFile)
	if err != nil || string(gotFile) != "from-file" {
		t.Fatalf("downloaded file mismatch: got=%q err=%v", string(gotFile), err)
	}

	t.Setenv("MOCK_OP_AMBIGUOUS", "1")
	if _, err := FindDocument(context.Background(), opBin, "", "dup"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error, got %v", err)
	}
	t.Setenv("MOCK_OP_AMBIGUOUS", "0")

	t.Setenv("MOCK_OP_SLEEP", "5")
	shortCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := RequireSignedIn(shortCtx); !errors.Is(err, context.DeadlineExceeded) && (err == nil || !strings.Contains(err.Error(), "whoami")) {
		t.Fatalf("expected timeout failure from RequireSignedIn, got %v", err)
	}
	if _, ok := IsSignedIn(shortCtx); ok {
		t.Fatalf("expected IsSignedIn false on timeout")
	}
}

func TestDisableTTYRestores(t *testing.T) {
	restore := DisableTTY()
	if !ttyDisabled.Load() {
		t.Fatal("expected tty passthrough disabled")
	}
	inner := DisableTTY()
	inner()
	if !ttyDisabled.Load() {
		t.Fatal("nested restore must keep outer disable")
	}
	restore()
	if ttyDisabled.Load() {
		t.Fatal("expected tty passthrough restored")
	}
}

func TestTimeoutInteractiveVsNonInteractive(t *testing.T) {
	prevIsTerm := isTerminal
	t.Cleanup(func() { isTerminal = prevIsTerm })

	isTerminal = func(fd uintptr) bool { return false }
	if got := timeoutFor(nil, true); got != DefaultTimeout {
		t.Fatalf("non-TTY timeout = %v, want %v", got, DefaultTimeout)
	}

	isTerminal = func(fd uintptr) bool { return fd == os.Stdin.Fd() }
	if got := timeoutFor(nil, true); got != InteractiveTimeout {
		t.Fatalf("stdin-TTY interactive timeout = %v, want %v", got, InteractiveTimeout)
	}
	if got := timeoutFor(nil, false); got != DefaultTimeout {
		t.Fatalf("allowTTY=false timeout = %v, want %v", got, DefaultTimeout)
	}
	if got := timeoutFor(bytes.NewReader([]byte("payload")), true); got != DefaultTimeout {
		t.Fatalf("piped stdin without stderr TTY timeout = %v, want %v", got, DefaultTimeout)
	}

	isTerminal = func(fd uintptr) bool { return fd == os.Stderr.Fd() }
	if got := timeoutFor(bytes.NewReader([]byte("payload")), true); got != InteractiveTimeout {
		t.Fatalf("stderr-TTY interactive timeout = %v, want %v", got, InteractiveTimeout)
	}

	restore := DisableTTY()
	if got := timeoutFor(nil, true); got != DefaultTimeout {
		t.Fatalf("DisableTTY timeout = %v, want %v", got, DefaultTimeout)
	}
	restore()

	cctx, cancel := withTimeout(context.Background(), InteractiveTimeout)
	defer cancel()
	dl, ok := cctx.Deadline()
	if !ok {
		t.Fatal("expected deadline from withTimeout")
	}
	if rem := time.Until(dl); rem < 2*time.Minute+50*time.Second || rem > 3*time.Minute+time.Second {
		t.Fatalf("interactive deadline remaining = %v, want ~3m", rem)
	}

	existingCtx, existingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer existingCancel()
	keptCtx, keptCancel := withTimeout(existingCtx, InteractiveTimeout)
	defer keptCancel()
	keptDL, _ := keptCtx.Deadline()
	if rem := time.Until(keptDL); rem > 3*time.Second {
		t.Fatalf("expected existing caller deadline preserved, got remaining %v", rem)
	}
}
