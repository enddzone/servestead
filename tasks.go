package main

import (
	"context"
	"fmt"
	"io"
)

type Task struct {
	Name  string
	Apply string
}

func runTasks(ctx context.Context, client remoteClient, sshUser string, tasks []Task, progress io.Writer) error {
	for _, task := range tasks {
		if progress != nil {
			fmt.Fprintf(progress, "- %s\n", task.Name)
		}
		if err := client.Run(ctx, privilegedCommand(sshUser, task.Apply)); err != nil {
			return fmt.Errorf("task %q failed: %w", task.Name, err)
		}
	}
	return nil
}
