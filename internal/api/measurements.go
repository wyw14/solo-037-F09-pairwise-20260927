package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type MeasurementService interface {
	CreateMeasuredRun(name, modelName, section, carriage string) (service.MeasuredRun, error)
	RecordMeasurement(model.RunID, service.MeasurementInput) (model.Evidence, error)
	MeasuredRuns() []service.MeasuredRun
	MeasuredRun(model.RunID) (service.MeasuredRun, bool)
	AnalyzeMeasuredRun(model.RunID) (service.MeasurementAnalysis, error)
	SearchMeasuredRuns(service.MeasurementFilter, int) (service.MeasurementSearchPage, error)
	SearchMeasuredRunsNext(string) (service.MeasurementSearchPage, error)
}

type createMeasuredRunRequest struct {
	Name      string `json:"name"`
	ModelName string `json:"model_name"`
	Section   string `json:"section"`
	Carriage  string `json:"carriage"`
}

func decodeMeasurementBody(request *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func (r *Router) measuredRuns(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.MeasuredRuns())
}

func (r *Router) createMeasuredRun(w http.ResponseWriter, request *http.Request) {
	var input createMeasuredRunRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.CreateMeasuredRun(input.Name, input.ModelName, input.Section, input.Carriage)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) measuredRun(w http.ResponseWriter, request *http.Request) {
	value, ok := r.dependencies.MeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}

func (r *Router) recordMeasurement(w http.ResponseWriter, request *http.Request) {
	var input service.MeasurementInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.RecordMeasurement(model.RunID(chi.URLParam(request, "id")), input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) measurementAnalysis(w http.ResponseWriter, request *http.Request) {
	value, err := r.dependencies.AnalyzeMeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}

// searchQueryParams are the first-page parameters that pin a snapshot. None of
// them may accompany a cursor: continuation reuses the pinned conditions so a
// paged scan can never silently change filters between pages.
var searchQueryParams = map[string]bool{
	"model_name": true,
	"section":    true,
	"source_id":  true,
	"start":      true,
	"end":        true,
	"limit":      true,
}

func (r *Router) searchMeasurements(w http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	for name := range query {
		if name != "cursor" && !searchQueryParams[name] {
			http.Error(w, "unknown search parameter: "+name, http.StatusBadRequest)
			return
		}
	}
	cursor := strings.TrimSpace(query.Get("cursor"))
	if cursor != "" {
		for name, values := range query {
			if name == "cursor" {
				continue
			}
			if searchQueryParams[name] && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				http.Error(w, "search conditions cannot change while paging; resend only cursor or start a new search", http.StatusBadRequest)
				return
			}
		}
		page, err := r.dependencies.SearchMeasuredRunsNext(cursor)
		if errors.Is(err, service.ErrSearchCursorInvalid) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if errors.Is(err, service.ErrSearchCursorExpired) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, page)
		return
	}

	filter := service.MeasurementFilter{
		ModelName: strings.TrimSpace(query.Get("model_name")),
		Section:   strings.TrimSpace(query.Get("section")),
		SourceID:  strings.TrimSpace(query.Get("source_id")),
	}
	var err error
	if raw := strings.TrimSpace(query.Get("start")); raw != "" {
		if filter.Start, err = time.Parse(time.RFC3339, raw); err != nil {
			http.Error(w, "start must be an RFC3339 timestamp", http.StatusBadRequest)
			return
		}
	}
	if raw := strings.TrimSpace(query.Get("end")); raw != "" {
		if filter.End, err = time.Parse(time.RFC3339, raw); err != nil {
			http.Error(w, "end must be an RFC3339 timestamp", http.StatusBadRequest)
			return
		}
	}
	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
	}
	page, err := r.dependencies.SearchMeasuredRuns(filter, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, page)
}
