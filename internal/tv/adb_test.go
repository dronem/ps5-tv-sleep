package tv

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testSerial = "tv:5555"

// fakeADB scripts adb replies by the joined command line (without "-s serial").
type fakeADB struct {
	replies map[string]func() (string, error)
	calls   []string
}

func (f *fakeADB) run(_ context.Context, args ...string) (string, error) {
	if len(args) > 2 && args[0] == "-s" {
		args = args[2:]
	}
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if reply, ok := f.replies[cmd]; ok {
		return reply()
	}
	return "", nil
}

func (f *fakeADB) sentSleep() bool {
	return slices.Contains(f.calls, cmdSleep)
}

const (
	cmdConnect = "connect " + testSerial
	cmdPower   = "shell dumpsys power"
	cmdWindow  = "shell dumpsys window"
	cmdSleep   = "shell input keyevent KEYCODE_SLEEP"
)

func text(s string) func() (string, error) {
	return func() (string, error) { return s, nil }
}

func newFake(wakefulness, window string) *fakeADB {
	return &fakeADB{replies: map[string]func() (string, error){
		cmdConnect: text("connected to " + testSerial + "\n"),
		cmdPower:   text("  mWakefulness=" + wakefulness + "\r\n  mWakefulnessChanging=false\r\n"),
		cmdWindow:  text(window),
	}}
}

func newTestClient(f *fakeADB, requireFocus string, dryRun bool) *Client {
	var re *regexp.Regexp
	if requireFocus != "" {
		re = regexp.MustCompile(requireFocus)
	}
	tv := NewClient(ClientConfig{Serial: testSerial, RequireFocus: re, DryRun: dryRun}, f.run)
	tv.powerOffDelay = time.Millisecond
	tv.checkDelay = time.Millisecond
	return tv
}

const hdmiWindow = "  mCurrentFocus=Window{1a u0 com.vendor.tv/com.vendor.tv.HdmiActivity}\r\n"

func TestPowerOff(t *testing.T) {
	tests := []struct {
		name         string
		wakefulness  string
		window       string
		requireFocus string
		dryRun       bool
		want         Outcome
		wantSleep    bool
	}{
		{name: "awake", wakefulness: "Awake", window: hdmiWindow, want: OutcomeSleepSent, wantSleep: true},
		{name: "screensaver", wakefulness: "Dreaming", window: hdmiWindow, want: OutcomeSleepSent, wantSleep: true},
		{name: "asleep", wakefulness: "Asleep", window: hdmiWindow, want: OutcomeAlreadyOff},
		{name: "dozing", wakefulness: "Dozing", window: hdmiWindow, want: OutcomeAlreadyOff},
		{name: "focus matches", wakefulness: "Awake", window: hdmiWindow, requireFocus: "HdmiActivity", want: OutcomeSleepSent, wantSleep: true},
		{name: "watching netflix", wakefulness: "Awake", window: "  mCurrentFocus=Window{1a u0 com.netflix.ninja/.MainActivity}\n", requireFocus: "HdmiActivity", want: OutcomeFocusMismatch},
		{name: "focus falls back to app", wakefulness: "Awake", window: "  mCurrentFocus=null\n  mFocusedApp=ActivityRecord{1a u0 com.vendor.tv/.HdmiActivity t1}\n", requireFocus: "HdmiActivity", want: OutcomeSleepSent, wantSleep: true},
		{name: "dry run", wakefulness: "Awake", window: hdmiWindow, dryRun: true, want: OutcomeDryRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(tt.wakefulness, tt.window)
			got, err := newTestClient(f, tt.requireFocus, tt.dryRun).PowerOff(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("outcome = %q, want %q", got, tt.want)
			}
			if f.sentSleep() != tt.wantSleep {
				t.Errorf("sleep sent = %v, want %v; calls: %v", f.sentSleep(), tt.wantSleep, f.calls)
			}
		})
	}
}

func TestPowerOffFailedConnectExitsZero(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	f.replies[cmdConnect] = text("failed to connect to " + testSerial + "\n")
	tv := newTestClient(f, "", false)

	if _, err := tv.PowerOff(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if f.sentSleep() {
		t.Error("must not send sleep without a connection")
	}
	if got := connectCalls(f); got != tv.powerOffAttempts {
		t.Errorf("connect attempts = %d, want %d", got, tv.powerOffAttempts)
	}
}

// A sleeping TV leaves adb connect hanging. If it then woke up, a retry would
// switch off a TV that is just being turned on.
func TestPowerOffDoesNotRetrySilentTV(t *testing.T) {
	connects := 0
	sleepSent := false
	run := func(ctx context.Context, args ...string) (string, error) {
		switch {
		case args[0] == "connect":
			connects++
			if connects == 1 {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "connected to " + testSerial, nil
		case args[len(args)-1] == "dumpsys" || args[len(args)-1] == "power":
			return "mWakefulness=Awake", nil
		case args[len(args)-1] == "KEYCODE_SLEEP":
			sleepSent = true
		}
		return "", nil
	}
	c := NewClient(ClientConfig{Serial: testSerial}, run)
	c.connectTimeout = 20 * time.Millisecond
	c.powerOffDelay = time.Millisecond

	got, err := c.PowerOff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != OutcomeNoAnswer {
		t.Errorf("outcome = %q, want %q", got, OutcomeNoAnswer)
	}
	if connects != 1 || sleepSent {
		t.Errorf("connects = %d, sleep sent = %v: must give up after the first hang", connects, sleepSent)
	}
}

func TestPowerOffRecoversByReconnecting(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	fail := true
	f.replies[cmdPower] = func() (string, error) {
		if fail {
			fail = false
			return "error: device offline", errors.New("exit status 1")
		}
		return "mWakefulness=Awake", nil
	}

	got, err := newTestClient(f, "", false).PowerOff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != OutcomeSleepSent {
		t.Errorf("outcome = %q", got)
	}
	if !slices.Contains(f.calls, "disconnect "+testSerial) {
		t.Errorf("expected a reconnect, calls: %v", f.calls)
	}
}

func TestPowerOffUnknownFocus(t *testing.T) {
	window := "nothing useful\n"

	f := newFake("Awake", window)
	if _, err := newTestClient(f, "HdmiActivity", false).PowerOff(context.Background()); err == nil {
		t.Error("with TV_REQUIRE_FOCUS, an unknown foreground app must be an error")
	}
	if f.sentSleep() {
		t.Error("with TV_REQUIRE_FOCUS, an unknown foreground app must not power off")
	}

	f = newFake("Awake", window)
	if _, err := newTestClient(f, "", false).PowerOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.sentSleep() {
		t.Error("without TV_REQUIRE_FOCUS, focus is informational and must not block")
	}
}

func TestPowerOffStopsOnCancel(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	ctx, cancel := context.WithCancel(context.Background())
	f.replies[cmdConnect] = func() (string, error) {
		cancel()
		return "", context.Canceled
	}

	if _, err := newTestClient(f, "", false).PowerOff(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := connectCalls(f); got != 1 {
		t.Errorf("connect attempts = %d, want 1", got)
	}
}

func TestCheckConnectionKeepsSessionWhileAwaitingPrompt(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	remaining := 3
	f.replies["get-state"] = func() (string, error) {
		if remaining > 0 {
			remaining--
			return "error: device unauthorized.", errors.New("exit status 1")
		}
		return "device\n", nil
	}

	if err := newTestClient(f, "", false).CheckConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.calls, "disconnect "+testSerial) {
		t.Error("disconnecting would dismiss the authorization prompt on the TV")
	}
}

func connectCalls(f *fakeADB) int {
	n := 0
	for _, c := range f.calls {
		if c == cmdConnect {
			n++
		}
	}
	return n
}

func TestPowerOffNowIgnoresFocus(t *testing.T) {
	const netflix = "  mCurrentFocus=Window{1a u0 com.netflix.ninja/.MainActivity}\n"

	f := newFake("Awake", netflix)
	got, err := newTestClient(f, "HdmiActivity", false).PowerOffNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != OutcomeSleepSent || !f.sentSleep() {
		t.Errorf("outcome = %q, sleep sent = %v; a person asked, so focus must not block", got, f.sentSleep())
	}
}

func TestPowerOffNowRespectsDryRunAndOffState(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	if got, err := newTestClient(f, "", true).PowerOffNow(context.Background()); err != nil || got != OutcomeDryRun || f.sentSleep() {
		t.Errorf("dry run: outcome = %q, err = %v, sleep sent = %v", got, err, f.sentSleep())
	}

	f = newFake("Asleep", hdmiWindow)
	if got, err := newTestClient(f, "", false).PowerOffNow(context.Background()); err != nil || got != OutcomeAlreadyOff || f.sentSleep() {
		t.Errorf("asleep: outcome = %q, err = %v, sleep sent = %v", got, err, f.sentSleep())
	}
}

func TestForegroundActivity(t *testing.T) {
	tests := []struct {
		name   string
		window string
		want   string
	}{
		{"current focus", "  mCurrentFocus=Window{1a u0 com.netflix.ninja/.MainActivity}\n", "com.netflix.ninja/.MainActivity"},
		{"full activity name", hdmiWindow, "com.vendor.tv/com.vendor.tv.HdmiActivity"},
		{
			"falls back to the focused app",
			"  mCurrentFocus=null\n  mFocusedApp=ActivityRecord{1a u0 com.vendor.tv/.HdmiActivity t1}\n",
			"com.vendor.tv/.HdmiActivity",
		},
		{"window without an activity", "  mCurrentFocus=Window{1a u0 NotificationShade}\n", "Window{1a u0 NotificationShade}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake("Awake", tt.window)
			got, err := newTestClient(f, "", false).ForegroundActivity(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("activity = %q, want %q", got, tt.want)
			}
			if f.sentSleep() {
				t.Error("reading the activity must not send keys")
			}
		})
	}
}

func TestForegroundActivityFailures(t *testing.T) {
	f := newFake("Awake", "nothing useful\n")
	if _, err := newTestClient(f, "", false).ForegroundActivity(context.Background()); err == nil {
		t.Error("no focused window: expected error")
	}

	f = newFake("Awake", hdmiWindow)
	f.replies[cmdConnect] = text("failed to connect to " + testSerial)
	if _, err := newTestClient(f, "", false).ForegroundActivity(context.Background()); err == nil {
		t.Error("no connection: expected error")
	}
}

func TestStartServer(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	if err := newTestClient(f, "", false).StartServer(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls, "start-server") {
		t.Errorf("calls = %v", f.calls)
	}

	f.replies["start-server"] = func() (string, error) { return "", errors.New("adb: not found") }
	if err := newTestClient(f, "", false).StartServer(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestOperationsAreSerialized(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	var mu sync.Mutex
	var inFlight, overlaps atomic.Int32
	run := func(ctx context.Context, args ...string) (string, error) {
		if inFlight.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer inFlight.Add(-1)
		time.Sleep(time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return f.run(ctx, args...)
	}
	client := NewClient(ClientConfig{Serial: testSerial}, run)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := client.PowerOffNow(context.Background()); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := client.State(context.Background()); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := client.CheckConnection(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := overlaps.Load(); n != 0 {
		t.Errorf("%d adb commands overlapped", n)
	}
}

// A TV closes its adb session as it goes to sleep, as a real one was logged doing.
func TestStateAfterSleepKeyCountsAsOff(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	c := newTestClient(f, "", false)
	if _, err := c.PowerOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.replies[cmdPower] = func() (string, error) {
		return "error: closed", errors.New("exit status 1")
	}

	if got, _ := c.State(context.Background()); got != Off {
		t.Errorf("right after the sleep key: state = %v, want off", got)
	}

	c.sleepSentAt = time.Now().Add(-2 * sleepSettle)
	if got, _ := c.State(context.Background()); got != Unreachable {
		t.Errorf("long after the sleep key: state = %v, want unreachable", got)
	}
}

func TestState(t *testing.T) {
	tests := []struct {
		wakefulness string
		want        State
	}{
		{"Awake", On},
		{"Dreaming", On},
		{"Asleep", Off},
		{"Dozing", Off},
	}
	for _, tt := range tests {
		t.Run(tt.wakefulness, func(t *testing.T) {
			f := newFake(tt.wakefulness, hdmiWindow)
			got, err := newTestClient(f, "", false).State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("state = %v, want %v", got, tt.want)
			}
			if f.sentSleep() {
				t.Error("reading the state must not send keys")
			}
		})
	}
}

func TestStateFailureReconnectsNextTime(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	f.replies[cmdPower] = func() (string, error) {
		return "error: closed", errors.New("exit status 1")
	}

	got, err := newTestClient(f, "", false).State(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if got != Unreachable {
		t.Errorf("state = %v, want unreachable", got)
	}
	if !slices.Contains(f.calls, "disconnect "+testSerial) {
		t.Errorf("a stale session must be dropped, calls: %v", f.calls)
	}
}

func TestStateKeepsSessionAwaitingApproval(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		reply func() (string, error)
	}{
		{"device unauthorized", "wait-for-device", func() (string, error) {
			return "error: device unauthorized.", errors.New("exit status 1")
		}},
		{"connect exits zero", cmdConnect, text("failed to authenticate to " + testSerial)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake("Awake", hdmiWindow)
			f.replies[tt.cmd] = tt.reply

			got, err := newTestClient(f, "", false).State(context.Background())
			if err == nil || got != Unreachable {
				t.Errorf("state = %v, err = %v", got, err)
			}
			if slices.Contains(f.calls, "disconnect "+testSerial) {
				t.Error("disconnecting would dismiss the authorization prompt on the TV")
			}
		})
	}
}

// A TV in standby leaves adb connect hanging until its context expires.
func TestStateGivesUpOnSilentTV(t *testing.T) {
	var calls []string
	run := func(ctx context.Context, args ...string) (string, error) {
		calls = append(calls, args[0])
		if args[0] == "disconnect" {
			return "", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	c := NewClient(ClientConfig{Serial: testSerial}, run)
	c.powerStateTimeout = 20 * time.Millisecond

	start := time.Now()
	got, err := c.State(context.Background())
	if err == nil || got != Unreachable {
		t.Errorf("state = %v, err = %v", got, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v: a poll must not wait for the full adb timeout", took)
	}
	if !slices.Contains(calls, "disconnect") {
		t.Errorf("calls = %v: disconnect must run even though the read timed out", calls)
	}
}

func TestCheckConnectionDoesNotWaitForSilentTV(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	f.replies[cmdConnect] = text("failed to connect to " + testSerial)

	if err := newTestClient(f, "", false).CheckConnection(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if got := connectCalls(f); got != 1 {
		t.Errorf("connect attempts = %d, want 1", got)
	}
}

func TestCheckConnectionWaitsWhileApprovalPending(t *testing.T) {
	f := newFake("Awake", hdmiWindow)
	remaining := 2
	f.replies[cmdConnect] = func() (string, error) {
		if remaining > 0 {
			remaining--
			return "failed to authenticate to " + testSerial, nil
		}
		return "connected to " + testSerial, nil
	}

	if err := newTestClient(f, "", false).CheckConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := connectCalls(f); got != 3 {
		t.Errorf("connect attempts = %d, want 3", got)
	}
}
