package moderator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// probeKey is the API key TestConnection is expected to send.
const probeKey = "probe-key"

// probeAPI is a minimal OpenAI-compatible endpoint for TestConnection:
// it records the request's model and answers with a fixed completion.
// wantKey is the API key the request is expected to carry.
func probeAPI(t *testing.T, status int, wantKey string, gotModel *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if gotModel != nil {
			*gotModel = req.Model
		}
		wantAuth := "Bearer " + wantKey
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Errorf("auth header mismatch")
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
			return
		}
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: "ok"}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTestConnectionSuccess(t *testing.T) {
	var model string
	srv := probeAPI(t, http.StatusOK, probeKey, &model)
	latency, err := TestConnection(context.Background(), Config{
		BaseURL: srv.URL, APIKey: probeKey, Model: "model-x", Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if latency < 0 {
		t.Fatalf("latency = %v", latency)
	}
	if model != "model-x" {
		t.Fatalf("endpoint saw model %q, want model-x", model)
	}
}

func TestTestConnectionFailures(t *testing.T) {
	srv := probeAPI(t, http.StatusUnauthorized, "bad-key", nil)
	if _, err := TestConnection(context.Background(), Config{
		BaseURL: srv.URL, APIKey: "bad-key", Model: "model-x", Timeout: 5 * time.Second,
	}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 endpoint: err = %v", err)
	}

	for _, cfg := range []Config{
		{APIKey: "k", Model: "m"},
		{BaseURL: "http://x", Model: "m"},
		{BaseURL: "http://x", APIKey: "k"},
	} {
		if _, err := TestConnection(context.Background(), cfg); err == nil {
			t.Fatalf("incomplete config %+v: want error", cfg)
		}
	}
}

// TestSweepWritesModerationLog: a sweep's verdicts land in the
// store's audit log with the right label/action pairs, including the
// dry-run wording.
func TestSweepWritesModerationLog(t *testing.T) {
	label := func(title string) string {
		switch {
		case strings.Contains(title, "Adult"):
			return "adult"
		case strings.Contains(title, "Spam"):
			return "spam"
		}
		return "ok"
	}

	st := newStore(t)
	seed(t, st, "Clean Movie", "Adult Thing", "Spam Bait")
	m := newMod(t, st, fakeAPI(t, label, nil).URL, nil)
	if _, err := m.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RecentModeration(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("log rows = %d, want 3: %+v", len(recs), recs)
	}
	byName := map[string][2]string{}
	for _, r := range recs {
		byName[r.Name] = [2]string{r.Label, r.Action}
	}
	if got := byName["Clean Movie"]; got != [2]string{"ok", "kept"} {
		t.Fatalf("clean record = %v", got)
	}
	if got := byName["Adult Thing"]; got != [2]string{"adult", "deleted"} {
		t.Fatalf("adult record = %v", got)
	}
	if got := byName["Spam Bait"]; got != [2]string{"spam", "deleted"} {
		t.Fatalf("spam record = %v", got)
	}

	// Dry-run: removals are recorded as would_delete and nothing is
	// actually deleted.
	st2 := newStore(t)
	seed(t, st2, "Adult Thing")
	m2 := newMod(t, st2, fakeAPI(t, label, nil).URL, func(c *Config) { c.DryRun = true })
	if _, err := m2.SweepOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs, err = st2.RecentModeration(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Action != "would_delete" {
		t.Fatalf("dry-run records = %+v", recs)
	}
}
