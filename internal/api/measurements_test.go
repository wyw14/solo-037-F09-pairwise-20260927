package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

func TestMeasuredRunHTTPUsesOperatorEvidence(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.Operation.ID
	if id == "" {
		t.Fatal("missing ID")
	}
	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	input := `{"kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"` + stamp + `"}`
	recorded := httptest.NewRecorder()
	server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(id)+"/evidence", strings.NewReader(input)))
	if recorded.Code != http.StatusOK {
		t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(id)+"/analysis", nil))
	if analysis.Code != http.StatusOK || !strings.Contains(analysis.Body.String(), `"mean":0.24`) {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	reloaded, err := service.OpenRuntime(t.TempDir())
	if err != nil || len(reloaded.MeasuredRuns()) != 0 {
		t.Fatal("archive separation failed", err)
	}
}

func createMeasuredRunForSearch(t *testing.T, server http.Handler, body string) service.MeasuredRun {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("create run: %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func recordEvidenceForSearch(t *testing.T, server http.Handler, id, body string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("record evidence: %d %s", recorder.Code, recorder.Body)
	}
}

func TestSearchMeasurementsHTTPFiltersAndEchoesConditions(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	runA := createMeasuredRunForSearch(t, server, `{"name":"tow A","model_name":"hull-A","section":"section-a","carriage":"carriage-1"}`)
	runB := createMeasuredRunForSearch(t, server, `{"name":"tow B","model_name":"hull-B","section":"section-a","carriage":"carriage-1"}`)
	recordEvidenceForSearch(t, server, string(runA.Operation.ID), `{"kind":"wave_time","source_id":"probe-a","value":0.4,"recorded_at":"2026-09-01T09:00:00Z"}`)
	recordEvidenceForSearch(t, server, string(runA.Operation.ID), `{"kind":"force","source_id":"probe-b","value":7,"recorded_at":"2026-09-01T10:00:00Z"}`)
	recordEvidenceForSearch(t, server, string(runB.Operation.ID), `{"kind":"wave_time","source_id":"probe-a","value":0.9,"recorded_at":"2026-09-01T09:30:00Z"}`)

	url := "/api/measured-runs/search?model_name=hull-A&section=section-a&source_id=probe-a" +
		"&start=2026-09-01T08:00:00Z&end=2026-09-01T09:30:00Z"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("search: %d %s", recorder.Code, recorder.Body)
	}
	var response struct {
		Data struct {
			Items []struct {
				Operation model.Operation           `json:"operation"`
				Matches   []service.EvidenceSummary `json:"matches"`
			} `json:"items"`
			Filters struct {
				ModelName string    `json:"model_name"`
				Section   string    `json:"section"`
				SourceID  string    `json:"source_id"`
				Start     time.Time `json:"start"`
				End       time.Time `json:"end"`
			} `json:"filters"`
			Limit  int    `json:"limit"`
			Cursor string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 1 || response.Data.Items[0].Operation.ID != runA.Operation.ID {
		t.Fatalf("joint filter returned %+v", response.Data.Items)
	}
	if len(response.Data.Items[0].Matches) != 1 || response.Data.Items[0].Matches[0].Value != 0.4 {
		t.Fatalf("expected only probe-a's in-window frame, got %+v", response.Data.Items[0].Matches)
	}
	if response.Data.Filters.ModelName != "hull-A" || response.Data.Filters.SourceID != "probe-a" ||
		response.Data.Filters.Start.Format(time.RFC3339) != "2026-09-01T08:00:00Z" {
		t.Fatalf("pinned filters not echoed: %+v", response.Data.Filters)
	}
	if response.Data.Limit != 20 || response.Data.Cursor != "" {
		t.Fatalf("unexpected limit/cursor: %d %q", response.Data.Limit, response.Data.Cursor)
	}
}

func TestSearchMeasurementsHTTPPagesWithoutSkipOrDuplicate(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	var ids []string
	for _, name := range []string{"r1", "r2", "r3"} {
		run := createMeasuredRunForSearch(t, server, `{"name":"`+name+`","model_name":"hull-A","section":"section-a","carriage":"carriage-1"}`)
		ids = append(ids, string(run.Operation.ID))
	}

	fetch := func(rawURL string) (int, []model.RunID, string) {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, rawURL, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", rawURL, recorder.Code, recorder.Body)
		}
		var response struct {
			Data struct {
				Items []struct {
					Operation model.Operation `json:"operation"`
				} `json:"items"`
				Cursor string `json:"next_cursor"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		pageIDs := make([]model.RunID, 0, len(response.Data.Items))
		for _, item := range response.Data.Items {
			pageIDs = append(pageIDs, item.Operation.ID)
		}
		return recorder.Code, pageIDs, response.Data.Cursor
	}

	_, pageOne, cursor := fetch("/api/measured-runs/search?limit=2")
	if cursor == "" || len(pageOne) != 2 {
		t.Fatalf("first page should hold two runs and a cursor, got %+v", pageOne)
	}

	// New evidence lands in a run on page one while the client pages.
	recordEvidenceForSearch(t, server, string(pageOne[0]), `{"kind":"force","source_id":"probe-late","value":1,"recorded_at":"2026-09-02T00:00:00Z"}`)

	_, pageTwo, cursorTwo := fetch("/api/measured-runs/search?cursor=" + cursor)
	if len(pageTwo) != 1 || pageTwo[0] != model.RunID(ids[2]) {
		t.Fatalf("second page skipped or duplicated: %+v", pageTwo)
	}
	if cursorTwo != "" {
		t.Fatal("cursor should be empty after the final page")
	}

	// Conditions must stay consistent: filter arguments alongside a cursor
	// are rejected rather than silently changing the snapshot.
	conflict := httptest.NewRecorder()
	server.ServeHTTP(conflict, httptest.NewRequest(http.MethodGet, "/api/measured-runs/search?cursor="+cursor+"&model_name=hull-A", nil))
	if conflict.Code != http.StatusBadRequest {
		t.Fatalf("cursor with changed filter: expected 400, got %d", conflict.Code)
	}
}

func TestSearchMeasurementsHTTPRejectsBadParameters(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	cases := map[string]int{
		"/api/measured-runs/search?start=not-a-time":                                    http.StatusBadRequest,
		"/api/measured-runs/search?end=2026-09-01T09:00:00Z&start=2026-09-01T10:00:00Z": http.StatusBadRequest,
		"/api/measured-runs/search?limit=0":                                             http.StatusBadRequest,
		"/api/measured-runs/search?probe=probe-a":                                       http.StatusBadRequest,
		"/api/measured-runs/search?cursor=garbage":                                      http.StatusBadRequest,
	}
	for rawURL, expected := range cases {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, rawURL, nil))
		if recorder.Code != expected {
			t.Fatalf("GET %s: expected %d, got %d (%s)", rawURL, expected, recorder.Code, recorder.Body)
		}
	}
}
