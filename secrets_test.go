package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sopsdecrypt "github.com/getsops/sops/v3/decrypt"
)

func TestAgeProviderEncryptsWithoutPlaintextAndDecrypts(t *testing.T) {
	provider := newAgeSecretProvider()
	ref := newAgeProviderTestRef(t)
	secrets := SecretSet{"API_KEY": "secret-value", "TOKEN": "second-secret"}
	if err := provider.PutStackSecrets(context.Background(), ref, secrets); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ref.RepositoryPath, filepath.FromSlash(defaultStackSecretSource("site")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertAgeSecretFileEncrypted(t, data)
	assertAgeSecretFileMode(t, path)
	assertAgeProviderDecrypts(t, provider, ref)
	assertSOPSDecryptsAgeSecretFile(t, ref.Identity, data)
	assertAgeProviderListsKeys(t, provider, ref)
}

func newAgeProviderTestRef(t *testing.T) StackSecretRef {
	t.Helper()
	identity, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return StackSecretRef{
		RepositoryPath: t.TempDir(),
		StackName:      "site",
		Source:         defaultStackSecretSource("site"),
		Recipients:     []string{recipient},
		Identity:       identity,
	}
}

func assertAgeSecretFileEncrypted(t *testing.T, data []byte) {
	t.Helper()
	for _, leaked := range []string{"secret-value", "second-secret"} {
		if bytes.Contains(data, []byte(leaked)) {
			t.Fatalf("secret file leaked plaintext %q:\n%s", leaked, data)
		}
	}
	for _, expected := range []string{"sops:", "age:", "encrypted_regex: .*", "API_KEY: ENC[AES256_GCM", "TOKEN: ENC[AES256_GCM"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("secret file missing %q:\n%s", expected, data)
		}
	}
}

func assertAgeSecretFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret file mode = %o, want 0600", info.Mode().Perm())
	}
}

func assertAgeProviderDecrypts(t *testing.T, provider SecretProvider, ref StackSecretRef) {
	t.Helper()
	values, err := provider.GetStackSecrets(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if values["API_KEY"] != "secret-value" || values["TOKEN"] != "second-secret" {
		t.Fatalf("unexpected decrypted values: %+v", values.Redacted())
	}
}

func assertSOPSDecryptsAgeSecretFile(t *testing.T, identity string, data []byte) {
	t.Helper()
	var sopsPlaintext []byte
	err := withSOPSAgeKey(identity, func() error {
		var decryptErr error
		sopsPlaintext, decryptErr = sopsdecrypt.Data(data, "yaml")
		return decryptErr
	})
	if err != nil {
		t.Fatalf("SOPS decrypt API could not decrypt stack secret file: %v", err)
	}
	if !bytes.Contains(sopsPlaintext, []byte("API_KEY: secret-value")) ||
		!bytes.Contains(sopsPlaintext, []byte("TOKEN: second-secret")) {
		t.Fatalf("SOPS decrypt API returned unexpected plaintext:\n%s", sopsPlaintext)
	}
}

func assertAgeProviderListsKeys(t *testing.T, provider SecretProvider, ref StackSecretRef) {
	t.Helper()
	metas, err := provider.ListStackSecretKeys(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].Name != "API_KEY" || metas[1].Name != "TOKEN" {
		t.Fatalf("unexpected key metadata: %+v", metas)
	}
}

func TestAgeProviderDeletesStackSecrets(t *testing.T) {
	repository := t.TempDir()
	identity, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	provider := newAgeSecretProvider()
	ref := StackSecretRef{
		RepositoryPath: repository,
		StackName:      "site",
		Source:         defaultStackSecretSource("site"),
		Recipients:     []string{recipient},
		Identity:       identity,
	}
	if err := provider.PutStackSecrets(context.Background(), ref, SecretSet{"API_KEY": "secret-value"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository, filepath.FromSlash(defaultStackSecretSource("site")))
	if err := provider.DeleteStackSecrets(context.Background(), ref, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("secret file should be removed, got %v", err)
	}
}

func TestAgeProviderRejectsInvalidInputs(t *testing.T) {
	provider := newAgeSecretProvider()
	ref := newAgeProviderTestRef(t)
	ctx := context.Background()
	assertSecretErrorContains(t, provider.PutStackSecrets(ctx, ref, SecretSet{"bad-key": "secret"}), "valid environment variable name")
	badRef := ref
	badRef.RepositoryPath = ""
	assertSecretErrorContains(t, provider.PutStackSecrets(ctx, badRef, SecretSet{"API_KEY": "secret"}), "repository path")
	badRecipientRef := ref
	badRecipientRef.Recipients = []string{"not-age"}
	assertSecretError(t, provider.PutStackSecrets(ctx, badRecipientRef, SecretSet{"API_KEY": "secret"}))
	_, err := provider.GetStackSecrets(ctx, badRef)
	assertSecretErrorContains(t, err, "repository path")
	missingRef := ref
	missingRef.RepositoryPath = t.TempDir()
	_, err = provider.GetStackSecrets(ctx, missingRef)
	assertSecretError(t, err)
	invalidIdentityRef := ref
	path, err := stackSecretPath(invalidIdentityRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("not encrypted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	invalidIdentityRef.Identity = "not-an-age-identity"
	_, err = provider.GetStackSecrets(ctx, invalidIdentityRef)
	assertSecretErrorContains(t, err, "profile stack secret identity is invalid")
	assertSecretErrorContains(t, provider.DeleteStackSecrets(ctx, badRef, nil), "repository path")
	assertSecretError(t, provider.DeleteStackSecrets(ctx, missingRef, []string{"API_KEY"}))
	_, err = provider.ListStackSecretKeys(ctx, badRef)
	assertSecretErrorContains(t, err, "repository path")
	_, err = provider.ListStackSecretKeys(ctx, missingRef)
	assertSecretError(t, err)
	_, err = provider.ListStackSecretKeys(ctx, invalidIdentityRef)
	assertSecretError(t, err)
}

func assertSecretError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
}

func assertSecretErrorContains(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), expected) {
		t.Fatalf("expected %q error, got %v", expected, err)
	}
}

func TestProfileSecretsManageStackSecretIdentity(t *testing.T) {
	var secrets ProfileSecrets
	recipient, created, err := secrets.EnsureStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !created || recipient == "" || !strings.HasPrefix(secrets.StackSecretIdentity, "AGE-SECRET-KEY-") {
		t.Fatalf("identity was not generated: created=%v recipient=%q secrets=%+v", created, recipient, secrets)
	}
	identity, exportedRecipient, err := secrets.StackSecretIdentityPair()
	if err != nil {
		t.Fatal(err)
	}
	if identity != secrets.StackSecretIdentity || exportedRecipient != recipient {
		t.Fatalf("identity pair mismatch")
	}
	var imported ProfileSecrets
	importedRecipient, err := imported.SetStackSecretIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	if importedRecipient != recipient || imported.StackSecretRecipient != recipient {
		t.Fatalf("imported recipient mismatch: %+v", imported)
	}
}

func TestSecretParsingRejectsInvalidInputs(t *testing.T) {
	if _, err := parseStackSecretRecipients(nil); err == nil || !strings.Contains(err.Error(), "recipient is required") {
		t.Fatalf("missing recipient was accepted: %v", err)
	}
	if _, err := parseStackSecretRecipients([]string{"not-age"}); err == nil {
		t.Fatal("invalid recipient was accepted")
	}
	if _, err := parseStackSecretIdentity(""); err == nil || !strings.Contains(err.Error(), "no stack secret identity") {
		t.Fatalf("missing identity was accepted: %v", err)
	}
	if _, err := parseStackSecretIdentity("not-an-age-identity"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid identity was accepted: %v", err)
	}
	secrets := ProfileSecrets{StackSecretIdentity: "not-an-age-identity"}
	if _, _, err := secrets.EnsureStackSecretIdentity(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid stored identity was accepted: %v", err)
	}
	if _, _, err := secrets.StackSecretIdentityPair(); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid identity pair was accepted: %v", err)
	}
	if _, _, err := parseEnvironmentSecretSet("API_KEY=secret\x00"); err == nil || !strings.Contains(err.Error(), "NUL byte") {
		t.Fatalf("NUL byte environment was accepted: %v", err)
	}
	if value := parseEnvironmentValue(`"quoted secret"`); value != "quoted secret" {
		t.Fatalf("quoted environment value = %q", value)
	}
}

func TestValidateStackSecretMetadata(t *testing.T) {
	_, recipient, err := generateStackSecretIdentity()
	if err != nil {
		t.Fatal(err)
	}
	validMetadata := func() stackSecretMetadata {
		return ageStackSecretMetadata("site", SecretSet{"API_KEY": "secret"}, recipient)
	}
	if err := validateStackSecretMetadata("site", stackSecretMetadata{}); err != nil {
		t.Fatalf("zero metadata should be valid: %v", err)
	}
	if err := validateStackSecretMetadata("site", validMetadata()); err != nil {
		t.Fatalf("valid metadata was rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*stackSecretMetadata)
		want   string
	}{
		{name: "provider", mutate: func(metadata *stackSecretMetadata) { metadata.Provider = "vault" }, want: "secrets provider"},
		{name: "source", mutate: func(metadata *stackSecretMetadata) { metadata.Source = "stacks/other/servestead.secrets.yaml" }, want: "secrets source"},
		{name: "missing recipients", mutate: func(metadata *stackSecretMetadata) { metadata.Recipients = nil }, want: "secrets recipients are required"},
		{name: "invalid recipient", mutate: func(metadata *stackSecretMetadata) { metadata.Recipients = []string{"not-age"} }, want: "secrets recipient"},
		{name: "duplicate recipient", mutate: func(metadata *stackSecretMetadata) { metadata.Recipients = []string{recipient, recipient} }, want: "duplicated"},
		{name: "runtime sink", mutate: func(metadata *stackSecretMetadata) { metadata.Runtime.Sink = "file" }, want: "secrets runtime sink"},
		{name: "runtime mode", mutate: func(metadata *stackSecretMetadata) { metadata.Runtime.Mode = "file" }, want: "secrets runtime mode"},
		{name: "missing keys", mutate: func(metadata *stackSecretMetadata) { metadata.Keys = nil }, want: "secrets keys are required"},
		{name: "invalid key", mutate: func(metadata *stackSecretMetadata) { metadata.Keys = []stackSecretKeyMetadata{{Name: "bad-key"}} }, want: "valid environment variable name"},
		{name: "duplicate key", mutate: func(metadata *stackSecretMetadata) {
			metadata.Keys = []stackSecretKeyMetadata{{Name: "API_KEY"}, {Name: "API_KEY"}}
		}, want: "duplicated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := validMetadata()
			tc.mutate(&metadata)
			err := validateStackSecretMetadata("site", metadata)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestAgeSecretProviderLookupAndCapabilities(t *testing.T) {
	provider, err := defaultSecretProviderForName(stackSecretProviderAge)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != stackSecretProviderAge {
		t.Fatalf("unexpected provider name: %s", provider.Name())
	}
	capabilities := provider.Capabilities()
	if !capabilities.Read || !capabilities.Write || !capabilities.Delete || !capabilities.List {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
	if _, err := defaultSecretProviderForName("unknown"); err == nil || !strings.Contains(err.Error(), "unknown secret provider") {
		t.Fatalf("unexpected unknown provider error: %v", err)
	}
}

func TestSecretSetRedacted(t *testing.T) {
	redacted := SecretSet{"API_KEY": "secret-value", "TOKEN": "second-secret"}.Redacted()
	if redacted["API_KEY"] != "********" || redacted["TOKEN"] != "********" {
		t.Fatalf("unexpected redacted secrets: %+v", redacted)
	}
	for _, value := range redacted {
		if strings.Contains(value, "secret") {
			t.Fatalf("redacted secret leaked value: %+v", redacted)
		}
	}
}

func TestStackSecretPathValidation(t *testing.T) {
	valid := StackSecretRef{
		RepositoryPath: t.TempDir(),
		StackName:      "site",
		Source:         defaultStackSecretSource("site"),
	}
	if path, err := stackSecretPath(valid); err != nil || !strings.HasSuffix(path, filepath.Join("stacks", "site", stackSecretFilename)) {
		t.Fatalf("unexpected stack secret path: path=%q err=%v", path, err)
	}
	cases := []struct {
		name string
		ref  StackSecretRef
		want string
	}{
		{name: "missing repository", ref: StackSecretRef{StackName: "site", Source: defaultStackSecretSource("site")}, want: "repository path is required"},
		{name: "bad stack name", ref: StackSecretRef{RepositoryPath: valid.RepositoryPath, StackName: "Bad", Source: defaultStackSecretSource("Bad")}, want: "stack name"},
		{name: "bad source", ref: StackSecretRef{RepositoryPath: valid.RepositoryPath, StackName: "site", Source: "wrong.yaml"}, want: "secrets source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stackSecretPath(tc.ref)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}
