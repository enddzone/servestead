package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"golang.org/x/crypto/ssh"
)

var newProvisionCloudProvider = func(token string) cloudProvider {
	return newDigitalOceanProvider(token)
}

type provisionScreen int

const (
	provisionScreenInput provisionScreen = iota
	provisionScreenLoading
	provisionScreenRegion
	provisionScreenSize
	provisionScreenImage
	provisionScreenSSHKey
	provisionScreenReview
	provisionScreenCreating
	provisionScreenSavingProfile
	provisionScreenSaveRecovery
	provisionScreenDone
)

type provisionInputConfig struct {
	Token          string
	Name           string
	PrivateKeyPath string
}

type digitalOceanProvisionModel struct {
	ctx                  context.Context // NOSONAR: Bubble Tea's Update method has no context parameter; this is the program-scoped cancellation context.
	store                ProfileStore
	screen               provisionScreen
	inputs               []textinput.Model
	focus                int
	catalog              cloudCatalog
	localPublicKey       string
	localKeyFingerprint  string
	regionList           list.Model
	sizeList             list.Model
	imageList            list.Model
	keyList              list.Model
	selectedRegion       cloudRegion
	selectedSize         cloudSize
	selectedImage        cloudImage
	selectedKey          provisionSSHKeyChoice
	confirmInput         textinput.Model
	createdProfile       Profile
	pendingProfile       Profile
	operationCancel      context.CancelFunc
	cancelling           bool
	notice               string
	err                  string
	width                int
	height               int
	done                 bool
	quit                 bool
	returnToSetup        bool
	cancelled            bool
	remoteOutcomeUnknown bool
}

type provisionCatalogMsg struct {
	catalog     cloudCatalog
	publicKey   string
	fingerprint string
	err         error
}

type provisionCreateMsg struct {
	profile              Profile
	uploadedKey          cloudSSHKey
	remoteOutcomeUnknown bool
	err                  error
}

type provisionSaveMsg struct {
	profile Profile
	err     error
}

type provisionListItem struct {
	kind        string
	index       int
	title       string
	description string
}

func (item provisionListItem) Title() string       { return item.title }
func (item provisionListItem) Description() string { return item.description }
func (item provisionListItem) FilterValue() string { return item.title + " " + item.description }

type provisionSSHKeyChoice struct {
	Key    cloudSSHKey
	Upload bool
}

func newDigitalOceanProvisionModel(ctx context.Context, store ProfileStore) digitalOceanProvisionModel {
	token := firstNonEmpty(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN"), os.Getenv("DIGITALOCEAN_TOKEN"))
	inputs := newSetupInputs([]setupInputField{
		{label: "DigitalOcean API token", value: token, secret: true},
		{label: "Droplet name", placeholder: "servestead-vps", value: "servestead-vps"},
		{label: "Servestead private key", placeholder: defaultKeygenConfig().Path, value: defaultKeygenConfig().Path},
	})
	inputs[0].Focus()
	confirmInput := textinput.New()
	confirmInput.Prompt = "Type confirmation: "
	confirmInput.CharLimit = 256
	confirmInput.SetWidth(72)
	return digitalOceanProvisionModel{
		ctx:          ctx,
		store:        store,
		screen:       provisionScreenInput,
		inputs:       inputs,
		confirmInput: confirmInput,
		regionList:   newProvisionList("DigitalOcean regions", nil),
		sizeList:     newProvisionList("DigitalOcean sizes", nil),
		imageList:    newProvisionList("Ubuntu images", nil),
		keyList:      newProvisionList("DigitalOcean SSH keys", nil),
		width:        82,
		height:       24,
	}
}

func (model digitalOceanProvisionModel) Init() tea.Cmd {
	return textinput.Blink
}

func (model digitalOceanProvisionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return model.updateWindowSize(msg)
	case provisionCatalogMsg:
		return model.updateCatalog(msg)
	case provisionCreateMsg:
		return model.updateCreatedProfile(msg)
	case provisionSaveMsg:
		return model.updateSavedProfile(msg)
	case tea.KeyMsg:
		return model.updateKey(msg)
	default:
		return model, nil
	}
}

func (model digitalOceanProvisionModel) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	model.width = msg.Width
	model.height = msg.Height
	inputWidth := max(10, msg.Width-24)
	for index := range model.inputs {
		model.inputs[index].SetWidth(inputWidth)
	}
	model.confirmInput.SetWidth(max(10, msg.Width-22))
	model.resizeLists()
	return model, nil
}

func (model digitalOceanProvisionModel) updateCatalog(msg provisionCatalogMsg) (tea.Model, tea.Cmd) {
	wasCancelling := model.cancelling
	model.finishOperation()
	if wasCancelling {
		model.cancelled = true
		return model, tea.Quit
	}
	if msg.err != nil {
		model.screen = provisionScreenInput
		model.err = msg.err.Error()
		return model, nil
	}
	model.catalog = msg.catalog
	model.localPublicKey = msg.publicKey
	model.localKeyFingerprint = msg.fingerprint
	model.regionList = newProvisionList("DigitalOcean regions", provisionRegionItems(msg.catalog))
	model.resizeLists()
	if len(model.regionList.Items()) == 0 {
		model.screen = provisionScreenInput
		model.err = "DigitalOcean returned no available regions"
		return model, nil
	}
	model.err = ""
	model.screen = provisionScreenRegion
	return model, nil
}

func (model digitalOceanProvisionModel) updateCreatedProfile(msg provisionCreateMsg) (tea.Model, tea.Cmd) {
	wasCancelling := model.cancelling
	model.finishOperation()
	if msg.uploadedKey.ID != 0 || msg.uploadedKey.Fingerprint != "" {
		model.selectedKey = provisionSSHKeyChoice{Key: msg.uploadedKey}
	}
	if msg.err != nil {
		if msg.profile.Cloud != nil && msg.profile.Cloud.ResourceID != "" {
			model.pendingProfile = msg.profile
			model.screen = provisionScreenSavingProfile
			model.err = ""
			model.notice = fmt.Sprintf(
				"DigitalOcean created Droplet %s, but Servestead stopped while waiting for its IPv4 address: %v. Saving a recovery profile without calling DigitalOcean again.",
				msg.profile.Cloud.ResourceID,
				msg.err,
			)
			return model, model.saveProvisionedProfile(msg.profile)
		}
		if msg.remoteOutcomeUnknown || wasCancelling {
			model.cancelled = wasCancelling
			model.remoteOutcomeUnknown = true
			model.err = "Servestead did not receive a definitive response from DigitalOcean after sending a create request. The remote outcome is unknown; check DigitalOcean for a new SSH key or billable Droplet before retrying."
			return model, tea.Quit
		}
		model.screen = provisionScreenReview
		model.err = msg.err.Error()
		model.confirmInput.SetValue("")
		model.confirmInput.Focus()
		return model, nil
	}
	model.pendingProfile = msg.profile
	model.screen = provisionScreenSavingProfile
	model.err = ""
	if wasCancelling {
		model.notice = "Cancellation was requested, but DigitalOcean created the Droplet. Servestead is saving its local profile now."
	}
	return model, model.saveProvisionedProfile(msg.profile)
}

func (model digitalOceanProvisionModel) updateSavedProfile(msg provisionSaveMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		model.screen = provisionScreenSaveRecovery
		model.err = fmt.Sprintf("save local profile: %v", msg.err)
		return model, nil
	}
	model.createdProfile = msg.profile
	model.pendingProfile = Profile{}
	model.done = true
	model.err = ""
	model.screen = provisionScreenDone
	return model, nil
}

func (model digitalOceanProvisionModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.provisionTerminalTooSmall() {
		if updated, cmd, handled := model.updateGlobalKey(msg); handled {
			return updated, cmd
		}
		return model, nil
	}
	// Ctrl+C remains a global cancellation key while a list filter owns normal
	// text input. The list deliberately owns q and Esc until filtering ends.
	if msg.String() == "ctrl+c" {
		if updated, cmd, handled := model.updateGlobalKey(msg); handled {
			return updated, cmd
		}
	}
	if model.activeListIsFiltering() {
		return model.updateScreenKey(msg)
	}
	if updated, cmd, handled := model.updateGlobalKey(msg); handled {
		return updated, cmd
	}
	return model.updateScreenKey(msg)
}

func (model digitalOceanProvisionModel) provisionTerminalTooSmall() bool {
	return model.width > 0 && (model.width < profileSetupMinWidth || model.height < profileSetupMinHeight)
}

func (model digitalOceanProvisionModel) activeListIsFiltering() bool {
	switch model.screen {
	case provisionScreenRegion:
		return model.regionList.FilterState() == list.Filtering
	case provisionScreenSize:
		return model.sizeList.FilterState() == list.Filtering
	case provisionScreenImage:
		return model.imageList.FilterState() == list.Filtering
	case provisionScreenSSHKey:
		return model.keyList.FilterState() == list.Filtering
	default:
		return false
	}
}

func (model digitalOceanProvisionModel) updateGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		if model.screen == provisionScreenDone {
			model.quit = true
			return model, tea.Quit, true
		}
		if model.providerOperationBusy() {
			model.requestOperationCancellation()
			return model, nil, true
		}
		if model.screen == provisionScreenSavingProfile || model.screen == provisionScreenSaveRecovery {
			model.err = "The Droplet exists. Finish saving its local recovery profile before exiting."
			return model, nil, true
		}
		model.cancelled = true
		return model, tea.Quit, true
	case "q":
		return model.updateQuitKey()
	case "esc":
		if model.providerOperationBusy() {
			model.requestOperationCancellation()
			return model, nil, true
		}
		if model.screen == provisionScreenSavingProfile || model.screen == provisionScreenSaveRecovery {
			model.err = "The Droplet exists. Save its local profile before going back."
			return model, nil, true
		}
		if model.screen == provisionScreenInput {
			model.returnToSetup = true
			return model, tea.Quit, true
		}
		model.goBack()
		model.err = ""
		return model, nil, true
	default:
		return model, nil, false
	}
}

func (model digitalOceanProvisionModel) updateQuitKey() (tea.Model, tea.Cmd, bool) {
	if model.screen == provisionScreenDone {
		model.quit = true
		return model, tea.Quit, true
	}
	if model.providerOperationBusy() {
		model.requestOperationCancellation()
		return model, nil, true
	}
	if model.screen == provisionScreenSavingProfile {
		model.err = "The Droplet exists. Wait for the local profile save to finish."
		return model, nil, true
	}
	if model.screen == provisionScreenSaveRecovery {
		model.err = "The Droplet exists. Press Enter to retry saving its local recovery profile before exiting."
		return model, nil, true
	}
	if model.screen != provisionScreenInput && model.screen != provisionScreenReview {
		model.cancelled = true
		return model, tea.Quit, true
	}
	return model, nil, false
}

func (model digitalOceanProvisionModel) updateScreenKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch model.screen {
	case provisionScreenInput:
		return model.updateInput(msg)
	case provisionScreenRegion:
		return model.updateRegion(msg)
	case provisionScreenSize:
		return model.updateSize(msg)
	case provisionScreenImage:
		return model.updateImage(msg)
	case provisionScreenSSHKey:
		return model.updateSSHKey(msg)
	case provisionScreenReview:
		return model.updateReview(msg)
	case provisionScreenSaveRecovery:
		if msg.String() == "enter" {
			model.err = ""
			model.screen = provisionScreenSavingProfile
			return model, model.saveProvisionedProfile(model.pendingProfile)
		}
		return model, nil
	case provisionScreenDone:
		if msg.String() == "enter" {
			return model, tea.Quit
		}
		return model, nil
	default:
		return model, nil
	}
}

func (model digitalOceanProvisionModel) updateInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "tab", "down":
		model.inputs[model.focus].Blur()
		model.focus = (model.focus + 1) % len(model.inputs)
		model.inputs[model.focus].Focus()
		return model, nil
	case "shift+tab", "up":
		model.inputs[model.focus].Blur()
		model.focus--
		if model.focus < 0 {
			model.focus = len(model.inputs) - 1
		}
		model.inputs[model.focus].Focus()
		return model, nil
	case "enter":
		if model.focus < len(model.inputs)-1 {
			model.inputs[model.focus].Blur()
			model.focus++
			model.inputs[model.focus].Focus()
			return model, nil
		}
		config, err := model.inputConfig()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.err = ""
		model.screen = provisionScreenLoading
		operationCtx := model.beginOperation()
		return model, model.loadCatalog(operationCtx, config)
	}
	var cmd tea.Cmd
	model.inputs[model.focus], cmd = updateSetupTextInput(model.inputs[model.focus], key)
	return model, cmd
}

func (model digitalOceanProvisionModel) updateRegion(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		selected, ok := model.regionList.SelectedItem().(provisionListItem)
		if !ok {
			return model, nil
		}
		regions := provisionAvailableRegions(model.catalog)
		if selected.index < 0 || selected.index >= len(regions) {
			model.err = "selected region is no longer available"
			return model, nil
		}
		model.selectedRegion = regions[selected.index]
		model.sizeList = newProvisionList("DigitalOcean sizes", provisionSizeItems(model.catalog, model.selectedRegion.Slug))
		model.resizeLists()
		if len(model.sizeList.Items()) == 0 {
			model.err = fmt.Sprintf("DigitalOcean returned no available sizes for %s", model.selectedRegion.Slug)
			return model, nil
		}
		model.err = ""
		model.screen = provisionScreenSize
		return model, nil
	}
	var cmd tea.Cmd
	model.regionList, cmd = model.regionList.Update(key)
	return model, cmd
}

func (model digitalOceanProvisionModel) updateSize(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		selected, ok := model.sizeList.SelectedItem().(provisionListItem)
		if !ok {
			return model, nil
		}
		sizes := provisionAvailableSizes(model.catalog, model.selectedRegion.Slug)
		if selected.index < 0 || selected.index >= len(sizes) {
			model.err = "selected size is no longer available"
			return model, nil
		}
		model.selectedSize = sizes[selected.index]
		model.imageList = newProvisionList("Ubuntu images", provisionImageItems(model.catalog, model.selectedRegion.Slug, model.selectedSize.DiskGB))
		provisionSelectDefaultImage(&model.imageList)
		model.resizeLists()
		if len(model.imageList.Items()) == 0 {
			model.err = fmt.Sprintf("DigitalOcean returned no Ubuntu images for %s and %s", model.selectedRegion.Slug, model.selectedSize.Slug)
			return model, nil
		}
		model.err = ""
		model.screen = provisionScreenImage
		return model, nil
	}
	var cmd tea.Cmd
	model.sizeList, cmd = model.sizeList.Update(key)
	return model, cmd
}

func (model digitalOceanProvisionModel) updateImage(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		selected, ok := model.imageList.SelectedItem().(provisionListItem)
		if !ok {
			return model, nil
		}
		images := provisionAvailableImages(model.catalog, model.selectedRegion.Slug, model.selectedSize.DiskGB)
		if selected.index < 0 || selected.index >= len(images) {
			model.err = "selected image is no longer available"
			return model, nil
		}
		model.selectedImage = images[selected.index]
		model.keyList = newProvisionList("DigitalOcean SSH keys", provisionSSHKeyItems(model.catalog, model.localPublicKey, model.localKeyFingerprint))
		model.resizeLists()
		if len(model.keyList.Items()) == 0 {
			model.err = "DigitalOcean returned no SSH key choices"
			return model, nil
		}
		model.err = ""
		model.screen = provisionScreenSSHKey
		return model, nil
	}
	var cmd tea.Cmd
	model.imageList, cmd = model.imageList.Update(key)
	return model, cmd
}

func (model digitalOceanProvisionModel) updateSSHKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "enter" {
		selected, ok := model.keyList.SelectedItem().(provisionListItem)
		if !ok {
			return model, nil
		}
		choices := provisionSSHKeyChoices(model.catalog, model.localPublicKey, model.localKeyFingerprint)
		if selected.index < 0 || selected.index >= len(choices) {
			model.err = "selected SSH key is no longer available"
			return model, nil
		}
		model.selectedKey = choices[selected.index]
		model.confirmInput.SetValue("")
		model.confirmInput.Focus()
		model.err = ""
		model.screen = provisionScreenReview
		return model, nil
	}
	var cmd tea.Cmd
	model.keyList, cmd = model.keyList.Update(key)
	return model, cmd
}

func (model digitalOceanProvisionModel) updateReview(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "enter":
		expected := provisionConfirmPhrase(model.inputConfigName())
		if strings.TrimSpace(model.confirmInput.Value()) != expected {
			model.err = fmt.Sprintf("type %q to create this billable Droplet", expected)
			return model, nil
		}
		config, err := model.inputConfig()
		if err != nil {
			model.err = err.Error()
			return model, nil
		}
		model.confirmInput.Blur()
		model.err = ""
		model.notice = ""
		model.screen = provisionScreenCreating
		operationCtx := model.beginOperation()
		return model, model.createDroplet(operationCtx, config)
	}
	var cmd tea.Cmd
	model.confirmInput, cmd = updateSetupTextInput(model.confirmInput, key)
	return model, cmd
}

func (model *digitalOceanProvisionModel) goBack() {
	switch model.screen {
	case provisionScreenInput:
		return
	case provisionScreenLoading, provisionScreenRegion:
		model.screen = provisionScreenInput
	case provisionScreenSize:
		model.screen = provisionScreenRegion
	case provisionScreenImage:
		model.screen = provisionScreenSize
	case provisionScreenSSHKey:
		model.screen = provisionScreenImage
	case provisionScreenReview:
		model.confirmInput.Blur()
		model.screen = provisionScreenSSHKey
	case provisionScreenCreating, provisionScreenSavingProfile, provisionScreenSaveRecovery:
		return
	case provisionScreenDone:
		return
	}
}

func (model *digitalOceanProvisionModel) resizeLists() {
	width := max(20, model.width-4)
	height := max(4, model.height-9)
	model.regionList.SetSize(width, height)
	model.sizeList.SetSize(width, height)
	model.imageList.SetSize(width, height)
	model.keyList.SetSize(width, height)
}

func (model digitalOceanProvisionModel) inputConfig() (provisionInputConfig, error) {
	token := strings.TrimSpace(model.inputs[0].Value())
	name := strings.TrimSpace(model.inputs[1].Value())
	privateKey := expandUserPath(strings.TrimSpace(model.inputs[2].Value()))
	if token == "" {
		return provisionInputConfig{}, errors.New("DigitalOcean API token is required")
	}
	if name == "" {
		return provisionInputConfig{}, errors.New("Droplet name is required")
	}
	if strings.ContainsAny(name, "\r\n") {
		return provisionInputConfig{}, errors.New("Droplet name must not contain newlines")
	}
	if sanitizeTerminalLine(name) != name {
		return provisionInputConfig{}, errors.New("Droplet name must not contain terminal control characters")
	}
	if privateKey == "" {
		return provisionInputConfig{}, errors.New("Servestead private key path is required")
	}
	return provisionInputConfig{Token: token, Name: name, PrivateKeyPath: privateKey}, nil
}

func (model digitalOceanProvisionModel) inputConfigName() string {
	return strings.TrimSpace(model.inputs[1].Value())
}

func (model *digitalOceanProvisionModel) beginOperation() context.Context {
	base := model.ctx
	if base == nil {
		base = context.Background()
	}
	operationCtx, cancel := context.WithCancel(base)
	model.operationCancel = cancel
	model.cancelling = false
	return operationCtx
}

func (model *digitalOceanProvisionModel) finishOperation() {
	if model.operationCancel != nil {
		model.operationCancel()
	}
	model.operationCancel = nil
	model.cancelling = false
}

func (model digitalOceanProvisionModel) providerOperationBusy() bool {
	return model.screen == provisionScreenLoading || model.screen == provisionScreenCreating
}

func (model *digitalOceanProvisionModel) requestOperationCancellation() {
	if model.cancelling {
		return
	}
	model.cancelling = true
	model.err = ""
	if model.operationCancel != nil {
		model.operationCancel()
	}
}

func (model digitalOceanProvisionModel) loadCatalog(ctx context.Context, config provisionInputConfig) tea.Cmd {
	return func() tea.Msg {
		publicKey, fingerprint, err := readProvisionPublicKey(config.PrivateKeyPath)
		if err != nil {
			return provisionCatalogMsg{err: err}
		}
		catalog, err := newProvisionCloudProvider(config.Token).Catalog(ctx)
		return provisionCatalogMsg{
			catalog:     catalog,
			publicKey:   publicKey,
			fingerprint: fingerprint,
			err:         err,
		}
	}
}

func (model digitalOceanProvisionModel) createDroplet(ctx context.Context, config provisionInputConfig) tea.Cmd {
	return func() tea.Msg {
		provider := newProvisionCloudProvider(config.Token)
		keyReference := model.selectedKeyReference()
		var uploadedKey cloudSSHKey
		if model.selectedKey.Upload {
			keyName := provisionSSHKeyName(config.PrivateKeyPath)
			key, err := provider.CreateSSHKey(ctx, keyName, model.localPublicKey)
			if err != nil {
				return provisionCreateMsg{
					remoteOutcomeUnknown: cloudMutationOutcomeUnknown(err),
					err:                  fmt.Errorf("upload DigitalOcean SSH key: %w", err),
				}
			}
			uploadedKey = key
			keyReference = strconv.Itoa(key.ID)
		}
		created, err := provider.Create(ctx, provisionConfig{
			Name:   config.Name,
			Region: model.selectedRegion.Slug,
			Size:   model.selectedSize.Slug,
			Image:  model.selectedImage.Slug,
			SSHKey: keyReference,
		})
		if err != nil {
			message := provisionCreateMsg{
				uploadedKey:          uploadedKey,
				remoteOutcomeUnknown: cloudMutationOutcomeUnknown(err),
				err:                  fmt.Errorf("create DigitalOcean Droplet: %w", err),
			}
			if created.ID != "" {
				message.profile = newProvisionedDigitalOceanProfile(config, model, created)
			}
			return message
		}
		return provisionCreateMsg{
			profile:     newProvisionedDigitalOceanProfile(config, model, created),
			uploadedKey: uploadedKey,
		}
	}
}

func (model digitalOceanProvisionModel) saveProvisionedProfile(profile Profile) tea.Cmd {
	return func() tea.Msg {
		if model.store == nil {
			return provisionSaveMsg{profile: profile, err: errors.New("profile store is unavailable")}
		}
		saved, err := model.store.Create(profile)
		return provisionSaveMsg{profile: saved, err: err}
	}
}

func (model digitalOceanProvisionModel) selectedKeyReference() string {
	if model.selectedKey.Key.ID != 0 {
		return strconv.Itoa(model.selectedKey.Key.ID)
	}
	return model.selectedKey.Key.Fingerprint
}

func (model digitalOceanProvisionModel) View() tea.View {
	if model.provisionTerminalTooSmall() {
		return altScreenView(fmt.Sprintf(
			"Provision a DigitalOcean VPS\n\nTerminal too small: %dx%d\nResize to at least %dx%d to continue.",
			model.width,
			model.height,
			profileSetupMinWidth,
			profileSetupMinHeight,
		))
	}
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Provision a DigitalOcean VPS"))
	builder.WriteString("\n")
	tagline := "This creates one billable Droplet. Bootstrap and hardening remain separate setup actions."
	builder.WriteString(setupHelpStyle.Render(truncateForTable(tagline, max(1, model.width))))
	builder.WriteString("\n\n")

	switch model.screen {
	case provisionScreenInput:
		builder.WriteString("Enter the token, Droplet name, and local Servestead key. The token is used for this run only and is not saved.\n\n")
		for _, input := range model.inputs {
			builder.WriteString(input.View())
			builder.WriteString("\n")
		}
	case provisionScreenLoading:
		if model.cancelling {
			builder.WriteString("Cancelling the DigitalOcean catalog request...\n")
		} else {
			builder.WriteString("Loading DigitalOcean regions, sizes, images, and SSH keys...\n")
		}
	case provisionScreenRegion:
		builder.WriteString("Choose a region. Press / to filter.\n\n")
		builder.WriteString(model.regionList.View())
	case provisionScreenSize:
		builder.WriteString(fmt.Sprintf("Region: %s (%s)\n", sanitizeTerminalLine(model.selectedRegion.Name), sanitizeTerminalLine(model.selectedRegion.Slug)))
		builder.WriteString("Choose a size. Prices come from the DigitalOcean API. Press / to filter.\n\n")
		builder.WriteString(model.sizeList.View())
	case provisionScreenImage:
		builder.WriteString(fmt.Sprintf("Region: %s • Size: %s\n", sanitizeTerminalLine(model.selectedRegion.Slug), sanitizeTerminalLine(model.selectedSize.Slug)))
		builder.WriteString("Choose an Ubuntu image. Press / to filter.\n\n")
		builder.WriteString(model.imageList.View())
	case provisionScreenSSHKey:
		builder.WriteString(fmt.Sprintf("Local key fingerprint: %s\n", sanitizeTerminalLine(model.localKeyFingerprint)))
		builder.WriteString("Choose an existing provider key or upload the local public key.\n\n")
		builder.WriteString(model.keyList.View())
	case provisionScreenReview:
		builder.WriteString(model.provisionReviewView())
	case provisionScreenCreating:
		if model.cancelling {
			builder.WriteString("Cancellation requested. Waiting for DigitalOcean to confirm whether the Droplet was created...\n")
		} else {
			builder.WriteString("Creating the Droplet and waiting for its public IPv4 address...\n")
		}
	case provisionScreenSavingProfile:
		builder.WriteString(fmt.Sprintf("Droplet %s exists. Saving its local Servestead profile...\n", sanitizeTerminalLine(model.pendingProfile.Cloud.ResourceID)))
	case provisionScreenSaveRecovery:
		builder.WriteString("DigitalOcean created the Droplet, but Servestead could not save its local profile.\n\n")
		builder.WriteString(fmt.Sprintf("Droplet ID: %s\n", sanitizeTerminalLine(model.pendingProfile.Cloud.ResourceID)))
		builder.WriteString(fmt.Sprintf("IPv4:       %s\n\n", sanitizeTerminalLine(model.pendingProfile.IP)))
		builder.WriteString("Press Enter to retry the local save. DigitalOcean will not be called again.\n")
	case provisionScreenDone:
		builder.WriteString(fmt.Sprintf("Droplet created and saved as profile %s.\n\n", sanitizeTerminalLine(firstNonEmpty(model.createdProfile.Name, model.createdProfile.ID))))
		builder.WriteString(fmt.Sprintf("IPv4: %s\n", sanitizeTerminalLine(firstNonEmpty(model.createdProfile.IP, "not available yet"))))
		if model.createdProfile.IP == "" {
			builder.WriteString("Enter the Droplet's public IPv4 address in profile settings before running setup.\n")
		}
		builder.WriteString("Press Enter to open the profile dashboard.\n")
	}
	if model.err != "" {
		builder.WriteString("\n")
		builder.WriteString(setupErrorStyle.Render(sanitizeTerminalText(model.err)))
		builder.WriteString("\n")
	}
	if model.notice != "" {
		builder.WriteString("\n")
		builder.WriteString(setupWarningStyle.Render(sanitizeTerminalText(model.notice)))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render(model.provisionHelpText()))
	return altScreenView(fitTerminalWidth(builder.String(), model.width))
}

func (model digitalOceanProvisionModel) provisionReviewView() string {
	keyLabel := "upload local public key"
	if !model.selectedKey.Upload {
		keyLabel = fmt.Sprintf("%s (%s)", sanitizeTerminalLine(firstNonEmpty(model.selectedKey.Key.Name, "unnamed key")), sanitizeTerminalLine(firstNonEmpty(model.selectedKey.Key.Fingerprint, strconv.Itoa(model.selectedKey.Key.ID))))
	}
	name := sanitizeTerminalLine(model.inputConfigName())
	expected := provisionConfirmPhrase(name)
	var builder strings.Builder
	builder.WriteString("Review billable Droplet:\n\n")
	builder.WriteString(fmt.Sprintf("Name:   %s\n", name))
	builder.WriteString(fmt.Sprintf("Region: %s (%s)\n", sanitizeTerminalLine(model.selectedRegion.Name), sanitizeTerminalLine(model.selectedRegion.Slug)))
	builder.WriteString(fmt.Sprintf("Size:   %s - %d vCPU, %d MiB RAM, %d GiB disk\n", sanitizeTerminalLine(model.selectedSize.Slug), model.selectedSize.VCPUs, model.selectedSize.MemoryMB, model.selectedSize.DiskGB))
	builder.WriteString(fmt.Sprintf("Image:  %s\n", sanitizeTerminalLine(model.selectedImage.Slug)))
	builder.WriteString(fmt.Sprintf("SSH:    %s\n", keyLabel))
	builder.WriteString(fmt.Sprintf("Cost:   $%.2f/month, $%.5f/hour\n\n", model.selectedSize.PriceMonthly, model.selectedSize.PriceHourly))
	builder.WriteString("This creates one DigitalOcean Droplet. It does not bootstrap, harden, configure DNS, or deploy apps.\n")
	builder.WriteString(fmt.Sprintf("To continue, type %q.\n\n", expected))
	builder.WriteString(model.confirmInput.View())
	return builder.String()
}

func (model digitalOceanProvisionModel) provisionHelpText() string {
	switch model.screen {
	case provisionScreenInput:
		return "Enter advances. Tab changes field. Esc goes back. Ctrl+C cancels."
	case provisionScreenLoading, provisionScreenCreating:
		if model.cancelling {
			return "Cancellation requested. Waiting for DigitalOcean to respond."
		}
		return "Waiting for DigitalOcean. Ctrl+C, q, or Esc requests cancellation."
	case provisionScreenSavingProfile:
		return "Saving the local profile. Please wait."
	case provisionScreenSaveRecovery:
		return "Enter retries the local save only. Exiting stays locked until the recovery profile is saved."
	case provisionScreenRegion, provisionScreenSize, provisionScreenImage, provisionScreenSSHKey:
		return "j/k selects. / filters. Enter chooses. Esc goes back. q cancels."
	case provisionScreenReview:
		return "Enter creates after exact confirmation. Esc goes back. Ctrl+C cancels."
	case provisionScreenDone:
		return "Enter opens the saved profile dashboard. q or Ctrl+C exits setup."
	default:
		return "Ctrl+C cancels."
	}
}

func newProvisionList(title string, items []list.Item) list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.SetHeight(2)
	model := list.New(items, delegate, 82, 14)
	model.Title = title
	model.SetShowStatusBar(false)
	model.SetFilteringEnabled(true)
	model.DisableQuitKeybindings()
	model.SetShowHelp(false)
	return model
}

func provisionRegionItems(catalog cloudCatalog) []list.Item {
	regions := provisionAvailableRegions(catalog)
	items := make([]list.Item, 0, len(regions))
	for index, region := range regions {
		items = append(items, provisionListItem{
			index:       index,
			title:       fmt.Sprintf("%s (%s)", sanitizeTerminalLine(region.Name), sanitizeTerminalLine(region.Slug)),
			description: fmt.Sprintf("%d available size(s)", len(region.Sizes)),
		})
	}
	return items
}

func provisionSizeItems(catalog cloudCatalog, region string) []list.Item {
	sizes := provisionAvailableSizes(catalog, region)
	items := make([]list.Item, 0, len(sizes))
	for index, size := range sizes {
		items = append(items, provisionListItem{
			index:       index,
			title:       fmt.Sprintf("%s - $%.2f/month", sanitizeTerminalLine(size.Slug), size.PriceMonthly),
			description: fmt.Sprintf("%d vCPU, %d MiB RAM, %d GiB disk, %.2f TiB transfer, $%.5f/hour", size.VCPUs, size.MemoryMB, size.DiskGB, size.TransferTB, size.PriceHourly),
		})
	}
	return items
}

func provisionImageItems(catalog cloudCatalog, region string, diskGB int) []list.Item {
	images := provisionAvailableImages(catalog, region, diskGB)
	items := make([]list.Item, 0, len(images))
	for index, image := range images {
		items = append(items, provisionListItem{
			index:       index,
			title:       sanitizeTerminalLine(firstNonEmpty(image.Slug, image.Name)),
			description: fmt.Sprintf("%s image, min disk %d GiB", sanitizeTerminalLine(firstNonEmpty(image.Distribution, "Ubuntu")), image.MinDiskGB),
		})
	}
	return items
}

func provisionSSHKeyItems(catalog cloudCatalog, publicKey, fingerprint string) []list.Item {
	choices := provisionSSHKeyChoices(catalog, publicKey, fingerprint)
	items := make([]list.Item, 0, len(choices))
	for index, choice := range choices {
		if choice.Upload {
			items = append(items, provisionListItem{
				index:       index,
				title:       "Upload the local Servestead public key",
				description: "Creates a DigitalOcean SSH key, then uses it for the new Droplet.",
			})
			continue
		}
		items = append(items, provisionListItem{
			index:       index,
			title:       sanitizeTerminalLine(firstNonEmpty(choice.Key.Name, "unnamed DigitalOcean key")),
			description: fmt.Sprintf("ID %d - %s", choice.Key.ID, sanitizeTerminalLine(choice.Key.Fingerprint)),
		})
	}
	return items
}

func provisionAvailableRegions(catalog cloudCatalog) []cloudRegion {
	regions := make([]cloudRegion, 0, len(catalog.Regions))
	for _, region := range catalog.Regions {
		if region.Available {
			regions = append(regions, region)
		}
	}
	sort.Slice(regions, func(i, j int) bool {
		if regions[i].Slug == defaultDigitalOceanRegion {
			return true
		}
		if regions[j].Slug == defaultDigitalOceanRegion {
			return false
		}
		return regions[i].Slug < regions[j].Slug
	})
	return regions
}

func provisionAvailableSizes(catalog cloudCatalog, region string) []cloudSize {
	sizes := make([]cloudSize, 0, len(catalog.Sizes))
	for _, size := range catalog.Sizes {
		if !size.Available || !containsString(size.Regions, region) || size.PriceMonthly <= 0 {
			continue
		}
		sizes = append(sizes, size)
	}
	sort.Slice(sizes, func(i, j int) bool {
		if sizes[i].Slug == defaultDigitalOceanSize {
			return true
		}
		if sizes[j].Slug == defaultDigitalOceanSize {
			return false
		}
		if sizes[i].PriceMonthly != sizes[j].PriceMonthly {
			return sizes[i].PriceMonthly < sizes[j].PriceMonthly
		}
		return sizes[i].Slug < sizes[j].Slug
	})
	return sizes
}

func provisionAvailableImages(catalog cloudCatalog, region string, diskGB int) []cloudImage {
	images := make([]cloudImage, 0, len(catalog.Images))
	for _, image := range catalog.Images {
		if image.Slug == "" || !containsString(image.Regions, region) {
			continue
		}
		if image.MinDiskGB > diskGB {
			continue
		}
		images = append(images, image)
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].Slug == defaultDigitalOceanImage {
			return true
		}
		if images[j].Slug == defaultDigitalOceanImage {
			return false
		}
		return images[i].Slug < images[j].Slug
	})
	return images
}

func provisionSSHKeyChoices(catalog cloudCatalog, publicKey, fingerprint string) []provisionSSHKeyChoice {
	normalizedPublicKey := normalizeAuthorizedKey(publicKey)
	matches := []provisionSSHKeyChoice{}
	others := []provisionSSHKeyChoice{}
	for _, key := range catalog.SSHKeys {
		choice := provisionSSHKeyChoice{Key: key}
		if key.Fingerprint == fingerprint || (normalizedPublicKey != "" && normalizeAuthorizedKey(key.PublicKey) == normalizedPublicKey) {
			matches = append(matches, choice)
			continue
		}
		others = append(others, choice)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Key.Name < matches[j].Key.Name })
	sort.Slice(others, func(i, j int) bool { return others[i].Key.Name < others[j].Key.Name })
	choices := append([]provisionSSHKeyChoice{}, matches...)
	if len(matches) == 0 {
		choices = append(choices, provisionSSHKeyChoice{Upload: true})
	}
	choices = append(choices, others...)
	return choices
}

func provisionSelectDefaultImage(model *list.Model) {
	for index, item := range model.Items() {
		listItem, ok := item.(provisionListItem)
		if ok && strings.HasPrefix(listItem.title, defaultDigitalOceanImage) {
			model.Select(index)
			return
		}
	}
}

func provisionConfirmPhrase(name string) string {
	return "provision " + sanitizeTerminalLine(name)
}

func readProvisionPublicKey(privateKeyPath string) (string, string, error) {
	publicKeyPath := publicKeyPath(privateKeyPath)
	data, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", "", fmt.Errorf("read public key %s: %w", publicKeyPath, err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return "", "", fmt.Errorf("parse public key %s: %w", publicKeyPath, err)
	}
	return strings.TrimSpace(string(data)), ssh.FingerprintLegacyMD5(parsed), nil
}

func provisionSSHKeyName(privateKeyPath string) string {
	name := strings.TrimSuffix(filepath.Base(privateKeyPath), filepath.Ext(privateKeyPath))
	if name == "" || name == "." {
		return "servestead-key"
	}
	return "servestead-" + name
}

func normalizeAuthorizedKey(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) < 2 {
		return strings.TrimSpace(value)
	}
	return fields[0] + " " + fields[1]
}

func saveProvisionedDigitalOceanProfile(store ProfileStore, config provisionInputConfig, model digitalOceanProvisionModel, created server) (Profile, error) {
	return store.Create(newProvisionedDigitalOceanProfile(config, model, created))
}

func newProvisionedDigitalOceanProfile(config provisionInputConfig, model digitalOceanProvisionModel, created server) Profile {
	cloudCreatedAt := time.Now().UTC()
	if created.CreatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, created.CreatedAt); err == nil {
			cloudCreatedAt = parsed
		}
	}
	now := time.Now().UTC()
	profileIdentity := created.IPv4
	if profileIdentity == "" {
		profileIdentity = digitalOceanProviderName + "-" + created.ID
	}
	return Profile{
		ID:             newProfileID(profileIdentity, now),
		Name:           firstNonEmpty(created.Name, config.Name),
		IP:             created.IPv4,
		InitialSSHUser: "root",
		AdminUser:      "servestead",
		PrivateKeyPath: config.PrivateKeyPath,
		CreatedAt:      now,
		UpdatedAt:      now,
		Cloud: &ProfileCloud{
			Provider:     digitalOceanProviderName,
			ResourceID:   created.ID,
			Name:         firstNonEmpty(created.Name, config.Name),
			Region:       model.selectedRegion.Slug,
			Size:         model.selectedSize.Slug,
			Image:        model.selectedImage.Slug,
			PriceMonthly: model.selectedSize.PriceMonthly,
			PriceHourly:  model.selectedSize.PriceHourly,
			CreatedAt:    cloudCreatedAt,
		},
	}
}
