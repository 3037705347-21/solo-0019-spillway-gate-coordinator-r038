package main

import (
	"fmt"
	"net/http"
	"sync"
)

func runReplacePendingCommand(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-replace", "替换流程泄洪闸", 6, 0.5); err != nil {
		return err
	}
	original, err := fixtureCommand(client, "gate-replace", 2.0, "原始申请依据", "dispatcher-lin")
	if err != nil {
		return err
	}
	originalID, err := stringField(original, "id")
	if err != nil {
		return err
	}

	// 1. Replace the pending application with new business facts.
	replacement, err := fixtureReplace(client, originalID, 3.25, "修订后的申请依据", "dispatcher-lin")
	if err != nil {
		return err
	}
	replacementID, err := stringField(replacement, "id")
	if err != nil {
		return err
	}
	if replacementID == originalID {
		return fmt.Errorf("replacement must get a new command id, got %q", replacementID)
	}
	if status, _ := stringField(replacement, "status"); status != "pending" {
		return fmt.Errorf("replacement status: expected pending, got %q", status)
	}
	if target, _ := floatField(replacement, "target_aperture"); target != 3.25 {
		return fmt.Errorf("replacement target aperture: expected 3.25, got %v", target)
	}
	if reason, _ := stringField(replacement, "reason"); reason != "修订后的申请依据" {
		return fmt.Errorf("replacement reason: expected revised reason, got %q", reason)
	}
	if starting, _ := floatField(replacement, "starting_aperture"); starting != 0.5 {
		return fmt.Errorf("replacement starting aperture: expected frozen 0.5, got %v", starting)
	}
	if supersedes, _ := stringField(replacement, "supersedes_id"); supersedes != originalID {
		return fmt.Errorf("replacement supersedes_id: expected %q, got %q", originalID, supersedes)
	}

	// 2. The old content stays traceable and points at the current application.
	oldView, err := fixtureCommandView(client, originalID)
	if err != nil {
		return err
	}
	if status, _ := stringField(oldView, "status"); status != "superseded" {
		return fmt.Errorf("original status: expected superseded, got %q", status)
	}
	if target, _ := floatField(oldView, "target_aperture"); target != 2.0 {
		return fmt.Errorf("original target must remain traceable: expected 2.0, got %v", target)
	}
	if reason, _ := stringField(oldView, "reason"); reason != "原始申请依据" {
		return fmt.Errorf("original reason must remain traceable, got %q", reason)
	}
	if by, _ := stringField(oldView, "superseded_by"); by != replacementID {
		return fmt.Errorf("original superseded_by: expected %q, got %q", replacementID, by)
	}
	if version, _ := floatField(oldView, "version"); version != 2 {
		return fmt.Errorf("original version: expected 2 after supersede, got %v", version)
	}

	// 3. The superseded content cannot be reviewed again.
	staleReview, err := client.post("/commands/"+originalID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "不应被复核",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(staleReview, http.StatusConflict, "reject review of superseded command"); err != nil {
		return err
	}
	if err := requireErrorCode(staleReview, "command_not_pending"); err != nil {
		return err
	}
	oldView, err = fixtureCommandView(client, originalID)
	if err != nil {
		return err
	}
	if status, _ := stringField(oldView, "status"); status != "superseded" {
		return fmt.Errorf("superseded command changed after failed review: status=%q", status)
	}

	// 4. Invalid replacement facts fail without changing the current application.
	invalid, err := client.post("/commands/"+replacementID+"/replacement", map[string]any{
		"target_aperture": 9.0,
		"reason":          "越界修订",
		"requested_by":    "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(invalid, http.StatusBadRequest, "reject out-of-range replacement"); err != nil {
		return err
	}
	current, err := fixtureCommandView(client, replacementID)
	if err != nil {
		return err
	}
	if status, _ := stringField(current, "status"); status != "pending" {
		return fmt.Errorf("current application changed after invalid replacement: status=%q", status)
	}
	if target, _ := floatField(current, "target_aperture"); target != 3.25 {
		return fmt.Errorf("current application changed after invalid replacement: target=%v", target)
	}
	if by, _ := stringField(current, "superseded_by"); by != "" {
		return fmt.Errorf("invalid replacement must not mark the current application superseded")
	}

	// 5. Replace again: the prior current application retires, the newest one is head.
	second, err := fixtureReplace(client, replacementID, 4.5, "二次修订依据", "dispatcher-lin")
	if err != nil {
		return err
	}
	secondID, err := stringField(second, "id")
	if err != nil {
		return err
	}
	if supersedes, _ := stringField(second, "supersedes_id"); supersedes != replacementID {
		return fmt.Errorf("second replacement supersedes_id: expected %q, got %q", replacementID, supersedes)
	}
	retired, err := fixtureCommandView(client, replacementID)
	if err != nil {
		return err
	}
	if status, _ := stringField(retired, "status"); status != "superseded" {
		return fmt.Errorf("first replacement status: expected superseded, got %q", status)
	}
	if by, _ := stringField(retired, "superseded_by"); by != secondID {
		return fmt.Errorf("first replacement superseded_by: expected %q, got %q", secondID, by)
	}

	// 6. Duplicate replacement of a non-head application is rejected.
	duplicate, err := client.post("/commands/"+replacementID+"/replacement", map[string]any{
		"target_aperture": 5.0,
		"reason":          "重复替换已失效申请",
		"requested_by":    "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(duplicate, http.StatusConflict, "reject replacement of superseded command"); err != nil {
		return err
	}
	if err := requireErrorCode(duplicate, "command_not_replaceable"); err != nil {
		return err
	}

	// 7. Only the current application can be reviewed; it reserves the gate.
	approved, err := fixtureClearReview(client, secondID, "safety-wu")
	if err != nil {
		return err
	}
	if status, _ := stringField(approved, "status"); status != "approved" {
		return fmt.Errorf("review of current application: expected approved, got %q", status)
	}
	token, err := stringField(approved, "execution_token")
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("review of current application did not issue an execution token")
	}
	gateResult, err := client.get("/gates/gate-replace")
	if err != nil {
		return err
	}
	gate, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	if status, _ := stringField(gate, "status"); status != "reserved" {
		return fmt.Errorf("gate status: expected reserved, got %q", status)
	}
	if active, _ := stringField(gate, "active_command_id"); active != secondID {
		return fmt.Errorf("gate active_command_id: expected %q, got %q", secondID, active)
	}

	// 8. Replacement is rejected once the command left pending, original stays.
	afterReview, err := client.post("/commands/"+secondID+"/replacement", map[string]any{
		"target_aperture": 5.0,
		"reason":          "复核后替换",
		"requested_by":    "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(afterReview, http.StatusConflict, "reject replacement after review"); err != nil {
		return err
	}
	if err := requireErrorCode(afterReview, "command_not_replaceable"); err != nil {
		return err
	}

	// 9. Current application executes; superseded commands never held the gate.
	executed, err := client.post("/commands/"+secondID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "execute current application"); err != nil {
		return err
	}

	// 10. Executing against a superseded command is rejected.
	staleExecute, err := client.post("/commands/"+replacementID+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(staleExecute, http.StatusConflict, "reject execution of superseded command"); err != nil {
		return err
	}

	// 11. Unknown command id for replacement is a 404.
	missing, err := client.post("/commands/does-not-exist/replacement", map[string]any{
		"target_aperture": 1.0,
		"reason":          "不存在的命令",
		"requested_by":    "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(missing, http.StatusNotFound, "replacement of unknown command"); err != nil {
		return err
	}

	// 12. Raced review vs replacement against the same pending command: exactly
	// one wins, the loser gets a conflict and neither record is left half-written.
	if err := runReplaceReviewRace(client); err != nil {
		return err
	}

	fmt.Println("replace-pending-command: current application replaced and reviewed; stale content traceable but inert")
	return nil
}

func runReplaceReviewRace(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-race", "竞态泄洪闸", 6, 1.0); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-race", 2.0, "竞态原始申请", "dispatcher-lin")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}

	const contenders = 8
	var wg sync.WaitGroup
	results := make([]apiResult, contenders)
	wg.Add(contenders)
	for i := 0; i < contenders; i++ {
		i := i
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				results[i], _ = client.post("/commands/"+commandID+"/replacement", map[string]any{
					"target_aperture": 3.0,
					"reason":          "并发替换",
					"requested_by":    "dispatcher-lin",
				})
			} else {
				results[i], _ = client.post("/commands/"+commandID+"/interlock", map[string]any{
					"verdict":  "clear",
					"verifier": "safety-wu",
					"note":     "并发复核",
				})
			}
		}()
	}
	wg.Wait()

	successes := 0
	for _, result := range results {
		if result.Status == http.StatusOK || result.Status == http.StatusCreated {
			successes++
			continue
		}
		if result.Status != http.StatusConflict {
			return fmt.Errorf("race loser: expected 409, got HTTP %d body %s", result.Status, prettyBody(result.Body))
		}
	}
	if successes != 1 {
		return fmt.Errorf("race: expected exactly one winner, got %d", successes)
	}

	original, err := fixtureCommandView(client, commandID)
	if err != nil {
		return err
	}
	originalStatus, _ := stringField(original, "status")

	gateResult, err := client.get("/gates/gate-race")
	if err != nil {
		return err
	}
	gate, err := objectField(gateResult.Body, "gate")
	if err != nil {
		return err
	}
	gateStatus, _ := stringField(gate, "status")
	activeID, _ := stringField(gate, "active_command_id")

	switch originalStatus {
	case "superseded":
		// Replacement won: gate must still be idle and unbound; the new head is pending.
		if gateStatus != "idle" || activeID != "" {
			return fmt.Errorf("replacement won but gate is bound: status=%q active=%q", gateStatus, activeID)
		}
		headID, _ := stringField(original, "superseded_by")
		head, err := fixtureCommandView(client, headID)
		if err != nil {
			return err
		}
		if status, _ := stringField(head, "status"); status != "pending" {
			return fmt.Errorf("replacement winner must be pending, got %q", status)
		}
	case "approved":
		// Review won: gate must be reserved by exactly this command.
		if gateStatus != "reserved" || activeID != commandID {
			return fmt.Errorf("review won but gate mismatch: status=%q active=%q", gateStatus, activeID)
		}
	default:
		return fmt.Errorf("race left original command in unexpected status %q", originalStatus)
	}
	return nil
}
