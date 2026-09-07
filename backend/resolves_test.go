package main

import "testing"

func TestResolvesAllVsAny(t *testing.T) {
	conditions := []ResolveCondition{
		{Field: "POURING", Equals: "YES"},
		{Field: "CHECKING", Equals: "YES"},
	}

	cases := []struct {
		name   string
		match  string
		fields map[string]string
		want   bool
	}{
		{"all: both true", "all", map[string]string{"POURING": "YES", "CHECKING": "YES"}, true},
		{"all: only one true", "all", map[string]string{"POURING": "YES", "CHECKING": "NO"}, false},
		{"all: neither true", "all", map[string]string{"POURING": "NO", "CHECKING": "NO"}, false},
		{"all: default (empty string) behaves like all", "", map[string]string{"POURING": "YES", "CHECKING": "NO"}, false},

		{"any: both true", "any", map[string]string{"POURING": "YES", "CHECKING": "YES"}, true},
		{"any: only pouring true", "any", map[string]string{"POURING": "YES", "CHECKING": "NO"}, true},
		{"any: only checking true", "any", map[string]string{"POURING": "NO", "CHECKING": "YES"}, true},
		{"any: neither true", "any", map[string]string{"POURING": "NO", "CHECKING": "NO"}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolves(conditions, c.match, c.fields); got != c.want {
				t.Errorf("resolves(match=%q, fields=%v) = %v, want %v", c.match, c.fields, got, c.want)
			}
		})
	}

	if resolves(nil, "any", map[string]string{"POURING": "YES"}) {
		t.Error("empty resolveWhen should never resolve, regardless of match mode")
	}
}
