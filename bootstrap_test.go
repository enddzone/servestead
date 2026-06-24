package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractBootstrapPlaybook(t *testing.T) {
	directory := t.TempDir()
	path, err := extractBootstrapPlaybook(directory)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(directory, "bootstrap.yml") {
		t.Fatalf("unexpected playbook path: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Install the administrative public key") {
		t.Fatal("extracted playbook does not contain the bootstrap tasks")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected playbook permissions: %o", info.Mode().Perm())
	}
}

func TestAnsibleArgsEncodeExtraVariablesAsJSON(t *testing.T) {
	config := bootstrapConfig{
		Host: "203.0.113.10", SSHUser: "root", AdminUser: "aegisadmin", PrivateKeyPath: "/tmp/id_ed25519",
	}
	publicKey := `ssh-ed25519 AAAAkey admin's key`
	args, err := ansibleArgs(config, publicKey, "/tmp/bootstrap.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "-o StrictHostKeyChecking=accept-new") {
		t.Fatalf("host key policy is missing from arguments: %#v", args)
	}
	var extraVars map[string]string
	if err := json.Unmarshal([]byte(args[len(args)-2]), &extraVars); err != nil {
		t.Fatalf("extra vars are not valid JSON: %v", err)
	}
	if extraVars["admin_username"] != "aegisadmin" || extraVars["admin_public_key"] != publicKey {
		t.Fatalf("unexpected extra vars: %#v", extraVars)
	}
}
