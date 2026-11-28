package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/spf13/cobra"
)

const androidTestRepoXML = `<?xml version='1.0' encoding='utf-8'?>
<sdk:sdk-repository xmlns:sdk="http://schemas.android.com/sdk/android/repo/repository2/03">
  <remotePackage path="cmdline-tools;19.0">
    <archives><archive><complete><checksum type="sha1">0000</checksum><url>commandlinetools-linux-old.zip</url></complete></archive></archives>
  </remotePackage>
  <remotePackage path="cmdline-tools;latest">
    <archives>
      <archive><complete><size>1</size><checksum type="sha1">%s</checksum><url>commandlinetools-linux-42_latest.zip</url></complete><host-os>linux</host-os></archive>
      <archive><complete><size>1</size><checksum type="sha1">aaaa</checksum><url>commandlinetools-mac_x86_64-42_latest.zip</url></complete><host-os>macosx</host-os></archive>
      <archive><complete><size>1</size><checksum type="sha1">bbbb</checksum><url>commandlinetools-mac_arm64-42_latest.zip</url></complete><host-os>macosx</host-os></archive>
    </archives>
  </remotePackage>
</sdk:sdk-repository>
`

func androidTestZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, mode := range map[string]os.FileMode{
		"cmdline-tools/bin/sdkmanager":    0o755,
		"cmdline-tools/bin/avdmanager":    0o755,
		"cmdline-tools/source.properties": 0o644,
	} {
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("#!/bin/sh\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSelectAndroidCmdlineToolsPerHost(t *testing.T) {
	raw := []byte(fmt.Sprintf(androidTestRepoXML, "cccc"))
	cases := []struct {
		goos, abi, wantURL, wantSum string
	}{
		{"linux", "x86_64", "commandlinetools-linux-42_latest.zip", "cccc"},
		{"linux", "arm64-v8a", "commandlinetools-linux-42_latest.zip", "cccc"},
		{"darwin", "x86_64", "commandlinetools-mac_x86_64-42_latest.zip", "aaaa"},
		{"darwin", "arm64-v8a", "commandlinetools-mac_arm64-42_latest.zip", "bbbb"},
	}
	for _, tc := range cases {
		url, typ, sum, err := selectAndroidCmdlineTools(raw, androidCmdlineToolsPrefix(tc.goos, tc.abi))
		if err != nil || url != tc.wantURL || sum != tc.wantSum || typ != "sha1" {
			t.Fatalf("%s/%s: got url=%q type=%q sum=%q err=%v", tc.goos, tc.abi, url, typ, sum, err)
		}
	}
	if _, _, _, err := selectAndroidCmdlineTools(raw, "commandlinetools-win-"); err == nil {
		t.Fatalf("expected an error for a host with no archive")
	}
}

type androidTestEnv struct {
	cmd     *cobra.Command
	out     *bytes.Buffer
	ctx     *app.Context
	sdkRoot string
	avdHome string
	calls   []string
	deps    androidDeps
	server  *httptest.Server
}

func newAndroidTestEnv(t *testing.T, checksum string) *androidTestEnv {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "platform-tools\nsystem-images;android-36;google_apis;x86_64\nsystem-images;android-36;google_apis;arm64-v8a\n"
	if err := os.WriteFile(filepath.Join(repo, "pkg", "android.txt"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	zipRaw := androidTestZip(t)
	if checksum == "" {
		sum := sha1.Sum(zipRaw)
		checksum = hex.EncodeToString(sum[:])
	}
	e := &androidTestEnv{
		out:     &bytes.Buffer{},
		sdkRoot: filepath.Join(home, "sdk"),
		avdHome: filepath.Join(home, ".android", "avd"),
	}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repository2-3.xml":
			fmt.Fprintf(w, androidTestRepoXML, checksum)
		case "/commandlinetools-linux-42_latest.zip":
			_, _ = w.Write(zipRaw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.server.Close)
	t.Setenv("ANDROID_HOME", e.sdkRoot)
	t.Setenv("ANDROID_SDK_ROOT", "")
	t.Setenv("ANDROID_AVD_HOME", "")
	t.Setenv("ANDROID_USER_HOME", "")
	t.Setenv("WS_ANDROID_AVDS", "# phones\npixel|pixel_9|system-images;android-36;google_apis\ntablet|pixel_tablet|system-images;android-35;google_apis\nold|pixel_6|system-images;android-34;google_apis\n")
	e.ctx = &app.Context{
		RepoRoot: repo,
		HomeDir:  home,
		Host:     app.HostInfo{OS: "linux", Distro: "arch", Arch: "x86_64"},
	}
	e.cmd = &cobra.Command{}
	e.cmd.SetOut(e.out)
	e.cmd.SetErr(e.out)
	e.deps = androidDeps{
		httpClient: e.server.Client(),
		repoURL:    e.server.URL,
		javaOK:     func(context.Context, []string) bool { return true },
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		isTTY:      func(*cobra.Command, *app.Context) bool { return false },
		confirm: func(*cobra.Command, *app.Context, string) (bool, error) {
			t.Fatalf("unexpected license prompt")
			return false, nil
		},
		run: func(_ context.Context, name string, args, env []string, stdin io.Reader, stdout, _ io.Writer) error {
			call := filepath.Base(name) + " " + strings.Join(args, " ")
			e.calls = append(e.calls, call)
			if !strings.Contains(strings.Join(env, "\n"), "ANDROID_HOME="+e.sdkRoot) {
				t.Fatalf("expected ANDROID_HOME in env for %s", call)
			}
			switch {
			case strings.HasSuffix(call, "--licenses"):
				if stdin == nil {
					t.Fatalf("license acceptance needs stdin answers")
				}
				b := make([]byte, 4)
				if _, err := io.ReadFull(stdin, b); err != nil || string(b) != "y\ny\n" {
					t.Fatalf("expected yes answers on license stdin, got %q err=%v", string(b), err)
				}
			case strings.HasPrefix(call, "avdmanager list avd -c"):
				fmt.Fprintln(stdout, "old")
			case strings.HasPrefix(call, "avdmanager create avd"):
				nameIdx := slices.Index(args, "-n")
				dir := filepath.Join(e.avdHome, args[nameIdx+1]+".avd")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "config.ini"), []byte("hw.ramSize=1536\nhw.keyboard=no\n"), 0o644)
			}
			return nil
		},
	}
	return e
}

func TestAndroidDownloadsCmdlineToolsInstallsAndCreatesAVDs(t *testing.T) {
	e := newAndroidTestEnv(t, "")
	if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, true); err != nil {
		t.Fatalf("runAndroid: %v\n%s", err, e.out.String())
	}
	latestBin := filepath.Join(e.sdkRoot, "cmdline-tools", "latest", "bin")
	fi, err := os.Stat(filepath.Join(latestBin, "sdkmanager"))
	if err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected executable sdkmanager under cmdline-tools/latest, err=%v", err)
	}
	sdk := "--sdk_root=" + e.sdkRoot
	want := []string{
		"sdkmanager " + sdk + " --licenses",
		"sdkmanager " + sdk + " --install platform-tools system-images;android-36;google_apis;x86_64",
		"avdmanager list avd -c",
		"avdmanager create avd -n pixel -d pixel_9 -k system-images;android-36;google_apis;x86_64",
		"sdkmanager " + sdk + " --install system-images;android-35;google_apis;x86_64",
		"avdmanager create avd -n tablet -d pixel_tablet -k system-images;android-35;google_apis;x86_64",
	}
	if strings.Join(e.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("command sequence mismatch:\ngot:\n%s\nwant:\n%s", strings.Join(e.calls, "\n"), strings.Join(want, "\n"))
	}
	cfg, err := os.ReadFile(filepath.Join(e.avdHome, "pixel.avd", "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"hw.ramSize=4096", "disk.dataPartition.size=8G", "hw.keyboard=yes", "hw.gpu.enabled=yes", "hw.gpu.mode=host"} {
		if !strings.Contains(string(cfg), line+"\n") {
			t.Fatalf("expected %q in AVD config:\n%s", line, cfg)
		}
	}
	if strings.Contains(string(cfg), "hw.ramSize=1536") || strings.Contains(string(cfg), "hw.keyboard=no") {
		t.Fatalf("expected existing AVD keys replaced:\n%s", cfg)
	}

	e.calls = nil
	if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, true); err != nil {
		t.Fatalf("second runAndroid: %v", err)
	}
	if len(e.calls) == 0 || e.calls[0] != "sdkmanager "+sdk+" --licenses" {
		t.Fatalf("expected second run to reuse the installed sdkmanager, got %v", e.calls)
	}
}

type androidStagingProbe struct {
	t       *testing.T
	sdkRoot string
	base    http.RoundTripper
	seen    []string
}

func (p *androidStagingProbe) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, ".zip") {
		matches, err := filepath.Glob(filepath.Join(p.sdkRoot, androidStagingPrefix+"*"))
		if err != nil {
			p.t.Fatal(err)
		}
		p.seen = append(p.seen, matches...)
	}
	return p.base.RoundTrip(r)
}

func TestAndroidStagesCmdlineToolsInsideSDKRoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		checksum string
		wantErr  bool
	}{
		{name: "success"},
		{name: "checksum_mismatch", checksum: "deadbeef", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAndroidTestEnv(t, tc.checksum)
			probe := &androidStagingProbe{t: t, sdkRoot: e.sdkRoot, base: e.deps.httpClient.Transport}
			e.deps.httpClient = &http.Client{Transport: probe}
			err := runAndroid(e.cmd, e.ctx, e.deps, "", false, true)
			if (err != nil) != tc.wantErr {
				t.Fatalf("runAndroid err=%v, wantErr=%v", err, tc.wantErr)
			}
			if len(probe.seen) != 1 || filepath.Dir(probe.seen[0]) != e.sdkRoot {
				t.Fatalf("expected one staging dir directly under %s during download, got %v", e.sdkRoot, probe.seen)
			}
			if _, serr := os.Lstat(probe.seen[0]); !os.IsNotExist(serr) {
				t.Fatalf("expected staging dir %s removed, err=%v", probe.seen[0], serr)
			}
			left, gerr := filepath.Glob(filepath.Join(e.sdkRoot, androidStagingPrefix+"*"))
			if gerr != nil || len(left) != 0 {
				t.Fatalf("expected no staging dirs left in SDK root, got %v (err=%v)", left, gerr)
			}
		})
	}
}

func TestAndroidRejectsChecksumMismatch(t *testing.T) {
	e := newAndroidTestEnv(t, "deadbeef")
	err := runAndroid(e.cmd, e.ctx, e.deps, "", false, true)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if _, serr := os.Lstat(filepath.Join(e.sdkRoot, "cmdline-tools", "latest")); !os.IsNotExist(serr) {
		t.Fatalf("expected no cmdline-tools/latest after a bad checksum, err=%v", serr)
	}
	if len(e.calls) != 0 {
		t.Fatalf("expected no sdkmanager calls after a bad checksum, got %v", e.calls)
	}
}

func TestAndroidLicenseConsentGate(t *testing.T) {
	t.Run("non_interactive_without_consent_skips_nonzero", func(t *testing.T) {
		e := newAndroidTestEnv(t, "")
		err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false)
		if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "--yes or --allow all") {
			t.Fatalf("expected exit 1 asking for --yes or --allow all, got %v (code %d)", err, ExitCode(err))
		}
		if len(e.calls) != 0 {
			t.Fatalf("expected nothing run without consent, got %v", e.calls)
		}
		if _, serr := os.Lstat(filepath.Join(e.sdkRoot, "cmdline-tools")); !os.IsNotExist(serr) {
			t.Fatalf("expected no download without consent, err=%v", serr)
		}
	})

	t.Run("per_scope_grants_are_not_consent", func(t *testing.T) {
		e := newAndroidTestEnv(t, "")
		e.ctx.Allow = app.AllowSet{Replace: true, Sudo: true, Remove: true, Script: true}
		err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false)
		if err == nil || ExitCode(err) != 1 || len(e.calls) != 0 {
			t.Fatalf("expected every scope without --yes to skip with exit 1, got err=%v calls=%v", err, e.calls)
		}
	})

	t.Run("allow_all_counts_as_consent", func(t *testing.T) {
		e := newAndroidTestEnv(t, "")
		all, err := app.ParseAllowStrict("all")
		if err != nil {
			t.Fatal(err)
		}
		e.ctx.Allow = all
		if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false); err != nil {
			t.Fatalf("expected --allow all to accept licenses: %v", err)
		}
		if len(e.calls) == 0 || !strings.HasSuffix(e.calls[0], "--licenses") {
			t.Fatalf("expected license acceptance first, got %v", e.calls)
		}
	})

	t.Run("tty_prompt_decline_and_accept", func(t *testing.T) {
		e := newAndroidTestEnv(t, "")
		answer := false
		prompts := 0
		e.deps.isTTY = func(*cobra.Command, *app.Context) bool { return true }
		e.deps.confirm = func(_ *cobra.Command, _ *app.Context, title string) (bool, error) {
			prompts++
			if !strings.Contains(title, "Android SDK licenses") {
				t.Fatalf("unexpected prompt %q", title)
			}
			return answer, nil
		}
		err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false)
		if err == nil || ExitCode(err) != 1 || len(e.calls) != 0 {
			t.Fatalf("expected declined prompt to skip with exit 1, got err=%v calls=%v", err, e.calls)
		}
		answer = true
		if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false); err != nil {
			t.Fatalf("accepted prompt: %v", err)
		}
		if prompts != 2 || len(e.calls) == 0 {
			t.Fatalf("expected two prompts and commands after accepting, prompts=%d calls=%v", prompts, e.calls)
		}
	})
}

func TestAndroidDryRunPlansDownloadAndAVDs(t *testing.T) {
	e := newAndroidTestEnv(t, "")
	e.ctx.DryRun = true
	e.ctx.JSON = true
	if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	out := e.out.String()
	for _, want := range []string{`"installCmdlineTools": true`, `"name": "pixel"`, `"system-images;android-35;google_apis;x86_64"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s in dry-run output:\n%s", want, out)
		}
	}
	if len(e.calls) != 0 {
		t.Fatalf("dry-run ran commands: %v", e.calls)
	}

	t.Setenv("WS_ANDROID_AVDS", "broken|only-two")
	if err := runAndroid(e.cmd, e.ctx, e.deps, "", false, false); err == nil || !strings.Contains(err.Error(), "malformed WS_ANDROID_AVDS") {
		t.Fatalf("expected malformed WS_ANDROID_AVDS error, got %v", err)
	}
}
