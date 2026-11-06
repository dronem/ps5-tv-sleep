// Package config reads the service settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Config holds the validated service settings. The README documents each one.
type Config struct {
	PS5Host        string
	TVHost         string
	TVPort         string
	PollInterval   time.Duration
	StandbyConfirm int
	RequireFocus   *regexp.Regexp // nil means any foreground app
	DryRun         bool

	TelegramToken  string // empty means Telegram is off
	TelegramChatID string
	NotifyPS5      bool
	NotifyTV       bool
	TVButton       bool
}

// Load builds a Config from getenv, usually os.Getenv. The error lists every
// invalid setting, so they can all be fixed in one go.
func Load(getenv func(string) string) (Config, error) {
	env := environment{get: getenv}
	cfg := Config{
		PS5Host:        env.required("PS5_HOST"),
		TVHost:         env.required("TV_HOST"),
		TVPort:         read(&env, "TV_PORT", "5555", parsePort),
		PollInterval:   read(&env, "POLL_INTERVAL", 5*time.Second, parseInterval),
		StandbyConfirm: read(&env, "STANDBY_CONFIRM", 2, parseCount),
		RequireFocus:   read(&env, "TV_REQUIRE_FOCUS", nil, regexp.Compile),
		DryRun:         read(&env, "DRY_RUN", false, parseBool),
		TelegramToken:  getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID: read(&env, "TELEGRAM_CHAT_ID", "", parseChatID),
		NotifyPS5:      read(&env, "TELEGRAM_NOTIFY_PS5", true, parseBool),
		NotifyTV:       read(&env, "TELEGRAM_NOTIFY_TV", true, parseBool),
		TVButton:       read(&env, "TELEGRAM_TV_BUTTON", true, parseBool),
	}
	if (getenv("TELEGRAM_BOT_TOKEN") == "") != (getenv("TELEGRAM_CHAT_ID") == "") {
		env.errs = append(env.errs, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together"))
	}
	if err := errors.Join(env.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// environment reads variables and collects what is wrong with them.
type environment struct {
	get  func(string) string
	errs []error
}

func (e *environment) required(name string) string {
	v := e.get(name)
	if v == "" {
		e.errs = append(e.errs, fmt.Errorf("%s: required", name))
	}
	return v
}

// read parses a variable, using def when it is not set.
func read[T any](e *environment, name string, def T, parse func(string) (T, error)) T {
	v := e.get(name)
	if v == "" {
		return def
	}
	parsed, err := parse(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", name, err))
		return def
	}
	return parsed
}

func parseBool(v string) (bool, error) {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("must be true or false, got %q", v)
	}
	return b, nil
}

func parseInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("must be a positive duration such as 5s, got %q", v)
	}
	return d, nil
}

func parseCount(v string) (int, error) {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("must be a positive integer, got %q", v)
	}
	return n, nil
}

func parsePort(v string) (string, error) {
	if _, err := strconv.ParseUint(v, 10, 16); err != nil || v == "0" {
		return "", fmt.Errorf("must be a port number, got %q", v)
	}
	return v, nil
}

// parseChatID insists on a number: updates identify their chat by number, so
// with a name such as @channel every command would be ignored as a stranger's.
func parseChatID(v string) (string, error) {
	if _, err := strconv.ParseInt(v, 10, 64); err != nil {
		return "", fmt.Errorf("must be a numeric chat id, got %q", v)
	}
	return v, nil
}
