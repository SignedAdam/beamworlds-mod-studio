package main

import (
	"context"
	"errors"
	"testing"
)

func TestAppServiceCancelsRemoteBeforeWorkspaceLock(t *testing.T) {
	manager := NewAgentManager(nil, AppConfig{}, nil)
	service := &AppService{
		agents:       manager,
		gitRemoteOps: make(map[string][]*appGitRemoteOperation),
	}
	lock := manager.workspaceToolMutex("blocked-workspace")
	lock.Lock()
	ctx, unregister := service.beginWorkspaceGitRemote("blocked-workspace")
	defer unregister()

	lockResult := make(chan error, 1)
	commandStarted := make(chan struct{}, 1)
	go func() {
		err := lockMutexContext(ctx, lock)
		if err == nil {
			commandStarted <- struct{}{}
			lock.Unlock()
		}
		lockResult <- err
	}()

	if !service.cancelWorkspaceGitOperations("blocked-workspace") {
		t.Fatal("cancel did not report the pending remote operation")
	}
	lock.Unlock()

	select {
	case err := <-lockResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("workspace lock result = %v, want context.Canceled", err)
		}
	case <-commandStarted:
		t.Fatal("remote command started after cancellation")
	}
	select {
	case <-commandStarted:
		t.Fatal("remote command acquired the workspace lock after cancellation")
	default:
	}
}
