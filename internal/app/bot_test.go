package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dronem/ps5-tv-sleep/internal/ps5"
	"github.com/dronem/ps5-tv-sleep/internal/tv"
)

func newTestCommands(screen *fakeTV, allowTVOff bool, replies *[]string) commands {
	return commands{
		client:     newTVClient(screen),
		probePS5:   func(context.Context) (ps5.State, error) { return ps5.Standby, nil },
		allowTVOff: allowTVOff,
		reply:      func(text string) { *replies = append(*replies, text) },
	}
}

func TestCommands(t *testing.T) {
	const statusReply = "🎮 PS5: in rest mode\n📺 TV: on\n▶️ Activity: " + testActivity

	tests := []struct {
		name       string
		text       string
		allowTVOff bool
		wantReply  string
		wantSleep  bool
	}{
		{"status", statusButton, true, statusReply, false},
		{"status with the tv button disabled", statusButton, false, statusReply, false},
		{"turn off", tvOffButton, true, "sent", true},
		{"turn off is refused when disabled", tvOffButton, false, buttonHint, false},
		{"any other message shows the buttons", "/start", true, buttonHint, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := &fakeTV{}
			var replies []string
			bot := newTestCommands(screen, tt.allowTVOff, &replies)

			bot.handle(context.Background(), tt.text)

			if len(replies) != 1 || !strings.Contains(replies[0], tt.wantReply) {
				t.Errorf("replies = %q, want one containing %q", replies, tt.wantReply)
			}
			if got := screen.sleepSent(); got != tt.wantSleep {
				t.Errorf("sleep key sent = %v, want %v", got, tt.wantSleep)
			}
		})
	}
}

func TestStatusWhenActivityCannotBeRead(t *testing.T) {
	screen := &fakeTV{failOn: "dumpsys window"}
	var replies []string
	bot := newTestCommands(screen, true, &replies)

	bot.handle(context.Background(), statusButton)

	want := "🎮 PS5: in rest mode\n📺 TV: on\n▶️ Activity: unknown"
	if len(replies) != 1 || replies[0] != want {
		t.Errorf("replies = %q, want %q: the rest of the status must still arrive", replies, want)
	}
}

func TestStatusDoesNotAskSleepingTVForActivity(t *testing.T) {
	screen := &fakeTV{}
	screen.asleep.Store(true)
	screen.failOn = "dumpsys window" // A question would show up as an "unknown" line.
	var replies []string
	bot := newTestCommands(screen, true, &replies)

	bot.handle(context.Background(), statusButton)

	want := "🎮 PS5: in rest mode\n📺 TV: off"
	if len(replies) != 1 || replies[0] != want {
		t.Errorf("replies = %q, want %q", replies, want)
	}
}

func TestKeyboard(t *testing.T) {
	if got, want := keyboard(true), []string{statusButton, tvOffButton}; !slices.Equal(got, want) {
		t.Errorf("keyboard(true) = %q, want %q", got, want)
	}
	if got, want := keyboard(false), []string{statusButton}; !slices.Equal(got, want) {
		t.Errorf("keyboard(false) = %q, want %q", got, want)
	}
}

func TestStatusReport(t *testing.T) {
	tests := []struct {
		name     string
		console  ps5.State
		screen   tv.State
		activity string
		want     string
	}{
		{"tv on", ps5.On, tv.On, "com.vendor.tv/.Hdmi", "🎮 PS5: on\n📺 TV: on\n▶️ Activity: com.vendor.tv/.Hdmi"},
		{"activity not read", ps5.Garbled, tv.On, "", "🎮 PS5: changing power state\n📺 TV: on\n▶️ Activity: unknown"},
		{"tv off", ps5.Standby, tv.Off, "", "🎮 PS5: in rest mode\n📺 TV: off"},
		{
			"nothing answers", ps5.Unreachable, tv.Unreachable, "",
			"🎮 PS5: not answering (switched off or no connection)\n📺 TV: not answering (asleep or no connection)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusReport(tt.console, tt.screen, tt.activity); got != tt.want {
				t.Errorf("statusReport = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTVOffReply(t *testing.T) {
	tests := []struct {
		name    string
		outcome tv.Outcome
		err     error
		want    string
	}{
		{"sent", tv.OutcomeSleepSent, nil, "sent"},
		{"already off", tv.OutcomeAlreadyOff, nil, "already off"},
		{"dry run", tv.OutcomeDryRun, nil, "DRY_RUN"},
		{"no answer", tv.OutcomeNoAnswer, nil, "not answering"},
		{"other outcome", tv.OutcomeFocusMismatch, nil, "was not turned off"},
		{"failure", "", errors.New("adb connect 192.168.1.6: refused"), "Could not"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tvOffReply(tt.outcome, tt.err)
			if !strings.Contains(got, tt.want) {
				t.Errorf("reply = %q, want it to contain %q", got, tt.want)
			}
			if strings.Contains(got, "192.168") {
				t.Errorf("reply = %q: error details must stay in the log", got)
			}
		})
	}
}
