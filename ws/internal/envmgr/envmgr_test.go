package envmgr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/store"
)

func setupTestEnv(t *testing.T) (*app.Context, *store.DB, *store.Keyring) {
	t.Helper()
	t.Setenv("WS_DB_KEY", "")
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_CODE_ROOT", "")

	home := t.TempDir()
	ctx := &app.Context{
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		LocalEnv:  map[string]string{},
	}

	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	kr, err := store.InitKey(ctx, false)
	if err != nil {
		t.Fatalf("store.InitKey: %v", err)
	}
	return ctx, db, kr
}

func mustWriteFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
}

func TestScanPrecedenceAndSummaries(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	mustWriteFile(t, filepath.Join(ctx.HomeDir, ".zshrc"), strings.Join([]string{
		"# shell defaults",
		"export SERVICE_TOKEN=\"sk-shell-000011112222\"",
		"DYNAMIC_SECRET=$(echo should-skip)",
		"BACKTICK_VAL=`uname`",
		"APP_MODE=shell",
	}, "\n")+"\n", 0o644)

	mustWriteFile(t, filepath.Join(ctx.ConfigDir, "mise", "config.toml"), strings.Join([]string{
		"[env]",
		"SERVICE_TOKEN = \"sk-global-333344445555\"",
		"APP_MODE = \"global\"",
	}, "\n")+"\n", 0o644)

	ctx.LocalEnv["WS_ZONES"] = "work|Test User|user@example.com"
	workRoot := filepath.Join(ctx.HomeDir, "work")
	mustWriteFile(t, filepath.Join(workRoot, "mise.toml"), strings.Join([]string{
		"[env]",
		"WS_ZONE = \"work\"",
		"SERVICE_TOKEN = \"sk-zone-666677778888\"",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, filepath.Join(workRoot, "mise.local.toml"), strings.Join([]string{
		"[env]",
		`ZONE_LOCAL_SECRET = "` + "ghp_" + `zoneLocalToken123456"`,
	}, "\n")+"\n", 0o600)

	svcA := filepath.Join(workRoot, "code", "svc-a")
	mustMkdir(t, filepath.Join(svcA, ".git"))
	mustWriteFile(t, filepath.Join(svcA, "mise.toml"), "[env]\nSERVICE_TOKEN = \"sk-proj-999900001111\"\n", 0o644)
	mustWriteFile(t, filepath.Join(svcA, ".env"), "SERVICE_TOKEN=sk-dotenv-222233334444\nAPP_MODE=dotenv\n", 0o600)
	mustWriteFile(t, filepath.Join(svcA, ".env.local"), "SERVICE_TOKEN=sk-local-555566667777\n", 0o600)

	mustWriteFile(t, filepath.Join(svcA, ".env.example"), "SERVICE_TOKEN=placeholder\n", 0o644)

	flatApp := filepath.Join(ctx.HomeDir, "code", "flat-app")
	mustMkdir(t, filepath.Join(flatApp, ".git"))
	mustWriteFile(t, filepath.Join(flatApp, ".env"), "FLAT_KEY=flat-secret-12345678\n", 0o600)

	res, err := Scan(ctx, db, kr)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.FilesScanned < 8 || res.EntriesCount < 10 || res.SecretsCount < 6 {
		t.Fatalf("unexpected scan counts: %+v", res)
	}
	if res.BlobsStored < 4 {
		t.Fatalf("expected encrypted envfile/mise.local blobs stored, got %+v", res)
	}

	dynEntries, err := FindEntriesForZone(db, "DYNAMIC_SECRET", "")
	if err != nil || len(dynEntries) != 0 {
		t.Fatalf("expected DYNAMIC_SECRET skipped, got %+v err=%v", dynEntries, err)
	}
	btEntries, err := FindEntriesForZone(db, "BACKTICK_VAL", "")
	if err != nil || len(btEntries) != 0 {
		t.Fatalf("expected BACKTICK_VAL skipped, got %+v err=%v", btEntries, err)
	}

	flatEntries, err := FindEntriesForZone(db, "FLAT_KEY", "")
	if err != nil || len(flatEntries) != 1 {
		t.Fatalf("expected FLAT_KEY from ~/code fallback, got %+v err=%v", flatEntries, err)
	}

	prov, err := GetProvenance(ctx, db, "SERVICE_TOKEN", svcA)
	if err != nil {
		t.Fatalf("GetProvenance: %v", err)
	}
	if len(prov.Layers) != 6 {
		t.Fatalf("expected 6 precedence layers for SERVICE_TOKEN in svc-a, got %d: %+v", len(prov.Layers), prov.Layers)
	}
	wantKinds := []string{
		SourceShell,
		SourceGlobalMise,
		SourceZoneMise,
		SourceProjectMise,
		SourceDotenv,
		SourceDotenvLocal,
	}
	for i, wk := range wantKinds {
		if prov.Layers[i].SourceKind != wk {
			t.Fatalf("layer %d kind=%q want %q", i, prov.Layers[i].SourceKind, wk)
		}
		wantStatus := StatusShadowed
		if i == len(wantKinds)-1 {
			wantStatus = StatusActive
		}
		if prov.Layers[i].Status != wantStatus {
			t.Fatalf("layer %d status=%q want %q", i, prov.Layers[i].Status, wantStatus)
		}
	}
	if prov.ActiveValueMasked != store.MaskValue("sk-local-555566667777", true) {
		t.Fatalf("unexpected active masked value: %q", prov.ActiveValueMasked)
	}

	secretSummaries, err := ListSummariesForZone(db, "", "", true)
	if err != nil {
		t.Fatalf("ListSummaries secretsOnly: %v", err)
	}
	foundGlobalToken := false
	foundWorkToken := false
	for _, s := range secretSummaries {
		if s.VarKey == "APP_MODE" {
			t.Fatalf("non-secret APP_MODE leaked into secretsOnly list")
		}
		if s.VarKey == "SERVICE_TOKEN" {
			switch s.Zone {
			case "global":
				foundGlobalToken = true
				if s.SourceCount != 2 || s.ShadowedCount != 1 {
					t.Fatalf("unexpected global SERVICE_TOKEN summary: %+v", s)
				}
			case "work":
				foundWorkToken = true
				if s.SourceCount != 4 || s.ShadowedCount != 3 {
					t.Fatalf("unexpected work SERVICE_TOKEN summary: %+v", s)
				}
			default:
				t.Fatalf("unexpected zone for SERVICE_TOKEN: %+v", s)
			}
		}
	}
	if !foundGlobalToken || !foundWorkToken {
		t.Fatalf("expected both global and work SERVICE_TOKEN rows, got global=%v work=%v", foundGlobalToken, foundWorkToken)
	}

	lockedRes, err := Scan(ctx, db, &store.Keyring{Source: store.KeySourceNone})
	if err != nil {
		t.Fatalf("Scan with locked keyring failed: %v", err)
	}
	if lockedRes.EntriesCount != res.EntriesCount || lockedRes.BlobsStored != 0 {
		t.Fatalf("unexpected locked scan result: %+v", lockedRes)
	}
}

func TestBlastRadiusByHMACFingerprintAndRevealFallback(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)
	ctx.LocalEnv["WS_ZONES"] = "work|Test User|user@example.com"
	workRoot := filepath.Join(ctx.HomeDir, "work")
	mustWriteFile(t, filepath.Join(workRoot, "mise.toml"), "[env]\nWS_ZONE = \"work\"\n", 0o600)

	sharedSecret := "sk-shared-blast-radius-9876543210"
	repoA := filepath.Join(workRoot, "code", "svc-a")
	repoB := filepath.Join(workRoot, "code", "svc-b")
	mustMkdir(t, filepath.Join(repoA, ".git"))
	mustMkdir(t, filepath.Join(repoB, ".git"))

	envA := filepath.Join(repoA, ".env")
	envB := filepath.Join(repoB, ".env")
	mustWriteFile(t, filepath.Join(repoA, "mise.toml"), "[env]\nPRIMARY_API_KEY = \""+sharedSecret+"\"\n", 0o644)
	mustWriteFile(t, envA, "PRIMARY_API_KEY="+sharedSecret+"\n", 0o600)
	mustWriteFile(t, envB, "LEGACY_LLM_TOKEN="+sharedSecret+"\n", 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	fp := store.Fingerprint(kr.Key, sharedSecret)
	matches, err := GetBlastRadius(db, fp)
	if err != nil {
		t.Fatalf("GetBlastRadius: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("expected 3 cross-repo fingerprint matches, got %d: %+v", len(matches), matches)
	}

	prov, err := GetProvenance(ctx, db, "PRIMARY_API_KEY", repoA)
	if err != nil {
		t.Fatalf("GetProvenance: %v", err)
	}
	if len(prov.Layers) != 2 || prov.Layers[0].Status != StatusMatch || prov.Layers[1].Status != StatusActive {
		t.Fatalf("expected MATCH then ACTIVE layers, got %+v", prov.Layers)
	}
	if len(prov.ImpactMatches) != 3 {
		t.Fatalf("expected 3 impact matches (including cross-key fingerprint match), got %+v", prov.ImpactMatches)
	}

	rawVal, err := RevealValue(ctx, db, kr, ctx.CollapseHome(envA), "PRIMARY_API_KEY")
	if err != nil || rawVal != sharedSecret {
		t.Fatalf("RevealValue live: got %q err=%v", rawVal, err)
	}

	if err := os.Remove(envA); err != nil {
		t.Fatalf("Remove envA: %v", err)
	}
	fallbackVal, err := RevealValue(ctx, db, kr, ctx.CollapseHome(envA), "PRIMARY_API_KEY")
	if err != nil || fallbackVal != sharedSecret {
		t.Fatalf("RevealValue fallback: got %q err=%v", fallbackVal, err)
	}
}

func TestAtomicRotateCommentPreservingDryRunAndEncryptedUndo(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)
	ctx.LocalEnv["WS_ZONES"] = "work|Test User|user@example.com"
	workRoot := filepath.Join(ctx.HomeDir, "work")
	mustWriteFile(t, filepath.Join(workRoot, "mise.toml"), "[env]\nWS_ZONE = \"work\"\n", 0o600)

	oldSecret := "sk-old-token-111122223333"
	newSecret := "sk-new token#with-special-99998888"

	repoA := filepath.Join(workRoot, "code", "svc-a")
	repoB := filepath.Join(workRoot, "code", "svc-b")
	mustMkdir(t, filepath.Join(repoA, ".git"))
	mustMkdir(t, filepath.Join(repoB, ".git"))

	envAPath := filepath.Join(repoA, ".env")
	origEnvA := strings.Join([]string{
		"# Top header comment",
		"export API_TOKEN='" + oldSecret + "' # inline note",
		"UNRELATED_KEY=keep-me",
		"",
		"# Footer comment",
	}, "\n") + "\n"
	mustWriteFile(t, envAPath, origEnvA, 0o644)

	miseLocalBPath := filepath.Join(repoB, "mise.local.toml")
	origMiseB := strings.Join([]string{
		"# Local overrides for svc-b",
		"[tools]",
		"go = \"1.27\"",
		"",
		"[env]",
		"# keep env comment",
		"ALT_TOKEN = \"" + oldSecret + "\" # shared secret",
		"OTHER_VAR = \"untouched\"",
	}, "\n") + "\n"
	mustWriteFile(t, miseLocalBPath, origMiseB, 0o644)
	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	oldFP := store.Fingerprint(kr.Key, oldSecret)
	plan, err := PlanRotate(ctx, db, kr, RotateRequest{MatchFingerprint: oldFP}, newSecret)
	if err != nil {
		t.Fatalf("PlanRotate: %v", err)
	}
	if len(plan.Targets) != 2 {
		t.Fatalf("expected 2 rotation targets, got %d: %+v", len(plan.Targets), plan.Targets)
	}

	if strings.Contains(plan.NewMasked, newSecret) {
		t.Fatalf("plan leaked raw newValue in NewMasked")
	}

	dryCtx := *ctx
	dryCtx.DryRun = true
	dryRes, err := ExecRotate(&dryCtx, db, kr, plan, newSecret)
	if err != nil {
		t.Fatalf("ExecRotate dry-run: %v", err)
	}
	if !dryRes.DryRun || len(dryRes.UpdatedFiles) != 2 {
		t.Fatalf("unexpected dry-run result: %+v", dryRes)
	}
	if got, _ := os.ReadFile(envAPath); string(got) != origEnvA {
		t.Fatalf("dry-run mutated %s", envAPath)
	}
	if got, _ := os.ReadFile(miseLocalBPath); string(got) != origMiseB {
		t.Fatalf("dry-run mutated %s", miseLocalBPath)
	}

	execRes, err := ExecRotate(ctx, db, kr, plan, newSecret)
	if err != nil {
		t.Fatalf("ExecRotate: %v", err)
	}
	if execRes.DryRun || len(execRes.UpdatedFiles) != 2 {
		t.Fatalf("unexpected ExecRotate result: %+v", execRes)
	}

	gotEnvA, err := os.ReadFile(envAPath)
	if err != nil {
		t.Fatalf("ReadFile envA: %v", err)
	}
	wantEnvA := strings.Join([]string{
		"# Top header comment",
		"export API_TOKEN='" + newSecret + "' # inline note",
		"UNRELATED_KEY=keep-me",
		"",
		"# Footer comment",
	}, "\n") + "\n"
	if string(gotEnvA) != wantEnvA {
		t.Fatalf("envA mismatch:\ngot:\n%s\nwant:\n%s", gotEnvA, wantEnvA)
	}
	if fi, _ := os.Stat(envAPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms on rotated .env, got %04o", fi.Mode().Perm())
	}

	gotMiseB, err := os.ReadFile(miseLocalBPath)
	if err != nil {
		t.Fatalf("ReadFile miseLocalB: %v", err)
	}
	if !strings.Contains(string(gotMiseB), "# keep env comment") ||
		!strings.Contains(string(gotMiseB), "OTHER_VAR = \"untouched\"") ||
		!strings.Contains(string(gotMiseB), "ALT_TOKEN = \""+newSecret+"\" # shared secret") {
		t.Fatalf("mise.local.toml did not preserve comments/keys:\n%s", gotMiseB)
	}
	if fi, _ := os.Stat(miseLocalBPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms on rotated mise.local.toml, got %04o", fi.Mode().Perm())
	}

	metaVal, ok, err := db.GetMeta("last_rotate_tx")
	if err != nil || !ok {
		t.Fatalf("GetMeta last_rotate_tx: ok=%v err=%v", ok, err)
	}
	if strings.Contains(metaVal, oldSecret) || strings.Contains(metaVal, newSecret) {
		t.Fatalf("last_rotate_tx meta leaked plaintext secret: %s", metaVal)
	}

	newMatches, err := GetBlastRadius(db, store.Fingerprint(kr.Key, newSecret))
	if err != nil || len(newMatches) != 2 {
		t.Fatalf("expected 2 entries with new fingerprint after rotate, got %+v err=%v", newMatches, err)
	}

	undoRes, err := UndoRotate(ctx, db, kr, execRes.TxID)
	if err != nil {
		t.Fatalf("UndoRotate: %v", err)
	}
	if len(undoRes.UpdatedFiles) != 2 {
		t.Fatalf("unexpected UndoRotate result: %+v", undoRes)
	}
	if got, _ := os.ReadFile(envAPath); string(got) != origEnvA {
		t.Fatalf("envA not restored:\ngot:\n%s\nwant:\n%s", got, origEnvA)
	}
	if got, _ := os.ReadFile(miseLocalBPath); string(got) != origMiseB {
		t.Fatalf("miseLocalB not restored:\ngot:\n%s\nwant:\n%s", got, origMiseB)
	}
	restoredMatches, err := GetBlastRadius(db, oldFP)
	if err != nil || len(restoredMatches) != 2 {
		t.Fatalf("expected 2 entries with old fingerprint after undo, got %+v err=%v", restoredMatches, err)
	}
}

func TestSymlinkAndEscapeRejectionAndSetVar(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	repoDir := filepath.Join(ctx.HomeDir, "code", "secure-app")
	mustMkdir(t, filepath.Join(repoDir, ".git"))

	realFile := filepath.Join(repoDir, ".env.real")
	mustWriteFile(t, realFile, "API_KEY=sk-real-111122223333\n", 0o600)
	symlinkEnv := filepath.Join(repoDir, ".env")
	if err := os.Symlink(realFile, symlinkEnv); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := PlanSetVar(ctx, db, kr, symlinkEnv, "API_KEY", "sk-new-999988887777"); err == nil {
		t.Fatalf("expected PlanSetVar to reject symlink target")
	}
	if _, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "API_KEY", Paths: []string{symlinkEnv}}, "sk-new-999988887777"); err == nil {
		t.Fatalf("expected PlanRotate to reject symlink target")
	}

	outsidePath := filepath.Join(t.TempDir(), ".env")
	if _, err := PlanSetVar(ctx, db, kr, outsidePath, "API_KEY", "sk-new-999988887777"); err == nil {
		t.Fatalf("expected PlanSetVar to reject path outside HomeDir")
	}

	if _, err := PlanSetVar(ctx, db, kr, filepath.Join(ctx.HomeDir, ".zshrc"), "API_KEY", "val"); err == nil {
		t.Fatalf("expected PlanSetVar to refuse .zshrc")
	}
	if _, err := PlanSetVar(ctx, db, kr, filepath.Join(repoDir, "mise.toml"), "API_KEY", "val"); err == nil {
		t.Fatalf("expected PlanSetVar to refuse mise.toml")
	}

	newLocal := filepath.Join(repoDir, ".env.local")
	setRes, err := execSetVarForTest(ctx, db, kr, newLocal, "NEW_API_TOKEN", "val with spaces # comment")
	if err != nil {
		t.Fatalf("ExecSetVar: %v", err)
	}
	raw, err := os.ReadFile(newLocal)
	if err != nil {
		t.Fatalf("ReadFile newLocal: %v", err)
	}
	if !strings.Contains(string(raw), `NEW_API_TOKEN="val with spaces # comment"`) {
		t.Fatalf("unexpected quoted dotenv line: %q", raw)
	}
	if fi, _ := os.Stat(newLocal); fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms on created .env.local, got %04o", fi.Mode().Perm())
	}

	if _, err := UndoRotate(ctx, db, kr, setRes.TxID); err != nil {
		t.Fatalf("UndoRotate created file: %v", err)
	}
	if _, err := os.Stat(newLocal); !os.IsNotExist(err) {
		t.Fatalf("expected UndoRotate to remove newly created %s", newLocal)
	}
}

func TestSelectiveRestoreEnvFiles(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	repoA := filepath.Join(ctx.HomeDir, "code", "alpha")
	repoB := filepath.Join(ctx.HomeDir, "code", "beta")
	mustMkdir(t, filepath.Join(repoA, ".git"))
	mustMkdir(t, filepath.Join(repoB, ".git"))

	envA := filepath.Join(repoA, ".env")
	envB := filepath.Join(repoB, ".env")
	contentA := "ALPHA_TOKEN=sk-alpha-111122223333\n"
	contentB := "BETA_TOKEN=sk-beta-444455556666\n"
	mustWriteFile(t, envA, contentA, 0o600)
	mustWriteFile(t, envB, contentB, 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if err := os.Remove(envA); err != nil {
		t.Fatalf("Remove envA: %v", err)
	}
	if err := os.Remove(envB); err != nil {
		t.Fatalf("Remove envB: %v", err)
	}

	written, err := RestoreEnvFiles(ctx, db, kr, []string{"code/alpha/.env"}, false)
	if err != nil {
		t.Fatalf("RestoreEnvFiles selective: %v", err)
	}
	if len(written) != 1 || written[0] != envA {
		t.Fatalf("unexpected written files: %+v", written)
	}
	if got, _ := os.ReadFile(envA); string(got) != contentA {
		t.Fatalf("restored envA mismatch: %q", got)
	}
	if fi, _ := os.Stat(envA); fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 on restored envA, got %04o", fi.Mode().Perm())
	}
	if _, err := os.Stat(envB); !os.IsNotExist(err) {
		t.Fatalf("beta/.env should not have been restored selectively")
	}

	if _, err := RestoreEnvFiles(ctx, db, kr, []string{"code/alpha/.env"}, false); err == nil {
		t.Fatalf("expected RestoreEnvFiles to refuse overwrite without allowReplace")
	}

	mustWriteFile(t, envA, "ALPHA_TOKEN=overwritten\n", 0o600)
	written2, err := RestoreEnvFiles(ctx, db, kr, []string{"code/alpha/.env"}, true)
	if err != nil || len(written2) != 1 {
		t.Fatalf("RestoreEnvFiles allowReplace: written=%+v err=%v", written2, err)
	}
	if got, _ := os.ReadFile(envA); string(got) != contentA {
		t.Fatalf("envA not restored with allowReplace: %q", got)
	}
}

func TestEnvSecurityHardening(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	repoA := filepath.Join(ctx.HomeDir, "code", "app-a")
	repoB := filepath.Join(ctx.HomeDir, "code", "app-b")
	mustMkdir(t, filepath.Join(repoA, ".git"))
	mustMkdir(t, filepath.Join(repoB, ".git"))

	patVal := "github_" + "pat_example0000111122223333"
	envAPath := filepath.Join(repoA, ".env")
	envBPath := filepath.Join(repoB, ".env")
	mustWriteFile(t, envAPath, strings.Join([]string{
		"DATABASE_URL=postgres://app:hunter2secret@db.internal/prod",
		"GITHUB_PAT=" + patVal,
		"CUSTOM_INTERNAL_FLAG=secret_by_default_val",
		"PORT=8080",
		"SHARED_TOKEN=val-one-1111222233334444",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, envBPath, "SHARED_TOKEN=val-two-5555666677778888\n", 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, key := range []string{"DATABASE_URL", "GITHUB_PAT", "CUSTOM_INTERNAL_FLAG"} {
		entries, err := FindEntriesForZone(db, key, "")
		if err != nil || len(entries) != 1 {
			t.Fatalf("FindEntriesForZone(%s): entries=%+v err=%v", key, entries, err)
		}
		if !entries[0].IsSecret {
			t.Fatalf("expected %s in .env to be classified secret", key)
		}
		if strings.Contains(entries[0].MaskedValue, "hunter2secret") ||
			strings.Contains(entries[0].MaskedValue, patVal) ||
			strings.Contains(entries[0].MaskedValue, "secret_by_default_val") {
			t.Fatalf("%s leaked raw value into MaskedValue: %q", key, entries[0].MaskedValue)
		}
	}
	portEntries, err := FindEntriesForZone(db, "PORT", "")
	if err != nil || len(portEntries) != 1 || portEntries[0].IsSecret || portEntries[0].MaskedValue != "8080" {
		t.Fatalf("expected allowlisted PORT unmasked non-secret, got %+v err=%v", portEntries, err)
	}

	if _, err := store.BackupIfChanged(ctx, db, kr, store.BackupOptions{}); err != nil {
		t.Fatalf("Backup 1: %v", err)
	}
	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan 2: %v", err)
	}
	b2, err := store.BackupIfChanged(ctx, db, kr, store.BackupOptions{})
	if err != nil || !b2.Skipped {
		t.Fatalf("expected second unchanged backup after rescan to skip, got %+v err=%v", b2, err)
	}
	mustWriteFile(t, envBPath, "SHARED_TOKEN=val-two-changed-9999\n", 0o600)
	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan 3: %v", err)
	}
	b3, err := store.BackupIfChanged(ctx, db, kr, store.BackupOptions{})
	if err != nil || b3.Skipped || !b3.Changed {
		t.Fatalf("expected backup after .env change to run, got %+v err=%v", b3, err)
	}

	if _, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "SHARED_TOKEN", OnlyExisting: true}, "new-secret"); err == nil || !strings.Contains(err.Error(), "distinct values") {
		t.Fatalf("expected PlanRotate ambiguity error across distinct values, got %v", err)
	}

	planA, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "SHARED_TOKEN", Paths: []string{envAPath}, OnlyExisting: true}, "rotated$with`specials`1234")
	if err != nil {
		t.Fatalf("PlanRotate narrowed: %v", err)
	}

	noKey := &store.Keyring{Source: store.KeySourceNone}
	if _, err := ExecRotate(ctx, db, noKey, planA, "rotated$with`specials`1234"); err == nil || !strings.Contains(err.Error(), "database key") {
		t.Fatalf("expected ExecRotate refusal without encryption key, got %v", err)
	}

	sameA := filepath.Join(repoA, ".env.local")
	sameB := filepath.Join(repoB, ".env.local")
	mustWriteFile(t, sameA, "PAIR_KEY=same-pair-secret-000011112222\n", 0o600)
	mustWriteFile(t, sameB, "PAIR_KEY=same-pair-secret-000011112222\n", 0o600)
	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatal(err)
	}
	pairPlan, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "PAIR_KEY", OnlyExisting: true}, "pair-new-value-123456")
	if err != nil || len(pairPlan.Targets) != 2 {
		t.Fatalf("PlanRotate PAIR_KEY: plan=%+v err=%v", pairPlan, err)
	}

	origModeB := os.FileMode(0o755)
	if err := os.Chmod(repoB, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(repoB, origModeB) })
	if _, err := ExecRotate(ctx, db, kr, pairPlan, "pair-new-value-123456"); err == nil {

		if os.Geteuid() == 0 {
			_ = os.Chmod(repoB, origModeB)
		} else {
			t.Fatalf("expected ExecRotate write error when second repo dir is 0555")
		}
	} else {
		gotSameA, _ := os.ReadFile(sameA)
		if string(gotSameA) != "PAIR_KEY=same-pair-secret-000011112222\n" {
			t.Fatalf("expected partial multi-file rollback of sameA, got %q", gotSameA)
		}
	}
	_ = os.Chmod(repoB, origModeB)

	pemPath := filepath.Join(repoA, ".env.pem")
	mustWriteFile(t, pemPath, strings.Join([]string{
		"# Header",
		`PEM_KEY="-----BEGIN PRIVATE KEY-----`,
		"line-two=fake",
		`-----END PRIVATE KEY-----" # pem inline`,
		"KEEP_AFTER=ok",
	}, "\n")+"\n", 0o600)
	if _, err := execSetVarForTest(ctx, db, kr, pemPath, "PEM_KEY", "new$val`tick`end"); err != nil {
		t.Fatalf("ExecSetVar multiline PEM: %v", err)
	}
	gotPem, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatal(err)
	}
	wantPem := strings.Join([]string{
		"# Header",
		`PEM_KEY="new\$val\` + "`tick\\`end\" # pem inline",
		"KEEP_AFTER=ok",
	}, "\n") + "\n"
	if string(gotPem) != wantPem {
		t.Fatalf("multiline PEM rewrite mismatch:\ngot:\n%s\nwant:\n%s", gotPem, wantPem)
	}

	if err := os.WriteFile(pemPath, []byte("PEM_KEY=externally-edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoRotate(ctx, db, kr, ""); err == nil || !strings.Contains(err.Error(), "modified since rotation") {
		t.Fatalf("expected UndoRotate refusal on modified file, got %v", err)
	}
	allowCtx := *ctx
	allowCtx.Allow.Replace = true
	if _, err := UndoRotate(&allowCtx, db, kr, ""); err != nil {
		t.Fatalf("UndoRotate with Allow.Replace failed: %v", err)
	}
}

func TestSecretsDotDirDiscoveryPrecedenceAndParsing(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)
	ctx.LocalEnv["WS_ZONES"] = "work|Test User|user@example.invalid"
	workRoot := filepath.Join(ctx.HomeDir, "work")
	mustWriteFile(t, filepath.Join(workRoot, "mise.toml"), "[env]\nWS_ZONE = \"work\"\n", 0o600)

	repoDir := filepath.Join(workRoot, "code", "billing-svc")
	mustMkdir(t, filepath.Join(repoDir, ".git"))

	mustWriteFile(t, filepath.Join(repoDir, ".env"), strings.Join([]string{
		"KEY_ALL=from-dotenv",
		"KEY_SECRETS_ONLY=from-dotenv",
		"KEY_SHARED_VS_DOTENV=from-dotenv",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, filepath.Join(repoDir, ".secrets", "shared.env"), strings.Join([]string{
		"KEY_ALL=from-shared",
		"KEY_SECRETS_ONLY=from-shared",
		"KEY_SHARED_VS_DOTENV=from-shared",
		"SHARED_PLAIN_FLAG=custom-non-benign-value",
		"PORT=9000",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, filepath.Join(repoDir, ".secrets", "prod.env"), strings.Join([]string{
		"# prod secrets",
		"KEY_ALL=from-secrets-prod",
		"KEY_NO_LOCAL=from-secrets-prod",
		"KEY_SECRETS_ONLY=from-secrets-prod",
		`DOTENV_DOLLAR_TOKEN="sk-prod-$(literal-not-shell)-11112222"`,
		"DOTENV_TICK_SECRET='tick`secret`value-33334444'",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, filepath.Join(repoDir, ".env.staging"), strings.Join([]string{
		"KEY_ALL=from-env-staging",
		"KEY_NO_LOCAL=from-env-staging",
	}, "\n")+"\n", 0o600)
	mustWriteFile(t, filepath.Join(repoDir, ".env.local"), "KEY_ALL=from-env-local\n", 0o600)

	mustWriteFile(t, filepath.Join(repoDir, ".env.example"), "SKIP_TEMPLATE=1\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, "prod.env.example"), "SKIP_TEMPLATE=2\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, ".secrets", "prod.sample.env"), "SKIP_TEMPLATE=3\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, ".secrets", "shared.template.env"), "SKIP_TEMPLATE=4\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, "sample.env"), "SKIP_TEMPLATE=5\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, "node_modules", "pkg", ".env"), "SKIP_HEAVY=1\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, ".venv", "prod.env"), "SKIP_HEAVY=2\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, ".cache", "prod.env"), "SKIP_HEAVY=3\n", 0o644)
	mustWriteFile(t, filepath.Join(repoDir, "dist", "prod.env"), "SKIP_HEAVY=4\n", 0o644)

	res, err := Scan(ctx, db, kr)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.FilesScanned < 5 {
		t.Fatalf("expected >=5 files scanned, got %+v", res)
	}

	for _, skippedKey := range []string{"SKIP_TEMPLATE", "SKIP_HEAVY"} {
		entries, err := FindEntriesForZone(db, skippedKey, "")
		if err != nil || len(entries) != 0 {
			t.Fatalf("expected %s skipped, got %+v err=%v", skippedKey, entries, err)
		}
	}

	for _, k := range []string{"DOTENV_DOLLAR_TOKEN", "DOTENV_TICK_SECRET", "SHARED_PLAIN_FLAG"} {
		entries, err := FindEntriesForZone(db, k, "")
		if err != nil || len(entries) != 1 {
			t.Fatalf("expected 1 entry for %s from .secrets/*.env, got %+v err=%v", k, entries, err)
		}
		if !entries[0].IsSecret {
			t.Fatalf("expected %s in .secrets/*.env to be classified secret", k)
		}
		if strings.Contains(entries[0].MaskedValue, "literal-not-shell") ||
			strings.Contains(entries[0].MaskedValue, "tick`secret") ||
			strings.Contains(entries[0].MaskedValue, "custom-non-benign-value") {
			t.Fatalf("%s leaked cleartext in MaskedValue: %q", k, entries[0].MaskedValue)
		}
	}
	portEntries, err := FindEntriesForZone(db, "PORT", "")
	if err != nil || len(portEntries) != 1 || portEntries[0].IsSecret || portEntries[0].MaskedValue != "9000" {
		t.Fatalf("expected allowlisted PORT=9000 non-secret in .secrets/shared.env, got %+v err=%v", portEntries, err)
	}

	prodEnvPath := filepath.Join(repoDir, ".secrets", "prod.env")
	rev, err := RevealValue(ctx, db, kr, ctx.CollapseHome(prodEnvPath), "DOTENV_DOLLAR_TOKEN")
	if err != nil || rev != "sk-prod-$(literal-not-shell)-11112222" {
		t.Fatalf("RevealValue .secrets/prod.env got %q err=%v", rev, err)
	}

	provAll, err := GetProvenance(ctx, db, "KEY_ALL", repoDir)
	if err != nil {
		t.Fatalf("GetProvenance KEY_ALL: %v", err)
	}
	wantSuffixes := []string{
		"/.env",
		"/.secrets/shared.env",
		"/.secrets/prod.env",
		"/.env.staging",
		"/.env.local",
	}
	if len(provAll.Layers) != len(wantSuffixes) {
		t.Fatalf("expected %d layers for KEY_ALL, got %d: %+v", len(wantSuffixes), len(provAll.Layers), provAll.Layers)
	}
	for i, suf := range wantSuffixes {
		if !strings.HasSuffix(filepath.ToSlash(provAll.Layers[i].SourcePath), suf) {
			t.Fatalf("layer %d path=%q want suffix %q", i, provAll.Layers[i].SourcePath, suf)
		}
		wantStatus := StatusShadowed
		if i == len(wantSuffixes)-1 {
			wantStatus = StatusActive
		}
		if provAll.Layers[i].Status != wantStatus {
			t.Fatalf("layer %d status=%q want %q", i, provAll.Layers[i].Status, wantStatus)
		}
	}

	provSecretsDir, err := GetProvenance(ctx, db, "KEY_SECRETS_ONLY", filepath.Join(repoDir, ".secrets"))
	if err != nil || len(provSecretsDir.Layers) != 3 {
		t.Fatalf("expected 3 layers for KEY_SECRETS_ONLY scoped to .secrets, got %+v err=%v", provSecretsDir, err)
	}
	if !strings.HasSuffix(provSecretsDir.Layers[2].SourcePath, "/.secrets/prod.env") || provSecretsDir.Layers[2].Status != StatusActive {
		t.Fatalf("expected .secrets/prod.env ACTIVE for KEY_SECRETS_ONLY, got %+v", provSecretsDir.Layers)
	}

	sums, err := ListSummariesForZone(db, "", "work", false)
	if err != nil {
		t.Fatalf("ListSummariesForZone: %v", err)
	}
	activeByKey := map[string]EnvSummary{}
	for _, s := range sums {
		activeByKey[s.VarKey] = s
	}
	if !strings.HasSuffix(activeByKey["KEY_ALL"].ActiveSourcePath, "/.env.local") {
		t.Fatalf("expected KEY_ALL winner .env.local, got %+v", activeByKey["KEY_ALL"])
	}
	if !strings.HasSuffix(activeByKey["KEY_NO_LOCAL"].ActiveSourcePath, "/.env.staging") {
		t.Fatalf("expected KEY_NO_LOCAL winner .env.staging, got %+v", activeByKey["KEY_NO_LOCAL"])
	}
	if !strings.HasSuffix(activeByKey["KEY_SECRETS_ONLY"].ActiveSourcePath, "/.secrets/prod.env") {
		t.Fatalf("expected KEY_SECRETS_ONLY winner .secrets/prod.env, got %+v", activeByKey["KEY_SECRETS_ONLY"])
	}
	if !strings.HasSuffix(activeByKey["KEY_SHARED_VS_DOTENV"].ActiveSourcePath, "/.secrets/shared.env") {
		t.Fatalf("expected KEY_SHARED_VS_DOTENV winner .secrets/shared.env, got %+v", activeByKey["KEY_SHARED_VS_DOTENV"])
	}
}

func TestSecretsDotEnvRotationAndSetVar(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)
	repoDir := filepath.Join(ctx.HomeDir, "code", "payments")
	mustMkdir(t, filepath.Join(repoDir, ".git"))

	prodPath := filepath.Join(repoDir, ".secrets", "prod.env")
	origProd := "# payment prod secrets\nSTRIPE_SECRET_KEY=\"sk-old-prod-11112222\"\nKEEP=1\n"
	mustWriteFile(t, prodPath, origProd, 0o644)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	plan, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "STRIPE_SECRET_KEY", OnlyExisting: true}, "sk-new-prod-99998888")
	if err != nil {
		t.Fatalf("PlanRotate .secrets/prod.env: %v", err)
	}
	if len(plan.Targets) != 1 || !strings.HasSuffix(plan.Targets[0].SourcePath, "/.secrets/prod.env") {
		t.Fatalf("expected 1 rotation target .secrets/prod.env, got %+v", plan.Targets)
	}

	res, err := ExecRotate(ctx, db, kr, plan, "sk-new-prod-99998888")
	if err != nil {
		t.Fatalf("ExecRotate .secrets/prod.env: %v", err)
	}
	got, err := os.ReadFile(prodPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "# payment prod secrets\nSTRIPE_SECRET_KEY=\"sk-new-prod-99998888\"\nKEEP=1\n"
	if string(got) != want {
		t.Fatalf("rotated .secrets/prod.env mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if fi, _ := os.Stat(prodPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 perms after rotating .secrets/prod.env, got %04o", fi.Mode().Perm())
	}

	if _, err := UndoRotate(ctx, db, kr, res.TxID); err != nil {
		t.Fatalf("UndoRotate .secrets/prod.env: %v", err)
	}
	if restored, _ := os.ReadFile(prodPath); string(restored) != origProd {
		t.Fatalf("expected UndoRotate to restore .secrets/prod.env, got %q", restored)
	}

	sharedPath := filepath.Join(repoDir, ".secrets", "shared.env")
	if _, err := execSetVarForTest(ctx, db, kr, sharedPath, "WEBHOOK_SECRET", "whsec_example_00001111"); err != nil {
		t.Fatalf("ExecSetVar .secrets/shared.env: %v", err)
	}
	if _, err := PlanSetVar(ctx, db, kr, filepath.Join(repoDir, ".secrets", "prod.sample.env"), "WEBHOOK_SECRET", "x"); err == nil {
		t.Fatalf("expected PlanSetVar to refuse prod.sample.env template")
	}
}

func TestZoneNonCollapsingAndListFiles(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)
	ctx.LocalEnv["WS_ZONES"] = strings.Join([]string{
		"work|Work User|work@example.invalid",
		"personal|Personal User|personal@example.invalid",
	}, "\n")

	for _, z := range []string{"work", "personal"} {
		zroot := filepath.Join(ctx.HomeDir, z)
		mustWriteFile(t, filepath.Join(zroot, "mise.toml"), "[env]\nWS_ZONE = \""+z+"\"\n", 0o600)
	}

	workRepo := filepath.Join(ctx.HomeDir, "work", "code", "api")
	personalRepo := filepath.Join(ctx.HomeDir, "personal", "code", "blog")
	mustMkdir(t, filepath.Join(workRepo, ".git"))
	mustMkdir(t, filepath.Join(personalRepo, ".git"))

	mustWriteFile(t, filepath.Join(workRepo, ".secrets", "prod.env"), "DATABASE_URL=postgres://u:pw1@work-db/prod\nPORT=8080\n", 0o600)
	mustWriteFile(t, filepath.Join(workRepo, ".env"), "SHARED_KEY=work-val-111122223333\n", 0o600)
	mustWriteFile(t, filepath.Join(personalRepo, ".secrets", "shared.env"), "DATABASE_URL=postgres://u:pw2@personal-db/app\nSHARED_KEY=personal-val-444455556666\n", 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	allSummaries, err := ListSummariesForZone(db, "DATABASE_URL", "", false)
	if err != nil {
		t.Fatalf("ListSummaries: %v", err)
	}
	if len(allSummaries) != 2 {
		t.Fatalf("expected 2 non-collapsed summaries for DATABASE_URL (work + personal), got %d: %+v", len(allSummaries), allSummaries)
	}
	zonesSeen := map[string]bool{}
	for _, s := range allSummaries {
		zonesSeen[s.Zone] = true
	}
	if !zonesSeen["work"] || !zonesSeen["personal"] {
		t.Fatalf("expected both work and personal zones in DATABASE_URL summaries, got %+v", allSummaries)
	}

	workSummaries, err := ListSummariesForZone(db, "", "work", false)
	if err != nil {
		t.Fatalf("ListSummariesForZone work: %v", err)
	}
	if len(workSummaries) == 0 {
		t.Fatalf("expected work summaries")
	}
	for _, s := range workSummaries {
		if s.Zone != "work" {
			t.Fatalf("unexpected non-work summary in work filter: %+v", s)
		}
	}

	personalEntries, err := FindEntriesForZone(db, "SHARED_KEY", "personal")
	if err != nil {
		t.Fatalf("FindEntriesForZone personal: %v", err)
	}
	if len(personalEntries) != 1 || personalEntries[0].Zone != "personal" {
		t.Fatalf("expected 1 personal entry for SHARED_KEY, got %+v", personalEntries)
	}

	allFiles, err := ListFiles(db, "")
	if err != nil {
		t.Fatalf("ListFiles all: %v", err)
	}
	if len(allFiles) < 5 {
		t.Fatalf("expected >=5 indexed files (2 zone mise + 3 dotenv), got %d: %+v", len(allFiles), allFiles)
	}
	workFiles, err := ListFiles(db, "work")
	if err != nil {
		t.Fatalf("ListFiles work: %v", err)
	}
	foundProdEnv := false
	for _, f := range workFiles {
		if f.Zone != "work" {
			t.Fatalf("ListFiles(work) leaked non-work file: %+v", f)
		}
		if strings.HasSuffix(f.Path, "/.secrets/prod.env") {
			foundProdEnv = true
			if f.VarCount != 2 || f.SecretCount != 1 {
				t.Fatalf("unexpected counts for .secrets/prod.env: %+v", f)
			}
		}
	}
	if !foundProdEnv {
		t.Fatalf("expected .secrets/prod.env in ListFiles(work), got %+v", workFiles)
	}
}

func TestPhase4EnvmgrHardening(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	miseDir := filepath.Join(ctx.ConfigDir, "mise")
	mustWriteFile(t, filepath.Join(miseDir, "config.toml"), "[env]\nGLOBAL_VAR = \"from-base\"\n", 0o644)
	mustWriteFile(t, filepath.Join(miseDir, "config.local.toml"), "[env]\nGLOBAL_VAR = \"from-local\"\n", 0o600)

	if !looksHighEntropySecret("0123456789abcdef0123456789abcdef") {
		t.Fatal("expected 32-char hex string to be classified high-entropy secret")
	}

	repoDir := filepath.Join(ctx.HomeDir, "code", "nested-app")
	mustMkdir(t, filepath.Join(repoDir, ".git"))
	nestedEnv := filepath.Join(repoDir, ".secrets", "prod", "app.env")
	mustWriteFile(t, nestedEnv, strings.Join([]string{
		`MULTI_PEM="line1`,
		`line2=fake"`,
		`ESC_SECRET="a\$b\` + "`c\"",
		`DUP_TOKEN=first-val-11112222`,
		`DUP_TOKEN=second-val-33334444`,
	}, "\n")+"\n", 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	globalEntries, err := FindEntriesForZone(db, "GLOBAL_VAR", "")
	if err != nil || len(globalEntries) != 2 {
		t.Fatalf("FindEntries(GLOBAL_VAR): %+v err=%v", globalEntries, err)
	}
	if globalEntries[0].SourceKind != SourceGlobalMiseLocal || !globalEntries[0].IsActive {
		t.Fatalf("expected active SourceGlobalMiseLocal entry, got %+v", globalEntries)
	}

	if gotScope := effectiveEnvDirWithCtx(ctx, nestedEnv); gotScope != filepath.ToSlash(repoDir) {
		t.Fatalf("effectiveEnvDirWithCtx(%s) = %q, want %q", nestedEnv, gotScope, filepath.ToSlash(repoDir))
	}

	multiVal, err := RevealValue(ctx, db, kr, ctx.CollapseHome(nestedEnv), "MULTI_PEM")
	if err != nil || multiVal != "line1\nline2=fake" {
		t.Fatalf("RevealValue multiline = %q err=%v", multiVal, err)
	}
	escVal, err := RevealValue(ctx, db, kr, ctx.CollapseHome(nestedEnv), "ESC_SECRET")
	if err != nil || escVal != "a$b`c" {
		t.Fatalf("RevealValue escaped = %q err=%v", escVal, err)
	}

	mustWriteFile(t, filepath.Join(ctx.HomeDir, ".ssh", "id_ed25519"), "SECRET_KEY=do-not-read\n", 0o600)
	if _, err := RevealValue(ctx, db, kr, "~/.ssh/id_ed25519", "SECRET_KEY"); err == nil {
		t.Fatal("expected RevealValue to reject unindexed ~/.ssh/id_ed25519")
	}
	if err := os.Remove(nestedEnv); err != nil {
		t.Fatal(err)
	}
	fbVal, err := RevealValue(ctx, db, kr, ctx.CollapseHome(nestedEnv), "ESC_SECRET")
	if err != nil || fbVal != "a$b`c" {
		t.Fatalf("RevealValue EnvBlobAAD fallback = %q err=%v", fbVal, err)
	}

	if _, err := RestoreEnvFiles(ctx, db, kr, []string{ctx.CollapseHome(nestedEnv)}, true); err != nil {
		t.Fatalf("RestoreEnvFiles EnvBlobAAD: %v", err)
	}
	if _, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "DUP_TOKEN", Paths: []string{nestedEnv}, OnlyExisting: true}, "new-dup-token"); err == nil || !strings.Contains(err.Error(), "distinct values") {
		t.Fatalf("expected PlanRotate ambiguity error on duplicate key in same file, got %v", err)
	}

	newLocal := filepath.Join(repoDir, ".env.local")
	rotRes, err := execSetVarForTest(ctx, db, kr, newLocal, "RAW_DOLLAR", "abc$def`ghi")
	if err != nil {
		t.Fatalf("ExecSetVar single-quote: %v", err)
	}
	rawLocal, _ := os.ReadFile(newLocal)
	if !strings.Contains(string(rawLocal), "RAW_DOLLAR='abc$def`ghi'") {
		t.Fatalf("expected single-quoted RAW_DOLLAR in %q", string(rawLocal))
	}

	if err := os.WriteFile(newLocal, []byte("RAW_DOLLAR=tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dryCtx := *ctx
	dryCtx.DryRun = true
	if _, err := UndoRotate(&dryCtx, db, kr, rotRes.TxID); err == nil {
		t.Fatal("expected DryRun UndoRotate to fail when post-write file was modified")
	}

	if written, err := RestoreEnvFiles(ctx, db, kr, []string{".env"}, false); err != nil || len(written) != 0 {
		t.Fatalf("expected .env filter to skip .env.local/app.env, got written=%v err=%v", written, err)
	}
	if err := os.Remove(nestedEnv); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nestedEnv, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreEnvFiles(ctx, db, kr, []string{ctx.CollapseHome(nestedEnv)}, true); err == nil {
		t.Fatal("expected RestoreEnvFiles to reject directory at target path")
	}

	miseWithNotes := strings.Join([]string{
		"[notes]",
		`banner = """`,
		"[env]",
		`API_KEY = "fake-inside-string"`,
		`"""`,
		"",
		"[env]",
		`API_KEY = "old-val"`,
		`OTHER = "keep"`,
	}, "\n") + "\n"
	updatedNotesTOML, err := updateMiseLocalContent([]byte(miseWithNotes), map[string]string{"API_KEY": "new-val"}, []string{"API_KEY"})
	if err != nil {
		t.Fatalf("updateMiseLocalContent multiline notes: %v", err)
	}
	if !strings.Contains(string(updatedNotesTOML), `API_KEY = "new-val"`) || !strings.Contains(string(updatedNotesTOML), `API_KEY = "fake-inside-string"`) {
		t.Fatalf("unexpected multiline notes TOML output:\n%s", string(updatedNotesTOML))
	}

	miseRootDotted := strings.Join([]string{
		`env.API_KEY = "old-root-key"`,
		"",
		"[tools]",
		`go = "1.27"`,
	}, "\n") + "\n"
	updatedDottedTOML, err := updateMiseLocalContent([]byte(miseRootDotted), map[string]string{"API_KEY": "new-root-key"}, []string{"API_KEY"})
	if err != nil {
		t.Fatalf("updateMiseLocalContent root dotted: %v", err)
	}
	if !strings.Contains(string(updatedDottedTOML), `env.API_KEY = "new-root-key"`) || strings.Count(string(updatedDottedTOML), "new-root-key") != 1 {
		t.Fatalf("expected single updated root env.API_KEY:\n%s", string(updatedDottedTOML))
	}
}

func execSetVarForTest(ctx *app.Context, db *store.DB, kr *store.Keyring, filePath, varKey, newValue string) (*RotateResult, error) {
	plan, err := PlanSetVar(ctx, db, kr, filePath, varKey, newValue)
	if err != nil {
		return nil, err
	}
	return ExecRotate(ctx, db, kr, plan, newValue)
}

func TestRotatePersistsUndoBeforeFirstWriteAndRetainsOnRollbackFailure(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	repoA := filepath.Join(ctx.HomeDir, "code", "svc-a")
	repoB := filepath.Join(ctx.HomeDir, "code", "svc-b")
	mustMkdir(t, filepath.Join(repoA, ".git"))
	mustMkdir(t, filepath.Join(repoB, ".git"))

	envA := filepath.Join(repoA, ".env")
	envB := filepath.Join(repoB, ".env")
	origA := "ROT_KEY=shared-secret-value-1111\n"
	origB := "ROT_KEY=shared-secret-value-1111\n"
	mustWriteFile(t, envA, origA, 0o600)
	mustWriteFile(t, envB, origB, 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	plan, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "ROT_KEY", OnlyExisting: true}, "new-secret-value-9999")
	if err != nil || len(plan.Targets) != 2 {
		t.Fatalf("PlanRotate: plan=%+v err=%v", plan, err)
	}

	origWriter := writeAtomicFile
	t.Cleanup(func() { writeAtomicFile = origWriter })

	checkedBeforeFirstWrite := false
	writeCount := 0
	writeAtomicFile = func(path string, data []byte, perm os.FileMode) error {
		writeCount++
		if writeCount == 1 {
			if _, ok, merr := db.GetMeta("rotate_tx:" + plan.TxID); merr != nil || !ok {
				t.Errorf("expected rotate_tx:%s persisted before first write, ok=%v err=%v", plan.TxID, ok, merr)
			}
			if _, ok, merr := db.GetMeta("last_rotate_tx"); merr != nil || !ok {
				t.Errorf("expected last_rotate_tx persisted before first write, ok=%v err=%v", ok, merr)
			}
			blobs, berr := db.ListSecretBlobs("rotate-undo")
			if berr != nil || len(blobs) != 2 {
				t.Errorf("expected 2 encrypted undo blobs before first write, got len=%d err=%v", len(blobs), berr)
			}
			checkedBeforeFirstWrite = true
			return origWriter(path, data, perm)
		}
		if writeCount == 2 {
			return errors.New("simulated second file write failure")
		}
		return errors.New("simulated rollback write failure")
	}

	_, err = ExecRotate(ctx, db, kr, plan, "new-secret-value-9999")
	if err == nil || !strings.Contains(err.Error(), "partial rollback errors") {
		t.Fatalf("expected partial rollback error, got %v", err)
	}
	if !checkedBeforeFirstWrite {
		t.Fatalf("expected pre-write undo persistence check to run")
	}

	gotA, rerr := os.ReadFile(envA)
	if rerr != nil || !strings.Contains(string(gotA), "new-secret-value-9999") {
		t.Fatalf("expected envA to still have rotated value after failed rollback, got %q err=%v", string(gotA), rerr)
	}
	blobsAfterFail, berr := db.ListSecretBlobs("rotate-undo")
	if berr != nil || len(blobsAfterFail) != 2 {
		t.Fatalf("expected encrypted undo blobs retained after failed rollback, got len=%d err=%v", len(blobsAfterFail), berr)
	}
	if _, ok, merr := db.GetMeta("rotate_tx:" + plan.TxID); merr != nil || !ok {
		t.Fatalf("expected rotate_tx:%s retained after failed rollback, ok=%v err=%v", plan.TxID, ok, merr)
	}

	writeAtomicFile = origWriter
	allowCtx := *ctx
	allowCtx.Allow.Replace = true
	if _, uerr := UndoRotate(&allowCtx, db, kr, plan.TxID); uerr != nil {
		t.Fatalf("UndoRotate recovery after failed rollback failed: %v", uerr)
	}
	restoredA, rerr := os.ReadFile(envA)
	if rerr != nil || string(restoredA) != origA {
		t.Fatalf("expected envA restored to %q, got %q err=%v", origA, string(restoredA), rerr)
	}
	restoredB, rerr := os.ReadFile(envB)
	if rerr != nil || string(restoredB) != origB {
		t.Fatalf("expected envB preserved as %q, got %q err=%v", origB, string(restoredB), rerr)
	}

	plan2, err := PlanRotate(ctx, db, kr, RotateRequest{VarKey: "ROT_KEY", OnlyExisting: true}, "second-attempt-value-8888")
	if err != nil {
		t.Fatalf("PlanRotate 2: %v", err)
	}
	writeCount = 0
	writeAtomicFile = func(path string, data []byte, perm os.FileMode) error {
		writeCount++
		if writeCount == 2 {
			return errors.New("simulated second file write failure with clean rollback")
		}
		return origWriter(path, data, perm)
	}
	if _, err := ExecRotate(ctx, db, kr, plan2, "second-attempt-value-8888"); err == nil {
		t.Fatalf("expected ExecRotate error on simulated second write failure")
	}
	if _, ok, _ := db.GetMeta("rotate_tx:" + plan2.TxID); ok {
		t.Fatalf("expected rotate_tx:%s cleaned up after complete rollback", plan2.TxID)
	}
	if blob, _ := db.GetSecretBlob("undo:" + plan2.TxID + ":0"); blob != nil {
		t.Fatalf("expected undo blob for cleanly rolled back tx deleted")
	}
}

func TestFingerprintRotateTargetsExactLineAndVerifiesBeforeWrite(t *testing.T) {
	ctx, db, kr := setupTestEnv(t)

	repoDir := filepath.Join(ctx.HomeDir, "code", "dup-app")
	mustMkdir(t, filepath.Join(repoDir, ".git"))
	envPath := filepath.Join(repoDir, ".env")
	secretOne := "sk-secret-one-11112222"
	secretTwo := "sk-secret-two-33334444"
	orig := strings.Join([]string{
		"API_KEY=" + secretOne,
		"KEEP_ME=unchanged",
		"API_KEY=" + secretTwo,
	}, "\n") + "\n"
	mustWriteFile(t, envPath, orig, 0o600)

	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	fpOne := store.Fingerprint(kr.Key, secretOne)
	fpTwo := store.Fingerprint(kr.Key, secretTwo)

	planOne, err := PlanRotate(ctx, db, kr, RotateRequest{MatchFingerprint: fpOne}, "sk-rotated-one-9999")
	if err != nil {
		t.Fatalf("PlanRotate fpOne: %v", err)
	}
	if len(planOne.Targets) != 1 || planOne.Targets[0].LineNum != 1 || planOne.Targets[0].ExpectedFingerprint != fpOne {
		t.Fatalf("expected single target at line 1 with expected fingerprint %s, got %+v", fpOne, planOne.Targets)
	}
	if _, err := ExecRotate(ctx, db, kr, planOne, "sk-rotated-one-9999"); err != nil {
		t.Fatalf("ExecRotate fpOne: %v", err)
	}

	afterOne, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	wantAfterOne := strings.Join([]string{
		"API_KEY=sk-rotated-one-9999",
		"KEEP_ME=unchanged",
		"API_KEY=" + secretTwo,
	}, "\n") + "\n"
	if string(afterOne) != wantAfterOne {
		t.Fatalf("fingerprint-limited rotation rewrote wrong line:\ngot:\n%s\nwant:\n%s", string(afterOne), wantAfterOne)
	}

	planTwo, err := PlanRotate(ctx, db, kr, RotateRequest{MatchFingerprint: fpTwo}, "sk-rotated-two-8888")
	if err != nil || len(planTwo.Targets) != 1 || planTwo.Targets[0].LineNum != 3 {
		t.Fatalf("PlanRotate fpTwo: plan=%+v err=%v", planTwo, err)
	}

	tampered := strings.Join([]string{
		"API_KEY=sk-rotated-one-9999",
		"KEEP_ME=unchanged",
		"API_KEY=sk-different-secret-7777",
	}, "\n") + "\n"
	mustWriteFile(t, envPath, tampered, 0o600)
	if _, err := ExecRotate(ctx, db, kr, planTwo, "sk-rotated-two-8888"); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("expected fingerprint mismatch error on tampered line, got %v", err)
	}
	if got, _ := os.ReadFile(envPath); string(got) != tampered {
		t.Fatalf("failed fingerprint verification must not modify file, got:\n%s", string(got))
	}

	shifted := "\n" + wantAfterOne
	mustWriteFile(t, envPath, shifted, 0o600)
	if _, err := ExecRotate(ctx, db, kr, planTwo, "sk-rotated-two-8888"); err == nil {
		t.Fatalf("expected line-identity error when target line shifted")
	}
	if got, _ := os.ReadFile(envPath); string(got) != shifted {
		t.Fatalf("failed line verification must not modify file, got:\n%s", string(got))
	}

	sameDup := strings.Join([]string{
		"API_KEY=" + secretTwo,
		"OTHER=ok",
		"API_KEY=" + secretTwo,
	}, "\n") + "\n"
	mustWriteFile(t, envPath, sameDup, 0o600)
	if _, err := Scan(ctx, db, kr); err != nil {
		t.Fatalf("Scan sameDup: %v", err)
	}
	planBoth, err := PlanRotate(ctx, db, kr, RotateRequest{MatchFingerprint: fpTwo}, "sk-rotated-both-5555")
	if err != nil || len(planBoth.Targets) != 2 {
		t.Fatalf("expected both identical-fingerprint lines targeted, got plan=%+v err=%v", planBoth, err)
	}
	if _, err := ExecRotate(ctx, db, kr, planBoth, "sk-rotated-both-5555"); err != nil {
		t.Fatalf("ExecRotate both: %v", err)
	}
	wantBoth := strings.Join([]string{
		"API_KEY=sk-rotated-both-5555",
		"OTHER=ok",
		"API_KEY=sk-rotated-both-5555",
	}, "\n") + "\n"
	if got, _ := os.ReadFile(envPath); string(got) != wantBoth {
		t.Fatalf("expected both matching lines rotated:\ngot:\n%s\nwant:\n%s", string(got), wantBoth)
	}

	dryCtx := *ctx
	dryCtx.DryRun = true
	if res, err := Scan(&dryCtx, nil, kr); err != nil || res.EntriesCount == 0 || res.BlobsStored != 0 {
		t.Fatalf("expected DryRun Scan in memory without DB to succeed with 0 blobs, got res=%+v err=%v", res, err)
	}
}
