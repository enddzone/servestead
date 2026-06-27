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
	"strings"

	"gopkg.in/yaml.v3"
)

const observabilityComposeRepositoryPath = "stacks/observability/compose.yaml"

var errRepositoryReviewRequired = errors.New("configuration repository scaffold requires review")

type configRepositoryRevision struct {
	Path       string
	Commit     string
	Compose    string
	ComposeSHA string
	Origin     string
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
	return filepath.Join(configDirectory, "aegisnode", "repositories", profileID), nil
}

func prepareDeclarativeSetup(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, config setupConfig) (Profile, setupConfig, error) {
	path := firstNonEmpty(config.ConfigRepositoryPath, profile.ConfigRepositoryPath)
	if path == "" {
		if fileStore, ok := store.(*fileProfileStore); ok && !fileStore.defaultRoot {
			path = filepath.Join(fileStore.root, "repositories", profile.ID)
		} else {
			var err error
			path, err = defaultConfigRepositoryPath(profile.ID)
			if err != nil {
				return profile, config, err
			}
		}
	}
	absolutePath, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return profile, config, fmt.Errorf("resolve configuration repository path: %w", err)
	}
	path = absolutePath
	scaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: config.BaseDomain,
		AdminEmail: config.PangolinAdminEmail,
	})
	revision, err := prepareConfigRepository(
		ctx,
		path,
		config.GitHubRepositoryURL,
		os.Getenv("AEGISNODE_GITHUB_TOKEN"),
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
	config.ConfigRepositoryOrigin = revision.Origin
	config.ConfigRepositoryCompose = revision.Compose
	config.ConfigRepositorySHA256 = revision.ComposeSHA
	config.GitHubToken = os.Getenv("AEGISNODE_GITHUB_TOKEN")
	if err := store.Save(profile, state); err != nil {
		return profile, config, err
	}
	return profile, config, nil
}

func prepareConfigRepository(ctx context.Context, path, githubURL, token, profileID, scaffold string) (configRepositoryRevision, error) {
	if githubURL != "" {
		if err := validateGitHubRepositoryURL(githubURL); err != nil {
			return configRepositoryRevision{}, err
		}
	}
	if path == "" {
		var err error
		path, err = defaultConfigRepositoryPath(profileID)
		if err != nil {
			return configRepositoryRevision{}, err
		}
	}
	path, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return configRepositoryRevision{}, fmt.Errorf("resolve configuration repository path: %w", err)
	}

	_, statErr := os.Stat(path)
	existed := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return configRepositoryRevision{}, statErr
	}
	if githubURL != "" && !existed {
		if err := cloneGitHubRepository(ctx, githubURL, path, token); err != nil {
			return configRepositoryRevision{}, err
		}
		existed = true
	}

	gitDirectory := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDirectory); errors.Is(err, os.ErrNotExist) {
		if existed {
			return configRepositoryRevision{}, fmt.Errorf("%s is not a Git repository", path)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return configRepositoryRevision{}, err
		}
		if _, err := runGit(ctx, path, nil, "init", "-b", "main"); err != nil {
			return configRepositoryRevision{}, err
		}
	} else if err != nil {
		return configRepositoryRevision{}, err
	}

	composePath := filepath.Join(path, filepath.FromSlash(observabilityComposeRepositoryPath))
	if _, err := os.Stat(composePath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(composePath), 0700); err != nil {
			return configRepositoryRevision{}, err
		}
		if err := os.WriteFile(composePath, []byte(scaffold), 0600); err != nil {
			return configRepositoryRevision{}, err
		}
		if existed {
			return configRepositoryRevision{}, fmt.Errorf("%w: created %s; commit it and rerun AegisNode", errRepositoryReviewRequired, composePath)
		}
		env := []string{
			"GIT_AUTHOR_NAME=AegisNode",
			"GIT_AUTHOR_EMAIL=aegisnode@localhost",
			"GIT_COMMITTER_NAME=AegisNode",
			"GIT_COMMITTER_EMAIL=aegisnode@localhost",
		}
		if _, err := runGit(ctx, path, env, "add", "--", observabilityComposeRepositoryPath); err != nil {
			return configRepositoryRevision{}, err
		}
		if _, err := runGit(ctx, path, env, "commit", "-m", "Initialize AegisNode configuration"); err != nil {
			return configRepositoryRevision{}, err
		}
	} else if err != nil {
		return configRepositoryRevision{}, err
	}

	status, err := runGit(ctx, path, nil, "status", "--porcelain", "--", observabilityComposeRepositoryPath)
	if err != nil {
		return configRepositoryRevision{}, err
	}
	if strings.TrimSpace(status) != "" {
		return configRepositoryRevision{}, fmt.Errorf("uncommitted changes in %s block deployment", observabilityComposeRepositoryPath)
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
	if output, err := runGit(ctx, path, nil, "remote", "get-url", "origin"); err == nil {
		origin = strings.TrimSpace(output)
		if err := validateGitHubRepositoryURL(origin); err != nil {
			return configRepositoryRevision{}, fmt.Errorf("origin: %w", err)
		}
		if githubURL != "" && strings.TrimSuffix(origin, ".git") != strings.TrimSuffix(githubURL, ".git") {
			return configRepositoryRevision{}, fmt.Errorf("origin %q does not match --github-repo %q", origin, githubURL)
		}
		contains, err := runGit(ctx, path, nil, "branch", "-r", "--contains", strings.TrimSpace(commit))
		if err != nil {
			return configRepositoryRevision{}, err
		}
		if !strings.Contains(contains, "origin/") {
			return configRepositoryRevision{}, errors.New("configuration commit has not been pushed to origin")
		}
	}
	if githubURL != "" && origin == "" {
		return configRepositoryRevision{}, errors.New("--github-repo requires the configuration checkout to have a GitHub origin")
	}

	sum := sha256.Sum256([]byte(compose))
	return configRepositoryRevision{
		Path:       path,
		Commit:     strings.TrimSpace(commit),
		Compose:    compose,
		ComposeSHA: hex.EncodeToString(sum[:]),
		Origin:     origin,
	}, nil
}

func runGit(ctx context.Context, directory string, extraEnv []string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), extraEnv...)
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

func cloneGitHubRepository(ctx context.Context, repositoryURL, destination, token string) error {
	if token == "" {
		token = os.Getenv("AEGISNODE_GITHUB_TOKEN")
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	askpassDirectory, err := os.MkdirTemp("", "aegisnode-git-askpass-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(askpassDirectory)
	askpassPath := filepath.Join(askpassDirectory, "askpass")
	askpass := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' x-access-token;; *) printf '%s\\n' \"$AEGISNODE_GITHUB_TOKEN\";; esac\n"
	if err := os.WriteFile(askpassPath, []byte(askpass), 0700); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "git", "clone", "--", repositoryURL, destination)
	command.Env = append(os.Environ(),
		"GIT_ASKPASS="+askpassPath,
		"GIT_TERMINAL_PROMPT=0",
		"AEGISNODE_GITHUB_TOKEN="+token,
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("clone GitHub repository: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
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

func validateObservabilityCompose(data []byte) error {
	var document struct {
		Services map[string]struct {
			Ports    []any    `yaml:"ports"`
			Networks []string `yaml:"networks"`
			Labels   []string `yaml:"labels"`
		} `yaml:"services"`
		Networks map[string]struct {
			External bool `yaml:"external"`
		} `yaml:"networks"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(false)
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse %s: %w", observabilityComposeRepositoryPath, err)
	}
	for _, serviceName := range []string{"beszel", "beszel-agent", "dozzle"} {
		service, ok := document.Services[serviceName]
		if !ok {
			return fmt.Errorf("%s is incompatible: required service %q is missing; migrate the file and rerun", observabilityComposeRepositoryPath, serviceName)
		}
		if (serviceName == "beszel" || serviceName == "dozzle") && len(service.Ports) != 0 {
			return fmt.Errorf("%s is incompatible: service %q must not publish host ports", observabilityComposeRepositoryPath, serviceName)
		}
		if !containsString(service.Networks, aegisPublicNetwork) {
			return fmt.Errorf("%s is incompatible: service %q must use the %s network", observabilityComposeRepositoryPath, serviceName, aegisPublicNetwork)
		}
	}
	if !document.Networks[aegisPublicNetwork].External {
		return fmt.Errorf("%s is incompatible: network %q must be external", observabilityComposeRepositoryPath, aegisPublicNetwork)
	}
	requiredLabels := map[string][]string{
		"beszel": {
			"pangolin.public-resources.aegisnode-beszel.name=Beszel",
			"pangolin.public-resources.aegisnode-beszel.protocol=http",
			"pangolin.public-resources.aegisnode-beszel.auth.sso-enabled=true",
			"pangolin.public-resources.aegisnode-beszel.targets[0].hostname=beszel",
			"pangolin.public-resources.aegisnode-beszel.targets[0].port=8090",
			"pangolin.public-resources.aegisnode-beszel.targets[0].method=http",
		},
		"dozzle": {
			"pangolin.public-resources.aegisnode-dozzle.name=Dozzle",
			"pangolin.public-resources.aegisnode-dozzle.protocol=http",
			"pangolin.public-resources.aegisnode-dozzle.auth.sso-enabled=true",
			"pangolin.public-resources.aegisnode-dozzle.targets[0].hostname=dozzle",
			"pangolin.public-resources.aegisnode-dozzle.targets[0].port=8080",
			"pangolin.public-resources.aegisnode-dozzle.targets[0].method=http",
		},
	}
	for serviceName, required := range requiredLabels {
		labels := document.Services[serviceName].Labels
		for _, label := range required {
			if !containsString(labels, label) {
				return fmt.Errorf("%s is incompatible: required label %q is missing", observabilityComposeRepositoryPath, label)
			}
		}
		prefix := "pangolin.public-resources.aegisnode-" + serviceName + ".full-domain="
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
