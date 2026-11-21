package settings

import "testing"

// TestAlertDefaults pins the alerts group's registry contract:
// the notifier ships OFF (a fresh deployment has no channel
// configured), the stall threshold defaults to the watchdog's
// historical 15 minutes and is a restart item (it is assembled
// into the watchdog at startup), and the channel knobs are live
// with the two secrets masked.
func TestAlertDefaults(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if m.Bool(KeyAlertEnabled) {
		t.Fatal("ALERT_ENABLED must default to false")
	}
	if got := m.Int(KeyAlertStallMinutes); got != 15 {
		t.Fatalf("ALERT_STALL_MINUTES = %d, want 15", got)
	}

	it, ok := ByKey(KeyAlertStallMinutes)
	if !ok {
		t.Fatal("ALERT_STALL_MINUTES missing from the registry")
	}
	if it.Group != "alerts" || !it.Editable || !it.Restart {
		t.Fatalf("ALERT_STALL_MINUTES: group=%q editable=%v restart=%v, want alerts/editable/restart",
			it.Group, it.Editable, it.Restart)
	}
	for _, key := range []string{KeyAlertEnabled, KeyAlertWebhookURL, KeyAlertTelegramBotToken, KeyAlertTelegramChatID} {
		it, ok := ByKey(key)
		if !ok {
			t.Fatalf("%s missing from the registry", key)
		}
		if it.Group != "alerts" || !it.Editable || it.Restart {
			t.Fatalf("%s: group=%q editable=%v restart=%v, want alerts/editable/hot",
				key, it.Group, it.Editable, it.Restart)
		}
	}
	for _, key := range []string{KeyAlertWebhookURL, KeyAlertTelegramBotToken} {
		it, _ := ByKey(key)
		if !it.Sensitive || it.Type != TypeSecret {
			t.Fatalf("%s: sensitive=%v type=%v, want a masked secret", key, it.Sensitive, it.Type)
		}
	}

	found := false
	for _, g := range Groups {
		if g.Key == "alerts" {
			found = true
		}
	}
	if !found {
		t.Fatal("alerts group missing from Groups")
	}
}

// The snapshot the settings page renders must carry the alerts
// group with all five items, the secrets masked to their set
// flags.
func TestAlertSnapshotMasked(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	if _, _, err := m.Update(map[string]*string{
		KeyAlertWebhookURL:     strptr("https://example.com/hook/secret"),
		KeyAlertTelegramChatID: strptr("-100123"),
	}); err != nil {
		t.Fatal(err)
	}
	var group *GroupState
	snap := m.Snapshot()
	for i := range snap {
		if snap[i].Key == "alerts" {
			group = &snap[i]
		}
	}
	if group == nil {
		t.Fatal("alerts group missing from snapshot")
	}
	if len(group.Items) != 5 {
		t.Fatalf("alerts group has %d items, want 5", len(group.Items))
	}
	for _, item := range group.Items {
		if item.Sensitive && item.Value != "" {
			t.Fatalf("%s leaks its value in the snapshot", item.Key)
		}
		if item.Key == KeyAlertWebhookURL && !item.Set {
			t.Fatal("webhook URL saved but snapshot says not set")
		}
		if item.Key == KeyAlertTelegramBotToken && item.Set {
			t.Fatal("bot token never saved but snapshot says set")
		}
	}
}

// A webhook value that is not an http(s) URL is rejected at the
// settings layer, like the other URL knobs.
func TestAlertWebhookURLValidation(t *testing.T) {
	m := newManager(t, nil, nil, &fakeStorage{})
	for _, bad := range []string{"not-a-url", "ftp://example.com/hook", "https://"} {
		if _, _, err := m.Update(map[string]*string{KeyAlertWebhookURL: strptr(bad)}); err == nil {
			t.Fatalf("webhook %q accepted, want a validation error", bad)
		}
	}
	if _, _, err := m.Update(map[string]*string{KeyAlertWebhookURL: strptr("https://example.com/hook")}); err != nil {
		t.Fatalf("valid webhook rejected: %v", err)
	}
}
