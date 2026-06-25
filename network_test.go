package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestNetworkCommandsContainPhase3Baseline(t *testing.T) {
	tasks := networkTasks(networkConfig{SSHUser: "aegisadmin", SSHPort: "2222"})
	joined := strings.Join(taskScripts(tasks), "\n")
	assertTaskNames(t, taskNames(tasks), []string{
		"Validate supported Ubuntu release",
		"Install Docker and UFW prerequisites",
		"Remove conflicting Docker packages",
		"Configure Docker keyring",
		"Configure Docker repository",
		"Install Docker runtime",
		"Ensure administrative sudo access",
		"Allow Docker commands without sudo",
		"Write Docker daemon config",
		"Enable IPv4 forwarding",
		"Apply IPv4 forwarding",
		"Configure UFW masquerade translations",
		"Configure UFW default policy and routes",
		"Restart Docker",
	})
	for _, expected := range []string{
		"download.docker.com/linux/ubuntu/gpg",
		"apt-get -o DPkg::Lock::Timeout=300 update",
		"apt-get -o DPkg::Lock::Timeout=300 remove -y \"$package\"",
		"DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin",
		"usermod --append --groups sudo 'aegisadmin'",
		"visudo -cf '/etc/sudoers.d/aegisadmin.aegisnode.tmp'",
		"getent group docker >/dev/null || groupadd docker",
		"usermod --append --groups docker 'aegisadmin'",
		"/etc/apt/sources.list.d/docker.sources",
		"docker-ce docker-ce-cli containerd.io docker-compose-plugin",
		"/etc/docker/daemon.json",
		"/etc/sysctl.d/98-aegisnode-forwarding.conf",
		"# START AegisNode UFW MASQUERADE TRANSLATIONS",
		"-A POSTROUTING -s 172.17.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"-A POSTROUTING -s 172.18.0.0/16 -o ${egress_interface} -j MASQUERADE",
		"ufw allow in proto tcp to any port '2222'",
		"ufw default deny incoming",
		"ufw default allow outgoing",
		"ufw default deny routed",
		"ufw route allow from 172.17.0.0/16 to any",
		"ufw route allow from 172.18.0.0/16 to any",
		"ufw allow 80/tcp",
		"ufw allow 443/tcp",
		"ufw --force enable",
		"systemctl restart docker",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("network commands missing %q:\n%s", expected, joined)
		}
	}
	daemonConfig := dockerDaemonConfig()
	for _, expected := range []string{`"iptables": false`, `"no-new-privileges": true`} {
		if !strings.Contains(daemonConfig, expected) {
			t.Fatalf("Docker daemon config missing %q:\n%s", expected, daemonConfig)
		}
	}
}

func TestRunNetworkStepsUsesPrivilegedCommands(t *testing.T) {
	client := &recordingRemoteClient{}
	config := networkConfig{SSHUser: "aegisadmin"}
	if err := runNetworkSteps(context.Background(), client, config, nil); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(networkTasks(config)) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
	if !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("network command did not use sudo: %q", client.commands[0])
	}
}

func TestSSHPortForHost(t *testing.T) {
	tests := map[string]string{
		"203.0.113.10":       "22",
		"203.0.113.10:2222":  "2222",
		"[2001:db8::1]:2200": "2200",
	}
	for host, expected := range tests {
		actual, err := sshPortForHost(host)
		if err != nil {
			t.Fatalf("sshPortForHost(%q): %v", host, err)
		}
		if actual != expected {
			t.Fatalf("sshPortForHost(%q) = %q, want %q", host, actual, expected)
		}
	}
}

func TestNetworkTasksSkipUserGroupManagementForRoot(t *testing.T) {
	tasks := networkTasks(networkConfig{SSHUser: "root"})
	joined := strings.Join(taskScripts(tasks), "\n")
	for _, unexpected := range []string{
		"Ensure administrative sudo access",
		"Allow Docker commands without sudo",
		"usermod --append --groups docker",
		"/etc/sudoers.d/root",
	} {
		if strings.Contains(strings.Join(taskNames(tasks), "\n")+"\n"+joined, unexpected) {
			t.Fatalf("root network tasks included %q:\n%s", unexpected, joined)
		}
	}
}

func TestNetworkRequiresHostAndPrivateKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"network"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "--host and --private-key are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}
