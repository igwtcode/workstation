package engine

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func TestRunActionCommandJSONKeepsChildOutputOffStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	ctx := &app.Context{JSON: true, HomeDir: t.TempDir()}
	act := Action{ID: "probe", NeedsTTY: true, Command: []string{"sh", "-c", "echo child-stdout-leak"}}
	runErr := runActionCommand(ctx, act)
	os.Stdout = orig
	_ = w.Close()
	out := <-done
	if runErr != nil {
		t.Fatalf("runActionCommand: %v", runErr)
	}
	if out != "" {
		t.Fatalf("child output leaked to stdout under --json: %q", out)
	}
}
