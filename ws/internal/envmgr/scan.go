package envmgr

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/pelletier/go-toml/v2"
)

const (
	SourceShell            = "shell"
	SourceGlobalMise       = "global-mise"
	SourceGlobalMiseLocal  = "global-mise-local"
	SourceZoneMise         = "zone-mise"
	SourceZoneMiseLocal    = "zone-mise-local"
	SourceProjectMise      = "project-mise"
	SourceProjectMiseLocal = "project-mise-local"
	SourceDotenv           = "dotenv"
	SourceDotenvLocal      = "dotenv-local"
)

const (
	PrecedenceShell            = 10
	PrecedenceGlobalMise       = 20
	PrecedenceGlobalMiseLocal  = 25
	PrecedenceZoneMise         = 30
	PrecedenceZoneMiseLocal    = 35
	PrecedenceProjectMise      = 40
	PrecedenceProjectMiseLocal = 45
	PrecedenceDotenv           = 50
	PrecedenceDotenvShared     = 51
	PrecedenceDotenvSecret     = 52
	PrecedenceDotenvEnv        = 54
	PrecedenceDotenvLocal      = 55
)

const maxProjectEnvDepth = 3

var heavyOrVCSDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	".cache":       true,
	"vendor":       true,
	".terraform":   true,
	".idea":        true,
	".vscode":      true,
}

var curatedSecretDotDirs = map[string]bool{
	".secrets": true,
	".env":     true,
	".config":  true,
	".local":   true,
	".keys":    true,
}

var secretKeySubstrings = []string{
	"TOKEN",
	"SECRET",
	"KEY",
	"PASS",
	"PWD",
	"AUTH",
	"CREDENTIAL",
	"PRIVATE",
	"DSN",
	"JWT",
	"BEARER",
	"APIKEY",
	"COOKIE",
	"PAT",
	"WEBHOOK",
	"SIGNING",
	"CERT",
	"CONN_STR",
	"CONNECTION_STRING",
	"PASSWD",
}

var secretValuePrefixes = []string{
	"sk-",
	"ghp_",
	"gho_",
	"github_pat_",
	"ghs_",
	"ghu_",
	"ghr_",
	"glpat-",
	"AKIA",
	"tskey-",
	"hcloud_",
	"xoxb-",
	"xoxp-",
	"xoxa-",
	"xoxr-",
	"sk_live_",
	"sk_test_",
	"rk_live_",
	"pk_live_",
	"AIza",
	"npm_",
	"pypi-",
	"SG.",
	"eyJ",
	"-----BEGIN ",
}

var benignLocalEnvKeys = map[string]bool{
	"PORT":                 true,
	"HOST":                 true,
	"HOSTNAME":             true,
	"NODE_ENV":             true,
	"RAILS_ENV":            true,
	"APP_ENV":              true,
	"GO_ENV":               true,
	"ENV":                  true,
	"ENVIRONMENT":          true,
	"APP_MODE":             true,
	"LOG_LEVEL":            true,
	"DEBUG":                true,
	"TZ":                   true,
	"LANG":                 true,
	"LC_ALL":               true,
	"EDITOR":               true,
	"VISUAL":               true,
	"TERM":                 true,
	"SHELL":                true,
	"PAGER":                true,
	"AWS_REGION":           true,
	"AWS_DEFAULT_REGION":   true,
	"AWS_PROFILE":          true,
	"CLAUDE_CONFIG_DIR":    true,
	"CODEX_HOME":           true,
	"PUBLIC_URL":           true,
	"NEXT_PUBLIC_APP_NAME": true,
	"VITE_APP_NAME":        true,
}

var shellRCFiles = []string{
	".zshenv",
	".zshrc",
	".zprofile",
	".bashrc",
	".bash_profile",
	".profile",
	".customrc.sh",
}

type EnvEntry struct {
	ID          int64  `json:"id"`
	VarKey      string `json:"varKey"`
	Zone        string `json:"zone"`
	SourceKind  string `json:"sourceKind"`
	SourcePath  string `json:"sourcePath"`
	LineNum     int    `json:"lineNum"`
	Precedence  int    `json:"precedence"`
	IsActive    bool   `json:"isActive"`
	MaskedValue string `json:"maskedValue"`
	Fingerprint string `json:"fingerprint"`
	IsSecret    bool   `json:"isSecret"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type EnvSummary struct {
	VarKey            string `json:"varKey"`
	Zone              string `json:"zone"`
	ActiveSourceKind  string `json:"activeSourceKind"`
	ActiveSourcePath  string `json:"activeSourcePath"`
	ActiveMaskedValue string `json:"activeMaskedValue"`
	Fingerprint       string `json:"fingerprint"`
	IsSecret          bool   `json:"isSecret"`
	SourceCount       int    `json:"sourceCount"`
	ShadowedCount     int    `json:"shadowedCount"`
}

type ScanResult struct {
	FilesScanned int   `json:"filesScanned"`
	EntriesCount int   `json:"entriesCount"`
	SecretsCount int   `json:"secretsCount"`
	BlobsStored  int   `json:"blobsStored"`
	ScannedAt    int64 `json:"scannedAt"`
}

type EnvFileSummary struct {
	Path        string `json:"path"`
	SourceKind  string `json:"sourceKind"`
	Zone        string `json:"zone"`
	VarCount    int    `json:"varCount"`
	SecretCount int    `json:"secretCount"`
	Missing     bool   `json:"missing"`
	Ignored     bool   `json:"ignored"`
}

type parsedVar struct {
	Key     string
	Value   string
	LineNum int
}

type scanTarget struct {
	AbsPath    string
	Zone       string
	SourceKind string
	Precedence int
	StoreBlob  bool
}

func ClassifySecret(key, value string) bool {
	upper := strings.ToUpper(key)
	for _, sub := range secretKeySubstrings {
		if strings.Contains(upper, sub) {
			return true
		}
	}
	for _, pref := range secretValuePrefixes {
		if strings.HasPrefix(value, pref) {
			return true
		}
	}
	if strings.Contains(value, "://") {
		if u, err := url.Parse(value); err == nil && u.User != nil {
			return true
		}
	}
	return looksHighEntropySecret(value)
}

func ClassifySecretForSource(sourceKind, key, value string) bool {
	if ClassifySecret(key, value) {
		return true
	}
	switch sourceKind {
	case SourceDotenv, SourceDotenvLocal, SourceZoneMiseLocal, SourceProjectMiseLocal:
		return !benignLocalEnvKeys[strings.ToUpper(strings.TrimSpace(key))]
	default:
		return false
	}
}

func looksHighEntropySecret(value string) bool {
	if len(value) < 20 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	for _, prefix := range []string{"/", "~/", "./", "../"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	if strings.Contains(value, "://") {
		if u, err := url.Parse(value); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" {
			return false
		}
	}

	var hasLower, hasUpper, hasDigit, hasSymbol int
	var counts [256]int
	allHex := len(value) >= 32
	for i := 0; i < len(value); i++ {
		c := value[i]
		counts[c]++
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			allHex = false
		}
		switch {
		case c >= 'a' && c <= 'z':
			hasLower = 1
		case c >= 'A' && c <= 'Z':
			hasUpper = 1
		case c >= '0' && c <= '9':
			hasDigit = 1
		default:
			hasSymbol = 1
		}
	}
	if !allHex && hasLower+hasUpper+hasDigit+hasSymbol < 3 {
		return false
	}

	n := float64(len(value))
	entropy := 0.0
	for _, cnt := range counts {
		if cnt == 0 {
			continue
		}
		p := float64(cnt) / n
		entropy -= p * math.Log2(p)
	}
	minEntropy := 3.5
	if allHex {
		minEntropy = 3.0
	}
	return entropy >= minEntropy
}

type scannedEnvData struct {
	entries      []EnvEntry
	blobs        []store.SecretBlob
	filesScanned int
	secretsCount int
	scannedAt    int64
}

func ScanInMemory(ctx *app.Context, kr *store.Keyring) ([]EnvEntry, *ScanResult, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("context is required")
	}
	data, err := collectEnvScanData(ctx, nil, kr, false)
	if err != nil {
		return nil, nil, err
	}
	res := &ScanResult{
		FilesScanned: data.filesScanned,
		EntriesCount: len(data.entries),
		SecretsCount: data.secretsCount,
		BlobsStored:  0,
		ScannedAt:    data.scannedAt,
	}
	return data.entries, res, nil
}

func collectEnvScanData(ctx *app.Context, db *store.DB, kr *store.Keyring, collectBlobs bool) (*scannedEnvData, error) {
	now := time.Now().UTC().Unix()
	targets := discoverScanTargets(ctx)

	var keyBytes []byte
	if kr.HasKey() {
		keyBytes = kr.Key
	}

	type pendingEntry struct {
		entry EnvEntry
		scope string
	}

	var pending []pendingEntry
	var blobs []store.SecretBlob
	filesScanned := 0

	for _, t := range targets {
		raw, err := os.ReadFile(t.AbsPath)
		if err != nil {
			continue
		}
		filesScanned++

		vars := parseBytesBySourceKind(t.SourceKind, raw)
		collapsed := ctx.CollapseHome(t.AbsPath)
		scope := effectiveScopeForTarget(ctx, t.SourceKind, t.Zone, t.AbsPath, collapsed)

		for _, v := range vars {
			isSecret := ClassifySecretForSource(t.SourceKind, v.Key, v.Value)
			masked := store.MaskValue(v.Value, isSecret)
			fp := store.Fingerprint(keyBytes, v.Value)
			pending = append(pending, pendingEntry{
				entry: EnvEntry{
					VarKey:      v.Key,
					Zone:        t.Zone,
					SourceKind:  t.SourceKind,
					SourcePath:  collapsed,
					LineNum:     v.LineNum,
					Precedence:  t.Precedence,
					MaskedValue: masked,
					Fingerprint: fp,
					IsSecret:    isSecret,
					UpdatedAt:   now,
				},
				scope: scope,
			})
		}

		if collectBlobs && t.StoreBlob && kr.HasKey() {
			fi, lerr := os.Lstat(t.AbsPath)
			if lerr == nil && fi.Mode()&os.ModeSymlink == 0 && !fi.IsDir() {
				blobID, label := envBlobIdentity(ctx, t.AbsPath)
				perm := uint32(fi.Mode().Perm())
				if perm == 0 {
					perm = 0o600
				}
				fp := store.Fingerprint(kr.Key, string(raw))
				if db != nil {
					if existing, _ := db.GetSecretBlob(blobID); existing != nil &&
						existing.Scope == "envfile" &&
						existing.Label == label &&
						existing.SourcePath == collapsed &&
						existing.Fingerprint == fp &&
						existing.Perm == perm {
						blobs = append(blobs, *existing)
						continue
					}
				}
				nonce, ct, eerr := store.Encrypt(kr.Key, raw, store.EnvBlobAAD(blobID, "envfile", collapsed, perm))
				if eerr != nil {
					return nil, fmt.Errorf("encrypt %s: %w", collapsed, eerr)
				}
				blobs = append(blobs, store.SecretBlob{
					ID:          blobID,
					Scope:       "envfile",
					Label:       label,
					SourcePath:  collapsed,
					Nonce:       nonce,
					Ciphertext:  ct,
					Fingerprint: fp,
					Perm:        perm,
					UpdatedAt:   now,
				})
			}
		}
	}

	type scopeKey struct {
		varKey string
		scope  string
	}
	winners := map[scopeKey]int{}
	for i := range pending {
		sk := scopeKey{varKey: pending[i].entry.VarKey, scope: pending[i].scope}
		cur, ok := winners[sk]
		if !ok {
			winners[sk] = i
			continue
		}
		a := pending[cur].entry
		b := pending[i].entry
		if compareEnvEntryPriority(b, a) >= 0 {
			winners[sk] = i
		}
	}
	for _, idx := range winners {
		pending[idx].entry.IsActive = true
	}

	entries := make([]EnvEntry, 0, len(pending))
	secretsCount := 0
	for _, p := range pending {
		if p.entry.IsSecret {
			secretsCount++
		}
		entries = append(entries, p.entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].VarKey != entries[j].VarKey {
			return entries[i].VarKey < entries[j].VarKey
		}
		if entries[i].Precedence != entries[j].Precedence {
			return entries[i].Precedence > entries[j].Precedence
		}
		if entries[i].SourcePath != entries[j].SourcePath {
			return entries[i].SourcePath < entries[j].SourcePath
		}
		return entries[i].LineNum < entries[j].LineNum
	})

	return &scannedEnvData{
		entries:      entries,
		blobs:        blobs,
		filesScanned: filesScanned,
		secretsCount: secretsCount,
		scannedAt:    now,
	}, nil
}

func Scan(ctx *app.Context, db *store.DB, kr *store.Keyring) (*ScanResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context and open database are required")
	}
	if ctx.DryRun {
		_, res, err := ScanInMemory(ctx, kr)
		return res, err
	}
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("context and open database are required")
	}

	data, err := collectEnvScanData(ctx, db, kr, true)
	if err != nil {
		return nil, err
	}

	tx, err := db.SQL().Begin()
	if err != nil {
		return nil, fmt.Errorf("begin env scan tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM env_entries`); err != nil {
		return nil, fmt.Errorf("clear env_entries: %w", err)
	}
	stmt, err := tx.Prepare(
		`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return nil, fmt.Errorf("prepare env_entries insert: %w", err)
	}

	for _, e := range data.entries {
		if _, err := stmt.Exec(
			e.VarKey, e.Zone, e.SourceKind, e.SourcePath, e.LineNum,
			e.Precedence, boolToInt(e.IsActive), e.MaskedValue, e.Fingerprint,
			boolToInt(e.IsSecret), e.UpdatedAt,
		); err != nil {
			_ = stmt.Close()
			return nil, fmt.Errorf("insert env_entry %s: %w", e.VarKey, err)
		}
	}
	if err := stmt.Close(); err != nil {
		return nil, fmt.Errorf("close env_entries stmt: %w", err)
	}

	blobsStored := 0
	for _, b := range data.blobs {
		if err := store.PutSecretBlobTx(tx, b); err != nil {
			return nil, fmt.Errorf("store secret blob %s: %w", b.ID, err)
		}
		blobsStored++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit env scan tx: %w", err)
	}

	return &ScanResult{
		FilesScanned: data.filesScanned,
		EntriesCount: len(data.entries),
		SecretsCount: data.secretsCount,
		BlobsStored:  blobsStored,
		ScannedAt:    data.scannedAt,
	}, nil
}

func ListSummariesForZone(db *store.DB, query, zone string, secretsOnly bool) ([]EnvSummary, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	entries, err := queryAllEnvEntries(db.SQL())
	if err != nil {
		return nil, err
	}

	z := normalizeZoneFilter(zone)
	type summaryGroupKey struct {
		varKey string
		zone   string
	}
	grouped := map[summaryGroupKey][]EnvEntry{}
	for _, e := range entries {
		if z != "" && !strings.EqualFold(e.Zone, z) {
			continue
		}
		sk := summaryGroupKey{varKey: e.VarKey, zone: e.Zone}
		grouped[sk] = append(grouped[sk], e)
	}

	q := strings.ToLower(strings.TrimSpace(query))
	var out []EnvSummary

	for gk, list := range grouped {
		isSecret := false
		for _, e := range list {
			if e.IsSecret {
				isSecret = true
				break
			}
		}
		if secretsOnly && !isSecret {
			continue
		}
		if q != "" && !matchesGroupQuery(gk.varKey, list, q) {
			continue
		}

		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Precedence != list[j].Precedence {
				return list[i].Precedence > list[j].Precedence
			}
			if list[i].IsActive != list[j].IsActive {
				return list[i].IsActive
			}
			return compareEnvEntryPriority(list[i], list[j]) > 0
		})

		winner := list[0]
		shadowed := 0
		for i := 1; i < len(list); i++ {
			e := list[i]
			if !e.IsActive || (e.Precedence < winner.Precedence && layerAppliesTo(e, winner)) {
				shadowed++
			}
		}

		out = append(out, EnvSummary{
			VarKey:            gk.varKey,
			Zone:              gk.zone,
			ActiveSourceKind:  winner.SourceKind,
			ActiveSourcePath:  winner.SourcePath,
			ActiveMaskedValue: winner.MaskedValue,
			Fingerprint:       winner.Fingerprint,
			IsSecret:          isSecret,
			SourceCount:       len(list),
			ShadowedCount:     shadowed,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].VarKey != out[j].VarKey {
			return out[i].VarKey < out[j].VarKey
		}
		return out[i].Zone < out[j].Zone
	})
	return out, nil
}

func FindEntriesForZone(db *store.DB, query, zone string) ([]EnvEntry, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	entries, err := queryAllEnvEntries(db.SQL())
	if err != nil {
		return nil, err
	}
	z := normalizeZoneFilter(zone)
	q := strings.ToLower(strings.TrimSpace(query))
	if z == "" && q == "" {
		return entries, nil
	}
	var out []EnvEntry
	for _, e := range entries {
		if z != "" && !strings.EqualFold(e.Zone, z) {
			continue
		}
		if q == "" || entryMatchesQuery(e, q) {
			out = append(out, e)
		}
	}
	return out, nil
}

func ListFiles(db *store.DB, zone string) ([]EnvFileSummary, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	z := normalizeZoneFilter(zone)
	query := `SELECT source_path, source_kind, zone, COUNT(*), COALESCE(SUM(CASE WHEN is_secret != 0 THEN 1 ELSE 0 END), 0)
		FROM env_entries`
	var args []any
	if z != "" {
		query += ` WHERE LOWER(zone) = LOWER(?)`
		args = append(args, z)
	}
	query += ` GROUP BY source_path, source_kind, zone
		ORDER BY zone ASC, source_path ASC`

	rows, err := db.SQL().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EnvFileSummary
	for rows.Next() {
		var f EnvFileSummary
		if err := rows.Scan(&f.Path, &f.SourceKind, &f.Zone, &f.VarCount, &f.SecretCount); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func normalizeZoneFilter(zone string) string {
	z := strings.TrimSpace(zone)
	if strings.EqualFold(z, "all") {
		return ""
	}
	return z
}

func compareEnvEntryPriority(a, b EnvEntry) int {
	if a.Precedence != b.Precedence {
		if a.Precedence > b.Precedence {
			return 1
		}
		return -1
	}
	if a.SourcePath != b.SourcePath {
		if a.SourcePath > b.SourcePath {
			return 1
		}
		return -1
	}
	if a.LineNum != b.LineNum {
		if a.LineNum > b.LineNum {
			return 1
		}
		return -1
	}
	return 0
}

func matchesGroupQuery(varKey string, list []EnvEntry, q string) bool {
	if strings.Contains(strings.ToLower(varKey), q) {
		return true
	}
	for _, e := range list {
		if entryMatchesQuery(e, q) {
			return true
		}
	}
	return false
}

func entryMatchesQuery(e EnvEntry, q string) bool {
	return strings.Contains(strings.ToLower(e.VarKey), q) ||
		strings.Contains(strings.ToLower(e.Zone), q) ||
		strings.Contains(strings.ToLower(e.SourceKind), q) ||
		strings.Contains(strings.ToLower(e.SourcePath), q) ||
		strings.Contains(strings.ToLower(e.MaskedValue), q) ||
		strings.Contains(strings.ToLower(e.Fingerprint), q)
}

func layerAppliesTo(lower, higher EnvEntry) bool {
	if lower.SourceKind == SourceShell || lower.SourceKind == SourceGlobalMise || lower.SourceKind == SourceGlobalMiseLocal {
		return true
	}
	if lower.Zone != "" && lower.Zone == higher.Zone {
		if lower.SourceKind == SourceZoneMise || lower.SourceKind == SourceZoneMiseLocal {
			return true
		}
	}
	return effectiveEnvDir(lower.SourcePath) == effectiveEnvDir(higher.SourcePath)
}

func queryAllEnvEntries(sqlDB *sql.DB) ([]EnvEntry, error) {
	rows, err := sqlDB.Query(
		`SELECT id, var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at
		 FROM env_entries
		 ORDER BY var_key ASC, precedence DESC, source_path ASC, line_num ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEnvRows(rows)
}

func scanEnvRows(rows *sql.Rows) ([]EnvEntry, error) {
	var out []EnvEntry
	for rows.Next() {
		var e EnvEntry
		var activeInt, secretInt int
		if err := rows.Scan(
			&e.ID, &e.VarKey, &e.Zone, &e.SourceKind, &e.SourcePath,
			&e.LineNum, &e.Precedence, &activeInt, &e.MaskedValue,
			&e.Fingerprint, &secretInt, &e.UpdatedAt,
		); err != nil {
			return nil, err
		}
		e.IsActive = activeInt != 0
		e.IsSecret = secretInt != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func discoverScanTargets(ctx *app.Context) []scanTarget {
	var targets []scanTarget
	seenFiles := map[string]bool{}

	addFile := func(absPath, zone, kind string, prec int, storeBlob bool) {
		clean := filepath.Clean(absPath)
		if seenFiles[clean] {
			return
		}
		fi, err := os.Lstat(clean)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
			return
		}
		seenFiles[clean] = true
		targets = append(targets, scanTarget{
			AbsPath:    clean,
			Zone:       zone,
			SourceKind: kind,
			Precedence: prec,
			StoreBlob:  storeBlob,
		})
	}

	if ctx.HomeDir != "" {
		for _, name := range shellRCFiles {
			addFile(filepath.Join(ctx.HomeDir, name), "global", SourceShell, PrecedenceShell, false)
		}
	}

	if ctx.ConfigDir != "" {
		miseDir := filepath.Join(ctx.ConfigDir, "mise")
		addFile(filepath.Join(miseDir, "config.toml"), "global", SourceGlobalMise, PrecedenceGlobalMise, false)
		addFile(filepath.Join(miseDir, "config.local.toml"), "global", SourceGlobalMiseLocal, PrecedenceGlobalMiseLocal, false)
	}

	zoneRoots := map[string]string{}
	zoneCodeRoots := map[string]string{}
	var zoneOrder []string
	registerZone := func(name, root, codeRoot string) {
		if name == "" || root == "" {
			return
		}
		if _, ok := zoneRoots[name]; !ok {
			zoneOrder = append(zoneOrder, name)
			zoneRoots[name] = filepath.Clean(root)
		}
		if codeRoot != "" {
			zoneCodeRoots[name] = filepath.Clean(codeRoot)
		}
	}

	if statuses, err := shell.ZoneStatuses(ctx); err == nil {
		for _, st := range statuses {
			registerZone(st.Spec.Name, st.Root, st.CodeRoot)
		}
	}
	if ctx.HomeDir != "" {
		if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				zroot := filepath.Join(ctx.HomeDir, e.Name())
				envMap, err := shell.ReadZoneMiseEnv(zroot)
				if err != nil {
					continue
				}
				if zname := envMap["WS_ZONE"]; zname != "" {
					codeRoot := filepath.Join(zroot, "code")
					if cr := envMap["WS_CODE_ROOT"]; cr != "" {
						codeRoot = cr
					}
					registerZone(zname, zroot, codeRoot)
				}
			}
		}
	}
	sort.Strings(zoneOrder)

	for _, zname := range zoneOrder {
		zroot := zoneRoots[zname]
		addFile(filepath.Join(zroot, "mise.toml"), zname, SourceZoneMise, PrecedenceZoneMise, false)
		addFile(filepath.Join(zroot, "mise.local.toml"), zname, SourceZoneMiseLocal, PrecedenceZoneMiseLocal, true)
	}

	seenRepoDirs := map[string]bool{}
	scanProjectDir := func(dir, zone string) {
		cleanDir := filepath.Clean(dir)
		if seenRepoDirs[cleanDir] {
			return
		}
		seenRepoDirs[cleanDir] = true

		addFile(filepath.Join(cleanDir, "mise.toml"), zone, SourceProjectMise, PrecedenceProjectMise, false)
		addFile(filepath.Join(cleanDir, "mise.local.toml"), zone, SourceProjectMiseLocal, PrecedenceProjectMiseLocal, true)

		type foundEnvFile struct {
			absPath  string
			relSlash string
		}
		var envFiles []foundEnvFile
		var walkEnv func(curDir string, depth int)
		walkEnv = func(curDir string, depth int) {
			entries, err := os.ReadDir(curDir)
			if err != nil {
				return
			}
			for _, e := range entries {
				if e.Type()&os.ModeSymlink != 0 {
					continue
				}
				name := e.Name()
				full := filepath.Join(curDir, name)
				if e.IsDir() {
					if depth >= maxProjectEnvDepth || !shouldDescendProjectDir(name) {
						continue
					}
					walkEnv(full, depth+1)
					continue
				}
				if isDotenvFilename(name) && !isEnvTemplateFile(name) {
					rel, rerr := filepath.Rel(cleanDir, full)
					if rerr != nil {
						continue
					}
					envFiles = append(envFiles, foundEnvFile{
						absPath:  full,
						relSlash: filepath.ToSlash(rel),
					})
				}
			}
		}
		walkEnv(cleanDir, 0)

		sort.Slice(envFiles, func(i, j int) bool {
			return envFiles[i].relSlash < envFiles[j].relSlash
		})
		for _, ef := range envFiles {
			kind, prec := classifyDotenvPath(ef.relSlash)
			addFile(ef.absPath, zone, kind, prec, true)
		}
	}

	if repos, err := shell.DiscoverRepos(ctx); err == nil {
		for _, r := range repos {
			scanProjectDir(r.Path, r.Zone)
		}
	}

	for _, zname := range zoneOrder {
		cr := zoneCodeRoots[zname]
		if cr == "" {
			cr = filepath.Join(zoneRoots[zname], "code")
		}
		for _, dir := range findProjectDirs(cr, 4) {
			scanProjectDir(dir, zname)
		}
	}

	if ctx.HomeDir != "" {
		flatCode := filepath.Join(ctx.HomeDir, "code")
		for _, dir := range findProjectDirs(flatCode, 4) {
			scanProjectDir(dir, "")
		}
	}

	if cr := strings.TrimSpace(ctx.LookupEnv("WS_CODE_ROOT")); cr != "" {
		cleanCR := filepath.Clean(ctx.ExpandHome(cr))
		fromLocalEnv := ctx.LocalEnv != nil && strings.TrimSpace(ctx.LocalEnv["WS_CODE_ROOT"]) != ""
		realHome, _ := os.UserHomeDir()
		sameAsRealHome := ctx.HomeDir != "" && realHome != "" && filepath.Clean(ctx.HomeDir) == filepath.Clean(realHome)
		if fromLocalEnv || sameAsRealHome || (ctx.HomeDir != "" && isPathWithin(cleanCR, ctx.HomeDir)) {
			crZone := ""
			for _, zname := range zoneOrder {
				if isPathWithin(cleanCR, zoneRoots[zname]) {
					crZone = zname
					break
				}
			}
			for _, dir := range findProjectDirs(cleanCR, 4) {
				scanProjectDir(dir, crZone)
			}
		}
	}

	return targets
}

func findProjectDirs(root string, maxDepth int) []string {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		clean := filepath.Clean(d)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		hasGit := false
		hasEnvOrMise := false
		for _, e := range entries {
			name := e.Name()
			if name == ".git" {
				hasGit = true
				break
			}
			if !e.IsDir() && ((isDotenvFilename(name) && !isEnvTemplateFile(name)) || name == "mise.toml" || name == "mise.local.toml") {
				hasEnvOrMise = true
			}
			if e.IsDir() && depth == 1 && curatedSecretDotDirs[strings.ToLower(name)] {
				if dirHasEnvFile(filepath.Join(dir, name), maxProjectEnvDepth-1) {
					hasEnvOrMise = true
				}
			}
		}
		if hasGit {
			add(dir)
			return
		}
		if depth == 1 && hasEnvOrMise {
			add(dir)
		}
		for _, e := range entries {
			if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || heavyOrVCSDirs[name] {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	walk(filepath.Clean(root), 0)
	sort.Strings(out)
	return out
}

func dirHasEnvFile(dir string, maxDepth int) bool {
	if maxDepth < 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			if maxDepth > 0 && shouldDescendProjectDir(name) && dirHasEnvFile(filepath.Join(dir, name), maxDepth-1) {
				return true
			}
			continue
		}
		if isDotenvFilename(name) && !isEnvTemplateFile(name) {
			return true
		}
	}
	return false
}

func shouldDescendProjectDir(name string) bool {
	if heavyOrVCSDirs[name] {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return curatedSecretDotDirs[strings.ToLower(name)]
	}
	return true
}

func isDotenvFilename(baseName string) bool {
	lower := strings.ToLower(baseName)
	return lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".env")
}

func isEnvTemplateFile(baseName string) bool {
	lower := strings.ToLower(baseName)
	for _, suf := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(lower, suf) || strings.HasSuffix(lower, suf+".env") {
			return true
		}
	}
	switch lower {
	case "example.env", "sample.env", "template.env", "dist.env":
		return true
	}
	return false
}

func hasCuratedSecretDotDirSegment(dir string) bool {
	slash := filepath.ToSlash(filepath.Clean(dir))
	if slash == "." || slash == "" {
		return false
	}
	for _, seg := range strings.Split(slash, "/") {
		if curatedSecretDotDirs[strings.ToLower(seg)] {
			return true
		}
	}
	return false
}

func classifyDotenvPath(relPath string) (string, int) {
	slash := filepath.ToSlash(filepath.Clean(relPath))
	base := strings.ToLower(filepath.Base(slash))
	inSecretDotDir := hasCuratedSecretDotDirSegment(filepath.Dir(slash))

	if base == ".env.local" {
		if inSecretDotDir {
			return SourceDotenvLocal, PrecedenceDotenvSecret + 1
		}
		return SourceDotenvLocal, PrecedenceDotenvLocal
	}
	if strings.HasPrefix(base, ".env.") && strings.HasSuffix(base, ".local") {
		if inSecretDotDir {
			return SourceDotenvLocal, PrecedenceDotenvSecret + 1
		}
		return SourceDotenvLocal, PrecedenceDotenvLocal + 1
	}
	if base == ".env" {
		if inSecretDotDir {
			return SourceDotenv, PrecedenceDotenvSecret
		}
		return SourceDotenv, PrecedenceDotenv
	}
	if strings.HasPrefix(base, ".env.") {
		if inSecretDotDir {
			return SourceDotenv, PrecedenceDotenvSecret + 1
		}
		return SourceDotenv, PrecedenceDotenvEnv
	}
	stem := strings.TrimSuffix(base, ".env")
	if strings.HasSuffix(stem, ".local") {
		if inSecretDotDir {
			return SourceDotenvLocal, PrecedenceDotenvSecret + 1
		}
		return SourceDotenvLocal, PrecedenceDotenvLocal
	}
	switch stem {
	case "shared", "common", "base", "default":
		return SourceDotenv, PrecedenceDotenvShared
	default:
		return SourceDotenv, PrecedenceDotenvSecret
	}
}

func effectiveEnvDir(path string) string {
	return effectiveEnvDirWithCtx(nil, path)
}

func effectiveEnvDirWithCtx(ctx *app.Context, path string) string {
	dir := filepath.Clean(filepath.Dir(path))
	homeBound := ""
	if ctx != nil && ctx.HomeDir != "" {
		if absHome, err := filepath.Abs(filepath.Clean(ctx.HomeDir)); err == nil {
			homeBound = absHome
		} else {
			homeBound = filepath.Clean(ctx.HomeDir)
		}
	}

	resolved := dir
	for cur := dir; ; {
		if homeBound != "" && cur == homeBound {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." || parent == "~" {
			break
		}
		if curatedSecretDotDirs[strings.ToLower(filepath.Base(cur))] {
			resolved = parent
		}
		cur = parent
	}
	return filepath.ToSlash(resolved)
}

func effectiveScopeForTarget(ctx *app.Context, sourceKind, zone, absPath, collapsedPath string) string {
	switch sourceKind {
	case SourceShell, SourceGlobalMise, SourceGlobalMiseLocal:
		return "global"
	case SourceZoneMise, SourceZoneMiseLocal:
		return "zone:" + zone
	default:
		if ctx != nil && ctx.HomeDir != "" {
			return "dir:" + effectiveEnvDirWithCtx(ctx, absPath)
		}
		return "dir:" + effectiveEnvDir(collapsedPath)
	}
}

func envBlobIdentity(ctx *app.Context, absPath string) (id, label string) {
	clean := filepath.Clean(absPath)
	rel := clean
	if ctx != nil && ctx.HomeDir != "" {
		if r, err := filepath.Rel(ctx.HomeDir, clean); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	relSlash := filepath.ToSlash(filepath.Clean(rel))
	return "envfile:" + relSlash, relSlash
}

func parseBytesBySourceKind(kind string, raw []byte) []parsedVar {
	switch kind {
	case SourceShell:
		return parseShellBytes(raw)
	case SourceGlobalMise, SourceGlobalMiseLocal, SourceZoneMise, SourceZoneMiseLocal, SourceProjectMise, SourceProjectMiseLocal:
		return parseMiseEnvBytes(raw)
	default:
		return parseDotenvBytes(raw)
	}
}

func parseBytesForPath(path string, raw []byte) []parsedVar {
	base := filepath.Base(path)
	if isDotenvFilename(base) {
		return parseDotenvBytes(raw)
	}
	if strings.HasSuffix(strings.ToLower(base), ".toml") {
		return parseMiseEnvBytes(raw)
	}
	return parseShellBytes(raw)
}

func parseShellBytes(raw []byte) []parsedVar {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []parsedVar
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "export\t") {
			trimmed = strings.TrimSpace(trimmed[len("export"):])
		}
		key, valRaw, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !isValidEnvKey(key) {
			continue
		}
		if strings.Contains(valRaw, "$(") || strings.Contains(valRaw, "`") {
			continue
		}
		val, ok := parseEnvAssignmentValue(valRaw)
		if !ok {
			continue
		}
		out = append(out, parsedVar{
			Key:     key,
			Value:   val,
			LineNum: idx + 1,
		})
	}
	return out
}

func parseDotenvBytes(raw []byte) []parsedVar {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	var out []parsedVar
	for i := 0; i < len(lines); i++ {
		parsed, ok, err := inspectDotenvAssignment(lines, i)
		if err != nil || !ok {
			continue
		}
		joined := strings.Join(lines[i:parsed.endLine+1], "\n")
		if len(parsed.prefix) > len(joined) {
			i = parsed.endLine
			continue
		}
		val, valOK := parseEnvAssignmentValue(joined[len(parsed.prefix):])
		if valOK {
			out = append(out, parsedVar{
				Key:     parsed.key,
				Value:   val,
				LineNum: i + 1,
			})
		}
		i = parsed.endLine
	}
	return out
}

func parseEnvAssignmentValue(raw string) (string, bool) {
	s := strings.TrimLeft(raw, " \t")
	if s == "" {
		return "", true
	}
	switch s[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\', '$', '`':
					b.WriteByte(s[i])
				default:
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
				continue
			}
			if s[i] == '"' {
				rest := strings.TrimSpace(s[i+1:])
				if rest != "" && !strings.HasPrefix(rest, "#") {
					return "", false
				}
				return b.String(), true
			}
			b.WriteByte(s[i])
		}
		return "", false
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", false
		}
		rest := strings.TrimSpace(s[end+2:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", false
		}
		return s[1 : end+1], true
	default:
		if s[0] == '#' {
			return "", true
		}
		for i := 1; i < len(s); i++ {
			if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
				return strings.TrimRight(s[:i], " \t"), true
			}
		}
		return strings.TrimRight(s, " \t"), true
	}
}

type miseEnvDocument struct {
	Env map[string]any `toml:"env"`
}

func parseMiseEnvBytes(raw []byte) []parsedVar {
	var doc miseEnvDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	if len(doc.Env) == 0 {
		return nil
	}
	lineNums := findTOMLEnvLineNumbers(raw)
	var out []parsedVar
	for k, v := range doc.Env {
		if k == "_" {
			continue
		}
		var str string
		switch tv := v.(type) {
		case string:
			str = tv
		case int64, float64, bool:
			str = fmt.Sprintf("%v", tv)
		default:
			continue
		}
		ln := lineNums[k]
		if ln == 0 {
			ln = 1
		}
		out = append(out, parsedVar{
			Key:     k,
			Value:   str,
			LineNum: ln,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LineNum != out[j].LineNum {
			return out[i].LineNum < out[j].LineNum
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func findTOMLEnvLineNumbers(raw []byte) map[string]int {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := map[string]int{}
	section := ""
	multilineDelim := ""

	for idx, line := range lines {
		if multilineDelim != "" {
			if containsUnescapedDelim(line, multilineDelim) {
				multilineDelim = ""
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if sec, ok := parseTOMLSectionHeader(trimmed); ok {
			section = sec
			continue
		}
		lhs, rhs, ok := splitTOMLKeyVal(trimmed)
		if !ok {
			continue
		}
		if d := opensMultilineTOMLString(rhs); d != "" {
			multilineDelim = d
		}
		if section == "env" {
			k := unquoteTOMLKey(lhs)
			if k != "" {
				out[k] = idx + 1
			}
		} else if section == "" && strings.HasPrefix(lhs, "env.") {
			k := unquoteTOMLKey(strings.TrimPrefix(lhs, "env."))
			if k != "" {
				out[k] = idx + 1
			}
		}
	}
	return out
}

func parseTOMLSectionHeader(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	isArray := strings.HasPrefix(trimmed, "[[")
	endToken := "]"
	start := 1
	if isArray {
		endToken = "]]"
		start = 2
	}
	end := strings.Index(trimmed[start:], endToken)
	if end < 0 {
		return "", false
	}
	after := strings.TrimSpace(trimmed[start+end+len(endToken):])
	if after != "" && !strings.HasPrefix(after, "#") {
		return "", false
	}
	return strings.TrimSpace(trimmed[start : start+end]), true
}

func splitTOMLKeyVal(s string) (string, string, bool) {
	inDouble := false
	inSingle := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inDouble {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		switch c {
		case '"':
			inDouble = true
		case '\'':
			inSingle = true
		case '#':
			return "", "", false
		case '=':
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func unquoteTOMLKey(raw string) string {
	k := strings.TrimSpace(raw)
	if len(k) >= 2 && ((k[0] == '"' && k[len(k)-1] == '"') || (k[0] == '\'' && k[len(k)-1] == '\'')) {
		return k[1 : len(k)-1]
	}
	return k
}

func opensMultilineTOMLString(rhs string) string {
	s := strings.TrimSpace(rhs)
	for _, delim := range []string{`"""`, `'''`} {
		if strings.HasPrefix(s, delim) {
			rest := s[len(delim):]
			if !containsUnescapedDelim(rest, delim) {
				return delim
			}
		}
	}
	return ""
}

func containsUnescapedDelim(s, delim string) bool {
	if delim == `'''` {
		return strings.Contains(s, delim)
	}
	for i := 0; i+len(delim) <= len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i:i+len(delim)] == delim {
			return true
		}
	}
	return false
}

func isValidEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		isLetter := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isLetter {
			return false
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return true
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}

func checkNoSymlinkEscape(homeDir, absTarget string) error {
	cleanHome, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return err
	}
	evalHome := cleanHome
	if resolved, err := filepath.EvalSymlinks(cleanHome); err == nil {
		evalHome = resolved
	}
	if fi, err := os.Lstat(absTarget); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink target %s", absTarget)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cur := filepath.Dir(absTarget)
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if resolved != evalHome && !strings.HasPrefix(resolved, evalHome+string(filepath.Separator)) {
				return fmt.Errorf("target %s resolves outside home directory", absTarget)
			}
			break
		} else if errors.Is(err, os.ErrNotExist) {
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		} else {
			return err
		}
	}
	return nil
}
