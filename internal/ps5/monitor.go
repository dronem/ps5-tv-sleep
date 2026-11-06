package ps5

import "time"

// Event is a power transition detected by Monitor.
type Event int

// The transitions Monitor reports.
const (
	EventNone Event = iota
	EventEnteredRest
	EventPoweredOn
	EventWentOffline
)

// MonitorConfig sets how Monitor separates real transitions from network noise.
type MonitorConfig struct {
	// StandbyConfirm is how many standby replies in a row count as rest mode.
	StandbyConfirm int
	// OffAfter is how long the console must stay silent to count as off, so
	// that a Wi-Fi blip does not look like a power-on.
	OffAfter time.Duration
	// ShutdownSilence replaces OffAfter once the console has sent a garbled
	// reply: a network outage does not produce those, a power change does.
	ShutdownSilence time.Duration
	// DisarmAfter limits how long the console may stay silent and still count
	// as "was just on" for EventEnteredRest.
	DisarmAfter time.Duration
}

// Monitor turns a stream of PS5 states into power transition events.
//
// EventEnteredRest fires only after the console was seen on, so starting the
// service while the PS5 is already resting does not switch off a TV someone is
// watching. EventPoweredOn fires only after the console was seen off, so
// starting the service while the PS5 is on does not send a false notification.
// EventWentOffline fires when a console that was on stays silent for OffAfter,
// or for ShutdownSilence if a garbled reply preceded the silence. Plain silence
// looks the same for a switched-off console and a lost network.
type Monitor struct {
	cfg MonitorConfig

	armed            bool // seen on, and rest mode not reported yet
	off              bool // seen off, and power-on not reported yet
	garbled          bool // garbled reply since the last valid one
	standbyStreak    int
	unreachableSince time.Time
}

// NewMonitor returns a Monitor that has seen nothing yet.
func NewMonitor(cfg MonitorConfig) *Monitor {
	return &Monitor{cfg: cfg}
}

// Observe records a state and returns the transition it completes, if any.
func (m *Monitor) Observe(state State, now time.Time) Event {
	switch state {
	case On:
		event := EventNone
		if m.off {
			event = EventPoweredOn
		}
		m.armed = true
		m.off = false
		m.garbled = false
		m.standbyStreak = 0
		m.unreachableSince = time.Time{}
		return event
	case Standby:
		m.garbled = false
		m.unreachableSince = time.Time{}
		if !m.armed {
			m.off = true
			return EventNone
		}
		m.standbyStreak++
		if m.standbyStreak >= m.cfg.StandbyConfirm {
			m.armed = false
			m.off = true
			m.standbyStreak = 0
			return EventEnteredRest
		}
	case Unreachable, Garbled:
		m.standbyStreak = 0
		if state == Garbled {
			m.garbled = true
		}
		if m.unreachableSince.IsZero() {
			m.unreachableSince = now
		}
		offAfter := m.cfg.OffAfter
		if m.garbled {
			offAfter = m.cfg.ShutdownSilence
		}
		silence := now.Sub(m.unreachableSince)
		event := EventNone
		if silence >= offAfter {
			if m.armed && !m.off {
				event = EventWentOffline
			}
			m.off = true
		}
		if silence >= m.cfg.DisarmAfter {
			m.armed = false
		}
		return event
	}
	return EventNone
}
