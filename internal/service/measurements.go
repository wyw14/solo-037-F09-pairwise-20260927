package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

// MeasuredRun contains observations supplied by an operator, unlike the
// deterministic diagnostic scenario used by the legacy equipment page.
type MeasuredRun struct {
	Operation model.Operation  `json:"operation"`
	Evidence  []model.Evidence `json:"evidence"`
}

type MeasurementInput struct {
	Kind       model.EvidenceKind `json:"kind"`
	SourceID   string             `json:"source_id"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
}

type MeasurementAnalysis struct {
	Operation  model.Operation `json:"operation"`
	Wave       probe.Analysis  `json:"wave"`
	ForceCount int             `json:"force_count"`
	ForceMean  float64         `json:"force_mean"`
	Evaluated  time.Time       `json:"evaluated_at"`
}

// MeasurementFilter narrows the archive by ship model and section (run-level)
// and by probe and observation time window (evidence-level). Empty fields are
// unconstrained, so every supplied field combines with AND semantics.
type MeasurementFilter struct {
	ModelName string
	Section   string
	SourceID  string
	Start     time.Time
	End       time.Time
}

// EvidenceSummary records why an evidence item matched the query: the probe
// and kind that satisfied the filter with the observed value and instant.
type EvidenceSummary struct {
	ID         model.EventID      `json:"id"`
	SourceID   string             `json:"source_id"`
	Kind       model.EvidenceKind `json:"kind"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
}

// MeasurementMatch pairs a run with the evidence that matched the filter.
type MeasurementMatch struct {
	Operation model.Operation   `json:"operation"`
	Matches   []EvidenceSummary `json:"matches"`
}

// MeasurementSearchPage is one stable slice of a search. Cursor is an opaque
// continuation token; an empty token means the snapshot has been exhausted.
// Filters echoes the conditions pinned when the snapshot opened, so later
// pages demonstrably answer the same query.
type MeasurementSearchPage struct {
	Items   []MeasurementMatch `json:"items"`
	Filters MeasurementFilter  `json:"filters"`
	Limit   int                `json:"limit"`
	Cursor  string             `json:"next_cursor"`
}

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 200
	// maxSearchSessions bounds retained snapshot sessions. Sessions are
	// short-lived pagination handles; once the cap is reached the least
	// recently used session is discarded and its cursor reports expiry.
	maxSearchSessions = 128
)

type MeasurementArchive struct {
	mu       sync.Mutex
	path     string
	runs     map[model.RunID]MeasuredRun
	sessions map[string]*measurementSession
	nextAge  uint64
}

func OpenMeasurementArchive(directory string) (*MeasurementArchive, error) {
	a := &MeasurementArchive{runs: make(map[model.RunID]MeasuredRun), sessions: make(map[string]*measurementSession)}
	if directory == "" {
		return a, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	a.path = filepath.Join(directory, "operator-measurements.json")
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.runs); err != nil {
		return nil, fmt.Errorf("read measurement archive: %w", err)
	}
	if a.runs == nil {
		return nil, errors.New("measurement archive must contain a run map")
	}
	return a, nil
}

func cloneMeasuredRun(value MeasuredRun) MeasuredRun {
	value.Evidence = append([]model.Evidence(nil), value.Evidence...)
	return value
}

func (a *MeasurementArchive) commit(next map[model.RunID]MeasuredRun) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "measurements-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.runs = next
	return nil
}

func (a *MeasurementArchive) Create(name, modelName, section, carriage string) (MeasuredRun, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(modelName) == "" || strings.TrimSpace(section) == "" || strings.TrimSpace(carriage) == "" {
		return MeasuredRun{}, errors.New("name, model, section and carriage are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	operation := model.NewOperation(name, modelName, section, carriage, time.Now().UTC())
	value := MeasuredRun{Operation: operation, Evidence: []model.Evidence{}}
	next := make(map[model.RunID]MeasuredRun, len(a.runs)+1)
	for id, run := range a.runs {
		next[id] = run
	}
	next[operation.ID] = value
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return value, nil
}

func (a *MeasurementArchive) Record(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	if input.Kind != model.EvidenceWaveTime && input.Kind != model.EvidenceForce {
		return model.Evidence{}, errors.New("measurement kind must be wave_time or force")
	}
	if strings.TrimSpace(input.SourceID) == "" || math.IsNaN(input.Value) || math.IsInf(input.Value, 0) || input.RecordedAt.IsZero() || input.RecordedAt.After(time.Now().Add(time.Minute)) {
		return model.Evidence{}, errors.New("source, finite value and valid observation time are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return model.Evidence{}, errors.New("run not found")
	}
	evidence := model.NewEvidence(id, run.Operation.Revision, input.SourceID, input.Kind, input.Value, input.RecordedAt).WithDurability(a.path != "")
	run = cloneMeasuredRun(run)
	run.Evidence = append(run.Evidence, evidence)
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

func (a *MeasurementArchive) Get(id model.RunID) (MeasuredRun, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	return cloneMeasuredRun(run), exists
}

func (a *MeasurementArchive) List() []MeasuredRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]MeasuredRun, 0, len(a.runs))
	for _, run := range a.runs {
		result = append(result, cloneMeasuredRun(run))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation.CreatedAt.Before(result[j].Operation.CreatedAt) })
	return result
}

// Search errors and filter/session internals.
var (
	ErrSearchCursorInvalid = errors.New("invalid continuation cursor")
	ErrSearchCursorExpired = errors.New("continuation cursor has expired; start a new search")
)

// measurementSnapshotRun pins one run in a search snapshot: its ordering key
// and the number of evidence rows visible when the snapshot opened.
type measurementSnapshotRun struct {
	ID        model.RunID
	CreatedAt time.Time
	EvidenceN int
}

// measurementSession is a point-in-time view over the matching runs. Every
// continuation walks the same ordered run list and may only see the evidence
// prefix captured here, so frames recorded while a client pages can neither
// shift run positions nor surface as duplicates.
type measurementSession struct {
	id     string
	filter MeasurementFilter
	limit  int
	runs   []measurementSnapshotRun
	age    uint64
}

type measurementCursor struct {
	Session string `json:"s"`
	Offset  int    `json:"o"`
}

// MarshalJSON emits the filter with snake_case names and omits unset fields,
// keeping the echoed query conditions unambiguous for API clients.
func (f MeasurementFilter) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 5)
	if f.ModelName != "" {
		out["model_name"] = f.ModelName
	}
	if f.Section != "" {
		out["section"] = f.Section
	}
	if f.SourceID != "" {
		out["source_id"] = f.SourceID
	}
	if !f.Start.IsZero() {
		out["start"] = f.Start
	}
	if !f.End.IsZero() {
		out["end"] = f.End
	}
	return json.Marshal(out)
}

func normalizeSearch(filter MeasurementFilter, limit int) (MeasurementFilter, int, error) {
	filter.ModelName = strings.TrimSpace(filter.ModelName)
	filter.Section = strings.TrimSpace(filter.Section)
	filter.SourceID = strings.TrimSpace(filter.SourceID)
	if !filter.Start.IsZero() {
		filter.Start = filter.Start.UTC()
	}
	if !filter.End.IsZero() {
		filter.End = filter.End.UTC()
	}
	if !filter.Start.IsZero() && !filter.End.IsZero() && filter.End.Before(filter.Start) {
		return filter, 0, errors.New("time window end must not be before start")
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	return filter, limit, nil
}

func searchRequiresEvidence(filter MeasurementFilter) bool {
	return filter.SourceID != "" || !filter.Start.IsZero() || !filter.End.IsZero()
}

func runMatchesFilter(run MeasuredRun, filter MeasurementFilter) bool {
	if filter.ModelName != "" && run.Operation.ModelName != filter.ModelName {
		return false
	}
	if filter.Section != "" && run.Operation.Section != filter.Section {
		return false
	}
	return true
}

// matchingEvidence summarizes evidence that satisfies probe and time-window
// conditions, never reading past bound — the snapshot's evidence prefix.
func matchingEvidence(evidence []model.Evidence, filter MeasurementFilter, bound int) []EvidenceSummary {
	if bound > len(evidence) {
		bound = len(evidence)
	}
	matches := make([]EvidenceSummary, 0)
	for i := 0; i < bound; i++ {
		item := evidence[i]
		if filter.SourceID != "" && item.SourceID != filter.SourceID {
			continue
		}
		if !filter.Start.IsZero() && item.RecordedAt.Before(filter.Start) {
			continue
		}
		if !filter.End.IsZero() && item.RecordedAt.After(filter.End) {
			continue
		}
		matches = append(matches, EvidenceSummary{
			ID:         item.ID,
			SourceID:   item.SourceID,
			Kind:       item.Kind,
			Value:      item.Value,
			RecordedAt: item.RecordedAt,
		})
	}
	return matches
}

func (a *MeasurementArchive) touch(session *measurementSession) {
	session.age = a.nextAge
	a.nextAge++
}

// evictOldestSession drops the least recently used session to keep the session
// table bounded; the caller must hold a.mu and insert the new session after.
func (a *MeasurementArchive) evictOldestSession() {
	var oldestID string
	var oldestAge uint64
	first := true
	for id, session := range a.sessions {
		if first || session.age < oldestAge {
			oldestID, oldestAge, first = id, session.age, false
		}
	}
	if oldestID != "" {
		delete(a.sessions, oldestID)
	}
}

func (a *MeasurementArchive) openSession(filter MeasurementFilter, limit int) *measurementSession {
	session := &measurementSession{
		id:     uuid.NewString(),
		filter: filter,
		limit:  limit,
		runs:   make([]measurementSnapshotRun, 0),
	}
	for _, run := range a.runs {
		if !runMatchesFilter(run, filter) {
			continue
		}
		evidenceN := len(run.Evidence)
		if searchRequiresEvidence(filter) && len(matchingEvidence(run.Evidence, filter, evidenceN)) == 0 {
			continue
		}
		session.runs = append(session.runs, measurementSnapshotRun{ID: run.Operation.ID, CreatedAt: run.Operation.CreatedAt, EvidenceN: evidenceN})
	}
	sort.Slice(session.runs, func(i, j int) bool {
		if !session.runs[i].CreatedAt.Equal(session.runs[j].CreatedAt) {
			return session.runs[i].CreatedAt.Before(session.runs[j].CreatedAt)
		}
		return session.runs[i].ID < session.runs[j].ID
	})
	if len(a.sessions) >= maxSearchSessions {
		a.evictOldestSession()
	}
	a.touch(session)
	a.sessions[session.id] = session
	return session
}

// pageLocked returns the slice starting at offset, encoding the next cursor
// only when more snapshot runs remain. The caller must hold a.mu.
func (a *MeasurementArchive) pageLocked(session *measurementSession, offset int) MeasurementSearchPage {
	page := MeasurementSearchPage{
		Items:   []MeasurementMatch{},
		Filters: session.filter,
		Limit:   session.limit,
	}
	end := offset + session.limit
	if end > len(session.runs) {
		end = len(session.runs)
	}
	for _, snapshot := range session.runs[offset:end] {
		run, exists := a.runs[snapshot.ID]
		if !exists {
			continue
		}
		page.Items = append(page.Items, MeasurementMatch{
			Operation: run.Operation,
			Matches:   matchingEvidence(run.Evidence, session.filter, snapshot.EvidenceN),
		})
	}
	if end < len(session.runs) {
		page.Cursor = encodeCursor(session.id, end)
	}
	return page
}

// Search opens a point-in-time snapshot of every run matching filter and
// returns its first page. Frames recorded afterwards never move or duplicate
// rows in this snapshot; resume it with SearchNext.
func (a *MeasurementArchive) Search(filter MeasurementFilter, limit int) (MeasurementSearchPage, error) {
	filter, limit, err := normalizeSearch(filter, limit)
	if err != nil {
		return MeasurementSearchPage{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session := a.openSession(filter, limit)
	return a.pageLocked(session, 0), nil
}

// SearchNext resumes a snapshot previously opened by Search. The cursor pins
// both the session and the exact offset, and the filter cannot change between
// pages — only the conditions stored in the snapshot are applied.
func (a *MeasurementArchive) SearchNext(token string) (MeasurementSearchPage, error) {
	cursor, err := decodeCursor(token)
	if err != nil {
		return MeasurementSearchPage{}, ErrSearchCursorInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, exists := a.sessions[cursor.Session]
	if !exists || cursor.Offset < 0 || cursor.Offset > len(session.runs) {
		delete(a.sessions, cursor.Session)
		return MeasurementSearchPage{}, ErrSearchCursorExpired
	}
	a.touch(session)
	return a.pageLocked(session, cursor.Offset), nil
}

func encodeCursor(session string, offset int) string {
	data, _ := json.Marshal(measurementCursor{Session: session, Offset: offset})
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(token string) (measurementCursor, error) {
	var cursor measurementCursor
	if strings.TrimSpace(token) == "" {
		return cursor, ErrSearchCursorInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor, ErrSearchCursorInvalid
	}
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Session == "" {
		return cursor, ErrSearchCursorInvalid
	}
	return cursor, nil
}

func (a *MeasurementArchive) Analyze(id model.RunID) (MeasurementAnalysis, error) {
	run, exists := a.Get(id)
	if !exists {
		return MeasurementAnalysis{}, errors.New("run not found")
	}
	waves := make([]model.Evidence, 0)
	result := MeasurementAnalysis{Operation: run.Operation, Evaluated: time.Now().UTC()}
	for _, evidence := range run.Evidence {
		if evidence.Kind == model.EvidenceWaveTime {
			waves = append(waves, evidence)
		} else if evidence.Kind == model.EvidenceForce {
			result.ForceCount++
			result.ForceMean += evidence.Value
		}
	}
	result.Wave = probe.Analyze(waves)
	if result.ForceCount > 0 {
		result.ForceMean /= float64(result.ForceCount)
	}
	return result, nil
}

func (r *Runtime) CreateMeasuredRun(name, modelName, section, carriage string) (MeasuredRun, error) {
	return r.Measurements.Create(name, modelName, section, carriage)
}

func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	return r.Measurements.Record(id, input)
}

func (r *Runtime) MeasuredRuns() []MeasuredRun { return r.Measurements.List() }

func (r *Runtime) SearchMeasuredRuns(filter MeasurementFilter, limit int) (MeasurementSearchPage, error) {
	return r.Measurements.Search(filter, limit)
}

func (r *Runtime) SearchMeasuredRunsNext(cursor string) (MeasurementSearchPage, error) {
	return r.Measurements.SearchNext(cursor)
}

func (r *Runtime) MeasuredRun(id model.RunID) (MeasuredRun, bool) { return r.Measurements.Get(id) }

func (r *Runtime) AnalyzeMeasuredRun(id model.RunID) (MeasurementAnalysis, error) {
	return r.Measurements.Analyze(id)
}
