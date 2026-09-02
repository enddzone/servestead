package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileProfileLockContendsAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	firstStore := newFileProfileStore(root)
	secondStore := newFileProfileStore(root)
	profile, err := firstStore.Create(Profile{ID: setupTestProfileID, IP: setupTestHost})
	if err != nil {
		t.Fatal(err)
	}

	firstLock, err := firstStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.TryLockProfile(profile.ID); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("second store acquired a locked profile: %v", err)
	}
	if err := firstLock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := firstLock.Release(); err != nil {
		t.Fatalf("second release should be harmless: %v", err)
	}

	secondLock, err := secondStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatalf("released profile remained locked: %v", err)
	}
	if err := secondLock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarativePreparationRespectsCanonicalRepositoryLock(t *testing.T) {
	repository := t.TempDir()
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID: setupTestProfileID, IP: setupTestHost, BaseDomain: setupTestDomain,
		LetsEncryptEmail: setupTestEmail, ConfigRepositoryPath: repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.TryLockRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProfileOperationLock(lock)
	_, _, err = prepareDeclarativeSetup(
		context.Background(),
		newFileProfileStore(store.root),
		profile,
		ProfileState{Runs: map[string]SetupRun{}},
		setupConfig{ProfileID: profile.ID, ConfigRepositoryPath: repository + string(os.PathSeparator) + "."},
	)
	if !errors.Is(err, errRepositoryOperationLocked) {
		t.Fatalf("repository preparation ignored the canonical checkout lock: %v", err)
	}
}

func TestLockAndLoadProfileRecoversRunLeftRunningByDeadProcess(t *testing.T) {
	root := t.TempDir()
	store := newFileProfileStore(root)
	profile, err := store.Create(Profile{ID: setupTestProfileID, IP: setupTestHost})
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-interrupted"
	started := time.Now().UTC().Add(-time.Minute)
	state := ProfileState{
		ActiveRunID: runID,
		Runs: map[string]SetupRun{runID: {
			ID:        runID,
			Status:    runStatusRunning,
			CreatedAt: started,
			UpdatedAt: started,
			Stages: map[string]SetupStageStatus{
				"bootstrap": {Status: stageStatusComplete, LastEnded: started},
				"harden":    {Status: stageStatusRunning, LastStarted: started},
			},
		}},
	}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}

	_, recovered, lock, err := lockAndLoadProfile(newFileProfileStore(root), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	releaseProfileOperationLock(lock)
	if recovered.ActiveRunID != "" {
		t.Fatalf("interrupted run remained active: %+v", recovered)
	}
	run := recovered.Runs[runID]
	if run.Status != runStatusCancelled || run.Stages["harden"].Status != stageStatusCancelled || run.Stages["harden"].LastError == "" {
		t.Fatalf("interrupted run was not marked cancelled: %+v", run)
	}
	if run.Stages["bootstrap"].Status != stageStatusComplete {
		t.Fatalf("recovery changed a completed stage: %+v", run.Stages["bootstrap"])
	}
	_, persisted, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveRunID != "" || persisted.Runs[runID].Status != runStatusCancelled {
		t.Fatalf("interrupted run recovery was not persisted: %+v", persisted)
	}
	events, err := store.LoadRunEvents(profile.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != TaskCancelled || !strings.Contains(events[0].Line, "interrupted") {
		t.Fatalf("interrupted run recovery event missing: %+v", events)
	}
}

func TestDestructiveProfileChangesRespectRunLock(t *testing.T) {
	root := t.TempDir()
	runStore := newFileProfileStore(root)
	mutationStore := newFileProfileStore(root)
	profile, err := runStore.Create(Profile{
		ID:                   setupTestProfileID,
		Name:                 "production",
		IP:                   setupTestHost,
		ConfigRepositoryPath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := runStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProfileOperationLock(lock)

	if err := deleteProfileWhenIdle(mutationStore, profile.ID); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("profile deletion ignored the run lock: %v", err)
	}
	model := newProfileSetupModel([]profileChoice{{
		Profile: profile,
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.profileStore = mutationStore
	model.selectedIndex = 0
	model.screen = profileSetupScreenStackDeleteConfirm
	model.stacks = []editableStack{{Name: "site"}}
	model.stackTable = newStackTable(model.stacks, setupTestDomain, nil)
	updated, command := model.deleteSelectedStack()
	result := updated.(profileSetupModel)
	if command == nil || result.stackOperation != profileStackOperationDelete {
		t.Fatalf("stack deletion did not start its lock-backed worker: command=%v operation=%q", command, result.stackOperation)
	}
	result, _ = settleProfileStackOperation(t, result, command)
	if !strings.Contains(result.err, errProfileOperationLocked.Error()) {
		t.Fatalf("stack deletion ignored the run lock: command=%v err=%q", command, result.err)
	}
	if _, _, err := mutationStore.Load(profile.ID); err != nil {
		t.Fatalf("locked mutation removed the profile: %v", err)
	}
}

type profileLockBlockingRemoteClient struct {
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (client *profileLockBlockingRemoteClient) Run(ctx context.Context, _ string) error {
	client.once.Do(func() { close(client.started) })
	select {
	case <-client.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (client *profileLockBlockingRemoteClient) Close() error {
	return nil
}

func TestProfileRunLockRejectsConcurrentRunAndReleasesAfterCompletion(t *testing.T) {
	originalHardening := newHardeningRemoteClient
	defer func() { newHardeningRemoteClient = originalHardening }()

	root := t.TempDir()
	firstStore := newFileProfileStore(root)
	secondStore := newFileProfileStore(root)
	run, config := prepareProfileLockHardenRun(t, firstStore)
	release := make(chan struct{})
	client := &profileLockBlockingRemoteClient{started: make(chan struct{}), release: release}
	newHardeningRemoteClient = func(context.Context, hardeningConfig, io.Writer, io.Writer) (remoteClient, error) {
		return client, nil
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- runProfileSetupStagePlan(context.Background(), run, "harden")
	}()
	waitForProfileLockSignal(t, client.started, "first profile run")

	profile, state, err := secondStore.Load(run.profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRun := profileSetupPlanRun{
		store: secondStore, profile: profile, state: state, config: config,
		stdout: io.Discard, stderr: io.Discard,
	}
	if err := runProfileSetupStagePlan(context.Background(), secondRun, "harden"); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("concurrent profile run was not rejected: %v", err)
	}

	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first profile run failed: %v", err)
	}
	profile, state, err = secondStore.Load(run.profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondRun.profile = profile
	secondRun.state = state
	if err := runProfileSetupStagePlan(context.Background(), secondRun, "harden"); err != nil {
		t.Fatalf("completed profile run did not release its lock: %v", err)
	}
}

func TestProfileRunLockReleasesAfterError(t *testing.T) {
	originalHardening := newHardeningRemoteClient
	defer func() { newHardeningRemoteClient = originalHardening }()

	root := t.TempDir()
	store := newFileProfileStore(root)
	otherStore := newFileProfileStore(root)
	run, _ := prepareProfileLockHardenRun(t, store)
	newHardeningRemoteClient = func(context.Context, hardeningConfig, io.Writer, io.Writer) (remoteClient, error) {
		return &recordingRemoteClient{err: errors.New("remote failure")}, nil
	}
	if err := runProfileSetupStagePlan(context.Background(), run, "harden"); err == nil {
		t.Fatal("profile run unexpectedly succeeded")
	}
	lock, err := otherStore.TryLockProfile(run.profile.ID)
	if err != nil {
		t.Fatalf("failed profile run did not release its lock: %v", err)
	}
	releaseProfileOperationLock(lock)
}

func prepareProfileLockHardenRun(t *testing.T, store ProfileStore) (profileSetupPlanRun, setupConfig) {
	t.Helper()
	privateKey := writeSetupPlanKeypair(t)
	profile, err := store.Create(Profile{
		ID:             setupTestProfileID,
		Name:           "production",
		IP:             setupTestHost,
		AdminUser:      "servestead",
		PrivateKeyPath: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, state, config, err := prepareProfileStageSetup(setupCLIOptions{ProfileID: profile.ID}, store, "harden")
	if err != nil {
		t.Fatal(err)
	}
	return profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
		stdout: io.Discard, stderr: io.Discard,
	}, config
}

func waitForProfileLockSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
