package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
)

type GHItem struct {
	Kind          string   `json:"kind"`
	Repo          string   `json:"repo"`
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	State         string   `json:"state"`
	Author        string   `json:"author"`
	URL           string   `json:"url"`
	Zone          string   `json:"zone"`
	Body          string   `json:"body,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Assignees     []string `json:"assignees,omitempty"`
	Milestone     string   `json:"milestone,omitempty"`
	IsDraft       bool     `json:"isDraft,omitempty"`
	CommentsCount int      `json:"commentsCount,omitempty"`
	HeadRef       string   `json:"headRef,omitempty"`
	CreatedAt     int64    `json:"createdAt,omitempty"`
	UpdatedAt     int64    `json:"updatedAt"`
	FetchedAt     int64    `json:"fetchedAt"`
	Source        string   `json:"source,omitempty"`
}

type GHComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"createdAt"`
}

type GHItemDetail struct {
	GHItem
	HeadRef        string      `json:"headRef,omitempty"`
	BaseRef        string      `json:"baseRef,omitempty"`
	ReviewDecision string      `json:"reviewDecision,omitempty"`
	Mergeable      string      `json:"mergeable,omitempty"`
	ChecksSummary  string      `json:"checksSummary,omitempty"`
	Additions      int         `json:"additions,omitempty"`
	Deletions      int         `json:"deletions,omitempty"`
	ChangedFiles   int         `json:"changedFiles,omitempty"`
	Comments       []GHComment `json:"comments,omitempty"`
}

type CommandRunner interface {
	Output(env []string, dir, name string, args ...string) ([]byte, error)
}

const (
	ghCommandTimeout    = 15 * time.Second
	cloneCommandTimeout = 2 * time.Minute
)

type execCommandRunner struct {
	timeout time.Duration
}

func (r execCommandRunner) Output(env []string, dir, name string, args ...string) ([]byte, error) {
	timeout := r.timeout
	if timeout <= 0 {
		if name == "git" && len(args) > 0 && args[0] == "config" {
			timeout = ghCommandTimeout
		} else {
			timeout = cloneCommandTimeout
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s timed out after %s: %w", name, timeout, ctx.Err())
	}
	return out, err
}

const ghCacheTTLSeconds int64 = 3600

type ghNamedLogin struct {
	Login string `json:"login"`
}

type ghNamedLabel struct {
	Name string `json:"name"`
}

type ghCommentEntry struct {
	Author    ghNamedLogin `json:"author"`
	Body      string       `json:"body"`
	CreatedAt string       `json:"createdAt"`
}

type ghCheckEntry struct {
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
}

type ghSearchEntry struct {
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
		FullName      string `json:"fullName"`
		Name          string `json:"name"`
	} `json:"repository"`
	Number    int            `json:"number"`
	Title     string         `json:"title"`
	State     string         `json:"state"`
	Author    ghNamedLogin   `json:"author"`
	URL       string         `json:"url"`
	Body      string         `json:"body"`
	Labels    []ghNamedLabel `json:"labels"`
	Assignees []ghNamedLogin `json:"assignees"`
	Milestone *struct {
		Title string `json:"title"`
	} `json:"milestone"`
	IsDraft       bool             `json:"isDraft"`
	HeadRefName   string           `json:"headRefName"`
	CommentsCount int              `json:"commentsCount"`
	Comments      []ghCommentEntry `json:"comments"`
	CreatedAt     string           `json:"createdAt"`
	UpdatedAt     string           `json:"updatedAt"`
}

type ghDetailEntry struct {
	ghSearchEntry
	BaseRefName       string         `json:"baseRefName"`
	ReviewDecision    string         `json:"reviewDecision"`
	Mergeable         string         `json:"mergeable"`
	Additions         int            `json:"additions"`
	Deletions         int            `json:"deletions"`
	ChangedFiles      int            `json:"changedFiles"`
	StatusCheckRollup []ghCheckEntry `json:"statusCheckRollup"`
}

type ghZoneTarget struct {
	zone        string
	zoneRoot    string
	ghConfigDir string
	ghEnv       map[string]string
	explicitEnv bool
}

const (
	ghSearchPRFields    = "repository,number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,commentsCount,isDraft"
	ghSearchIssueFields = "repository,number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,commentsCount"
	ghListPRFields      = "number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,milestone,isDraft,headRefName"
	ghListIssueFields   = "number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,milestone"
	ghViewPRFields      = "number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,milestone,isDraft,headRefName,baseRefName,reviewDecision,mergeable,additions,deletions,changedFiles,comments,statusCheckRollup"
	ghViewIssueFields   = "number,title,state,author,url,updatedAt,createdAt,body,labels,assignees,milestone,comments"
)

func FetchGitHubItems(ctx *app.Context, db *store.DB, refresh bool, runner CommandRunner) ([]GHItem, []string, error) {
	return FetchGitHubItemsForZone(ctx, db, "", refresh, runner)
}

func FetchGitHubItemsForZone(ctx *app.Context, db *store.DB, zone string, refresh bool, runner CommandRunner) ([]GHItem, []string, error) {
	now := time.Now().Unix()
	zone = strings.TrimSpace(zone)
	if strings.EqualFold(zone, "all") {
		zone = ""
	}

	var cached []GHItem
	var cacheFetchedAt int64
	cacheFormatV2 := false
	if db != nil {
		if rows, maxFetched, err := loadCachedGHItems(db, "involved"); err == nil {
			cached = rows
			cacheFetchedAt = maxFetched
		}
		if rawTS, ok, err := db.GetMeta("gh_cache_fetched_at"); err == nil && ok {
			if ts, perr := strconv.ParseInt(rawTS, 10, 64); perr == nil && ts > cacheFetchedAt {
				cacheFetchedAt = ts
			}
		}
		if fmtVer, ok, err := db.GetMeta("gh_cache_format"); err == nil && ok && fmtVer == "2" {
			cacheFormatV2 = true
		}
	}

	if !refresh && cacheFormatV2 && cacheFetchedAt > 0 && now-cacheFetchedAt < ghCacheTTLSeconds {
		return filterGHItemsByZoneSlice(cached, zone), nil, nil
	}

	if runner == nil {
		if _, err := exec.LookPath("gh"); err != nil {
			return filterGHItemsByZoneSlice(cached, zone), []string{"gh CLI not found on PATH; skipping GitHub PR/issue fetch"}, nil
		}
		runner = execCommandRunner{timeout: ghCommandTimeout}
	}

	repoZones := buildLocalRepoZoneMap(db)
	targets := selectGHTargetsForZone(ctx, zone)
	queries := []struct {
		kind string
		args []string
	}{
		{
			kind: "pr",
			args: []string{"search", "prs", "--involves=@me", "--state=open", "--limit=100", "--json", ghSearchPRFields},
		},
		{
			kind: "pr",
			args: []string{"search", "prs", "--review-requested=@me", "--state=open", "--limit=100", "--json", ghSearchPRFields},
		},
		{
			kind: "issue",
			args: []string{"search", "issues", "--involves=@me", "--state=open", "--limit=100", "--json", ghSearchIssueFields},
		},
	}

	type itemKey struct {
		kind   string
		repo   string
		number int
	}

	dedup := map[itemKey]GHItem{}
	succeededScopes := map[string]bool{}
	var notes []string
	succeeded := 0

	for _, t := range targets {
		env := buildGHEnvForTarget(os.Environ(), t)
		workDir := targetWorkDir(ctx, t)
		zoneLabel := t.zone
		if zoneLabel == "" {
			zoneLabel = "default"
		}

		for _, q := range queries {
			out, err := runner.Output(env, workDir, "gh", q.args...)
			if err != nil {
				msg := describeGHErr(err)
				notes = append(notes, fmt.Sprintf("github (%s): %s", zoneLabel, msg))
				if isGHFatalForZone(err, msg) {
					break
				}
				continue
			}

			succeeded++
			succeededScopes[strings.ToLower(t.zone)+"\x00"+q.kind] = true
			trimmed := strings.TrimSpace(string(out))
			if trimmed == "" {
				continue
			}
			var parsed []ghSearchEntry
			if jerr := json.Unmarshal([]byte(trimmed), &parsed); jerr != nil {
				notes = append(notes, fmt.Sprintf("github (%s): decode output: %v", zoneLabel, jerr))
				continue
			}
			for _, e := range parsed {
				item := searchEntryToGHItem(q.kind, "", t.zone, "involved", now, e, repoZones)
				k := itemKey{kind: item.Kind, repo: strings.ToLower(item.Repo), number: item.Number}
				if existing, exists := dedup[k]; !exists || item.UpdatedAt > existing.UpdatedAt {
					dedup[k] = item
				}
			}
		}
	}

	if succeeded == 0 {
		return filterGHItemsByZoneSlice(cached, zone), notes, nil
	}

	items := make([]GHItem, 0, len(dedup))
	for _, it := range dedup {
		items = append(items, it)
	}
	sortGHItems(items)

	if db != nil && (ctx == nil || !ctx.DryRun) {
		if err := replaceCachedGHItemsScoped(db, items, succeededScopes, now); err != nil {
			return filterGHItemsByZoneSlice(items, zone), notes, err
		}
	}

	return filterGHItemsByZoneSlice(items, zone), notes, nil
}

func FetchRepoGitHubItems(ctx *app.Context, db *store.DB, repoSlug, zone, state string, refresh bool, runner CommandRunner) ([]GHItem, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) {
		return nil, fmt.Errorf("invalid GitHub repo slug %q (expected owner/repo)", repoSlug)
	}
	state = normalizeGHState(state)
	now := time.Now().Unix()
	cacheKey := fmt.Sprintf("gh_repo_fetched:%s:%s", strings.ToLower(repoSlug), state)

	var cached []GHItem
	var cacheFetchedAt int64
	if db != nil {
		if rows, err := loadCachedRepoGHItems(db, repoSlug, state); err == nil {
			cached = rows
		}
		if rawTS, ok, err := db.GetMeta(cacheKey); err == nil && ok {
			if ts, perr := strconv.ParseInt(rawTS, 10, 64); perr == nil {
				cacheFetchedAt = ts
			}
		}
	}

	if !refresh && cacheFetchedAt > 0 && now-cacheFetchedAt < ghCacheTTLSeconds {
		return cached, nil
	}

	if runner == nil {
		if _, err := exec.LookPath("gh"); err != nil {
			return cached, fmt.Errorf("gh CLI not found on PATH")
		}
		runner = execCommandRunner{timeout: ghCommandTimeout}
	}

	repoZones := buildLocalRepoZoneMap(db)
	resolvedZone := strings.TrimSpace(zone)
	if resolvedZone == "" {
		resolvedZone = repoZones[strings.ToLower(repoSlug)]
	}
	targets := selectGHTargetsForZone(ctx, resolvedZone)

	queries := []struct {
		kind string
		args []string
	}{
		{
			kind: "pr",
			args: []string{"pr", "list", "-R", repoSlug, "--state", state, "--limit", "100", "--json", ghListPRFields},
		},
		{
			kind: "issue",
			args: []string{"issue", "list", "-R", repoSlug, "--state", state, "--limit", "100", "--json", ghListIssueFields},
		},
	}

	type itemKey struct {
		kind   string
		number int
	}
	dedup := map[itemKey]GHItem{}
	var lastErr error
	succeeded := 0

	for _, t := range targets {
		env := buildGHEnvForTarget(os.Environ(), t)
		workDir := targetWorkDir(ctx, t)
		zoneSucceeded := 0
		for _, q := range queries {
			out, err := runner.Output(env, workDir, "gh", q.args...)
			if err != nil {
				msg := describeGHErr(err)
				lastErr = fmt.Errorf("gh %s list %s: %s", q.kind, repoSlug, msg)
				if isGHFatalForZone(err, msg) || isGHRepoNotFound(msg) {
					break
				}
				continue
			}
			zoneSucceeded++
			succeeded++
			trimmed := strings.TrimSpace(string(out))
			if trimmed == "" {
				continue
			}
			var parsed []ghSearchEntry
			if jerr := json.Unmarshal([]byte(trimmed), &parsed); jerr != nil {
				lastErr = fmt.Errorf("decode gh %s list %s: %w", q.kind, repoSlug, jerr)
				continue
			}
			itZone := resolvedZone
			if itZone == "" {
				itZone = t.zone
			}
			for _, e := range parsed {
				item := searchEntryToGHItem(q.kind, repoSlug, itZone, "repo", now, e, repoZones)
				dedup[itemKey{kind: item.Kind, number: item.Number}] = item
			}
		}
		if zoneSucceeded > 0 {
			break
		}
	}

	if succeeded == 0 {
		if len(cached) > 0 {
			return cached, nil
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no GitHub results for %s", repoSlug)
	}

	items := make([]GHItem, 0, len(dedup))
	for _, it := range dedup {
		items = append(items, it)
	}
	sortGHItems(items)

	if db != nil && (ctx == nil || !ctx.DryRun) {
		if err := upsertRepoCachedGHItems(db, repoSlug, state, items, now, cacheKey); err != nil {
			return items, err
		}
	}

	return items, nil
}

func FetchGHItemDetail(ctx *app.Context, kind, repoSlug, zone string, number int, runner CommandRunner) (GHItemDetail, error) {
	return FetchGHItemDetailCached(ctx, nil, kind, repoSlug, zone, number, true, runner)
}

func FetchGHItemDetailCached(ctx *app.Context, db *store.DB, kind, repoSlug, zone string, number int, refresh bool, runner CommandRunner) (GHItemDetail, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "pr" && kind != "issue" {
		return GHItemDetail{}, fmt.Errorf("unsupported GitHub item kind %q", kind)
	}
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) {
		return GHItemDetail{}, fmt.Errorf("invalid GitHub repo slug %q", repoSlug)
	}
	if number <= 0 {
		return GHItemDetail{}, fmt.Errorf("invalid GitHub item number %d", number)
	}

	now := time.Now().Unix()
	var cachedDetail GHItemDetail
	hasCached := false
	var cacheFetchedAt int64
	if db != nil {
		if det, fetchedAt, ok := loadCachedGHDetail(db, kind, repoSlug, number); ok {
			cachedDetail = det
			hasCached = true
			cacheFetchedAt = fetchedAt
		}
	}
	if !refresh && hasCached && cacheFetchedAt > 0 && now-cacheFetchedAt < ghCacheTTLSeconds {
		return cachedDetail, nil
	}

	if runner == nil {
		if _, err := exec.LookPath("gh"); err != nil {
			if hasCached {
				return cachedDetail, nil
			}
			return GHItemDetail{}, fmt.Errorf("gh CLI not found on PATH")
		}
		runner = execCommandRunner{timeout: ghCommandTimeout}
	}

	args := []string{kind, "view", strconv.Itoa(number), "-R", repoSlug, "--json"}
	if kind == "pr" {
		args = append(args, ghViewPRFields)
	} else {
		args = append(args, ghViewIssueFields)
	}

	targets := selectGHTargetsForZone(ctx, strings.TrimSpace(zone))
	var out []byte
	var lastErr error
	for _, t := range targets {
		env := buildGHEnvForTarget(os.Environ(), t)
		workDir := targetWorkDir(ctx, t)
		raw, err := runner.Output(env, workDir, "gh", args...)
		if err != nil {
			lastErr = fmt.Errorf("gh %s view #%d: %s", kind, number, describeGHErr(err))
			if errors.Is(err, exec.ErrNotFound) {
				break
			}
			continue
		}
		out = raw
		lastErr = nil
		break
	}
	if lastErr != nil {
		if hasCached {
			return cachedDetail, nil
		}
		return GHItemDetail{}, lastErr
	}

	var parsed ghDetailEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &parsed); err != nil {
		return GHItemDetail{}, fmt.Errorf("decode gh %s view #%d: %w", kind, number, err)
	}

	base := searchEntryToGHItem(kind, repoSlug, zone, "detail", now, parsed.ghSearchEntry, nil)
	comments := make([]GHComment, 0, min(len(parsed.Comments), 5))
	startIdx := 0
	if len(parsed.Comments) > 5 {
		startIdx = len(parsed.Comments) - 5
	}
	for _, c := range parsed.Comments[startIdx:] {
		var ts int64
		if parsedTS, perr := time.Parse(time.RFC3339, c.CreatedAt); perr == nil {
			ts = parsedTS.Unix()
		}
		comments = append(comments, GHComment{
			Author:    c.Author.Login,
			Body:      c.Body,
			CreatedAt: ts,
		})
	}

	det := GHItemDetail{
		GHItem:         base,
		HeadRef:        parsed.HeadRefName,
		BaseRef:        parsed.BaseRefName,
		ReviewDecision: parsed.ReviewDecision,
		Mergeable:      parsed.Mergeable,
		ChecksSummary:  summarizeGHChecks(parsed.StatusCheckRollup),
		Additions:      parsed.Additions,
		Deletions:      parsed.Deletions,
		ChangedFiles:   parsed.ChangedFiles,
		Comments:       comments,
	}
	if db != nil && (ctx == nil || !ctx.DryRun) {
		_ = upsertCachedGHDetail(db, kind, repoSlug, number, det, now)
	}
	return det, nil
}

func summarizeGHChecks(checks []ghCheckEntry) string {
	if len(checks) == 0 {
		return ""
	}
	passed, failed, pending := 0, 0, 0
	for _, c := range checks {
		conc := strings.ToUpper(strings.TrimSpace(c.Conclusion))
		st := strings.ToUpper(strings.TrimSpace(c.Status))
		state := strings.ToUpper(strings.TrimSpace(c.State))
		switch {
		case conc == "SUCCESS" || conc == "NEUTRAL" || conc == "SKIPPED" || state == "SUCCESS":
			passed++
		case conc == "FAILURE" || conc == "TIMED_OUT" || conc == "CANCELLED" || conc == "ACTION_REQUIRED" || state == "FAILURE" || state == "ERROR":
			failed++
		case st == "IN_PROGRESS" || st == "QUEUED" || st == "PENDING" || state == "PENDING" || conc == "":
			pending++
		default:
			passed++
		}
	}
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", passed))
	}
	return strings.Join(parts, ", ")
}

func normalizeGHState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "closed":
		return "closed"
	case "all":
		return "all"
	default:
		return "open"
	}
}

func buildLocalRepoZoneMap(db *store.DB) map[string]string {
	out := map[string]string{}
	if db == nil {
		return out
	}
	repos, err := ListCachedRepos(db)
	if err != nil {
		return out
	}
	for _, r := range repos {
		if slug := ExtractGitHubRepoSlug(r.RemoteURL); slug != "" && r.Zone != "" {
			out[strings.ToLower(slug)] = r.Zone
		}
	}
	return out
}

func searchEntryToGHItem(kind, fallbackRepo, defaultZone, source string, fetchedAt int64, e ghSearchEntry, repoZones map[string]string) GHItem {
	repoName := e.Repository.NameWithOwner
	if repoName == "" {
		repoName = e.Repository.FullName
	}
	if repoName == "" {
		repoName = e.Repository.Name
	}
	if repoName == "" {
		repoName = fallbackRepo
	}

	var updatedUnix, createdUnix int64
	if ts, perr := time.Parse(time.RFC3339, e.UpdatedAt); perr == nil {
		updatedUnix = ts.Unix()
	}
	if ts, perr := time.Parse(time.RFC3339, e.CreatedAt); perr == nil {
		createdUnix = ts.Unix()
	}

	labels := make([]string, 0, len(e.Labels))
	for _, l := range e.Labels {
		if name := strings.TrimSpace(l.Name); name != "" {
			labels = append(labels, name)
		}
	}
	assignees := make([]string, 0, len(e.Assignees))
	for _, a := range e.Assignees {
		if login := strings.TrimSpace(a.Login); login != "" {
			assignees = append(assignees, login)
		}
	}
	commentsCount := e.CommentsCount
	if len(e.Comments) > commentsCount {
		commentsCount = len(e.Comments)
	}

	zone := defaultZone
	if repoZones != nil && repoName != "" {
		if rz, ok := repoZones[strings.ToLower(repoName)]; ok && rz != "" {
			zone = rz
		}
	}

	milestone := ""
	if e.Milestone != nil {
		milestone = strings.TrimSpace(e.Milestone.Title)
	}

	return GHItem{
		Kind:          kind,
		Repo:          repoName,
		Number:        e.Number,
		Title:         e.Title,
		State:         strings.ToLower(strings.TrimSpace(e.State)),
		Author:        e.Author.Login,
		URL:           e.URL,
		Zone:          zone,
		Body:          e.Body,
		Labels:        labels,
		Assignees:     assignees,
		Milestone:     milestone,
		IsDraft:       e.IsDraft,
		CommentsCount: commentsCount,
		HeadRef:       strings.TrimSpace(e.HeadRefName),
		CreatedAt:     createdUnix,
		UpdatedAt:     updatedUnix,
		FetchedAt:     fetchedAt,
		Source:        source,
	}
}

func filterGHItemsByZoneSlice(items []GHItem, zone string) []GHItem {
	z := strings.TrimSpace(zone)
	if z == "" || strings.EqualFold(z, "all") {
		return items
	}
	out := make([]GHItem, 0, len(items))
	for _, it := range items {
		if strings.EqualFold(it.Zone, z) {
			out = append(out, it)
		}
	}
	return out
}

func selectGHTargetsForZone(ctx *app.Context, zone string) []ghZoneTarget {
	all := resolveGHZoneTargets(ctx)
	zone = strings.TrimSpace(zone)
	if zone == "" || strings.EqualFold(zone, "all") {
		return all
	}
	var matchingEnv, matchingZones []ghZoneTarget
	for _, t := range all {
		if strings.EqualFold(t.zone, zone) {
			if t.explicitEnv {
				matchingEnv = append(matchingEnv, t)
			} else {
				matchingZones = append(matchingZones, t)
			}
		}
	}
	if len(matchingEnv)+len(matchingZones) > 0 {
		ordered := make([]ghZoneTarget, 0, len(matchingEnv)+len(matchingZones))
		ordered = append(ordered, matchingEnv...)
		ordered = append(ordered, matchingZones...)
		return ordered
	}
	return all
}

func buildGHEnvForTarget(base []string, t ghZoneTarget) []string {
	cleaned := make([]string, 0, len(base)+len(t.ghEnv)+2)
	for _, entry := range base {
		k, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if t.explicitEnv {
			if k == "GH_CONFIG_DIR" && t.ghConfigDir != "" {
				continue
			}
			if k == "WS_ZONE" && t.zone != "" {
				continue
			}
			cleaned = append(cleaned, entry)
			continue
		}
		if k == "WS_ZONE" || strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "GITHUB_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	if t.zone != "" && t.zone != "env" {
		cleaned = append(cleaned, "WS_ZONE="+t.zone)
	}
	if t.ghConfigDir != "" {
		cleaned = append(cleaned, "GH_CONFIG_DIR="+t.ghConfigDir)
	}
	if len(t.ghEnv) > 0 {
		keys := make([]string, 0, len(t.ghEnv))
		for k := range t.ghEnv {
			if k == "GH_CONFIG_DIR" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cleaned = append(cleaned, k+"="+t.ghEnv[k])
		}
	}
	return cleaned
}

func targetWorkDir(ctx *app.Context, t ghZoneTarget) string {
	if !t.explicitEnv && t.zoneRoot != "" {
		if fi, err := os.Stat(t.zoneRoot); err == nil && fi.IsDir() {
			return t.zoneRoot
		}
	}
	if ctx != nil && ctx.HomeDir != "" {
		if fi, err := os.Stat(ctx.HomeDir); err == nil && fi.IsDir() {
			return ctx.HomeDir
		}
	}
	return ""
}

func stripGHAuthTokenEnv(env []string) []string {
	return stripEnvKey(stripEnvKey(env, "GH_TOKEN"), "GITHUB_TOKEN")
}

func expandGHConfigDir(ctx *app.Context, raw string) string {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return ""
	}
	if strings.HasPrefix(dir, "~/") {
		home := ""
		if ctx != nil && ctx.HomeDir != "" {
			home = ctx.HomeDir
		} else if uh, err := os.UserHomeDir(); err == nil {
			home = uh
		}
		if home != "" {
			dir = filepath.Join(home, dir[2:])
		}
	}
	return filepath.Clean(dir)
}

func resolveGHZoneTargets(ctx *app.Context) []ghZoneTarget {
	var zoneTargets []ghZoneTarget
	seenZones := map[string]bool{}
	dirToZone := map[string]string{}

	collectZoneRoot := func(zname, zroot string) {
		zname = strings.TrimSpace(zname)
		if zname == "" || zroot == "" {
			return
		}
		zkey := strings.ToLower(zname)
		if seenZones[zkey] {
			return
		}
		envMap, err := shell.ReadZoneMiseEnvFast(zroot)
		if err != nil {
			envMap = map[string]string{}
		}
		if wz := strings.TrimSpace(envMap["WS_ZONE"]); wz != "" {
			zname = wz
			zkey = strings.ToLower(zname)
			if seenZones[zkey] {
				return
			}
		}
		ghEnv := map[string]string{}
		ghDir := ""
		for k, v := range envMap {
			if !strings.HasPrefix(k, "GH_") && !strings.HasPrefix(k, "GITHUB_") {
				continue
			}
			if k == "GH_CONFIG_DIR" {
				ghDir = expandGHConfigDir(ctx, v)
				ghEnv[k] = ghDir
			} else {
				ghEnv[k] = v
			}
		}
		if ghDir != "" && dirToZone[ghDir] == "" {
			dirToZone[ghDir] = zname
		}
		seenZones[zkey] = true
		zoneTargets = append(zoneTargets, ghZoneTarget{
			zone:        zname,
			zoneRoot:    zroot,
			ghConfigDir: ghDir,
			ghEnv:       ghEnv,
		})
	}

	if ctx != nil && ctx.HomeDir != "" {
		if specs, err := shell.ParseZones(ctx); err == nil {
			for _, s := range specs {
				collectZoneRoot(s.Name, filepath.Join(ctx.HomeDir, s.Name))
			}
		}
		if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				zroot := filepath.Join(ctx.HomeDir, e.Name())
				envMap, err := shell.ReadZoneMiseEnvFast(zroot)
				if err != nil {
					continue
				}
				zname := strings.TrimSpace(envMap["WS_ZONE"])
				if zname == "" && envMap["GH_CONFIG_DIR"] != "" {
					zname = e.Name()
				}
				if zname != "" {
					collectZoneRoot(zname, zroot)
				}
			}
		}
	}

	activeZone := os.Getenv("WS_ZONE")
	sort.SliceStable(zoneTargets, func(i, j int) bool {
		if zoneTargets[i].zone == activeZone && zoneTargets[j].zone != activeZone {
			return true
		}
		if zoneTargets[j].zone == activeZone && zoneTargets[i].zone != activeZone {
			return false
		}
		return zoneTargets[i].zone < zoneTargets[j].zone
	})

	var targets []ghZoneTarget
	seenDir := map[string]bool{}
	rawEnvGHDir := os.Getenv("GH_CONFIG_DIR")
	rawEnvZone := os.Getenv("WS_ZONE")
	if ctx != nil {
		rawEnvGHDir = ctx.LookupEnv("GH_CONFIG_DIR")
		rawEnvZone = ctx.LookupEnv("WS_ZONE")
	}
	if envDir := expandGHConfigDir(ctx, rawEnvGHDir); envDir != "" {
		envZone := strings.TrimSpace(rawEnvZone)
		if envZone == "" {
			envZone = dirToZone[envDir]
		}
		if envZone == "" {
			envZone = "env"
		}
		seenDir[envDir] = true
		targets = append(targets, ghZoneTarget{zone: envZone, ghConfigDir: envDir, explicitEnv: true})
	}

	for _, t := range zoneTargets {
		if t.ghConfigDir != "" && seenDir[t.ghConfigDir] {
			continue
		}
		if t.ghConfigDir != "" {
			seenDir[t.ghConfigDir] = true
		}
		targets = append(targets, t)
	}

	if len(targets) == 0 {
		targets = append(targets, ghZoneTarget{zone: "", ghConfigDir: ""})
	}
	return targets
}

func runGHZoneMutation(ctx *app.Context, zone, workDir string, runner CommandRunner, args []string) (string, error) {
	if ctx != nil && ctx.DryRun {
		return fmt.Sprintf("[dry-run] would run: gh %s", strings.Join(args, " ")), nil
	}
	if runner == nil {
		if _, err := exec.LookPath("gh"); err != nil {
			return "", fmt.Errorf("gh CLI not found on PATH")
		}
		runner = execCommandRunner{timeout: cloneCommandTimeout}
	}
	targets := selectGHTargetsForZone(ctx, strings.TrimSpace(zone))
	var lastErr error
	for _, t := range targets {
		env := buildGHEnvForTarget(os.Environ(), t)
		dir := strings.TrimSpace(workDir)
		if dir == "" {
			dir = targetWorkDir(ctx, t)
		}
		out, err := runner.Output(env, dir, "gh", args...)
		if err != nil {
			msg := describeGHErr(err)
			lastErr = fmt.Errorf("gh %s: %s", args[0], msg)
			if errors.Is(err, exec.ErrNotFound) {
				break
			}
			continue
		}
		return strings.TrimSpace(string(out)), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("gh command failed")
}

func GHApprovePR(ctx *app.Context, repoSlug, zone string, number int, body string, runner CommandRunner) (string, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) || number <= 0 {
		return "", fmt.Errorf("invalid pull request target %s #%d", repoSlug, number)
	}
	args := []string{"pr", "review", strconv.Itoa(number), "-R", repoSlug, "--approve"}
	if strings.TrimSpace(body) != "" {
		args = append(args, "--body", strings.TrimSpace(body))
	}
	out, err := runGHZoneMutation(ctx, zone, "", runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("Approved PR #%d (%s)", number, repoSlug), nil
	}
	return out, nil
}

func GHRequestChangesPR(ctx *app.Context, repoSlug, zone string, number int, body string, runner CommandRunner) (string, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) || number <= 0 {
		return "", fmt.Errorf("invalid pull request target %s #%d", repoSlug, number)
	}
	args := []string{"pr", "review", strconv.Itoa(number), "-R", repoSlug, "--request-changes"}
	if strings.TrimSpace(body) != "" {
		args = append(args, "--body", strings.TrimSpace(body))
	}
	out, err := runGHZoneMutation(ctx, zone, "", runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("Requested changes on PR #%d (%s)", number, repoSlug), nil
	}
	return out, nil
}

func GHCommentItem(ctx *app.Context, kind, repoSlug, zone string, number int, body string, runner CommandRunner) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "pr" && kind != "issue" {
		return "", fmt.Errorf("unsupported GitHub item kind %q", kind)
	}
	repoSlug = strings.TrimSpace(repoSlug)
	body = strings.TrimSpace(body)
	if !isGitHubShorthand(repoSlug) || number <= 0 || body == "" {
		return "", fmt.Errorf("repo, item number, and non-empty comment body are required")
	}
	args := []string{kind, "comment", strconv.Itoa(number), "-R", repoSlug, "--body", body}
	out, err := runGHZoneMutation(ctx, zone, "", runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("Commented on %s #%d (%s)", strings.ToUpper(kind), number, repoSlug), nil
	}
	return out, nil
}

func GHSetItemState(ctx *app.Context, kind, repoSlug, zone string, number int, closeItem bool, runner CommandRunner) (string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "pr" && kind != "issue" {
		return "", fmt.Errorf("unsupported GitHub item kind %q", kind)
	}
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) || number <= 0 {
		return "", fmt.Errorf("invalid GitHub target %s #%d", repoSlug, number)
	}
	verb := "reopen"
	doneWord := "Reopened"
	if closeItem {
		verb = "close"
		doneWord = "Closed"
	}
	args := []string{kind, verb, strconv.Itoa(number), "-R", repoSlug}
	out, err := runGHZoneMutation(ctx, zone, "", runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("%s %s #%d (%s)", doneWord, strings.ToUpper(kind), number, repoSlug), nil
	}
	return out, nil
}

func GHMergePR(ctx *app.Context, repoSlug, zone string, number int, method string, runner CommandRunner) (string, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) || number <= 0 {
		return "", fmt.Errorf("invalid pull request target %s #%d", repoSlug, number)
	}
	methodFlag := "--squash"
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "", "squash":
		methodFlag = "--squash"
	case "merge":
		methodFlag = "--merge"
	case "rebase":
		methodFlag = "--rebase"
	default:
		return "", fmt.Errorf("unsupported merge method %q (expected squash, merge, or rebase)", method)
	}
	args := []string{"pr", "merge", strconv.Itoa(number), "-R", repoSlug, methodFlag}
	out, err := runGHZoneMutation(ctx, zone, "", runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("Merged PR #%d (%s, %s)", number, repoSlug, strings.TrimPrefix(methodFlag, "--")), nil
	}
	return out, nil
}

func GHCheckoutPR(ctx *app.Context, repoDir, repoSlug, zone string, number int, runner CommandRunner) (string, error) {
	repoDir = strings.TrimSpace(repoDir)
	repoSlug = strings.TrimSpace(repoSlug)
	if repoDir == "" || number <= 0 {
		return "", fmt.Errorf("local repository path and PR number are required")
	}
	args := []string{"pr", "checkout", strconv.Itoa(number)}
	if isGitHubShorthand(repoSlug) {
		args = append(args, "-R", repoSlug)
	}
	out, err := runGHZoneMutation(ctx, zone, repoDir, runner, args)
	if err != nil {
		return "", err
	}
	if out == "" {
		return fmt.Sprintf("Checked out PR #%d in %s", number, repoDir), nil
	}
	return out, nil
}

func describeGHErr(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "gh CLI not found"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if s := strings.TrimSpace(string(exitErr.Stderr)); s != "" {
			return s
		}
	}
	return err.Error()
}

func isGHRepoNotFound(msg string) bool {
	lower := strings.ToLower(msg)
	for _, token := range []string{"could not resolve", "not found", "http 404", "repository not found"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func isGHFatalForZone(err error, msg string) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	lower := strings.ToLower(msg)
	for _, token := range []string{"not logged", "gh auth login", "authentication", "unauthorized", "401", "executable file not found"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func sortGHItems(items []GHItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		if items[i].Number != items[j].Number {
			return items[i].Number < items[j].Number
		}
		return items[i].Kind < items[j].Kind
	})
}

func scanGHCacheRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]GHItem, int64, error) {
	var out []GHItem
	var maxFetched int64
	for rows.Next() {
		var it GHItem
		var labelsJSON, assigneesJSON string
		var draftInt int
		if err := rows.Scan(
			&it.Kind, &it.Repo, &it.Number, &it.Title, &it.State,
			&it.Author, &it.URL, &it.Zone, &it.UpdatedAt, &it.FetchedAt,
			&it.Body, &labelsJSON, &assigneesJSON, &draftInt, &it.CommentsCount, &it.CreatedAt, &it.Source, &it.HeadRef, &it.Milestone,
		); err != nil {
			return nil, 0, err
		}
		it.IsDraft = draftInt != 0
		if labelsJSON != "" && labelsJSON != "[]" {
			_ = json.Unmarshal([]byte(labelsJSON), &it.Labels)
		}
		if assigneesJSON != "" && assigneesJSON != "[]" {
			_ = json.Unmarshal([]byte(assigneesJSON), &it.Assignees)
		}
		if it.FetchedAt > maxFetched {
			maxFetched = it.FetchedAt
		}
		out = append(out, it)
	}
	return out, maxFetched, rows.Err()
}

func loadCachedGHItems(db *store.DB, source string) ([]GHItem, int64, error) {
	if db == nil || db.SQL() == nil {
		return nil, 0, fmt.Errorf("database is not open")
	}
	query := `SELECT kind, repo, number, title, state, author, url, zone, updated_at, fetched_at,
		 body, labels_json, assignees_json, is_draft, comments_count, created_at, source, head_ref, milestone
		 FROM gh_cache`
	var args []any
	if source != "" {
		query += ` WHERE source = ? OR source = ''`
		args = append(args, source)
	}
	query += ` ORDER BY updated_at DESC, repo ASC, number ASC`

	rows, err := db.SQL().Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return scanGHCacheRows(rows)
}

func LoadCachedRepoGHItems(db *store.DB, repoSlug, state string) ([]GHItem, int64, error) {
	if db == nil || db.SQL() == nil {
		return nil, 0, nil
	}
	repoSlug = strings.TrimSpace(repoSlug)
	if !isGitHubShorthand(repoSlug) {
		return nil, 0, nil
	}
	state = normalizeGHState(state)
	items, err := loadCachedRepoGHItems(db, repoSlug, state)
	if err != nil {
		return nil, 0, err
	}
	var fetchedAt int64
	cacheKey := fmt.Sprintf("gh_repo_fetched:%s:%s", strings.ToLower(repoSlug), state)
	if rawTS, ok, merr := db.GetMeta(cacheKey); merr == nil && ok {
		if ts, perr := strconv.ParseInt(rawTS, 10, 64); perr == nil {
			fetchedAt = ts
		}
	}
	if fetchedAt == 0 {
		for _, it := range items {
			if it.FetchedAt > fetchedAt {
				fetchedAt = it.FetchedAt
			}
		}
	}
	return items, fetchedAt, nil
}

func loadCachedGHDetail(db *store.DB, kind, repoSlug string, number int) (GHItemDetail, int64, bool) {
	if db == nil || db.SQL() == nil {
		return GHItemDetail{}, 0, false
	}
	var payload string
	var fetchedAt int64
	err := db.SQL().QueryRow(
		`SELECT payload_json, fetched_at FROM gh_detail_cache WHERE kind = ? AND lower(repo) = lower(?) AND number = ?`,
		kind, repoSlug, number,
	).Scan(&payload, &fetchedAt)
	if err != nil || strings.TrimSpace(payload) == "" {
		return GHItemDetail{}, 0, false
	}
	var det GHItemDetail
	if jerr := json.Unmarshal([]byte(payload), &det); jerr != nil {
		return GHItemDetail{}, 0, false
	}
	return det, fetchedAt, true
}

func upsertCachedGHDetail(db *store.DB, kind, repoSlug string, number int, det GHItemDetail, fetchedAt int64) error {
	if db == nil || db.SQL() == nil {
		return fmt.Errorf("database is not open")
	}
	b, err := json.Marshal(det)
	if err != nil {
		return err
	}
	_, err = db.SQL().Exec(
		`INSERT INTO gh_detail_cache(kind, repo, number, payload_json, fetched_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(kind, repo, number) DO UPDATE SET
		   payload_json=excluded.payload_json,
		   fetched_at=excluded.fetched_at`,
		kind, strings.ToLower(strings.TrimSpace(repoSlug)), number, string(b), fetchedAt,
	)
	return err
}

func loadCachedRepoGHItems(db *store.DB, repoSlug, state string) ([]GHItem, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	query := `SELECT kind, repo, number, title, state, author, url, zone, updated_at, fetched_at,
		 body, labels_json, assignees_json, is_draft, comments_count, created_at, source, head_ref, milestone
		 FROM gh_cache WHERE lower(repo) = lower(?)`
	args := []any{repoSlug}
	if state == "closed" {
		query += ` AND lower(state) IN ('closed', 'merged')`
	} else if state != "" && state != "all" {
		query += ` AND lower(state) = lower(?)`
		args = append(args, state)
	}
	query += ` ORDER BY updated_at DESC, number ASC`

	rows, err := db.SQL().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _, err := scanGHCacheRows(rows)
	return out, err
}

func replaceCachedGHItemsScoped(db *store.DB, items []GHItem, succeededScopes map[string]bool, fetchedAt int64) error {
	if db == nil || db.SQL() == nil {
		return fmt.Errorf("database is not open")
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for scopeKey := range succeededScopes {
		z, k, ok := strings.Cut(scopeKey, "\x00")
		if !ok {
			continue
		}
		if _, err := tx.Exec(
			`DELETE FROM gh_cache WHERE (source = 'involved' OR source = '') AND lower(zone) = ? AND lower(kind) = ?`,
			z, k,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM gh_cache WHERE source = ''`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO gh_cache(kind, repo, number, title, state, author, url, zone, updated_at, fetched_at, body, labels_json, assignees_json, is_draft, comments_count, created_at, source, head_ref, milestone)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(kind, repo, number) DO UPDATE SET
		   title=excluded.title, state=excluded.state, author=excluded.author, url=excluded.url,
		   zone=excluded.zone, updated_at=excluded.updated_at, fetched_at=excluded.fetched_at,
		   body=excluded.body, labels_json=excluded.labels_json, assignees_json=excluded.assignees_json,
		   is_draft=excluded.is_draft, comments_count=excluded.comments_count, created_at=excluded.created_at,
		   source='involved',
		   head_ref=CASE WHEN excluded.head_ref != '' THEN excluded.head_ref ELSE gh_cache.head_ref END,
		   milestone=CASE WHEN excluded.milestone != '' THEN excluded.milestone ELSE gh_cache.milestone END`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, it := range items {
		if err := execGHInsert(stmt, it, "involved"); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.SetMeta("gh_cache_format", "2"); err != nil {
		return err
	}
	return db.SetMeta("gh_cache_fetched_at", strconv.FormatInt(fetchedAt, 10))
}

func upsertRepoCachedGHItems(db *store.DB, repoSlug, state string, items []GHItem, fetchedAt int64, metaKey string) error {
	if db == nil || db.SQL() == nil {
		return fmt.Errorf("database is not open")
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if state == "all" {
		if _, err := tx.Exec(`DELETE FROM gh_cache WHERE lower(repo) = lower(?) AND source = 'repo'`, repoSlug); err != nil {
			return err
		}
	} else if state == "closed" {
		if _, err := tx.Exec(`DELETE FROM gh_cache WHERE lower(repo) = lower(?) AND source = 'repo' AND lower(state) IN ('closed', 'merged')`, repoSlug); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM gh_cache WHERE lower(repo) = lower(?) AND source = 'repo' AND lower(state) = lower(?)`, repoSlug, state); err != nil {
			return err
		}
	}

	stmt, err := tx.Prepare(
		`INSERT INTO gh_cache(kind, repo, number, title, state, author, url, zone, updated_at, fetched_at, body, labels_json, assignees_json, is_draft, comments_count, created_at, source, head_ref, milestone)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(kind, repo, number) DO UPDATE SET
		   title=excluded.title, state=excluded.state, author=excluded.author, url=excluded.url,
		   zone=CASE WHEN excluded.zone != '' THEN excluded.zone ELSE gh_cache.zone END,
		   updated_at=excluded.updated_at, fetched_at=excluded.fetched_at,
		   body=excluded.body, labels_json=excluded.labels_json, assignees_json=excluded.assignees_json,
		   is_draft=excluded.is_draft, comments_count=excluded.comments_count, created_at=excluded.created_at,
		   head_ref=CASE WHEN excluded.head_ref != '' THEN excluded.head_ref ELSE gh_cache.head_ref END,
		   milestone=CASE WHEN excluded.milestone != '' THEN excluded.milestone ELSE gh_cache.milestone END`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, it := range items {
		if err := execGHInsert(stmt, it, "repo"); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.SetMeta(metaKey, strconv.FormatInt(fetchedAt, 10))
}

func execGHInsert(stmt *sql.Stmt, it GHItem, defaultSource string) error {
	labelsRaw := "[]"
	if len(it.Labels) > 0 {
		if b, err := json.Marshal(it.Labels); err == nil {
			labelsRaw = string(b)
		}
	}
	assigneesRaw := "[]"
	if len(it.Assignees) > 0 {
		if b, err := json.Marshal(it.Assignees); err == nil {
			assigneesRaw = string(b)
		}
	}
	draftInt := 0
	if it.IsDraft {
		draftInt = 1
	}
	src := strings.TrimSpace(it.Source)
	if src == "" {
		src = defaultSource
	}
	_, err := stmt.Exec(
		it.Kind, strings.ToLower(strings.TrimSpace(it.Repo)), it.Number, it.Title, it.State,
		it.Author, it.URL, it.Zone, it.UpdatedAt, it.FetchedAt,
		it.Body, labelsRaw, assigneesRaw, draftInt, it.CommentsCount, it.CreatedAt, src, it.HeadRef, it.Milestone,
	)
	return err
}
