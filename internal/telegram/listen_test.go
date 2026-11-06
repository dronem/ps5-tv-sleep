package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// pollServer serves each batch of updates to one getUpdates call, then cancels
// the listener. A nil batch answers 500. It records the offset of every call.
func pollServer(t *testing.T, cancel context.CancelFunc, batches ...[]string) (*httptest.Server, *[]int64) {
	t.Helper()
	offsets := &[]int64{}
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Errorf("path = %q", r.URL.Path)
		}
		var req struct {
			Offset  int64 `json:"offset"`
			Timeout int   `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Timeout != pollTimeoutSeconds {
			t.Errorf("timeout = %d", req.Timeout)
		}
		*offsets = append(*offsets, req.Offset)

		if call >= len(batches) {
			cancel()
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		batch := batches[call]
		call++
		if batch == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(batch, ","))
	}))
	t.Cleanup(srv.Close)
	return srv, offsets
}

func message(updateID, chatID int64, date time.Time, text string) string {
	return fmt.Sprintf(`{"update_id":%d,"message":{"date":%d,"text":%q,"chat":{"id":%d}}}`,
		updateID, date.Unix(), text, chatID)
}

func listen(t *testing.T, ctx context.Context, url string) []string {
	t.Helper()
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		newTestClient(url).Listen(ctx, func(_ context.Context, text string) {
			got = append(got, text)
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not stop after cancel")
	}
	return got
}

func TestListenFiltersMessages(t *testing.T) {
	const chat = -100500
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, offsets := pollServer(t, cancel, []string{
		message(10, chat, now, "first"),
		message(11, 777, now, "from a stranger"),
		message(12, chat, now.Add(-time.Hour), "pressed while the service was down"),
		`{"update_id":13}`,
		message(14, chat, now, "second"),
	})

	got := listen(t, ctx, srv.URL)
	if want := []string{"first", "second"}; !slices.Equal(got, want) {
		t.Errorf("handled = %q, want %q", got, want)
	}
	if want := []int64{0, 15}; !slices.Equal(*offsets, want) {
		t.Errorf("offsets = %v, want %v: every update must be confirmed, handled or not", *offsets, want)
	}
}

func TestListenSurvivesServerError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv, offsets := pollServer(t, cancel, nil, []string{message(5, -100500, time.Now(), "after the error")})

	got := listen(t, ctx, srv.URL)
	if want := []string{"after the error"}; !slices.Equal(got, want) {
		t.Errorf("handled = %q, want %q", got, want)
	}
	if want := []int64{0, 0, 6}; !slices.Equal(*offsets, want) {
		t.Errorf("offsets = %v, want %v", *offsets, want)
	}
}
