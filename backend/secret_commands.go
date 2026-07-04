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

const secretsUsage = `Usage:
  servestead secrets init --profile <id>
  servestead secrets status --profile <id>
  servestead secrets export-key --profile <id>
  servestead secrets import-key --profile <id> --file <path>
`

func runSecrets(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, secretsUsage)
		return errors.New("secrets command is required")
	}
	switch args[0] {
	case "init":
		return runSecretsInit(args[1:], stdout, stderr)
	case "status":
		return runSecretsStatus(ctx, args[1:], stdout, stderr)
	case "export-key":
		return runSecretsExportKey(args[1:], stdout, stderr)
	case "import-key":
		return runSecretsImportKey(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, secretsUsage)
		return fmt.Errorf("unknown secrets command %q", args[0])
	}
}

func runSecretsInit(args []string, stdout, stderr io.Writer) error {
	profileID, err := parseSecretsProfileFlag("secrets init", args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if _, _, err := store.Load(profileID); err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	recipient, created, err := secrets.EnsureStackSecretIdentity()
	if err != nil {
		return err
	}
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return err
	}
	if created {
		fmt.Fprintln(stdout, "Created stack secret identity.")
	} else {
		fmt.Fprintln(stdout, "Stack secret identity already exists.")
	}
	fmt.Fprintf(stdout, "Recipient: %s\n", recipient)
	return nil
}

func runSecretsStatus(_ context.Context, args []string, stdout, stderr io.Writer) error {
	profileID, err := parseSecretsProfileFlag("secrets status", args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, _, err := store.Load(profileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	if _, recipient, err := secrets.StackSecretIdentityPair(); err == nil {
		fmt.Fprintf(stdout, "Recipient: %s\n", recipient)
	} else {
		fmt.Fprintln(stdout, "Recipient: not initialized")
	}
	if profile.ConfigRepositoryPath == "" {
		fmt.Fprintln(stdout, "Configuration repository: not configured")
		return nil
	}
	fmt.Fprintf(stdout, "Configuration repository: %s\n", profile.ConfigRepositoryPath)
	stacks, err := loadEditableStacks(profile.ConfigRepositoryPath)
	if err != nil {
		return err
	}
	count := 0
	for _, stack := range stacks {
		if !stack.Metadata.Secrets.HasSecrets() {
			continue
		}
		count++
		fmt.Fprintf(stdout, "Stack %s: %s\n", stack.Name, strings.Join(stack.Metadata.Secrets.KeyNames(), ", "))
	}
	if count == 0 {
		fmt.Fprintln(stdout, "Stack secrets: none")
	}
	return nil
}

func runSecretsExportKey(args []string, stdout, stderr io.Writer) error {
	profileID, err := parseSecretsProfileFlag("secrets export-key", args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if _, _, err := store.Load(profileID); err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	identity, _, err := secrets.StackSecretIdentityPair()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, identity)
	return nil
}

func runSecretsImportKey(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("secrets import-key", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var profileID, path string
	flags.StringVar(&profileID, "profile", "", "saved Servestead profile ID")
	flags.StringVar(&path, "file", "", "file containing an AGE-SECRET-KEY identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if profileID == "" || path == "" {
		return errors.New("--profile and --file are required")
	}
	data, err := os.ReadFile(expandUserPath(path))
	if err != nil {
		return fmt.Errorf("read stack secret identity: %w", err)
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if _, _, err := store.Load(profileID); err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	recipient, err := secrets.SetStackSecretIdentity(string(data))
	if err != nil {
		return err
	}
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Imported stack secret identity.\nRecipient: %s\n", recipient)
	return nil
}

func parseSecretsProfileFlag(name string, args []string, stderr io.Writer) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var profileID string
	flags.StringVar(&profileID, "profile", "", "saved Servestead profile ID")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if profileID == "" {
		return "", errors.New("--profile is required")
	}
	return profileID, nil
}
