package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestMeasurementSearchHTTPFiltersPagesAndGuardsCursor(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow B","model_name":"hull Q","section":"section-c","carriage":"carriage-2"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var run struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Minute).UTC()
	for i, probe := range []string{"probe-a", "probe-a", "probe-b"} {
		stamp := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		body := `{"kind":"wave_time","source_id":"` + probe + `","value":0.5,"recorded_at":"` + stamp + `"}`
		recorded := httptest.NewRecorder()
		server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(run.Data.Operation.ID)+"/evidence", strings.NewReader(body)))
		if recorded.Code != http.StatusOK {
			t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
		}
	}
	query := "/api/measured-runs/search?model=hull%20Q&section=section-c&probe=probe-a&limit=1"
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodGet, query, nil))
	if first.Code != http.StatusOK {
		t.Fatalf("search: %d %s", first.Code, first.Body)
	}
	var page struct {
		Data service.MeasurementPage `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data.Items) != 1 || page.Data.NextCursor == "" {
		t.Fatalf("first page wrong: %+v", page.Data)
	}
	if page.Data.Summary.Matched != 2 || page.Data.Summary.ByProbe["probe-a"] != 2 {
		t.Fatalf("summary wrong: %+v", page.Data.Summary)
	}
	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodGet, query+"&cursor="+page.Data.NextCursor, nil))
	if second.Code != http.StatusOK {
		t.Fatalf("continuation: %d %s", second.Code, second.Body)
	}
	var follow struct {
		Data service.MeasurementPage `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &follow); err != nil {
		t.Fatal(err)
	}
	if len(follow.Data.Items) != 1 || follow.Data.NextCursor != "" {
		t.Fatalf("second page wrong: %+v", follow.Data)
	}
	if follow.Data.Items[0].Evidence.ID == page.Data.Items[0].Evidence.ID {
		t.Fatal("continuation repeated a frame")
	}
	drifted := httptest.NewRecorder()
	server.ServeHTTP(drifted, httptest.NewRequest(http.MethodGet, "/api/measured-runs/search?model=hull%20Q&probe=probe-b&cursor="+page.Data.NextCursor, nil))
	if drifted.Code != http.StatusBadRequest {
		t.Fatalf("changed conditions must be rejected: %d %s", drifted.Code, drifted.Body)
	}
}
