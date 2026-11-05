package cronexpr

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	return l
}

func TestPrevNext(t *testing.T) {
	loc := time.UTC
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		spec, now, prev, next string
	}{
		{"0 2 * * *", "2026-11-05 10:00", "2026-11-05 02:00", "2026-11-06 02:00"},
		{"0 2 * * *", "2026-11-05 02:00", "2026-11-05 02:00", "2026-11-06 02:00"},
		{"*/5 * * * *", "2026-11-05 10:07", "2026-11-05 10:05", "2026-11-05 10:10"},
		// weekdays only: Thursday 2026-11-05 -> on Saturday the previous run was Friday
		{"0 2 * * 1-5", "2026-11-07 12:00", "2026-11-06 02:00", "2026-11-09 02:00"},
		{"0 2 * * mon-fri", "2026-11-07 12:00", "2026-11-06 02:00", "2026-11-09 02:00"},
		{"30 23 * * 7", "2026-11-05 12:00", "2026-11-01 23:30", "2026-11-08 23:30"},
		{"0 0 1 * *", "2026-11-05 12:00", "2026-11-01 00:00", "2026-12-01 00:00"},
		{"@monthly", "2026-11-05 12:00", "2026-11-01 00:00", "2026-12-01 00:00"},
		{"@hourly", "2026-11-05 12:30", "2026-11-05 12:00", "2026-11-05 13:00"},
		{"0 0 29 2 *", "2026-11-05 12:00", "2024-02-29 00:00", "2028-02-29 00:00"},
		// dom and dow both restricted: OR semantics (1st of month OR any Monday)
		{"0 0 1 * 1", "2026-11-05 12:00", "2026-11-02 00:00", "2026-11-09 00:00"},
		// dom starts with '*': AND semantics (odd days that are Mondays)
		{"0 0 */2 * 1", "2026-11-05 12:00", "2026-10-19 00:00", "2026-11-09 00:00"},
		{"15 8-17/3 * jan,nov *", "2026-11-05 12:00", "2026-11-05 11:15", "2026-11-05 14:15"},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		p, ok := s.Prev(at(c.now))
		if !ok || !p.Equal(at(c.prev)) {
			t.Errorf("%s Prev(%s) = %v, want %s", c.spec, c.now, p, c.prev)
		}
		n, ok := s.Next(at(c.now))
		if !ok || !n.Equal(at(c.next)) {
			t.Errorf("%s Next(%s) = %v, want %s", c.spec, c.now, n, c.next)
		}
	}
}

func TestLocalZoneAndDST(t *testing.T) {
	loc := mustLoc(t, "Europe/Bratislava")
	s, _ := Parse("30 2 * * *")
	// 2026-03-29: 02:00 -> 03:00 (02:30 does not exist)
	now := time.Date(2026, 3, 29, 12, 0, 0, 0, loc)
	p, ok := s.Prev(now)
	if !ok || p.Day() != 28 {
		t.Errorf("Prev across DST gap = %v", p)
	}
	n, ok := s.Next(time.Date(2026, 3, 28, 12, 0, 0, 0, loc))
	if !ok || n.Before(time.Date(2026, 3, 29, 0, 0, 0, 0, loc)) {
		t.Errorf("Next across DST gap = %v", n)
	}
	// India (+05:30): hour boundaries are not multiples of an hour in UTC
	ist := mustLoc(t, "Asia/Kolkata")
	s2, _ := Parse("0 * * * *")
	p2, _ := s2.Prev(time.Date(2026, 11, 5, 10, 20, 0, 0, ist))
	if p2.Hour() != 10 || p2.Minute() != 0 {
		t.Errorf("IST prev = %v", p2)
	}
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "5-1 * * * *", "@sometimes", "a * * * *"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", spec)
		}
	}
	s, err := Parse("@reboot")
	if err != nil || !s.Reboot {
		t.Fatalf("@reboot: %v", err)
	}
	if _, ok := s.Prev(time.Now()); ok {
		t.Error("@reboot must not have a previous run")
	}
	if s, _ := Parse("0 0 30 2 *"); s != nil {
		if _, ok := s.Next(time.Now()); ok {
			t.Error("Feb 30 must never run")
		}
	}
}
