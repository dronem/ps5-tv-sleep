// Package telegram talks to one chat through the Telegram Bot API.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultBaseURL = "https://api.telegram.org"
	// pollTimeoutSeconds is how long Telegram holds a getUpdates request open.
	pollTimeoutSeconds = 30
)

// Client exchanges messages between one bot and one chat.
type Client struct {
	token   string
	chatID  string
	buttons []string
	baseURL string
	http    *http.Client
	// pollHTTP outlives the long poll, which http's timeout would cut short.
	pollHTTP *http.Client

	attempts       int
	retryDelay     time.Duration
	pollRetryDelay time.Duration
	maxMessageAge  time.Duration
}

// New returns a client whose messages show buttons as permanent keys in one row
// of the chat keyboard.
func New(token, chatID string, buttons ...string) *Client {
	return &Client{
		token:          token,
		chatID:         chatID,
		buttons:        buttons,
		baseURL:        defaultBaseURL,
		http:           &http.Client{Timeout: 10 * time.Second},
		pollHTTP:       &http.Client{Timeout: (pollTimeoutSeconds + 10) * time.Second},
		attempts:       3,
		retryDelay:     5 * time.Second,
		pollRetryDelay: 10 * time.Second,
		maxMessageAge:  time.Minute,
	}
}

type sendMessageRequest struct {
	ChatID      string         `json:"chat_id"`
	Text        string         `json:"text"`
	ReplyMarkup *replyKeyboard `json:"reply_markup,omitempty"`
}

type replyKeyboard struct {
	Keyboard       [][]keyboardButton `json:"keyboard"`
	ResizeKeyboard bool               `json:"resize_keyboard"`
	IsPersistent   bool               `json:"is_persistent"`
}

type keyboardButton struct {
	Text string `json:"text"`
}

// keyboard returns the chat keyboard, or nil if there are no buttons.
func (c *Client) keyboard() *replyKeyboard {
	if len(c.buttons) == 0 {
		return nil
	}
	row := make([]keyboardButton, 0, len(c.buttons))
	for _, b := range c.buttons {
		row = append(row, keyboardButton{Text: b})
	}
	return &replyKeyboard{
		Keyboard:       [][]keyboardButton{row},
		ResizeKeyboard: true,
		IsPersistent:   true,
	}
}

// Send delivers text, retrying failures that may be temporary.
func (c *Client) Send(ctx context.Context, text string) error {
	payload := sendMessageRequest{ChatID: c.chatID, Text: text, ReplyMarkup: c.keyboard()}

	var err error
	for attempt := range c.attempts {
		if attempt > 0 && !wait(ctx, c.retryDelay) {
			return ctx.Err()
		}
		var retry bool
		retry, err = c.call(ctx, c.http, "sendMessage", payload, nil)
		if err == nil || !retry {
			return err
		}
	}
	return err
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Date int64  `json:"date"`
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

// Listen calls handle for each new message from the configured chat until ctx
// is cancelled. Anyone can message a bot, so other chats are ignored. So are
// old messages: Telegram keeps them while the service is down, and acting on a
// stale request could switch off a TV someone is watching now.
func (c *Client) Listen(ctx context.Context, handle func(ctx context.Context, text string)) {
	var offset int64
	for ctx.Err() == nil {
		var updates []update
		_, err := c.call(ctx, c.pollHTTP, "getUpdates", getUpdatesRequest{
			Offset:         offset,
			Timeout:        pollTimeoutSeconds,
			AllowedUpdates: []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram poll failed", "err", err)
			if !wait(ctx, c.pollRetryDelay) {
				return
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if c.accepts(u) {
				handle(ctx, u.Message.Text)
			}
		}
	}
}

// accepts reports whether an update is a fresh message from the configured chat.
func (c *Client) accepts(u update) bool {
	m := u.Message
	if m == nil {
		return false
	}
	if chat := strconv.FormatInt(m.Chat.ID, 10); chat != c.chatID {
		slog.Warn("ignored telegram message from another chat", "chat", chat)
		return false
	}
	if age := time.Since(time.Unix(m.Date, 0)); age > c.maxMessageAge {
		slog.Info("ignored old telegram message", "age", age.Round(time.Second))
		return false
	}
	return true
}

// call invokes an API method. If result is not nil, it receives the method's result.
func (c *Client) call(ctx context.Context, client *http.Client, method string, payload, result any) (retry bool, err error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return false, scrubURL(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return true, scrubURL(err)
	}
	defer resp.Body.Close()

	var reply struct {
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&reply)

	if resp.StatusCode != http.StatusOK {
		// The description is best effort: an error page may not be JSON.
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return retry, fmt.Errorf("telegram: status %d: %s", resp.StatusCode, reply.Description)
	}
	if result == nil {
		return false, nil
	}
	if decodeErr != nil {
		return true, fmt.Errorf("telegram: decode reply: %w", decodeErr)
	}
	if err := json.Unmarshal(reply.Result, result); err != nil {
		return true, fmt.Errorf("telegram: decode result: %w", err)
	}
	return false, nil
}

// scrubURL drops the request URL from net/http errors: it contains the bot
// token, and these errors end up in logs.
func scrubURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("telegram: %w", urlErr.Err)
	}
	return err
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
