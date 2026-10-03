package keel

import (
	"encoding/json"
	"time"
)

// Int is a whole Convex number. Convex sends numbers as floats (`5432.0`), which encoding/json
// refuses to put in an int.
type Int int

func (n *Int) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = Int(f)
	return nil
}

// Time is a Convex timestamp (milliseconds since the epoch), printed as RFC 3339 in UTC.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	var ms float64
	if err := json.Unmarshal(b, &ms); err != nil {
		return err
	}
	t.Time = time.UnixMilli(int64(ms)).UTC()
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
}
