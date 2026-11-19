package core

import (
	"slices"
	"testing"

	"github.com/jmoiron/sqlx/types"
)

func TestCampaignListIDs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []int
	}{
		{``, []int{}},
		{`[]`, []int{}},
		{`[{"id": 3, "name": "a"}, {"id": 0, "name": "deleted"}, {"id": 1, "name": "b"}]`, []int{3, 1}},
	} {
		got, err := CampaignListIDs(types.JSONText(tc.in))
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%q: got %v, want %v", tc.in, got, tc.want)
		}
	}

	if _, err := CampaignListIDs(types.JSONText(`{`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestSameIDSet(t *testing.T) {
	for _, tc := range []struct {
		a, b []int
		want bool
	}{
		{nil, nil, true},
		{[]int{1, 2}, []int{2, 1}, true},
		{[]int{1, 1, 2}, []int{2, 1}, true},
		{[]int{1, 0}, []int{1}, true},
		{[]int{1}, []int{1, 2}, false},
		{[]int{1, 3}, []int{1, 2}, false},
		{[]int{}, []int{1}, false},
	} {
		if got := SameIDSet(tc.a, tc.b); got != tc.want {
			t.Fatalf("SameIDSet(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
