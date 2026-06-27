package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &stdout, &stderr, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "aegisnode provision") || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestSubcommandHelpIsSuccessful(t *testing.T) {
	for _, command := range []string{"provision", "bootstrap", "harden", "network", "proxy", "pangolin-token", "pangolin-credentials", "keygen", "setup", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), []string{command, "-help"}, &stdout, &stderr, func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stderr.String(), "Usage of "+command) {
				t.Fatalf("unexpected help output: %q", stderr.String())
			}
		})
	}
}

func TestProvisionRequiresProviderCredential(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), []string{
		"provision", "--provider", "hetzner", "--name", "aegis-01", "--ssh-key", "key-id",
	}, &stdout, &stderr, func(string) string { return "" })
	if err == nil || err.Error() != "HETZNER_API_TOKEN or HCLOUD_TOKEN is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProvisionConfigDefaults(t *testing.T) {
	config := provisionConfig{}
	config.withDefaults("region", "size", "image")
	if config.Region != "region" || config.Size != "size" || config.Image != "image" {
		t.Fatalf("defaults not applied: %+v", config)
	}
	config.withDefaults("other", "other", "other")
	if config.Region != "region" || config.Size != "size" || config.Image != "image" {
		t.Fatalf("existing values were overwritten: %+v", config)
	}
}
