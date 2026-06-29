package resources

import "embed"

// FS contains deployment files and shell templates rendered by the main package.
//
//go:embed bootstrap hardening network observability proxy stacks
var FS embed.FS

const (
	BootstrapSudoersScript = "bootstrap/scripts/write-sudoers.sh.tmpl"

	HardeningAutoUpgradesConfig  = "hardening/auto-upgrades.conf"
	HardeningConfigureSwapScript = "hardening/scripts/configure-swap.sh"
	HardeningSSHConfig           = "hardening/sshd-hardening.conf"
	HardeningSSHReloadScript     = "hardening/scripts/reload-ssh.sh"
	HardeningSupportedUbuntu     = "hardening/scripts/supported-ubuntu.sh"
	HardeningSysctlConfig        = "hardening/sysctl.conf.tmpl"
	HardeningSystemUpgradeScript = "hardening/scripts/system-upgrade.sh.tmpl"

	NetworkDockerDaemonConfig            = "network/docker-daemon.json"
	NetworkDockerRepositoryScript        = "network/scripts/docker-repository.sh.tmpl"
	NetworkRemoveConflictingDockerScript = "network/scripts/remove-conflicting-docker-packages.sh.tmpl"
	NetworkUFWMasqueradeScript           = "network/scripts/ufw-masquerade.sh.tmpl"

	ObservabilityBeszelConfig              = "observability/beszel-config.yml.tmpl"
	ObservabilityCompose                   = "observability/docker-compose.yml.tmpl"
	ObservabilityEnvironment               = "observability/observability.env.tmpl"
	ObservabilityPangolinLoginScript       = "observability/scripts/pangolin-login.sh.tmpl"
	ObservabilityResourceReconcileScript   = "observability/scripts/reconcile-resources.sh.tmpl"
	ObservabilityResourceVerifyScript      = "observability/scripts/verify-resources.sh.tmpl"
	ObservabilityStackResourceVerifyScript = "observability/scripts/verify-stack-public-resources.sh.tmpl"

	ProxyBootstrapPangolinScript = "proxy/scripts/bootstrap-pangolin.sh.tmpl"
	ProxyCompose                 = "proxy/docker-compose.yml.tmpl"
	ProxyPangolinConfig          = "proxy/pangolin-config.yml.tmpl"
	ProxyTraefikDynamicConfig    = "proxy/traefik-dynamic.yml.tmpl"
	ProxyTraefikStaticConfig     = "proxy/traefik-static.yml.tmpl"

	StackPangolinOverride = "stacks/pangolin-override.yml.tmpl"
)
