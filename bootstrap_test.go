package main

import (
	"context"
	"strings"
	"testing"
)

func TestBootstrapCommandsConfigureAdminAccess(t *testing.T) {
	config := bootstrapConfig{SSHUser: "root", AdminUser: "aegisadmin"}
	tasks := bootstrapTasks(config, `ssh-ed25519 AAAAkey admin's key`)
	joined := strings.Join(taskScripts(tasks), "\n")
	assertTaskNames(t, taskNames(tasks), []string{
		"Install bootstrap packages",
		"Create administrative group and user",
		"Configure passwordless sudo",
		"Create administrative SSH directory",
		"Install administrative public key",
	})
	for _, expected := range []string{
		"apt-get -o DPkg::Lock::Timeout=300 install -y 'curl' 'git' 'gnupg2' 'sudo'",
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
	if err := runBootstrapSteps(context.Background(), client, config, "ssh-ed25519 AAAATEST user@example", nil); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != len(bootstrapTasks(config, "ssh-ed25519 AAAATEST user@example")) {
		t.Fatalf("unexpected command count: %d", len(client.commands))
	}
	if !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("non-root bootstrap did not use sudo: %q", client.commands[0])
	}
}

func assertTaskNames(t *testing.T, actual, expected []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("task names = %#v, want %#v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("task names = %#v, want %#v", actual, expected)
		}
	}
}
