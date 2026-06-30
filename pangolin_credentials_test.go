package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

const (
	pangolinCredentialsTestProfileID       = "production"
	pangolinCredentialsTestHost            = "203.0.113.10"
	pangolinCredentialsTestDomain          = "example.com"
	pangolinCredentialsTestOwnerEmail      = "owner@example.com"
	pangolinCredentialsTestSetupToken      = "0123456789abcdefghijklmnopqrstuv"
	pangolinCredentialsTestCurrentPassword = "current-password"
)

func TestPrintSavedPangolinCredentialsResolvesProfileByIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                 pangolinCredentialsTestProfileID,
		IP:                 pangolinCredentialsTestHost,
		BaseDomain:         pangolinCredentialsTestDomain,
		LetsEncryptEmail:   "admin@example.com",
		PangolinAdminEmail: pangolinCredentialsTestOwnerEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		ServerSecret:          "server-secret",
		PangolinAdminPassword: pangolinCredentialsTestCurrentPassword,
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := printSavedPangolinCredentials(store, "", profile.IP, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Pangolin URL: https://pangolin." + pangolinCredentialsTestDomain,
		"Username: " + pangolinCredentialsTestOwnerEmail,
		"Password: " + pangolinCredentialsTestCurrentPassword,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "setup token") || strings.Contains(output.String(), "initial-setup") {
		t.Fatalf("credentials output exposed obsolete setup-token guidance:\n%s", output.String())
	}
}

func TestPrintSavedPangolinCredentialsRevealsSetupTokenWhenRegistrationIncomplete(t *testing.T) {
	originalChecker := savedPangolinInitialSetupComplete
	defer func() { savedPangolinInitialSetupComplete = originalChecker }()
	savedPangolinInitialSetupComplete = func(_ context.Context, dashboardURL string) (bool, error) {
		if dashboardURL != "https://pangolin."+pangolinCredentialsTestDomain {
			t.Fatalf("unexpected dashboard URL: %s", dashboardURL)
		}
		return false, nil
	}

	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                 pangolinCredentialsTestProfileID,
		IP:                 pangolinCredentialsTestHost,
		BaseDomain:         pangolinCredentialsTestDomain,
		LetsEncryptEmail:   "admin@example.com",
		PangolinAdminEmail: pangolinCredentialsTestOwnerEmail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		ServerSecret:          "server-secret",
		PangolinSetupToken:    pangolinCredentialsTestSetupToken,
		PangolinAdminPassword: pangolinCredentialsTestCurrentPassword,
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := printSavedPangolinCredentials(store, profile.ID, "", &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Pangolin initial setup: https://pangolin." + pangolinCredentialsTestDomain + "/auth/initial-setup",
		"Setup token: " + pangolinCredentialsTestSetupToken,
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Password: "+pangolinCredentialsTestCurrentPassword) {
		t.Fatalf("incomplete registration output exposed admin password instead of setup token:\n%s", output.String())
	}
}

func TestPrintSavedPangolinCredentialsRejectsAmbiguousIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	for _, id := range []string{"old-server", "new-server"} {
		if _, err := store.Create(Profile{ID: id, IP: pangolinCredentialsTestHost}); err != nil {
			t.Fatal(err)
		}
	}

	err := printSavedPangolinCredentials(store, "", pangolinCredentialsTestHost, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rerun with --profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}
