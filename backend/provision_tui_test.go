package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

const (
	provisionTestDropletName     = "servestead-test"
	provisionTestIPv4            = "198.51.100.84"
	provisionTestSize            = "s-1vcpu-1gb"
	provisionTestImage           = "ubuntu-24-04-x64"
	provisionTestUploadedKeyName = "servestead-id_ed25519"
	provisionTestExistingKeyName = "existing-key"
	provisionTestProfileID       = "profile-1"
	provisionTestLocalPublicKey  = "ssh-ed25519 AAAA local"
	provisionTestRegionName      = "New York 3"
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

func TestProvisionListFilterReceivesQuitAndEscapeKeysFirst(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.screen = provisionScreenRegion
	model.regionList = newProvisionList("DigitalOcean regions", []list.Item{
		provisionListItem{index: 0, title: "New York", description: "nyc3"},
	})

	updated, _ := model.Update(keyRunes("/"))
	result := updated.(digitalOceanProvisionModel)
	if result.regionList.FilterState() != list.Filtering {
		t.Fatal("/ did not focus the region filter")
	}
	updated, _ = result.Update(keyRunes("q"))
	result = updated.(digitalOceanProvisionModel)
	if result.screen != provisionScreenRegion || result.cancelled || result.regionList.FilterInput.Value() != "q" {
		t.Fatalf("q bypassed the active filter: screen=%d cancelled=%v filter=%q", result.screen, result.cancelled, result.regionList.FilterInput.Value())
	}
	updated, _ = result.Update(keyCode(tea.KeyEsc))
	result = updated.(digitalOceanProvisionModel)
	if result.screen != provisionScreenRegion || result.cancelled || result.regionList.FilterState() == list.Filtering {
		t.Fatalf("Esc did not clear the active filter in place: screen=%d cancelled=%v filterState=%d", result.screen, result.cancelled, result.regionList.FilterState())
	}
}

func TestProvisionListFilterKeepsCtrlCGlobal(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.screen = provisionScreenRegion
	model.regionList = newProvisionList("DigitalOcean regions", []list.Item{
		provisionListItem{index: 0, title: "New York", description: "nyc3"},
	})

	updated, _ := model.Update(keyRunes("/"))
	result := updated.(digitalOceanProvisionModel)
	if result.regionList.FilterState() != list.Filtering {
		t.Fatal("/ did not focus the region filter")
	}
	updated, command := result.Update(keyCtrl('c'))
	result = updated.(digitalOceanProvisionModel)
	if command == nil || !result.cancelled {
		t.Fatalf("ctrl+c was swallowed by the active list filter: cancelled=%v command=%v", result.cancelled, command)
	}
}

func TestProvisionTUIShowsMinimumTerminalSizeGuard(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	view := updated.(digitalOceanProvisionModel).View().Content
	if !strings.Contains(view, "Terminal too small: 40x12") || !strings.Contains(view, "80x24") {
		t.Fatalf("small terminal guard missing:\n%s", view)
	}
}

func TestProvisionReviewSanitizesNamesAndCatalogFields(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.inputs[0].SetValue("token")
	model.inputs[1].SetValue("prod\u202eowned")
	model.selectedRegion = cloudRegion{Name: "New\x1b[31m York", Slug: "nyc3\x1b]0;owned\a"}
	model.selectedSize = cloudSize{Slug: "small\x1b[31m", VCPUs: 1, MemoryMB: 1024, DiskGB: 25}
	model.selectedImage = cloudImage{Slug: "ubuntu\x1b]0;owned\a"}
	model.selectedKey = provisionSSHKeyChoice{Key: cloudSSHKey{Name: "operator\x1b[31m", Fingerprint: "aa:bb\x1b]0;owned\a"}}

	view := model.provisionReviewView()
	if strings.Contains(view, "\u202e") || strings.Contains(view, "\x1b]0;owned") || strings.Contains(view, "\x1b[31m") {
		t.Fatalf("provision review rendered terminal controls:\n%s", view)
	}
	for _, expected := range []string{"Name:   prodowned", "New York", "small", "ubuntu", "operator", "aa:bb"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("sanitized provision review missing %q:\n%s", expected, view)
		}
	}
	if _, err := model.inputConfig(); err == nil || !strings.Contains(err.Error(), "terminal control") {
		t.Fatalf("Droplet name control characters were accepted: %v", err)
	}
}

func TestProvisionVisibleInputsRemoveUnicodeFormatCharacters(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.focus = 1
	model.inputs[1].SetValue("")
	model.inputs[1].Focus()
	updated, _ := model.updateInput(keyRunes("prod\u202eowned"))
	model = updated.(digitalOceanProvisionModel)
	if model.inputs[1].Value() != "prodowned" || strings.Contains(model.inputs[1].View(), "\u202e") {
		t.Fatalf("provision input retained a Unicode format control: value=%q view=%q", model.inputs[1].Value(), model.inputs[1].View())
	}

	model.confirmInput.SetValue("")
	model.confirmInput.Focus()
	updated, _ = model.updateReview(keyRunes("create\u2066 now"))
	model = updated.(digitalOceanProvisionModel)
	if model.confirmInput.Value() != "create now" || strings.Contains(model.confirmInput.View(), "\u2066") {
		t.Fatalf("provision confirmation retained a Unicode format control: value=%q view=%q", model.confirmInput.Value(), model.confirmInput.View())
	}
}

func TestProvisionMinimumSizeGuardBlocksHiddenCreateConfirmation(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.screen = provisionScreenReview
	model.confirmInput.SetValue(provisionConfirmPhrase(model.inputConfigName()))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	result := updated.(digitalOceanProvisionModel)

	updated, command := result.Update(keyCode(tea.KeyEnter))
	result = updated.(digitalOceanProvisionModel)
	if command != nil || result.screen != provisionScreenReview || result.operationCancel != nil {
		t.Fatalf("small-terminal Enter triggered hidden Droplet creation: %+v", result)
	}
}

func TestProvisionTUIHandlesMessages(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	updated, command := model.Update(struct{}{})
	result := updated.(digitalOceanProvisionModel)
	if command != nil || result.screen != provisionScreenInput {
		t.Fatalf("unexpected non-key update result: %+v", result)
	}

	updated, command = model.Update(provisionCreateMsg{err: errors.New("create failed")})
	result = updated.(digitalOceanProvisionModel)
	if command != nil || result.screen != provisionScreenReview || !strings.Contains(result.err, "create failed") {
		t.Fatalf("create error was not shown on review screen: %+v", result)
	}

	updated, command = model.Update(provisionCreateMsg{profile: Profile{ID: provisionTestProfileID, IP: provisionTestIPv4}})
	result = updated.(digitalOceanProvisionModel)
	if command == nil || result.done || result.screen != provisionScreenSavingProfile || result.pendingProfile.ID != provisionTestProfileID {
		t.Fatalf("created Droplet message did not begin local persistence: %+v", result)
	}

	updated, command = result.Update(provisionSaveMsg{profile: result.pendingProfile})
	result = updated.(digitalOceanProvisionModel)
	if command != nil || !result.done || result.screen != provisionScreenDone || result.createdProfile.ID != provisionTestProfileID {
		t.Fatalf("saved profile message returned unexpected result: %+v", result)
	}
}

func TestProvisionTUIHandlesGlobalKeys(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	updated, command, handled := model.updateGlobalKey(keyCtrl('c'))
	result := updated.(digitalOceanProvisionModel)
	if !handled || command == nil || !result.cancelled {
		t.Fatalf("ctrl+c did not cancel: handled=%v result=%+v", handled, result)
	}

	model = newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	updated, command, handled = model.updateGlobalKey(keyCode(tea.KeyEsc))
	result = updated.(digitalOceanProvisionModel)
	if !handled || command == nil || !result.returnToSetup || result.cancelled {
		t.Fatalf("root Esc did not return to setup: handled=%v result=%+v", handled, result)
	}
	if _, err := profileFromProvisionResult(result); !errors.Is(err, errReturnToSetup) {
		t.Fatalf("root Esc result did not propagate return-to-setup: %v", err)
	}

	model.screen = provisionScreenRegion
	updated, command, handled = model.updateGlobalKey(keyRunes("q"))
	result = updated.(digitalOceanProvisionModel)
	if !handled || command == nil || !result.cancelled {
		t.Fatalf("q did not cancel list screen: handled=%v result=%+v", handled, result)
	}

	model.screen = provisionScreenDone
	updated, command, handled = model.updateGlobalKey(keyRunes("q"))
	result = updated.(digitalOceanProvisionModel)
	if !handled || command == nil || result.cancelled || !result.quit {
		t.Fatalf("q did not quit done screen cleanly: handled=%v result=%+v", handled, result)
	}
	if _, err := profileFromProvisionResult(result); !errors.Is(err, errSetupQuit) {
		t.Fatalf("done-screen quit would reopen the dashboard: %v", err)
	}

	model.screen = provisionScreenRegion
	model.err = "stale"
	updated, command, handled = model.updateGlobalKey(keyCode(tea.KeyEsc))
	result = updated.(digitalOceanProvisionModel)
	if !handled || command != nil || result.err != "" {
		t.Fatalf("esc did not go back cleanly: handled=%v result=%+v", handled, result)
	}

	model.screen = provisionScreenDone
	updated, command = model.updateScreenKey(keyCode(tea.KeyEnter))
	if command == nil {
		t.Fatalf("enter did not quit done screen: %+v", updated)
	}
	updated, command = model.updateScreenKey(keyRunes("x"))
	result = updated.(digitalOceanProvisionModel)
	if command != nil || result.screen != provisionScreenDone {
		t.Fatalf("non-enter done key changed state: %+v", result)
	}
}

func TestProvisionTUIHappyPathUploadsKeyAndSavesProfile(t *testing.T) {
	model, store, fake, restore := newProvisionHappyPathFixture(t)
	defer restore()
	model = loadProvisionCatalog(t, model)
	model = completeProvisionSelections(t, model)
	model = confirmProvisionCreate(t, model)
	assertProvisionCreatedWithUploadedKey(t, model, store, fake)
}

func newProvisionHappyPathFixture(t *testing.T) (digitalOceanProvisionModel, ProfileStore, *recordingCloudProvider, func()) {
	t.Helper()
	privateKeyPath := writeProvisionTestKeypair(t)
	fake := &recordingCloudProvider{
		catalog: provisionTestCatalog(),
		createdKey: cloudSSHKey{
			ID:          99,
			Name:        provisionTestUploadedKeyName,
			Fingerprint: "99:aa",
		},
		created: server{
			ID:        "84",
			Name:      provisionTestDropletName,
			IPv4:      provisionTestIPv4,
			Region:    "nyc3",
			Size:      provisionTestSize,
			Image:     provisionTestImage,
			CreatedAt: "2026-06-30T12:00:00Z",
		},
	}
	restore := replaceProvisionCloudProvider(fake)

	store := newFileProfileStore(t.TempDir())
	model := newDigitalOceanProvisionModel(context.Background(), store)
	model.inputs[0].SetValue("token")
	model.inputs[1].SetValue(provisionTestDropletName)
	model.inputs[2].SetValue(privateKeyPath)
	model.focus = 2
	model.inputs[2].Focus()
	if !containsAll(model.View().Content, "Provision a DigitalOcean VPS", "token") {
		t.Fatalf("input view missing expected guidance:\n%s", model.View().Content)
	}
	return model, store, fake, restore
}

func loadProvisionCatalog(t *testing.T, model digitalOceanProvisionModel) digitalOceanProvisionModel {
	t.Helper()
	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenLoading || command == nil {
		t.Fatalf("enter should load catalog: screen=%d command=%v err=%q", model.screen, command, model.err)
	}
	if !strings.Contains(model.View().Content, "Loading DigitalOcean") {
		t.Fatalf("loading view missing catalog message:\n%s", model.View().Content)
	}
	updated, _ = model.Update(command())
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenRegion || model.localPublicKey == "" || model.localKeyFingerprint == "" {
		t.Fatalf("catalog did not open region selection: screen=%d public=%q fingerprint=%q err=%q", model.screen, model.localPublicKey, model.localKeyFingerprint, model.err)
	}
	return model
}

func completeProvisionSelections(t *testing.T, model digitalOceanProvisionModel) digitalOceanProvisionModel {
	t.Helper()
	for _, step := range []struct {
		screen provisionScreen
		text   string
	}{
		{provisionScreenRegion, "Choose a region"},
		{provisionScreenSize, "Choose a size"},
		{provisionScreenImage, "Choose an Ubuntu image"},
		{provisionScreenSSHKey, "Choose an existing provider key"},
	} {
		if model.screen != step.screen {
			t.Fatalf("expected screen %d, got %d", step.screen, model.screen)
		}
		if !strings.Contains(model.View().Content, step.text) {
			t.Fatalf("view for screen %d missing %q:\n%s", step.screen, step.text, model.View().Content)
		}
		updated, _ := model.Update(keyRunes("j"))
		model = updated.(digitalOceanProvisionModel)
		updated, _ = model.Update(keyCode(tea.KeyEnter))
		model = updated.(digitalOceanProvisionModel)
	}
	if model.screen != provisionScreenReview || !model.selectedKey.Upload {
		t.Fatalf("SSH key selection should review an upload choice: screen=%d key=%+v", model.screen, model.selectedKey)
	}
	if !containsAll(model.View().Content, "Review billable Droplet", "$6.00/month", "upload local public key") {
		t.Fatalf("review missing billable Droplet details:\n%s", model.View().Content)
	}
	return model
}

func confirmProvisionCreate(t *testing.T, model digitalOceanProvisionModel) digitalOceanProvisionModel {
	t.Helper()
	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "type \"provision "+provisionTestDropletName+"\"") {
		t.Fatalf("wrong confirmation was not rejected: %q", model.err)
	}
	model.confirmInput.SetValue(provisionConfirmPhrase(provisionTestDropletName))
	updated, command = model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenCreating || command == nil {
		t.Fatalf("confirmed review should create: screen=%d command=%v err=%q", model.screen, command, model.err)
	}
	if !strings.Contains(model.View().Content, "Creating the Droplet") {
		t.Fatalf("creating view missing wait message:\n%s", model.View().Content)
	}
	updated, command = model.Update(command())
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenSavingProfile || command == nil {
		t.Fatalf("created Droplet should begin local profile save: screen=%d command=%v err=%q", model.screen, command, model.err)
	}
	updated, _ = model.Update(command())
	model = updated.(digitalOceanProvisionModel)
	if !model.done || model.screen != provisionScreenDone {
		t.Fatalf("create did not finish: screen=%d done=%v err=%q", model.screen, model.done, model.err)
	}
	if !strings.Contains(model.View().Content, provisionTestIPv4) {
		t.Fatalf("done view missing created IP:\n%s", model.View().Content)
	}
	return model
}

func assertProvisionCreatedWithUploadedKey(t *testing.T, model digitalOceanProvisionModel, store ProfileStore, fake *recordingCloudProvider) {
	t.Helper()
	if fake.createdKeyName != provisionTestUploadedKeyName || fake.createdConfig.SSHKey != "99" {
		t.Fatalf("upload key should be used for droplet create: key=%q config=%+v", fake.createdKeyName, fake.createdConfig)
	}
	loaded, _, err := store.Load(model.createdProfile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cloud == nil || loaded.Cloud.ResourceID != "84" || loaded.Cloud.PriceMonthly != 6 {
		t.Fatalf("created profile missing cloud metadata: %+v", loaded)
	}
}

func TestProvisionTUIUsesExistingSSHKeyReference(t *testing.T) {
	privateKeyPath := writeProvisionTestKeypair(t)
	publicKey, fingerprint, err := readProvisionPublicKey(privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingCloudProvider{
		catalog: provisionTestCatalog(),
		created: server{ID: "85", Name: provisionTestExistingKeyName, IPv4: "198.51.100.85"},
	}
	fake.catalog.SSHKeys = []cloudSSHKey{{
		ID:          12,
		Name:        "matching",
		Fingerprint: fingerprint,
		PublicKey:   publicKey + " comment ignored",
	}}
	restore := replaceProvisionCloudProvider(fake)
	defer restore()

	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.inputs[0].SetValue("token")
	model.inputs[1].SetValue(provisionTestExistingKeyName)
	model.inputs[2].SetValue(privateKeyPath)
	config, err := model.inputConfig()
	if err != nil {
		t.Fatal(err)
	}
	catalogMessage := model.loadCatalog(context.Background(), config)().(provisionCatalogMsg)
	updated, _ := model.Update(catalogMessage)
	model = updated.(digitalOceanProvisionModel)
	for step := 0; model.screen != provisionScreenReview && step < 4; step++ {
		updated, _ = model.Update(keyCode(tea.KeyEnter))
		model = updated.(digitalOceanProvisionModel)
	}
	if model.screen != provisionScreenReview {
		t.Fatalf("existing key flow did not reach review: screen=%d err=%q", model.screen, model.err)
	}
	if model.selectedKey.Upload || model.selectedKeyReference() != "12" {
		t.Fatalf("existing provider key should be selected: %+v reference=%q", model.selectedKey, model.selectedKeyReference())
	}
	model.confirmInput.SetValue(provisionConfirmPhrase(provisionTestExistingKeyName))
	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenCreating {
		t.Fatalf("existing key review should begin creation: screen=%d err=%q", model.screen, model.err)
	}
	message := command().(provisionCreateMsg)
	if message.err != nil {
		t.Fatal(message.err)
	}
	if fake.createdKeyName != "" || fake.createdConfig.SSHKey != "12" {
		t.Fatalf("existing key should not be uploaded: key=%q config=%+v", fake.createdKeyName, fake.createdConfig)
	}
}

func TestProvisionTUIValidationErrorsAndBackNavigation(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	if model.Init() == nil {
		t.Fatal("provision model should start text input blinking")
	}
	item := provisionListItem{title: "title", description: "description"}
	if item.Title() != "title" || item.Description() != "description" || item.FilterValue() != "title description" {
		t.Fatalf("unexpected list item labels: %+v", item)
	}
	assertProvisionInputNavigation(t, model)
	assertProvisionInputConfigValidation(t)
	assertProvisionGoBackScreens(t)
	assertProvisionHelpText(t, model)
	assertProvisionSelectionQuit(t, model)
}

func assertProvisionInputNavigation(t *testing.T, model digitalOceanProvisionModel) {
	t.Helper()
	updated, _ := model.Update(keyRunes("x"))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.inputs[0].Value(), "x") {
		t.Fatalf("text input did not accept typed token: %q", model.inputs[0].Value())
	}
	updated, _ = model.Update(keyCode(tea.KeyTab))
	model = updated.(digitalOceanProvisionModel)
	if model.focus != 1 {
		t.Fatalf("tab did not advance focus: %d", model.focus)
	}
	updated, _ = model.Update(keyMod(tea.KeyTab, tea.ModShift))
	model = updated.(digitalOceanProvisionModel)
	if model.focus != 0 {
		t.Fatalf("shift-tab did not move focus back: %d", model.focus)
	}
}

func assertProvisionInputConfigValidation(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name   string
		values []string
		err    string
	}{
		{"token", []string{"", "droplet", "/tmp/key"}, "DigitalOcean API token is required"},
		{"name", []string{"token", "", "/tmp/key"}, "Droplet name is required"},
		{"key", []string{"token", "droplet", ""}, "Servestead private key path is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
			for index, value := range tc.values {
				model.inputs[index].SetValue(value)
			}
			_, err := model.inputConfig()
			if err == nil || err.Error() != tc.err {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func assertProvisionGoBackScreens(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		from provisionScreen
		to   provisionScreen
	}{
		{provisionScreenInput, provisionScreenInput},
		{provisionScreenLoading, provisionScreenInput},
		{provisionScreenRegion, provisionScreenInput},
		{provisionScreenSize, provisionScreenRegion},
		{provisionScreenImage, provisionScreenSize},
		{provisionScreenSSHKey, provisionScreenImage},
		{provisionScreenReview, provisionScreenSSHKey},
		{provisionScreenCreating, provisionScreenCreating},
		{provisionScreenDone, provisionScreenDone},
	} {
		model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
		model.screen = tc.from
		model.goBack()
		if model.screen != tc.to {
			t.Fatalf("goBack from %d went to %d, want %d", tc.from, model.screen, tc.to)
		}
	}
}

func assertProvisionHelpText(t *testing.T, model digitalOceanProvisionModel) {
	t.Helper()
	for _, screen := range []provisionScreen{
		provisionScreenInput,
		provisionScreenLoading,
		provisionScreenRegion,
		provisionScreenSize,
		provisionScreenImage,
		provisionScreenSSHKey,
		provisionScreenReview,
		provisionScreenCreating,
		provisionScreenSavingProfile,
		provisionScreenSaveRecovery,
		provisionScreenDone,
		provisionScreen(99),
	} {
		model.screen = screen
		if model.provisionHelpText() == "" {
			t.Fatalf("empty help for screen %d", screen)
		}
	}
}

func assertProvisionSelectionQuit(t *testing.T, model digitalOceanProvisionModel) {
	t.Helper()
	model.screen = provisionScreenRegion
	updated, _ := model.Update(keyRunes("q"))
	if !updated.(digitalOceanProvisionModel).cancelled {
		t.Fatal("q should cancel selection screens")
	}
}

func TestProvisionTUIHandlesCatalogAndSelectionErrors(t *testing.T) {
	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	updated, _ := model.Update(provisionCatalogMsg{err: errors.New("token rejected")})
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenInput || !strings.Contains(model.err, "token rejected") {
		t.Fatalf("catalog error was not shown: %+v", model)
	}

	updated, _ = model.Update(provisionCatalogMsg{catalog: cloudCatalog{}})
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenInput || !strings.Contains(model.err, "no available regions") {
		t.Fatalf("empty regions were not rejected: screen=%d err=%q", model.screen, model.err)
	}

	model.catalog = cloudCatalog{Regions: []cloudRegion{{Slug: "nyc3", Name: provisionTestRegionName, Available: true}}}
	model.regionList = newProvisionList("regions", []list.Item{listItemForTest{}})
	updated, _ = model.updateRegion(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenInput {
		t.Fatalf("non-provision region item should be ignored: screen=%d", model.screen)
	}
	model.regionList = newProvisionList("regions", []list.Item{provisionListItem{index: 10, title: "stale"}})
	updated, _ = model.updateRegion(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "selected region is no longer available") {
		t.Fatalf("stale region selection was not rejected: %q", model.err)
	}

	model.catalog = cloudCatalog{Regions: []cloudRegion{{Slug: "nyc3", Name: provisionTestRegionName, Available: true}}}
	model.regionList = newProvisionList("regions", provisionRegionItems(model.catalog))
	updated, _ = model.updateRegion(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "no available sizes") {
		t.Fatalf("region with no sizes was not rejected: %q", model.err)
	}

	model.catalog = cloudCatalog{Sizes: []cloudSize{{Slug: provisionTestSize, Regions: []string{"nyc3"}, Available: true, PriceMonthly: 6, DiskGB: 25}}}
	model.selectedRegion = cloudRegion{Slug: "nyc3"}
	model.sizeList = newProvisionList("sizes", []list.Item{provisionListItem{index: 9, title: "stale"}})
	updated, _ = model.updateSize(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "selected size is no longer available") {
		t.Fatalf("stale size selection was not rejected: %q", model.err)
	}

	model.catalog = cloudCatalog{Sizes: []cloudSize{{Slug: provisionTestSize, Regions: []string{"nyc3"}, Available: true, PriceMonthly: 6, DiskGB: 25}}}
	model.sizeList = newProvisionList("sizes", provisionSizeItems(model.catalog, "nyc3"))
	updated, _ = model.updateSize(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "no Ubuntu images") {
		t.Fatalf("size with no images was not rejected: %q", model.err)
	}

	model.catalog = cloudCatalog{Images: []cloudImage{{Slug: provisionTestImage, Regions: []string{"nyc3"}, MinDiskGB: 7}}}
	model.selectedRegion = cloudRegion{Slug: "nyc3"}
	model.selectedSize = cloudSize{DiskGB: 25}
	model.imageList = newProvisionList("images", []list.Item{provisionListItem{index: 9, title: "stale"}})
	updated, _ = model.updateImage(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "selected image is no longer available") {
		t.Fatalf("stale image selection was not rejected: %q", model.err)
	}

	model.catalog = cloudCatalog{}
	model.keyList = newProvisionList("keys", []list.Item{provisionListItem{index: 9, title: "stale"}})
	updated, _ = model.updateSSHKey(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if !strings.Contains(model.err, "selected SSH key is no longer available") {
		t.Fatalf("stale SSH key selection was not rejected: %q", model.err)
	}
}

func TestProvisionCatalogHelpersFilterSortAndRenderItems(t *testing.T) {
	catalog := cloudCatalog{
		Regions: []cloudRegion{
			{Slug: "sfo3", Name: "San Francisco 3", Available: true, Sizes: []string{"s-2vcpu-2gb"}},
			{Slug: defaultDigitalOceanRegion, Name: provisionTestRegionName, Available: true, Sizes: []string{provisionTestSize}},
			{Slug: "ams3", Name: "Amsterdam 3", Available: false},
		},
		Sizes: []cloudSize{
			{Slug: "s-2vcpu-2gb", VCPUs: 2, MemoryMB: 2048, DiskGB: 50, TransferTB: 2, PriceMonthly: 12, PriceHourly: 0.01786, Regions: []string{"sfo3"}, Available: true},
			{Slug: defaultDigitalOceanSize, VCPUs: 1, MemoryMB: 1024, DiskGB: 25, TransferTB: 1, PriceMonthly: 6, PriceHourly: 0.00893, Regions: []string{"nyc3", "sfo3"}, Available: true},
			{Slug: "free", Regions: []string{"nyc3"}, Available: true},
			{Slug: "unavailable", Regions: []string{"nyc3"}, Available: false, PriceMonthly: 1},
		},
		Images: []cloudImage{
			{Slug: "ubuntu-22-04-x64", Distribution: "Ubuntu", Regions: []string{"nyc3"}, MinDiskGB: 7},
			{Slug: defaultDigitalOceanImage, Distribution: "Ubuntu", Regions: []string{"nyc3"}, MinDiskGB: 7},
			{Slug: "too-large", Distribution: "Ubuntu", Regions: []string{"nyc3"}, MinDiskGB: 100},
			{Slug: "", Distribution: "Ubuntu", Regions: []string{"nyc3"}, MinDiskGB: 7},
			{Slug: "wrong-region", Distribution: "Ubuntu", Regions: []string{"sfo3"}, MinDiskGB: 7},
		},
		SSHKeys: []cloudSSHKey{
			{ID: 2, Name: "z-other", Fingerprint: "bb:bb"},
			{ID: 1, Name: "a-match", Fingerprint: "aa:aa", PublicKey: provisionTestLocalPublicKey},
		},
	}

	regions := provisionAvailableRegions(catalog)
	if len(regions) != 2 || regions[0].Slug != defaultDigitalOceanRegion {
		t.Fatalf("regions were not filtered and sorted: %+v", regions)
	}
	sizes := provisionAvailableSizes(catalog, "nyc3")
	if len(sizes) != 1 || sizes[0].Slug != defaultDigitalOceanSize {
		t.Fatalf("sizes were not filtered by region/price: %+v", sizes)
	}
	images := provisionAvailableImages(catalog, "nyc3", 25)
	if len(images) != 2 || images[0].Slug != defaultDigitalOceanImage {
		t.Fatalf("images were not filtered and sorted: %+v", images)
	}
	if len(provisionRegionItems(catalog)) != 2 ||
		len(provisionSizeItems(catalog, "nyc3")) != 1 ||
		len(provisionImageItems(catalog, "nyc3", 25)) != 2 ||
		len(provisionSSHKeyItems(catalog, provisionTestLocalPublicKey+" comment", "aa:aa")) != 2 {
		t.Fatal("provision list item builders did not include expected rows")
	}

	imageList := newProvisionList("images", provisionImageItems(catalog, "nyc3", 25))
	imageList.Select(1)
	provisionSelectDefaultImage(&imageList)
	selected := imageList.SelectedItem().(provisionListItem)
	if !strings.HasPrefix(selected.title, defaultDigitalOceanImage) {
		t.Fatalf("default image was not selected: %+v", selected)
	}
}

func TestProvisionPublicKeyAndNameHelpers(t *testing.T) {
	privateKeyPath := writeProvisionTestKeypair(t)
	publicKey, fingerprint, err := readProvisionPublicKey(privateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") || fingerprint == "" {
		t.Fatalf("unexpected public key/fingerprint: %q %q", publicKey, fingerprint)
	}

	if _, _, err := readProvisionPublicKey(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing public key should fail")
	}
	invalidKey := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(invalidKey+".pub", []byte("not an authorized key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readProvisionPublicKey(invalidKey); err == nil {
		t.Fatal("invalid public key should fail")
	}
	if provisionSSHKeyName("/tmp/id_ed25519") != provisionTestUploadedKeyName || provisionSSHKeyName(".") != "servestead-key" {
		t.Fatal("unexpected SSH key name generation")
	}
	if normalizeAuthorizedKey("ssh-ed25519 AAAA comment") != "ssh-ed25519 AAAA" || normalizeAuthorizedKey("raw") != "raw" {
		t.Fatal("authorized key normalization changed")
	}
	if provisionConfirmPhrase("  named  ") != "provision named" {
		t.Fatal("confirmation phrase should trim name")
	}
}

func TestProvisionCreateDropletReportsProviderErrorsAndPreservesProfileData(t *testing.T) {
	privateKeyPath := writeProvisionTestKeypair(t)
	config := provisionInputConfig{Token: "token", Name: provisionTestDropletName, PrivateKeyPath: privateKeyPath}
	model := digitalOceanProvisionModel{
		ctx:            context.Background(),
		store:          newFileProfileStore(t.TempDir()),
		selectedRegion: cloudRegion{Slug: "nyc3"},
		selectedSize:   cloudSize{Slug: provisionTestSize},
		selectedImage:  cloudImage{Slug: provisionTestImage},
		selectedKey:    provisionSSHKeyChoice{Upload: true},
		localPublicKey: "ssh-ed25519 AAAA test",
	}

	fake := &recordingCloudProvider{keyErr: errors.New("key rejected")}
	restore := replaceProvisionCloudProvider(fake)
	message := model.createDroplet(context.Background(), config)().(provisionCreateMsg)
	restore()
	if message.err == nil || !strings.Contains(message.err.Error(), "upload DigitalOcean SSH key") {
		t.Fatalf("unexpected key upload error: %v", message.err)
	}
	if !message.remoteOutcomeUnknown {
		t.Fatal("transport-like SSH key upload error was treated as safe to retry")
	}

	fake = &recordingCloudProvider{createdKey: cloudSSHKey{ID: 5}, createErr: errors.New("quota exceeded")}
	restore = replaceProvisionCloudProvider(fake)
	message = model.createDroplet(context.Background(), config)().(provisionCreateMsg)
	restore()
	if message.err == nil || !strings.Contains(message.err.Error(), "create DigitalOcean Droplet") {
		t.Fatalf("unexpected droplet create error: %v", message.err)
	}
	if message.profile.ID != "" {
		t.Fatalf("provider rejection without a Droplet ID produced a recovery profile: %+v", message.profile)
	}
	if !message.remoteOutcomeUnknown {
		t.Fatal("transport-like create error without a Droplet ID was treated as safe to retry")
	}
	updated, quitCommand := model.Update(message)
	unknown := updated.(digitalOceanProvisionModel)
	if quitCommand == nil || !unknown.remoteOutcomeUnknown || !strings.Contains(unknown.err, "check DigitalOcean") {
		t.Fatalf("ambiguous create result did not stop retry: command=%v model=%+v", quitCommand, unknown)
	}

	fake = &recordingCloudProvider{
		created:   server{ID: "84", Name: provisionTestDropletName},
		createErr: context.Canceled,
	}
	restore = replaceProvisionCloudProvider(fake)
	message = model.createDroplet(context.Background(), config)().(provisionCreateMsg)
	restore()
	if !errors.Is(message.err, context.Canceled) || message.profile.Cloud == nil || message.profile.Cloud.ResourceID != "84" || message.profile.IP != "" {
		t.Fatalf("IPv4 wait cancellation lost the created Droplet: %+v", message)
	}
	if !strings.HasPrefix(message.profile.ID, "digitalocean-84-") {
		t.Fatalf("recovery profile ID is not based on the Droplet identity: %q", message.profile.ID)
	}

	model.selectedKey = provisionSSHKeyChoice{Key: cloudSSHKey{Fingerprint: "aa:bb"}}
	if model.selectedKeyReference() != "aa:bb" {
		t.Fatalf("fingerprint should be used when key has no ID: %q", model.selectedKeyReference())
	}
	fake = &recordingCloudProvider{created: server{ID: "84", IPv4: provisionTestIPv4}}
	restore = replaceProvisionCloudProvider(fake)
	message = model.createDroplet(context.Background(), config)().(provisionCreateMsg)
	restore()
	if message.err != nil || message.profile.Cloud == nil || message.profile.Cloud.ResourceID != "84" {
		t.Fatalf("provider success should preserve unsaved profile data: message=%+v", message)
	}
}

func TestProvisionSaveFailureRetriesLocallyWithoutCreatingAnotherDroplet(t *testing.T) {
	model, _, fake, restore := newProvisionHappyPathFixture(t)
	defer restore()
	store := &failOnceCreateProfileStore{
		ProfileStore: newFileProfileStore(t.TempDir()),
		err:          errors.New("disk full"),
	}
	model.store = store
	model = loadProvisionCatalog(t, model)
	model = completeProvisionSelections(t, model)
	model, createCommand := startProvisionCreateWithoutDuplicates(t, model)
	model, saveCommand := applyProvisionCreateWithoutDuplicates(t, model, createCommand, fake)
	model = applyFailedProvisionSave(t, model, saveCommand)
	firstProfileID := model.pendingProfile.ID
	model = retryProvisionSaveLocally(t, model, fake)

	if store.calls != 2 || len(store.attempts) != 2 || store.attempts[0].ID != store.attempts[1].ID {
		t.Fatalf("local save retries did not preserve identity: calls=%d attempts=%+v", store.calls, store.attempts)
	}
	if !model.done || model.screen != provisionScreenDone || model.createdProfile.ID != firstProfileID {
		t.Fatalf("local retry did not finish with the preserved profile: screen=%d profile=%+v", model.screen, model.createdProfile)
	}
	if fake.createCalls != 1 || fake.keyCalls != 1 {
		t.Fatalf("successful local retry duplicated provider work: createCalls=%d keyCalls=%d", fake.createCalls, fake.keyCalls)
	}
}

func TestProvisionSaveRecoveryLocksExitUntilDropletIsRecorded(t *testing.T) {
	model := digitalOceanProvisionModel{
		screen: provisionScreenSaveRecovery,
		pendingProfile: Profile{
			ID: "digitalocean-84-recovery",
			IP: provisionTestIPv4,
			Cloud: &ProfileCloud{
				Provider:   digitalOceanProviderName,
				ResourceID: "84",
			},
		},
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyCtrl('c'), keyCode(tea.KeyEsc)} {
		updated, command := model.Update(key)
		result := updated.(digitalOceanProvisionModel)
		if command != nil || result.cancelled || result.screen != provisionScreenSaveRecovery {
			t.Fatalf("%s escaped save recovery: command=%v model=%+v", key.String(), command, result)
		}
		if !strings.Contains(result.err, "Droplet exists") {
			t.Fatalf("%s did not explain the recovery lock: %q", key.String(), result.err)
		}
	}
}

func TestProvisionPartialCreateSavesRecoveryProfileWithoutRetryingProvider(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	model := newDigitalOceanProvisionModel(context.Background(), store)
	model.screen = provisionScreenCreating
	model.cancelling = true
	partial := newProvisionedDigitalOceanProfile(
		provisionInputConfig{Name: provisionTestDropletName, PrivateKeyPath: "/tmp/servestead"},
		model,
		server{ID: "84", Name: provisionTestDropletName},
	)

	updated, saveCommand := model.Update(provisionCreateMsg{profile: partial, err: context.Canceled})
	model = updated.(digitalOceanProvisionModel)
	if saveCommand == nil || model.cancelled || model.screen != provisionScreenSavingProfile || model.pendingProfile.Cloud.ResourceID != "84" {
		t.Fatalf("partial create did not enter local recovery save: command=%v model=%+v", saveCommand, model)
	}
	if !strings.Contains(model.notice, "created Droplet 84") || !strings.Contains(model.notice, "without calling DigitalOcean again") {
		t.Fatalf("partial create recovery notice = %q", model.notice)
	}

	updated, command := model.Update(saveCommand())
	model = updated.(digitalOceanProvisionModel)
	if command != nil || !model.done || model.createdProfile.Cloud.ResourceID != "84" || model.createdProfile.IP != "" {
		t.Fatalf("partial recovery profile was not saved: command=%v model=%+v", command, model)
	}
	profiles, err := store.List()
	if err != nil || len(profiles) != 1 || profiles[0].ID != model.createdProfile.ID {
		t.Fatalf("saved recovery profile list = %+v, err=%v", profiles, err)
	}
}

func startProvisionCreateWithoutDuplicates(t *testing.T, model digitalOceanProvisionModel) (digitalOceanProvisionModel, tea.Cmd) {
	t.Helper()
	model.confirmInput.SetValue(provisionConfirmPhrase(provisionTestDropletName))
	updated, createCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenCreating || createCommand == nil {
		t.Fatalf("confirmed review did not start provider creation: screen=%d command=%v", model.screen, createCommand)
	}
	updated, duplicateCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if duplicateCommand != nil || model.screen != provisionScreenCreating {
		t.Fatalf("enter while creating should not start a duplicate action: screen=%d command=%v", model.screen, duplicateCommand)
	}
	return model, createCommand
}

func applyProvisionCreateWithoutDuplicates(t *testing.T, model digitalOceanProvisionModel, createCommand tea.Cmd, fake *recordingCloudProvider) (digitalOceanProvisionModel, tea.Cmd) {
	t.Helper()
	updated, saveCommand := model.Update(createCommand())
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenSavingProfile || saveCommand == nil || fake.createCalls != 1 {
		t.Fatalf("provider success did not start one local save: screen=%d command=%v createCalls=%d", model.screen, saveCommand, fake.createCalls)
	}
	updated, duplicateCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if duplicateCommand != nil || model.screen != provisionScreenSavingProfile {
		t.Fatalf("enter while saving should not start a duplicate action: screen=%d command=%v", model.screen, duplicateCommand)
	}
	return model, saveCommand
}

func applyFailedProvisionSave(t *testing.T, model digitalOceanProvisionModel, saveCommand tea.Cmd) digitalOceanProvisionModel {
	t.Helper()
	updated, _ := model.Update(saveCommand())
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenSaveRecovery || model.pendingProfile.Cloud == nil || model.pendingProfile.Cloud.ResourceID != "84" || model.pendingProfile.IP != provisionTestIPv4 {
		t.Fatalf("save failure did not preserve created resource data: screen=%d profile=%+v err=%q", model.screen, model.pendingProfile, model.err)
	}
	if !containsAll(model.View().Content, "Droplet ID: 84", provisionTestIPv4, "DigitalOcean will not be called again") {
		t.Fatalf("save recovery view missing resource and retry guidance:\n%s", model.View().Content)
	}
	return model
}

func retryProvisionSaveLocally(t *testing.T, model digitalOceanProvisionModel, fake *recordingCloudProvider) digitalOceanProvisionModel {
	t.Helper()
	updated, retryCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenSavingProfile || retryCommand == nil {
		t.Fatalf("recovery enter did not retry local persistence: screen=%d command=%v", model.screen, retryCommand)
	}
	if fake.createCalls != 1 || fake.keyCalls != 1 {
		t.Fatalf("local retry called DigitalOcean again: createCalls=%d keyCalls=%d", fake.createCalls, fake.keyCalls)
	}
	updated, _ = model.Update(retryCommand())
	return updated.(digitalOceanProvisionModel)
}

func TestProvisionCancellationReachesDelayedProvider(t *testing.T) {
	fake := newDelayedCloudProvider("create")
	restore := replaceProvisionCloudProvider(fake)
	defer restore()

	model := newDigitalOceanProvisionModel(context.Background(), newFileProfileStore(t.TempDir()))
	model.inputs[0].SetValue("token")
	model.inputs[1].SetValue(provisionTestDropletName)
	model.inputs[2].SetValue("/tmp/servestead")
	model.selectedRegion = cloudRegion{Slug: "nyc3"}
	model.selectedSize = cloudSize{Slug: provisionTestSize}
	model.selectedImage = cloudImage{Slug: provisionTestImage}
	model.selectedKey = provisionSSHKeyChoice{Key: cloudSSHKey{ID: 12}}
	model.confirmInput.SetValue(provisionConfirmPhrase(provisionTestDropletName))
	model.screen = provisionScreenReview

	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if model.screen != provisionScreenCreating || command == nil {
		t.Fatalf("creation did not start: screen=%d command=%v", model.screen, command)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()
	waitForTestSignal(t, fake.started, "provider create start")

	updated, duplicateCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(digitalOceanProvisionModel)
	if duplicateCommand != nil || fake.calls != 1 {
		t.Fatalf("busy creation accepted a duplicate action: command=%v calls=%d", duplicateCommand, fake.calls)
	}
	updated, cancelCommand := model.Update(keyRunes("q"))
	model = updated.(digitalOceanProvisionModel)
	if cancelCommand != nil || !model.cancelling || model.cancelled {
		t.Fatalf("q should request provider cancellation without exiting early: command=%v model=%+v", cancelCommand, model)
	}
	if !strings.Contains(model.View().Content, "Cancellation requested") {
		t.Fatalf("cancelling view missing acknowledgement:\n%s", model.View().Content)
	}
	waitForTestSignal(t, fake.cancelled, "provider cancellation")

	updated, quitCommand := model.Update(<-result)
	model = updated.(digitalOceanProvisionModel)
	if quitCommand == nil || !model.cancelled || model.cancelling || !model.remoteOutcomeUnknown || !strings.Contains(model.err, "remote outcome is unknown") {
		t.Fatalf("acknowledged cancellation did not exit cleanly: command=%v model=%+v", quitCommand, model)
	}
}

func TestProvisionSSHKeyChoicesPreferExistingMatch(t *testing.T) {
	catalog := cloudCatalog{SSHKeys: []cloudSSHKey{
		{ID: 2, Name: "other", Fingerprint: "bb:bb", PublicKey: "ssh-ed25519 BBBB other"},
		{ID: 1, Name: "servestead", Fingerprint: "aa:aa", PublicKey: provisionTestLocalPublicKey},
	}}

	choices := provisionSSHKeyChoices(catalog, provisionTestLocalPublicKey, "aa:aa")
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
		selectedSize:   cloudSize{Slug: provisionTestSize, PriceMonthly: 6, PriceHourly: 0.00893},
		selectedImage:  cloudImage{Slug: provisionTestImage},
	}
	profile, err := saveProvisionedDigitalOceanProfile(store, provisionInputConfig{
		Name:           provisionTestDropletName,
		PrivateKeyPath: "/tmp/servestead",
	}, model, server{
		ID:        "84",
		Name:      provisionTestDropletName,
		IPv4:      provisionTestIPv4,
		CreatedAt: "2026-06-30T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.IP != provisionTestIPv4 || loaded.Cloud == nil || loaded.Cloud.ResourceID != "84" {
		t.Fatalf("provisioned profile did not persist cloud metadata: %+v", loaded)
	}
	if loaded.Cloud.Provider != digitalOceanProviderName || loaded.Cloud.Region != "nyc3" ||
		loaded.Cloud.Size != provisionTestSize || loaded.Cloud.Image != provisionTestImage ||
		loaded.Cloud.PriceMonthly != 6 {
		t.Fatalf("unexpected cloud metadata: %+v", loaded.Cloud)
	}
}

func TestProfileCloudScreensConfirmActionsAndRenderStatus(t *testing.T) {
	model, fake, restore := openProfileCloudActions(t)
	defer restore()
	assertProfileCloudHelpBindings(t)
	model = startProfileCloudRestart(t, model)
	model = validateProfileCloudRestartConfirmation(t, model)
	model = runProfileCloudRestart(t, model, fake)
	updated, _ := model.Update(keyCode(tea.KeyEsc))
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenDashboard {
		t.Fatalf("esc should return from cloud actions to dashboard: %d", model.screen)
	}
}

func openProfileCloudActions(t *testing.T) (profileSetupModel, *recordingCloudProvider, func()) {
	t.Helper()
	fake := &recordingCloudProvider{}
	restore := replaceProvisionCloudProvider(fake)

	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model.screen = profileSetupScreenDashboard
	updated, _ := model.Update(keyRunes("o"))
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloud || model.err != "" {
		t.Fatalf("cloud shortcut did not open action screen: screen=%d err=%q", model.screen, model.err)
	}
	if !containsAll(model.View().Content, "DigitalOcean Droplet actions", "restart", "destroy") {
		t.Fatalf("cloud view missing actions:\n%s", model.View().Content)
	}
	return model, fake, restore
}

func assertProfileCloudHelpBindings(t *testing.T) {
	t.Helper()
	if len((profileSetupHelp{screen: profileSetupScreenDashboard, hasProfile: true, hasCloud: true}).ShortHelp()) == 0 ||
		len((profileSetupHelp{screen: profileSetupScreenCloud}).ShortHelp()) == 0 ||
		len((profileSetupHelp{screen: profileSetupScreenCloudConfirm}).ShortHelp()) == 0 ||
		len((profileSetupHelp{screen: profileSetupScreenCloudRunning}).ShortHelp()) == 0 ||
		len((profileSetupHelp{screen: profileSetupScreenCloudSaveRecovery}).ShortHelp()) == 0 {
		t.Fatal("cloud help bindings should be available")
	}
}

func startProfileCloudRestart(t *testing.T, model profileSetupModel) profileSetupModel {
	t.Helper()
	updated, _ := model.Update(keyRunes("r"))
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloudConfirm || model.cloudAction != "restart" || model.focus != 0 {
		t.Fatalf("restart should request token first: screen=%d action=%q focus=%d", model.screen, model.cloudAction, model.focus)
	}
	if !strings.Contains(model.profileCloudConfirmView(), "Confirm DigitalOcean restart") {
		t.Fatalf("confirm view missing action:\n%s", model.profileCloudConfirmView())
	}
	return model
}

func validateProfileCloudRestartConfirmation(t *testing.T, model profileSetupModel) profileSetupModel {
	t.Helper()
	updated, _ := model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if !strings.Contains(model.err, "DigitalOcean API token is required") {
		t.Fatalf("blank token was not rejected: %q", model.err)
	}
	model.cloudTokenInput.SetValue("token")
	updated, _ = model.Update(keyCode(tea.KeyTab))
	model = updated.(profileSetupModel)
	if model.focus != 1 {
		t.Fatalf("tab should focus confirmation: %d", model.focus)
	}
	updated, _ = model.Update(keyMod(tea.KeyTab, tea.ModShift))
	model = updated.(profileSetupModel)
	if model.focus != 0 {
		t.Fatalf("shift-tab should focus token: %d", model.focus)
	}
	model.focus = 1
	model.cloudConfirmInput.SetValue("wrong")
	updated, _ = model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if !strings.Contains(model.err, "type \"restart 84\"") {
		t.Fatalf("wrong confirmation was not rejected: %q", model.err)
	}
	return model
}

func TestProfileCloudConfirmationRemovesUnicodeFormatCharacters(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model = model.openProfileCloudConfirm("restart")
	model.cloudTokenInput.SetValue("local-test-token")
	model.cloudTokenInput.Blur()
	model.cloudConfirmInput.SetValue("")
	model.cloudConfirmInput.Focus()
	model.focus = 1
	updated, _ := model.updateProfileCloudConfirm(keyRunes("restart\u202e 84"))
	model = updated.(profileSetupModel)
	if model.cloudConfirmInput.Value() != "restart 84" || strings.Contains(model.cloudConfirmInput.View(), "\u202e") {
		t.Fatalf("cloud confirmation retained a Unicode format control: value=%q view=%q", model.cloudConfirmInput.Value(), model.cloudConfirmInput.View())
	}
}

func runProfileCloudRestart(t *testing.T, model profileSetupModel, fake *recordingCloudProvider) profileSetupModel {
	t.Helper()
	model.cloudConfirmInput.SetValue("restart 84")
	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloudRunning || command == nil {
		t.Fatalf("confirmed restart should run: screen=%d command=%v", model.screen, command)
	}
	if !strings.Contains(model.View().Content, "Running DigitalOcean restart action") {
		t.Fatalf("running view missing action:\n%s", model.View().Content)
	}
	updated, _ = model.Update(command())
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloud || fake.rebootedID != "84" || !strings.Contains(model.cloudNotice, "reboot") {
		t.Fatalf("restart result not applied: screen=%d fake=%+v notice=%q", model.screen, fake, model.cloudNotice)
	}
	return model
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
		IP:             provisionTestIPv4,
		PrivateKeyPath: "/tmp/servestead",
		Cloud: &ProfileCloud{
			Provider:   digitalOceanProviderName,
			ResourceID: "84",
			Name:       "production",
			Region:     "nyc3",
			Size:       provisionTestSize,
			Image:      provisionTestImage,
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
	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
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

func TestProfileCloudInactiveAndErrorPaths(t *testing.T) {
	destroyedAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	model := newProfileSetupModel([]profileChoice{{
		Profile: Profile{ID: provisionTestProfileID, Name: "production", IP: provisionTestIPv4, Cloud: &ProfileCloud{
			Provider:    digitalOceanProviderName,
			ResourceID:  "84",
			Name:        "production",
			Region:      "nyc3",
			Size:        provisionTestSize,
			Image:       provisionTestImage,
			DestroyedAt: &destroyedAt,
		}},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}})
	model.selectedIndex = 0
	if _, _, active := model.selectedProfileActiveCloud(); active {
		t.Fatal("destroyed cloud metadata should not be active")
	}
	model.screen = profileSetupScreenCloud
	updated, _ := model.Update(keyRunes("d"))
	model = updated.(profileSetupModel)
	if !strings.Contains(model.err, "active DigitalOcean Droplet") {
		t.Fatalf("inactive cloud action should be blocked: %q", model.err)
	}
	if !containsAll(model.profileCloudSummary(model.profiles[0].Profile), "destroyed", "Cost: $0.00/mo") {
		t.Fatalf("destroyed cloud summary missing status:\n%s", model.profileCloudSummary(model.profiles[0].Profile))
	}
	if !strings.Contains(model.profileCloudView(), "No active DigitalOcean") {
		t.Fatalf("inactive cloud view missing guidance:\n%s", model.profileCloudView())
	}
	if model.profileCloudConfirmView() != "No active DigitalOcean Droplet action is available for this profile." {
		t.Fatalf("unexpected inactive confirm view: %q", model.profileCloudConfirmView())
	}
	if model.profileCloudSummary(Profile{}) != "" {
		t.Fatal("profile without cloud should have empty cloud summary")
	}
	model.selectedIndex = -1
	if model.profileCloudView() != "No profile selected." {
		t.Fatalf("unexpected no-profile cloud view: %q", model.profileCloudView())
	}

	noCloud := newProfileSetupModel([]profileChoice{{Profile: Profile{ID: "profile-2"}, State: ProfileState{Runs: map[string]SetupRun{}}}})
	noCloud.selectedIndex = 0
	noCloud.screen = profileSetupScreenDashboard
	updated, _ = noCloud.Update(keyRunes("o"))
	noCloud = updated.(profileSetupModel)
	if !strings.Contains(noCloud.err, "no DigitalOcean Droplet metadata") {
		t.Fatalf("dashboard should reject cloud screen without metadata: %q", noCloud.err)
	}
}

func TestProfileCloudActionRejectsMissingMetadataAndAppliesError(t *testing.T) {
	model := newProfileSetupModel([]profileChoice{{Profile: Profile{ID: provisionTestProfileID}, State: ProfileState{Runs: map[string]SetupRun{}}}})
	model.selectedIndex = 0
	model.cloudAction = "restart"
	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "no cloud metadata") {
		t.Fatalf("missing cloud metadata error not returned: %v", message.err)
	}
	result := model.applyProfileCloudAction(profileCloudActionMsg{action: "restart", err: errors.New("provider down")})
	if result.screen != profileSetupScreenCloudConfirm || result.err != "provider down" {
		t.Fatalf("cloud action error was not applied: %+v", result)
	}
}

func TestProfileCloudViewSanitizesProviderErrorsAndMetadata(t *testing.T) {
	choice := activeCloudProfileChoice()
	choice.Profile.Name = "production\x1b]0;owned\a"
	choice.Profile.Cloud.Name = "droplet\x1b[31m-red"
	model := newProfileSetupModel([]profileChoice{choice})
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudConfirm
	providerError := "provider\x1b]0;owned\a down"
	model = model.applyProfileCloudAction(profileCloudActionMsg{action: "restart", err: errors.New(providerError)})

	view := model.View().Content
	if strings.Contains(view, "]0;owned") || strings.Contains(view, "\x1b[31m-red") {
		t.Fatalf("profile cloud view rendered untrusted terminal controls:\n%s", view)
	}
	if !strings.Contains(view, "provider down") || !strings.Contains(model.profileCloudSummary(choice.Profile), "droplet-red") {
		t.Fatalf("profile cloud view lost sanitized content:\n%s", view)
	}
}

func TestProfileCloudProviderErrors(t *testing.T) {
	originalProvider := newProvisionCloudProvider
	defer func() { newProvisionCloudProvider = originalProvider }()

	active := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	active.selectedIndex = 0
	active.cloudAction = "restart"
	newProvisionCloudProvider = func(string) cloudProvider {
		return &recordingCloudProvider{rebootErr: errors.New("reboot refused")}
	}
	message := active.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "restart DigitalOcean Droplet") {
		t.Fatalf("unexpected reboot error: %v", message.err)
	}

	active.cloudAction = "destroy"
	newProvisionCloudProvider = func(string) cloudProvider {
		return &recordingCloudProvider{destroyErr: errors.New("destroy refused")}
	}
	message = active.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "destroy DigitalOcean Droplet") {
		t.Fatalf("unexpected destroy error: %v", message.err)
	}
}

func TestProfileCloudDestroySaveFailureEntersRecovery(t *testing.T) {
	originalProvider := newProvisionCloudProvider
	defer func() { newProvisionCloudProvider = originalProvider }()

	active := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	active.selectedIndex = 0
	active.cloudAction = "destroy"
	baseStore := newFileProfileStore(t.TempDir())
	profile, err := baseStore.Create(active.profiles[0].Profile)
	if err != nil {
		t.Fatal(err)
	}
	active.profiles[0].Profile = profile
	active.profileStore = failingSaveProfileStore{ProfileStore: baseStore, err: errors.New("save failed")}
	newProvisionCloudProvider = func(string) cloudProvider { return &recordingCloudProvider{} }
	message := active.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "save failed") {
		t.Fatalf("unexpected save error: %v", message.err)
	}
	if !profileCloudMarkedDestroyed(message.profile) {
		t.Fatalf("save failure lost the successful remote destroy state: %+v", message.profile)
	}
	result := active.applyProfileCloudAction(message)
	if result.screen != profileSetupScreenCloudSaveRecovery || !strings.Contains(result.profileCloudSaveRecoveryView(), "DigitalOcean will not be called again") {
		t.Fatalf("destroy save failure did not enter local recovery: screen=%d view=%q", result.screen, result.profileCloudSaveRecoveryView())
	}
}

func TestProfileCloudUnknownActionAndDefaultNotice(t *testing.T) {
	originalProvider := newProvisionCloudProvider
	defer func() { newProvisionCloudProvider = originalProvider }()
	newProvisionCloudProvider = func(string) cloudProvider { return &recordingCloudProvider{} }

	active := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	active.selectedIndex = 0
	active.cloudAction = "resize"
	message := active.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err == nil || !strings.Contains(message.err.Error(), "unknown cloud action") {
		t.Fatalf("unexpected unknown action error: %v", message.err)
	}
	result := active.applyProfileCloudAction(profileCloudActionMsg{action: "resize", profile: active.profiles[0].Profile})
	if !strings.Contains(result.cloudNotice, "completed") {
		t.Fatalf("default action notice missing: %q", result.cloudNotice)
	}
	if profileCloudConfirmPhrase("destroy", nil) != "" {
		t.Fatal("nil cloud should have empty confirmation phrase")
	}
}

func TestProfileCloudAmbiguousMutationRequiresReconciliation(t *testing.T) {
	originalProvider := newProvisionCloudProvider
	defer func() { newProvisionCloudProvider = originalProvider }()

	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudRunning
	model.cloudAction = "destroy"
	newProvisionCloudProvider = func(string) cloudProvider {
		return &recordingCloudProvider{destroyErr: errors.New("connection reset")}
	}

	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if !message.remoteOutcomeUnknown {
		t.Fatalf("transport-like destroy error was treated as definitive: %+v", message)
	}
	result := model.applyProfileCloudAction(message)
	if result.screen != profileSetupScreenCloud || result.cloudOutcomeUnknownID != provisionTestProfileID ||
		!strings.Contains(result.cloudNotice, "remote outcome is unknown") {
		t.Fatalf("ambiguous destroy did not enter reconciliation state: %+v", result)
	}
	updated, command := result.Update(keyRunes("d"))
	blocked := updated.(profileSetupModel)
	if command != nil || blocked.screen != profileSetupScreenCloud || !strings.Contains(blocked.err, "restart Servestead") {
		t.Fatalf("ambiguous destroy permitted an immediate retry: command=%v model=%+v", command, blocked)
	}
}

func TestProfileCloudDestroySaveRecoveryDoesNotRepeatProvider(t *testing.T) {
	baseStore := newFileProfileStore(t.TempDir())
	choice := activeCloudProfileChoice()
	profile, err := baseStore.Create(choice.Profile)
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{}}
	if err := baseStore.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	store := &failOnceSaveProfileStore{
		ProfileStore: baseStore,
		err:          errors.New("disk full"),
	}
	fake := &recordingCloudProvider{}
	restore := replaceProvisionCloudProvider(fake)
	defer restore()

	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state}})
	model.profileStore = store
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudRunning
	model.cloudAction = "destroy"
	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	updated, command := model.Update(message)
	model = updated.(profileSetupModel)
	if command != nil || fake.destroyCalls != 1 || store.calls != 1 || model.screen != profileSetupScreenCloudSaveRecovery {
		t.Fatalf("remote success did not stop at local recovery: command=%v destroyCalls=%d saveCalls=%d screen=%d", command, fake.destroyCalls, store.calls, model.screen)
	}
	if !profileCloudMarkedDestroyed(model.profiles[0].Profile) {
		t.Fatalf("in-memory profile did not retain destroyed state: %+v", model.profiles[0].Profile)
	}

	for _, key := range []tea.KeyMsg{keyRunes("q"), keyCtrl('c'), keyCode(tea.KeyEsc)} {
		updated, command = model.Update(key)
		result := updated.(profileSetupModel)
		if command != nil || result.quit || result.cancelled || result.screen != profileSetupScreenCloudSaveRecovery {
			t.Fatalf("%s escaped cloud save recovery: command=%v model=%+v", key.String(), command, result)
		}
	}

	updated, command = model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if command == nil || !model.cloudRecoverySaving || fake.destroyCalls != 1 {
		t.Fatalf("recovery did not start one local-only save: command=%v model=%+v destroyCalls=%d", command, model, fake.destroyCalls)
	}
	updated, nextCommand := model.Update(command())
	model = updated.(profileSetupModel)
	if nextCommand != nil || model.screen != profileSetupScreenCloud || model.cloudRecoverySaving || fake.destroyCalls != 1 || store.calls != 2 {
		t.Fatalf("local recovery result = command=%v model=%+v destroyCalls=%d saveCalls=%d", nextCommand, model, fake.destroyCalls, store.calls)
	}
	loaded, _, err := baseStore.Load(profile.ID)
	if err != nil || !profileCloudMarkedDestroyed(loaded) {
		t.Fatalf("recovered profile state = %+v, err=%v", loaded, err)
	}
}

func TestProfileCloudActionRespectsProfileLock(t *testing.T) {
	root := t.TempDir()
	store := newFileProfileStore(root)
	choice := activeCloudProfileChoice()
	profile, err := store.Create(choice.Profile)
	if err != nil {
		t.Fatal(err)
	}
	otherStore := newFileProfileStore(root)
	lock, err := otherStore.TryLockProfile(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseProfileOperationLock(lock)

	fake := &recordingCloudProvider{}
	restore := replaceProvisionCloudProvider(fake)
	defer restore()
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: ProfileState{Runs: map[string]SetupRun{}}}})
	model.profileStore = store
	model.selectedIndex = 0
	model.cloudAction = "destroy"
	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if !errors.Is(message.err, errProfileOperationLocked) || message.remoteOutcomeUnknown || fake.destroyCalls != 0 {
		t.Fatalf("locked cloud action = %+v, destroy calls=%d", message, fake.destroyCalls)
	}
}

func TestProfileCloudActionRecoversInterruptedRunAfterLockRelease(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	choice := activeCloudProfileChoice()
	profile, err := store.Create(choice.Profile)
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{ActiveRunID: "run-active", Runs: map[string]SetupRun{
		"run-active": {ID: "run-active", Status: runStatusRunning},
	}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	fake := &recordingCloudProvider{}
	restore := replaceProvisionCloudProvider(fake)
	defer restore()
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: ProfileState{Runs: map[string]SetupRun{}}}})
	model.profileStore = store
	model.selectedIndex = 0
	model.cloudAction = "restart"
	message := model.runProfileCloudAction(context.Background(), "token")().(profileCloudActionMsg)
	if message.err != nil || fake.rebootCalls != 1 || message.state.ActiveRunID != "" || message.state.Runs["run-active"].Status != runStatusCancelled {
		t.Fatalf("interrupted-run cloud action = %+v, reboot calls=%d", message, fake.rebootCalls)
	}
	_, persisted, err := store.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ActiveRunID != "" || persisted.Runs["run-active"].Status != runStatusCancelled {
		t.Fatalf("cloud action did not persist interrupted-run recovery: %+v", persisted)
	}
}

func TestProfileCloudRecoveryMergesLatestRunState(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	choice := activeCloudProfileChoice()
	profile, err := store.Create(choice.Profile)
	if err != nil {
		t.Fatal(err)
	}
	latest := ProfileState{ActiveRunID: "run-new", Runs: map[string]SetupRun{
		"run-new": {ID: "run-new", Status: runStatusComplete},
	}}
	if err := store.Save(profile, latest); err != nil {
		t.Fatal(err)
	}
	recovery := profile
	cloud := *profile.Cloud
	destroyedAt := time.Now().UTC()
	cloud.DestroyedAt = &destroyedAt
	recovery.Cloud = &cloud

	savedProfile, savedState, err := saveProfileCloudRecovery(store, recovery)
	if err != nil {
		t.Fatal(err)
	}
	if !profileCloudMarkedDestroyed(savedProfile) || savedState.ActiveRunID != "run-new" || savedState.Runs["run-new"].Status != runStatusComplete {
		t.Fatalf("merged cloud recovery = profile %+v state %+v", savedProfile, savedState)
	}
}

func TestProfileCloudParentContextCancellationReachesProvider(t *testing.T) {
	fake := newDelayedCloudProvider("reboot")
	restore := replaceProvisionCloudProvider(fake)
	defer restore()
	parentCtx, cancel := context.WithCancel(context.Background())

	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.tuiContext = parentCtx
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudConfirm
	model.cloudAction = "restart"
	model.cloudTokenInput.SetValue("token")
	model.cloudConfirmInput.SetValue("restart 84")
	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if command == nil {
		t.Fatal("cloud action did not start")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()
	waitForTestSignal(t, fake.started, "cloud action start")
	cancel()
	waitForTestSignal(t, fake.cancelled, "parent context cancellation")
	updated, _ = model.Update(<-result)
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloud || !strings.Contains(model.cloudNotice, "remote outcome is unknown") {
		t.Fatalf("parent cancellation result = screen=%d notice=%q err=%q", model.screen, model.cloudNotice, model.err)
	}
}

func TestProfileCloudConfirmUsesEnvTokenWhenAvailable(t *testing.T) {
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "env-token")
	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model = model.openProfileCloudConfirm("destroy")
	if model.focus != 1 || model.cloudTokenInput.Value() != "env-token" {
		t.Fatalf("env token should focus confirmation: focus=%d token=%q", model.focus, model.cloudTokenInput.Value())
	}
	if !strings.Contains(model.profileCloudConfirmView(), "permanently deletes") {
		t.Fatalf("destroy confirm view missing warning:\n%s", model.profileCloudConfirmView())
	}
}

func TestProfileCloudCancellationReachesDelayedProvider(t *testing.T) {
	for _, test := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "ctrl+c", key: keyCtrl('c')},
		{name: "q", key: keyRunes("q")},
		{name: "esc", key: keyCode(tea.KeyEsc)},
	} {
		t.Run(test.name, func(t *testing.T) {
			runProfileCloudCancellationTest(t, test.name, test.key)
		})
	}
}

func runProfileCloudCancellationTest(t *testing.T, keyName string, cancelKey tea.KeyMsg) {
	t.Helper()
	fake := newDelayedCloudProvider("reboot")
	restore := replaceProvisionCloudProvider(fake)
	defer restore()

	model := newProfileSetupModel([]profileChoice{activeCloudProfileChoice()})
	model.selectedIndex = 0
	model.screen = profileSetupScreenCloudConfirm
	model.cloudAction = "restart"
	model.cloudTokenInput.SetValue("token")
	model.cloudConfirmInput.SetValue("restart 84")

	updated, command := model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloudRunning || model.cloudCancel == nil || command == nil {
		t.Fatalf("cloud action did not start: screen=%d cancel=%v command=%v", model.screen, model.cloudCancel != nil, command)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()
	waitForTestSignal(t, fake.started, "cloud action start")

	updated, duplicateCommand := model.Update(keyCode(tea.KeyEnter))
	model = updated.(profileSetupModel)
	if duplicateCommand != nil || fake.calls != 1 {
		t.Fatalf("busy cloud action accepted a duplicate action: command=%v calls=%d", duplicateCommand, fake.calls)
	}
	updated, cancelCommand := model.Update(cancelKey)
	model = updated.(profileSetupModel)
	if cancelCommand != nil || model.cloudCancel != nil || model.cancelled {
		t.Fatalf("%s should request cancellation without quitting setup: command=%v model=%+v", keyName, cancelCommand, model)
	}
	if !strings.Contains(model.profileCloudRunningView(), "Cancelling DigitalOcean restart action") {
		t.Fatalf("cloud cancelling view missing acknowledgement:\n%s", model.profileCloudRunningView())
	}
	waitForTestSignal(t, fake.cancelled, "cloud action cancellation")

	updated, _ = model.Update(<-result)
	model = updated.(profileSetupModel)
	if model.screen != profileSetupScreenCloud || model.err != "" || !strings.Contains(model.cloudNotice, "remote outcome is unknown") {
		t.Fatalf("cancelled cloud action did not recover to cloud screen: screen=%d err=%q notice=%q", model.screen, model.err, model.cloudNotice)
	}
	if fake.calls != 1 {
		t.Fatalf("cloud cancellation duplicated provider calls: %d", fake.calls)
	}
}

type recordingCloudProvider struct {
	catalog          cloudCatalog
	catalogErr       error
	created          server
	createErr        error
	createdConfig    provisionConfig
	createdKey       cloudSSHKey
	keyErr           error
	createdKeyName   string
	createdPublicKey string
	destroyErr       error
	rebootErr        error
	destroyedID      string
	rebootedID       string
	createCalls      int
	keyCalls         int
	destroyCalls     int
	rebootCalls      int
}

func (provider *recordingCloudProvider) Catalog(context.Context) (cloudCatalog, error) {
	return provider.catalog, provider.catalogErr
}

func (provider *recordingCloudProvider) Create(_ context.Context, config provisionConfig) (server, error) {
	provider.createCalls++
	provider.createdConfig = config
	if provider.createErr != nil {
		return provider.created, provider.createErr
	}
	return provider.created, nil
}

func (provider *recordingCloudProvider) CreateSSHKey(_ context.Context, name, publicKey string) (cloudSSHKey, error) {
	provider.keyCalls++
	provider.createdKeyName = name
	provider.createdPublicKey = publicKey
	if provider.keyErr != nil {
		return cloudSSHKey{}, provider.keyErr
	}
	return provider.createdKey, nil
}

func (provider *recordingCloudProvider) Reboot(_ context.Context, id string) error {
	provider.rebootCalls++
	provider.rebootedID = id
	return provider.rebootErr
}

func (provider *recordingCloudProvider) Destroy(_ context.Context, id string) error {
	provider.destroyCalls++
	provider.destroyedID = id
	return provider.destroyErr
}

type delayedCloudProvider struct {
	recordingCloudProvider
	operation string
	started   chan struct{}
	cancelled chan struct{}
	calls     int
}

func newDelayedCloudProvider(operation string) *delayedCloudProvider {
	return &delayedCloudProvider{
		operation: operation,
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (provider *delayedCloudProvider) Create(ctx context.Context, config provisionConfig) (server, error) {
	if provider.operation != "create" {
		return provider.recordingCloudProvider.Create(ctx, config)
	}
	return server{}, provider.waitForCancellation(ctx)
}

func (provider *delayedCloudProvider) Reboot(ctx context.Context, id string) error {
	provider.rebootedID = id
	if provider.operation != "reboot" {
		return provider.recordingCloudProvider.Reboot(ctx, id)
	}
	return provider.waitForCancellation(ctx)
}

func (provider *delayedCloudProvider) waitForCancellation(ctx context.Context) error {
	provider.calls++
	if provider.calls == 1 {
		close(provider.started)
	}
	<-ctx.Done()
	if provider.calls == 1 {
		close(provider.cancelled)
	}
	return ctx.Err()
}

func waitForTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

type listItemForTest struct {
	index int
}

func (item listItemForTest) Title() string       { return "test" }
func (item listItemForTest) Description() string { return "test" }
func (item listItemForTest) FilterValue() string { return "test" }

type failOnceCreateProfileStore struct {
	ProfileStore
	err      error
	calls    int
	attempts []Profile
}

func (store *failOnceCreateProfileStore) Create(profile Profile) (Profile, error) {
	store.calls++
	store.attempts = append(store.attempts, profile)
	if store.calls == 1 {
		return Profile{}, store.err
	}
	return store.ProfileStore.Create(profile)
}

type failingSaveProfileStore struct {
	ProfileStore
	err error
}

type failOnceSaveProfileStore struct {
	ProfileStore
	err   error
	calls int
}

func (store *failOnceSaveProfileStore) Save(profile Profile, state ProfileState) error {
	store.calls++
	if store.calls == 1 {
		return store.err
	}
	return store.ProfileStore.Save(profile, state)
}

func (store failingSaveProfileStore) Save(Profile, ProfileState) error {
	return store.err
}

func replaceProvisionCloudProvider(provider cloudProvider) func() {
	original := newProvisionCloudProvider
	newProvisionCloudProvider = func(string) cloudProvider { return provider }
	return func() { newProvisionCloudProvider = original }
}

func writeProvisionTestKeypair(t *testing.T) string {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	var stdout, stderr bytes.Buffer
	if err := generateProviderKeypair(context.Background(), keygenConfig{Path: keyPath, Comment: "test@example"}, &stdout, &stderr); err != nil {
		t.Fatalf("generate test keypair: %v stderr=%s", err, stderr.String())
	}
	return keyPath
}

func provisionTestCatalog() cloudCatalog {
	return cloudCatalog{
		Regions: []cloudRegion{
			{Slug: "sfo3", Name: "San Francisco 3", Available: true, Sizes: []string{provisionTestSize}},
			{Slug: defaultDigitalOceanRegion, Name: provisionTestRegionName, Available: true, Sizes: []string{provisionTestSize}},
		},
		Sizes: []cloudSize{{
			Slug:         defaultDigitalOceanSize,
			VCPUs:        1,
			MemoryMB:     1024,
			DiskGB:       25,
			TransferTB:   1,
			PriceMonthly: 6,
			PriceHourly:  0.00893,
			Regions:      []string{defaultDigitalOceanRegion, "sfo3"},
			Available:    true,
		}},
		Images: []cloudImage{{
			Slug:         defaultDigitalOceanImage,
			Name:         "Ubuntu 24.04",
			Distribution: "Ubuntu",
			Status:       "available",
			MinDiskGB:    7,
			Regions:      []string{defaultDigitalOceanRegion, "sfo3"},
		}},
	}
}

func activeCloudProfileChoice() profileChoice {
	return profileChoice{
		Profile: Profile{
			ID:   provisionTestProfileID,
			Name: "production",
			IP:   provisionTestIPv4,
			Cloud: &ProfileCloud{
				Provider:     digitalOceanProviderName,
				ResourceID:   "84",
				Name:         "production",
				Region:       "nyc3",
				Size:         provisionTestSize,
				Image:        provisionTestImage,
				PriceMonthly: 6,
			},
		},
		State: ProfileState{Runs: map[string]SetupRun{}},
	}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}

var _ ProfileStore = failingSaveProfileStore{}
