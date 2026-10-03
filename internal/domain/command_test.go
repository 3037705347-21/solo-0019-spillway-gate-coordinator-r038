package domain_test

import (
	"errors"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

func TestGateCommandReplaceAppendsOldFacts(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版依据", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}

	replacedAt := now.Add(time.Minute)
	if err := command.Replace(3.0, 5.0, "第二版依据", "dispatcher-lin", 1, replacedAt); err != nil {
		t.Fatalf("replace command: %v", err)
	}

	if command.Status != domain.CommandPending {
		t.Fatalf("status: expected pending, got %s", command.Status)
	}
	if command.TargetAperture != 3.0 {
		t.Fatalf("current target: expected 3.0, got %v", command.TargetAperture)
	}
	if command.Reason != "第二版依据" {
		t.Fatalf("current reason: expected replacement reason, got %q", command.Reason)
	}
	if command.StartingAperture != 1.0 {
		t.Fatalf("starting aperture changed: expected 1.0, got %v", command.StartingAperture)
	}
	if command.Version != 2 {
		t.Fatalf("version: expected 2, got %d", command.Version)
	}
	if !command.UpdatedAt.Equal(replacedAt) {
		t.Fatalf("updated at: expected %s, got %s", replacedAt, command.UpdatedAt)
	}
	if len(command.Revisions) != 1 {
		t.Fatalf("revision count: expected 1, got %d", len(command.Revisions))
	}
	revision := command.Revisions[0]
	if revision.TargetAperture != 2.0 || revision.Reason != "第一版依据" {
		t.Fatalf("revision does not retain old facts: %+v", revision)
	}
	if revision.Version != 1 || !revision.SupersededAt.Equal(replacedAt) {
		t.Fatalf("revision metadata mismatch: %+v", revision)
	}
}

func TestGateCommandReplaceRejectsStaleVersionWithoutMutation(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版依据", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	if err := command.Replace(3.0, 5.0, "第二版依据", "dispatcher-lin", 1, now.Add(time.Minute)); err != nil {
		t.Fatalf("first replacement: %v", err)
	}

	err = command.Replace(4.0, 5.0, "重复替换", "dispatcher-lin", 1, now.Add(2*time.Minute))
	if !hasErrorCode(err, "command_version_conflict") {
		t.Fatalf("stale replacement error: expected command_version_conflict, got %v", err)
	}
	if command.TargetAperture != 3.0 || command.Reason != "第二版依据" {
		t.Fatalf("failed replacement changed facts: target=%v reason=%q", command.TargetAperture, command.Reason)
	}
	if command.Version != 2 || len(command.Revisions) != 1 {
		t.Fatalf("failed replacement changed version history: version=%d revisions=%d", command.Version, len(command.Revisions))
	}
}

func TestGateCommandReplaceOnlyAllowedWhilePending(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版依据", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	decision, err := domain.NewInterlockDecision("blocked", "safety-wu", "条件不满足", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("create decision: %v", err)
	}
	if err := command.Review(decision, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("review command: %v", err)
	}

	err = command.Replace(3.0, 5.0, "复核后修改", "dispatcher-lin", 2, now.Add(2*time.Minute))
	if !hasErrorCode(err, "command_not_pending") {
		t.Fatalf("post-review replacement error: expected command_not_pending, got %v", err)
	}
	if command.TargetAperture != 2.0 || command.Reason != "第一版依据" {
		t.Fatalf("failed replacement changed reviewed facts: target=%v reason=%q", command.TargetAperture, command.Reason)
	}
	if command.Status != domain.CommandRejected || len(command.Revisions) != 0 {
		t.Fatalf("failed replacement changed status or history: status=%s revisions=%d", command.Status, len(command.Revisions))
	}
}

func TestGateCommandReplaceRejectsUnchangedOrInvalidFacts(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	command, err := domain.NewGateCommand("cmd-1", "gate-1", 2.0, 1.0, 5.0, "第一版依据", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}

	tests := []struct {
		name           string
		targetAperture float64
		maxAperture    float64
		reason         string
		requestedBy    string
		expectedCode   string
	}{
		{name: "missing version", targetAperture: 3.0, maxAperture: 5.0, reason: "新版本", requestedBy: "dispatcher-lin", expectedCode: "invalid_expected_version"},
		{name: "out of range", targetAperture: 6.0, maxAperture: 5.0, reason: "新版本", requestedBy: "dispatcher-lin", expectedCode: "invalid_target_aperture"},
		{name: "same as starting aperture", targetAperture: 1.0, maxAperture: 5.0, reason: "新版本", requestedBy: "dispatcher-lin", expectedCode: "no_aperture_change"},
		{name: "blank reason", targetAperture: 3.0, maxAperture: 5.0, reason: "   ", requestedBy: "dispatcher-lin", expectedCode: "invalid_command_reason"},
		{name: "identical facts", targetAperture: 2.0, maxAperture: 5.0, reason: "第一版依据", requestedBy: "dispatcher-lin", expectedCode: "no_command_facts_change"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := command
			version := int64(1)
			if test.expectedCode == "invalid_expected_version" {
				version = 0
			}
			err := copy.Replace(test.targetAperture, test.maxAperture, test.reason, test.requestedBy, version, now.Add(time.Minute))
			if !hasErrorCode(err, test.expectedCode) {
				t.Fatalf("error: expected %s, got %v", test.expectedCode, err)
			}
			if copy.TargetAperture != command.TargetAperture || copy.Reason != command.Reason {
				t.Fatal("invalid replacement mutated command facts")
			}
		})
	}
}

func hasErrorCode(err error, code string) bool {
	var target *domain.Error
	return errors.As(err, &target) && target.Code == code
}
