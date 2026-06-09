package invalidation

import "testing"

func TestDedupeBasic(t *testing.T) {
	d := NewDedupe(3)
	if d.Seen("a") {
		t.Fatal("first sighting of a should be new")
	}
	if !d.Seen("a") {
		t.Fatal("second sighting of a should be deduped")
	}
}

// TestDedupeEmptyID guards the fix for treating "" as an empty slot: an
// empty-string ID must dedupe like any other value.
func TestDedupeEmptyID(t *testing.T) {
	d := NewDedupe(4)
	if d.Seen("") {
		t.Fatal("first empty id should be new")
	}
	if !d.Seen("") {
		t.Fatal("second empty id should be deduped, not bypassed")
	}
}

func TestDedupeEvictsOldest(t *testing.T) {
	d := NewDedupe(2)
	d.Seen("a")
	d.Seen("b")
	d.Seen("c") // capacity 2: "a" is evicted; the ring now holds {b, c}
	if !d.Seen("c") {
		t.Fatal("c should still be remembered (a read does not mutate the ring)")
	}
	if d.Seen("a") {
		t.Fatal("a should have been evicted and now read as new")
	}
}
