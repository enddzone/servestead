package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type profileRunDetail struct {
	Path      string
	Lines     []string
	Truncated bool
}

const (
	profileRunHistoryMaxEvents         = 500
	profileRunHistoryTailBytes         = 8 << 20
	profileRunHistoryMaxEventBytes     = 2 << 20
	profileRunHistoryMaxDisplayColumns = 4096
)

type profileRunEventTail struct {
	Events    []TaskEvent
	Truncated bool
}

type profileRunEventTailLoader interface {
	LoadRunEventTail(context.Context, string, string) (profileRunEventTail, error)
}

func (store *fileProfileStore) LoadRunEvents(profileID string, runID string) ([]TaskEvent, error) {
	path, err := store.runLogPath(profileID, runID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readTaskEventsJSONL(file)
}

func (store *fileProfileStore) RunLogPath(profileID string, runID string) (string, error) {
	return store.runLogPath(profileID, runID)
}

func loadProfileRunDetail(store ProfileStore, profileID string, runID string, secrets ProfileSecrets) (profileRunDetail, error) {
	return loadProfileRunDetailContext(context.Background(), store, profileID, runID, secrets)
}

func loadProfileRunDetailContext(ctx context.Context, store ProfileStore, profileID string, runID string, secrets ProfileSecrets) (profileRunDetail, error) {
	path, err := store.RunLogPath(profileID, runID)
	if err != nil {
		return profileRunDetail{}, err
	}
	tail, err := loadProfileRunEventTail(ctx, store, profileID, runID)
	if err != nil {
		return profileRunDetail{}, err
	}
	detail := profileRunDetail{Path: path, Lines: make([]string, 0, len(tail.Events)), Truncated: tail.Truncated}
	for _, event := range tail.Events {
		if err := ctx.Err(); err != nil {
			return profileRunDetail{}, err
		}
		line := taskEventLogLine(event)
		if line != "" {
			line = maskProfileSecrets(line, secrets)
			detail.Lines = append(detail.Lines, ansi.Truncate(line, profileRunHistoryMaxDisplayColumns, " …"))
		}
	}
	return detail, nil
}

func loadProfileRunEventTail(ctx context.Context, store ProfileStore, profileID string, runID string) (profileRunEventTail, error) {
	if loader, ok := store.(profileRunEventTailLoader); ok {
		return loader.LoadRunEventTail(ctx, profileID, runID)
	}
	if err := ctx.Err(); err != nil {
		return profileRunEventTail{}, err
	}
	events, err := store.LoadRunEvents(profileID, runID)
	if err != nil {
		return profileRunEventTail{}, err
	}
	tail := profileRunEventTail{Events: events}
	if len(tail.Events) > profileRunHistoryMaxEvents {
		tail.Events = append([]TaskEvent(nil), tail.Events[len(tail.Events)-profileRunHistoryMaxEvents:]...)
		tail.Truncated = true
	}
	return tail, nil
}

func (store *fileProfileStore) LoadRunEventTail(ctx context.Context, profileID string, runID string) (profileRunEventTail, error) {
	path, err := store.runLogPath(profileID, runID)
	if err != nil {
		return profileRunEventTail{}, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return profileRunEventTail{}, nil
	}
	if err != nil {
		return profileRunEventTail{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return profileRunEventTail{}, err
	}
	offset := max(int64(0), info.Size()-profileRunHistoryTailBytes)
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return profileRunEventTail{}, err
	}
	reader := bufio.NewReader(io.LimitReader(file, profileRunHistoryTailBytes))
	tail := profileRunEventTail{Truncated: offset > 0}
	if offset > 0 {
		if err := discardPartialRunEventLine(ctx, reader); err != nil {
			return profileRunEventTail{}, err
		}
	}
	return readTaskEventTail(ctx, reader, tail)
}

func discardPartialRunEventLine(ctx context.Context, reader *bufio.Reader) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := reader.ReadSlice('\n')
		switch {
		case err == nil, errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return err
		}
	}
}

func readTaskEventTail(ctx context.Context, reader *bufio.Reader, tail profileRunEventTail) (profileRunEventTail, error) {
	ring := newTaskEventTailRing()
	for {
		line, readErr := readBoundedRunEventLine(ctx, reader)
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		event, include, omitted := decodeTaskEventTailLine(line, readErr)
		if omitted || (include && ring.Add(event)) {
			tail.Truncated = true
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, errRunHistoryEventTooLarge) {
			return profileRunEventTail{}, readErr
		}
	}
	tail.Events = ring.Events()
	return tail, nil
}

func decodeTaskEventTailLine(line []byte, readErr error) (TaskEvent, bool, bool) {
	if errors.Is(readErr, errRunHistoryEventTooLarge) {
		return TaskEvent{}, false, true
	}
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return TaskEvent{}, false, false
	}
	var event TaskEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return TaskEvent{}, false, true
	}
	return event, true, false
}

type taskEventTailRing struct {
	events []TaskEvent
	count  int
	next   int
}

func newTaskEventTailRing() *taskEventTailRing {
	return &taskEventTailRing{events: make([]TaskEvent, profileRunHistoryMaxEvents)}
}

func (ring *taskEventTailRing) Add(event TaskEvent) bool {
	overwrote := ring.count == len(ring.events)
	ring.events[ring.next] = event
	ring.next = (ring.next + 1) % len(ring.events)
	if !overwrote {
		ring.count++
	}
	return overwrote
}

func (ring *taskEventTailRing) Events() []TaskEvent {
	if ring.count < len(ring.events) {
		return append([]TaskEvent(nil), ring.events[:ring.count]...)
	}
	result := append([]TaskEvent(nil), ring.events[ring.next:]...)
	return append(result, ring.events[:ring.next]...)
}

var errRunHistoryEventTooLarge = errors.New("run history event exceeds display limit")

func readBoundedRunEventLine(ctx context.Context, reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, min(64*1024, profileRunHistoryMaxEventBytes))
	tooLarge := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fragment, err := reader.ReadSlice('\n')
		if !tooLarge && len(line)+len(fragment) <= profileRunHistoryMaxEventBytes {
			line = append(line, fragment...)
		} else {
			tooLarge = true
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == nil:
			if tooLarge {
				return nil, errRunHistoryEventTooLarge
			}
			return line, nil
		case errors.Is(err, io.EOF):
			if tooLarge {
				return nil, errors.Join(errRunHistoryEventTooLarge, io.EOF)
			}
			return line, io.EOF
		default:
			return nil, err
		}
	}
}

func readTaskEventsJSONL(reader io.Reader) ([]TaskEvent, error) {
	buffered := bufio.NewReader(reader)
	var events []TaskEvent
	lineNumber := 0
	for {
		data, readErr := buffered.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("read run events: %w", readErr)
		}
		if data == "" && errors.Is(readErr, io.EOF) {
			break
		}
		lineNumber++
		line := strings.TrimSpace(data)
		if line == "" {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		var event TaskEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("decode run event line %d: %w", lineNumber, err)
		}
		events = append(events, event)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
}

func sortedSetupRuns(state ProfileState) []SetupRun {
	runs := make([]SetupRun, 0, len(state.Runs))
	for _, run := range state.Runs {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].UpdatedAt.Equal(runs[j].UpdatedAt) {
			return runs[i].ID > runs[j].ID
		}
		return runs[i].UpdatedAt.After(runs[j].UpdatedAt)
	})
	return runs
}

func profileRunStageSummary(run SetupRun) string {
	names := make([]string, 0, len(run.Stages))
	for stage, status := range run.Stages {
		if status.Status == stageStatusRunning || status.Status == stageStatusFailed || status.Status == stageStatusComplete || status.Status == stageStatusCancelled {
			names = append(names, stage+":"+status.Status)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return runStatusPlanned
	}
	return strings.Join(names, ", ")
}

func profileRunErrorSummary(run SetupRun) string {
	stages := make([]string, 0, len(run.Stages))
	for stage := range run.Stages {
		stages = append(stages, stage)
	}
	sort.Strings(stages)
	for _, stage := range stages {
		if message := strings.TrimSpace(run.Stages[stage].LastError); message != "" {
			return message
		}
	}
	return ""
}

func taskEventLogLine(event TaskEvent) string {
	line := strings.TrimSpace(event.Line)
	if line == "" {
		line = strings.TrimSpace(event.Error)
	}
	if line == "" {
		line = strings.TrimSpace(strings.Join([]string{string(event.Type), event.Stage, event.TaskName}, " "))
	}
	if line == "" {
		return ""
	}
	if event.Time.IsZero() {
		return line
	}
	return event.Time.Local().Format("15:04:05") + "  " + line
}

func maskProfileSecrets(text string, secrets ProfileSecrets) string {
	return maskSecretValues(text, profileSecretValues(secrets))
}

func maskSecretValues(text string, values []string) string {
	values = append([]string(nil), values...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	seen := map[string]bool{}
	for _, secret := range values {
		if secret == "" || seen[secret] {
			continue
		}
		seen[secret] = true
		text = strings.ReplaceAll(text, secret, "***")
	}
	text = sanitizeTerminalText(text)
	visibleValues := make([]string, 0, len(values))
	seen = map[string]bool{}
	for _, secret := range values {
		visible := sanitizeTerminalText(secret)
		if visible == "" || seen[visible] {
			continue
		}
		seen[visible] = true
		visibleValues = append(visibleValues, visible)
	}
	sort.Slice(visibleValues, func(i, j int) bool { return len(visibleValues[i]) > len(visibleValues[j]) })
	for _, secret := range visibleValues {
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text
}

func sanitizeTerminalText(text string) string {
	text = ansi.Strip(text)
	var sanitized strings.Builder
	sanitized.Grow(len(text))
	for _, character := range text {
		switch character {
		case '\n', '\t':
			sanitized.WriteRune(character)
		case '\r':
			continue
		default:
			if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
				continue
			}
			sanitized.WriteRune(character)
		}
	}
	return sanitized.String()
}

func sanitizeTerminalLine(text string) string {
	text = sanitizeTerminalText(text)
	text = strings.NewReplacer("\n", " ", "\t", " ").Replace(text)
	return strings.TrimSpace(text)
}

func truncateUTF8Bytes(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func setupConfigSecretValues(config setupConfig) []string {
	values := []string{
		config.ServerSecret,
		config.PangolinSetupToken,
		config.PangolinAdminPassword,
		config.NewtID,
		config.NewtSecret,
		config.BeszelAdminPassword,
		config.BeszelSystemToken,
		config.BeszelHubPrivateKey,
		config.BeszelHubPublicKey,
		config.GitHubToken,
	}
	for _, stack := range config.Stacks {
		for _, secret := range stack.SecretValues {
			values = append(values, secret)
		}
	}
	return values
}

func profileSecretValues(secrets ProfileSecrets) []string {
	return []string{
		secrets.ServerSecret,
		secrets.PangolinSetupToken,
		secrets.PangolinAdminPassword,
		secrets.NewtID,
		secrets.NewtSecret,
		secrets.BeszelAdminPassword,
		secrets.BeszelSystemToken,
		secrets.BeszelHubPrivateKey,
		secrets.BeszelHubPublicKey,
		secrets.GitHubToken,
		secrets.StackSecretIdentity,
		secrets.StackSecretRecipient,
	}
}
