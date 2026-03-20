package runtime

import (
	"fmt"
	"time"
)

type Clock struct {
	startNS int64
	endNS   int64
	curNS   int64

	loc        *time.Location
	tradingDay string
}

func NewClock(start, end time.Time, loc *time.Location) (*Clock, error) {
	if loc == nil {
		loc = time.Local
	}
	s := start.In(loc)
	e := end.In(loc)
	if !s.Before(e) {
		return nil, fmt.Errorf("start must be before end")
	}
	c := &Clock{
		startNS: s.UnixNano(),
		endNS:   e.UnixNano(),
		curNS:   s.UnixNano(),
		loc:     loc,
	}
	c.tradingDay = c.dayString(c.curNS)
	return c, nil
}

func (c *Clock) AdvanceTo(ts int64) bool {
	if ts < c.curNS {
		return false
	}
	c.curNS = ts
	day := c.dayString(c.curNS)
	switched := day != c.tradingDay
	c.tradingDay = day
	return switched
}

func (c *Clock) CurrentNS() int64 {
	return c.curNS
}

func (c *Clock) TradingDay() string {
	return c.tradingDay
}

// dayString computes the trading day for a given timestamp.
// Chinese futures night sessions (hour >= 18) belong to the NEXT trading day;
// late-night sessions (hour < 3) belong to the current calendar day.
// Weekends are skipped.
func (c *Clock) dayString(ts int64) string {
	t := time.Unix(0, ts).In(c.loc)
	hour := t.Hour()
	if hour >= 18 {
		// Night session before midnight → trading day is next calendar day
		t = t.AddDate(0, 0, 1)
	}
	// Skip weekends
	for t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		t = t.AddDate(0, 0, 1)
	}
	return t.Format("2006-01-02")
}
