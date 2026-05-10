package mirror

import (
	"reflect"
	"sort"
	"testing"
)

func TestDiff_NewLocalEntryNeedsPush(t *testing.T) {
	local := []Entry{
		{Text: "rule a", MemoryID: "m_a"},
		{Text: "rule b new"},
	}
	remote := map[string]string{"m_a": "rule a"}
	plan := Diff(local, remote)
	if len(plan.ToPost) != 1 || plan.ToPost[0].Text != "rule b new" {
		t.Fatalf("ToPost %+v", plan.ToPost)
	}
	if len(plan.ToPut) != 0 {
		t.Fatalf("ToPut %+v", plan.ToPut)
	}
}

func TestDiff_LocalEditedNeedsPut(t *testing.T) {
	local := []Entry{
		{Text: "rule a edited", MemoryID: "m_a"},
	}
	remote := map[string]string{"m_a": "rule a"}
	plan := Diff(local, remote)
	if len(plan.ToPut) != 1 || plan.ToPut[0].MemoryID != "m_a" {
		t.Fatalf("ToPut %+v", plan.ToPut)
	}
}

func TestDiff_RemoteOnlyAppendedToLocal(t *testing.T) {
	local := []Entry{}
	remote := map[string]string{"m_x": "remote rule"}
	plan := Diff(local, remote)
	if len(plan.ToAppend) != 1 || plan.ToAppend[0].MemoryID != "m_x" {
		t.Fatalf("ToAppend %+v", plan.ToAppend)
	}
}

func TestDiff_StableOrdering(t *testing.T) {
	local := []Entry{}
	remote := map[string]string{"m_b": "b", "m_a": "a", "m_c": "c"}
	plan := Diff(local, remote)
	got := []string{plan.ToAppend[0].MemoryID, plan.ToAppend[1].MemoryID, plan.ToAppend[2].MemoryID}
	want := []string{"m_a", "m_b", "m_c"}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDiff_StampedLocalMissingFromRemote_NoDelete(t *testing.T) {
	local := []Entry{
		{Text: "removed upstream", MemoryID: "m_gone"},
	}
	remote := map[string]string{}
	plan := Diff(local, remote)
	if len(plan.ToPost) != 0 || len(plan.ToPut) != 0 || len(plan.ToAppend) != 0 {
		t.Fatalf("expected no ops, got %+v", plan)
	}
}
