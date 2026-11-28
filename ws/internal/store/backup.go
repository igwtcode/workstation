package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/opcli"
)

const (
	backupFormatVersion = "ws-backup-v1"
	backupAAD           = "ws-backup-v1"
	opBackupDocTitle    = "ws-db-backup.enc"
	maxRetainedBackups  = 10
)

type BackupOptions struct {
	Force           bool
	OnePassword     bool
	AutoOnePassword bool
	OutputPath      string
	Now             time.Time
}

type BackupResult struct {
	Changed     bool           `json:"changed"`
	Skipped     bool           `json:"skipped"`
	Hash        string         `json:"hash"`
	LocalPath   string         `json:"localPath,omitempty"`
	OnePassword bool           `json:"onePassword,omitempty"`
	Counts      map[string]int `json:"counts"`
	CreatedAt   int64          `json:"createdAt"`
	DryRun      bool           `json:"dryRun,omitempty"`
}

type RestoreOptions struct {
	InputPath       string
	FromOnePassword bool
	Scopes          []string
	Vars            []string
	Paths           []string
	WriteFiles      bool
	AllowReplace    bool
}

type RestoreResult struct {
	Source         string         `json:"source"`
	Hash           string         `json:"hash"`
	TablesRestored map[string]int `json:"tablesRestored"`
	FilesWritten   []string       `json:"filesWritten,omitempty"`
	SkippedFiles   []string       `json:"skippedFiles,omitempty"`
	DryRun         bool           `json:"dryRun,omitempty"`
}

type encryptedEnvelope struct {
	Format     string `json:"format"`
	CreatedAt  int64  `json:"createdAt"`
	Hash       string `json:"hash"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type snapshotPayload struct {
	Format     string           `json:"format"`
	CreatedAt  int64            `json:"createdAt"`
	Hash       string           `json:"hash"`
	EnvEntries []snapEnvEntry   `json:"envEntries,omitempty"`
	Secrets    []snapSecretBlob `json:"secrets,omitempty"`
	Repos      []snapRepo       `json:"repos,omitempty"`
	FleetNodes []snapFleetNode  `json:"fleetNodes,omitempty"`
}

type snapEnvEntry struct {
	VarKey      string `json:"varKey"`
	Zone        string `json:"zone"`
	SourceKind  string `json:"sourceKind"`
	SourcePath  string `json:"sourcePath"`
	LineNum     int    `json:"lineNum"`
	Precedence  int    `json:"precedence"`
	IsActive    int    `json:"isActive"`
	MaskedValue string `json:"maskedValue"`
	Fingerprint string `json:"fingerprint"`
	IsSecret    int    `json:"isSecret"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type snapSecretBlob struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	SourcePath  string `json:"sourcePath"`
	Nonce       []byte `json:"nonce"`
	Ciphertext  []byte `json:"ciphertext"`
	Fingerprint string `json:"fingerprint"`
	Perm        uint32 `json:"perm"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type snapRepo struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Zone                string `json:"zone"`
	Forge               string `json:"forge"`
	RemoteURL           string `json:"remoteUrl"`
	LocalPath           string `json:"localPath"`
	Branch              string `json:"branch"`
	Dirty               int    `json:"dirty"`
	Ahead               int    `json:"ahead"`
	Behind              int    `json:"behind"`
	UntrackedLocalsJSON string `json:"untrackedLocalsJson"`
	ScannedAt           int64  `json:"scannedAt"`
}

type snapFleetNode struct {
	Name              string `json:"name"`
	Provider          string `json:"provider"`
	TailscaleIP       string `json:"tailscaleIp"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Tags              string `json:"tags"`
	Preset            string `json:"preset"`
	NoZones           int    `json:"noZones"`
	ExcludePathsJSON  string `json:"excludePathsJson"`
	SelectedReposJSON string `json:"selectedReposJson"`
	AIAccountsJSON    string `json:"aiAccountsJson"`
	ChecklistJSON     string `json:"checklistJson"`
	LastSyncedAt      int64  `json:"lastSyncedAt"`
}

func DefaultBackupPath(wsHome string) string {
	return filepath.Join(wsHome, "backups", "ws-backup-latest.enc")
}

func destBackupMetaKey(targetPath string) string {
	resolved := resolveStoreHome(targetPath)
	sum := sha256.Sum256([]byte(resolved))
	return "last_backup_hash:" + hex.EncodeToString(sum[:8])
}

func targetArchiveMatchesHash(targetPath string, kr *Keyring, expectedHash string) bool {
	if strings.TrimSpace(targetPath) == "" || strings.TrimSpace(expectedHash) == "" {
		return false
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return false
	}
	var env encryptedEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	if env.Format != backupFormatVersion || env.Hash != expectedHash || len(env.Nonce) == 0 || len(env.Ciphertext) == 0 {
		return false
	}
	if kr.HasKey() {
		plain, err := Decrypt(kr.Key, env.Nonce, env.Ciphertext, []byte(backupAAD))
		if err != nil {
			return false
		}
		var snap snapshotPayload
		if err := json.Unmarshal(plain, &snap); err != nil {
			return false
		}
		if snap.Format != backupFormatVersion || snap.Hash != expectedHash {
			return false
		}
	}
	return true
}

func BackupIfChanged(ctx *app.Context, d *DB, kr *Keyring, opts BackupOptions) (*BackupResult, error) {
	if ctx == nil || d == nil {
		return nil, fmt.Errorf("context and open database are required")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	lastOPHash, _, err := d.GetMeta("last_op_backup_hash")
	if err != nil {
		return nil, err
	}

	curHash, snap, counts, err := d.exportSnapshotAtomic(now.Unix())
	if err != nil {
		return nil, err
	}

	defaultTarget := DefaultBackupPath(ctx.WSBaseDir())
	targetPath := opts.OutputPath
	if targetPath == "" {
		targetPath = defaultTarget
	}
	isDefaultDest := opts.OutputPath == "" || filepath.Clean(targetPath) == filepath.Clean(defaultTarget)

	localUpToDate := false
	if !opts.Force && targetArchiveMatchesHash(targetPath, kr, curHash) {
		localUpToDate = true
	}
	needOP := opts.OnePassword || (opts.AutoOnePassword && lastOPHash != curHash)
	if localUpToDate && (!needOP || (opts.AutoOnePassword && lastOPHash == curHash)) {
		return &BackupResult{
			Changed:   false,
			Skipped:   true,
			Hash:      curHash,
			Counts:    counts,
			CreatedAt: now.Unix(),
			DryRun:    ctx.DryRun,
		}, nil
	}

	if ctx.DryRun {
		return &BackupResult{
			Changed:   true,
			Skipped:   false,
			Hash:      curHash,
			LocalPath: targetPath,
			Counts:    counts,
			CreatedAt: now.Unix(),
			DryRun:    true,
		}, nil
	}

	if !kr.HasKey() {
		return nil, fmt.Errorf("database key not loaded; run `ws db key init` or set WS_DB_KEY")
	}

	if !localUpToDate {
		plain, err := json.Marshal(snap)
		if err != nil {
			return nil, err
		}
		nonce, ciphertext, err := Encrypt(kr.Key, plain, []byte(backupAAD))
		if err != nil {
			return nil, err
		}
		envBytes, err := json.MarshalIndent(encryptedEnvelope{
			Format:     backupFormatVersion,
			CreatedAt:  now.Unix(),
			Hash:       curHash,
			Nonce:      nonce,
			Ciphertext: ciphertext,
		}, "", "  ")
		if err != nil {
			return nil, err
		}
		envBytes = append(envBytes, '\n')

		backupDir := filepath.Dir(targetPath)
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return nil, err
		}
		if err := app.WriteAtomicFile(targetPath, envBytes, 0o600); err != nil {
			return nil, err
		}

		if opts.OutputPath == "" {
			stampName := fmt.Sprintf("ws-backup-%s.enc", now.Format("20060102-150405"))
			if err := app.WriteAtomicFile(filepath.Join(backupDir, stampName), envBytes, 0o600); err != nil {
				return nil, fmt.Errorf("write stamped backup %s: %w", stampName, err)
			}
			pruneOldBackups(backupDir, maxRetainedBackups)
		}
		if err := d.SetMeta(destBackupMetaKey(targetPath), curHash); err != nil {
			return nil, err
		}
		if isDefaultDest {
			if err := d.SetMeta("last_backup_hash", curHash); err != nil {
				return nil, err
			}
			if err := d.SetMeta("last_backup_at", strconv.FormatInt(now.Unix(), 10)); err != nil {
				return nil, err
			}
		}
	}

	uploadedOP := false
	if opts.OnePassword {
		opBin, err := opcli.RequireSignedIn(context.Background())
		if err != nil {
			return nil, err
		}
		vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
		if err := opcli.PutDocument(context.Background(), opBin, vault, opBackupDocTitle, targetPath); err != nil {
			return nil, err
		}
		uploadedOP = true
	} else if opts.AutoOnePassword {
		if opBin, ok := opcli.IsSignedIn(context.Background()); ok {
			vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
			if err := opcli.PutDocument(context.Background(), opBin, vault, opBackupDocTitle, targetPath); err == nil {
				uploadedOP = true
			}
		}
	}
	if uploadedOP {
		if err := d.SetMeta("last_op_backup_hash", curHash); err != nil {
			return nil, err
		}
	}

	return &BackupResult{
		Changed:     !localUpToDate || uploadedOP,
		Skipped:     false,
		Hash:        curHash,
		LocalPath:   targetPath,
		OnePassword: uploadedOP,
		Counts:      counts,
		CreatedAt:   now.Unix(),
	}, nil
}

func RestoreBackup(ctx *app.Context, d *DB, kr *Keyring, opts RestoreOptions) (*RestoreResult, error) {
	if ctx == nil || d == nil {
		return nil, fmt.Errorf("context and open database are required")
	}
	if !kr.HasKey() {
		return nil, fmt.Errorf("database key not loaded; restore key or set WS_DB_KEY")
	}

	var rawEnv []byte
	sourceDesc := opts.InputPath

	if opts.FromOnePassword {
		opBin, err := opcli.RequireSignedIn(context.Background())
		if err != nil {
			return nil, err
		}
		tmpDir, err := os.MkdirTemp("", "ws-restore-op-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)
		tmpFile := filepath.Join(tmpDir, opBackupDocTitle)
		vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
		if err := opcli.GetDocument(context.Background(), opBin, vault, opBackupDocTitle, tmpFile); err != nil {
			return nil, err
		}
		rawEnv, err = os.ReadFile(tmpFile)
		if err != nil {
			return nil, err
		}
		sourceDesc = "1password:" + opBackupDocTitle
	} else {
		inPath := opts.InputPath
		if inPath == "" {
			inPath = DefaultBackupPath(ctx.WSBaseDir())
		}
		sourceDesc = inPath
		var err error
		rawEnv, err = os.ReadFile(inPath)
		if err != nil {
			return nil, err
		}
	}

	var env encryptedEnvelope
	if err := json.Unmarshal(rawEnv, &env); err != nil {
		return nil, fmt.Errorf("parse encrypted backup: %w", err)
	}
	if env.Format != backupFormatVersion {
		return nil, fmt.Errorf("unsupported backup format %q", env.Format)
	}
	plain, err := Decrypt(kr.Key, env.Nonce, env.Ciphertext, []byte(backupAAD))
	if err != nil {
		return nil, err
	}
	var snap snapshotPayload
	if err := json.Unmarshal(plain, &snap); err != nil {
		return nil, fmt.Errorf("decode backup snapshot: %w", err)
	}

	scopeSet := normalizeScopeSet(opts.Scopes)
	wantScope := func(name string) bool {
		return len(scopeSet) == 0 || scopeSet[name]
	}

	varRestored := map[string]int{}
	res := &RestoreResult{
		Source:         sourceDesc,
		Hash:           snap.Hash,
		TablesRestored: varRestored,
		DryRun:         ctx.DryRun,
	}

	var matchedEnv []snapEnvEntry
	if wantScope("env") {
		for _, e := range snap.EnvEntries {
			if matchFilters(e.VarKey, e.SourcePath, opts.Vars, opts.Paths) {
				matchedEnv = append(matchedEnv, e)
			}
		}
		varRestored["env_entries"] = len(matchedEnv)
	}

	var matchedSecrets []snapSecretBlob
	if wantScope("secrets") {
		for _, s := range snap.Secrets {
			if matchFilters(s.Label, s.SourcePath, opts.Vars, opts.Paths) {
				matchedSecrets = append(matchedSecrets, s)
			}
		}
		varRestored["secret_blobs"] = len(matchedSecrets)
	}

	var matchedRepos []snapRepo
	if wantScope("repos") {
		for _, r := range snap.Repos {
			if len(opts.Paths) == 0 || matchPathFilters(r.LocalPath, opts.Paths) || matchPathFilters(r.Name, opts.Paths) {
				matchedRepos = append(matchedRepos, r)
			}
		}
		varRestored["repos"] = len(matchedRepos)
	}

	var matchedFleet []snapFleetNode
	if wantScope("fleet") {
		matchedFleet = snap.FleetNodes
		varRestored["fleet_nodes"] = len(matchedFleet)
	}

	allowReplace := opts.AllowReplace || ctx.Allow.Replace

	type pendingWrite struct {
		target string
		data   []byte
		perm   os.FileMode
	}
	var writes []pendingWrite
	if opts.WriteFiles {
		for _, sb := range matchedSecrets {
			if sb.Scope != "envfile" {
				continue
			}
			target, err := ResolveSafeHomePath(ctx.HomeDir, sb.SourcePath)
			if err != nil {
				res.SkippedFiles = append(res.SkippedFiles, fmt.Sprintf("%s (%v)", sb.SourcePath, err))
				continue
			}
			if !IsWritableEnvFilename(filepath.Base(target)) {
				res.SkippedFiles = append(res.SkippedFiles, fmt.Sprintf("%s (non-writable env filename)", sb.SourcePath))
				continue
			}
			if err := checkNoSymlinkParent(ctx.HomeDir, target); err != nil {
				return nil, err
			}
			if fi, lerr := os.Lstat(target); lerr == nil {
				if fi.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("refusing symlink destination %s", target)
				}
				if fi.IsDir() {
					return nil, fmt.Errorf("refusing directory destination %s", target)
				}
				if !allowReplace {
					return nil, fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", ctx.CollapseHome(target))
				}
			} else if !errors.Is(lerr, os.ErrNotExist) {
				return nil, lerr
			}

			plainBytes, derr := DecryptSecretBlob(kr, SecretBlob{
				ID:         sb.ID,
				Scope:      sb.Scope,
				Label:      sb.Label,
				SourcePath: sb.SourcePath,
				Nonce:      sb.Nonce,
				Ciphertext: sb.Ciphertext,
				Perm:       sb.Perm,
			})
			if derr != nil {
				return nil, fmt.Errorf("decrypt secret blob %s: %w", sb.ID, derr)
			}
			perm := os.FileMode(sb.Perm) & 0o777
			if perm == 0 || perm&0o077 != 0 {
				perm = 0o600
			}
			writes = append(writes, pendingWrite{
				target: target,
				data:   plainBytes,
				perm:   perm,
			})
		}
	}

	if ctx.DryRun {
		for _, w := range writes {
			res.FilesWritten = append(res.FilesWritten, w.target)
		}
		return res, nil
	}

	if err := d.applySnapshotRows(matchedEnv, matchedSecrets, matchedRepos, matchedFleet, len(opts.Vars) == 0 && len(opts.Paths) == 0); err != nil {
		return nil, err
	}

	for _, w := range writes {
		if err := app.WriteAtomicFile(w.target, w.data, w.perm); err != nil {
			return res, err
		}
		res.FilesWritten = append(res.FilesWritten, w.target)
	}

	return res, nil
}

func ResolveSafeHomePath(homeDir, targetPath string) (string, error) {
	if strings.TrimSpace(homeDir) == "" || strings.TrimSpace(targetPath) == "" {
		return "", fmt.Errorf("homeDir and targetPath are required")
	}
	cleanHome, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return "", err
	}
	resolved := targetPath
	if resolved == "~" {
		resolved = cleanHome
	} else if strings.HasPrefix(resolved, "~/") {
		resolved = filepath.Join(cleanHome, resolved[2:])
	} else if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cleanHome, resolved)
	}
	cleanTarget, err := filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", err
	}
	if cleanTarget != cleanHome && !strings.HasPrefix(cleanTarget, cleanHome+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes home directory %q", targetPath, cleanHome)
	}
	return cleanTarget, nil
}

func checkNoSymlinkParent(homeDir, targetPath string) error {
	cleanHome, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return err
	}
	cleanTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(cleanHome, filepath.Dir(cleanTarget))
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("destination %s escapes home directory", targetPath)
	}
	cur := cleanHome
	if rel != "." {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "" || part == "." {
				continue
			}
			cur = filepath.Join(cur, part)
			fi, lerr := os.Lstat(cur)
			if lerr != nil {
				if errors.Is(lerr, os.ErrNotExist) {
					break
				}
				return lerr
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink parent directory %s", cur)
			}
		}
	}
	return nil
}

func (d *DB) exportSnapshotAtomic(createdAt int64) (string, snapshotPayload, map[string]int, error) {
	tx, err := d.sqlDB.Begin()
	if err != nil {
		return "", snapshotPayload{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	hash, err := contentHashWithQuerier(tx)
	if err != nil {
		return "", snapshotPayload{}, nil, err
	}
	snap, counts, err := exportSnapshotWithQuerier(tx, hash, createdAt)
	if err != nil {
		return "", snapshotPayload{}, nil, err
	}
	return hash, snap, counts, nil
}

func exportSnapshotWithQuerier(q rowQuerier, hash string, createdAt int64) (snapshotPayload, map[string]int, error) {
	snap := snapshotPayload{
		Format:    backupFormatVersion,
		CreatedAt: createdAt,
		Hash:      hash,
	}
	counts := map[string]int{}

	rows, err := q.Query(
		`SELECT var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at
		 FROM env_entries ORDER BY source_path ASC, line_num ASC, var_key ASC`,
	)
	if err != nil {
		return snap, nil, err
	}
	for rows.Next() {
		var r snapEnvEntry
		if err := rows.Scan(&r.VarKey, &r.Zone, &r.SourceKind, &r.SourcePath, &r.LineNum, &r.Precedence, &r.IsActive, &r.MaskedValue, &r.Fingerprint, &r.IsSecret, &r.UpdatedAt); err != nil {
			rows.Close()
			return snap, nil, err
		}
		snap.EnvEntries = append(snap.EnvEntries, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snap, nil, err
	}
	rows.Close()
	counts["env_entries"] = len(snap.EnvEntries)

	rows, err = q.Query(
		`SELECT id, scope, label, source_path, nonce, ciphertext, fingerprint, perm, updated_at
		 FROM secret_blobs WHERE scope != 'rotate-undo' ORDER BY id ASC`,
	)
	if err != nil {
		return snap, nil, err
	}
	for rows.Next() {
		var s snapSecretBlob
		if err := rows.Scan(&s.ID, &s.Scope, &s.Label, &s.SourcePath, &s.Nonce, &s.Ciphertext, &s.Fingerprint, &s.Perm, &s.UpdatedAt); err != nil {
			rows.Close()
			return snap, nil, err
		}
		snap.Secrets = append(snap.Secrets, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snap, nil, err
	}
	rows.Close()
	counts["secret_blobs"] = len(snap.Secrets)

	rows, err = q.Query(
		`SELECT id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at
		 FROM repos ORDER BY id ASC`,
	)
	if err != nil {
		return snap, nil, err
	}
	for rows.Next() {
		var r snapRepo
		if err := rows.Scan(&r.ID, &r.Name, &r.Zone, &r.Forge, &r.RemoteURL, &r.LocalPath, &r.Branch, &r.Dirty, &r.Ahead, &r.Behind, &r.UntrackedLocalsJSON, &r.ScannedAt); err != nil {
			rows.Close()
			return snap, nil, err
		}
		snap.Repos = append(snap.Repos, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snap, nil, err
	}
	rows.Close()
	counts["repos"] = len(snap.Repos)

	rows, err = q.Query(
		`SELECT name, provider, tailscale_ip, os, arch, tags, preset, no_zones, exclude_paths_json, selected_repos_json, ai_accounts_json, checklist_json, last_synced_at
		 FROM fleet_nodes ORDER BY name ASC`,
	)
	if err != nil {
		return snap, nil, err
	}
	for rows.Next() {
		var f snapFleetNode
		if err := rows.Scan(&f.Name, &f.Provider, &f.TailscaleIP, &f.OS, &f.Arch, &f.Tags, &f.Preset, &f.NoZones, &f.ExcludePathsJSON, &f.SelectedReposJSON, &f.AIAccountsJSON, &f.ChecklistJSON, &f.LastSyncedAt); err != nil {
			rows.Close()
			return snap, nil, err
		}
		snap.FleetNodes = append(snap.FleetNodes, f)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return snap, nil, err
	}
	rows.Close()
	counts["fleet_nodes"] = len(snap.FleetNodes)

	return snap, counts, nil
}

func (d *DB) applySnapshotRows(
	envs []snapEnvEntry,
	secrets []snapSecretBlob,
	repos []snapRepo,
	fleet []snapFleetNode,
	replaceScopeTables bool,
) error {
	tx, err := d.sqlDB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if len(envs) > 0 {
		if replaceScopeTables {
			if _, err := tx.Exec(`DELETE FROM env_entries`); err != nil {
				return err
			}
		} else {
			seenPairs := map[string]bool{}
			for _, e := range envs {
				pk := e.VarKey + "\x00" + e.SourcePath
				if seenPairs[pk] {
					continue
				}
				seenPairs[pk] = true
				if _, err := tx.Exec(`DELETE FROM env_entries WHERE var_key = ? AND source_path = ?`, e.VarKey, e.SourcePath); err != nil {
					return err
				}
			}
		}
		for _, e := range envs {
			if _, err := tx.Exec(
				`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
				 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.VarKey, e.Zone, e.SourceKind, e.SourcePath, e.LineNum, e.Precedence, e.IsActive, e.MaskedValue, e.Fingerprint, e.IsSecret, e.UpdatedAt,
			); err != nil {
				return err
			}
		}
	}

	for _, s := range secrets {
		if _, err := tx.Exec(
			`INSERT INTO secret_blobs(id, scope, label, source_path, nonce, ciphertext, fingerprint, perm, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
				scope=excluded.scope, label=excluded.label, source_path=excluded.source_path,
				nonce=excluded.nonce, ciphertext=excluded.ciphertext, fingerprint=excluded.fingerprint,
				perm=excluded.perm, updated_at=excluded.updated_at`,
			s.ID, s.Scope, s.Label, s.SourcePath, s.Nonce, s.Ciphertext, s.Fingerprint, s.Perm, s.UpdatedAt,
		); err != nil {
			return err
		}
	}

	for _, r := range repos {
		if _, err := tx.Exec(
			`INSERT INTO repos(id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, zone=excluded.zone, forge=excluded.forge, remote_url=excluded.remote_url,
				local_path=excluded.local_path, branch=excluded.branch, dirty=excluded.dirty, ahead=excluded.ahead,
				behind=excluded.behind, untracked_locals_json=excluded.untracked_locals_json, scanned_at=excluded.scanned_at`,
			r.ID, r.Name, r.Zone, r.Forge, r.RemoteURL, r.LocalPath, r.Branch, r.Dirty, r.Ahead, r.Behind, r.UntrackedLocalsJSON, r.ScannedAt,
		); err != nil {
			return err
		}
	}

	for _, f := range fleet {
		if _, err := tx.Exec(
			`INSERT INTO fleet_nodes(name, provider, tailscale_ip, os, arch, tags, preset, no_zones, exclude_paths_json, selected_repos_json, ai_accounts_json, checklist_json, last_synced_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(name) DO UPDATE SET
				provider=excluded.provider, tailscale_ip=excluded.tailscale_ip, os=excluded.os, arch=excluded.arch,
				tags=excluded.tags, preset=excluded.preset, no_zones=excluded.no_zones,
				exclude_paths_json=excluded.exclude_paths_json, selected_repos_json=excluded.selected_repos_json,
				ai_accounts_json=excluded.ai_accounts_json, checklist_json=excluded.checklist_json, last_synced_at=excluded.last_synced_at`,
			f.Name, f.Provider, f.TailscaleIP, f.OS, f.Arch, f.Tags, f.Preset, f.NoZones, f.ExcludePathsJSON, f.SelectedReposJSON, f.AIAccountsJSON, f.ChecklistJSON, f.LastSyncedAt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func normalizeScopeSet(scopes []string) map[string]bool {
	set := map[string]bool{}
	for _, s := range scopes {
		norm := strings.ToLower(strings.TrimSpace(s))
		if norm == "" || norm == "all" {
			return nil
		}
		set[norm] = true
	}
	return set
}

func matchFilters(name, path string, vars, paths []string) bool {
	if len(vars) > 0 && !slices.Contains(vars, name) {
		return false
	}
	if len(paths) > 0 && !matchPathFilters(path, paths) {
		return false
	}
	return true
}

func IsWritableEnvFilename(base string) bool {
	lower := strings.ToLower(strings.TrimSpace(base))
	if lower == "" || lower == "." || lower == ".." {
		return false
	}
	if isTemplateEnvName(lower) {
		return false
	}
	switch lower {
	case ".env", ".envrc", "mise.local.toml", "config.local.toml":
		return true
	}
	if strings.HasPrefix(lower, ".env.") {
		return true
	}
	if strings.HasSuffix(lower, ".env") && lower != ".env" {
		return true
	}
	return false
}

func isTemplateEnvName(lower string) bool {
	for _, suf := range []string{".example", ".sample", ".template"} {
		if strings.HasSuffix(lower, suf) || strings.Contains(lower, suf+".") {
			return true
		}
	}
	return false
}

func matchPathFilters(candidate string, filters []string) bool {
	clean := filepath.ToSlash(filepath.Clean(candidate))
	base := filepath.Base(clean)
	for _, f := range filters {
		nf := filepath.ToSlash(filepath.Clean(strings.TrimSpace(f)))
		if nf == "" || nf == "." {
			continue
		}
		if clean == nf || base == nf {
			return true
		}
		prefix := strings.TrimSuffix(nf, "/") + "/"
		if strings.HasPrefix(clean, prefix) {
			return true
		}
		if strings.ContainsAny(nf, "*?[") {
			if ok, _ := filepath.Match(nf, clean); ok {
				return true
			}
			if ok, _ := filepath.Match(nf, base); ok {
				return true
			}
		}
	}
	return false
}

func pruneOldBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var stamped []string
	for _, e := range entries {
		name := e.Name()
		if name == "ws-backup-latest.enc" {
			continue
		}
		if strings.HasPrefix(name, "ws-backup-") && strings.HasSuffix(name, ".enc") {
			stamped = append(stamped, name)
		}
	}
	if len(stamped) <= keep {
		return
	}
	sort.Strings(stamped)
	for i := 0; i < len(stamped)-keep; i++ {
		_ = os.Remove(filepath.Join(dir, stamped[i]))
	}
}
