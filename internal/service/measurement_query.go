package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

// MeasurementFilter selects archived frames by ship model, tank section,
// probe and observation window. Empty fields match everything.
type MeasurementFilter struct {
	ModelName string    `json:"model_name,omitempty"`
	Section   string    `json:"section,omitempty"`
	SourceID  string    `json:"source_id,omitempty"`
	Since     time.Time `json:"since,omitempty"`
	Until     time.Time `json:"until,omitempty"`
}

func (f MeasurementFilter) normalized() MeasurementFilter {
	f.ModelName = strings.TrimSpace(f.ModelName)
	f.Section = strings.TrimSpace(f.Section)
	f.SourceID = strings.TrimSpace(f.SourceID)
	f.Since = f.Since.UTC()
	f.Until = f.Until.UTC()
	return f
}

func (f MeasurementFilter) validate() error {
	if !f.Since.IsZero() && !f.Until.IsZero() && f.Since.After(f.Until) {
		return errors.New("since must not be after until")
	}
	return nil
}

func (f MeasurementFilter) matches(run MeasuredRun, evidence model.Evidence) bool {
	if f.ModelName != "" && run.Operation.ModelName != f.ModelName {
		return false
	}
	if f.Section != "" && run.Operation.Section != f.Section {
		return false
	}
	if f.SourceID != "" && evidence.SourceID != f.SourceID {
		return false
	}
	if !f.Since.IsZero() && evidence.RecordedAt.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && evidence.RecordedAt.After(f.Until) {
		return false
	}
	return true
}

// digest binds a cursor to the exact filter it was issued for, so a
// continuation cannot silently drift onto different query conditions.
func (f MeasurementFilter) digest() string {
	canonical, _ := json.Marshal(f.normalized())
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:16])
}

// measurementCursor is a keyset position: records are totally ordered by
// (recorded_at, run, event), so a page boundary stays exact even while new
// frames are appended between requests.
type measurementCursor struct {
	RecordedAt time.Time     `json:"recorded_at"`
	Run        model.RunID   `json:"run"`
	Event      model.EventID `json:"event"`
	Filter     string        `json:"filter"`
}

func encodeMeasurementCursor(cursor measurementCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeMeasurementCursor(token string) (measurementCursor, error) {
	var cursor measurementCursor
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor, errors.New("invalid cursor")
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return cursor, errors.New("invalid cursor")
	}
	if cursor.RecordedAt.IsZero() || cursor.Run == "" || cursor.Event == "" || cursor.Filter == "" {
		return cursor, errors.New("invalid cursor")
	}
	return cursor, nil
}

// after reports whether key sorts strictly after the cursor position.
func (c measurementCursor) after(recordedAt time.Time, run model.RunID, event model.EventID) bool {
	if !recordedAt.Equal(c.RecordedAt) {
		return recordedAt.After(c.RecordedAt)
	}
	if run != c.Run {
		return run > c.Run
	}
	return event > c.Event
}

// MeasurementMatch is one archived frame together with the run context it
// was selected by.
type MeasurementMatch struct {
	Run       model.RunID    `json:"run"`
	ModelName string         `json:"model_name"`
	Section   string         `json:"section"`
	Evidence  model.Evidence `json:"evidence"`
}

// MeasurementSummary aggregates the whole match set, independent of paging.
type MeasurementSummary struct {
	Matched int                        `json:"matched"`
	ByKind  map[model.EvidenceKind]int `json:"by_kind"`
	ByProbe map[string]int             `json:"by_probe"`
	FirstAt time.Time                  `json:"first_at,omitempty"`
	LastAt  time.Time                  `json:"last_at,omitempty"`
}

// MeasurementPage is one page of matches plus the cursor that continues it.
// NextCursor is empty once the match set is exhausted.
type MeasurementPage struct {
	Items      []MeasurementMatch `json:"items"`
	Summary    MeasurementSummary `json:"summary"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

const (
	defaultMeasurementPageSize = 50
	maxMeasurementPageSize     = 500
)

// Query returns archived frames matching the filter in a stable
// (recorded_at, run, event) order. Passing the previous page's cursor
// resumes exactly after the last returned frame: frames recorded before the
// cursor are never re-emitted and existing frames are never skipped, even
// while new frames are being recorded. The cursor carries a digest of the
// filter; continuing with different conditions is rejected.
func (a *MeasurementArchive) Query(filter MeasurementFilter, limit int, cursorToken string) (MeasurementPage, error) {
	filter = filter.normalized()
	if err := filter.validate(); err != nil {
		return MeasurementPage{}, err
	}
	if limit <= 0 {
		limit = defaultMeasurementPageSize
	}
	if limit > maxMeasurementPageSize {
		limit = maxMeasurementPageSize
	}
	var cursor measurementCursor
	if cursorToken != "" {
		decoded, err := decodeMeasurementCursor(cursorToken)
		if err != nil {
			return MeasurementPage{}, err
		}
		if decoded.Filter != filter.digest() {
			return MeasurementPage{}, errors.New("query conditions do not match the cursor")
		}
		cursor = decoded
	}

	a.mu.Lock()
	matches := make([]MeasurementMatch, 0)
	summary := MeasurementSummary{
		ByKind:  make(map[model.EvidenceKind]int),
		ByProbe: make(map[string]int),
	}
	for id, run := range a.runs {
		for _, evidence := range run.Evidence {
			if !filter.matches(run, evidence) {
				continue
			}
			matches = append(matches, MeasurementMatch{
				Run:       id,
				ModelName: run.Operation.ModelName,
				Section:   run.Operation.Section,
				Evidence:  evidence,
			})
			summary.Matched++
			summary.ByKind[evidence.Kind]++
			summary.ByProbe[evidence.SourceID]++
			if summary.FirstAt.IsZero() || evidence.RecordedAt.Before(summary.FirstAt) {
				summary.FirstAt = evidence.RecordedAt
			}
			if evidence.RecordedAt.After(summary.LastAt) {
				summary.LastAt = evidence.RecordedAt
			}
		}
	}
	a.mu.Unlock()

	sort.Slice(matches, func(i, j int) bool {
		x, y := matches[i].Evidence, matches[j].Evidence
		if !x.RecordedAt.Equal(y.RecordedAt) {
			return x.RecordedAt.Before(y.RecordedAt)
		}
		if x.Run != y.Run {
			return x.Run < y.Run
		}
		return x.ID < y.ID
	})

	page := MeasurementPage{Items: []MeasurementMatch{}, Summary: summary}
	remaining := matches
	if cursorToken != "" {
		start := sort.Search(len(matches), func(i int) bool {
			m := matches[i]
			return cursor.after(m.Evidence.RecordedAt, m.Run, m.Evidence.ID)
		})
		remaining = matches[start:]
	}
	if len(remaining) > limit {
		page.Items = remaining[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeMeasurementCursor(measurementCursor{
			RecordedAt: last.Evidence.RecordedAt,
			Run:        last.Run,
			Event:      last.Evidence.ID,
			Filter:     filter.digest(),
		})
	} else {
		page.Items = remaining
	}
	return page, nil
}

func (r *Runtime) QueryMeasurements(filter MeasurementFilter, limit int, cursor string) (MeasurementPage, error) {
	return r.Measurements.Query(filter, limit, cursor)
}
