package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "123456:SECRET"

func newTestClient(url string) *Client {
	c := New(testToken, "-100500", "Button")
	c.baseURL = url
	c.retryDelay = time.Millisecond
	c.pollRetryDelay = time.Millisecond
	return c
}

func TestSendRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/bot" + testToken + "/sendMessage"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		var got struct {
			ChatID      string `json:"chat_id"`
			Text        string `json:"text"`
			ReplyMarkup struct {
				Keyboard [][]struct {
					Text string `json:"text"`
				} `json:"keyboard"`
				IsPersistent bool `json:"is_persistent"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got.ChatID != "-100500" || got.Text != "hello" {
			t.Errorf("payload = %+v", got)
		}
		kb := got.ReplyMarkup.Keyboard
		if len(kb) != 1 || len(kb[0]) != 1 || kb[0][0].Text != "Button" || !got.ReplyMarkup.IsPersistent {
			t.Errorf("reply_markup = %+v", got.ReplyMarkup)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if err := newTestClient(srv.URL).Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
}

// statusServer replies with the given statuses in order, repeating the last one.
func statusServer(t *testing.T, statuses ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(calls.Add(1)) - 1
		w.WriteHeader(statuses[min(i, len(statuses)-1)])
		w.Write([]byte(`{"ok":false,"description":"reason from telegram"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestSendRetries(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		wantErr   bool
		wantCalls int32
	}{
		{"recovers after server error", []int{500, 200}, false, 2},
		{"recovers after rate limit", []int{429, 200}, false, 2},
		{"gives up after all attempts", []int{503}, true, 3},
		{"bad token is not retried", []int{401}, true, 1},
		{"bad chat is not retried", []int{400}, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, calls := statusServer(t, tt.statuses...)
			err := newTestClient(srv.URL).Send(context.Background(), "x")
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "reason from telegram") {
				t.Errorf("err = %v, want telegram's description", err)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestErrorsDoNotLeakToken(t *testing.T) {
	srv, _ := statusServer(t, 401)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	for name, url := range map[string]string{"api error": srv.URL, "network error": closedURL, "invalid url": "http://bad host"} {
		t.Run(name, func(t *testing.T) {
			err := newTestClient(url).Send(context.Background(), "x")
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("token leaked: %v", err)
			}
		})
	}
}

func TestSendStopsOnCancel(t *testing.T) {
	srv, calls := statusServer(t, 500)
	c := newTestClient(srv.URL)
	c.retryDelay = time.Hour

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, "x"); err == nil {
		t.Fatal("expected error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestSendWithoutButtonOmitsKeyboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if _, ok := got["reply_markup"]; ok {
			t.Errorf("payload = %v, want no reply_markup", got)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.buttons = nil
	if err := c.Send(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestSendPutsButtonsInOneRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got struct {
			ReplyMarkup struct {
				Keyboard [][]struct {
					Text string `json:"text"`
				} `json:"keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		kb := got.ReplyMarkup.Keyboard
		if len(kb) != 1 || len(kb[0]) != 2 || kb[0][0].Text != "one" || kb[0][1].Text != "two" {
			t.Errorf("keyboard = %+v", kb)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	c.buttons = []string{"one", "two"}
	if err := c.Send(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}
