package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractHardeningPlaybook(t *testing.T) {
	directory := t.TempDir()
	path, err := extractHardeningPlaybook(directory)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "hardening.yml") {
		t.Fatalf("unexpected playbook path: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, expected := range []string{
		"Deploy validated sysctl hardening configuration",
		"unattended-upgrades",
		"Install CrowdSec security agent",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("extracted playbook is missing %q", expected)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected playbook permissions: %o", info.Mode().Perm())
	}
}

func TestHardeningArgs(t *testing.T) {
	config := hardeningConfig{
		Host: "203.0.113.10", SSHUser: "aegisadmin", PrivateKeyPath: "/tmp/id_ed25519",
	}
	args := hardeningArgs(config, "/tmp/hardening.yml")
	if !slices.Contains(args, "-o StrictHostKeyChecking=accept-new") {
		t.Fatalf("host key policy is missing from arguments: %#v", args)
	}
	if args[len(args)-1] != "/tmp/hardening.yml" {
		t.Fatalf("playbook path is not the final argument: %#v", args)
	}
}

func TestHardenRequiresHostAndPrivateKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{"harden"}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "--host and --private-key are required" {
		t.Fatalf("unexpected error: %v", err)
	}
}
