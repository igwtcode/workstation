package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type ManagedToolRecord struct {
	Provider    string    `json:"provider"`
	PackageName string    `json:"packageName,omitempty"`
	Version     string    `json:"version,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	Updates     string    `json:"updates,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

type UndoOp struct {
	Kind              string `json:"kind"`
	Target            string `json:"target"`
	PrevExists        bool   `json:"prevExists"`
	PrevIsSymlink     bool   `json:"prevIsSymlink,omitempty"`
	PrevLinkTarget    string `json:"prevLinkTarget,omitempty"`
	PostLinkTarget    string `json:"postLinkTarget,omitempty"`
	PrevWasManaged    bool   `json:"prevWasManaged,omitempty"`
	PrevContentB64    string `json:"prevContentB64,omitempty"`
	PrevContentSHA256 string `json:"prevContentSha256,omitempty"`
	PostContentSHA256 string `json:"postContentSha256,omitempty"`
	PrevMode          uint32 `json:"prevMode,omitempty"`
	BackupPath        string `json:"backupPath,omitempty"`
}

type UndoTx struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Summary    string    `json:"summary"`
	PrevTheme  string    `json:"prevTheme,omitempty"`
	PrevPreset string    `json:"prevPreset,omitempty"`
	Ops        []UndoOp  `json:"ops"`
}

type JournalEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Command      string    `json:"command"`
	Preset       string    `json:"preset,omitempty"`
	Summary      string    `json:"summary"`
	ActionsCount int       `json:"actionsCount"`
	SnapshotID   string    `json:"snapshotId,omitempty"`
	Warnings     []string  `json:"warnings,omitempty"`
}

type Ledger struct {
	ActiveTheme    string                       `json:"activeTheme,omitempty"`
	ActivePreset   string                       `json:"activePreset,omitempty"`
	RenderedHashes map[string]string            `json:"renderedHashes,omitempty"`
	ManagedLinks   map[string]string            `json:"managedLinks,omitempty"`
	ManagedTools   map[string]ManagedToolRecord `json:"managedTools,omitempty"`
	UndoLog        []UndoTx                     `json:"undoLog,omitempty"`
	RunJournal     []JournalEntry               `json:"runJournal,omitempty"`
}

const (
	maxUndoHistory    = 25
	maxJournalEntries = 100
)

func LedgerPath(wsHome string) string {
	return filepath.Join(wsHome, "ledger.json")
}

func LegacyThemePath(wsHome string) string {
	return filepath.Join(wsHome, "theme")
}

func WithLedgerLock(wsHome string, fn func() error) error {
	if wsHome == "" {
		return fn()
	}
	if err := os.MkdirAll(wsHome, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(wsHome, "ledger.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func LoadLedger(stateDir string) (*Ledger, error) {
	l := &Ledger{
		RenderedHashes: map[string]string{},
		ManagedLinks:   map[string]string{},
		ManagedTools:   map[string]ManagedToolRecord{},
	}
	ledgerPath := LedgerPath(stateDir)
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return l, err
		}
		if theme := ReadLegacyTheme(stateDir); isValidLegacyThemeID(theme) {
			l.ActiveTheme = theme
		}
		return l, nil
	}
	if err := json.Unmarshal(raw, l); err != nil {
		return l, err
	}
	if l.RenderedHashes == nil {
		l.RenderedHashes = map[string]string{}
	}
	if l.ManagedLinks == nil {
		l.ManagedLinks = map[string]string{}
	}
	if l.ManagedTools == nil {
		l.ManagedTools = map[string]ManagedToolRecord{}
	}
	if legacyTheme := ReadLegacyTheme(stateDir); isValidLegacyThemeID(legacyTheme) {
		if l.ActiveTheme == "" {
			l.ActiveTheme = legacyTheme
		} else if legacyTheme != l.ActiveTheme {
			if lfi, err1 := os.Stat(ledgerPath); err1 == nil {
				if tfi, err2 := os.Stat(LegacyThemePath(stateDir)); err2 == nil && tfi.ModTime().After(lfi.ModTime()) {
					l.ActiveTheme = legacyTheme
				}
			}
		}
	}
	return l, nil
}

func isValidLegacyThemeID(id string) bool {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return false
	}
	return strings.HasSuffix(id, "-dark") || strings.HasSuffix(id, "-light")
}

func SaveLedger(stateDir string, l *Ledger) error {
	if len(l.UndoLog) > maxUndoHistory {
		l.UndoLog = l.UndoLog[len(l.UndoLog)-maxUndoHistory:]
	}
	if len(l.RunJournal) > maxJournalEntries {
		l.RunJournal = l.RunJournal[len(l.RunJournal)-maxJournalEntries:]
	}

	path := LedgerPath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if l.ActiveTheme != "" {
		if err := WriteLegacyTheme(stateDir, l.ActiveTheme); err != nil {
			return err
		}
	} else {
		_ = os.Remove(LegacyThemePath(stateDir))
	}

	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	return WriteAtomicFile(path, data, 0o600)
}

func ReadLegacyTheme(stateDir string) string {
	raw, err := os.ReadFile(LegacyThemePath(stateDir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func WriteLegacyTheme(stateDir, id string) error {
	return WriteAtomicFile(LegacyThemePath(stateDir), []byte(id+"\n"), 0o644)
}
