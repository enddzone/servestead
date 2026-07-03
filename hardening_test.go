package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHardeningCommandsContainBaseline(t *testing.T) {
	tasks := hardeningTasks()
	joined := strings.Join(taskScripts(tasks), "\n")
	assertTaskNames(t, taskNames(tasks), []string{
		"Validate supported Ubuntu release",
		"Validate sysctl keys",
		"Apply package upgrades",
		"Install hardening prerequisites",
		"Configure swap",
		"Write sshd hardening config",
		"Validate and reload SSH",
		"Write sysctl hardening config",
		"Reload sysctl settings",
		"Enable unattended upgrades",
		"Configure CrowdSec keyring",
		"Configure CrowdSec repository",
		"Install CrowdSec and firewall bouncer",
	})
	for _, expected := range []string{
		`bash -c 'set -e`,
		`[[ "$ID" = "ubuntu" ]]`,
		`dpkg --compare-versions "$VERSION_ID" ge 22.04`,
		"sysctl -n 'net.ipv4.conf.all.rp_filter'",
		"apt-get -o DPkg::Lock::Timeout=300 update",
		"apt-get -o DPkg::Lock::Timeout=300 full-upgrade -y",
		"apt-get -o DPkg::Lock::Timeout=300 autoremove -y",
		"DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y \"$bouncer_package\"",
		"apt-get -o DPkg::Lock::Timeout=300 full-upgrade -y",
		"apt-get -o DPkg::Lock::Timeout=300 autoremove -y",
		"apt-get -o DPkg::Lock::Timeout=300 install -y 'apt-transport-https' 'ca-certificates' 'curl' 'gnupg' 'iptables' 'unattended-upgrades'",
		"/etc/ssh/sshd_config.d/99-servestead-hardening.conf",
		`ram_gib="$(( (mem_kib + 1048575) / 1048576 ))"`,
		`if [[ "$ram_gib" -lt 2 ]]; then swap_gib="$((ram_gib * 2))"; elif [[ "$ram_gib" -le 8 ]]; then swap_gib="$ram_gib"; else swap_gib=4; fi`,
		`swapon --show=NAME --noheadings --raw`,
		`fallocate -l "${swap_gib}G" /swapfile`,
		`dd if=/dev/zero of=/swapfile`,
		`chmod 600 /swapfile`,
		`mkswap /swapfile`,
		`/swapfile none swap sw 0 0`,
		"passwd -l root",
		"install -d -m 0755 -o root -g root /run/sshd",
		"/usr/sbin/sshd -t",
		"systemctl reload-or-restart ssh || systemctl reload-or-restart sshd",
		"/etc/sysctl.d/99-vps-hardening.conf",
		"packagecloud.io/crowdsec/crowdsec/gpgkey",
		"systemctl enable --now crowdsec",
		"crowdsec-firewall-bouncer-nftables",
		"crowdsec-firewall-bouncer-iptables",
		"systemctl enable --now crowdsec-firewall-bouncer",
		"cscli bouncers list",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("hardening commands missing %q:\n%s", expected, joined)
		}
	}
	config := strings.Join(sysctlConfigLines(), "\n")
	if !strings.Contains(config, "kernel.unprivileged_bpf_disabled = 1") {
		t.Fatalf("sysctl config missing expected setting:\n%s", config)
	}
	for _, expected := range []string{"vm.swappiness = 10", "vm.vfs_cache_pressure = 50"} {
		if !strings.Contains(config, expected) {
			t.Fatalf("sysctl config missing %q:\n%s", expected, config)
		}
	}
	sshdConfig := sshdHardeningConfig()
	for _, expected := range []string{
		"PermitRootLogin no",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"PubkeyAuthentication yes",
	} {
		if !strings.Contains(sshdConfig, expected) {
			t.Fatalf("sshd config missing %q:\n%s", expected, sshdConfig)
		}
	}
}

func TestRunHardeningStepsUsesPrivilegedCommands(t *testing.T) {
	client := &recordingRemoteClient{}
	config := hardeningConfig{SSHUser: "servestead"}
	if err := runHardeningSteps(context.Background(), client, config, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(hardeningTasks()) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
	if !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("non-root hardening did not use sudo: %q", client.commands[0])
	}
}

func TestHardenRequiresHostAndPrivateKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"harden"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "--host and --private-key are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}
