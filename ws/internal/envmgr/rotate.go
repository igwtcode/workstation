package envmgr

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/pelletier/go-toml/v2"
)

type RotateRequest struct {
	VarKey           string   `json:"varKey,omitempty"`
	MatchFingerprint string   `json:"matchFingerprint,omitempty"`
	Zone             string   `json:"zone,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	OnlyExisting     bool     `json:"onlyExisting,omitempty"`
}

var writeAtomicFile = app.WriteAtomicFile

type RotateTarget struct {
	VarKey              string `json:"varKey,omitempty"`
	SourcePath          string `json:"sourcePath"`
	SourceKind          string `json:"sourceKind"`
	Zone                string `json:"zone"`
	LineNum             int    `json:"lineNum"`
	OldMasked           string `json:"oldMasked"`
	NewMasked           string `json:"newMasked"`
	ExpectedFingerprint string `json:"expectedFingerprint,omitempty"`
	FileMode            uint32 `json:"fileMode"`
	WillCreate          bool   `json:"willCreate,omitempty"`
}

type RotatePlan struct {
	TxID      string         `json:"txId"`
	VarKey    string         `json:"varKey"`
	NewMasked string         `json:"newMasked"`
	Targets   []RotateTarget `json:"targets"`
}

type RotateResult struct {
	TxID         string   `json:"txId"`
	UpdatedFiles []string `json:"updatedFiles"`
	SkippedFiles []string `json:"skippedFiles,omitempty"`
	DryRun       bool     `json:"dryRun,omitempty"`
}

type rotateUndoRecord struct {
	TxID      string           `json:"txId"`
	VarKey    string           `json:"varKey"`
	Timestamp int64            `json:"timestamp"`
	Targets   []rotateUndoItem `json:"targets"`
}

type rotateUndoItem struct {
	BlobID          string `json:"blobId"`
	SourcePath      string `json:"sourcePath"`
	Existed         bool   `json:"existed"`
	Perm            uint32 `json:"perm"`
	PostWriteSHA256 string `json:"postWriteSha256,omitempty"`
}

func undoBlobAAD(blobID, sourcePath string) []byte {
	return []byte(blobID + "\x00" + sourcePath)
}

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func PlanRotate(ctx *app.Context, db *store.DB, kr *store.Keyring, req RotateRequest, newValue string) (*RotatePlan, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context and open database are required")
	}
	var entries []EnvEntry
	var err error
	if db == nil || db.SQL() == nil {
		if !ctx.DryRun {
			return nil, fmt.Errorf("context and open database are required")
		}
		entries, _, err = ScanInMemory(ctx, kr)
		if err != nil {
			return nil, err
		}
	} else {
		entries, err = queryAllEnvEntries(db.SQL())
		if err != nil {
			return nil, err
		}
	}
	return PlanRotateFromEntries(ctx, entries, req, newValue)
}

func PlanRotateFromEntries(ctx *app.Context, entries []EnvEntry, req RotateRequest, newValue string) (*RotatePlan, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	req.VarKey = strings.TrimSpace(req.VarKey)
	req.MatchFingerprint = strings.TrimSpace(req.MatchFingerprint)
	req.Zone = strings.TrimSpace(req.Zone)

	if req.VarKey == "" && req.MatchFingerprint == "" {
		return nil, fmt.Errorf("varKey or matchFingerprint is required")
	}

	newMasked := store.MaskValue(newValue, true)
	var targets []RotateTarget
	seenTarget := map[string]bool{}
	matchedPaths := map[string]bool{}
	distinctFPs := map[string]bool{}

	for _, e := range entries {
		if !isWritableSourceKind(e.SourceKind) {
			continue
		}
		if req.VarKey != "" && e.VarKey != req.VarKey {
			continue
		}
		if req.MatchFingerprint != "" && e.Fingerprint != req.MatchFingerprint {
			continue
		}
		if req.Zone != "" && e.Zone != req.Zone {
			continue
		}
		if len(req.Paths) > 0 && !matchesRequestedPaths(ctx, e.SourcePath, req.Paths) {
			continue
		}

		absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, e.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("unsafe target path %s: %w", e.SourcePath, err)
		}
		if !isWritableEnvFilename(filepath.Base(absPath)) {
			continue
		}
		if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
			return nil, err
		}

		fi, lerr := os.Lstat(absPath)
		willCreate := false
		var fileMode uint32 = 0o600
		if lerr != nil {
			if errors.Is(lerr, os.ErrNotExist) {
				if req.OnlyExisting {
					continue
				}
				willCreate = true
			} else {
				return nil, lerr
			}
		} else {
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("refusing symlink target %s", e.SourcePath)
			}
			if fi.IsDir() {
				return nil, fmt.Errorf("target %s is a directory", e.SourcePath)
			}
			fileMode = uint32(fi.Mode().Perm())
		}

		if e.Fingerprint != "" {
			distinctFPs[e.Fingerprint] = true
		}

		dedupKey := absPath + "\x00" + e.VarKey + "\x00" + strconv.Itoa(e.LineNum)
		if seenTarget[dedupKey] {
			continue
		}
		seenTarget[dedupKey] = true
		matchedPaths[absPath] = true

		targets = append(targets, RotateTarget{
			VarKey:              e.VarKey,
			SourcePath:          ctx.CollapseHome(absPath),
			SourceKind:          e.SourceKind,
			Zone:                e.Zone,
			LineNum:             e.LineNum,
			OldMasked:           e.MaskedValue,
			NewMasked:           newMasked,
			ExpectedFingerprint: e.Fingerprint,
			FileMode:            fileMode,
			WillCreate:          willCreate,
		})
	}

	if len(req.Paths) > 0 && req.VarKey != "" && req.MatchFingerprint == "" && !req.OnlyExisting {
		for _, p := range req.Paths {
			absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, p)
			if err != nil {
				return nil, fmt.Errorf("unsafe target path %s: %w", p, err)
			}
			if !isWritableEnvFilename(filepath.Base(absPath)) {
				return nil, fmt.Errorf("refusing non-writable config target %s (only .env* and mise.local.toml allowed)", p)
			}
			if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
				return nil, err
			}
			if matchedPaths[absPath] {
				continue
			}
			fi, lerr := os.Lstat(absPath)
			willCreate := false
			var fileMode uint32 = 0o600
			if lerr != nil {
				if !errors.Is(lerr, os.ErrNotExist) {
					return nil, lerr
				}
				willCreate = true
			} else {
				if fi.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("refusing symlink target %s", p)
				}
				if fi.IsDir() {
					return nil, fmt.Errorf("target %s is a directory", p)
				}
				fileMode = uint32(fi.Mode().Perm())
			}
			kind, zone := inferTargetKindAndZone(ctx, absPath)
			targets = append(targets, RotateTarget{
				VarKey:     req.VarKey,
				SourcePath: ctx.CollapseHome(absPath),
				SourceKind: kind,
				Zone:       zone,
				LineNum:    0,
				OldMasked:  "",
				NewMasked:  newMasked,
				FileMode:   fileMode,
				WillCreate: willCreate,
			})
		}
	}

	if req.MatchFingerprint == "" && len(distinctFPs) > 1 {
		return nil, fmt.Errorf("key %s matches %d distinct values across %d files; narrow scope with --fingerprint, --zone, or --path",
			req.VarKey, len(distinctFPs), len(matchedPaths))
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].SourcePath != targets[j].SourcePath {
			return targets[i].SourcePath < targets[j].SourcePath
		}
		if targets[i].LineNum != targets[j].LineNum {
			return targets[i].LineNum < targets[j].LineNum
		}
		return targets[i].VarKey < targets[j].VarKey
	})

	planVarKey := req.VarKey
	if planVarKey == "" && len(targets) > 0 {
		planVarKey = targets[0].VarKey
	}

	return &RotatePlan{
		TxID:      newTxID(),
		VarKey:    planVarKey,
		NewMasked: newMasked,
		Targets:   targets,
	}, nil
}

type targetUpdate struct {
	varKey              string
	lineNum             int
	expectedFingerprint string
	newValue            string
}

func ExecRotate(ctx *app.Context, db *store.DB, kr *store.Keyring, plan *RotatePlan, newValue string) (*RotateResult, error) {
	if ctx == nil || plan == nil {
		return nil, fmt.Errorf("context, open database, and plan are required")
	}

	if ctx.DryRun {
		return &RotateResult{
			TxID:         plan.TxID,
			UpdatedFiles: uniquePlanPaths(plan),
			DryRun:       true,
		}, nil
	}
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("context, open database, and plan are required")
	}

	if len(plan.Targets) == 0 {
		return &RotateResult{
			TxID:         plan.TxID,
			UpdatedFiles: nil,
		}, nil
	}

	type fileBatch struct {
		sourcePath string
		absPath    string
		sourceKind string
		targets    []targetUpdate
		origBytes  []byte
		existed    bool
		origPerm   uint32
		updated    []byte
	}

	var batches []*fileBatch
	batchByAbs := map[string]*fileBatch{}

	for _, t := range plan.Targets {
		absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, t.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("unsafe target path %s: %w", t.SourcePath, err)
		}
		if !isWritableEnvFilename(filepath.Base(absPath)) {
			return nil, fmt.Errorf("refusing non-writable config target %s", t.SourcePath)
		}
		if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
			return nil, err
		}

		varKey := t.VarKey
		if varKey == "" {
			varKey = plan.VarKey
		}
		if !isValidEnvKey(varKey) {
			return nil, fmt.Errorf("invalid variable key %q for %s", varKey, t.SourcePath)
		}

		b, exists := batchByAbs[absPath]
		if !exists {
			b = &fileBatch{
				sourcePath: ctx.CollapseHome(absPath),
				absPath:    absPath,
				sourceKind: t.SourceKind,
			}
			batchByAbs[absPath] = b
			batches = append(batches, b)
		}
		b.targets = append(b.targets, targetUpdate{
			varKey:              varKey,
			lineNum:             t.LineNum,
			expectedFingerprint: strings.TrimSpace(t.ExpectedFingerprint),
			newValue:            newValue,
		})
	}

	hasExisting := false
	for _, b := range batches {
		fi, lerr := os.Lstat(b.absPath)
		if lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("refusing symlink target %s", b.sourcePath)
			}
			if fi.IsDir() {
				return nil, fmt.Errorf("target %s is a directory", b.sourcePath)
			}
			orig, rerr := os.ReadFile(b.absPath)
			if rerr != nil {
				return nil, fmt.Errorf("read %s: %w", b.sourcePath, rerr)
			}
			b.origBytes = orig
			b.existed = true
			b.origPerm = uint32(fi.Mode().Perm())
			hasExisting = true
		} else if errors.Is(lerr, os.ErrNotExist) {
			b.existed = false
			b.origPerm = 0o600
		} else {
			return nil, lerr
		}
	}
	if hasExisting && !kr.HasKey() {
		return nil, fmt.Errorf("database key is required to record encrypted rollback snapshots before rotating files; run `ws db key init` first")
	}

	var keyBytes []byte
	if kr.HasKey() {
		keyBytes = kr.Key
	}

	for _, b := range batches {
		var updated []byte
		var uerr error
		if filepath.Base(b.absPath) == "mise.local.toml" {
			updated, uerr = applyMiseLocalTargets(b.origBytes, b.targets, keyBytes)
		} else {
			updated, uerr = applyDotenvTargets(b.origBytes, b.targets, keyBytes)
		}
		if uerr != nil {
			return nil, fmt.Errorf("update %s: %w", b.sourcePath, uerr)
		}
		b.updated = updated
	}

	prevLastTx, hadPrevLastTx, _ := db.GetMeta("last_rotate_tx")

	now := time.Now().UTC().Unix()
	undoRec := rotateUndoRecord{
		TxID:      plan.TxID,
		VarKey:    plan.VarKey,
		Timestamp: now,
		Targets:   make([]rotateUndoItem, 0, len(batches)),
	}
	var storedBlobIDs []string
	cleanupPreWriteUndo := func() {
		for _, id := range storedBlobIDs {
			_ = db.DeleteSecretBlob(id)
		}
		_, _ = db.SQL().Exec(`DELETE FROM meta WHERE key = ?`, "rotate_tx:"+plan.TxID)
		if hadPrevLastTx {
			_ = db.SetMeta("last_rotate_tx", prevLastTx)
		} else {
			_, _ = db.SQL().Exec(`DELETE FROM meta WHERE key = ?`, "last_rotate_tx")
		}
	}
	for idx, b := range batches {
		blobID := "undo:" + plan.TxID + ":" + strconv.Itoa(idx)
		if b.existed {
			nonce, ct, eerr := store.Encrypt(kr.Key, b.origBytes, undoBlobAAD(blobID, b.sourcePath))
			if eerr != nil {
				cleanupPreWriteUndo()
				return nil, fmt.Errorf("encrypt undo snapshot for %s: %w", b.sourcePath, eerr)
			}
			if err := db.PutSecretBlob(store.SecretBlob{
				ID:          blobID,
				Scope:       "rotate-undo",
				Label:       plan.VarKey,
				SourcePath:  b.sourcePath,
				Nonce:       nonce,
				Ciphertext:  ct,
				Fingerprint: store.Fingerprint(kr.Key, string(b.origBytes)),
				Perm:        b.origPerm,
				UpdatedAt:   now,
			}); err != nil {
				cleanupPreWriteUndo()
				return nil, fmt.Errorf("save undo snapshot for %s: %w", b.sourcePath, err)
			}
			storedBlobIDs = append(storedBlobIDs, blobID)
		}
		undoRec.Targets = append(undoRec.Targets, rotateUndoItem{
			BlobID:          blobID,
			SourcePath:      b.sourcePath,
			Existed:         b.existed,
			Perm:            b.origPerm,
			PostWriteSHA256: sha256HexBytes(b.updated),
		})
	}
	metaJSON, err := json.Marshal(undoRec)
	if err != nil {
		cleanupPreWriteUndo()
		return nil, err
	}
	if err := db.SetMeta("rotate_tx:"+plan.TxID, string(metaJSON)); err != nil {
		cleanupPreWriteUndo()
		return nil, err
	}
	if err := db.SetMeta("last_rotate_tx", string(metaJSON)); err != nil {
		cleanupPreWriteUndo()
		return nil, err
	}

	var updatedFiles []string
	var written []*fileBatch
	for _, b := range batches {
		if err := writeAtomicFile(b.absPath, b.updated, 0o600); err != nil {
			var rollbackErrs []string
			for i := len(written) - 1; i >= 0; i-- {
				wb := written[i]
				if wb.existed {
					perm := os.FileMode(wb.origPerm) & 0o777
					if perm == 0 {
						perm = 0o600
					}
					if rerr := writeAtomicFile(wb.absPath, wb.origBytes, perm); rerr != nil {
						rollbackErrs = append(rollbackErrs, fmt.Sprintf("%s: %v", wb.sourcePath, rerr))
					}
				} else if rerr := os.Remove(wb.absPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
					rollbackErrs = append(rollbackErrs, fmt.Sprintf("%s: %v", wb.sourcePath, rerr))
				}
			}
			if len(rollbackErrs) > 0 {
				return &RotateResult{
					TxID:         plan.TxID,
					UpdatedFiles: updatedFiles,
				}, fmt.Errorf("write %s: %w (partial rollback errors: %s; recovery tx %s kept)", b.sourcePath, err, strings.Join(rollbackErrs, "; "), plan.TxID)
			}
			cleanupPreWriteUndo()
			return &RotateResult{
				TxID: plan.TxID,
			}, fmt.Errorf("write %s (rolled back %d earlier file(s)): %w", b.sourcePath, len(written), err)
		}
		written = append(written, b)
		updatedFiles = append(updatedFiles, b.sourcePath)
	}

	pruneOldRotateUndo(db, 20)

	if _, err := Scan(ctx, db, kr); err != nil {
		return &RotateResult{
			TxID:         plan.TxID,
			UpdatedFiles: updatedFiles,
		}, fmt.Errorf("rescan after rotate: %w", err)
	}

	return &RotateResult{
		TxID:         plan.TxID,
		UpdatedFiles: updatedFiles,
	}, nil
}

func UndoRotate(ctx *app.Context, db *store.DB, kr *store.Keyring, txID string) (*RotateResult, error) {
	if ctx == nil || db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("context and open database are required")
	}

	rec, err := loadUndoRecord(db, strings.TrimSpace(txID))
	if err != nil {
		return nil, err
	}

	type pendingRestore struct {
		sourcePath string
		absPath    string
		existed    bool
		data       []byte
		perm       os.FileMode
		preExisted bool
		preBytes   []byte
		prePerm    os.FileMode
	}
	restores := make([]pendingRestore, 0, len(rec.Targets))
	paths := make([]string, 0, len(rec.Targets))

	for _, item := range rec.Targets {
		absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, item.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("unsafe rollback path %s: %w", item.SourcePath, err)
		}
		if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
			return nil, err
		}

		if item.PostWriteSHA256 != "" && !ctx.Allow.Replace {
			curBytes, rerr := os.ReadFile(absPath)
			if rerr != nil {
				return nil, fmt.Errorf("%s could not be verified before undo (%w); re-run with --allow replace to overwrite", item.SourcePath, rerr)
			}
			if sha256HexBytes(curBytes) != item.PostWriteSHA256 {
				return nil, fmt.Errorf("%s was modified since rotation %s; re-run with --allow replace to overwrite", item.SourcePath, rec.TxID)
			}
		}

		pr := pendingRestore{
			sourcePath: item.SourcePath,
			absPath:    absPath,
			existed:    item.Existed,
			perm:       0o600,
		}
		if item.Existed {
			if !kr.HasKey() {
				return nil, fmt.Errorf("database key is required to decrypt rollback snapshot %s", item.BlobID)
			}
			blob, err := db.GetSecretBlob(item.BlobID)
			if err != nil {
				return nil, err
			}
			if blob == nil {
				return nil, fmt.Errorf("rollback snapshot %s not found in ws.db", item.BlobID)
			}
			if blob.SourcePath != item.SourcePath {
				return nil, fmt.Errorf("rollback snapshot %s source path mismatch (%s != %s)", item.BlobID, blob.SourcePath, item.SourcePath)
			}
			plain, err := store.Decrypt(kr.Key, blob.Nonce, blob.Ciphertext, undoBlobAAD(blob.ID, blob.SourcePath))
			if err != nil {
				return nil, fmt.Errorf("decrypt rollback snapshot %s: %w", item.BlobID, err)
			}
			pr.data = plain
			perm := os.FileMode(item.Perm) & 0o777
			if perm != 0 && perm&0o077 == 0 {
				pr.perm = perm
			}
		}
		if fi, lerr := os.Lstat(absPath); lerr == nil && !fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			if cur, rerr := os.ReadFile(absPath); rerr == nil {
				pr.preExisted = true
				pr.preBytes = cur
				pr.prePerm = fi.Mode().Perm()
				if pr.prePerm == 0 {
					pr.prePerm = 0o600
				}
			}
		}
		restores = append(restores, pr)
		paths = append(paths, item.SourcePath)
	}

	if ctx.DryRun {
		return &RotateResult{
			TxID:         rec.TxID,
			UpdatedFiles: paths,
			DryRun:       true,
		}, nil
	}

	rollbackPartialUndo := func(applied []pendingRestore) []string {
		var errs []string
		for i := len(applied) - 1; i >= 0; i-- {
			ar := applied[i]
			if ar.preExisted {
				if rerr := app.WriteAtomicFile(ar.absPath, ar.preBytes, ar.prePerm); rerr != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", ar.sourcePath, rerr))
				}
			} else if rerr := os.Remove(ar.absPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				errs = append(errs, fmt.Sprintf("%s: %v", ar.sourcePath, rerr))
			}
		}
		return errs
	}

	var restoredFiles []string
	var applied []pendingRestore
	for _, r := range restores {
		var opErr error
		if r.existed {
			opErr = app.WriteAtomicFile(r.absPath, r.data, r.perm)
		} else {
			if err := os.Remove(r.absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				opErr = err
			}
		}
		if opErr != nil {
			rbErrs := rollbackPartialUndo(applied)
			if len(rbErrs) > 0 {
				return &RotateResult{
					TxID:         rec.TxID,
					UpdatedFiles: restoredFiles,
				}, fmt.Errorf("undo %s: %w (rollback errors: %s)", r.sourcePath, opErr, strings.Join(rbErrs, "; "))
			}
			return &RotateResult{
				TxID: rec.TxID,
			}, fmt.Errorf("undo %s (rolled back %d earlier file(s)): %w", r.sourcePath, len(applied), opErr)
		}
		applied = append(applied, r)
		restoredFiles = append(restoredFiles, r.sourcePath)
	}

	if _, err := Scan(ctx, db, kr); err != nil {
		return &RotateResult{
			TxID:         rec.TxID,
			UpdatedFiles: restoredFiles,
		}, fmt.Errorf("rescan after undo: %w", err)
	}

	return &RotateResult{
		TxID:         rec.TxID,
		UpdatedFiles: restoredFiles,
	}, nil
}

func pruneOldRotateUndo(db *store.DB, keepTx int) {
	if db == nil || db.SQL() == nil || keepTx <= 0 {
		return
	}
	blobs, err := db.ListSecretBlobs("rotate-undo")
	if err != nil || len(blobs) == 0 {
		return
	}
	type txEntry struct {
		txID      string
		updatedAt int64
		blobIDs   []string
	}
	byTx := map[string]*txEntry{}
	var entries []*txEntry
	for _, b := range blobs {
		if !strings.HasPrefix(b.ID, "undo:") {
			continue
		}
		rest := strings.TrimPrefix(b.ID, "undo:")
		txID := rest
		if idx := strings.LastIndexByte(rest, ':'); idx > 0 {
			txID = rest[:idx]
		}
		ent, ok := byTx[txID]
		if !ok {
			ent = &txEntry{txID: txID, updatedAt: b.UpdatedAt}
			byTx[txID] = ent
			entries = append(entries, ent)
		}
		if b.UpdatedAt > ent.updatedAt {
			ent.updatedAt = b.UpdatedAt
		}
		ent.blobIDs = append(ent.blobIDs, b.ID)
	}
	if len(entries) <= keepTx {
		return
	}

	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].updatedAt > entries[i].updatedAt ||
				(entries[j].updatedAt == entries[i].updatedAt && entries[j].txID > entries[i].txID) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	for _, old := range entries[keepTx:] {
		for _, id := range old.blobIDs {
			_ = db.DeleteSecretBlob(id)
		}
		_, _ = db.SQL().Exec(`DELETE FROM meta WHERE key = ?`, "rotate_tx:"+old.txID)
	}
}

func PlanSetVar(ctx *app.Context, db *store.DB, kr *store.Keyring, filePath, varKey, newValue string) (*RotatePlan, error) {
	_ = db
	_ = kr
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	varKey = strings.TrimSpace(varKey)
	if !isValidEnvKey(varKey) {
		return nil, fmt.Errorf("invalid variable key %q", varKey)
	}
	absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, filePath)
	if err != nil {
		return nil, fmt.Errorf("unsafe target path %s: %w", filePath, err)
	}
	if !isWritableEnvFilename(filepath.Base(absPath)) {
		return nil, fmt.Errorf("refusing non-writable config target %s (only .env* and mise.local.toml allowed)", filePath)
	}
	if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
		return nil, err
	}

	kind, zone := inferTargetKindAndZone(ctx, absPath)

	willCreate := false
	var fileMode uint32 = 0o600
	oldMasked := ""
	lineNum := 0

	fi, lerr := os.Lstat(absPath)
	if lerr != nil {
		if !errors.Is(lerr, os.ErrNotExist) {
			return nil, lerr
		}
		willCreate = true
	} else {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink target %s", filePath)
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("target %s is a directory", filePath)
		}
		fileMode = uint32(fi.Mode().Perm())
		if raw, rerr := os.ReadFile(absPath); rerr == nil {
			for _, v := range parseBytesForPath(absPath, raw) {
				if v.Key == varKey {
					lineNum = v.LineNum
					oldMasked = store.MaskValue(v.Value, ClassifySecretForSource(kind, v.Key, v.Value))
				}
			}
		}
	}

	isSecret := ClassifySecretForSource(kind, varKey, newValue)
	newMasked := store.MaskValue(newValue, isSecret)

	return &RotatePlan{
		TxID:      newTxID(),
		VarKey:    varKey,
		NewMasked: newMasked,
		Targets: []RotateTarget{
			{
				VarKey:     varKey,
				SourcePath: ctx.CollapseHome(absPath),
				SourceKind: kind,
				Zone:       zone,
				LineNum:    lineNum,
				OldMasked:  oldMasked,
				NewMasked:  newMasked,
				FileMode:   fileMode,
				WillCreate: willCreate,
			},
		},
	}, nil
}

func RestoreEnvFiles(ctx *app.Context, db *store.DB, kr *store.Keyring, paths []string, allowReplace bool) ([]string, error) {
	if ctx == nil || db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("context and open database are required")
	}
	if !kr.HasKey() {
		return nil, fmt.Errorf("database key is required to decrypt stored env files")
	}

	blobs, err := db.ListSecretBlobs("envfile")
	if err != nil {
		return nil, err
	}

	replaceAllowed := allowReplace || ctx.Allow.Replace

	type pendingFile struct {
		absPath string
		data    []byte
	}
	var pending []pendingFile

	for _, b := range blobs {
		if len(paths) > 0 && !matchesBlobFilter(ctx, b, paths) {
			continue
		}
		absPath, err := store.ResolveSafeHomePath(ctx.HomeDir, b.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("unsafe restore path %s: %w", b.SourcePath, err)
		}
		if !store.IsWritableEnvFilename(filepath.Base(absPath)) {
			return nil, fmt.Errorf("refusing non-writable restored file %s", b.SourcePath)
		}
		if err := checkNoSymlinkEscape(ctx.HomeDir, absPath); err != nil {
			return nil, err
		}
		if fi, lerr := os.Lstat(absPath); lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("refusing symlink destination %s", b.SourcePath)
			}
			if fi.IsDir() {
				return nil, fmt.Errorf("destination %s is a directory", b.SourcePath)
			}
			if !replaceAllowed {
				return nil, fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", ctx.CollapseHome(absPath))
			}
		} else if !errors.Is(lerr, os.ErrNotExist) {
			return nil, lerr
		}

		plain, derr := store.DecryptSecretBlob(kr, b)
		if derr != nil {
			return nil, fmt.Errorf("decrypt envfile blob %s: %w", b.ID, derr)
		}
		pending = append(pending, pendingFile{
			absPath: absPath,
			data:    plain,
		})
	}

	written := make([]string, 0, len(pending))
	if ctx.DryRun {
		for _, p := range pending {
			written = append(written, p.absPath)
		}
		return written, nil
	}

	for _, p := range pending {
		if err := app.WriteAtomicFile(p.absPath, p.data, 0o600); err != nil {
			return written, fmt.Errorf("write %s: %w", p.absPath, err)
		}
		written = append(written, p.absPath)
	}
	return written, nil
}

func loadUndoRecord(db *store.DB, txID string) (*rotateUndoRecord, error) {
	var raw string
	var ok bool
	var err error
	if txID != "" {
		raw, ok, err = db.GetMeta("rotate_tx:" + txID)
		if err != nil {
			return nil, err
		}
	}
	if !ok {
		raw, ok, err = db.GetMeta("last_rotate_tx")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no rotation transaction recorded")
		}
	}
	var rec rotateUndoRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("decode rollback metadata: %w", err)
	}
	if txID != "" && rec.TxID != txID {
		return nil, fmt.Errorf("rotation transaction %q not found", txID)
	}
	return &rec, nil
}

func newTxID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("rot-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(buf))
}

func uniquePlanPaths(plan *RotatePlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range plan.Targets {
		if !seen[t.SourcePath] {
			seen[t.SourcePath] = true
			out = append(out, t.SourcePath)
		}
	}
	return out
}

func isWritableSourceKind(kind string) bool {
	switch kind {
	case SourceDotenv, SourceDotenvLocal, SourceProjectMiseLocal, SourceZoneMiseLocal:
		return true
	default:
		return false
	}
}

func isWritableEnvFilename(base string) bool {
	if base == "mise.local.toml" {
		return true
	}
	return isDotenvFilename(base) && !isEnvTemplateFile(base)
}

func matchesRequestedPaths(ctx *app.Context, sourcePath string, filters []string) bool {
	normSrc := filepath.ToSlash(filepath.Clean(sourcePath))
	absSrc := normSrc
	if ctx != nil {
		absSrc = filepath.ToSlash(filepath.Clean(ctx.ExpandHome(sourcePath)))
	}
	for _, f := range filters {
		trimmed := strings.TrimSpace(f)
		if trimmed == "" {
			continue
		}
		nf := filepath.ToSlash(filepath.Clean(trimmed))
		absF := nf
		if ctx != nil {
			absF = filepath.ToSlash(filepath.Clean(ctx.ExpandHome(trimmed)))
		}
		if matchPathBoundaryOrGlob(normSrc, nf) || matchPathBoundaryOrGlob(absSrc, absF) {
			return true
		}
	}
	return false
}

func matchesBlobFilter(ctx *app.Context, b store.SecretBlob, filters []string) bool {
	normSrc := filepath.ToSlash(filepath.Clean(b.SourcePath))
	normLabel := filepath.ToSlash(filepath.Clean(b.Label))
	absSrc := normSrc
	if ctx != nil {
		absSrc = filepath.ToSlash(filepath.Clean(ctx.ExpandHome(b.SourcePath)))
	}
	for _, f := range filters {
		trimmed := strings.TrimSpace(f)
		if trimmed == "" {
			continue
		}
		nf := filepath.ToSlash(filepath.Clean(trimmed))
		absF := nf
		if ctx != nil {
			absF = filepath.ToSlash(filepath.Clean(ctx.ExpandHome(trimmed)))
		}
		if b.ID == trimmed ||
			matchPathBoundaryOrGlob(normSrc, nf) ||
			matchPathBoundaryOrGlob(absSrc, absF) ||
			matchPathBoundaryOrGlob(normLabel, nf) {
			return true
		}
	}
	return false
}

func matchPathBoundaryOrGlob(candidate, filter string) bool {
	if filter == "" || filter == "." {
		return false
	}
	if candidate == filter {
		return true
	}
	if !strings.Contains(filter, "/") && filepath.Base(candidate) == filter {
		return true
	}
	prefix := strings.TrimSuffix(filter, "/") + "/"
	if strings.HasPrefix(candidate, prefix) {
		return true
	}
	if strings.ContainsAny(filter, "*?[") {
		if ok, _ := filepath.Match(filter, candidate); ok {
			return true
		}
		if !strings.Contains(filter, "/") {
			if ok, _ := filepath.Match(filter, filepath.Base(candidate)); ok {
				return true
			}
		}
	}
	return false
}

func inferTargetKindAndZone(ctx *app.Context, absPath string) (kind, zone string) {
	base := filepath.Base(absPath)
	if base == "mise.local.toml" {
		kind = SourceProjectMiseLocal
		if ctx != nil && ctx.HomeDir != "" && filepath.Dir(filepath.Dir(absPath)) == filepath.Clean(ctx.HomeDir) {
			kind = SourceZoneMiseLocal
		}
	} else {
		relPath := base
		if ctx != nil && ctx.HomeDir != "" {
			if rel, err := filepath.Rel(ctx.HomeDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
				relPath = rel
			}
		}
		kind, _ = classifyDotenvPath(relPath)
	}
	if ctx != nil && ctx.HomeDir != "" {
		if rel, err := filepath.Rel(ctx.HomeDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) >= 2 && parts[0] != "code" && !strings.HasPrefix(parts[0], ".") {
				zone = parts[0]
			}
		}
	}
	return kind, zone
}

func applyDotenvTargets(raw []byte, targets []targetUpdate, keyBytes []byte) ([]byte, error) {
	eol := "\n"
	text := string(raw)
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}

	hadTrailingNewline := len(text) == 0 || strings.HasSuffix(text, "\n")
	var lines []string
	if len(text) > 0 {
		trimmedText := strings.TrimSuffix(text, "\n")
		lines = strings.Split(trimmedText, "\n")
	}

	byLine := map[int]targetUpdate{}
	var pinnedOrder []int
	var unpinned []targetUpdate
	for _, t := range targets {
		if t.lineNum > 0 {
			if prev, dup := byLine[t.lineNum]; dup {
				if prev.varKey != t.varKey || (prev.expectedFingerprint != "" && t.expectedFingerprint != "" && prev.expectedFingerprint != t.expectedFingerprint) {
					return nil, fmt.Errorf("conflicting rotation targets at line %d", t.lineNum)
				}
				continue
			}
			byLine[t.lineNum] = t
			pinnedOrder = append(pinnedOrder, t.lineNum)
		} else {
			unpinned = append(unpinned, t)
		}
	}

	matchedLine := map[int]bool{}
	matchedUnpinned := make([]bool, len(unpinned))
	var outLines []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		parsed, ok, err := inspectDotenvAssignment(lines, i)
		if err != nil {
			return nil, err
		}
		if !ok {
			outLines = append(outLines, line)
			continue
		}

		lineNo := i + 1
		joined := strings.Join(lines[i:parsed.endLine+1], "\n")
		curVal := ""
		curOK := false
		if len(parsed.prefix) <= len(joined) {
			curVal, curOK = parseEnvAssignmentValue(joined[len(parsed.prefix):])
		}

		if t, hasPinned := byLine[lineNo]; hasPinned {
			if parsed.key != t.varKey {
				return nil, fmt.Errorf("expected %s at line %d, found %s (file changed since planning)", t.varKey, lineNo, parsed.key)
			}
			if t.expectedFingerprint != "" {
				if !curOK || store.Fingerprint(keyBytes, curVal) != t.expectedFingerprint {
					return nil, fmt.Errorf("assignment %s at line %d changed since planning (fingerprint mismatch)", t.varKey, lineNo)
				}
			}
			formatted := formatDotenvValue(t.newValue, parsed.quoteStyle)
			outLines = append(outLines, parsed.prefix+formatted+parsed.inlineComment)
			matchedLine[lineNo] = true
			i = parsed.endLine
			continue
		}

		rewritten := false
		for ui, u := range unpinned {
			if parsed.key != u.varKey {
				continue
			}
			if u.expectedFingerprint != "" {
				if !curOK || store.Fingerprint(keyBytes, curVal) != u.expectedFingerprint {
					continue
				}
			}
			formatted := formatDotenvValue(u.newValue, parsed.quoteStyle)
			outLines = append(outLines, parsed.prefix+formatted+parsed.inlineComment)
			matchedUnpinned[ui] = true
			rewritten = true
			break
		}
		if !rewritten {
			outLines = append(outLines, lines[i:parsed.endLine+1]...)
		}
		i = parsed.endLine
	}

	for _, lineNo := range pinnedOrder {
		if !matchedLine[lineNo] {
			t := byLine[lineNo]
			return nil, fmt.Errorf("expected %s at line %d not found (file changed since planning)", t.varKey, lineNo)
		}
	}

	for ui, u := range unpinned {
		if !matchedUnpinned[ui] {
			if u.expectedFingerprint != "" {
				return nil, fmt.Errorf("expected %s matching fingerprint not found (file changed since planning)", u.varKey)
			}
			outLines = append(outLines, u.varKey+"="+formatDotenvValue(u.newValue, 0))
		}
	}

	out := strings.Join(outLines, eol)
	if hadTrailingNewline || len(outLines) > 0 {
		out += eol
	}
	return []byte(out), nil
}

func applyMiseLocalTargets(raw []byte, targets []targetUpdate, keyBytes []byte) ([]byte, error) {
	needVerify := false
	for _, t := range targets {
		if t.lineNum > 0 || t.expectedFingerprint != "" {
			needVerify = true
			break
		}
	}
	if needVerify {
		parsedByKey := map[string]parsedVar{}
		for _, v := range parseMiseEnvBytes(raw) {
			parsedByKey[v.Key] = v
		}
		for _, t := range targets {
			if t.lineNum <= 0 && t.expectedFingerprint == "" {
				continue
			}
			cur, ok := parsedByKey[t.varKey]
			if !ok {
				return nil, fmt.Errorf("expected %s at line %d not found (file changed since planning)", t.varKey, t.lineNum)
			}
			if t.lineNum > 0 && cur.LineNum != t.lineNum {
				return nil, fmt.Errorf("expected %s at line %d, found at line %d (file changed since planning)", t.varKey, t.lineNum, cur.LineNum)
			}
			if t.expectedFingerprint != "" && store.Fingerprint(keyBytes, cur.Value) != t.expectedFingerprint {
				return nil, fmt.Errorf("assignment %s at line %d changed since planning (fingerprint mismatch)", t.varKey, cur.LineNum)
			}
		}
	}

	updates := make(map[string]string, len(targets))
	var orderedKeys []string
	for _, t := range targets {
		if _, dup := updates[t.varKey]; !dup {
			orderedKeys = append(orderedKeys, t.varKey)
		}
		updates[t.varKey] = t.newValue
	}
	return updateMiseLocalContent(raw, updates, orderedKeys)
}

func updateDotenvContent(raw []byte, updates map[string]string, orderedKeys []string) ([]byte, error) {
	targets := make([]targetUpdate, 0, len(orderedKeys))
	for _, k := range orderedKeys {
		targets = append(targets, targetUpdate{
			varKey:   k,
			newValue: updates[k],
		})
	}
	return applyDotenvTargets(raw, targets, nil)
}

type dotenvAssignment struct {
	key           string
	prefix        string
	quoteStyle    byte
	inlineComment string
	endLine       int
}

func inspectDotenvAssignment(lines []string, startIdx int) (dotenvAssignment, bool, error) {
	line := lines[startIdx]
	leadLen := len(line) - len(strings.TrimLeft(line, " \t"))
	indent := line[:leadLen]
	rest := line[leadLen:]
	if rest == "" || strings.HasPrefix(rest, "#") {
		return dotenvAssignment{}, false, nil
	}

	exportPrefix := ""
	if strings.HasPrefix(rest, "export ") || strings.HasPrefix(rest, "export\t") {
		afterExport := rest[len("export"):]
		wsLen := len(afterExport) - len(strings.TrimLeft(afterExport, " \t"))
		exportPrefix = rest[:len("export")+wsLen]
		rest = rest[len(exportPrefix):]
	}

	eqIdx := strings.IndexByte(rest, '=')
	if eqIdx < 0 {
		return dotenvAssignment{}, false, nil
	}
	keyPart := rest[:eqIdx]
	key := strings.TrimSpace(keyPart)
	if !isValidEnvKey(key) {
		return dotenvAssignment{}, false, nil
	}

	afterEq := rest[eqIdx+1:]
	valLeadWS := len(afterEq) - len(strings.TrimLeft(afterEq, " \t"))
	valAndComment := afterEq[valLeadWS:]
	prefix := indent + exportPrefix + keyPart + "=" + afterEq[:valLeadWS]

	if valAndComment == "" {
		return dotenvAssignment{key: key, prefix: prefix, endLine: startIdx}, true, nil
	}

	first := valAndComment[0]
	if first != '"' && first != '\'' {
		quoteStyle, inlineComment := detectDotenvStyleAndComment(valAndComment)
		return dotenvAssignment{
			key:           key,
			prefix:        prefix,
			quoteStyle:    quoteStyle,
			inlineComment: inlineComment,
			endLine:       startIdx,
		}, true, nil
	}

	for ln := startIdx; ln < len(lines); ln++ {
		seg := valAndComment[1:]
		if ln > startIdx {
			seg = lines[ln]
		}
		for pos := 0; pos < len(seg); pos++ {
			if first == '"' && seg[pos] == '\\' {
				if pos+1 < len(seg) {
					pos++
				}
				continue
			}
			if seg[pos] == first {
				return dotenvAssignment{
					key:           key,
					prefix:        prefix,
					quoteStyle:    first,
					inlineComment: seg[pos+1:],
					endLine:       ln,
				}, true, nil
			}
		}
	}
	return dotenvAssignment{}, false, fmt.Errorf("unterminated %c quote for %s at line %d", first, key, startIdx+1)
}

func detectDotenvStyleAndComment(s string) (byte, string) {
	if s == "" {
		return 0, ""
	}
	switch s[0] {
	case '"':
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if s[i] == '"' {
				return '"', s[i+1:]
			}
		}
		return '"', ""
	case '\'':
		if end := strings.IndexByte(s[1:], '\''); end >= 0 {
			return '\'', s[end+2:]
		}
		return '\'', ""
	default:
		if s[0] == '#' {
			return 0, " " + s
		}
		for i := 1; i < len(s); i++ {
			if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
				wsStart := i
				for wsStart > 0 && (s[wsStart-1] == ' ' || s[wsStart-1] == '\t') {
					wsStart--
				}
				return 0, s[wsStart:]
			}
		}
		return 0, ""
	}
}

func formatDotenvValue(val string, preferredQuote byte) string {
	switch preferredQuote {
	case '"':
		return formatDotenvDoubleQuoted(val)
	case '\'':
		if !strings.ContainsAny(val, "'\r\n") {
			return "'" + val + "'"
		}
		return formatDotenvDoubleQuoted(val)
	default:
		if val == "" {
			return ""
		}
		if strings.ContainsAny(val, "$`") && !strings.ContainsAny(val, "'\r\n") {
			return "'" + val + "'"
		}
		if needsDotenvQuoting(val) {
			return formatDotenvDoubleQuoted(val)
		}
		return val
	}
}

func needsDotenvQuoting(val string) bool {
	if strings.TrimSpace(val) != val {
		return true
	}
	return strings.ContainsAny(val, " \t\r\n#\"'$`\\")
}

func formatDotenvDoubleQuoted(val string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + r.Replace(val) + `"`
}

func updateMiseLocalContent(raw []byte, updates map[string]string, orderedKeys []string) ([]byte, error) {
	eol := "\n"
	text := string(raw)
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}

	var lines []string
	if len(text) > 0 {
		trimmedText := strings.TrimSuffix(text, "\n")
		lines = strings.Split(trimmedText, "\n")
	}

	section := ""
	envSectionFound := false
	envInsertIdx := -1
	replaced := map[string]bool{}
	var outLines []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if sec, ok := parseTOMLSectionHeader(trimmed); ok {
			section = sec
			if section == "env" {
				envSectionFound = true
				envInsertIdx = len(outLines) + 1
			}
			outLines = append(outLines, line)
			continue
		}

		if section == "env" && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			updatedLine, key, multiDelim, ok := rewriteTOMLEnvLine(line, updates, false)
			if ok {
				outLines = append(outLines, updatedLine)
				replaced[key] = true
				envInsertIdx = len(outLines)

				if multiDelim != "" {
					for i+1 < len(lines) {
						i++
						if containsUnescapedDelim(lines[i], multiDelim) {
							break
						}
					}
				}
				continue
			}

			if _, rhs, splitOK := splitTOMLKeyVal(trimmed); splitOK {
				if d := opensMultilineTOMLString(rhs); d != "" {
					outLines = append(outLines, line)
					envInsertIdx = len(outLines)
					for i+1 < len(lines) {
						i++
						outLines = append(outLines, lines[i])
						envInsertIdx = len(outLines)
						if containsUnescapedDelim(lines[i], d) {
							break
						}
					}
					continue
				}
			}
			outLines = append(outLines, line)
			envInsertIdx = len(outLines)
			continue
		}

		if section == "" && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			updatedLine, key, multiDelim, ok := rewriteTOMLEnvLine(line, updates, true)
			if ok {
				outLines = append(outLines, updatedLine)
				replaced[key] = true
				if multiDelim != "" {
					for i+1 < len(lines) {
						i++
						if containsUnescapedDelim(lines[i], multiDelim) {
							break
						}
					}
				}
				continue
			}
		}

		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if _, rhs, splitOK := splitTOMLKeyVal(trimmed); splitOK {
				if d := opensMultilineTOMLString(rhs); d != "" {
					outLines = append(outLines, line)
					for i+1 < len(lines) {
						i++
						outLines = append(outLines, lines[i])
						if containsUnescapedDelim(lines[i], d) {
							break
						}
					}
					continue
				}
			}
		}

		outLines = append(outLines, line)
	}

	var missingLines []string
	for _, k := range orderedKeys {
		if !replaced[k] {
			missingLines = append(missingLines, formatTOMLKey(k)+" = "+formatTOMLString(updates[k]))
		}
	}

	if len(missingLines) > 0 {
		if envSectionFound && envInsertIdx >= 0 && envInsertIdx <= len(outLines) {
			head := append([]string{}, outLines[:envInsertIdx]...)
			tail := outLines[envInsertIdx:]
			outLines = append(head, missingLines...)
			outLines = append(outLines, tail...)
		} else {
			if len(outLines) > 0 && strings.TrimSpace(outLines[len(outLines)-1]) != "" {
				outLines = append(outLines, "")
			}
			outLines = append(outLines, "[env]")
			outLines = append(outLines, missingLines...)
		}
	}

	result := strings.Join(outLines, eol)
	if len(outLines) > 0 {
		result += eol
	}

	var check map[string]any
	if err := toml.Unmarshal([]byte(result), &check); err != nil {
		return nil, fmt.Errorf("generated invalid TOML: %w", err)
	}
	return []byte(result), nil
}

func rewriteTOMLEnvLine(line string, updates map[string]string, rootDotted bool) (updated, key, multiDelim string, ok bool) {
	leadLen := len(line) - len(strings.TrimLeft(line, " \t"))
	indent := line[:leadLen]
	rest := line[leadLen:]

	inDouble := false
	inSingle := false
	eqIdx := -1
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if inDouble {
			if c == '\\' && i+1 < len(rest) {
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
		if c == '"' {
			inDouble = true
		} else if c == '\'' {
			inSingle = true
		} else if c == '#' {
			return line, "", "", false
		} else if c == '=' {
			eqIdx = i
			break
		}
	}
	if eqIdx < 0 {
		return line, "", "", false
	}

	rawKeyPart := rest[:eqIdx]
	var k string
	if rootDotted {
		trimmedKey := strings.TrimSpace(rawKeyPart)
		if !strings.HasPrefix(trimmedKey, "env.") {
			return line, "", "", false
		}
		k = unquoteTOMLKey(strings.TrimSpace(strings.TrimPrefix(trimmedKey, "env.")))
	} else {
		k = unquoteTOMLKey(rawKeyPart)
	}
	newVal, found := updates[k]
	if !found {
		return line, "", "", false
	}

	afterEq := rest[eqIdx+1:]
	valLead := len(afterEq) - len(strings.TrimLeft(afterEq, " \t"))
	eqPart := "=" + afterEq[:valLead]
	rhs := afterEq[valLead:]

	multiDelim = opensMultilineTOMLString(rhs)
	quoteStyle, inlineComment := detectTOMLStyleAndComment(rhs)

	var formatted string
	if quoteStyle == '\'' && !strings.ContainsAny(newVal, "'\r\n\t") {
		formatted = "'" + newVal + "'"
	} else {
		formatted = formatTOMLString(newVal)
	}
	return indent + rawKeyPart + eqPart + formatted + inlineComment, k, multiDelim, true
}

func detectTOMLStyleAndComment(rhs string) (byte, string) {
	if rhs == "" {
		return '"', ""
	}
	if strings.HasPrefix(rhs, `"""`) || strings.HasPrefix(rhs, `'''`) {
		return '"', ""
	}
	switch rhs[0] {
	case '"':
		for i := 1; i < len(rhs); i++ {
			if rhs[i] == '\\' && i+1 < len(rhs) {
				i++
				continue
			}
			if rhs[i] == '"' {
				return '"', rhs[i+1:]
			}
		}
		return '"', ""
	case '\'':
		if end := strings.IndexByte(rhs[1:], '\''); end >= 0 {
			return '\'', rhs[end+2:]
		}
		return '\'', ""
	default:
		if idx := strings.IndexByte(rhs, '#'); idx >= 0 {
			wsStart := idx
			for wsStart > 0 && (rhs[wsStart-1] == ' ' || rhs[wsStart-1] == '\t') {
				wsStart--
			}
			return '"', rhs[wsStart:]
		}
		return '"', ""
	}
}

func formatTOMLKey(k string) string {
	for i := 0; i < len(k); i++ {
		c := k[i]
		valid := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
		if !valid {
			return strconv.Quote(k)
		}
	}
	return k
}

func formatTOMLString(val string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range val {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
