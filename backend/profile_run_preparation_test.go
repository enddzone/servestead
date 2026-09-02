package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestProfileRunPreparationRendersAndAcknowledgesCancellation(t *testing.T) {
	store, profile, state, config := newProfileRunPreparationFixture(t)
	started := make(chan struct{})
	previous := runProfilePreparation
	runProfilePreparation = func(ctx context.Context, request profileRunPreparationRequest) profileRunPreparedMsg {
		close(started)
		<-ctx.Done()
		return profileRunPreparedMsg{run: request.run, stage: request.stage, err: ctx.Err()}
	}
	t.Cleanup(func() { runProfilePreparation = previous })

	runContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	messages := make(chan tea.Msg, 4)
	model := newPreparingProfileRunModel(runContext, profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
	}, "stacks", messages, cancel, true)

	view := model.View().Content
	for _, expected := range []string{"Preparing Sync stacks", "local preflight and configuration repository", "Ctrl+C requests cancellation before SSH starts"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("preparation view missing %q:\n%s", expected, view)
		}
	}

	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("preparation Init command = %T, want two-command batch", model.Init()())
	}
	prepared := make(chan tea.Msg, 1)
	go func() { prepared <- batch[1]() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("preparation worker did not start")
	}

	updated, command := model.Update(keyCtrl('c'))
	model = updated.(profileRunModel)
	if command != nil || !model.cancelled || !model.preparing {
		t.Fatalf("cancellation request did not remain in preparation: %+v", model)
	}
	if view := model.View().Content; !strings.Contains(view, "Cancelling setup") {
		t.Fatalf("preparation cancellation was not visible:\n%s", view)
	}

	select {
	case message := <-prepared:
		updated, command = model.Update(message)
	case <-time.After(2 * time.Second):
		t.Fatal("preparation worker did not acknowledge cancellation")
	}
	model = updated.(profileRunModel)
	if command != nil || !model.done || model.preparing || !model.cancelled || model.err != nil || model.profileReporter != nil {
		t.Fatalf("cancelled preparation result = %+v", model)
	}
	_, saved, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveRunID != "" || len(saved.Runs) != 0 {
		t.Fatalf("cancelled preparation leaked a planned run: %+v", saved)
	}
}

func TestProfileRunPreparationFailureStaysInRunViewAndMasksSecrets(t *testing.T) {
	store, profile, state, config := newProfileRunPreparationFixture(t)
	runContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newPreparingProfileRunModel(runContext, profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
	}, "stacks", make(chan tea.Msg, 4), cancel, true)
	secret := "github_pat_preparation_secret"
	prepared := profileRunPreparedMsg{
		run:          profileSetupPlanRun{store: store, profile: profile, state: state, config: config},
		stage:        "stacks",
		output:       "clone output contained " + secret,
		secretValues: []string{secret},
		err:          fmt.Errorf("prepare repository with %s: failed", secret),
	}

	updated, command := model.Update(prepared)
	model = updated.(profileRunModel)
	if command != nil || !model.done || model.preparing || model.cancelled || model.err == nil || model.profileReporter != nil {
		t.Fatalf("failed preparation result = %+v", model)
	}
	view := model.View().Content
	if strings.Contains(view, secret) || !strings.Contains(view, "***") || !strings.Contains(view, "Failed") {
		t.Fatalf("preparation failure was not safely rendered:\n%s", view)
	}
	updated, command = model.Update(keyCode(tea.KeyEsc))
	if command == nil || !updated.(profileRunModel).returnToSetup {
		t.Fatal("failed preparation did not allow returning to setup")
	}
	_, saved, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveRunID != "" || len(saved.Runs) != 0 {
		t.Fatalf("failed preparation leaked a planned run: %+v", saved)
	}
}

func TestProfileRunPreparedStartsOnceAndRebuildsStackStages(t *testing.T) {
	store, profile, state, config := newProfileRunPreparationFixture(t)
	runContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newPreparingProfileRunModel(runContext, profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
	}, "", make(chan tea.Msg, 4), cancel, true)
	preparedConfig := config
	preparedConfig.ConfigRepositoryCommit = "0123456789abcdef"
	preparedConfig.Stacks = []configuredStack{{Name: "site"}}
	prepared := profileRunPreparedMsg{
		run: profileSetupPlanRun{
			store: store, profile: profile, state: state, config: preparedConfig,
		},
		secretValues: []string{"prepared-secret"},
	}

	updated, command := model.Update(prepared)
	model = updated.(profileRunModel)
	batchMessage := command()
	batch, ok := batchMessage.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("prepared run command = %T, want start and event wait", batchMessage)
	}
	if model.preparing || model.done || model.profileReporter == nil || model.runID == "" || model.runID == "preparation" {
		t.Fatalf("prepared model did not transition to a run: %+v", model)
	}
	if model.config.ConfigRepositoryCommit != preparedConfig.ConfigRepositoryCommit || !containsProfileRunStage(model.stages, setupStageStackPrefix+"site") {
		t.Fatalf("prepared stack configuration was not applied: config=%+v stages=%+v", model.config, model.stages)
	}
	if !containsString(model.secretValues, "prepared-secret") {
		t.Fatalf("prepared secret masks were not installed: %#v", model.secretValues)
	}

	duplicate, duplicateCommand := model.Update(prepared)
	if duplicateCommand != nil || duplicate.(profileRunModel).runID != model.runID {
		t.Fatal("duplicate preparation result launched a second run")
	}
	_, saved, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveRunID != "" || len(saved.Runs) != 0 {
		t.Fatalf("planned run was saved before the background start command: %+v", saved)
	}
}

func TestProfileRunCancellationBeforePreparedResultDoesNotStartRun(t *testing.T) {
	store, profile, state, config := newProfileRunPreparationFixture(t)
	runContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	model := newPreparingProfileRunModel(runContext, profileSetupPlanRun{
		store: store, profile: profile, state: state, config: config,
	}, "stacks", make(chan tea.Msg, 4), cancel, true)
	updated, _ := model.Update(keyCtrl('c'))
	model = updated.(profileRunModel)

	updated, command := model.Update(profileRunPreparedMsg{
		run:                          profileSetupPlanRun{store: store, profile: profile, state: state, config: config},
		stage:                        "stacks",
		repositoryPreparationStarted: true,
		repositoryPrepared:           true,
	})
	model = updated.(profileRunModel)
	if command != nil || !model.done || !model.cancelled || model.profileReporter != nil {
		t.Fatalf("prepared result started work after cancellation: %+v", model)
	}
	if view := model.View().Content; !strings.Contains(view, "Local repository preparation completed") ||
		!strings.Contains(view, "No remote SSH commands started") {
		t.Fatalf("completed local preparation was hidden after cancellation:\n%s", view)
	}
	_, saved, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ActiveRunID != "" || len(saved.Runs) != 0 {
		t.Fatalf("cancel-before-prepared leaked a planned run: %+v", saved)
	}
}

func TestProfileRunPreparationForNonRepositoryStageRunsOnlyPreflight(t *testing.T) {
	privateKey := t.TempDir() + "/id_ed25519"
	if err := os.WriteFile(privateKey, []byte("test key"), 0600); err != nil {
		t.Fatal(err)
	}
	config := setupConfig{
		Mode:           setupModeHardenOnly,
		Host:           setupTestHost,
		AdminUser:      "servestead",
		PrivateKeyPath: privateKey,
	}
	result := defaultRunProfilePreparation(context.Background(), profileRunPreparationRequest{
		run: profileSetupPlanRun{
			profile: Profile{ID: setupTestProfileID, IP: setupTestHost},
			state:   ProfileState{Runs: map[string]SetupRun{}},
			config:  config,
		},
		stage: "harden",
	})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(result.output, "Preflight checks:") || strings.Contains(result.output, "Preparing configuration repository") {
		t.Fatalf("non-repository stage preparation output is wrong:\n%s", result.output)
	}
}

func TestProfileRunInitializationHonorsCancellationBeforeSaving(t *testing.T) {
	store, profile, state, _ := newProfileRunPreparationFixture(t)
	runID := "run-not-saved"
	state.ActiveRunID = runID
	state.Runs[runID] = newSetupRunForStage(runID, "harden", nil)
	reporter := &profileRunReporter{store: store, profile: profile, state: &state, runID: runID}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (profileRunCommand{profileReporter: reporter, initialize: true}).initializeRun(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("initializeRun error = %v, want context cancellation", err)
	}
	_, saved, loadErr := store.Load(profile.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if saved.ActiveRunID != "" || len(saved.Runs) != 0 {
		t.Fatalf("cancelled initialization saved the planned run: %+v", saved)
	}
}

func TestFullProfilePreparationAcquiresLockBeforeMutations(t *testing.T) {
	assertProfilePreparationAcquiresLockBeforeMutations(t, func(store ProfileStore, options setupCLIOptions) error {
		_, _, _, _, err := prepareProfileSetupWithOperationLock(options, store, nil)
		return err
	})
}

func TestStageProfilePreparationAcquiresLockBeforeMutations(t *testing.T) {
	assertProfilePreparationAcquiresLockBeforeMutations(t, func(store ProfileStore, options setupCLIOptions) error {
		_, _, _, _, err := prepareProfileStageSetupWithOperationLock(options, store, "harden")
		return err
	})
}

func assertProfilePreparationAcquiresLockBeforeMutations(
	t *testing.T,
	prepare func(ProfileStore, setupCLIOptions) error,
) {
	t.Helper()
	root := t.TempDir()
	lockStore := newFileProfileStore(root)
	mutationStore := newFileProfileStore(root)
	privateKey := writeSetupPlanKeypair(t)
	profile, err := lockStore.Create(Profile{
		ID:                 setupTestProfileID,
		Name:               "production",
		IP:                 setupTestHost,
		InitialSSHUser:     "root",
		AdminUser:          "servestead",
		PrivateKeyPath:     privateKey,
		BaseDomain:         setupTestDomain,
		LetsEncryptEmail:   setupTestEmail,
		PangolinAdminEmail: setupTestEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseProfileOperationLock(lock) })

	err = prepare(mutationStore, setupCLIOptions{ProfileID: profile.ID, Name: "mutated-before-lock"})
	if !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("preparation did not stop at the held lock: %v", err)
	}
	assertProfilePreparationStateUnchanged(t, lockStore, profile.ID)
}

func assertProfilePreparationStateUnchanged(t *testing.T, store ProfileStore, profileID string) {
	t.Helper()
	savedProfile, savedState, err := store.Load(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if savedProfile.Name != "production" || savedState.ActiveRunID != "" || len(savedState.Runs) != 0 {
		t.Fatalf("locked preparation mutated profile state: profile=%+v state=%+v", savedProfile, savedState)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range profileSecretValues(secrets) {
		if secret != "" {
			t.Fatalf("locked preparation generated secrets: %+v", secrets)
		}
	}
}

func TestPreparedOperationLockIsHandedThroughWithoutRelocking(t *testing.T) {
	root := t.TempDir()
	store := newFileProfileStore(root)
	otherStore := newFileProfileStore(root)
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
	profile, state, config, lock, err := prepareProfileStageSetupWithOperationLock(
		setupCLIOptions{ProfileID: profile.ID},
		store,
		"harden",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProfileOperationLock(lock)
	if competing, err := otherStore.TryLockProfile(profile.ID); !errors.Is(err, errProfileOperationLocked) {
		releaseProfileOperationLock(competing)
		t.Fatalf("preparation did not retain its lock: %v", err)
	}

	run := profileSetupPlanRun{store: store, profile: profile, state: state, config: config, lock: lock}
	lockedRun, handedLock, err := lockProfileSetupPlanRun(run)
	if err != nil {
		t.Fatalf("held lock was acquired twice: %v", err)
	}
	if handedLock != lock || lockedRun.lock != lock {
		t.Fatal("prepared lock was not handed to the run")
	}
}

func newProfileRunPreparationFixture(t *testing.T) (*fileProfileStore, Profile, ProfileState, setupConfig) {
	t.Helper()
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		IP:             setupTestHost,
		Name:           "production",
		InitialSSHUser: "root",
		AdminUser:      "servestead",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	config := setupConfig{
		Mode:           setupModeObservability,
		Host:           setupTestHost,
		InitialSSHUser: "root",
		AdminUser:      "servestead",
		PrivateKeyPath: setupTestPrivateKey,
		BaseDomain:     setupTestDomain,
		ProfileID:      profile.ID,
	}
	return store, profile, state, config
}

func containsProfileRunStage(stages []profileRunStageView, key string) bool {
	for _, stage := range stages {
		if stage.Key == key {
			return true
		}
	}
	return false
}
