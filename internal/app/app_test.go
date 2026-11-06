package app

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dronem/ps5-tv-sleep/internal/config"
	"github.com/dronem/ps5-tv-sleep/internal/ps5"
	"github.com/dronem/ps5-tv-sleep/internal/tv"
)

const (
	replyOn      = "HTTP/1.1 200 Ok\n"
	replyStandby = "HTTP/1.1 620 Server Standby\n"

	testActivity = "com.netflix.ninja/.MainActivity"
)

// fakeTV is an adb runner for a TV that is on until it receives the sleep key.
type fakeTV struct {
	asleep atomic.Bool
	reads  atomic.Int32 // power state reads answered
	failOn string       // fail commands that contain this
}

func (f *fakeTV) run(_ context.Context, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	switch {
	case f.failOn != "" && strings.Contains(cmd, f.failOn):
		return "", errors.New("adb failed")
	case strings.HasPrefix(cmd, "connect"):
		return "connected to tv", nil
	case strings.HasSuffix(cmd, "dumpsys power"):
		f.reads.Add(1)
		if f.asleep.Load() {
			return "mWakefulness=Asleep", nil
		}
		return "mWakefulness=Awake", nil
	case strings.HasSuffix(cmd, "dumpsys window"):
		return "  mCurrentFocus=Window{1a u0 " + testActivity + "}\n", nil
	case strings.HasSuffix(cmd, "KEYCODE_SLEEP"):
		f.asleep.Store(true)
	}
	return "", nil
}

func (f *fakeTV) sleepSent() bool { return f.asleep.Load() }

func newTVClient(f *fakeTV) *tv.Client {
	return tv.NewClient(tv.ClientConfig{Serial: "tv"}, f.run)
}

// fakeChat records what the service sends and blocks in Listen like the real one.
type fakeChat struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeChat) Send(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeChat) Listen(ctx context.Context, _ func(context.Context, string)) {
	<-ctx.Done()
}

func (f *fakeChat) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// fakePS5 answers DDP searches on a local UDP socket with its current reply.
type fakePS5 struct {
	reply  atomic.Value
	probes atomic.Int32 // searches answered
	addr   string
}

func newFakePS5(t *testing.T, reply string) *fakePS5 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	f := &fakePS5{addr: conn.LocalAddr().String()}
	f.reply.Store(reply)
	go func() {
		buf := make([]byte, 2048)
		for {
			_, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo([]byte(f.reply.Load().(string)), from)
			f.probes.Add(1)
		}
	}()
	return f
}

// newTestApp returns an App wired to the fakes. A nil chat leaves Telegram off.
func newTestApp(console *fakePS5, screen *fakeTV, chat messenger, cfg config.Config) *App {
	cfg.PS5Host, cfg.TVHost, cfg.TVPort = "ps5", "tv", "5555"
	cfg.PollInterval = 5 * time.Millisecond
	cfg.StandbyConfirm = 2

	a := New(cfg, screen.run)
	a.prober.Addr = console.addr
	a.prober.Timeout = 200 * time.Millisecond
	a.chat = chat
	a.tvPollInterval = 5 * time.Millisecond
	return a
}

// start runs the App in the background. The returned function stops it and fails
// the test if it does not shut down cleanly.
func start(t *testing.T, a *App) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run = %v, want nil on a normal stop", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitProbes waits for n more searches. Polling is sequential, so by the time a
// search arrives the service has fully acted on the reply before it.
func awaitProbes(t *testing.T, console *fakePS5, n int32) {
	t.Helper()
	target := console.probes.Load() + n
	waitFor(t, "the service to poll the ps5", func() bool { return console.probes.Load() >= target })
}

func TestRunPowersOffTVWhenPS5Rests(t *testing.T) {
	console := newFakePS5(t, replyOn)
	screen := &fakeTV{}
	chat := &fakeChat{}
	a := newTestApp(console, screen, chat, config.Config{NotifyPS5: true, NotifyTV: true, TVButton: true})
	stop := start(t, a)

	// Let the service see both devices on: that arms the power-off, and a TV must
	// be seen on before it can be reported as turned off. The second read proves
	// the first was acted on.
	awaitProbes(t, console, 2)
	waitFor(t, "the service to poll the tv", func() bool { return screen.reads.Load() >= 2 })
	console.reply.Store(replyStandby)

	waitFor(t, "the sleep key", screen.sleepSent)
	waitFor(t, "both messages", func() bool {
		got := chat.messages()
		return slices.Contains(got, restMessage) && slices.Contains(got, tvOffMessage)
	})
	stop()
}

func TestRunStartedDuringRestLeavesTVAlone(t *testing.T) {
	console := newFakePS5(t, replyStandby)
	screen := &fakeTV{}
	chat := &fakeChat{}
	a := newTestApp(console, screen, chat, config.Config{NotifyPS5: true, NotifyTV: true})
	stop := start(t, a)

	awaitProbes(t, console, 5) // more than the standby replies it takes to act
	stop()

	if screen.sleepSent() {
		t.Error("a TV someone is watching must not be switched off at startup")
	}
	if got := chat.messages(); len(got) != 0 {
		t.Errorf("sent %q, want silence", got)
	}
}

func TestRunWithoutTelegramStillPowersOffTV(t *testing.T) {
	console := newFakePS5(t, replyOn)
	screen := &fakeTV{}
	a := newTestApp(console, screen, nil, config.Config{NotifyPS5: true, NotifyTV: true})
	stop := start(t, a)

	awaitProbes(t, console, 2)
	console.reply.Store(replyStandby)

	waitFor(t, "the sleep key", screen.sleepSent)
	stop()
}

func TestRunRespectsNotificationSettings(t *testing.T) {
	console := newFakePS5(t, replyOn)
	screen := &fakeTV{}
	chat := &fakeChat{}
	a := newTestApp(console, screen, chat, config.Config{NotifyPS5: false, NotifyTV: false})
	stop := start(t, a)

	awaitProbes(t, console, 2)
	console.reply.Store(replyStandby)

	waitFor(t, "the sleep key", screen.sleepSent)
	stop()

	if got := chat.messages(); len(got) != 0 {
		t.Errorf("sent %q, want silence", got)
	}
	if got := screen.reads.Load(); got != 1 {
		t.Errorf("tv power read %d times, want only the one before the sleep key: nobody asked to watch the tv", got)
	}
}

func TestRunFailsWithoutADB(t *testing.T) {
	a := newTestApp(newFakePS5(t, replyOn), &fakeTV{failOn: "start-server"}, nil, config.Config{})

	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "start adb server") {
		t.Errorf("err = %v, want it to mention the adb server", err)
	}
}

func TestRunStoppedDuringStartupIsNotAnError(t *testing.T) {
	a := newTestApp(newFakePS5(t, replyOn), &fakeTV{failOn: "start-server"}, nil, config.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.Run(ctx); err != nil {
		t.Errorf("Run = %v, want nil on a normal stop", err)
	}
}

func TestNewEnablesTelegramOnlyWithToken(t *testing.T) {
	off := New(config.Config{PS5Host: "a", TVHost: "b", TVPort: "5555"}, (&fakeTV{}).run)
	if off.chat != nil {
		t.Error("chat must be nil without a token: a typed nil would pass the nil checks")
	}
	on := New(config.Config{PS5Host: "a", TVHost: "b", TVPort: "5555", TelegramToken: "t", TelegramChatID: "1"}, (&fakeTV{}).run)
	if on.chat == nil {
		t.Error("chat must be set with a token")
	}
	if want := "a:9302"; off.prober.Addr != want {
		t.Errorf("ps5 address = %q, want %q", off.prober.Addr, want)
	}
}

func TestHandlePS5Event(t *testing.T) {
	tests := []struct {
		name      string
		event     ps5.Event
		wantText  []string
		wantSleep bool
	}{
		{"nothing happened", ps5.EventNone, nil, false},
		{"power-on leaves the tv alone", ps5.EventPoweredOn, []string{powerOnMessage}, false},
		{"rest mode powers the tv off", ps5.EventEnteredRest, []string{restMessage}, true},
		{"switched off powers the tv off", ps5.EventWentOffline, []string{offlineMessage}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := &fakeTV{}
			var sent []string
			notify := func(text string) { sent = append(sent, text) }

			handlePS5Event(context.Background(), tt.event, newTVClient(screen), notify)

			if !slices.Equal(sent, tt.wantText) {
				t.Errorf("notified %v, want %v", sent, tt.wantText)
			}
			if got := screen.sleepSent(); got != tt.wantSleep {
				t.Errorf("sleep key sent = %v, want %v", got, tt.wantSleep)
			}
		})
	}
}
