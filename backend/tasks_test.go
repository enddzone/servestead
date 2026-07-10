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
	err := runTasks(context.Background(), client, "servestead", []Task{
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

func TestRunTasksWithReporterEmitsStructuredEvents(t *testing.T) {
	client := &recordingRemoteClient{}
	events := []TaskEvent{}
	reporter := TaskReporterFunc(func(event TaskEvent) {
		events = append(events, event)
	})
	var progress bytes.Buffer

	err := runTasksWithReporter(context.Background(), client, "root", taskRunOptions{runID: "run-1", stage: "bootstrap", tasks: []Task{
		{Name: "First", Apply: "true"},
		{Name: "Second", Apply: "true"},
	}, progress: &progress, reporter: reporter})
	if err != nil {
		t.Fatal(err)
	}
	if progress.String() != "- First\n- Second\n" {
		t.Fatalf("unexpected progress output: %q", progress.String())
	}
	actualTypes := []TaskEventType{}
	for _, event := range events {
		actualTypes = append(actualTypes, event.Type)
		if event.RunID != "run-1" || event.Stage != "bootstrap" {
			t.Fatalf("event missing run context: %+v", event)
		}
	}
	expectedTypes := []TaskEventType{
		TaskRunStarted,
		TaskStarted,
		TaskSucceeded,
		TaskStarted,
		TaskSucceeded,
		TaskRunCompleted,
	}
	if len(actualTypes) != len(expectedTypes) {
		t.Fatalf("event types = %#v, want %#v", actualTypes, expectedTypes)
	}
	for index := range expectedTypes {
		if actualTypes[index] != expectedTypes[index] {
			t.Fatalf("event types = %#v, want %#v", actualTypes, expectedTypes)
		}
	}
}
