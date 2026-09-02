package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

type profileStackOperationKind string

const (
	profileStackOperationRefresh profileStackOperationKind = "refresh"
	profileStackOperationDiff    profileStackOperationKind = "diff"
	profileStackOperationStage   profileStackOperationKind = "stage"
	profileStackOperationCommit  profileStackOperationKind = "commit"
	profileStackOperationPush    profileStackOperationKind = "push"
	profileStackOperationSync    profileStackOperationKind = "sync"
	profileStackOperationSave    profileStackOperationKind = "save"
	profileStackOperationDelete  profileStackOperationKind = "delete"
)

type profileStackOperationRequest struct {
	Kind           profileStackOperationKind
	RepositoryPath string
	Choice         profileChoice
	ProfileStore   ProfileStore
	CommitMessage  string
	QuietMissing   bool
	Save           *stackEditorSaveRequest
	StackName      string
}

type profileStackRepositorySnapshot struct {
	Stacks      []editableStack
	GitStatus   string
	Head        string
	NeedsPush   bool
	SyncStatus  string
	Notice      string
	Unavailable bool
}

type profileStackOperationResult struct {
	Snapshot             profileStackRepositorySnapshot
	Diff                 string
	Save                 profileStackSaveResult
	Choice               profileChoice
	ChoiceUpdated        bool
	MutationStarted      bool
	CancellationObserved bool
	DeletedStackName     string
}

type profileStackOperationMsg struct {
	Kind          profileStackOperationKind
	CommitMessage string
	Result        profileStackOperationResult
	Err           error
}

var runProfileStackOperation = defaultRunProfileStackOperation
var runProfileStackStage = stageStackChanges
var runProfileStackCommit = commitStackChanges
var runProfileStackPush = pushStackRepository
var runProfileStackDelete = func(_ context.Context, repositoryPath, stackName string) error {
	return removeEditableStack(repositoryPath, stackName)
}

func newProfileStackSpinner() spinner.Model {
	model := spinner.New()
	model.Spinner = spinner.Dot
	return model
}

func defaultRunProfileStackOperation(ctx context.Context, request profileStackOperationRequest) (profileStackOperationResult, error) {
	request, result, lock, err := prepareProfileStackOperation(ctx, request)
	if err != nil {
		return profileStackOperationResult{}, err
	}
	defer releaseProfileOperationLock(lock)
	if err := validateProfileStackRepository(request.RepositoryPath); err != nil {
		if request.QuietMissing && errors.Is(err, os.ErrNotExist) {
			result.Snapshot = profileStackRepositorySnapshot{Unavailable: true}
			return result, nil
		}
		return result, err
	}
	result, err = executeProfileStackOperation(ctx, request, result)
	return normalizeProfileStackOperationCancellation(ctx, request.Kind, result, err)
}

func prepareProfileStackOperation(
	ctx context.Context,
	request profileStackOperationRequest,
) (profileStackOperationRequest, profileStackOperationResult, profileOperationLock, error) {
	if !profileStackOperationMutates(request.Kind) {
		return request, profileStackOperationResult{}, nil, nil
	}
	lockedRequest, lock, err := lockProfileStackMutation(ctx, request)
	if err != nil {
		return profileStackOperationRequest{}, profileStackOperationResult{}, nil, err
	}
	result := profileStackOperationResult{Choice: lockedRequest.Choice, ChoiceUpdated: true}
	return lockedRequest, result, lock, nil
}

func executeProfileStackOperation(
	ctx context.Context,
	request profileStackOperationRequest,
	result profileStackOperationResult,
) (profileStackOperationResult, error) {
	var err error
	switch request.Kind {
	case profileStackOperationRefresh, profileStackOperationSync:
		result.Snapshot, err = loadProfileStackRepositorySnapshot(ctx, request.RepositoryPath, request.Choice)
	case profileStackOperationDiff:
		result.Diff, err = stackRepositoryDiff(ctx, request.RepositoryPath)
	case profileStackOperationStage:
		result.MutationStarted, err = runStartedProfileStackMutation(ctx, func() error {
			return runProfileStackStage(ctx, request.RepositoryPath)
		})
	case profileStackOperationCommit:
		result.MutationStarted, err = runStartedProfileStackMutation(ctx, func() error {
			return runProfileStackCommit(ctx, request.RepositoryPath, request.CommitMessage)
		})
	case profileStackOperationPush:
		result.MutationStarted, err = runStartedProfileStackMutation(ctx, func() error {
			return runProfileStackPush(ctx, request.RepositoryPath)
		})
	case profileStackOperationSave:
		if request.Save == nil {
			err = errors.New("stack save request is missing")
			break
		}
		result.Save, err = runStackEditorSave(ctx, *request.Save)
		result.MutationStarted = result.Save.MutationStarted
		result.CancellationObserved = result.Save.CancellationObserved
	case profileStackOperationDelete:
		if strings.TrimSpace(request.StackName) == "" {
			err = errors.New("stack removal request is missing a stack name")
			break
		}
		result.MutationStarted, err = runStartedProfileStackMutation(ctx, func() error {
			return runProfileStackDelete(ctx, request.RepositoryPath, request.StackName)
		})
		result.DeletedStackName = request.StackName
	default:
		err = fmt.Errorf("unknown stack repository operation %q", request.Kind)
	}
	return result, err
}

func runStartedProfileStackMutation(ctx context.Context, operation func() error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, operation()
}

func normalizeProfileStackOperationCancellation(
	ctx context.Context,
	kind profileStackOperationKind,
	result profileStackOperationResult,
	err error,
) (profileStackOperationResult, error) {
	if ctx.Err() != nil {
		if result.MutationStarted {
			result.CancellationObserved = true
		}
		if err != nil && !(kind == profileStackOperationSave && result.Save.MutationStarted) {
			return result, ctx.Err()
		}
	}
	return result, err
}

func profileStackOperationMutates(kind profileStackOperationKind) bool {
	switch kind {
	case profileStackOperationStage, profileStackOperationCommit, profileStackOperationPush, profileStackOperationSave, profileStackOperationDelete:
		return true
	default:
		return false
	}
}

func lockProfileStackMutation(ctx context.Context, request profileStackOperationRequest) (profileStackOperationRequest, profileOperationLock, error) {
	if err := ctx.Err(); err != nil {
		return profileStackOperationRequest{}, nil, err
	}
	if request.ProfileStore == nil {
		return profileStackOperationRequest{}, nil, errors.New(setupProfileStoreUnavailable)
	}
	profileID := request.Choice.Profile.ID
	if profileID == "" && request.Save != nil {
		profileID = request.Save.Profile.ID
	}
	if profileID == "" {
		return profileStackOperationRequest{}, nil, errors.New(setupNoProfileSelectedMessage)
	}
	profile, state, lock, err := lockAndLoadProfile(request.ProfileStore, profileID)
	if err != nil {
		return profileStackOperationRequest{}, nil, err
	}
	releaseAfterError := func(err error) (profileStackOperationRequest, profileOperationLock, error) {
		releaseProfileOperationLock(lock)
		return profileStackOperationRequest{}, nil, err
	}
	if profileStateHasActiveRun(state) {
		return releaseAfterError(errors.New("cannot change the stack repository while this profile's setup run is active"))
	}
	secrets, err := request.ProfileStore.LoadSecrets(profileID)
	if err != nil {
		return releaseAfterError(err)
	}
	currentRepositoryPath := strings.TrimSpace(profile.ConfigRepositoryPath)
	if currentRepositoryPath == "" {
		return releaseAfterError(errors.New("configuration repository is not ready; run Platform once before managing stacks"))
	}
	if !sameProfileRepository(request.RepositoryPath, currentRepositoryPath) {
		return releaseAfterError(errors.New("the profile's configuration repository changed; reopen the stack manager and retry"))
	}
	request.RepositoryPath = currentRepositoryPath
	request.Choice = profileChoice{Profile: profile, State: state, Secrets: secrets}
	if request.Save != nil {
		save := *request.Save
		save.RepositoryPath = currentRepositoryPath
		save.Profile = cloneStackEditorProfile(profile)
		save.ProfileSecrets = secrets
		save.ProfileStore = request.ProfileStore
		request.Save = &save
	}
	repositoryLock, err := acquireRepositoryOperationLock(request.ProfileStore, currentRepositoryPath)
	if err != nil {
		return releaseAfterError(err)
	}
	return request, combineProfileOperationLocks(lock, repositoryLock), nil
}

func sameProfileRepository(left, right string) bool {
	leftPath, leftErr := filepath.Abs(expandUserPath(strings.TrimSpace(left)))
	rightPath, rightErr := filepath.Abs(expandUserPath(strings.TrimSpace(right)))
	return leftErr == nil && rightErr == nil && filepath.Clean(leftPath) == filepath.Clean(rightPath)
}

func validateProfileStackRepository(repositoryPath string) error {
	if _, err := os.Stat(filepath.Join(expandUserPath(repositoryPath), ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("configuration repository is not ready; run Platform once before managing stacks: %w", os.ErrNotExist)
		}
		return err
	}
	return nil
}

func loadProfileStackRepositorySnapshot(ctx context.Context, repositoryPath string, choice profileChoice) (profileStackRepositorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return profileStackRepositorySnapshot{}, err
	}
	stacks, err := loadEditableStacksContext(ctx, repositoryPath)
	if err != nil {
		return profileStackRepositorySnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return profileStackRepositorySnapshot{}, err
	}
	status, err := stackRepositoryStatus(ctx, repositoryPath)
	if err != nil {
		return profileStackRepositorySnapshot{}, normalizeProfileStackContextError(ctx, err)
	}
	snapshot := profileStackRepositorySnapshot{Stacks: stacks, GitStatus: status}
	if status != "clean" {
		snapshot.SyncStatus = "commit required"
		return snapshot, nil
	}
	if stack, ok := firstStackMissingMetadata(stacks); ok {
		snapshot.SyncStatus = "review required"
		snapshot.Notice = stackNeedsMetadataMessage(stack.Name)
		return snapshot, nil
	}
	head, err := stackRepositoryHead(ctx, repositoryPath)
	if err != nil {
		return profileStackRepositorySnapshot{}, normalizeProfileStackContextError(ctx, err)
	}
	needsPush, err := stackRepositoryNeedsPush(ctx, repositoryPath, head)
	if err != nil {
		return profileStackRepositorySnapshot{}, normalizeProfileStackContextError(ctx, err)
	}
	snapshot.Head = head
	snapshot.NeedsPush = needsPush
	switch {
	case needsPush:
		snapshot.SyncStatus = "push required"
	case choice.State.StackRepositoryCommit != head:
		snapshot.SyncStatus = "sync required"
	default:
		snapshot.SyncStatus = "in sync"
	}
	return snapshot, nil
}

func normalizeProfileStackContextError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (model profileSetupModel) profileStackOperationBusy() bool {
	return model.stackOperation != ""
}

func (model profileSetupModel) startProfileStackOperation(kind profileStackOperationKind, commitMessage string, quietMissing bool) (profileSetupModel, tea.Cmd) {
	if model.profileStackOperationBusy() {
		return model, nil
	}
	request, err := model.profileStackRequest(kind, commitMessage, quietMissing)
	if err != nil {
		model.err = err.Error()
		return model, nil
	}
	return model.startProfileStackOperationRequest(request)
}

func (model profileSetupModel) startProfileStackOperationRequest(request profileStackOperationRequest) (profileSetupModel, tea.Cmd) {
	if model.profileStackOperationBusy() {
		return model, nil
	}
	parentCtx := model.tuiContext
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(parentCtx)
	model.stackOperation = request.Kind
	model.stackOperationCancel = cancel
	model.stackOperationCancelling = false
	model.err = ""
	operationCommand := func() tea.Msg {
		result, operationErr := runProfileStackOperation(operationCtx, request)
		return profileStackOperationMsg{
			Kind:          request.Kind,
			CommitMessage: request.CommitMessage,
			Result:        result,
			Err:           operationErr,
		}
	}
	return model, tea.Batch(operationCommand, model.stackSpinner.Tick)
}

func (model profileSetupModel) profileStackRequest(kind profileStackOperationKind, commitMessage string, quietMissing bool) (profileStackOperationRequest, error) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) {
		return profileStackOperationRequest{}, errors.New(setupNoProfileSelectedMessage)
	}
	choice := model.profiles[model.selectedIndex]
	repositoryPath := strings.TrimSpace(choice.Profile.ConfigRepositoryPath)
	if repositoryPath == "" {
		if quietMissing {
			return profileStackOperationRequest{}, nil
		}
		return profileStackOperationRequest{}, errors.New("configuration repository is not ready; run Platform once before managing stacks")
	}
	return profileStackOperationRequest{
		Kind:           kind,
		RepositoryPath: repositoryPath,
		Choice:         choice,
		ProfileStore:   model.profileStore,
		CommitMessage:  commitMessage,
		QuietMissing:   quietMissing,
	}, nil
}

func (model profileSetupModel) startProfileStackRefreshIfConfigured() (profileSetupModel, tea.Cmd) {
	if model.selectedIndex < 0 || model.selectedIndex >= len(model.profiles) || strings.TrimSpace(model.profiles[model.selectedIndex].Profile.ConfigRepositoryPath) == "" {
		return model, nil
	}
	return model.startProfileStackOperation(profileStackOperationRefresh, "", true)
}

func (model profileSetupModel) updateProfileStackBusyKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case setupKeyCtrlC, "esc":
		if !model.stackOperationCancelling {
			model.stackOperationCancelling = true
			if model.stackOperationCancel != nil {
				model.stackOperationCancel()
			}
		}
	}
	return model, nil
}

func (model profileSetupModel) updateProfileStackSpinner(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	if !model.profileStackOperationBusy() {
		return model, nil
	}
	var command tea.Cmd
	model.stackSpinner, command = model.stackSpinner.Update(msg)
	return model, command
}

func (model profileSetupModel) applyProfileStackOperation(msg profileStackOperationMsg) (tea.Model, tea.Cmd) {
	if msg.Kind != model.stackOperation {
		return model, nil
	}
	wasCancelling := model.stackOperationCancelling
	model.finishProfileStackOperation()
	if msg.Result.ChoiceUpdated {
		model.applyProfileStackChoice(msg.Result.Choice)
	}
	if msg.Kind == profileStackOperationSave && msg.Result.Save.ProfileSecretsUpdated {
		model.applyStackEditorProfileSecrets(msg.Result.Save)
	}
	if msg.Err != nil {
		if errors.Is(msg.Err, context.Canceled) && msg.Result.MutationStarted && msg.Kind != profileStackOperationSave {
			return model.reconcileCancelledProfileStackMutation(msg.Kind)
		}
		return model.applyProfileStackOperationError(msg, wasCancelling), nil
	}
	return model.applyProfileStackOperationSuccess(msg, wasCancelling)
}

func (model profileSetupModel) applyProfileStackOperationError(msg profileStackOperationMsg, wasCancelling bool) profileSetupModel {
	if wasCancelling && errors.Is(msg.Err, context.Canceled) {
		model.err = ""
		model.stackNotice = profileStackCancellationNotice(msg.Kind)
		if msg.Kind == profileStackOperationDelete {
			model.screen = profileSetupScreenStacks
			model.stackDeleteInput.Blur()
		}
	} else {
		model.err = sanitizeTerminalLine(msg.Err.Error())
	}
	if msg.Kind == profileStackOperationCommit {
		model.stackCommitInput.Focus()
	} else if msg.Kind == profileStackOperationDelete && model.screen == profileSetupScreenStackDeleteConfirm {
		model.stackDeleteInput.Focus()
	}
	return model
}

func (model profileSetupModel) applyProfileStackOperationSuccess(msg profileStackOperationMsg, wasCancelling bool) (tea.Model, tea.Cmd) {
	cancellationRequested := wasCancelling || msg.Result.CancellationObserved
	switch msg.Kind {
	case profileStackOperationRefresh:
		model.applyProfileStackSnapshot(msg.Result.Snapshot)
		if wasCancelling {
			model.stackNotice = "Repository refresh completed before cancellation took effect."
		}
		return model, nil
	case profileStackOperationSync:
		model.applyProfileStackSnapshot(msg.Result.Snapshot)
		if wasCancelling {
			model.stackNotice = "Repository check completed before cancellation took effect. Synchronization was not started."
			return model, nil
		}
		return model.finishStackSync()
	case profileStackOperationDiff:
		model.stackDiffViewport.SetContent(sanitizeTerminalText(msg.Result.Diff))
		model.stackDiffViewport.GotoTop()
		model.screen = profileSetupScreenStackDiff
		model.err = ""
		if wasCancelling {
			model.stackNotice = "Repository diff completed before cancellation took effect."
		}
		return model, nil
	case profileStackOperationStage:
		model.stackNotice = profileStackCompletionNotice("All changes under stacks/ are staged.", cancellationRequested)
	case profileStackOperationCommit:
		model.stackCommitInput.Blur()
		model.stackNotice = profileStackCompletionNotice(fmt.Sprintf("Committed stack changes: %s. Press y to synchronize the server.", msg.CommitMessage), cancellationRequested)
		model.screen = profileSetupScreenStacks
		model.stackTable.Focus()
	case profileStackOperationPush:
		model.stackNotice = profileStackCompletionNotice("Pushed the current configuration branch to origin.", cancellationRequested)
	case profileStackOperationSave:
		model.finishStackEditorSave(msg.Result.Save)
		if wasCancelling || msg.Result.Save.CancellationObserved {
			model.stackNotice = profileStackCompletionNotice(model.stackNotice, true)
		}
	case profileStackOperationDelete:
		model.stackDeleteInput.Blur()
		model.screen = profileSetupScreenStacks
		name := safeStackConfirmationName(msg.Result.DeletedStackName)
		model.stackNotice = profileStackCompletionNotice(
			fmt.Sprintf("Stack %s removed. Review and commit the deletion before deployment.", name),
			cancellationRequested,
		)
	}
	model.err = ""
	return model.startProfileStackOperation(profileStackOperationRefresh, "", false)
}

func (model profileSetupModel) reconcileCancelledProfileStackMutation(kind profileStackOperationKind) (tea.Model, tea.Cmd) {
	model.err = ""
	model.screen = profileSetupScreenStacks
	model.stackCommitInput.Blur()
	switch kind {
	case profileStackOperationPush:
		model.stackNotice = "Push cancellation reached Git after the mutation started. The remote outcome is unknown; check origin before retrying. Refreshing repository state."
	case profileStackOperationCommit:
		model.stackNotice = "Commit cancellation reached Git after the mutation started. The commit may have completed; refreshing repository state before another action."
	case profileStackOperationDelete:
		model.stackNotice = "Stack removal cancellation arrived after filesystem deletion started. The directory may be partially or fully removed; refreshing repository state before retrying."
	default:
		model.stackNotice = "Staging cancellation reached Git after the mutation started. The index may have changed; refreshing repository state before another action."
	}
	return model.startProfileStackOperation(profileStackOperationRefresh, "", false)
}

func (model *profileSetupModel) finishProfileStackOperation() {
	if model.stackOperationCancel != nil {
		model.stackOperationCancel()
	}
	model.stackOperation = ""
	model.stackOperationCancel = nil
	model.stackOperationCancelling = false
}

func (model *profileSetupModel) applyProfileStackSnapshot(snapshot profileStackRepositorySnapshot) {
	if snapshot.Unavailable {
		model.stacks = nil
		model.stackGitStatus = ""
		model.stackHead = ""
		model.stackNeedsPush = false
		model.stackSyncStatus = ""
		model.stackTable = newStackTable(nil, "", nil)
		model.resizeStackTable()
		if model.screen == profileSetupScreenDashboard {
			model.stackTable.Blur()
		}
		model.err = ""
		return
	}
	model.stacks = snapshot.Stacks
	if model.selectedIndex >= 0 && model.selectedIndex < len(model.profiles) {
		choice := model.profiles[model.selectedIndex]
		model.stackTable = newStackTable(snapshot.Stacks, choice.Profile.BaseDomain, &choice.State)
	} else {
		model.stackTable = newStackTable(snapshot.Stacks, "", nil)
	}
	model.resizeStackTable()
	if model.screen == profileSetupScreenDashboard {
		model.stackTable.Blur()
	} else if model.screen == profileSetupScreenStacks {
		model.stackTable.Focus()
	}
	model.stackGitStatus = snapshot.GitStatus
	model.stackHead = snapshot.Head
	model.stackNeedsPush = snapshot.NeedsPush
	model.stackSyncStatus = snapshot.SyncStatus
	if snapshot.Notice != "" {
		model.stackNotice = snapshot.Notice
	}
	model.err = ""
}

func (model profileSetupModel) profileStackOperationView() string {
	label := profileStackOperationLabel(model.stackOperation)
	if model.stackOperationCancelling {
		label = "Cancelling " + strings.ToLower(label)
	}
	return fmt.Sprintf("%s %s...\n%s", model.stackSpinner.View(), label, setupHelpStyle.Render("Ctrl+C or Esc cancels. Other keys are disabled until this finishes."))
}

func profileStackOperationLabel(kind profileStackOperationKind) string {
	switch kind {
	case profileStackOperationRefresh:
		return "Refreshing stack repository"
	case profileStackOperationDiff:
		return "Loading stack repository diff"
	case profileStackOperationStage:
		return "Staging stack changes"
	case profileStackOperationCommit:
		return "Committing stack changes"
	case profileStackOperationPush:
		return "Pushing stack repository"
	case profileStackOperationSync:
		return "Checking stack repository before synchronization"
	case profileStackOperationSave:
		return "Saving stack configuration"
	case profileStackOperationDelete:
		return "Removing stack configuration"
	default:
		return "Running stack repository operation"
	}
}

func (model *profileSetupModel) applyStackEditorProfileSecrets(result profileStackSaveResult) {
	for index := range model.profiles {
		if model.profiles[index].Profile.ID == result.ProfileID {
			model.profiles[index].Secrets = result.ProfileSecrets
			return
		}
	}
}

func (model *profileSetupModel) applyProfileStackChoice(choice profileChoice) {
	for index := range model.profiles {
		if model.profiles[index].Profile.ID == choice.Profile.ID {
			model.profiles[index] = choice
			return
		}
	}
}

func profileStackCancellationNotice(kind profileStackOperationKind) string {
	return profileStackOperationLabel(kind) + " cancelled."
}

func profileStackCompletionNotice(notice string, cancellationRequested bool) string {
	if !cancellationRequested {
		return notice
	}
	return notice + " The operation completed before cancellation took effect."
}
