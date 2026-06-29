package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintSavedPangolinCredentialsResolvesProfileByIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:                 "production",
		IP:                 "203.0.113.10",
		BaseDomain:         "example.com",
		LetsEncryptEmail:   "admin@example.com",
		PangolinAdminEmail: "owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		ServerSecret:          "server-secret",
		PangolinAdminPassword: "current-password",
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := printSavedPangolinCredentials(store, "", profile.IP, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"Pangolin URL: https://pangolin.example.com",
		"Username: owner@example.com",
		"Password: current-password",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "setup token") || strings.Contains(output.String(), "initial-setup") {
		t.Fatalf("credentials output exposed obsolete setup-token guidance:\n%s", output.String())
	}
}

func TestPrintSavedPangolinCredentialsRejectsAmbiguousIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	for _, id := range []string{"old-server", "new-server"} {
		if _, err := store.Create(Profile{ID: id, IP: "203.0.113.10"}); err != nil {
			t.Fatal(err)
		}
	}

	err := printSavedPangolinCredentials(store, "", "203.0.113.10", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rerun with --profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}
