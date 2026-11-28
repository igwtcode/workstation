package envmgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/store"
)

const (
	StatusActive   = "ACTIVE"
	StatusShadowed = "SHADOWED"
	StatusMatch    = "MATCH"
	StatusBase     = "BASE"
)

type ProvenanceLayer struct {
	Status      string `json:"status"`
	VarKey      string `json:"varKey"`
	Zone        string `json:"zone"`
	SourceKind  string `json:"sourceKind"`
	SourcePath  string `json:"sourcePath"`
	LineNum     int    `json:"lineNum"`
	Precedence  int    `json:"precedence"`
	MaskedValue string `json:"maskedValue"`
	Fingerprint string `json:"fingerprint"`
	IsSecret    bool   `json:"isSecret"`
}

type ProvenanceReport struct {
	VarKey            string            `json:"varKey"`
	TargetDir         string            `json:"targetDir,omitempty"`
	ActiveValueMasked string            `json:"activeValueMasked"`
	Fingerprint       string            `json:"fingerprint"`
	Layers            []ProvenanceLayer `json:"layers"`
	ImpactMatches     []EnvEntry        `json:"impactMatches"`
}

func GetProvenance(ctx *app.Context, db *store.DB, varKey, targetDir string) (*ProvenanceReport, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	varKey = strings.TrimSpace(varKey)
	if varKey == "" {
		return nil, fmt.Errorf("variable key is required")
	}

	rows, err := db.SQL().Query(
		`SELECT id, var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at
		 FROM env_entries
		 WHERE var_key = ?
		 ORDER BY precedence ASC, source_path ASC, line_num ASC`,
		varKey,
	)
	if err != nil {
		return nil, err
	}
	allForKey, err := scanEnvRows(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}

	absTarget := ""
	displayTarget := strings.TrimSpace(targetDir)
	if displayTarget != "" {
		expanded := displayTarget
		if ctx != nil {
			expanded = ctx.ExpandHome(displayTarget)
		}
		if abs, aerr := filepath.Abs(filepath.Clean(expanded)); aerr == nil {
			absTarget = abs
		} else {
			absTarget = filepath.Clean(expanded)
		}
		if ctx != nil {
			displayTarget = ctx.CollapseHome(absTarget)
		}
	}

	var scoped []EnvEntry
	for _, e := range allForKey {
		if absTarget == "" || entryAppliesToDir(ctx, e, absTarget) {
			scoped = append(scoped, e)
		}
	}

	sort.SliceStable(scoped, func(i, j int) bool {
		if scoped[i].Precedence != scoped[j].Precedence {
			return scoped[i].Precedence < scoped[j].Precedence
		}
		if scoped[i].SourcePath != scoped[j].SourcePath {
			return scoped[i].SourcePath < scoped[j].SourcePath
		}
		return scoped[i].LineNum < scoped[j].LineNum
	})

	report := &ProvenanceReport{
		VarKey:    varKey,
		TargetDir: displayTarget,
	}

	if len(scoped) > 0 {
		active := scoped[len(scoped)-1]
		report.ActiveValueMasked = active.MaskedValue
		report.Fingerprint = active.Fingerprint

		report.Layers = make([]ProvenanceLayer, len(scoped))
		for i, e := range scoped {
			status := StatusShadowed
			if i == len(scoped)-1 {
				status = StatusActive
			} else if active.Fingerprint != "" && e.Fingerprint == active.Fingerprint {
				status = StatusMatch
			}
			report.Layers[i] = ProvenanceLayer{
				Status:      status,
				VarKey:      e.VarKey,
				Zone:        e.Zone,
				SourceKind:  e.SourceKind,
				SourcePath:  e.SourcePath,
				LineNum:     e.LineNum,
				Precedence:  e.Precedence,
				MaskedValue: e.MaskedValue,
				Fingerprint: e.Fingerprint,
				IsSecret:    e.IsSecret,
			}
		}
	}

	byKey, err := GetBlastRadius(db, varKey)
	if err != nil {
		return nil, err
	}
	seenIDs := map[int64]bool{}
	var impacts []EnvEntry
	for _, e := range byKey {
		if !seenIDs[e.ID] {
			seenIDs[e.ID] = true
			impacts = append(impacts, e)
		}
	}
	if report.Fingerprint != "" {
		byFP, err := GetBlastRadius(db, report.Fingerprint)
		if err != nil {
			return nil, err
		}
		for _, e := range byFP {
			if !seenIDs[e.ID] {
				seenIDs[e.ID] = true
				impacts = append(impacts, e)
			}
		}
	}
	sort.SliceStable(impacts, func(i, j int) bool {
		if impacts[i].Precedence != impacts[j].Precedence {
			return impacts[i].Precedence > impacts[j].Precedence
		}
		if impacts[i].SourcePath != impacts[j].SourcePath {
			return impacts[i].SourcePath < impacts[j].SourcePath
		}
		return impacts[i].LineNum < impacts[j].LineNum
	})
	report.ImpactMatches = impacts

	return report, nil
}

func GetBlastRadius(db *store.DB, keyOrFingerprint string) ([]EnvEntry, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	needle := strings.TrimSpace(keyOrFingerprint)
	if needle == "" {
		return nil, nil
	}

	rows, err := db.SQL().Query(
		`SELECT id, var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at
		 FROM env_entries
		 WHERE var_key = ? OR (fingerprint != '' AND fingerprint = ?)
		 ORDER BY precedence DESC, source_path ASC, line_num ASC`,
		needle, needle,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEnvRows(rows)
}

func RevealValue(ctx *app.Context, db *store.DB, kr *store.Keyring, sourcePath, varKey string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	varKey = strings.TrimSpace(varKey)
	if sourcePath == "" || varKey == "" {
		return "", fmt.Errorf("sourcePath and varKey are required")
	}

	absPath := sourcePath
	if ctx != nil && ctx.HomeDir != "" {
		resolved, err := store.ResolveSafeHomePath(ctx.HomeDir, sourcePath)
		if err != nil {
			return "", fmt.Errorf("unsafe source path %s: %w", sourcePath, err)
		}
		if err := checkNoSymlinkEscape(ctx.HomeDir, resolved); err != nil {
			return "", err
		}
		absPath = resolved
	} else if ctx != nil {
		absPath = filepath.Clean(ctx.ExpandHome(sourcePath))
	} else {
		absPath = filepath.Clean(absPath)
	}

	if !isAllowedRevealPath(ctx, db, sourcePath, absPath) {
		return "", fmt.Errorf("refusing to reveal non-env/non-indexed path %s", sourcePath)
	}

	var raw []byte
	if fi, lerr := os.Lstat(absPath); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink source %s", sourcePath)
		}
		if fi.IsDir() {
			return "", fmt.Errorf("source path %s is a directory", sourcePath)
		}
		data, rerr := os.ReadFile(absPath)
		if rerr != nil {
			return "", fmt.Errorf("read %s: %w", sourcePath, rerr)
		}
		raw = data
	} else {
		if !errors.Is(lerr, os.ErrNotExist) {
			return "", fmt.Errorf("stat %s: %w", sourcePath, lerr)
		}

		if db == nil || !kr.HasKey() {
			return "", fmt.Errorf("source file %s not found and encrypted fallback unavailable", sourcePath)
		}
		blob, berr := findEnvBlobForPath(ctx, db, sourcePath, absPath)
		if berr != nil {
			return "", berr
		}
		if blob == nil {
			return "", fmt.Errorf("source file %s not found on disk or in ws.db", sourcePath)
		}
		plain, derr := store.DecryptSecretBlob(kr, *blob)
		if derr != nil {
			return "", fmt.Errorf("decrypt fallback blob %s: %w", blob.ID, derr)
		}
		raw = plain
	}

	vars := parseBytesForPath(absPath, raw)
	found := false
	val := ""
	for _, v := range vars {
		if v.Key == varKey {
			found = true
			val = v.Value
		}
	}
	if !found {
		return "", fmt.Errorf("variable %q not found in %s", varKey, sourcePath)
	}
	return val, nil
}

func isAllowedRevealPath(ctx *app.Context, db *store.DB, sourcePath, absPath string) bool {
	base := filepath.Base(absPath)
	if store.IsWritableEnvFilename(base) || base == "mise.toml" || (base == "config.toml" && strings.Contains(filepath.ToSlash(absPath), "/mise/")) {
		return true
	}
	if ctx != nil && ctx.HomeDir != "" && filepath.Dir(absPath) == filepath.Clean(ctx.HomeDir) {
		for _, rc := range shellRCFiles {
			if base == rc {
				return true
			}
		}
	}
	if db != nil && db.SQL() != nil {
		collapsed := sourcePath
		if ctx != nil {
			collapsed = ctx.CollapseHome(absPath)
		}
		var one int
		if err := db.SQL().QueryRow(
			`SELECT 1 FROM env_entries WHERE source_path = ? OR source_path = ? OR source_path = ? LIMIT 1`,
			collapsed, absPath, sourcePath,
		).Scan(&one); err == nil {
			return true
		}
		if err := db.SQL().QueryRow(
			`SELECT 1 FROM secret_blobs WHERE scope = 'envfile' AND (source_path = ? OR source_path = ? OR source_path = ?) LIMIT 1`,
			collapsed, absPath, sourcePath,
		).Scan(&one); err == nil {
			return true
		}
	}
	return false
}

func entryAppliesToDir(ctx *app.Context, e EnvEntry, absTarget string) bool {
	if e.SourceKind == SourceShell || e.SourceKind == SourceGlobalMise || e.SourceKind == SourceGlobalMiseLocal {
		return true
	}
	srcAbs := e.SourcePath
	if ctx != nil {
		srcAbs = ctx.ExpandHome(e.SourcePath)
	}
	if abs, err := filepath.Abs(filepath.Clean(srcAbs)); err == nil {
		srcAbs = abs
	} else {
		srcAbs = filepath.Clean(srcAbs)
	}
	return isPathWithin(absTarget, effectiveEnvDirWithCtx(ctx, srcAbs))
}

func findEnvBlobForPath(ctx *app.Context, db *store.DB, sourcePath, absPath string) (*store.SecretBlob, error) {
	blobID, _ := envBlobIdentity(ctx, absPath)
	if b, err := db.GetSecretBlob(blobID); err != nil {
		return nil, err
	} else if b != nil {
		return b, nil
	}

	collapsed := sourcePath
	if ctx != nil {
		collapsed = ctx.CollapseHome(absPath)
	}
	blobs, err := db.ListSecretBlobs("envfile")
	if err != nil {
		return nil, err
	}
	for i := range blobs {
		b := &blobs[i]
		if b.SourcePath == collapsed || b.SourcePath == sourcePath || b.SourcePath == absPath {
			return b, nil
		}
	}
	return nil, nil
}
