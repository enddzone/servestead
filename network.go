package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

type networkConfig struct {
	Host           string
	SSHUser        string
	SSHPort        string
	PrivateKeyPath string
}

type networkRemoteClientFactory func(context.Context, networkConfig, io.Writer, io.Writer) (remoteClient, error)

var newNetworkRemoteClient networkRemoteClientFactory = func(ctx context.Context, config networkConfig, stdout, stderr io.Writer) (remoteClient, error) {
	return newSSHRemoteClient(ctx, config.Host, config.SSHUser, config.PrivateKeyPath, stdout, stderr)
}

func runNetwork(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("network", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := networkConfig{}
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
	sshPort, err := sshPortForHost(config.Host)
	if err != nil {
		return err
	}
	config.SSHPort = sshPort

	client, err := newNetworkRemoteClient(ctx, config, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(stdout, "configuring Docker networking and UFW on %s as %s...\n", config.Host, config.SSHUser)
	if err := runNetworkSteps(ctx, client, config, stdout); err != nil {
		return fmt.Errorf("network configuration failed: %w", err)
	}
	fmt.Fprintf(stdout, "network configuration complete: %s\n", config.Host)
	return nil
}

func runNetworkSteps(ctx context.Context, client remoteClient, config networkConfig, progress io.Writer) error {
	return runTasks(ctx, client, config.SSHUser, networkTasks(config), progress)
}

func networkTasks(config networkConfig) []Task {
	sshPort := firstNonEmpty(config.SSHPort, defaultSSHPort)
	tasks := []Task{
		{Name: "Validate supported Ubuntu release", Apply: supportedUbuntuCommand()},
		{Name: "Install Docker and UFW prerequisites", Apply: commandScript(
			aptInstallCommand("ca-certificates", "curl", "gnupg", "ufw"),
		)},
		{Name: "Remove conflicting Docker packages", Apply: removeConflictingDockerPackagesCommand()},
		{Name: "Configure Docker keyring", Apply: commandScript(
			"install -d -m 0755 -o root -g root /etc/apt/keyrings",
			"curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc",
			"chown root:root /etc/apt/keyrings/docker.asc",
			"chmod 0644 /etc/apt/keyrings/docker.asc",
		)},
		{Name: "Configure Docker repository", Apply: dockerRepositoryCommand()},
		{Name: "Install Docker runtime", Apply: commandScript(
			aptGetCommand("update"),
			noninteractiveAptGetCommand("install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin"),
		)},
	}
	if config.SSHUser != "root" {
		tasks = append(tasks,
			Task{Name: "Ensure administrative sudo access", Apply: administrativeSudoAccessCommand(config.SSHUser)},
			Task{Name: "Allow Docker commands without sudo", Apply: dockerGroupAccessCommand(config.SSHUser)},
		)
	}
	tasks = append(tasks,
		Task{Name: "Write Docker daemon config", Apply: remoteWriteFileCommand("/etc/docker/daemon.json", dockerDaemonConfig(), "root", "root", 0644)},
		Task{Name: "Enable IPv4 forwarding", Apply: remoteWriteFileCommand("/etc/sysctl.d/98-aegisnode-forwarding.conf", "net.ipv4.ip_forward = 1\n", "root", "root", 0644)},
		Task{Name: "Apply IPv4 forwarding", Apply: commandScript("sysctl --system")},
		Task{Name: "Configure UFW masquerade translations", Apply: ufwMasqueradeCommand()},
		Task{Name: "Configure UFW default policy and routes", Apply: ufwPolicyCommand(sshPort)},
		Task{Name: "Restart Docker", Apply: commandScript(
			"systemctl enable docker",
			"systemctl restart docker",
			"docker info >/dev/null",
		)},
	)
	return tasks
}

func removeConflictingDockerPackagesCommand() string {
	return commandScript(
		"for package in docker.io docker-compose docker-compose-v2 docker-doc podman-docker containerd runc; do",
		"  "+aptGetCommand("remove -y \"$package\"")+" >/dev/null 2>&1 || true",
		"done",
	)
}

func dockerRepositoryCommand() string {
	return commandScript(
		". /etc/os-release",
		`codename="${UBUNTU_CODENAME:-$VERSION_CODENAME}"`,
		`test -n "$codename"`,
		`architecture="$(dpkg --print-architecture)"`,
		`cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${codename}
Components: stable
Architectures: ${architecture}
Signed-By: /etc/apt/keyrings/docker.asc
EOF`,
	)
}

func dockerDaemonConfig() string {
	return strings.Join([]string{
		"{",
		`  "storage-driver": "overlay2",`,
		`  "log-driver": "json-file",`,
		`  "log-opts": {`,
		`    "max-size": "50m",`,
		`    "max-file": "3"`,
		`  },`,
		`  "iptables": true,`,
		`  "ip-forward-no-drop": true,`,
		`  "no-new-privileges": true`,
		"}",
		"",
	}, "\n")
}

func administrativeSudoAccessCommand(adminUser string) string {
	return commandScript(
		"usermod --append --groups sudo "+shellQuote(adminUser),
		sudoersCommand(adminUser),
	)
}

func dockerGroupAccessCommand(adminUser string) string {
	return commandScript(
		"getent group docker >/dev/null || groupadd docker",
		"usermod --append --groups docker "+shellQuote(adminUser),
	)
}

func ufwMasqueradeCommand() string {
	return commandScript(
		`egress_interface="$(ip -4 route show default 0.0.0.0/0 | awk '{print $5; exit}')"`,
		`test -n "$egress_interface"`,
		installUFWMasqueradeBlockCommand("AegisNode UFW MASQUERADE TRANSLATIONS", "172.17.0.0/16", "172.18.0.0/16"),
	)
}

func installUFWMasqueradeBlockCommand(marker string, subnets ...string) string {
	startMarker := "# START " + marker
	endMarker := "# END " + marker
	commands := []string{
		"test -f /etc/ufw/before.rules",
		`tmp="$(mktemp)"`,
		"awk -v start=" + shellQuote(startMarker) + " -v end=" + shellQuote(endMarker) + ` 'BEGIN { skip = 0 } $0 == start { skip = 1; next } $0 == end { skip = 0; next } !skip { print }' /etc/ufw/before.rules > "$tmp"`,
		`cat > /etc/ufw/before.rules <<EOF`,
		startMarker,
		"*nat",
		":POSTROUTING ACCEPT [0:0]",
	}
	for _, subnet := range subnets {
		commands = append(commands, "-A POSTROUTING -s "+subnet+" -o ${egress_interface} -j MASQUERADE")
	}
	commands = append(commands,
		"COMMIT",
		endMarker,
		"",
		"EOF",
		`cat "$tmp" >> /etc/ufw/before.rules`,
		`rm -f "$tmp"`,
	)
	return strings.Join(commands, "\n")
}

func sshPortForHost(host string) (string, error) {
	hostPort, err := sshHostPort(host)
	if err != nil {
		return "", err
	}
	_, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "", err
	}
	return port, nil
}

func ufwPolicyCommand(sshPort string) string {
	return commandScript(
		"ufw allow in proto tcp to any port "+shellQuote(sshPort),
		"ufw default deny incoming",
		"ufw default allow outgoing",
		"ufw default deny routed",
		"ufw allow 80/tcp",
		"ufw allow 443/tcp",
		"ufw route allow from 172.17.0.0/16 to any",
		"ufw route allow from 172.18.0.0/16 to any",
		"ufw --force enable",
		"ufw reload",
		"ufw status verbose",
	)
}
