package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const profileCloudNoActiveDropletMessage = "this profile does not have an active DigitalOcean Droplet"

type profileCloudActionMsg struct {
	action               string
	profile              Profile
	state                ProfileState
	remoteOutcomeUnknown bool
	err                  error
}

type profileCloudSaveMsg struct {
	profile Profile
	state   ProfileState
	err     error
}

func (model profileSetupModel) selectedProfileHasCloud() bool {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return false
	}
	return model.profiles[model.selectedIndex].Profile.Cloud != nil
}

func (model profileSetupModel) selectedProfileActiveCloud() (*Profile, *ProfileCloud, bool) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return nil, nil, false
	}
	profile := model.profiles[model.selectedIndex].Profile
	if profile.Cloud == nil || profile.Cloud.Provider != digitalOceanProviderName || profile.Cloud.ResourceID == "" || profile.Cloud.DestroyedAt != nil {
		return &profile, profile.Cloud, false
	}
	return &profile, profile.Cloud, true
}

func (model profileSetupModel) updateProfileCloud(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) &&
		model.cloudOutcomeUnknownID == model.profiles[model.selectedIndex].Profile.ID {
		model.err = "the last DigitalOcean mutation has an unknown remote outcome; check DigitalOcean and restart Servestead before trying another action"
		return model, nil
	}
	switch key.String() {
	case "r", "R":
		_, _, active := model.selectedProfileActiveCloud()
		if !active {
			model.err = profileCloudNoActiveDropletMessage
			return model, nil
		}
		return model.openProfileCloudConfirm("restart"), nil
	case "d", "D":
		_, _, active := model.selectedProfileActiveCloud()
		if !active {
			model.err = profileCloudNoActiveDropletMessage
			return model, nil
		}
		return model.openProfileCloudConfirm("destroy"), nil
	}
	return model, nil
}

func (model profileSetupModel) openProfileCloudConfirm(action string) profileSetupModel {
	if model.cloudCancel != nil {
		model.cloudCancel()
		model.cloudCancel = nil
	}
	model.cloudAction = action
	model.cloudNotice = ""
	model.cloudCancelling = false
	model.cloudTokenInput.SetValue(firstNonEmpty(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN"), os.Getenv("DIGITALOCEAN_TOKEN"), model.cloudTokenInput.Value()))
	model.cloudConfirmInput.SetValue("")
	model.cloudTokenInput.Blur()
	model.cloudConfirmInput.Blur()
	if strings.TrimSpace(model.cloudTokenInput.Value()) == "" {
		model.focus = 0
		model.cloudTokenInput.Focus()
	} else {
		model.focus = 1
		model.cloudConfirmInput.Focus()
	}
	model.screen = profileSetupScreenCloudConfirm
	return model
}

func (model profileSetupModel) updateProfileCloudConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down":
		model.blurFocusedCloudInput()
		model.focus = (model.focus + 1) % 2
		model.focusCloudInput()
		return model, nil
	case "shift+tab", "up":
		model.blurFocusedCloudInput()
		model.focus--
		if model.focus < 0 {
			model.focus = 1
		}
		model.focusCloudInput()
		return model, nil
	case "enter":
		token := strings.TrimSpace(model.cloudTokenInput.Value())
		if token == "" {
			model.err = "DigitalOcean API token is required"
			return model, nil
		}
		_, cloud, active := model.selectedProfileActiveCloud()
		if !active {
			model.err = profileCloudNoActiveDropletMessage
			return model, nil
		}
		expected := profileCloudConfirmPhrase(model.cloudAction, cloud)
		if strings.TrimSpace(model.cloudConfirmInput.Value()) != expected {
			model.err = fmt.Sprintf("type %q to continue", expected)
			return model, nil
		}
		model.cloudTokenInput.Blur()
		model.cloudConfirmInput.Blur()
		model.err = ""
		model.screen = profileSetupScreenCloudRunning
		model.cloudCancelling = false
		parentCtx := model.tuiContext
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		operationCtx, cancel := context.WithCancel(parentCtx)
		model.cloudCancel = cancel
		return model, model.runProfileCloudAction(operationCtx, token)
	}
	var cmd tea.Cmd
	if model.focus == 0 {
		model.cloudTokenInput, cmd = updateSetupTextInput(model.cloudTokenInput, key)
	} else {
		model.cloudConfirmInput, cmd = updateSetupTextInput(model.cloudConfirmInput, key)
	}
	return model, cmd
}

func (model *profileSetupModel) blurFocusedCloudInput() {
	if model.focus == 0 {
		model.cloudTokenInput.Blur()
		return
	}
	model.cloudConfirmInput.Blur()
}

func (model *profileSetupModel) focusCloudInput() {
	if model.focus == 0 {
		model.cloudTokenInput.Focus()
		return
	}
	model.cloudConfirmInput.Focus()
}

func (model profileSetupModel) runProfileCloudAction(ctx context.Context, token string) tea.Cmd {
	action := model.cloudAction
	profile := model.profiles[model.selectedIndex].Profile
	if profile.Cloud != nil {
		cloud := *profile.Cloud
		profile.Cloud = &cloud
	}
	state := model.profiles[model.selectedIndex].State
	store := model.profileStore
	return func() tea.Msg {
		if profile.Cloud == nil {
			return profileCloudActionMsg{action: action, state: state, err: fmt.Errorf("profile %s has no cloud metadata", profile.ID)}
		}
		lockedProfile, lockedState, lock, err := lockAndReloadProfileCloudAction(store, profile, state)
		if err != nil {
			return profileCloudActionMsg{action: action, profile: profile, state: state, err: err}
		}
		defer releaseProfileOperationLock(lock)
		profile = lockedProfile
		state = lockedState
		if profileStateHasActiveRun(state) {
			return profileCloudActionMsg{action: action, profile: profile, state: state, err: errors.New("cannot change a DigitalOcean Droplet while its setup run is active")}
		}
		if !profileHasActiveCloudResource(profile) {
			return profileCloudActionMsg{action: action, profile: profile, state: state, err: errors.New(profileCloudNoActiveDropletMessage)}
		}
		provider := newProvisionCloudProvider(token)
		profile, err = performProfileCloudAction(ctx, action, provider, store, profile, state)
		return profileCloudActionMsg{
			action:               action,
			profile:              profile,
			state:                state,
			remoteOutcomeUnknown: err != nil && (action == "restart" || action == "destroy") && cloudMutationOutcomeUnknown(err),
			err:                  err,
		}
	}
}

func lockAndReloadProfileCloudAction(store ProfileStore, profile Profile, state ProfileState) (Profile, ProfileState, profileOperationLock, error) {
	if store == nil {
		return profile, state, nil, nil
	}
	latestProfile, latestState, lock, err := lockAndLoadProfile(store, profile.ID)
	if err != nil {
		return Profile{}, ProfileState{}, nil, fmt.Errorf("reload profile before DigitalOcean action: %w", err)
	}
	return latestProfile, latestState, lock, nil
}

func profileHasActiveCloudResource(profile Profile) bool {
	return profile.Cloud != nil && profile.Cloud.Provider == digitalOceanProviderName && profile.Cloud.ResourceID != "" && profile.Cloud.DestroyedAt == nil
}

func performProfileCloudAction(ctx context.Context, action string, provider cloudProvider, store ProfileStore, profile Profile, state ProfileState) (Profile, error) {
	switch action {
	case "restart":
		return restartProfileCloudDroplet(ctx, provider, profile)
	case "destroy":
		return destroyProfileCloudDroplet(ctx, provider, store, profile, state)
	default:
		return Profile{}, fmt.Errorf("unknown cloud action %q", action)
	}
}

func restartProfileCloudDroplet(ctx context.Context, provider cloudProvider, profile Profile) (Profile, error) {
	if err := provider.Reboot(ctx, profile.Cloud.ResourceID); err != nil {
		return profile, fmt.Errorf("restart DigitalOcean Droplet: %w", err)
	}
	return profile, nil
}

func destroyProfileCloudDroplet(ctx context.Context, provider cloudProvider, store ProfileStore, profile Profile, state ProfileState) (Profile, error) {
	if err := provider.Destroy(ctx, profile.Cloud.ResourceID); err != nil {
		return profile, fmt.Errorf("destroy DigitalOcean Droplet: %w", err)
	}
	now := time.Now().UTC()
	profile.Cloud.DestroyedAt = &now
	if store == nil {
		return profile, errors.New("save destroyed Droplet state: profile store is unavailable")
	}
	if err := store.Save(profile, state); err != nil {
		return profile, fmt.Errorf("save destroyed Droplet state: %w", err)
	}
	return profile, nil
}

func (model profileSetupModel) applyProfileCloudAction(msg profileCloudActionMsg) profileSetupModel {
	cancellationRequested := model.cloudCancelling
	if model.cloudCancel != nil {
		model.cloudCancel()
		model.cloudCancel = nil
	}
	model.cloudCancelling = false
	if msg.err != nil {
		return model.applyProfileCloudActionError(msg)
	}
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) && msg.profile.ID != "" {
		model.profiles[model.selectedIndex].Profile = msg.profile
		model.profiles[model.selectedIndex].State = msg.state
	}
	model.cloudNotice = profileCloudSuccessNotice(msg.action, cancellationRequested)
	model.err = ""
	model.cloudOutcomeUnknownID = ""
	model.screen = profileSetupScreenCloud
	return model
}

func (model profileSetupModel) applyProfileCloudActionError(msg profileCloudActionMsg) profileSetupModel {
	if msg.action == "destroy" && profileCloudMarkedDestroyed(msg.profile) {
		model.setProfileCloudRecovery(msg.profile, msg.state)
		model.err = msg.err.Error()
		model.cloudNotice = "DigitalOcean destroyed the Droplet, but Servestead still needs to save that result locally. Retrying will not call DigitalOcean again."
		model.screen = profileSetupScreenCloudSaveRecovery
		return model
	}
	if msg.remoteOutcomeUnknown || errors.Is(msg.err, context.Canceled) {
		model.screen = profileSetupScreenCloud
		model.err = ""
		model.cloudOutcomeUnknownID = firstNonEmpty(msg.profile.ID, model.selectedProfileID())
		model.cloudNotice = fmt.Sprintf("Servestead could not confirm the DigitalOcean %s result. Its remote outcome is unknown. Check DigitalOcean before retrying. No local profile changes were made.", msg.action)
		return model
	}
	model.screen = profileSetupScreenCloudConfirm
	model.cloudConfirmInput.Focus()
	model.err = msg.err.Error()
	return model
}

func profileCloudSuccessNotice(action string, cancellationRequested bool) string {
	switch action {
	case "restart":
		if cancellationRequested {
			return "DigitalOcean reboot completed before cancellation took effect."
		}
		return "DigitalOcean reboot action requested."
	case "destroy":
		if cancellationRequested {
			return "DigitalOcean Droplet was destroyed before cancellation took effect. The local Servestead profile was retained."
		}
		return "DigitalOcean Droplet destroyed. The local Servestead profile was retained."
	default:
		return "DigitalOcean action completed."
	}
}

func (model profileSetupModel) selectedProfileID() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return ""
	}
	return model.profiles[model.selectedIndex].Profile.ID
}

func profileCloudMarkedDestroyed(profile Profile) bool {
	return profile.Cloud != nil && profile.Cloud.DestroyedAt != nil && profile.Cloud.ResourceID != ""
}

func (model *profileSetupModel) setProfileCloudRecovery(profile Profile, state ProfileState) {
	model.cloudRecoveryProfile = profile
	model.cloudRecoveryState = state
	model.cloudRecoverySaving = false
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		model.profiles[model.selectedIndex].Profile = profile
		model.profiles[model.selectedIndex].State = state
	}
}

func (model profileSetupModel) updateProfileCloudSaveRecovery(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() != "enter" || model.cloudRecoverySaving {
		return model, nil
	}
	model.cloudRecoverySaving = true
	model.err = ""
	profile := model.cloudRecoveryProfile
	store := model.profileStore
	return model, func() tea.Msg {
		if store == nil {
			return profileCloudSaveMsg{profile: profile, err: errors.New("profile store is unavailable")}
		}
		profile, state, err := saveProfileCloudRecovery(store, profile)
		return profileCloudSaveMsg{profile: profile, state: state, err: err}
	}
}

func saveProfileCloudRecovery(store ProfileStore, recovery Profile) (Profile, ProfileState, error) {
	profile, state, lock, err := lockAndLoadProfile(store, recovery.ID)
	if err != nil {
		return Profile{}, ProfileState{}, fmt.Errorf("reload profile before saving destroyed Droplet state: %w", err)
	}
	defer releaseProfileOperationLock(lock)
	if recovery.Cloud == nil || profile.Cloud == nil || recovery.Cloud.ResourceID != profile.Cloud.ResourceID {
		return Profile{}, ProfileState{}, errors.New("saved profile no longer matches the destroyed DigitalOcean Droplet")
	}
	cloud := *recovery.Cloud
	profile.Cloud = &cloud
	if err := store.Save(profile, state); err != nil {
		return profile, state, err
	}
	return profile, state, nil
}

func (model profileSetupModel) applyProfileCloudSave(msg profileCloudSaveMsg) profileSetupModel {
	model.cloudRecoverySaving = false
	if msg.err != nil {
		model.err = fmt.Sprintf("save destroyed Droplet state: %v", msg.err)
		model.screen = profileSetupScreenCloudSaveRecovery
		return model
	}
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		model.profiles[model.selectedIndex].Profile = msg.profile
		model.profiles[model.selectedIndex].State = msg.state
	}
	model.cloudRecoveryProfile = Profile{}
	model.cloudRecoveryState = ProfileState{}
	model.err = ""
	model.cloudNotice = "DigitalOcean Droplet destroyed. The local Servestead profile now records the remote deletion."
	model.screen = profileSetupScreenCloud
	return model
}

func (model profileSetupModel) cancelProfileCloudAction() (tea.Model, tea.Cmd, bool) {
	if model.screen != profileSetupScreenCloudRunning {
		return model, nil, false
	}
	if model.cloudCancel != nil {
		model.cloudCancel()
		model.cloudCancel = nil
	}
	model.cloudCancelling = true
	return model, nil, true
}

func (model profileSetupModel) profileCloudSummary(profile Profile) string {
	if profile.Cloud == nil {
		return ""
	}
	cloud := profile.Cloud
	status := "active"
	if cloud.DestroyedAt != nil {
		status = "destroyed " + cloud.DestroyedAt.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf(
		"DigitalOcean: %s (%s)\nDroplet: %s  Region: %s  Size: %s  Image: %s  Cost: $%.2f/mo",
		sanitizeTerminalLine(firstNonEmpty(cloud.Name, profile.Name)),
		status,
		sanitizeTerminalLine(cloud.ResourceID),
		sanitizeTerminalLine(firstNonEmpty(cloud.Region, "unknown")),
		sanitizeTerminalLine(firstNonEmpty(cloud.Size, "unknown")),
		sanitizeTerminalLine(firstNonEmpty(cloud.Image, "unknown")),
		cloud.PriceMonthly,
	)
}

func (model profileSetupModel) profileCloudView() string {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return "No profile selected."
	}
	profile := model.profiles[model.selectedIndex].Profile
	var builder strings.Builder
	builder.WriteString("DigitalOcean Droplet actions\n\n")
	builder.WriteString(model.profileCloudSummary(profile))
	builder.WriteString("\n\n")
	if model.cloudNotice != "" {
		builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.cloudNotice)))
		builder.WriteString("\n\n")
	}
	_, _, active := model.selectedProfileActiveCloud()
	if !active {
		builder.WriteString("No active DigitalOcean Droplet action is available for this profile.\n")
		return builder.String()
	}
	builder.WriteString("Actions:\n")
	builder.WriteString("- r: request a Droplet reboot.\n")
	builder.WriteString("- d: permanently destroy the Droplet at DigitalOcean.\n\n")
	builder.WriteString("Destroy only changes the remote Droplet and marks this profile as destroyed. It does not delete local profile files, secrets, or run logs.\n")
	return builder.String()
}

func (model profileSetupModel) profileCloudConfirmView() string {
	_, cloud, active := model.selectedProfileActiveCloud()
	if !active {
		return "No active DigitalOcean Droplet action is available for this profile."
	}
	expected := profileCloudConfirmPhrase(model.cloudAction, cloud)
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Confirm DigitalOcean %s\n\n", sanitizeTerminalLine(model.cloudAction)))
	builder.WriteString(fmt.Sprintf("Droplet ID: %s\n", sanitizeTerminalLine(cloud.ResourceID)))
	builder.WriteString(fmt.Sprintf("Name:       %s\n", sanitizeTerminalLine(firstNonEmpty(cloud.Name, "(unnamed)"))))
	builder.WriteString(fmt.Sprintf("Region:     %s\n", sanitizeTerminalLine(firstNonEmpty(cloud.Region, "unknown"))))
	if model.cloudAction == "destroy" {
		builder.WriteString("\nThis permanently deletes the remote Droplet at DigitalOcean. Local Servestead profile files remain.\n")
	}
	builder.WriteString(fmt.Sprintf("\nType %q to continue.\n\n", expected))
	builder.WriteString(model.cloudTokenInput.View())
	builder.WriteString("\n")
	builder.WriteString(model.cloudConfirmInput.View())
	return builder.String()
}

func (model profileSetupModel) profileCloudRunningView() string {
	if model.cloudCancelling {
		return fmt.Sprintf("Cancelling DigitalOcean %s action...\nWaiting for DigitalOcean to acknowledge cancellation.\n", sanitizeTerminalLine(model.cloudAction))
	}
	return fmt.Sprintf("Running DigitalOcean %s action...\n", sanitizeTerminalLine(model.cloudAction))
}

func (model profileSetupModel) profileCloudSaveRecoveryView() string {
	cloud := model.cloudRecoveryProfile.Cloud
	resourceID := "unknown"
	if cloud != nil {
		resourceID = firstNonEmpty(cloud.ResourceID, resourceID)
	}
	var builder strings.Builder
	builder.WriteString("Finish recording a destroyed DigitalOcean Droplet\n\n")
	builder.WriteString(fmt.Sprintf("Droplet ID: %s\n", sanitizeTerminalLine(resourceID)))
	builder.WriteString("The remote Droplet was destroyed, but the local profile update failed. DigitalOcean will not be called again.\n\n")
	if model.cloudRecoverySaving {
		builder.WriteString("Saving the local profile state...")
	} else {
		builder.WriteString("Press Enter to retry the local save.")
	}
	return builder.String()
}

func profileCloudConfirmPhrase(action string, cloud *ProfileCloud) string {
	if cloud == nil {
		return ""
	}
	return strings.TrimSpace(sanitizeTerminalLine(action) + " " + sanitizeTerminalLine(cloud.ResourceID))
}
