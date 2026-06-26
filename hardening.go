package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type hardeningConfig struct {
	Host           string
	SSHUser        string
	PrivateKeyPath string
}

type hardeningRemoteClientFactory func(context.Context, hardeningConfig, io.Writer, io.Writer) (remoteClient, error)

var newHardeningRemoteClient hardeningRemoteClientFactory = func(ctx context.Context, config hardeningConfig, stdout, stderr io.Writer) (remoteClient, error) {
	return newSSHRemoteClient(ctx, config.Host, config.SSHUser, config.PrivateKeyPath, stdout, stderr)
}

func runHarden(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("harden", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := hardeningConfig{}
	flags.StringVar(&config.Host, "host", "", "target VPS IPv4 address or hostname")
	flags.StringVar(&config.SSHUser, "ssh-user", "aegisadmin", "administrative SSH user")
	flags.StringVar(&config.PrivateKeyPath, "private-key", "", "path to the administrative private key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if config.Host == "" || config.PrivateKeyPath == "" {
		return errors.New("--host and --private-key are required")
	}
	if !linuxUsername.MatchString(config.SSHUser) {
		return errors.New("--ssh-user must be a valid Linux username")
	}
	if _, err := os.Stat(config.PrivateKeyPath); err != nil {
		return fmt.Errorf("access private key: %w", err)
	}

	client, err := newHardeningRemoteClient(ctx, config, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(stdout, "hardening %s as %s...\n", config.Host, config.SSHUser)
	if err := runHardeningSteps(ctx, client, config, stdout); err != nil {
		return fmt.Errorf("hardening failed: %w", err)
	}
	fmt.Fprintf(stdout, "hardening complete: %s\n", config.Host)
	return nil
}

func runHardeningSteps(ctx context.Context, client remoteClient, config hardeningConfig, progress io.Writer) error {
	return runTasks(ctx, client, config.SSHUser, hardeningTasks(), progress)
}

func runHardeningStepsWithReporter(ctx context.Context, client remoteClient, config hardeningConfig, runID string, reporter TaskReporter, progress io.Writer) error {
	return runTasksWithReporter(ctx, client, config.SSHUser, runID, "harden", hardeningTasks(), progress, reporter)
}

func hardeningTasks() []Task {
	sysctlContent := strings.Join(sysctlConfigLines(), "\n") + "\n"
	return []Task{
		{Name: "Validate supported Ubuntu release", Apply: supportedUbuntuCommand()},
		{Name: "Validate sysctl keys", Apply: validateSysctlKeysCommand()},
		{Name: "Apply package upgrades", Apply: systemUpgradeCommand()},
		{Name: "Install hardening prerequisites", Apply: commandScript(
			aptInstallCommand("apt-transport-https", "ca-certificates", "curl", "gnupg", "iptables", "unattended-upgrades"),
		)},
		{Name: "Write sshd hardening config", Apply: remoteWriteFileCommand("/etc/ssh/sshd_config.d/99-aegisnode-hardening.conf", sshdHardeningConfig(), "root", "root", 0644)},
		{Name: "Validate and reload SSH", Apply: sshHardeningCommand()},
		{Name: "Write sysctl hardening config", Apply: remoteWriteFileCommand("/etc/sysctl.d/99-vps-hardening.conf", sysctlContent, "root", "root", 0644)},
		{Name: "Reload sysctl settings", Apply: commandScript("sysctl --system")},
		{Name: "Enable unattended upgrades", Apply: remoteWriteFileCommand("/etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n", "root", "root", 0644)},
		{Name: "Configure CrowdSec keyring", Apply: commandScript(
			"install -d -m 0755 -o root -g root /etc/apt/keyrings",
			"curl -fsSL https://packagecloud.io/crowdsec/crowdsec/gpgkey -o /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.key",
			"gpg --dearmor --yes --output /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.key",
			"chown root:root /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg",
			"chmod 0644 /etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg",
		)},
		{Name: "Configure CrowdSec repository", Apply: remoteWriteFileCommand("/etc/apt/sources.list.d/crowdsec_crowdsec.list", "deb [signed-by=/etc/apt/keyrings/crowdsec_crowdsec-archive-keyring.gpg] https://packagecloud.io/crowdsec/crowdsec/any any main\n", "root", "root", 0644)},
		{Name: "Install CrowdSec and firewall bouncer", Apply: commandScript(
			aptInstallCommand("crowdsec"),
			"systemctl enable --now crowdsec",
			"if iptables -V | grep -qi nf_tables; then bouncer_package=crowdsec-firewall-bouncer-nftables; else bouncer_package=crowdsec-firewall-bouncer-iptables; fi",
			noninteractiveAptGetCommand("install -y \"$bouncer_package\""),
			"systemctl enable --now crowdsec-firewall-bouncer",
			"cscli bouncers list",
		)},
	}
}

func systemUpgradeCommand() string {
	return commandScript(
		"export DEBIAN_FRONTEND=noninteractive",
		"export NEEDRESTART_MODE=a",
		aptGetCommand("update"),
		aptGetCommand("full-upgrade -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"),
		aptGetCommand("autoremove -y"),
		"if [ -f /var/run/reboot-required ]; then echo 'reboot required after package upgrades'; fi",
	)
}

func sshHardeningCommand() string {
	return commandScript(
		"passwd -l root >/dev/null 2>&1 || true",
		"install -d -m 0755 -o root -g root /run/sshd",
		"/usr/sbin/sshd -t",
		"systemctl reload-or-restart ssh || systemctl reload-or-restart sshd",
	)
}

func sshdHardeningConfig() string {
	return strings.Join([]string{
		"# Managed by AegisNode.",
		"PermitRootLogin no",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PubkeyAuthentication yes",
		"",
	}, "\n")
}

func supportedUbuntuCommand() string {
	return commandScript(
		". /etc/os-release",
		`test "$ID" = "ubuntu"`,
		`dpkg --compare-versions "$VERSION_ID" ge 22.04`,
		`kernel_version="$(uname -r | cut -d- -f1)"`,
		`dpkg --compare-versions "$kernel_version" ge 5.15`,
	)
}

func validateSysctlKeysCommand() string {
	lines := []string{}
	for _, setting := range sysctlSettings() {
		lines = append(lines, "sysctl -n "+shellQuote(setting.name)+" >/dev/null")
	}
	return commandScript(lines...)
}

type sysctlSetting struct {
	name  string
	value string
}

func sysctlSettings() []sysctlSetting {
	return []sysctlSetting{
		{name: "net.ipv4.conf.all.rp_filter", value: "1"},
		{name: "net.ipv4.conf.default.rp_filter", value: "1"},
		{name: "net.ipv4.conf.all.accept_source_route", value: "0"},
		{name: "net.ipv4.conf.default.accept_source_route", value: "0"},
		{name: "net.ipv4.conf.all.accept_redirects", value: "0"},
		{name: "net.ipv4.conf.default.accept_redirects", value: "0"},
		{name: "net.ipv4.conf.all.secure_redirects", value: "0"},
		{name: "net.ipv4.conf.default.secure_redirects", value: "0"},
		{name: "net.ipv4.conf.all.send_redirects", value: "0"},
		{name: "net.ipv4.conf.default.send_redirects", value: "0"},
		{name: "net.ipv4.tcp_syncookies", value: "1"},
		{name: "net.ipv4.tcp_synack_retries", value: "5"},
		{name: "kernel.dmesg_restrict", value: "1"},
		{name: "kernel.unprivileged_bpf_disabled", value: "1"},
	}
}

func sysctlConfigLines() []string {
	lines := []string{"# Managed by AegisNode."}
	for _, setting := range sysctlSettings() {
		lines = append(lines, setting.name+" = "+setting.value)
	}
	return lines
}
