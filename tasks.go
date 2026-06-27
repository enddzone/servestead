package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type Task struct {
	Name  string
	Apply string
	Stdin string
}

func runTasks(ctx context.Context, client remoteClient, sshUser string, tasks []Task, progress io.Writer) error {
	return runTasksWithReporter(ctx, client, sshUser, "", "", tasks, progress, nil)
}

type TaskEventType string

const (
	TaskRunStarted   TaskEventType = "run_started"
	TaskStarted      TaskEventType = "task_started"
	TaskLogLine      TaskEventType = "log_line"
	TaskSucceeded    TaskEventType = "task_succeeded"
	TaskFailed       TaskEventType = "task_failed"
	TaskRunCompleted TaskEventType = "run_completed"
)

type TaskEvent struct {
	Type      TaskEventType `json:"type"`
	RunID     string        `json:"run_id,omitempty"`
	Stage     string        `json:"stage,omitempty"`
	TaskIndex int           `json:"task_index,omitempty"`
	TaskTotal int           `json:"task_total,omitempty"`
	TaskName  string        `json:"task_name,omitempty"`
	Stream    string        `json:"stream,omitempty"`
	Line      string        `json:"line,omitempty"`
	Error     string        `json:"error,omitempty"`
	Time      time.Time     `json:"time"`
}

type TaskReporter interface {
	Report(TaskEvent)
}

type TaskReporterFunc func(TaskEvent)

func (fn TaskReporterFunc) Report(event TaskEvent) {
	fn(event)
}

func runTasksWithReporter(ctx context.Context, client remoteClient, sshUser string, runID string, stage string, tasks []Task, progress io.Writer, reporter TaskReporter) error {
	reportTaskEvent(reporter, TaskEvent{Type: TaskRunStarted, RunID: runID, Stage: stage, TaskTotal: len(tasks), Time: time.Now()})
	for index, task := range tasks {
		reportTaskEvent(reporter, TaskEvent{Type: TaskStarted, RunID: runID, Stage: stage, TaskIndex: index + 1, TaskTotal: len(tasks), TaskName: task.Name, Time: time.Now()})
		if progress != nil {
			fmt.Fprintf(progress, "- %s\n", task.Name)
		}
		var err error
		if task.Stdin != "" {
			stdinClient, ok := client.(remoteStdinClient)
			if !ok {
				err = fmt.Errorf("remote client does not support standard input")
			} else {
				err = stdinClient.RunWithStdin(ctx, privilegedCommand(sshUser, task.Apply), strings.NewReader(task.Stdin))
			}
		} else {
			err = client.Run(ctx, privilegedCommand(sshUser, task.Apply))
		}
		if err != nil {
			reportTaskEvent(reporter, TaskEvent{Type: TaskFailed, RunID: runID, Stage: stage, TaskIndex: index + 1, TaskTotal: len(tasks), TaskName: task.Name, Error: err.Error(), Time: time.Now()})
			return fmt.Errorf("task %q failed: %w", task.Name, err)
		}
		reportTaskEvent(reporter, TaskEvent{Type: TaskSucceeded, RunID: runID, Stage: stage, TaskIndex: index + 1, TaskTotal: len(tasks), TaskName: task.Name, Time: time.Now()})
	}
	reportTaskEvent(reporter, TaskEvent{Type: TaskRunCompleted, RunID: runID, Stage: stage, TaskTotal: len(tasks), Time: time.Now()})
	return nil
}

func reportTaskEvent(reporter TaskReporter, event TaskEvent) {
	if reporter != nil {
		reporter.Report(event)
	}
}

func writeTaskEventJSONL(writer io.Writer, event TaskEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
