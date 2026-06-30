package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	pangolinRegistrationTestProfileID       = "production"
	pangolinRegistrationTestRunID           = "run-1"
	pangolinRegistrationTestHost            = "203.0.113.10"
	pangolinRegistrationTestDomain          = "example.com"
	pangolinRegistrationTestOwnerEmail      = "owner@example.com"
	pangolinRegistrationTestSetupToken      = "0123456789abcdefghijklmnopqrstuv"
	pangolinRegistrationTestCurrentPassword = "current-password"
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

func TestProfileDashboardHighlightsIncompletePangolinAndRevealsSetupToken(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:                 pangolinRegistrationTestProfileID,
			Name:               pangolinRegistrationTestProfileID,
			IP:                 pangolinRegistrationTestHost,
			BaseDomain:         pangolinRegistrationTestDomain,
			PangolinAdminEmail: pangolinRegistrationTestOwnerEmail,
		},
		State: ProfileState{
			ActiveRunID: pangolinRegistrationTestRunID,
			Runs: map[string]SetupRun{
				pangolinRegistrationTestRunID: {
					ID: pangolinRegistrationTestRunID,
					Stages: map[string]SetupStageStatus{
						"proxy": {Status: stageStatusComplete},
					},
				},
			},
		},
		Secrets: ProfileSecrets{
			PangolinSetupToken:    pangolinRegistrationTestSetupToken,
			PangolinAdminPassword: pangolinRegistrationTestCurrentPassword,
		},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard

	updated, _ := model.Update(pangolinRegistrationStatusMsg{profileID: pangolinRegistrationTestProfileID, complete: false})
	model = updated.(profileSetupModel)
	view := model.View()
	for _, expected := range []string{
		"ACTION REQUIRED: Pangolin initial admin registration is incomplete.",
		"Press p to reveal the saved setup token and initial-setup URL.",
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, choice.Secrets.PangolinAdminPassword) {
		t.Fatalf("dashboard exposed Pangolin password before reveal:\n%s", view)
	}
	if strings.Contains(view, choice.Secrets.PangolinSetupToken) {
		t.Fatalf("dashboard exposed setup token before reveal:\n%s", view)
	}

	updated, _ = model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	view = updated.(profileSetupModel).View()
	for _, expected := range []string{
		"https://pangolin." + pangolinRegistrationTestDomain + "/auth/initial-setup",
		"Setup token: " + pangolinRegistrationTestSetupToken,
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("revealed dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, choice.Secrets.PangolinAdminPassword) {
		t.Fatalf("incomplete registration dashboard exposed admin password instead of setup token:\n%s", view)
	}
}

func TestProfileDashboardRevealsPangolinCredentialsWhenRegistrationComplete(t *testing.T) {
	choice := profileChoice{
		Profile: Profile{
			ID:                 pangolinRegistrationTestProfileID,
			Name:               pangolinRegistrationTestProfileID,
			IP:                 pangolinRegistrationTestHost,
			BaseDomain:         pangolinRegistrationTestDomain,
			PangolinAdminEmail: pangolinRegistrationTestOwnerEmail,
		},
		State: ProfileState{
			ActiveRunID: pangolinRegistrationTestRunID,
			Runs: map[string]SetupRun{
				pangolinRegistrationTestRunID: {
					ID: pangolinRegistrationTestRunID,
					Stages: map[string]SetupStageStatus{
						"proxy": {Status: stageStatusComplete},
					},
				},
			},
		},
		Secrets: ProfileSecrets{
			PangolinSetupToken:    pangolinRegistrationTestSetupToken,
			PangolinAdminPassword: pangolinRegistrationTestCurrentPassword,
		},
	}
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.refreshDashboard()
	model.screen = profileSetupScreenDashboard

	updated, _ := model.Update(pangolinRegistrationStatusMsg{profileID: pangolinRegistrationTestProfileID, complete: true})
	model = updated.(profileSetupModel)
	updated, _ = model.updateProfileDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	view := updated.(profileSetupModel).View()
	for _, expected := range []string{
		"Pangolin URL: https://pangolin." + pangolinRegistrationTestDomain,
		"Username: " + pangolinRegistrationTestOwnerEmail,
		"Password: " + pangolinRegistrationTestCurrentPassword,
	} {
		if !strings.Contains(view, expected) {
			t.Fatalf("revealed dashboard missing %q:\n%s", expected, view)
		}
	}
	if strings.Contains(view, choice.Secrets.PangolinSetupToken) || strings.Contains(view, "initial-setup") {
		t.Fatalf("completed registration dashboard exposed setup-token access:\n%s", view)
	}
}

func TestLoadProfileChoicesIncludesSavedPangolinCredentials(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: pangolinRegistrationTestProfileID, IP: pangolinRegistrationTestHost})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		PangolinAdminPassword: pangolinRegistrationTestCurrentPassword,
	}); err != nil {
		t.Fatal(err)
	}

	choices, err := loadProfileChoices(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || choices[0].Secrets.PangolinAdminPassword != pangolinRegistrationTestCurrentPassword {
		t.Fatalf("saved Pangolin credentials were not loaded into TUI choice: %+v", choices)
	}
}

func TestProfileDashboardChecksPangolinBeforeRetryingProxy(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: pangolinRegistrationTestProfileID, BaseDomain: pangolinRegistrationTestDomain},
		State:   ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	model.refreshDashboard()
	if command := model.checkPangolinRegistration(); command == nil {
		t.Fatal("registration check should detect partial Proxy deployments")
	}
	if model.pangolinStatus != pangolinRegistrationChecking {
		t.Fatalf("unexpected status: %s", model.pangolinStatus)
	}
}
