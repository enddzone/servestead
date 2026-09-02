package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type stackEditorSaveRequest struct {
	Name             string
	OriginalName     string
	RepositoryPath   string
	Profile          Profile
	ProfileSecrets   ProfileSecrets
	ProfileStore     ProfileStore
	Compose          []byte
	Resources        []stackPublicResource
	Services         []composeServiceSummary
	Environment      string
	EnvironmentDirty bool
	MetadataMissing  bool
}

type stackEditorSecretPlan struct {
	Metadata       stackSecretMetadata
	Values         SecretSet
	Identity       string
	RenameExisting bool
}

type preparedStackEditorSave struct {
	Request         stackEditorSaveRequest
	CurrentSecrets  stackSecretMetadata
	Options         stackAddOptions
	SecretPlan      stackEditorSecretPlan
	ProfileSecrets  ProfileSecrets
	SecretsChanged  bool
	RemovalIdentity string
	Snapshot        stackEditorMutationSnapshot
}

type stackEditorMutationSnapshot struct {
	RepositoryPath           string
	SourceDirectory          string
	DestinationDirectory     string
	SourceDirectoryExisted   bool
	ScaffoldDirectory        string
	ScaffoldDirectoryExisted bool
	Files                    []stackEditorFileSnapshot
	Secrets                  *stackEditorSecretSnapshot
}

type stackEditorSecretSnapshot struct {
	StackName string
	Metadata  stackSecretMetadata
	Values    SecretSet
	Identity  string
}

type stackEditorFileSnapshot struct {
	Path      string
	StackName string
	Name      string
	Data      []byte
	Mode      os.FileMode
	Exists    bool
}

type profileStackSaveResult struct {
	ProfileID             string
	ProfileSecrets        ProfileSecrets
	ProfileSecretsUpdated bool
	OriginalName          string
	MetadataMissing       bool
	ScaffoldCreated       bool
	MutationStarted       bool
	CancellationObserved  bool
}

func (model profileSetupModel) stackEditorSaveOperationRequest() (profileStackOperationRequest, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return profileStackOperationRequest{}, errors.New(setupNoProfileSelectedMessage)
	}
	if len(model.stackInputs) == 0 {
		return profileStackOperationRequest{}, errors.New("stack editor is not ready")
	}
	choice := model.profiles[model.selectedIndex]
	repositoryPath := strings.TrimSpace(choice.Profile.ConfigRepositoryPath)
	if repositoryPath == "" {
		return profileStackOperationRequest{}, errors.New("configuration repository is not ready; run Platform once before managing stacks")
	}
	request := stackEditorSaveRequest{
		Name:             strings.TrimSpace(model.stackInputs[0].Value()),
		OriginalName:     model.stackOriginalName,
		RepositoryPath:   repositoryPath,
		Profile:          cloneStackEditorProfile(choice.Profile),
		ProfileSecrets:   choice.Secrets,
		ProfileStore:     model.profileStore,
		Compose:          append([]byte(nil), []byte(model.stackCompose)...),
		Resources:        append([]stackPublicResource(nil), model.stackResources...),
		Services:         cloneComposeServiceSummaries(model.stackServices),
		Environment:      model.stackEnvironment,
		EnvironmentDirty: model.stackEnvironmentDirty,
		MetadataMissing:  model.stackMetadataMissing,
	}
	return profileStackOperationRequest{
		Kind:           profileStackOperationSave,
		RepositoryPath: repositoryPath,
		Choice:         profileChoice{Profile: request.Profile, Secrets: request.ProfileSecrets},
		ProfileStore:   model.profileStore,
		Save:           &request,
	}, nil
}

func cloneStackEditorProfile(profile Profile) Profile {
	cloned := profile
	if profile.Cloud != nil {
		cloud := *profile.Cloud
		if profile.Cloud.DestroyedAt != nil {
			destroyedAt := *profile.Cloud.DestroyedAt
			cloud.DestroyedAt = &destroyedAt
		}
		cloned.Cloud = &cloud
	}
	return cloned
}

func cloneComposeServiceSummaries(services []composeServiceSummary) []composeServiceSummary {
	cloned := make([]composeServiceSummary, len(services))
	for index, service := range services {
		cloned[index] = service
		cloned[index].ContainerPorts = append([]int(nil), service.ContainerPorts...)
	}
	return cloned
}

func runStackEditorSave(ctx context.Context, request stackEditorSaveRequest) (profileStackSaveResult, error) {
	result := profileStackSaveResult{
		ProfileID:             request.Profile.ID,
		ProfileSecrets:        request.ProfileSecrets,
		ProfileSecretsUpdated: request.ProfileStore != nil,
		OriginalName:          request.OriginalName,
		MetadataMissing:       request.MetadataMissing,
	}
	prepared, err := prepareStackEditorSave(ctx, request)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	result.MutationStarted = true
	mutationCtx := context.WithoutCancel(ctx)
	if prepared.SecretsChanged {
		if prepared.Request.ProfileStore != nil {
			if err := prepared.Request.ProfileStore.SaveSecrets(prepared.Request.Profile.ID, prepared.ProfileSecrets); err != nil {
				return result, err
			}
		}
		result.ProfileSecrets = prepared.ProfileSecrets
	}
	result.ScaffoldCreated, err = ensureStackEditorScaffold(mutationCtx, prepared.Request.RepositoryPath, prepared.Request.Profile)
	if err != nil {
		return result, rollbackStackEditorAfterError(prepared, err)
	}
	secretsWritten, err := writeStackEditorSecretsBeforeMetadata(mutationCtx, prepared)
	if err != nil {
		return result, rollbackStackEditorAfterError(prepared, err)
	}
	if err := writeEditableStack(
		prepared.Request.RepositoryPath,
		prepared.Request.OriginalName,
		prepared.Options,
		prepared.Request.Compose,
	); err != nil {
		return result, rollbackStackEditorAfterError(prepared, err)
	}
	if err := reconcileStackEditorSecrets(mutationCtx, prepared, secretsWritten); err != nil {
		return result, rollbackStackEditorAfterError(prepared, err)
	}
	result.CancellationObserved = ctx.Err() != nil
	return result, nil
}

func prepareStackEditorSave(ctx context.Context, request stackEditorSaveRequest) (preparedStackEditorSave, error) {
	if err := ctx.Err(); err != nil {
		return preparedStackEditorSave{}, err
	}
	currentSecrets, err := loadCurrentStackEditorSecrets(request)
	if err != nil {
		return preparedStackEditorSave{}, err
	}
	secretPlan, profileSecrets, secretsChanged, removalIdentity, err := prepareStackEditorSecretPlan(ctx, request, currentSecrets)
	if err != nil {
		return preparedStackEditorSave{}, err
	}
	options := stackAddOptions{
		Name:      request.Name,
		Resources: append([]stackPublicResource(nil), request.Resources...),
		Secrets:   cloneStackSecretMetadata(secretPlan.Metadata),
	}
	metadata := stackMetadata{Version: 1, PublicResources: options.Resources, Secrets: options.Secrets}
	if err := validateStackMetadata(request.Name, metadata, request.Services); err != nil {
		return preparedStackEditorSave{}, err
	}
	if _, err := inspectComposeServices(request.Compose); err != nil {
		return preparedStackEditorSave{}, err
	}
	if err := validateStackEditorDestination(request.RepositoryPath, request.OriginalName, request.Name); err != nil {
		return preparedStackEditorSave{}, err
	}
	snapshot, err := snapshotStackEditorMutation(ctx, request, currentSecrets)
	if err != nil {
		return preparedStackEditorSave{}, err
	}
	if err := ctx.Err(); err != nil {
		return preparedStackEditorSave{}, err
	}
	return preparedStackEditorSave{
		Request:         request,
		CurrentSecrets:  currentSecrets,
		Options:         options,
		SecretPlan:      secretPlan,
		ProfileSecrets:  profileSecrets,
		SecretsChanged:  secretsChanged,
		RemovalIdentity: removalIdentity,
		Snapshot:        snapshot,
	}, nil
}

func snapshotStackEditorMutation(ctx context.Context, request stackEditorSaveRequest, currentSecrets stackSecretMetadata) (stackEditorMutationSnapshot, error) {
	snapshot, stackName, err := newStackEditorMutationSnapshot(request)
	if err != nil {
		return stackEditorMutationSnapshot{}, err
	}
	stackFiles, err := captureStackEditorFiles(
		ctx,
		snapshot.RepositoryPath,
		stackName,
		snapshot.SourceDirectoryExisted,
		[]string{stackComposeFilename, stackMetadataFilename, stackSecretFilename},
	)
	if err != nil {
		return stackEditorMutationSnapshot{}, err
	}
	scaffoldFiles, err := captureStackEditorFiles(
		ctx,
		snapshot.RepositoryPath,
		reservedObservabilityStackName,
		snapshot.ScaffoldDirectoryExisted,
		[]string{stackComposeFilename},
	)
	if err != nil {
		return stackEditorMutationSnapshot{}, err
	}
	snapshot.Files = append(stackFiles, scaffoldFiles...)
	snapshot.Secrets, err = captureStackEditorSecretSnapshot(ctx, request, currentSecrets, stackName)
	return snapshot, err
}

func newStackEditorMutationSnapshot(request stackEditorSaveRequest) (stackEditorMutationSnapshot, string, error) {
	repositoryPath := expandUserPath(request.RepositoryPath)
	stackName := firstNonEmpty(request.OriginalName, request.Name)
	sourceExisted, err := managedStackDirectoryExists(repositoryPath, stackName)
	if err != nil {
		return stackEditorMutationSnapshot{}, "", err
	}
	scaffoldExisted, err := managedStackDirectoryExists(repositoryPath, reservedObservabilityStackName)
	if err != nil {
		return stackEditorMutationSnapshot{}, "", err
	}
	return stackEditorMutationSnapshot{
		RepositoryPath:           repositoryPath,
		SourceDirectory:          filepath.Join(repositoryPath, "stacks", stackName),
		DestinationDirectory:     filepath.Join(repositoryPath, "stacks", request.Name),
		SourceDirectoryExisted:   sourceExisted,
		ScaffoldDirectory:        filepath.Join(repositoryPath, "stacks", reservedObservabilityStackName),
		ScaffoldDirectoryExisted: scaffoldExisted,
		Files:                    make([]stackEditorFileSnapshot, 0, 4),
	}, stackName, nil
}

func captureStackEditorFiles(ctx context.Context, repositoryPath, stackName string, directoryExists bool, names []string) ([]stackEditorFileSnapshot, error) {
	var directory *os.Root
	var err error
	if directoryExists {
		directory, err = openManagedStackRoot(repositoryPath, stackName, false)
		if err != nil {
			return nil, err
		}
		defer directory.Close()
	}
	files := make([]stackEditorFileSnapshot, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := snapshotStackEditorFile(directory, repositoryPath, stackName, name)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func captureStackEditorSecretSnapshot(
	ctx context.Context,
	request stackEditorSaveRequest,
	currentSecrets stackSecretMetadata,
	stackName string,
) (*stackEditorSecretSnapshot, error) {
	if !currentSecrets.HasSecrets() || !request.EnvironmentDirty && request.OriginalName == request.Name {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, _, err := request.ProfileSecrets.StackSecretIdentityPair()
	if err != nil {
		return nil, err
	}
	provider, err := secretProviderForName(currentSecrets.Provider)
	if err != nil {
		return nil, err
	}
	values, err := provider.GetStackSecrets(ctx, currentSecrets.Ref(request.RepositoryPath, stackName, identity))
	if err != nil {
		return nil, err
	}
	return &stackEditorSecretSnapshot{
		StackName: stackName,
		Metadata:  cloneStackSecretMetadata(currentSecrets),
		Values:    cloneSecretSet(values),
		Identity:  identity,
	}, nil
}

var snapshotStackEditorManagedFile = readManagedFile

func snapshotStackEditorFile(directory *os.Root, repositoryPath, stackName, name string) (stackEditorFileSnapshot, error) {
	path := filepath.Join(repositoryPath, "stacks", stackName, name)
	snapshot := stackEditorFileSnapshot{Path: path, StackName: stackName, Name: name}
	if directory == nil {
		return snapshot, nil
	}
	info, err := directory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return stackEditorFileSnapshot{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return stackEditorFileSnapshot{}, fmt.Errorf("stack %s %s is a symbolic link", stackName, name)
	}
	limit := stackRepositoryFileMaxBytes
	switch name {
	case stackComposeFilename:
		limit = stackComposeMaxBytes
	case stackMetadataFilename:
		limit = stackMetadataMaxBytes
	case stackSecretFilename:
		limit = stackSecretMaxBytes
	}
	data, err := snapshotStackEditorManagedFile(directory, name, fmt.Sprintf("stack %s %s", stackName, name), limit)
	if err != nil {
		return stackEditorFileSnapshot{}, err
	}
	snapshot.Data = data
	snapshot.Mode = info.Mode().Perm()
	snapshot.Exists = true
	return snapshot, nil
}

func rollbackStackEditorAfterError(prepared preparedStackEditorSave, mutationErr error) error {
	if err := rollbackStackEditorMutation(prepared); err != nil {
		return errors.Join(mutationErr, errors.New("restore stack after failed save: "+err.Error()))
	}
	return mutationErr
}

func rollbackStackEditorMutation(prepared preparedStackEditorSave) error {
	if err := rollbackStackEditorRename(prepared.Snapshot); err != nil {
		return err
	}
	if err := restoreStackEditorSecretProvider(prepared); err != nil {
		return err
	}
	if err := restoreStackEditorFiles(prepared.Snapshot.RepositoryPath, prepared.Snapshot.Files); err != nil {
		return err
	}
	return removeNewStackEditorDirectories(prepared.Snapshot)
}

func rollbackStackEditorRename(snapshot stackEditorMutationSnapshot) error {
	if snapshot.SourceDirectory == snapshot.DestinationDirectory {
		return nil
	}
	stacksDirectory, err := openManagedStacksRoot(snapshot.RepositoryPath, false)
	if err != nil {
		return err
	}
	defer stacksDirectory.Close()
	sourceName := filepath.Base(snapshot.SourceDirectory)
	destinationName := filepath.Base(snapshot.DestinationDirectory)
	if info, err := stacksDirectory.Lstat(sourceName); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed stack directory %q is a symbolic link", sourceName)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := stacksDirectory.Lstat(destinationName); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed stack directory %q is a symbolic link", destinationName)
	}
	return stacksDirectory.Rename(destinationName, sourceName)
}

func restoreStackEditorFiles(repositoryPath string, files []stackEditorFileSnapshot) error {
	for _, file := range files {
		directory, err := openManagedStackRoot(repositoryPath, file.StackName, file.Exists)
		if errors.Is(err, os.ErrNotExist) && !file.Exists {
			continue
		}
		if err != nil {
			return err
		}
		if file.Exists {
			err = atomicWriteManagedFile(directory, file.Name, file.Data, file.Mode)
			_ = directory.Close()
			if err != nil {
				return err
			}
			continue
		}
		if err := rejectManagedFileSymlink(directory, file.Name, fmt.Sprintf("stack %s %s", file.StackName, file.Name)); err != nil {
			_ = directory.Close()
			return err
		}
		err = directory.Remove(file.Name)
		_ = directory.Close()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func removeNewStackEditorDirectories(snapshot stackEditorMutationSnapshot) error {
	if err := removeStackEditorDirectoryIfNew(snapshot.RepositoryPath, filepath.Base(snapshot.SourceDirectory), snapshot.SourceDirectoryExisted); err != nil {
		return err
	}
	return removeStackEditorDirectoryIfNew(snapshot.RepositoryPath, filepath.Base(snapshot.ScaffoldDirectory), snapshot.ScaffoldDirectoryExisted)
}

func removeStackEditorDirectoryIfNew(repositoryPath, name string, existed bool) error {
	if existed {
		return nil
	}
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer stacksDirectory.Close()
	info, err := stacksDirectory.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed stack directory %q is a symbolic link", name)
	}
	if err := stacksDirectory.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func restoreStackEditorSecretProvider(prepared preparedStackEditorSave) error {
	snapshot := prepared.Snapshot.Secrets
	if snapshot != nil {
		provider, err := secretProviderForName(snapshot.Metadata.Provider)
		if err != nil {
			return err
		}
		if prepared.Request.Name != snapshot.StackName && prepared.SecretPlan.Metadata.HasSecrets() {
			if err := provider.DeleteStackSecrets(
				context.Background(),
				prepared.SecretPlan.Metadata.Ref(prepared.Request.RepositoryPath, prepared.Request.Name, prepared.SecretPlan.Identity),
				nil,
			); err != nil {
				return err
			}
		}
		return provider.PutStackSecrets(
			context.Background(),
			snapshot.Metadata.Ref(prepared.Request.RepositoryPath, snapshot.StackName, snapshot.Identity),
			snapshot.Values,
		)
	}
	if !prepared.SecretPlan.Metadata.HasSecrets() || prepared.SecretPlan.Identity == "" {
		return nil
	}
	provider, err := secretProviderForName(prepared.SecretPlan.Metadata.Provider)
	if err != nil {
		return err
	}
	return provider.DeleteStackSecrets(
		context.Background(),
		prepared.SecretPlan.Metadata.Ref(prepared.Request.RepositoryPath, prepared.Request.Name, prepared.SecretPlan.Identity),
		nil,
	)
}

func loadCurrentStackEditorSecrets(request stackEditorSaveRequest) (stackSecretMetadata, error) {
	if request.OriginalName == "" {
		return stackSecretMetadata{}, nil
	}
	existing, err := readManagedStackMetadata(request.RepositoryPath, request.OriginalName)
	if errors.Is(err, os.ErrNotExist) {
		return stackSecretMetadata{}, nil
	}
	if err != nil {
		return stackSecretMetadata{}, err
	}
	return cloneStackSecretMetadata(existing.Secrets), nil
}

func prepareStackEditorSecretPlan(
	ctx context.Context,
	request stackEditorSaveRequest,
	currentSecrets stackSecretMetadata,
) (stackEditorSecretPlan, ProfileSecrets, bool, string, error) {
	if request.EnvironmentDirty {
		return prepareDirtyStackEditorSecretPlan(request, currentSecrets)
	}
	return prepareExistingStackEditorSecretPlan(ctx, request, currentSecrets)
}

func prepareDirtyStackEditorSecretPlan(
	request stackEditorSaveRequest,
	currentSecrets stackSecretMetadata,
) (stackEditorSecretPlan, ProfileSecrets, bool, string, error) {
	profileSecrets := request.ProfileSecrets
	if request.Environment == "" {
		if !currentSecrets.HasSecrets() {
			return stackEditorSecretPlan{}, profileSecrets, false, "", nil
		}
		identity, _, err := profileSecrets.StackSecretIdentityPair()
		return stackEditorSecretPlan{}, profileSecrets, false, identity, err
	}
	values, _, err := parseEnvironmentSecretSet(request.Environment)
	if err != nil {
		return stackEditorSecretPlan{}, profileSecrets, false, "", err
	}
	recipient, changed, err := profileSecrets.EnsureStackSecretIdentity()
	if err != nil {
		return stackEditorSecretPlan{}, profileSecrets, false, "", err
	}
	if changed && request.ProfileStore == nil {
		return stackEditorSecretPlan{}, request.ProfileSecrets, false, "", errors.New(setupProfileStoreUnavailable)
	}
	return stackEditorSecretPlan{
		Metadata: ageStackSecretMetadata(request.Name, values, recipient),
		Values:   cloneSecretSet(values),
		Identity: profileSecrets.StackSecretIdentity,
	}, profileSecrets, changed, "", nil
}

func prepareExistingStackEditorSecretPlan(
	ctx context.Context,
	request stackEditorSaveRequest,
	currentSecrets stackSecretMetadata,
) (stackEditorSecretPlan, ProfileSecrets, bool, string, error) {
	profileSecrets := request.ProfileSecrets
	if !currentSecrets.HasSecrets() {
		return stackEditorSecretPlan{}, profileSecrets, false, "", nil
	}
	plan := stackEditorSecretPlan{Metadata: cloneStackSecretMetadata(currentSecrets)}
	if request.OriginalName != "" && request.OriginalName != request.Name {
		identity, _, err := profileSecrets.StackSecretIdentityPair()
		if err != nil {
			return stackEditorSecretPlan{}, profileSecrets, false, "", err
		}
		provider, err := secretProviderForName(currentSecrets.Provider)
		if err != nil {
			return stackEditorSecretPlan{}, profileSecrets, false, "", err
		}
		values, err := provider.GetStackSecrets(ctx, currentSecrets.Ref(request.RepositoryPath, request.OriginalName, identity))
		if err != nil {
			return stackEditorSecretPlan{}, profileSecrets, false, "", err
		}
		plan.Values = cloneSecretSet(values)
		plan.Identity = identity
		plan.RenameExisting = true
	}
	plan.Metadata.Source = defaultStackSecretSource(request.Name)
	return plan, profileSecrets, false, "", nil
}

func cloneStackSecretMetadata(metadata stackSecretMetadata) stackSecretMetadata {
	cloned := metadata
	cloned.Recipients = append([]string(nil), metadata.Recipients...)
	cloned.Keys = append([]stackSecretKeyMetadata(nil), metadata.Keys...)
	return cloned
}

func cloneSecretSet(values SecretSet) SecretSet {
	if values == nil {
		return nil
	}
	cloned := make(SecretSet, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func validateStackEditorDestination(repositoryPath, originalName, name string) error {
	if err := validateStackName(name); err != nil {
		return err
	}
	if originalName != "" {
		if err := validateOriginalStackName(originalName); err != nil {
			return err
		}
	}
	stacksDirectory, err := openManagedStacksRoot(repositoryPath, false)
	if errors.Is(err, os.ErrNotExist) {
		if originalName == "" {
			return nil
		}
		return err
	}
	if err != nil {
		return err
	}
	defer stacksDirectory.Close()
	if originalName == "" {
		return validateNewEditableStackDestination(stacksDirectory, name)
	}
	if originalName == name {
		return validateExistingEditableStackDestination(stacksDirectory, name)
	}
	return validateStackEditorRenameDestination(stacksDirectory, originalName, name)
}

func validateOriginalStackName(name string) error {
	if err := validateStackName(name); err != nil {
		if !stackSlugPattern.MatchString(name) {
			return errors.New("original stack name must be a lowercase DNS label")
		}
		return err
	}
	return nil
}

func validateStackEditorRenameDestination(stacksDirectory *os.Root, originalName, name string) error {
	if info, err := stacksDirectory.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed stack directory %q is a symbolic link", name)
		}
		return errors.New("stack \"" + name + "\" already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := stacksDirectory.Lstat(originalName)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed stack directory %q is a symbolic link", originalName)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed stack directory %q is not a directory", originalName)
	}
	return nil
}

func writeStackEditorSecretsBeforeMetadata(ctx context.Context, prepared preparedStackEditorSave) (bool, error) {
	canWrite := prepared.Request.EnvironmentDirty &&
		prepared.SecretPlan.Metadata.HasSecrets() &&
		(prepared.Request.OriginalName == "" || prepared.Request.OriginalName == prepared.Request.Name)
	if !canWrite {
		return false, nil
	}
	return true, putStackSecrets(
		ctx,
		prepared.Request.RepositoryPath,
		prepared.Request.Name,
		prepared.SecretPlan.Metadata,
		prepared.SecretPlan.Identity,
		prepared.SecretPlan.Values,
	)
}

func reconcileStackEditorSecrets(ctx context.Context, prepared preparedStackEditorSave, secretsWritten bool) error {
	if prepared.Request.EnvironmentDirty {
		if prepared.SecretPlan.Metadata.HasSecrets() {
			if secretsWritten {
				return nil
			}
			return putStackSecrets(
				ctx,
				prepared.Request.RepositoryPath,
				prepared.Request.Name,
				prepared.SecretPlan.Metadata,
				prepared.SecretPlan.Identity,
				prepared.SecretPlan.Values,
			)
		}
		if !prepared.CurrentSecrets.HasSecrets() {
			return nil
		}
		currentSecrets := cloneStackSecretMetadata(prepared.CurrentSecrets)
		currentSecrets.Source = defaultStackSecretSource(prepared.Request.Name)
		return removeStackSecrets(
			ctx,
			prepared.Request.RepositoryPath,
			prepared.Request.Name,
			currentSecrets,
			prepared.RemovalIdentity,
		)
	}
	if prepared.SecretPlan.RenameExisting {
		return putStackSecrets(
			ctx,
			prepared.Request.RepositoryPath,
			prepared.Request.Name,
			prepared.SecretPlan.Metadata,
			prepared.SecretPlan.Identity,
			prepared.SecretPlan.Values,
		)
	}
	return nil
}

func ensureStackEditorScaffold(ctx context.Context, repositoryPath string, profile Profile) (bool, error) {
	return ensureConfigRepositoryScaffold(ctx, repositoryPath, observabilityComposeFile(observabilityConfig{
		BaseDomain: profile.BaseDomain,
		AdminEmail: firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail),
	}))
}
