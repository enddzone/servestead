package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

const defaultServesteadKeyName = "servestead_ed25519"

type keygenConfig struct {
	Path    string
	Comment string
	Force   bool
}

const keygenUsage = `Usage of keygen:
  servestead keygen [--path <private-key-path>] [--comment <label>] [--force]

Generates the default ED25519 SSH keypair for cloud-provider server creation and later Servestead admin access, then prints the public key for provider registration.
`

func runKeygen(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, keygenUsage)
		flags.PrintDefaults()
	}
	config := defaultKeygenConfig()
	flags.StringVar(&config.Path, "path", config.Path, "private key path to create")
	flags.StringVar(&config.Comment, "comment", config.Comment, "public key comment/label")
	flags.BoolVar(&config.Force, "force", false, "overwrite an existing keypair at the selected path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return generateProviderKeypair(ctx, config, stdout, stderr)
}

func defaultKeygenConfig() keygenConfig {
	return keygenConfig{
		Path:    filepath.Join("$HOME", ".ssh", defaultServesteadKeyName),
		Comment: "servestead-key",
	}
}

func generateProviderKeypair(ctx context.Context, config keygenConfig, stdout, stderr io.Writer) error {
	config.Path = expandUserPath(config.Path)
	config.Comment = strings.TrimSpace(config.Comment)
	if config.Path == "" {
		return errors.New("--path is required")
	}
	if config.Comment == "" {
		config.Comment = "servestead-key"
	}

	publicPath := config.Path + ".pub"
	if !config.Force {
		if _, err := os.Stat(config.Path); err == nil {
			return fmt.Errorf("%s already exists; choose another --path or pass --force", config.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check private key path: %w", err)
		}
		if _, err := os.Stat(publicPath); err == nil {
			return fmt.Errorf("%s already exists; choose another --path or pass --force", publicPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check public key path: %w", err)
		}
	} else {
		if err := os.Remove(config.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove existing private key: %w", err)
		}
		if err := os.Remove(publicPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove existing public key: %w", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(config.Path), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ED25519 keypair: %w", err)
	}
	privateBlock, err := ssh.MarshalPrivateKey(privateKey, config.Comment)
	if err != nil {
		return fmt.Errorf("encode private key: %w", err)
	}
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("encode public key: %w", err)
	}
	privateBytes := pem.EncodeToMemory(privateBlock)
	publicBytes := append(bytesTrimRightNewline(ssh.MarshalAuthorizedKey(sshPublicKey)), []byte(" "+config.Comment+"\n")...)
	if err := os.WriteFile(config.Path, privateBytes, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(publicPath, publicBytes, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	printProviderKeyGuidance(stdout, config.Path, publicPath, strings.TrimSpace(string(publicBytes)))
	return nil
}

func bytesTrimRightNewline(value []byte) []byte {
	return []byte(strings.TrimRight(string(value), "\n"))
}

func printProviderKeyGuidance(stdout io.Writer, privatePath, publicPath, publicKey string) {
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Servestead SSH keypair ready.")
	fmt.Fprintf(stdout, "Private key: %s\n", privatePath)
	fmt.Fprintf(stdout, "Public key:  %s\n", publicPath)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Copy this public key into your VPS provider before provisioning:")
	fmt.Fprintln(stdout, publicKey)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "This helper creates an unencrypted private key so Servestead and OpenSSH can use it non-interactively. Keep the private key file local and protected.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Provider guidance:")
	fmt.Fprintln(stdout, "- DigitalOcean: Settings -> Security -> SSH Keys -> Add SSH Key. Use the key fingerprint or ID with `servestead provision --ssh-key`.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "After the provider accepts the key:")
	fmt.Fprintln(stdout, "1. Run `servestead provision --provider digitalocean --name <name> --ssh-key <key-id-or-fingerprint>`.")
	fmt.Fprintf(stdout, "2. Use `%s` as the private key when running `servestead setup` or logging in manually.\n", privatePath)
}
