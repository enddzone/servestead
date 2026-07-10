package frontend

type ShellData struct {
	Title         string
	Heading       string
	ActiveSection string
	ActiveProfile string
	CSRFToken     string
	Profiles      []ProfileOption
	Notice        string
	Error         string
}

type ProfileOption struct {
	ID     string
	Name   string
	IP     string
	Status string
}

type StartData struct {
	CSRFToken string
	Profiles  []ProfileOption
	Notice    string
	Error     string
}

type HomeData struct {
	CSRFToken          string
	Profiles           []ProfileOption
	HasProfile         bool
	Greeting           string
	NodeName           string
	SelectedProfileID  string
	SelectedProfile    string
	SelectedAddress    string
	SelectedDomain     string
	SelectedRepository string
	Region             string
	Uptime             string
	HealthStatus       string
	HealthDetail       string
	SetupProgressLabel string
	SetupProgress      int
	ActiveRunStatus    string
	SetupStatus        string
	GitState           string
	CloudState         string
	NextAction         string
	UpdatedAt          string
	Issues             []HomeIssue
	Activities         []HomeActivity
	Commands           []CommandItem
	Notice             string
	Error              string
}

type HomeIssue struct {
	Tone        string
	Title       string
	Detail      string
	ActionLabel string
	URL         string
}

type HomeActivity struct {
	Tone   string
	Title  string
	Detail string
	Time   string
	URL    string
}

type CommandItem struct {
	Label    string
	Detail   string
	URL      string
	Tone     string
	Disabled bool
}

type ProfileFormData struct {
	CSRFToken           string
	DraftID             string
	Intent              string
	Target              string
	ProfileID           string
	Name                string
	IP                  string
	PrivateKeyPath      string
	BaseDomain          string
	LetsEncryptEmail    string
	InitialSSHUser      string
	AdminUser           string
	PangolinAdminEmail  string
	PangolinAdminStatus string
	ConfigRepository    string
	GitHubRepositoryURL string
	Errors              []string
}

type RepositoryFormData struct {
	ProfileFormData
	RepositoryMode string
}

type ReviewData struct {
	ProfileFormData
	RepositoryMode string
	Plan           string
	RepositoryLine string
	RunLine        string
}

type RunData struct {
	CSRFToken string
	ProfileID string
	RunID     string
	Target    string
	Status    string
	StreamURL string
}

type RecoveryData struct {
	CSRFToken string
	ProfileID string
	RunID     string
	Kind      string
	Message   string
	CanRetry  bool
}

type OpsProfilesData struct {
	CSRFToken     string
	Rows          []OpsProfileRow
	Selected      string
	SelectedPanel OpsProfileDrawerData
	HasSelected   bool
	Notice        string
	Error         string
}

type OpsProfileRow struct {
	ID              string
	Name            string
	IP              string
	BaseDomain      string
	ActiveRunStatus string
	SetupStatus     string
	GitState        string
	CloudState      string
	NextAction      string
	UpdatedAt       string
}

type OpsProfileDrawerData struct {
	CSRFToken        string
	ProfileID        string
	Name             string
	IP               string
	BaseDomain       string
	LetsEncryptEmail string
	RepositoryPath   string
	ActiveRunStatus  string
	SetupStatus      string
	GitState         string
	CloudState       string
	NextAction       string
	UpdatedAt        string
	RecentRuns       []OpsRunRow
	Notice           string
	Error            string
}

type OpsDiagnosticsDrawerData struct {
	CSRFToken string
	ProfileID string
	Name      string
	Runs      []OpsRunRow
	GitOps    OpsGitOpsData
	Cloud     OpsCloudData
	Notice    string
	Error     string
}

type OpsStacksData struct {
	CSRFToken      string
	ProfileID      string
	RepositoryPath string
	Rows           []OpsStackRow
	Notice         string
	Error          string
}

type OpsStackRow struct {
	Name                string
	PublicResourceCount int
	MetadataStatus      string
	GitState            string
	Eligible            bool
}

type OpsStackEditorData struct {
	CSRFToken      string
	ProfileID      string
	RepositoryPath string
	OriginalName   string
	Name           string
	Compose        string
	Resources      []OpsStackResourceData
	Notice         string
	Error          string
}

type OpsStackResourceData struct {
	ID         string
	Service    string
	Subdomain  string
	Name       string
	Port       string
	Protocol   string
	SSO        bool
	HealthPath string
}

type OpsGitOpsData struct {
	CSRFToken      string
	ProfileID      string
	RepositoryPath string
	Diff           string
	Status         string
	State          string
	Head           string
	NeedsPush      bool
	NextAction     string
	Notice         string
	Error          string
}

type OpsGitOpsReviewData struct {
	CSRFToken      string
	ProfileID      string
	ProfileName    string
	BaseDomain     string
	RepositoryPath string
	Status         string
	State          string
	Head           string
	NeedsPush      bool
	NextAction     string
	Commits        []OpsCommitRow
	Runs           []OpsRunRow
	Diff           string
	Notice         string
	Error          string
}

type OpsCommitRow struct {
	Hash    string
	Message string
	When    string
}

type OpsRunsData struct {
	CSRFToken string
	ProfileID string
	Query     string
	Rows      []OpsRunRow
	Notice    string
	Error     string
}

type OpsRunRow struct {
	ID        string
	Status    string
	Stages    string
	Error     string
	CreatedAt string
	UpdatedAt string
}

type OpsRunDetailData struct {
	CSRFToken string
	ProfileID string
	RunID     string
	Status    string
	LogLines  []string
	Notice    string
	Error     string
}

type OpsAccessData struct {
	CSRFToken      string
	ProfileID      string
	GitHubStatus   string
	PangolinEmail  string
	PangolinStatus string
	RevealName     string
	RevealValue    string
	Notice         string
	Error          string
}

type OpsCloudData struct {
	CSRFToken  string
	ProfileID  string
	Provider   string
	ResourceID string
	Name       string
	Region     string
	Size       string
	Image      string
	IP         string
	Destroyed  bool
	Notice     string
	Error      string
}
