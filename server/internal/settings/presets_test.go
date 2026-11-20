package settings

import "testing"

// The filter presets are served to the settings page, which fills
// its form from them verbatim and saves through the normal Update
// path — so every preset value must be something Update would
// accept: a real, editable filter-group key carrying a valid,
// already-canonical value.
func TestFilterPresetsAreValid(t *testing.T) {
	if len(FilterPresets) == 0 {
		t.Fatal("no filter presets defined")
	}
	names := map[string]bool{}
	var keySet map[string]bool
	for _, p := range FilterPresets {
		if p.Name == "" || p.Label == "" {
			t.Errorf("preset with empty name/label: %+v", p)
		}
		if names[p.Name] {
			t.Errorf("duplicate preset name %q", p.Name)
		}
		names[p.Name] = true
		if len(p.Values) == 0 {
			t.Errorf("preset %q has no values", p.Name)
		}
		this := map[string]bool{}
		for key, v := range p.Values {
			it, ok := ByKey(key)
			if !ok {
				t.Errorf("preset %q: unknown setting %s", p.Name, key)
				continue
			}
			if it.Group != "filter" {
				t.Errorf("preset %q: %s belongs to group %q, not filter", p.Name, key, it.Group)
			}
			if !it.Editable {
				t.Errorf("preset %q: %s is not editable", p.Name, key)
			}
			if it.Type == TypeList {
				t.Errorf("preset %q: %s is a list — presets must never touch operator lists", p.Name, key)
			}
			if err := validate(it, v); err != nil {
				t.Errorf("preset %q: %s=%q invalid: %v", p.Name, key, v, err)
			}
			if c := canonical(it, v); c != v {
				t.Errorf("preset %q: %s=%q is not canonical (would be %q)", p.Name, key, v, c)
			}
			this[key] = true
		}
		// All presets cover the same keys, so switching between them
		// never leaves a key at whatever a previous preset set.
		if keySet == nil {
			keySet = this
		} else {
			if len(this) != len(keySet) {
				t.Errorf("preset %q covers %d keys, want %d (same set as the others)", p.Name, len(this), len(keySet))
			}
			for k := range keySet {
				if !this[k] {
					t.Errorf("preset %q does not set %s", p.Name, k)
				}
			}
		}
	}
}

func presetByName(t *testing.T, name string) Preset {
	t.Helper()
	for _, p := range FilterPresets {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("preset %q not found", name)
	return Preset{}
}

// 标准 is defined as the registry defaults: if a default ever moves,
// the preset must move with it, or the "standard" template would
// silently stop restoring stock behaviour.
func TestStandardPresetEqualsRegistryDefaults(t *testing.T) {
	std := presetByName(t, "standard")
	for key, v := range std.Values {
		it, _ := ByKey(key)
		if it.Default != v {
			t.Errorf("standard preset %s=%q, registry default is %q", key, v, it.Default)
		}
	}
}

// 成人模式 is 标准 with exactly one change: the adult category off
// (the master switch stays on, so spam and size filtering keep
// their standard behaviour and adult torrents are simply indexed).
func TestAdultPresetDiffersFromStandardOnlyInAdultSwitch(t *testing.T) {
	std := presetByName(t, "standard")
	adult := presetByName(t, "adult")
	if adult.Values[KeyFilterEnabled] != "true" {
		t.Errorf("adult preset: master switch = %q, want true", adult.Values[KeyFilterEnabled])
	}
	if adult.Values[KeyFilterAdultEnabled] != "false" {
		t.Errorf("adult preset: adult switch = %q, want false", adult.Values[KeyFilterAdultEnabled])
	}
	for key, v := range std.Values {
		if key == KeyFilterAdultEnabled {
			continue
		}
		if adult.Values[key] != v {
			t.Errorf("adult preset %s=%q, standard has %q — only the adult switch may differ", key, adult.Values[key], v)
		}
	}
}
