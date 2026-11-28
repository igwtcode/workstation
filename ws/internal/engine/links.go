package engine

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

func planLinkEntriesWithPlanned(ctx *app.Context, presetID string, entries []config.LinkEntry, plannedOutputs map[string]bool) (*Plan, []string, error) {
	plan := &Plan{
		Verb:      "apply",
		Host:      FormatPlanHost(ctx),
		Title:     "Sync symlinks",
		Preset:    presetID,
		CreatedAt: time.Now().UTC(),
		Actions:   []Action{},
	}
	var skipped []string

	for _, entry := range entries {
		srcAbs := filepath.Join(ctx.RepoRoot, entry.SourceRel)
		if _, err := os.Lstat(srcAbs); err != nil {
			if !(errors.Is(err, os.ErrNotExist) && isPlannedOutput(plannedOutputs, entry.SourceRel)) {
				return nil, nil, fmt.Errorf("link source missing: %s: %w", entry.SourceRel, err)
			}
		}

		targetAbs := ctx.ExpandHome(entry.TargetRaw)
		disp := ctx.CollapseHome(targetAbs)

		fi, err := os.Lstat(targetAbs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				plan.Actions = append(plan.Actions, Action{
					ID:      "link:" + entry.TargetRaw,
					Kind:    ActionSymlink,
					Target:  targetAbs,
					Source:  srcAbs,
					Summary: fmt.Sprintf("create symlink %s -> %s", disp, entry.SourceRel),
					Detail:  "created",
					Risk:    RiskSafe,
					Metadata: map[string]string{
						"state":     "created",
						"manifest":  entry.Manifest,
						"sourceRel": entry.SourceRel,
						"targetRaw": entry.TargetRaw,
					},
				})
				continue
			}
			return nil, nil, fmt.Errorf("stat %s: %w", targetAbs, err)
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			cur, rerr := os.Readlink(targetAbs)
			if rerr != nil {
				return nil, nil, fmt.Errorf("readlink %s: %w", targetAbs, rerr)
			}
			if cur == srcAbs {
				skipped = append(skipped, disp)
				plan.Skipped = append(plan.Skipped, fmt.Sprintf("ok %s", disp))
				continue
			}
			plan.Actions = append(plan.Actions, Action{
				ID:      "link:" + entry.TargetRaw,
				Kind:    ActionSymlink,
				Target:  targetAbs,
				Source:  srcAbs,
				Summary: fmt.Sprintf("relink %s -> %s", disp, entry.SourceRel),
				Detail:  fmt.Sprintf("relinked (was %s)", cur),
				Risk:    RiskSafe,
				Metadata: map[string]string{
					"state":      "relinked",
					"prevTarget": cur,
					"manifest":   entry.Manifest,
					"sourceRel":  entry.SourceRel,
					"targetRaw":  entry.TargetRaw,
				},
			})
			continue
		}

		backupAbs := targetAbs + ".pre-ws"
		kindName := "file"
		if fi.IsDir() {
			kindName = "directory"
		}
		if _, berr := os.Lstat(backupAbs); berr == nil {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("backup target already exists: %s", ctx.CollapseHome(backupAbs)))
		}
		plan.Actions = append(plan.Actions, Action{
			ID:            "link:" + entry.TargetRaw,
			Kind:          ActionSymlink,
			Target:        targetAbs,
			Source:        srcAbs,
			Summary:       fmt.Sprintf("replace %s %s -> %s (backup: %s.pre-ws)", kindName, disp, entry.SourceRel, disp),
			Detail:        fmt.Sprintf("replaced existing %s; backup at %s", kindName, ctx.CollapseHome(backupAbs)),
			Risk:          RiskMedium,
			RequiresAllow: "replace",
			Metadata: map[string]string{
				"state":     "replaced",
				"backup":    backupAbs,
				"manifest":  entry.Manifest,
				"sourceRel": entry.SourceRel,
				"targetRaw": entry.TargetRaw,
			},
		})
	}

	return plan, skipped, nil
}

func isPlannedOutput(plannedOutputs map[string]bool, sourceRel string) bool {
	if len(plannedOutputs) == 0 {
		return false
	}
	norm := filepath.ToSlash(filepath.Clean(sourceRel))
	if plannedOutputs[norm] {
		return true
	}
	prefix := norm + "/"
	for out := range plannedOutputs {
		if strings.HasPrefix(out, prefix) {
			return true
		}
	}
	return false
}

func ApplySymlink(srcAbs, targetAbs string, allowReplace bool, tx *app.UndoTx) (string, error) {
	srcAbs = filepath.Clean(srcAbs)
	targetAbs = filepath.Clean(targetAbs)
	if _, err := os.Lstat(srcAbs); err != nil {
		return "", fmt.Errorf("symlink source missing %s: %w", srcAbs, err)
	}

	fi, err := os.Lstat(targetAbs)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(targetAbs), 0o755); err != nil {
			return "", err
		}
		if err := os.Symlink(srcAbs, targetAbs); err != nil {
			return "", err
		}
		if tx != nil {
			tx.Ops = append(tx.Ops, app.UndoOp{
				Kind:           "create_symlink",
				Target:         targetAbs,
				PrevExists:     false,
				PostLinkTarget: srcAbs,
			})
		}
		return "created", nil
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		prevTarget, err := os.Readlink(targetAbs)
		if err != nil {
			return "", err
		}
		if prevTarget == srcAbs {
			return "ok", nil
		}
		if err := atomicSymlink(srcAbs, targetAbs); err != nil {
			return "", err
		}
		if tx != nil {
			tx.Ops = append(tx.Ops, app.UndoOp{
				Kind:           "replace_symlink",
				Target:         targetAbs,
				PrevExists:     true,
				PrevIsSymlink:  true,
				PrevLinkTarget: prevTarget,
				PostLinkTarget: srcAbs,
			})
		}
		return "relinked", nil
	}

	if !allowReplace {
		return "", fmt.Errorf("refusing to overwrite existing %s without --allow replace", targetAbs)
	}

	backupAbs := targetAbs + ".pre-ws"
	if _, berr := os.Lstat(backupAbs); berr == nil {
		return "", fmt.Errorf("backup path already exists: %s", backupAbs)
	} else if !errors.Is(berr, os.ErrNotExist) {
		return "", berr
	}
	if err := os.Rename(targetAbs, backupAbs); err != nil {
		return "", fmt.Errorf("backup %s: %w", targetAbs, err)
	}
	if err := os.Symlink(srcAbs, targetAbs); err != nil {
		if rerr := os.Rename(backupAbs, targetAbs); rerr != nil {
			return "", fmt.Errorf("symlink failed (%v) and restoring backup %s failed (%w)", err, backupAbs, rerr)
		}
		return "", err
	}
	if tx != nil {
		tx.Ops = append(tx.Ops, app.UndoOp{
			Kind:           "restore_backup",
			Target:         targetAbs,
			PrevExists:     true,
			PrevMode:       uint32(fi.Mode().Perm()),
			BackupPath:     backupAbs,
			PostLinkTarget: srcAbs,
		})
	}
	return "replaced", nil
}

func atomicSymlink(srcAbs, targetAbs string) error {
	dir := filepath.Dir(targetAbs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		var rnd [8]byte
		_, _ = rand.Read(rnd[:])
		tmpPath := filepath.Join(dir, ".ws-link-"+hex.EncodeToString(rnd[:]))
		if err := os.Symlink(srcAbs, tmpPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				lastErr = err
				continue
			}
			return err
		}
		if err := os.Rename(tmpPath, targetAbs); err != nil {
			_ = os.Remove(tmpPath)
			return err
		}
		return nil
	}
	return lastErr
}
