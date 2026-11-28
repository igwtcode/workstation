package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 2

type DB struct {
	sqlDB *sql.DB
	path  string
}

type SecretBlob struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	SourcePath  string `json:"sourcePath"`
	Nonce       []byte `json:"-"`
	Ciphertext  []byte `json:"-"`
	Fingerprint string `json:"fingerprint"`
	Perm        uint32 `json:"perm"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type DBStats struct {
	Path             string         `json:"path"`
	SchemaVersion    int            `json:"schemaVersion"`
	SizeBytes        int64          `json:"sizeBytes"`
	TableCounts      map[string]int `json:"tableCounts"`
	LastBackupHash   string         `json:"lastBackupHash,omitempty"`
	LastBackupAt     int64          `json:"lastBackupAt,omitempty"`
	CurrentHash      string         `json:"currentHash"`
	DirtySinceBackup bool           `json:"dirtySinceBackup"`
}

func DefaultPath(wsHome string) string {
	return filepath.Join(wsHome, "ws.db")
}

func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}
	cleanPath := filepath.Clean(path)
	if fi, err := os.Lstat(cleanPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink database path %s", cleanPath)
	}
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod store directory: %w", err)
	}

	preFile, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		_ = preFile.Close()
	} else if errors.Is(err, os.ErrExist) {
		if cerr := os.Chmod(cleanPath, 0o600); cerr != nil {
			return nil, fmt.Errorf("chmod database file: %w", cerr)
		}
	} else {
		return nil, fmt.Errorf("create database file %s: %w", cleanPath, err)
	}

	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(cleanPath),
		RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)",
	}).String()
	rawDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", cleanPath, err)
	}
	rawDB.SetMaxOpenConns(4)

	d := &DB{sqlDB: rawDB, path: cleanPath}
	if err := d.initPragmasAndSchema(); err != nil {
		_ = rawDB.Close()
		return nil, err
	}
	for _, p := range []string{cleanPath, cleanPath + "-wal", cleanPath + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = rawDB.Close()
			return nil, fmt.Errorf("chmod %s: %w", p, err)
		}
	}
	return d, nil
}

type columnMigration struct {
	table  string
	column string
	def    string
}

var expectedTables = []string{
	"meta",
	"env_entries",
	"secret_blobs",
	"repos",
	"gh_cache",
	"gh_detail_cache",
	"fleet_nodes",
}

var legacyTablesToDrop = []string{
	"ai_providers",
	"ai_file_cache",
	"ai_limits_cache",
}

var expectedColumnMigrations = []columnMigration{
	{"gh_cache", "body", "TEXT NOT NULL DEFAULT ''"},
	{"gh_cache", "labels_json", "TEXT NOT NULL DEFAULT '[]'"},
	{"gh_cache", "assignees_json", "TEXT NOT NULL DEFAULT '[]'"},
	{"gh_cache", "is_draft", "INTEGER NOT NULL DEFAULT 0"},
	{"gh_cache", "comments_count", "INTEGER NOT NULL DEFAULT 0"},
	{"gh_cache", "created_at", "INTEGER NOT NULL DEFAULT 0"},
	{"gh_cache", "source", "TEXT NOT NULL DEFAULT 'involved'"},
	{"gh_cache", "head_ref", "TEXT NOT NULL DEFAULT ''"},
	{"gh_cache", "milestone", "TEXT NOT NULL DEFAULT ''"},
}

func (d *DB) schemaUpToDateReadOnly() (bool, error) {
	rows, err := d.sqlDB.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return false, err
	}
	existingTables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, err
		}
		existingTables[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	for _, tbl := range expectedTables {
		if !existingTables[tbl] {
			return false, nil
		}
	}
	for _, tbl := range legacyTablesToDrop {
		if existingTables[tbl] {
			return false, nil
		}
	}

	colsByTable := map[string]map[string]bool{}
	for _, m := range expectedColumnMigrations {
		cols, ok := colsByTable[m.table]
		if !ok {
			var cerr error
			cols, cerr = listTableColumns(d.sqlDB, m.table)
			if cerr != nil {
				return false, cerr
			}
			colsByTable[m.table] = cols
		}
		if !cols[m.column] {
			return false, nil
		}
	}

	var ver string
	err = d.sqlDB.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ver == strconv.Itoa(SchemaVersion), nil
}

func (d *DB) initPragmasAndSchema() error {
	upToDate, err := d.schemaUpToDateReadOnly()
	if err != nil {
		return fmt.Errorf("inspect sqlite schema: %w", err)
	}
	if upToDate {
		return nil
	}

	ctx := context.Background()
	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite schema conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS env_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			var_key TEXT NOT NULL,
			zone TEXT NOT NULL,
			source_kind TEXT NOT NULL,
			source_path TEXT NOT NULL,
			line_num INTEGER NOT NULL,
			precedence INTEGER NOT NULL,
			is_active INTEGER NOT NULL,
			masked_value TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			is_secret INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_env_var_key ON env_entries(var_key);`,
		`CREATE INDEX IF NOT EXISTS idx_env_fingerprint ON env_entries(fingerprint);`,
		`CREATE INDEX IF NOT EXISTS idx_env_source_path ON env_entries(source_path);`,
		`CREATE TABLE IF NOT EXISTS secret_blobs (
			id TEXT PRIMARY KEY,
			scope TEXT NOT NULL,
			label TEXT NOT NULL,
			source_path TEXT NOT NULL,
			nonce BLOB NOT NULL,
			ciphertext BLOB NOT NULL,
			fingerprint TEXT NOT NULL,
			perm INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`DROP TABLE IF EXISTS ai_providers;`,
		`DROP TABLE IF EXISTS ai_file_cache;`,
		`DROP TABLE IF EXISTS ai_limits_cache;`,
		`CREATE TABLE IF NOT EXISTS repos (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			zone TEXT NOT NULL,
			forge TEXT NOT NULL,
			remote_url TEXT NOT NULL,
			local_path TEXT NOT NULL,
			branch TEXT NOT NULL,
			dirty INTEGER NOT NULL,
			ahead INTEGER NOT NULL,
			behind INTEGER NOT NULL,
			untracked_locals_json TEXT NOT NULL,
			scanned_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_repos_zone ON repos(zone);`,
		`CREATE TABLE IF NOT EXISTS gh_cache (
			kind TEXT NOT NULL,
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			state TEXT NOT NULL,
			author TEXT NOT NULL,
			url TEXT NOT NULL,
			zone TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			fetched_at INTEGER NOT NULL,
			PRIMARY KEY (kind, repo, number)
		);`,
		`CREATE TABLE IF NOT EXISTS gh_detail_cache (
			kind TEXT NOT NULL,
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			payload_json TEXT NOT NULL,
			fetched_at INTEGER NOT NULL,
			PRIMARY KEY (kind, repo, number)
		);`,
		`CREATE TABLE IF NOT EXISTS fleet_nodes (
			name TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			tailscale_ip TEXT NOT NULL,
			os TEXT NOT NULL,
			arch TEXT NOT NULL,
			tags TEXT NOT NULL,
			preset TEXT NOT NULL,
			no_zones INTEGER NOT NULL,
			exclude_paths_json TEXT NOT NULL,
			selected_repos_json TEXT NOT NULL,
			ai_accounts_json TEXT NOT NULL,
			checklist_json TEXT NOT NULL,
			last_synced_at INTEGER NOT NULL
		);`,
	}

	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	for _, col := range expectedColumnMigrations {
		if err := ensureColumnOnConn(ctx, conn, col.table, col.column, col.def); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO meta(key, value, updated_at) VALUES('schema_version', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		strconv.Itoa(SchemaVersion), time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("set schema_version: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	committed = true
	return nil
}

type rowQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func listTableColumns(q rowQuerier, table string) (map[string]bool, error) {
	if !isSafeSQLIdent(table) {
		return nil, fmt.Errorf("invalid sqlite identifier %q", table)
	}
	rows, err := q.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", table, err)
	}
	return cols, nil
}

func ensureColumnOnConn(ctx context.Context, conn *sql.Conn, table, column, columnDef string) error {
	if !isSafeSQLIdent(table) || !isSafeSQLIdent(column) {
		return fmt.Errorf("invalid sqlite identifier %q.%q", table, column)
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if name == column {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("table_info(%s): %w", table, err)
	}
	rows.Close()
	if found {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+columnDef); err != nil {
		return fmt.Errorf("alter table %s add column %s: %w", table, column, err)
	}
	return nil
}

func isSafeSQLIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func (d *DB) Close() error {
	if d == nil || d.sqlDB == nil {
		return nil
	}
	return d.sqlDB.Close()
}

func (d *DB) SQL() *sql.DB {
	if d == nil {
		return nil
	}
	return d.sqlDB
}

func (d *DB) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

func (d *DB) GetMeta(key string) (string, bool, error) {
	if d == nil || d.sqlDB == nil {
		return "", false, fmt.Errorf("database is not open")
	}
	var val string
	err := d.sqlDB.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (d *DB) SetMeta(key, val string) error {
	if d == nil || d.sqlDB == nil {
		return fmt.Errorf("database is not open")
	}
	_, err := d.sqlDB.Exec(
		`INSERT INTO meta(key, value, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, val, time.Now().Unix(),
	)
	return err
}

func (d *DB) PutSecretBlob(b SecretBlob) error {
	if d == nil || d.sqlDB == nil {
		return fmt.Errorf("database is not open")
	}
	return putSecretBlobExec(d.sqlDB, b)
}

func PutSecretBlobTx(tx *sql.Tx, b SecretBlob) error {
	if tx == nil {
		return fmt.Errorf("transaction is nil")
	}
	return putSecretBlobExec(tx, b)
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putSecretBlobExec(ex sqlExecer, b SecretBlob) error {
	if b.UpdatedAt == 0 {
		b.UpdatedAt = time.Now().Unix()
	}
	_, err := ex.Exec(
		`INSERT INTO secret_blobs(id, scope, label, source_path, nonce, ciphertext, fingerprint, perm, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			scope=excluded.scope,
			label=excluded.label,
			source_path=excluded.source_path,
			nonce=excluded.nonce,
			ciphertext=excluded.ciphertext,
			fingerprint=excluded.fingerprint,
			perm=excluded.perm,
			updated_at=excluded.updated_at`,
		b.ID, b.Scope, b.Label, b.SourcePath, b.Nonce, b.Ciphertext, b.Fingerprint, b.Perm, b.UpdatedAt,
	)
	return err
}

func (d *DB) GetSecretBlob(id string) (*SecretBlob, error) {
	if d == nil || d.sqlDB == nil {
		return nil, fmt.Errorf("database is not open")
	}
	var b SecretBlob
	err := d.sqlDB.QueryRow(
		`SELECT id, scope, label, source_path, nonce, ciphertext, fingerprint, perm, updated_at
		 FROM secret_blobs WHERE id = ?`, id,
	).Scan(&b.ID, &b.Scope, &b.Label, &b.SourcePath, &b.Nonce, &b.Ciphertext, &b.Fingerprint, &b.Perm, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (d *DB) ListSecretBlobs(scope string) ([]SecretBlob, error) {
	if d == nil || d.sqlDB == nil {
		return nil, fmt.Errorf("database is not open")
	}
	query := `SELECT id, scope, label, source_path, nonce, ciphertext, fingerprint, perm, updated_at FROM secret_blobs`
	var args []any
	if scope != "" {
		query += ` WHERE scope = ?`
		args = append(args, scope)
	}
	query += ` ORDER BY id ASC`

	rows, err := d.sqlDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SecretBlob
	for rows.Next() {
		var b SecretBlob
		if err := rows.Scan(&b.ID, &b.Scope, &b.Label, &b.SourcePath, &b.Nonce, &b.Ciphertext, &b.Fingerprint, &b.Perm, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *DB) DeleteSecretBlob(id string) error {
	if d == nil || d.sqlDB == nil {
		return fmt.Errorf("database is not open")
	}
	_, err := d.sqlDB.Exec(`DELETE FROM secret_blobs WHERE id = ?`, id)
	return err
}

func (d *DB) ContentHash() (string, error) {
	if d == nil || d.sqlDB == nil {
		return "", fmt.Errorf("database is not open")
	}
	return contentHashWithQuerier(d.sqlDB)
}

func contentHashWithQuerier(q rowQuerier) (string, error) {
	h := sha256.New()

	writeRow := func(prefix string, fields ...string) {
		h.Write([]byte(prefix))
		for _, f := range fields {
			h.Write([]byte{0})
			h.Write([]byte(f))
		}
		h.Write([]byte{'\n'})
	}

	rows, err := q.Query(
		`SELECT var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret
		 FROM env_entries
		 ORDER BY source_path ASC, line_num ASC, var_key ASC`,
	)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var k, z, sk, sp, mv, fp string
		var ln, prec, act, sec int
		if err := rows.Scan(&k, &z, &sk, &sp, &ln, &prec, &act, &mv, &fp, &sec); err != nil {
			rows.Close()
			return "", err
		}
		writeRow("env", k, z, sk, sp, strconv.Itoa(ln), strconv.Itoa(prec), strconv.Itoa(act), mv, fp, strconv.Itoa(sec))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()

	rows, err = q.Query(
		`SELECT id, scope, label, source_path, fingerprint, perm
		 FROM secret_blobs
		 WHERE scope != 'rotate-undo'
		 ORDER BY id ASC`,
	)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, sc, lbl, sp, fp string
		var perm uint32
		if err := rows.Scan(&id, &sc, &lbl, &sp, &fp, &perm); err != nil {
			rows.Close()
			return "", err
		}
		writeRow("blob", id, sc, lbl, sp, fp, strconv.FormatUint(uint64(perm), 8))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()

	rows, err = q.Query(
		`SELECT id, name, zone, forge, remote_url, local_path, untracked_locals_json
		 FROM repos
		 ORDER BY id ASC`,
	)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, name, zone, forge, ru, lp, ul string
		if err := rows.Scan(&id, &name, &zone, &forge, &ru, &lp, &ul); err != nil {
			rows.Close()
			return "", err
		}
		writeRow("repo", id, name, zone, forge, ru, lp, ul)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()

	rows, err = q.Query(
		`SELECT name, provider, tailscale_ip, os, arch, tags, preset, no_zones, exclude_paths_json, selected_repos_json, ai_accounts_json, checklist_json
		 FROM fleet_nodes
		 ORDER BY name ASC`,
	)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var name, prov, ip, osN, arch, tags, preset, exc, sel, aia, chk string
		var nz int
		if err := rows.Scan(&name, &prov, &ip, &osN, &arch, &tags, &preset, &nz, &exc, &sel, &aia, &chk); err != nil {
			rows.Close()
			return "", err
		}
		writeRow("fleet", name, prov, ip, osN, arch, tags, preset, strconv.Itoa(nz), exc, sel, aia, chk)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func (d *DB) Stats() (DBStats, error) {
	if d == nil || d.sqlDB == nil {
		return DBStats{}, fmt.Errorf("database is not open")
	}
	stats := DBStats{
		Path:          d.path,
		SchemaVersion: SchemaVersion,
		TableCounts:   map[string]int{},
	}
	if fi, err := os.Stat(d.path); err == nil {
		stats.SizeBytes = fi.Size()
	}

	tables := []string{
		"env_entries",
		"secret_blobs",
		"repos",
		"gh_cache",
		"gh_detail_cache",
		"fleet_nodes",
	}
	sort.Strings(tables)
	for _, tbl := range tables {
		var cnt int
		if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&cnt); err != nil {
			return DBStats{}, fmt.Errorf("count %s: %w", tbl, err)
		}
		stats.TableCounts[tbl] = cnt
	}

	if v, ok, err := d.GetMeta("last_backup_hash"); err == nil && ok {
		stats.LastBackupHash = v
	}
	if v, ok, err := d.GetMeta("last_backup_at"); err == nil && ok {
		if ts, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			stats.LastBackupAt = ts
		}
	}
	curHash, err := d.ContentHash()
	if err != nil {
		return DBStats{}, err
	}
	stats.CurrentHash = curHash
	stats.DirtySinceBackup = stats.LastBackupHash == "" || stats.LastBackupHash != stats.CurrentHash
	return stats, nil
}
