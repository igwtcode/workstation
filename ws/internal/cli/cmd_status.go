package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

type StatusOutdatedItem struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Current  string `json:"current,omitempty"`
	Latest   string `json:"latest,omitempty"`
	Risk     string `json:"risk,omitempty"`
}

type StatusOutdatedSection struct {
	Available  bool                 `json:"available"`
	Source     string               `json:"source"`
	Count      int                  `json:"count"`
	AgeSeconds int64                `json:"ageSeconds,omitempty"`
	Age        string               `json:"age,omitempty"`
	Items      []StatusOutdatedItem `json:"items,omitempty"`
	Note       string               `json:"note,omitempty"`
	Error      string               `json:"error,omitempty"`
}

type StatusDoctorSection struct {
	Available      bool   `json:"available"`
	ErrorCount     int    `json:"errorCount"`
	WarnCount      int    `json:"warnCount"`
	InfoCount      int    `json:"infoCount"`
	TotalCount     int    `json:"totalCount"`
	LinksOK        int    `json:"linksOk"`
	LinksTotal     int    `json:"linksTotal"`
	TemplatesOK    int    `json:"templatesOk"`
	TemplatesTotal int    `json:"templatesTotal"`
	Error          string `json:"error,omitempty"`
}

type StatusRepoItem struct {
	Name      string `json:"name"`
	Zone      string `json:"zone,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Dirty     bool   `json:"dirty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	LocalPath string `json:"localPath"`
}

type StatusReposSection struct {
	Available      bool             `json:"available"`
	Source         string           `json:"source"`
	TotalCount     int              `json:"totalCount"`
	DirtyCount     int              `json:"dirtyCount"`
	BehindCount    int              `json:"behindCount"`
	AheadCount     int              `json:"aheadCount"`
	AttentionCount int              `json:"attentionCount"`
	Repos          []StatusRepoItem `json:"repos,omitempty"`
	Error          string           `json:"error,omitempty"`
}

type StatusPRItem struct {
	Repo      string `json:"repo"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	URL       string `json:"url,omitempty"`
	Zone      string `json:"zone,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type StatusPRsSection struct {
	Available bool           `json:"available"`
	Source    string         `json:"source"`
	Count     int            `json:"count"`
	Items     []StatusPRItem `json:"items,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type StatusUntrackedItem struct {
	Provider      string `json:"provider"`
	Name          string `json:"name"`
	SuggestedFile string `json:"suggestedFile,omitempty"`
}

type StatusUntrackedSection struct {
	Available bool                  `json:"available"`
	Count     int                   `json:"count"`
	Items     []StatusUntrackedItem `json:"items,omitempty"`
	Error     string                `json:"error,omitempty"`
}

type StatusBackupSection struct {
	Available        bool   `json:"available"`
	LastBackupAt     string `json:"lastBackupAt,omitempty"`
	LastBackupUnix   int64  `json:"lastBackupUnix,omitempty"`
	AgeSeconds       int64  `json:"ageSeconds,omitempty"`
	Age              string `json:"age"`
	DirtySinceBackup bool   `json:"dirtySinceBackup"`
	Error            string `json:"error,omitempty"`
}

type StatusReport struct {
	Host      string                 `json:"host"`
	Preset    string                 `json:"preset"`
	Zone      string                 `json:"zone,omitempty"`
	Outdated  StatusOutdatedSection  `json:"outdated"`
	Doctor    StatusDoctorSection    `json:"doctor"`
	Repos     StatusReposSection     `json:"repos"`
	ReviewPRs StatusPRsSection       `json:"reviewPRs"`
	Untracked StatusUntrackedSection `json:"untracked"`
	Backup    StatusBackupSection    `json:"backup"`
}

func newStatusCmd(opts *GlobalOpts) *cobra.Command {
	var refresh bool
	var zone string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Read-only workstation overview (packages, doctor, repos, PRs, backup)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			rep := collectStatusReport(ctx, statusCollectOptions{
				Refresh: refresh,
				Zone:    zone,
			})
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			printStatusReport(cmd.OutOrStdout(), loadWriterRoles(ctx, cmd.OutOrStdout()), rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Query live package outdated digest and refresh GitHub PR cache")
	cmd.Flags().StringVar(&zone, "zone", "", "Filter repos and PRs to a specific zone")
	return cmd
}

type statusCollectOptions struct {
	Refresh bool
	Zone    string
	Now     func() time.Time
}

func collectStatusReport(ctx *app.Context, opts statusCollectOptions) StatusReport {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()

	hostLabel := strings.Trim(ctx.Host.OS+"/"+ctx.Host.Arch, "/")
	if ctx.Host.Distro != "" && ctx.Host.Distro != ctx.Host.OS {
		hostLabel = ctx.Host.Distro + " " + hostLabel
	}
	rep := StatusReport{
		Host:   hostLabel,
		Preset: ctx.EffectivePreset(),
		Zone:   strings.TrimSpace(opts.Zone),
	}

	var db *store.DB
	if dbPath := store.DefaultPath(ctx.WSBaseDir()); dbPath != "" {
		if fi, err := os.Stat(dbPath); err == nil && !fi.IsDir() {
			if opened, oerr := store.Open(dbPath); oerr == nil {
				db = opened
				defer func() { _ = db.Close() }()
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(6)

	go func() {
		defer wg.Done()
		rep.Outdated = collectStatusOutdated(ctx, opts.Refresh, now)
	}()

	go func() {
		defer wg.Done()
		rep.Doctor = collectStatusDoctor(ctx)
	}()

	go func() {
		defer wg.Done()
		rep.Repos = collectStatusRepos(ctx, db, opts.Zone)
	}()

	go func() {
		defer wg.Done()
		rep.ReviewPRs = collectStatusReviewPRs(ctx, db, opts.Zone, opts.Refresh)
	}()

	go func() {
		defer wg.Done()
		rep.Untracked = collectStatusUntracked(ctx)
	}()

	go func() {
		defer wg.Done()
		rep.Backup = collectStatusBackup(db, now)
	}()

	wg.Wait()
	return rep
}

func collectStatusOutdated(ctx *app.Context, refresh bool, now time.Time) StatusOutdatedSection {
	if !refresh {
		digest, warn := loadDigest(ctx.WSBaseDir())
		if digest != nil {
			sec := StatusOutdatedSection{
				Available: true,
				Source:    "cache",
				Count:     len(digest.Items),
				Note:      warn,
			}
			if fi, err := os.Stat(ctx.WSDigestPath()); err == nil {
				d := now.Sub(fi.ModTime())
				if d < 0 {
					d = 0
				}
				sec.AgeSeconds = int64(d.Seconds())
				sec.Age = formatStatusAge(d)
			}
			for _, it := range digest.Items {
				sec.Items = append(sec.Items, StatusOutdatedItem{
					Provider: it.Provider,
					Name:     it.Name,
					Current:  it.Current,
					Latest:   it.Latest,
					Risk:     string(it.Risk),
				})
			}
			return sec
		}
		if warn != "" {
			return StatusOutdatedSection{
				Available: false,
				Source:    "cache",
				Error:     warn,
			}
		}
		return StatusOutdatedSection{
			Available: false,
			Source:    "none",
			Note:      "no cached update digest (run ws update --digest or pass --refresh)",
		}
	}

	digest, err := pkgmgr.BuildUpdateDigest(ctx, true)
	if err != nil {
		return StatusOutdatedSection{
			Available: false,
			Source:    "live",
			Error:     err.Error(),
		}
	}
	sec := StatusOutdatedSection{
		Available: true,
		Source:    "live",
		Count:     len(digest.Items),
	}
	for _, it := range digest.Items {
		sec.Items = append(sec.Items, StatusOutdatedItem{
			Provider: it.Provider,
			Name:     it.Name,
			Current:  it.Current,
			Latest:   it.Latest,
			Risk:     string(it.Risk),
		})
	}
	return sec
}

func collectStatusDoctor(ctx *app.Context) StatusDoctorSection {
	doc, err := engine.RunDoctor(ctx)
	if err != nil {
		return StatusDoctorSection{
			Available: false,
			Error:     err.Error(),
		}
	}
	sec := StatusDoctorSection{
		Available:      true,
		TotalCount:     len(doc.Findings),
		LinksOK:        doc.LinksOK,
		LinksTotal:     doc.LinksTotal,
		TemplatesOK:    doc.TemplatesOK,
		TemplatesTotal: doc.TemplatesTotal,
	}
	for _, f := range doc.Findings {
		switch strings.ToLower(strings.TrimSpace(string(f.Severity))) {
		case "err", "error":
			sec.ErrorCount++
		case "warn", "warning":
			sec.WarnCount++
		default:
			sec.InfoCount++
		}
	}
	return sec
}

func collectStatusRepos(ctx *app.Context, db *store.DB, zone string) StatusReposSection {
	var records []projects.RepoRecord
	source := "cache"
	if db != nil {
		if cached, err := projects.ListCachedRepos(db); err == nil && len(cached) > 0 {
			records = cached
		}
	}
	if len(records) == 0 {
		source = "scan"
		scanned, err := projects.ScanRepos(ctx, nil)
		if err != nil {
			return StatusReposSection{
				Available: false,
				Source:    source,
				Error:     err.Error(),
			}
		}
		records = scanned
	}

	z := strings.TrimSpace(zone)
	if strings.EqualFold(z, "all") {
		z = ""
	}
	sec := StatusReposSection{
		Available: true,
		Source:    source,
	}
	for _, r := range records {
		if z != "" && !strings.EqualFold(r.Zone, z) {
			continue
		}
		sec.TotalCount++
		if r.Dirty {
			sec.DirtyCount++
		}
		if r.Behind > 0 {
			sec.BehindCount++
		}
		if r.Ahead > 0 {
			sec.AheadCount++
		}
		if r.Dirty || r.Behind > 0 {
			sec.AttentionCount++
			sec.Repos = append(sec.Repos, StatusRepoItem{
				Name:      r.Name,
				Zone:      r.Zone,
				Branch:    r.Branch,
				Dirty:     r.Dirty,
				Ahead:     r.Ahead,
				Behind:    r.Behind,
				LocalPath: ctx.CollapseHome(r.LocalPath),
			})
		}
	}
	return sec
}

func detectLocalGHLogins(ctx *app.Context) map[string]bool {
	out := map[string]bool{}
	var paths []string
	if envDir := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR")); envDir != "" {
		paths = append(paths, filepath.Join(envDir, "hosts.yml"))
	}
	if ctx != nil && ctx.ConfigDir != "" {
		paths = append(paths, filepath.Join(ctx.ConfigDir, "gh", "hosts.yml"))
	}
	if ctx != nil {
		if zstats, err := shell.ZoneStatuses(ctx); err == nil {
			for _, zs := range zstats {
				if zs.Root != "" {
					paths = append(paths, filepath.Join(zs.Root, ".config", "gh", "hosts.yml"))
				}
			}
		}
	}
	seenPath := map[string]bool{}
	for _, p := range paths {
		clean := filepath.Clean(p)
		if seenPath[clean] {
			continue
		}
		seenPath[clean] = true
		raw, err := os.ReadFile(clean)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(ln)
			if strings.HasPrefix(trimmed, "user:") {
				u := strings.TrimSpace(strings.Trim(strings.TrimPrefix(trimmed, "user:"), `"'`))
				if u != "" {
					out[strings.ToLower(u)] = true
				}
			}
		}
	}
	return out
}

func collectStatusReviewPRs(ctx *app.Context, db *store.DB, zone string, refresh bool) StatusPRsSection {
	readCtx := *ctx
	if !refresh {
		readCtx.DryRun = true
	}
	items, notes, err := projects.FetchGitHubItemsForZone(&readCtx, db, zone, refresh, nil)
	if err != nil {
		return StatusPRsSection{
			Available: false,
			Source:    "cache",
			Notes:     notes,
			Error:     err.Error(),
		}
	}
	if len(items) == 0 && len(notes) > 0 {
		return StatusPRsSection{
			Available: false,
			Source:    "cache",
			Notes:     notes,
			Error:     strings.Join(notes, "; "),
		}
	}

	myLogins := detectLocalGHLogins(ctx)
	sec := StatusPRsSection{
		Available: true,
		Source:    "cache",
		Notes:     notes,
	}
	if refresh {
		sec.Source = "live"
	}
	for _, it := range items {
		if !strings.EqualFold(it.Kind, "pr") {
			continue
		}
		if it.State != "" && !strings.EqualFold(it.State, "open") {
			continue
		}
		if it.IsDraft {
			continue
		}
		if len(myLogins) > 0 && it.Author != "" && myLogins[strings.ToLower(it.Author)] {
			continue
		}
		upd := ""
		if it.UpdatedAt > 0 {
			upd = time.Unix(it.UpdatedAt, 0).UTC().Format(time.RFC3339)
		}
		sec.Items = append(sec.Items, StatusPRItem{
			Repo:      it.Repo,
			Number:    it.Number,
			Title:     it.Title,
			Author:    it.Author,
			URL:       it.URL,
			Zone:      it.Zone,
			UpdatedAt: upd,
		})
	}
	sec.Count = len(sec.Items)
	return sec
}

func collectStatusUntracked(ctx *app.Context) StatusUntrackedSection {
	pkgs, err := pkgmgr.DetectUntrackedPackages(ctx, ctx.EffectivePreset(), nil)
	if err != nil {
		return StatusUntrackedSection{
			Available: false,
			Error:     err.Error(),
		}
	}
	sec := StatusUntrackedSection{
		Available: true,
		Count:     len(pkgs),
	}
	for _, p := range pkgs {
		sec.Items = append(sec.Items, StatusUntrackedItem{
			Provider:      p.Provider,
			Name:          p.Name,
			SuggestedFile: p.SuggestedFile,
		})
	}
	return sec
}

func collectStatusBackup(db *store.DB, now time.Time) StatusBackupSection {
	if db == nil {
		return StatusBackupSection{
			Available: false,
			Age:       "never",
			Error:     "ws.db not initialized",
		}
	}
	stats, err := db.Stats()
	if err != nil {
		return StatusBackupSection{
			Available: false,
			Age:       "unknown",
			Error:     err.Error(),
		}
	}
	sec := StatusBackupSection{
		Available:        true,
		LastBackupUnix:   stats.LastBackupAt,
		DirtySinceBackup: stats.DirtySinceBackup,
		Age:              "never",
	}
	if stats.LastBackupAt > 0 {
		ts := time.Unix(stats.LastBackupAt, 0)
		d := now.Sub(ts)
		if d < 0 {
			d = 0
		}
		sec.LastBackupAt = ts.UTC().Format(time.RFC3339)
		sec.AgeSeconds = int64(d.Seconds())
		sec.Age = formatStatusAge(d)
	}
	return sec
}

func formatStatusAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func printStatusReport(w io.Writer, roles ui.Roles, rep StatusReport) {
	log := ui.NewLogger(w, roles)
	log.KV("Host", 14, rep.Host)
	log.KV("Preset", 14, rep.Preset)
	if rep.Zone != "" {
		log.KV("Zone", 14, rep.Zone)
	}

	outStr := ""
	if !rep.Outdated.Available {
		msg := nonEmptyCLI(rep.Outdated.Error, rep.Outdated.Note)
		outStr = roles.Muted.Render("unavailable (" + msg + ")")
	} else if rep.Outdated.Count == 0 {
		outStr = roles.OK.Render(fmt.Sprintf("0 outdated (%s)", rep.Outdated.Source))
	} else {
		outStr = roles.Warn.Render(fmt.Sprintf("%d outdated (%s)", rep.Outdated.Count, rep.Outdated.Source))
	}
	log.KV("Outdated", 14, outStr)

	docStr := ""
	if !rep.Doctor.Available {
		docStr = roles.Muted.Render("unavailable (" + rep.Doctor.Error + ")")
	} else {
		summary := fmt.Sprintf("%d err, %d warn, %d info (links %d/%d, templates %d/%d)",
			rep.Doctor.ErrorCount, rep.Doctor.WarnCount, rep.Doctor.InfoCount,
			rep.Doctor.LinksOK, rep.Doctor.LinksTotal, rep.Doctor.TemplatesOK, rep.Doctor.TemplatesTotal)
		switch {
		case rep.Doctor.ErrorCount > 0:
			docStr = roles.Err.Render(summary)
		case rep.Doctor.WarnCount > 0:
			docStr = roles.Warn.Render(summary)
		default:
			docStr = roles.OK.Render(summary)
		}
	}
	log.KV("Doctor", 14, docStr)

	repoStr := ""
	if !rep.Repos.Available {
		repoStr = roles.Muted.Render("unavailable (" + rep.Repos.Error + ")")
	} else if rep.Repos.AttentionCount == 0 {
		repoStr = roles.OK.Render(fmt.Sprintf("all %d repos clean & synced", rep.Repos.TotalCount))
	} else {
		repoStr = roles.Warn.Render(fmt.Sprintf("%d dirty, %d behind (of %d)", rep.Repos.DirtyCount, rep.Repos.BehindCount, rep.Repos.TotalCount))
	}
	log.KV("Repos", 14, repoStr)

	prStr := ""
	if !rep.ReviewPRs.Available {
		prStr = roles.Muted.Render("unavailable (" + nonEmptyCLI(rep.ReviewPRs.Error, "no gh auth") + ")")
	} else if rep.ReviewPRs.Count == 0 {
		prStr = roles.OK.Render("0 awaiting review")
	} else {
		prStr = roles.Info.Render(fmt.Sprintf("%d awaiting review (%s)", rep.ReviewPRs.Count, rep.ReviewPRs.Source))
	}
	log.KV("Review PRs", 14, prStr)

	untrStr := ""
	if !rep.Untracked.Available {
		untrStr = roles.Muted.Render("unavailable (" + rep.Untracked.Error + ")")
	} else if rep.Untracked.Count == 0 {
		untrStr = roles.OK.Render("0 untracked")
	} else {
		untrStr = roles.Warn.Render(fmt.Sprintf("%d untracked", rep.Untracked.Count))
	}
	log.KV("Untracked", 14, untrStr)

	bkStr := ""
	if !rep.Backup.Available {
		bkStr = roles.Muted.Render("unavailable (" + rep.Backup.Error + ")")
	} else if rep.Backup.LastBackupUnix == 0 {
		bkStr = roles.Warn.Render("never")
	} else if rep.Backup.DirtySinceBackup {
		bkStr = roles.Warn.Render(rep.Backup.Age + " (dirty since backup)")
	} else {
		bkStr = roles.OK.Render(rep.Backup.Age + " (clean)")
	}
	log.KV("Last Backup", 14, bkStr)
}
