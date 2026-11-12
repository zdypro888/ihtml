package progress

import (
	"encoding/json"
	"testing"
)

func TestNormalizePreservesUnknownTotalsAndDetachesCounters(t *testing.T) {
	total := int64(19)
	input := Snapshot{Message: "running", Counters: []Counter{{Label: "processed", Unit: "items", Current: 7, Total: &total}, {Label: "scanned", Unit: "records", Current: 803}}}
	got, ok := Normalize(input)
	if !ok || got.Counters[1].Total != nil {
		t.Fatal("invented total")
	}
	total = 2
	input.Counters[0].Label = "changed"
	if *got.Counters[0].Total != 19 || got.Counters[0].Label != "processed" {
		t.Fatal("shared mutable progress")
	}
	data, err := json.Marshal(got)
	if err != nil || string(data) == "" {
		t.Fatal(err)
	}
}
func TestNormalizeRejectsImpossibleTotalsAndTerminalClaims(t *testing.T) {
	total := int64(2)
	for _, v := range []Snapshot{{Message: "running", State: "completed"}, {Message: "running", Counters: []Counter{{Label: "x", Current: 3, Total: &total}}}, {Message: "running", Counters: []Counter{{Label: "x", Current: -1}}}} {
		if _, ok := Normalize(v); ok {
			t.Fatal("invalid progress accepted")
		}
	}
}
