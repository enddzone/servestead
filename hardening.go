package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"servestead/resources"
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
	flags.StringVar(&config.SSHUser, "ssh-user", "servestead", "administrative SSH user")
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
	sysctlContent := sysctlHardeningConfig()
	return []Task{
		{Name: "Validate supported Ubuntu release", Apply: supportedUbuntuCommand()},
		{Name: "Validate sysctl keys", Apply: validateSysctlKeysCommand()},
		{Name: "Apply package upgrades", Apply: systemUpgradeCommand()},
		{Name: "Install hardening prerequisites", Apply: commandScript(
			aptInstallCommand("apt-transport-https", "ca-certificates", "curl", "gnupg", "iptables", "unattended-upgrades"),
		)},
		{Name: "Configure swap", Apply: configureSwapCommand()},
		{Name: "Write sshd hardening config", Apply: remoteWriteFileCommand("/etc/ssh/sshd_config.d/99-servestead-hardening.conf", sshdHardeningConfig(), "root", "root", 0644)},
		{Name: "Validate and reload SSH", Apply: sshHardeningCommand()},
		{Name: "Write sysctl hardening config", Apply: remoteWriteFileCommand("/etc/sysctl.d/99-vps-hardening.conf", sysctlContent, "root", "root", 0644)},
		{Name: "Reload sysctl settings", Apply: commandScript("sysctl --system")},
		{Name: "Enable unattended upgrades", Apply: remoteWriteFileCommand("/etc/apt/apt.conf.d/20auto-upgrades", mustReadResource(resources.HardeningAutoUpgradesConfig), "root", "root", 0644)},
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

func configureSwapCommand() string {
	return commandScript(mustReadResource(resources.HardeningConfigureSwapScript))
}

func systemUpgradeCommand() string {
	return commandScript(mustRenderResourceTemplate(resources.HardeningSystemUpgradeScript, nil))
}

func sshHardeningCommand() string {
	return commandScript(mustReadResource(resources.HardeningSSHReloadScript))
}

func sshdHardeningConfig() string {
	return mustReadResource(resources.HardeningSSHConfig)
}

func supportedUbuntuCommand() string {
	return commandScript(mustReadResource(resources.HardeningSupportedUbuntu))
}

func validateSysctlKeysCommand() string {
	lines := []string{}
	for _, setting := range sysctlSettings() {
		lines = append(lines, "sysctl -n "+shellQuote(setting.Name)+" >/dev/null")
	}
	return commandScript(lines...)
}

type sysctlSetting struct {
	Name  string
	Value string
}

func sysctlSettings() []sysctlSetting {
	return []sysctlSetting{
		{Name: "net.ipv4.conf.all.rp_filter", Value: "1"},
		{Name: "net.ipv4.conf.default.rp_filter", Value: "1"},
		{Name: "net.ipv4.conf.all.accept_source_route", Value: "0"},
		{Name: "net.ipv4.conf.default.accept_source_route", Value: "0"},
		{Name: "net.ipv4.conf.all.accept_redirects", Value: "0"},
		{Name: "net.ipv4.conf.default.accept_redirects", Value: "0"},
		{Name: "net.ipv4.conf.all.secure_redirects", Value: "0"},
		{Name: "net.ipv4.conf.default.secure_redirects", Value: "0"},
		{Name: "net.ipv4.conf.all.send_redirects", Value: "0"},
		{Name: "net.ipv4.conf.default.send_redirects", Value: "0"},
		{Name: "net.ipv4.tcp_syncookies", Value: "1"},
		{Name: "net.ipv4.tcp_synack_retries", Value: "5"},
		{Name: "kernel.dmesg_restrict", Value: "1"},
		{Name: "kernel.unprivileged_bpf_disabled", Value: "1"},
		{Name: "vm.swappiness", Value: "10"},
		{Name: "vm.vfs_cache_pressure", Value: "50"},
	}
}

func sysctlConfigLines() []string {
	lines := []string{"# Managed by Servestead."}
	for _, setting := range sysctlSettings() {
		lines = append(lines, setting.Name+" = "+setting.Value)
	}
	return lines
}

func sysctlHardeningConfig() string {
	return mustRenderResourceTemplate(resources.HardeningSysctlConfig, struct {
		Settings []sysctlSetting
	}{Settings: sysctlSettings()})
}
