package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errProfileOperationLocked = errors.New("profile is busy")
var errRepositoryOperationLocked = errors.New("configuration repository is busy")

type profileOperationLock interface {
	Release() error
}

type profileOperationLockStore interface {
	TryLockProfile(string) (profileOperationLock, error)
}

type repositoryOperationLockStore interface {
	TryLockRepository(string) (profileOperationLock, error)
}

type profileFileLock struct {
	file       *os.File
	once       sync.Once
	releaseErr error
}

type guardedProfileOperationLock struct {
	lock       profileOperationLock
	once       sync.Once
	releaseErr error
}

func (store *fileProfileStore) TryLockProfile(id string) (profileOperationLock, error) {
	if _, err := store.profileDirectory(id); err != nil {
		return nil, err
	}
	// Keep locks outside profile directories so profile deletion cannot replace a locked inode.
	lockDirectory := filepath.Join(store.root, "locks")
	if err := os.MkdirAll(lockDirectory, 0700); err != nil {
		return nil, fmt.Errorf("create profile lock directory: %w", err)
	}
	if err := os.Chmod(lockDirectory, 0700); err != nil {
		return nil, fmt.Errorf("secure profile lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(lockDirectory, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open profile lock: %w", err)
	}
	locked, err := tryLockProfileFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock profile %q: %w", id, err)
	}
	if !locked {
		_ = file.Close()
		return nil, fmt.Errorf("%w: profile %q has another setup run or local change in progress", errProfileOperationLocked, id)
	}
	return &profileFileLock{file: file}, nil
}

func (store *fileProfileStore) TryLockRepository(path string) (profileOperationLock, error) {
	canonicalPath, err := canonicalRepositoryLockPath(path)
	if err != nil {
		return nil, err
	}
	lockDirectory := filepath.Join(store.root, "locks", "repositories")
	if err := os.MkdirAll(lockDirectory, 0700); err != nil {
		return nil, fmt.Errorf("create repository lock directory: %w", err)
	}
	if err := os.Chmod(lockDirectory, 0700); err != nil {
		return nil, fmt.Errorf("secure repository lock directory: %w", err)
	}
	key := sha256.Sum256([]byte(canonicalPath))
	file, err := os.OpenFile(filepath.Join(lockDirectory, fmt.Sprintf("%x.lock", key)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open repository lock: %w", err)
	}
	locked, err := tryLockProfileFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock configuration repository %q: %w", canonicalPath, err)
	}
	if !locked {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %q has another local change in progress", errRepositoryOperationLocked, canonicalPath)
	}
	return &profileFileLock{file: file}, nil
}

func canonicalRepositoryLockPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("configuration repository lock path is required")
	}
	absolute, err := filepath.Abs(expandUserPath(path))
	if err != nil {
		return "", fmt.Errorf("resolve configuration repository lock path: %w", err)
	}
	current := filepath.Clean(absolute)
	missing := []string{}
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", fmt.Errorf("resolve configuration repository lock path: %w", resolveErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve configuration repository lock path: %w", resolveErr)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func (lock *profileFileLock) Release() error {
	lock.once.Do(func() {
		unlockErr := unlockProfileFile(lock.file)
		closeErr := lock.file.Close()
		lock.releaseErr = errors.Join(unlockErr, closeErr)
	})
	return lock.releaseErr
}

func guardProfileOperationLock(lock profileOperationLock) profileOperationLock {
	if lock == nil {
		return nil
	}
	if _, guarded := lock.(*guardedProfileOperationLock); guarded {
		return lock
	}
	return &guardedProfileOperationLock{lock: lock}
}

func (lock *guardedProfileOperationLock) Release() error {
	lock.once.Do(func() {
		lock.releaseErr = lock.lock.Release()
	})
	return lock.releaseErr
}

func acquireProfileOperationLock(store ProfileStore, profileID string) (profileOperationLock, error) {
	locker, ok := store.(profileOperationLockStore)
	if !ok {
		return nil, nil
	}
	return locker.TryLockProfile(profileID)
}

func acquireRepositoryOperationLock(store ProfileStore, repositoryPath string) (profileOperationLock, error) {
	locker, ok := store.(repositoryOperationLockStore)
	if !ok {
		return nil, nil
	}
	return locker.TryLockRepository(repositoryPath)
}

type combinedProfileOperationLock struct {
	locks      []profileOperationLock
	once       sync.Once
	releaseErr error
}

func combineProfileOperationLocks(locks ...profileOperationLock) profileOperationLock {
	combined := make([]profileOperationLock, 0, len(locks))
	for _, lock := range locks {
		if lock != nil {
			combined = append(combined, lock)
		}
	}
	if len(combined) == 0 {
		return nil
	}
	if len(combined) == 1 {
		return combined[0]
	}
	return &combinedProfileOperationLock{locks: combined}
}

func (lock *combinedProfileOperationLock) Release() error {
	lock.once.Do(func() {
		for index := len(lock.locks) - 1; index >= 0; index-- {
			lock.releaseErr = errors.Join(lock.releaseErr, lock.locks[index].Release())
		}
	})
	return lock.releaseErr
}

func lockAndLoadProfile(store ProfileStore, profileID string) (Profile, ProfileState, profileOperationLock, error) {
	lock, err := acquireProfileOperationLock(store, profileID)
	if err != nil {
		return Profile{}, ProfileState{}, nil, err
	}
	profile, state, err := store.Load(profileID)
	if err != nil {
		if lock != nil {
			_ = lock.Release()
		}
		return Profile{}, ProfileState{}, nil, err
	}
	state, err = normalizeInterruptedProfileRun(store, profile, state, lock)
	if err != nil {
		releaseProfileOperationLock(lock)
		return Profile{}, ProfileState{}, nil, err
	}
	return profile, state, lock, nil
}

func normalizeInterruptedProfileRun(store ProfileStore, profile Profile, state ProfileState, lock profileOperationLock) (ProfileState, error) {
	if lock == nil || !profileStateHasActiveRun(state) {
		return state, nil
	}
	runID := state.ActiveRunID
	run := state.Runs[runID]
	now := time.Now().UTC()
	const interruptedMessage = "setup run was interrupted before the previous Servestead process released its profile lock"
	terminalStage := terminalStageForRun(run, runStatusCancelled, "", "")
	for stageName, stage := range run.Stages {
		if stage.Status != stageStatusRunning && stageName != terminalStage {
			continue
		}
		stage.Status = stageStatusCancelled
		stage.LastEnded = now
		stage.LastError = interruptedMessage
		run.Stages[stageName] = stage
	}
	run.Status = runStatusCancelled
	run.UpdatedAt = now
	state.Runs[runID] = run
	state.ActiveRunID = ""
	if err := store.Save(profile, state); err != nil {
		return ProfileState{}, fmt.Errorf("recover interrupted setup run: %w", err)
	}
	_ = store.AppendRunEvent(profile.ID, runID, TaskEvent{
		Type:     TaskCancelled,
		RunID:    runID,
		Stage:    terminalStage,
		TaskName: profileRunStageLabel(terminalStage),
		Line:     interruptedMessage,
		Time:     now,
	})
	return state, nil
}

func releaseProfileOperationLock(lock profileOperationLock) {
	if lock != nil {
		_ = lock.Release()
	}
}

func deleteProfileWhenIdle(store ProfileStore, profileID string) error {
	_, state, lock, err := lockAndLoadProfile(store, profileID)
	if err != nil {
		return err
	}
	defer releaseProfileOperationLock(lock)
	if profileStateHasActiveRun(state) {
		return errors.New("cannot delete a profile while its setup run is active")
	}
	return store.Delete(profileID)
}
