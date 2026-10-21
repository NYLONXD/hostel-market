// Package clock abstracts time so business rules can be tested at fixed instants.
package clock

import (
	"sync"
	"time"
	_ "time/tzdata" // IST must load even on hosts without a zoneinfo database (e.g. Windows)
)

// IST is Asia/Kolkata. Listing availability windows are evaluated in this zone.
var IST = mustLoad("Asia/Kolkata")

type Clock interface {
	Now() time.Time
}

// System is the real wall clock.
type System struct{}

func (System) Now() time.Time { return time.Now() }

// Manual is a settable clock for tests.
type Manual struct {
	mu sync.Mutex
	t  time.Time
}

func NewManual(t time.Time) *Manual { return &Manual{t: t} }

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t
}

func (m *Manual) Set(t time.Time) {
	m.mu.Lock()
	m.t = t
	m.mu.Unlock()
}

func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	m.t = m.t.Add(d)
	m.mu.Unlock()
}

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("clock: load location " + name + ": " + err.Error())
	}
	return loc
}
