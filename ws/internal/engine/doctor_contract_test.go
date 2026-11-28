package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/config"
)

func TestDoctorReportsRepoContractViolations(t *testing.T) {
	f := newSyntheticFixture(t)

	badHook := filepath.Join(f.repo, "os", "bad.sh")
	if err := os.MkdirAll(filepath.Dir(badHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badHook, []byte("#!/usr/bin/env bash\nif true; then\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	presetsPath := filepath.Join(f.repo, "presets.toml")
	raw, err := os.ReadFile(presetsPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(
		string(raw),
		`secrets = "none"`,
		"secrets = \"none\"\nos_hooks = [\"os/bad.sh\", \"os/nope.sh\"]",
		1,
	)
	if err := os.WriteFile(presetsPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(f.repo, "links", "extra.links"), []byte("config/missing-src ~/.missing-src\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	got := map[string]Finding{}
	for _, fd := range rep.Findings {
		got[fd.ID] = fd
	}
	for _, want := range []string{
		"contract:os-hook-syntax:os/bad.sh",
		"contract:os-hook-missing:os/nope.sh",
		"contract:link-source:links/extra.links:config/missing-src",
	} {
		fd, ok := got[want]
		if !ok {
			t.Errorf("missing expected doctor contract finding %q; got %v", want, keysOf(got))
			continue
		}
		if fd.Severity != SeverityError || fd.Category != "contracts" {
			t.Errorf("unexpected finding metadata for %q: %+v", want, fd)
		}
	}

	prevVer := config.BinaryVersion
	config.BinaryVersion = "0.0.1"
	t.Cleanup(func() { config.BinaryVersion = prevVer })

	if err := os.WriteFile(presetsPath, []byte("ws_min_version = \"0.5.0\"\n\n[preset.base]\ndescription = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repVer, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor version check: %v", err)
	}
	foundVerErr := false
	for _, fd := range repVer.Findings {
		if fd.ID == "preset:load-error" && strings.Contains(fd.Detail, "0.5.0") {
			foundVerErr = true
			break
		}
	}
	if !foundVerErr {
		t.Errorf("expected doctor to report ws_min_version error, got %+v", repVer.Findings)
	}
}
