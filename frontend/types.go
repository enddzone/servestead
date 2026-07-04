package frontend

type ShellData struct {
	Title     string
	CSRFToken string
	Notice    string
	Error     string
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
