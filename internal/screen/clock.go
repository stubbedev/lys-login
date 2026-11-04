package screen

import "time"

// clockUnit is the finest thing a clock's formats show.
type clockUnit int

const (
	everySecond clockUnit = iota
	everyMinute
	everyHour
)

// probe is a moment whose second, minute and hour each show differently from the
// next one's in any format that shows them.
var probe = time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC)

func finestUnit(formats []func(time.Time) string) clockUnit {
	unit := everyHour
	for _, format := range formats {
		shown := format(probe)
		switch {
		case format(probe.Add(time.Second)) != shown:
			return everySecond
		case format(probe.Add(time.Minute)) != shown:
			unit = everyMinute
		}
	}
	return unit
}

// next is when a display of this unit next changes after now: the start of the
// next second, minute or hour on the local clock, which daylight saving and
// half-hour zones keep from being a whole multiple of it.
func (u clockUnit) next(now time.Time) time.Time {
	y, mo, d := now.Date()
	h, m, s := now.Clock()
	var at time.Time
	var step time.Duration
	switch u {
	case everySecond:
		at, step = time.Date(y, mo, d, h, m, s+1, 0, now.Location()), time.Second
	case everyMinute:
		at, step = time.Date(y, mo, d, h, m+1, 0, 0, now.Location()), time.Minute
	default:
		at, step = time.Date(y, mo, d, h+1, 0, 0, 0, now.Location()), time.Hour
	}
	if !at.After(now) {
		return now.Truncate(step).Add(step)
	}
	return at
}

// ClockTicker shows a clock when what its formats show changes, and not in
// between: a clock of hours and minutes wakes the loop once a minute.
type ClockTicker struct {
	now     func() time.Time
	after   func(d time.Duration, fn func()) (cancel func())
	show    func()
	armed   bool
	unit    clockUnit
	running bool
	cancel  func()
	due     time.Time
}

// NewClockTicker makes a stopped ticker that calls show on the loop whenever one
// of formats would give something new; after runs a function on the loop, once.
// With no formats nothing shows a clock, so it never arms.
func NewClockTicker(now func() time.Time, after func(time.Duration, func()) func(), show func(), formats ...func(time.Time) string) *ClockTicker {
	return &ClockTicker{now: now, after: after, show: show, armed: len(formats) > 0, unit: finestUnit(formats)}
}

// Start shows the clock and keeps it right until Stop.
func (c *ClockTicker) Start() {
	c.show()
	c.Arm()
}

// Arm keeps a clock shown just now right until Stop.
func (c *ClockTicker) Arm() {
	c.disarm()
	c.running = c.armed
	if !c.running {
		return
	}
	now := c.now()
	c.due = c.unit.next(now)
	c.cancel = c.after(c.due.Sub(now), c.fired)
}

func (c *ClockTicker) fired() {
	if c.running {
		c.Start()
	}
}

func (c *ClockTicker) disarm() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

// Stop lets the clock go stale until the next Start.
func (c *ClockTicker) Stop() {
	c.disarm()
	c.running = false
}

// CatchUp shows the clock now if its timer is overdue, as after a suspend, which
// timers do not count. It costs nothing while the clock is right, so a key can
// call it.
func (c *ClockTicker) CatchUp() {
	if c.running && !c.now().Before(c.due) {
		c.Start()
	}
}
