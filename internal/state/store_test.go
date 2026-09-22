package state

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Persisted JSON is re-indented on write, so stored templates are compared by
// value rather than by bytes.
func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotV, wantV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("stored value is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Fatalf("stored %s, want %s", got, want)
	}
}

func newTestStore(t *testing.T, max int) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir(), max)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestLoadStagingReportsAbsenceDistinctly(t *testing.T) {
	s := newTestStore(t, 5)
	_, err := s.LoadStaging()
	if !errors.Is(err, ErrNoStaging) {
		t.Fatalf("err = %v, want ErrNoStaging", err)
	}
}

func TestStagingRoundTrips(t *testing.T) {
	s := newTestStore(t, 5)
	want := &Staging{
		Base:    json.RawMessage(`{"v":1}`),
		Draft:   json.RawMessage(`{"v":2}`),
		Entries: []StagedEntry{{At: time.Now(), Module: "routing", Summary: "新增规则"}},
	}
	if err := s.SaveStaging(want); err != nil {
		t.Fatalf("SaveStaging: %v", err)
	}
	got, err := s.LoadStaging()
	if err != nil {
		t.Fatalf("LoadStaging: %v", err)
	}
	assertJSONEqual(t, got.Draft, `{"v":2}`)
	if len(got.Entries) != 1 || got.Entries[0].Summary != "新增规则" {
		t.Fatalf("Entries = %+v", got.Entries)
	}

	if err := s.ClearStaging(); err != nil {
		t.Fatalf("ClearStaging: %v", err)
	}
	if _, err := s.LoadStaging(); !errors.Is(err, ErrNoStaging) {
		t.Fatalf("after clear err = %v, want ErrNoStaging", err)
	}
}

func TestClearStagingIsIdempotent(t *testing.T) {
	s := newTestStore(t, 5)
	if err := s.ClearStaging(); err != nil {
		t.Fatalf("clearing an empty staging area must not error: %v", err)
	}
}

func TestSnapshotsAreNewestFirstAndTrimmed(t *testing.T) {
	s := newTestStore(t, 2)
	for i := range 4 {
		if _, err := s.SaveSnapshot("改动", json.RawMessage(`{"n":`+string(rune('0'+i))+`}`)); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
		// Rapid saves must remain distinct even in the same second.
	}
	got, err := s.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d snapshots, want 2", len(got))
	}
	if !got[0].At.After(got[1].At) {
		t.Fatal("snapshots must be newest first")
	}
	assertJSONEqual(t, got[0].Template, `{"n":3}`)
}

func TestGetSnapshotRejectsUnknownID(t *testing.T) {
	s := newTestStore(t, 5)
	if _, err := s.GetSnapshot("nope"); err == nil {
		t.Fatal("unknown snapshot id must error")
	}
}
