package service

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func TestOperatorMeasurementsPersistAndAnalyzeSuppliedValues(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test A", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, input := range []MeasurementInput{
		{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 0.1, RecordedAt: at},
		{Kind: model.EvidenceWaveTime, SourceID: "right", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at},
	} {
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := loaded.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Wave.Count != 2 || math.Abs(analysis.Wave.Mean-0.2) > 1e-9 || analysis.ForceCount != 1 || analysis.ForceMean != 12 {
		t.Fatalf("analysis did not reflect supplied values after reload: %+v", analysis)
	}
	if len(loaded.List()) != 1 || len(loaded.List()[0].Evidence) != 3 || !loaded.List()[0].Evidence[0].Durable {
		t.Fatalf("persisted evidence missing: %+v", loaded.List())
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test B", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 1, RecordedAt: time.Now().UTC()}
	if _, err := a.Record("missing", input); err == nil {
		t.Fatal("unknown run accepted")
	}
	input.Value = math.NaN()
	if _, err := a.Record(run.Operation.ID, input); err == nil {
		t.Fatal("NaN accepted")
	}
	if len(a.List()[0].Evidence) != 0 {
		t.Fatal("rejected values changed archive")
	}
}

// seedSearchArchive builds runs across two models, sections and probes so the
// joint filter and pagination behavior has something to discriminate on.
func seedSearchArchive(t *testing.T) *MeasurementArchive {
	t.Helper()
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	runs := []struct {
		name, modelName, section string
	}{
		{"run-1", "hull-A", "section-a"},
		{"run-2", "hull-A", "section-b"},
		{"run-3", "hull-B", "section-a"},
		{"run-4", "hull-A", "section-a"},
	}
	created := make([]model.RunID, len(runs))
	for i, spec := range runs {
		run, err := a.Create(spec.name, spec.modelName, spec.section, "carriage-1")
		if err != nil {
			t.Fatal(err)
		}
		created[i] = run.Operation.ID
	}
	// Each run gets probe-a at :00 and :30 plus probe-b at :00.
	for i, id := range created {
		at := base.Add(time.Duration(i) * time.Hour)
		for _, input := range []MeasurementInput{
			{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: float64(i), RecordedAt: at},
			{Kind: model.EvidenceForce, SourceID: "probe-b", Value: float64(100 + i), RecordedAt: at.Add(15 * time.Minute)},
			{Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: float64(200 + i), RecordedAt: at.Add(30 * time.Minute)},
		} {
			if _, err := a.Record(id, input); err != nil {
				t.Fatal(err)
			}
		}
	}
	return a
}

func drainSearch(t *testing.T, a *MeasurementArchive, filter MeasurementFilter, pageLimit int) ([]MeasurementMatch, MeasurementFilter) {
	t.Helper()
	page, err := a.Search(filter, pageLimit)
	if err != nil {
		t.Fatal(err)
	}
	pinned := page.Filters
	collected := append([]MeasurementMatch{}, page.Items...)
	for page.Cursor != "" {
		page, err = a.SearchNext(page.Cursor)
		if err != nil {
			t.Fatalf("continuation failed: %v", err)
		}
		if page.Filters != pinned {
			t.Fatalf("filter changed across pages: %+v vs %+v", page.Filters, pinned)
		}
		collected = append(collected, page.Items...)
	}
	return collected, pinned
}

func TestSearchCombinesModelSectionProbeAndTimeWindow(t *testing.T) {
	a := seedSearchArchive(t)
	windowStart := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	matches, _ := drainSearch(t, a, MeasurementFilter{ModelName: "hull-A", Section: "section-a"}, 2)
	if len(matches) != 2 || matches[0].Operation.Name != "run-1" || matches[1].Operation.Name != "run-4" {
		t.Fatalf("model+section filter returned %+v", matches)
	}
	for _, match := range matches {
		if len(match.Matches) != 3 {
			t.Fatalf("run-level filter should include all evidence, got %d", len(match.Matches))
		}
	}

	probeMatches, _ := drainSearch(t, a, MeasurementFilter{SourceID: "probe-a"}, 50)
	if len(probeMatches) != 4 {
		t.Fatalf("probe filter should keep every run, got %d", len(probeMatches))
	}
	for _, match := range probeMatches {
		if len(match.Matches) != 2 {
			t.Fatalf("expected two probe-a frames per run, got %d", len(match.Matches))
		}
		for _, summary := range match.Matches {
			if summary.SourceID != "probe-a" {
				t.Fatalf("evidence summary leaked another probe: %+v", summary)
			}
		}
	}

	joint, filter := drainSearch(t, a, MeasurementFilter{
		ModelName: "hull-A",
		Section:   "section-a",
		SourceID:  "probe-a",
		Start:     windowStart,
		End:       windowEnd,
	}, 1)
	if filter.Start != windowStart || filter.End != windowEnd {
		t.Fatalf("pinned filter lost the time window: %+v", filter)
	}
	// run-2 is section-b and run-3 is hull-B; run-1's probe-a frames
	// (09:00/09:30) precede the window; run-4's 12:00 frame sits on the
	// inclusive end boundary while its 12:30 frame is outside it.
	if len(joint) != 1 || joint[0].Operation.Name != "run-4" || len(joint[0].Matches) != 1 {
		t.Fatalf("window should retain only run-4's boundary frame, got %+v", joint)
	}
	if joint[0].Matches[0].RecordedAt != windowEnd {
		t.Fatalf("expected the frame exactly at window end, got %+v", joint[0].Matches[0])
	}

	// Loosen to an inclusive window covering run-1's 09:00 and 09:30 frames.
	inclusive, _ := drainSearch(t, a, MeasurementFilter{
		ModelName: "hull-A",
		Section:   "section-a",
		SourceID:  "probe-a",
		Start:     time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		End:       time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC),
	}, 10)
	if len(inclusive) != 1 || inclusive[0].Operation.Name != "run-1" || len(inclusive[0].Matches) != 2 {
		t.Fatalf("inclusive window mismatch: %+v", inclusive)
	}
	if inclusive[0].Matches[0].ID == "" || inclusive[0].Matches[0].Kind != model.EvidenceWaveTime {
		t.Fatalf("evidence summary missing provenance: %+v", inclusive[0].Matches)
	}
}

func TestSearchPaginationVisitsEveryRunOnce(t *testing.T) {
	a := seedSearchArchive(t)
	page, err := a.Search(MeasurementFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	type orderingKey struct {
		at time.Time
		id model.RunID
	}
	var keys []orderingKey
	accept := func(items []MeasurementMatch) {
		for _, item := range items {
			seen[item.Operation.Name]++
			keys = append(keys, orderingKey{item.Operation.CreatedAt, item.Operation.ID})
		}
	}
	accept(page.Items)
	cursors := map[string]bool{page.Cursor: true}
	for page.Cursor != "" {
		page, err = a.SearchNext(page.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		accept(page.Items)
		if page.Cursor != "" && cursors[page.Cursor] {
			t.Fatal("cursor did not advance")
		}
		cursors[page.Cursor] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected four distinct runs, got %d", len(seen))
	}
	for name, count := range seen {
		if count != 1 {
			t.Fatalf("run %s visited %d times", name, count)
		}
	}
	for i := 1; i < len(keys); i++ {
		prev, next := keys[i-1], keys[i]
		if prev.at.After(next.at) || (prev.at.Equal(next.at) && prev.id > next.id) {
			t.Fatalf("pages left snapshot order at %d: %+v before %+v", i, prev, next)
		}
	}

	// Every snapshot run corresponds to a run the unfiltered archive lists.
	listed := a.List()
	if len(listed) != len(keys) {
		t.Fatalf("paged count %d does not match archive count %d", len(keys), len(listed))
	}
}

func TestSearchSnapshotIgnoresFramesAndRunsAddedWhilePaging(t *testing.T) {
	a := seedSearchArchive(t)
	first, err := a.Search(MeasurementFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Cursor == "" {
		t.Fatalf("unexpected first page: %+v", first)
	}
	servedID := first.Items[0].Operation.ID

	// Add a frame to a run already served and create a brand new run while
	// the client is between pages; neither may disturb the snapshot walk.
	if _, err := a.Record(servedID, MeasurementInput{
		Kind: model.EvidenceForce, SourceID: "probe-late", Value: 9,
		RecordedAt: time.Date(2026, 9, 1, 9, 45, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create("run-5-late", "hull-A", "section-a", "carriage-1"); err != nil {
		t.Fatal(err)
	}

	second, err := a.SearchNext(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 {
		t.Fatalf("snapshot should still hold four runs, got %d on page two", len(second.Items))
	}
	if second.Cursor != "" {
		t.Fatal("snapshot should end after the two remaining runs")
	}
	for _, match := range second.Items {
		if match.Operation.Name == "run-5-late" {
			t.Fatal("run created after the snapshot leaked into pagination")
		}
	}
}

func TestSearchEvidenceBoundFreezesFramesOnReplayedPages(t *testing.T) {
	a := seedSearchArchive(t)
	first, err := a.Search(MeasurementFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cursor == "" {
		t.Fatal("expected a continuation cursor")
	}
	target := first.Items[0].Operation.ID
	if _, err := a.Record(target, MeasurementInput{
		Kind: model.EvidenceForce, SourceID: "probe-b", Value: 42,
		RecordedAt: time.Date(2026, 9, 1, 9, 50, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err := decodeCursor(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := a.SearchNext(encodeCursor(cursor.Session, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Items) != 1 || len(replayed.Items[0].Matches) != 3 {
		t.Fatalf("replayed page must keep the 3-row evidence prefix, got %+v", replayed.Items)
	}
	for _, summary := range replayed.Items[0].Matches {
		if summary.Value == 42 {
			t.Fatal("frame recorded after snapshot leaked into replayed page")
		}
	}
}

func TestSearchRejectsInvertedWindowAndBadCursor(t *testing.T) {
	a := seedSearchArchive(t)
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if _, err := a.Search(MeasurementFilter{Start: start, End: start.Add(-time.Hour)}, 10); err == nil {
		t.Fatal("inverted time window accepted")
	}
	if _, err := a.SearchNext("not-a-cursor"); !errors.Is(err, ErrSearchCursorInvalid) {
		t.Fatalf("expected ErrSearchCursorInvalid, got %v", err)
	}
	if _, err := a.SearchNext(encodeCursor("missing-session", 0)); !errors.Is(err, ErrSearchCursorExpired) {
		t.Fatalf("expected ErrSearchCursorExpired, got %v", err)
	}
}

func TestSearchSessionEvictionExpiresOldestCursor(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"seed-1", "seed-2"} {
		if _, err := a.Create(name, "hull-A", "section-a", "carriage-1"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := a.Search(MeasurementFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cursor == "" {
		t.Fatal("two seed runs with limit one must yield a continuation cursor")
	}
	oldCursor, err := decodeCursor(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	oldSession := oldCursor.Session
	for i := 0; i < maxSearchSessions; i++ {
		if _, err := a.Search(MeasurementFilter{ModelName: "distinct"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	// The original session is LRU-oldest and must have been evicted.
	if _, err := a.SearchNext(encodeCursor(oldSession, 0)); !errors.Is(err, ErrSearchCursorExpired) {
		t.Fatalf("expected evicted cursor to expire, got %v", err)
	}
}
