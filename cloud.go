package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
)

const (
	digitalOceanProviderName   = "digitalocean"
	defaultDigitalOceanRegion  = "nyc3"
	defaultDigitalOceanSize    = "s-1vcpu-1gb"
	defaultDigitalOceanImage   = "ubuntu-24-04-x64"
	digitalOceanAPIPollTimeout = 2 * time.Second
)

type provisionConfig struct {
	Name   string
	Region string
	Size   string
	Image  string
	SSHKey string
}

type server struct {
	ID           string
	Name         string
	IPv4         string
	Region       string
	Size         string
	Image        string
	PriceMonthly float64
	PriceHourly  float64
	CreatedAt    string
}

type cloudRegion struct {
	Slug      string
	Name      string
	Sizes     []string
	Available bool
}

type cloudSize struct {
	Slug         string
	Description  string
	MemoryMB     int
	VCPUs        int
	DiskGB       int
	TransferTB   float64
	PriceMonthly float64
	PriceHourly  float64
	Regions      []string
	Available    bool
}

type cloudImage struct {
	Slug         string
	Name         string
	Distribution string
	Status       string
	MinDiskGB    int
	Regions      []string
}

type cloudSSHKey struct {
	ID          int
	Name        string
	Fingerprint string
	PublicKey   string
}

type cloudCatalog struct {
	Regions []cloudRegion
	Sizes   []cloudSize
	Images  []cloudImage
	SSHKeys []cloudSSHKey
}

type cloudProvider interface {
	Catalog(context.Context) (cloudCatalog, error)
	Create(context.Context, provisionConfig) (server, error)
	CreateSSHKey(context.Context, string, string) (cloudSSHKey, error)
	Reboot(context.Context, string) error
	Destroy(context.Context, string) error
}

type digitalOceanProvider struct {
	droplets     godo.DropletsService
	actions      godo.DropletActionsService
	regions      godo.RegionsService
	sizes        godo.SizesService
	images       godo.ImagesService
	keys         godo.KeysService
	pollInterval time.Duration
}

func newDigitalOceanProvider(token string) *digitalOceanProvider {
	return newDigitalOceanProviderFromClient(godo.NewFromToken(token))
}

func newDigitalOceanProviderFromClient(client *godo.Client) *digitalOceanProvider {
	return &digitalOceanProvider{
		droplets:     client.Droplets,
		actions:      client.DropletActions,
		regions:      client.Regions,
		sizes:        client.Sizes,
		images:       client.Images,
		keys:         client.Keys,
		pollInterval: digitalOceanAPIPollTimeout,
	}
}

func (provider *digitalOceanProvider) Catalog(ctx context.Context) (cloudCatalog, error) {
	regions, err := provider.listRegions(ctx)
	if err != nil {
		return cloudCatalog{}, fmt.Errorf("list DigitalOcean regions: %w", err)
	}
	sizes, err := provider.listSizes(ctx)
	if err != nil {
		return cloudCatalog{}, fmt.Errorf("list DigitalOcean sizes: %w", err)
	}
	images, err := provider.listImages(ctx)
	if err != nil {
		return cloudCatalog{}, fmt.Errorf("list DigitalOcean images: %w", err)
	}
	keys, err := provider.listKeys(ctx)
	if err != nil {
		return cloudCatalog{}, fmt.Errorf("list DigitalOcean SSH keys: %w", err)
	}
	return cloudCatalog{Regions: regions, Sizes: sizes, Images: images, SSHKeys: keys}, nil
}

func (provider *digitalOceanProvider) Create(ctx context.Context, config provisionConfig) (server, error) {
	request := &godo.DropletCreateRequest{
		Name:   config.Name,
		Region: config.Region,
		Size:   config.Size,
		Image:  godo.DropletCreateImage{Slug: config.Image},
		SSHKeys: []godo.DropletCreateSSHKey{
			digitalOceanSSHKey(config.SSHKey),
		},
	}
	droplet, _, err := provider.droplets.Create(ctx, request)
	if err != nil {
		return server{}, err
	}
	if droplet == nil || droplet.ID == 0 {
		return server{}, errors.New("API response did not include a droplet ID")
	}
	return provider.waitForIPv4(ctx, droplet)
}

func (provider *digitalOceanProvider) CreateSSHKey(ctx context.Context, name, publicKey string) (cloudSSHKey, error) {
	created, _, err := provider.keys.Create(ctx, &godo.KeyCreateRequest{Name: name, PublicKey: publicKey})
	if err != nil {
		return cloudSSHKey{}, err
	}
	if created == nil || created.ID == 0 {
		return cloudSSHKey{}, errors.New("API response did not include an SSH key ID")
	}
	return cloudSSHKeyFromGodo(*created), nil
}

func (provider *digitalOceanProvider) Reboot(ctx context.Context, id string) error {
	dropletID, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || dropletID <= 0 {
		return fmt.Errorf("invalid DigitalOcean droplet ID %q", id)
	}
	_, _, err = provider.actions.Reboot(ctx, dropletID)
	return err
}

func (provider *digitalOceanProvider) Destroy(ctx context.Context, id string) error {
	dropletID, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || dropletID <= 0 {
		return fmt.Errorf("invalid DigitalOcean droplet ID %q", id)
	}
	_, err = provider.droplets.Delete(ctx, dropletID)
	return err
}

func (provider *digitalOceanProvider) waitForIPv4(ctx context.Context, created *godo.Droplet) (server, error) {
	for {
		if created.Status == "active" {
			ipv4, err := created.PublicIPv4()
			if err == nil && ipv4 != "" {
				return serverFromDigitalOceanDroplet(created, ipv4), nil
			}
		}
		if err := wait(ctx, provider.pollInterval); err != nil {
			return server{}, fmt.Errorf("wait for droplet %d: %w", created.ID, err)
		}
		next, _, err := provider.droplets.Get(ctx, created.ID)
		if err != nil {
			return server{}, err
		}
		if next == nil {
			return server{}, fmt.Errorf("API response did not include droplet %d", created.ID)
		}
		created = next
	}
}

func (provider *digitalOceanProvider) listRegions(ctx context.Context) ([]cloudRegion, error) {
	var result []cloudRegion
	options := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		regions, response, err := provider.regions.List(ctx, options)
		if err != nil {
			return nil, err
		}
		for _, region := range regions {
			result = append(result, cloudRegion{
				Slug:      region.Slug,
				Name:      region.Name,
				Sizes:     append([]string(nil), region.Sizes...),
				Available: region.Available,
			})
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			break
		}
		options.Page++
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Available != result[j].Available {
			return result[i].Available
		}
		return result[i].Slug < result[j].Slug
	})
	return result, nil
}

func (provider *digitalOceanProvider) listSizes(ctx context.Context) ([]cloudSize, error) {
	var result []cloudSize
	options := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		sizes, response, err := provider.sizes.List(ctx, options)
		if err != nil {
			return nil, err
		}
		for _, size := range sizes {
			result = append(result, cloudSize{
				Slug:         size.Slug,
				Description:  size.Description,
				MemoryMB:     size.Memory,
				VCPUs:        size.Vcpus,
				DiskGB:       size.Disk,
				TransferTB:   size.Transfer,
				PriceMonthly: size.PriceMonthly,
				PriceHourly:  size.PriceHourly,
				Regions:      append([]string(nil), size.Regions...),
				Available:    size.Available,
			})
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			break
		}
		options.Page++
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PriceMonthly != result[j].PriceMonthly {
			return result[i].PriceMonthly < result[j].PriceMonthly
		}
		return result[i].Slug < result[j].Slug
	})
	return result, nil
}

func (provider *digitalOceanProvider) listImages(ctx context.Context) ([]cloudImage, error) {
	var result []cloudImage
	options := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		images, response, err := provider.images.ListDistribution(ctx, options)
		if err != nil {
			return nil, err
		}
		for _, image := range images {
			if !isUsableDigitalOceanUbuntuImage(image) {
				continue
			}
			result = append(result, cloudImage{
				Slug:         image.Slug,
				Name:         image.Name,
				Distribution: image.Distribution,
				Status:       image.Status,
				MinDiskGB:    image.MinDiskSize,
				Regions:      append([]string(nil), image.Regions...),
			})
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			break
		}
		options.Page++
	}
	sort.Slice(result, func(i, j int) bool { return lessDigitalOceanImage(result[i], result[j]) })
	return result, nil
}

func isUsableDigitalOceanUbuntuImage(image godo.Image) bool {
	if !strings.EqualFold(image.Distribution, "Ubuntu") || image.Slug == "" {
		return false
	}
	return image.Status == "" || image.Status == "available"
}

func lessDigitalOceanImage(left, right cloudImage) bool {
	if left.Slug == defaultDigitalOceanImage {
		return true
	}
	if right.Slug == defaultDigitalOceanImage {
		return false
	}
	return left.Slug < right.Slug
}

func (provider *digitalOceanProvider) listKeys(ctx context.Context) ([]cloudSSHKey, error) {
	var result []cloudSSHKey
	options := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		keys, response, err := provider.keys.List(ctx, options)
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			result = append(result, cloudSSHKeyFromGodo(key))
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			break
		}
		options.Page++
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func serverFromDigitalOceanDroplet(droplet *godo.Droplet, ipv4 string) server {
	result := server{
		ID:        strconv.Itoa(droplet.ID),
		Name:      droplet.Name,
		IPv4:      ipv4,
		Size:      droplet.SizeSlug,
		CreatedAt: droplet.Created,
	}
	if droplet.Region != nil {
		result.Region = droplet.Region.Slug
	}
	if droplet.Image != nil {
		result.Image = firstNonEmpty(droplet.Image.Slug, droplet.Image.Name)
	}
	if droplet.Size != nil {
		result.Size = firstNonEmpty(result.Size, droplet.Size.Slug)
		result.PriceMonthly = droplet.Size.PriceMonthly
		result.PriceHourly = droplet.Size.PriceHourly
	}
	return result
}

func cloudSSHKeyFromGodo(key godo.Key) cloudSSHKey {
	return cloudSSHKey{
		ID:          key.ID,
		Name:        key.Name,
		Fingerprint: key.Fingerprint,
		PublicKey:   strings.TrimSpace(key.PublicKey),
	}
}

func digitalOceanSSHKey(value string) godo.DropletCreateSSHKey {
	value = strings.TrimSpace(value)
	if id, err := strconv.Atoi(value); err == nil {
		return godo.DropletCreateSSHKey{ID: id}
	}
	return godo.DropletCreateSSHKey{Fingerprint: value}
}

func wait(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (config *provisionConfig) withDefaults(region, size, image string) {
	if config.Region == "" {
		config.Region = region
	}
	if config.Size == "" {
		config.Size = size
	}
	if config.Image == "" {
		config.Image = image
	}
}
