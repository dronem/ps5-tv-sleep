package config

import (
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"PS5_HOST": "a", "TV_HOST": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PS5Host != "a" || cfg.TVHost != "b" {
		t.Errorf("hosts = %q, %q", cfg.PS5Host, cfg.TVHost)
	}
	if cfg.TVPort != "5555" || cfg.PollInterval != 5*time.Second || cfg.StandbyConfirm != 2 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.DryRun || cfg.RequireFocus != nil || cfg.TelegramToken != "" {
		t.Errorf("optional features must be off by default: %+v", cfg)
	}
	if !cfg.NotifyPS5 || !cfg.NotifyTV || !cfg.TVButton {
		t.Errorf("telegram features must be on by default: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PS5_HOST":            "a",
		"TV_HOST":             "b",
		"TV_PORT":             "5556",
		"POLL_INTERVAL":       "1m",
		"STANDBY_CONFIRM":     "3",
		"TV_REQUIRE_FOCUS":    "hdmi",
		"DRY_RUN":             "true",
		"TELEGRAM_BOT_TOKEN":  "token",
		"TELEGRAM_CHAT_ID":    "-100500",
		"TELEGRAM_NOTIFY_PS5": "false",
		"TELEGRAM_NOTIFY_TV":  "false",
		"TELEGRAM_TV_BUTTON":  "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TVPort != "5556" || cfg.PollInterval != time.Minute || cfg.StandbyConfirm != 3 || !cfg.DryRun {
		t.Errorf("unexpected values: %+v", cfg)
	}
	if cfg.RequireFocus == nil || !cfg.RequireFocus.MatchString("hdmi") {
		t.Errorf("RequireFocus = %v", cfg.RequireFocus)
	}
	if cfg.TelegramToken != "token" || cfg.TelegramChatID != "-100500" {
		t.Errorf("telegram = %q, %q", cfg.TelegramToken, cfg.TelegramChatID)
	}
	if cfg.NotifyPS5 || cfg.NotifyTV || cfg.TVButton {
		t.Errorf("telegram features must turn off: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		key  string // the variable to set on top of a valid config
		val  string
		want string // what the error must name
	}{
		{"port is not a number", "TV_PORT", "adb", "TV_PORT"},
		{"port is zero", "TV_PORT", "0", "TV_PORT"},
		{"port is too large", "TV_PORT", "65536", "TV_PORT"},
		{"interval has no unit", "POLL_INTERVAL", "5", "POLL_INTERVAL"},
		{"interval is negative", "POLL_INTERVAL", "-1s", "POLL_INTERVAL"},
		{"confirm is zero", "STANDBY_CONFIRM", "0", "STANDBY_CONFIRM"},
		{"focus is not a regexp", "TV_REQUIRE_FOCUS", "(", "TV_REQUIRE_FOCUS"},
		{"dry run is not a bool", "DRY_RUN", "maybe", "DRY_RUN"},
		{"notify ps5 is not a bool", "TELEGRAM_NOTIFY_PS5", "maybe", "TELEGRAM_NOTIFY_PS5"},
		{"notify tv is not a bool", "TELEGRAM_NOTIFY_TV", "maybe", "TELEGRAM_NOTIFY_TV"},
		{"tv button is not a bool", "TELEGRAM_TV_BUTTON", "maybe", "TELEGRAM_TV_BUTTON"},
		{"token without chat", "TELEGRAM_BOT_TOKEN", "t", "must be set together"},
		{"chat without token", "TELEGRAM_CHAT_ID", "1", "must be set together"},
		{"chat is a name", "TELEGRAM_CHAT_ID", "@channel", "TELEGRAM_CHAT_ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(map[string]string{"PS5_HOST": "a", "TV_HOST": "b", tt.key: tt.val}))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{"POLL_INTERVAL": "soon"}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"PS5_HOST", "TV_HOST", "POLL_INTERVAL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %s", err, want)
		}
	}
}

// Configuration errors end up in logs.
func TestLoadDoesNotLeakToken(t *testing.T) {
	_, err := Load(env(map[string]string{"PS5_HOST": "a", "TV_HOST": "b", "TELEGRAM_BOT_TOKEN": "SECRET"}))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("token leaked: %v", err)
	}
}
