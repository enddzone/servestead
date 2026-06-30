package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	runStatusPlanned  = "planned"
	runStatusRunning  = "running"
	runStatusComplete = "complete"
	runStatusFailed   = "failed"

	stageStatusPending  = "pending"
	stageStatusRunning  = "running"
	stageStatusComplete = "complete"
	stageStatusFailed   = "failed"
)

type Profile struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name"`
	IP                   string        `json:"ip"`
	InitialSSHUser       string        `json:"initial_ssh_user"`
	AdminUser            string        `json:"admin_user"`
	PrivateKeyPath       string        `json:"private_key_path"`
	BaseDomain           string        `json:"base_domain,omitempty"`
	LetsEncryptEmail     string        `json:"lets_encrypt_email,omitempty"`
	PangolinAdminEmail   string        `json:"pangolin_admin_email,omitempty"`
	ConfigRepositoryPath string        `json:"config_repository_path,omitempty"`
	Cloud                *ProfileCloud `json:"cloud,omitempty"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
}

type ProfileCloud struct {
	Provider     string     `json:"provider"`
	ResourceID   string     `json:"resource_id"`
	Name         string     `json:"name,omitempty"`
	Region       string     `json:"region,omitempty"`
	Size         string     `json:"size,omitempty"`
	Image        string     `json:"image,omitempty"`
	PriceMonthly float64    `json:"price_monthly,omitempty"`
	PriceHourly  float64    `json:"price_hourly,omitempty"`
	CreatedAt    time.Time  `json:"created_at,omitempty"`
	DestroyedAt  *time.Time `json:"destroyed_at,omitempty"`
}

type ProfileSummary struct {
	ID         string
	Name       string
	IP         string
	LastStatus string
	UpdatedAt  time.Time
}

type ProfileState struct {
	ActiveRunID           string              `json:"active_run_id,omitempty"`
	StackRepositoryCommit string              `json:"stack_repository_commit,omitempty"`
	Runs                  map[string]SetupRun `json:"runs"`
}

type SetupRun struct {
	ID        string                      `json:"id"`
	Status    string                      `json:"status"`
	Stages    map[string]SetupStageStatus `json:"stages"`
	CreatedAt time.Time                   `json:"created_at"`
	UpdatedAt time.Time                   `json:"updated_at"`
}

type SetupStageStatus struct {
	Status      string    `json:"status"`
	LastStarted time.Time `json:"last_started,omitempty"`
	LastEnded   time.Time `json:"last_ended,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type ProfileSecrets struct {
	ServerSecret          string            `json:"server_secret"`
	PangolinSetupToken    string            `json:"pangolin_setup_token"`
	PangolinAdminPassword string            `json:"pangolin_admin_password"`
	NewtID                string            `json:"newt_id"`
	NewtSecret            string            `json:"newt_secret"`
	BeszelAdminPassword   string            `json:"beszel_admin_password"`
	BeszelSystemToken     string            `json:"beszel_system_token"`
	BeszelHubPrivateKey   string            `json:"beszel_hub_private_key"`
	BeszelHubPublicKey    string            `json:"beszel_hub_public_key"`
	StackEnvironments     map[string]string `json:"stack_environments,omitempty"`
}

func (secrets *ProfileSecrets) EnsureServerSecret() error {
	if secrets.ServerSecret != "" {
		return nil
	}
	generated, err := GenerateServerSecret()
	if err != nil {
		return err
	}
	secrets.ServerSecret = generated
	return nil
}

func GenerateServerSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (secrets *ProfileSecrets) EnsurePangolinSetupToken() error {
	if secrets.PangolinSetupToken != "" {
		return nil
	}
	generated, err := GeneratePangolinSetupToken()
	if err != nil {
		return err
	}
	secrets.PangolinSetupToken = generated
	return nil
}

func GeneratePangolinSetupToken() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	token := make([]byte, 32)
	for i := range token {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		token[i] = alphabet[index.Int64()]
	}
	return string(token), nil
}

func (secrets *ProfileSecrets) EnsureComposeWiringSecrets() error {
	if secrets.PangolinAdminPassword == "" {
		generated, err := generatePassword(32)
		if err != nil {
			return err
		}
		secrets.PangolinAdminPassword = generated
	}
	generators := []struct {
		value *string
		size  int
	}{
		{&secrets.NewtID, 15},
		{&secrets.NewtSecret, 48},
		{&secrets.BeszelAdminPassword, 32},
		{&secrets.BeszelSystemToken, 48},
	}
	for _, item := range generators {
		if *item.value != "" {
			continue
		}
		generated, err := generateLowercaseSecret(item.size)
		if err != nil {
			return err
		}
		*item.value = generated
	}
	if secrets.BeszelHubPrivateKey == "" || secrets.BeszelHubPublicKey == "" {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		privateBlock, err := ssh.MarshalPrivateKey(privateKey, "servestead-beszel")
		if err != nil {
			return err
		}
		sshPublicKey, err := ssh.NewPublicKey(publicKey)
		if err != nil {
			return err
		}
		secrets.BeszelHubPrivateKey = string(pem.EncodeToMemory(privateBlock))
		secrets.BeszelHubPublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublicKey)))
	}
	return nil
}

func pangolinPasswordValid(password string) bool {
	if len(password) < 8 || len(password) > 128 {
		return false
	}
	var upper, lower, digit, special bool
	for _, character := range password {
		switch {
		case character >= 'A' && character <= 'Z':
			upper = true
		case character >= 'a' && character <= 'z':
			lower = true
		case character >= '0' && character <= '9':
			digit = true
		case strings.ContainsRune("~!`@#$%^&*()_-+={}[]|\\:;\"'<>,./?", character):
			special = true
		}
	}
	return upper && lower && digit && special
}

func generatePassword(length int) (string, error) {
	if length < 8 {
		return "", errors.New("password length must be at least 8")
	}
	suffix, err := generateLowercaseSecret(length - 4)
	if err != nil {
		return "", err
	}
	return "Aa1!" + suffix, nil
}

func generateLowercaseSecret(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := make([]byte, length)
	for i := range value {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		value[i] = alphabet[index.Int64()]
	}
	return string(value), nil
}

type ProfileStore interface {
	List() ([]ProfileSummary, error)
	ResolveByIP(ip string) ([]ProfileSummary, error)
	Create(Profile) (Profile, error)
	Load(id string) (Profile, ProfileState, error)
	Save(Profile, ProfileState) error
	Delete(id string) error
	LoadSecrets(id string) (ProfileSecrets, error)
	SaveSecrets(id string, secrets ProfileSecrets) error
	AppendRunEvent(profileID string, runID string, event TaskEvent) error
}

type fileProfileStore struct {
	root        string
	defaultRoot bool
}

func newDefaultProfileStore() (ProfileStore, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user config directory: %w", err)
	}
	return &fileProfileStore{root: filepath.Join(configDirectory, "servestead"), defaultRoot: true}, nil
}

func newFileProfileStore(root string) *fileProfileStore {
	return &fileProfileStore{root: root}
}

func (store *fileProfileStore) List() ([]ProfileSummary, error) {
	entries, err := os.ReadDir(store.profilesDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := []ProfileSummary{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profile, state, err := store.Load(entry.Name())
		if err != nil {
			return nil, err
		}
		summary := ProfileSummary{
			ID:        profile.ID,
			Name:      profile.Name,
			IP:        profile.IP,
			UpdatedAt: profile.UpdatedAt,
		}
		if run, ok := state.Runs[state.ActiveRunID]; ok {
			summary.LastStatus = run.Status
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
	})
	return summaries, nil
}

func (store *fileProfileStore) ResolveByIP(ip string) ([]ProfileSummary, error) {
	summaries, err := store.List()
	if err != nil {
		return nil, err
	}
	matches := []ProfileSummary{}
	for _, summary := range summaries {
		if summary.IP == ip {
			matches = append(matches, summary)
		}
	}
	return matches, nil
}

func (store *fileProfileStore) Create(profile Profile) (Profile, error) {
	now := time.Now().UTC()
	if profile.ID == "" {
		profile.ID = newProfileID(profile.IP, now)
	}
	if profile.Name == "" {
		profile.Name = profile.IP
	}
	if profile.InitialSSHUser == "" {
		profile.InitialSSHUser = "root"
	}
	if profile.AdminUser == "" {
		profile.AdminUser = "servestead"
	}
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	state := ProfileState{Runs: map[string]SetupRun{}}
	if err := store.Save(profile, state); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (store *fileProfileStore) Load(id string) (Profile, ProfileState, error) {
	var profile Profile
	if err := readJSONFile(store.profilePath(id), &profile); err != nil {
		return Profile{}, ProfileState{}, err
	}
	var state ProfileState
	if err := readJSONFile(store.statePath(id), &state); err != nil {
		return Profile{}, ProfileState{}, err
	}
	if state.Runs == nil {
		state.Runs = map[string]SetupRun{}
	}
	return profile, state, nil
}

func (store *fileProfileStore) Save(profile Profile, state ProfileState) error {
	if profile.ID == "" {
		return errors.New("profile ID is required")
	}
	if state.Runs == nil {
		state.Runs = map[string]SetupRun{}
	}
	now := time.Now().UTC()
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	directory := store.profileDirectory(profile.ID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	if err := atomicWriteJSON(store.profilePath(profile.ID), profile); err != nil {
		return err
	}
	if err := atomicWriteJSON(store.statePath(profile.ID), state); err != nil {
		return err
	}
	return nil
}

func (store *fileProfileStore) Delete(id string) error {
	if id == "" {
		return errors.New("profile ID is required")
	}
	if filepath.Base(id) != id || id == "." || id == ".." {
		return errors.New("profile ID must not contain path separators")
	}
	return os.RemoveAll(store.profileDirectory(id))
}

func (store *fileProfileStore) LoadSecrets(id string) (ProfileSecrets, error) {
	var secrets ProfileSecrets
	if err := readJSONFile(store.secretsPath(id), &secrets); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProfileSecrets{}, nil
		}
		return ProfileSecrets{}, err
	}
	return secrets, nil
}

func (store *fileProfileStore) SaveSecrets(id string, secrets ProfileSecrets) error {
	directory := store.profileDirectory(id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	return atomicWriteJSON(store.secretsPath(id), secrets)
}

func (store *fileProfileStore) AppendRunEvent(profileID string, runID string, event TaskEvent) error {
	directory := filepath.Join(store.profileDirectory(profileID), "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(directory, runID+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := writeTaskEventJSONL(file, event); err != nil {
		return err
	}
	return file.Sync()
}

func (store *fileProfileStore) profilesDirectory() string {
	return filepath.Join(store.root, "profiles")
}

func (store *fileProfileStore) profileDirectory(id string) string {
	return filepath.Join(store.profilesDirectory(), id)
}

func (store *fileProfileStore) profilePath(id string) string {
	return filepath.Join(store.profileDirectory(id), "profile.json")
}

func (store *fileProfileStore) statePath(id string) string {
	return filepath.Join(store.profileDirectory(id), "state.json")
}

func (store *fileProfileStore) secretsPath(id string) string {
	return filepath.Join(store.profileDirectory(id), "secrets.json")
}

func newProfileID(ip string, now time.Time) string {
	replacer := strings.NewReplacer(".", "-", ":", "-", "[", "", "]", "")
	safeIP := replacer.Replace(ip)
	return strings.ToLower(safeIP) + "-" + now.Format("20060102t150405.000000000z")
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse JSON %s: %w", path, err)
	}
	return nil
}

func atomicWriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(path, data, 0600)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return os.Chmod(path, mode)
}
