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

	if err := provider.PutStackSecrets(context.Background(), ref, SecretSet{"API_KEY": "secret-value", "TOKEN": "second-secret"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repository, filepath.FromSlash(defaultStackSecretSource("site")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret file mode = %o, want 0600", info.Mode().Perm())
	}
	values, err := provider.GetStackSecrets(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if values["API_KEY"] != "secret-value" || values["TOKEN"] != "second-secret" {
		t.Fatalf("unexpected decrypted values: %+v", values.Redacted())
	}
	var sopsPlaintext []byte
	err = withSOPSAgeKey(identity, func() error {
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
