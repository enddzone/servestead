package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	filage "filippo.io/age"
	sops "github.com/getsops/sops/v3"
	sopsaes "github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	sopsconfig "github.com/getsops/sops/v3/config"
	sopsyaml "github.com/getsops/sops/v3/stores/yaml"
)

const (
	stackSecretProviderAge = "age"
	stackSecretRuntimeSink = "dockhand"
	stackSecretRuntimeMode = "env"
	stackSecretFilename    = "servestead.secrets.yaml"
	stackSecretSOPSVersion = "3.13.2"
)

type SecretSet map[string]string

type SecretCapabilities struct {
	Read   bool
	Write  bool
	Delete bool
	List   bool
}

type StackSecretRef struct {
	RepositoryPath string
	StackName      string
	Source         string
	Recipients     []string
	Identity       string
}

type SecretKeyMeta struct {
	Name     string
	Required bool
}

type StackRuntimeTarget struct {
	StackName string
	Sink      string
	Mode      string
}

type SecretProvider interface {
	Name() string
	Capabilities() SecretCapabilities
	GetStackSecrets(ctx context.Context, ref StackSecretRef) (SecretSet, error)
	PutStackSecrets(ctx context.Context, ref StackSecretRef, values SecretSet) error
	DeleteStackSecrets(ctx context.Context, ref StackSecretRef, keys []string) error
	ListStackSecretKeys(ctx context.Context, ref StackSecretRef) ([]SecretKeyMeta, error)
}

type RuntimeSecretSink interface {
	Name() string
	ReconcileStackSecrets(ctx context.Context, target StackRuntimeTarget, secrets SecretSet) error
	PreserveExisting(ctx context.Context, target StackRuntimeTarget, keys []string) error
	DeleteStackSecrets(ctx context.Context, target StackRuntimeTarget, keys []string) error
}

type stackSecretMetadata struct {
	Provider   string                     `yaml:"provider,omitempty"`
	Source     string                     `yaml:"source,omitempty"`
	Recipients []string                   `yaml:"recipients,omitempty"`
	Runtime    stackSecretRuntimeMetadata `yaml:"runtime,omitempty"`
	Keys       []stackSecretKeyMetadata   `yaml:"keys,omitempty"`
}

type stackSecretRuntimeMetadata struct {
	Sink string `yaml:"sink,omitempty"`
	Mode string `yaml:"mode,omitempty"`
}

type stackSecretKeyMetadata struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
}

func (metadata stackSecretMetadata) IsZero() bool {
	return metadata.Provider == "" && metadata.Source == "" && len(metadata.Recipients) == 0 && metadata.Runtime.IsZero() && len(metadata.Keys) == 0
}

func (metadata stackSecretRuntimeMetadata) IsZero() bool {
	return metadata.Sink == "" && metadata.Mode == ""
}

func (metadata stackSecretMetadata) HasSecrets() bool {
	return !metadata.IsZero()
}

func (metadata stackSecretMetadata) Ref(repositoryPath, stackName, identity string) StackSecretRef {
	return StackSecretRef{
		RepositoryPath: repositoryPath,
		StackName:      stackName,
		Source:         metadata.Source,
		Recipients:     append([]string(nil), metadata.Recipients...),
		Identity:       identity,
	}
}

func (metadata stackSecretMetadata) KeyNames() []string {
	keys := make([]string, 0, len(metadata.Keys))
	for _, key := range metadata.Keys {
		if key.Name != "" {
			keys = append(keys, key.Name)
		}
	}
	sort.Strings(keys)
	return keys
}

func ageStackSecretMetadata(stackName string, values SecretSet, recipient string) stackSecretMetadata {
	keys := secretSetKeys(values)
	metas := make([]stackSecretKeyMetadata, 0, len(keys))
	for _, key := range keys {
		metas = append(metas, stackSecretKeyMetadata{Name: key, Required: true})
	}
	return stackSecretMetadata{
		Provider:   stackSecretProviderAge,
		Source:     defaultStackSecretSource(stackName),
		Recipients: []string{recipient},
		Runtime: stackSecretRuntimeMetadata{
			Sink: stackSecretRuntimeSink,
			Mode: stackSecretRuntimeMode,
		},
		Keys: metas,
	}
}

func defaultStackSecretSource(stackName string) string {
	return filepath.ToSlash(filepath.Join("stacks", stackName, stackSecretFilename))
}

func validateStackSecretMetadata(stackName string, metadata stackSecretMetadata) error {
	if metadata.IsZero() {
		return nil
	}
	if metadata.Provider != stackSecretProviderAge {
		return fmt.Errorf("secrets provider must be %q", stackSecretProviderAge)
	}
	if metadata.Source != defaultStackSecretSource(stackName) {
		return fmt.Errorf("secrets source must be %s", defaultStackSecretSource(stackName))
	}
	if len(metadata.Recipients) == 0 {
		return errors.New("secrets recipients are required")
	}
	seenRecipients := map[string]bool{}
	for _, recipient := range metadata.Recipients {
		if _, err := sopsage.MasterKeyFromRecipient(recipient); err != nil {
			return fmt.Errorf("secrets recipient %q is invalid: %w", recipient, err)
		}
		if seenRecipients[recipient] {
			return fmt.Errorf("secrets recipient %q is duplicated", recipient)
		}
		seenRecipients[recipient] = true
	}
	if metadata.Runtime.Sink != stackSecretRuntimeSink {
		return fmt.Errorf("secrets runtime sink must be %q", stackSecretRuntimeSink)
	}
	if metadata.Runtime.Mode != stackSecretRuntimeMode {
		return fmt.Errorf("secrets runtime mode must be %q", stackSecretRuntimeMode)
	}
	if len(metadata.Keys) == 0 {
		return errors.New("secrets keys are required")
	}
	seen := map[string]bool{}
	for _, key := range metadata.Keys {
		if !environmentKeyPattern.MatchString(key.Name) {
			return fmt.Errorf("secret key %q must be a valid environment variable name", key.Name)
		}
		if seen[key.Name] {
			return fmt.Errorf("secret key %q is duplicated", key.Name)
		}
		seen[key.Name] = true
	}
	return nil
}

func secretSetKeys(values SecretSet) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (values SecretSet) Redacted() map[string]string {
	redacted := map[string]string{}
	for _, key := range secretSetKeys(values) {
		redacted[key] = "********"
	}
	return redacted
}

func validateSecretSet(values SecretSet) error {
	for key := range values {
		if !environmentKeyPattern.MatchString(key) {
			return fmt.Errorf("secret key %q must be a valid environment variable name", key)
		}
	}
	return nil
}

type ageSecretProvider struct{}

func newAgeSecretProvider() SecretProvider {
	return ageSecretProvider{}
}

func (provider ageSecretProvider) Name() string {
	return stackSecretProviderAge
}

func (provider ageSecretProvider) Capabilities() SecretCapabilities {
	return SecretCapabilities{Read: true, Write: true, Delete: true, List: true}
}

func (provider ageSecretProvider) GetStackSecrets(_ context.Context, ref StackSecretRef) (SecretSet, error) {
	path, err := stackSecretPath(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	identity, err := parseStackSecretIdentity(ref.Identity)
	if err != nil {
		return nil, err
	}
	tree, err := decryptSOPSStackSecrets(data, identity)
	if err != nil {
		return nil, err
	}
	return secretSetFromSOPSTree(tree)
}

func (provider ageSecretProvider) PutStackSecrets(_ context.Context, ref StackSecretRef, values SecretSet) error {
	if err := validateSecretSet(values); err != nil {
		return err
	}
	path, err := stackSecretPath(ref)
	if err != nil {
		return err
	}
	if _, err := parseStackSecretRecipients(ref.Recipients); err != nil {
		return err
	}
	data, err := encryptSOPSStackSecrets(path, ref.Recipients, values)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0600)
}

func (provider ageSecretProvider) DeleteStackSecrets(ctx context.Context, ref StackSecretRef, keys []string) error {
	if len(keys) == 0 {
		path, err := stackSecretPath(ref)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	values, err := provider.GetStackSecrets(ctx, ref)
	if err != nil {
		return err
	}
	for _, key := range keys {
		delete(values, key)
	}
	if len(values) == 0 {
		return provider.DeleteStackSecrets(ctx, ref, nil)
	}
	return provider.PutStackSecrets(ctx, ref, values)
}

func (provider ageSecretProvider) ListStackSecretKeys(_ context.Context, ref StackSecretRef) ([]SecretKeyMeta, error) {
	path, err := stackSecretPath(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tree, err := stackSOPSYAMLStore().LoadEncryptedFile(data)
	if err != nil {
		return nil, err
	}
	keys, err := secretKeysFromSOPSTree(tree)
	if err != nil {
		return nil, err
	}
	metas := make([]SecretKeyMeta, 0, len(keys))
	for _, key := range keys {
		metas = append(metas, SecretKeyMeta{Name: key, Required: true})
	}
	return metas, nil
}

func encryptSOPSStackSecrets(path string, recipients []string, values SecretSet) ([]byte, error) {
	keyGroup := sops.KeyGroup{}
	for _, recipient := range recipients {
		key, err := sopsage.MasterKeyFromRecipient(recipient)
		if err != nil {
			return nil, err
		}
		keyGroup = append(keyGroup, key)
	}
	tree := sops.Tree{
		Branches: sops.TreeBranches{sopsBranchFromSecretSet(values)},
		Metadata: sops.Metadata{
			EncryptedRegex: ".*",
			KeyGroups:      []sops.KeyGroup{keyGroup},
			Version:        stackSecretSOPSVersion,
		},
		FilePath: path,
	}
	dataKey, errs := tree.GenerateDataKey()
	if len(errs) > 0 {
		return nil, fmt.Errorf("generate SOPS data key: %w", errors.Join(errs...))
	}
	cipher := sopsaes.NewCipher()
	unencryptedMAC, err := tree.Encrypt(dataKey, cipher)
	if err != nil {
		return nil, fmt.Errorf("encrypt SOPS tree: %w", err)
	}
	tree.Metadata.LastModified = time.Now().UTC()
	tree.Metadata.MessageAuthenticationCode, err = cipher.Encrypt(
		unencryptedMAC,
		dataKey,
		tree.Metadata.LastModified.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("encrypt SOPS MAC: %w", err)
	}
	data, err := stackSOPSYAMLStore().EmitEncryptedFile(tree)
	if err != nil {
		return nil, fmt.Errorf("emit SOPS YAML: %w", err)
	}
	return data, nil
}

var sopsAgeKeyEnvMutex sync.Mutex

func decryptSOPSStackSecrets(data []byte, identity string) (sops.Tree, error) {
	store := stackSOPSYAMLStore()
	tree, err := store.LoadEncryptedFile(data)
	if err != nil {
		return sops.Tree{}, err
	}
	cipher := sopsaes.NewCipher()
	err = withSOPSAgeKey(identity, func() error {
		dataKey, err := tree.Metadata.GetDataKey()
		if err != nil {
			return err
		}
		mac, err := tree.Decrypt(dataKey, cipher)
		if err != nil {
			return err
		}
		originalMAC, err := cipher.Decrypt(
			tree.Metadata.MessageAuthenticationCode,
			dataKey,
			tree.Metadata.LastModified.Format(time.RFC3339),
		)
		if err != nil {
			return fmt.Errorf("decrypt SOPS MAC: %w", err)
		}
		if originalMAC != mac {
			return fmt.Errorf("SOPS MAC mismatch: expected %q, got %q", originalMAC, mac)
		}
		return nil
	})
	if err != nil {
		return sops.Tree{}, err
	}
	return tree, nil
}

func withSOPSAgeKey(identity string, fn func() error) error {
	sopsAgeKeyEnvMutex.Lock()
	defer sopsAgeKeyEnvMutex.Unlock()
	previous, hadPrevious := os.LookupEnv(sopsage.SopsAgeKeyEnv)
	if err := os.Setenv(sopsage.SopsAgeKeyEnv, identity); err != nil {
		return err
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv(sopsage.SopsAgeKeyEnv, previous)
		} else {
			_ = os.Unsetenv(sopsage.SopsAgeKeyEnv)
		}
	}()
	return fn()
}

func stackSOPSYAMLStore() *sopsyaml.Store {
	config := sopsconfig.NewStoresConfig()
	return sopsyaml.NewStore(&config.YAML)
}

func sopsBranchFromSecretSet(values SecretSet) sops.TreeBranch {
	branch := sops.TreeBranch{}
	for _, key := range secretSetKeys(values) {
		branch = append(branch, sops.TreeItem{Key: key, Value: values[key]})
	}
	return branch
}

func secretSetFromSOPSTree(tree sops.Tree) (SecretSet, error) {
	values := SecretSet{}
	for _, branch := range tree.Branches {
		for _, item := range branch {
			key, ok := item.Key.(string)
			if !ok {
				continue
			}
			value, ok := item.Value.(string)
			if !ok {
				return nil, fmt.Errorf("SOPS secret %s is not a string", key)
			}
			values[key] = value
		}
	}
	return values, validateSecretSet(values)
}

func secretKeysFromSOPSTree(tree sops.Tree) ([]string, error) {
	values := SecretSet{}
	for _, branch := range tree.Branches {
		for _, item := range branch {
			key, ok := item.Key.(string)
			if !ok {
				continue
			}
			if !environmentKeyPattern.MatchString(key) {
				return nil, fmt.Errorf("secret key %q must be a valid environment variable name", key)
			}
			values[key] = ""
		}
	}
	return secretSetKeys(values), nil
}

func stackSecretPath(ref StackSecretRef) (string, error) {
	if ref.RepositoryPath == "" {
		return "", errors.New("repository path is required")
	}
	if !stackSlugPattern.MatchString(ref.StackName) {
		return "", errors.New("stack name must be a lowercase DNS label")
	}
	if ref.Source != defaultStackSecretSource(ref.StackName) {
		return "", fmt.Errorf("secrets source must be %s", defaultStackSecretSource(ref.StackName))
	}
	return filepath.Join(expandUserPath(ref.RepositoryPath), filepath.FromSlash(ref.Source)), nil
}

var secretProviderForName = defaultSecretProviderForName

func defaultSecretProviderForName(name string) (SecretProvider, error) {
	switch name {
	case stackSecretProviderAge:
		return newAgeSecretProvider(), nil
	default:
		return nil, fmt.Errorf("unknown secret provider %q", name)
	}
}

func parseStackSecretRecipients(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one stack secret recipient is required")
	}
	recipients := make([]string, 0, len(values))
	for _, value := range values {
		recipient := strings.TrimSpace(value)
		if _, err := sopsage.MasterKeyFromRecipient(recipient); err != nil {
			return nil, err
		}
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func parseStackSecretIdentity(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("profile has no stack secret identity; run servestead secrets init --profile <id>")
	}
	if _, err := filage.ParseX25519Identity(value); err != nil {
		return "", fmt.Errorf("profile stack secret identity is invalid: %w", err)
	}
	return value, nil
}

func generateStackSecretIdentity() (identity string, recipient string, err error) {
	generated, err := filage.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return generated.String(), generated.Recipient().String(), nil
}

func (secrets *ProfileSecrets) EnsureStackSecretIdentity() (recipient string, created bool, err error) {
	if strings.TrimSpace(secrets.StackSecretIdentity) == "" {
		identity, recipient, err := generateStackSecretIdentity()
		if err != nil {
			return "", false, err
		}
		secrets.StackSecretIdentity = identity
		secrets.StackSecretRecipient = recipient
		return recipient, true, nil
	}
	identity, err := filage.ParseX25519Identity(strings.TrimSpace(secrets.StackSecretIdentity))
	if err != nil {
		return "", false, fmt.Errorf("profile stack secret identity is invalid: %w", err)
	}
	recipient = identity.Recipient().String()
	if secrets.StackSecretRecipient != recipient {
		secrets.StackSecretRecipient = recipient
		return recipient, true, nil
	}
	return recipient, false, nil
}

func (secrets *ProfileSecrets) SetStackSecretIdentity(identityValue string) (string, error) {
	identity, err := filage.ParseX25519Identity(strings.TrimSpace(identityValue))
	if err != nil {
		return "", fmt.Errorf("stack secret identity is invalid: %w", err)
	}
	secrets.StackSecretIdentity = identity.String()
	secrets.StackSecretRecipient = identity.Recipient().String()
	return secrets.StackSecretRecipient, nil
}

func (secrets ProfileSecrets) StackSecretIdentityPair() (identity string, recipient string, err error) {
	parsed, err := filage.ParseX25519Identity(strings.TrimSpace(secrets.StackSecretIdentity))
	if err != nil {
		if strings.TrimSpace(secrets.StackSecretIdentity) == "" {
			return "", "", errors.New("profile has no stack secret identity; run servestead secrets init --profile <id>")
		}
		return "", "", fmt.Errorf("profile stack secret identity is invalid: %w", err)
	}
	return parsed.String(), parsed.Recipient().String(), nil
}

func ensureProfileStackSecretIdentity(store ProfileStore, profileID string) (ProfileSecrets, string, string, error) {
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return ProfileSecrets{}, "", "", err
	}
	recipient, changed, err := secrets.EnsureStackSecretIdentity()
	if err != nil {
		return ProfileSecrets{}, "", "", err
	}
	if changed {
		if err := store.SaveSecrets(profileID, secrets); err != nil {
			return ProfileSecrets{}, "", "", err
		}
	}
	return secrets, secrets.StackSecretIdentity, recipient, nil
}

func parseEnvironmentSecretSet(content string) (SecretSet, []string, error) {
	if strings.IndexByte(content, 0) >= 0 {
		return nil, nil, errors.New("environment file contains a NUL byte")
	}
	values := SecretSet{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, rawValue, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !environmentKeyPattern.MatchString(key) {
			continue
		}
		values[key] = parseEnvironmentValue(rawValue)
	}
	keys := secretSetKeys(values)
	return values, keys, nil
}

func parseEnvironmentValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
			return value[1 : len(value)-1]
		}
	}
	return value
}
