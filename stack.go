package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"servestead/resources"
)

const stackMetadataFilename = "servestead.yaml"
const stackComposeFilename = "compose.yaml"
const gitNoExtDiffFlag = "--no-ext-diff"

var stackSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type stackMetadata struct {
	Version         int                   `yaml:"version"`
	PublicResources []stackPublicResource `yaml:"public_resources"`
}

type configuredStack struct {
	Name          string
	Compose       string
	Metadata      string
	Override      string
	ComposeSHA256 string
	Resources     []stackPublicResource
	Files         map[string]string
	Environment   string
}

type stackPublicResource struct {
	ID          string                   `yaml:"id"`
	Service     string                   `yaml:"service"`
	Name        string                   `yaml:"name"`
	Subdomain   string                   `yaml:"subdomain"`
	Port        int                      `yaml:"port"`
	Protocol    string                   `yaml:"protocol"`
	SSO         bool                     `yaml:"sso"`
	Healthcheck stackResourceHealthcheck `yaml:"healthcheck"`
}

type stackResourceHealthcheck struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path,omitempty"`
}

type composeServiceSummary struct {
	Name           string
	ContainerPorts []int
	PublishesPorts bool
}

type stackAddOptions struct {
	ProfileID       string
	Compose         string
	Name            string
	Resources       []stackPublicResource
	EnvironmentFile string
}

type editableStack struct {
	Name            string
	Compose         string
	Metadata        stackMetadata
	Services        []composeServiceSummary
	MetadataMissing bool
}

func loadEditableStacks(repositoryPath string) ([]editableStack, error) {
	stacksDirectory := filepath.Join(expandUserPath(repositoryPath), "stacks")
	entries, err := os.ReadDir(stacksDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stacks := []editableStack{}
	for _, entry := range entries {
		stack, include, err := loadEditableStackEntry(stacksDirectory, entry)
		if err != nil {
			return nil, err
		}
		if include {
			stacks = append(stacks, stack)
		}
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}

func loadEditableStackEntry(stacksDirectory string, entry os.DirEntry) (editableStack, bool, error) {
	if !entry.IsDir() {
		if isStackComposeFilename(entry.Name()) {
			return editableStack{}, false, fmt.Errorf(
				"compose file %s is outside a stack directory; move it to %s or press a in setup to import it",
				filepath.Join("stacks", entry.Name()),
				filepath.Join("stacks", "<stack-name>", stackComposeFilename),
			)
		}
		return editableStack{}, false, nil
	}
	if entry.Name() == "observability" {
		return editableStack{}, false, nil
	}
	if !stackSlugPattern.MatchString(entry.Name()) {
		return editableStack{}, false, fmt.Errorf("stack directory %q must be a lowercase DNS label", entry.Name())
	}
	directory := filepath.Join(stacksDirectory, entry.Name())
	compose, err := readEditableStackCompose(directory, entry.Name())
	if err != nil {
		return editableStack{}, false, err
	}
	if compose == nil {
		return editableStack{}, false, nil
	}
	services, err := inspectComposeServices(compose)
	if err != nil {
		return editableStack{}, false, fmt.Errorf("stack %s: %w", entry.Name(), err)
	}
	metadata, metadataMissing, err := readEditableStackMetadata(directory, entry.Name(), services)
	if err != nil {
		return editableStack{}, false, err
	}
	return editableStack{
		Name: entry.Name(), Compose: string(compose), Metadata: metadata, Services: services,
		MetadataMissing: metadataMissing,
	}, true, nil
}

func readEditableStackCompose(directory, name string) ([]byte, error) {
	compose, err := os.ReadFile(filepath.Join(directory, stackComposeFilename))
	if err == nil {
		return compose, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stack %s: read %s: %w", name, stackComposeFilename, err)
	}
	children, readErr := os.ReadDir(directory)
	if readErr == nil && len(children) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf(
		"stack %s is incomplete: expected %s; move a Compose file there or press a in setup to import one",
		name,
		filepath.Join("stacks", name, stackComposeFilename),
	)
}

func readEditableStackMetadata(directory, name string, services []composeServiceSummary) (stackMetadata, bool, error) {
	metadataData, err := os.ReadFile(filepath.Join(directory, stackMetadataFilename))
	metadata := stackMetadata{Version: 1}
	if errors.Is(err, os.ErrNotExist) {
		return metadata, true, nil
	}
	if err != nil {
		return metadata, false, fmt.Errorf("stack %s: read %s: %w", name, stackMetadataFilename, err)
	}
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return metadata, false, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	if err := validateStackMetadata(name, metadata, services); err != nil {
		return metadata, false, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	return metadata, false, nil
}

func isStackComposeFilename(name string) bool {
	switch strings.ToLower(name) {
	case stackComposeFilename, "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	default:
		return false
	}
}

func writeEditableStack(repositoryPath, originalName string, options stackAddOptions, compose []byte) error {
	services, err := inspectComposeServices(compose)
	if err != nil {
		return err
	}
	metadata := stackMetadata{Version: 1, PublicResources: options.Resources}
	if err := validateStackMetadata(options.Name, metadata, services); err != nil {
		return err
	}
	stacksDirectory := filepath.Join(expandUserPath(repositoryPath), "stacks")
	destination := filepath.Join(stacksDirectory, options.Name)
	if err := prepareEditableStackDestination(stacksDirectory, destination, originalName, options.Name); err != nil {
		return err
	}
	return writeEditableStackFiles(destination, metadata, compose)
}

func prepareEditableStackDestination(stacksDirectory, destination, originalName, name string) error {
	if originalName == "" {
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("stack %q already exists", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if !stackSlugPattern.MatchString(originalName) {
		return errors.New("original stack name must be a lowercase DNS label")
	}
	if originalName == name {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("stack %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source := filepath.Join(stacksDirectory, originalName)
	if err := os.Rename(source, destination); err != nil {
		return fmt.Errorf("rename stack: %w", err)
	}
	return nil
}

func writeEditableStackFiles(destination string, metadata stackMetadata, compose []byte) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destination, stackComposeFilename), compose, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destination, stackMetadataFilename), metadataData, 0600)
}

func removeEditableStack(repositoryPath, name string) error {
	if !stackSlugPattern.MatchString(name) || name == "observability" {
		return errors.New("stack name must be a lowercase DNS label")
	}
	directory := filepath.Join(expandUserPath(repositoryPath), "stacks", name)
	if _, err := os.Stat(filepath.Join(directory, stackMetadataFilename)); err != nil {
		return fmt.Errorf("stack %q is not configured: %w", name, err)
	}
	return os.RemoveAll(directory)
}

func stackRepositoryStatus(ctx context.Context, repositoryPath string) (string, error) {
	status, err := runGit(ctx, expandUserPath(repositoryPath), nil, "status", "--short", "--", "stacks")
	if err != nil {
		return "", err
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return "clean", nil
	}
	return status, nil
}

func stackRepositoryHead(ctx context.Context, repositoryPath string) (string, error) {
	head, err := runGit(ctx, expandUserPath(repositoryPath), nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

func stackRepositoryNeedsPush(ctx context.Context, repositoryPath, head string) (bool, error) {
	remotes, err := runGit(ctx, expandUserPath(repositoryPath), nil, "remote")
	if err != nil {
		return false, err
	}
	hasOrigin := false
	for _, remote := range strings.Fields(remotes) {
		if remote == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		return false, nil
	}
	contains, err := runGit(ctx, expandUserPath(repositoryPath), nil, "branch", "-r", "--contains", head)
	if err != nil {
		return false, err
	}
	return !strings.Contains(contains, gitOriginRemotePrefix), nil
}

func stackRepositoryDiff(ctx context.Context, repositoryPath string) (string, error) {
	repositoryPath = expandUserPath(repositoryPath)
	unstaged, err := runGit(ctx, repositoryPath, nil, "diff", gitNoExtDiffFlag, "--", "stacks")
	if err != nil {
		return "", err
	}
	staged, err := runGit(ctx, repositoryPath, nil, "diff", "--cached", gitNoExtDiffFlag, "--", "stacks")
	if err != nil {
		return "", err
	}
	untracked, err := runGit(ctx, repositoryPath, nil, "ls-files", "-z", "--others", "--exclude-standard", "--", "stacks")
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	appendDiffSection := func(title, content string) {
		if strings.TrimSpace(content) == "" {
			return
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(title + "\n\n" + strings.TrimSpace(content) + "\n")
	}
	appendDiffSection("Unstaged changes", unstaged)
	appendDiffSection("Staged changes", staged)
	for _, name := range strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		diff, err := untrackedFileDiff(ctx, repositoryPath, name)
		if err != nil {
			return "", err
		}
		appendDiffSection("Untracked: "+name, diff)
	}
	if builder.Len() == 0 {
		return "No stack changes.", nil
	}
	return builder.String(), nil
}

func untrackedFileDiff(ctx context.Context, repositoryPath, name string) (string, error) {
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if cleanName == "." || cleanName == "stacks" || !strings.HasPrefix(cleanName, "stacks"+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid untracked stack path %q", name)
	}
	command, err := newGitCommand(ctx, "-C", repositoryPath, "diff", "--no-index", gitNoExtDiffFlag, "--", "/dev/null", cleanName)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err == nil {
		return stdout.String(), nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return stdout.String(), nil
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = err.Error()
	}
	return "", fmt.Errorf("git diff: %s", detail)
}

func stageStackChanges(ctx context.Context, repositoryPath string) error {
	_, err := runGit(ctx, expandUserPath(repositoryPath), nil, "add", "-A", "--", "stacks")
	return err
}

func commitStackChanges(ctx context.Context, repositoryPath, message string) error {
	message = strings.TrimSpace(message)
	if message == "" || strings.ContainsAny(message, "\r\n") {
		return errors.New("commit message must be a non-empty single line")
	}
	staged, err := runGit(ctx, expandUserPath(repositoryPath), nil, "diff", "--cached", "--name-only", "--", "stacks")
	if err != nil {
		return err
	}
	if strings.TrimSpace(staged) == "" {
		return errors.New("no staged stack changes; press g to stage them first")
	}
	_, err = runGit(ctx, expandUserPath(repositoryPath), nil, "commit", "-m", message, "--", "stacks")
	return err
}

func pushStackRepository(ctx context.Context, repositoryPath string) error {
	repositoryPath = expandUserPath(repositoryPath)
	remotes, err := runGit(ctx, repositoryPath, nil, "remote")
	if err != nil {
		return err
	}
	hasOrigin := false
	for _, remote := range strings.Fields(remotes) {
		if remote == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		return errors.New("configuration repository has no origin remote")
	}
	branch, err := runGit(ctx, repositoryPath, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return errors.New("configuration repository is not on a local branch")
	}
	_, err = runGit(ctx, repositoryPath, nil, "push", "--set-upstream", "origin", branch)
	return err
}

func runStack(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(`usage: servestead stack <add|env>`)
	}
	switch args[0] {
	case "add":
		return runStackAdd(ctx, args[1:], stdout, stderr)
	case "env":
		return runStackEnvironment(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown stack command %q", args[0])
	}
}

type stackPublishFlags []string

func (values *stackPublishFlags) String() string { return strings.Join(*values, ",") }

func (values *stackPublishFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runStackAdd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options, services, metadata, environment, environmentKeys, err := stackAddInputs(args, stderr)
	if err != nil {
		return err
	}
	store, profile, state, err := loadStackAddProfile(options)
	if err != nil {
		return err
	}
	override, err := generateStackPangolinOverride(options.Name, metadata, services, profile)
	if err != nil {
		return err
	}
	revision, scaffoldCreated, err := prepareStackAddRepository(ctx, store, profile, state, stdout)
	if err != nil {
		return err
	}
	directory, copiedCompose, err := writeStackAddFiles(store, profile.ID, revision.Path, options, metadata, environment)
	if err != nil {
		return err
	}
	printStackAddSummary(stdout, stackAddSummary{
		Options:         options,
		RepositoryPath:  revision.Path,
		Directory:       directory,
		CopiedCompose:   copiedCompose,
		ScaffoldCreated: scaffoldCreated,
		Override:        override,
		EnvironmentKeys: environmentKeys,
		Metadata:        metadata,
		Services:        services,
		BaseDomain:      profile.BaseDomain,
	})
	return nil
}

func stackAddInputs(args []string, stderr io.Writer) (stackAddOptions, []composeServiceSummary, stackMetadata, string, []string, error) {
	flags := flag.NewFlagSet("stack add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := stackAddOptions{}
	var publications stackPublishFlags
	flags.StringVar(&options.ProfileID, "profile", "", "saved Servestead profile ID")
	flags.StringVar(&options.Compose, "compose", "", "Docker Compose file to add")
	flags.StringVar(&options.Name, "name", "", "stack name used in the repository")
	flags.Var(&publications, "publish", "public route service:port:subdomain[:id] (repeatable)")
	flags.StringVar(&options.EnvironmentFile, "env-file", "", "runtime environment file stored outside Git")
	if err := flags.Parse(args); err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, err
	}
	if flags.NArg() != 0 {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.ProfileID == "" || options.Compose == "" {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, errors.New("--profile and --compose are required")
	}

	composeData, err := os.ReadFile(expandUserPath(options.Compose))
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, fmt.Errorf("read Compose file: %w", err)
	}
	services, err := inspectComposeServices(composeData)
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, err
	}
	options = withStackAddDefaults(options, services)
	options.Resources, err = parseStackPublications(publications)
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, err
	}
	metadata := stackMetadata{Version: 1, PublicResources: options.Resources}
	var environment string
	var environmentKeys []string
	if options.EnvironmentFile != "" {
		environment, environmentKeys, err = readStackEnvironmentFile(options.EnvironmentFile)
		if err != nil {
			return stackAddOptions{}, nil, stackMetadata{}, "", nil, err
		}
	}
	if err := validateStackMetadata(options.Name, metadata, services); err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, "", nil, err
	}
	return options, services, metadata, environment, environmentKeys, nil
}

func loadStackAddProfile(options stackAddOptions) (ProfileStore, Profile, ProfileState, error) {
	store, err := newDefaultProfileStore()
	if err != nil {
		return nil, Profile{}, ProfileState{}, err
	}
	profile, state, err := store.Load(options.ProfileID)
	if err != nil {
		return nil, Profile{}, ProfileState{}, fmt.Errorf("load profile: %w", err)
	}
	if profile.BaseDomain == "" && len(options.Resources) > 0 {
		return nil, Profile{}, ProfileState{}, errors.New("profile base domain is required before adding a public stack")
	}
	return store, profile, state, nil
}

func prepareStackAddRepository(ctx context.Context, store ProfileStore, profile Profile, state ProfileState, stdout io.Writer) (configRepositoryRevision, bool, error) {
	repositoryPath := profile.ConfigRepositoryPath
	if repositoryPath == "" {
		var err error
		repositoryPath, err = defaultConfigRepositoryPath(profile.ID)
		if err != nil {
			return configRepositoryRevision{}, false, err
		}
	}
	fmt.Fprintf(stdout, "Preparing configuration repository at %s...\n", repositoryPath)
	absoluteRepositoryPath, err := filepath.Abs(expandUserPath(repositoryPath))
	if err != nil {
		return configRepositoryRevision{}, false, err
	}
	revision := configRepositoryRevision{Path: absoluteRepositoryPath}
	repositoryScaffold := observabilityComposeFile(observabilityConfig{
		BaseDomain: profile.BaseDomain,
		AdminEmail: firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
	})
	if _, err := os.Stat(filepath.Join(absoluteRepositoryPath, ".git")); errors.Is(err, os.ErrNotExist) {
		revision, err = prepareConfigRepository(ctx, repositoryPath, "", "", profile.ID, repositoryScaffold)
		if err != nil {
			return configRepositoryRevision{}, false, err
		}
	} else if err != nil {
		return configRepositoryRevision{}, false, err
	}
	scaffoldCreated, err := ensureConfigRepositoryScaffold(ctx, revision.Path, repositoryScaffold)
	if err != nil {
		return configRepositoryRevision{}, false, err
	}
	profile.ConfigRepositoryPath = revision.Path
	if err := store.Save(profile, state); err != nil {
		return configRepositoryRevision{}, false, err
	}
	return revision, scaffoldCreated, nil
}

func writeStackAddFiles(store ProfileStore, profileID, repositoryPath string, options stackAddOptions, metadata stackMetadata, environment string) (string, bool, error) {
	directory := filepath.Join(repositoryPath, "stacks", options.Name)
	composeDestination := filepath.Join(directory, stackComposeFilename)
	sourcePath, err := filepath.Abs(expandUserPath(options.Compose))
	if err != nil {
		return "", false, err
	}
	destinationPath, err := filepath.Abs(composeDestination)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(filepath.Join(directory, stackMetadataFilename)); err == nil {
		return "", false, fmt.Errorf("stack %q is already configured at %s", options.Name, directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", false, err
	}
	copiedCompose := sourcePath != destinationPath
	if err := copyStackAddCompose(sourcePath, destinationPath, composeDestination, options.Name); err != nil {
		return "", false, err
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(filepath.Join(directory, stackMetadataFilename), metadataData, 0600); err != nil {
		return "", false, err
	}
	if options.EnvironmentFile != "" {
		if err := saveStackEnvironment(store, profileID, options.Name, environment); err != nil {
			return "", false, err
		}
	}
	return directory, copiedCompose, nil
}

func copyStackAddCompose(sourcePath, destinationPath, composeDestination, name string) error {
	if sourcePath == destinationPath {
		return nil
	}
	if _, err := os.Stat(composeDestination); err == nil {
		return fmt.Errorf("stack %q already has a %s", name, stackComposeFilename)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return copyFile(sourcePath, composeDestination, 0600)
}

type stackAddSummary struct {
	Options         stackAddOptions
	RepositoryPath  string
	Directory       string
	CopiedCompose   bool
	ScaffoldCreated bool
	Override        string
	EnvironmentKeys []string
	Metadata        stackMetadata
	Services        []composeServiceSummary
	BaseDomain      string
}

func printStackAddSummary(stdout io.Writer, summary stackAddSummary) {
	if summary.Options.EnvironmentFile != "" {
		fmt.Fprintf(stdout, "Runtime environment saved outside Git (%d keys).\n", len(summary.EnvironmentKeys))
	}
	fmt.Fprintf(stdout, "Stack scaffold created: %s\n", summary.Directory)
	if summary.ScaffoldCreated {
		fmt.Fprintln(stdout, "The managed observability scaffold was prepared in the same change set.")
	}
	if summary.CopiedCompose {
		fmt.Fprintln(stdout, "Only the Compose file was imported. Copy any relative bind-mount, env, or configuration files into the stack directory before committing.")
	}
	if len(summary.Metadata.PublicResources) == 0 {
		fmt.Fprintln(stdout, "Public resources: none; this stack will remain private.")
	}
	for _, resource := range summary.Metadata.PublicResources {
		fmt.Fprintf(stdout, "Public resource: https://%s.%s -> %s:%d\n", resource.Subdomain, summary.BaseDomain, resource.Service, resource.Port)
	}
	fmt.Fprintln(stdout, "Review the imported Compose file for literal secrets; Servestead does not move application-specific secrets out of Git.")
	for _, resource := range summary.Metadata.PublicResources {
		if servicePublishesPorts(summary.Services, resource.Service) {
			fmt.Fprintf(stdout, "Servestead will suppress %s's direct host port bindings in its generated deployment override.\n", resource.Service)
		}
	}
	fmt.Fprintln(stdout, "Servestead will generate and validate these deployment labels:")
	for _, label := range pangolinLabelsFromOverride(summary.Override) {
		fmt.Fprintf(stdout, "  %s\n", label)
	}
	fmt.Fprintln(stdout, "\nReview the complete configuration change, then commit it once. Servestead deploys committed configuration only:")
	fmt.Fprintf(stdout, "  git -C %s add stacks\n", shellQuote(summary.RepositoryPath))
	fmt.Fprintf(stdout, "  git -C %s commit -m %s\n", shellQuote(summary.RepositoryPath), shellQuote("Add "+summary.Options.Name+" stack"))
	fmt.Fprintln(stdout, "Then open the profile dashboard, press s, select this stack, and press r to deploy it independently.")
}

func runStackEnvironment(args []string, stdout, stderr io.Writer) error {
	action, profileID, stackName, path, err := stackEnvironmentInputs(args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	if err := ensureStackEnvironmentTarget(store, profileID, stackName); err != nil {
		return err
	}
	if action == "remove" {
		return removeStackEnvironment(store, profileID, stackName, path, stdout)
	}
	return setStackEnvironment(store, profileID, stackName, path, stdout)
}

func stackEnvironmentInputs(args []string, stderr io.Writer) (string, string, string, string, error) {
	if len(args) == 0 || !isStackEnvironmentAction(args[0]) {
		return "", "", "", "", errors.New(`usage: servestead stack env <set|remove> --profile <id> --stack <name> [--file <path>]`)
	}
	action := args[0]
	flags := flag.NewFlagSet("stack env "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var profileID, stackName, path string
	flags.StringVar(&profileID, "profile", "", "saved Servestead profile ID")
	flags.StringVar(&stackName, "stack", "", "stack name")
	flags.StringVar(&path, "file", "", "environment file")
	if err := flags.Parse(args[1:]); err != nil {
		return "", "", "", "", err
	}
	if flags.NArg() != 0 {
		return "", "", "", "", fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if profileID == "" || !stackSlugPattern.MatchString(stackName) {
		return "", "", "", "", errors.New("--profile and a valid --stack are required")
	}
	return action, profileID, stackName, path, nil
}

func isStackEnvironmentAction(action string) bool {
	return action == "set" || action == "remove"
}

func ensureStackEnvironmentTarget(store ProfileStore, profileID, stackName string) error {
	profile, _, err := store.Load(profileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	if profile.ConfigRepositoryPath == "" {
		return errors.New("profile configuration repository is not ready")
	}
	metadataPath := filepath.Join(expandUserPath(profile.ConfigRepositoryPath), "stacks", stackName, stackMetadataFilename)
	if _, err := os.Stat(metadataPath); err != nil {
		return fmt.Errorf("stack %q is not configured: %w", stackName, err)
	}
	return nil
}

func removeStackEnvironment(store ProfileStore, profileID, stackName, path string, stdout io.Writer) error {
	if path != "" {
		return errors.New("--file cannot be used with env remove")
	}
	if err := saveStackEnvironment(store, profileID, stackName, ""); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed the runtime environment for %s. Deploy or synchronize the stack to apply it.\n", stackName)
	return nil
}

func setStackEnvironment(store ProfileStore, profileID, stackName, path string, stdout io.Writer) error {
	if path == "" {
		return errors.New("--file is required with env set")
	}
	environment, keys, err := readStackEnvironmentFile(path)
	if err != nil {
		return err
	}
	if err := saveStackEnvironment(store, profileID, stackName, environment); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved %d runtime environment keys for %s outside Git: %s\n", len(keys), stackName, strings.Join(keys, ", "))
	fmt.Fprintln(stdout, "Deploy or synchronize the stack to apply the environment.")
	return nil
}

func parseStackPublications(values []string) ([]stackPublicResource, error) {
	resources := make([]stackPublicResource, 0, len(values))
	for _, value := range values {
		parts := strings.Split(value, ":")
		if len(parts) != 3 && len(parts) != 4 {
			return nil, fmt.Errorf("publication %q must use service:port:subdomain[:id]", value)
		}
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("publication %q has an invalid port", value)
		}
		id := parts[0]
		if len(parts) == 4 {
			id = parts[3]
		}
		resources = append(resources, stackPublicResource{
			ID: id, Service: parts[0], Port: port, Subdomain: parts[2],
			Name: titleFromSlug(parts[2]), Protocol: "http", SSO: true,
			Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"},
		})
	}
	return resources, nil
}

func readStackEnvironmentFile(path string) (string, []string, error) {
	data, err := os.ReadFile(expandUserPath(path))
	if err != nil {
		return "", nil, fmt.Errorf("read environment file: %w", err)
	}
	return readStackEnvironmentContent(string(data))
}

func readStackEnvironmentContent(content string) (string, []string, error) {
	if strings.IndexByte(content, 0) >= 0 {
		return "", nil, errors.New("environment file contains a NUL byte")
	}
	keys := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, _, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !environmentKeyPattern.MatchString(key) {
			continue
		}
		if !containsString(keys, key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	environment := content
	if environment != "" && !strings.HasSuffix(environment, "\n") {
		environment += "\n"
	}
	return environment, keys, nil
}

func saveStackEnvironment(store ProfileStore, profileID, stackName, environment string) error {
	if !stackSlugPattern.MatchString(stackName) {
		return errors.New("stack name must be a lowercase DNS label")
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	if secrets.StackEnvironments == nil {
		secrets.StackEnvironments = map[string]string{}
	}
	if environment == "" {
		delete(secrets.StackEnvironments, stackName)
	} else {
		secrets.StackEnvironments[stackName] = environment
	}
	return store.SaveSecrets(profileID, secrets)
}

func inspectComposeServices(data []byte) ([]composeServiceSummary, error) {
	var document struct {
		Services map[string]struct {
			Expose []any `yaml:"expose"`
			Ports  []any `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse Compose file: %w", err)
	}
	if len(document.Services) == 0 {
		return nil, errors.New("Compose file has no services")
	}
	services := make([]composeServiceSummary, 0, len(document.Services))
	for name, service := range document.Services {
		ports := []int{}
		for _, value := range service.Expose {
			if port := composeContainerPort(value); port > 0 {
				ports = appendUniqueInt(ports, port)
			}
		}
		for _, value := range service.Ports {
			if port := composeContainerPort(value); port > 0 {
				ports = appendUniqueInt(ports, port)
			}
		}
		sort.Ints(ports)
		services = append(services, composeServiceSummary{
			Name:           name,
			ContainerPorts: ports,
			PublishesPorts: len(service.Ports) > 0,
		})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services, nil
}

func composeContainerPort(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case string:
		value := strings.TrimSuffix(typed, "/tcp")
		value = strings.TrimSuffix(value, "/udp")
		parts := strings.Split(value, ":")
		port, _ := strconv.Atoi(parts[len(parts)-1])
		return port
	case map[string]any:
		return composeContainerPort(typed["target"])
	case map[any]any:
		return composeContainerPort(typed["target"])
	default:
		return 0
	}
}

func withStackAddDefaults(options stackAddOptions, services []composeServiceSummary) stackAddOptions {
	if options.Name == "" {
		base := strings.TrimSuffix(filepath.Base(options.Compose), filepath.Ext(options.Compose))
		if base == "compose" || base == "docker-compose" {
			base = filepath.Base(filepath.Dir(options.Compose))
		}
		options.Name = slugifyStackValue(base)
	}
	return options
}

func suggestedStackResource(stackName string, services []composeServiceSummary) (stackPublicResource, bool) {
	if len(services) != 1 || len(services[0].ContainerPorts) != 1 {
		return stackPublicResource{}, false
	}
	service := services[0]
	return stackPublicResource{
		ID: service.Name, Service: service.Name, Name: titleFromSlug(stackName),
		Subdomain: stackName, Port: service.ContainerPorts[0], Protocol: "http", SSO: true,
		Healthcheck: stackResourceHealthcheck{Enabled: true, Path: "/"},
	}, true
}

func validateStackResource(resource stackPublicResource, services []composeServiceSummary) error {
	if !stackSlugPattern.MatchString(resource.Subdomain) {
		return errors.New("subdomain must be a lowercase DNS label")
	}
	if resource.Name == "" {
		return errors.New("display name is required")
	}
	if strings.ContainsAny(resource.Name, "\r\n") || strings.ContainsAny(resource.Healthcheck.Path, "\r\n") {
		return errors.New("display name and health-check path must be single-line values")
	}
	if resource.Port < 1 || resource.Port > 65535 {
		return errors.New("service port must be between 1 and 65535")
	}
	found := false
	for _, service := range services {
		if service.Name == resource.Service {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("service %q does not exist in the Compose file", resource.Service)
	}
	if resource.Healthcheck.Path != "" && !strings.HasPrefix(resource.Healthcheck.Path, "/") {
		return errors.New("health-check path must start with /")
	}
	return nil
}

func validateStackMetadata(stackName string, metadata stackMetadata, services []composeServiceSummary) error {
	if metadata.Version != 1 {
		return fmt.Errorf("unsupported stack metadata version %d", metadata.Version)
	}
	if !stackSlugPattern.MatchString(stackName) {
		return errors.New("stack name must be a lowercase DNS label")
	}
	ids := map[string]bool{}
	subdomains := map[string]bool{}
	for _, resource := range metadata.PublicResources {
		if err := validateStackMetadataResource(resource, services, ids, subdomains); err != nil {
			return fmt.Errorf("resource %q: %w", resource.ID, err)
		}
	}
	return nil
}

func validateStackMetadataResource(resource stackPublicResource, services []composeServiceSummary, ids, subdomains map[string]bool) error {
	if !stackSlugPattern.MatchString(resource.ID) {
		return fmt.Errorf("resource ID %q must be a lowercase DNS label", resource.ID)
	}
	if ids[resource.ID] {
		return fmt.Errorf("resource ID %q is duplicated", resource.ID)
	}
	ids[resource.ID] = true
	if subdomains[resource.Subdomain] {
		return fmt.Errorf("resource subdomain %q is duplicated", resource.Subdomain)
	}
	subdomains[resource.Subdomain] = true
	if resource.Protocol != "http" && resource.Protocol != "https" {
		return fmt.Errorf("resource %q protocol must be http or https", resource.ID)
	}
	if resource.Healthcheck.Enabled && resource.Healthcheck.Path == "" {
		return fmt.Errorf("resource %q enables health checks but has no path", resource.ID)
	}
	return validateStackResource(resource, services)
}

func generateStackPangolinOverride(stackName string, metadata stackMetadata, services []composeServiceSummary, profile Profile) (string, error) {
	if err := validateStackMetadata(stackName, metadata, services); err != nil {
		return "", err
	}
	resourcesByService := map[string][]stackPublicResource{}
	for _, resource := range metadata.PublicResources {
		resourcesByService[resource.Service] = append(resourcesByService[resource.Service], resource)
	}
	type stackOverrideService struct {
		Labels     []string
		Name       string
		Public     bool
		ResetPorts bool
	}
	overrideServices := make([]stackOverrideService, 0, len(services))
	for _, serviceSummary := range services {
		service := serviceSummary.Name
		resources := resourcesByService[service]
		overrideService := stackOverrideService{
			Name:       service,
			Public:     len(resources) > 0,
			ResetPorts: len(resources) > 0 && servicePublishesPorts(services, service),
		}
		labels, err := stackPangolinOverrideLabels(stackName, resources, profile)
		if err != nil {
			return "", err
		}
		overrideService.Labels = labels
		overrideServices = append(overrideServices, overrideService)
	}
	return mustRenderResourceTemplate(resources.StackPangolinOverride, struct {
		ServesteadPublicNetwork string
		HasPublicResources      bool
		Services                []stackOverrideService
	}{
		ServesteadPublicNetwork: servesteadPublicNetwork,
		HasPublicResources:      len(metadata.PublicResources) > 0,
		Services:                overrideServices,
	}), nil
}

func stackPangolinOverrideLabels(stackName string, resources []stackPublicResource, profile Profile) ([]string, error) {
	labels := []string{}
	for _, resource := range resources {
		resourceLabels, err := stackPangolinResourceLabels(stackName, resource, profile)
		if err != nil {
			return nil, err
		}
		labels = append(labels, resourceLabels...)
	}
	return labels, nil
}

func stackPangolinResourceLabels(stackName string, resource stackPublicResource, profile Profile) ([]string, error) {
	adminEmail := firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail)
	if resource.SSO && adminEmail == "" {
		return nil, fmt.Errorf("resource %q enables SSO but the profile has no Pangolin administrator email", resource.ID)
	}
	prefix := "pangolin.public-resources.servestead-" + stackName + "-" + resource.ID
	labels := []string{
		prefix + ".name=" + resource.Name,
		prefix + ".protocol=" + resource.Protocol,
		prefix + ".full-domain=" + resource.Subdomain + "." + profile.BaseDomain,
		prefix + ".auth.sso-enabled=" + strconv.FormatBool(resource.SSO),
		prefix + ".targets[0].hostname=" + resource.Service,
		prefix + ".targets[0].port=" + strconv.Itoa(resource.Port),
		prefix + ".targets[0].method=" + resource.Protocol,
	}
	if resource.SSO {
		labels = append(labels, prefix+".auth.sso-users[0]="+adminEmail)
	}
	if resource.Healthcheck.Enabled {
		labels = append(labels,
			prefix+".targets[0].healthcheck.enabled=true",
			prefix+".targets[0].healthcheck.hostname="+resource.Service,
			prefix+".targets[0].healthcheck.port="+strconv.Itoa(resource.Port),
			prefix+".targets[0].healthcheck.scheme="+resource.Protocol,
			prefix+".targets[0].healthcheck.path="+resource.Healthcheck.Path,
		)
	}
	return labels, nil
}

func servicePublishesPorts(services []composeServiceSummary, name string) bool {
	for _, service := range services {
		if service.Name == name {
			return service.PublishesPorts
		}
	}
	return false
}

func validateConfiguredStackSet(stacks []configuredStack) error {
	domains := map[string]string{
		"beszel":   "observability",
		"dozzle":   "observability",
		"pangolin": "proxy",
	}
	resourceIDs := map[string]string{}
	for _, stack := range stacks {
		for _, resource := range stack.Resources {
			if owner, exists := domains[resource.Subdomain]; exists {
				return fmt.Errorf("stack %s subdomain %q conflicts with %s", stack.Name, resource.Subdomain, owner)
			}
			domains[resource.Subdomain] = stack.Name
			resourceID := "servestead-" + stack.Name + "-" + resource.ID
			if owner, exists := resourceIDs[resourceID]; exists {
				return fmt.Errorf("stack %s resource ID %q conflicts with stack %s", stack.Name, resourceID, owner)
			}
			resourceIDs[resourceID] = stack.Name
		}
	}
	return nil
}

func pangolinLabelsFromOverride(override string) []string {
	labels := []string{}
	for _, line := range strings.Split(override, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `- "pangolin.`) {
			labels = append(labels, strings.TrimSuffix(strings.TrimPrefix(line, `- "`), `"`))
		}
	}
	return labels
}

func appendUniqueInt(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func slugifyStackValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	hyphen := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
			hyphen = false
		} else if builder.Len() > 0 && !hyphen {
			builder.WriteByte('-')
			hyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func titleFromSlug(value string) string {
	parts := strings.Split(value, "-")
	for index, part := range parts {
		if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func copyFile(source, destination string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, mode)
}
