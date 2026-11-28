package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
)

type Plugin struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Source string `json:"source"`
}

func DiscoverPlugins(ctx *app.Context) ([]Plugin, error) {
	seen := map[string]Plugin{}

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "ws-") || len(name) <= 3 {
				continue
			}
			full := filepath.Join(dir, name)
			if !isExecutableFile(full) {
				continue
			}
			pluginName := strings.TrimPrefix(name, "ws-")
			if _, exists := seen[pluginName]; !exists {
				seen[pluginName] = Plugin{
					Name:   pluginName,
					Path:   full,
					Source: "path",
				}
			}
		}
	}

	if ctx != nil && ctx.RepoRoot != "" {
		localDir := filepath.Join(ctx.RepoRoot, ".mise", "tasks", "local")
		if entries, err := os.ReadDir(localDir); err == nil {
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") {
					continue
				}
				full := filepath.Join(localDir, name)
				if !isExecutableFile(full) {
					continue
				}
				if _, exists := seen[name]; !exists {
					seen[name] = Plugin{
						Name:   name,
						Path:   full,
						Source: "local-task",
					}
				}
			}
		}
	}

	plugins := make([]Plugin, 0, len(seen))
	for _, p := range seen {
		plugins = append(plugins, p)
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].Name < plugins[j].Name
	})
	return plugins, nil
}

func FindPlugin(ctx *app.Context, name string) (*Plugin, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("invalid plugin name %q", name)
	}
	plugins, err := DiscoverPlugins(ctx)
	if err != nil {
		return nil, err
	}
	for i := range plugins {
		if plugins[i].Name == name {
			p := plugins[i]
			return &p, nil
		}
	}
	return nil, fmt.Errorf("plugin %q not found", name)
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
