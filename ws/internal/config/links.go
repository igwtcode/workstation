package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type LinkEntry struct {
	Manifest   string `json:"manifest"`
	SourceRel  string `json:"sourceRel"`
	TargetRaw  string `json:"targetRaw"`
	LineNumber int    `json:"lineNumber"`
}

func ParseLinkManifest(repoRoot, manifestRel string) ([]LinkEntry, error) {
	path := filepath.Join(repoRoot, manifestRel)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []LinkEntry
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sep := strings.IndexAny(line, " \t")
		if sep < 0 {
			return nil, fmt.Errorf("%s:%d: malformed link entry %q", manifestRel, lineNo, line)
		}
		srcRel := strings.TrimSpace(line[:sep])
		targetRaw := strings.TrimSpace(line[sep+1:])
		if srcRel == "" || targetRaw == "" {
			return nil, fmt.Errorf("%s:%d: empty source or target", manifestRel, lineNo)
		}
		cleanSrc := filepath.ToSlash(filepath.Clean(filepath.FromSlash(srcRel)))
		if strings.HasPrefix(srcRel, "/") || filepath.IsAbs(srcRel) || cleanSrc == "." || cleanSrc == ".." || strings.HasPrefix(cleanSrc, "../") {
			return nil, fmt.Errorf("%s:%d: source must be relative inside repo: %q", manifestRel, lineNo, srcRel)
		}
		if !strings.HasPrefix(targetRaw, "~/") && !strings.HasPrefix(targetRaw, "/") {
			return nil, fmt.Errorf("%s:%d: target must start with ~/ or /: %q", manifestRel, lineNo, targetRaw)
		}
		entries = append(entries, LinkEntry{
			Manifest:   manifestRel,
			SourceRel:  cleanSrc,
			TargetRaw:  targetRaw,
			LineNumber: lineNo,
		})
	}
	return entries, sc.Err()
}

func LoadLinksForPreset(repoRoot string, preset *ResolvedPreset) ([]LinkEntry, error) {
	var all []LinkEntry
	seen := map[string]int{}
	for _, rel := range preset.Links {
		entries, err := ParseLinkManifest(repoRoot, rel)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			key := filepath.ToSlash(filepath.Clean(e.TargetRaw))
			if prevIdx, dup := seen[key]; dup {
				if all[prevIdx].SourceRel != e.SourceRel {
					return nil, fmt.Errorf("duplicate link target %q across manifests (%s -> %s vs %s -> %s)",
						e.TargetRaw, all[prevIdx].Manifest, all[prevIdx].SourceRel, e.Manifest, e.SourceRel)
				}
				continue
			}
			seen[key] = len(all)
			all = append(all, e)
		}
	}
	return all, nil
}
