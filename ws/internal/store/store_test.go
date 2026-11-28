package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func testContext(t *testing.T) *app.Context {
	t.Helper()
	home := t.TempDir()
	return &app.Context{
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		LocalEnv:  map[string]string{},
	}
}

func TestDBOpenMigrationsPermsAndMeta(t *testing.T) {
	ctx := testContext(t)
	dbPath := DefaultPath(ctx.WSBaseDir())
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected db perms 0600, got %04o", perm)
	}

	ver, ok, err := db.GetMeta("schema_version")
	if err != nil || !ok || ver != "2" {
		t.Fatalf("unexpected schema_version: ok=%v ver=%q err=%v", ok, ver, err)
	}

	st, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.SchemaVersion != SchemaVersion || len(st.TableCounts) != 6 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestCryptoEncryptDecryptFingerprintAndMask(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")
	ctx := testContext(t)

	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	if !kr.HasKey() || kr.Source != KeySourceKeyFile {
		t.Fatalf("unexpected keyring: %+v", kr)
	}

	fi, err := os.Stat(DefaultKeyPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Stat keyfile: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 keyfile, got %04o", fi.Mode().Perm())
	}

	reloaded, err := LoadKey(ctx)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if !bytes.Equal(kr.Key, reloaded.Key) {
		t.Fatalf("reloaded key mismatch")
	}
	if _, err := InitKey(ctx, false); err == nil {
		t.Fatalf("expected InitKey overwrite refusal without Allow.Replace")
	}

	plain := []byte("API_TOKEN=sk-example-secret-123456\n")
	nonce, ct, err := Encrypt(kr.Key, plain, []byte("envfile:app/.env"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(kr.Key, nonce, ct, []byte("envfile:app/.env"))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted mismatch: got %q want %q", got, plain)
	}
	if _, err := Decrypt(kr.Key, nonce, ct, []byte("envfile:wrong")); err == nil {
		t.Fatalf("expected AAD mismatch failure")
	}

	fp1 := Fingerprint(kr.Key, "secret-token-value")
	fp2 := Fingerprint(kr.Key, "secret-token-value")
	fp3 := Fingerprint(kr.Key, "different-token")
	if !strings.HasPrefix(fp1, "fp:") || fp1 != fp2 || fp1 == fp3 {
		t.Fatalf("unexpected fingerprints: fp1=%q fp2=%q fp3=%q", fp1, fp2, fp3)
	}

	if m := MaskValue("sk-proj-1234567890abcd", true); m != "sk-p…••••" {
		t.Fatalf("unexpected secret mask (22 chars): %q", m)
	}
	if m := MaskValue("hunter2pass!", true); m != "••••" {
		t.Fatalf("expected short/medium secret (<20 chars) fully masked, got %q", m)
	}
	if m := MaskValue("short", true); m != "••••" {
		t.Fatalf("unexpected short secret mask: %q", m)
	}
}

func TestContentHashStabilityAndDiffGatedBackupRestore(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")
	ctx := testContext(t)
	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	db, err := Open(DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	envData := []byte("TOKEN=topsecret-9999\n")
	nonce, ct, err := Encrypt(kr.Key, envData, []byte("envfile:proj/.env"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := db.PutSecretBlob(SecretBlob{
		ID:          "envfile:proj/.env",
		Scope:       "envfile",
		Label:       "proj/.env",
		SourcePath:  filepath.Join(ctx.HomeDir, "proj", ".env"),
		Nonce:       nonce,
		Ciphertext:  ct,
		Fingerprint: Fingerprint(kr.Key, string(envData)),
		Perm:        0o600,
	}); err != nil {
		t.Fatalf("PutSecretBlob: %v", err)
	}

	if _, err := db.SQL().Exec(
		`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
		 VALUES('TOKEN', 'personal', 'dotenv', ?, 1, 50, 1, 'tops…9999', ?, 1, 1000)`,
		filepath.Join(ctx.HomeDir, "proj", ".env"), Fingerprint(kr.Key, "topsecret-9999"),
	); err != nil {
		t.Fatalf("insert env_entries: %v", err)
	}

	h1, err := db.ContentHash()
	if err != nil {
		t.Fatalf("ContentHash h1: %v", err)
	}

	if _, err := db.SQL().Exec(
		`INSERT INTO gh_cache(kind, repo, number, title, state, author, url, zone, updated_at, fetched_at)
		 VALUES('pr', 'org/app', 1, 'Test', 'open', 'dev', 'https://example.invalid/1', 'personal', 1000, 1000)`,
	); err != nil {
		t.Fatalf("insert cache: %v", err)
	}
	h2, err := db.ContentHash()
	if err != nil || h1 != h2 {
		t.Fatalf("cache write mutated ContentHash: %q vs %q (err=%v)", h1, h2, err)
	}

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	res1, err := BackupIfChanged(ctx, db, kr, BackupOptions{Now: now})
	if err != nil {
		t.Fatalf("BackupIfChanged 1: %v", err)
	}
	if !res1.Changed || res1.Skipped {
		t.Fatalf("expected initial backup to run: %+v", res1)
	}

	res2, err := BackupIfChanged(ctx, db, kr, BackupOptions{Now: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("BackupIfChanged 2: %v", err)
	}
	if res2.Changed || !res2.Skipped {
		t.Fatalf("expected second unchanged backup to skip: %+v", res2)
	}

	db2Path := filepath.Join(t.TempDir(), "ws2.db")
	db2, err := Open(db2Path)
	if err != nil {
		t.Fatalf("Open db2: %v", err)
	}
	defer db2.Close()

	targetEnv := filepath.Join(ctx.HomeDir, "proj", ".env")
	if _, err := os.Stat(targetEnv); !os.IsNotExist(err) {
		t.Fatalf("expected %s not to exist yet", targetEnv)
	}

	rRes, err := RestoreBackup(ctx, db2, kr, RestoreOptions{
		InputPath:  res1.LocalPath,
		WriteFiles: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if len(rRes.FilesWritten) != 1 || rRes.TablesRestored["env_entries"] != 1 {
		t.Fatalf("unexpected restore result: %+v", rRes)
	}
	restoredBytes, err := os.ReadFile(targetEnv)
	if err != nil || !bytes.Equal(restoredBytes, envData) {
		t.Fatalf("restored file mismatch: got %q err=%v", restoredBytes, err)
	}
	rfi, _ := os.Stat(targetEnv)
	if rfi.Mode().Perm() != 0o600 {
		t.Fatalf("expected restored file perm 0600, got %04o", rfi.Mode().Perm())
	}

	if _, err := RestoreBackup(ctx, db2, kr, RestoreOptions{
		InputPath:  res1.LocalPath,
		WriteFiles: true,
	}); err == nil {
		t.Fatalf("expected restore to refuse overwrite without AllowReplace")
	}

	if _, err := ResolveSafeHomePath(ctx.HomeDir, "/etc/passwd"); err == nil {
		t.Fatalf("expected ResolveSafeHomePath to reject escape outside HomeDir")
	}
}

func TestStoreSecurityHardening(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")
	ctx := testContext(t)

	dbPath := DefaultPath(ctx.WSBaseDir())
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	dirInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("expected store dir tightened to 0700, got %04o", dirInfo.Mode().Perm())
	}
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if info, statErr := os.Stat(p); statErr == nil {
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("expected %s to have no group/other perms, got %04o", p, info.Mode().Perm())
			}
		}
	}

	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}

	plain := []byte("API_SECRET=same-value-1234567890\n")
	n1, c1, err := Encrypt(kr.Key, plain, []byte("envfile:app/.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSecretBlob(SecretBlob{
		ID:          "envfile:app/.env",
		Scope:       "envfile",
		Label:       "app/.env",
		SourcePath:  "~/app/.env",
		Nonce:       n1,
		Ciphertext:  c1,
		Fingerprint: Fingerprint(kr.Key, string(plain)),
		Perm:        0o600,
	}); err != nil {
		t.Fatal(err)
	}
	hA, err := db.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	n2, c2, err := Encrypt(kr.Key, plain, []byte("envfile:app/.env"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.PutSecretBlob(SecretBlob{
		ID:          "envfile:app/.env",
		Scope:       "envfile",
		Label:       "app/.env",
		SourcePath:  "~/app/.env",
		Nonce:       n2,
		Ciphertext:  c2,
		Fingerprint: Fingerprint(kr.Key, string(plain)),
		Perm:        0o600,
	}); err != nil {
		t.Fatal(err)
	}
	hB, err := db.ContentHash()
	if err != nil {
		t.Fatal(err)
	}
	if hA != hB {
		t.Fatalf("ContentHash changed solely due to random nonce: %q vs %q", hA, hB)
	}

	replaceCtx := *ctx
	replaceCtx.Allow.Replace = true
	if _, err := InitKey(&replaceCtx, false); err == nil || !strings.Contains(err.Error(), "encrypted secret blob") {
		t.Fatalf("expected InitKey replacement refusal while secret_blobs has rows, got %v", err)
	}

	now := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)
	bRes1, err := BackupIfChanged(ctx, db, kr, BackupOptions{Now: now})
	if err != nil || !bRes1.Changed {
		t.Fatalf("BackupIfChanged 1 failed: %+v err=%v", bRes1, err)
	}
	if err := os.Remove(bRes1.LocalPath); err != nil {
		t.Fatal(err)
	}
	bRes2, err := BackupIfChanged(ctx, db, kr, BackupOptions{Now: now.Add(time.Minute)})
	if err != nil || bRes2.Skipped || !bRes2.Changed {
		t.Fatalf("expected BackupIfChanged to rewrite missing local backup file, got %+v err=%v", bRes2, err)
	}

	outsideDir := t.TempDir()
	restoreHomeCtx := testContext(t)
	if err := os.Symlink(outsideDir, filepath.Join(restoreHomeCtx.HomeDir, "app")); err != nil {
		t.Fatal(err)
	}
	dbRestore, err := Open(DefaultPath(restoreHomeCtx.WSBaseDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer dbRestore.Close()
	if _, err := RestoreBackup(restoreHomeCtx, dbRestore, kr, RestoreOptions{
		InputPath:  bRes2.LocalPath,
		WriteFiles: true,
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected RestoreBackup to reject symlink parent directory, got %v", err)
	}

	for line := 1; line <= 2; line++ {
		if _, err := db.SQL().Exec(
			`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
			 VALUES('DUP_KEY', 'personal', 'dotenv', '~/app/.env', ?, 50, ?, '••••', 'fp:same', 1, 1000)`,
			line, line-1,
		); err != nil {
			t.Fatal(err)
		}
	}
	bDup, err := BackupIfChanged(ctx, db, kr, BackupOptions{Force: true, Now: now.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	freshCtx := testContext(t)
	dbDup, err := Open(DefaultPath(freshCtx.WSBaseDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer dbDup.Close()
	if _, err := RestoreBackup(freshCtx, dbDup, kr, RestoreOptions{
		InputPath: bDup.LocalPath,
		Vars:      []string{"DUP_KEY"},
	}); err != nil {
		t.Fatalf("filtered RestoreBackup: %v", err)
	}
	var dupCount int
	if err := dbDup.SQL().QueryRow(`SELECT COUNT(*) FROM env_entries WHERE var_key = 'DUP_KEY'`).Scan(&dupCount); err != nil {
		t.Fatal(err)
	}
	if dupCount != 2 {
		t.Fatalf("expected both duplicate DUP_KEY rows preserved on filtered restore, got %d", dupCount)
	}
}

func TestDBOpenDropsLegacyAITables(t *testing.T) {
	ctx := testContext(t)
	dbPath := DefaultPath(ctx.WSBaseDir())

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE ai_providers (id TEXT PRIMARY KEY);`,
		`CREATE TABLE ai_file_cache (path TEXT PRIMARY KEY);`,
		`CREATE TABLE ai_limits_cache (provider_id TEXT PRIMARY KEY);`,
		`UPDATE meta SET value = '1' WHERE key = 'schema_version';`,
	} {
		if _, err := db.SQL().Exec(stmt); err != nil {
			t.Fatalf("seed legacy table: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer reopened.Close()

	for _, tbl := range legacyTablesToDrop {
		var count int
		if err := reopened.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, tbl).Scan(&count); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if count != 0 {
			t.Fatalf("legacy table %s was not dropped on reopen", tbl)
		}
	}

	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	legacyPlain := []byte(`{"format":"ws-backup-v1","createdAt":1000,"hash":"sha256:legacy","providers":[{"id":"p1","kind":"claude"}],"envEntries":[{"varKey":"LEGACY_OK","zone":"personal","sourceKind":"dotenv","sourcePath":"~/.env","lineNum":1,"precedence":50,"isActive":1,"maskedValue":"ok","fingerprint":"fp:1","isSecret":0,"updatedAt":1000}]}`)
	nonce, ct, err := Encrypt(kr.Key, legacyPlain, []byte(backupAAD))
	if err != nil {
		t.Fatalf("Encrypt legacy snapshot: %v", err)
	}
	envJSON, err := json.Marshal(encryptedEnvelope{
		Format:     backupFormatVersion,
		CreatedAt:  1000,
		Hash:       "sha256:legacy",
		Nonce:      nonce,
		Ciphertext: ct,
	})
	if err != nil {
		t.Fatalf("Marshal envelope: %v", err)
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy-backup.enc")
	if err := os.WriteFile(legacyPath, envJSON, 0o600); err != nil {
		t.Fatalf("WriteFile legacy backup: %v", err)
	}
	res, err := RestoreBackup(ctx, reopened, kr, RestoreOptions{InputPath: legacyPath})
	if err != nil {
		t.Fatalf("RestoreBackup legacy providers payload: %v", err)
	}
	if res.TablesRestored["env_entries"] != 1 {
		t.Fatalf("expected 1 env_entry restored from legacy backup, got %+v", res.TablesRestored)
	}
}

func TestDecodeKeyStrictAndFingerprintPrefix(t *testing.T) {
	validHex := strings.Repeat("a1", 32)
	got, err := decodeKeyStrict(validHex + "\n")
	if err != nil {
		t.Fatalf("decodeKeyStrict valid hex: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("decoded length = %d, want 32", len(got))
	}

	malformed := []string{
		"",
		"abcd",
		strings.Repeat("a", 63),
		strings.Repeat("a", 66),
		strings.Repeat("z", 64),
		strings.Repeat("g", 64),
	}
	for _, raw := range malformed {
		if _, err := decodeKeyStrict(raw); err == nil {
			t.Fatalf("expected decodeKeyStrict(%q) to fail", raw)
		}
	}

	unkeyed := Fingerprint(nil, "super-secret-value")
	if !strings.HasPrefix(unkeyed, "fpu:") {
		t.Fatalf("unkeyed Fingerprint = %q, want fpu: prefix", unkeyed)
	}
	keyed := Fingerprint(got, "super-secret-value")
	if strings.HasPrefix(keyed, "fpu:") || keyed == unkeyed {
		t.Fatalf("keyed Fingerprint = %q, should differ from unkeyed %q", keyed, unkeyed)
	}
}

func TestDecryptSecretBlobAADAndLegacyFallback(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	kr := &Keyring{Key: key, Source: KeySourceEnv}
	plaintext := []byte("API_KEY=secret-value\n")

	aad := EnvBlobAAD("blob-aad-1", "envfile", "~/.env", 0o600)
	nonce, ct, err := Encrypt(kr.Key, plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt bound AAD: %v", err)
	}
	blob := SecretBlob{
		ID:         "blob-aad-1",
		Scope:      "envfile",
		SourcePath: "~/.env",
		Perm:       0o600,
		Nonce:      nonce,
		Ciphertext: ct,
	}
	dec, err := DecryptSecretBlob(kr, blob)
	if err != nil {
		t.Fatalf("DecryptSecretBlob valid AAD: %v", err)
	}
	if !bytes.Equal(dec, plaintext) {
		t.Fatalf("decrypted = %q, want %q", dec, plaintext)
	}

	tampered := blob
	tampered.SourcePath = "~/.env.local"
	if _, err := DecryptSecretBlob(kr, tampered); err == nil {
		t.Fatal("expected tampered SourcePath to fail decryption")
	}

	n2, c2, err := Encrypt(kr.Key, plaintext, []byte("blob-legacy-1"))
	if err != nil {
		t.Fatalf("Encrypt legacy AAD: %v", err)
	}
	legacyBlob := SecretBlob{
		ID:         "blob-legacy-1",
		Scope:      "envfile",
		SourcePath: "~/.env",
		Perm:       0o600,
		Nonce:      n2,
		Ciphertext: c2,
	}
	decLegacy, err := DecryptSecretBlob(kr, legacyBlob)
	if err != nil {
		t.Fatalf("DecryptSecretBlob legacy fallback: %v", err)
	}
	if !bytes.Equal(decLegacy, plaintext) {
		t.Fatalf("legacy decrypted = %q, want %q", decLegacy, plaintext)
	}
}

func TestPathFiltersAndWritableEnvFilename(t *testing.T) {
	if !IsWritableEnvFilename(".env") || !IsWritableEnvFilename(".env.local") || !IsWritableEnvFilename(".envrc") {
		t.Fatal("expected .env, .env.local, and .envrc to be writable")
	}
	for _, bad := range []string{".bashrc", ".zshrc", ".env.example", "evil.sh", "authorized_keys"} {
		if IsWritableEnvFilename(bad) {
			t.Fatalf("expected IsWritableEnvFilename(%q) == false", bad)
		}
	}

	if matchPathFilters("~/work/code/app/.env.local", []string{"~/work/code/app/.env"}) {
		t.Fatal("exact .env filter must not substring-match .env.local")
	}
	if matchPathFilters("~/work/code/app/.env.local", []string{".env"}) {
		t.Fatal("basename .env filter must not match .env.local")
	}
	if matchPathFilters("~/work/code/app2/.env", []string{"~/work/code/app"}) {
		t.Fatal("directory filter ~/work/code/app must not match ~/work/code/app2/.env")
	}
	if !matchPathFilters("~/work/code/app/.env", []string{"~/work/code/app"}) {
		t.Fatal("directory filter ~/work/code/app should match ~/work/code/app/.env")
	}
	if !matchPathFilters("~/work/code/app/.env", []string{".env"}) {
		t.Fatal("basename filter .env should match ~/work/code/app/.env")
	}
}

func TestBackupZoneRoundTripSeparateOpHashAndSafeRestore(t *testing.T) {
	ctx := testContext(t)
	db, err := Open(DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}

	validData := []byte("VALID_KEY=1\n")
	n1, c1, err := Encrypt(kr.Key, validData, EnvBlobAAD("b-valid", "envfile", "~/.env", 0o600))
	if err != nil {
		t.Fatalf("Encrypt valid: %v", err)
	}
	if err := db.PutSecretBlob(SecretBlob{
		ID:         "b-valid",
		Scope:      "envfile",
		Label:      ".env",
		SourcePath: "~/.env",
		Nonce:      n1,
		Ciphertext: c1,
		Perm:       0o600,
		UpdatedAt:  time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PutSecretBlob valid: %v", err)
	}

	badData := []byte("alias pwn=true\n")
	nBad, cBad, err := Encrypt(kr.Key, badData, EnvBlobAAD("b-bad", "envfile", "~/.bashrc", 0o600))
	if err != nil {
		t.Fatalf("Encrypt bad: %v", err)
	}
	if err := db.PutSecretBlob(SecretBlob{
		ID:         "b-bad",
		Scope:      "envfile",
		Label:      ".bashrc",
		SourcePath: "~/.bashrc",
		Nonce:      nBad,
		Ciphertext: cBad,
		Perm:       0o600,
		UpdatedAt:  time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PutSecretBlob bad: %v", err)
	}

	bkRes, err := BackupIfChanged(ctx, db, kr, BackupOptions{})
	if err != nil {
		t.Fatalf("BackupIfChanged: %v", err)
	}
	if !bkRes.Changed || bkRes.LocalPath == "" {
		t.Fatalf("expected local backup written, got %+v", bkRes)
	}
	if _, ok, _ := db.GetMeta("last_backup_hash"); !ok {
		t.Fatal("expected last_backup_hash set after local backup")
	}
	if _, ok, _ := db.GetMeta("last_op_backup_hash"); ok {
		t.Fatal("expected last_op_backup_hash NOT set when 1Password upload is disabled")
	}

	conn, err := db.SQL().Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	concurrentDB, err := Open(DefaultPath(ctx.WSBaseDir()))
	_, _ = conn.ExecContext(t.Context(), `ROLLBACK`)
	_ = conn.Close()
	if err != nil {
		t.Fatalf("Open already-migrated DB under active writer lock failed: %v", err)
	}
	_ = concurrentDB.Close()

	ctx2 := testContext(t)
	if err := os.MkdirAll(filepath.Join(ctx2.HomeDir, ".env"), 0o755); err != nil {
		t.Fatalf("MkdirAll .env dir: %v", err)
	}
	db2, err := Open(DefaultPath(ctx2.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db2: %v", err)
	}
	defer db2.Close()

	_, _ = RestoreBackup(ctx2, db2, kr, RestoreOptions{
		InputPath:    bkRes.LocalPath,
		WriteFiles:   true,
		AllowReplace: true,
	})
	if _, err := os.Stat(filepath.Join(ctx2.HomeDir, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf("disallowed file ~/.bashrc must never be restored, stat err=%v", err)
	}
	fi, err := os.Stat(filepath.Join(ctx2.HomeDir, ".env"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("existing ~/.env directory must not be clobbered: fi=%v err=%v", fi, err)
	}

	ctx3 := testContext(t)
	db3, err := Open(DefaultPath(ctx3.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open db3: %v", err)
	}
	defer db3.Close()
	res3, err := RestoreBackup(ctx3, db3, kr, RestoreOptions{
		InputPath:    bkRes.LocalPath,
		WriteFiles:   true,
		Paths:        []string{"~/.env"},
		AllowReplace: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup clean home: %v", err)
	}
	if len(res3.FilesWritten) != 1 {
		t.Fatalf("FilesWritten = %v, want 1", res3.FilesWritten)
	}
}

func TestScopedOSKeyringIsolationAndLegacyMigration(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")

	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			binDir := t.TempDir()
			kvDir := t.TempDir()

			secScript := `#!/bin/sh
set -eu
store="$MOCK_KEYRING_DIR"
if [ "${1:-}" = "-i" ]; then
  while IFS= read -r line; do
    set -- $line
    cmd="$1"
    shift
    svc=""
    acct=""
    val=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -U) shift ;;
        -s) svc="$2"; shift 2 ;;
        -a) acct="$2"; shift 2 ;;
        -w) val="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ "$cmd" = "add-generic-password" ]; then
      printf '%s\n' "$val" > "$store/${svc}__${acct}"
    fi
  done
  exit 0
fi
cmd="${1:-}"
shift || true
svc=""
acct=""
show_pw=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    -s) svc="$2"; shift 2 ;;
    -a) acct="$2"; shift 2 ;;
    -w) show_pw=1; shift ;;
    *) shift ;;
  esac
done
file="$store/${svc}__${acct}"
case "$cmd" in
  find-generic-password)
    if [ ! -f "$file" ]; then
      exit 44
    fi
    if [ "$show_pw" = "1" ]; then
      cat "$file"
    fi
    ;;
  delete-generic-password)
    if [ ! -f "$file" ]; then
      printf 'could not be found\n' >&2
      exit 44
    fi
    rm -f "$file"
    ;;
esac
`
			stScript := `#!/bin/sh
set -eu
store="$MOCK_KEYRING_DIR"
cmd="${1:-}"
shift || true
svc=""
acct=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --label=*) shift ;;
    service) svc="$2"; shift 2 ;;
    account) acct="$2"; shift 2 ;;
    *) shift ;;
  esac
done
file="$store/${svc}__${acct}"
case "$cmd" in
  store)
    cat > "$file"
    ;;
  lookup)
    if [ ! -f "$file" ]; then
      exit 1
    fi
    cat "$file"
    ;;
  clear)
    rm -f "$file"
    ;;
esac
`
			if err := os.WriteFile(filepath.Join(binDir, "security"), []byte(secScript), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(binDir, "secret-tool"), []byte(stScript), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MOCK_KEYRING_DIR", kvDir)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")

			prevAllowed := osKeyringAllowed
			prevGOOS := osKeyringGOOS
			osKeyringAllowed = func(*app.Context) bool { return true }
			osKeyringGOOS = func() string { return goos }
			t.Cleanup(func() {
				osKeyringAllowed = prevAllowed
				osKeyringGOOS = prevGOOS
			})

			primaryCtx := testContext(t)
			altCtx := &app.Context{
				HomeDir:  primaryCtx.HomeDir,
				WSHome:   filepath.Join(t.TempDir(), "alt-ws"),
				LocalEnv: map[string]string{},
			}

			primaryKR, err := InitKey(primaryCtx, true)
			if err != nil {
				t.Fatalf("InitKey primary: %v", err)
			}
			primaryDB, err := Open(DefaultPath(primaryCtx.WSBaseDir()))
			if err != nil {
				t.Fatal(err)
			}
			n, ct, err := Encrypt(primaryKR.Key, []byte("SECRET=primary\n"), EnvBlobAAD("b1", "envfile", "~/.env", 0o600))
			if err != nil {
				t.Fatal(err)
			}
			if err := primaryDB.PutSecretBlob(SecretBlob{
				ID: "b1", Scope: "envfile", Label: ".env", SourcePath: "~/.env", Nonce: n, Ciphertext: ct, Perm: 0o600,
			}); err != nil {
				t.Fatal(err)
			}
			_ = primaryDB.Close()

			altKR, err := InitKey(altCtx, true)
			if err != nil {
				t.Fatalf("InitKey alt keyring: %v", err)
			}
			if bytes.Equal(altKR.Key, primaryKR.Key) {
				t.Fatal("expected alternate WS_HOME to get an independent key")
			}

			altReplaceCtx := *altCtx
			altReplaceCtx.Allow.Replace = true
			if _, err := InitKey(&altReplaceCtx, false); err != nil {
				t.Fatalf("InitKey alt replace to keyfile: %v", err)
			}

			reloadedPrimary, err := LoadKey(primaryCtx)
			if err != nil {
				t.Fatalf("LoadKey primary after alt replace: %v", err)
			}
			if !bytes.Equal(reloadedPrimary.Key, primaryKR.Key) {
				t.Fatal("alternate WS_HOME key init/replace corrupted primary DB key")
			}

			legacyKey := bytes.Repeat([]byte{0x5a}, 32)
			legacyHex := hex.EncodeToString(legacyKey)
			if err := os.WriteFile(filepath.Join(kvDir, keyringService+"__"+keyringAccount), []byte(legacyHex+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			migratedCtx := &app.Context{
				HomeDir:  primaryCtx.HomeDir,
				WSHome:   filepath.Join(t.TempDir(), "legacy-blobs-ws"),
				LocalEnv: map[string]string{},
			}
			migDB, err := Open(DefaultPath(migratedCtx.WSBaseDir()))
			if err != nil {
				t.Fatal(err)
			}
			mn, mct, err := Encrypt(legacyKey, []byte("LEGACY=ok\n"), EnvBlobAAD("mb1", "envfile", "~/.env", 0o600))
			if err != nil {
				t.Fatal(err)
			}
			if err := migDB.PutSecretBlob(SecretBlob{
				ID: "mb1", Scope: "envfile", Label: ".env", SourcePath: "~/.env", Nonce: mn, Ciphertext: mct, Perm: 0o600,
			}); err != nil {
				t.Fatal(err)
			}
			_ = migDB.Close()

			loadedLegacy, err := LoadKey(migratedCtx)
			if err != nil {
				t.Fatalf("LoadKey legacy migration: %v", err)
			}
			if !bytes.Equal(loadedLegacy.Key, legacyKey) {
				t.Fatal("expected transparent adoption of legacy key matching store blobs")
			}
			scopedFile := filepath.Join(kvDir, keyringService+"__"+keyringAccountForStore(migratedCtx.WSBaseDir()))
			if _, err := os.Stat(scopedFile); err != nil {
				t.Fatalf("expected legacy key migrated to scoped keyring entry %s: %v", scopedFile, err)
			}

			freshAltCtx := &app.Context{
				HomeDir:  primaryCtx.HomeDir,
				WSHome:   filepath.Join(t.TempDir(), "fresh-empty-alt"),
				LocalEnv: map[string]string{},
			}
			freshLoaded, err := LoadKey(freshAltCtx)
			if err != nil {
				t.Fatalf("LoadKey fresh empty alt: %v", err)
			}
			if freshLoaded.HasKey() {
				t.Fatal("fresh empty alternate WS_HOME must not adopt unrelated legacy keyring entry")
			}

			defaultLegacyCtx := testContext(t)
			defaultLoaded, err := LoadKey(defaultLegacyCtx)
			if err != nil {
				t.Fatalf("LoadKey default WS_HOME legacy fallback: %v", err)
			}
			if !bytes.Equal(defaultLoaded.Key, legacyKey) {
				t.Fatal("expected existing default WS_HOME user key in legacy entry to keep working transparently")
			}
		})
	}
}

func TestBackupVerifiesTargetArchiveHashPerDestination(t *testing.T) {
	t.Setenv("WS_DB_KEY", "")
	ctx := testContext(t)
	kr, err := InitKey(ctx, false)
	if err != nil {
		t.Fatalf("InitKey: %v", err)
	}
	db, err := Open(DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.SQL().Exec(
		`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
		 VALUES('K1', 'personal', 'dotenv', '~/.env', 1, 50, 1, 'v1', 'fp:1', 0, 1000)`,
	); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	altPath := filepath.Join(t.TempDir(), "alt-backup.enc")
	rAlt1, err := BackupIfChanged(ctx, db, kr, BackupOptions{OutputPath: altPath, Now: now})
	if err != nil || !rAlt1.Changed || rAlt1.Skipped {
		t.Fatalf("initial alt backup failed: %+v err=%v", rAlt1, err)
	}
	staleAltBytes, err := os.ReadFile(altPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.SQL().Exec(
		`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
		 VALUES('K2', 'personal', 'dotenv', '~/.env', 2, 50, 1, 'v2', 'fp:2', 0, 1001)`,
	); err != nil {
		t.Fatal(err)
	}

	rDef, err := BackupIfChanged(ctx, db, kr, BackupOptions{Now: now.Add(time.Minute)})
	if err != nil || !rDef.Changed || rDef.Skipped {
		t.Fatalf("default backup at H2 failed: %+v err=%v", rDef, err)
	}
	if rDef.Hash == rAlt1.Hash {
		t.Fatal("expected DB hash to change after second insert")
	}

	rAlt2, err := BackupIfChanged(ctx, db, kr, BackupOptions{OutputPath: altPath, Now: now.Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("alt backup at H2 failed: %v", err)
	}
	if rAlt2.Skipped || !rAlt2.Changed {
		t.Fatalf("expected stale alternate archive to be rewritten, got %+v", rAlt2)
	}
	if !targetArchiveMatchesHash(altPath, kr, rDef.Hash) {
		t.Fatalf("expected alternate archive %s to match current hash %s", altPath, rDef.Hash)
	}

	rAlt3, err := BackupIfChanged(ctx, db, kr, BackupOptions{OutputPath: altPath, Now: now.Add(3 * time.Minute)})
	if err != nil || !rAlt3.Skipped || rAlt3.Changed {
		t.Fatalf("expected up-to-date alternate archive to skip, got %+v err=%v", rAlt3, err)
	}

	if err := os.WriteFile(rDef.LocalPath, staleAltBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	uploadedPath := filepath.Join(t.TempDir(), "uploaded.enc")
	mockOp := `#!/bin/sh
set -eu
case "${1:-}" in
  whoami) exit 0 ;;
  document)
    case "${2:-}" in
      list) printf '[]\n' ;;
      create) cp "$3" "$MOCK_UPLOAD_DST" ;;
      edit) cp "$4" "$MOCK_UPLOAD_DST" ;;
    esac
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "op"), []byte(mockOp), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_UPLOAD_DST", uploadedPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")

	rOP, err := BackupIfChanged(ctx, db, kr, BackupOptions{OnePassword: true, Now: now.Add(4 * time.Minute)})
	if err != nil {
		t.Fatalf("BackupIfChanged with stale default archive + OnePassword: %v", err)
	}
	if !rOP.Changed || !rOP.OnePassword {
		t.Fatalf("expected stale default archive to be rewritten and uploaded, got %+v", rOP)
	}
	if !targetArchiveMatchesHash(uploadedPath, kr, rDef.Hash) {
		t.Fatalf("expected uploaded archive to contain current hash %s, not stale H1", rDef.Hash)
	}
}
