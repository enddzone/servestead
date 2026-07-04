package main

import (
	"os"
	"testing"
)

const (
	testShellPath         = "/bin/sh"
	testDockerCommandName = "docker"
)

var testDockerExecutablePaths = []string{
	"/usr/local/bin/docker",
	"/opt/homebrew/bin/docker",
	"/usr/bin/docker",
}

func testDockerPath(t *testing.T) string {
	t.Helper()
	for _, path := range testDockerExecutablePaths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return path
		}
	}
	t.Skip(testDockerCommandName + " CLI is not installed")
	return ""
}
