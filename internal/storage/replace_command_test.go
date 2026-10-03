package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
	"example.com/spillway-gate-coordinator/internal/storage"
)

func TestReplaceCommandPersistsCurrentFactsAndRevisionHistory(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	repository := storage.NewMemoryRepository()
	gate, err := domain.NewGate("gate-1", "一号闸门", 5.0, 1.0, now)
	if err != nil {
		t.Fatalf("create gate: %v", err)
	}
	if err := repository.CreateGate(ctx, gate); err != nil {
		t.Fatalf("store gate: %v", err)
	}
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	if err := repository.CreateCommand(ctx, command); err != nil {
		t.Fatalf("store command: %v", err)
	}

	replacedAt := now.Add(time.Minute)
	replaced, err := repository.ReplaceCommand(ctx, "cmd-1", 3.0, "第二版", "dispatcher-lin", 1, replacedAt)
	if err != nil {
		t.Fatalf("replace command: %v", err)
	}
	if replaced.TargetAperture != 3.0 || replaced.Reason != "第二版" {
		t.Fatalf("replacement facts mismatch: %+v", replaced)
	}
	if replaced.Version != 2 || len(replaced.Revisions) != 1 {
		t.Fatalf("replacement metadata mismatch: version=%d revisions=%d", replaced.Version, len(replaced.Revisions))
	}

	persisted, err := repository.Command(ctx, "cmd-1")
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if persisted.TargetAperture != 3.0 || persisted.Reason != "第二版" {
		t.Fatalf("persisted current facts mismatch: %+v", persisted)
	}
	if persisted.Revisions[0].TargetAperture != 2.0 || persisted.Revisions[0].Reason != "第一版" {
		t.Fatalf("persisted revision does not retain old facts: %+v", persisted.Revisions[0])
	}

	gateAfter, err := repository.Gate(ctx, "gate-1")
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if gateAfter.Status != domain.GateIdle || gateAfter.ActiveCommandID != "" {
		t.Fatalf("replacement reserved gate: status=%s active=%q", gateAfter.Status, gateAfter.ActiveCommandID)
	}
}

func TestReplaceCommandConcurrentDuplicateHasOneWinner(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	repository := storage.NewMemoryRepository()
	gate, err := domain.NewGate("gate-1", "一号闸门", 5.0, 1.0, now)
	if err != nil {
		t.Fatalf("create gate: %v", err)
	}
	if err := repository.CreateGate(ctx, gate); err != nil {
		t.Fatalf("store gate: %v", err)
	}
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	if err := repository.CreateCommand(ctx, command); err != nil {
		t.Fatalf("store command: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := repository.ReplaceCommand(ctx, "cmd-1", 3.0, "第二版A", "dispatcher-lin", 1, now.Add(time.Minute))
		results <- err
	}()
	go func() {
		<-start
		_, err := repository.ReplaceCommand(ctx, "cmd-1", 4.0, "第二版B", "dispatcher-lin", 1, now.Add(time.Minute))
		results <- err
	}()
	close(start)

	var failures int
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			failures++
			var domainError *domain.Error
			if !errors.As(err, &domainError) || domainError.Code != "command_version_conflict" {
				t.Fatalf("duplicate replacement error: expected command_version_conflict, got %v", err)
			}
		}
	}
	if failures != 1 {
		t.Fatalf("concurrent duplicate results: expected 1 failure, got %d", failures)
	}

	persisted, err := repository.Command(ctx, "cmd-1")
	if err != nil {
		t.Fatalf("read command: %v", err)
	}
	if persisted.Version != 2 || len(persisted.Revisions) != 1 {
		t.Fatalf("duplicate replacement changed history twice: version=%d revisions=%d", persisted.Version, len(persisted.Revisions))
	}
}
