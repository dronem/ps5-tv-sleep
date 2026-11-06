// Package app runs the service: it watches the PS5 and the TV, powers the TV off
// when the console stops being used, and talks to the user over Telegram.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/dronem/ps5-tv-sleep/internal/config"
	"github.com/dronem/ps5-tv-sleep/internal/ps5"
	"github.com/dronem/ps5-tv-sleep/internal/telegram"
	"github.com/dronem/ps5-tv-sleep/internal/tv"
)

const (
	// offAfter is how long a PS5 must stay silent to be reported as gone, and
	// for its next "on" reply to count as a power-on rather than a network blip.
	offAfter = 2 * time.Minute
	// shutdownSilence is the same limit once the PS5 has sent a garbled reply.
	// Entering rest mode was seen to take at most 11 seconds from that reply.
	shutdownSilence = 30 * time.Second
	// disarmAfter limits how long a PS5 may stay silent and still count as "was
	// just on". Entering rest mode takes well under this; a longer silence means
	// the console was switched off, and a later standby reply is not a transition.
	disarmAfter = 10 * time.Minute
	// tvPollInterval is how often the TV's power state is read over adb.
	tvPollInterval = 10 * time.Second

	probeTimeout  = time.Second
	probeAttempts = 3
)

// messenger is the chat with the user.
type messenger interface {
	Send(ctx context.Context, text string) error
	Listen(ctx context.Context, handle func(ctx context.Context, text string))
}

// App is the running service. Use it once.
type App struct {
	cfg    config.Config
	tvAddr string
	prober ps5.Prober
	tv     *tv.Client
	chat   messenger // nil when Telegram is not configured

	tvPollInterval time.Duration

	// wg tracks everything Run started, so that Run returns only after it ended.
	wg sync.WaitGroup
}

// New returns an App that runs adb commands through adb.
func New(cfg config.Config, adb tv.Runner) *App {
	tvAddr := net.JoinHostPort(cfg.TVHost, cfg.TVPort)
	a := &App{
		cfg:    cfg,
		tvAddr: tvAddr,
		prober: ps5.Prober{
			Addr:     net.JoinHostPort(cfg.PS5Host, strconv.Itoa(ps5.DDPPort)),
			Timeout:  probeTimeout,
			Attempts: probeAttempts,
		},
		tv: tv.NewClient(tv.ClientConfig{
			Serial:       tvAddr,
			RequireFocus: cfg.RequireFocus,
			DryRun:       cfg.DryRun,
		}, adb),
		tvPollInterval: tvPollInterval,
	}
	if cfg.TelegramToken != "" {
		a.chat = telegram.New(cfg.TelegramToken, cfg.TelegramChatID, keyboard(cfg.TVButton)...)
	}
	return a
}

// Run works until ctx is cancelled, which is a normal stop and returns nil.
func (a *App) Run(ctx context.Context) error {
	err := a.tv.StartServer(ctx)
	if ctx.Err() != nil {
		return nil //nolint:nilerr // Stopped during startup, which also kills adb: a normal stop.
	}
	if err != nil {
		return fmt.Errorf("start adb server: %w", err)
	}

	slog.Info("started", "ps5", a.prober.Addr, "tv", a.tvAddr,
		"poll", a.cfg.PollInterval, "standby_confirm", a.cfg.StandbyConfirm, "dry_run", a.cfg.DryRun,
		"telegram", a.chat != nil, "notify_ps5", a.cfg.NotifyPS5, "notify_tv", a.cfg.NotifyTV, "tv_button", a.cfg.TVButton)

	if a.chat != nil {
		bot := commands{
			client:     a.tv,
			probePS5:   a.prober.Probe,
			allowTVOff: a.cfg.TVButton,
			reply:      func(text string) { a.notify(ctx, text) },
		}
		a.wg.Go(func() { a.chat.Listen(ctx, bot.handle) })
	}
	// Runs alongside polling: it may wait a while for the prompt on the TV.
	a.wg.Go(func() { a.superviseTV(ctx) })

	a.watchPS5(ctx)

	slog.Info("stopping")
	a.wg.Wait()
	return nil
}

// notify sends text in the background: a slow Telegram must not delay polling or
// the TV. It does nothing when Telegram is not configured.
func (a *App) notify(ctx context.Context, text string) {
	if a.chat == nil {
		return
	}
	a.wg.Go(func() {
		if err := a.chat.Send(ctx, text); err != nil {
			if ctx.Err() == nil {
				slog.Error("telegram notification failed", "err", err)
			}
			return
		}
		slog.Info("telegram notification sent", "text", text)
	})
}

// watchPS5 polls the console and acts on its power transitions until ctx is
// cancelled.
func (a *App) watchPS5(ctx context.Context) {
	monitor := ps5.NewMonitor(ps5.MonitorConfig{
		StandbyConfirm:  a.cfg.StandbyConfirm,
		OffAfter:        offAfter,
		ShutdownSilence: shutdownSilence,
		DisarmAfter:     disarmAfter,
	})
	notify := func(text string) {
		if a.cfg.NotifyPS5 {
			a.notify(ctx, text)
		}
	}

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	var last ps5.State
	first := true
	for {
		state, err := a.prober.Probe(ctx)
		if ctx.Err() != nil {
			return // A probe cut short says nothing about the console.
		}
		if err != nil {
			slog.Warn("ps5 probe failed", "err", err)
		}
		if first || state != last {
			slog.Info("ps5 state", "state", state)
			last, first = state, false
		}
		handlePS5Event(ctx, monitor.Observe(state, time.Now()), a.tv, notify)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// handlePS5Event notifies about a PS5 transition and powers the TV off when the
// console has stopped being used. A console that was switched off fully just goes
// silent, so a long outage of the PS5's own network also powers the TV off.
func handlePS5Event(ctx context.Context, event ps5.Event, client *tv.Client, notify func(string)) {
	switch event {
	case ps5.EventPoweredOn:
		slog.Info("ps5 powered on")
		notify(powerOnMessage)
		return
	case ps5.EventWentOffline:
		slog.Info("ps5 went offline, powering off tv")
		notify(offlineMessage)
	case ps5.EventEnteredRest:
		slog.Info("ps5 entered rest mode, powering off tv")
		notify(restMessage)
	default:
		return
	}

	outcome, err := client.PowerOff(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("tv power off failed", "err", err)
		}
		return
	}
	slog.Info("tv power off finished", "outcome", outcome)
}

// superviseTV checks the adb connection, then watches the TV if anyone wants to
// hear about it.
func (a *App) superviseTV(ctx context.Context) {
	monitor := tv.NewMonitor(offAfter)
	if err := a.tv.CheckConnection(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("tv is not reachable over adb yet; will retry when the ps5 enters rest mode", "err", err)
		// The TV may come up before the first poll, which then has to count
		// as turning on rather than as "was already on".
		monitor.Observe(tv.Unreachable, time.Now())
	} else {
		slog.Info("tv adb connection ok")
	}
	// After the check, so polling cannot interfere with the approval prompt.
	if a.chat != nil && a.cfg.NotifyTV {
		a.watchTV(ctx, monitor)
	}
}

// watchTV reports each time the TV turns on or off, until ctx is cancelled.
func (a *App) watchTV(ctx context.Context, monitor *tv.Monitor) {
	ticker := time.NewTicker(a.tvPollInterval)
	defer ticker.Stop()

	var last tv.State
	first := true
	for {
		state, err := a.tv.State(ctx)
		if ctx.Err() != nil {
			return
		}
		if first || state != last {
			slog.Info("tv state", "state", state, "err", err)
			last, first = state, false
		}
		switch monitor.Observe(state, time.Now()) {
		case tv.EventTurnedOn:
			slog.Info("tv turned on")
			a.notify(ctx, tvOnMessage)
		case tv.EventTurnedOff:
			slog.Info("tv turned off")
			a.notify(ctx, tvOffMessage)
		case tv.EventNone:
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
