package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

const (
	fleetSubNodes = iota
	fleetSubFileSync
	fleetSubCount
)

type fleetFileSyncStartMsg struct{}

type fleetPanel struct {
	subView int

	nodes         []FleetNodeRow
	filtered      []FleetNodeRow
	cursor        int
	chkCursor     int
	previewScroll int
	drillOpen     bool
	filterQuery   string
	joinCmd       string
	joinNode      string
	joinErr       string
	noZones       bool
	pushEnv       bool
	preview       FleetSyncPreview
	previewNode   string
	previewErr    string
	loaded        bool
	loadErr       string
	syncing       bool
	loadActID     int
	syncActID     int

	syncOpen         bool
	syncConfirmFocus bool

	syncPeerIdx     int
	fileDiff        FleetFileDiffView
	filteredFiles   []FleetFileDiffRow
	fileCursor      int
	fileSel         SelectionSet
	fileFilters     FacetFilterState
	fileIncludeEnv  bool
	fileIncludeDB   bool
	fileLoading     bool
	fileSyncing     bool
	fileDiffErr     string
	fileTextNode    string
	fileTextPath    string
	fileTextDiff    string
	fileTextErr     string
	fileTextLoading bool
	fileDiffActID   int
	fileSyncActID   int
}

func newFleetPanel() fleetPanel {
	return fleetPanel{}
}

func (p fleetPanel) ID() string         { return "fleet" }
func (p fleetPanel) Title() string      { return "Fleet & Tailscale" }
func (p fleetPanel) ShortTitle() string { return "Fleet" }
func (p fleetPanel) SubViews() []string { return []string{"Nodes", "File Sync"} }
func (p fleetPanel) ActiveSubView() int { return p.subView }
func (p fleetPanel) SetSubView(idx int) ResourcePanel {
	if idx >= 0 && idx < fleetSubCount {
		p.subView = idx
		p.drillOpen = false
		p.previewScroll = 0
		p.fileFilters.Close()
	}
	return p
}

func (p fleetPanel) OnSubViewChanged(backend Backend) (ResourcePanel, tea.Cmd) {
	if p.subView != fleetSubFileSync {
		return p, nil
	}
	p.ensurePreferredSyncPeer()
	peer, ok := p.selectedSyncPeer()
	if !ok || backend == nil || p.fileLoading {
		return p, nil
	}
	if p.fileDiff.Node != peer.Name || (len(p.fileDiff.Entries) == 0 && p.fileDiffErr == "") {
		return p.startFileDiff(backend)
	}
	return p, nil
}

func (p fleetPanel) SetScope(string, Backend) (ResourcePanel, tea.Cmd) { return p, nil }
func (p fleetPanel) IsZoned() bool                                     { return false }
func (p fleetPanel) FilterQuery() string                               { return p.filterQuery }
func (p fleetPanel) FilterTarget() string {
	if p.subView == fleetSubFileSync {
		return "File Diff"
	}
	return "Fleet Nodes"
}
func (p fleetPanel) SelectedCount() int {
	if p.subView == fleetSubFileSync {
		return p.fileSel.Count()
	}
	return 0
}
func (p fleetPanel) ClearSelection() ResourcePanel {
	if p.subView == fleetSubFileSync {
		p.fileSel = p.fileSel.Clear()
	}
	return p
}
func (p fleetPanel) SupportsPreview() bool { return !p.drillOpen }
func (p fleetPanel) DefaultSplit(width int) SplitMode {
	if p.subView == fleetSubFileSync && width >= 112 {
		return SplitVertical
	}
	return SplitOff
}
func (p fleetPanel) CanPop() bool { return p.drillOpen || p.subView == fleetSubFileSync }
func (p fleetPanel) Pop() ResourcePanel {
	if p.drillOpen {
		p.drillOpen = false
		p.previewScroll = 0
		return p
	}
	if p.subView == fleetSubFileSync {
		p.subView = fleetSubNodes
		p.previewScroll = 0
	}
	return p
}

func (p fleetPanel) SetFilterQuery(q string, backend Backend) (ResourcePanel, tea.Cmd) {
	p.filterQuery = q
	p.recomputeFiltered()
	p.recomputeFileFiltered()
	if p.subView == fleetSubFileSync {
		p.fileCursor = 0
		p.previewScroll = 0
		return p, nil
	}
	p.cursor = 0
	if n, ok := p.selectedNode(); ok && backend != nil && (p.previewNode != n.Name || p.joinNode != n.Name) {
		p.noZones = n.NoZones
		p.pushEnv = n.PushEnv
		p.chkCursor = 0
		return p, p.refreshSelectionCmds(backend, n)
	}
	return p, nil
}

func (p fleetPanel) OnBlur() ResourcePanel {
	p.syncOpen = false
	p.syncConfirmFocus = false
	p.fileFilters.Close()
	return p
}

func (p *fleetPanel) recomputeFiltered() {
	p.filtered = fuzzy.Filter(p.nodes, p.filterQuery, func(n FleetNodeRow) ([]string, map[string]string) {
		online := "offline"
		if n.Online {
			online = "online"
		}
		return []string{n.Name, n.Provider, n.OS + "/" + n.Arch, n.TailscaleIP, n.Preset, strings.Join(n.Tags, " ")},
			map[string]string{
				"status":   online,
				"provider": strings.ToLower(n.Provider),
				"os":       strings.ToLower(n.OS),
				"preset":   strings.ToLower(n.Preset),
			}
	})
	if p.cursor >= len(p.filtered) {
		p.cursor = max(0, len(p.filtered)-1)
	}
}

func (p *fleetPanel) recomputeFileFiltered() {
	pre := fuzzy.Filter(p.fileDiff.Entries, p.filterQuery, func(e FleetFileDiffRow) ([]string, map[string]string) {
		sec := "config"
		if e.IsSecret {
			sec = "secret"
		}
		return []string{e.RelPath, e.Category, e.Status, e.Root, sec},
			map[string]string{
				"status": e.Status,
				"cat":    e.Category,
				"sec":    sec,
			}
	})

	out := make([]FleetFileDiffRow, 0, len(pre))
	for _, e := range pre {
		wantStatus := p.fileFilters.Get("status")
		if wantStatus != "" {
			if strings.EqualFold(wantStatus, "diff") {
				if e.Status != "local-newer" && e.Status != "remote-newer" && e.Status != "modified" {
					continue
				}
			} else if !strings.EqualFold(wantStatus, e.Status) {
				continue
			}
		}
		if !p.fileFilters.Matches("cat", e.Category) {
			continue
		}
		sec := "config"
		if e.IsSecret {
			sec = "secret"
		}
		if !p.fileFilters.Matches("sec", sec) {
			continue
		}
		out = append(out, e)
	}

	if p.fileFilters.SortKey != "" {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := out[i], out[j]
			var less bool
			switch p.fileFilters.SortKey {
			case "path":
				less = a.RelPath < b.RelPath
			case "size":
				sa := max(a.Local.SizeBytes, a.Remote.SizeBytes)
				sb := max(b.Local.SizeBytes, b.Remote.SizeBytes)
				if sa != sb {
					less = sa < sb
				} else {
					less = a.RelPath < b.RelPath
				}
			case "mtime":
				ma := max(a.Local.ModTime, a.Remote.ModTime)
				mb := max(b.Local.ModTime, b.Remote.ModTime)
				if ma != mb {
					less = ma < mb
				} else {
					less = a.RelPath < b.RelPath
				}
			default:
				ra, rb := fleetFileStatusOrder(a.Status), fleetFileStatusOrder(b.Status)
				if ra != rb {
					less = ra < rb
				} else {
					less = a.RelPath < b.RelPath
				}
			}
			if p.fileFilters.SortDesc {
				return !less
			}
			return less
		})
	}

	p.filteredFiles = out
	if p.fileCursor >= len(p.filteredFiles) {
		p.fileCursor = max(0, len(p.filteredFiles)-1)
	}
}

func fleetFileStatusOrder(st string) int {
	switch st {
	case "local-newer", "remote-newer", "modified":
		return 0
	case "local-only", "remote-only":
		return 1
	default:
		return 2
	}
}

func (p fleetPanel) fileSyncFacetGroups() ([]FacetGroup, []SortOption) {
	statusCounts := map[string]int{"diff": 0, "local-only": 0, "remote-only": 0, "same": 0}
	catCounts := map[string]int{}
	secCounts := map[string]int{"secret": 0, "config": 0}
	for _, e := range p.fileDiff.Entries {
		switch e.Status {
		case "local-newer", "remote-newer", "modified":
			statusCounts["diff"]++
		case "local-only":
			statusCounts["local-only"]++
		case "remote-only":
			statusCounts["remote-only"]++
		case "same":
			statusCounts["same"]++
		}
		if e.Category != "" {
			catCounts[e.Category]++
		}
		if e.IsSecret {
			secCounts["secret"]++
		} else {
			secCounts["config"]++
		}
	}

	var catOpts []FacetOption
	for _, c := range []string{"ssh", "files", "secrets", "envfiles", "db"} {
		if n := catCounts[c]; n > 0 {
			catOpts = append(catOpts, FacetOption{Value: c, Label: c, Count: n})
		}
	}

	groups := []FacetGroup{
		{
			Key:   "status",
			Label: "Diff State",
			Options: []FacetOption{
				{Value: "diff", Label: "Modified", Count: statusCounts["diff"]},
				{Value: "local-only", Label: "Local Only", Count: statusCounts["local-only"]},
				{Value: "remote-only", Label: "Remote Only", Count: statusCounts["remote-only"]},
				{Value: "same", Label: "Matched", Count: statusCounts["same"]},
			},
		},
		{
			Key:   "sec",
			Label: "Sensitivity",
			Options: []FacetOption{
				{Value: "config", Label: "Non-Secret Config", Count: secCounts["config"]},
				{Value: "secret", Label: "Secret / Key", Count: secCounts["secret"]},
			},
		},
	}
	if len(catOpts) > 0 {
		groups = append(groups, FacetGroup{
			Key:     "cat",
			Label:   "Category",
			Options: catOpts,
		})
	}

	sorts := []SortOption{
		{Key: "status", Label: "Diff Status"},
		{Key: "path", Label: "Path"},
		{Key: "size", Label: "File Size"},
		{Key: "mtime", Label: "Modified Time"},
	}
	return groups, sorts
}

func (p fleetPanel) Attention() AttentionLevel {
	if p.loadErr != "" || p.joinErr != "" || p.previewErr != "" || p.fileDiffErr != "" {
		return AttentionErr
	}
	return AttentionNone
}

func (p fleetPanel) PaneStatus() string {
	switch {
	case p.fileSyncing:
		return "syncing files..."
	case p.fileLoading:
		return "diffing peer..."
	case p.syncing:
		return "syncing..."
	case p.loadActID != 0:
		return "refreshing..."
	default:
		return ""
	}
}

func (p fleetPanel) Badge() string {
	if p.subView == fleetSubFileSync {
		if peer, ok := p.selectedSyncPeer(); ok {
			return fmt.Sprintf("%s · %dΔ", peer.Name, p.fileDiff.DiffCount+p.fileDiff.LocalOnlyCount+p.fileDiff.RemoteOnlyCount)
		}
	}
	if !p.loaded {
		return "…"
	}
	up := 0
	for _, n := range p.nodes {
		if n.Online {
			up++
		}
	}
	return fmt.Sprintf("%d/%d up", up, len(p.nodes))
}

func (p fleetPanel) Breadcrumbs() []string {
	if p.subView == fleetSubFileSync {
		peerName := "no-peer"
		if peer, ok := p.selectedSyncPeer(); ok {
			peerName = peer.Name
		}
		if p.drillOpen {
			if row, ok := p.selectedDiffFile(); ok {
				return []string{"Fleet", "File Sync", peerName, truncate(row.RelPath, 28)}
			}
		}
		return []string{"Fleet", fmt.Sprintf("File Sync (%s · %d)", peerName, len(p.filteredFiles))}
	}
	if p.drillOpen {
		if n, ok := p.selectedNode(); ok {
			return []string{"Fleet", n.Name}
		}
	}
	return []string{"Fleet", fmt.Sprintf("Nodes (%d)", len(p.filtered))}
}

func (p fleetPanel) StatusChips() []string {
	var chips []string
	if p.filterQuery != "" {
		chips = append(chips, "/"+truncate(p.filterQuery, 16))
	}
	if p.subView == fleetSubFileSync {
		chips = append(chips, p.fileFilters.Chips()...)
		if p.fileIncludeEnv {
			chips = append(chips, "env*")
		}
		if p.fileIncludeDB {
			chips = append(chips, "ws.db.enc")
		}
		return chips
	}
	if p.noZones {
		chips = append(chips, "no-zones")
	}
	if p.pushEnv {
		chips = append(chips, "push-env")
	}
	return chips
}

func (p fleetPanel) KeyHints(split SplitMode, focusPreview bool) [][2]string {
	if p.subView == fleetSubFileSync {
		if p.drillOpen {
			return [][2]string{
				{"d", "Load diff"},
				{"a", "Sync actions"},
				{"y", "Copy CLI"},
				{"r", "Refresh"},
				{"esc", "Back to files"},
			}
		}
		return [][2]string{
			{"enter/d", "Diff preview"},
			{"a", "Sync actions"},
			{"</>", "Switch peer"},
			{"y", "Copy CLI"},
			{"r", "Refresh files"},
			{"F/S", "Filter / Sort"},
			{"space/*", "Mark files"},
		}
	}
	if p.drillOpen {
		return [][2]string{
			{"j/k", "Scroll / checklist"},
			{"d", "Diff files"},
			{"o", "Open SSH"},
			{"y/Y", "Copy cmd / join"},
			{"a", "Node actions"},
			{"r", "Refresh"},
			{"esc", "Back to nodes"},
		}
	}
	return [][2]string{
		{"enter", "Node detail"},
		{"d", "Diff files"},
		{"o", "Open SSH"},
		{"y/Y", "Copy cmd / join"},
		{"a", "Node actions"},
		{"r", "Refresh fleet"},
	}
}

func (p fleetPanel) RowActions(split SplitMode, focusPreview bool) []RowAction {
	if p.subView == fleetSubFileSync {
		return []RowAction{
			{Key: "P", Label: "Push files to peer", Description: "Upload selected file(s) to remote peer", Dangerous: true},
			{Key: "L", Label: "Pull files from peer", Description: "Download selected file(s) from remote peer", Dangerous: true},
			{Key: "E", Label: "Toggle .env* files", Description: "Include or exclude zone/code .env* files in diff"},
			{Key: "B", Label: "Toggle ws.db.enc", Description: "Include or exclude encrypted ws.db snapshot in diff"},
		}
	}
	return []RowAction{
		{Key: "s", Label: "Sync node", Description: "Plan and execute workstation sync to node", Dangerous: true},
		{Key: "t", Label: "Open SSH session", Description: "Connect to selected node over Tailscale SSH"},
		{Key: "Z", Label: "Toggle --no-zones", Description: "Toggle flat ~/code sync mode for selected node"},
		{Key: "E", Label: "Toggle --push-env", Description: "Toggle pushing .env* files during node sync"},
		{Key: "d", Label: "Diff files", Description: "Compare local and remote files with selected peer"},
	}
}

func (p fleetPanel) RunRowAction(actionKey string, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if p.subView == fleetSubFileSync {
		switch actionKey {
		case "P":
			return p.requestFileSyncModal("push")
		case "L":
			return p.requestFileSyncModal("pull")
		case "E":
			p.fileIncludeEnv = !p.fileIncludeEnv
			return p.startFileDiff(backend)
		case "B":
			p.fileIncludeDB = !p.fileIncludeDB
			return p.startFileDiff(backend)
		}
		return p, nil
	}
	switch actionKey {
	case "s":
		if _, ok := p.selectedNode(); !ok {
			return p, func() tea.Msg { return statusToastMsg{text: "No fleet node selected"} }
		}
		p.syncOpen = true
		p.syncConfirmFocus = false
	case "t":
		return p.connectSelectedNode(backend)
	case "Z":
		p.noZones = !p.noZones
		if n, ok := p.selectedNode(); ok {
			return p, previewFleetSyncCmd(backend, p.buildSyncReq(true), n.Name)
		}
	case "E":
		p.pushEnv = !p.pushEnv
		if n, ok := p.selectedNode(); ok {
			return p, previewFleetSyncCmd(backend, p.buildSyncReq(true), n.Name)
		}
	case "d":
		return p.openFileSyncForSelectedNode(backend)
	}
	return p, nil
}

func (p fleetPanel) CapturingInput() bool   { return false }
func (p fleetPanel) HasOpenModal() bool     { return p.syncOpen || p.fileFilters.Open }
func (p fleetPanel) MutationInFlight() bool { return p.syncing || p.fileSyncing }

func (p fleetPanel) selectedNode() (FleetNodeRow, bool) {
	if len(p.filtered) == 0 || p.cursor < 0 || p.cursor >= len(p.filtered) {
		return FleetNodeRow{}, false
	}
	return p.filtered[p.cursor], true
}

func (p fleetPanel) remotePeers() []FleetNodeRow {
	var peers []FleetNodeRow
	for _, n := range p.nodes {
		if !n.IsSelf {
			peers = append(peers, n)
		}
	}
	return peers
}

func (p *fleetPanel) ensurePreferredSyncPeer() {
	peers := p.remotePeers()
	if len(peers) == 0 {
		p.syncPeerIdx = 0
		return
	}
	if p.syncPeerIdx > 0 && p.syncPeerIdx < len(peers) {
		return
	}
	if p.fileDiff.Node != "" {
		for i, peer := range peers {
			if peer.Name == p.fileDiff.Node {
				p.syncPeerIdx = i
				return
			}
		}
	}
	for i, peer := range peers {
		if peer.Online && peer.SSHEnabled {
			p.syncPeerIdx = i
			return
		}
	}
	for i, peer := range peers {
		if peer.Online {
			p.syncPeerIdx = i
			return
		}
	}
	p.syncPeerIdx = 0
}

func (p fleetPanel) selectedSyncPeer() (FleetNodeRow, bool) {
	peers := p.remotePeers()
	if len(peers) == 0 {
		return FleetNodeRow{}, false
	}
	idx := clamp(p.syncPeerIdx, 0, len(peers)-1)
	return peers[idx], true
}

func (p fleetPanel) selectedDiffFile() (FleetFileDiffRow, bool) {
	if len(p.filteredFiles) == 0 || p.fileCursor < 0 || p.fileCursor >= len(p.filteredFiles) {
		return FleetFileDiffRow{}, false
	}
	return p.filteredFiles[p.fileCursor], true
}

func (p fleetPanel) targetSyncFiles() []FleetFileDiffRow {
	if p.fileSel.Count() > 0 {
		var out []FleetFileDiffRow
		for _, row := range p.filteredFiles {
			if p.fileSel.Has(row.RelPath) {
				out = append(out, row)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if row, ok := p.selectedDiffFile(); ok {
		return []FleetFileDiffRow{row}
	}
	return nil
}

func (p fleetPanel) activeChecklist(n FleetNodeRow) []FleetChecklistItem {
	if len(p.preview.Checklist) > 0 && p.previewNode == n.Name {
		return p.preview.Checklist
	}
	return n.Checklist
}

func (p fleetPanel) buildSyncReq(dryRun bool) FleetSyncReq {
	n, _ := p.selectedNode()
	return FleetSyncReq{
		Node:         n.Name,
		Transport:    "tailscale-ssh",
		TargetID:     n.TargetID,
		Preset:       n.Preset,
		Arch:         n.Arch,
		NoZones:      p.noZones,
		PushEnvFiles: p.pushEnv,
		DryRun:       dryRun,
	}
}

func (p fleetPanel) CLIEquivalent() string {
	if p.subView == fleetSubFileSync {
		if peer, ok := p.selectedSyncPeer(); ok {
			if p.fileSel.Count() > 0 {
				targets := p.targetSyncFiles()
				relPaths := make([]string, 0, len(targets))
				for _, t := range targets {
					if t.RelPath != "" {
						relPaths = append(relPaths, t.RelPath)
					}
				}
				if len(relPaths) > 0 {
					return buildFleetSyncFilesCLI(peer.Name, "push", relPaths, false)
				}
			}
			cmd := fmt.Sprintf("ws fleet diff %s", peer.Name)
			if p.fileIncludeEnv {
				cmd += " --include-env"
			}
			if p.fileIncludeDB {
				cmd += " --include-db"
			}
			return cmd
		}
		return "ws fleet status"
	}
	if n, ok := p.selectedNode(); ok {
		cmd := fmt.Sprintf("ws fleet sync %s", n.Name)
		if p.noZones {
			cmd += " --no-zones"
		}
		if p.pushEnv {
			cmd += " --push-env"
		}
		return cmd
	}
	return "ws fleet status"
}

func (p fleetPanel) Update(msg tea.Msg, backend Backend, split SplitMode, focusPreview bool) (ResourcePanel, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetFileSyncStartMsg:
		p.fileSyncing = true
		if p.fileSyncActID == 0 {
			p.fileSyncActID = newActivityID()
		}
		return p, startActivityCmd(p.fileSyncActID, "Syncing fleet files")

	case fleetNodesLoadedMsg:
		p.loaded = true
		var cmds []tea.Cmd
		if p.loadActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.loadActID, "Refreshing fleet nodes", msg.err != nil))
			p.loadActID = 0
		}
		if msg.err != nil {
			p.loadErr = msg.err.Error()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Fleet load failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.loadErr = ""
		p.nodes = msg.nodes
		p.recomputeFiltered()
		p.ensurePreferredSyncPeer()
		if n, ok := p.selectedNode(); ok {
			p.noZones = n.NoZones
			p.pushEnv = n.PushEnv
			p.chkCursor = 0
			cmds = append(cmds, p.refreshSelectionCmds(backend, n))
		}
		return p, batchNonNil(cmds...)

	case fleetJoinCmdLoadedMsg:
		if n, ok := p.selectedNode(); ok && msg.node != "" && msg.node != n.Name {
			return p, nil
		}
		p.joinNode = msg.node
		if msg.err != nil {
			p.joinErr = msg.err.Error()
			p.joinCmd = ""
			return p, func() tea.Msg {
				return statusToastMsg{text: "Tailscale join command failed: " + msg.err.Error(), isErr: true}
			}
		}
		p.joinErr = ""
		p.joinCmd = msg.cmd
		return p, nil

	case fleetSyncPreviewMsg:
		if n, ok := p.selectedNode(); ok && msg.node != "" && msg.node != n.Name {
			return p, nil
		}
		p.previewNode = msg.node
		if msg.err != nil {
			p.previewErr = msg.err.Error()
			p.preview = FleetSyncPreview{}
			return p, func() tea.Msg {
				return statusToastMsg{text: "Fleet sync preview failed: " + msg.err.Error(), isErr: true}
			}
		}
		p.previewErr = ""
		p.preview = msg.preview
		if p.chkCursor >= len(p.preview.Checklist) {
			p.chkCursor = max(0, len(p.preview.Checklist)-1)
		}
		return p, nil

	case fleetSyncedMsg:
		p.syncing = false
		var actCmd tea.Cmd
		if p.syncActID != 0 {
			actCmd = finishActivityCmd(p.syncActID, "Syncing "+msg.node, msg.err != nil)
			p.syncActID = 0
		}
		if msg.err != nil {
			return p, batchNonNil(actCmd, func() tea.Msg {
				return statusToastMsg{text: "Fleet sync failed: " + msg.err.Error(), isErr: true}
			})
		}
		summary := nonEmpty(msg.summary, "Synced "+msg.node)
		p.loadActID = newActivityID()
		return p, batchNonNil(
			actCmd,
			func() tea.Msg { return statusToastMsg{text: summary} },
			startActivityCmd(p.loadActID, "Refreshing fleet nodes"),
			loadFleetNodesCmd(backend),
		)

	case fleetConnectFinishedMsg:
		if msg.err != nil {
			return p, func() tea.Msg {
				return statusToastMsg{text: fmt.Sprintf("Session %s ended: %v", msg.node, msg.err), isErr: true}
			}
		}
		return p, func() tea.Msg {
			return statusToastMsg{text: "Closed session to " + msg.node}
		}

	case fleetFileDiffLoadedMsg:
		p.fileLoading = false
		var cmds []tea.Cmd
		if p.fileDiffActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.fileDiffActID, "Diffing "+msg.node, msg.err != nil))
			p.fileDiffActID = 0
		}
		if peer, ok := p.selectedSyncPeer(); ok && msg.node != "" && msg.node != peer.Name {
			return p, batchNonNil(cmds...)
		}
		if msg.err != nil {
			p.fileDiffErr = msg.err.Error()
			p.fileDiff = FleetFileDiffView{Node: msg.node}
			p.recomputeFileFiltered()
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "Fleet file diff failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.fileDiffErr = ""
		p.fileDiff = msg.view
		p.fileSel = p.fileSel.Clear()
		p.fileTextNode = ""
		p.fileTextPath = ""
		p.fileTextDiff = ""
		p.fileTextErr = ""
		p.recomputeFileFiltered()
		return p, batchNonNil(cmds...)

	case fleetFileTextDiffMsg:
		p.fileTextLoading = false
		p.fileTextNode = msg.node
		p.fileTextPath = msg.relPath
		if msg.err != nil {
			p.fileTextErr = msg.err.Error()
			p.fileTextDiff = ""
			return p, func() tea.Msg {
				return statusToastMsg{text: "Diff preview failed: " + msg.err.Error(), isErr: true}
			}
		}
		p.fileTextErr = ""
		p.fileTextDiff = msg.diff
		p.previewScroll = 0
		return p, nil

	case fleetFileSyncedMsg:
		p.fileSyncing = false
		var cmds []tea.Cmd
		if p.fileSyncActID != 0 {
			cmds = append(cmds, finishActivityCmd(p.fileSyncActID, "Syncing "+msg.node, msg.err != nil))
			p.fileSyncActID = 0
		}
		if msg.err != nil {
			cmds = append(cmds, func() tea.Msg {
				return statusToastMsg{text: "File sync failed: " + msg.err.Error(), isErr: true}
			})
			return p, batchNonNil(cmds...)
		}
		p.fileSel = p.fileSel.Clear()
		toast := nonEmpty(msg.res.Summary, fmt.Sprintf("File %s to %s complete (see Output 0)", msg.dir, msg.node))
		cmds = append(cmds, func() tea.Msg { return statusToastMsg{text: toast} })
		nextPanel, diffCmd := p.startFileDiff(backend)
		if diffCmd != nil {
			cmds = append(cmds, diffCmd)
		}
		return nextPanel, batchNonNil(cmds...)

	case tea.KeyPressMsg:
		if p.syncOpen {
			return p.updateSyncModal(msg, backend)
		}
		if p.subView == fleetSubFileSync {
			return p.updateFileSyncKey(msg, backend, focusPreview)
		}
		if m := classifyVimMotion(msg.String()); m != motionNone {
			if focusPreview || p.drillOpen {
				if n, ok := p.selectedNode(); ok {
					chk := p.activeChecklist(n)
					if (m == motionDown || m == motionUp) && len(chk) > 0 {
						if next, changed := applyListMotion(p.chkCursor, len(chk), defaultMotionPage, m); changed {
							p.chkCursor = next
							return p, nil
						}
					}
				}
				p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
				return p, nil
			}
			if next, changed := applyListMotion(p.cursor, len(p.filtered), defaultMotionPage, m); changed {
				p.cursor = next
				p.chkCursor = 0
				p.previewScroll = 0
				if n, ok := p.selectedNode(); ok {
					p.noZones = n.NoZones
					p.pushEnv = n.PushEnv
					return p, p.refreshSelectionCmds(backend, n)
				}
			}
			return p, nil
		}
		switch msg.String() {
		case "enter":
			if _, ok := p.selectedNode(); ok {
				p.drillOpen = !p.drillOpen
				p.previewScroll = 0
			}
		case "d":
			return p.openFileSyncForSelectedNode(backend)
		case "y":
			if focusPreview || p.drillOpen {
				if n, ok := p.selectedNode(); ok {
					chk := p.activeChecklist(n)
					if p.chkCursor >= 0 && p.chkCursor < len(chk) && chk[p.chkCursor].Command != "" {
						return p, copyCmd(chk[p.chkCursor].Command, "checklist command")
					}
				}
			}
			if p.joinCmd != "" {
				return p, copyCmd(p.joinCmd, "Tailscale join command")
			}
		case "Y":
			if p.joinCmd != "" {
				return p, copyCmd(p.joinCmd, "Tailscale join command")
			}
		case "o":
			return p.connectSelectedNode(backend)
		case "r":
			p.loadActID = newActivityID()
			return p, tea.Batch(
				func() tea.Msg { return statusToastMsg{text: "Refreshing Tailscale & fleet nodes..."} },
				startActivityCmd(p.loadActID, "Refreshing fleet nodes"),
				loadFleetNodesCmd(backend),
			)
		}
	}

	return p, nil
}

func (p fleetPanel) openFileSyncForSelectedNode(backend Backend) (ResourcePanel, tea.Cmd) {
	n, ok := p.selectedNode()
	if !ok {
		return p, func() tea.Msg { return statusToastMsg{text: "Select a remote peer first"} }
	}
	if n.IsSelf {
		return p, func() tea.Msg {
			return statusToastMsg{text: "Cannot diff a machine against itself (select a remote peer)", isErr: true}
		}
	}
	peers := p.remotePeers()
	for i, peer := range peers {
		if peer.Name == n.Name {
			p.syncPeerIdx = i
			break
		}
	}
	p.subView = fleetSubFileSync
	p.drillOpen = false
	p.previewScroll = 0
	return p.startFileDiff(backend)
}

func (p fleetPanel) startFileDiff(backend Backend) (fleetPanel, tea.Cmd) {
	peer, ok := p.selectedSyncPeer()
	if !ok {
		return p, func() tea.Msg { return statusToastMsg{text: "No remote Tailscale peer available for file diff"} }
	}
	if backend == nil {
		return p, func() tea.Msg { return statusToastMsg{text: "Backend unavailable", isErr: true} }
	}
	p.fileLoading = true
	p.fileDiffErr = ""
	p.fileDiffActID = newActivityID()
	target := nonEmpty(peer.TargetID, nonEmpty(peer.DNSName, nonEmpty(peer.TailscaleIP, peer.Name)))
	return p, tea.Batch(
		startActivityCmd(p.fileDiffActID, "Diffing files on "+peer.Name),
		diffFleetFilesCmd(backend, peer.Name, target, p.fileIncludeEnv, p.fileIncludeDB),
	)
}

func (p fleetPanel) updateFileSyncKey(msg tea.KeyPressMsg, backend Backend, focusPreview bool) (ResourcePanel, tea.Cmd) {
	if p.fileFilters.Open {
		groups, sorts := p.fileSyncFacetGroups()
		handled, changed := p.fileFilters.Update(msg, groups, sorts)
		if changed {
			p.recomputeFileFiltered()
			p.fileCursor = 0
			p.previewScroll = 0
		}
		if handled {
			return p, nil
		}
	}

	if !p.drillOpen && isSpaceKey(msg) {
		if row, ok := p.selectedDiffFile(); ok {
			p.fileSel = p.fileSel.Toggle(row.RelPath)
		}
		return p, nil
	}

	if m := classifyVimMotion(msg.String()); m != motionNone {
		if focusPreview || p.drillOpen {
			p.previewScroll, _ = applyScrollMotion(p.previewScroll, 0, defaultMotionPage, m)
			return p, nil
		}
		if next, changed := applyListMotion(p.fileCursor, len(p.filteredFiles), defaultMotionPage, m); changed {
			p.fileCursor = next
			p.previewScroll = 0
		}
		return p, nil
	}

	switch msg.String() {
	case "*":
		keys := make([]string, len(p.filteredFiles))
		for i, row := range p.filteredFiles {
			keys[i] = row.RelPath
		}
		p.fileSel = p.fileSel.ToggleAll(keys)
	case `\`:
		p.fileSel = p.fileSel.Clear()
	case "F":
		groups, sorts := p.fileSyncFacetGroups()
		p.fileFilters.OpenFilters(groups, sorts)
	case "S":
		groups, sorts := p.fileSyncFacetGroups()
		p.fileFilters.OpenSort(groups, sorts)
	case "<":
		peers := p.remotePeers()
		if len(peers) > 1 {
			p.syncPeerIdx = (p.syncPeerIdx - 1 + len(peers)) % len(peers)
			return p.startFileDiff(backend)
		}
	case ">":
		peers := p.remotePeers()
		if len(peers) > 1 {
			p.syncPeerIdx = (p.syncPeerIdx + 1) % len(peers)
			return p.startFileDiff(backend)
		}
	case "d":
		return p.loadSelectedFileTextDiff(backend)
	case "enter":
		if _, ok := p.selectedDiffFile(); ok {
			p.drillOpen = !p.drillOpen
			p.previewScroll = 0
			if p.drillOpen {
				return p.loadSelectedFileTextDiff(backend)
			}
		}
	case "y":
		return p, copyCmd(p.CLIEquivalent(), "fleet diff command")
	case "r":
		return p.startFileDiff(backend)
	}
	return p, nil
}

func (p fleetPanel) loadSelectedFileTextDiff(backend Backend) (ResourcePanel, tea.Cmd) {
	row, ok := p.selectedDiffFile()
	if !ok {
		return p, nil
	}
	peer, pok := p.selectedSyncPeer()
	if !pok || backend == nil {
		return p, nil
	}
	target := nonEmpty(peer.TargetID, nonEmpty(peer.DNSName, nonEmpty(peer.TailscaleIP, peer.Name)))
	p.fileTextLoading = true
	p.fileTextNode = peer.Name
	p.fileTextPath = row.RelPath
	p.fileTextErr = ""
	return p, previewFleetFileDiffCmd(backend, peer.Name, target, row.RelPath)
}

func (p fleetPanel) requestFileSyncModal(direction string) (ResourcePanel, tea.Cmd) {
	if p.fileSyncing {
		return p, func() tea.Msg { return statusToastMsg{text: "File sync already in progress"} }
	}
	peer, ok := p.selectedSyncPeer()
	if !ok {
		return p, func() tea.Msg { return statusToastMsg{text: "No remote Tailscale peer selected"} }
	}
	targets := p.targetSyncFiles()
	if len(targets) == 0 {
		return p, func() tea.Msg { return statusToastMsg{text: "No file selected for sync"} }
	}

	relPaths := make([]string, 0, len(targets))
	var sample []string
	for i, t := range targets {
		relPaths = append(relPaths, t.RelPath)
		if i < 5 {
			sample = append(sample, fmt.Sprintf("  • %s (%s)", t.RelPath, t.Status))
		}
	}
	if len(targets) > 5 {
		sample = append(sample, fmt.Sprintf("  … +%d more", len(targets)-5))
	}

	title := fmt.Sprintf("Push Files: Local -> %s", peer.Name)
	confirmLbl := fmt.Sprintf("Push (%d)", len(targets))
	if direction == "pull" {
		title = fmt.Sprintf("Pull Files: %s -> Local", peer.Name)
		confirmLbl = fmt.Sprintf("Pull (%d)", len(targets))
	}

	cliPreview := "$ " + buildFleetSyncFilesCLI(peer.Name, direction, relPaths, true)
	body := fmt.Sprintf("Peer: %s (%s)\nFiles (%d):\n%s\n\n%s",
		peer.Name, nonEmpty(peer.TailscaleIP, nonEmpty(peer.TargetID, "ssh")),
		len(targets), strings.Join(sample, "\n"), cliPreview)
	warn := "Existing files on the destination are backed up with .pre-ws-sync.<stamp> (mode 0600) before overwrite."

	targetID := nonEmpty(peer.TargetID, nonEmpty(peer.DNSName, nonEmpty(peer.TailscaleIP, peer.Name)))
	req := FleetFileSyncReq{
		Node:      peer.Name,
		TargetID:  targetID,
		Direction: direction,
		RelPaths:  relPaths,
	}

	return p, func() tea.Msg {
		return confirmRequestMsg{
			Title:        title,
			Body:         body,
			Warning:      warn,
			ConfirmLabel: confirmLbl,
			Dangerous:    true,
			StartPanel:   PanelFleet,
			StartMsg:     fleetFileSyncStartMsg{},
			OnConfirm: func(b Backend) tea.Cmd {
				return syncFleetFilesCmd(b, req)
			},
		}
	}
}

func (p fleetPanel) refreshSelectionCmds(backend Backend, n FleetNodeRow) tea.Cmd {
	return tea.Batch(
		tailscaleJoinCmd(backend, n.Name, n.Tags),
		previewFleetSyncCmd(backend, p.buildSyncReq(true), n.Name),
	)
}

func (p fleetPanel) connectSelectedNode(backend Backend) (ResourcePanel, tea.Cmd) {
	n, ok := p.selectedNode()
	if !ok {
		return p, func() tea.Msg { return statusToastMsg{text: "No fleet node selected"} }
	}
	if backend == nil {
		return p, func() tea.Msg { return statusToastMsg{text: "Backend unavailable", isErr: true} }
	}
	cmd, err := backend.FleetConnectCmd(n)
	if err != nil {
		return p, func() tea.Msg { return statusToastMsg{text: "Connect failed: " + err.Error(), isErr: true} }
	}
	if cmd == nil {
		return p, func() tea.Msg { return statusToastMsg{text: "No SSH command available for " + n.Name, isErr: true} }
	}
	nodeName := n.Name
	return p, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return fleetConnectFinishedMsg{node: nodeName, err: err}
	})
}

func (p fleetPanel) updateSyncModal(msg tea.KeyPressMsg, backend Backend) (ResourcePanel, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab", "h", "l":
		p.syncConfirmFocus = !p.syncConfirmFocus
	case "esc", "n", "q":
		p.syncOpen = false
		p.syncConfirmFocus = false
		return p, func() tea.Msg { return statusToastMsg{text: "Fleet sync cancelled"} }
	case "enter":
		if !p.syncConfirmFocus {
			p.syncOpen = false
			p.syncConfirmFocus = false
			return p, func() tea.Msg { return statusToastMsg{text: "Fleet sync cancelled"} }
		}
		req := p.buildSyncReq(false)
		p.syncOpen = false
		p.syncConfirmFocus = false
		p.syncing = true
		p.syncActID = newActivityID()
		return p, tea.Batch(
			startActivityCmd(p.syncActID, "Syncing "+req.Node),
			execFleetSyncCmd(backend, req),
		)
	}
	return p, nil
}

func (p fleetPanel) ViewMain(width, height int, focused bool, st Styles) string {
	if p.drillOpen {
		return p.ViewPreview(width, height, focused, false, st)
	}
	if p.subView == fleetSubFileSync {
		return p.viewFileSyncMain(width, height, focused, st)
	}
	innerW := max(12, width-4)
	innerH := max(1, height-2)
	title := fmt.Sprintf("Fleet & Tailscale Nodes (%d)", len(p.filtered))

	if !p.loaded {
		return renderTitledBox(title, p.Badge(), []string{st.Muted.Render("Querying Tailscale & fleet...")}, width, height, focused, st)
	}
	if p.loadErr != "" {
		return renderTitledBox(title, p.Badge(), []string{st.ErrText.Render(truncate(p.loadErr, innerW))}, width, height, focused, st)
	}
	if len(p.filtered) == 0 {
		return renderTitledBox(title, p.Badge(), []string{
			st.Muted.Render("No fleet nodes matched."),
		}, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filtered))
	for _, n := range p.filtered {
		stText := st.WarnText.Render("○ off")
		if n.Online {
			stText = st.OKText.Render("● up")
		}
		nameCell := n.Name
		if n.IsSelf {
			nameCell = n.Name + " (self)"
		}
		transport := "—"
		if n.SSHEnabled {
			transport = "ssh"
		}
		flags := []string{}
		if n.NoZones {
			flags = append(flags, "--no-zones")
		}
		if n.PushEnv {
			flags = append(flags, "--push-env")
		}
		rows = append(rows, []string{
			nameCell,
			stText,
			nonEmpty(n.Provider, "node"),
			strings.Trim(n.OS+"/"+n.Arch, "/"),
			nonEmpty(n.TailscaleIP, "—"),
			nonEmpty(n.Preset, "—"),
			transport,
			nonEmpty(strings.Join(flags, ","), "default"),
			nonEmpty(n.LastSynced, "never"),
		})
	}
	lines := renderDataTable([]string{"Node", "State", "Provider", "OS/Arch", "Tailscale IP", "Preset", "Trans", "Flags", "Synced"}, rows, p.cursor, focused, innerW, innerH, st)
	return renderTitledBox(title, "", lines, width, height, focused, st)
}

func (p fleetPanel) viewFileSyncMain(width, height int, focused bool, st Styles) string {
	innerW := max(12, width-4)
	innerH := max(1, height-2)

	peer, hasPeer := p.selectedSyncPeer()
	if !hasPeer {
		return renderTitledBox("Tailscale File Sync", "", []string{
			st.Muted.Render("No remote Tailscale peers available. Connect or refresh peers first."),
		}, width, height, focused, st)
	}

	envFlag := ".env*:off"
	if p.fileIncludeEnv {
		envFlag = ".env*:ON"
	}
	dbFlag := "ws.db.enc:off"
	if p.fileIncludeDB {
		dbFlag = "ws.db.enc:ON"
	}
	peerBar := st.AccentText.Render("< "+peer.Name+" >") + " " +
		st.Muted.Render(nonEmpty(peer.TailscaleIP, peer.TargetID)) + "  " +
		st.InfoText.Render(fmt.Sprintf("Δ:%d local:%d remote:%d same:%d",
			p.fileDiff.DiffCount, p.fileDiff.LocalOnlyCount, p.fileDiff.RemoteOnlyCount, p.fileDiff.SameCount)) + "  " +
		st.Muted.Render(envFlag+" · "+dbFlag)

	lines := []string{truncate(peerBar, innerW)}
	if p.fileLoading {
		lines = append(lines, st.InfoText.Render("Comparing files over Tailscale SSH..."))
	} else if p.fileDiffErr != "" {
		lines = append(lines, st.ErrText.Render(truncate("Diff error: "+p.fileDiffErr, innerW)))
	}
	lines = append(lines, "")

	title := fmt.Sprintf("Machine File Diff · %s (%d)", peer.Name, len(p.filteredFiles))
	if len(p.filteredFiles) == 0 {
		msg := "No files under default sync scopes (~/.ssh, WS_OP_FILES, private layer)."
		if p.fileLoading {
			msg = "Scanning local & remote files..."
		} else if p.filterQuery != "" || p.fileFilters.HasActive() {
			msg = "No files match active search or filter criteria."
		}
		lines = append(lines, st.Muted.Render(msg))
		return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
	}

	rows := make([][]string, 0, len(p.filteredFiles))
	marked := make(map[int]bool, p.fileSel.Count())
	for i, row := range p.filteredFiles {
		if p.fileSel.Has(row.RelPath) {
			marked[i] = true
		}
		locStr := "—"
		if row.Local.Exists {
			locStr = formatBytes(row.Local.SizeBytes) + " " + nonEmpty(row.Local.Mode, "0644")
		}
		remStr := "—"
		if row.Remote.Exists {
			remStr = formatBytes(row.Remote.SizeBytes) + " " + nonEmpty(row.Remote.Mode, "0644")
		}
		secStr := "cfg"
		if row.IsSecret {
			secStr = st.WarnText.Render("secret")
		}
		rows = append(rows, []string{
			fleetFileStatusBadge(row.Status, st),
			nonEmpty(row.Category, "files"),
			row.RelPath,
			locStr,
			remStr,
			secStr,
		})
	}
	footerH := 0
	tableH := max(4, innerH-len(lines)-footerH)
	lines = append(lines, renderSelectableTable([]string{"Status", "Cat", "Relative Path", "Local", "Remote", "Sec"}, rows, p.fileCursor, marked, focused, innerW, tableH, st)...)
	return renderTitledBox(title, p.Badge(), lines, width, height, focused, st)
}

func fleetFileStatusBadge(status string, st Styles) string {
	switch status {
	case "same":
		return st.OKText.Render("= same")
	case "local-newer":
		return st.InfoText.Render("→ local>")
	case "remote-newer":
		return st.WarnText.Render("← rem>")
	case "modified":
		return st.WarnText.Render("≠ diff")
	case "local-only":
		return st.AccentText.Render("+ local")
	case "remote-only":
		return st.AccentText.Render("+ remote")
	default:
		return status
	}
}

func (p fleetPanel) ViewPreview(width, height int, focused bool, borderless bool, st Styles) string {
	if p.subView == fleetSubFileSync {
		return p.viewFileSyncPreview(width, height, focused, borderless, st)
	}
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	n, ok := p.selectedNode()
	if !ok {
		return renderTitledBox("Node Detail", "", []string{st.Muted.Render("Select a fleet node.")}, width, height, focused, st)
	}

	var all []string
	stateText := st.WarnText.Render("OFFLINE")
	if n.Online {
		stateText = st.OKText.Render("ONLINE")
	}
	sshText := st.Muted.Render("SSH OFF")
	if n.SSHEnabled {
		sshText = st.OKText.Render("TAILSCALE SSH")
	}
	pairs := [][2]string{
		{"Status", stateText + " · " + sshText},
		{"Tailscale", nonEmpty(n.TailscaleIP, "—") + " · " + nonEmpty(n.DNSName, "—")},
		{"Platform", strings.Trim(nonEmpty(n.Provider, "node")+" · "+n.OS+"/"+n.Arch, " ·/")},
		{"Preset / Sync", nonEmpty(n.Preset, "—") + " · last sync " + nonEmpty(n.LastSynced, "never")},
		{"Tags", nonEmpty(strings.Join(n.Tags, ", "), "none")},
	}
	all = append(all, renderKVTable(pairs, innerW, st)...)
	all = append(all, "", st.SectionHeader.Render("Tailscale Join Command"))
	switch {
	case p.joinNode != "" && p.joinNode != n.Name:
		all = append(all, st.Muted.Render("Loading join command for "+n.Name+"..."))
	case p.joinErr != "":
		all = append(all, st.ErrText.Render(truncate(p.joinErr, innerW)))
	case p.joinCmd != "":
		all = append(all, wrapPlainTextLines(p.joinCmd, innerW, st.InfoText)...)
	}

	all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Sync Plan · no-zones=%v · push-env=%v", p.noZones, p.pushEnv)))
	if p.previewNode != "" && p.previewNode != n.Name {
		all = append(all, st.Muted.Render("Loading sync preview for "+n.Name+"..."))
	} else {
		if p.previewErr != "" {
			all = append(all, st.ErrText.Render(truncate(p.previewErr, innerW)))
		} else if p.preview.Summary != "" {
			all = append(all, st.Muted.Render(truncate(p.preview.Summary, innerW)))
		}
		for i, step := range p.preview.Steps {
			all = append(all, truncate(fmt.Sprintf("  %d. %s", i+1, step), innerW))
		}
		if len(p.preview.SkippedConfigs) > 0 {
			all = append(all, st.WarnText.Render(truncate("  Skipped macOS: "+strings.Join(p.preview.SkippedConfigs, ", "), innerW)))
		}
	}

	checklist := p.activeChecklist(n)
	all = append(all, "", st.SectionHeader.Render(fmt.Sprintf("Post-Sync Checklist (%d)", len(checklist))))
	if len(checklist) == 0 {
		all = append(all, st.OKText.Render("  All post-sync steps complete."))
	} else {
		chkRows := make([][]string, 0, len(checklist))
		for _, item := range checklist {
			chk := "○"
			if item.Done {
				chk = "✓"
			}
			chkRows = append(chkRows, []string{chk, item.Title, nonEmpty(item.Command, "—")})
		}
		all = append(all, renderDataTable([]string{"Done", "Step", "Command"}, chkRows, p.chkCursor, focused, innerW, max(4, len(chkRows)+2), st)...)
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("Node · "+n.Name, "", vis, width, height, focused, st)
}

func (p fleetPanel) viewFileSyncPreview(width, height int, focused bool, borderless bool, st Styles) string {
	innerW := width - 4
	innerH := height - 2
	if borderless {
		innerW = width
		innerH = height
	}
	innerW = max(12, innerW)
	innerH = max(1, innerH)

	row, ok := p.selectedDiffFile()
	if !ok {
		msg := []string{st.Muted.Render("Select a file to inspect metadata, SHA-256 digests, and safe config diff.")}
		if borderless {
			return fitLines(strings.Join(msg, "\n"), height)
		}
		return renderTitledBox("File Diff Preview", "", msg, width, height, focused, st)
	}

	peerName := nonEmpty(p.fileDiff.Node, "peer")
	formatMeta := func(m FleetFileMeta) string {
		if !m.Exists {
			return "missing"
		}
		sha := m.SHA256
		if len(sha) > 12 {
			sha = sha[:12]
		}
		mt := "—"
		if m.ModTime > 0 {
			mt = time.Unix(m.ModTime, 0).Local().Format("2006-01-02 15:04")
		}
		return fmt.Sprintf("%s · %s · %s · %s", formatBytes(m.SizeBytes), nonEmpty(m.Mode, "0644"), sha, mt)
	}

	secLabel := "Config (text diff allowed)"
	if row.IsSecret {
		secLabel = "Sensitive (text diff blocked / mode 0600 enforced)"
	}

	var all []string
	all = append(all, fleetFileStatusBadge(row.Status, st)+" "+st.AccentText.Render(truncate(row.RelPath, max(6, innerW-12))))
	all = append(all, "")
	all = append(all, renderKVTable([][2]string{
		{"Scope / Root", row.Category + " (" + nonEmpty(row.Root, "~") + ")"},
		{"Security", secLabel},
		{"Local (~/)", formatMeta(row.Local)},
		{"Remote (" + peerName + ")", formatMeta(row.Remote)},
	}, innerW, st)...)
	all = append(all, "")
	all = append(all, st.SectionHeader.Render("Content Diff Preview"))

	switch {
	case row.IsSecret && (p.fileTextPath != row.RelPath || p.fileTextNode != peerName):
		all = append(all, wrapPlainTextLines("Sensitive file (key, secret, .env, or encrypted snapshot): cleartext diff disabled. Sync applies 0600 permissions and .pre-ws-sync backups.", innerW, st.WarnText)...)
	case p.fileTextLoading && p.fileTextPath == row.RelPath:
		all = append(all, st.InfoText.Render("Fetching safe diff over SSH..."))
	case p.fileTextErr != "" && p.fileTextPath == row.RelPath:
		all = append(all, st.ErrText.Render(truncate(p.fileTextErr, innerW)))
	case p.fileTextPath == row.RelPath && p.fileTextNode == peerName && p.fileTextDiff != "":
		for _, dl := range strings.Split(p.fileTextDiff, "\n") {
			t := truncate(dl, innerW)
			switch {
			case strings.HasPrefix(dl, "+") && !strings.HasPrefix(dl, "+++"):
				all = append(all, st.DiffAdd.Render(t))
			case strings.HasPrefix(dl, "-") && !strings.HasPrefix(dl, "---"):
				all = append(all, st.DiffDel.Render(t))
			case strings.HasPrefix(dl, "@@") || strings.HasPrefix(dl, "---") || strings.HasPrefix(dl, "+++"):
				all = append(all, st.DiffHeader.Render(t))
			case strings.HasPrefix(dl, "Redacted:"):
				all = append(all, st.WarnText.Render(t))
			default:
				all = append(all, st.Muted.Render(t))
			}
		}
	default:
		all = append(all, st.Muted.Render("Unified line diff not loaded (non-secret configs only)."))
	}

	maxScroll := max(0, len(all)-innerH)
	sc := clamp(p.previewScroll, 0, maxScroll)
	vis := all[sc:min(len(all), sc+innerH)]
	if borderless {
		return fitLines(strings.Join(vis, "\n"), height)
	}
	return renderTitledBox("File · "+truncate(row.RelPath, 26), "", vis, width, height, focused, st)
}

func wrapPlainTextLines(s string, width int, style lipgloss.Style) []string {
	lines := wrapPlainText(s, max(1, width))
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if ln != "" {
			out = append(out, style.Render(ln))
		}
	}
	return out
}

func (p fleetPanel) ViewModal(width, height int, st Styles) (string, bool) {
	if p.fileFilters.Open {
		groups, sorts := p.fileSyncFacetGroups()
		return p.fileFilters.ViewModal("Fleet File Diff", groups, sorts, width, height, st)
	}

	if p.syncOpen {
		n, _ := p.selectedNode()
		body := fmt.Sprintf("Node: %s (%s/%s)\nOptions: --no-zones=%v  --push-env=%v\nEnv files to deliver: %d\nSteps: %d planned step(s)",
			n.Name, n.OS, n.Arch, p.noZones, p.pushEnv, p.preview.EnvFilesCount, len(p.preview.Steps))
		warn := ""
		if p.pushEnv && p.preview.EnvFilesCount > 0 {
			warn = "Encrypted .env files will be streamed over stdin (0600) without 1Password on the VM."
		}
		return renderConfirmModal("Confirm Fleet Sync & Adjust", body, warn, "Sync Node", false, p.syncConfirmFocus, width, height, st), true
	}

	return "", false
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '/', r == '@', r == ':', r == '+', r == '-', r == '=', r == ',', r == '~':
		default:
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func joinShellArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, shellQuoteArg(a))
	}
	return strings.Join(quoted, " ")
}

func buildFleetSyncFilesCLI(node, direction string, relPaths []string, execute bool) string {
	flag := "--push"
	if direction == "pull" {
		flag = "--pull"
	}
	parts := []string{"ws", "fleet", "sync-files", shellQuoteArg(node), flag}
	hasPaths := false
	for _, p := range relPaths {
		if s := strings.TrimSpace(p); s != "" {
			parts = append(parts, shellQuoteArg(s))
			hasPaths = true
		}
	}
	if !hasPaths {
		parts = append(parts, "--all-diff")
	}
	if execute {
		parts = append(parts, "--execute")
	}
	return strings.Join(parts, " ")
}

func loadFleetNodesCmd(backend Backend) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetNodesLoadedMsg{err: fmt.Errorf("backend unavailable")}
		}
		nodes, err := backend.LoadFleetNodes()
		return fleetNodesLoadedMsg{nodes: nodes, err: err}
	}
}

func tailscaleJoinCmd(backend Backend, hostname string, tags []string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetJoinCmdLoadedMsg{node: hostname, err: fmt.Errorf("backend unavailable")}
		}
		cmd, err := backend.TailscaleJoinCmd(hostname, tags)
		return fleetJoinCmdLoadedMsg{node: hostname, cmd: cmd, err: err}
	}
}

func previewFleetSyncCmd(backend Backend, req FleetSyncReq, node string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetSyncPreviewMsg{node: node, err: fmt.Errorf("backend unavailable")}
		}
		prev, err := backend.PreviewFleetSync(req)
		return fleetSyncPreviewMsg{node: node, preview: prev, err: err}
	}
}

func execFleetSyncCmd(backend Backend, req FleetSyncReq) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetSyncedMsg{node: req.Node, err: fmt.Errorf("backend unavailable")}
		}
		summary, err := backend.ExecFleetSync(req)
		return fleetSyncedMsg{node: req.Node, summary: summary, err: err}
	}
}

func diffFleetFilesCmd(backend Backend, node, targetID string, includeEnv, includeDB bool) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetFileDiffLoadedMsg{node: node, err: fmt.Errorf("backend unavailable")}
		}
		view, err := backend.DiffFleetFiles(node, targetID, includeEnv, includeDB)
		return fleetFileDiffLoadedMsg{node: node, view: view, err: err}
	}
}

func previewFleetFileDiffCmd(backend Backend, node, targetID, relPath string) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetFileTextDiffMsg{node: node, relPath: relPath, err: fmt.Errorf("backend unavailable")}
		}
		diff, err := backend.PreviewFleetFileDiff(node, targetID, relPath)
		return fleetFileTextDiffMsg{node: node, relPath: relPath, diff: diff, err: err}
	}
}

func syncFleetFilesCmd(backend Backend, req FleetFileSyncReq) tea.Cmd {
	return func() tea.Msg {
		if backend == nil {
			return fleetFileSyncedMsg{node: req.Node, dir: req.Direction, err: fmt.Errorf("backend unavailable")}
		}
		res, err := backend.SyncFleetFiles(req)
		outCmd := buildFleetSyncFilesCLI(req.Node, req.Direction, req.RelPaths, true)
		return fleetFileSyncedMsg{
			node: req.Node,
			dir:  req.Direction,
			res:  res,
			result: CommandRunResult{
				Command: outCmd,
				Output:  strings.Join(append([]string{res.Summary}, res.Lines...), "\n"),
				IsErr:   err != nil,
			},
			err: err,
		}
	}
}
