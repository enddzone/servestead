package main

import (
	"context"
	"strings"
	"testing"
)

type recordingRemoteClient struct {
	commands []string
	err      error
}

func (client *recordingRemoteClient) Run(_ context.Context, command string) error {
	client.commands = append(client.commands, command)
	return client.err
}

func (client *recordingRemoteClient) Close() error {
	return nil
}

func TestSSHHostPortDefaultsAndParsesPorts(t *testing.T) {
	tests := map[string]string{
		"203.0.113.10":       "203.0.113.10:22",
		"203.0.113.10:2222":  "203.0.113.10:2222",
		"[2001:db8::1]:2222": "[2001:db8::1]:2222",
	}
	for input, expected := range tests {
		actual, err := sshHostPort(input)
		if err != nil {
			t.Fatalf("sshHostPort(%q): %v", input, err)
		}
		if actual != expected {
			t.Fatalf("sshHostPort(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestRemoteWriteFileCommandEncodesContent(t *testing.T) {
	command := remoteWriteFileCommand("/etc/example.conf", "value with ' quote\n", "root", "root", 0644)
	for _, expected := range []string{
		"base64 -d > '/etc/example.conf.aegisnode.tmp'",
		"chown 'root:root' '/etc/example.conf.aegisnode.tmp'",
		"chmod '0644' '/etc/example.conf.aegisnode.tmp'",
		"dmFsdWUgd2l0aCAnIHF1b3RlCg==",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("write command missing %q:\n%s", expected, command)
		}
	}
}
