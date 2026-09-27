package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type Dependencies interface {
	TestService
	Operations() any
	Equipment() any
	Interlocks() any
	Incidents() any
	MeasurementService
}

type Router struct{ dependencies Dependencies }

func NewRouter(dependencies Dependencies) *Router { return &Router{dependencies: dependencies} }

func (r *Router) Handler() http.Handler {
	router := chi.NewRouter()
	router.Get("/healthz", r.health)
	router.Get("/api/operations", r.operations)
	router.Get("/api/equipment", r.equipment)
	router.Get("/api/interlocks", r.interlocks)
	router.Get("/api/incidents", r.incidents)
	router.Get("/api/measured-runs", r.measuredRuns)
	router.Get("/api/measured-runs/search", r.searchMeasurements)
	router.Post("/api/measured-runs", r.createMeasuredRun)
	router.Get("/api/measured-runs/{id}", r.measuredRun)
	router.Post("/api/measured-runs/{id}/evidence", r.recordMeasurement)
	router.Get("/api/measured-runs/{id}/analysis", r.measurementAnalysis)
	tests := TestHandler{Service: r.dependencies}
	router.Post("/api/operations", tests.Create)
	router.Post("/api/abort", tests.Abort)
	router.Get("/operations", r.operationsPage)
	router.Get("/equipment", r.equipmentPage)
	router.Get("/interlocks", r.interlocksPage)
	router.Get("/incidents", r.incidentsPage)
	return router
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": value})
}

func (r *Router) health(w http.ResponseWriter, request *http.Request) {
	Health{StartedAt: time.Now().UTC(), Version: "1"}.Handler(w, request)
}
func (r *Router) operations(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.Operations())
}
func (r *Router) equipment(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.Equipment())
}
func (r *Router) interlocks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.Interlocks())
}
func (r *Router) incidents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.Incidents())
}
func (r *Router) operationsPage(w http.ResponseWriter, request *http.Request) {
	http.ServeFile(w, request, "web/operations.html")
}
func (r *Router) equipmentPage(w http.ResponseWriter, request *http.Request) {
	http.ServeFile(w, request, "web/equipment.html")
}
func (r *Router) interlocksPage(w http.ResponseWriter, request *http.Request) {
	http.ServeFile(w, request, "web/interlocks.html")
}
func (r *Router) incidentsPage(w http.ResponseWriter, request *http.Request) {
	http.ServeFile(w, request, "web/incidents.html")
}
