package ps5

import (
	"slices"
	"testing"
	"time"
)

type firedEvent struct {
	index int
	event Event
}

func TestMonitorObserve(t *testing.T) {
	const (
		on = On
		sb = Standby
		un = Unreachable

		rest = EventEnteredRest
		up   = EventPoweredOn
	)
	tests := []struct {
		name    string
		confirm int
		states  []State
		want    []firedEvent
	}{
		{"fires on transition", 1, []State{on, sb}, []firedEvent{{1, rest}}},
		{"debounced", 2, []State{on, sb, sb}, []firedEvent{{2, rest}}},
		{"started during standby does not fire rest", 1, []State{sb, sb, sb}, nil},
		{"fires once per transition", 1, []State{on, sb, sb, sb}, []firedEvent{{1, rest}}},
		{"blip of standby is ignored", 2, []State{on, sb, on, on}, nil},
		{"silence during shutdown still fires", 1, []State{on, un, un, sb}, []firedEvent{{3, rest}}},
		{"silence alone does not fire", 1, []State{on, un, un, un}, nil},
		{"silence resets the streak", 2, []State{on, sb, un, sb, sb}, []firedEvent{{4, rest}}},

		{"started while on does not fire power-on", 1, []State{on, on}, nil},
		{"wake from standby at startup", 1, []State{sb, on}, []firedEvent{{1, up}}},
		{"full session", 1, []State{on, sb, on, sb}, []firedEvent{{1, rest}, {2, up}, {3, rest}}},
		{"power-on fires once", 1, []State{sb, on, on, on}, []firedEvent{{1, up}}},
		{"short silence is not a power-on", 1, []State{on, un, un, on}, nil},
		{"single unreachable at startup is not a power-on", 1, []State{un, on}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMonitor(MonitorConfig{StandbyConfirm: tt.confirm, OffAfter: time.Hour, DisarmAfter: time.Hour})
			now := time.Unix(0, 0)
			var got []firedEvent
			for i, s := range tt.states {
				if e := m.Observe(s, now); e != EventNone {
					got = append(got, firedEvent{i, e})
				}
				now = now.Add(5 * time.Second)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("events = %v, want %v", got, tt.want)
			}
		})
	}
}

func newTimedMonitor() *Monitor {
	return NewMonitor(MonitorConfig{
		StandbyConfirm:  1,
		OffAfter:        2 * time.Minute,
		ShutdownSilence: 30 * time.Second,
		DisarmAfter:     10 * time.Minute,
	})
}

// The sequences below follow what a real PS5 was logged doing.
func TestMonitorGarbledReplies(t *testing.T) {
	type step struct {
		at    time.Duration
		state State
		want  Event
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"switched off: garbled then silence", []step{
			{0, On, EventNone},
			{5 * time.Second, Garbled, EventNone},
			{10 * time.Second, Garbled, EventNone},
			{30 * time.Second, Unreachable, EventNone},
			{35 * time.Second, Unreachable, EventWentOffline},
			{40 * time.Second, Unreachable, EventNone},
		}},
		{"rest mode: garbled then standby", []step{
			{0, On, EventNone},
			{5 * time.Second, Garbled, EventNone},
			{10 * time.Second, Garbled, EventNone},
			{16 * time.Second, Standby, EventEnteredRest},
		}},
		{"wake from rest: garbled and silence stay quiet", []step{
			{0, On, EventNone},
			{5 * time.Second, Standby, EventEnteredRest},
			{10 * time.Second, Garbled, EventNone},
			{50 * time.Second, Unreachable, EventNone},
			{55 * time.Second, Garbled, EventNone},
			{60 * time.Second, On, EventPoweredOn},
		}},
		{"plain silence still waits for the long threshold", []step{
			{0, On, EventNone},
			{5 * time.Second, Unreachable, EventNone},
			{time.Minute, Unreachable, EventNone},
			{5*time.Second + 2*time.Minute, Unreachable, EventWentOffline},
		}},
		{"a valid reply forgets the garbled one", []step{
			{0, On, EventNone},
			{5 * time.Second, Garbled, EventNone},
			{10 * time.Second, On, EventNone},
			{15 * time.Second, Unreachable, EventNone},
			{time.Minute, Unreachable, EventNone},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTimedMonitor()
			start := time.Unix(0, 0)
			for i, s := range tt.steps {
				if got := m.Observe(s.state, start.Add(s.at)); got != s.want {
					t.Errorf("step %d (%v at %v): event = %v, want %v", i, s.state, s.at, got, s.want)
				}
			}
		})
	}
}

func TestMonitorLongSilenceDisarms(t *testing.T) {
	m := newTimedMonitor()
	start := time.Unix(0, 0)

	m.Observe(On, start)
	m.Observe(Unreachable, start.Add(time.Minute))
	m.Observe(Unreachable, start.Add(12*time.Minute))
	if e := m.Observe(Standby, start.Add(13*time.Minute)); e != EventNone {
		t.Errorf("standby after a long silence = %v: the console was off, not on", e)
	}

	if e := m.Observe(On, start.Add(14*time.Minute)); e != EventPoweredOn {
		t.Errorf("on after being off = %v, want power-on", e)
	}
	if e := m.Observe(Standby, start.Add(15*time.Minute)); e != EventEnteredRest {
		t.Errorf("seeing the console on again must re-arm, got %v", e)
	}
}

func TestMonitorShortSilenceKeepsArmed(t *testing.T) {
	m := newTimedMonitor()
	start := time.Unix(0, 0)

	m.Observe(On, start)
	m.Observe(Unreachable, start.Add(time.Minute))
	m.Observe(Unreachable, start.Add(9*time.Minute))
	if e := m.Observe(Standby, start.Add(10*time.Minute)); e != EventEnteredRest {
		t.Errorf("standby after a short silence = %v, want rest", e)
	}
}

func TestMonitorPowerOnAfterSilence(t *testing.T) {
	tests := []struct {
		name    string
		silence time.Duration
		want    Event
	}{
		{"wifi blip", time.Minute, EventNone},
		{"exactly the threshold", 2 * time.Minute, EventPoweredOn},
		{"switched off overnight", 8 * time.Hour, EventPoweredOn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTimedMonitor()
			start := time.Unix(0, 0)
			m.Observe(On, start)
			m.Observe(Unreachable, start.Add(time.Second))
			m.Observe(Unreachable, start.Add(time.Second+tt.silence))
			if e := m.Observe(On, start.Add(time.Second+tt.silence+time.Second)); e != tt.want {
				t.Errorf("event = %v, want %v", e, tt.want)
			}
		})
	}
}

func TestMonitorWentOffline(t *testing.T) {
	type step struct {
		at    time.Duration
		state State
		want  Event
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"fires once at the threshold", []step{
			{0, On, EventNone},
			{time.Second, Unreachable, EventNone},
			{time.Minute, Unreachable, EventNone},
			{time.Second + 2*time.Minute, Unreachable, EventWentOffline},
			{3 * time.Minute, Unreachable, EventNone},
			{time.Hour, Unreachable, EventNone},
		}},
		{"back online reports power-on and can fire again", []step{
			{0, On, EventNone},
			{time.Second, Unreachable, EventNone},
			{3 * time.Minute, Unreachable, EventWentOffline},
			{4 * time.Minute, On, EventPoweredOn},
			{5 * time.Minute, Unreachable, EventNone},
			{8 * time.Minute, Unreachable, EventWentOffline},
		}},
		{"short silence stays quiet", []step{
			{0, On, EventNone},
			{time.Second, Unreachable, EventNone},
			{time.Minute, Unreachable, EventNone},
			{2 * time.Minute, On, EventNone},
		}},
		{"never seen on at startup stays quiet", []step{
			{0, Unreachable, EventNone},
			{time.Hour, Unreachable, EventNone},
		}},
		{"silence during rest stays quiet", []step{
			{0, On, EventNone},
			{time.Second, Standby, EventEnteredRest},
			{time.Minute, Unreachable, EventNone},
			{time.Hour, Unreachable, EventNone},
		}},
		{"rest after going offline still fires", []step{
			{0, On, EventNone},
			{time.Second, Unreachable, EventNone},
			{3 * time.Minute, Unreachable, EventWentOffline},
			{4 * time.Minute, Standby, EventEnteredRest},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTimedMonitor()
			start := time.Unix(0, 0)
			for i, s := range tt.steps {
				if got := m.Observe(s.state, start.Add(s.at)); got != s.want {
					t.Errorf("step %d (%v at %v): event = %v, want %v", i, s.state, s.at, got, s.want)
				}
			}
		})
	}
}

func TestMonitorPowerOnAfterSilenceAtStartup(t *testing.T) {
	m := newTimedMonitor()
	start := time.Unix(0, 0)
	m.Observe(Unreachable, start)
	m.Observe(Unreachable, start.Add(3*time.Minute))
	if e := m.Observe(On, start.Add(4*time.Minute)); e != EventPoweredOn {
		t.Errorf("event = %v, want power-on", e)
	}
}
