package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/bootstrap"
)

type apiPayload map[string]any

func TestReplaceCommandUpdatesCurrentFactsAndKeepsRevisionHistory(t *testing.T) {
	server := httptest.NewServer(bootstrap.NewHandler())
	t.Cleanup(server.Close)
	client := server.Client()

	createGate(t, client, server.URL, apiPayload{
		"id":               "gate-replace",
		"name":             "替换闸门",
		"max_aperture":     6.0,
		"initial_aperture": 0.5,
	})
	commandID := createCommandID(t, client, server.URL, apiPayload{
		"gate_id":         "gate-replace",
		"target_aperture": 2.75,
		"reason":          "上游水位调整",
		"requested_by":    "dispatcher-lin",
	})

	response := requestJSON(t, client, http.MethodPut, server.URL+"/commands/"+commandID, apiPayload{
		"target_aperture":  3.5,
		"reason":           "最新来水预报",
		"requested_by":     "dispatcher-lin",
		"expected_version": 1,
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replace status: expected %d, got %d: %s", http.StatusOK, response.StatusCode, readBody(t, response))
	}

	command := decodeResponseCommand(t, response)
	if got := command["target_aperture"]; got != 3.5 {
		t.Fatalf("current target aperture: expected 3.5, got %v", got)
	}
	if got := command["reason"]; got != "最新来水预报" {
		t.Fatalf("current reason: expected replacement reason, got %v", got)
	}
	if got := command["starting_aperture"]; got != 0.5 {
		t.Fatalf("starting aperture changed: expected 0.5, got %v", got)
	}
	if got := command["version"]; got != float64(2) {
		t.Fatalf("version: expected 2, got %v", got)
	}

	revisions, ok := command["revisions"].([]any)
	if !ok {
		t.Fatalf("revisions is missing or not an array: %T", command["revisions"])
	}
	if len(revisions) != 1 {
		t.Fatalf("revision count: expected 1, got %d", len(revisions))
	}
	oldRevision, ok := revisions[0].(map[string]any)
	if !ok {
		t.Fatalf("revision is not an object: %T", revisions[0])
	}
	if got := oldRevision["target_aperture"]; got != 2.75 {
		t.Fatalf("old target aperture: expected 2.75, got %v", got)
	}
	if got := oldRevision["reason"]; got != "上游水位调整" {
		t.Fatalf("old reason: expected original reason, got %v", got)
	}
	if got := oldRevision["version"]; got != float64(1) {
		t.Fatalf("old revision version: expected 1, got %v", got)
	}
	if _, exists := oldRevision["superseded_at"]; !exists {
		t.Fatal("old revision is missing superseded_at")
	}

	approvedResponse := postJSON(t, client, server.URL, "/commands/"+commandID+"/interlock", apiPayload{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "复核当前申请",
	})
	defer approvedResponse.Body.Close()
	if approvedResponse.StatusCode != http.StatusOK {
		t.Fatalf("review status: expected %d, got %d: %s", http.StatusOK, approvedResponse.StatusCode, readBody(t, approvedResponse))
	}
	approved := decodeResponseCommand(t, approvedResponse)
	if got := approved["target_aperture"]; got != 3.5 {
		t.Fatalf("reviewed target: expected current facts 3.5, got %v", got)
	}
	if _, exists := approved["execution_token"]; !exists {
		t.Fatal("clear review did not issue an execution token")
	}
}

func TestReplaceCommandStaleVersionFailsWithoutMutation(t *testing.T) {
	server := httptest.NewServer(bootstrap.NewHandler())
	t.Cleanup(server.Close)
	client := server.Client()

	createGate(t, client, server.URL, apiPayload{
		"id":               "gate-stale",
		"name":             "重复替换闸门",
		"max_aperture":     6.0,
		"initial_aperture": 1.0,
	})
	commandID := createCommandID(t, client, server.URL, apiPayload{
		"gate_id":         "gate-stale",
		"target_aperture": 2.0,
		"reason":          "第一版依据",
		"requested_by":    "dispatcher-lin",
	})

	first := requestJSON(t, client, http.MethodPut, server.URL+"/commands/"+commandID, apiPayload{
		"target_aperture":  2.5,
		"reason":           "第二版依据",
		"requested_by":     "dispatcher-lin",
		"expected_version": 1,
	})
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first replace status: expected %d, got %d", http.StatusOK, first.StatusCode)
	}

	stale := requestJSON(t, client, http.MethodPut, server.URL+"/commands/"+commandID, apiPayload{
		"target_aperture":  3.0,
		"reason":           "重复提交依据",
		"requested_by":     "dispatcher-lin",
		"expected_version": 1,
	})
	stale.Body.Close()
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale replace status: expected %d, got %d", http.StatusConflict, stale.StatusCode)
	}

	fetchResponse := getJSON(t, client, server.URL, "/commands/"+commandID)
	defer fetchResponse.Body.Close()
	if fetchResponse.StatusCode != http.StatusOK {
		t.Fatalf("fetch command status: expected %d, got %d", http.StatusOK, fetchResponse.StatusCode)
	}
	command := decodeResponseCommand(t, fetchResponse)
	if got := command["target_aperture"]; got != 2.5 {
		t.Fatalf("stale replacement mutated target: expected 2.5, got %v", got)
	}
	if got := command["reason"]; got != "第二版依据" {
		t.Fatalf("stale replacement mutated reason: got %v", got)
	}
	if got := command["version"]; got != float64(2) {
		t.Fatalf("stale replacement changed version: expected 2, got %v", got)
	}
	revisions := command["revisions"].([]any)
	if len(revisions) != 1 {
		t.Fatalf("failed replacement appended history: expected 1 revision, got %d", len(revisions))
	}
}

func TestReplaceCommandRejectedAfterReviewDoesNotChangeApplication(t *testing.T) {
	server := httptest.NewServer(bootstrap.NewHandler())
	t.Cleanup(server.Close)
	client := server.Client()

	createGate(t, client, server.URL, apiPayload{
		"id":               "gate-reviewed",
		"name":             "已复核闸门",
		"max_aperture":     5.0,
		"initial_aperture": 1.0,
	})
	commandID := createCommandID(t, client, server.URL, apiPayload{
		"gate_id":         "gate-reviewed",
		"target_aperture": 2.0,
		"reason":          "复核依据",
		"requested_by":    "dispatcher-lin",
	})

	reviewResponse := postJSON(t, client, server.URL, "/commands/"+commandID+"/interlock", apiPayload{
		"verdict":  "blocked",
		"verifier": "safety-wu",
		"note":     "条件不满足",
	})
	reviewResponse.Body.Close()
	if reviewResponse.StatusCode != http.StatusOK {
		t.Fatalf("review status: expected %d, got %d", http.StatusOK, reviewResponse.StatusCode)
	}

	replaceResponse := requestJSON(t, client, http.MethodPut, server.URL+"/commands/"+commandID, apiPayload{
		"target_aperture":  2.5,
		"reason":           "复核后修改",
		"requested_by":     "dispatcher-lin",
		"expected_version": 2,
	})
	replaceResponse.Body.Close()
	if replaceResponse.StatusCode != http.StatusConflict {
		t.Fatalf("post-review replace status: expected %d, got %d", http.StatusConflict, replaceResponse.StatusCode)
	}

	fetchResponse := getJSON(t, client, server.URL, "/commands/"+commandID)
	defer fetchResponse.Body.Close()
	command := decodeResponseCommand(t, fetchResponse)
	if got := command["status"]; got != "rejected" {
		t.Fatalf("status changed: expected rejected, got %v", got)
	}
	if got := command["target_aperture"]; got != 2.0 {
		t.Fatalf("post-review replacement mutated target: expected 2.0, got %v", got)
	}
	if revisions, exists := command["revisions"]; exists {
		t.Fatalf("post-review replacement appended revisions: %v", revisions)
	}
}

func TestReplaceAndReviewRaceHasOneWinner(t *testing.T) {
	server := httptest.NewServer(bootstrap.NewHandler())
	t.Cleanup(server.Close)
	client := server.Client()

	createGate(t, client, server.URL, apiPayload{
		"id":               "gate-race",
		"name":             "并发闸门",
		"max_aperture":     5.0,
		"initial_aperture": 1.0,
	})
	commandID := createCommandID(t, client, server.URL, apiPayload{
		"gate_id":         "gate-race",
		"target_aperture": 2.0,
		"reason":          "并发依据",
		"requested_by":    "dispatcher-lin",
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var replaceStatus, reviewStatus int
	go func() {
		defer wg.Done()
		<-start
		response := requestJSON(t, client, http.MethodPut, server.URL+"/commands/"+commandID, apiPayload{
			"target_aperture":  2.5,
			"reason":           "替换获胜依据",
			"requested_by":     "dispatcher-lin",
			"expected_version": 1,
		})
		response.Body.Close()
		replaceStatus = response.StatusCode
	}()
	go func() {
		defer wg.Done()
		<-start
		response := postJSON(t, client, server.URL, "/commands/"+commandID+"/interlock", apiPayload{
			"verdict":  "clear",
			"verifier": "safety-wu",
			"note":     "复核获胜",
		})
		response.Body.Close()
		reviewStatus = response.StatusCode
	}()
	close(start)
	wg.Wait()

	if replaceStatus == http.StatusOK && reviewStatus == http.StatusOK {
		t.Fatal("both replacement and review succeeded")
	}
	if replaceStatus != http.StatusOK && replaceStatus != http.StatusConflict {
		t.Fatalf("unexpected replace race status %d", replaceStatus)
	}
	if reviewStatus != http.StatusOK && reviewStatus != http.StatusConflict {
		t.Fatalf("unexpected review race status %d", reviewStatus)
	}

	fetchResponse := getJSON(t, client, server.URL, "/commands/"+commandID)
	defer fetchResponse.Body.Close()
	command := decodeResponseCommand(t, fetchResponse)
	if reviewStatus == http.StatusOK {
		if got := command["status"]; got != "approved" {
			t.Fatalf("review winner status: expected approved, got %v", got)
		}
		if got := command["target_aperture"]; got != 2.0 {
			t.Fatalf("review winner target: expected original 2.0, got %v", got)
		}
	} else {
		if got := command["status"]; got != "pending" {
			t.Fatalf("replace winner status: expected pending, got %v", got)
		}
		if got := command["target_aperture"]; got != 2.5 {
			t.Fatalf("replace winner target: expected 2.5, got %v", got)
		}
	}
}

func createGate(t *testing.T, client *http.Client, baseURL string, payload apiPayload) {
	t.Helper()
	response := postJSON(t, client, baseURL, "/gates", payload)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create gate status: expected %d, got %d: %s", http.StatusCreated, response.StatusCode, readBody(t, response))
	}
}

func createCommandID(t *testing.T, client *http.Client, baseURL string, payload apiPayload) string {
	t.Helper()
	response := postJSON(t, client, baseURL, "/commands", payload)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create command status: expected %d, got %d: %s", http.StatusCreated, response.StatusCode, readBody(t, response))
	}
	command := decodeResponseCommand(t, response)
	id, ok := command["id"].(string)
	if !ok || id == "" {
		t.Fatalf("created command is missing id: %v", command)
	}
	return id
}

func postJSON(t *testing.T, client *http.Client, baseURL, path string, payload apiPayload) *http.Response {
	t.Helper()
	return requestJSON(t, client, http.MethodPost, baseURL+path, payload)
}

func getJSON(t *testing.T, client *http.Client, baseURL, path string) *http.Response {
	t.Helper()
	return requestJSON(t, client, http.MethodGet, baseURL+path, nil)
}

func requestJSON(t *testing.T, client *http.Client, method, url string, payload apiPayload) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}
	request, err := http.NewRequest(method, url, &body)
	if err != nil {
		t.Fatalf("build %s request: %v", method, err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	t.Cleanup(cancel)
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		t.Fatalf("perform %s request: %v", method, err)
	}
	return response
}

func decodeResponseCommand(t *testing.T, response *http.Response) map[string]any {
	t.Helper()
	var body struct {
		Command map[string]any `json:"command"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response command: %v", err)
	}
	if body.Command == nil {
		t.Fatal("response is missing command")
	}
	return body.Command
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "<unreadable response body>"
	}
	return string(raw)
}
