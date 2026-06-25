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

type bootstrapConfig struct {
	Host               string
	SSHUser            string
	AdminUser          string
	AdminPublicKeyPath string
	PrivateKeyPath     string
}

type bootstrapRemoteClientFactory func(context.Context, bootstrapConfig, io.Writer, io.Writer) (remoteClient, error)

var newBootstrapRemoteClient bootstrapRemoteClientFactory = func(ctx context.Context, config bootstrapConfig, stdout, stderr io.Writer) (remoteClient, error) {
	return newSSHRemoteClient(ctx, config.Host, config.SSHUser, config.PrivateKeyPath, stdout, stderr)
}

func runBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	config := bootstrapConfig{}
	flags.StringVar(&config.Host, "host", "", "target VPS IPv4 address or hostname")
	flags.StringVar(&config.SSHUser, "ssh-user", "root", "initial SSH user")
	flags.StringVar(&config.AdminUser, "admin-user", "aegisadmin", "administrative user to create")
	flags.StringVar(&config.AdminPublicKeyPath, "admin-public-key", "", "path to the admin ED25519 public key")
	flags.StringVar(&config.PrivateKeyPath, "private-key", "", "path to the private key used for initial SSH access")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if config.Host == "" || config.AdminPublicKeyPath == "" || config.PrivateKeyPath == "" {
		return errors.New("--host, --admin-public-key, and --private-key are required")
	}
	if !linuxUsername.MatchString(config.SSHUser) || !linuxUsername.MatchString(config.AdminUser) {
		return errors.New("--ssh-user and --admin-user must be valid Linux usernames")
	}

	adminPublicKey, err := os.ReadFile(config.AdminPublicKeyPath)
	if err != nil {
		return fmt.Errorf("read admin public key: %w", err)
	}
	key := strings.TrimSpace(string(adminPublicKey))
	if fields := strings.Fields(key); len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return errors.New("--admin-public-key must contain an ED25519 public key")
	}
	if _, err := os.Stat(config.PrivateKeyPath); err != nil {
		return fmt.Errorf("access private key: %w", err)
	}

	client, err := newBootstrapRemoteClient(ctx, config, stdout, stderr)
	if err != nil {
		return err
	}
	defer client.Close()

	fmt.Fprintf(stdout, "bootstrapping %s as %s...\n", config.Host, config.AdminUser)
	if err := runBootstrapSteps(ctx, client, config, key); err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	if config.AdminPublicKeyPath == publicKeyPath(config.PrivateKeyPath) {
		fmt.Fprintf(stdout, "bootstrap complete: ssh -i %s %s@%s\n", shellQuoteForDisplay(config.PrivateKeyPath), config.AdminUser, config.Host)
	} else {
		fmt.Fprintf(stdout, "bootstrap complete: ssh %s@%s with the private key matching %s\n", config.AdminUser, config.Host, config.AdminPublicKeyPath)
	}
	return nil
}

func runBootstrapSteps(ctx context.Context, client remoteClient, config bootstrapConfig, adminPublicKey string) error {
	for _, command := range bootstrapCommands(config, adminPublicKey) {
		if err := client.Run(ctx, privilegedCommand(config.SSHUser, command)); err != nil {
			return err
		}
	}
	return nil
}

func bootstrapCommands(config bootstrapConfig, adminPublicKey string) []string {
	sshDirectory := "/home/" + config.AdminUser + "/.ssh"
	authorizedKeysPath := sshDirectory + "/authorized_keys"
	return []string{
		commandScript(
			aptInstallCommand("curl", "git", "gnupg2", "sudo"),
		),
		commandScript(
			"getent group "+shellQuote(config.AdminUser)+" >/dev/null || groupadd "+shellQuote(config.AdminUser),
			"id -u "+shellQuote(config.AdminUser)+" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash --gid "+shellQuote(config.AdminUser)+" --groups sudo "+shellQuote(config.AdminUser),
			"usermod --append --groups sudo "+shellQuote(config.AdminUser),
			"passwd -l "+shellQuote(config.AdminUser)+" >/dev/null 2>&1 || true",
		),
		sudoersCommand(config.AdminUser),
		commandScript(
			"install -d -m 0700 -o " + shellQuote(config.AdminUser) + " -g " + shellQuote(config.AdminUser) + " " + shellQuote(sshDirectory),
		),
		remoteWriteFileCommand(authorizedKeysPath, adminPublicKey+"\n", config.AdminUser, config.AdminUser, 0600),
	}
}

func sudoersCommand(adminUser string) string {
	path := "/etc/sudoers.d/" + adminUser
	temporaryPath := path + ".aegisnode.tmp"
	content := adminUser + " ALL=(ALL) NOPASSWD:ALL\n"
	return strings.Join([]string{
		"set -e",
		"printf '%s' " + shellQuote(content) + " > " + shellQuote(temporaryPath),
		"chown root:root " + shellQuote(temporaryPath),
		"chmod 0440 " + shellQuote(temporaryPath),
		"visudo -cf " + shellQuote(temporaryPath),
		"mv " + shellQuote(temporaryPath) + " " + shellQuote(path),
	}, "\n")
}
