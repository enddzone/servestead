package main

import (
	"bytes"
	"context"
	"errors"
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

func TestPrepareKeypairDestinationForceRemovesExistingFiles(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "provider")
	publicPath := privatePath + ".pub"
	if err := os.WriteFile(privatePath, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicPath, []byte("public"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := prepareKeypairDestination(privatePath, publicPath, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(privatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private key was not removed: %v", err)
	}
	if _, err := os.Stat(publicPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("public key was not removed: %v", err)
	}
	if err := prepareKeypairDestination(privatePath, publicPath, true); err != nil {
		t.Fatalf("force should ignore already-missing files: %v", err)
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
