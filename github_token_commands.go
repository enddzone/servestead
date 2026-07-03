package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

const githubTokenUsage = `Usage:
  servestead github-token set --profile <id> --file <path>
  servestead github-token set --profile <id> --from-env
  servestead github-token status --profile <id>
  servestead github-token remove --profile <id>
`

func runGitHubToken(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, githubTokenUsage)
		return errors.New("github-token command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, githubTokenUsage)
		return flag.ErrHelp
	case "set":
		return runGitHubTokenSet(args[1:], stdout, stderr)
	case "status":
		return runGitHubTokenStatus(args[1:], stdout, stderr)
	case "remove":
		return runGitHubTokenRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, githubTokenUsage)
		return fmt.Errorf("unknown github-token command %q", args[0])
	}
}

func runGitHubTokenSet(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("github-token set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var profileID, path string
	var fromEnv bool
	flags.StringVar(&profileID, "profile", "", "saved Servestead profile ID")
	flags.StringVar(&path, "file", "", "file containing a GitHub personal access token; use - to read stdin")
	flags.BoolVar(&fromEnv, "from-env", false, "store SERVESTEAD_GITHUB_TOKEN in the profile")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if profileID == "" {
		return errors.New("--profile is required")
	}
	if path == "" && !fromEnv {
		return errors.New("exactly one of --file or --from-env is required")
	}
	if path != "" && fromEnv {
		return errors.New("exactly one of --file or --from-env is required")
	}

	token, err := readGitHubToken(path, fromEnv)
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
	secrets.GitHubToken = token
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Stored GitHub token for profile.")
	return nil
}

func runGitHubTokenStatus(args []string, stdout, stderr io.Writer) error {
	profileID, err := parseGitHubTokenProfileFlag("github-token status", args, stderr)
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
	_, source := effectiveGitHubToken(secrets)
	if strings.TrimSpace(secrets.GitHubToken) != "" {
		fmt.Fprintln(stdout, "Profile token: configured")
	} else {
		fmt.Fprintln(stdout, "Profile token: not configured")
	}
	if strings.TrimSpace(os.Getenv("SERVESTEAD_GITHUB_TOKEN")) != "" {
		fmt.Fprintln(stdout, "Environment token: configured")
	} else {
		fmt.Fprintln(stdout, "Environment token: not configured")
	}
	fmt.Fprintf(stdout, "Effective source: %s\n", source)
	return nil
}

func runGitHubTokenRemove(args []string, stdout, stderr io.Writer) error {
	profileID, err := parseGitHubTokenProfileFlag("github-token remove", args, stderr)
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
	secrets.GitHubToken = ""
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Removed stored GitHub token for profile.")
	return nil
}

func parseGitHubTokenProfileFlag(name string, args []string, stderr io.Writer) (string, error) {
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

func readGitHubToken(path string, fromEnv bool) (string, error) {
	var data []byte
	var err error
	switch {
	case fromEnv:
		data = []byte(os.Getenv("SERVESTEAD_GITHUB_TOKEN"))
	case path == "-":
		data, err = io.ReadAll(os.Stdin)
	default:
		data, err = os.ReadFile(expandUserPath(path))
	}
	if err != nil {
		return "", fmt.Errorf("read GitHub token: %w", err)
	}
	return normalizeGitHubToken(string(data))
}

func normalizeGitHubToken(value string) (string, error) {
	token := strings.TrimSpace(value)
	if token == "" {
		return "", errors.New("GitHub token is empty")
	}
	for _, character := range token {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", errors.New("GitHub token cannot contain whitespace or control characters")
		}
	}
	return token, nil
}
