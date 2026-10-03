package main

import (
	"fmt"
	"net/http"
)

func runReplaceGateCommand(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-replace", "替换申请泄洪闸", 6, 0.5); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-replace", 2.75, "上游水位调整", "dispatcher-lin")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}

	replaced, err := client.put("/commands/"+commandID, map[string]any{
		"target_aperture":  3.5,
		"reason":           "最新来水预报要求调整",
		"requested_by":     "dispatcher-lin",
		"expected_version": 1,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(replaced, http.StatusOK, "replace pending command"); err != nil {
		return err
	}
	current, err := objectField(replaced.Body, "command")
	if err != nil {
		return err
	}
	target, err := floatField(current, "target_aperture")
	if err != nil {
		return err
	}
	version, err := numberField(current, "version")
	if err != nil {
		return err
	}
	if target != 3.5 || version != 2 {
		return fmt.Errorf("replacement mismatch: target=%v version=%v", target, version)
	}
	revisions, err := objectArrayField(current, "revisions")
	if err != nil {
		return err
	}
	if len(revisions) != 1 {
		return fmt.Errorf("old content traceability: expected 1 revision, got %d", len(revisions))
	}
	oldTarget, err := floatField(revisions[0], "target_aperture")
	if err != nil {
		return err
	}
	if oldTarget != 2.75 {
		return fmt.Errorf("old target: expected 2.75, got %v", oldTarget)
	}

	duplicate, err := client.put("/commands/"+commandID, map[string]any{
		"target_aperture":  4.0,
		"reason":           "重复替换验证",
		"requested_by":     "dispatcher-lin",
		"expected_version": 1,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(duplicate, http.StatusConflict, "reject stale replacement"); err != nil {
		return err
	}

	approved, err := fixtureClearReview(client, commandID, "safety-wu")
	if err != nil {
		return err
	}
	approvedTarget, err := floatField(approved, "target_aperture")
	if err != nil {
		return err
	}
	if approvedTarget != 3.5 {
		return fmt.Errorf("reviewed target: expected 3.5, got %v", approvedTarget)
	}

	afterReview, err := client.put("/commands/"+commandID, map[string]any{
		"target_aperture":  4.0,
		"reason":           "复核后替换验证",
		"requested_by":     "dispatcher-lin",
		"expected_version": 2,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(afterReview, http.StatusConflict, "reject replacement after review"); err != nil {
		return err
	}

	fetched, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(fetched, http.StatusOK, "read reviewed command"); err != nil {
		return err
	}
	persisted, err := objectField(fetched.Body, "command")
	if err != nil {
		return err
	}
	persistedTarget, err := floatField(persisted, "target_aperture")
	if err != nil {
		return err
	}
	status, err := stringField(persisted, "status")
	if err != nil {
		return err
	}
	if persistedTarget != 3.5 || status != "approved" {
		return fmt.Errorf("failed replacement changed application: target=%v status=%q", persistedTarget, status)
	}

	fmt.Println("replace-gate-command: current facts reviewed; stale and post-review replacements rejected")
	return nil
}
