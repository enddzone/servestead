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

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

const stackMetadataFilename = "aegisnode.yaml"

var stackSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

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
}

type stackPublicResource struct {
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
	ProfileID   string
	Compose     string
	Name        string
	Service     string
	Port        int
	Subdomain   string
	DisplayName string
	HealthPath  string
	SSO         bool
	Yes         bool
}

type editableStack struct {
	Name     string
	Compose  string
	Metadata stackMetadata
	Services []composeServiceSummary
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
		if !entry.IsDir() || entry.Name() == "observability" {
			continue
		}
		if !stackSlugPattern.MatchString(entry.Name()) {
			return nil, fmt.Errorf("stack directory %q must be a lowercase DNS label", entry.Name())
		}
		directory := filepath.Join(stacksDirectory, entry.Name())
		compose, err := os.ReadFile(filepath.Join(directory, "compose.yaml"))
		if err != nil {
			return nil, fmt.Errorf("stack %s: read compose.yaml: %w", entry.Name(), err)
		}
		services, err := inspectComposeServices(compose)
		if err != nil {
			return nil, fmt.Errorf("stack %s: %w", entry.Name(), err)
		}
		metadataData, err := os.ReadFile(filepath.Join(directory, stackMetadataFilename))
		if err != nil {
			return nil, fmt.Errorf("stack %s: read %s: %w", entry.Name(), stackMetadataFilename, err)
		}
		var metadata stackMetadata
		if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
			return nil, fmt.Errorf("stack %s metadata: %w", entry.Name(), err)
		}
		stacks = append(stacks, editableStack{
			Name: entry.Name(), Compose: string(compose), Metadata: metadata, Services: services,
		})
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}

func writeEditableStack(repositoryPath, originalName string, options stackAddOptions, compose []byte) error {
	services, err := inspectComposeServices(compose)
	if err != nil {
		return err
	}
	if err := validateStackAddOptions(options, services); err != nil {
		return err
	}
	stacksDirectory := filepath.Join(expandUserPath(repositoryPath), "stacks")
	destination := filepath.Join(stacksDirectory, options.Name)
	if originalName == "" {
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("stack %q already exists", options.Name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		if !stackSlugPattern.MatchString(originalName) {
			return errors.New("original stack name must be a lowercase DNS label")
		}
		source := filepath.Join(stacksDirectory, originalName)
		if originalName != options.Name {
			if _, err := os.Stat(destination); err == nil {
				return fmt.Errorf("stack %q already exists", options.Name)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Rename(source, destination); err != nil {
				return fmt.Errorf("rename stack: %w", err)
			}
		}
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	metadata := stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{{
			Service: options.Service, Name: options.DisplayName, Subdomain: options.Subdomain,
			Port: options.Port, Protocol: "http", SSO: options.SSO,
			Healthcheck: stackResourceHealthcheck{Enabled: options.HealthPath != "", Path: options.HealthPath},
		}},
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(destination, "compose.yaml"), compose, 0600); err != nil {
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
	return !strings.Contains(contains, "origin/"), nil
}

func stackRepositoryDiff(ctx context.Context, repositoryPath string) (string, error) {
	repositoryPath = expandUserPath(repositoryPath)
	unstaged, err := runGit(ctx, repositoryPath, nil, "diff", "--no-ext-diff", "--", "stacks")
	if err != nil {
		return "", err
	}
	staged, err := runGit(ctx, repositoryPath, nil, "diff", "--cached", "--no-ext-diff", "--", "stacks")
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
	command := exec.CommandContext(ctx, "git", "-C", repositoryPath, "diff", "--no-index", "--no-ext-diff", "--", "/dev/null", cleanName)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
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
	if len(args) == 0 || args[0] != "add" {
		return errors.New(`usage: aegisnode stack add --profile <id> --compose <path>`)
	}
	flags := flag.NewFlagSet("stack add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := stackAddOptions{SSO: true, HealthPath: "/"}
	flags.StringVar(&options.ProfileID, "profile", "", "saved AegisNode profile ID")
	flags.StringVar(&options.Compose, "compose", "", "Docker Compose file to add")
	flags.StringVar(&options.Name, "name", "", "stack name used in the repository")
	flags.StringVar(&options.Service, "service", "", "Compose service to publish")
	flags.IntVar(&options.Port, "port", 0, "service container port")
	flags.StringVar(&options.Subdomain, "subdomain", "", "public subdomain")
	flags.StringVar(&options.DisplayName, "display-name", "", "Pangolin resource display name")
	flags.StringVar(&options.HealthPath, "health-path", "/", "HTTP health-check path")
	flags.BoolVar(&options.SSO, "sso", true, "require Pangolin SSO")
	flags.BoolVar(&options.Yes, "yes", false, "run non-interactively")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.ProfileID == "" || options.Compose == "" {
		return errors.New("--profile and --compose are required")
	}

	composeData, err := os.ReadFile(expandUserPath(options.Compose))
	if err != nil {
		return fmt.Errorf("read Compose file: %w", err)
	}
	services, err := inspectComposeServices(composeData)
	if err != nil {
		return err
	}
	options = withStackAddDefaults(options, services)
	if !options.Yes && isInteractiveWriter(stderr) {
		options, err = collectStackAddOptions(options, services, stderr)
		if err != nil {
			return err
		}
	}
	if err := validateStackAddOptions(options, services); err != nil {
		return err
	}

	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, state, err := store.Load(options.ProfileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	if profile.BaseDomain == "" {
		return errors.New("profile base domain is required before adding a public stack")
	}
	repositoryPath := profile.ConfigRepositoryPath
	if repositoryPath == "" {
		repositoryPath, err = defaultConfigRepositoryPath(profile.ID)
		if err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "Preparing configuration repository at %s...\n", repositoryPath)
	absoluteRepositoryPath, err := filepath.Abs(expandUserPath(repositoryPath))
	if err != nil {
		return err
	}
	revision := configRepositoryRevision{Path: absoluteRepositoryPath}
	if _, err := os.Stat(filepath.Join(absoluteRepositoryPath, ".git")); errors.Is(err, os.ErrNotExist) {
		revision, err = prepareConfigRepository(ctx, repositoryPath, "", "", profile.ID, observabilityComposeFile(observabilityConfig{
			BaseDomain: profile.BaseDomain,
			AdminEmail: firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
		}))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	profile.ConfigRepositoryPath = revision.Path
	if err := store.Save(profile, state); err != nil {
		return err
	}

	directory := filepath.Join(revision.Path, "stacks", options.Name)
	composeDestination := filepath.Join(directory, "compose.yaml")
	sourcePath, err := filepath.Abs(expandUserPath(options.Compose))
	if err != nil {
		return err
	}
	destinationPath, err := filepath.Abs(composeDestination)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(directory, stackMetadataFilename)); err == nil {
		return fmt.Errorf("stack %q is already configured at %s", options.Name, directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if sourcePath != destinationPath {
		if _, err := os.Stat(composeDestination); err == nil {
			return fmt.Errorf("stack %q already has a compose.yaml", options.Name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := copyFile(sourcePath, composeDestination, 0600); err != nil {
			return err
		}
	}
	metadata := stackMetadata{
		Version: 1,
		PublicResources: []stackPublicResource{{
			Service:   options.Service,
			Name:      options.DisplayName,
			Subdomain: options.Subdomain,
			Port:      options.Port,
			Protocol:  "http",
			SSO:       options.SSO,
			Healthcheck: stackResourceHealthcheck{
				Enabled: options.HealthPath != "",
				Path:    options.HealthPath,
			},
		}},
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, stackMetadataFilename), metadataData, 0600); err != nil {
		return err
	}
	override, err := generateStackPangolinOverride(options.Name, metadata, services, profile)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "Stack scaffold created: %s\n", directory)
	if sourcePath != destinationPath {
		fmt.Fprintln(stdout, "Only the Compose file was imported. Copy any relative bind-mount, env, or configuration files into the stack directory before committing.")
	}
	fmt.Fprintf(stdout, "Public resource: https://%s.%s -> %s:%d\n", options.Subdomain, profile.BaseDomain, options.Service, options.Port)
	fmt.Fprintln(stdout, "Review the imported Compose file for literal secrets; AegisNode does not move application-specific secrets out of Git.")
	if servicePublishesPorts(services, options.Service) {
		fmt.Fprintln(stdout, "AegisNode will suppress the selected service's direct host port bindings in its generated deployment override.")
	}
	fmt.Fprintln(stdout, "AegisNode will generate and validate these deployment labels:")
	for _, label := range pangolinLabelsFromOverride(override) {
		fmt.Fprintf(stdout, "  %s\n", label)
	}
	fmt.Fprintln(stdout, "\nReview the files, then commit them. AegisNode deploys committed configuration only:")
	fmt.Fprintf(stdout, "  git -C %s add stacks/%s\n", shellQuote(revision.Path), options.Name)
	fmt.Fprintf(stdout, "  git -C %s commit -m %s\n", shellQuote(revision.Path), shellQuote("Add "+options.Name+" stack"))
	fmt.Fprintln(stdout, "Then open the profile dashboard, press s, select this stack, and press r to deploy it independently.")
	return nil
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
	if options.Service == "" && len(services) == 1 {
		options.Service = services[0].Name
	}
	if options.Port == 0 {
		for _, service := range services {
			if service.Name == options.Service && len(service.ContainerPorts) == 1 {
				options.Port = service.ContainerPorts[0]
			}
		}
	}
	if options.Subdomain == "" {
		options.Subdomain = options.Name
	}
	if options.DisplayName == "" {
		options.DisplayName = titleFromSlug(options.Name)
	}
	return options
}

func validateStackAddOptions(options stackAddOptions, services []composeServiceSummary) error {
	if !stackSlugPattern.MatchString(options.Name) {
		return errors.New("stack name must be a lowercase DNS label")
	}
	if !stackSlugPattern.MatchString(options.Subdomain) {
		return errors.New("subdomain must be a lowercase DNS label")
	}
	if options.DisplayName == "" {
		return errors.New("display name is required")
	}
	if strings.ContainsAny(options.DisplayName, "\r\n") || strings.ContainsAny(options.HealthPath, "\r\n") {
		return errors.New("display name and health-check path must be single-line values")
	}
	if options.Port < 1 || options.Port > 65535 {
		return errors.New("service port must be between 1 and 65535")
	}
	found := false
	for _, service := range services {
		if service.Name == options.Service {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("service %q does not exist in the Compose file", options.Service)
	}
	if options.HealthPath != "" && !strings.HasPrefix(options.HealthPath, "/") {
		return errors.New("health-check path must start with /")
	}
	return nil
}

func generateStackPangolinOverride(stackName string, metadata stackMetadata, services []composeServiceSummary, profile Profile) (string, error) {
	if metadata.Version != 1 {
		return "", fmt.Errorf("unsupported stack metadata version %d", metadata.Version)
	}
	if len(metadata.PublicResources) == 0 {
		return "", errors.New("stack metadata must define at least one public resource")
	}
	var builder strings.Builder
	builder.WriteString("services:\n")
	seenServices := map[string]bool{}
	for _, resource := range metadata.PublicResources {
		if seenServices[resource.Service] {
			return "", fmt.Errorf("service %q has more than one public resource; use one resource per service", resource.Service)
		}
		seenServices[resource.Service] = true
		if resource.Protocol != "" && resource.Protocol != "http" && resource.Protocol != "https" {
			return "", fmt.Errorf("resource %q protocol must be http or https", resource.Name)
		}
		if resource.SSO && firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail) == "" {
			return "", fmt.Errorf("resource %q enables SSO but the profile has no Pangolin administrator email", resource.Name)
		}
		if err := validateStackAddOptions(stackAddOptions{
			Name: stackName, Service: resource.Service, Port: resource.Port,
			Subdomain: resource.Subdomain, DisplayName: resource.Name, HealthPath: resource.Healthcheck.Path,
		}, services); err != nil {
			return "", err
		}
		resourceID := "aegisnode-" + stackName + "-" + slugifyStackValue(resource.Service)
		prefix := "pangolin.public-resources." + resourceID
		builder.WriteString("  " + resource.Service + ":\n")
		if servicePublishesPorts(services, resource.Service) {
			builder.WriteString("    ports: !reset []\n")
		}
		builder.WriteString("    networks:\n      - " + aegisPublicNetwork + "\n")
		builder.WriteString("    labels:\n")
		labels := []string{
			prefix + ".name=" + resource.Name,
			prefix + ".protocol=" + firstNonEmpty(resource.Protocol, "http"),
			prefix + ".full-domain=" + resource.Subdomain + "." + profile.BaseDomain,
			prefix + ".auth.sso-enabled=" + strconv.FormatBool(resource.SSO),
			prefix + ".targets[0].hostname=" + resource.Service,
			prefix + ".targets[0].port=" + strconv.Itoa(resource.Port),
			prefix + ".targets[0].method=" + firstNonEmpty(resource.Protocol, "http"),
		}
		if resource.SSO {
			labels = append(labels, prefix+".auth.sso-users[0]="+firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail))
		}
		if resource.Healthcheck.Enabled {
			labels = append(labels,
				prefix+".targets[0].healthcheck.enabled=true",
				prefix+".targets[0].healthcheck.hostname="+resource.Service,
				prefix+".targets[0].healthcheck.port="+strconv.Itoa(resource.Port),
				prefix+".targets[0].healthcheck.scheme="+firstNonEmpty(resource.Protocol, "http"),
				prefix+".targets[0].healthcheck.path="+firstNonEmpty(resource.Healthcheck.Path, "/"),
			)
		}
		for _, label := range labels {
			builder.WriteString("      - " + yamlDoubleQuote(label) + "\n")
		}
	}
	builder.WriteString("networks:\n  " + aegisPublicNetwork + ":\n    external: true\n")
	return builder.String(), nil
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
			resourceID := "aegisnode-" + stack.Name + "-" + slugifyStackValue(resource.Service)
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

type stackAddModel struct {
	options   stackAddOptions
	services  []composeServiceSummary
	inputs    []textinput.Model
	focus     int
	err       string
	done      bool
	cancelled bool
}

func collectStackAddOptions(options stackAddOptions, services []composeServiceSummary, output io.Writer) (stackAddOptions, error) {
	model := newStackAddModel(options, services)
	final, err := tea.NewProgram(model, tea.WithOutput(output), tea.WithAltScreen()).Run()
	if err != nil {
		return options, err
	}
	result := final.(stackAddModel)
	if result.cancelled {
		return options, errors.New("stack add cancelled")
	}
	return result.optionsFromInputs()
}

func newStackAddModel(options stackAddOptions, services []composeServiceSummary) stackAddModel {
	inputs := newSetupInputs([]setupInputField{
		{label: "Stack name", value: options.Name},
		{label: "Service to publish", value: options.Service},
		{label: "Container port", value: func() string {
			if options.Port == 0 {
				return ""
			}
			return strconv.Itoa(options.Port)
		}()},
		{label: "Public subdomain", value: options.Subdomain},
		{label: "Pangolin display name", value: options.DisplayName},
		{label: "Health-check path", value: options.HealthPath},
	})
	inputs[0].Focus()
	return stackAddModel{options: options, services: services, inputs: inputs}
}

func (model stackAddModel) Init() tea.Cmd { return textinput.Blink }

func (model stackAddModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return model, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		model.cancelled = true
		return model, tea.Quit
	case "tab", "down":
		model.inputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(model.inputs)
		model.inputs[model.focus].Focus()
		return model, nil
	case "shift+tab", "up":
		model.inputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(model.inputs) - 1
		}
		model.inputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.focus < len(model.inputs)-1 {
			model.inputs[model.focus].Blur()
			model.focus++
			model.inputs[model.focus].Focus()
			return model, nil
		}
		options, err := model.optionsFromInputs()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.options = options
		model.done = true
		return model, tea.Quit
	}
	var command tea.Cmd
	model.inputs[model.focus], command = model.inputs[model.focus].Update(key)
	return model, command
}

func (model stackAddModel) View() string {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Add Compose stack"))
	builder.WriteString("\n")
	builder.WriteString("Detected services:\n")
	for _, service := range model.services {
		ports := "no declared ports"
		if len(service.ContainerPorts) > 0 {
			values := make([]string, len(service.ContainerPorts))
			for index, port := range service.ContainerPorts {
				values[index] = strconv.Itoa(port)
			}
			ports = strings.Join(values, ", ")
		}
		builder.WriteString(fmt.Sprintf("  %s: %s\n", service.Name, ports))
	}
	builder.WriteString("\nAegisNode will preserve the Compose file and generate a deployment override for Pangolin.\n\n")
	builder.WriteString("Review application-specific secrets before committing; AegisNode cannot infer their storage requirements.\n\n")
	for _, input := range model.inputs {
		builder.WriteString(input.View())
		builder.WriteByte('\n')
	}
	if model.err != "" {
		builder.WriteString("\n" + setupErrorStyle.Render(model.err))
	}
	builder.WriteString("\nenter next • tab field • esc cancel\n")
	return builder.String()
}

func (model stackAddModel) optionsFromInputs() (stackAddOptions, error) {
	options := model.options
	options.Name = strings.TrimSpace(model.inputs[0].Value())
	options.Service = strings.TrimSpace(model.inputs[1].Value())
	options.Port, _ = strconv.Atoi(strings.TrimSpace(model.inputs[2].Value()))
	options.Subdomain = strings.TrimSpace(model.inputs[3].Value())
	options.DisplayName = strings.TrimSpace(model.inputs[4].Value())
	options.HealthPath = strings.TrimSpace(model.inputs[5].Value())
	return options, validateStackAddOptions(options, model.services)
}
