package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPangolinInitialSetupComplete(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		complete bool
	}{
		{name: "incomplete", response: `{"data":{"complete":false}}`, complete: false},
		{name: "complete", response: `{"data":{"complete":true}}`, complete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v1/auth/initial-setup-complete" {
					t.Fatalf("unexpected path: %s", request.URL.Path)
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(test.response))
			}))
			defer server.Close()

			complete, err := pangolinInitialSetupComplete(context.Background(), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if complete != test.complete {
				t.Fatalf("complete = %t, want %t", complete, test.complete)
			}
		})
	}
}

func TestPangolinInitialSetupCompleteRejectsMissingState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`{"data":{}}`))
	}))
	defer server.Close()

	_, err := pangolinInitialSetupComplete(context.Background(), server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "did not include setup completion state") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProfileDashboardHighlightsIncompletePangolinAndRevealsToken(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:         "production",
			Name:       "production",
			IP:         "203.0.113.10",
			BaseDomain: "example.com",
		},
		State: ProfileState{
			ActiveRunID: "run-1",
			Runs: map[string]SetupRun{
				"run-1": {
					ID: "run-1",
					Stages: map[string]SetupStageStatus{
						"proxy": {Status: stageStatusComplete},
					},
				},
			},
		},
		Secrets: ProfileSecrets{PangolinSetupToken: "0123456789abcdefghijklmnopqrstuv"},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard

	updated, _ := model.Update(pangolinRegistrationStatusMsg{profileID: "production", complete: false})
	model = updated.(profileSetupModel)
	view := model.View()
	for _, expected := range []string{
		"ACTION REQUIRED: Pangolin initial admin registration is incomplete.",
		"Press t to reveal the saved setup token",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, choice.Secrets.PangolinSetupToken) {
		t.Fatalf("dashboard exposed setup token before reveal:\n%s", view)
	}

	updated, _ = model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	view = updated.(profileSetupModel).View()
	for _, expected := range []string{
		"https://pangolin.example.com/auth/initial-setup",
		choice.Secrets.PangolinSetupToken,
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("revealed dashboard missing %q:\n%s", expected, view)
		}
	}
}

func TestLoadProfileChoicesIncludesSavedPangolinToken(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "production", IP: "203.0.113.10"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		PangolinSetupToken: "0123456789abcdefghijklmnopqrstuv",
	}); err != nil {
		t.Fatal(err)
	}

	choices, err := loadProfileChoices(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].Secrets.PangolinSetupToken != "0123456789abcdefghijklmnopqrstuv" {
		t.Fatalf("saved setup token was not loaded into TUI choice: %+v", choices)
	}
}

func TestProfileDashboardChecksPangolinOnlyAfterProxyDeployment(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: "production", BaseDomain: "example.com"},
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.refreshDashboard()
	if command := model.checkPangolinRegistration(); command != nil {
		t.Fatal("registration check should not run before Proxy deployment")
	}
	if model.pangolinStatus != pangolinRegistrationUnknown {
		t.Fatalf("unexpected status: %s", model.pangolinStatus)
	}
}
