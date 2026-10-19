package tasks

import (
	"time"

	"recall/internal/model"
)

// DateRange converts inclusive local YYYY-MM-DD bounds to a UTC [start, until)
// range. An empty bound is unbounded. location must be non-nil; callers can pass
// time.Local for the computer's timezone. Days use local calendar arithmetic,
// so daylight-saving transitions need not span 24 hours.
func DateRange(from, through string, location *time.Location) (start, until *time.Time, err error) {
	if location == nil {
		return nil, nil, &model.Error{Code: "invalid_argument", Message: "a timezone is required for date ranges"}
	}
	if from != "" {
		day, err := time.ParseInLocation(time.DateOnly, from, location)
		if err != nil {
			return nil, nil, &model.Error{Code: "invalid_argument", Message: "from must be a valid YYYY-MM-DD date"}
		}
		utc := day.UTC()
		start = &utc
	}
	if through != "" {
		day, err := time.ParseInLocation(time.DateOnly, through, location)
		if err != nil {
			return nil, nil, &model.Error{Code: "invalid_argument", Message: "through must be a valid YYYY-MM-DD date"}
		}
		utc := day.AddDate(0, 0, 1).UTC()
		until = &utc
	}
	if start != nil && until != nil && !start.Before(*until) {
		return nil, nil, &model.Error{Code: "invalid_argument", Message: "from must not be after through"}
	}
	return start, until, nil
}
