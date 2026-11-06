package tv

import "time"

// State is the TV's power state as seen over adb.
type State int

// The states a TV can be in.
const (
	Unreachable State = iota
	Off
	On
)

// String returns the state's name as it appears in the log.
func (s State) String() string {
	switch s {
	case On:
		return "on"
	case Off:
		return "off"
	default:
		return "unreachable"
	}
}

// Event is a power transition reported by Monitor.
type Event int

// The transitions Monitor reports.
const (
	EventNone Event = iota
	EventTurnedOn
	EventTurnedOff
)

// Monitor turns a stream of power states into "the TV just turned on or off".
//
// It reports turning on only after the TV was seen off, and turning off only after
// it was seen on, so starting the service stays quiet either way. A TV may drop off
// the network in standby, so silence longer than offAfter also counts as off; a
// shorter one is treated as a network blip. There is no earlier "on" to fall back on
// at startup, so a TV that does not answer the first read counts as off right away.
type Monitor struct {
	offAfter time.Duration

	observed         bool
	on               bool
	off              bool
	unreachableSince time.Time
}

// NewMonitor returns a Monitor that has seen nothing yet.
func NewMonitor(offAfter time.Duration) *Monitor {
	return &Monitor{offAfter: offAfter}
}

// Observe records a state and returns the transition it completes, if any.
func (m *Monitor) Observe(state State, now time.Time) Event {
	first := !m.observed
	m.observed = true

	switch state {
	case On:
		turnedOn := m.off
		m.on = true
		m.off = false
		m.unreachableSince = time.Time{}
		if turnedOn {
			return EventTurnedOn
		}
	case Off:
		m.unreachableSince = time.Time{}
		return m.markOff()
	case Unreachable:
		if m.unreachableSince.IsZero() {
			m.unreachableSince = now
		}
		if first || now.Sub(m.unreachableSince) >= m.offAfter {
			return m.markOff()
		}
	}
	return EventNone
}

func (m *Monitor) markOff() Event {
	wasOn := m.on
	m.on = false
	m.off = true
	if wasOn {
		return EventTurnedOff
	}
	return EventNone
}
