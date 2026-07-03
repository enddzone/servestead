package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const observabilityComposeRepositoryPath = "stacks/observability/compose.yaml"
const gitStatusPorcelainFlag = "--porcelain"
const gitOriginRemotePrefix = "origin/"
const trustedCommandPath = "/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin"

var trustedGitExecutablePaths = []string{
	"/usr/bin/git",
	"/usr/local/bin/git",
	"/opt/homebrew/bin/git",
}

var errRepositoryReviewRequired = errors.New("configuration repository scaffold requires review")

type configRepositoryRevision struct {
	Path       string
	Commit     string
	Branch     string
	Compose    string
	ComposeSHA string
	Origin     string
	Stacks     []repositoryStack
}

func defaultConfigRepositoryPath(profileID string) (string, error) {
	configDirectory := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if configDirectory == "" {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		configDirectory = filepath.Join(homeDirectory, ".config")
	}
	return filepath.Join(configDirectory, "servestead", "repositories", profileID), nil
}

func prepareDeclarativeSetup(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig) (Profile, setupConfig, error) {
	profileSecrets, err := store.LoadSecrets(profile.ID)
	if err != nil {
		return profile, config, err
	}
	githubToken, _ := effectiveGitHubToken(profileSecrets)
	path, err := declarativeConfigRepositoryPath(store, profile, config)
	if err != nil {
		return profile, config, err
	}
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: config.BaseDomain,
		AdminEmail: config.PangolinAdminEmail,
	})
	revision, err := prepareConfigRepository(
		ctx,
		path,
		config.GitHubRepositoryURL,
		githubToken,
		profile.ID,
		scaffold,
	)
	profile.ConfigRepositoryPath = path
	if saveErr := store.Save(profile, state); saveErr != nil {
		return profile, config, saveErr
	}
	if err != nil {
		return profile, config, err
	}
	profile.ConfigRepositoryPath = revision.Path
	config.ConfigRepositoryPath = revision.Path
	config.ConfigRepositoryCommit = revision.Commit
	config.ConfigRepositoryBranch = revision.Branch
	config.ConfigRepositoryOrigin = revision.Origin
	config.ConfigRepositoryCompose = revision.Compose
	config.ConfigRepositorySHA256 = revision.ComposeSHA
	config.GitHubToken = githubToken
	config.Stacks, err = configuredStacksFromRevision(ctx, revision, profileSecrets, profile)
	if err != nil {
		return profile, config, err
	}
	if err := validateConfiguredStackSet(config.Stacks); err != nil {
		return profile, config, err
	}
	if err := store.Save(profile, state); err != nil {
		return profile, config, err
	}
	return profile, config, nil
}

func declarativeConfigRepositoryPath(store ProfileStore, profile Profile, config setupConfig) (string, error) {
	path := firstNonEmpty(config.ConfigRepositoryPath, profile.ConfigRepositoryPath)
	if path == "" {
		if fileStore, ok := store.(*fileProfileStore); ok && !fileStore.defaultRoot {
			path = filepath.Join(fileStore.root, "repositories", profile.ID)
		} else {
			var err error
			path, err = defaultConfigRepositoryPath(profile.ID)
			if err != nil {
				return "", err
			}
		}
	}
	absolutePath, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return "", fmt.Errorf("resolve configuration repository path: %w", err)
	}
	return absolutePath, nil
}

func configuredStacksFromRevision(ctx context.Context, revision configRepositoryRevision, secrets ProfileSecrets, profile Profile) ([]configuredStack, error) {
	stacks := make([]configuredStack, 0, len(revision.Stacks))
	for _, stack := range revision.Stacks {
		services, err := inspectComposeServices([]byte(stack.Compose))
		if err != nil {
			return nil, fmt.Errorf("stack %s: %w", stack.Name, err)
		}
		override, err := generateStackPangolinOverride(stack.Name, stack.Metadata, services, profile)
		if err != nil {
			return nil, fmt.Errorf("stack %s: %w", stack.Name, err)
		}
		secretValues, err := stackSecretValuesFromRevision(ctx, revision, stack, secrets)
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, configuredStack{
			Name:          stack.Name,
			Compose:       stack.Compose,
			Metadata:      stack.MetadataContent,
			Override:      override,
			ComposeSHA256: stack.ComposeSHA256,
			Resources:     stack.Metadata.PublicResources,
			Files:         stack.Files,
			Secrets:       stack.Metadata.Secrets,
			SecretValues:  secretValues,
		})
	}
	return stacks, nil
}

func stackSecretValuesFromRevision(ctx context.Context, revision configRepositoryRevision, stack repositoryStack, secrets ProfileSecrets) (SecretSet, error) {
	if !stack.Metadata.Secrets.HasSecrets() {
		return nil, nil
	}
	if revision.Origin == "" || revision.Branch == "" {
		return nil, fmt.Errorf("stack %s has secrets; Dockhand secret sync requires a pushed configuration repository origin", stack.Name)
	}
	identity, _, err := secrets.StackSecretIdentityPair()
	if err != nil {
		return nil, fmt.Errorf("stack %s secrets: %w", stack.Name, err)
	}
	provider, err := secretProviderForName(stack.Metadata.Secrets.Provider)
	if err != nil {
		return nil, fmt.Errorf("stack %s secrets: %w", stack.Name, err)
	}
	values, err := provider.GetStackSecrets(ctx, stack.Metadata.Secrets.Ref(revision.Path, stack.Name, identity))
	if err != nil {
		return nil, fmt.Errorf("stack %s secrets: %w", stack.Name, err)
	}
	for _, key := range stack.Metadata.Secrets.KeyNames() {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("stack %s secrets: missing required key %s", stack.Name, key)
		}
	}
	return values, nil
}

func prepareConfigRepository(ctx context.Context, path, githubURL, token, profileID, scaffold string) (configRepositoryRevision, error) {
	path, err := resolveConfigRepositoryPath(path, profileID)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	existed, err := prepareConfigRepositoryCheckout(ctx, path, githubURL, token, profileID)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	composePath := filepath.Join(path, filepath.FromSlash(observabilityComposeRepositoryPath))
	scaffoldChanged, err := ensureConfigRepositoryScaffold(ctx, path, scaffold)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	if scaffoldChanged {
		if existed {
			return configRepositoryRevision{}, fmt.Errorf("%w: updated %s; review, commit, and rerun Servestead", errRepositoryReviewRequired, composePath)
		}
		if err := commitInitialConfigRepositoryScaffold(ctx, path); err != nil {
			return configRepositoryRevision{}, err
		}
	}
	return readConfigRepositoryRevision(ctx, path, githubURL)
}

func resolveConfigRepositoryPath(path, profileID string) (string, error) {
	if path == "" {
		var err error
		path, err = defaultConfigRepositoryPath(profileID)
		if err != nil {
			return "", err
		}
	}
	path, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return "", fmt.Errorf("resolve configuration repository path: %w", err)
	}
	return path, nil
}

func prepareConfigRepositoryCheckout(ctx context.Context, path, githubURL, token, profileID string) (bool, error) {
	if githubURL != "" {
		if err := validateGitHubRepositoryURL(githubURL); err != nil {
			return false, err
		}
	}
	existed, err := ensureConfigRepositoryPath(ctx, path, githubURL, token, profileID)
	if err != nil {
		return false, err
	}
	gitDirectory := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDirectory); errors.Is(err, os.ErrNotExist) {
		if existed {
			return false, fmt.Errorf("%s is not a Git repository", path)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return false, err
		}
		if _, err := runGit(ctx, path, nil, "init", "-b", "main"); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	return existed, nil
}

func ensureConfigRepositoryPath(ctx context.Context, path, githubURL, token, profileID string) (bool, error) {
	_, statErr := os.Stat(path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return false, statErr
	}
	if githubURL == "" || existed {
		return existed, nil
	}
	if err := cloneGitHubRepository(ctx, githubURL, path, token, profileID); err != nil {
		return false, err
	}
	return true, nil
}

func commitInitialConfigRepositoryScaffold(ctx context.Context, path string) error {
	env := []string{
		"GIT_AUTHOR_NAME=Servestead",
		"GIT_AUTHOR_EMAIL=servestead@localhost",
		"GIT_COMMITTER_NAME=Servestead",
		"GIT_COMMITTER_EMAIL=servestead@localhost",
	}
	if _, err := runGit(ctx, path, env, "add", "--", observabilityComposeRepositoryPath); err != nil {
		return err
	}
	if _, err := runGit(ctx, path, env, "commit", "-m", "Initialize Servestead configuration"); err != nil {
		return err
	}
	return nil
}

func readConfigRepositoryRevision(ctx context.Context, path, githubURL string) (configRepositoryRevision, error) {
	status, err := runGit(ctx, path, nil, "status", gitStatusPorcelainFlag, "--", observabilityComposeRepositoryPath)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	if strings.TrimSpace(status) != "" {
		return configRepositoryRevision{}, fmt.Errorf("uncommitted changes in %s block deployment", observabilityComposeRepositoryPath)
	}
	stackStatus, err := runGit(ctx, path, nil, "status", gitStatusPorcelainFlag, "--", "stacks")
	if err != nil {
		return configRepositoryRevision{}, err
	}
	if strings.TrimSpace(stackStatus) != "" {
		return configRepositoryRevision{}, fmt.Errorf("configuration repository %s has uncommitted changes under stacks/; review and commit them first", path)
	}
	commit, err := runGit(ctx, path, nil, "rev-parse", "HEAD")
	if err != nil {
		return configRepositoryRevision{}, err
	}
	compose, err := runGit(ctx, path, nil, "show", "HEAD:"+observabilityComposeRepositoryPath)
	if err != nil {
		return configRepositoryRevision{}, fmt.Errorf("read committed observability Compose file: %w", err)
	}
	if err := validateObservabilityCompose([]byte(compose)); err != nil {
		return configRepositoryRevision{}, err
	}

	origin := ""
	branch := ""
	if remoteOrigin, remoteBranch, err := resolveConfigRepositoryRemote(ctx, path, githubURL, commit); err != nil {
		return configRepositoryRevision{}, err
	} else {
		origin = remoteOrigin
		branch = remoteBranch
	}
	if githubURL != "" && origin == "" {
		return configRepositoryRevision{}, errors.New("--github-repo requires the configuration checkout to have a GitHub origin")
	}

	sum := sha256.Sum256([]byte(compose))
	stacks, err := loadCommittedStacks(ctx, path)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	return configRepositoryRevision{
		Path:       path,
		Commit:     strings.TrimSpace(commit),
		Branch:     branch,
		Compose:    compose,
		ComposeSHA: hex.EncodeToString(sum[:]),
		Origin:     origin,
		Stacks:     stacks,
	}, nil
}

func resolveConfigRepositoryRemote(ctx context.Context, path, githubURL, commit string) (string, string, error) {
	output, err := runGit(ctx, path, nil, "remote", "get-url", "origin")
	if err != nil {
		return "", "", nil
	}
	origin := strings.TrimSpace(output)
	if err := validateGitHubRepositoryURL(origin); err != nil {
		return "", "", fmt.Errorf("origin: %w", err)
	}
	if githubURL != "" && strings.TrimSuffix(origin, ".git") != strings.TrimSuffix(githubURL, ".git") {
		return "", "", fmt.Errorf("origin %q does not match --github-repo %q", origin, githubURL)
	}
	contains, err := runGit(ctx, path, nil, "branch", "-r", "--contains", strings.TrimSpace(commit))
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(contains, gitOriginRemotePrefix) {
		return "", "", errors.New("configuration commit has not been pushed to origin")
	}
	branch, err := resolveConfigRepositoryBranch(ctx, path, contains)
	if err != nil {
		return "", "", err
	}
	return origin, branch, nil
}

func resolveConfigRepositoryBranch(ctx context.Context, path, containsOutput string) (string, error) {
	current, err := runGit(ctx, path, nil, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	current = strings.TrimSpace(current)
	remoteBranches := []string{}
	for _, line := range strings.Split(containsOutput, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if !strings.HasPrefix(line, gitOriginRemotePrefix) || line == "origin/HEAD" {
			continue
		}
		name := strings.TrimPrefix(line, gitOriginRemotePrefix)
		remoteBranches = append(remoteBranches, name)
	}
	sort.Strings(remoteBranches)
	if current != "" {
		for _, branch := range remoteBranches {
			if branch == current {
				return current, nil
			}
		}
	}
	if len(remoteBranches) == 1 {
		return remoteBranches[0], nil
	}
	if current == "" {
		return "", errors.New("configuration checkout is detached and the commit is on multiple origin branches; check out the intended branch before deploying Dockhand Git stacks")
	}
	return "", fmt.Errorf("configuration branch %q does not contain the pushed commit on origin", current)
}

func ensureConfigRepositoryScaffold(ctx context.Context, path, scaffold string) (bool, error) {
	path, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return false, fmt.Errorf("resolve configuration repository path: %w", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("%s is not a Git repository", path)
		}
		return false, err
	}
	composePath := filepath.Join(path, filepath.FromSlash(observabilityComposeRepositoryPath))
	if _, err := os.Stat(composePath); err == nil {
		return refreshConfigRepositoryScaffold(ctx, path, composePath, scaffold)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(composePath), 0700); err != nil {
		return false, err
	}
	if err := os.WriteFile(composePath, []byte(scaffold), 0600); err != nil {
		return false, err
	}
	return true, nil
}

func refreshConfigRepositoryScaffold(ctx context.Context, path, composePath, scaffold string) (bool, error) {
	existing, err := os.ReadFile(composePath)
	if err != nil {
		return false, err
	}
	if string(existing) == scaffold {
		return false, nil
	}
	status, err := runGit(ctx, path, nil, "status", gitStatusPorcelainFlag, "--", observabilityComposeRepositoryPath)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) != "" {
		return false, fmt.Errorf("uncommitted changes in %s block managed scaffold refresh", observabilityComposeRepositoryPath)
	}
	if !isManagedObservabilityCompose(existing) {
		return false, nil
	}
	if err := os.WriteFile(composePath, []byte(scaffold), 0600); err != nil {
		return false, err
	}
	return true, nil
}

func isManagedObservabilityCompose(data []byte) bool {
	var document struct {
		Services map[string]struct {
			Labels []string `yaml:"labels"`
		} `yaml:"services"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(false)
	if err := decoder.Decode(&document); err != nil {
		return false
	}
	for _, serviceName := range []string{"beszel", "beszel-agent", "dozzle"} {
		if _, ok := document.Services[serviceName]; !ok {
			return false
		}
	}
	for serviceName, prefix := range map[string]string{
		"beszel": "pangolin.public-resources.servestead-beszel.",
		"dozzle": "pangolin.public-resources.servestead-dozzle.",
	} {
		managed := false
		for _, label := range document.Services[serviceName].Labels {
			if strings.HasPrefix(label, prefix) {
				managed = true
				break
			}
		}
		if !managed {
			return false
		}
	}
	return true
}

type repositoryStack struct {
	Name            string
	Compose         string
	MetadataContent string
	Metadata        stackMetadata
	ComposeSHA256   string
	Files           map[string]string
}

func loadCommittedStacks(ctx context.Context, repositoryPath string) ([]repositoryStack, error) {
	output, err := runGit(ctx, repositoryPath, nil, "ls-tree", "-d", "--name-only", "HEAD:stacks")
	if err != nil {
		return nil, err
	}
	stacks := []repositoryStack{}
	for _, name := range strings.Fields(output) {
		stack, include, err := loadCommittedStack(ctx, repositoryPath, name)
		if err != nil {
			return nil, err
		}
		if include {
			stacks = append(stacks, stack)
		}
	}
	return stacks, nil
}

func loadCommittedStack(ctx context.Context, repositoryPath, name string) (repositoryStack, bool, error) {
	if name == "observability" {
		return repositoryStack{}, false, nil
	}
	if !stackSlugPattern.MatchString(name) {
		return repositoryStack{}, false, fmt.Errorf("stack directory %q must be a lowercase DNS label", name)
	}
	base := "stacks/" + name + "/"
	compose, err := runGit(ctx, repositoryPath, nil, "show", "HEAD:"+base+stackComposeFilename)
	if err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s: committed %s is required", name, stackComposeFilename)
	}
	metadataContent, err := runGit(ctx, repositoryPath, nil, "show", "HEAD:"+base+stackMetadataFilename)
	if err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s is not configured; run servestead stack add --profile <id> --compose %s", name, filepath.Join(repositoryPath, filepath.FromSlash(base+stackComposeFilename)))
	}
	var metadata stackMetadata
	if err := yaml.Unmarshal([]byte(metadataContent), &metadata); err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	services, err := inspectComposeServices([]byte(compose))
	if err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s: %w", name, err)
	}
	if err := validateStackMetadata(name, metadata, services); err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	sum := sha256.Sum256([]byte(compose))
	files, err := loadCommittedStackFiles(ctx, repositoryPath, base)
	if err != nil {
		return repositoryStack{}, false, fmt.Errorf("stack %s files: %w", name, err)
	}
	return repositoryStack{
		Name: name, Compose: compose, MetadataContent: metadataContent,
		Metadata: metadata, ComposeSHA256: hex.EncodeToString(sum[:]), Files: files,
	}, true, nil
}

func loadCommittedStackFiles(ctx context.Context, repositoryPath, base string) (map[string]string, error) {
	output, err := runGit(ctx, repositoryPath, nil, "ls-tree", "-r", "--name-only", "HEAD", "--", strings.TrimSuffix(base, "/"))
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, repositoryFile := range strings.Split(strings.TrimSpace(output), "\n") {
		repositoryFile = strings.TrimSpace(repositoryFile)
		relative := strings.TrimPrefix(repositoryFile, base)
		if relative == "" || strings.Contains(relative, "\n") {
			continue
		}
		content, err := runGit(ctx, repositoryPath, nil, "show", "HEAD:"+repositoryFile)
		if err != nil {
			return nil, err
		}
		files[relative] = content
	}
	return files, nil
}

func runGit(ctx context.Context, directory string, extraEnv []string, arguments ...string) (string, error) {
	command, err := newGitCommand(ctx, append([]string{"-C", directory}, arguments...)...)
	if err != nil {
		return "", err
	}
	command.Env = trustedCommandEnvironment(extraEnv)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", arguments[0], detail)
	}
	return stdout.String(), nil
}

func newGitCommand(ctx context.Context, arguments ...string) (*exec.Cmd, error) {
	gitPath, err := trustedGitExecutable()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, gitPath, arguments...)
	command.Env = trustedCommandEnvironment(nil)
	return command, nil
}

func trustedGitExecutable() (string, error) {
	for _, path := range trustedGitExecutablePaths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("git executable not found in trusted system paths")
}

func trustedCommandEnvironment(extraEnv []string) []string {
	env := os.Environ()
	pathSet := false
	for index, value := range env {
		if strings.HasPrefix(value, "PATH=") {
			env[index] = "PATH=" + trustedCommandPath
			pathSet = true
			break
		}
	}
	if !pathSet {
		env = append(env, "PATH="+trustedCommandPath)
	}
	return append(env, extraEnv...)
}

func cloneGitHubRepository(ctx context.Context, repositoryURL, destination, token, profileID string) error {
	if token == "" {
		token = os.Getenv("SERVESTEAD_GITHUB_TOKEN")
	}
	tokenProvided := strings.TrimSpace(token) != ""
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	askpassDirectory, err := os.MkdirTemp("", "servestead-git-askpass-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(askpassDirectory)
	askpassPath := filepath.Join(askpassDirectory, "askpass")
	askpass := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' x-access-token;; *) printf '%s\\n' \"$SERVESTEAD_GITHUB_TOKEN\";; esac\n"
	if err := os.WriteFile(askpassPath, []byte(askpass), 0700); err != nil {
		return err
	}
	command, err := newGitCommand(ctx, "clone", "--", repositoryURL, destination)
	if err != nil {
		return err
	}
	command.Env = trustedCommandEnvironment([]string{
		"GIT_ASKPASS=" + askpassPath,
		"GIT_TERMINAL_PROMPT=0",
		"SERVESTEAD_GITHUB_TOKEN=" + token,
	})
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("clone GitHub repository: %s", githubCheckoutFailureDetail(detail, tokenProvided, profileID))
	}
	return nil
}

func effectiveGitHubToken(secrets ProfileSecrets) (string, string) {
	if token := strings.TrimSpace(os.Getenv("SERVESTEAD_GITHUB_TOKEN")); token != "" {
		return token, "environment"
	}
	if token := strings.TrimSpace(secrets.GitHubToken); token != "" {
		return token, "profile"
	}
	return "", "none"
}

func githubCheckoutFailureDetail(detail string, tokenProvided bool, profileID string) string {
	if !githubCheckoutNeedsTokenGuidance(detail) {
		return detail
	}
	return detail + "\n\n" + githubTokenGuidance(tokenProvided, profileID)
}

func githubCheckoutNeedsTokenGuidance(detail string) bool {
	lower := strings.ToLower(detail)
	for _, marker := range []string{
		"authentication failed",
		"authentication required",
		"could not read username",
		"invalid username or token",
		"password authentication is not supported",
		"rate limit",
		"repository not found",
		"support for password authentication",
		"the requested url returned error: 403",
		"the requested url returned error: 429",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func githubTokenGuidance(tokenProvided bool, profileID string) string {
	commandProfileID := profileID
	if commandProfileID == "" {
		commandProfileID = "<profile-id>"
	}
	var builder strings.Builder
	if tokenProvided {
		builder.WriteString("A GitHub token was provided, but GitHub rejected it or the repository is not accessible.\n")
		builder.WriteString("Check that the token is not expired and can read this repository.\n")
	} else {
		builder.WriteString("No GitHub token was provided.\n")
	}
	builder.WriteString("Private GitHub repositories require a personal access token; public repositories can also use one to avoid anonymous rate limits.\n")
	builder.WriteString("Recommended token: fine-grained PAT, selected repository only, Contents: Read-only.\n")
	builder.WriteString("Store it locally with: ")
	builder.WriteString(githubTokenSetCommand(commandProfileID))
	builder.WriteByte('\n')
	builder.WriteString("Or set SERVESTEAD_GITHUB_TOKEN before launching Servestead.")
	return builder.String()
}

func githubTokenSetCommand(profileID string) string {
	if profileID == "" {
		profileID = "<profile-id>"
	}
	return "servestead github-token set --profile " + profileID + " --file /path/to/token.txt"
}

func validateGitHubRepositoryURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("GitHub repository must be a valid HTTPS URL")
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("GitHub repository must use an uncredentialed https://github.com URL")
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("GitHub repository URL must identify an owner and repository")
	}
	return nil
}

type observabilityComposeDocument struct {
	Services map[string]observabilityComposeService `yaml:"services"`
	Networks map[string]struct {
		External bool `yaml:"external"`
	} `yaml:"networks"`
}

type observabilityComposeService struct {
	Ports    []any    `yaml:"ports"`
	Networks []string `yaml:"networks"`
	Labels   []string `yaml:"labels"`
}

func validateObservabilityCompose(data []byte) error {
	var document observabilityComposeDocument
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(false)
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse %s: %w", observabilityComposeRepositoryPath, err)
	}
	if err := validateObservabilityComposeServices(document); err != nil {
		return err
	}
	return validateObservabilityComposeLabels(document)
}

func validateObservabilityComposeServices(document observabilityComposeDocument) error {
	for _, serviceName := range []string{"beszel", "beszel-agent", "dozzle", "dockhand", "dockhand-socket-proxy"} {
		service, ok := document.Services[serviceName]
		if !ok {
			return fmt.Errorf("%s is incompatible: required service %q is missing; migrate the file and rerun", observabilityComposeRepositoryPath, serviceName)
		}
		if (serviceName == "beszel" || serviceName == "dozzle") && len(service.Ports) != 0 {
			return fmt.Errorf("%s is incompatible: service %q must not publish host ports", observabilityComposeRepositoryPath, serviceName)
		}
		if serviceName != "dockhand-socket-proxy" && !containsString(service.Networks, servesteadPublicNetwork) {
			return fmt.Errorf("%s is incompatible: service %q must use the %s network", observabilityComposeRepositoryPath, serviceName, servesteadPublicNetwork)
		}
	}
	if !document.Networks[servesteadPublicNetwork].External {
		return fmt.Errorf("%s is incompatible: network %q must be external", observabilityComposeRepositoryPath, servesteadPublicNetwork)
	}
	return nil
}

func validateObservabilityComposeLabels(document observabilityComposeDocument) error {
	requiredLabels := map[string][]string{
		"beszel": {
			"pangolin.public-resources.servestead-beszel.name=Beszel",
			"pangolin.public-resources.servestead-beszel.protocol=http",
			"pangolin.public-resources.servestead-beszel.auth.sso-enabled=true",
			"pangolin.public-resources.servestead-beszel.targets[0].hostname=beszel",
			"pangolin.public-resources.servestead-beszel.targets[0].port=8090",
			"pangolin.public-resources.servestead-beszel.targets[0].method=http",
		},
		"dozzle": {
			"pangolin.public-resources.servestead-dozzle.name=Dozzle",
			"pangolin.public-resources.servestead-dozzle.protocol=http",
			"pangolin.public-resources.servestead-dozzle.auth.sso-enabled=true",
			"pangolin.public-resources.servestead-dozzle.targets[0].hostname=dozzle",
			"pangolin.public-resources.servestead-dozzle.targets[0].port=8080",
			"pangolin.public-resources.servestead-dozzle.targets[0].method=http",
		},
		"dockhand": {
			"pangolin.public-resources.servestead-dockhand.name=Dockhand",
			"pangolin.public-resources.servestead-dockhand.protocol=http",
			"pangolin.public-resources.servestead-dockhand.auth.sso-enabled=true",
			"pangolin.public-resources.servestead-dockhand.targets[0].hostname=dockhand",
			"pangolin.public-resources.servestead-dockhand.targets[0].port=3000",
			"pangolin.public-resources.servestead-dockhand.targets[0].method=http",
		},
	}
	for serviceName, required := range requiredLabels {
		labels := document.Services[serviceName].Labels
		for _, label := range required {
			if !containsString(labels, label) {
				return fmt.Errorf("%s is incompatible: required label %q is missing", observabilityComposeRepositoryPath, label)
			}
		}
		prefix := "pangolin.public-resources.servestead-" + serviceName + ".full-domain="
		if !containsPrefix(labels, prefix) {
			return fmt.Errorf("%s is incompatible: required label %q is missing", observabilityComposeRepositoryPath, prefix+"<hostname>")
		}
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}
