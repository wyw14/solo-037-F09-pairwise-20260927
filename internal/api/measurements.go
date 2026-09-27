package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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
	QueryMeasurements(service.MeasurementFilter, int, string) (service.MeasurementPage, error)
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

func parseMeasurementWindow(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func (r *Router) searchMeasurements(w http.ResponseWriter, request *http.Request) {
	params := request.URL.Query()
	since, err := parseMeasurementWindow(params.Get("since"))
	if err != nil {
		http.Error(w, "invalid since: "+err.Error(), http.StatusBadRequest)
		return
	}
	until, err := parseMeasurementWindow(params.Get("until"))
	if err != nil {
		http.Error(w, "invalid until: "+err.Error(), http.StatusBadRequest)
		return
	}
	limit := 0
	if raw := params.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	filter := service.MeasurementFilter{
		ModelName: params.Get("model"),
		Section:   params.Get("section"),
		SourceID:  params.Get("probe"),
		Since:     since,
		Until:     until,
	}
	page, err := r.dependencies.QueryMeasurements(filter, limit, params.Get("cursor"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, page)
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
