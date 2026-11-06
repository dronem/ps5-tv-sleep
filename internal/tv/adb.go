// Package tv controls an Android TV through the adb command line tool and detects
// its power transitions.
package tv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	adbCommandTimeout = 15 * time.Second
	// sleepSettle is how long after a sleep key a TV that stops answering counts
	// as off rather than unreachable: it closes its adb session as it goes to sleep.
	sleepSettle = time.Minute
)

// Runner runs one adb command and returns its combined output.
type Runner func(ctx context.Context, args ...string) (string, error)

// ExecADB is the Runner that uses the adb binary on PATH.
func ExecADB(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "adb", args...) //nolint:gosec // The arguments come from this package and from the operator's own config.
	// The first adb call forks a server that can inherit our output pipe;
	// without WaitDelay, reading the output would block for the server's lifetime.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return string(out), err
}

// Outcome says how a power-off request ended when no error occurred.
type Outcome string

// The ways a power-off request can end.
const (
	OutcomeSleepSent     Outcome = "sleep key sent"
	OutcomeAlreadyOff    Outcome = "tv already off"
	OutcomeFocusMismatch Outcome = "skipped: foreground app does not match TV_REQUIRE_FOCUS"
	OutcomeDryRun        Outcome = "dry run: sleep key not sent"
	OutcomeNoAnswer      Outcome = "tv does not answer, probably already off"
)

// errNoAnswer means adb connect hung, which is how a TV in standby behaves.
var errNoAnswer = errors.New("tv does not answer")

// ClientConfig describes the TV a Client controls.
type ClientConfig struct {
	// Serial is the TV's adb address, host:port.
	Serial string
	// RequireFocus, if set, limits PowerOff to foreground apps that match it.
	RequireFocus *regexp.Regexp
	// DryRun checks everything but does not send the sleep key.
	DryRun bool
}

// Client controls an Android TV through the adb command line tool. It is safe for
// concurrent use.
type Client struct {
	serial       string
	run          Runner
	requireFocus *regexp.Regexp
	dryRun       bool

	// mu keeps one operation's reconnect from breaking another's session.
	mu          sync.Mutex
	sleepSentAt time.Time

	powerOffAttempts int
	powerOffDelay    time.Duration
	checkAttempts    int
	checkDelay       time.Duration

	powerStateTimeout time.Duration
	connectTimeout    time.Duration
}

// NewClient returns a Client that runs adb commands through run.
func NewClient(cfg ClientConfig, run Runner) *Client {
	return &Client{
		serial:           cfg.Serial,
		run:              run,
		requireFocus:     cfg.RequireFocus,
		dryRun:           cfg.DryRun,
		powerOffAttempts: 5,
		powerOffDelay:    3 * time.Second,
		checkAttempts:    24,
		checkDelay:       5 * time.Second,

		powerStateTimeout: 8 * time.Second,
		connectTimeout:    adbCommandTimeout,
	}
}

// StartServer starts the local adb server, and fails if adb cannot run at all.
func (c *Client) StartServer(ctx context.Context) error {
	_, err := c.adb(ctx, "start-server")
	return err
}

// CheckConnection connects at startup so the authorization prompt shows up on
// the TV while someone is setting the service up. It keeps the connection open
// between attempts: reconnecting would dismiss the prompt. A TV that does not
// answer at all is usually in standby, so there is nothing to wait for.
func (c *Client) CheckConnection(ctx context.Context) error {
	var err error
	for attempt := range c.checkAttempts {
		if attempt > 0 {
			slog.Info("waiting for tv adb; if a debugging prompt is on screen, allow it", "err", err)
			if !sleep(ctx, c.checkDelay) {
				return ctx.Err()
			}
		}
		var retry bool
		retry, err = c.checkOnce(ctx)
		if !retry {
			return err
		}
	}
	return err
}

// checkOnce holds the lock for one attempt only: the whole check can take minutes,
// and a power-off request must not wait for that.
func (c *Client) checkOnce(ctx context.Context) (retry bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connect(ctx); err != nil {
		return awaitingApproval(err), err
	}
	_, err = c.adb(ctx, "-s", c.serial, "get-state")
	return err != nil, err
}

// awaitingApproval reports whether adb failed because the TV has not yet
// allowed this key, as opposed to not answering.
func awaitingApproval(err error) bool {
	s := err.Error()
	return strings.Contains(s, "unauthorized") || strings.Contains(s, "failed to authenticate")
}

// PowerOff sleeps the TV if it is on and the foreground app passes
// TV_REQUIRE_FOCUS. Use it for automatic decisions.
func (c *Client) PowerOff(ctx context.Context) (Outcome, error) {
	return c.powerOff(ctx, true)
}

// PowerOffNow sleeps the TV if it is on, whatever the foreground app. Use it
// when a person explicitly asked for it.
func (c *Client) PowerOffNow(ctx context.Context) (Outcome, error) {
	return c.powerOff(ctx, false)
}

// powerOff rebuilds the connection between attempts because an adb session goes
// stale while the TV is in standby.
func (c *Client) powerOff(ctx context.Context, checkFocus bool) (Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var err error
	for attempt := 1; attempt <= c.powerOffAttempts; attempt++ {
		var outcome Outcome
		outcome, err = c.powerOffOnce(ctx, checkFocus)
		if err == nil {
			return outcome, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Do not keep trying: a TV that wakes up during the retries is most
		// likely being turned on, and the sleep key would switch it right off.
		if errors.Is(err, errNoAnswer) {
			slog.Info("tv power off skipped", "err", err)
			return OutcomeNoAnswer, nil
		}
		slog.Warn("tv power off attempt failed", "attempt", attempt, "err", err)
		if _, derr := c.adb(ctx, "disconnect", c.serial); derr != nil {
			slog.Debug("adb disconnect failed", "err", derr)
		}
		if attempt < c.powerOffAttempts && !sleep(ctx, c.powerOffDelay) {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("gave up after %d attempts: %w", c.powerOffAttempts, err)
}

// State reads whether the TV is on. It only reads, so it does not wake the TV. A
// failure means Unreachable, or Off just after a sleep key; the error says why.
func (c *Client) State(ctx context.Context) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A TV in standby may not answer at all. Giving up early keeps a poll from
	// holding the lock for long and delaying a power-off request.
	readCtx, cancel := context.WithTimeout(ctx, c.powerStateTimeout)
	defer cancel()

	on, err := c.readPower(readCtx)
	if err != nil {
		// The next poll needs a fresh session, but dropping one that is
		// awaiting approval would dismiss the prompt on the TV.
		if ctx.Err() == nil && !awaitingApproval(err) {
			if _, derr := c.adb(ctx, "disconnect", c.serial); derr != nil {
				slog.Debug("adb disconnect failed", "err", derr)
			}
		}
		if time.Since(c.sleepSentAt) < sleepSettle {
			return Off, err
		}
		return Unreachable, err
	}
	if on {
		return On, nil
	}
	return Off, nil
}

// ForegroundActivity returns what the TV is showing, as package/activity when the
// TV reports it that way. It only reads, so it does not wake the TV, but a TV in
// standby may not answer: read State first.
func (c *Client) ForegroundActivity(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	readCtx, cancel := context.WithTimeout(ctx, c.powerStateTimeout)
	defer cancel()

	if err := c.ready(readCtx); err != nil {
		return "", err
	}
	focus, err := c.focus(readCtx)
	if err != nil {
		return "", err
	}
	return activityName(focus), nil
}

var activityRe = regexp.MustCompile(`[\w.]+/[\w.$]+`)

// activityName picks package/activity out of a dumpsys record such as
// "Window{1a u0 com.netflix.ninja/.MainActivity}". A window that belongs to no
// activity has no such part, so the record is returned as it is.
func activityName(focus string) string {
	if name := activityRe.FindString(focus); name != "" {
		return name
	}
	return focus
}

func (c *Client) readPower(ctx context.Context) (bool, error) {
	if err := c.ready(ctx); err != nil {
		return false, err
	}
	return c.isOn(ctx)
}

func (c *Client) ready(ctx context.Context) error {
	if err := c.connect(ctx); err != nil {
		return err
	}
	// adb connect can return before the device leaves the "offline" state.
	_, err := c.adb(ctx, "-s", c.serial, "wait-for-device")
	return err
}

func (c *Client) powerOffOnce(ctx context.Context, checkFocus bool) (Outcome, error) {
	if err := c.ready(ctx); err != nil {
		return "", err
	}

	on, err := c.isOn(ctx)
	if err != nil {
		return "", err
	}
	if !on {
		return OutcomeAlreadyOff, nil
	}

	if checkFocus {
		focus, err := c.focus(ctx)
		switch {
		case err != nil && c.requireFocus != nil:
			// Without knowing the foreground app we could cut off someone's movie.
			return "", err
		case err != nil:
			slog.Warn("could not read tv foreground app", "err", err)
		default:
			slog.Info("tv foreground app", "focus", focus)
		}
		if c.requireFocus != nil && !c.requireFocus.MatchString(focus) {
			return OutcomeFocusMismatch, nil
		}
	}

	if c.dryRun {
		return OutcomeDryRun, nil
	}
	if _, err := c.adb(ctx, "-s", c.serial, "shell", "input", "keyevent", "KEYCODE_SLEEP"); err != nil {
		return "", err
	}
	c.sleepSentAt = time.Now()
	return OutcomeSleepSent, nil
}

func (c *Client) connect(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, c.connectTimeout)
	defer cancel()
	out, err := c.adb(connectCtx, "connect", c.serial)
	if err != nil {
		if ctx.Err() == nil && connectCtx.Err() != nil {
			return fmt.Errorf("%w: %w", errNoAnswer, err)
		}
		return err
	}
	// adb connect exits 0 even when it fails, so the output is the only signal.
	// Matches both "connected to X" and "already connected to X".
	if !strings.Contains(out, "connected to") {
		return fmt.Errorf("adb connect %s: %s", c.serial, strings.TrimSpace(out))
	}
	return nil
}

var wakefulnessRe = regexp.MustCompile(`mWakefulness=(\w+)`)

// isOn counts Dreaming as on: that is a screensaver with the panel still lit.
func (c *Client) isOn(ctx context.Context) (bool, error) {
	out, err := c.adb(ctx, "-s", c.serial, "shell", "dumpsys", "power")
	if err != nil {
		return false, err
	}
	m := wakefulnessRe.FindStringSubmatch(out)
	if m == nil {
		return false, errors.New("mWakefulness not found in dumpsys power output")
	}
	return m[1] == "Awake" || m[1] == "Dreaming", nil
}

var (
	currentFocusRe = regexp.MustCompile(`(?m)^\s*mCurrentFocus=(.+)$`)
	focusedAppRe   = regexp.MustCompile(`(?m)^\s*mFocusedApp=(.+)$`)
)

// focus returns the foreground window; which field is populated varies by vendor.
func (c *Client) focus(ctx context.Context) (string, error) {
	out, err := c.adb(ctx, "-s", c.serial, "shell", "dumpsys", "window")
	if err != nil {
		return "", err
	}
	for _, re := range []*regexp.Regexp{currentFocusRe, focusedAppRe} {
		if m := re.FindStringSubmatch(out); m != nil {
			if v := strings.TrimSpace(m[1]); v != "null" {
				return v, nil
			}
		}
	}
	return "", errors.New("no focused window in dumpsys window output")
}

func (c *Client) adb(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, adbCommandTimeout)
	defer cancel()
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
