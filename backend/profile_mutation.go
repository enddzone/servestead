package main

import (
	"errors"
	"strings"
)

type profileSettingsPatch struct {
	Name                 *string
	IP                   *string
	InitialSSHUser       *string
	AdminUser            *string
	PrivateKeyPath       *string
	BaseDomain           *string
	LetsEncryptEmail     *string
	PangolinAdminEmail   *string
	ConfigRepositoryPath *string
	PangolinPassword     *string
}

func newProfileSettingsPatch(base Profile, baseSecrets ProfileSecrets, options setupCLIOptions) profileSettingsPatch {
	desired := base
	if desired.IP == "" {
		desired.IP = strings.TrimSpace(options.IP)
	}
	desired.Name = firstNonEmpty(options.Name, desired.IP)
	desired.InitialSSHUser = firstNonEmpty(options.InitialSSHUser, "root")
	desired.AdminUser = firstNonEmpty(options.AdminUser, "servestead")
	desired.PrivateKeyPath = expandUserPath(options.PrivateKeyPath)
	desired.BaseDomain = strings.TrimSpace(options.BaseDomain)
	desired.LetsEncryptEmail = strings.TrimSpace(options.LetsEncryptEmail)
	desired.PangolinAdminEmail = firstNonEmpty(strings.TrimSpace(options.PangolinAdminEmail), desired.LetsEncryptEmail)
	desired.ConfigRepositoryPath = expandUserPath(strings.TrimSpace(options.ConfigRepositoryPath))

	patch := profileSettingsPatch{}
	setChangedString(&patch.Name, base.Name, desired.Name)
	setChangedString(&patch.IP, base.IP, desired.IP)
	setChangedString(&patch.InitialSSHUser, base.InitialSSHUser, desired.InitialSSHUser)
	setChangedString(&patch.AdminUser, base.AdminUser, desired.AdminUser)
	setChangedString(&patch.PrivateKeyPath, base.PrivateKeyPath, desired.PrivateKeyPath)
	setChangedString(&patch.BaseDomain, base.BaseDomain, desired.BaseDomain)
	setChangedString(&patch.LetsEncryptEmail, base.LetsEncryptEmail, desired.LetsEncryptEmail)
	setChangedString(&patch.PangolinAdminEmail, base.PangolinAdminEmail, desired.PangolinAdminEmail)
	setChangedString(&patch.ConfigRepositoryPath, base.ConfigRepositoryPath, desired.ConfigRepositoryPath)
	if password := strings.TrimSpace(options.PangolinAdminPassword); password != "" && password != baseSecrets.PangolinAdminPassword {
		patch.PangolinPassword = &password
	}
	return patch
}

func setChangedString(target **string, current, desired string) {
	if current != desired {
		*target = &desired
	}
}

func saveProfileSettings(store ProfileStore, profileID string, patch profileSettingsPatch) (profileChoice, error) {
	if store == nil {
		return profileChoice{}, errors.New(setupProfileStoreUnavailable)
	}
	profile, state, lock, err := lockAndLoadProfile(store, profileID)
	if err != nil {
		return profileChoice{}, err
	}
	defer releaseProfileOperationLock(lock)
	if profileStateHasActiveRun(state) {
		return profileChoice{}, errors.New("cannot save profile settings while its setup run is active")
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return profileChoice{}, err
	}

	profileChanged := applyProfileSettingsPatch(&profile, patch)
	if profileChanged {
		if err := store.Save(profile, state); err != nil {
			return profileChoice{}, err
		}
	}
	if patch.PangolinPassword != nil {
		secrets.PangolinAdminPassword = *patch.PangolinPassword
		if err := store.SaveSecrets(profileID, secrets); err != nil {
			return profileChoice{}, err
		}
	}
	return profileChoice{Profile: profile, State: state, Secrets: secrets}, nil
}

func applyProfileSettingsPatch(profile *Profile, patch profileSettingsPatch) bool {
	changed := false
	apply := func(target *string, value *string) {
		if value != nil && *target != *value {
			*target = *value
			changed = true
		}
	}
	apply(&profile.Name, patch.Name)
	apply(&profile.IP, patch.IP)
	apply(&profile.InitialSSHUser, patch.InitialSSHUser)
	apply(&profile.AdminUser, patch.AdminUser)
	apply(&profile.PrivateKeyPath, patch.PrivateKeyPath)
	apply(&profile.BaseDomain, patch.BaseDomain)
	apply(&profile.LetsEncryptEmail, patch.LetsEncryptEmail)
	apply(&profile.PangolinAdminEmail, patch.PangolinAdminEmail)
	apply(&profile.ConfigRepositoryPath, patch.ConfigRepositoryPath)
	return changed
}

func saveProfileGitHubToken(store ProfileStore, profileID, token string) (profileChoice, error) {
	return mutateProfileSecretsWhenIdle(
		store,
		profileID,
		"cannot change the GitHub token while this profile's setup run is active",
		func(secrets *ProfileSecrets) error {
			secrets.GitHubToken = token
			return nil
		},
	)
}

func mutateProfileSecretsWhenIdle(
	store ProfileStore,
	profileID string,
	activeRunMessage string,
	mutate func(*ProfileSecrets) error,
) (profileChoice, error) {
	if store == nil {
		return profileChoice{}, errors.New(setupProfileStoreUnavailable)
	}
	profile, state, lock, err := lockAndLoadProfile(store, profileID)
	if err != nil {
		return profileChoice{}, err
	}
	defer releaseProfileOperationLock(lock)
	if profileStateHasActiveRun(state) {
		return profileChoice{}, errors.New(activeRunMessage)
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return profileChoice{}, err
	}
	if err := mutate(&secrets); err != nil {
		return profileChoice{}, err
	}
	if err := store.SaveSecrets(profileID, secrets); err != nil {
		return profileChoice{}, err
	}
	return profileChoice{Profile: profile, State: state, Secrets: secrets}, nil
}
