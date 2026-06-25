package main

import (
	"context"
	"strings"
	"testing"
)

func TestBootstrapCommandsConfigureAdminAccess(t *testing.T) {
	config := bootstrapConfig{SSHUser: "root", AdminUser: "aegisadmin"}
	commands := bootstrapCommands(config, `ssh-ed25519 AAAAkey admin's key`)
	joined := strings.Join(commands, "\n")
	for _, expected := range []string{
		"apt-get install -y 'curl' 'git' 'gnupg2' 'sudo'",
		"groupadd 'aegisadmin'",
		"useradd --create-home --shell /bin/bash --gid 'aegisadmin' --groups sudo 'aegisadmin'",
		"visudo -cf '/etc/sudoers.d/aegisadmin.aegisnode.tmp'",
		"/home/aegisadmin/.ssh/authorized_keys",
		"c3NoLWVkMjU1MTkgQUFBQWtleSBhZG1pbidzIGtleQo=",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("bootstrap commands missing %q:\n%s", expected, joined)
		}
	}
}

func TestRunBootstrapStepsUsesPrivilegedCommands(t *testing.T) {
	client := &recordingRemoteClient{}
	config := bootstrapConfig{SSHUser: "aegisadmin", AdminUser: "aegisadmin"}
	if err := runBootstrapSteps(context.Background(), client, config, "ssh-ed25519 AAAATEST user@example"); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(bootstrapCommands(config, "ssh-ed25519 AAAATEST user@example")) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
	if !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("non-root bootstrap did not use sudo: %q", client.commands[0])
	}
}
