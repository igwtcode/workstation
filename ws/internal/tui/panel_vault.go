package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

type vaultPanel struct {
	dbStatus      DBStatusSummary
	vaultInv      VaultInventoryView
	filteredVault []VaultItemRow
	filterQuery   string
	dbLoaded      bool
	vaultLoaded   bool
	dbErr         string
	vaultErr      string
	vaultCursor   int
	vaultSel      SelectionSet
	drillOpen     bool
	previewScroll int
	backingUp     bool
	undoing       bool
	vaultBusy     bool
	dbActID       int
	vaultActID    int
	vaultOpActID  int
	bkActID       int
	undoActID     int
}

func newVaultPanel() vaultPanel {
	return vaultPanel{}
}

func (p vaultPanel) FilterQuery() string    { return p.filterQuery }
func (p vaultPanel) FilterTarget() string   { return "Vault" }
func (p vaultPanel) SelectedCount() int     { return p.vaultSel.Count() }
func (p vaultPanel) SupportsPreview() bool  { return !p.drillOpen }
func (p vaultPanel) CanPop() bool           { return p.drillOpen }
func (p vaultPanel) CapturingInput() bool   { return false }
func (p vaultPanel) HasOpenModal() bool     { return false }
func (p vaultPanel) MutationInFlight() bool { return p.backingUp || p.undoing || p.vaultBusy }

func (p vaultPanel) DefaultSplit(width int) SplitMode {
	if width >= 112 {
		return SplitVertical
	}
	return SplitOff
}

func (p vaultPanel) ClearSelection() vaultPanel {
	p.vaultSel = p.vaultSel.Clear()
	return p
}

func (p vaultPanel) Pop() vaultPanel {
	p.drillOpen = false
	p.previewScroll = 0
	return p
}

func (p vaultPanel) SetFilterQuery(q string) vaultPanel {
	p.filterQuery = q
	p.recomputeVaultFiltered()
	p.vaultCursor = 0
	p.previewScroll = 0
	return p
}

func (p *vaultPanel) recomputeVaultFiltered() {
	p.filteredVault = fuzzy.Filter(p.vaultInv.Items, p.filterQuery, func(it VaultItemRow) ([]string, map[string]string) {
		local := "missing"
		if it.ExistsLocal {
			local = "present"
		}
		return []string{it.Label, it.Scope, it.DocTitle, it.LocalPath, it.Category, it.Note},
			map[string]string{
				"scope": it.Scope,
				"local": local,
			}
	})
	if p.vaultCursor >= len(p.filteredVault) {
		p.vaultCursor = max(0, len(p.filteredVault)-1)
	}
}

func (p vaultPanel) Attention() AttentionLevel {
	if p.dbErr != "" || p.vaultErr != "" {
		return AttentionErr
	}
	return AttentionNone
}

func (p vaultPanel) PaneStatus() string {
	switch {
	case p.backingUp:
		return "backing up ws.db..."
	case p.undoing:
		return "undoing..."
	case p.vaultBusy:
		return "running vault op..."
	default:
		return ""
	}
}

func (p vaultPanel) Badge() string {
	if !p.vaultLoaded {
		return "…"
	}
	base := fmt.Sprintf("%d items", len(p.filteredVault))
	if p.dbStatus.DirtySinceBackup {
		base += " · db*"
	}
	return base
}

func (p vaultPanel) Breadcrumbs() []string {
	if p.drillOpen {
		if it, ok := p.selectedVaultItem(); ok {
			return []string{"Env & Secrets", "Vault", truncate(it.Label, 28)}
		}
	}
	return []string{"Env & Secrets", fmt.Sprintf("Vault (%d)", len(p.filteredVault))}
}

func (p vaultPanel) StatusChips() []string {
	var chips []string
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	if p.vaultLoaded {
		present := 0
		for _, it := range p.vaultInv.Items {
			if it.ExistsLocal {
				present++
			}
		}
		chips = append(chips, fmt.Sprintf("vault:%d/%d", present, len(p.vaultInv.Items)))
		if p.vaultInv.HasOPCLI {
			chips = append(chips, "op:ready")
		} else {
			chips = append(chips, "op:missing")
		}
	}
	if p.dbStatus.DirtySinceBackup {
		chips = append(chips, "db:dirty")
	} else if p.dbLoaded && p.dbErr == "" {
		chips = append(chips, "db:synced")
	}
	return chips
}

func (p vaultPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.drillOpen {
		return [][2]string{
			{"j/k", "Scroll detail"},
			{"a", "Vault actions"},
			{"y", "Copy CLI"},
			{"r", "Refresh"},
			{"esc", "Back to vault"},
		}
	}
	return [][2]string{
		{"enter", "Item detail"},
		{"a", "Vault actions"},
		{"y", "Copy CLI"},
		{"r", "Refresh"},
		{"space/*", "Mark scopes"},
	}
}

func (p vaultPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	return []RowAction{
		{Key: "s", Label: "Save to 1Password", Description: "Upload selected scope(s) to 1Password"},
		{Key: "l", Label: "Load from 1Password", Description: "Restore selected scope(s) from 1Password", Dangerous: true},
		{Key: "K", Label: "Init / Backup DB key", Description: "Ensure ~/.ws/db.key exists and back up to 1Password"},
		{Key: "b", Label: "Backup ws.db", Description: "Write encrypted ws.db backup if changed"},
		{Key: "U", Label: "Undo last transaction", Description: "Roll back last workstation transaction (ws undo)", Dangerous: true},
	}
}

func (p vaultPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (vaultPanel, tea.Cmd) {
	switch actionKey {
	case "s":
		return p.requestVaultAction("save")
	case "l":
		return p.requestVaultAction("load")
	case "K":
		return p.requestVaultKeyInitBackup()
	case "b":
		return p.startBackup(backend)
	case "U":
		return p.requestUndo()
	}
	return p, nil
}

func (p vaultPanel) requestUndo() (vaultPanel, tea.Cmd) {
	if p.undoing {
		return p, nil
	}
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Undo Last Transaction",
			Body:         "Roll back the most recent recorded workstation transaction from ws.db / ledger snapshots?\n$ ws undo",
			Warning:      "Files modified by that transaction will be restored or removed.",
			ConfirmLabel: "Undo",
			Dangerous:    true,
			StartPanel:   PanelEnv,
			StartMsg:     vaultMutationStartMsg{action: "undo"},
			OnConfirm: func(b Backend) tea.Cmd {
				return undoLastCmd(b)
			},
		}
	}
}

func (p vaultPanel) selectedVaultItem() (VaultItemRow, bool) {
	if len(p.filteredVault) == 0 || p.vaultCursor < 0 || p.vaultCursor >= len(p.filteredVault) {
		return VaultItemRow{}, false
	}
	return p.filteredVault[p.vaultCursor], true
}

func (p vaultPanel) targetVaultItems() []VaultItemRow {
	if p.vaultSel.Count() > 0 {
		var out []VaultItemRow
		for _, it := range p.filteredVault {
			if p.vaultSel.Has(it.ID) {
				out = append(out, it)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if it, ok := p.selectedVaultItem(); ok {
		return []VaultItemRow{it}
	}
	return nil
}

func vaultItemCLICommand(mode string, it VaultItemRow) string {
	switch it.Scope {
	case "db":
		if mode == "load" {
			return "ws db restore --from-1password"
		}
		return "ws db backup --1password --force"
	case "db-key":
		if mode == "load" {
			return "ws db key restore"
		}
		return "ws db key backup"
	default:
		parts := []string{"ws", "vault", it.Scope, mode}
		if it.DocTitle != "" {
			parts = append(parts, "--doc", it.DocTitle)
		}
		if it.TargetArg != "" {
			parts = append(parts, it.TargetArg)
		}
		return strings.Join(parts, " ")
	}
}

func (p vaultPanel) CLIEquivalent() string {
	if it, ok := p.selectedVaultItem(); ok {
		return vaultItemCLICommand("save", it)
	}
	return "ws db backup --if-changed"
}

func (p vaultPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (vaultPanel, tea.Cmd) {
	switch msg := msg.(type) {
	case vaultMutationStartMsg:
		if msg.action == "undo" {
			p.undoing = true
			return p, nil
		}
		p.vaultBusy = true
		if p.vaultOpActID == 0 {
			p.vaultOpActID = newActivityID()
		}
		return p, startActivityCmd(p.vaultOpActID, "Running 1Password "+msg.action)

	case vaultInventoryLoadedMsg:
		p.vaultLoaded = true
		var cmds []tea.Cmd
		if p.vaultActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.vaultActID, "Scanning 1Password vault", msg.err != nil))
			p.vaultActID = 0
		}
		if msg.err != nil {
			p.vaultErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Vault inventory failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.vaultErr = ""
		p.vaultInv = msg.inv
		p.recomputeVaultFiltered()
		return p, batchNonNil(cmds...)

	case vaultActionDoneMsg:
		p.vaultBusy = false
		var cmds []tea.Cmd
		if p.vaultOpActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.vaultOpActID, "1Password "+msg.action, msg.err != nil))
			p.vaultOpActID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Vault " + msg.action + " failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.vaultSel = p.vaultSel.Clear()
		p.vaultActID = newActivityID()
		p.dbActID = newActivityID()
		cmds = append(cmds,
			func() tea.Msg {
				return statusToastMsg{text: "Vault " + msg.action + " completed (see Output 0 for log)"}
			},
			startActivityCmd(p.vaultActID, "Refreshing vault inventory"),
			startActivityCmd(p.dbActID, "Checking ws.db status"),
			loadVaultInventoryCmd(backend),
			loadDBStatusCmd(backend),
		)
		return p, batchNonNil(cmds...)

	case dbStatusLoadedMsg:
		p.dbLoaded = true
		var cmds []tea.Cmd
		if p.dbActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.dbActID, "Checking ws.db status", msg.err != nil))
			p.dbActID = 0
		}
		if msg.err != nil {
			p.dbErr = msg.err.Error()
			return p, batchNonNil(cmds...)
		}
		p.dbErr = ""
		p.dbStatus = msg.status
		return p, batchNonNil(cmds...)

	case dbBackedUpMsg:
		p.backingUp = false
		var actCmd tea.Cmd
		if p.bkActID != 0 {
			actCmd = finishActivityCmd(p.bkActID, "Backing up ws.db", msg.err != nil)
			p.bkActID = 0
		}
		return p, actCmd

	case undoDoneMsg:
		p.undoing = false
		var actCmd tea.Cmd
		if p.undoActID != 0 {
			actCmd = finishActivityCmd(p.undoActID, "Undoing transaction", msg.err != nil)
			p.undoActID = 0
		}
		return p, actCmd

	case tea.KeyPressMsg:
		if !p.drillOpen && isSpaceKey(msg) {
			if it, ok := p.selectedVaultItem(); ok {
				p.vaultSel = p.vaultSel.Toggle(it.ID)
			}
			return p, nil
		}
		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview || p.drillOpen {
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
				return p, nil
			}
			if next, changed := applyListMotion(p.vaultCursor, len(p.filteredVault), defaultMotionPage, m); changed {
				p.vaultCursor = next
				p.previewScroll = 0
			}
			return p, nil
		}
		switch msg.String() {
		case "*":
			keys := make([]string, len(p.filteredVault))
			for i, it := range p.filteredVault {
				keys[i] = it.ID
			}
			p.vaultSel = p.vaultSel.ToggleAll(keys)
			return p, nil
		case `\`:
			p.vaultSel = p.vaultSel.Clear()
			return p, nil
		case "enter":
			if _, ok := p.selectedVaultItem(); ok || p.dbLoaded {
				p.drillOpen = !p.drillOpen
				p.previewScroll = 0
			}
			return p, nil
		case "y":
			if it, ok := p.selectedVaultItem(); ok {
				return p, copyCmd(vaultItemCLICommand("save", it), "1Password command")
			}
			if p.dbStatus.Path != "" {
				return p, copyCmd(p.dbStatus.Path, "ws.db path")
			}
			return p, nil
		case "r":
			p.dbActID = newActivityID()
			p.vaultActID = newActivityID()
			return p, tea.Batch(
				startActivityCmd(p.dbActID, "Checking ws.db status"),
				startActivityCmd(p.vaultActID, "Scanning 1Password vault"),
				loadDBStatusCmd(backend),
				loadVaultInventoryCmd(backend),
			)
		}
	}
	return p, nil
}

func (p vaultPanel) requestVaultAction(mode string) (vaultPanel, tea.Cmd) {
	if p.vaultBusy {
		return p, func() tea.Msg { return statusToastMsg{text: "Vault action already in progress"} }
	}
	targets := p.targetVaultItems()
	if len(targets) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "No vault item selected"} }
	}

	var labels []string
	var cmds []string
	for _, it := range targets {
		labels = append(labels, it.Label)
		cmds = append(cmds, "$ "+vaultItemCLICommand(mode, it))
	}
	title := "Save Selected Items to 1Password"
	confirmLabel := fmt.Sprintf("Save (%d)", len(targets))
	warn := "Uploads encrypted backups or archives to 1Password via signed-in op CLI."
	dangerous := false
	if mode == "load" {
		title = "Load / Restore Selected Items from 1Password"
		confirmLabel = fmt.Sprintf("Restore (%d)", len(targets))
		warn = "Restoring from 1Password replaces existing local files or database records for the selected scope(s)."
		dangerous = true
	}

	body := fmt.Sprintf("Scopes (%d): %s\n\n%s", len(targets), strings.Join(labels, ", "), strings.Join(cmds, "\n"))
	targetsCopy := append([]VaultItemRow(nil), targets...)
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        title,
			Body:         body,
			Warning:      warn,
			ConfirmLabel: confirmLabel,
			Dangerous:    dangerous,
			StartPanel:   PanelEnv,
			StartMsg:     vaultMutationStartMsg{action: mode},
			OnConfirm: func(b Backend) tea.Cmd {
				return runVaultActionCmd(b, mode, targetsCopy)
			},
		}
	}
}

func (p vaultPanel) requestVaultKeyInitBackup() (vaultPanel, tea.Cmd) {
	if p.vaultBusy {
		return p, func() tea.Msg { return statusToastMsg{text: "Vault action already in progress"} }
	}
	var target VaultItemRow
	for _, it := range p.vaultInv.Items {
		if it.Scope == "db-key" {
			target = it
			break
		}
	}
	if target.ID == "" {
		target = VaultItemRow{ID: "ws-db-key", Scope: "db-key", Label: "ws.db Master Key (db.key)", DocTitle: "ws-db.key"}
	}
	body := fmt.Sprintf("Ensure 256-bit ws.db key exists at %s (chmod 0600) and upload backup document %q to 1Password.\n\n$ ws db key init && ws db key backup",
		nonEmpty(target.LocalPath, "~/.ws/db.key"), nonEmpty(target.DocTitle, "ws-db.key"))
	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        "Initialize & Back Up ws.db Key",
			Body:         body,
			Warning:      "Keep ws-db.key in 1Password separate from ws-db-backup.enc; losing the key makes encrypted snapshots unreadable.",
			ConfirmLabel: "Init & Backup Key",
			Dangerous:    false,
			StartPanel:   PanelEnv,
			StartMsg:     vaultMutationStartMsg{action: "key"},
			OnConfirm: func(b Backend) tea.Cmd {
				return runVaultActionCmd(b, "key", []VaultItemRow{target})
			},
		}
	}
}

func (p vaultPanel) startBackup(backend Backend) (vaultPanel, tea.Cmd) {
	if p.backingUp {
		return p, nil
	}
	p.backingUp = true
	p.bkActID = newActivityID()
	return p, tea.Batch(
		startActivityCmd(p.bkActID, "Backing up ws.db"),
		backupDBCmd(backend, true),
	)
}

func (p vaultPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.drillOpen {
		return p.ViewPreview(width, height, focused, false, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	dbState := st.BadgeOK.Render("DB SYNCED")
	if p.dbStatus.DirtySinceBackup {
		dbState = st.BadgeWarn.Render("DB DIRTY")
	}
	if p.dbErr != "" {
		dbState = st.BadgeErr.Render("DB ERR")
	}
	keyState := st.BadgeErr.Render("KEY MISSING")
	if p.dbStatus.HasKey {
		keyState = st.BadgeOK.Render("KEY READY")
	}
	opState := st.BadgeWarn.Render("op missing")
	if p.vaultInv.HasOPCLI {
		vaultName := nonEmpty(p.vaultInv.OPVault, "default")
		opState = st.BadgeOK.Render("op:" + vaultName)
	}
	headerLine := dbState + " " + keyState + " " + opState + "  " + st.Muted.Render(truncate("Path: "+nonEmpty(p.dbStatus.Path, "~/.ws/ws.db"), max(8, innerW-38)))
	lines := []string{
		headerLine,
		st.InfoText.Render(truncate("Policy: back up ws.db (ws-db-backup.enc) + db.key separately; ~/.ws/cache, logs & exports excluded", innerW)),
	}
	if p.dbErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("ws.db error: "+p.dbErr, innerW)))
	}
	if p.vaultErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("vault error: "+p.vaultErr, innerW)))
	}
	lines = append(lines, "")

	if !p.vaultLoaded {
		lines = append(lines, st.Muted.Render("Scanning 1Password & local vault scopes..."))
		lines = append(lines, "")
		lines = append(lines, renderDBDetailsBlock(p.dbStatus, p.dbLoaded, p.dbErr, innerW, st)...)
		return renderTitledBox("Vault & 1Password Manager", p.Badge(), lines, width, height, focused, st)
	}
	if len(p.filteredVault) == 0 {
		lines = append(lines, st.Muted.Render("No vault items match the current filter."))
		return renderTitledBox("Vault & 1Password Manager (0)", p.Badge(), lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredVault))
	marked := make(map[int]bool, p.vaultSel.Count())
	for i, it := range p.filteredVault {
		if p.vaultSel.Has(it.ID) {
			marked[i] = true
		}
		localSummary := st.WarnText.Render("missing")
		if it.ExistsLocal {
			if it.IsDir {
				localSummary = st.OKText.Render(fmt.Sprintf("%d file(s) (%s)", it.FileCount, formatBytes(it.SizeBytes)))
			} else {
				localSummary = st.OKText.Render(formatBytes(it.SizeBytes))
			}
		}
		rows = append(rows, []string{
			strings.ToUpper(nonEmpty(it.Scope, "vault")),
			it.Label,
			it.DocTitle,
			localSummary,
		})
	}
	tableH := max(4, innerH-len(lines))
	lines = append(lines, renderSelectableTable([]string{"Scope", "Item", "1Password Doc", "Local"}, rows, p.vaultCursor, marked, focused, innerW, tableH, st)...)
	return renderTitledBox(fmt.Sprintf("Vault & 1Password Manager (%d)", len(p.filteredVault)), p.Badge(), lines, width, height, focused, st)
}

func (p vaultPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	var all []string
	if it, ok := p.selectedVaultItem(); ok {
		stateBadge := st.BadgeWarn.Render("LOCAL MISSING")
		if it.ExistsLocal {
			stateBadge = st.BadgeOK.Render("LOCAL PRESENT")
		}
		all = append(all, stateBadge+" "+st.AccentText.Render(truncate(it.Label, max(6, innerW-16))))
		all = append(all, "")
		modStr := "—"
		if it.ModTime > 0 {
			modStr = time.Unix(it.ModTime, 0).Local().Format("2006-01-02 15:04")
		}
		itemPairs := [][2]string{
			{"Scope", it.Scope},
			{"1Password Doc", it.DocTitle},
			{"Local Path", nonEmpty(it.LocalPath, "—")},
			{"Files / Size", fmt.Sprintf("%d file(s) · %s", it.FileCount, formatBytes(it.SizeBytes))},
			{"Modified", modStr},
			{"Save CLI", vaultItemCLICommand("save", it)},
			{"Restore CLI", vaultItemCLICommand("load", it)},
		}
		all = append(all, renderKVTable(itemPairs, innerW, st)...)
		if it.Note != "" {
			all = append(all, "")
			all = append(all, wrapPlainTextLines(it.Note, innerW, st.InfoText)...)
		}
		all = append(all, "")
	}
	all = append(all, st.SectionHeader.Render(truncate("ws.db Encrypted Store & ~/.ws Backup Policy", innerW)))
	all = append(all, renderDBDetailsBlock(p.dbStatus, p.dbLoaded, p.dbErr, innerW, st)...)
	all = append(all, "")
	all = append(all, wrapPlainTextLines("Why exclude ~/.ws/cache, logs, exports, and ledger.lock? Caches/logs are machine-local rebuildable data and SQLite WAL files cannot be copied mid-write. Vault/backup exports create consistent AES-256-GCM ws.db snapshots alongside separate db.key backups.", innerW, st.Muted)...)

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Vault Scope & ws.db Recovery", "", vis, width, height, focused, st)
}

func renderDBDetailsBlock(d DBStatusSummary, dbLoaded bool, dbErr string, innerW int, st Styles) []string {
	if !dbLoaded && d.Path == "" && dbErr == "" {
		return []string{st.Muted.Render("Loading ws.db status...")}
	}
	if dbErr != "" {
		return []string{
			st.BadgeErr.Render("DB ERROR") + " " + st.AccentText.Render("ws.db"),
			"",
			st.ErrText.Render(truncate(dbErr, innerW)),
		}
	}
	stateBadge := st.BadgeOK.Render("SYNCED")
	if d.DirtySinceBackup {
		stateBadge = st.BadgeWarn.Render("DIRTY")
	}
	keyBadge := st.BadgeErr.Render("KEY LOCKED")
	if d.HasKey {
		keyBadge = st.BadgeOK.Render("KEY READY")
	}

	pairs := [][2]string{
		{"State", stateBadge + " " + keyBadge},
		{"Path", nonEmpty(d.Path, "—")},
		{"Schema", fmt.Sprintf("v%d", d.SchemaVersion)},
		{"Key Source", st.InfoText.Render(nonEmpty(d.KeySource, "none"))},
		{"Last Backup", nonEmpty(d.LastBackupAt, "never")},
		{"Current Hash", st.Muted.Render(nonEmpty(d.CurrentHash, "—"))},
		{"Indexed State", fmt.Sprintf("Envs: %d · Blobs: %d · Repos: %d", d.EnvCount, d.BlobCount, d.RepoCount)},
	}
	return renderKVTable(pairs, innerW, st)
}

func loadVaultInventoryCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return vaultInventoryLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		inv, err := backend.LoadVaultInventory()
		return vaultInventoryLoadedMsg{inv: inv, err: err}
	}
}

func runVaultActionCmd(backend Backend, action string, items []VaultItemRow) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return vaultActionDoneMsg{action: action, err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.RunVaultAction(action, items)
		return vaultActionDoneMsg{action: action, result: res, err: err}
	}
}
