// Theme presets and appearance modes.
//
// The primary-color presets are keyed by the same names the Go
// settings registry validates for THEME_COLOR (server/internal/
// settings/registry.go) — the server stores only the key, the hex
// values live here and nowhere else. Each preset carries four values:
// the primary itself (filled surfaces in both modes), its hover
// shade and the accent-ink for light mode, plus a brightened accent
// ink for dark mode, where the raw primary is too dark to read as
// text on the dark surface (Polaris's rule: filled surfaces keep the
// primary, text brightens). Derived shades (soft backgrounds, lines)
// are computed in globals.css with color-mix from --pri.

export interface ThemePreset {
  key: string;
  label: string;
  pri: string;
  hover: string;
  inkLight: string;
  inkDark: string;
}

export const THEME_PRESETS: Record<string, ThemePreset> = {
  teal: {
    key: "teal",
    label: "青蓝",
    pri: "#0e7490",
    hover: "#155e75",
    inkLight: "#155e75",
    inkDark: "#2dd4bf",
  },
  blue: {
    key: "blue",
    label: "蓝色",
    pri: "#2563eb",
    hover: "#1d4ed8",
    inkLight: "#1d4ed8",
    inkDark: "#93c5fd",
  },
  violet: {
    key: "violet",
    label: "紫色",
    pri: "#7c3aed",
    hover: "#6d28d9",
    inkLight: "#6d28d9",
    inkDark: "#c4b5fd",
  },
  green: {
    key: "green",
    label: "绿色",
    pri: "#16a34a",
    hover: "#15803d",
    inkLight: "#15803d",
    inkDark: "#86efac",
  },
  orange: {
    key: "orange",
    label: "橙色",
    pri: "#ea580c",
    hover: "#c2410c",
    inkLight: "#c2410c",
    inkDark: "#fdba74",
  },
  red: {
    key: "red",
    label: "红色",
    pri: "#dc2626",
    hover: "#b91c1c",
    inkLight: "#b91c1c",
    inkDark: "#fca5a5",
  },
};

export const DEFAULT_PRESET = THEME_PRESETS.teal;

export function themePreset(key: string | undefined): ThemePreset {
  return (key && THEME_PRESETS[key]) || DEFAULT_PRESET;
}

export type Appearance = "system" | "light" | "dark";

export const APPEARANCES: { key: Appearance; label: string }[] = [
  { key: "light", label: "浅色" },
  { key: "dark", label: "深色" },
  { key: "system", label: "跟随系统" },
];

/** localStorage key for the visitor's own appearance choice. It wins
 *  over the admin-set default appearance. */
export const APPEARANCE_STORAGE_KEY = "dhtsearch-appearance";

export function asAppearance(v: string | undefined): Appearance {
  return v === "light" || v === "dark" ? v : "system";
}
