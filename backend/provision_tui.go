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
	provisionScreenDone
)

type provisionInputConfig struct {
	Token          string
	Name           string
	PrivateKeyPath string
}

type digitalOceanProvisionModel struct {
	ctx                 context.Context
	store               ProfileStore
	screen              provisionScreen
	inputs              []textinput.Model
	focus               int
	catalog             cloudCatalog
	localPublicKey      string
	localKeyFingerprint string
	regionList          list.Model
	sizeList            list.Model
	imageList           list.Model
	keyList             list.Model
	selectedRegion      cloudRegion
	selectedSize        cloudSize
	selectedImage       cloudImage
	selectedKey         provisionSSHKeyChoice
	confirmInput        textinput.Model
	createdProfile      Profile
	err                 string
	width               int
	height              int
	done                bool
	cancelled           bool
}

type provisionCatalogMsg struct {
	catalog     cloudCatalog
	publicKey   string
	fingerprint string
	err         error
}

type provisionCreateMsg struct {
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
	case tea.KeyMsg:
		return model.updateKey(msg)
	default:
		return model, nil
	}
}

func (model digitalOceanProvisionModel) updateWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	model.width = msg.Width
	model.height = msg.Height
	model.resizeLists()
	return model, nil
}

func (model digitalOceanProvisionModel) updateCatalog(msg provisionCatalogMsg) (tea.Model, tea.Cmd) {
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
	if msg.err != nil {
		model.screen = provisionScreenReview
		model.err = msg.err.Error()
		model.confirmInput.Focus()
		return model, nil
	}
	model.createdProfile = msg.profile
	model.done = true
	model.screen = provisionScreenDone
	return model, nil
}

func (model digitalOceanProvisionModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if updated, cmd, handled := model.updateGlobalKey(msg); handled {
		return updated, cmd
	}
	return model.updateScreenKey(msg)
}

func (model digitalOceanProvisionModel) updateGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		model.cancelled = true
		return model, tea.Quit, true
	case "q":
		return model.updateQuitKey()
	case "esc":
		model.goBack()
		model.err = ""
		return model, nil, true
	default:
		return model, nil, false
	}
}

func (model digitalOceanProvisionModel) updateQuitKey() (tea.Model, tea.Cmd, bool) {
	if model.screen == provisionScreenDone {
		return model, tea.Quit, true
	}
	if model.screen != provisionScreenInput && model.screen != provisionScreenReview && model.screen != provisionScreenCreating {
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
		return model, model.loadCatalog(config)
	}
	var cmd tea.Cmd
	model.inputs[model.focus], cmd = model.inputs[model.focus].Update(key)
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
		model.screen = provisionScreenCreating
		return model, model.createDroplet(config)
	}
	var cmd tea.Cmd
	model.confirmInput, cmd = model.confirmInput.Update(key)
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
	case provisionScreenCreating:
		return
	case provisionScreenDone:
		return
	}
}

func (model *digitalOceanProvisionModel) resizeLists() {
	width := max(40, model.width-4)
	height := max(8, model.height-9)
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
	if privateKey == "" {
		return provisionInputConfig{}, errors.New("Servestead private key path is required")
	}
	return provisionInputConfig{Token: token, Name: name, PrivateKeyPath: privateKey}, nil
}

func (model digitalOceanProvisionModel) inputConfigName() string {
	return strings.TrimSpace(model.inputs[1].Value())
}

func (model digitalOceanProvisionModel) loadCatalog(config provisionInputConfig) tea.Cmd {
	return func() tea.Msg {
		publicKey, fingerprint, err := readProvisionPublicKey(config.PrivateKeyPath)
		if err != nil {
			return provisionCatalogMsg{err: err}
		}
		catalog, err := newProvisionCloudProvider(config.Token).Catalog(model.ctx)
		return provisionCatalogMsg{
			catalog:     catalog,
			publicKey:   publicKey,
			fingerprint: fingerprint,
			err:         err,
		}
	}
}

func (model digitalOceanProvisionModel) createDroplet(config provisionInputConfig) tea.Cmd {
	return func() tea.Msg {
		provider := newProvisionCloudProvider(config.Token)
		keyReference := model.selectedKeyReference()
		if model.selectedKey.Upload {
			keyName := provisionSSHKeyName(config.PrivateKeyPath)
			key, err := provider.CreateSSHKey(model.ctx, keyName, model.localPublicKey)
			if err != nil {
				return provisionCreateMsg{err: fmt.Errorf("upload DigitalOcean SSH key: %w", err)}
			}
			keyReference = strconv.Itoa(key.ID)
		}
		created, err := provider.Create(model.ctx, provisionConfig{
			Name:   config.Name,
			Region: model.selectedRegion.Slug,
			Size:   model.selectedSize.Slug,
			Image:  model.selectedImage.Slug,
			SSHKey: keyReference,
		})
		if err != nil {
			return provisionCreateMsg{err: fmt.Errorf("create DigitalOcean Droplet: %w", err)}
		}
		profile, err := saveProvisionedDigitalOceanProfile(model.store, config, model, created)
		if err != nil {
			return provisionCreateMsg{err: err}
		}
		return provisionCreateMsg{profile: profile}
	}
}

func (model digitalOceanProvisionModel) selectedKeyReference() string {
	if model.selectedKey.Key.ID != 0 {
		return strconv.Itoa(model.selectedKey.Key.ID)
	}
	return model.selectedKey.Key.Fingerprint
}

func (model digitalOceanProvisionModel) View() tea.View {
	var builder strings.Builder
	builder.WriteString(setupTitleStyle.Render("Provision a DigitalOcean VPS"))
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render("This creates one billable Droplet. Bootstrap and hardening remain separate setup actions."))
	builder.WriteString("\n\n")

	switch model.screen {
	case provisionScreenInput:
		builder.WriteString("Enter the token, Droplet name, and local Servestead key. The token is used for this run only and is not saved.\n\n")
		for _, input := range model.inputs {
			builder.WriteString(input.View())
			builder.WriteString("\n")
		}
	case provisionScreenLoading:
		builder.WriteString("Loading DigitalOcean regions, sizes, images, and SSH keys...\n")
	case provisionScreenRegion:
		builder.WriteString("Choose a region. Press / to filter.\n\n")
		builder.WriteString(model.regionList.View())
	case provisionScreenSize:
		builder.WriteString(fmt.Sprintf("Region: %s (%s)\n", model.selectedRegion.Name, model.selectedRegion.Slug))
		builder.WriteString("Choose a size. Prices come from the DigitalOcean API. Press / to filter.\n\n")
		builder.WriteString(model.sizeList.View())
	case provisionScreenImage:
		builder.WriteString(fmt.Sprintf("Region: %s • Size: %s\n", model.selectedRegion.Slug, model.selectedSize.Slug))
		builder.WriteString("Choose an Ubuntu image. Press / to filter.\n\n")
		builder.WriteString(model.imageList.View())
	case provisionScreenSSHKey:
		builder.WriteString(fmt.Sprintf("Local key fingerprint: %s\n", model.localKeyFingerprint))
		builder.WriteString("Choose an existing provider key or upload the local public key.\n\n")
		builder.WriteString(model.keyList.View())
	case provisionScreenReview:
		builder.WriteString(model.provisionReviewView())
	case provisionScreenCreating:
		builder.WriteString("Creating the Droplet and waiting for its public IPv4 address...\n")
	case provisionScreenDone:
		builder.WriteString(fmt.Sprintf("Droplet created and saved as profile %s.\n\n", firstNonEmpty(model.createdProfile.Name, model.createdProfile.ID)))
		builder.WriteString(fmt.Sprintf("IPv4: %s\n", model.createdProfile.IP))
		builder.WriteString("Press Enter to open the profile dashboard.\n")
	}
	if model.err != "" {
		builder.WriteString("\n")
		builder.WriteString(setupErrorStyle.Render(model.err))
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	builder.WriteString(setupHelpStyle.Render(model.provisionHelpText()))
	return altScreenView(builder.String())
}

func (model digitalOceanProvisionModel) provisionReviewView() string {
	keyLabel := "upload local public key"
	if !model.selectedKey.Upload {
		keyLabel = fmt.Sprintf("%s (%s)", firstNonEmpty(model.selectedKey.Key.Name, "unnamed key"), firstNonEmpty(model.selectedKey.Key.Fingerprint, strconv.Itoa(model.selectedKey.Key.ID)))
	}
	name := model.inputConfigName()
	expected := provisionConfirmPhrase(name)
	var builder strings.Builder
	builder.WriteString("Review billable Droplet:\n\n")
	builder.WriteString(fmt.Sprintf("Name:   %s\n", name))
	builder.WriteString(fmt.Sprintf("Region: %s (%s)\n", model.selectedRegion.Name, model.selectedRegion.Slug))
	builder.WriteString(fmt.Sprintf("Size:   %s - %d vCPU, %d MiB RAM, %d GiB disk\n", model.selectedSize.Slug, model.selectedSize.VCPUs, model.selectedSize.MemoryMB, model.selectedSize.DiskGB))
	builder.WriteString(fmt.Sprintf("Image:  %s\n", model.selectedImage.Slug))
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
		return "Waiting for DigitalOcean. Ctrl+C cancels."
	case provisionScreenRegion, provisionScreenSize, provisionScreenImage, provisionScreenSSHKey:
		return "j/k selects. / filters. Enter chooses. Esc goes back. q cancels."
	case provisionScreenReview:
		return "Enter creates after exact confirmation. Esc goes back. Ctrl+C cancels."
	case provisionScreenDone:
		return "Enter opens the saved profile dashboard."
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
	return model
}

func provisionRegionItems(catalog cloudCatalog) []list.Item {
	regions := provisionAvailableRegions(catalog)
	items := make([]list.Item, 0, len(regions))
	for index, region := range regions {
		items = append(items, provisionListItem{
			index:       index,
			title:       fmt.Sprintf("%s (%s)", region.Name, region.Slug),
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
			title:       fmt.Sprintf("%s - $%.2f/month", size.Slug, size.PriceMonthly),
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
			title:       firstNonEmpty(image.Slug, image.Name),
			description: fmt.Sprintf("%s image, min disk %d GiB", firstNonEmpty(image.Distribution, "Ubuntu"), image.MinDiskGB),
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
			title:       firstNonEmpty(choice.Key.Name, "unnamed DigitalOcean key"),
			description: fmt.Sprintf("ID %d - %s", choice.Key.ID, choice.Key.Fingerprint),
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
	return "provision " + strings.TrimSpace(name)
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
	cloudCreatedAt := time.Now().UTC()
	if created.CreatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, created.CreatedAt); err == nil {
			cloudCreatedAt = parsed
		}
	}
	profile, err := store.Create(Profile{
		Name:           firstNonEmpty(created.Name, config.Name),
		IP:             created.IPv4,
		InitialSSHUser: "root",
		AdminUser:      "servestead",
		PrivateKeyPath: config.PrivateKeyPath,
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
	})
	if err != nil {
		return Profile{}, err
	}
	return profile, nil
}
