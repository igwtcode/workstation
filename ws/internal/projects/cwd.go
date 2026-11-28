package projects

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type CWDContext struct {
	WorkDir   string
	RepoRoot  string
	RepoName  string
	RepoSlug  string
	Branch    string
	IssueHint int
}

func DetectCWDContext(workDir string) CWDContext {
	dir := strings.TrimSpace(workDir)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return CWDContext{}
		}
		dir = wd
	}
	dir = filepath.Clean(dir)
	out := CWDContext{WorkDir: dir}

	root, ok := runQuickGit(dir, "rev-parse", "--show-toplevel")
	if !ok || root == "" {
		return out
	}
	out.RepoRoot = filepath.Clean(root)
	out.RepoName = filepath.Base(out.RepoRoot)

	if remote, rok := runQuickGit(out.RepoRoot, "remote", "get-url", "origin"); rok && remote != "" {
		out.RepoSlug = ExtractGitHubRepoSlug(remote)
	}
	if out.RepoSlug == "" {
		if remotes, rok := runQuickGit(out.RepoRoot, "remote", "-v"); rok && remotes != "" {
			for _, ln := range strings.Split(remotes, "\n") {
				fields := strings.Fields(ln)
				if len(fields) >= 2 {
					if slug := ExtractGitHubRepoSlug(fields[1]); slug != "" {
						out.RepoSlug = slug
						break
					}
				}
			}
		}
	}
	branch, bok := runQuickGit(out.RepoRoot, "symbolic-ref", "--short", "HEAD")
	if !bok || branch == "" {
		if abbrev, aok := runQuickGit(out.RepoRoot, "rev-parse", "--abbrev-ref", "HEAD"); aok && abbrev != "HEAD" {
			branch = abbrev
		}
	}
	out.Branch = strings.TrimSpace(branch)
	out.IssueHint = ParseIssueNumberFromBranch(out.Branch)
	return out
}

func runQuickGit(dir string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = 300 * time.Millisecond
	raw, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

var (
	dateBranchRe       = regexp.MustCompile(`20\d{2}[-_/]\d{2}[-_/]\d{2}`)
	versionSegmentRe   = regexp.MustCompile(`(?i)^v?\d+(?:\.\d+)+$`)
	explicitIssueRefRe = regexp.MustCompile(`(?i)(?:^|[/_-])(?:issue|iss|gh|pr|#|ticket)[/#_-]*0*([1-9][0-9]{0,5})(?:[^0-9]|$)`)
	leadingNumSegRe    = regexp.MustCompile(`(?:^|/)0*([1-9][0-9]{0,5})(?:[-_].*)?$`)
	trailingNumSegRe   = regexp.MustCompile(`(?i)[/_-]0*([1-9][0-9]{0,5})$`)
)

func ParseIssueNumberFromBranch(branch string) int {
	b := strings.TrimSpace(branch)
	if b == "" {
		return 0
	}
	switch strings.ToLower(b) {
	case "main", "master", "develop", "dev", "trunk", "head":
		return 0
	}
	cleaned := dateBranchRe.ReplaceAllString(b, " ")
	var kept []string
	for _, seg := range strings.Split(cleaned, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" || versionSegmentRe.MatchString(seg) {
			continue
		}
		kept = append(kept, seg)
	}
	if len(kept) == 0 {
		return 0
	}
	norm := strings.Join(kept, "/")

	for _, re := range []*regexp.Regexp{explicitIssueRefRe, leadingNumSegRe, trailingNumSegRe} {
		if m := re.FindStringSubmatch(norm); len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
