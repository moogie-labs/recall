package tasks

import (
	"testing"
	"time"

	"recall/internal/model"
)

func TestSpringForwardDay(t *testing.T) {
	loc := loadLocation(t, "America/New_York")
	from, until, err := DateRange("2026-03-08", "2026-03-08", loc)
	if err != nil {
		t.Fatal(err)
	}
	if from == nil || until == nil || until.Sub(*from) != 23*time.Hour {
		t.Fatalf("range: %v %v", from, until)
	}
	assertUTCDate(t, from, "2026-03-08T05:00:00Z")
	assertUTCDate(t, until, "2026-03-09T04:00:00Z")
}

func TestFallBackDay(t *testing.T) {
	loc := loadLocation(t, "America/New_York")
	from, until, err := DateRange("2026-11-01", "2026-11-01", loc)
	if err != nil {
		t.Fatal(err)
	}
	if from == nil || until == nil || until.Sub(*from) != 25*time.Hour {
		t.Fatalf("range: %v %v", from, until)
	}
	assertUTCDate(t, from, "2026-11-01T04:00:00Z")
	assertUTCDate(t, until, "2026-11-02T05:00:00Z")
}

func TestDateRangeCalendarBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, from, through, zone, start, until string
	}{
		{"leap day", "2028-02-29", "2028-02-29", "UTC", "2028-02-29T00:00:00Z", "2028-03-01T00:00:00Z"},
		{"month boundary", "2026-04-30", "2026-05-01", "UTC", "2026-04-30T00:00:00Z", "2026-05-02T00:00:00Z"},
		{"year boundary", "2026-12-31", "2026-12-31", "America/New_York", "2026-12-31T05:00:00Z", "2027-01-01T05:00:00Z"},
		{"positive offset", "2026-10-19", "2026-10-19", "Asia/Kathmandu", "2026-10-18T18:15:00Z", "2026-10-19T18:15:00Z"},
		{"spring range", "2026-03-07", "2026-03-09", "America/New_York", "2026-03-07T05:00:00Z", "2026-03-10T04:00:00Z"},
		{"fall range", "2026-10-31", "2026-11-02", "America/New_York", "2026-10-31T04:00:00Z", "2026-11-03T05:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start, until, err := DateRange(tt.from, tt.through, loadLocation(t, tt.zone))
			if err != nil {
				t.Fatal(err)
			}
			assertUTCDate(t, start, tt.start)
			assertUTCDate(t, until, tt.until)
		})
	}
}

func TestDateRangeAllowsOpenBounds(t *testing.T) {
	loc := loadLocation(t, "America/New_York")
	for _, tt := range []struct{ name, from, through, start, until string }{
		{"unbounded", "", "", "", ""},
		{"from only", "2026-03-08", "", "2026-03-08T05:00:00Z", ""},
		{"through only", "", "2026-11-01", "", "2026-11-02T05:00:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start, until, err := DateRange(tt.from, tt.through, loc)
			if err != nil {
				t.Fatal(err)
			}
			assertUTCDate(t, start, tt.start)
			assertUTCDate(t, until, tt.until)
		})
	}
}

func TestDateRangeRejectsInvalidInputs(t *testing.T) {
	for _, tt := range []struct{ name, from, through string }{
		{"reversed adjacent", "2026-10-20", "2026-10-19"},
		{"reversed across DST", "2026-11-02", "2026-10-31"},
		{"non-leap day", "2026-02-29", ""},
		{"invalid upper day", "", "2026-04-31"},
		{"missing zero padding", "2026-3-08", ""},
		{"non-date", "SECRET", ""},
		{"timestamp lower", "2026-10-19T00:00:00Z", ""},
		{"timestamp upper", "", "2026-10-19T00:00:00-04:00"},
		{"leading whitespace", " 2026-10-19", ""},
		{"trailing whitespace", "", "2026-10-19 "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			start, until, err := DateRange(tt.from, tt.through, time.UTC)
			if model.ErrorCode(err) != "invalid_argument" || start != nil || until != nil {
				t.Fatalf("range: %v %v err=%v", start, until, err)
			}
		})
	}
}

func TestDateRangeRejectsNilLocation(t *testing.T) {
	start, until, err := DateRange("2026-10-19", "2026-10-19", nil)
	if model.ErrorCode(err) != "invalid_argument" || start != nil || until != nil {
		t.Fatalf("range: %v %v err=%v", start, until, err)
	}
}

func loadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load timezone %s: %v", name, err)
	}
	return loc
}

func assertUTCDate(t *testing.T, got *time.Time, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("date=%v, want open bound", got)
		}
		return
	}
	if got == nil || got.Location() != time.UTC || got.Format(time.RFC3339Nano) != want {
		t.Fatalf("date=%v, want UTC %s", got, want)
	}
}
