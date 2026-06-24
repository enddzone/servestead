package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed playbooks/*.yml
var embeddedPlaybooks embed.FS

type bootstrapConfig struct {
	Host               string
	SSHUser            string
	AdminUser          string
	AdminPublicKeyPath string
	PrivateKeyPath     string
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
	ansiblePath, err := exec.LookPath("ansible-playbook")
	if err != nil {
		return errors.New("ansible-playbook is required; install ansible-core and ensure it is on PATH")
	}

	tempDir, err := os.MkdirTemp("", "aegisnode-playbooks-")
	if err != nil {
		return fmt.Errorf("create temporary playbook directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	playbookPath, err := extractBootstrapPlaybook(tempDir)
	if err != nil {
		return err
	}

	commandArgs, err := ansibleArgs(config, key, playbookPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "bootstrapping %s as %s...\n", config.Host, config.AdminUser)
	command := exec.CommandContext(ctx, ansiblePath, commandArgs...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("ansible bootstrap failed: %w", err)
	}
	fmt.Fprintf(stdout, "bootstrap complete: ssh %s@%s\n", config.AdminUser, config.Host)
	return nil
}

func extractBootstrapPlaybook(directory string) (string, error) {
	data, err := embeddedPlaybooks.ReadFile("playbooks/bootstrap.yml")
	if err != nil {
		return "", fmt.Errorf("read embedded bootstrap playbook: %w", err)
	}
	path := filepath.Join(directory, "bootstrap.yml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write temporary bootstrap playbook: %w", err)
	}
	return path, nil
}

func ansibleArgs(config bootstrapConfig, adminPublicKey, playbookPath string) ([]string, error) {
	extraVars, err := json.Marshal(map[string]string{
		"admin_username":   config.AdminUser,
		"admin_public_key": adminPublicKey,
	})
	if err != nil {
		return nil, fmt.Errorf("encode Ansible variables: %w", err)
	}
	return []string{
		"--inventory", config.Host + ",",
		"--user", config.SSHUser,
		"--private-key", config.PrivateKeyPath,
		"--ssh-common-args", "-o StrictHostKeyChecking=accept-new",
		"--extra-vars", string(extraVars),
		playbookPath,
	}, nil
}
