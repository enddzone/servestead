package main

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProvisionTUIHandlesInitialResizeBeforeCatalog(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("initial resize before catalog loaded should not panic: %v", recovered)
		}
	}()

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	result, ok := updated.(digitalOceanProvisionModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if result.width != 120 || result.height != 40 {
		t.Fatalf("resize was not applied: width=%d height=%d", result.width, result.height)
	}
}

func TestProvisionSSHKeyChoicesPreferExistingMatch(t *testing.T) {
	catalog := cloudCatalog{SSHKeys: []cloudSSHKey{
		{ID: 2, Name: "other", Fingerprint: "bb:bb", PublicKey: "ssh-ed25519 BBBB other"},
		{ID: 1, Name: "servestead", Fingerprint: "aa:aa", PublicKey: "ssh-ed25519 AAAA local"},
	}}

	choices := provisionSSHKeyChoices(catalog, "ssh-ed25519 AAAA local", "aa:aa")
	if len(choices) != 2 || choices[0].Upload || choices[0].Key.ID != 1 {
		t.Fatalf("matching provider key should be first without upload prompt: %+v", choices)
	}

	choices = provisionSSHKeyChoices(catalog, "ssh-ed25519 CCCC new", "cc:cc")
	if len(choices) != 3 || !choices[0].Upload {
		t.Fatalf("missing provider key should offer upload first: %+v", choices)
	}
}

func TestSaveProvisionedDigitalOceanProfileStoresCloudMetadata(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	model := digitalOceanProvisionModel{
		selectedRegion: cloudRegion{Slug: "nyc3"},
		selectedSize:   cloudSize{Slug: "s-1vcpu-1gb", PriceMonthly: 6, PriceHourly: 0.00893},
		selectedImage:  cloudImage{Slug: "ubuntu-24-04-x64"},
	}
	profile, err := saveProvisionedDigitalOceanProfile(store, provisionInputConfig{
		Name:           "servestead-test",
		PrivateKeyPath: "/tmp/servestead",
	}, model, server{
		ID:        "84",
		Name:      "servestead-test",
		IPv4:      "198.51.100.84",
		CreatedAt: "2026-06-30T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IP != "198.51.100.84" || loaded.Cloud == nil || loaded.Cloud.ResourceID != "84" {
		t.Fatalf("provisioned profile did not persist cloud metadata: %+v", loaded)
	}
	if loaded.Cloud.Provider != digitalOceanProviderName || loaded.Cloud.Region != "nyc3" ||
		loaded.Cloud.Size != "s-1vcpu-1gb" || loaded.Cloud.Image != "ubuntu-24-04-x64" ||
		loaded.Cloud.PriceMonthly != 6 {
		t.Fatalf("unexpected cloud metadata: %+v", loaded.Cloud)
	}
}

func TestProfileCloudDestroyMarksProfileWithoutDeleting(t *testing.T) {
	originalProvider := newProvisionCloudProvider
	fake := &recordingCloudProvider{}
	newProvisionCloudProvider = func(string) cloudProvider { return fake }
	defer func() { newProvisionCloudProvider = originalProvider }()

	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{
		ID:             "production",
		Name:           "production",
		IP:             "198.51.100.84",
		PrivateKeyPath: "/tmp/servestead",
		Cloud: &ProfileCloud{
			Provider:   digitalOceanProviderName,
			ResourceID: "84",
			Name:       "production",
			Region:     "nyc3",
			Size:       "s-1vcpu-1gb",
			Image:      "ubuntu-24-04-x64",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}

	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state}})
	model.profileStore = store
	model.selectedIndex = 0
	model.cloudAction = "destroy"
	message := model.runProfileCloudAction("token")().(profileCloudActionMsg)
	result := model.applyProfileCloudAction(message)

	if fake.destroyedID != "84" {
		t.Fatalf("destroy was not sent to provider: %+v", fake)
	}
	loaded, _, err := store.Load(profile.ID)
	if err != nil {
		t.Fatalf("local profile should remain after remote destroy: %v", err)
	}
	if loaded.Cloud == nil || loaded.Cloud.DestroyedAt == nil {
		t.Fatalf("destroyed profile was not marked: %+v", loaded.Cloud)
	}
	if !strings.Contains(result.cloudNotice, "retained") {
		t.Fatalf("destroy notice should explain profile retention: %q", result.cloudNotice)
	}
}

type recordingCloudProvider struct {
	destroyedID string
	rebootedID  string
}

func (provider *recordingCloudProvider) Catalog(context.Context) (cloudCatalog, error) {
	return cloudCatalog{}, nil
}

func (provider *recordingCloudProvider) Create(context.Context, provisionConfig) (server, error) {
	return server{}, nil
}

func (provider *recordingCloudProvider) CreateSSHKey(context.Context, string, string) (cloudSSHKey, error) {
	return cloudSSHKey{}, nil
}

func (provider *recordingCloudProvider) Reboot(_ context.Context, id string) error {
	provider.rebootedID = id
	return nil
}

func (provider *recordingCloudProvider) Destroy(_ context.Context, id string) error {
	provider.destroyedID = id
	return nil
}
