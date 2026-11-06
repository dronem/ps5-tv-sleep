package tv

import (
	"slices"
	"testing"
	"time"
)

func TestMonitor(t *testing.T) {
	const (
		on  = On
		off = Off
		un  = Unreachable
		gap = time.Minute

		up   = EventTurnedOn
		down = EventTurnedOff
	)
	type fired struct {
		index int
		event Event
	}
	tests := []struct {
		name   string
		states []State
		want   []fired
	}{
		{"started while on stays quiet", []State{on, on}, nil},
		{"started while off stays quiet", []State{off, off}, nil},
		{"started while off", []State{off, on}, []fired{{1, up}}},
		{"turned on fires once", []State{off, on, on, on}, []fired{{1, up}}},
		{"turned off fires once", []State{on, off, off, un, un, un}, []fired{{1, down}}},
		{"every session", []State{on, off, on, off, on}, []fired{{1, down}, {2, up}, {3, down}, {4, up}}},
		{"short silence is a blip", []State{on, un, un, on}, nil},
		{"long silence counts as off", []State{on, un, un, un, on}, []fired{{3, down}, {4, up}}},
		{"long silence at startup counts as off", []State{un, un, un, on}, []fired{{3, up}}},
		{"silent at startup counts as off", []State{un, on}, []fired{{1, up}}},
		{"startup rule fires once", []State{un, on, on, un, on}, []fired{{1, up}}},
		{"off then short silence still fires", []State{off, un, on}, []fired{{2, up}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMonitor(2 * gap)
			now := time.Unix(0, 0)
			var got []fired
			for i, s := range tt.states {
				if e := m.Observe(s, now); e != EventNone {
					got = append(got, fired{i, e})
				}
				now = now.Add(gap)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("events = %v, want %v", got, tt.want)
			}
		})
	}
}
