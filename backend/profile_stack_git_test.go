package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestProfileStackOperationLocksInputAndWaitsForCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "ctrl+c", key: keyCtrl('c')},
		{name: "escape", key: keyCode(tea.KeyEscape)},
	} {
		t.Run(test.name, func(t *testing.T) {
			testProfileStackOperationCancellation(t, test.key)
		})
	}
}

func testProfileStackOperationCancellation(t *testing.T, cancellationKey tea.KeyMsg) {
	t.Helper()
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })

	started := make(chan struct{})
	cancelled := make(chan struct{})
	calls := 0
	runProfileStackOperation = func(ctx context.Context, _ profileStackOperationRequest) (profileStackOperationResult, error) {
		calls++
		close(started)
		<-ctx.Done()
		close(cancelled)
		return profileStackOperationResult{}, ctx.Err()
	}

	model := newProfileStackOperationTestModel(t)
	updated, command := model.updateStacks(keyRunes("v"))
	busy := updated.(profileSetupModel)
	if command == nil || busy.stackOperation != profileStackOperationDiff {
		t.Fatalf("diff did not start as a busy command: operation=%q command=%v", busy.stackOperation, command)
	}
	view := busy.View().Content
	if !strings.Contains(view, "Loading stack repository diff") || !strings.Contains(view, "Other keys are disabled") {
		t.Fatalf("busy operation is not visible:\n%s", view)
	}

	result := make(chan tea.Msg, 1)
	worker := profileStackWorkerCommand(t, command)
	go func() { result <- worker() }()
	waitForProfileStackSignal(t, started, "operation start")

	updated, duplicateCommand := busy.Update(keyRunes("g"))
	busy = updated.(profileSetupModel)
	if duplicateCommand != nil || calls != 1 || busy.stackOperation != profileStackOperationDiff {
		t.Fatalf("busy operation accepted a duplicate action: calls=%d operation=%q command=%v", calls, busy.stackOperation, duplicateCommand)
	}
	updated, quitCommand := busy.Update(keyRunes("q"))
	busy = updated.(profileSetupModel)
	if quitCommand != nil || busy.quit || busy.cancelled || busy.stackOperationCancelling {
		t.Fatalf("q changed a busy operation: quit=%v cancelled=%v cancelling=%v command=%v", busy.quit, busy.cancelled, busy.stackOperationCancelling, quitCommand)
	}

	updated, cancelCommand := busy.Update(cancellationKey)
	cancelling := updated.(profileSetupModel)
	if cancelCommand != nil || !cancelling.stackOperationCancelling || cancelling.stackOperation != profileStackOperationDiff {
		t.Fatalf("cancellation did not wait for acknowledgement: operation=%q cancelling=%v command=%v", cancelling.stackOperation, cancelling.stackOperationCancelling, cancelCommand)
	}
	if view = cancelling.View().Content; !strings.Contains(view, "Cancelling loading stack repository diff") {
		t.Fatalf("cancelling state is not visible:\n%s", view)
	}
	waitForProfileStackSignal(t, cancelled, "context cancellation")

	message := waitForProfileStackMessage(t, result)
	updated, nextCommand := cancelling.Update(message)
	finished := updated.(profileSetupModel)
	if nextCommand != nil || finished.profileStackOperationBusy() || finished.stackOperationCancelling {
		t.Fatalf("cancellation acknowledgement did not clear busy state: operation=%q cancelling=%v command=%v", finished.stackOperation, finished.stackOperationCancelling, nextCommand)
	}
	if finished.err != "" || !strings.Contains(strings.ToLower(finished.stackNotice), "cancelled") {
		t.Fatalf("cancellation result is unclear: err=%q notice=%q", finished.err, finished.stackNotice)
	}
}

func TestProfileStackSyncCancellationRaceDoesNotStartRun(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })

	started := make(chan struct{})
	release := make(chan struct{})
	kind := make(chan profileStackOperationKind, 1)
	runProfileStackOperation = func(_ context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
		kind <- request.Kind
		close(started)
		<-release
		return profileStackOperationResult{Snapshot: profileStackRepositorySnapshot{
			Stacks:     []editableStack{{Name: "site"}},
			GitStatus:  "clean",
			Head:       "abc123",
			SyncStatus: "sync required",
		}}, nil
	}

	model := newProfileStackOperationTestModel(t)
	updated, command := model.runStackSync()
	busy := updated.(profileSetupModel)
	result := make(chan tea.Msg, 1)
	worker := profileStackWorkerCommand(t, command)
	go func() { result <- worker() }()
	waitForProfileStackSignal(t, started, "synchronization precheck")
	if operation := <-kind; operation != profileStackOperationSync {
		t.Fatalf("unexpected operation: %q", operation)
	}

	updated, _ = busy.Update(keyCode(tea.KeyEscape))
	cancelling := updated.(profileSetupModel)
	close(release)
	updated, nextCommand := cancelling.Update(waitForProfileStackMessage(t, result))
	finished := updated.(profileSetupModel)
	if nextCommand != nil || finished.done || finished.singleStage != "" {
		t.Fatalf("cancelled synchronization precheck started a run: done=%v stage=%q command=%v", finished.done, finished.singleStage, nextCommand)
	}
	if !strings.Contains(finished.stackNotice, "Synchronization was not started") {
		t.Fatalf("sync cancellation race notice is unclear: %q", finished.stackNotice)
	}
}

func TestProfileStackOperationUsesTUIContext(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })

	started := make(chan struct{})
	runProfileStackOperation = func(ctx context.Context, _ profileStackOperationRequest) (profileStackOperationResult, error) {
		close(started)
		<-ctx.Done()
		return profileStackOperationResult{}, ctx.Err()
	}

	parentCtx, cancel := context.WithCancel(context.Background())
	model := newProfileStackOperationTestModel(t)
	model.tuiContext = parentCtx
	updated, command := model.openStackDiff()
	busy := updated.(profileSetupModel)
	result := make(chan tea.Msg, 1)
	worker := profileStackWorkerCommand(t, command)
	go func() { result <- worker() }()
	waitForProfileStackSignal(t, started, "operation start")
	cancel()

	message := waitForProfileStackMessage(t, result).(profileStackOperationMsg)
	if !errors.Is(message.Err, context.Canceled) {
		t.Fatalf("parent TUI cancellation was not propagated: %v", message.Err)
	}
	updated, _ = busy.Update(message)
	if finished := updated.(profileSetupModel); finished.profileStackOperationBusy() || !errors.Is(message.Err, context.Canceled) {
		t.Fatalf("parent cancellation left operation busy: operation=%q", finished.stackOperation)
	}
}

func TestProfileStackDashboardRefreshStartsAsCommand(t *testing.T) {
	model := newProfileStackOperationTestModel(t)
	model.screen = profileSetupScreenPicker
	model.profileList.SetItems(profilePickerItems(model.profiles))
	model.profileList.Select(0)

	updated, command := model.updateProfilePicker(keyCode(tea.KeyEnter))
	busy := updated.(profileSetupModel)
	if command == nil || busy.screen != profileSetupScreenDashboard || busy.stackOperation != profileStackOperationRefresh {
		t.Fatalf("profile selection did not start repository refresh as a command: screen=%d operation=%q command=%v", busy.screen, busy.stackOperation, command)
	}
	busy.finishProfileStackOperation()
}

func TestProfileStackMutationRefreshesThroughTypedCommands(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })

	var requests []profileStackOperationRequest
	runProfileStackOperation = func(_ context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
		requests = append(requests, request)
		if request.Kind == profileStackOperationRefresh {
			return profileStackOperationResult{Snapshot: profileStackRepositorySnapshot{
				Stacks:     []editableStack{{Name: "site"}},
				GitStatus:  "modified",
				SyncStatus: "commit required",
			}}, nil
		}
		return profileStackOperationResult{}, nil
	}

	model := newProfileStackOperationTestModel(t)
	updated, command := model.stageStackGitChanges()
	result, nextCommand := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if nextCommand != nil || result.profileStackOperationBusy() {
		t.Fatalf("stage/refresh command chain did not settle: operation=%q command=%v", result.stackOperation, nextCommand)
	}
	if len(requests) != 2 || requests[0].Kind != profileStackOperationStage || requests[1].Kind != profileStackOperationRefresh {
		t.Fatalf("unexpected operation sequence: %+v", requests)
	}
	if result.stackGitStatus != "modified" || !strings.Contains(result.stackNotice, "staged") {
		t.Fatalf("stage result did not apply refreshed state: status=%q notice=%q", result.stackGitStatus, result.stackNotice)
	}
}

func TestProfileStackCommitRunsAsCommandAndRefreshes(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })

	var requests []profileStackOperationRequest
	runProfileStackOperation = func(_ context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
		requests = append(requests, request)
		if request.Kind == profileStackOperationRefresh {
			return profileStackOperationResult{Snapshot: profileStackRepositorySnapshot{
				Stacks:     []editableStack{{Name: "site"}},
				GitStatus:  "clean",
				Head:       "abc123",
				SyncStatus: "sync required",
			}}, nil
		}
		return profileStackOperationResult{}, nil
	}

	model := newProfileStackOperationTestModel(t)
	model.screen = profileSetupScreenStackCommit
	model.stackCommitInput.SetValue("Update site")
	updated, command := model.updateStackCommit(keyCode(tea.KeyEnter))
	result, _ := settleProfileStackOperation(t, updated.(profileSetupModel), command)
	if len(requests) != 2 || requests[0].Kind != profileStackOperationCommit || requests[0].CommitMessage != "Update site" || requests[1].Kind != profileStackOperationRefresh {
		t.Fatalf("unexpected commit operation sequence: %+v", requests)
	}
	if result.screen != profileSetupScreenStacks || result.stackCommitInput.Focused() || !strings.Contains(result.stackNotice, "Committed stack changes") {
		t.Fatalf("commit result was not applied: screen=%d focused=%v notice=%q", result.screen, result.stackCommitInput.Focused(), result.stackNotice)
	}
}

func TestProfileStackGitMutationRespectsLockAndReloadsProfileData(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackComposeFilename), []byte(testApplicationCompose), 0600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	lockedStore := newFileProfileStore(root)
	mutationStore := newFileProfileStore(root)
	profile, err := lockedStore.Create(Profile{
		ID:                   setupTestProfileID,
		IP:                   setupTestHost,
		ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleProfile, staleState, err := lockedStore.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	latestState := staleState
	latestState.StackRepositoryCommit = "latest-commit"
	if err := lockedStore.Save(profile, latestState); err != nil {
		t.Fatal(err)
	}
	latestSecrets := ProfileSecrets{GitHubToken: "latest-token", StackSecretIdentity: "latest-identity"}
	if err := lockedStore.SaveSecrets(profile.ID, latestSecrets); err != nil {
		t.Fatal(err)
	}
	request := profileStackOperationRequest{
		Kind:           profileStackOperationStage,
		RepositoryPath: repository,
		Choice:         profileChoice{Profile: staleProfile, State: staleState},
		ProfileStore:   mutationStore,
	}

	lock, err := lockedStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defaultRunProfileStackOperation(context.Background(), request); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("Git mutation ignored the profile operation lock: %v", err)
	}
	staged, err := runGit(context.Background(), repository, nil, "diff", "--cached", "--name-only", "--", "stacks")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(staged) != "" {
		t.Fatalf("locked Git mutation staged files: %q", staged)
	}
	releaseProfileOperationLock(lock)

	result, err := defaultRunProfileStackOperation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ChoiceUpdated || result.Choice.State.StackRepositoryCommit != latestState.StackRepositoryCommit || result.Choice.Secrets != latestSecrets {
		t.Fatalf("Git mutation did not return freshly loaded profile data: %+v", result.Choice)
	}
	staged, err = runGit(context.Background(), repository, nil, "diff", "--cached", "--name-only", "--", "stacks")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "stacks/site/"+stackComposeFilename) {
		t.Fatalf("unlocked Git mutation did not stage the stack: %q", staged)
	}
}

func TestProfileStackGitMutationRejectsChangedRepository(t *testing.T) {
	requireGit(t)
	staleRepository := t.TempDir()
	currentRepository := t.TempDir()
	runGitCommand(t, staleRepository, "init")
	runGitCommand(t, currentRepository, "init")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                   setupTestProfileID,
		IP:                   setupTestHost,
		ConfigRepositoryPath: staleRepository,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleProfile, staleState, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile.ConfigRepositoryPath = currentRepository
	if err := store.Save(profile, staleState); err != nil {
		t.Fatal(err)
	}

	_, err = defaultRunProfileStackOperation(context.Background(), profileStackOperationRequest{
		Kind:           profileStackOperationStage,
		RepositoryPath: staleRepository,
		Choice:         profileChoice{Profile: staleProfile, State: staleState},
		ProfileStore:   store,
	})
	if err == nil || !strings.Contains(err.Error(), "repository changed") {
		t.Fatalf("stale repository mutation was not rejected: %v", err)
	}
}

func TestProfileStackGitMutationSerializesProfilesSharingRepository(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	store := newFileProfileStore(t.TempDir())
	firstProfile, firstState := createProfileStackGitTestProfile(t, store, "profile-one", repository)
	aliasPath := repository + string(filepath.Separator) + "."
	secondProfile, secondState := createProfileStackGitTestProfile(t, store, "profile-two", aliasPath)

	originalStage := runProfileStackStage
	started := make(chan struct{})
	release := make(chan struct{})
	runProfileStackStage = func(_ context.Context, _ string) error {
		close(started)
		<-release
		return nil
	}
	t.Cleanup(func() { runProfileStackStage = originalStage })

	firstDone := make(chan error, 1)
	go func() {
		_, err := defaultRunProfileStackOperation(context.Background(), profileStackOperationRequest{
			Kind: profileStackOperationStage, RepositoryPath: repository,
			Choice: profileChoice{Profile: firstProfile, State: firstState}, ProfileStore: store,
		})
		firstDone <- err
	}()
	waitForProfileStackSignal(t, started, "first shared-repository mutation")
	_, err := defaultRunProfileStackOperation(context.Background(), profileStackOperationRequest{
		Kind: profileStackOperationStage, RepositoryPath: aliasPath,
		Choice: profileChoice{Profile: secondProfile, State: secondState}, ProfileStore: store,
	})
	if !errors.Is(err, errRepositoryOperationLocked) {
		t.Fatalf("second profile mutated the shared repository concurrently: %v", err)
	}
	secondProfileLock, err := store.TryLockProfile(secondProfile.ID)
	if err != nil {
		t.Fatalf("repository-lock rejection retained the second profile lock: %v", err)
	}
	releaseProfileOperationLock(secondProfileLock)
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first shared-repository mutation failed: %v", err)
	}
}

func createProfileStackGitTestProfile(t *testing.T, store ProfileStore, id, repositoryPath string) (Profile, ProfileState) {
	t.Helper()
	profile, err := store.Create(Profile{ID: id, IP: setupTestHost, ConfigRepositoryPath: repositoryPath})
	if err != nil {
		t.Fatal(err)
	}
	profile, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	return profile, state
}

func TestProfileStackDeleteCancellationBeforeMutationPreservesStack(t *testing.T) {
	requireGit(t)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	stackDirectory := filepath.Join(repository, "stacks", "site")
	if err := os.MkdirAll(stackDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stackDirectory, stackMetadataFilename), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := newFileProfileStore(t.TempDir())
	profile, state := createProfileStackGitTestProfile(t, store, setupTestProfileID, repository)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := defaultRunProfileStackOperation(ctx, profileStackOperationRequest{
		Kind: profileStackOperationDelete, RepositoryPath: repository, StackName: "site",
		Choice: profileChoice{Profile: profile, State: state}, ProfileStore: store,
	})
	if !errors.Is(err, context.Canceled) || result.MutationStarted {
		t.Fatalf("pre-mutation cancellation was reported incorrectly: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(stackDirectory); err != nil {
		t.Fatalf("pre-mutation cancellation removed the stack: %v", err)
	}
}

func TestProfileStackDeleteStartsAsyncWorker(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })
	var requests []profileStackOperationRequest
	runProfileStackOperation = func(_ context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
		requests = append(requests, request)
		if request.Kind == profileStackOperationDelete {
			return profileStackOperationResult{MutationStarted: true, DeletedStackName: request.StackName}, nil
		}
		return profileStackOperationResult{Snapshot: profileStackRepositorySnapshot{GitStatus: "clean"}}, nil
	}
	model := newProfileStackOperationTestModel(t)
	model.screen = profileSetupScreenStackDeleteConfirm
	model.stackDeleteInput.SetValue("delete site")
	updated, command := model.updateStackDeleteConfirm(keyCode(tea.KeyEnter))
	busy := updated.(profileSetupModel)
	if command == nil || busy.stackOperation != profileStackOperationDelete || len(requests) != 0 {
		t.Fatalf("stack deletion did not defer work to the worker: operation=%q calls=%d", busy.stackOperation, len(requests))
	}
	finished, _ := settleProfileStackOperation(t, busy, command)
	if len(requests) != 2 || requests[0].Kind != profileStackOperationDelete || requests[0].StackName != "site" || requests[1].Kind != profileStackOperationRefresh {
		t.Fatalf("stack deletion worker sequence = %+v", requests)
	}
	if finished.screen != profileSetupScreenStacks || !strings.Contains(finished.stackNotice, "Stack site removed") {
		t.Fatalf("stack deletion result was not applied: screen=%d notice=%q", finished.screen, finished.stackNotice)
	}
}

func TestProfileStackGitMutationCancellationReportsStartedOutcome(t *testing.T) {
	for _, kind := range []profileStackOperationKind{
		profileStackOperationStage,
		profileStackOperationCommit,
		profileStackOperationPush,
		profileStackOperationDelete,
	} {
		t.Run(string(kind), func(t *testing.T) {
			testProfileStackGitMutationCancellationReportsStartedOutcome(t, kind)
		})
	}
}

func TestProfileStackGitMutationCancellationAlwaysRefreshesWithWarning(t *testing.T) {
	originalRunner := runProfileStackOperation
	t.Cleanup(func() { runProfileStackOperation = originalRunner })
	for _, test := range []struct {
		kind profileStackOperationKind
		want string
	}{
		{kind: profileStackOperationStage, want: "index may have changed"},
		{kind: profileStackOperationCommit, want: "commit may have completed"},
		{kind: profileStackOperationPush, want: "remote outcome is unknown"},
		{kind: profileStackOperationDelete, want: "partially or fully removed"},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			testProfileStackGitMutationCancellationRefresh(t, test.kind, test.want)
		})
	}
}

func testProfileStackGitMutationCancellationReportsStartedOutcome(t *testing.T, kind profileStackOperationKind) {
	t.Helper()
	started := make(chan struct{})
	installCancellableProfileStackMutation(t, kind, started)
	repository := t.TempDir()
	runGitCommand(t, repository, "init")
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: setupTestProfileID, IP: setupTestHost, ConfigRepositoryPath: repository})
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan profileStackOperationResult, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, runErr := defaultRunProfileStackOperation(ctx, profileStackOperationRequest{
			Kind: kind, RepositoryPath: repository, Choice: profileChoice{Profile: profile, State: state},
			ProfileStore: store, CommitMessage: "Update site", StackName: "site",
		})
		resultChannel <- result
		errorChannel <- runErr
	}()
	waitForProfileStackSignal(t, started, "Git mutation start")
	cancel()
	result := <-resultChannel
	runErr := <-errorChannel
	if !errors.Is(runErr, context.Canceled) || !result.MutationStarted || !result.CancellationObserved {
		t.Fatalf("cancelled Git mutation lost its uncertain outcome: result=%+v err=%v", result, runErr)
	}
	lock, err := store.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatalf("cancelled Git mutation did not release the profile lock: %v", err)
	}
	releaseProfileOperationLock(lock)
}

func installCancellableProfileStackMutation(t *testing.T, kind profileStackOperationKind, started chan struct{}) {
	t.Helper()
	originalStage := runProfileStackStage
	originalCommit := runProfileStackCommit
	originalPush := runProfileStackPush
	originalDelete := runProfileStackDelete
	t.Cleanup(func() {
		runProfileStackStage = originalStage
		runProfileStackCommit = originalCommit
		runProfileStackPush = originalPush
		runProfileStackDelete = originalDelete
	})
	waitForCancellation := func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	switch kind {
	case profileStackOperationStage:
		runProfileStackStage = func(ctx context.Context, _ string) error { return waitForCancellation(ctx) }
	case profileStackOperationCommit:
		runProfileStackCommit = func(ctx context.Context, _, _ string) error { return waitForCancellation(ctx) }
	case profileStackOperationPush:
		runProfileStackPush = func(ctx context.Context, _ string) error { return waitForCancellation(ctx) }
	case profileStackOperationDelete:
		runProfileStackDelete = func(ctx context.Context, _, _ string) error { return waitForCancellation(ctx) }
	}
}

func testProfileStackGitMutationCancellationRefresh(t *testing.T, kind profileStackOperationKind, want string) {
	t.Helper()
	var requests []profileStackOperationKind
	runProfileStackOperation = func(_ context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
		requests = append(requests, request.Kind)
		return profileStackOperationResult{Snapshot: profileStackRepositorySnapshot{
			GitStatus: "clean", Head: "abc123", SyncStatus: "sync required",
		}}, nil
	}
	model := newProfileStackOperationTestModel(t)
	model.stackOperation = kind
	model.stackOperationCancelling = true
	updated, refreshCommand := model.Update(profileStackOperationMsg{
		Kind: kind, Result: profileStackOperationResult{MutationStarted: true, CancellationObserved: true}, Err: context.Canceled,
	})
	refreshing := updated.(profileSetupModel)
	if refreshCommand == nil || refreshing.stackOperation != profileStackOperationRefresh {
		t.Fatalf("cancelled %s did not start a repository refresh", kind)
	}
	finished, _ := settleProfileStackOperation(t, refreshing, refreshCommand)
	if len(requests) != 1 || requests[0] != profileStackOperationRefresh {
		t.Fatalf("cancelled %s operation sequence = %v", kind, requests)
	}
	if finished.err != "" || !strings.Contains(strings.ToLower(finished.stackNotice), want) {
		t.Fatalf("cancelled %s warning is unclear: err=%q notice=%q", kind, finished.err, finished.stackNotice)
	}
	if kind == profileStackOperationPush && !strings.Contains(strings.ToLower(finished.stackNotice), "origin") {
		t.Fatalf("push cancellation warning does not direct origin reconciliation: %q", finished.stackNotice)
	}
}

func newProfileStackOperationTestModel(t *testing.T) profileSetupModel {
	t.Helper()
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{
			ID:                   setupTestProfileID,
			IP:                   setupTestHost,
			BaseDomain:           setupTestDomain,
			ConfigRepositoryPath: t.TempDir(),
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.screen = profileSetupScreenStacks
	model.stacks = []editableStack{{Name: "site"}}
	model.stackTable = newStackTable(model.stacks, setupTestDomain, nil)
	return model
}

func settleProfileStackOperation(t *testing.T, model profileSetupModel, command tea.Cmd) (profileSetupModel, tea.Cmd) {
	t.Helper()
	for attempt := 0; model.profileStackOperationBusy() && attempt < 4; attempt++ {
		if command == nil {
			t.Fatalf("operation %q has no command", model.stackOperation)
		}
		message := profileStackWorkerCommand(t, command)()
		updated, nextCommand := model.Update(message)
		model = updated.(profileSetupModel)
		command = nextCommand
	}
	if model.profileStackOperationBusy() {
		t.Fatalf("operation did not settle: %q", model.stackOperation)
	}
	return model, command
}

func profileStackWorkerCommand(t *testing.T, command tea.Cmd) tea.Cmd {
	t.Helper()
	if command == nil {
		t.Fatal("expected stack operation command")
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("stack operation command did not include worker and spinner: %T", message)
	}
	return batch[0]
}

func waitForProfileStackSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForProfileStackMessage(t *testing.T, messages <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stack operation result")
		return nil
	}
}
