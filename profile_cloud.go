package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const profileCloudNoActiveDropletMessage = "this profile does not have an active DigitalOcean Droplet"

type profileCloudActionMsg struct {
	action  string
	profile Profile
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
	model.cloudAction = action
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
		return model, model.runProfileCloudAction(token)
	}
	var cmd tea.Cmd
	if model.focus == 0 {
		model.cloudTokenInput, cmd = model.cloudTokenInput.Update(key)
	} else {
		model.cloudConfirmInput, cmd = model.cloudConfirmInput.Update(key)
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

func (model profileSetupModel) runProfileCloudAction(token string) tea.Cmd {
	action := model.cloudAction
	profile := model.profiles[model.selectedIndex].Profile
	state := model.profiles[model.selectedIndex].State
	store := model.profileStore
	return func() tea.Msg {
		if profile.Cloud == nil {
			return profileCloudActionMsg{action: action, err: fmt.Errorf("profile %s has no cloud metadata", profile.ID)}
		}
		provider := newProvisionCloudProvider(token)
		profile, err := performProfileCloudAction(action, provider, store, profile, state)
		if err != nil {
			return profileCloudActionMsg{action: action, err: err}
		}
		return profileCloudActionMsg{action: action, profile: profile}
	}
}

func performProfileCloudAction(action string, provider cloudProvider, store ProfileStore, profile Profile, state ProfileState) (Profile, error) {
	switch action {
	case "restart":
		return restartProfileCloudDroplet(provider, profile)
	case "destroy":
		return destroyProfileCloudDroplet(provider, store, profile, state)
	default:
		return Profile{}, fmt.Errorf("unknown cloud action %q", action)
	}
}

func restartProfileCloudDroplet(provider cloudProvider, profile Profile) (Profile, error) {
	if err := provider.Reboot(context.Background(), profile.Cloud.ResourceID); err != nil {
		return Profile{}, fmt.Errorf("restart DigitalOcean Droplet: %w", err)
	}
	return profile, nil
}

func destroyProfileCloudDroplet(provider cloudProvider, store ProfileStore, profile Profile, state ProfileState) (Profile, error) {
	if err := provider.Destroy(context.Background(), profile.Cloud.ResourceID); err != nil {
		return Profile{}, fmt.Errorf("destroy DigitalOcean Droplet: %w", err)
	}
	now := time.Now().UTC()
	profile.Cloud.DestroyedAt = &now
	if store == nil {
		return profile, nil
	}
	return profile, store.Save(profile, state)
}

func (model profileSetupModel) applyProfileCloudAction(msg profileCloudActionMsg) profileSetupModel {
	if msg.err != nil {
		model.screen = profileSetupScreenCloudConfirm
		model.cloudConfirmInput.Focus()
		model.err = msg.err.Error()
		return model
	}
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) && msg.profile.ID != "" {
		model.profiles[model.selectedIndex].Profile = msg.profile
	}
	switch msg.action {
	case "restart":
		model.cloudNotice = "DigitalOcean reboot action requested."
	case "destroy":
		model.cloudNotice = "DigitalOcean Droplet destroyed. The local Servestead profile was retained."
	default:
		model.cloudNotice = "DigitalOcean action completed."
	}
	model.err = ""
	model.screen = profileSetupScreenCloud
	return model
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
		firstNonEmpty(cloud.Name, profile.Name),
		status,
		cloud.ResourceID,
		firstNonEmpty(cloud.Region, "unknown"),
		firstNonEmpty(cloud.Size, "unknown"),
		firstNonEmpty(cloud.Image, "unknown"),
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
		builder.WriteString(setupWarningStyle.Render(model.cloudNotice))
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
	builder.WriteString(fmt.Sprintf("Confirm DigitalOcean %s\n\n", model.cloudAction))
	builder.WriteString(fmt.Sprintf("Droplet ID: %s\n", cloud.ResourceID))
	builder.WriteString(fmt.Sprintf("Name:       %s\n", firstNonEmpty(cloud.Name, "(unnamed)")))
	builder.WriteString(fmt.Sprintf("Region:     %s\n", firstNonEmpty(cloud.Region, "unknown")))
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
	return fmt.Sprintf("Running DigitalOcean %s action...\n", model.cloudAction)
}

func profileCloudConfirmPhrase(action string, cloud *ProfileCloud) string {
	if cloud == nil {
		return ""
	}
	return strings.TrimSpace(action + " " + cloud.ResourceID)
}
