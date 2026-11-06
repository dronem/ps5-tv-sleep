package app

import (
	"context"
	"log/slog"

	"github.com/dronem/ps5-tv-sleep/internal/ps5"
	"github.com/dronem/ps5-tv-sleep/internal/tv"
)

const (
	powerOnMessage = "🎮 PS5 turned on"
	restMessage    = "💤 PS5 entered rest mode"
	offlineMessage = "📴 PS5 went offline: switched off or no connection"
	tvOnMessage    = "📺 TV turned on"
	tvOffMessage   = "📺 TV turned off"

	statusButton = "📊 Status"
	tvOffButton  = "📺 Turn off TV"
	buttonHint   = "Use the buttons at the bottom of the chat"
)

// keyboard lists the buttons shown in the chat.
func keyboard(allowTVOff bool) []string {
	if allowTVOff {
		return []string{statusButton, tvOffButton}
	}
	return []string{statusButton}
}

// commands answers messages sent to the bot.
type commands struct {
	client     *tv.Client
	probePS5   func(context.Context) (ps5.State, error)
	allowTVOff bool
	reply      func(string)
}

func (c commands) handle(ctx context.Context, text string) {
	switch {
	case text == statusButton:
		c.status(ctx)
	case text == tvOffButton && c.allowTVOff:
		c.turnOffTV(ctx)
	default:
		// Any message, /start included, brings up the buttons.
		c.reply(buttonHint)
	}
}

// status reads both devices now rather than reporting the last poll. Reading
// does not wake the TV.
func (c commands) status(ctx context.Context) {
	console, err := c.probePS5(ctx)
	if err != nil && ctx.Err() == nil {
		slog.Warn("ps5 probe failed", "err", err)
	}
	screen, err := c.client.State(ctx)
	if ctx.Err() != nil {
		return
	}
	var activity string
	if screen == tv.On {
		var activityErr error
		activity, activityErr = c.client.ForegroundActivity(ctx)
		if ctx.Err() != nil {
			return
		}
		if activityErr != nil {
			slog.Warn("could not read tv foreground activity", "err", activityErr)
		}
	}
	slog.Info("status requested from telegram", "ps5", console, "tv", screen, "activity", activity, "tv_err", err)
	c.reply(statusReport(console, screen, activity))
}

func (c commands) turnOffTV(ctx context.Context) {
	slog.Info("tv power off requested from telegram")
	outcome, err := c.client.PowerOffNow(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("tv power off failed", "err", err)
	} else {
		slog.Info("tv power off finished", "outcome", outcome)
	}
	c.reply(tvOffReply(outcome, err))
}

// statusReport describes the current state of both devices for the chat. activity
// is what the TV is showing: it is reported only for a TV that is on, and empty
// means it could not be read.
func statusReport(console ps5.State, screen tv.State, activity string) string {
	var consoleText, screenText string
	switch console {
	case ps5.On:
		consoleText = "on"
	case ps5.Standby:
		consoleText = "in rest mode"
	case ps5.Garbled:
		consoleText = "changing power state"
	default:
		consoleText = "not answering (switched off or no connection)"
	}
	switch screen {
	case tv.On:
		screenText = "on"
	case tv.Off:
		screenText = "off"
	default:
		screenText = "not answering (asleep or no connection)"
	}
	report := "🎮 PS5: " + consoleText + "\n📺 TV: " + screenText
	if screen == tv.On {
		if activity == "" {
			activity = "unknown"
		}
		report += "\n▶️ Activity: " + activity
	}
	return report
}

// tvOffReply describes the result of a button press for the chat. Details of a
// failure stay in the service log.
func tvOffReply(outcome tv.Outcome, err error) string {
	switch {
	case err != nil:
		return "⚠️ Could not turn off the TV, see the service log"
	case outcome == tv.OutcomeSleepSent:
		return "📺 Command to turn off the TV sent"
	case outcome == tv.OutcomeAlreadyOff:
		return "📺 The TV is already off"
	case outcome == tv.OutcomeNoAnswer:
		return "📺 The TV is not answering: it is probably already off"
	case outcome == tv.OutcomeDryRun:
		return "🧪 DRY_RUN is on: not turning off the TV"
	default:
		return "⚠️ The TV was not turned off: " + string(outcome)
	}
}
