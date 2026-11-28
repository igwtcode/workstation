package engine

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
)

type ActionKind string

const (
	ActionRender  ActionKind = "render"
	ActionSymlink ActionKind = "symlink"
	ActionBackup  ActionKind = "backup"
	ActionHook    ActionKind = "hook"
	ActionInstall ActionKind = "install"
	ActionUpgrade ActionKind = "upgrade"
	ActionRemove  ActionKind = "remove"
	ActionClean   ActionKind = "clean"
	ActionOSExec  ActionKind = "os_exec"
	ActionEnv     ActionKind = "env_write"
)

type RiskLevel string

const (
	RiskSafe      RiskLevel = "safe"
	RiskMedium    RiskLevel = "medium"
	RiskCostly    RiskLevel = "costly"
	RiskDangerous RiskLevel = "dangerous"
)

type Action struct {
	ID            string            `json:"id"`
	Kind          ActionKind        `json:"kind"`
	Target        string            `json:"target"`
	Source        string            `json:"source,omitempty"`
	Summary       string            `json:"summary"`
	Detail        string            `json:"detail,omitempty"`
	Diff          string            `json:"diff,omitempty"`
	Risk          RiskLevel         `json:"risk"`
	RequiresAllow string            `json:"requiresAllow,omitempty"`
	Command       []string          `json:"command,omitempty"`
	NeedsTTY      bool              `json:"needsTty,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type Plan struct {
	Verb      string            `json:"verb,omitempty"`
	Host      string            `json:"host,omitempty"`
	Title     string            `json:"title"`
	Preset    string            `json:"preset,omitempty"`
	Theme     string            `json:"theme,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	Actions   []Action          `json:"actions"`
	Skipped   []string          `json:"skipped,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func FormatPlanHost(ctx *app.Context) string {
	if ctx == nil {
		return ""
	}
	var parts []string
	if ctx.Host.OS != "" {
		parts = append(parts, ctx.Host.OS)
	}
	if ctx.Host.Distro != "" {
		parts = append(parts, ctx.Host.Distro)
	}
	if ctx.Host.Arch != "" {
		parts = append(parts, ctx.Host.Arch)
	}
	return strings.Join(parts, "/")
}

type ApplyResult struct {
	Applied        int      `json:"applied"`
	Skipped        int      `json:"skipped"`
	ConsentSkipped int      `json:"consentSkipped,omitempty"`
	MissingAllows  []string `json:"missingAllows,omitempty"`
	Declined       int      `json:"declined,omitempty"`
	Changed        []string `json:"changed,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	UndoID         string   `json:"undoId,omitempty"`
}

func (p *Plan) Save(path string) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return app.WriteAtomicFile(path, append(raw, '\n'), 0o600)
}

func LoadPlan(path string) (*Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
