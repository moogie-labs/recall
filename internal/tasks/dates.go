package tasks

import (
	"time"

	"recall/internal/model"
)

// DateRange converts inclusive local YYYY-MM-DD bounds to a UTC [start, until)
// range. An empty bound is unbounded. location must be non-nil; callers can pass
// time.Local for the computer's timezone. Calendar boundaries resolve to their
// first actual instant, including when midnight is skipped or repeated.
func DateRange(from, through string, location *time.Location) (start, until *time.Time, err error) {
	if location == nil {
		return nil, nil, &model.Error{Code: "invalid_argument", Message: "a timezone is required for date ranges"}
	}
	if from != "" {
		day, err := time.Parse(time.DateOnly, from)
		if err != nil {
			return nil, nil, &model.Error{Code: "invalid_argument", Message: "from must be a valid YYYY-MM-DD date"}
		}
		utc := localDayStart(day, location)
		start = &utc
	}
	if through != "" {
		day, err := time.Parse(time.DateOnly, through)
		if err != nil {
			return nil, nil, &model.Error{Code: "invalid_argument", Message: "through must be a valid YYYY-MM-DD date"}
		}
		utc := localDayStart(day.AddDate(0, 0, 1), location)
		until = &utc
	}
	if start != nil && until != nil && !start.Before(*until) {
		return nil, nil, &model.Error{Code: "invalid_argument", Message: "from must not be after through"}
	}
	return start, until, nil
}

// day holds the requested calendar date in UTC, not an instant in location.
// Resolve its midnight against timezone intervals rather than trusting Date's
// unspecified choice when a local clock reading is skipped or repeated.
func localDayStart(day time.Time, location *time.Location) time.Time {
	year, month, date := day.Date()
	cursor := time.Date(year, month, date, 0, 0, 0, 0, location)
	for {
		start, end := cursor.ZoneBounds()
		_, offset := cursor.Zone()
		midnight := day.Add(-time.Duration(offset) * time.Second)
		if !end.IsZero() && !midnight.Before(end) {
			// This zone ends before the requested clock reading. Advance into
			// the next zone, which may have skipped midnight altogether.
			cursor = end
			continue
		}
		if !start.IsZero() {
			previous := start.Add(-time.Nanosecond)
			_, previousOffset := previous.Zone()
			if day.Add(-time.Duration(previousOffset) * time.Second).Before(start) {
				// The previous zone also reached this date: find its earlier
				// midnight instead of choosing the repeated occurrence.
				cursor = previous
				continue
			}
			if midnight.Before(start) {
				// Midnight lies in a forward gap. The transition is the first
				// real instant of the new local day, even if its clock is 01:00.
				return start.UTC()
			}
		}
		return midnight.UTC()
	}
}
