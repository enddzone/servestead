package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

const tuiDecoyProfileName = "REAL PROFILE MUST NOT APPEAR"

func TestProfilePickerProgramUsesInjectedEmptyStore(t *testing.T) {
	decoyStore := newFileProfileStore(t.TempDir())
	if _, err := decoyStore.Create(Profile{
		ID:   "real-profile-decoy",
		Name: tuiDecoyProfileName,
		IP:   "198.51.100.77",
	}); err != nil {
		t.Fatalf("create decoy profile: %v", err)
	}

	configRoot := t.TempDir()
	t.Setenv(servesteadConfigDirEnv, configRoot)
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "")
	t.Setenv("DIGITALOCEAN_TOKEN", "")
	t.Setenv("SERVESTEAD_GITHUB_TOKEN", "")
	t.Setenv("PANGOLIN_ADMIN_PASSWORD", "")

	store, err := newDefaultProfileStore()
	if err != nil {
		t.Fatalf("create isolated profile store: %v", err)
	}
	fileStore, ok := store.(*fileProfileStore)
	if !ok || fileStore.root != configRoot {
		t.Fatalf("expected %s profile store at %q, got %#v", servesteadConfigDirEnv, configRoot, store)
	}
	profiles, err := loadProfileChoices(store)
	if err != nil {
		t.Fatalf("load isolated profiles: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("expected an empty profile picker, got %d profile(s)", len(profiles))
	}

	model := newProfileSetupModel(profiles)
	model.profileStore = store
	var output bytes.Buffer
	input := bytes.NewBufferString("q")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	finalModel, err := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(&output),
		tea.WithWindowSize(80, 24),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithoutSignals(),
	).Run()
	if err != nil {
		t.Fatalf("run profile picker program: %v", err)
	}

	result, ok := finalModel.(profileSetupModel)
	if !ok {
		t.Fatalf("unexpected final model type %T", finalModel)
	}
	if !result.quit || result.cancelled {
		t.Fatalf("expected q to quit cleanly, got quit=%t cancelled=%t", result.quit, result.cancelled)
	}

	rendered := output.String()
	for _, expected := range []string{
		"Servestead profiles",
		"Provision a new DigitalOcean VPS",
		"Set up a new server profile",
		"Advanced legacy setup paths",
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("profile picker output does not contain %q", expected)
		}
	}
	if strings.Contains(rendered, tuiDecoyProfileName) {
		t.Fatalf("profile picker leaked a profile outside its injected store: %q", rendered)
	}
}
