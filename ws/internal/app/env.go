package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type miseLocalFile struct {
	Env map[string]any `toml:"env"`
}

func LoadMiseLocalEnv(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed miseLocalFile
	if err := toml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]string, len(parsed.Env))
	for k, v := range parsed.Env {
		switch tv := v.(type) {
		case string:
			out[k] = tv
		case int64, float64, bool:
			out[k] = fmt.Sprintf("%v", tv)
		case map[string]any:
			if rawVal, ok := tv["value"]; ok {
				switch sv := rawVal.(type) {
				case string:
					out[k] = sv
				case int64, float64, bool:
					out[k] = fmt.Sprintf("%v", sv)
				}
			}
		}
	}
	return out, nil
}

func ParsePipeLines(raw string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.Split(trimmed, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		rows = append(rows, parts)
	}
	return rows
}
