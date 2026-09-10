package main

import "testing"

// recipients decides who gets alerted, so a mistake here is silent: someone
// simply never hears about a breach.
func TestTelegramRecipients(t *testing.T) {
	cases := []struct {
		name string
		cfg  TelegramConfig
		want []string
	}{
		{"single chat_id, the original shape", TelegramConfig{ChatID: "111"}, []string{"111"}},
		{"chat_ids list only", TelegramConfig{ChatIDs: []string{"111", "222"}}, []string{"111", "222"}},
		{"both are unioned", TelegramConfig{ChatID: "111", ChatIDs: []string{"222"}}, []string{"111", "222"}},
		{"a chat in both is not alerted twice", TelegramConfig{ChatID: "111", ChatIDs: []string{"111", "222"}}, []string{"111", "222"}},
		{"unconfigured", TelegramConfig{}, nil},
		{"the TBD placeholder is not a recipient", TelegramConfig{ChatID: "TBD"}, nil},
		{"TBD is skipped without dropping real ones", TelegramConfig{ChatID: "TBD", ChatIDs: []string{"222"}}, []string{"222"}},
		{"empty strings in the list are ignored", TelegramConfig{ChatIDs: []string{"", "222", ""}}, []string{"222"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.cfg.recipients()
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}
