package main

import (
	"errors"
	"strings"
	"testing"
)

type profileStoreWithoutOperationLocks struct {
	ProfileStore
}

func TestProfileSettingsMutationRespectsLockAndMergesStaleSnapshot(t *testing.T) {
	root := t.TempDir()
	staleStore := newFileProfileStore(root)
	mutationStore := newFileProfileStore(root)
	profile, err := staleStore.Create(Profile{
		ID:                   setupTestProfileID,
		Name:                 "production",
		IP:                   setupTestHost,
		InitialSSHUser:       "root",
		AdminUser:            "servestead",
		PrivateKeyPath:       setupTestPrivateKey,
		BaseDomain:           "old.example.com",
		LetsEncryptEmail:     setupTestEmail,
		PangolinAdminEmail:   setupTestEmail,
		ConfigRepositoryPath: "/tmp/original-repository",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := staleStore.SaveSecrets(profile.ID, ProfileSecrets{
		PangolinAdminPassword: "old-password",
		GitHubToken:           "old-token",
	}); err != nil {
		t.Fatal(err)
	}
	staleProfile, staleState, err := staleStore.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	staleSecrets, err := staleStore.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}

	latestProfile := staleProfile
	latestProfile.Name = "renamed elsewhere"
	latestProfile.Cloud = &ProfileCloud{Provider: digitalOceanProviderName, ResourceID: "droplet-42"}
	latestState := staleState
	latestState.StackRepositoryCommit = "latest-commit"
	if err := staleStore.Save(latestProfile, latestState); err != nil {
		t.Fatal(err)
	}
	latestSecrets := staleSecrets
	latestSecrets.PangolinAdminPassword = "latest-password"
	latestSecrets.GitHubToken = "latest-token"
	latestSecrets.StackSecretIdentity = "latest-identity"
	if err := staleStore.SaveSecrets(profile.ID, latestSecrets); err != nil {
		t.Fatal(err)
	}

	options := profileSettingsOptions(staleProfile, staleSecrets)
	options.BaseDomain = "edited.example.com"
	patch := newProfileSettingsPatch(staleProfile, staleSecrets, options)
	lock, err := staleStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfileSettings(mutationStore, profile.ID, patch); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("profile settings save ignored the operation lock: %v", err)
	}
	assertProfileMutationLatestValues(t, mutationStore, profile.ID, "old.example.com", latestProfile, latestState, latestSecrets)
	releaseProfileOperationLock(lock)

	choice, err := saveProfileSettings(mutationStore, profile.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	assertProfileMutationLatestValues(t, mutationStore, profile.ID, "edited.example.com", latestProfile, latestState, latestSecrets)
	if choice.Profile.Name != latestProfile.Name || choice.State.StackRepositoryCommit != latestState.StackRepositoryCommit || choice.Secrets.GitHubToken != latestSecrets.GitHubToken {
		t.Fatalf("profile settings save returned stale data: %+v", choice)
	}
}

func TestProfileGitHubTokenMutationRespectsLockAndPreservesOtherSecrets(t *testing.T) {
	root := t.TempDir()
	lockedStore := newFileProfileStore(root)
	mutationStore := newFileProfileStore(root)
	profile, err := lockedStore.Create(Profile{ID: setupTestProfileID, IP: setupTestHost})
	if err != nil {
		t.Fatal(err)
	}
	latestSecrets := ProfileSecrets{
		ServerSecret:         "current-server-secret",
		GitHubToken:          "current-token",
		StackSecretIdentity:  "current-identity",
		StackSecretRecipient: "current-recipient",
	}
	if err := lockedStore.SaveSecrets(profile.ID, latestSecrets); err != nil {
		t.Fatal(err)
	}
	lock, err := lockedStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveProfileGitHubToken(mutationStore, profile.ID, "replacement-token"); !errors.Is(err, errProfileOperationLocked) {
		t.Fatalf("GitHub token save ignored the operation lock: %v", err)
	}
	stored, err := mutationStore.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != latestSecrets {
		t.Fatalf("locked GitHub token save changed secrets: %+v", stored)
	}
	releaseProfileOperationLock(lock)

	choice, err := saveProfileGitHubToken(mutationStore, profile.ID, "replacement-token")
	if err != nil {
		t.Fatal(err)
	}
	stored, err = mutationStore.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.GitHubToken != "replacement-token" || stored.ServerSecret != latestSecrets.ServerSecret ||
		stored.StackSecretIdentity != latestSecrets.StackSecretIdentity || stored.StackSecretRecipient != latestSecrets.StackSecretRecipient {
		t.Fatalf("GitHub token save clobbered unrelated secrets: %+v", stored)
	}
	if choice.Secrets != stored {
		t.Fatalf("GitHub token save returned stale secrets: %+v", choice.Secrets)
	}
}

func TestProfileSecretMutationRejectsActiveRunWithoutMutatingSecrets(t *testing.T) {
	base := newFileProfileStore(t.TempDir())
	profile, err := base.Create(Profile{ID: setupTestProfileID, IP: setupTestHost})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{
		ActiveRunID: "run-active",
		Runs: map[string]SetupRun{"run-active": {
			ID: "run-active", Status: runStatusRunning, Stages: map[string]SetupStageStatus{},
		}},
	}
	if err := base.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	before := ProfileSecrets{ServerSecret: "server-secret", GitHubToken: "github-token"}
	if err := base.SaveSecrets(profile.ID, before); err != nil {
		t.Fatal(err)
	}
	_, err = mutateProfileSecretsWhenIdle(
		profileStoreWithoutOperationLocks{ProfileStore: base},
		profile.ID,
		"cannot mutate secrets during an active run",
		func(secrets *ProfileSecrets) error {
			secrets.StackSecretIdentity = "replacement"
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "active run") {
		t.Fatalf("secret mutation did not reject an active run: %v", err)
	}
	after, err := base.LoadSecrets(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("active-run rejection changed secrets: before=%+v after=%+v", before, after)
	}
}

func profileSettingsOptions(profile Profile, secrets ProfileSecrets) setupCLIOptions {
	return setupCLIOptions{
		IP:                    profile.IP,
		Name:                  profile.Name,
		InitialSSHUser:        profile.InitialSSHUser,
		AdminUser:             profile.AdminUser,
		PrivateKeyPath:        profile.PrivateKeyPath,
		BaseDomain:            profile.BaseDomain,
		LetsEncryptEmail:      profile.LetsEncryptEmail,
		PangolinAdminEmail:    profile.PangolinAdminEmail,
		PangolinAdminPassword: secrets.PangolinAdminPassword,
		ConfigRepositoryPath:  profile.ConfigRepositoryPath,
	}
}

func assertProfileMutationLatestValues(
	t *testing.T,
	store ProfileStore,
	profileID string,
	wantDomain string,
	latestProfile Profile,
	latestState ProfileState,
	latestSecrets ProfileSecrets,
) {
	t.Helper()
	profile, state, err := store.Load(profileID)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.BaseDomain != wantDomain || profile.Name != latestProfile.Name || profile.Cloud == nil || profile.Cloud.ResourceID != latestProfile.Cloud.ResourceID {
		t.Fatalf("profile settings save overwrote fresh profile fields: %+v", profile)
	}
	if state.StackRepositoryCommit != latestState.StackRepositoryCommit {
		t.Fatalf("profile settings save overwrote fresh state: %+v", state)
	}
	if secrets.PangolinAdminPassword != latestSecrets.PangolinAdminPassword || secrets.GitHubToken != latestSecrets.GitHubToken || secrets.StackSecretIdentity != latestSecrets.StackSecretIdentity {
		t.Fatalf("profile settings save overwrote fresh secrets: %+v", secrets)
	}
}
