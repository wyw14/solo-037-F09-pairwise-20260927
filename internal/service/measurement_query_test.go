package service

import (
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func seedQueryArchive(t *testing.T) (*MeasurementArchive, MeasuredRun, MeasuredRun, time.Time) {
	t.Helper()
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hullA, err := a.Create("run A", "hull-a", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	hullB, err := a.Create("run B", "hull-b", "section-b", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	inputs := []struct {
		run   MeasuredRun
		input MeasurementInput
	}{
		{hullA, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-1", Value: 0.1, RecordedAt: base}},
		{hullA, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-2", Value: 0.2, RecordedAt: base.Add(time.Minute)}},
		{hullA, MeasurementInput{Kind: model.EvidenceForce, SourceID: "probe-1", Value: 5, RecordedAt: base.Add(2 * time.Minute)}},
		{hullB, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-1", Value: 0.4, RecordedAt: base.Add(3 * time.Minute)}},
	}
	for _, item := range inputs {
		if _, err := a.Record(item.run.Operation.ID, item.input); err != nil {
			t.Fatal(err)
		}
	}
	return a, hullA, hullB, base
}

func TestMeasurementQueryCombinesModelSectionProbeAndWindow(t *testing.T) {
	a, hullA, _, base := seedQueryArchive(t)
	page, err := a.Query(MeasurementFilter{
		ModelName: "hull-a",
		Section:   "section-a",
		SourceID:  "probe-1",
		Since:     base,
		Until:     base.Add(90 * time.Second),
	}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected exactly one frame, got %+v", page.Items)
	}
	item := page.Items[0]
	if item.Run != hullA.Operation.ID || item.Evidence.SourceID != "probe-1" || item.Evidence.Kind != model.EvidenceWaveTime {
		t.Fatalf("wrong frame matched: %+v", item)
	}
	if page.NextCursor != "" {
		t.Fatal("single match must not yield a continuation cursor")
	}
	if page.Summary.Matched != 1 || page.Summary.ByKind[model.EvidenceWaveTime] != 1 || page.Summary.ByProbe["probe-1"] != 1 {
		t.Fatalf("summary does not reflect the match set: %+v", page.Summary)
	}
	if !page.Summary.FirstAt.Equal(base) || !page.Summary.LastAt.Equal(base) {
		t.Fatalf("summary window wrong: %+v", page.Summary)
	}
}

func TestMeasurementQuerySummaryCoversAllPages(t *testing.T) {
	a, _, _, _ := seedQueryArchive(t)
	first, err := a.Query(MeasurementFilter{SourceID: "probe-1"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Summary.Matched != 3 || first.Summary.ByKind[model.EvidenceWaveTime] != 2 || first.Summary.ByKind[model.EvidenceForce] != 1 {
		t.Fatalf("summary must aggregate beyond the page: %+v", first.Summary)
	}
	second, err := a.Query(MeasurementFilter{SourceID: "probe-1"}, 1, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.Matched != first.Summary.Matched {
		t.Fatalf("summary changed between pages: %+v vs %+v", first.Summary, second.Summary)
	}
}

func TestMeasurementQueryPagesStableWhileRecording(t *testing.T) {
	a, hullA, _, base := seedQueryArchive(t)
	filter := MeasurementFilter{ModelName: "hull-a"}
	first, err := a.Query(filter, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page wrong: %+v", first)
	}
	// New frames arrive between pages: one before and one after the cursor.
	if _, err := a.Record(hullA.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-early", Value: 9, RecordedAt: base.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Record(hullA.Operation.ID, MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "probe-late", Value: 9, RecordedAt: base.Add(10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	seen := map[model.EventID]int{}
	for _, item := range first.Items {
		seen[item.Evidence.ID]++
	}
	cursor := first.NextCursor
	for cursor != "" {
		page, err := a.Query(filter, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			seen[item.Evidence.ID]++
		}
		cursor = page.NextCursor
	}
	// The three pre-existing hull-a frames must appear exactly once; the
	// back-dated frame must not resurrect behind the cursor, and the late
	// frame may join at the tail exactly once.
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("frame %s emitted %d times", id, count)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("expected 3 pre-existing frames plus the late arrival, got %d", len(seen))
	}
}

func TestMeasurementQueryRejectsChangedConditionsAndBadCursor(t *testing.T) {
	a, _, _, _ := seedQueryArchive(t)
	first, err := a.Query(MeasurementFilter{ModelName: "hull-a"}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Query(MeasurementFilter{ModelName: "hull-b"}, 1, first.NextCursor); err == nil {
		t.Fatal("continuation with changed model accepted")
	}
	if _, err := a.Query(MeasurementFilter{ModelName: "hull-a", SourceID: "probe-1"}, 1, first.NextCursor); err == nil {
		t.Fatal("continuation with added probe accepted")
	}
	if _, err := a.Query(MeasurementFilter{ModelName: "hull-a"}, 1, "not-a-cursor"); err == nil {
		t.Fatal("garbage cursor accepted")
	}
	if _, err := a.Query(MeasurementFilter{Since: time.Now(), Until: time.Now().Add(-time.Hour)}, 0, ""); err == nil {
		t.Fatal("inverted window accepted")
	}
}
