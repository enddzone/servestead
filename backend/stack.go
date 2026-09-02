package main

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"servestead/backend/resources"
)

const stackMetadataFilename = "servestead.yaml"
const stackComposeFilename = "compose.yaml"
const gitNoExtDiffFlag = "--no-ext-diff"

const (
	stackComposeMaxBytes        int64 = 4 << 20
	stackMetadataMaxBytes       int64 = 1 << 20
	stackEnvironmentMaxBytes    int64 = 1 << 20
	stackSecretMaxBytes         int64 = 8 << 20
	stackRepositoryFileMaxBytes int64 = 4 << 20
	stackRepositoryListMaxBytes int64 = 1 << 20
	stackRepositoryTotalBytes   int64 = 16 << 20
	stackRepositoryDiffMaxBytes int64 = 2 << 20
	stackRepositoryMaxStacks          = 256
	stackRepositoryMaxEntries         = 1024
)

var stackSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var stackPublicResourceProtocols = []string{"http", "tcp", "udp", "ssh", "rdp", "vnc"}

const reservedObservabilityStackName = "observability"

type stackMetadata struct {
	Version         int                   `yaml:"version"`
	PublicResources []stackPublicResource `yaml:"public_resources"`
	Secrets         stackSecretMetadata   `yaml:"secrets,omitempty"`
}

type configuredStack struct {
	Name          string
	Compose       string
	Metadata      string
	Override      string
	ComposeSHA256 string
	Resources     []stackPublicResource
	Files         map[string]string
	Secrets       stackSecretMetadata
	SecretValues  SecretSet
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
	Secrets         stackSecretMetadata
}

type editableStack struct {
	Name            string
	Compose         string
	Metadata        stackMetadata
	Services        []composeServiceSummary
	MetadataMissing bool
}

func loadEditableStacks(repositoryPath string) ([]editableStack, error) {
	return loadEditableStacksContext(context.Background(), repositoryPath)
}

func loadEditableStacksContext(ctx context.Context, repositoryPath string) ([]editableStack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer stacksDirectory.Close()
	entries, err := readManagedDirectoryEntries(stacksDirectory)
	if err != nil {
		return nil, err
	}
	if err := validateEditableStackDirectoryCount(entries); err != nil {
		return nil, err
	}
	stacks := []editableStack{}
	var totalBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stack, include, bytesRead, err := loadEditableStackEntry(ctx, stacksDirectory, entry)
		if err != nil {
			return nil, err
		}
		if !include {
			continue
		}
		totalBytes += bytesRead
		if totalBytes > stackRepositoryTotalBytes {
			return nil, fmt.Errorf("editable stack files exceed the %s total limit", formatByteLimit(stackRepositoryTotalBytes))
		}
		stacks = append(stacks, stack)
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i].Name < stacks[j].Name })
	return stacks, nil
}

func readManagedDirectoryEntries(directory *os.Root) ([]os.DirEntry, error) {
	entriesFile, err := directory.Open(".")
	if err != nil {
		return nil, err
	}
	defer entriesFile.Close()
	entries, err := entriesFile.ReadDir(stackRepositoryMaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > stackRepositoryMaxEntries {
		return nil, fmt.Errorf("managed stacks directory has more than %d entries", stackRepositoryMaxEntries)
	}
	return entries, nil
}

func validateEditableStackDirectoryCount(entries []os.DirEntry) error {
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == reservedObservabilityStackName {
			continue
		}
		count++
		if count > stackRepositoryMaxStacks {
			return fmt.Errorf("configuration repository has more than %d application stacks", stackRepositoryMaxStacks)
		}
	}
	return nil
}

func loadEditableStackEntry(ctx context.Context, stacksDirectory *os.Root, entry os.DirEntry) (editableStack, bool, int64, error) {
	if !entry.IsDir() {
		if isStackComposeFilename(entry.Name()) {
			return editableStack{}, false, 0, fmt.Errorf(
				"compose file %s is outside a stack directory; move it to %s or press a in setup to import it",
				filepath.Join("stacks", entry.Name()),
				filepath.Join("stacks", "<stack-name>", stackComposeFilename),
			)
		}
		return editableStack{}, false, 0, nil
	}
	if entry.Name() == reservedObservabilityStackName {
		return editableStack{}, false, 0, nil
	}
	if err := validateStackName(entry.Name()); err != nil {
		return editableStack{}, false, 0, fmt.Errorf("stack directory %q must be a lowercase DNS label", entry.Name())
	}
	directory, err := openManagedDirectory(stacksDirectory, entry.Name(), fmt.Sprintf("managed stack directory %q", entry.Name()), false)
	if err != nil {
		return editableStack{}, false, 0, err
	}
	defer directory.Close()
	compose, err := readEditableStackCompose(directory, entry.Name())
	if err != nil {
		return editableStack{}, false, 0, err
	}
	if compose == nil {
		return editableStack{}, false, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return editableStack{}, false, 0, err
	}
	services, err := inspectComposeServices(compose)
	if err != nil {
		return editableStack{}, false, 0, fmt.Errorf("stack %s: %w", entry.Name(), err)
	}
	if err := ctx.Err(); err != nil {
		return editableStack{}, false, 0, err
	}
	metadata, metadataMissing, metadataBytes, err := readEditableStackMetadata(directory, entry.Name(), services)
	if err != nil {
		return editableStack{}, false, 0, err
	}
	return editableStack{
		Name: entry.Name(), Compose: string(compose), Metadata: metadata, Services: services,
		MetadataMissing: metadataMissing,
	}, true, int64(len(compose)) + metadataBytes, nil
}

var readEditableStackManagedFile = readManagedFile

func readEditableStackCompose(directory *os.Root, name string) ([]byte, error) {
	compose, err := readEditableStackManagedFile(directory, stackComposeFilename, fmt.Sprintf("stack %s %s", name, stackComposeFilename), stackComposeMaxBytes)
	if err == nil {
		return compose, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stack %s: read %s: %w", name, stackComposeFilename, err)
	}
	directoryFile, openErr := directory.Open(".")
	if openErr != nil {
		return nil, fmt.Errorf("stack %s: inspect directory: %w", name, openErr)
	}
	children, readErr := directoryFile.ReadDir(1)
	closeErr := directoryFile.Close()
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	if readErr == nil && closeErr != nil {
		readErr = closeErr
	}
	if readErr == nil && len(children) == 0 {
		return nil, nil
	}
	if readErr != nil {
		return nil, fmt.Errorf("stack %s: inspect directory: %w", name, readErr)
	}
	return nil, fmt.Errorf(
		"stack %s is incomplete: expected %s; move a Compose file there or press a in setup to import one",
		name,
		filepath.Join("stacks", name, stackComposeFilename),
	)
}

func readEditableStackMetadata(directory *os.Root, name string, services []composeServiceSummary) (stackMetadata, bool, int64, error) {
	metadataData, err := readEditableStackManagedFile(directory, stackMetadataFilename, fmt.Sprintf("stack %s %s", name, stackMetadataFilename), stackMetadataMaxBytes)
	metadata := stackMetadata{Version: 1}
	if errors.Is(err, os.ErrNotExist) {
		return metadata, true, 0, nil
	}
	if err != nil {
		return metadata, false, 0, fmt.Errorf("stack %s: read %s: %w", name, stackMetadataFilename, err)
	}
	if err := yaml.Unmarshal(metadataData, &metadata); err != nil {
		return metadata, false, 0, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	if err := validateStackMetadata(name, metadata, services); err != nil {
		return metadata, false, 0, fmt.Errorf("stack %s metadata: %w", name, err)
	}
	return metadata, false, int64(len(metadataData)), nil
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
	metadata := stackMetadata{Version: 1, PublicResources: options.Resources, Secrets: options.Secrets}
	if err := validateStackMetadata(options.Name, metadata, services); err != nil {
		return err
	}
	if originalName != "" {
		if err := validateEditableStackManagedFiles(repositoryPath, originalName); err != nil {
			return err
		}
	}
	stacksDirectory := filepath.Join(expandUserPath(repositoryPath), "stacks")
	if _, err := prepareEditableStackDestination(stacksDirectory, originalName, options.Name); err != nil {
		return err
	}
	return writeEditableStackFiles(repositoryPath, options.Name, metadata, compose)
}

func validateEditableStackManagedFiles(repositoryPath, name string) error {
	directory, err := openManagedStackRoot(repositoryPath, name, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	for _, managedFile := range []string{stackComposeFilename, stackMetadataFilename, stackSecretFilename} {
		if err := rejectManagedFileSymlink(directory, managedFile, fmt.Sprintf("stack %s %s", name, managedFile)); err != nil {
			return err
		}
	}
	return nil
}

var writeEditableStackFile = atomicWriteManagedFile

func prepareEditableStackDestination(stacksPath, originalName, name string) (string, error) {
	if err := validateStackName(name); err != nil {
		return "", err
	}
	stacksPath = expandUserPath(stacksPath)
	repositoryPath := filepath.Dir(filepath.Clean(stacksPath))
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, true)
	if err != nil {
		return "", err
	}
	defer stacksDirectory.Close()
	destination := filepath.Join(stacksPath, name)
	if originalName == "" {
		if err := validateNewEditableStackDestination(stacksDirectory, name); err != nil {
			return "", err
		}
		return destination, nil
	}
	if err := validateStackName(originalName); err != nil {
		if !stackSlugPattern.MatchString(originalName) {
			return "", errors.New("original stack name must be a lowercase DNS label")
		}
		return "", err
	}
	if originalName == name {
		if err := validateExistingEditableStackDestination(stacksDirectory, name); err != nil {
			return "", err
		}
		return destination, nil
	}
	if err := renameEditableStackDestination(stacksDirectory, originalName, name); err != nil {
		return "", fmt.Errorf("rename stack: %w", err)
	}
	return destination, nil
}

func validateNewEditableStackDestination(stacksDirectory *os.Root, name string) error {
	info, err := stacksDirectory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to save stack %q: managed stack directory is a symbolic link", name)
	}
	stackDirectory, err := openManagedDirectory(stacksDirectory, name, fmt.Sprintf("managed stack directory %q", name), false)
	if err != nil {
		return err
	}
	defer stackDirectory.Close()
	if !stackRootContainsOnlySecretFile(stackDirectory) {
		return fmt.Errorf("stack %q already exists", name)
	}
	return nil
}

func validateExistingEditableStackDestination(stacksDirectory *os.Root, name string) error {
	info, err := stacksDirectory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to save stack %q: managed stack directory is a symbolic link", name)
	}
	if !info.IsDir() {
		return fmt.Errorf("stack %q is not a directory", name)
	}
	return nil
}

func renameEditableStackDestination(stacksDirectory *os.Root, originalName, name string) error {
	if _, err := stacksDirectory.Lstat(name); err == nil {
		return fmt.Errorf("stack %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sourceInfo, err := stacksDirectory.Lstat(originalName)
	if err != nil {
		return err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to rename stack %q: managed stack directory is a symbolic link", originalName)
	}
	if !sourceInfo.IsDir() {
		return fmt.Errorf("stack %q is not a directory", originalName)
	}
	return stacksDirectory.Rename(originalName, name)
}

func writeEditableStackFiles(repositoryPath, name string, metadata stackMetadata, compose []byte) error {
	if err := validateStackName(name); err != nil {
		return err
	}
	destination, err := openManagedStackRoot(repositoryPath, name, true)
	if err != nil {
		return err
	}
	defer destination.Close()
	for _, managedFile := range []string{stackComposeFilename, stackMetadataFilename} {
		if err := rejectManagedFileSymlink(destination, managedFile, fmt.Sprintf("stack %s %s", name, managedFile)); err != nil {
			return err
		}
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := writeEditableStackFile(destination, stackComposeFilename, compose, 0600); err != nil {
		return err
	}
	return writeEditableStackFile(destination, stackMetadataFilename, metadataData, 0600)
}

func openManagedStacksRoot(repositoryPath string, create bool) (*os.Root, error) {
	repository, err := os.OpenRoot(expandUserPath(repositoryPath))
	if err != nil {
		return nil, fmt.Errorf("open configuration repository: %w", err)
	}
	defer repository.Close()
	return openManagedDirectory(repository, "stacks", "managed stacks directory", create)
}

func openManagedStackRoot(repositoryPath, name string, create bool) (*os.Root, error) {
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, create)
	if err != nil {
		return nil, err
	}
	defer stacksDirectory.Close()
	return openManagedDirectory(stacksDirectory, name, fmt.Sprintf("managed stack directory %q", name), create)
}

func managedStackDirectoryExists(repositoryPath, name string) (bool, error) {
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer stacksDirectory.Close()
	info, err := stacksDirectory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("managed stack directory %q is a symbolic link", name)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("managed stack directory %q is not a directory", name)
	}
	directory, err := openManagedDirectory(stacksDirectory, name, fmt.Sprintf("managed stack directory %q", name), false)
	if err != nil {
		return false, err
	}
	return true, directory.Close()
}

func openManagedDirectory(parent *os.Root, name, label string, create bool) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := parent.Mkdir(name, 0700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return nil, mkdirErr
		}
		info, err = parent.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link", label)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", label)
	}
	directory, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := directory.Stat(".")
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		_ = directory.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", label)
	}
	return directory, nil
}

func readManagedFile(root *os.Root, name, label string, limit int64) ([]byte, error) {
	info, err := managedRegularFileInfo(root, name, label)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%s changed while it was being opened", label)
	}
	return readBounded(file, label, limit)
}

func readBoundedFile(path, label string, limit int64) ([]byte, error) {
	file, err := os.Open(expandUserPath(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, label, limit)
}

func readBounded(reader io.Reader, label string, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds the %s limit", label, formatByteLimit(limit))
	}
	return data, nil
}

func formatByteLimit(limit int64) string {
	if limit%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", limit>>20)
	}
	return fmt.Sprintf("%d bytes", limit)
}

func managedRegularFileInfo(root *os.Root, name, label string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", label)
	}
	return info, nil
}

func rejectManagedFileSymlink(root *os.Root, name, label string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", label)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", label)
	}
	return nil
}

func atomicWriteManagedFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if err := rejectManagedFileSymlink(root, name, name); err != nil {
		return err
	}
	var randomSuffix [8]byte
	if _, err := rand.Read(randomSuffix[:]); err != nil {
		return err
	}
	tempName := fmt.Sprintf(".%s.tmp-%x", name, randomSuffix)
	temp, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Remove(tempName)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tempName, name); err != nil {
		return err
	}
	cleanup = false
	if directory, err := root.Open("."); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func stackRootContainsOnlySecretFile(directory *os.Root) bool {
	file, err := directory.Open(".")
	if err != nil {
		return false
	}
	entries, readErr := file.ReadDir(2)
	closeErr := file.Close()
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil || len(entries) != 1 {
		return false
	}
	return entries[0].Type().IsRegular() && entries[0].Name() == stackSecretFilename
}

func removeEditableStack(repositoryPath, name string) error {
	if err := validateStackName(name); err != nil {
		return err
	}
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, false)
	if err != nil {
		return fmt.Errorf("stack %q is not configured: %w", name, err)
	}
	defer stacksDirectory.Close()
	info, err := stacksDirectory.Lstat(name)
	if err != nil {
		return fmt.Errorf("stack %q is not configured: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove stack %q: managed stack directory is a symbolic link", name)
	}
	stackDirectory, err := openManagedDirectory(stacksDirectory, name, fmt.Sprintf("managed stack directory %q", name), false)
	if err != nil {
		return err
	}
	if _, err := managedRegularFileInfo(stackDirectory, stackMetadataFilename, fmt.Sprintf("stack %s %s", name, stackMetadataFilename)); err != nil {
		_ = stackDirectory.Close()
		return fmt.Errorf("stack %q is not configured: %w", name, err)
	}
	if err := stackDirectory.Close(); err != nil {
		return err
	}
	return stacksDirectory.RemoveAll(name)
}

func stackRepositoryStatus(ctx context.Context, repositoryPath string) (string, error) {
	status, err := runGitLimited(ctx, expandUserPath(repositoryPath), stackRepositoryListMaxBytes, "status", "--short", "--", "stacks")
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
	output := newStackRepositoryDiffOutput(stackRepositoryDiffMaxBytes)
	unstaged, truncated, err := runBoundedGitDiff(ctx, repositoryPath, false, "diff", gitNoExtDiffFlag, "--", "stacks")
	if err != nil {
		return "", err
	}
	output.appendSection("Unstaged changes", unstaged, truncated)
	if output.truncated {
		return output.String(), nil
	}
	staged, truncated, err := runBoundedGitDiff(ctx, repositoryPath, false, "diff", "--cached", gitNoExtDiffFlag, "--", "stacks")
	if err != nil {
		return "", err
	}
	output.appendSection("Staged changes", staged, truncated)
	if output.truncated {
		return output.String(), nil
	}
	untracked, truncated, err := runBoundedGitDiff(ctx, repositoryPath, false, "ls-files", "-z", "--others", "--exclude-standard", "--", "stacks")
	if err != nil {
		return "", err
	}
	if truncated {
		output.truncated = true
		return output.String(), nil
	}
	for _, name := range strings.Split(strings.TrimSuffix(untracked, "\x00"), "\x00") {
		if name == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		diff, truncated, err := untrackedFileDiff(ctx, repositoryPath, name)
		if err != nil {
			return "", err
		}
		output.appendSection("Untracked: "+name, diff, truncated)
		if output.truncated {
			break
		}
	}
	return output.String(), nil
}

func untrackedFileDiff(ctx context.Context, repositoryPath, name string) (string, bool, error) {
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if cleanName == "." || cleanName == "stacks" || !strings.HasPrefix(cleanName, "stacks"+string(filepath.Separator)) {
		return "", false, fmt.Errorf("invalid untracked stack path %q", name)
	}
	return runBoundedGitDiff(ctx, repositoryPath, true, "diff", "--no-index", gitNoExtDiffFlag, "--", "/dev/null", cleanName)
}

type boundedCommandBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedCommandBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = buffer.buffer.Write(data[:remaining])
			buffer.truncated = true
		} else {
			_, _ = buffer.buffer.Write(data)
		}
	}
	if len(data) > remaining {
		buffer.truncated = true
	}
	return written, nil
}

func (buffer *boundedCommandBuffer) String() string {
	return buffer.buffer.String()
}

func runBoundedGitDiff(ctx context.Context, repositoryPath string, allowDiffExit bool, arguments ...string) (string, bool, error) {
	command, err := newGitCommand(ctx, append([]string{"-C", repositoryPath}, arguments...)...)
	if err != nil {
		return "", false, err
	}
	command.Env = trustedCommandEnvironment(nil)
	stdout := &boundedCommandBuffer{limit: int(stackRepositoryDiffMaxBytes)}
	stderr := &boundedCommandBuffer{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	if err == nil {
		return stdout.String(), stdout.truncated, nil
	}
	var exitError *exec.ExitError
	if allowDiffExit && errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return stdout.String(), stdout.truncated, nil
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = err.Error()
	}
	return "", false, fmt.Errorf("git %s: %s", arguments[0], detail)
}

type stackRepositoryDiffOutput struct {
	builder   strings.Builder
	limit     int
	truncated bool
}

func newStackRepositoryDiffOutput(limit int64) *stackRepositoryDiffOutput {
	return &stackRepositoryDiffOutput{limit: int(limit)}
}

func (output *stackRepositoryDiffOutput) appendSection(title, content string, sourceTruncated bool) {
	content = strings.TrimSpace(content)
	if content == "" && !sourceTruncated {
		return
	}
	section := title + "\n\n" + content + "\n"
	if output.builder.Len() > 0 {
		section = "\n" + section
	}
	remaining := output.limit - output.builder.Len()
	if remaining <= 0 {
		output.truncated = true
		return
	}
	if len(section) > remaining {
		output.builder.WriteString(truncateUTF8Bytes(section, remaining))
		output.truncated = true
		return
	}
	output.builder.WriteString(section)
	output.truncated = sourceTruncated
}

func (output *stackRepositoryDiffOutput) String() string {
	if output.builder.Len() == 0 && !output.truncated {
		return "No stack changes."
	}
	content := output.builder.String()
	if !output.truncated {
		return content
	}
	notice := "\n[Additional diff output was omitted after the " + formatByteLimit(stackRepositoryDiffMaxBytes) + " display limit.]\n"
	return truncateUTF8Bytes(content, output.limit-len(notice)) + notice
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
	staged, err := runGitLimited(ctx, expandUserPath(repositoryPath), stackRepositoryListMaxBytes, "diff", "--cached", "--name-only", "--", "stacks")
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
		return runStackEnvironment(ctx, args[1:], stdout, stderr)
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
	options, services, metadata, secretValues, secretKeys, err := stackAddInputs(args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, state, profileLock, err := lockAndLoadProfile(store, options.ProfileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	defer releaseProfileOperationLock(profileLock)
	if profile.BaseDomain == "" && len(options.Resources) > 0 {
		return errors.New("profile base domain is required before adding a public stack")
	}
	repositoryPath, err := stackAddRepositoryPath(profile)
	if err != nil {
		return err
	}
	repositoryLock, err := acquireRepositoryOperationLock(store, repositoryPath)
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(repositoryLock)
	stackSecretIdentity := ""
	if options.EnvironmentFile != "" {
		_, identity, recipient, err := ensureProfileStackSecretIdentity(store, profile.ID)
		if err != nil {
			return err
		}
		stackSecretIdentity = identity
		options.Secrets = ageStackSecretMetadata(options.Name, secretValues, recipient)
		metadata.Secrets = options.Secrets
		if err := validateStackMetadata(options.Name, metadata, services); err != nil {
			return err
		}
	}
	override, err := generateStackPangolinOverride(options.Name, metadata, services, profile)
	if err != nil {
		return err
	}
	revision, scaffoldCreated, err := prepareStackAddRepository(ctx, store, profile, state, stdout)
	if err != nil {
		return err
	}
	directory, copiedCompose, err := writeStackAddFiles(ctx, revision.Path, options, metadata, stackSecretIdentity, secretValues, stdout)
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
		EnvironmentKeys: secretKeys,
		Metadata:        metadata,
		Services:        services,
		BaseDomain:      profile.BaseDomain,
	})
	return nil
}

func stackAddRepositoryPath(profile Profile) (string, error) {
	repositoryPath := profile.ConfigRepositoryPath
	if repositoryPath == "" {
		var err error
		repositoryPath, err = defaultConfigRepositoryPath(profile.ID)
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(expandUserPath(repositoryPath))
}

func stackAddInputs(args []string, stderr io.Writer) (stackAddOptions, []composeServiceSummary, stackMetadata, SecretSet, []string, error) {
	flags := flag.NewFlagSet("stack add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := stackAddOptions{}
	var publications stackPublishFlags
	flags.StringVar(&options.ProfileID, "profile", "", "saved Servestead profile ID")
	flags.StringVar(&options.Compose, "compose", "", "Docker Compose file to add")
	flags.StringVar(&options.Name, "name", "", "stack name used in the repository")
	flags.Var(&publications, "publish", "public route service:port:subdomain[:id] (repeatable)")
	flags.StringVar(&options.EnvironmentFile, "env-file", "", "runtime secret environment file stored as encrypted stack metadata")
	if err := flags.Parse(args); err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, err
	}
	if flags.NArg() != 0 {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if options.ProfileID == "" || options.Compose == "" {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, errors.New("--profile and --compose are required")
	}

	composeData, err := readBoundedFile(options.Compose, "Compose file", stackComposeMaxBytes)
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, fmt.Errorf("read Compose file: %w", err)
	}
	services, err := inspectComposeServices(composeData)
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, err
	}
	options = withStackAddDefaults(options, services)
	options.Resources, err = parseStackPublications(publications)
	if err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, err
	}
	var secretValues SecretSet
	var secretKeys []string
	if options.EnvironmentFile != "" {
		secretValues, secretKeys, err = readStackEnvironmentSecrets(options.EnvironmentFile)
		if err != nil {
			return stackAddOptions{}, nil, stackMetadata{}, nil, nil, err
		}
	}
	metadata := stackMetadata{Version: 1, PublicResources: options.Resources}
	if err := validateStackMetadata(options.Name, metadata, services); err != nil {
		return stackAddOptions{}, nil, stackMetadata{}, nil, nil, err
	}
	return options, services, metadata, secretValues, secretKeys, nil
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

func writeStackAddFiles(ctx context.Context, repositoryPath string, options stackAddOptions, metadata stackMetadata, stackSecretIdentity string, secretValues SecretSet, stdout io.Writer) (string, bool, error) {
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
	stackDirectory, err := openManagedStackRoot(repositoryPath, options.Name, true)
	if err != nil {
		return "", false, err
	}
	defer stackDirectory.Close()
	if info, err := stackDirectory.Lstat(stackMetadataFilename); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("stack %s %s is a symbolic link", options.Name, stackMetadataFilename)
		}
		return "", false, fmt.Errorf("stack %q is already configured at %s", options.Name, directory)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	copiedCompose := sourcePath != destinationPath
	if err := copyStackAddCompose(stackDirectory, sourcePath, destinationPath, options.Name); err != nil {
		return "", false, err
	}
	if options.EnvironmentFile != "" {
		if err := putStackSecrets(ctx, repositoryPath, options.Name, metadata.Secrets, stackSecretIdentity, secretValues); err != nil {
			return "", false, err
		}
		fmt.Fprintf(stdout, "Stack secrets saved to %s (%d keys).\n", metadata.Secrets.Source, len(secretSetKeys(secretValues)))
	}
	metadataData, err := yaml.Marshal(metadata)
	if err != nil {
		return "", false, err
	}
	if err := atomicWriteManagedFile(stackDirectory, stackMetadataFilename, metadataData, 0600); err != nil {
		return "", false, err
	}
	return directory, copiedCompose, nil
}

func copyStackAddCompose(directory *os.Root, sourcePath, destinationPath, name string) error {
	if sourcePath == destinationPath {
		_, err := managedRegularFileInfo(directory, stackComposeFilename, fmt.Sprintf("stack %s %s", name, stackComposeFilename))
		return err
	}
	if info, err := directory.Lstat(stackComposeFilename); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("stack %s %s is a symbolic link", name, stackComposeFilename)
		}
		return fmt.Errorf("stack %q already has a %s", name, stackComposeFilename)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := readBoundedFile(sourcePath, "Compose file", stackComposeMaxBytes)
	if err != nil {
		return err
	}
	return atomicWriteManagedFile(directory, stackComposeFilename, data, 0600)
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
		fmt.Fprintf(stdout, "Runtime secrets saved as encrypted stack metadata (%d keys).\n", len(summary.EnvironmentKeys))
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
	fmt.Fprintln(stdout, "Review the imported Compose file for literal secrets; Servestead stores imported environment values only as encrypted stack secrets.")
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

func runStackEnvironment(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	action, profileID, stackName, path, err := stackEnvironmentInputs(args, stderr)
	if err != nil {
		return err
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, _, profileLock, err := lockAndLoadProfile(store, profileID)
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(profileLock)
	if profile.ConfigRepositoryPath == "" {
		return errors.New("profile configuration repository is not ready")
	}
	repositoryLock, err := acquireRepositoryOperationLock(store, profile.ConfigRepositoryPath)
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(repositoryLock)
	if err := ensureStackEnvironmentTarget(store, profileID, stackName); err != nil {
		return err
	}
	if action == "remove" {
		return removeStackEnvironment(ctx, store, profileID, stackName, path, stdout)
	}
	return setStackEnvironment(ctx, store, profileID, stackName, path, stdout)
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
	if profileID == "" || validateStackName(stackName) != nil {
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
	directory, err := openManagedStackRoot(profile.ConfigRepositoryPath, stackName, false)
	if err != nil {
		return fmt.Errorf("stack %q is not configured: %w", stackName, err)
	}
	defer directory.Close()
	if _, err := managedRegularFileInfo(directory, stackMetadataFilename, fmt.Sprintf("stack %s %s", stackName, stackMetadataFilename)); err != nil {
		return fmt.Errorf("stack %q is not configured: %w", stackName, err)
	}
	return nil
}

func removeStackEnvironment(ctx context.Context, store ProfileStore, profileID, stackName, path string, stdout io.Writer) error {
	if path != "" {
		return errors.New("--file cannot be used with env remove")
	}
	profile, _, err := store.Load(profileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	metadata, err := readManagedStackMetadata(profile.ConfigRepositoryPath, stackName)
	if err != nil {
		return err
	}
	if metadata.Secrets.HasSecrets() {
		secrets, err := store.LoadSecrets(profileID)
		if err != nil {
			return err
		}
		identity, _, err := secrets.StackSecretIdentityPair()
		if err != nil {
			return err
		}
		if err := removeStackSecrets(ctx, profile.ConfigRepositoryPath, stackName, metadata.Secrets, identity); err != nil {
			return err
		}
		metadata.Secrets = stackSecretMetadata{}
		if err := writeManagedStackMetadata(profile.ConfigRepositoryPath, stackName, metadata); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "Removed the encrypted runtime secrets for %s. Review and commit the stack metadata change.\n", stackName)
	return nil
}

func setStackEnvironment(ctx context.Context, store ProfileStore, profileID, stackName, path string, stdout io.Writer) error {
	if path == "" {
		return errors.New("--file is required with env set")
	}
	profile, _, err := store.Load(profileID)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	values, keys, err := readStackEnvironmentSecrets(path)
	if err != nil {
		return err
	}
	metadata, err := readManagedStackMetadata(profile.ConfigRepositoryPath, stackName)
	if err != nil {
		return err
	}
	composeData, err := readManagedStackCompose(profile.ConfigRepositoryPath, stackName)
	if err != nil {
		return fmt.Errorf("read stack Compose file: %w", err)
	}
	services, err := inspectComposeServices(composeData)
	if err != nil {
		return err
	}
	_, identity, recipient, err := ensureProfileStackSecretIdentity(store, profileID)
	if err != nil {
		return err
	}
	metadata.Secrets = ageStackSecretMetadata(stackName, values, recipient)
	if err := validateStackMetadata(stackName, metadata, services); err != nil {
		return err
	}
	if err := putStackSecrets(ctx, profile.ConfigRepositoryPath, stackName, metadata.Secrets, identity, values); err != nil {
		return err
	}
	if err := writeManagedStackMetadata(profile.ConfigRepositoryPath, stackName, metadata); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Saved %d encrypted runtime secret keys for %s: %s\n", len(keys), stackName, strings.Join(keys, ", "))
	fmt.Fprintln(stdout, "Review and commit the stack secret file and metadata, then deploy or synchronize the stack to apply them.")
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

func readManagedStackMetadata(repositoryPath, stackName string) (stackMetadata, error) {
	directory, err := openManagedStackRoot(repositoryPath, stackName, false)
	if err != nil {
		return stackMetadata{}, err
	}
	defer directory.Close()
	data, err := readManagedFile(directory, stackMetadataFilename, fmt.Sprintf("stack %s %s", stackName, stackMetadataFilename), stackMetadataMaxBytes)
	if err != nil {
		return stackMetadata{}, err
	}
	metadata := stackMetadata{Version: 1}
	if err := yaml.Unmarshal(data, &metadata); err != nil {
		return stackMetadata{}, err
	}
	return metadata, nil
}

func readManagedStackCompose(repositoryPath, stackName string) ([]byte, error) {
	directory, err := openManagedStackRoot(repositoryPath, stackName, false)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return readManagedFile(directory, stackComposeFilename, fmt.Sprintf("stack %s %s", stackName, stackComposeFilename), stackComposeMaxBytes)
}

func writeManagedStackMetadata(repositoryPath, stackName string, metadata stackMetadata) error {
	data, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	directory, err := openManagedStackRoot(repositoryPath, stackName, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	return atomicWriteManagedFile(directory, stackMetadataFilename, data, 0600)
}

func putStackSecrets(ctx context.Context, repositoryPath, stackName string, metadata stackSecretMetadata, identity string, values SecretSet) error {
	provider, err := secretProviderForName(metadata.Provider)
	if err != nil {
		return err
	}
	return provider.PutStackSecrets(ctx, metadata.Ref(repositoryPath, stackName, identity), values)
}

func removeStackSecrets(ctx context.Context, repositoryPath, stackName string, metadata stackSecretMetadata, identity string) error {
	provider, err := secretProviderForName(metadata.Provider)
	if err != nil {
		return err
	}
	return provider.DeleteStackSecrets(ctx, metadata.Ref(repositoryPath, stackName, identity), nil)
}

func readStackEnvironmentSecrets(path string) (SecretSet, []string, error) {
	data, err := readBoundedFile(path, "environment file", stackEnvironmentMaxBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read environment file: %w", err)
	}
	return parseEnvironmentSecretSet(string(data))
}

func readStackEnvironmentFile(path string) (string, []string, error) {
	data, err := readBoundedFile(path, "environment file", stackEnvironmentMaxBytes)
	if err != nil {
		return "", nil, fmt.Errorf("read environment file: %w", err)
	}
	return readStackEnvironmentContent(string(data))
}

func readStackEnvironmentContent(content string) (string, []string, error) {
	if strings.IndexByte(content, 0) >= 0 {
		return "", nil, errors.New("environment file contains a NUL byte")
	}
	_, keys, err := parseEnvironmentSecretSet(content)
	if err != nil {
		return "", nil, err
	}
	environment := content
	if environment != "" && !strings.HasSuffix(environment, "\n") {
		environment += "\n"
	}
	return environment, keys, nil
}

func inspectComposeServices(data []byte) ([]composeServiceSummary, error) {
	if int64(len(data)) > stackComposeMaxBytes {
		return nil, fmt.Errorf("Compose file exceeds the %s limit", formatByteLimit(stackComposeMaxBytes))
	}
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
	if err := validateStackName(stackName); err != nil {
		return err
	}
	ids := map[string]bool{}
	subdomains := map[string]bool{}
	for _, resource := range metadata.PublicResources {
		if err := validateStackMetadataResource(resource, services, ids, subdomains); err != nil {
			return fmt.Errorf("resource %q: %w", resource.ID, err)
		}
	}
	if err := validateStackSecretMetadata(stackName, metadata.Secrets); err != nil {
		return err
	}
	return nil
}

func validateStackName(name string) error {
	if !stackSlugPattern.MatchString(name) {
		return errors.New("stack name must be a lowercase DNS label")
	}
	if name == reservedObservabilityStackName {
		return fmt.Errorf("stack name %q is reserved for managed observability services", name)
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
	if !isStackPublicResourceProtocol(resource.Protocol) {
		return fmt.Errorf("resource %q protocol must be one of %s; use http for web apps because Pangolin handles public HTTPS", resource.ID, strings.Join(stackPublicResourceProtocols, ", "))
	}
	if resource.Healthcheck.Enabled && resource.Healthcheck.Path == "" {
		return fmt.Errorf("resource %q enables health checks but has no path", resource.ID)
	}
	return validateStackResource(resource, services)
}

func isStackPublicResourceProtocol(protocol string) bool {
	for _, allowed := range stackPublicResourceProtocols {
		if protocol == allowed {
			return true
		}
	}
	return false
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
