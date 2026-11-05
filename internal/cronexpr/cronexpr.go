// Package cronexpr parses classic 5-field (Vixie/cronie) cron expressions and
// computes the previous and next activation times.
package cronexpr

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	// Reboot is true for @reboot entries, which have no time-based schedule.
	Reboot bool
}

var macros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// Parse parses a 5-field cron expression or an @macro.
func Parse(spec string) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "@") {
		l := strings.ToLower(spec)
		if l == "@reboot" {
			return &Schedule{Reboot: true}, nil
		}
		m, ok := macros[l]
		if !ok {
			return nil, fmt.Errorf("unknown macro %q", spec)
		}
		spec = m
	}
	f := strings.Fields(spec)
	if len(f) != 5 {
		return nil, fmt.Errorf("expected 5 fields, got %d in %q", len(f), spec)
	}
	s := &Schedule{}
	var err error
	if s.minute, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if s.month, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.dow, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 == Sunday
		s.dow |= 1
	}
	// Vixie cron: a field starting with '*' is "unrestricted"; if both
	// day fields are restricted, a day matches when EITHER matches.
	s.domStar = strings.HasPrefix(f[2], "*")
	s.dowStar = strings.HasPrefix(f[4], "*")
	return s, nil
}

func parseValue(v string, names map[string]int) (int, error) {
	if names != nil {
		if n, ok := names[strings.ToLower(v)]; ok {
			return n, nil
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", v)
	}
	return n, nil
}

func parseField(field string, min, max int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, fmt.Errorf("empty list element in %q", field)
		}
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		lo, hi := min, max
		switch {
		case rng == "*":
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if lo, err = parseValue(a, names); err != nil {
				return 0, err
			}
			if hi, err = parseValue(b, names); err != nil {
				return 0, err
			}
		default:
			v, err := parseValue(rng, names)
			if err != nil {
				return 0, err
			}
			lo = v
			if !hasStep {
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("value out of range in %q (allowed %d-%d)", part, min, max)
		}
		for i := lo; i <= hi; i += step {
			bits |= 1 << uint(i)
		}
	}
	return bits, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

func (s *Schedule) dayMatches(t time.Time) bool {
	d := has(s.dom, t.Day())
	w := has(s.dow, int(t.Weekday()))
	if s.domStar || s.dowStar {
		return d && w
	}
	return d || w
}

const searchLimit = 5 * 366 * 24 * time.Hour

// Next returns the first activation strictly after t. ok is false for
// @reboot or when no activation exists within 5 years (e.g. "0 0 30 2 *").
func (s *Schedule) Next(t time.Time) (time.Time, bool) {
	if s.Reboot {
		return time.Time{}, false
	}
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(searchLimit)
	for t.Before(limit) {
		y, mo, d := t.Date()
		h := t.Hour()
		switch {
		case !has(s.month, int(mo)):
			t = time.Date(y, mo+1, 1, 0, 0, 0, 0, loc)
		case !s.dayMatches(t):
			t = time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
		case !has(s.hour, h):
			n := time.Date(y, mo, d, h+1, 0, 0, 0, loc)
			if !n.After(t) { // DST edge
				n = t.Add(time.Duration(60-t.Minute()) * time.Minute)
			}
			t = n
		case !has(s.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

// Prev returns the latest activation at or before t.
func (s *Schedule) Prev(t time.Time) (time.Time, bool) {
	if s.Reboot {
		return time.Time{}, false
	}
	loc := t.Location()
	t = t.Truncate(time.Minute)
	limit := t.Add(-searchLimit)
	for t.After(limit) {
		y, mo, d := t.Date()
		h := t.Hour()
		switch {
		case !has(s.month, int(mo)):
			t = time.Date(y, mo, 1, 0, 0, 0, 0, loc).Add(-time.Minute)
		case !s.dayMatches(t):
			t = time.Date(y, mo, d, 0, 0, 0, 0, loc).Add(-time.Minute)
		case !has(s.hour, h):
			t = t.Add(-time.Duration(t.Minute()+1) * time.Minute)
		case !has(s.minute, t.Minute()):
			t = t.Add(-time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}
