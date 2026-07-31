package moderator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With AllowAdult the static filter is admitting adult content on purpose, so
// the hourly pass must not undo that. Deleting here would be worse than a
// no-op: the infohash lands on the blocklist and can never be re-indexed.
func TestAllowAdultKeepsAdultRemovesSpam(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Big Buck Bunny 1080p", "Hot XXX Collection", "FREE CRACK DOWNLOAD click here")

	srv := fakeAPI(t, func(title string) string {
		switch {
		case strings.Contains(title, "XXX"):
			return "adult"
		case strings.Contains(title, "CRACK"):
			return "spam"
		}
		return "ok"
	}, nil)

	s, err := newMod(t, st, srv.URL, func(c *Config) { c.AllowAdult = true }).SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Reviewed != 3 {
		t.Fatalf("reviewed = %d, want 3", s.Reviewed)
	}
	if s.Adult != 0 {
		t.Errorf("Adult = %d, want 0: adult verdicts must not be actioned", s.Adult)
	}
	if s.Spam != 1 || s.Deleted != 1 {
		t.Errorf("spam=%d deleted=%d, want 1/1: spam removal is unaffected", s.Spam, s.Deleted)
	}

	// The adult row survives...
	res, err := st.Search(t.Context(), "XXX", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Errorf("adult row total = %d, want 1 (it was deleted)", res.Total)
	}
	// ...and is not blocklisted, so a rediscovery is not permanently refused.
	if n, _ := st.BlockedCount(t.Context()); n != 1 {
		t.Errorf("blocked = %d, want 1 (only the spam row)", n)
	}
	if known, _ := st.Known(t.Context(), "bhash"); !known {
		t.Error("adult row should still be indexed")
	}
}

// The zero value must filter: a Config built without touching AllowAdult has
// to behave exactly as before the flag existed.
func TestAllowAdultDefaultsToFiltering(t *testing.T) {
	st := newStore(t)
	seed(t, st, "Hot XXX Collection")
	srv := fakeAPI(t, func(string) string { return "adult" }, nil)

	s, err := newMod(t, st, srv.URL, nil).SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Adult != 1 || s.Deleted != 1 {
		t.Fatalf("summary = %+v, want Adult=1 Deleted=1 by default", s)
	}
}

// A kept adult listing should still get its title cleaned — it is on the page
// like anything else, so the ad banners have to come off it too.
func TestAllowAdultStillTrimsTitles(t *testing.T) {
	st := newStore(t)
	const raw = "【高清剧集网 www.bphdtv.com】Hot XXX Collection 1080p"
	seed(t, st, raw)

	// The shared trimAPI helper hardcodes an "ok" label; this case needs the
	// adult label and a cleaned title in the same verdict.
	srv := adultTrimAPI(t, "Hot XXX Collection 1080p")
	m := newMod(t, st, srv.URL, func(c *Config) {
		c.AllowAdult = true
		c.TrimTitles = true
	})
	s, err := m.SweepOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Trimmed != 1 {
		t.Fatalf("Trimmed = %d, want 1", s.Trimmed)
	}
	res, err := st.Search(t.Context(), "Hot XXX", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "Hot XXX Collection 1080p" {
		t.Fatalf("displayed name = %+v, want the trimmed title", res.Items)
	}
}

// adultTrimAPI answers every listing with the adult label and a fixed cleaned
// title, so a test can exercise "kept because adult is allowed, and trimmed".
func adultTrimAPI(t *testing.T, clean string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		var items []promptItem
		if err := json.Unmarshal([]byte(req.Messages[1].Content), &items); err != nil {
			t.Fatalf("listing is not JSON: %v", err)
		}
		type v struct {
			I     int    `json:"i"`
			Label string `json:"label"`
			Clean string `json:"clean"`
		}
		out := make([]v, len(items))
		for i, it := range items {
			out[i] = v{I: it.I, Label: "adult", Clean: clean}
		}
		content, _ := json.Marshal(map[string]any{"verdicts": out})
		json.NewEncoder(w).Encode(chatResponse{Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Content: string(content)}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}
