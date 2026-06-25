package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type hardeningConfig struct {
	Host           string
	SSHUser        string
	PrivateKeyPath string
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
	ansiblePath, err := exec.LookPath("ansible-playbook")
	if err != nil {
		return errors.New("ansible-playbook is required; install ansible-core and ensure it is on PATH")
	}

	tempDir, err := os.MkdirTemp("", "aegisnode-playbooks-")
	if err != nil {
		return fmt.Errorf("create temporary playbook directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	playbookPath, err := extractHardeningPlaybook(tempDir)
	if err != nil {
		return err
	}

	commandArgs := hardeningArgs(config, playbookPath)
	fmt.Fprintf(stdout, "hardening %s as %s...\n", config.Host, config.SSHUser)
	command := exec.CommandContext(ctx, ansiblePath, commandArgs...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("ansible hardening failed: %w", err)
	}
	fmt.Fprintf(stdout, "hardening complete: %s\n", config.Host)
	return nil
}

func extractHardeningPlaybook(directory string) (string, error) {
	data, err := embeddedPlaybooks.ReadFile("playbooks/hardening.yml")
	if err != nil {
		return "", fmt.Errorf("read embedded hardening playbook: %w", err)
	}
	path := filepath.Join(directory, "hardening.yml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write temporary hardening playbook: %w", err)
	}
	return path, nil
}

func hardeningArgs(config hardeningConfig, playbookPath string) []string {
	return []string{
		"--inventory", config.Host + ",",
		"--user", config.SSHUser,
		"--private-key", config.PrivateKeyPath,
		"--ssh-common-args", "-o StrictHostKeyChecking=accept-new",
		playbookPath,
	}
}
