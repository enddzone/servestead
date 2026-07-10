package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const profileTestHost = "203.0.113.10"

func TestProfileStoreCreatesPrivateProfileFiles(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile := createTestProfileWithSecrets(t, store)
	loaded, state, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IP != profileTestHost || state.Runs == nil {
		t.Fatalf("unexpected loaded profile/state: %+v %+v", loaded, state)
	}
	assertPrivateProfileFiles(t, store, profile.ID)
}

func createTestProfileWithSecrets(t *testing.T, store *fileProfileStore) Profile {
	t.Helper()
	profile, err := store.Create(Profile{IP: profileTestHost, PrivateKeyPath: "/tmp/aegis-key"})
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID == "" || profile.InitialSSHUser != "root" || profile.AdminUser != "servestead" {
		t.Fatalf("unexpected profile defaults: %+v", profile)
	}
	secrets := ProfileSecrets{}
	if err := secrets.EnsureServerSecret(); err != nil {
		t.Fatal(err)
	}
	if len(secrets.ServerSecret) < 40 || strings.ContainsAny(secrets.ServerSecret, "\r\n") {
		t.Fatalf("unexpected generated secret shape: %q", secrets.ServerSecret)
	}
	if err := secrets.EnsurePangolinSetupToken(); err != nil {
		t.Fatal(err)
	}
	if !pangolinSetupToken.MatchString(secrets.PangolinSetupToken) {
		t.Fatalf("unexpected generated Pangolin setup token: %q", secrets.PangolinSetupToken)
	}
	if err := store.SaveSecrets(profile.ID, secrets); err != nil {
		t.Fatal(err)
	}
	return profile
}

func assertPrivateProfileFiles(t *testing.T, store *fileProfileStore, profileID string) {
	t.Helper()
	profileDirectory, err := store.profileDirectory(profileID)
	if err != nil {
		t.Fatal(err)
	}
	profilePath, err := store.profilePath(profileID)
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := store.statePath(profileID)
	if err != nil {
		t.Fatal(err)
	}
	secretsPath, err := store.secretsPath(profileID)
	if err != nil {
		t.Fatal(err)
	}
	assertFileMode(t, profileDirectory, 0700)
	assertFileMode(t, profilePath, 0600)
	assertFileMode(t, statePath, 0600)
	assertFileMode(t, secretsPath, 0600)
}

func TestProfileStoreResolveByIPSortsNewestFirst(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	older, err := store.Create(Profile{IP: profileTestHost, Name: "older"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := store.Create(Profile{IP: profileTestHost, Name: "newer"})
	if err != nil {
		t.Fatal(err)
	}
	older.UpdatedAt = time.Now().Add(-time.Hour)
	newer.UpdatedAt = time.Now()
	if err := store.Save(older, ProfileState{Runs: map[string]SetupRun{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(newer, ProfileState{Runs: map[string]SetupRun{}}); err != nil {
		t.Fatal(err)
	}

	matches, err := store.ResolveByIP(profileTestHost)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != newer.ID {
		t.Fatalf("unexpected matches: %+v", matches)
	}
}

func TestProfileStoreCorruptJSONNamesPath(t *testing.T) {
	root := t.TempDir()
	store := newFileProfileStore(root)
	profileDirectory := filepath.Join(root, "profiles", "broken")
	if err := os.MkdirAll(profileDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(profileDirectory, "profile.json")
	if err := os.WriteFile(profilePath, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDirectory, "state.json"), []byte(`{"runs":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := store.Load("broken")
	if err == nil || !strings.Contains(err.Error(), profilePath) {
		t.Fatalf("corrupt JSON error did not name path: %v", err)
	}
}

func TestProfileStoreDeletesProfileDirectory(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "old-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSecrets(profile.ID, ProfileSecrets{ServerSecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunEvent(profile.ID, "run-1", TaskEvent{Type: TaskStarted, RunID: "run-1", Stage: "bootstrap", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(profile.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted profile should not load: %v", err)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("deleted profile still listed: %+v", summaries)
	}
}

func TestProfileStoreAppendsJSONLEvents(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	event := TaskEvent{Type: TaskStarted, RunID: "run-1", Stage: "bootstrap", TaskName: "Example", Time: time.Now()}
	if err := store.AppendRunEvent(profile.ID, "run-1", event); err != nil {
		t.Fatal(err)
	}
	profileDirectory, err := store.profileDirectory(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(profileDirectory, "logs", "run-1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded TaskEvent
	if err := json.Unmarshal(data[:len(data)-1], &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Type != TaskStarted || decoded.TaskName != "Example" {
		t.Fatalf("unexpected event: %+v", decoded)
	}
}

func TestProfileStoreRejectsRunLogPathTraversal(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"../outside", `..\outside`, "run/one", "run..one"} {
		if err := store.AppendRunEvent(profile.ID, runID, TaskEvent{Type: TaskStarted, RunID: runID}); err == nil {
			t.Fatalf("AppendRunEvent(%q) succeeded, want error", runID)
		}
	}
	if err := store.AppendRunEvent("../outside", "run-1", TaskEvent{Type: TaskStarted, RunID: "run-1"}); err == nil {
		t.Fatal("AppendRunEvent accepted a traversal profile ID")
	}
}

func assertFileMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != expected {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), expected)
	}
}

func TestEnsureComposeWiringSecretsIsStable(t *testing.T) {
	var secrets ProfileSecrets
	if err := secrets.EnsureComposeWiringSecrets(); err != nil {
		t.Fatal(err)
	}
	first := secrets
	if err := secrets.EnsureComposeWiringSecrets(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secrets, first) {
		t.Fatal("existing compose wiring secrets changed")
	}
	if len(secrets.PangolinAdminPassword) != 32 || len(secrets.NewtID) != 15 ||
		len(secrets.NewtSecret) != 48 || len(secrets.BeszelAdminPassword) != 32 ||
		len(secrets.BeszelSystemToken) != 48 {
		t.Fatalf("unexpected generated secret lengths: %+v", secrets)
	}
	for _, required := range []string{"A", "a", "1", "!"} {
		if !strings.Contains(secrets.PangolinAdminPassword, required) {
			t.Fatalf("Pangolin password does not meet upstream complexity requirements")
		}
	}
	if _, err := ssh.ParsePrivateKey([]byte(secrets.BeszelHubPrivateKey)); err != nil {
		t.Fatalf("invalid Beszel Hub private key: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(secrets.BeszelHubPublicKey)); err != nil {
		t.Fatalf("invalid Beszel Hub public key: %v", err)
	}
}

func TestEnsureGeneratedSecretPropagatesGeneratorError(t *testing.T) {
	expected := errors.New("generator failed")
	value := ""
	err := ensureGeneratedSecret(&value, 32, func(int) (string, error) {
		return "", expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("expected generator error, got %v", err)
	}
	if value != "" {
		t.Fatalf("secret was set after generator failure: %q", value)
	}
}
