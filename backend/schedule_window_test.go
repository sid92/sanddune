package main

import (
	"testing"
	"time"
)

func TestInScheduleWindow(t *testing.T) {
	s := ScheduleConfig{Day: "monday", WindowStartHour: 7, DeadlineHour: 14}

	cases := []struct {
		name string
		when time.Time
		want bool
	}{
		{"monday, inside window", time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local), true},
		{"monday, at window start (inclusive)", time.Date(2026, 8, 31, 7, 0, 0, 0, time.Local), true},
		{"monday, before window", time.Date(2026, 8, 31, 6, 59, 0, 0, time.Local), false},
		{"monday, at deadline (exclusive)", time.Date(2026, 8, 31, 14, 0, 0, 0, time.Local), false},
		{"monday, after deadline", time.Date(2026, 8, 31, 20, 0, 0, 0, time.Local), false},
		{"sunday, same hour as a valid monday window", time.Date(2026, 8, 30, 9, 0, 0, 0, time.Local), false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inScheduleWindow(s, c.when); got != c.want {
				t.Errorf("inScheduleWindow(%v) = %v, want %v", c.when, got, c.want)
			}
		})
	}
}
