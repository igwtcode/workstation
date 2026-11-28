package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/opcli"
)

const osKeyringTimeout = 3 * time.Second

func execOutputWithTimeout(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), osKeyringTimeout)
	defer cancel()
	return exec.CommandContext(ctx, bin, args...).Output()
}

func execCombinedWithTimeout(stdin string, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), osKeyringTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func execRunWithTimeout(stdin string, bin string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), osKeyringTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Run()
}

type KeySource string

const (
	KeySourceEnv        KeySource = "env"
	KeySourceKeychain   KeySource = "keychain"
	KeySourceSecretTool KeySource = "secret-tool"
	KeySourceKeyFile    KeySource = "keyfile"
	KeySourceNone       KeySource = "none"
)

const (
	keyringService = "ws-db-key"
	keyringAccount = "ws"
	opKeyDocTitle  = "ws-db.key"
)

var (
	osKeyringAllowed = isRealUserHome
	osKeyringGOOS    = func() string { return runtime.GOOS }
)

func evalExistingStorePath(path string) string {
	clean := filepath.Clean(path)
	var suffix []string
	cur := clean
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}

func resolveStoreHome(wsHome string) string {
	trimmed := strings.TrimSpace(wsHome)
	if trimmed == "" {
		return ""
	}
	abs, err := filepath.Abs(filepath.Clean(trimmed))
	if err != nil {
		return filepath.Clean(trimmed)
	}
	return evalExistingStorePath(abs)
}

func keyringAccountForStore(wsHome string) string {
	resolved := resolveStoreHome(wsHome)
	if resolved == "" {
		return keyringAccount
	}
	sum := sha256.Sum256([]byte(resolved))
	return keyringAccount + "-" + hex.EncodeToString(sum[:8])
}

func isDefaultWSHome(ctx *app.Context) bool {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" || strings.TrimSpace(ctx.WSBaseDir()) == "" {
		return false
	}
	return resolveStoreHome(ctx.WSBaseDir()) == resolveStoreHome(filepath.Join(ctx.HomeDir, ".ws"))
}

func isSafeKeyringToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

type Keyring struct {
	Key         []byte    `json:"-"`
	Source      KeySource `json:"source"`
	KeyFilePath string    `json:"keyFilePath,omitempty"`
}

func DefaultKeyPath(wsHome string) string {
	return filepath.Join(wsHome, "db.key")
}

func (k *Keyring) HasKey() bool {
	return k != nil && len(k.Key) == 32
}

func LoadKey(ctx *app.Context) (*Keyring, error) {
	wsBase := ""
	keyPath := ""
	if ctx != nil && ctx.WSBaseDir() != "" {
		wsBase = ctx.WSBaseDir()
		keyPath = DefaultKeyPath(wsBase)
	}

	if envRaw := strings.TrimSpace(os.Getenv("WS_DB_KEY")); envRaw != "" {
		key, err := decodeKeyStrict(envRaw)
		if err != nil {
			return nil, fmt.Errorf("WS_DB_KEY: %w", err)
		}
		return &Keyring{
			Key:         key,
			Source:      KeySourceEnv,
			KeyFilePath: keyPath,
		}, nil
	}

	scopedAccount := keyringAccountForStore(wsBase)
	if osKeyringAllowed(ctx) {
		if raw, src, found := lookupOSKeyring(scopedAccount); found {
			key, kerr := decodeKeyStrict(raw)
			if kerr != nil {
				return nil, fmt.Errorf("%s database key: %w", src, kerr)
			}
			return &Keyring{
				Key:         key,
				Source:      src,
				KeyFilePath: keyPath,
			}, nil
		}
	}

	if keyPath != "" {
		fi, err := os.Lstat(keyPath)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("refusing symlink keyfile %s", keyPath)
			}
			if fi.Mode().Perm()&0o077 != 0 {
				return nil, fmt.Errorf("keyfile %s has overly permissive mode %04o (expected 0600)", keyPath, fi.Mode().Perm())
			}
			raw, err := os.ReadFile(keyPath)
			if err != nil {
				return nil, err
			}
			trimmed := strings.TrimSpace(string(raw))
			key, kerr := decodeKeyStrict(trimmed)
			if kerr != nil {
				return nil, fmt.Errorf("keyfile %s: %w", keyPath, kerr)
			}
			return &Keyring{
				Key:         key,
				Source:      KeySourceKeyFile,
				KeyFilePath: keyPath,
			}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	if osKeyringAllowed(ctx) && scopedAccount != keyringAccount {
		legacyKey, src, matches, err := legacyKeyMatchesStore(ctx)
		if err != nil {
			return nil, err
		}
		if matches {
			_ = storeOSKeyring(scopedAccount, hex.EncodeToString(legacyKey))
			return &Keyring{
				Key:         legacyKey,
				Source:      src,
				KeyFilePath: keyPath,
			}, nil
		}
	}

	return &Keyring{
		Source:      KeySourceNone,
		KeyFilePath: keyPath,
	}, nil
}

func storeBlobsDecryptWithKey(wsHome string, key []byte) (hasBlobs bool, allDecrypted bool, err error) {
	if strings.TrimSpace(wsHome) == "" {
		return false, false, nil
	}
	dbPath := DefaultPath(wsHome)
	if _, statErr := os.Stat(dbPath); errors.Is(statErr, os.ErrNotExist) {
		return false, false, nil
	} else if statErr != nil {
		return false, false, statErr
	}
	db, err := Open(dbPath)
	if err != nil {
		return false, false, err
	}
	defer db.Close()

	blobs, err := db.ListSecretBlobs("")
	if err != nil {
		return false, false, err
	}
	if len(blobs) == 0 {
		return false, false, nil
	}
	kr := &Keyring{Key: key}
	for _, b := range blobs {
		if _, derr := DecryptSecretBlob(kr, b); derr != nil {
			return true, false, nil
		}
	}
	return true, true, nil
}

func legacyKeyMatchesStore(ctx *app.Context) ([]byte, KeySource, bool, error) {
	raw, src, found := lookupOSKeyring(keyringAccount)
	if !found {
		return nil, KeySourceNone, false, nil
	}
	key, err := decodeKeyStrict(raw)
	if err != nil {
		return nil, src, false, fmt.Errorf("%s database key: %w", src, err)
	}
	hasBlobs, allDecrypted, berr := storeBlobsDecryptWithKey(ctx.WSBaseDir(), key)
	if berr != nil {
		return nil, src, false, nil
	}
	if hasBlobs {
		return key, src, allDecrypted, nil
	}
	if isDefaultWSHome(ctx) {
		return key, src, true, nil
	}
	return nil, src, false, nil
}

func checkKeyReplacementAllowed(ctx *app.Context, keyPath string) (keyringHadEntry bool, err error) {
	existingKey := false
	if keyPath != "" {
		if fi, lerr := os.Lstat(keyPath); lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return false, fmt.Errorf("refusing symlink keyfile %s", keyPath)
			}
			existingKey = true
			if !ctx.Allow.Replace {
				return false, fmt.Errorf("database key already exists at %s: requires --allow replace", ctx.CollapseHome(keyPath))
			}
		} else if !errors.Is(lerr, os.ErrNotExist) {
			return false, lerr
		}
	}

	scopedAccount := keyringAccountForStore(ctx.WSBaseDir())
	keyringHadEntry = osKeyringAllowed(ctx) && hasOSKeyring(scopedAccount)
	if keyringHadEntry {
		existingKey = true
		if !ctx.Allow.Replace {
			return keyringHadEntry, fmt.Errorf("database key already exists in OS keyring: requires --allow replace")
		}
	} else if !existingKey && osKeyringAllowed(ctx) && scopedAccount != keyringAccount {
		if _, _, matches, lerr := legacyKeyMatchesStore(ctx); lerr == nil && matches {
			existingKey = true
			if !ctx.Allow.Replace {
				return false, fmt.Errorf("database key already exists in OS keyring: requires --allow replace")
			}
		}
	}

	if existingKey && ctx.WSBaseDir() != "" {
		dbPath := DefaultPath(ctx.WSBaseDir())
		if _, statErr := os.Stat(dbPath); statErr == nil {
			db, derr := Open(dbPath)
			if derr != nil {
				return keyringHadEntry, fmt.Errorf("check existing ws.db blobs before replacing key: %w", derr)
			}
			var blobCount int
			qerr := db.SQL().QueryRow(`SELECT COUNT(*) FROM secret_blobs`).Scan(&blobCount)
			_ = db.Close()
			if qerr != nil {
				return keyringHadEntry, fmt.Errorf("count existing secret_blobs before replacing key: %w", qerr)
			}
			if blobCount > 0 {
				return keyringHadEntry, fmt.Errorf("refusing to replace database key while %d encrypted secret blob(s) exist in ws.db; back up or remove existing blobs first", blobCount)
			}
		}
	}
	return keyringHadEntry, nil
}

func applyKeyStorage(ctx *app.Context, keyPath, hexKey string, saveToOSKeyring, keyringHadEntry bool) (KeySource, error) {
	scopedAccount := keyringAccountForStore(ctx.WSBaseDir())
	if saveToOSKeyring && osKeyringAllowed(ctx) {
		if err := storeOSKeyring(scopedAccount, hexKey); err != nil {
			return KeySourceNone, fmt.Errorf("store database key in OS keyring: %w", err)
		}
		if keyPath != "" {
			if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return KeySourceNone, fmt.Errorf("remove plaintext keyfile after storing in OS keyring: %w", err)
			}
		}
		if osKeyringGOOS() == "darwin" {
			return KeySourceKeychain, nil
		}
		return KeySourceSecretTool, nil
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return KeySourceNone, err
	}
	if err := app.WriteAtomicFile(keyPath, []byte(hexKey+"\n"), 0o600); err != nil {
		return KeySourceNone, err
	}
	if !saveToOSKeyring && keyringHadEntry {
		if err := deleteOSKeyring(scopedAccount); err != nil {
			return KeySourceNone, fmt.Errorf("remove stale OS keyring entry: %w", err)
		}
	}
	return KeySourceKeyFile, nil
}

func InitKey(ctx *app.Context, saveToOSKeyring bool) (*Keyring, error) {
	if ctx == nil || ctx.WSBaseDir() == "" {
		return nil, fmt.Errorf("workstation home directory is required to initialize database key")
	}
	if ctx.DryRun {
		return nil, fmt.Errorf("db key init refused in dry-run mode")
	}

	keyPath := DefaultKeyPath(ctx.WSBaseDir())
	keyringHadEntry, err := checkKeyReplacementAllowed(ctx, keyPath)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}
	hexKey := hex.EncodeToString(buf)

	src, err := applyKeyStorage(ctx, keyPath, hexKey, saveToOSKeyring, keyringHadEntry)
	if err != nil {
		return nil, err
	}
	return &Keyring{
		Key:         buf,
		Source:      src,
		KeyFilePath: keyPath,
	}, nil
}

func isHexKey(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func lookupOSKeyring(account string) (string, KeySource, bool) {
	if !isSafeKeyringToken(account) {
		return "", KeySourceNone, false
	}
	switch osKeyringGOOS() {
	case "darwin":
		secBin, err := exec.LookPath("security")
		if err != nil {
			return "", KeySourceKeychain, false
		}
		out, err := execOutputWithTimeout(secBin, "find-generic-password", "-s", keyringService, "-a", account, "-w")
		if err != nil {
			return "", KeySourceKeychain, false
		}
		raw := strings.TrimSpace(string(out))
		if raw == "" {
			return "", KeySourceKeychain, false
		}
		return raw, KeySourceKeychain, true
	case "linux":
		stBin, err := exec.LookPath("secret-tool")
		if err != nil {
			return "", KeySourceSecretTool, false
		}
		out, err := execOutputWithTimeout(stBin, "lookup", "service", keyringService, "account", account)
		if err != nil {
			return "", KeySourceSecretTool, false
		}
		raw := strings.TrimSpace(string(out))
		if raw == "" {
			return "", KeySourceSecretTool, false
		}
		return raw, KeySourceSecretTool, true
	default:
		return "", KeySourceNone, false
	}
}

func hasOSKeyring(account string) bool {
	if !isSafeKeyringToken(account) {
		return false
	}
	switch osKeyringGOOS() {
	case "darwin":
		secBin, err := exec.LookPath("security")
		if err != nil {
			return false
		}
		return execRunWithTimeout("", secBin, "find-generic-password", "-s", keyringService, "-a", account) == nil
	case "linux":
		stBin, err := exec.LookPath("secret-tool")
		if err != nil {
			return false
		}
		out, err := execOutputWithTimeout(stBin, "lookup", "service", keyringService, "account", account)
		return err == nil && strings.TrimSpace(string(out)) != ""
	default:
		return false
	}
}

func storeOSKeyring(account, hexKey string) error {
	if !isHexKey(hexKey) {
		return fmt.Errorf("invalid hex key material for OS keyring")
	}
	if !isSafeKeyringToken(account) {
		return fmt.Errorf("invalid keyring account %q", account)
	}
	switch osKeyringGOOS() {
	case "darwin":
		secBin, err := exec.LookPath("security")
		if err != nil {
			return err
		}
		stdin := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", keyringService, account, hexKey)
		out, err := execCombinedWithTimeout(stdin, secBin, "-i")
		sanitizedOut := strings.ReplaceAll(strings.TrimSpace(string(out)), hexKey, "[REDACTED]")
		if err != nil {
			return fmt.Errorf("security -i add-generic-password failed (%v): %s", err, sanitizedOut)
		}
		if strings.Contains(strings.ToLower(string(out)), "security:") {
			return fmt.Errorf("security -i add-generic-password failed: %s", sanitizedOut)
		}
		return nil
	case "linux":
		stBin, err := exec.LookPath("secret-tool")
		if err != nil {
			return err
		}
		return execRunWithTimeout(hexKey, stBin, "store", "--label=ws database key", "service", keyringService, "account", account)
	default:
		return fmt.Errorf("OS keyring unsupported on %s", osKeyringGOOS())
	}
}

func deleteOSKeyring(account string) error {
	if !isSafeKeyringToken(account) {
		return fmt.Errorf("invalid keyring account %q", account)
	}
	switch osKeyringGOOS() {
	case "darwin":
		secBin, err := exec.LookPath("security")
		if err != nil {
			return nil
		}
		out, err := execCombinedWithTimeout("", secBin, "delete-generic-password", "-s", keyringService, "-a", account)
		if err != nil {
			lower := strings.ToLower(string(out))
			if strings.Contains(lower, "could not be found") {
				return nil
			}
			return fmt.Errorf("delete keychain item: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "linux":
		stBin, err := exec.LookPath("secret-tool")
		if err != nil {
			return nil
		}
		return execRunWithTimeout("", stBin, "clear", "service", keyringService, "account", account)
	default:
		return nil
	}
}

func decodeKeyStrict(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty master key")
	}
	if len(trimmed) == 64 {
		if b, err := hex.DecodeString(trimmed); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	for _, dec := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := dec.DecodeString(trimmed); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid master key format (expected 64 hex chars or base64-encoded 32 bytes)")
}

func isRealUserHome(ctx *app.Context) bool {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" {
		return false
	}
	realHome, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(realHome) == "" {
		return false
	}
	cleanCtx, err1 := filepath.Abs(filepath.Clean(ctx.HomeDir))
	cleanReal, err2 := filepath.Abs(filepath.Clean(realHome))
	if err1 != nil || err2 != nil {
		return false
	}
	return cleanCtx == cleanReal
}

func Encrypt(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	if len(key) != 32 {
		return nil, nil, fmt.Errorf("encryption key must be 32 bytes (got %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = gcm.Seal(nil, nonce, plaintext, aad)
	return nonce, ciphertext, nil
}

func Decrypt(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("decryption key must be 32 bytes (got %d)", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid nonce length %d", len(nonce))
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed (wrong key or tampered payload): %w", err)
	}
	return plain, nil
}

func EnvBlobAAD(id, scope, sourcePath string, perm uint32) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%o", id, scope, sourcePath, perm))
}

func DecryptSecretBlob(kr *Keyring, b SecretBlob) ([]byte, error) {
	if !kr.HasKey() {
		return nil, fmt.Errorf("database key not loaded")
	}
	if b.Scope == "envfile" {
		plain, err := Decrypt(kr.Key, b.Nonce, b.Ciphertext, EnvBlobAAD(b.ID, b.Scope, b.SourcePath, b.Perm))
		if err == nil {
			return plain, nil
		}
	}
	return Decrypt(kr.Key, b.Nonce, b.Ciphertext, []byte(b.ID))
}

func Fingerprint(key []byte, value string) string {
	if value == "" {
		return ""
	}
	var sum [32]byte
	if len(key) == 32 {
		subMac := hmac.New(sha256.New, key)
		subMac.Write([]byte("ws-fp-v1"))
		fpKey := subMac.Sum(nil)

		mac := hmac.New(sha256.New, fpKey)
		mac.Write([]byte(value))
		copy(sum[:], mac.Sum(nil))
		return "fp:" + hex.EncodeToString(sum[:8])
	}
	sum = sha256.Sum256([]byte("ws-fp-unkeyed-v1:" + value))
	return "fpu:" + hex.EncodeToString(sum[:8])
}

func MaskValue(val string, isSecret bool) string {
	if val == "" {
		return ""
	}
	if !isSecret {
		r := []rune(val)
		if len(r) > 64 {
			return string(r[:61]) + "…"
		}
		return val
	}
	r := []rune(val)
	if len(r) < 20 {
		return "••••"
	}
	return string(r[:4]) + "…••••"
}

func BackupKeyTo1Password(ctx *app.Context, kr *Keyring) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if ctx.DryRun {
		return fmt.Errorf("key backup refused in dry-run mode")
	}
	if !kr.HasKey() {
		return fmt.Errorf("no database key loaded; run ws db key init first")
	}

	opBin, err := opcli.RequireSignedIn(context.Background())
	if err != nil {
		return err
	}
	vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
	return opcli.PutDocumentBytes(context.Background(), opBin, vault, opKeyDocTitle, []byte(hex.EncodeToString(kr.Key)+"\n"))
}

func RestoreKeyFrom1Password(ctx *app.Context, saveToOSKeyring bool) (*Keyring, error) {
	if ctx == nil || ctx.WSBaseDir() == "" {
		return nil, fmt.Errorf("workstation home directory is required")
	}
	if ctx.DryRun {
		return nil, fmt.Errorf("key restore refused in dry-run mode")
	}
	keyPath := DefaultKeyPath(ctx.WSBaseDir())
	keyringHadEntry, err := checkKeyReplacementAllowed(ctx, keyPath)
	if err != nil {
		return nil, err
	}

	opBin, err := opcli.RequireSignedIn(context.Background())
	if err != nil {
		return nil, err
	}
	vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
	raw, err := opcli.GetDocumentBytes(context.Background(), opBin, vault, opKeyDocTitle)
	if err != nil {
		return nil, err
	}
	key, err := decodeKeyStrict(string(raw))
	if err != nil {
		return nil, fmt.Errorf("1Password key document %q: %w", opKeyDocTitle, err)
	}
	hexKey := hex.EncodeToString(key)

	src, err := applyKeyStorage(ctx, keyPath, hexKey, saveToOSKeyring, keyringHadEntry)
	if err != nil {
		return nil, err
	}
	return &Keyring{
		Key:         key,
		Source:      src,
		KeyFilePath: keyPath,
	}, nil
}
