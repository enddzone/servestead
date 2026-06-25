package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func taskScripts(tasks []Task) []string {
	scripts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		scripts = append(scripts, task.Apply)
	}
	return scripts
}

func taskNames(tasks []Task) []string {
	names := make([]string, 0, len(tasks))
	for _, task := range tasks {
		names = append(names, task.Name)
	}
	return names
}

func TestRunTasksPrintsTaskNamesAndUsesPrivileges(t *testing.T) {
	client := &recordingRemoteClient{}
	var progress bytes.Buffer
	err := runTasks(context.Background(), client, "aegisadmin", []Task{
		{Name: "Example task", Apply: "true"},
	}, &progress)
	if err != nil {
		t.Fatal(err)
	}
	if progress.String() != "- Example task\n" {
		t.Fatalf("unexpected progress output: %q", progress.String())
	}
	if len(client.commands) != 1 || !strings.HasPrefix(client.commands[0], "sudo sh -c ") {
		t.Fatalf("task did not run with sudo: %#v", client.commands)
	}
}

func TestRunTasksWrapsErrorsWithTaskName(t *testing.T) {
	client := &recordingRemoteClient{err: errors.New("boom")}
	err := runTasks(context.Background(), client, "root", []Task{
		{Name: "Failing task", Apply: "false"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), `task "Failing task" failed: boom`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
