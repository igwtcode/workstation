package shell

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
)

type VaultItem struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	LocalPath   string `json:"localPath"`
	TargetArg   string `json:"targetArg,omitempty"`
	DocTitle    string `json:"docTitle"`
	Category    string `json:"category"`
	ExistsLocal bool   `json:"existsLocal"`
	IsDir       bool   `json:"isDir"`
	FileCount   int    `json:"fileCount"`
	SizeBytes   int64  `json:"sizeBytes"`
	ModTime     int64  `json:"modTime"`
	Note        string `json:"note,omitempty"`
}

type VaultInventory struct {
	HasOPCLI bool        `json:"hasOpCli"`
	OPVault  string      `json:"opVault,omitempty"`
	Items    []VaultItem `json:"items"`
}

func DiscoverVaultItems(ctx *app.Context) VaultInventory {
	inv := VaultInventory{}
	if _, err := exec.LookPath("op"); err == nil {
		inv.HasOPCLI = true
	}
	if ctx == nil {
		return inv
	}
	inv.OPVault = strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))

	addItem := func(it VaultItem) {
		it.ExistsLocal, it.IsDir, it.FileCount, it.SizeBytes, it.ModTime = inspectVaultLocalPath(it.LocalPath, it.Scope == "envfiles")
		inv.Items = append(inv.Items, it)
	}

	dbPath := ctx.WSDBPath()
	addItem(VaultItem{
		ID:        "ws-db",
		Scope:     "db",
		Label:     "ws.db (Encrypted Snapshot Vault)",
		LocalPath: dbPath,
		DocTitle:  "ws-db-backup.enc",
		Category:  "vault",
		Note:      "Recommended: AES-256-GCM snapshot; excludes ~/.ws/cache, logs, exports, and ledger.lock",
	})

	keyPath := ctx.WSKeyPath()
	addItem(VaultItem{
		ID:        "ws-db-key",
		Scope:     "db-key",
		Label:     "ws.db Master Key (db.key)",
		LocalPath: keyPath,
		DocTitle:  "ws-db.key",
		Category:  "key",
		Note:      "Backed up separately from ws-db-backup.enc (or via OS Keychain / WS_DB_KEY)",
	})

	if ctx.HomeDir != "" {
		sshDoc := strings.TrimSpace(ctx.LookupEnv("WS_OP_SSH_DOC"))
		if sshDoc == "" {
			sshDoc = "~/.ssh.zip"
		}
		addItem(VaultItem{
			ID:        "ssh",
			Scope:     "ssh",
			Label:     "~/.ssh (SSH Keys & Config)",
			LocalPath: filepath.Join(ctx.HomeDir, ".ssh"),
			DocTitle:  sshDoc,
			Category:  "ssh",
			Note:      "Restores keys with mode 0600 and ~/.ssh with mode 0700 (excludes sockets)",
		})
	}

	if ctx.RepoRoot != "" {
		secDoc := strings.TrimSpace(ctx.LookupEnv("WS_OP_SECRETS_DOC"))
		if secDoc == "" {
			secDoc = "workstation-secrets.zip"
		}
		secPath := filepath.Join(ctx.RepoRoot, "mise.local.toml")
		if !pathExists(secPath) {
			alt := filepath.Join(ctx.RepoRoot, ".mise", "tasks", "local")
			if pathExists(alt) {
				secPath = alt
			}
		}
		addItem(VaultItem{
			ID:        "secrets",
			Scope:     "secrets",
			Label:     "Private Layer (mise.local.toml + local tasks)",
			LocalPath: secPath,
			DocTitle:  secDoc,
			Category:  "secrets",
			Note:      "Zones, 1Password file lists, and machine-local tasks (gitignored)",
		})
	}

	for _, rel := range resolveVaultFileTargets(ctx, nil) {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		abs := ctx.ExpandHome(rel)
		if !filepath.IsAbs(abs) && ctx.HomeDir != "" {
			cleanRel := strings.TrimPrefix(filepath.ToSlash(rel), "~/")
			abs = filepath.Join(ctx.HomeDir, filepath.FromSlash(cleanRel))
		}
		docTitle := rel
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			docTitle = rel + ".zip"
		}
		addItem(VaultItem{
			ID:        "file:" + rel,
			Scope:     "files",
			Label:     rel,
			LocalPath: abs,
			TargetArg: rel,
			DocTitle:  docTitle,
			Category:  "files",
			Note:      "Configured in WS_OP_FILES (~/-relative portable backup)",
		})
	}

	order, roots, _ := discoverZoneRoots(ctx)
	for _, zname := range order {
		zroot := roots[zname]
		codeRoot := filepath.Join(zroot, "code")
		if envMap, err := ReadZoneMiseEnv(zroot); err == nil && envMap["WS_CODE_ROOT"] != "" {
			codeRoot = envMap["WS_CODE_ROOT"]
		}
		addItem(VaultItem{
			ID:        "envfiles:" + zname,
			Scope:     "envfiles",
			Label:     fmt.Sprintf(".env* Tree (%s zone)", zname),
			LocalPath: codeRoot,
			TargetArg: codeRoot,
			DocTitle:  fmt.Sprintf("workstation-envfiles-%s.zip", zname),
			Category:  "envfiles",
			Note:      fmt.Sprintf("Collects .env and .env.* files under %s (skips .git and node_modules)", ctx.CollapseHome(codeRoot)),
		})
	}

	return inv
}

func inspectVaultLocalPath(absPath string, envTreeOnly bool) (exists, isDir bool, fileCount int, sizeBytes int64, modTime int64) {
	if strings.TrimSpace(absPath) == "" {
		return false, false, 0, 0, 0
	}
	fi, err := os.Lstat(absPath)
	if err != nil {
		return false, false, 0, 0, 0
	}
	modTime = fi.ModTime().Unix()
	if !fi.IsDir() {
		if fi.Mode()&os.ModeSymlink != 0 {
			return false, false, 0, 0, modTime
		}
		return true, false, 1, fi.Size(), modTime
	}

	isDir = true
	rootClean := filepath.Clean(absPath)
	_ = filepath.WalkDir(rootClean, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(rootClean, p)
		if rerr == nil && rel != "." {
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if d.IsDir() && depth > 5 {
				return filepath.SkipDir
			}
		}
		if d.Type()&os.ModeSymlink != 0 || d.Type()&os.ModeSocket != 0 {
			return nil
		}
		if d.IsDir() {
			if (d.Name() == "agent" && filepath.Base(filepath.Dir(p)) == ".ssh") ||
				(envTreeOnly && (d.Name() == "node_modules" || d.Name() == ".git")) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		if envTreeOnly && d.Name() != ".env" && !strings.HasPrefix(d.Name(), ".env.") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		fileCount++
		sizeBytes += info.Size()
		if mt := info.ModTime().Unix(); mt > modTime {
			modTime = mt
		}
		return nil
	})
	return true, true, fileCount, sizeBytes, modTime
}
