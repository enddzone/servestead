package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestProfileStackSaveAppliesPersistedSecretsFromFailedMutation(t *testing.T) {
	model, _, _ := newProfileStackSaveTestModel(t)
	model.stackOperation = profileStackOperationSave
	updatedSecrets := model.profiles[0].Secrets
	updatedSecrets.StackSecretIdentity = "persisted-identity"
	updatedSecrets.StackSecretRecipient = "persisted-recipient"

	updated, command := model.Update(profileStackOperationMsg{
		Kind: profileStackOperationSave,
		Result: profileStackOperationResult{Save: profileStackSaveResult{
			ProfileID:             model.profiles[0].Profile.ID,
			ProfileSecrets:        updatedSecrets,
			ProfileSecretsUpdated: true,
			MutationStarted:       true,
		}},
		Err: errors.New("stack metadata write failed"),
	})
	finished := updated.(profileSetupModel)
	if command != nil || finished.profileStackOperationBusy() {
		t.Fatalf("failed stack save did not settle: operation=%q command=%v", finished.stackOperation, command)
	}
	if finished.profiles[0].Secrets.StackSecretIdentity != updatedSecrets.StackSecretIdentity {
		t.Fatal("persisted profile secrets were not applied after a later mutation failed")
	}
	if !strings.Contains(finished.err, "stack metadata write failed") {
		t.Fatalf("stack save failure was lost: %q", finished.err)
	}
}

func TestProfileStackSaveStartsBusyWithoutMutatingDiskAndSnapshotsInput(t *testing.T) {
	model, repository, _ := newProfileStackSaveTestModel(t)
	updated, command := model.updateStackEditor(keyCtrl('s'))
	busy := updated.(profileSetupModel)
	if command == nil || busy.stackOperation != profileStackOperationSave {
		t.Fatalf("stack save did not start as a busy command: operation=%q command=%v", busy.stackOperation, command)
	}
	if view := busy.View().Content; !strings.Contains(view, "Saving stack configuration") {
		t.Fatalf("stack save busy state is not visible:\n%s", view)
	}
	assertStackSaveHasNotMutatedDisk(t, repository, "site")

	updated, duplicateCommand := busy.Update(keyCtrl('s'))
	busy = updated.(profileSetupModel)
	if duplicateCommand != nil || busy.stackOperation != profileStackOperationSave {
		t.Fatalf("busy stack save accepted a duplicate save: operation=%q command=%v", busy.stackOperation, duplicateCommand)
	}

	busy.stackResources[0].Subdomain = "changed-after-save-started"
	finished, _ := settleProfileStackOperation(t, busy, command)
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || stacks[0].Metadata.PublicResources[0].Subdomain != "site" {
		t.Fatalf("worker observed mutable editor state after save started: %+v", stacks)
	}
	if finished.screen != profileSetupScreenStacks || finished.err != "" {
		t.Fatalf("stack save did not return to the stack manager: screen=%d err=%q", finished.screen, finished.err)
	}
}

func TestProfileStackSaveCancellationBeforeMutationLeavesDiskUntouched(t *testing.T) {
	model, repository, store := newProfileStackSaveTestModel(t)
	model.stackEnvironment = setupTestAPIKeyEnvironment
	model.stackEnvironmentDirty = true

	updated, command := model.updateStackEditor(keyCtrl('s'))
	busy := updated.(profileSetupModel)
	updated, cancelCommand := busy.Update(keyCode(tea.KeyEscape))
	cancelling := updated.(profileSetupModel)
	if cancelCommand != nil || !cancelling.stackOperationCancelling {
		t.Fatalf("stack save cancellation did not enter the cancelling state: %+v", cancelling)
	}

	message := profileStackWorkerCommand(t, command)()
	updated, nextCommand := cancelling.Update(message)
	finished := updated.(profileSetupModel)
	if nextCommand != nil || finished.profileStackOperationBusy() || finished.err != "" {
		t.Fatalf("cancelled stack save did not settle cleanly: operation=%q err=%q command=%v", finished.stackOperation, finished.err, nextCommand)
	}
	if !strings.Contains(strings.ToLower(finished.stackNotice), "cancelled") {
		t.Fatalf("cancelled stack save did not explain the outcome: %q", finished.stackNotice)
	}
	assertStackSaveHasNotMutatedDisk(t, repository, "site")
	secrets, err := store.LoadSecrets(model.profiles[0].Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.StackSecretIdentity != "" || finished.profiles[0].Secrets.StackSecretIdentity != "" {
		t.Fatal("cancellation before mutation created a stack secret identity")
	}
}

func TestProfileStackSaveRespectsProfileLockWithoutMutatingDisk(t *testing.T) {
	model, repository, store := newProfileStackSaveTestModel(t)
	lockStore := newFileProfileStore(store.root)
	lock, err := lockStore.TryLockProfile(model.profiles[0].Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProfileOperationLock(lock)

	updated, command := model.updateStackEditor(keyCtrl('s'))
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if !strings.Contains(finished.err, errProfileOperationLocked.Error()) {
		t.Fatalf("stack save ignored the profile operation lock: %q", finished.err)
	}
	assertStackSaveHasNotMutatedDisk(t, repository, "site")
}

func TestProfileStackSaveMergesFreshStateAndUnrelatedSecrets(t *testing.T) {
	installRecordingSecretProvider(t)
	model, _, store := newProfileStackSaveTestModel(t)
	profileID := model.profiles[0].Profile.ID
	profile, latestState, err := store.Load(profileID)
	if err != nil {
		t.Fatal(err)
	}
	latestState.StackRepositoryCommit = "latest-commit"
	if err := store.Save(profile, latestState); err != nil {
		t.Fatal(err)
	}
	latestSecrets := ProfileSecrets{
		ServerSecret: "latest-server-secret",
		GitHubToken:  "latest-github-token",
	}
	if err := store.SaveSecrets(profileID, latestSecrets); err != nil {
		t.Fatal(err)
	}
	model.profiles[0].Secrets = ProfileSecrets{
		ServerSecret: "stale-server-secret",
		GitHubToken:  "stale-github-token",
	}
	model.stackEnvironment = setupTestAPIKeyEnvironment
	model.stackEnvironmentDirty = true

	updated, command := model.updateStackEditor(keyCtrl('s'))
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if finished.err != "" {
		t.Fatalf("stack save failed: %s", finished.err)
	}
	persistedSecrets, err := store.LoadSecrets(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedSecrets.ServerSecret != latestSecrets.ServerSecret || persistedSecrets.GitHubToken != latestSecrets.GitHubToken || persistedSecrets.StackSecretIdentity == "" {
		t.Fatalf("stack save clobbered freshly persisted secrets: %+v", persistedSecrets)
	}
	if finished.profiles[0].State.StackRepositoryCommit != latestState.StackRepositoryCommit || finished.profiles[0].Secrets != persistedSecrets {
		t.Fatalf("stack save left stale profile data in the TUI: %+v", finished.profiles[0])
	}
}

func TestProfileStackSaveLateCancellationFinishesConsistentMutation(t *testing.T) {
	provider := installRecordingSecretProvider(t)
	model, repository, fileStore := newProfileStackSaveTestModel(t)
	store := &blockingSaveSecretsProfileStore{
		ProfileStore: fileStore,
		saved:        make(chan struct{}),
		release:      make(chan struct{}),
	}
	model.profileStore = store
	model.stackEnvironment = setupTestAPIKeyEnvironment
	model.stackEnvironmentDirty = true

	updated, command := model.updateStackEditor(keyCtrl('s'))
	busy := updated.(profileSetupModel)
	result := make(chan tea.Msg, 1)
	worker := profileStackWorkerCommand(t, command)
	go func() { result <- worker() }()
	waitForProfileStackSignal(t, store.saved, "profile secret save")

	updated, _ = busy.Update(keyCode(tea.KeyEscape))
	cancelling := updated.(profileSetupModel)
	close(store.release)
	message := waitForProfileStackMessage(t, result)
	updated, refreshCommand := cancelling.Update(message)
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), refreshCommand)

	if finished.screen != profileSetupScreenStacks || finished.err != "" {
		t.Fatalf("late cancellation left the stack save incomplete: screen=%d err=%q", finished.screen, finished.err)
	}
	if !strings.Contains(finished.stackNotice, "completed before cancellation took effect") {
		t.Fatalf("late cancellation notice is unclear: %q", finished.stackNotice)
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || !stacks[0].Metadata.Secrets.HasSecrets() {
		t.Fatalf("late cancellation left stack metadata incomplete: %+v", stacks)
	}
	if provider.values[defaultStackSecretSource("site")]["API_KEY"] != "secret" {
		t.Fatalf("late cancellation left encrypted secrets incomplete: %+v", provider.values)
	}
	persistedSecrets, err := fileStore.LoadSecrets(model.profiles[0].Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedSecrets.StackSecretIdentity == "" ||
		finished.profiles[0].Secrets.StackSecretIdentity != persistedSecrets.StackSecretIdentity {
		t.Fatal("late cancellation did not apply the persisted profile secret identity to the model")
	}
}

func TestProfileStackSaveRenameRollsBackWhenSecretWriteFails(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment string
		wantSecret  string
	}{
		{name: "unchanged secrets", wantSecret: "secret"},
		{name: "dirty environment", environment: "API_KEY=replacement\nTOKEN=second\n", wantSecret: "replacement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testProfileStackSaveRenameRollback(t, test.environment, test.wantSecret)
		})
	}
}

func TestProfileStackSaveSameNameRollsBackSecretsAndFilesOnWriteFailure(t *testing.T) {
	for _, failedFile := range []string{stackComposeFilename, stackMetadataFilename} {
		t.Run(failedFile, func(t *testing.T) {
			testProfileStackSaveSameNameRollback(t, failedFile)
		})
	}
}

func testProfileStackSaveSameNameRollback(t *testing.T, failedFile string) {
	t.Helper()
	model, repository, store := newEncryptedProfileStackSaveTestModel(t)
	stackDirectory := filepath.Join(repository, "stacks", "site")
	before := snapshotStackSaveTestFiles(t, stackDirectory)
	model.stackCompose = strings.Replace(testApplicationCompose, "nginx:alpine", "nginx:stable", 1)
	model.stackEnvironment = "API_KEY=replacement\nSECOND=value\n"
	model.stackEnvironmentDirty = true
	installFailingStackEditorWriter(t, failedFile)

	updated, command := model.updateStackEditor(keyCtrl('s'))
	failed, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if !strings.Contains(failed.err, "injected "+failedFile+" write failure") {
		t.Fatalf("same-name save did not report the injected failure: %q", failed.err)
	}
	assertStackSaveTestFiles(t, stackDirectory, before)
	if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed same-name save left the repository scaffold behind: %v", err)
	}
	assertOriginalStackSaveSecret(t, store, model.profiles[0].Profile.ID, repository)
}

func installFailingStackEditorWriter(t *testing.T, failedFile string) {
	t.Helper()
	originalWriter := writeEditableStackFile
	writeEditableStackFile = func(root *os.Root, name string, data []byte, mode os.FileMode) error {
		if name == failedFile {
			return errors.New("injected " + failedFile + " write failure")
		}
		return atomicWriteManagedFile(root, name, data, mode)
	}
	t.Cleanup(func() { writeEditableStackFile = originalWriter })
}

func assertOriginalStackSaveSecret(t *testing.T, store ProfileStore, profileID, repository string) {
	t.Helper()
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := readManagedStackMetadata(repository, "site")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := defaultSecretProviderForName(metadata.Secrets.Provider)
	if err != nil {
		t.Fatal(err)
	}
	values, err := provider.GetStackSecrets(
		context.Background(),
		metadata.Secrets.Ref(repository, "site", secrets.StackSecretIdentity),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values["API_KEY"] != "original" {
		t.Fatalf("failed same-name save did not restore encrypted secrets: %+v", values)
	}
}

func TestProfileStackSaveRejectsManagedFileSymlinksDuringSnapshot(t *testing.T) {
	for _, name := range []string{stackComposeFilename, stackMetadataFilename, stackSecretFilename} {
		t.Run(name, func(t *testing.T) {
			assertProfileStackSaveRejectsManagedFileSymlink(t, name)
		})
	}
}

func assertProfileStackSaveRejectsManagedFileSymlink(t *testing.T, name string) {
	t.Helper()
	model, repository, _ := newEncryptedProfileStackSaveTestModel(t)
	managedPath := filepath.Join(repository, "stacks", "site", name)
	original, err := os.ReadFile(managedPath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), name)
	if err := os.Rename(managedPath, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, managedPath); err != nil {
		t.Skipf("cannot create managed-file symlink: %v", err)
	}

	updated, command := model.updateStackEditor(keyCtrl('s'))
	failed, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if !strings.Contains(failed.err, "symbolic link") || !strings.Contains(failed.err, name) {
		t.Fatalf("stack save did not reject the %s symlink: %q", name, failed.err)
	}
	unchanged, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(unchanged, original) {
		t.Fatalf("stack snapshot followed or changed the external %s target", name)
	}
}

func TestProfileStackSaveSnapshotCancelsBetweenManagedFiles(t *testing.T) {
	_, repository, _ := newEncryptedProfileStackSaveTestModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	originalReader := snapshotStackEditorManagedFile
	metadataReads := 0
	snapshotStackEditorManagedFile = func(root *os.Root, name, label string, limit int64) ([]byte, error) {
		data, err := readManagedFile(root, name, label, limit)
		if name == stackComposeFilename {
			cancel()
		}
		if name == stackMetadataFilename {
			metadataReads++
		}
		return data, err
	}
	t.Cleanup(func() { snapshotStackEditorManagedFile = originalReader })

	_, err := snapshotStackEditorMutation(ctx, stackEditorSaveRequest{
		Name: "site", OriginalName: "site", RepositoryPath: repository,
	}, stackSecretMetadata{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stack snapshot returned %v", err)
	}
	if metadataReads != 0 {
		t.Fatalf("stack snapshot read %d metadata files after cancellation", metadataReads)
	}
}

func TestProfileStackSaveSnapshotRejectsOversizedSecretFile(t *testing.T) {
	model, repository, _ := newEncryptedProfileStackSaveTestModel(t)
	secretPath := filepath.Join(repository, "stacks", "site", stackSecretFilename)
	if err := os.WriteFile(secretPath, []byte(strings.Repeat("x", int(stackSecretMaxBytes+1))), 0600); err != nil {
		t.Fatal(err)
	}

	updated, command := model.updateStackEditor(keyCtrl('s'))
	failed, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if !strings.Contains(failed.err, formatByteLimit(stackSecretMaxBytes)) {
		t.Fatalf("oversized stack secret snapshot returned %q", failed.err)
	}
}

func TestProfileStackSaveReplacesSameNameRuntimeSecrets(t *testing.T) {
	model, repository, _ := newEncryptedProfileStackSaveTestModel(t)
	model.stackEnvironment = "API_KEY=replacement\nSECOND=value\n"
	model.stackEnvironmentDirty = true

	updated, command := model.updateStackEditor(keyCtrl('s'))
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if finished.err != "" {
		t.Fatalf("same-name secret replacement failed: %s", finished.err)
	}
	metadata, err := readManagedStackMetadata(repository, "site")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := defaultSecretProviderForName(metadata.Secrets.Provider)
	if err != nil {
		t.Fatal(err)
	}
	identity := finished.profiles[0].Secrets.StackSecretIdentity
	values, err := provider.GetStackSecrets(context.Background(), metadata.Secrets.Ref(repository, "site", identity))
	if err != nil {
		t.Fatal(err)
	}
	if values["API_KEY"] != "replacement" || values["SECOND"] != "value" {
		t.Fatalf("same-name secret replacement was not preserved: %+v", values.Redacted())
	}
}

func newEncryptedProfileStackSaveTestModel(t *testing.T) (profileSetupModel, string, *fileProfileStore) {
	t.Helper()
	requireGit(t)
	originalResolver := secretProviderForName
	secretProviderForName = defaultSecretProviderForName
	t.Cleanup(func() { secretProviderForName = originalResolver })

	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID: setupTestProfileID, IP: setupTestHost, ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets := ProfileSecrets{}
	recipient, _, err := secrets.EnsureStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, secrets); err != nil {
		t.Fatal(err)
	}
	values := SecretSet{"API_KEY": "original"}
	metadata := ageStackSecretMetadata("site", values, recipient)
	if err := writeEditableStack(repository, "", stackAddOptions{Name: "site", Secrets: metadata}, []byte(testApplicationCompose)); err != nil {
		t.Fatal(err)
	}
	if err := putStackSecrets(context.Background(), repository, "site", metadata, secrets.StackSecretIdentity, values); err != nil {
		t.Fatal(err)
	}
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state, Secrets: secrets}})
	model.profileStore = store
	model.selectedIndex = 0
	model.openStackEditor(stacks[0])
	return model, repository, store
}

func testProfileStackSaveRenameRollback(t *testing.T, environment, wantSecret string) {
	t.Helper()
	model, provider, repository := newMultiResourceStackEditorModel(t)
	model = saveStackEditorRuntimeEnvironment(t, model)
	stacks, err := loadEditableStacks(repository)
	if err != nil {
		t.Fatal(err)
	}
	model.openStackEditor(stacks[0])
	if environment != "" {
		model.stackEnvironment = environment
		model.stackEnvironmentDirty = true
	}
	model.stackInputs[0].SetValue("suite-renamed")

	originalDirectory := filepath.Join(repository, "stacks", "suite")
	before := snapshotStackSaveTestFiles(t, originalDirectory)
	resolver := secretProviderForName
	failingProvider := &failOncePutSecretProvider{SecretProvider: provider, err: errors.New("injected secret write failure")}
	secretProviderForName = func(string) (SecretProvider, error) { return failingProvider, nil }
	t.Cleanup(func() { secretProviderForName = resolver })

	updated, command := model.updateStackEditor(keyCtrl('s'))
	failed, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if !strings.Contains(failed.err, "injected secret write failure") || failed.stackOriginalName != "suite" {
		t.Fatalf("failed rename did not remain retryable: original=%q err=%q", failed.stackOriginalName, failed.err)
	}
	if _, err := os.Stat(filepath.Join(repository, "stacks", "suite-renamed")); !os.IsNotExist(err) {
		t.Fatalf("failed rename left the destination behind: %v", err)
	}
	assertStackSaveTestFiles(t, originalDirectory, before)

	updated, command = failed.updateStackEditor(keyCtrl('s'))
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if finished.err != "" || finished.screen != profileSetupScreenStacks {
		t.Fatalf("retry after rolled-back rename failed: screen=%d err=%q", finished.screen, finished.err)
	}
	if provider.values[defaultStackSecretSource("suite-renamed")]["API_KEY"] != wantSecret {
		t.Fatalf("retry wrote the wrong renamed secret values: %+v", provider.values)
	}
}

func TestProfileStackSaveRequiresStoreForNewSecretIdentity(t *testing.T) {
	model, repository, _ := newProfileStackSaveTestModel(t)
	model.profileStore = nil
	model.stackEnvironment = setupTestAPIKeyEnvironment
	model.stackEnvironmentDirty = true
	updated, command := model.updateStackEditor(keyCtrl('s'))
	finished, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if finished.err != setupProfileStoreUnavailable {
		t.Fatalf("missing profile store returned %q", finished.err)
	}
	assertStackSaveHasNotMutatedDisk(t, repository, "site")
}

func newProfileStackSaveTestModel(t *testing.T) (profileSetupModel, string, *fileProfileStore) {
	t.Helper()
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                   setupTestProfileID,
		IP:                   setupTestHost,
		BaseDomain:           setupTestDomain,
		LetsEncryptEmail:     setupTestEmail,
		ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	services, err := inspectComposeServices([]byte(testApplicationCompose))
	if err != nil {
		t.Fatal(err)
	}
	resource := stackPublicResource{
		ID: "web", Service: "web", Name: "Site", Subdomain: "site", Port: 80,
		Protocol: "http", SSO: true,
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: profile,
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.profileStore = store
	model.selectedIndex = 0
	model.screen = profileSetupScreenStackEditor
	model.stackInputs = stackEditorInputs(stackAddOptions{Name: "site"})
	model.stackCompose = testApplicationCompose
	model.stackServices = services
	model.stackResources = []stackPublicResource{resource}
	model.stackResourceTable = newStackResourceTable(model.stackResources)
	model.focus = 1
	return model, repository, store
}

func assertStackSaveHasNotMutatedDisk(t *testing.T, repository, stackName string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(repository, "stacks", stackName),
		filepath.Join(repository, filepath.FromSlash(observabilityComposeRepositoryPath)),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stack save mutated %s before its worker ran: %v", path, err)
		}
	}
}

type blockingSaveSecretsProfileStore struct {
	ProfileStore
	saved   chan struct{}
	release chan struct{}
}

type failOncePutSecretProvider struct {
	SecretProvider
	err error
}

func (provider *failOncePutSecretProvider) PutStackSecrets(ctx context.Context, ref StackSecretRef, values SecretSet) error {
	if provider.err != nil {
		err := provider.err
		provider.err = nil
		return err
	}
	return provider.SecretProvider.PutStackSecrets(ctx, ref, values)
}

func snapshotStackSaveTestFiles(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{stackComposeFilename, stackMetadataFilename, stackSecretFilename} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = data
	}
	return files
}

func assertStackSaveTestFiles(t *testing.T, directory string, want map[string][]byte) {
	t.Helper()
	for name, wantData := range want {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(wantData) {
			t.Fatalf("failed rename changed %s", name)
		}
	}
}

func (store *blockingSaveSecretsProfileStore) SaveSecrets(profileID string, secrets ProfileSecrets) error {
	if err := store.ProfileStore.SaveSecrets(profileID, secrets); err != nil {
		return err
	}
	close(store.saved)
	<-store.release
	return nil
}
