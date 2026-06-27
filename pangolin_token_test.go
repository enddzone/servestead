package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintSavedPangolinTokenResolvesProfileByIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:         "production",
		IP:         "203.0.113.10",
		BaseDomain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{
		ServerSecret:       "server-secret",
		PangolinSetupToken: "0123456789abcdefghijklmnopqrstuv",
	}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := printSavedPangolinToken(store, "", profile.IP, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"https://pangolin.example.com/auth/initial-setup",
		"0123456789abcdefghijklmnopqrstuv",
		"valid only until the initial server admin is registered",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestPrintSavedPangolinTokenRejectsAmbiguousIP(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	for _, id := range []string{"old-server", "new-server"} {
		if _, err := store.Create(Profile{ID: id, IP: "203.0.113.10"}); err != nil {
			t.Fatal(err)
		}
	}

	err := printSavedPangolinToken(store, "", "203.0.113.10", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "rerun with --profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}
