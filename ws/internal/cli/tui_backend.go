package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/cleanup"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/envmgr"
	"github.com/igwtcode/workstation/ws/internal/fleet"
	"github.com/igwtcode/workstation/ws/internal/insights"
	"github.com/igwtcode/workstation/ws/internal/misemgr"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

var _ tui.Backend = (*TUIBackend)(nil)

type TUIBackend struct {
	ctx           *app.Context
	mu            sync.Mutex
	dbMu          sync.RWMutex
	db            *store.DB
	kr            *store.Keyring
	closed        bool
	envScanned    bool
	outdatedCache map[string]tui.PackageOutdatedInfo
	outdatedAt    time.Time
	miseCache     map[string]cachedMiseInventory
	watchedFP     map[string]string
}

type cachedMiseInventory struct {
	inv *misemgr.Inventory
	at  time.Time
}

func NewTUIBackend(ctx *app.Context) *TUIBackend {
	return &TUIBackend{ctx: ctx}
}

func (b *TUIBackend) OutputHistoryPath() (string, bool) {
	if b == nil || b.ctx == nil {
		return "", false
	}
	base := strings.TrimSpace(b.ctx.WSBaseDir())
	if base == "" {
		return "", false
	}
	return filepath.Join(base, "output_history.json"), !b.ctx.DryRun
}

func (b *TUIBackend) openDB() (*store.DB, *store.Keyring, error) {
	b.dbMu.Lock()
	defer b.dbMu.Unlock()
	if b.closed {
		return nil, nil, fmt.Errorf("tui backend is closed")
	}
	if b.db == nil {
		db, err := store.Open(store.DefaultPath(b.ctx.WSBaseDir()))
		if err != nil {
			return nil, nil, err
		}
		b.db = db
	}
	if b.kr == nil || !b.kr.HasKey() {
		kr, err := store.LoadKey(b.ctx)
		if err != nil {
			return nil, nil, err
		}
		b.kr = kr
	}
	return b.db, b.kr, nil
}

func (b *TUIBackend) withDB(fn func(db *store.DB, kr *store.Keyring) error) error {
	if _, _, err := b.openDB(); err != nil {
		return err
	}
	b.dbMu.RLock()
	defer b.dbMu.RUnlock()
	if b.closed || b.db == nil {
		return fmt.Errorf("tui backend is closed")
	}
	return fn(b.db, b.kr)
}

func (b *TUIBackend) Close() error {
	b.dbMu.Lock()
	defer b.dbMu.Unlock()
	b.closed = true
	if b.db == nil {
		return nil
	}
	err := b.db.Close()
	b.db = nil
	return err
}

func (b *TUIBackend) confirmedCtx(scopes ...string) *app.Context {
	cp := *b.ctx
	if b.ctx.NonInteractive {
		return &cp
	}
	for _, sc := range scopes {
		switch sc {
		case "replace":
			cp.Allow.Replace = true
		case "remove":
			cp.Allow.Remove = true
		case "sudo":
			cp.Allow.Sudo = true
		case "script":
			cp.Allow.Script = true
		}
	}
	return &cp
}

func (b *TUIBackend) HostSummary() (tui.HostBadge, error) {
	themeID, err := b.ActiveTheme()
	if err != nil {
		return tui.HostBadge{}, err
	}
	w, err := shell.Whoami(b.ctx, "")
	if err != nil {
		return tui.HostBadge{}, err
	}
	zone := ""
	if w != nil {
		zone = w.ActiveZone
	}
	cwd := projects.DetectCWDContext("")
	return tui.HostBadge{
		OS:           b.ctx.Host.OS,
		Distro:       b.ctx.Host.Distro,
		Arch:         b.ctx.Host.Arch,
		Preset:       b.ctx.EffectivePreset(),
		Zone:         zone,
		Theme:        themeID,
		Headless:     b.ctx.Host.Headless,
		PkgManagers:  b.ctx.Host.PkgManagers,
		WorkDir:      cwd.WorkDir,
		CWDRepoPath:  cwd.RepoRoot,
		CWDRepoSlug:  cwd.RepoSlug,
		CWDBranch:    cwd.Branch,
		CWDIssueHint: cwd.IssueHint,
		GHConfigDir:  strings.TrimSpace(os.Getenv("GH_CONFIG_DIR")),
	}, nil
}

func (b *TUIBackend) ScanEnv() error {
	return b.withDB(func(db *store.DB, kr *store.Keyring) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, err := scanAndRecordEnv(b.ctx, db, kr, time.Now()); err != nil {
			return err
		}
		b.envScanned = true
		return nil
	})
}

func (b *TUIBackend) ensureScanned(db *store.DB, kr *store.Keyring) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.envScanned {
		return nil
	}
	if err := ensureEnvIndexed(b.ctx, db, kr, false); err != nil {
		return err
	}
	b.envScanned = true
	return nil
}

func (b *TUIBackend) LoadEnvVars(query, zone string) ([]tui.EnvVarRow, error) {
	var rows []tui.EnvVarRow
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		if err := b.ensureScanned(db, kr); err != nil {
			return err
		}
		sums, err := envmgr.ListSummariesForZone(db, query, zone, false)
		if err != nil {
			return err
		}
		rows = make([]tui.EnvVarRow, 0, len(sums))
		for _, s := range sums {
			rows = append(rows, tui.EnvVarRow{
				VarKey:           s.VarKey,
				Zone:             s.Zone,
				ActiveSourceKind: s.ActiveSourceKind,
				ActiveSourcePath: s.ActiveSourcePath,
				ActiveMasked:     s.ActiveMaskedValue,
				Fingerprint:      s.Fingerprint,
				IsSecret:         s.IsSecret,
				SourceCount:      s.SourceCount,
				ShadowedCount:    s.ShadowedCount,
			})
		}
		return nil
	})
	return rows, err
}

func (b *TUIBackend) LoadEnvFiles(zone string) ([]tui.EnvFileRow, error) {
	var rows []tui.EnvFileRow
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		if err := b.ensureScanned(db, kr); err != nil {
			return err
		}
		res, err := queryEnvFiles(b.ctx, db, zone)
		if err != nil {
			return err
		}
		rows = res
		return nil
	})
	return rows, err
}

func (b *TUIBackend) LoadEnvDetail(varKey, targetDir string) (tui.EnvDetail, error) {
	scopeDir := strings.TrimSpace(targetDir)
	if scopeDir != "" {
		expanded := filepath.Clean(b.ctx.ExpandHome(scopeDir))
		if fi, serr := os.Stat(expanded); serr == nil && !fi.IsDir() {
			expanded = filepath.Dir(expanded)
		}
		cleanHome := filepath.Clean(b.ctx.HomeDir)
		globalMise := filepath.Clean(filepath.Join(b.ctx.ConfigDir, "mise"))
		if expanded == cleanHome || expanded == globalMise {
			scopeDir = ""
		} else {
			scopeDir = expanded
		}
	}

	var out tui.EnvDetail
	err := b.withDB(func(db *store.DB, _ *store.Keyring) error {
		rep, err := envmgr.GetProvenance(b.ctx, db, varKey, scopeDir)
		if err != nil {
			return err
		}
		isSecret := false
		layers := make([]tui.EnvLayerRow, 0, len(rep.Layers))
		for _, l := range rep.Layers {
			if l.IsSecret {
				isSecret = true
			}
			layers = append(layers, tui.EnvLayerRow{
				Status:      l.Status,
				VarKey:      l.VarKey,
				Zone:        l.Zone,
				SourceKind:  l.SourceKind,
				SourcePath:  l.SourcePath,
				LineNum:     l.LineNum,
				MaskedValue: l.MaskedValue,
				Fingerprint: l.Fingerprint,
				IsSecret:    l.IsSecret,
			})
		}
		impacts := make([]tui.EnvLayerRow, 0, len(rep.ImpactMatches))
		for _, m := range rep.ImpactMatches {
			if m.IsSecret {
				isSecret = true
			}
			st := envmgr.StatusShadowed
			if m.IsActive {
				st = envmgr.StatusActive
			}
			impacts = append(impacts, tui.EnvLayerRow{
				Status:      st,
				VarKey:      m.VarKey,
				Zone:        m.Zone,
				SourceKind:  m.SourceKind,
				SourcePath:  m.SourcePath,
				LineNum:     m.LineNum,
				MaskedValue: m.MaskedValue,
				Fingerprint: m.Fingerprint,
				IsSecret:    m.IsSecret,
			})
		}
		out = tui.EnvDetail{
			VarKey:       rep.VarKey,
			ActiveMasked: rep.ActiveValueMasked,
			Fingerprint:  rep.Fingerprint,
			IsSecret:     isSecret,
			Layers:       layers,
			BlastRadius:  impacts,
		}
		return nil
	})
	return out, err
}

func (b *TUIBackend) RevealEnvValue(sourcePath, varKey string) (string, error) {
	var val string
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		v, err := envmgr.RevealValue(b.ctx, db, kr, sourcePath, varKey)
		if err != nil {
			return err
		}
		val = v
		return nil
	})
	return val, err
}

func normalizeRotateZone(zone string) string {
	z := strings.TrimSpace(zone)
	if strings.EqualFold(z, "global") {
		return ""
	}
	return z
}

func (b *TUIBackend) PreviewEnvRotate(req tui.EnvRotateReq) (tui.EnvRotatePreview, error) {
	var out tui.EnvRotatePreview
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		plan, err := envmgr.PlanRotate(b.ctx, db, kr, envmgr.RotateRequest{
			VarKey:           req.VarKey,
			MatchFingerprint: req.MatchFingerprint,
			Zone:             normalizeRotateZone(req.Zone),
			Paths:            req.Paths,
			OnlyExisting:     true,
		}, req.NewValue)
		if err != nil {
			return err
		}
		fpByPath := map[string]string{}
		if rep, rerr := envmgr.GetProvenance(b.ctx, db, plan.VarKey, ""); rerr == nil && rep != nil {
			for _, m := range rep.ImpactMatches {
				fpByPath[fmt.Sprintf("%s:%d", m.SourcePath, m.LineNum)] = m.Fingerprint
			}
		}
		targets := make([]tui.EnvRotateTargetRow, 0, len(plan.Targets))
		for _, t := range plan.Targets {
			targets = append(targets, tui.EnvRotateTargetRow{
				SourcePath:  t.SourcePath,
				SourceKind:  t.SourceKind,
				Zone:        t.Zone,
				LineNum:     t.LineNum,
				OldMasked:   t.OldMasked,
				NewMasked:   t.NewMasked,
				Fingerprint: fpByPath[fmt.Sprintf("%s:%d", t.SourcePath, t.LineNum)],
			})
		}
		out = tui.EnvRotatePreview{
			VarKey:    plan.VarKey,
			NewMasked: plan.NewMasked,
			Targets:   targets,
		}
		return nil
	})
	return out, err
}

func (b *TUIBackend) ExecEnvRotate(req tui.EnvRotateReq) (tui.EnvRotateResult, error) {
	var out tui.EnvRotateResult
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		if _, err := envmgr.Scan(b.ctx, db, kr); err != nil {
			return err
		}
		runCtx := b.confirmedCtx("replace")
		plan, err := envmgr.PlanRotate(runCtx, db, kr, envmgr.RotateRequest{
			VarKey:           req.VarKey,
			MatchFingerprint: req.MatchFingerprint,
			Zone:             normalizeRotateZone(req.Zone),
			Paths:            req.Paths,
			OnlyExisting:     true,
		}, req.NewValue)
		if err != nil {
			return err
		}
		res, err := envmgr.ExecRotate(runCtx, db, kr, plan, req.NewValue)
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("Rotated %s in %d file(s) (tx %s)", plan.VarKey, len(res.UpdatedFiles), res.TxID)
		if res.DryRun || runCtx.DryRun {
			summary = fmt.Sprintf("[dry-run] Would rotate %s in %d file(s)", plan.VarKey, len(res.UpdatedFiles))
		}
		out = tui.EnvRotateResult{
			TxID:         res.TxID,
			UpdatedCount: len(res.UpdatedFiles),
			Summary:      summary,
		}
		return nil
	})
	return out, err
}

func mapRepoRecordsToTUI(records []projects.RepoRecord) []tui.RepoRow {
	rows := make([]tui.RepoRow, 0, len(records))
	for _, r := range records {
		locals := make([]tui.LocalManifestEntry, 0, len(r.UntrackedLocals))
		for _, loc := range r.UntrackedLocals {
			locals = append(locals, tui.LocalManifestEntry{
				RelPath:   loc.RelPath,
				SizeBytes: loc.SizeBytes,
				BackedUp:  loc.BackedUp,
			})
		}
		rows = append(rows, tui.RepoRow{
			Name:      r.Name,
			Zone:      r.Zone,
			Forge:     r.Forge,
			RemoteURL: r.RemoteURL,
			LocalPath: r.LocalPath,
			Branch:    r.Branch,
			Dirty:     r.Dirty,
			Ahead:     r.Ahead,
			Behind:    r.Behind,
			Locals:    locals,
		})
	}
	return rows
}

func (b *TUIBackend) LoadRepos(refresh bool, zone string) ([]tui.RepoRow, error) {
	var records []projects.RepoRecord
	err := b.withDB(func(db *store.DB, _ *store.Keyring) error {
		if !refresh {
			cached, cerr := projects.ListCachedRepos(db)
			if cerr != nil {
				return cerr
			}
			if len(cached) > 0 {
				records = cached
			}
		}
		if len(records) == 0 {
			scanned, err := projects.ScanRepos(b.ctx, db)
			if err != nil {
				return err
			}
			records = scanned
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if z := strings.TrimSpace(zone); z != "" {
		filtered := make([]projects.RepoRecord, 0, len(records))
		for _, r := range records {
			if strings.EqualFold(r.Zone, z) {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}
	return mapRepoRecordsToTUI(records), nil
}

func mapGHItemToTUI(it projects.GHItem) tui.GHItemRow {
	upd := ""
	if it.UpdatedAt > 0 {
		upd = time.Unix(it.UpdatedAt, 0).Local().Format("2006-01-02")
	}
	return tui.GHItemRow{
		Kind:          it.Kind,
		Repo:          it.Repo,
		Number:        it.Number,
		Title:         it.Title,
		State:         it.State,
		Author:        it.Author,
		URL:           it.URL,
		Zone:          it.Zone,
		Body:          it.Body,
		Labels:        append([]string(nil), it.Labels...),
		Assignees:     append([]string(nil), it.Assignees...),
		Milestone:     it.Milestone,
		IsDraft:       it.IsDraft,
		CommentsCount: it.CommentsCount,
		HeadRef:       it.HeadRef,
		UpdatedAt:     upd,
		FetchedAt:     it.FetchedAt,
		Source:        it.Source,
	}
}

func (b *TUIBackend) LoadGHItems(refresh bool, zone string) ([]tui.GHItemRow, error) {
	db, _, err := b.openDB()
	if err != nil {
		return nil, err
	}
	items, _, err := projects.FetchGitHubItemsForZone(b.ctx, db, zone, refresh, nil)
	if err != nil {
		return nil, err
	}
	z := strings.TrimSpace(zone)
	if strings.EqualFold(z, "all") {
		z = ""
	}
	rows := make([]tui.GHItemRow, 0, len(items))
	for _, it := range items {
		if z != "" && !strings.EqualFold(it.Zone, z) {
			continue
		}
		rows = append(rows, mapGHItemToTUI(it))
	}
	return rows, nil
}

func (b *TUIBackend) loadRepoRecordsForMise(zone string) []projects.RepoRecord {
	var records []projects.RepoRecord
	_ = b.withDB(func(db *store.DB, _ *store.Keyring) error {
		if cached, err := projects.ListCachedRepos(db); err == nil && len(cached) > 0 {
			records = cached
			return nil
		}
		if scanned, err := projects.ScanRepos(b.ctx, db); err == nil {
			records = scanned
		}
		return nil
	})
	if len(records) == 0 {
		records, _ = projects.ScanRepos(b.ctx, nil)
	}
	if z := strings.TrimSpace(zone); z != "" && !strings.EqualFold(z, "all") {
		var filtered []projects.RepoRecord
		for _, r := range records {
			if strings.EqualFold(r.Zone, z) {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}
	return records
}

func (b *TUIBackend) scanMiseInventoryCached(zone string, checkOutdated, force bool) (*misemgr.Inventory, error) {
	key := strings.ToLower(strings.TrimSpace(zone))
	if key == "" {
		key = "all"
	}
	if !force {
		b.mu.Lock()
		if b.miseCache != nil {
			if entry, ok := b.miseCache[key]; ok && entry.inv != nil && time.Since(entry.at) < 5*time.Minute {
				invCopy := entry.inv
				b.mu.Unlock()
				return invCopy, nil
			}
		}
		b.mu.Unlock()
	}

	repos := b.loadRepoRecordsForMise(zone)
	inv, err := misemgr.Scan(b.ctx, repos, zone, checkOutdated)
	if err != nil {
		return nil, err
	}
	if checkOutdated {
		b.mu.Lock()
		if b.miseCache == nil {
			b.miseCache = map[string]cachedMiseInventory{}
		}
		b.miseCache[key] = cachedMiseInventory{inv: inv, at: time.Now()}
		b.mu.Unlock()
	}
	return inv, nil
}

func (b *TUIBackend) LoadMiseInventory(zone string, checkOutdated, force bool) (tui.MiseInventoryView, error) {
	inv, err := b.scanMiseInventoryCached(zone, checkOutdated, force)
	if err != nil {
		return tui.MiseInventoryView{}, err
	}
	configs := make([]tui.MiseConfigRow, 0, len(inv.Configs))
	for _, c := range inv.Configs {
		decls := make([]tui.MiseToolDecl, 0, len(c.Tools))
		for _, td := range c.Tools {
			decls = append(decls, tui.MiseToolDecl{
				Name:      td.Name,
				Requested: td.Requested,
				Backend:   td.Backend,
				Line:      td.Line,
			})
		}
		configs = append(configs, tui.MiseConfigRow{
			Path:          c.Path,
			Kind:          c.Kind,
			Scope:         c.Scope,
			Zone:          c.Zone,
			ProjectName:   c.ProjectName,
			ProjectPath:   c.ProjectPath,
			Forge:         c.Forge,
			GitHubSlug:    c.GitHubSlug,
			RemoteURL:     c.RemoteURL,
			Branch:        c.Branch,
			Dirty:         c.Dirty,
			LocalOverride: c.LocalOverride,
			Tools:         decls,
			EnvKeys:       append([]string(nil), c.EnvKeys...),
			Tasks:         append([]string(nil), c.Tasks...),
			OutdatedCount: c.OutdatedCount,
			MissingCount:  c.MissingCount,
		})
	}
	tools := make([]tui.MiseToolRow, 0, len(inv.Tools))
	for _, t := range inv.Tools {
		tools = append(tools, tui.MiseToolRow{
			Name:        t.Name,
			Requested:   t.Requested,
			Version:     t.Version,
			Latest:      t.Latest,
			InstallPath: t.InstallPath,
			SourcePath:  t.SourcePath,
			Scope:       t.Scope,
			Zone:        t.Zone,
			ProjectName: t.ProjectName,
			ProjectPath: t.ProjectPath,
			Forge:       t.Forge,
			GitHubSlug:  t.GitHubSlug,
			Installed:   t.Installed,
			Active:      t.Active,
			Outdated:    t.Outdated,
			Status:      t.Status,
		})
	}
	return tui.MiseInventoryView{
		MiseVersion:   inv.MiseVersion,
		OutdatedKnown: checkOutdated,
		OutdatedCount: inv.OutdatedCount,
		MissingCount:  inv.MissingCount,
		Configs:       configs,
		Tools:         tools,
		Warnings:      append([]string(nil), inv.Warnings...),
	}, nil
}

func (b *TUIBackend) EditMiseFileCmd(path string) (*exec.Cmd, error) {
	clean, err := misemgr.ValidateEditableMisePath(b.ctx, path)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.miseCache = nil
	b.mu.Unlock()
	return resolveEditorCmd(clean), nil
}

func (b *TUIBackend) UpgradeMiseTools(tools []tui.MiseToolRow) (tui.CommandRunResult, error) {
	return b.UpgradeMiseToolsStream(context.Background(), tools, nil)
}

func (b *TUIBackend) UpgradeMiseToolsStream(ctx context.Context, tools []tui.MiseToolRow, onLine func(string)) (tui.CommandRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(tools) == 0 {
		return tui.CommandRunResult{}, fmt.Errorf("no mise tools selected for upgrade")
	}
	if _, err := exec.LookPath("mise"); err != nil {
		return tui.CommandRunResult{ExitCode: 1, IsErr: true, Output: err.Error()}, err
	}

	type dirBatch struct {
		dir   string
		names []string
		seen  map[string]bool
	}
	batchesMap := map[string]*dirBatch{}
	var order []string
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		dir := strings.TrimSpace(t.ProjectPath)
		if dir == "" {
			dir = b.ctx.HomeDir
		}
		if dir == "" {
			dir = "."
		}
		dbatch, ok := batchesMap[dir]
		if !ok {
			dbatch = &dirBatch{dir: dir, seen: map[string]bool{}}
			batchesMap[dir] = dbatch
			order = append(order, dir)
		}
		if !dbatch.seen[name] {
			dbatch.seen[name] = true
			dbatch.names = append(dbatch.names, name)
		}
	}
	if len(order) == 0 {
		return tui.CommandRunResult{}, fmt.Errorf("no valid tool names to upgrade")
	}

	start := time.Now()
	var logs []string
	var lastErr error
	exitCode := 0
	for _, dir := range order {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			exitCode = 130
			break
		}
		batch := batchesMap[dir]
		args := append([]string{"upgrade"}, batch.names...)
		hdr := fmt.Sprintf("[%s] mise %s", dir, strings.Join(args, " "))
		logs = append(logs, hdr)
		if onLine != nil {
			onLine(hdr)
		}
		cmd := exec.CommandContext(ctx, "mise", args...)
		cmd.Dir = dir
		raw, code, err := runCommandStreaming(ctx, cmd, onLine)
		if trimmed := strings.TrimRight(raw, "\r\n"); trimmed != "" {
			logs = append(logs, trimmed)
		}
		if err != nil {
			lastErr = err
			exitCode = code
			if errors.Is(err, context.Canceled) {
				break
			}
		}
	}

	b.mu.Lock()
	b.miseCache = nil
	b.outdatedAt = time.Time{}
	b.mu.Unlock()

	return tui.CommandRunResult{
		Command:  fmt.Sprintf("mise upgrade (%d tool(s))", len(tools)),
		ExitCode: exitCode,
		Duration: time.Since(start),
		Output:   strings.Join(logs, "\n"),
		IsErr:    lastErr != nil,
	}, lastErr
}

func (b *TUIBackend) LoadCachedRepoGHItems(repo, state string) ([]tui.GHItemRow, int64, error) {
	db, _, err := b.openDB()
	if err != nil {
		return nil, 0, err
	}
	items, fetchedAt, err := projects.LoadCachedRepoGHItems(db, repo, state)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]tui.GHItemRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, mapGHItemToTUI(it))
	}
	return rows, fetchedAt, nil
}

func (b *TUIBackend) LoadRepoGHItems(repo, zone, state string, refresh bool) ([]tui.GHItemRow, error) {
	db, _, err := b.openDB()
	if err != nil {
		return nil, err
	}
	items, err := projects.FetchRepoGitHubItems(b.ctx, db, repo, zone, state, refresh, nil)
	if err != nil {
		return nil, err
	}
	rows := make([]tui.GHItemRow, 0, len(items))
	for _, it := range items {
		rows = append(rows, mapGHItemToTUI(it))
	}
	return rows, nil
}

func (b *TUIBackend) LoadGHItemDetail(kind, repo, zone string, number int, refresh bool) (tui.GHItemDetail, error) {
	db, _, _ := b.openDB()
	det, err := projects.FetchGHItemDetailCached(b.ctx, db, kind, repo, zone, number, refresh, nil)
	if err != nil {
		return tui.GHItemDetail{}, err
	}
	comments := make([]tui.GHCommentRow, 0, len(det.Comments))
	for _, c := range det.Comments {
		ts := ""
		if c.CreatedAt > 0 {
			ts = time.Unix(c.CreatedAt, 0).Local().Format("2006-01-02 15:04")
		}
		comments = append(comments, tui.GHCommentRow{
			Author:    c.Author,
			Body:      c.Body,
			CreatedAt: ts,
		})
	}
	return tui.GHItemDetail{
		GHItemRow:      mapGHItemToTUI(det.GHItem),
		HeadRef:        det.HeadRef,
		BaseRef:        det.BaseRef,
		ReviewDecision: det.ReviewDecision,
		Mergeable:      det.Mergeable,
		ChecksSummary:  det.ChecksSummary,
		Additions:      det.Additions,
		Deletions:      det.Deletions,
		ChangedFiles:   det.ChangedFiles,
		Comments:       comments,
	}, nil
}

func (b *TUIBackend) PullRepos() (tui.RepoPullResult, error) {
	return b.PullSelectedRepos(nil)
}

func (b *TUIBackend) PullSelectedRepos(paths []string) (tui.RepoPullResult, error) {
	var repos []projects.RepoRecord
	err := b.withDB(func(db *store.DB, _ *store.Keyring) error {
		needScan := true
		if len(paths) > 0 {
			if cached, cerr := projects.ListCachedRepos(db); cerr == nil && len(cached) > 0 {
				cachedPaths := make(map[string]bool, len(cached))
				for _, r := range cached {
					cachedPaths[filepath.Clean(r.LocalPath)] = true
				}
				allFound := true
				for _, p := range paths {
					clean := filepath.Clean(b.ctx.ExpandHome(strings.TrimSpace(p)))
					if clean != "" && clean != "." && !cachedPaths[clean] {
						allFound = false
						break
					}
				}
				if allFound {
					repos = cached
					needScan = false
				}
			}
		}
		if needScan {
			scanned, serr := projects.ScanRepos(b.ctx, db)
			if serr != nil {
				return serr
			}
			repos = scanned
		}
		return nil
	})
	if err != nil {
		return tui.RepoPullResult{}, err
	}
	if len(paths) > 0 {
		want := make(map[string]bool, len(paths))
		for _, p := range paths {
			clean := filepath.Clean(b.ctx.ExpandHome(strings.TrimSpace(p)))
			if clean != "" && clean != "." {
				want[clean] = true
			}
		}
		var subset []projects.RepoRecord
		for _, r := range repos {
			if want[filepath.Clean(r.LocalPath)] {
				subset = append(subset, r)
			}
		}
		repos = subset
	}
	res, err := projects.BatchPull(b.ctx, repos)
	if err != nil {
		return tui.RepoPullResult{}, err
	}
	summary := fmt.Sprintf("Pull: %d updated, %d up-to-date, %d dirty, %d failed",
		len(res.Updated), len(res.UpToDate), len(res.Dirty), len(res.Failed))
	if b.ctx.DryRun {
		summary = "[dry-run] " + summary
	}
	lines := make([]string, 0, len(res.Items)+len(res.NeedsInteractive))
	for _, it := range res.Items {
		label := it.Rel
		if label == "" {
			label = b.ctx.CollapseHome(it.Path)
		}
		switch it.Status {
		case shell.PullUpdated:
			lines = append(lines, "✓ "+label+" (updated)")
		case shell.PullUpToDate:
			lines = append(lines, "= "+label+" (up-to-date)")
		case shell.PullDryRun:
			lines = append(lines, "~ "+label+" (dry-run)")
		case shell.PullDirty:
			lines = append(lines, "● "+label+" (dirty — skipped)")
		case shell.PullNoUpstream:
			lines = append(lines, "- "+label+" (no upstream)")
		case shell.PullFailed:
			errMsg := strings.TrimSpace(it.Error)
			if errMsg != "" {
				lines = append(lines, "✗ "+label+": "+errMsg)
			} else {
				lines = append(lines, "✗ "+label+" (failed)")
			}
		}
	}
	for _, nr := range res.NeedsInteractive {
		lines = append(lines, "! "+nr.Name+" (interactive CodeCommit pull required)")
	}
	return tui.RepoPullResult{
		Summary:          summary,
		Updated:          len(res.Updated),
		UpToDate:         len(res.UpToDate),
		Dirty:            len(res.Dirty),
		Failed:           len(res.Failed),
		NeedsInteractive: mapRepoRecordsToTUI(res.NeedsInteractive),
		Lines:            lines,
	}, nil
}

func (b *TUIBackend) InteractivePullCmd(repo tui.RepoRow) (*exec.Cmd, error) {
	return projects.BuildInteractivePullCmd(b.ctx, projects.RepoRecord{
		Name:      repo.Name,
		Zone:      repo.Zone,
		Forge:     repo.Forge,
		RemoteURL: repo.RemoteURL,
		LocalPath: repo.LocalPath,
	}), nil
}

func (b *TUIBackend) CloneRepo(source, zone string) (string, error) {
	plan, err := projects.PlanClone(b.ctx, projects.CloneRequest{
		Source: source,
		Zone:   zone,
	})
	if err != nil {
		return "", err
	}
	if err := projects.ExecClone(b.ctx, plan, nil); err != nil {
		return "", err
	}
	if b.ctx.DryRun {
		return fmt.Sprintf("[dry-run] Would clone %s -> %s", source, b.ctx.CollapseHome(plan.DestDir)), nil
	}
	return fmt.Sprintf("Cloned %s -> %s", source, b.ctx.CollapseHome(plan.DestDir)), nil
}

type savedFleetNode struct {
	Name         string
	Provider     string
	TailscaleIP  string
	OS           string
	Arch         string
	Tags         []string
	Preset       string
	NoZones      bool
	Checklist    []tui.FleetChecklistItem
	LastSyncedAt int64
}

func loadSavedFleetNodes(db *store.DB) (map[string]savedFleetNode, error) {
	out := map[string]savedFleetNode{}
	if db == nil || db.SQL() == nil {
		return out, fmt.Errorf("database is not open")
	}
	rows, err := db.SQL().Query(
		`SELECT name, provider, tailscale_ip, os, arch, tags, preset, no_zones, checklist_json, last_synced_at
		 FROM fleet_nodes ORDER BY name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var s savedFleetNode
		var tagsCSV, chkJSON string
		var noZonesInt int
		if err := rows.Scan(
			&s.Name, &s.Provider, &s.TailscaleIP, &s.OS, &s.Arch,
			&tagsCSV, &s.Preset, &noZonesInt, &chkJSON, &s.LastSyncedAt,
		); err != nil {
			return nil, err
		}
		s.NoZones = noZonesInt != 0
		if tagsCSV != "" {
			s.Tags = flattenCSV([]string{tagsCSV})
		}
		if chkJSON != "" {
			var items []fleet.ChecklistItem
			if uerr := json.Unmarshal([]byte(chkJSON), &items); uerr != nil {
				return nil, fmt.Errorf("decode checklist for fleet node %s: %w", s.Name, uerr)
			}
			for _, it := range items {
				s.Checklist = append(s.Checklist, tui.FleetChecklistItem{
					Done:    it.Done,
					Title:   it.Title,
					Command: it.Command,
				})
			}
		}
		out[s.Name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *TUIBackend) LoadFleetNodes() ([]tui.FleetNodeRow, error) {
	db, _, err := b.openDB()
	if err != nil {
		return nil, err
	}
	saved, err := loadSavedFleetNodes(db)
	if err != nil {
		return nil, err
	}

	tsNodes, tsErr := fleet.ListTailscaleNodes(b.ctx, nil)
	rows := make([]tui.FleetNodeRow, 0, len(tsNodes)+len(saved))
	seen := map[string]bool{}

	for _, n := range tsNodes {
		seen[n.Name] = true
		row := tui.FleetNodeRow{
			Name:        n.Name,
			DNSName:     n.DNSName,
			TailscaleIP: n.TailscaleIP,
			OS:          n.OS,
			Arch:        n.Arch,
			Provider:    n.Provider,
			TargetID:    n.TargetID,
			Preset:      "ubuntu-runner",
			Online:      n.Online,
			SSHEnabled:  n.SSHEnabled,
			IsSelf:      n.IsSelf,
			Tags:        n.Tags,
		}
		if s, ok := saved[n.Name]; ok {
			if s.Preset != "" {
				row.Preset = s.Preset
			}
			row.NoZones = s.NoZones
			row.Checklist = s.Checklist
			if s.LastSyncedAt > 0 {
				row.LastSynced = time.Unix(s.LastSyncedAt, 0).Local().Format("2006-01-02 15:04")
			}
		}
		rows = append(rows, row)
	}

	var extraNames []string
	for name := range saved {
		if !seen[name] {
			extraNames = append(extraNames, name)
		}
	}
	sort.Strings(extraNames)
	for _, name := range extraNames {
		s := saved[name]
		lastSynced := ""
		if s.LastSyncedAt > 0 {
			lastSynced = time.Unix(s.LastSyncedAt, 0).Local().Format("2006-01-02 15:04")
		}
		targetID := s.TailscaleIP
		if targetID == "" {
			targetID = s.Name
		}
		rows = append(rows, tui.FleetNodeRow{
			Name:        s.Name,
			TailscaleIP: s.TailscaleIP,
			OS:          s.OS,
			Arch:        s.Arch,
			Provider:    s.Provider,
			TargetID:    targetID,
			Preset:      s.Preset,
			Tags:        s.Tags,
			NoZones:     s.NoZones,
			Checklist:   s.Checklist,
			LastSynced:  lastSynced,
		})
	}

	if len(rows) == 0 && tsErr != nil {
		return nil, tsErr
	}
	return rows, nil
}

func (b *TUIBackend) TailscaleJoinCmd(hostname string, tags []string) (string, error) {
	return fleet.RenderJoinCommand(fleet.JoinOptions{
		Hostname:   hostname,
		Tags:       tags,
		AuthKeyEnv: "TAILSCALE_AUTHKEY",
		SSH:        true,
	})
}

func buildFleetSyncSpec(req tui.FleetSyncReq) fleet.SyncSpec {
	return fleet.SyncSpec{
		Node:          req.Node,
		Transport:     req.Transport,
		TargetID:      req.TargetID,
		Preset:        req.Preset,
		Arch:          req.Arch,
		NoZones:       req.NoZones,
		PushEnvFiles:  req.PushEnvFiles,
		SelectedRepos: req.SelectedRepos,
	}
}

func (b *TUIBackend) PreviewFleetSync(req tui.FleetSyncReq) (tui.FleetSyncPreview, error) {
	db, kr, err := b.openDB()
	if err != nil {
		return tui.FleetSyncPreview{}, err
	}
	dryCtx := *b.ctx
	dryCtx.DryRun = true
	plan, err := fleet.PlanFleetSync(&dryCtx, db, kr, buildFleetSyncSpec(req))
	if err != nil {
		return tui.FleetSyncPreview{}, err
	}
	steps := make([]string, 0, len(plan.Steps))
	for _, st := range plan.Steps {
		steps = append(steps, st.Title+": "+st.Detail)
	}
	checklist := make([]tui.FleetChecklistItem, 0, len(plan.ManualChecklist))
	for _, it := range plan.ManualChecklist {
		checklist = append(checklist, tui.FleetChecklistItem{
			Done:    it.Done,
			Title:   it.Title,
			Command: it.Command,
		})
	}
	return tui.FleetSyncPreview{
		Summary:        fmt.Sprintf("%d step(s) for %s via %s (preset %s)", len(plan.Steps), plan.Spec.Node, plan.Spec.Transport, plan.Spec.Preset),
		SkippedConfigs: plan.SkippedConfigs,
		EnvFilesCount:  plan.EnvFilesCount,
		Steps:          steps,
		Checklist:      checklist,
	}, nil
}

func (b *TUIBackend) ExecFleetSync(req tui.FleetSyncReq) (string, error) {
	db, kr, err := b.openDB()
	if err != nil {
		return "", err
	}
	runCtx := *b.ctx
	runCtx.DryRun = b.ctx.DryRun || req.DryRun
	plan, err := fleet.PlanFleetSync(&runCtx, db, kr, buildFleetSyncSpec(req))
	if err != nil {
		return "", err
	}
	if err := fleet.ExecFleetSync(&runCtx, db, kr, plan, nil); err != nil {
		return "", err
	}
	if runCtx.DryRun {
		return fmt.Sprintf("[dry-run] Would sync %s (%d step(s))", plan.Spec.Node, len(plan.Steps)), nil
	}
	return fmt.Sprintf("Synced %s (%d step(s))", plan.Spec.Node, len(plan.Steps)), nil
}

func (b *TUIBackend) FleetConnectCmd(node tui.FleetNodeRow) (*exec.Cmd, error) {
	target := strings.TrimSpace(node.DNSName)
	if target == "" {
		target = strings.TrimSpace(node.TailscaleIP)
	}
	if target == "" {
		target = strings.TrimSpace(node.Name)
	}
	if err := fleet.ValidateNodeTarget(target); err != nil {
		return nil, err
	}
	c := exec.Command("ssh", "-o", "StrictHostKeyChecking=accept-new", "--", target)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c, nil
}

func (b *TUIBackend) DiffFleetFiles(node, targetID string, includeEnv, includeDB bool) (tui.FleetFileDiffView, error) {
	rep, err := fleet.DiffNodeFiles(b.ctx, node, targetID, fleet.FileSyncDiffOptions{
		IncludeEnv: includeEnv,
		IncludeDB:  includeDB,
	})
	entries := make([]tui.FleetFileDiffRow, 0, len(rep.Entries))
	for _, e := range rep.Entries {
		entries = append(entries, tui.FleetFileDiffRow{
			RelPath:  e.RelPath,
			Root:     e.Root,
			Category: e.Category,
			Status:   e.Status,
			IsSecret: e.IsSecret,
			Local: tui.FleetFileMeta{
				Exists:    e.Local.Exists,
				SHA256:    e.Local.SHA256,
				SizeBytes: e.Local.SizeBytes,
				ModTime:   e.Local.ModTime,
				Mode:      e.Local.Mode,
			},
			Remote: tui.FleetFileMeta{
				Exists:    e.Remote.Exists,
				SHA256:    e.Remote.SHA256,
				SizeBytes: e.Remote.SizeBytes,
				ModTime:   e.Remote.ModTime,
				Mode:      e.Remote.Mode,
			},
		})
	}
	view := tui.FleetFileDiffView{
		Node:            nonEmptyCLI(rep.Node, node),
		TargetID:        nonEmptyCLI(rep.TargetID, targetID),
		IncludeEnv:      rep.IncludeEnv,
		IncludeDB:       rep.IncludeDB,
		Roots:           rep.Roots,
		Entries:         entries,
		SameCount:       rep.SameCount,
		DiffCount:       rep.DiffCount,
		LocalOnlyCount:  rep.LocalOnlyCount,
		RemoteOnlyCount: rep.RemoteOnlyCount,
		Warnings:        rep.Warnings,
	}
	return view, err
}

func (b *TUIBackend) PreviewFleetFileDiff(node, targetID, relPath string) (string, error) {
	target := nonEmptyCLI(targetID, node)
	return fleet.PreviewFileTextDiff(b.ctx, target, relPath, nil)
}

func (b *TUIBackend) SyncFleetFiles(req tui.FleetFileSyncReq) (tui.FleetFileSyncResult, error) {
	res, err := fleet.SyncNodeFiles(b.ctx, req.Node, req.TargetID, fleet.FileSyncOptions{
		Direction: req.Direction,
		RelPaths:  req.RelPaths,
		DryRun:    req.DryRun,
	})
	summary := fmt.Sprintf("%s %s: %d transferred, %d skipped, %d failed",
		strings.ToUpper(nonEmptyCLI(res.Direction, req.Direction)),
		nonEmptyCLI(res.Node, req.Node),
		res.Transferred, res.Skipped, res.Failed)
	return tui.FleetFileSyncResult{
		Node:        nonEmptyCLI(res.Node, req.Node),
		Direction:   nonEmptyCLI(res.Direction, req.Direction),
		Transferred: res.Transferred,
		Skipped:     res.Skipped,
		Failed:      res.Failed,
		Summary:     summary,
		Lines:       res.Lines,
	}, err
}

func (b *TUIBackend) ListPresets() ([]tui.PresetSummary, error) {
	presets, err := config.LoadPresets(b.ctx.RepoRoot)
	if err != nil {
		return nil, err
	}
	activeID := b.ctx.EffectivePreset()
	ids := config.ListPresetIDs(presets)
	out := make([]tui.PresetSummary, 0, len(ids))

	for _, id := range ids {
		p := presets[id]
		linksCount := 0
		pkgCount := 0
		if resolved, rerr := config.ResolvePreset(b.ctx.RepoRoot, presets, id); rerr == nil {
			pkgCount = len(resolved.PackageNames)
			if links, lerr := config.LoadLinksForPreset(b.ctx.RepoRoot, resolved); lerr == nil {
				linksCount = len(links)
			}
		}
		out = append(out, tui.PresetSummary{
			ID:           id,
			Description:  p.Description,
			Include:      p.Include,
			Exclude:      p.Exclude,
			Secrets:      p.Secrets,
			OSHooks:      p.OSHooks,
			Tags:         p.Tags,
			LinksCount:   linksCount,
			PackageCount: pkgCount,
			MatchesHost:  p.MatchesHost(b.ctx.Host),
			Active:       id == activeID,
		})
	}
	return out, nil
}

func (b *TUIBackend) ResolvePreset(id string) ([]tui.PresetToolRow, error) {
	presets, err := config.LoadPresets(b.ctx.RepoRoot)
	if err != nil {
		return nil, err
	}
	resolved, err := config.ResolvePreset(b.ctx.RepoRoot, presets, id)
	if err != nil {
		return nil, err
	}
	catalog, err := config.LoadTools(b.ctx.RepoRoot)
	if err != nil {
		return nil, err
	}

	tools := pkgmgr.RefineToolsForPreset(b.ctx, resolved, config.ResolveTools(resolved, catalog, b.ctx.Host), nil)
	det := newPkgInstallDetector(b.ctx, tools)
	rows := make([]tui.PresetToolRow, 0, len(tools))
	for _, t := range tools {
		pkgName := t.Spec.Name
		if pkgName == "" {
			pkgName = t.Spec.Ref
		}
		if pkgName == "" {
			pkgName = t.ID
		}
		desc := ""
		if spec, ok := catalog[t.ID]; ok {
			desc = spec.Description
		}
		var allProvs []string
		seenProv := map[string]bool{}
		for _, sp := range t.AllSpecs {
			if sp.Via != "" && !seenProv[sp.Via] {
				seenProv[sp.Via] = true
				allProvs = append(allProvs, sp.Via)
			}
		}
		if len(allProvs) == 0 && t.Spec.Via != "" {
			allProvs = []string{t.Spec.Via}
		}
		instCmd, updCmd, remCmd := formatPackageProviderCmds(t.Spec.Via, pkgName, t.Updates)
		_, status := det.checkStatus(t)
		rows = append(rows, tui.PresetToolRow{
			ID:           t.ID,
			Bin:          t.Bin,
			Provider:     t.Spec.Via,
			PackageName:  pkgName,
			Updates:      t.Updates,
			Source:       t.Source,
			Status:       status,
			Conflicts:    t.Conflicts,
			Description:  desc,
			OriginPreset: t.OriginPreset,
			OriginFile:   t.OriginFile,
			AllProviders: allProvs,
			InstallCmd:   instCmd,
			UpdateCmd:    updCmd,
			RemoveCmd:    remCmd,
		})
	}
	return rows, nil
}

func (b *TUIBackend) LoadPackageOutdated() (map[string]tui.PackageOutdatedInfo, error) {
	b.mu.Lock()
	if b.outdatedCache != nil && time.Since(b.outdatedAt) < 5*time.Minute {
		snap := make(map[string]tui.PackageOutdatedInfo, len(b.outdatedCache))
		for k, v := range b.outdatedCache {
			snap[k] = v
		}
		b.mu.Unlock()
		return snap, nil
	}
	b.mu.Unlock()

	digest, err := pkgmgr.BuildUpdateDigest(b.ctx, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]tui.PackageOutdatedInfo, len(digest.Items)*2)
	for _, it := range digest.Items {
		info := tui.PackageOutdatedInfo{
			Provider: it.Provider,
			Name:     it.Name,
			Current:  it.Current,
			Latest:   it.Latest,
			Risk:     string(it.Risk),
		}
		nameLow := strings.ToLower(strings.TrimSpace(it.Name))
		provLow := strings.ToLower(strings.TrimSpace(it.Provider))
		if provLow != "" && nameLow != "" {
			out[provLow+":"+nameLow] = info
		}
		if nameLow != "" {
			if _, exists := out[nameLow]; !exists {
				out[nameLow] = info
			}
		}
	}
	b.mu.Lock()
	b.outdatedCache = out
	b.outdatedAt = time.Now()
	b.mu.Unlock()
	return out, nil
}

func formatPackageProviderCmds(via, pkgName, updates string) (installCmd, updateCmd, removeCmd string) {
	if pkgName == "" {
		return "", "", ""
	}
	switch via {
	case "brew":
		installCmd = "brew install " + pkgName
		updateCmd = "brew upgrade " + pkgName
		removeCmd = "brew uninstall " + pkgName
	case "cask":
		installCmd = "brew install --cask " + pkgName
		updateCmd = "brew upgrade --cask " + pkgName
		removeCmd = "brew uninstall --cask " + pkgName
	case "mise":
		installCmd = "mise use -g " + pkgName
		updateCmd = "mise upgrade " + pkgName
		removeCmd = "mise uninstall " + pkgName
	case "gh-ext":
		installCmd = "gh extension install " + pkgName
		updateCmd = "gh extension upgrade " + pkgName
		removeCmd = "gh extension remove " + pkgName
	case "pacman":
		installCmd = "sudo pacman -S --noconfirm " + pkgName
		updateCmd = "sudo pacman -S --noconfirm " + pkgName
		removeCmd = "sudo pacman -Rns --noconfirm " + pkgName
	case "paru", "aur":
		installCmd = "paru -S --noconfirm " + pkgName
		updateCmd = "paru -S --noconfirm " + pkgName
		removeCmd = "paru -Rns --noconfirm " + pkgName
	case "apt":
		installCmd = "sudo apt-get install -y " + pkgName
		updateCmd = "sudo apt-get install --only-upgrade -y " + pkgName
		removeCmd = "sudo apt-get remove -y " + pkgName
	case "dnf":
		installCmd = "sudo dnf install -y " + pkgName
		updateCmd = "sudo dnf upgrade -y " + pkgName
		removeCmd = "sudo dnf remove -y " + pkgName
	case "script":
		installCmd = "ws bootstrap --allow script"
		updateCmd = "(self-updated)"
	}
	if updates == "self" {
		updateCmd = "(managed by tool self-updater)"
	}
	return installCmd, updateCmd, removeCmd
}

func buildPackageActionArgv(action string, p tui.PresetToolRow, nonInteractive bool) (argv []string, skipped string, err error) {
	target := strings.TrimSpace(p.PackageName)
	if target == "" {
		target = strings.TrimSpace(p.ID)
	}
	if target == "" {
		return nil, "", fmt.Errorf("empty package name")
	}
	if action == "update" && p.Updates == "self" {
		return nil, fmt.Sprintf("skipped %s (updates=self; manages its own updates)", p.ID), nil
	}
	sudoCmd := func(args ...string) []string {
		if nonInteractive {
			return append([]string{"sudo", "-n"}, args...)
		}
		return append([]string{"sudo"}, args...)
	}
	switch p.Provider {
	case "brew":
		switch action {
		case "install":
			return []string{"brew", "install", target}, "", nil
		case "update":
			return []string{"brew", "upgrade", target}, "", nil
		case "remove":
			return []string{"brew", "uninstall", target}, "", nil
		}
	case "cask":
		switch action {
		case "install":
			return []string{"brew", "install", "--cask", target}, "", nil
		case "update":
			return []string{"brew", "upgrade", "--cask", target}, "", nil
		case "remove":
			return []string{"brew", "uninstall", "--cask", target}, "", nil
		}
	case "mise":
		switch action {
		case "install":
			return []string{"mise", "use", "-g", target}, "", nil
		case "update":
			return []string{"mise", "upgrade", target}, "", nil
		case "remove":
			return []string{"mise", "uninstall", target}, "", nil
		}
	case "gh-ext":
		switch action {
		case "install":
			return []string{"gh", "extension", "install", target}, "", nil
		case "update":
			return []string{"gh", "extension", "upgrade", target}, "", nil
		case "remove":
			return []string{"gh", "extension", "remove", target}, "", nil
		}
	case "pacman":
		switch action {
		case "install":
			return sudoCmd("pacman", "-S", "--needed", "--noconfirm", target), "", nil
		case "update":
			return sudoCmd("pacman", "-S", "--noconfirm", target), "", nil
		case "remove":
			return sudoCmd("pacman", "-Rns", "--noconfirm", target), "", nil
		}
	case "paru", "aur":
		switch action {
		case "install":
			return []string{"paru", "-S", "--needed", "--noconfirm", target}, "", nil
		case "update":
			return []string{"paru", "-S", "--noconfirm", target}, "", nil
		case "remove":
			return []string{"paru", "-Rns", "--noconfirm", target}, "", nil
		}
	case "apt":
		switch action {
		case "install":
			return sudoCmd("-E", "apt-get", "install", "-y", target), "", nil
		case "update":
			return sudoCmd("-E", "apt-get", "install", "--only-upgrade", "-y", target), "", nil
		case "remove":
			return sudoCmd("-E", "apt-get", "remove", "-y", target), "", nil
		}
	case "dnf":
		switch action {
		case "install":
			return sudoCmd("dnf", "install", "-y", target), "", nil
		case "update":
			return sudoCmd("dnf", "upgrade", "-y", target), "", nil
		case "remove":
			return sudoCmd("dnf", "remove", "-y", target), "", nil
		}
	case "script":
		return nil, fmt.Sprintf("skipped %s (provider=script; run ws bootstrap --allow script)", p.ID), nil
	default:
		return nil, "", fmt.Errorf("unsupported package provider %q for %s", p.Provider, p.ID)
	}
	return nil, "", fmt.Errorf("unsupported package action %q", action)
}

func (b *TUIBackend) SearchPackages(query, provider string, limit int) ([]tui.PackageSearchRow, error) {
	items, err := pkgmgr.SearchPackages(b.ctx, query, pkgmgr.SearchOptions{
		Provider: provider,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]tui.PackageSearchRow, 0, len(items))
	for _, it := range items {
		instCmd, _, remCmd := formatPackageProviderCmds(it.Provider, it.Name, "manager")
		rows = append(rows, tui.PackageSearchRow{
			Name:          it.Name,
			Provider:      it.Provider,
			Repo:          it.Repo,
			Version:       it.Version,
			Description:   it.Description,
			Installed:     it.Installed,
			Tracked:       it.Tracked,
			SuggestedFile: pkgmgr.DefaultPackageListFile(b.ctx, it.Provider),
			InstallCmd:    instCmd,
			RemoveCmd:     remCmd,
		})
	}
	return rows, nil
}

func (b *TUIBackend) LoadPackageDetail(pkg tui.PresetToolRow) (tui.PackageDetail, error) {
	det := tui.PackageDetail{
		ID:           pkg.ID,
		PackageName:  pkg.PackageName,
		Provider:     pkg.Provider,
		Description:  pkg.Description,
		OriginPreset: pkg.OriginPreset,
		OriginFile:   pkg.OriginFile,
	}
	if pkg.Bin != "" {
		det.Artifacts = []string{"bin: " + pkg.Bin}
	}
	name := strings.TrimSpace(pkg.PackageName)
	if name == "" {
		name = pkg.ID
	}
	if name == "" {
		return det, nil
	}
	info, err := pkgmgr.GetPackageInfoWithRunner(b.ctx, name, pkg.Provider, nil)
	if err != nil {
		return det, nil
	}
	if info.Name != "" {
		det.PackageName = info.Name
	}
	if info.Provider != "" {
		det.Provider = info.Provider
	}
	det.Repo = info.Repo
	det.Version = info.Version
	det.LatestVersion = info.LatestVersion
	det.Installed = info.Installed
	det.Homepage = info.Homepage
	det.License = info.License
	det.InstalledSize = info.InstalledSize
	if info.Description != "" {
		det.Description = info.Description
	}
	det.Dependencies = append([]string(nil), info.Dependencies...)
	det.OptionalDeps = append([]string(nil), info.OptionalDeps...)
	det.RequiredBy = append([]string(nil), info.RequiredBy...)
	det.Conflicts = append([]string(nil), info.Conflicts...)
	if len(info.Artifacts) > 0 {
		det.Artifacts = append([]string(nil), info.Artifacts...)
	}
	det.Caveats = info.Caveats
	det.RawInfo = info.RawInfo
	return det, nil
}

func (b *TUIBackend) invalidatePackageCaches() {
	b.mu.Lock()
	b.outdatedCache = nil
	b.outdatedAt = time.Time{}
	b.mu.Unlock()
}

func (b *TUIBackend) wsCLIExecCmd(args []string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = "ws"
	}
	fullArgs := make([]string, 0, len(args)+4)
	if b.ctx.RepoRoot != "" && !wsArgsHaveFlag(args, "--repo") {
		fullArgs = append(fullArgs, "--repo", b.ctx.RepoRoot)
	}
	if b.ctx.PresetOverride != "" && !wsArgsHaveFlag(args, "--preset") {
		fullArgs = append(fullArgs, "--preset", b.ctx.PresetOverride)
	}
	fullArgs = append(fullArgs, args...)
	c := exec.Command(exe, fullArgs...)
	c.Env = app.PinnedMachineEnv(os.Environ())
	if b.ctx.HomeDir != "" {
		c.Env = shell.MergeEnv(c.Env, []string{"HOME=" + b.ctx.HomeDir})
	}
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

func (b *TUIBackend) PackageActionExecCmd(action string, pkgs []tui.PresetToolRow) (*exec.Cmd, error) {
	if b.ctx.DryRun || len(pkgs) == 0 {
		return nil, nil
	}
	var provOrder []string
	byProv := map[string][]string{}
	for _, p := range pkgs {
		_, skipped, err := buildPackageActionArgv(action, p, false)
		if err != nil {
			return nil, err
		}
		if skipped != "" {
			continue
		}
		target := strings.TrimSpace(p.PackageName)
		if target == "" {
			target = strings.TrimSpace(p.ID)
		}
		prov := strings.TrimSpace(p.Provider)
		if _, exists := byProv[prov]; !exists {
			provOrder = append(provOrder, prov)
		}
		byProv[prov] = append(byProv[prov], target)
	}
	if len(provOrder) == 0 {
		return nil, nil
	}

	allow := "sudo"
	if action == "remove" {
		allow = "remove,sudo"
	}
	b.invalidatePackageCaches()

	buildArgs := func(prov string, targets []string) []string {
		args := []string{"--allow", allow, "pkg", action}
		if prov != "" {
			args = append(args, "--provider", prov)
		}
		return append(args, targets...)
	}
	if len(provOrder) == 1 {
		prov := provOrder[0]
		return b.wsCLIExecCmd(buildArgs(prov, byProv[prov])), nil
	}

	parts := make([]string, 0, len(provOrder))
	for _, prov := range provOrder {
		sub := b.wsCLIExecCmd(buildArgs(prov, byProv[prov]))
		quoted := make([]string, len(sub.Args))
		for i, arg := range sub.Args {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		parts = append(parts, strings.Join(quoted, " "))
	}
	c := exec.Command("sh", "-c", strings.Join(parts, " && "))
	c.Env = app.PinnedMachineEnv(os.Environ())
	if b.ctx.HomeDir != "" {
		c.Env = shell.MergeEnv(c.Env, []string{"HOME=" + b.ctx.HomeDir})
	}
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c, nil
}

func (b *TUIBackend) AddAndTrackPackageExecCmd(name, provider, relFile string, install bool) (*exec.Cmd, error) {
	if !install || b.ctx.DryRun {
		return nil, nil
	}
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return nil, fmt.Errorf("package name is required")
	}
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "script" {
		return nil, nil
	}
	if prov == "" {
		prov = defaultProviderForHost(b.ctx)
	}
	args := []string{"--allow", "sudo", "pkg", "install", "--provider", prov, "--track"}
	if tf := strings.TrimSpace(relFile); tf != "" {
		args = append(args, "--file", tf)
	}
	args = append(args, cleanName)
	b.invalidatePackageCaches()
	return b.wsCLIExecCmd(args), nil
}

func (b *TUIBackend) UpdateAllExecCmd(all bool) (*exec.Cmd, error) {
	if b.ctx.DryRun {
		return nil, nil
	}
	args := []string{"--allow", "sudo", "update"}
	if all {
		args = append(args, "--all")
	}
	b.invalidatePackageCaches()
	return b.wsCLIExecCmd(args), nil
}

func (b *TUIBackend) ExecPackageAction(action string, pkgs []tui.PresetToolRow) (tui.CommandRunResult, error) {
	return b.ExecPackageActionStream(context.Background(), action, pkgs, nil)
}

func (b *TUIBackend) ExecPackageActionStream(ctx context.Context, action string, pkgs []tui.PresetToolRow, onLine func(string)) (tui.CommandRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(pkgs) == 0 {
		return tui.CommandRunResult{}, fmt.Errorf("no packages selected")
	}
	start := time.Now()
	var cmdSummaryParts []string
	var outLines []string
	emit := func(ln string) {
		outLines = append(outLines, ln)
		if onLine != nil {
			onLine(ln)
		}
	}
	lastExit := 0
	var lastErr error
	interactiveTTY := onLine == nil && !b.ctx.NonInteractive && !b.ctx.JSON && isCharDevice(os.Stdin) && isCharDevice(os.Stdout)

	for _, p := range pkgs {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			lastExit = 130
			break
		}
		argv, skipped, err := buildPackageActionArgv(action, p, !interactiveTTY)
		if err != nil {
			emit(fmt.Sprintf("✗ %s: %v", p.ID, err))
			lastExit = 1
			lastErr = err
			continue
		}
		if skipped != "" {
			emit("· " + skipped)
			continue
		}
		cmdStr := strings.Join(argv, " ")
		cmdSummaryParts = append(cmdSummaryParts, cmdStr)

		if b.ctx.DryRun {
			emit("[dry-run] $ " + cmdStr)
			continue
		}

		var rerr error
		code := 0
		if interactiveTTY {
			fmt.Fprintf(os.Stdout, "$ %s\n", cmdStr)
			c := exec.Command(argv[0], argv[1:]...)
			c.Env = app.PinnedMachineEnv(os.Environ())
			c.Env = append(c.Env, "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ASK=1", "DEBIAN_FRONTEND=noninteractive")
			c.Stdin = os.Stdin
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			rerr = c.Run()
			if rerr != nil {
				code = 1
				var exitErr *exec.ExitError
				if errors.As(rerr, &exitErr) {
					code = exitErr.ExitCode()
				}
			}
		} else {
			emit("$ " + cmdStr)
			c := exec.CommandContext(ctx, argv[0], argv[1:]...)
			c.Env = app.PinnedMachineEnv(os.Environ())
			c.Env = append(c.Env, "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ASK=1", "DEBIAN_FRONTEND=noninteractive")
			rawOut, exitCode, err := runCommandStreaming(ctx, c, onLine)
			rerr = err
			code = exitCode
			if trimmed := strings.TrimSpace(rawOut); trimmed != "" {
				outLines = append(outLines, trimmed)
			}
		}
		if rerr != nil {
			lastExit = code
			lastErr = rerr
			emit(fmt.Sprintf("✗ failed (exit %d): %v", code, rerr))
			if errors.Is(rerr, context.Canceled) {
				break
			}
		}
	}

	cmdTitle := ""
	if len(cmdSummaryParts) == 1 {
		cmdTitle = cmdSummaryParts[0]
	} else if len(cmdSummaryParts) > 1 {
		cmdTitle = strings.Join(cmdSummaryParts, " && ")
	} else {
		var names []string
		for _, p := range pkgs {
			name := strings.TrimSpace(p.PackageName)
			if name == "" {
				name = strings.TrimSpace(p.ID)
			}
			if name != "" {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			cmdTitle = "ws pkg " + action + " " + strings.Join(names, " ")
		} else {
			cmdTitle = "ws pkg list"
		}
	}
	return tui.CommandRunResult{
		Command:  cmdTitle,
		ExitCode: lastExit,
		Duration: time.Since(start),
		Output:   strings.Join(outLines, "\n"),
		IsErr:    lastExit != 0 || lastErr != nil,
	}, lastErr
}

func (b *TUIBackend) ApproveGHPR(repo, zone string, number int, body string) (string, error) {
	return projects.GHApprovePR(b.ctx, repo, zone, number, body, nil)
}

func (b *TUIBackend) MergeGHPR(repo, zone string, number int, method string) (string, error) {
	return projects.GHMergePR(b.ctx, repo, zone, number, method, nil)
}

func (b *TUIBackend) SetGHItemState(kind, repo, zone string, number int, closeItem bool) (string, error) {
	return projects.GHSetItemState(b.ctx, kind, repo, zone, number, closeItem, nil)
}

func (b *TUIBackend) CommentGHItem(kind, repo, zone string, number int, body string) (string, error) {
	return projects.GHCommentItem(b.ctx, kind, repo, zone, number, body, nil)
}

func (b *TUIBackend) CheckoutGHPR(repoDir, repo, zone string, number int) (string, error) {
	return projects.GHCheckoutPR(b.ctx, repoDir, repo, zone, number, nil)
}

func (b *TUIBackend) LoadUntrackedPackages(presetID string) ([]tui.PresetToolRow, error) {
	untracked, err := pkgmgr.DetectUntrackedPackages(b.ctx, presetID, nil)
	if err != nil {
		return nil, err
	}
	rows := make([]tui.PresetToolRow, 0, len(untracked))
	for _, u := range untracked {
		installCmd, updateCmd, removeCmd := formatPackageProviderCmds(u.Provider, u.Name, "manager")
		rows = append(rows, tui.PresetToolRow{
			ID:           fmt.Sprintf("untracked:%s:%s", u.Provider, u.Name),
			PackageName:  u.Name,
			Provider:     u.Provider,
			Status:       "untracked",
			Updates:      "manager",
			OriginPreset: "untracked",
			OriginFile:   u.SuggestedFile,
			Source:       "untracked",
			Description:  fmt.Sprintf("Explicitly installed via %s (not in %s)", u.Provider, u.SuggestedFile),
			InstallCmd:   installCmd,
			UpdateCmd:    updateCmd,
			RemoveCmd:    removeCmd,
		})
	}
	return rows, nil
}

func (b *TUIBackend) TrackPackages(relFile string, pkgs []tui.PresetToolRow) (string, error) {
	if len(pkgs) == 0 {
		return "", fmt.Errorf("no packages selected")
	}
	added := 0
	already := 0
	files := map[string]bool{}
	for _, p := range pkgs {
		name := strings.TrimSpace(p.PackageName)
		if name == "" {
			name = strings.TrimSpace(p.ID)
		}
		targetFile := strings.TrimSpace(relFile)
		if targetFile == "" {
			targetFile = strings.TrimSpace(p.OriginFile)
		}
		if targetFile == "" {
			targetFile = pkgmgr.DefaultPackageListFile(b.ctx, p.Provider)
		}
		if b.ctx.DryRun {
			added++
			files[targetFile] = true
			continue
		}
		wasAdded, err := pkgmgr.AddPackageToList(b.ctx.RepoRoot, targetFile, name)
		if err != nil {
			return "", err
		}
		if wasAdded {
			added++
			files[targetFile] = true
		} else {
			already++
		}
	}
	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	sort.Strings(fileList)
	prefix := ""
	if b.ctx.DryRun {
		prefix = "[dry-run] "
	}
	if added == 0 && already > 0 {
		return fmt.Sprintf("%s%d package(s) already tracked", prefix, already), nil
	}
	if len(fileList) > 0 {
		return fmt.Sprintf("%sTracked %d package(s) in %s", prefix, added, strings.Join(fileList, ", ")), nil
	}
	return fmt.Sprintf("%sTracked %d package(s)", prefix, added), nil
}

func (b *TUIBackend) AddAndTrackPackage(name, provider, relFile string, install bool) (tui.CommandRunResult, error) {
	start := time.Now()
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return tui.CommandRunResult{}, fmt.Errorf("package name is required")
	}
	prov := strings.ToLower(strings.TrimSpace(provider))
	if prov == "" {
		prov = defaultProviderForHost(b.ctx)
	}
	targetFile := strings.TrimSpace(relFile)
	if targetFile == "" {
		targetFile = pkgmgr.DefaultPackageListFile(b.ctx, prov)
	}

	var outLines []string
	var res tui.CommandRunResult
	if install {
		row := tui.PresetToolRow{
			ID:          cleanName,
			PackageName: cleanName,
			Provider:    prov,
			Updates:     "manager",
		}
		ires, ierr := b.ExecPackageAction("install", []tui.PresetToolRow{row})
		res = ires
		if strings.TrimSpace(ires.Output) != "" {
			outLines = append(outLines, ires.Output)
		}
		if ierr != nil {
			return res, ierr
		}
	}

	if b.ctx.DryRun {
		outLines = append(outLines, fmt.Sprintf("[dry-run] track %s -> %s", cleanName, targetFile))
		return tui.CommandRunResult{
			Command:  nonEmptyStr(res.Command, fmt.Sprintf("track %s -> %s", cleanName, targetFile)),
			ExitCode: 0,
			Duration: time.Since(start),
			Output:   strings.Join(outLines, "\n"),
		}, nil
	}

	added, err := pkgmgr.AddPackageToList(b.ctx.RepoRoot, targetFile, cleanName)
	if err != nil {
		outLines = append(outLines, fmt.Sprintf("✗ failed to write %s: %v", targetFile, err))
		return tui.CommandRunResult{
			Command:  nonEmptyStr(res.Command, fmt.Sprintf("track %s -> %s", cleanName, targetFile)),
			ExitCode: 1,
			Duration: time.Since(start),
			Output:   strings.Join(outLines, "\n"),
			IsErr:    true,
		}, err
	}
	if added {
		outLines = append(outLines, fmt.Sprintf("✓ Added %s to %s", cleanName, targetFile))
	} else {
		outLines = append(outLines, fmt.Sprintf("· %s already present in %s", cleanName, targetFile))
	}
	cmdTitle := fmt.Sprintf("track %s -> %s", cleanName, targetFile)
	if res.Command != "" {
		cmdTitle = res.Command + " && " + cmdTitle
	}
	return tui.CommandRunResult{
		Command:  cmdTitle,
		ExitCode: 0,
		Duration: time.Since(start),
		Output:   strings.Join(outLines, "\n"),
	}, nil
}

func (b *TUIBackend) UntrackPackages(pkgs []tui.PresetToolRow) (string, error) {
	if len(pkgs) == 0 {
		return "", fmt.Errorf("no packages selected")
	}
	removed := 0
	files := map[string]bool{}
	for _, p := range pkgs {
		name := strings.TrimSpace(p.PackageName)
		if name == "" {
			name = strings.TrimSpace(p.ID)
		}
		targetFile := strings.TrimSpace(p.OriginFile)
		if !strings.HasPrefix(targetFile, "pkg/") || !strings.HasSuffix(targetFile, ".txt") {
			if foundFile, ok := pkgmgr.FindPackageListFile(b.ctx.RepoRoot, name, nil); ok {
				targetFile = foundFile
			} else {
				targetFile = pkgmgr.DefaultPackageListFile(b.ctx, p.Provider)
			}
		}
		if b.ctx.DryRun {
			removed++
			files[targetFile] = true
			continue
		}
		wasRemoved, err := pkgmgr.RemovePackageFromList(b.ctx.RepoRoot, targetFile, name)
		if err != nil {
			return "", err
		}
		if wasRemoved {
			removed++
			files[targetFile] = true
		}
	}
	if removed == 0 {
		return "", fmt.Errorf("none of the selected packages were found in pkg/*.txt (tools defined in tools.toml cannot be untracked automatically)")
	}
	fileList := make([]string, 0, len(files))
	for f := range files {
		fileList = append(fileList, f)
	}
	sort.Strings(fileList)
	prefix := ""
	if b.ctx.DryRun {
		prefix = "[dry-run] "
	}
	return fmt.Sprintf("%sUntracked %d package(s) from %s", prefix, removed, strings.Join(fileList, ", ")), nil
}

func (b *TUIBackend) PreviewUpdateAll(all bool) (tui.UpdatePlanPreview, error) {
	digest, err := pkgmgr.PlanUpdate(b.ctx, pkgmgr.UpdateOptions{All: all})
	if err != nil {
		return tui.UpdatePlanPreview{}, err
	}
	prev := tui.UpdatePlanPreview{
		RepoDirty:    digest.RepoDirty,
		SnapshotHint: digest.SnapshotHint,
		PendingCount: len(digest.Items),
		Warnings:     slices.Clone(digest.Warnings),
	}
	if digest.Plan != nil {
		for _, act := range digest.Plan.Actions {
			phase := act.Metadata["phase"]
			if phase == "" {
				phase = "os-pkg"
			}
			step := fmt.Sprintf("[%s] %s", phase, act.Summary)
			if len(act.Command) > 0 {
				step += " (" + strings.Join(act.Command, " ") + ")"
			}
			prev.Steps = append(prev.Steps, step)
		}
		if digest.Plan.Metadata["post_apply"] == "true" {
			prev.Steps = append(prev.Steps, "[finalize] Render templates and verify symlinks (ws apply)")
			if b.ctx != nil && b.ctx.RepoRoot != "" {
				if fi, err := os.Stat(filepath.Join(b.ctx.RepoRoot, "ws", "go.mod")); err == nil && !fi.IsDir() {
					if _, lerr := exec.LookPath("go"); lerr == nil {
						prev.Steps = append(prev.Steps, "[finalize] Rebuild ~/.local/bin/ws if ws/ changed or binary is missing")
					}
				}
			}
		}
	}
	prev.Summary = fmt.Sprintf("%d package update(s) detected, %d planned step(s)", prev.PendingCount, len(prev.Steps))
	return prev, nil
}

func (b *TUIBackend) ExecUpdateAll(all bool) (tui.CommandRunResult, error) {
	return b.ExecUpdateAllStream(context.Background(), all, nil)
}

func (b *TUIBackend) ExecUpdateAllStream(ctx context.Context, all bool, onLine func(string)) (tui.CommandRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now()
	digest, err := pkgmgr.PlanUpdate(b.ctx, pkgmgr.UpdateOptions{All: all})
	if err != nil {
		return tui.CommandRunResult{
			Command:  "ws update --all",
			ExitCode: 1,
			Duration: time.Since(start),
			Output:   err.Error(),
			IsErr:    true,
		}, err
	}
	var outMu sync.Mutex
	var outLines []string
	emit := func(ln string) {
		outMu.Lock()
		outLines = append(outLines, ln)
		outMu.Unlock()
		if onLine != nil {
			onLine(ln)
		}
	}
	if len(digest.Items) > 0 {
		emit(fmt.Sprintf("Pending package updates (%d):", len(digest.Items)))
		for _, it := range digest.Items {
			emit(fmt.Sprintf("  · [%s] %s %s -> %s", it.Provider, it.Name, it.Current, it.Latest))
		}
	}
	emittedWarns := slices.Clone(digest.Warnings)
	streamW := newLineStreamWriter(onLine)
	res, err := pkgmgr.ExecUpdateWithOptions(b.ctx, digest, nil, pkgmgr.ExecUpdateOptions{
		StreamOut: streamW,
		StreamErr: streamW,
		OnEvent: func(ev pkgmgr.UpdateEvent) {
			switch ev.Status {
			case "phase":
				emit("==> " + ev.Summary)
			case "start":
				emit("  → " + ev.Summary)
			case "done":
				detail := ""
				if ev.Detail != "" {
					detail = " (" + ev.Detail + ")"
				}
				emit(fmt.Sprintf("  ✓ %s (%s)%s", ev.Summary, ui.FormatDuration(ev.Duration), detail))
			case "skip":
				detail := ""
				if ev.Detail != "" {
					detail = " (" + ev.Detail + ")"
				}
				emit(fmt.Sprintf("  - %s%s", ev.Summary, detail))
			case "warn":
				outMu.Lock()
				if ev.Detail != "" {
					emittedWarns = append(emittedWarns, ev.Detail)
				}
				outMu.Unlock()
				emit(fmt.Sprintf("  ! %s (%s): %s", ev.Summary, ui.FormatDuration(ev.Duration), ev.Detail))
			case "fail":
				emit(fmt.Sprintf("  ✗ %s (%s): %s", ev.Summary, ui.FormatDuration(ev.Duration), ev.Detail))
			}
		},
	})
	streamW.Flush()
	if res != nil {
		for _, w := range res.Warnings {
			outMu.Lock()
			already := slices.Contains(emittedWarns, w) || warningDetailEmitted(emittedWarns, w)
			outMu.Unlock()
			if !already {
				emit("WARN: " + w)
			}
		}
		emit(fmt.Sprintf("Summary: %d applied, %d skipped (%s)", res.Applied, res.Skipped, ui.FormatDuration(time.Since(start))))
	}
	if err != nil {
		emit("✗ " + err.Error())
		outMu.Lock()
		joined := strings.Join(outLines, "\n")
		outMu.Unlock()
		return tui.CommandRunResult{
			Command:  "ws update --all",
			ExitCode: 1,
			Duration: time.Since(start),
			Output:   joined,
			IsErr:    true,
		}, err
	}
	outMu.Lock()
	joined := strings.Join(outLines, "\n")
	outMu.Unlock()
	return tui.CommandRunResult{
		Command:  "ws update --all",
		ExitCode: 0,
		Duration: time.Since(start),
		Output:   joined,
	}, nil
}

func defaultProviderForHost(ctx *app.Context) string {
	if ctx != nil {
		if ctx.Host.OS == "darwin" {
			return "brew"
		}
		if ctx.Host.Distro == "arch" || ctx.Host.Distro == "cachyos" || slices.Contains(ctx.Host.DistroLike, "arch") {
			if _, err := exec.LookPath("paru"); err == nil {
				return "paru"
			}
			return "pacman"
		}
		if ctx.Host.Distro == "fedora" {
			return "dnf"
		}
	}
	return "apt"
}

func nonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (b *TUIBackend) LoadHistory(query string, limit int) ([]tui.HistoryRow, string, error) {
	entries, source, err := insights.SearchHistory(b.ctx, query, limit)
	if err != nil {
		return nil, source, err
	}
	rows := make([]tui.HistoryRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, tui.HistoryRow{
			Timestamp:  e.Timestamp,
			DurationMs: e.DurationMs,
			ExitCode:   e.ExitCode,
			Shell:      e.Shell,
			Zone:       e.Zone,
			Command:    e.Command,
		})
	}
	return rows, source, nil
}

func hasBrewConflictPath(binPath string, conflicts []string) bool {
	hasBrew := false
	for _, c := range conflicts {
		if strings.HasPrefix(c, "cask:") || strings.HasPrefix(c, "brew:") {
			hasBrew = true
			break
		}
	}
	if !hasBrew {
		return false
	}
	sl := filepath.ToSlash(binPath)
	return strings.Contains(sl, "/homebrew/") || strings.Contains(sl, "/Homebrew/") || strings.Contains(sl, "/Cellar/") || strings.Contains(sl, "/Caskroom/")
}

func (b *TUIBackend) ListThemes() ([]string, error) {
	return config.ListThemes(b.ctx.RepoRoot)
}

func (b *TUIBackend) LoadPalette(id string) (config.Palette, error) {
	return config.LoadPalette(b.ctx.RepoRoot, id)
}

func (b *TUIBackend) ActiveTheme() (string, error) {
	return resolveActiveThemeID(b.ctx), nil
}

func (b *TUIBackend) ApplyTheme(id string) (*engine.ApplyResult, error) {
	runCtx := b.confirmedCtx("replace")
	plan, err := engine.PlanApply(runCtx, "", id)
	if err != nil {
		return nil, err
	}
	return engine.ExecApply(runCtx, plan)
}

func (b *TUIBackend) ApplyThemeForApp(themeID, app string) (*engine.ApplyResult, error) {
	runCtx := b.confirmedCtx("replace")
	plan, err := engine.PlanApply(runCtx, strings.TrimSpace(app), strings.TrimSpace(themeID))
	if err != nil {
		return nil, err
	}
	return engine.ExecApply(runCtx, plan)
}

func (b *TUIBackend) TemplateStatuses() ([]tui.TemplateStatusRow, error) {
	statuses, err := engine.CheckTemplates(b.ctx, "")
	if err != nil {
		return nil, err
	}
	rows := make([]tui.TemplateStatusRow, 0, len(statuses))
	for _, st := range statuses {
		hash := st.ActualHash
		if hash == "" {
			hash = st.ExpectedHash
		}
		rows = append(rows, tui.TemplateStatusRow{
			App:      st.App,
			Template: st.Template,
			Output:   st.Output,
			PostHook: st.PostHook,
			Status:   st.State,
			Diff:     st.Diff,
			Hash:     hash,
		})
	}
	return rows, nil
}

func (b *TUIBackend) EditAppCmd(appName string) (*exec.Cmd, error) {
	entry, err := engine.ResolveAppTemplate(b.ctx.RepoRoot, appName)
	if err != nil {
		return nil, err
	}
	tmplAbs := filepath.Join(b.ctx.RepoRoot, entry.Template)
	return resolveEditorCmd(tmplAbs), nil
}

func (b *TUIBackend) FinishAppEdit(appName string) (*engine.ApplyResult, error) {
	return engine.ExecEditAfterSave(b.confirmedCtx("replace"), appName)
}

func cleanupCandidateNeedsTTY(c cleanup.Candidate) bool {
	if len(c.CleanerCmd) == 0 {
		return false
	}
	first := filepath.Base(c.CleanerCmd[0])
	return first == "sudo" || first == "paccache"
}

func (b *TUIBackend) ScanCleanup() ([]tui.CleanItem, error) {
	cands, err := cleanup.Scan(b.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tui.CleanItem, 0, len(cands))
	for _, c := range cands {
		out = append(out, tui.CleanItem{
			ID:                c.ID,
			Tier:              string(c.Tier),
			Category:          c.Category,
			Title:             c.Title,
			Paths:             c.Paths,
			Bytes:             c.Bytes,
			Warning:           c.Warning,
			SelectedByDefault: c.SelectedByDefault,
			NeedsSudo:         cleanupCandidateNeedsTTY(c),
		})
	}
	return out, nil
}

func (b *TUIBackend) CleanupExecCmd(ids []string) (*exec.Cmd, error) {
	if b.ctx.DryRun || len(ids) == 0 {
		return nil, nil
	}
	cands, err := cleanup.Scan(b.ctx)
	if err != nil {
		return nil, err
	}
	runCtx := b.confirmedCtx("remove", "sudo")
	plan, err := cleanup.PlanClean(runCtx, cands, ids, nil)
	if err != nil {
		return nil, err
	}
	var ttyCmds [][]string
	for _, act := range plan.Actions {
		if len(act.Command) == 0 {
			continue
		}
		first := filepath.Base(act.Command[0])
		if act.NeedsTTY || act.RequiresAllow == "sudo" || first == "sudo" || first == "paccache" {
			ttyCmds = append(ttyCmds, slices.Clone(act.Command))
		}
	}
	if len(ttyCmds) == 0 {
		return nil, nil
	}

	var cmd *exec.Cmd
	if len(ttyCmds) == 1 {
		cmd = exec.Command(ttyCmds[0][0], ttyCmds[0][1:]...)
	} else {
		parts := make([]string, 0, len(ttyCmds))
		for _, argv := range ttyCmds {
			quoted := make([]string, len(argv))
			for i, arg := range argv {
				quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
			}
			parts = append(parts, strings.Join(quoted, " "))
		}
		cmd = exec.Command("sh", "-c", strings.Join(parts, " && "))
	}
	cmd.Dir = b.ctx.HomeDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd, nil
}

func (b *TUIBackend) RunCleanup(ids []string, dryRun bool) (*engine.ApplyResult, error) {
	cands, err := cleanup.Scan(b.ctx)
	if err != nil {
		return nil, err
	}
	effectiveDry := b.ctx.DryRun || dryRun
	if !effectiveDry {
		for _, c := range cands {
			if slices.Contains(ids, c.ID) && cleanupCandidateNeedsTTY(c) {
				return nil, fmt.Errorf("cleanup target %s requires interactive TTY (sudo)", c.ID)
			}
		}
	}
	var runCtx *app.Context
	if effectiveDry {
		cp := *b.ctx
		runCtx = &cp
	} else {
		runCtx = b.confirmedCtx("remove")
	}
	runCtx.DryRun = effectiveDry
	plan, err := cleanup.PlanClean(runCtx, cands, ids, nil)
	if err != nil {
		return nil, err
	}
	return cleanup.ExecClean(runCtx, plan, nil)
}

func (b *TUIBackend) LoadDoctor() (tui.DoctorSummary, error) {
	rep, err := engine.RunDoctor(b.ctx)
	if err != nil {
		return tui.DoctorSummary{}, err
	}

	findings := make([]tui.DoctorFinding, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		findings = append(findings, tui.DoctorFinding{
			ID:       f.ID,
			Severity: string(f.Severity),
			Category: f.Category,
			Title:    f.Title,
			Detail:   f.Detail,
			FixCLI:   f.FixCLI,
			FixPlan:  f.FixPlan,
		})
	}

	zonesOK, zonesTotal := 0, 0
	if zstats, zerr := shell.ZoneStatuses(b.ctx); zerr != nil {
		findings = append(findings, tui.DoctorFinding{
			ID:       "zones:error",
			Severity: "warn",
			Category: "zones",
			Title:    "Zone configuration could not be parsed cleanly",
			Detail:   zerr.Error(),
			FixCLI:   "ws zone status",
		})
	} else {
		zonesTotal = len(zstats)
		for _, zs := range zstats {
			if zs.RootExists && zs.ConfigExists && !zs.ConfigIsSymlink && zs.GitConfigExists {
				zonesOK++
			}
		}
	}

	pkgManaged := 0
	if presets, perr := config.LoadPresets(b.ctx.RepoRoot); perr != nil {
		findings = append(findings, tui.DoctorFinding{
			ID:       "presets:error",
			Severity: "warn",
			Category: "packages",
			Title:    "Preset catalog could not be loaded",
			Detail:   perr.Error(),
			FixCLI:   "ws preset list",
		})
	} else if resolved, rerr := config.ResolvePreset(b.ctx.RepoRoot, presets, rep.Preset); rerr != nil {
		findings = append(findings, tui.DoctorFinding{
			ID:       "presets:resolve-error",
			Severity: "warn",
			Category: "packages",
			Title:    fmt.Sprintf("Preset %q could not be resolved", rep.Preset),
			Detail:   rerr.Error(),
			FixCLI:   "ws preset list",
		})
	} else {
		pkgManaged = len(resolved.PackageNames)
	}

	pkgOutdated := 0
	if digest, digestWarn := loadDigest(b.ctx.WSBaseDir()); digest != nil {
		pkgOutdated = len(digest.Items)
		if digestWarn != "" {
			findings = append(findings, tui.DoctorFinding{
				ID:       "digest:malformed",
				Severity: "warn",
				Category: "packages",
				Title:    "Update digest cache contained multiple records",
				Detail:   digestWarn,
				FixCLI:   fmt.Sprintf("ws update --digest --json --output %s", b.ctx.WSDigestPath()),
			})
		}
	} else if digestWarn != "" {
		findings = append(findings, tui.DoctorFinding{
			ID:       "digest:corrupt",
			Severity: "warn",
			Category: "packages",
			Title:    "Cached update digest is unreadable",
			Detail:   digestWarn,
			FixCLI:   fmt.Sprintf("ws update --digest --json --output %s", b.ctx.WSDigestPath()),
		})
	}

	var reclaimable int64
	if cands, cerr := cleanup.Scan(b.ctx); cerr != nil {
		findings = append(findings, tui.DoctorFinding{
			ID:       "cleanup:scan-error",
			Severity: "warn",
			Category: "cleanup",
			Title:    "Cleanup scan encountered an error",
			Detail:   cerr.Error(),
			FixCLI:   "ws clean --dry-run",
		})
	} else {
		for _, c := range cands {
			reclaimable += c.Bytes
		}
	}

	hostLabel := strings.Trim(b.ctx.Host.OS+"/"+b.ctx.Host.Arch, "/")
	if b.ctx.Host.Distro != "" && b.ctx.Host.Distro != b.ctx.Host.OS {
		hostLabel = b.ctx.Host.Distro + " " + hostLabel
	}

	return tui.DoctorSummary{
		Host:             hostLabel,
		Preset:           rep.Preset,
		Theme:            rep.Theme,
		LinksOK:          rep.LinksOK,
		LinksTotal:       rep.LinksTotal,
		TemplatesOK:      rep.TemplatesOK,
		TemplatesTotal:   rep.TemplatesTotal,
		PackagesManaged:  pkgManaged,
		PackagesOutdated: pkgOutdated,
		ZonesOK:          zonesOK,
		ZonesTotal:       zonesTotal,
		ReclaimableBytes: reclaimable,
		Findings:         findings,
	}, nil
}

func (b *TUIBackend) ApplyPlan(plan *engine.Plan) (*engine.ApplyResult, error) {
	return engine.ExecApply(b.confirmedCtx("replace"), plan)
}

func (b *TUIBackend) LoadDBStatus() (tui.DBStatusSummary, error) {
	var out tui.DBStatusSummary
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		stats, serr := db.Stats()
		if serr != nil {
			return serr
		}
		lastAt := ""
		if stats.LastBackupAt > 0 {
			lastAt = time.Unix(stats.LastBackupAt, 0).Local().Format("2006-01-02 15:04")
		}
		out = tui.DBStatusSummary{
			Path:             b.ctx.CollapseHome(stats.Path),
			SchemaVersion:    stats.SchemaVersion,
			KeySource:        string(kr.Source),
			HasKey:           kr.HasKey(),
			DirtySinceBackup: stats.DirtySinceBackup,
			LastBackupAt:     lastAt,
			CurrentHash:      stats.CurrentHash,
			EnvCount:         stats.TableCounts["env_entries"],
			BlobCount:        stats.TableCounts["secret_blobs"],
			RepoCount:        stats.TableCounts["repos"],
		}
		return nil
	})
	return out, err
}

func (b *TUIBackend) BackupDB(ifChanged bool) (tui.DBBackupResult, error) {
	var out tui.DBBackupResult
	err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
		res, berr := store.BackupIfChanged(b.ctx, db, kr, store.BackupOptions{
			Force: !ifChanged,
		})
		if berr != nil {
			return berr
		}
		summary := ""
		if res.Skipped {
			summary = "ws.db unchanged since last backup"
		} else if res.DryRun || b.ctx.DryRun {
			summary = fmt.Sprintf("[dry-run] Would back up ws.db to %s", b.ctx.CollapseHome(res.LocalPath))
		} else {
			summary = fmt.Sprintf("Backed up ws.db to %s", b.ctx.CollapseHome(res.LocalPath))
		}
		out = tui.DBBackupResult{
			Changed:     res.Changed,
			Skipped:     res.Skipped,
			LocalPath:   res.LocalPath,
			OnePassword: res.OnePassword,
			Summary:     summary,
		}
		return nil
	})
	return out, err
}

func (b *TUIBackend) UndoLast() (string, error) {
	return engine.UndoLast(b.ctx)
}

func (b *TUIBackend) LoadVaultInventory() (tui.VaultInventoryView, error) {
	inv := shell.DiscoverVaultItems(b.ctx)
	items := make([]tui.VaultItemRow, 0, len(inv.Items))
	for _, it := range inv.Items {
		items = append(items, tui.VaultItemRow{
			ID:          it.ID,
			Scope:       it.Scope,
			Label:       it.Label,
			LocalPath:   it.LocalPath,
			TargetArg:   it.TargetArg,
			DocTitle:    it.DocTitle,
			Category:    it.Category,
			ExistsLocal: it.ExistsLocal,
			IsDir:       it.IsDir,
			FileCount:   it.FileCount,
			SizeBytes:   it.SizeBytes,
			ModTime:     it.ModTime,
			Note:        it.Note,
		})
	}
	return tui.VaultInventoryView{
		HasOPCLI: inv.HasOPCLI,
		OPVault:  inv.OPVault,
		Items:    items,
	}, nil
}

func (b *TUIBackend) RunVaultAction(action string, items []tui.VaultItemRow) (tui.CommandRunResult, error) {
	act := strings.ToLower(strings.TrimSpace(action))
	if act != "save" && act != "load" && act != "key" {
		return tui.CommandRunResult{}, fmt.Errorf("unsupported vault action %q", action)
	}
	if len(items) == 0 && act != "key" {
		return tui.CommandRunResult{}, fmt.Errorf("no vault items selected")
	}

	start := time.Now()
	var buf strings.Builder
	var cmdLabels []string

	if act == "key" {
		cmdLabels = append(cmdLabels, "ws db key init && ws db key backup")
		kr, err := store.LoadKey(b.ctx)
		newlyCreated := false
		if err != nil || !kr.HasKey() {
			if b.ctx.DryRun {
				fmt.Fprintf(&buf, "[dry-run] Would initialize ws.db key at %s\n", b.ctx.CollapseHome(b.ctx.WSKeyPath()))
			} else {
				kr, err = store.InitKey(b.ctx, false)
				if err != nil {
					return tui.CommandRunResult{
						Command:  strings.Join(cmdLabels, "; "),
						ExitCode: 1,
						Duration: time.Since(start),
						Output:   "error: " + err.Error(),
						IsErr:    true,
					}, err
				}
				newlyCreated = true
				b.dbMu.Lock()
				b.kr = kr
				b.dbMu.Unlock()
				fmt.Fprintf(&buf, "Initialized ws.db key (%s): %s\n", kr.Source, b.ctx.CollapseHome(kr.KeyFilePath))
			}
		} else {
			fmt.Fprintf(&buf, "Loaded ws.db key (%s): %s\n", kr.Source, b.ctx.CollapseHome(kr.KeyFilePath))
		}

		if b.ctx.DryRun {
			fmt.Fprintln(&buf, "[dry-run] Would back up ws.db key (ws-db.key) to 1Password")
		} else if err := store.BackupKeyTo1Password(b.ctx, kr); err != nil {
			if newlyCreated {
				fmt.Fprintf(&buf, "warning: 1Password key backup skipped (%v)\n", err)
			} else {
				return tui.CommandRunResult{
					Command:  strings.Join(cmdLabels, "; "),
					ExitCode: 1,
					Duration: time.Since(start),
					Output:   strings.TrimRight(buf.String()+"\nerror: "+err.Error(), "\r\n"),
					IsErr:    true,
				}, err
			}
		} else {
			fmt.Fprintln(&buf, "Backed up ws.db master key (ws-db.key) to 1Password.")
		}

		return tui.CommandRunResult{
			Command:  strings.Join(cmdLabels, "; "),
			ExitCode: 0,
			Duration: time.Since(start),
			Output:   strings.TrimRight(buf.String(), "\r\n"),
		}, nil
	}

	opCtx := b.ctx
	if act == "load" {
		opCtx = b.confirmedCtx("replace")
	}

	var firstErr error
	for _, it := range items {
		switch it.Scope {
		case "db":
			if act == "save" {
				cmdLabels = append(cmdLabels, "ws db backup --1password --force")
				err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
					res, berr := store.BackupIfChanged(b.ctx, db, kr, store.BackupOptions{
						Force:       true,
						OnePassword: true,
					})
					if berr != nil {
						return berr
					}
					if res.DryRun || b.ctx.DryRun {
						fmt.Fprintf(&buf, "[dry-run] Would back up ws.db snapshot to %s and 1Password (%s)\n", b.ctx.CollapseHome(res.LocalPath), it.DocTitle)
					} else {
						fmt.Fprintf(&buf, "Backed up ws.db snapshot -> %s (1Password: %v)\n", b.ctx.CollapseHome(res.LocalPath), res.OnePassword)
					}
					return nil
				})
				if err != nil {
					firstErr = err
					fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
				}
			} else {
				cmdLabels = append(cmdLabels, "ws db restore --from-1password")
				err := b.withDB(func(db *store.DB, kr *store.Keyring) error {
					res, rerr := store.RestoreBackup(opCtx, db, kr, store.RestoreOptions{
						FromOnePassword: true,
						AllowReplace:    opCtx.Allow.Replace,
					})
					if rerr != nil {
						return rerr
					}
					fmt.Fprintf(&buf, "Restored ws.db from 1Password (%s, hash %s)\n", res.Source, res.Hash)
					return nil
				})
				if err != nil {
					firstErr = err
					fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
				}
			}

		case "db-key":
			if act == "save" {
				cmdLabels = append(cmdLabels, "ws db key backup")
				if b.ctx.DryRun {
					fmt.Fprintf(&buf, "[dry-run] Would back up ws.db key (%s) to 1Password\n", it.DocTitle)
					continue
				}
				kr, err := store.LoadKey(b.ctx)
				if err != nil {
					firstErr = err
					fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
					continue
				}
				if err := store.BackupKeyTo1Password(b.ctx, kr); err != nil {
					firstErr = err
					fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
					continue
				}
				fmt.Fprintln(&buf, "Backed up ws.db master key (ws-db.key) to 1Password.")
			} else {
				cmdLabels = append(cmdLabels, "ws db key restore")
				if b.ctx.DryRun {
					fmt.Fprintf(&buf, "[dry-run] Would restore ws.db key from 1Password to %s\n", b.ctx.CollapseHome(b.ctx.WSKeyPath()))
					continue
				}
				kr, err := store.RestoreKeyFrom1Password(opCtx, false)
				if err != nil {
					firstErr = err
					fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
					continue
				}
				b.dbMu.Lock()
				b.kr = kr
				b.dbMu.Unlock()
				fmt.Fprintf(&buf, "Restored ws.db master key (%s): %s\n", kr.Source, b.ctx.CollapseHome(kr.KeyFilePath))
			}

		case "ssh", "secrets", "files", "envfiles":
			var args []string
			if it.TargetArg != "" {
				args = []string{it.TargetArg}
			}
			cmdParts := []string{"ws", "vault", it.Scope, act}
			if it.DocTitle != "" {
				cmdParts = append(cmdParts, "--doc", it.DocTitle)
			}
			if len(args) > 0 {
				cmdParts = append(cmdParts, args...)
			}
			cmdLabels = append(cmdLabels, strings.Join(cmdParts, " "))
			opts := shell.VaultOptions{
				DocTitle: it.DocTitle,
				WarnOut:  &buf,
			}
			if err := shell.VaultOpWithOptions(opCtx, act, it.Scope, args, opts); err != nil {
				firstErr = err
				fmt.Fprintf(&buf, "[%s] error: %v\n", it.ID, err)
			} else {
				fmt.Fprintf(&buf, "Completed: %s %s (%s)\n", it.Scope, act, it.DocTitle)
			}
		}
	}

	code := 0
	if firstErr != nil {
		code = 1
	}
	return tui.CommandRunResult{
		Command:  strings.Join(cmdLabels, "; "),
		ExitCode: code,
		Duration: time.Since(start),
		Output:   strings.TrimRight(buf.String(), "\r\n"),
		IsErr:    firstErr != nil,
	}, firstErr
}

func wsArgsHaveFlag(args []string, names ...string) bool {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		for _, name := range names {
			if arg == name || strings.HasPrefix(arg, name+"=") {
				return true
			}
		}
	}
	return false
}

func wsFirstSubcommand(args []string) string {
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			return strings.ToLower(strings.TrimSpace(arg))
		}
		if strings.Contains(arg, "=") {
			continue
		}
		switch arg {
		case "--repo", "--preset", "--allow", "--plan", "--save-plan", "--zone", "--output", "-o", "--tier", "--category", "--window":
			skipNext = true
		}
	}
	return ""
}

type lineStreamWriter struct {
	mu      sync.Mutex
	onLine  func(string)
	partial string
	all     strings.Builder
}

func newLineStreamWriter(onLine func(string)) *lineStreamWriter {
	return &lineStreamWriter{onLine: onLine}
}

func (w *lineStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	s := string(p)
	w.all.WriteString(s)
	w.partial += s
	var ready []string
	for {
		idx := strings.IndexByte(w.partial, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(w.partial[:idx], "\r")
		w.partial = w.partial[idx+1:]
		ready = append(ready, line)
	}
	cb := w.onLine
	w.mu.Unlock()
	if cb != nil {
		for _, ln := range ready {
			cb(ln)
		}
	}
	return len(p), nil
}

func (w *lineStreamWriter) Flush() {
	w.mu.Lock()
	rem := strings.TrimRight(w.partial, "\r")
	w.partial = ""
	cb := w.onLine
	w.mu.Unlock()
	if cb != nil && rem != "" {
		cb(rem)
	}
}

func (w *lineStreamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimRight(w.all.String(), "\r\n")
}

func runCommandStreaming(ctx context.Context, c *exec.Cmd, onLine func(string)) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil || c.Process.Pid <= 0 {
			return nil
		}
		pgid := c.Process.Pid
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(-pgid, 0); err != nil {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	c.WaitDelay = 500 * time.Millisecond

	w := newLineStreamWriter(onLine)
	c.Stdout = w
	c.Stderr = w
	err := c.Run()
	w.Flush()

	if errors.Is(ctx.Err(), context.Canceled) {
		return w.String(), 130, context.Canceled
	}
	code := 0
	if err != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() != 0 {
			code = exitErr.ExitCode()
		}
	}
	return w.String(), code, err
}

func (b *TUIBackend) RunWSCommand(args []string) (tui.CommandRunResult, error) {
	return b.RunWSCommandStream(context.Background(), args, nil)
}

func (b *TUIBackend) RunWSCommandStream(ctx context.Context, args []string, onLine func(string)) (tui.CommandRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanArgs := make([]string, 0, len(args))
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a != "" {
			cleanArgs = append(cleanArgs, a)
		}
	}
	if len(cleanArgs) == 0 {
		return tui.CommandRunResult{}, fmt.Errorf("empty command")
	}
	if wsFirstSubcommand(cleanArgs) == "tui" {
		return tui.CommandRunResult{
			Command:  "ws " + strings.Join(cleanArgs, " "),
			ExitCode: 2,
			Output:   "refusing to launch nested ws tui inside active TUI",
			IsErr:    true,
		}, fmt.Errorf("refusing to launch nested ws tui")
	}

	displayCmd := "ws " + strings.Join(cleanArgs, " ")
	start := time.Now()

	hasNonInteractive := wsArgsHaveFlag(cleanArgs, "--non-interactive")
	fullArgs := make([]string, 0, len(cleanArgs)+5)
	if b.ctx.RepoRoot != "" && !wsArgsHaveFlag(cleanArgs, "--repo") {
		fullArgs = append(fullArgs, "--repo", b.ctx.RepoRoot)
	}
	if b.ctx.DryRun && !wsArgsHaveFlag(cleanArgs, "--dry-run", "-n") {
		fullArgs = append(fullArgs, "--dry-run")
	}
	if !hasNonInteractive {
		fullArgs = append(fullArgs, "--non-interactive")
	}
	fullArgs = append(fullArgs, cleanArgs...)

	exe, exeErr := os.Executable()
	if exeErr == nil && exe != "" && !strings.HasSuffix(exe, ".test") {
		c := exec.CommandContext(ctx, exe, fullArgs...)
		c.Env = app.PinnedMachineEnv(os.Environ())
		if b.ctx.HomeDir != "" {
			c.Env = shell.MergeEnv(c.Env, []string{"HOME=" + b.ctx.HomeDir})
		}
		outStr, code, err := runCommandStreaming(ctx, c, onLine)
		return tui.CommandRunResult{
			Command:  displayCmd,
			ExitCode: code,
			Duration: time.Since(start),
			Output:   outStr,
			IsErr:    err != nil,
		}, err
	}

	w := newLineStreamWriter(onLine)
	opts := &GlobalOpts{
		Repo:           b.ctx.RepoRoot,
		Home:           b.ctx.HomeDir,
		DryRun:         b.ctx.DryRun,
		NonInteractive: true,
	}
	cmd := NewRootCmd(opts)
	cmd.SetOut(w)
	cmd.SetErr(w)
	cmd.SetArgs(fullArgs)
	err := cmd.ExecuteContext(ctx)
	w.Flush()
	code := 0
	outStr := w.String()
	if err != nil {
		code = ExitCode(err)
		if errors.Is(err, context.Canceled) {
			code = 130
		}
		errLine := "error: " + err.Error()
		if onLine != nil {
			onLine(errLine)
		}
		if outStr != "" {
			outStr += "\n"
		}
		outStr += errLine
	}
	return tui.CommandRunResult{
		Command:  displayCmd,
		ExitCode: code,
		Duration: time.Since(start),
		Output:   strings.TrimRight(outStr, "\r\n"),
		IsErr:    err != nil,
	}, err
}

func loadDigest(wsHome string) (*pkgmgr.UpdateDigest, string) {
	if wsHome == "" {
		return nil, ""
	}
	path := filepath.Join(wsHome, "cache", "digest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ""
		}
		return nil, fmt.Sprintf("failed reading %s: %v", path, err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Sprintf("%s is empty; regenerate with ws update --digest --json --output %s", path, path)
	}

	var single pkgmgr.UpdateDigest
	if err := json.Unmarshal(trimmed, &single); err == nil {
		return &single, ""
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var latest *pkgmgr.UpdateDigest
	count := 0
	var decodeErr error
	for {
		var cur pkgmgr.UpdateDigest
		if err := dec.Decode(&cur); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			decodeErr = err
			break
		}
		copyCur := cur
		latest = &copyCur
		count++
	}
	if latest != nil && count > 0 {
		if decodeErr != nil {
			return latest, fmt.Sprintf("%s had trailing malformed data after %d record(s) (using latest); re-run ws schedule install", path, count)
		}
		return latest, fmt.Sprintf("%s contained %d concatenated records (using latest); re-run ws schedule install for atomic writes", path, count)
	}
	return nil, fmt.Sprintf("malformed digest JSON in %s: %v", path, decodeErr)
}

func (b *TUIBackend) LoadSettings() (tui.SettingsView, error) {
	themeIDs, presetIDs, zoneNames := gatherContextSettingOptions(b.ctx)
	entries := b.ctx.ListSettings(themeIDs, presetIDs, zoneNames)
	fp, watched := b.ctx.ConfigFingerprint()
	eff := b.ctx.EffectiveConfig()
	activeTheme := resolveActiveThemeID(b.ctx)
	if eff.TUI.Theme != "" {
		activeTheme = eff.TUI.Theme
	}

	items := make([]tui.SettingRow, 0, len(entries))
	for _, e := range entries {
		items = append(items, tui.SettingRow{
			Key:          e.Key,
			Category:     e.Category,
			Kind:         e.Kind,
			Value:        e.Value,
			DefaultValue: e.DefaultValue,
			Source:       e.Source,
			Options:      e.Options,
			Description:  e.Description,
			Overridden:   e.Overridden,
			ReadOnly:     e.ReadOnly,
		})
	}

	wRows := make([]tui.WatchedFileRow, 0, len(watched))
	fpMap := make(map[string]string, len(watched))
	for _, wf := range watched {
		fpMap[wf.Path] = wf.Fingerprint
		wRows = append(wRows, tui.WatchedFileRow{
			Label:       wf.Label,
			Path:        wf.Path,
			Exists:      wf.Exists,
			SizeBytes:   wf.SizeBytes,
			ModTime:     wf.ModTime,
			Fingerprint: wf.Fingerprint,
		})
	}
	b.mu.Lock()
	if b.watchedFP == nil {
		b.watchedFP = fpMap
	}
	b.mu.Unlock()

	cfgErrStr := ""
	if b.ctx.ConfigErr != nil {
		cfgErrStr = b.ctx.ConfigErr.Error()
	}
	miseErrStr := ""
	if b.ctx.MiseLocalErr != nil {
		miseErrStr = b.ctx.MiseLocalErr.Error()
	}

	return tui.SettingsView{
		ConfigPath:      b.ctx.WSConfigPath(),
		Mouse:           eff.TUI.Mouse,
		AutoReload:      app.BoolVal(eff.TUI.AutoReload, true),
		AutoReloadSecs:  eff.TUI.AutoReloadSecs,
		GHRefreshMins:   eff.TUI.GHRefreshMins,
		DefaultSplit:    eff.TUI.DefaultSplit,
		BorderedBar:     eff.TUI.BorderedBar,
		Theme:           activeTheme,
		DefaultZone:     eff.TUI.DefaultZone,
		StartTab:        eff.TUI.StartTab,
		FleetIncludeEnv: app.BoolVal(eff.Fleet.IncludeEnv, false),
		FleetIncludeDB:  app.BoolVal(eff.Fleet.IncludeDB, false),
		Fingerprint:     fp,
		ConfigErr:       cfgErrStr,
		MiseLocalErr:    miseErrStr,
		Items:           items,
		Watched:         wRows,
	}, nil
}

func (b *TUIBackend) invalidateConfigDependentCaches() {
	b.mu.Lock()
	b.miseCache = nil
	b.outdatedCache = nil
	b.watchedFP = nil
	b.mu.Unlock()
}

func (b *TUIBackend) SetSetting(key, value string) (tui.SettingsView, error) {
	if err := b.ctx.SetConfigOverride(key, value); err != nil {
		sv, _ := b.LoadSettings()
		return sv, err
	}
	b.invalidateConfigDependentCaches()
	return b.LoadSettings()
}

func (b *TUIBackend) ResetSetting(key string) (tui.SettingsView, error) {
	if err := b.ctx.ResetConfigOverride(key); err != nil {
		sv, _ := b.LoadSettings()
		return sv, err
	}
	b.invalidateConfigDependentCaches()
	return b.LoadSettings()
}

func (b *TUIBackend) CheckConfigWatch(prevFingerprint string) (tui.ConfigWatchResult, error) {
	fp, watched := b.ctx.ConfigFingerprint()
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.watchedFP == nil {
		b.watchedFP = make(map[string]string, len(watched))
		for _, wf := range watched {
			b.watchedFP[wf.Path] = wf.Fingerprint
		}
		return tui.ConfigWatchResult{
			Changed:     false,
			Fingerprint: fp,
		}, nil
	}

	var changedFiles []string
	nextMap := make(map[string]string, len(watched))
	for _, wf := range watched {
		nextMap[wf.Path] = wf.Fingerprint
		if prev, exists := b.watchedFP[wf.Path]; !exists || prev != wf.Fingerprint {
			changedFiles = append(changedFiles, wf.Label)
		}
	}
	for oldPath := range b.watchedFP {
		if _, ok := nextMap[oldPath]; !ok {
			changedFiles = append(changedFiles, filepath.Base(oldPath))
		}
	}
	sort.Strings(changedFiles)
	b.watchedFP = nextMap

	changed := len(changedFiles) > 0 || (prevFingerprint != "" && prevFingerprint != fp)
	return tui.ConfigWatchResult{
		Changed:      changed,
		Fingerprint:  fp,
		ChangedFiles: changedFiles,
	}, nil
}

func (b *TUIBackend) ReloadConfig() (tui.SettingsView, []string, error) {
	changed, reloadErr := b.ctx.ReloadConfig()
	b.invalidateConfigDependentCaches()
	sv, loadErr := b.LoadSettings()
	b.mu.Lock()
	nextMap := make(map[string]string, len(sv.Watched))
	for _, wf := range sv.Watched {
		nextMap[wf.Path] = wf.Fingerprint
	}
	b.watchedFP = nextMap
	b.mu.Unlock()
	if reloadErr != nil {
		return sv, changed, reloadErr
	}
	return sv, changed, loadErr
}

func (b *TUIBackend) EditConfigFileCmd(path string) (*exec.Cmd, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		target = b.ctx.WSConfigPath()
	}
	if target == "" {
		return nil, fmt.Errorf("config file path unavailable")
	}
	cleanTarget := filepath.Clean(target)
	allowed := cleanTarget == filepath.Clean(b.ctx.WSConfigPath())
	for _, wf := range b.ctx.WatchedConfigFiles() {
		if filepath.Clean(wf.Path) == cleanTarget {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("refusing to edit non-config path %s", target)
	}
	if fi, err := os.Lstat(cleanTarget); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to edit symlink %s", cleanTarget)
	}
	if b.ctx.HomeDir != "" && strings.HasPrefix(cleanTarget, filepath.Clean(b.ctx.HomeDir)+string(filepath.Separator)) {
		if _, err := store.ResolveSafeHomePath(b.ctx.HomeDir, cleanTarget); err != nil {
			return nil, err
		}
	}
	if !b.ctx.DryRun && cleanTarget == filepath.Clean(b.ctx.WSConfigPath()) {
		if _, err := os.Stat(cleanTarget); errors.Is(err, os.ErrNotExist) {
			if serr := app.SaveWSConfig(cleanTarget, b.ctx.Config); serr != nil {
				return nil, serr
			}
		}
	}
	return resolveEditorCmd(cleanTarget), nil
}
