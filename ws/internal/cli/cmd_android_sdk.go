package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

const (
	androidRepoURL       = "https://dl.google.com/android/repository"
	androidStagingPrefix = ".cmdline-tools-"
)

type androidDeps struct {
	httpClient *http.Client
	repoURL    string
	run        func(ctx context.Context, name string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) error
	javaOK     func(ctx context.Context, env []string) bool
	isTTY      func(cmd *cobra.Command, ctx *app.Context) bool
	confirm    func(cmd *cobra.Command, ctx *app.Context, title string) (bool, error)
	lookPath   func(file string) (string, error)
}

var defaultAndroidDeps = androidDeps{
	httpClient: &http.Client{Timeout: 10 * time.Minute},
	repoURL:    androidRepoURL,
	run:        androidExec,
	javaOK: func(ctx context.Context, env []string) bool {
		return androidExec(ctx, "java", []string{"-version"}, env, nil, io.Discard, io.Discard) == nil
	},
	isTTY:    isInteractiveTTY,
	confirm:  defaultAndroidConfirm,
	lookPath: exec.LookPath,
}

func androidExec(ctx context.Context, name string, args, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = env
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr
	return c.Run()
}

func defaultAndroidConfirm(cmd *cobra.Command, ctx *app.Context, title string) (bool, error) {
	var ok bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Affirmative("Accept").
			Negative("Skip").
			Value(&ok),
	)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
	if err := form.Run(); err != nil {
		return false, err
	}
	return ok, nil
}

type androidAVD struct {
	Name   string `json:"name"`
	Device string `json:"device"`
	Image  string `json:"image"`
}

func newAndroidCmd(opts *GlobalOpts) *cobra.Command {
	var manifestPath string
	var listOnly bool
	var yesFlag bool

	cmd := &cobra.Command{
		Use:   "android",
		Short: "Install Android SDK components and create AVDs",
		Long:  "Install the Android SDK packages from pkg/android.txt (system images only for the host ABI) and create the AVDs listed in WS_ANDROID_AVDS (name|device|image per line).\n\nDownloads and checksum-verifies the Android cmdline-tools when sdkmanager is missing. Accepting the Android SDK licenses needs consent: a prompt on a TTY, or --yes / --allow all.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			return runAndroid(cmd, ctx, defaultAndroidDeps, manifestPath, listOnly, yesFlag)
		},
	}

	cmd.Flags().StringVarP(&manifestPath, "file", "f", "", "Android package manifest path (default: pkg/android.txt)")
	cmd.Flags().BoolVar(&listOnly, "list", false, "List host-ABI filtered Android SDK packages without installing")
	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Accept the Android SDK licenses without prompting")
	return cmd
}

func runAndroid(cmd *cobra.Command, ctx *app.Context, deps androidDeps, manifestPath string, listOnly, yesFlag bool) error {
	file := strings.TrimSpace(manifestPath)
	if file == "" {
		if ctx.RepoRoot == "" {
			return fmt.Errorf("workstation repository root not found (pass --repo or --file)")
		}
		file = filepath.Join(ctx.RepoRoot, "pkg", "android.txt")
	} else {
		file = ctx.ExpandHome(file)
	}

	rawPkgs, err := config.ParsePackageList(file)
	if err != nil {
		return fmt.Errorf("read android manifest %s: %w", file, err)
	}
	abi := androidHostABI(ctx.Host.Arch)
	pkgs, skipped := filterAndroidPackages(rawPkgs, abi)
	sdkRoot := resolveAndroidSDKRoot(ctx)
	sdkMgr, _ := findAndroidSDKManager(sdkRoot, deps.lookPath)
	avds, err := parseAndroidAVDs(ctx.LookupEnv("WS_ANDROID_AVDS"), abi)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()

	if listOnly || ctx.DryRun {
		payload := map[string]any{
			"dryRun":              ctx.DryRun,
			"listOnly":            listOnly,
			"abi":                 abi,
			"sdkRoot":             sdkRoot,
			"sdkManager":          sdkMgr,
			"installCmdlineTools": sdkMgr == "",
			"packages":            pkgs,
			"skipped":             skipped,
			"avds":                avds,
		}
		if ctx.JSON {
			return writeJSON(w, payload)
		}
		prefix := ""
		if ctx.DryRun {
			prefix = "[dry-run] "
		}
		mgrDisp := sdkMgr
		if mgrDisp == "" {
			mgrDisp = "(missing: cmdline-tools will be downloaded)"
		}
		fmt.Fprintf(w, "%sAndroid SDK root: %s (abi=%s, sdkmanager=%s)\n", prefix, ctx.CollapseHome(sdkRoot), abi, ctx.CollapseHome(mgrDisp))
		rows := make([][]string, 0, len(pkgs)+len(skipped)+len(avds))
		for _, p := range pkgs {
			rows = append(rows, []string{p, "selected"})
		}
		for _, s := range skipped {
			rows = append(rows, []string{s, "skipped (other ABI)"})
		}
		for _, a := range avds {
			rows = append(rows, []string{"avd " + a.Name, fmt.Sprintf("%s, %s", a.Device, a.Image)})
		}
		if len(rows) == 0 {
			fmt.Fprintln(w, "No Android SDK packages or AVDs matched.")
			return nil
		}
		return renderCLITable(cmd, ctx, []ui.TableColumn{
			{Title: "PACKAGE", MinWidth: 36},
			{Title: "STATUS", MinWidth: 16},
		}, rows)
	}

	if len(pkgs) == 0 && len(avds) == 0 {
		if ctx.JSON {
			return writeJSON(w, map[string]any{"sdkRoot": sdkRoot, "abi": abi, "packages": pkgs, "installed": 0})
		}
		fmt.Fprintln(w, "No Android SDK packages to install.")
		return nil
	}

	consent := yesFlag || ctx.Allow.Yes
	if !consent {
		if deps.isTTY == nil || !deps.isTTY(cmd, ctx) {
			return &ExitCodeError{Code: 1, Err: fmt.Errorf("android: skipped, accepting the Android SDK licenses needs consent (rerun with --yes or --allow all)")}
		}
		title := fmt.Sprintf("Install %d Android SDK package(s) into %s and accept the Android SDK licenses?", len(pkgs), ctx.CollapseHome(sdkRoot))
		ok, cerr := deps.confirm(cmd, ctx, title)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return &ExitCodeError{Code: 1, Err: fmt.Errorf("android: skipped, Android SDK licenses not accepted")}
		}
	}

	runCtx := cmdContext(cmd)
	env := append(bootstrapCmdEnv(ctx), "ANDROID_HOME="+sdkRoot)
	if deps.javaOK != nil && !deps.javaOK(runCtx, env) {
		return fmt.Errorf("android: no working JDK (jdk17-openjdk on arch, zulu@17 on mac)")
	}

	var stdout, stderr io.Writer = cmd.OutOrStdout(), cmd.ErrOrStderr()
	var outBuf bytes.Buffer
	if ctx.JSON {
		stdout, stderr = &outBuf, &outBuf
	}
	logf := func(format string, args ...any) {
		if !ctx.JSON {
			fmt.Fprintf(w, format+"\n", args...)
		}
	}

	if err := os.MkdirAll(sdkRoot, 0o755); err != nil {
		return err
	}
	downloaded := ""
	if sdkMgr == "" {
		latest := filepath.Join(sdkRoot, "cmdline-tools", "latest")
		if _, err := os.Lstat(latest); err == nil {
			return fmt.Errorf("android: %s exists but has no sdkmanager — move it aside", ctx.CollapseHome(latest))
		}
		url, err := installAndroidCmdlineTools(runCtx, deps, ctx.Host.OS, abi, sdkRoot)
		if err != nil {
			return err
		}
		downloaded = url
		logf("android: installed cmdline-tools from %s", url)
		sdkMgr = filepath.Join(latest, "bin", "sdkmanager")
	}

	licErr := deps.run(runCtx, sdkMgr, []string{"--sdk_root=" + sdkRoot, "--licenses"}, env, strings.NewReader(strings.Repeat("y\n", 4096)), io.Discard, io.Discard)
	if licErr != nil && !ctx.JSON {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: sdkmanager --licenses exited with %v\n", licErr)
	}

	if len(pkgs) > 0 {
		args := append([]string{"--sdk_root=" + sdkRoot, "--install"}, pkgs...)
		if err := deps.run(runCtx, sdkMgr, args, env, nil, stdout, stderr); err != nil {
			if ctx.JSON && outBuf.Len() > 0 {
				return wrapCmdExitErr(fmt.Errorf("sdkmanager install failed: %w\n%s", err, strings.TrimSpace(outBuf.String())))
			}
			return wrapCmdExitErr(fmt.Errorf("sdkmanager install failed: %w", err))
		}
	}

	created, existing, err := createAndroidAVDs(runCtx, ctx, deps, sdkMgr, sdkRoot, env, pkgs, avds, stdout, stderr, logf)
	if err != nil {
		return err
	}
	if len(avds) == 0 {
		logf("android: WS_ANDROID_AVDS unset — no AVDs to create")
	}

	if ctx.JSON {
		return writeJSON(w, map[string]any{
			"dryRun":       false,
			"sdkRoot":      sdkRoot,
			"sdkManager":   sdkMgr,
			"cmdlineTools": downloaded,
			"abi":          abi,
			"packages":     pkgs,
			"installed":    len(pkgs),
			"avdsCreated":  created,
			"avdsExisting": existing,
			"output":       strings.TrimRight(outBuf.String(), "\r\n"),
		})
	}
	fmt.Fprintf(w, "Installed %d Android SDK component(s) into %s; created %d AVD(s).\n", len(pkgs), ctx.CollapseHome(sdkRoot), len(created))
	return nil
}

func parseAndroidAVDs(raw, abi string) ([]androidAVD, error) {
	var avds []androidAVD
	for _, row := range app.ParsePipeLines(raw) {
		if len(row) != 3 || row[0] == "" || row[1] == "" || row[2] == "" {
			return nil, fmt.Errorf("malformed WS_ANDROID_AVDS line %q (expected name|device|image)", strings.Join(row, "|"))
		}
		avds = append(avds, androidAVD{Name: row[0], Device: row[1], Image: row[2] + ";" + abi})
	}
	return avds, nil
}

func androidAVDHome(ctx *app.Context) string {
	if v := strings.TrimSpace(ctx.LookupEnv("ANDROID_AVD_HOME")); v != "" {
		return ctx.ExpandHome(v)
	}
	if v := strings.TrimSpace(ctx.LookupEnv("ANDROID_USER_HOME")); v != "" {
		return filepath.Join(ctx.ExpandHome(v), "avd")
	}
	return filepath.Join(ctx.HomeDir, ".android", "avd")
}

func createAndroidAVDs(runCtx context.Context, ctx *app.Context, deps androidDeps, sdkMgr, sdkRoot string, env, pkgs []string, avds []androidAVD, stdout, stderr io.Writer, logf func(string, ...any)) (created, existing []string, err error) {
	if len(avds) == 0 {
		return nil, nil, nil
	}
	avdMgr := filepath.Join(filepath.Dir(sdkMgr), "avdmanager")
	if !isExecutable(avdMgr) {
		if deps.lookPath != nil {
			if p, lerr := deps.lookPath("avdmanager"); lerr == nil {
				avdMgr = p
			}
		}
	}
	var listOut bytes.Buffer
	_ = deps.run(runCtx, avdMgr, []string{"list", "avd", "-c"}, env, nil, &listOut, io.Discard)
	have := map[string]bool{}
	for _, line := range strings.Split(listOut.String(), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			have[name] = true
		}
	}
	avdHome := androidAVDHome(ctx)
	for _, avd := range avds {
		if have[avd.Name] {
			logf("avd: %s exists", avd.Name)
			existing = append(existing, avd.Name)
			continue
		}
		if !slices.Contains(pkgs, avd.Image) {
			if err := deps.run(runCtx, sdkMgr, []string{"--sdk_root=" + sdkRoot, "--install", avd.Image}, env, nil, stdout, stderr); err != nil {
				return created, existing, wrapCmdExitErr(fmt.Errorf("sdkmanager install %s failed: %w", avd.Image, err))
			}
		}
		args := []string{"create", "avd", "-n", avd.Name, "-d", avd.Device, "-k", avd.Image}
		if err := deps.run(runCtx, avdMgr, args, env, strings.NewReader("no\n"), io.Discard, stderr); err != nil {
			return created, existing, wrapCmdExitErr(fmt.Errorf("avdmanager create avd %s failed: %w", avd.Name, err))
		}
		cfg := filepath.Join(avdHome, avd.Name+".avd", "config.ini")
		if err := setAndroidINI(cfg, [][2]string{
			{"hw.ramSize", "4096"},
			{"disk.dataPartition.size", "8G"},
			{"hw.keyboard", "yes"},
			{"hw.gpu.enabled", "yes"},
			{"hw.gpu.mode", "host"},
		}); err != nil {
			return created, existing, fmt.Errorf("avd %s: %w", avd.Name, err)
		}
		logf("avd: created %s (%s, %s)", avd.Name, avd.Device, avd.Image)
		created = append(created, avd.Name)
	}
	return created, existing, nil
}

func setAndroidINI(path string, kvs [][2]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	for _, kv := range kvs {
		found := false
		for i, line := range lines {
			if strings.HasPrefix(line, kv[0]+"=") {
				lines[i] = kv[0] + "=" + kv[1]
				found = true
			}
		}
		if !found {
			lines = append(lines, kv[0]+"="+kv[1])
		}
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

type androidRepoXML struct {
	Packages []struct {
		Path     string `xml:"path,attr"`
		Archives []struct {
			Complete struct {
				Checksum struct {
					Type  string `xml:"type,attr"`
					Value string `xml:",chardata"`
				} `xml:"checksum"`
				URL string `xml:"url"`
			} `xml:"complete"`
		} `xml:"archives>archive"`
	} `xml:"remotePackage"`
}

func androidCmdlineToolsPrefix(goos, abi string) string {
	if goos == "darwin" {
		if abi == "x86_64" {
			return "commandlinetools-mac_x86_64-"
		}
		return "commandlinetools-mac_arm64-"
	}
	return "commandlinetools-linux-"
}

func selectAndroidCmdlineTools(raw []byte, prefix string) (url, sumType, sum string, err error) {
	var repo androidRepoXML
	if err := xml.Unmarshal(raw, &repo); err != nil {
		return "", "", "", fmt.Errorf("android: parse SDK repository: %w", err)
	}
	for _, pkg := range repo.Packages {
		if pkg.Path != "cmdline-tools;latest" {
			continue
		}
		for _, a := range pkg.Archives {
			u := strings.TrimSpace(a.Complete.URL)
			if strings.HasPrefix(u, prefix) {
				return u, strings.ToLower(strings.TrimSpace(a.Complete.Checksum.Type)), strings.ToLower(strings.TrimSpace(a.Complete.Checksum.Value)), nil
			}
		}
	}
	return "", "", "", fmt.Errorf("android: no cmdline-tools archive matching %s in the SDK repository", prefix)
}

func installAndroidCmdlineTools(runCtx context.Context, deps androidDeps, goos, abi, sdkRoot string) (string, error) {
	client := deps.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	base := strings.TrimRight(deps.repoURL, "/")
	xmlRaw, err := androidFetch(runCtx, client, base+"/repository2-3.xml", nil)
	if err != nil {
		return "", err
	}
	url, sumType, sum, err := selectAndroidCmdlineTools(xmlRaw, androidCmdlineToolsPrefix(goos, abi))
	if err != nil {
		return "", err
	}
	if sum == "" {
		return "", fmt.Errorf("android: no checksum for %s in the SDK repository", url)
	}
	var h hash.Hash
	switch sumType {
	case "", "sha1", "sha-1":
		h = sha1.New()
	case "sha256", "sha-256":
		h = sha256.New()
	default:
		return "", fmt.Errorf("android: unsupported checksum type %q for %s", sumType, url)
	}

	if err := os.MkdirAll(sdkRoot, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(sdkRoot, androidStagingPrefix)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	zipPath := filepath.Join(tmp, "tools.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	_, ferr := androidFetch(runCtx, client, base+"/"+url, io.MultiWriter(f, h))
	if cerr := f.Close(); ferr == nil {
		ferr = cerr
	}
	if ferr != nil {
		return "", ferr
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("android: checksum mismatch for %s", url)
	}
	extract := filepath.Join(tmp, "x")
	if err := extractAndroidZip(zipPath, extract); err != nil {
		return "", err
	}
	src := filepath.Join(extract, "cmdline-tools")
	if fi, err := os.Stat(filepath.Join(src, "bin", "sdkmanager")); err != nil || fi.IsDir() {
		return "", fmt.Errorf("android: %s has no cmdline-tools/bin/sdkmanager", url)
	}
	dest := filepath.Join(sdkRoot, "cmdline-tools")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, filepath.Join(dest, "latest")); err != nil {
		return "", err
	}
	return url, nil
}

func androidFetch(runCtx context.Context, client *http.Client, url string, dst io.Writer) ([]byte, error) {
	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("android: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("android: download %s: %s", url, resp.Status)
	}
	if dst != nil {
		if _, err := io.Copy(dst, resp.Body); err != nil {
			return nil, fmt.Errorf("android: download %s: %w", url, err)
		}
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func extractAndroidZip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("android: open archive: %w", err)
	}
	defer zr.Close()
	root := filepath.Clean(dest) + string(filepath.Separator)
	for _, zf := range zr.File {
		target := filepath.Join(dest, zf.Name)
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("android: archive entry %q escapes the extract dir", zf.Name)
		}
		mode := zf.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("android: archive entry %q is a symlink", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		perm := mode.Perm() | 0o600
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, rc)
		rc.Close()
		if err := out.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

func androidHostABI(arch string) string {
	a := strings.ToLower(strings.TrimSpace(arch))
	if a == "" {
		a = runtime.GOARCH
	}
	switch a {
	case "arm64", "aarch64":
		return "arm64-v8a"
	case "amd64", "x86_64":
		return "x86_64"
	default:
		return a
	}
}

func filterAndroidPackages(pkgs []string, hostABI string) (kept, skipped []string) {
	for _, p := range pkgs {
		if strings.HasPrefix(p, "system-images;") {
			parts := strings.Split(p, ";")
			pkgABI := parts[len(parts)-1]
			if pkgABI != hostABI {
				skipped = append(skipped, p)
				continue
			}
		}
		kept = append(kept, p)
	}
	return kept, skipped
}

func resolveAndroidSDKRoot(ctx *app.Context) string {
	for _, key := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := strings.TrimSpace(ctx.LookupEnv(key)); v != "" {
			return ctx.ExpandHome(v)
		}
	}
	if ctx.Host.OS == "darwin" {
		return filepath.Join(ctx.HomeDir, "Library", "Android", "sdk")
	}
	return filepath.Join(ctx.HomeDir, "Android", "Sdk")
}

func findAndroidSDKManager(sdkRoot string, lookPath func(string) (string, error)) (string, error) {
	candidates := []string{
		filepath.Join(sdkRoot, "cmdline-tools", "latest", "bin", "sdkmanager"),
	}
	if matches, err := filepath.Glob(filepath.Join(sdkRoot, "cmdline-tools", "*", "bin", "sdkmanager")); err == nil {
		sort.Strings(matches)
		candidates = append(candidates, matches...)
	}
	candidates = append(candidates, filepath.Join(sdkRoot, "tools", "bin", "sdkmanager"))
	for _, c := range candidates {
		if isExecutable(c) {
			return c, nil
		}
	}
	if lookPath != nil {
		if p, err := lookPath("sdkmanager"); err == nil && p != "" {
			return p, nil
		}
	}
	return "", errors.New("sdkmanager not found")
}
