package tui

import (
	"os/exec"
	"time"

	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

type AttentionLevel int

const (
	AttentionNone AttentionLevel = iota
	AttentionInfo
	AttentionWarn
	AttentionErr
)

type EnvFileRow struct {
	Path        string
	SourceKind  string
	Zone        string
	VarCount    int
	SecretCount int
	Missing     bool
	Ignored     bool
}

type HostBadge struct {
	OS           string
	Distro       string
	Arch         string
	Preset       string
	Zone         string
	Theme        string
	Headless     bool
	PkgManagers  []string
	WorkDir      string
	CWDRepoPath  string
	CWDRepoSlug  string
	CWDBranch    string
	CWDIssueHint int
	GHConfigDir  string
}

type DoctorFinding struct {
	ID       string
	Severity string
	Category string
	Title    string
	Detail   string
	FixCLI   string
	FixPlan  *engine.Plan
}

type DoctorSummary struct {
	Host             string
	Preset           string
	Theme            string
	LinksOK          int
	LinksTotal       int
	TemplatesOK      int
	TemplatesTotal   int
	PackagesManaged  int
	PackagesOutdated int
	ZonesOK          int
	ZonesTotal       int
	ReclaimableBytes int64
	Findings         []DoctorFinding
}

type PresetSummary struct {
	ID           string
	Description  string
	Include      []string
	Exclude      []string
	Secrets      string
	OSHooks      []string
	Tags         []string
	LinksCount   int
	PackageCount int
	MatchesHost  bool
	Active       bool
}

type PresetToolRow struct {
	ID             string
	Bin            string
	Provider       string
	PackageName    string
	Updates        string
	Source         string
	Status         string
	CurrentVersion string
	LatestVersion  string
	Outdated       bool
	Conflicts      []string
	Description    string
	OriginPreset   string
	OriginFile     string
	AllProviders   []string
	InstallCmd     string
	UpdateCmd      string
	RemoveCmd      string
}

type PackageOutdatedInfo struct {
	Provider string
	Name     string
	Current  string
	Latest   string
	Risk     string
}

type PackageDetail struct {
	ID            string
	PackageName   string
	Provider      string
	Repo          string
	Version       string
	LatestVersion string
	Installed     bool
	Homepage      string
	License       string
	InstalledSize string
	Description   string
	OriginPreset  string
	OriginFile    string
	Dependencies  []string
	OptionalDeps  []string
	RequiredBy    []string
	Conflicts     []string
	Artifacts     []string
	Caveats       string
	RawInfo       string
}

type PackageSearchRow struct {
	Name          string
	Provider      string
	Repo          string
	Version       string
	Description   string
	Installed     bool
	Tracked       bool
	SuggestedFile string
	InstallCmd    string
	RemoveCmd     string
}

type CommandRunResult struct {
	Command  string
	ExitCode int
	Duration time.Duration
	Output   string
	IsErr    bool
}

type TemplateStatusRow struct {
	App      string
	Template string
	Output   string
	PostHook string
	Status   string
	Diff     string
	Hash     string
}

type CleanItem struct {
	ID                string
	Tier              string
	Category          string
	Title             string
	Paths             []string
	Bytes             int64
	Warning           string
	SelectedByDefault bool
	NeedsSudo         bool
}

type HistoryRow struct {
	Timestamp  time.Time
	DurationMs int64
	ExitCode   int
	Shell      string
	Zone       string
	Command    string
}

type EnvVarRow struct {
	VarKey           string
	Zone             string
	ActiveSourceKind string
	ActiveSourcePath string
	ActiveMasked     string
	Fingerprint      string
	IsSecret         bool
	SourceCount      int
	ShadowedCount    int
}

type EnvLayerRow struct {
	Status      string
	VarKey      string
	Zone        string
	SourceKind  string
	SourcePath  string
	LineNum     int
	MaskedValue string
	Fingerprint string
	IsSecret    bool
}

type EnvDetail struct {
	VarKey       string
	ActiveMasked string
	Fingerprint  string
	IsSecret     bool
	Layers       []EnvLayerRow
	BlastRadius  []EnvLayerRow
}

type EnvRotateReq struct {
	VarKey           string
	MatchFingerprint string
	Zone             string
	Paths            []string
	NewValue         string
}

type EnvRotateTargetRow struct {
	SourcePath  string
	SourceKind  string
	Zone        string
	LineNum     int
	OldMasked   string
	NewMasked   string
	Fingerprint string
}

type EnvRotatePreview struct {
	VarKey    string
	NewMasked string
	Targets   []EnvRotateTargetRow
}

type EnvRotateResult struct {
	TxID         string
	UpdatedCount int
	Summary      string
}

type LocalManifestEntry struct {
	RelPath   string
	SizeBytes int64
	BackedUp  bool
}

type RepoRow struct {
	Name      string
	Zone      string
	Forge     string
	RemoteURL string
	LocalPath string
	Branch    string
	Dirty     bool
	Ahead     int
	Behind    int
	Locals    []LocalManifestEntry
}

type GHItemRow struct {
	Kind          string
	Repo          string
	Number        int
	Title         string
	State         string
	Author        string
	URL           string
	Zone          string
	Body          string
	Labels        []string
	Assignees     []string
	Milestone     string
	IsDraft       bool
	CommentsCount int
	HeadRef       string
	UpdatedAt     string
	FetchedAt     int64
	Source        string
}

type GHCommentRow struct {
	Author    string
	Body      string
	CreatedAt string
}

type GHItemDetail struct {
	GHItemRow
	HeadRef        string
	BaseRef        string
	ReviewDecision string
	Mergeable      string
	ChecksSummary  string
	Additions      int
	Deletions      int
	ChangedFiles   int
	Comments       []GHCommentRow
}

type RepoPullResult struct {
	Summary          string
	Updated          int
	UpToDate         int
	Dirty            int
	Failed           int
	NeedsInteractive []RepoRow
	Lines            []string
}

type FleetChecklistItem struct {
	Done    bool
	Title   string
	Command string
}

type FleetNodeRow struct {
	Name        string
	DNSName     string
	TailscaleIP string
	OS          string
	Arch        string
	Provider    string
	TargetID    string
	Preset      string
	Online      bool
	SSHEnabled  bool
	IsSelf      bool
	Tags        []string
	NoZones     bool
	PushEnv     bool
	Checklist   []FleetChecklistItem
	LastSynced  string
}

type FleetFileMeta struct {
	Exists    bool
	SHA256    string
	SizeBytes int64
	ModTime   int64
	Mode      string
}

type FleetFileDiffRow struct {
	RelPath  string
	Root     string
	Category string
	Status   string
	IsSecret bool
	Local    FleetFileMeta
	Remote   FleetFileMeta
}

type FleetFileDiffView struct {
	Node            string
	TargetID        string
	IncludeEnv      bool
	IncludeDB       bool
	Roots           []string
	Entries         []FleetFileDiffRow
	SameCount       int
	DiffCount       int
	LocalOnlyCount  int
	RemoteOnlyCount int
	Warnings        []string
}

type FleetFileSyncReq struct {
	Node      string
	TargetID  string
	Direction string
	RelPaths  []string
	DryRun    bool
}

type FleetFileSyncResult struct {
	Node        string
	Direction   string
	Transferred int
	Skipped     int
	Failed      int
	Summary     string
	Lines       []string
}

type FleetSyncReq struct {
	Node          string
	Transport     string
	TargetID      string
	Preset        string
	Arch          string
	NoZones       bool
	PushEnvFiles  bool
	SelectedRepos []string
	DryRun        bool
}

type FleetSyncPreview struct {
	Summary        string
	SkippedConfigs []string
	EnvFilesCount  int
	Steps          []string
	Checklist      []FleetChecklistItem
}

type DBStatusSummary struct {
	Path             string
	SchemaVersion    int
	KeySource        string
	HasKey           bool
	DirtySinceBackup bool
	LastBackupAt     string
	CurrentHash      string
	EnvCount         int
	BlobCount        int
	RepoCount        int
}

type DBBackupResult struct {
	Changed     bool
	Skipped     bool
	LocalPath   string
	OnePassword bool
	Summary     string
}

type VaultItemRow struct {
	ID          string
	Scope       string
	Label       string
	LocalPath   string
	TargetArg   string
	DocTitle    string
	Category    string
	ExistsLocal bool
	IsDir       bool
	FileCount   int
	SizeBytes   int64
	ModTime     int64
	Note        string
}

type VaultInventoryView struct {
	HasOPCLI bool
	OPVault  string
	Items    []VaultItemRow
}

type MiseToolDecl struct {
	Name      string
	Requested string
	Backend   string
	Line      int
}

type MiseConfigRow struct {
	Path          string
	Kind          string
	Scope         string
	Zone          string
	ProjectName   string
	ProjectPath   string
	Forge         string
	GitHubSlug    string
	RemoteURL     string
	Branch        string
	Dirty         bool
	LocalOverride bool
	Tools         []MiseToolDecl
	EnvKeys       []string
	Tasks         []string
	OutdatedCount int
	MissingCount  int
}

type MiseToolRow struct {
	Name        string
	Requested   string
	Version     string
	Latest      string
	InstallPath string
	SourcePath  string
	Scope       string
	Zone        string
	ProjectName string
	ProjectPath string
	Forge       string
	GitHubSlug  string
	Installed   bool
	Active      bool
	Outdated    bool
	Status      string
}

type MiseInventoryView struct {
	MiseVersion   string
	OutdatedKnown bool
	OutdatedCount int
	MissingCount  int
	Configs       []MiseConfigRow
	Tools         []MiseToolRow
	Warnings      []string
}

type UpdatePlanPreview struct {
	Summary      string
	RepoDirty    bool
	SnapshotHint string
	PendingCount int
	Warnings     []string
	Steps        []string
}

type Backend interface {
	HostSummary() (HostBadge, error)

	ScanEnv() error
	LoadEnvVars(query, zone string) ([]EnvVarRow, error)
	LoadEnvFiles(zone string) ([]EnvFileRow, error)
	LoadEnvDetail(varKey, targetDir string) (EnvDetail, error)
	RevealEnvValue(sourcePath, varKey string) (string, error)
	PreviewEnvRotate(req EnvRotateReq) (EnvRotatePreview, error)
	ExecEnvRotate(req EnvRotateReq) (EnvRotateResult, error)

	LoadRepos(refresh bool, zone string) ([]RepoRow, error)
	LoadGHItems(refresh bool, zone string) ([]GHItemRow, error)
	LoadCachedRepoGHItems(repo, state string) ([]GHItemRow, int64, error)
	LoadRepoGHItems(repo, zone, state string, refresh bool) ([]GHItemRow, error)
	LoadGHItemDetail(kind, repo, zone string, number int, refresh bool) (GHItemDetail, error)
	PullRepos() (RepoPullResult, error)
	PullSelectedRepos(paths []string) (RepoPullResult, error)
	InteractivePullCmd(repo RepoRow) (*exec.Cmd, error)
	CloneRepo(source, zone string) (string, error)
	ApproveGHPR(repo, zone string, number int, body string) (string, error)
	MergeGHPR(repo, zone string, number int, method string) (string, error)
	SetGHItemState(kind, repo, zone string, number int, closeItem bool) (string, error)
	CommentGHItem(kind, repo, zone string, number int, body string) (string, error)
	CheckoutGHPR(repoDir, repo, zone string, number int) (string, error)

	LoadMiseInventory(zone string, checkOutdated, force bool) (MiseInventoryView, error)
	EditMiseFileCmd(path string) (*exec.Cmd, error)
	UpgradeMiseTools(tools []MiseToolRow) (CommandRunResult, error)

	LoadFleetNodes() ([]FleetNodeRow, error)
	TailscaleJoinCmd(hostname string, tags []string) (string, error)
	PreviewFleetSync(req FleetSyncReq) (FleetSyncPreview, error)
	ExecFleetSync(req FleetSyncReq) (string, error)
	FleetConnectCmd(node FleetNodeRow) (*exec.Cmd, error)
	DiffFleetFiles(node, targetID string, includeEnv, includeDB bool) (FleetFileDiffView, error)
	PreviewFleetFileDiff(node, targetID, relPath string) (string, error)
	SyncFleetFiles(req FleetFileSyncReq) (FleetFileSyncResult, error)

	ListPresets() ([]PresetSummary, error)
	ResolvePreset(id string) ([]PresetToolRow, error)
	LoadPackageOutdated() (map[string]PackageOutdatedInfo, error)
	LoadUntrackedPackages(presetID string) ([]PresetToolRow, error)
	SearchPackages(query, provider string, limit int) ([]PackageSearchRow, error)
	LoadPackageDetail(pkg PresetToolRow) (PackageDetail, error)
	PackageActionExecCmd(action string, pkgs []PresetToolRow) (*exec.Cmd, error)
	ExecPackageAction(action string, pkgs []PresetToolRow) (CommandRunResult, error)
	TrackPackages(relFile string, pkgs []PresetToolRow) (string, error)
	AddAndTrackPackageExecCmd(name, provider, relFile string, install bool) (*exec.Cmd, error)
	AddAndTrackPackage(name, provider, relFile string, install bool) (CommandRunResult, error)
	UntrackPackages(pkgs []PresetToolRow) (string, error)
	PreviewUpdateAll(all bool) (UpdatePlanPreview, error)
	UpdateAllExecCmd(all bool) (*exec.Cmd, error)
	ExecUpdateAll(all bool) (CommandRunResult, error)
	ListThemes() ([]string, error)
	LoadPalette(id string) (config.Palette, error)
	ActiveTheme() (string, error)
	ApplyTheme(id string) (*engine.ApplyResult, error)
	ApplyThemeForApp(themeID, app string) (*engine.ApplyResult, error)
	TemplateStatuses() ([]TemplateStatusRow, error)
	EditAppCmd(app string) (*exec.Cmd, error)
	FinishAppEdit(app string) (*engine.ApplyResult, error)
	ScanCleanup() ([]CleanItem, error)
	CleanupExecCmd(ids []string) (*exec.Cmd, error)
	RunCleanup(ids []string, dryRun bool) (*engine.ApplyResult, error)
	LoadHistory(query string, limit int) ([]HistoryRow, string, error)

	LoadDoctor() (DoctorSummary, error)
	ApplyPlan(plan *engine.Plan) (*engine.ApplyResult, error)
	LoadDBStatus() (DBStatusSummary, error)
	BackupDB(ifChanged bool) (DBBackupResult, error)
	UndoLast() (string, error)
	LoadVaultInventory() (VaultInventoryView, error)
	RunVaultAction(action string, items []VaultItemRow) (CommandRunResult, error)

	RunWSCommand(args []string) (CommandRunResult, error)

	LoadSettings() (SettingsView, error)
	SetSetting(key, value string) (SettingsView, error)
	ResetSetting(key string) (SettingsView, error)
	CheckConfigWatch(prevFingerprint string) (ConfigWatchResult, error)
	ReloadConfig() (SettingsView, []string, error)
	EditConfigFileCmd(path string) (*exec.Cmd, error)
}

type SettingRow struct {
	Key          string
	Category     string
	Kind         string
	Value        string
	DefaultValue string
	Source       string
	Options      []string
	Description  string
	Overridden   bool
	ReadOnly     bool
}

type WatchedFileRow struct {
	Label       string
	Path        string
	Exists      bool
	SizeBytes   int64
	ModTime     int64
	Fingerprint string
}

type SettingsView struct {
	ConfigPath      string
	Mouse           *bool
	AutoReload      bool
	AutoReloadSecs  int
	GHRefreshMins   int
	DefaultSplit    string
	BorderedBar     string
	Theme           string
	DefaultZone     string
	StartTab        string
	FleetIncludeEnv bool
	FleetIncludeDB  bool
	Fingerprint     string
	ConfigErr       string
	MiseLocalErr    string
	Items           []SettingRow
	Watched         []WatchedFileRow
}

type ConfigWatchResult struct {
	Changed      bool
	Fingerprint  string
	ChangedFiles []string
}
