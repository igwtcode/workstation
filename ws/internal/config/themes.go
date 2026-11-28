package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

var RequiredThemeTokens = []string{
	"name", "mode",
	"bg", "bg_alt", "fg", "fg_dim", "accent", "selection_bg", "selection_fg", "cursor", "border",
	"color0", "color1", "color2", "color3", "color4", "color5", "color6", "color7",
	"color8", "color9", "color10", "color11", "color12", "color13", "color14", "color15",
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

type paletteFile struct {
	Name        string `toml:"name"`
	Mode        string `toml:"mode"`
	BG          string `toml:"bg"`
	BGAlt       string `toml:"bg_alt"`
	FG          string `toml:"fg"`
	FGDim       string `toml:"fg_dim"`
	Accent      string `toml:"accent"`
	SelectionBG string `toml:"selection_bg"`
	SelectionFG string `toml:"selection_fg"`
	Cursor      string `toml:"cursor"`
	Border      string `toml:"border"`
	Color0      string `toml:"color0"`
	Color1      string `toml:"color1"`
	Color2      string `toml:"color2"`
	Color3      string `toml:"color3"`
	Color4      string `toml:"color4"`
	Color5      string `toml:"color5"`
	Color6      string `toml:"color6"`
	Color7      string `toml:"color7"`
	Color8      string `toml:"color8"`
	Color9      string `toml:"color9"`
	Color10     string `toml:"color10"`
	Color11     string `toml:"color11"`
	Color12     string `toml:"color12"`
	Color13     string `toml:"color13"`
	Color14     string `toml:"color14"`
	Color15     string `toml:"color15"`
}

func (pf paletteFile) toMap() map[string]string {
	return map[string]string{
		"name":         pf.Name,
		"mode":         pf.Mode,
		"bg":           pf.BG,
		"bg_alt":       pf.BGAlt,
		"fg":           pf.FG,
		"fg_dim":       pf.FGDim,
		"accent":       pf.Accent,
		"selection_bg": pf.SelectionBG,
		"selection_fg": pf.SelectionFG,
		"cursor":       pf.Cursor,
		"border":       pf.Border,
		"color0":       pf.Color0,
		"color1":       pf.Color1,
		"color2":       pf.Color2,
		"color3":       pf.Color3,
		"color4":       pf.Color4,
		"color5":       pf.Color5,
		"color6":       pf.Color6,
		"color7":       pf.Color7,
		"color8":       pf.Color8,
		"color9":       pf.Color9,
		"color10":      pf.Color10,
		"color11":      pf.Color11,
		"color12":      pf.Color12,
		"color13":      pf.Color13,
		"color14":      pf.Color14,
		"color15":      pf.Color15,
	}
}

type Palette struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Mode     string            `json:"mode"`
	FilePath string            `json:"filePath"`
	Tokens   map[string]string `json:"tokens"`
}

func (p Palette) Color(key, fallback string) string {
	if v, ok := p.Tokens[key]; ok && v != "" {
		return v
	}
	return fallback
}

type RegistryEntry struct {
	Template string `json:"template"`
	Output   string `json:"output"`
	PostHook string `json:"postHook,omitempty"`
	App      string `json:"app"`
}

func ListThemes(repoRoot string) ([]string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("workstation repo root not found (set WS_DIR, pass --repo <path>, or configure workspace.repo_root)")
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, "themes"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, mode := range []string{"dark", "light"} {
			candidate := filepath.Join(repoRoot, "themes", entry.Name(), mode+".toml")
			if _, err := os.Stat(candidate); err == nil {
				ids = append(ids, entry.Name()+"-"+mode)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func LoadPalette(repoRoot, id string) (Palette, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return Palette{}, fmt.Errorf("workstation repo root not found (set WS_DIR, pass --repo <path>, or configure workspace.repo_root)")
	}
	idx := strings.LastIndex(id, "-")
	if idx <= 0 || idx == len(id)-1 {
		return Palette{}, fmt.Errorf("invalid theme id %q (expected <name>-<mode>)", id)
	}
	name := id[:idx]
	mode := id[idx+1:]
	if mode != "dark" && mode != "light" {
		return Palette{}, fmt.Errorf("invalid theme id %q: mode must be dark or light", id)
	}
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || strings.Contains(name, "..") {
		return Palette{}, fmt.Errorf("invalid theme id %q: invalid theme name", id)
	}
	themesDir := filepath.Clean(filepath.Join(repoRoot, "themes"))
	path := filepath.Join(themesDir, name, mode+".toml")
	rel, rerr := filepath.Rel(themesDir, path)
	if rerr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return Palette{}, fmt.Errorf("invalid theme id %q: path escapes themes/", id)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Palette{}, fmt.Errorf("unknown theme %q: %w", id, err)
	}

	var parsed paletteFile
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return Palette{}, fmt.Errorf("parse palette %s: %w", path, err)
	}

	rawMap := parsed.toMap()
	tokens := make(map[string]string, len(rawMap)*2)
	for k, str := range rawMap {
		if str == "" {
			continue
		}
		tokens[k] = str
		if hexColorRe.MatchString(str) {
			tokens[k+"_strip"] = strings.TrimPrefix(str, "#")
		}
	}

	for _, req := range RequiredThemeTokens {
		if tokens[req] == "" {
			return Palette{}, fmt.Errorf("palette %s missing required token %q", path, req)
		}
	}

	return Palette{
		ID:       id,
		Name:     tokens["name"],
		Mode:     tokens["mode"],
		FilePath: path,
		Tokens:   tokens,
	}, nil
}

func ValidateTemplateTokens(content string, pal Palette) error {
	rest := content
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			return nil
		}
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return fmt.Errorf("unterminated {{ token")
		}
		token := rest[:end]
		if token == "" {
			return fmt.Errorf("empty {{}} token")
		}
		val := pal.Tokens[token]
		if val == "" && strings.HasSuffix(token, "_strip") {
			base := strings.TrimSuffix(token, "_strip")
			if rawVal := pal.Tokens[base]; rawVal != "" {
				val = strings.TrimPrefix(rawVal, "#")
			}
		}
		if val == "" {
			return fmt.Errorf("unknown token {{%s}}", token)
		}
		rest = rest[end+2:]
	}
}

func LoadThemeRegistry(repoRoot string) ([]RegistryEntry, error) {
	path := filepath.Join(repoRoot, "themes", "registry")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []RegistryEntry
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("themes/registry:%d: malformed line %q", lineNo, line)
		}
		tpl := fields[0]
		out := fields[1]
		hook := ""
		if len(fields) >= 3 {
			firstEnd := strings.Index(line, tpl) + len(tpl)
			secondIdx := strings.Index(line[firstEnd:], out)
			if secondIdx >= 0 {
				hook = strings.TrimSpace(line[firstEnd+secondIdx+len(out):])
			} else {
				hook = strings.Join(fields[2:], " ")
			}
			if hook == "-" {
				hook = ""
			}
		}
		entries = append(entries, RegistryEntry{
			Template: tpl,
			Output:   out,
			PostHook: hook,
			App:      DeriveAppName(tpl),
		})
	}
	return entries, sc.Err()
}

func DeriveAppName(templateRel string) string {
	slash := filepath.ToSlash(templateRel)
	parts := strings.Split(strings.TrimPrefix(slash, "config/"), "/")
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
	}
	return strings.TrimSuffix(filepath.Base(slash), filepath.Ext(slash))
}
