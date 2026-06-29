package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateProviderKeypairWritesOpenSSHKeysAndPrintsGuidance(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "keys", "provider")
	var stdout, stderr bytes.Buffer
	err := generateProviderKeypair(context.Background(), keygenConfig{
		Path:    keyPath,
		Comment: "aegis-test",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("generateProviderKeypair failed: %v stderr=%s", err, stderr.String())
	}
	privateKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privateKey); err != nil {
		t.Fatalf("private key is not parseable by SSH: %v", err)
	}
	publicKey, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parsedPublicKey, _, _, _, err := ssh.ParseAuthorizedKey(publicKey)
	if err != nil {
		t.Fatalf("public key is not parseable by SSH: %v", err)
	}
	if parsedPublicKey.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("unexpected public key type: %s", parsedPublicKey.Type())
	}
	for _, expected := range []string{
		"Servestead SSH keypair ready.",
		"ssh-ed25519 ",
		"aegis-test",
		"Hetzner Cloud",
		"DigitalOcean",
		"servestead provision",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("output missing %q:\n%s", expected, stdout.String())
		}
	}
}

func TestGenerateProviderKeypairRefusesOverwriteWithoutForce(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "provider")
	if err := os.WriteFile(keyPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := generateProviderKeypair(context.Background(), keygenConfig{Path: keyPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultKeygenConfigUsesServesteadKeyName(t *testing.T) {
	config := defaultKeygenConfig()
	if !strings.Contains(config.Path, defaultServesteadKeyName) {
		t.Fatalf("default path does not include provider key name: %q", config.Path)
	}
	if config.Comment == "" {
		t.Fatal("default comment is empty")
	}
}
