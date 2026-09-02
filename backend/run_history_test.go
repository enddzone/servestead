package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestFileProfileStoreLoadsRunEventsAndPath(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	want := TaskEvent{Type: TaskLogLine, RunID: "run-1", Stage: "bootstrap", Line: "hello", Time: time.Now().UTC()}
	if err := store.AppendRunEvent(profile.ID, want.RunID, want); err != nil {
		t.Fatal(err)
	}

	events, err := store.LoadRunEvents(profile.ID, want.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Line != want.Line || events[0].Type != want.Type {
		t.Fatalf("LoadRunEvents() = %+v, want %+v", events, want)
	}
	path, err := store.RunLogPath(profile.ID, want.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "run-1.jsonl" {
		t.Fatalf("RunLogPath() = %q", path)
	}
}

func TestFileProfileStoreRunEventsHandleMissingInvalidAndTraversal(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.LoadRunEvents(profile.ID, "run-missing")
	if err != nil || len(events) != 0 {
		t.Fatalf("missing LoadRunEvents() = %+v, %v", events, err)
	}
	if _, err := store.LoadRunEvents(profile.ID, "../outside"); err == nil {
		t.Fatal("LoadRunEvents accepted a traversal run ID")
	}
	path, err := store.RunLogPath(profile.ID, "run-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n{bad json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadRunEvents(profile.ID, "run-bad"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("bad JSON error = %v", err)
	}
}

func TestFileProfileStoreReadsLargeRunEventLines(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", 1024*1024+1)
	if err := store.AppendRunEvent(profile.ID, "run-large", TaskEvent{Type: TaskLogLine, RunID: "run-large", Line: want}); err != nil {
		t.Fatal(err)
	}
	events, err := store.LoadRunEvents(profile.ID, "run-large")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Line != want {
		t.Fatalf("large run event length = %d, want %d", len(events[0].Line), len(want))
	}
}

func TestLoadProfileRunDetailMasksSecretsAndFormatsFallbacks(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	secrets := ProfileSecrets{GitHubToken: "github_pat_secret", NewtSecret: "github_pat_secret_more"}
	if err := store.SaveSecrets(profile.ID, secrets); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := []TaskEvent{
		{Type: TaskLogLine, RunID: "run-1", Stage: "stacks", Line: "used github_pat_secret_more and github_pat_secret", Time: now},
		{Type: TaskFailed, RunID: "run-1", Stage: "stacks", Error: "failed with github_pat_secret", Time: now},
		{Type: TaskStarted, RunID: "run-1", Stage: "proxy", TaskName: "configure", Time: now},
	}
	for _, event := range events {
		if err := store.AppendRunEvent(profile.ID, "run-1", event); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := loadProfileRunDetail(store, profile.ID, "run-1", secrets)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(detail.Lines, "\n")
	if strings.Contains(joined, "github_pat_secret") || !strings.Contains(joined, "used *** and ***") || !strings.Contains(joined, "task_started proxy configure") {
		t.Fatalf("masked detail = %q", joined)
	}
	if filepath.Base(detail.Path) != "run-1.jsonl" {
		t.Fatalf("detail path = %q", detail.Path)
	}
}

func TestLoadProfileRunDetailBoundsSavedTail(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	secrets := ProfileSecrets{NewtSecret: "saved-secret"}
	for index := range profileRunHistoryMaxEvents + 25 {
		if err := store.AppendRunEvent(profile.ID, "run-tail", TaskEvent{
			Type: TaskLogLine, RunID: "run-tail", Line: fmt.Sprintf("event-%03d saved-secret", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := loadProfileRunDetail(store, profile.ID, "run-tail", secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Truncated || len(detail.Lines) != profileRunHistoryMaxEvents {
		t.Fatalf("bounded detail = truncated %v, %d lines", detail.Truncated, len(detail.Lines))
	}
	if !strings.Contains(detail.Lines[0], "event-025 ***") || !strings.Contains(detail.Lines[len(detail.Lines)-1], "event-524 ***") {
		t.Fatalf("bounded detail did not retain the latest masked events: first=%q last=%q", detail.Lines[0], detail.Lines[len(detail.Lines)-1])
	}
}

func TestLoadProfileRunDetailBoundsLongDisplayLines(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	longLine := strings.Repeat("x", 1024*1024+1)
	if err := store.AppendRunEvent(profile.ID, "run-long-display", TaskEvent{Type: TaskLogLine, RunID: "run-long-display", Line: longLine}); err != nil {
		t.Fatal(err)
	}
	detail, err := loadProfileRunDetail(store, profile.ID, "run-long-display", ProfileSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Lines) != 1 || len(detail.Lines[0]) > profileRunHistoryMaxDisplayColumns+len(" …") {
		t.Fatalf("long display line was not bounded: %d bytes", len(detail.Lines[0]))
	}
}

func TestLoadProfileRunDetailOmitsOversizedEvents(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("y", profileRunHistoryMaxEventBytes+1)
	if err := store.AppendRunEvent(profile.ID, "run-oversized", TaskEvent{Type: TaskLogLine, RunID: "run-oversized", Line: oversized}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunEvent(profile.ID, "run-oversized", TaskEvent{Type: TaskLogLine, RunID: "run-oversized", Line: "latest-safe-event"}); err != nil {
		t.Fatal(err)
	}
	detail, err := loadProfileRunDetail(store, profile.ID, "run-oversized", ProfileSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Truncated || len(detail.Lines) != 1 || detail.Lines[0] != "latest-safe-event" {
		t.Fatalf("oversized event was not safely omitted: %+v", detail)
	}
}

func TestLoadProfileRunDetailRetainsValidEventsBeforePartialFinalRecord(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunEvent(profile.ID, "run-partial", TaskEvent{Type: TaskLogLine, RunID: "run-partial", Line: "valid-before-crash"}); err != nil {
		t.Fatal(err)
	}
	path, err := store.RunLogPath(profile.ID, "run-partial")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":"log_line","line":"partial`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	detail, err := loadProfileRunDetail(store, profile.ID, "run-partial", ProfileSecrets{})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Truncated || len(detail.Lines) != 1 || detail.Lines[0] != "valid-before-crash" {
		t.Fatalf("partial final record hid valid history: %+v", detail)
	}
}

type blockingRunDetailStore struct {
	ProfileStore
	started chan struct{}
	release chan struct{}
	tail    profileRunEventTail
}

func (store *blockingRunDetailStore) LoadRunEventTail(ctx context.Context, _, _ string) (profileRunEventTail, error) {
	close(store.started)
	select {
	case <-store.release:
		return store.tail, nil
	case <-ctx.Done():
		return profileRunEventTail{}, ctx.Err()
	}
}

func TestRunHistoryDetailLoadsAsynchronouslyAndIgnoresCancelledResult(t *testing.T) {
	baseStore := newFileProfileStore(t.TempDir())
	profile, err := baseStore.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{"run-1": {ID: "run-1", Status: runStatusComplete, UpdatedAt: time.Now().UTC()}}}
	if err := baseStore.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	store := &blockingRunDetailStore{
		ProfileStore: baseStore,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		tail:         profileRunEventTail{Events: []TaskEvent{{Type: TaskLogLine, Line: "loaded"}}},
	}
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state}})
	model.profileStore = store
	model.selectedIndex = 0
	model = model.openRunHistory()

	updated, command := model.Update(keyCode(tea.KeyEnter))
	loading := updated.(profileSetupModel)
	if command == nil || !loading.runDetailLoading || loading.screen != profileSetupScreenRunHistory {
		t.Fatalf("run detail did not enter asynchronous loading state: %+v", loading)
	}
	if view := loading.View().Content; !strings.Contains(view, "Loading the bounded saved-log tail") {
		t.Fatalf("run detail loading state was not visible:\n%s", view)
	}
	message := make(chan tea.Msg, 1)
	go func() { message <- command() }()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("run detail command did not start")
	}

	updated, _ = loading.Update(keyCode(tea.KeyEsc))
	cancelled := updated.(profileSetupModel)
	if cancelled.screen != profileSetupScreenDashboard || cancelled.runDetailLoading {
		t.Fatalf("Esc did not cancel run detail loading: %+v", cancelled)
	}
	select {
	case loaded := <-message:
		updated, _ = cancelled.Update(loaded)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled run detail command did not stop")
	}
	if result := updated.(profileSetupModel); result.screen != profileSetupScreenDashboard {
		t.Fatalf("stale run detail result reopened the detail screen: %+v", result)
	}
}

func TestRunHistoryDetailAppliesBoundedLoadResult(t *testing.T) {
	baseStore := newFileProfileStore(t.TempDir())
	profile, err := baseStore.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{"run-1": {ID: "run-1", Status: runStatusComplete, UpdatedAt: time.Now().UTC()}}}
	if err := baseStore.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	close(release)
	store := &blockingRunDetailStore{
		ProfileStore: baseStore,
		started:      make(chan struct{}),
		release:      release,
		tail: profileRunEventTail{
			Events: []TaskEvent{{Type: TaskLogLine, Line: "loaded detail"}}, Truncated: true,
		},
	}
	model := newProfileSetupModel([]profileChoice{{Profile: profile, State: state}})
	model.profileStore = store
	model.selectedIndex = 0
	model = model.openRunHistory()

	updated, command := model.Update(keyCode(tea.KeyEnter))
	loaded, _ := updated.(profileSetupModel).Update(command())
	result := loaded.(profileSetupModel)
	if result.screen != profileSetupScreenRunDetail || result.runDetailLoading {
		t.Fatalf("loaded run detail was not applied: %+v", result)
	}
	view := result.View().Content
	if !strings.Contains(view, "Showing the latest 500 saved events") || !strings.Contains(view, "loaded detail") {
		t.Fatalf("bounded run detail notice/content missing:\n%s", view)
	}
}

func TestSetupConfigSecretValuesMaskLiveRunSecrets(t *testing.T) {
	config := setupConfig{
		GitHubToken:           "github_pat_live_secret",
		PangolinAdminPassword: "pangolin-live-password",
		Stacks: []configuredStack{{
			Name:         "site",
			SecretValues: SecretSet{"DATABASE_URL": "postgres://live-secret"},
		}},
	}
	masked := maskSecretValues(
		"github_pat_live_secret pangolin-live-password postgres://live-secret",
		setupConfigSecretValues(config),
	)
	if masked != "*** *** ***" {
		t.Fatalf("live secret masking = %q", masked)
	}
}

func TestMaskSecretValuesMasksShortConfiguredSecrets(t *testing.T) {
	if got := maskSecretValues("TOKEN=abc", []string{"abc"}); got != "TOKEN=***" {
		t.Fatalf("short secret masking = %q", got)
	}
}

func TestMaskSecretValuesSanitizesControlsBeforeRedaction(t *testing.T) {
	text := "before \x1b]2;spoofed title\x07 se\x1b[31mcr\b\ret\u202e after"
	masked := maskSecretValues(text, []string{"secret"})
	if strings.ContainsAny(masked, "\x1b\x07\b\r") || strings.Contains(masked, "\u202e") {
		t.Fatalf("terminal controls survived sanitization: %q", masked)
	}
	if strings.Join(strings.Fields(masked), " ") != "before *** after" {
		t.Fatalf("sanitized redaction = %q", masked)
	}
}

func TestMaskSecretValuesRedactsSanitizedSecretForms(t *testing.T) {
	secret := "abc\u200bXYZ"
	masked := maskSecretValues("raw="+secret+" visible=abcXYZ", []string{secret})
	if masked != "raw=*** visible=***" {
		t.Fatalf("sanitized secret form leaked: %q", masked)
	}
}

func TestProfileRunReporterRedactsSecretsBeforePersistence(t *testing.T) {
	store := newFileProfileStore(t.TempDir())
	profile, err := store.Create(Profile{ID: "history-profile", IP: profileTestHost})
	if err != nil {
		t.Fatal(err)
	}
	state := ProfileState{Runs: map[string]SetupRun{
		"run-secret": {
			ID:     "run-secret",
			Status: runStatusRunning,
			Stages: map[string]SetupStageStatus{"stacks": {Status: stageStatusRunning}},
		},
	}}
	if err := store.Save(profile, state); err != nil {
		t.Fatal(err)
	}
	reporter := &profileRunReporter{
		store:        store,
		profile:      profile,
		state:        &state,
		runID:        "run-secret",
		secretValues: []string{"postgres://persisted-secret"},
	}
	reporter.Report(TaskEvent{
		Type:  TaskLogLine,
		RunID: "run-secret",
		Stage: "stacks",
		Line:  "database=postgres://persisted-secret",
		Time:  time.Now().UTC(),
	})
	if reporter.err != nil {
		t.Fatal(reporter.err)
	}
	events, err := store.LoadRunEvents(profile.ID, "run-secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Line != "database=***" || strings.Contains(events[0].Line, "persisted-secret") {
		t.Fatalf("persisted run events = %+v", events)
	}
}

func TestSortedSetupRunsAndSummaries(t *testing.T) {
	older := time.Now().UTC().Add(-time.Minute)
	newer := older.Add(time.Minute)
	state := ProfileState{Runs: map[string]SetupRun{
		"old": {ID: "old", Status: runStatusComplete, UpdatedAt: older},
		"new": {
			ID: "new", Status: runStatusFailed, UpdatedAt: newer,
			Stages: map[string]SetupStageStatus{
				"proxy":     {Status: runStatusFailed, LastError: "proxy failed"},
				"bootstrap": {Status: runStatusComplete},
			},
		},
	}}
	runs := sortedSetupRuns(state)
	if len(runs) != 2 || runs[0].ID != "new" || runs[1].ID != "old" {
		t.Fatalf("sorted runs = %+v", runs)
	}
	if got := profileRunStageSummary(runs[0]); got != "bootstrap:complete, proxy:failed" {
		t.Fatalf("stage summary = %q", got)
	}
	if got := profileRunErrorSummary(runs[0]); got != "proxy failed" {
		t.Fatalf("error summary = %q", got)
	}
}
