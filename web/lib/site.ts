// Site presentation configuration, served by the Go backend's
// public /api/site endpoint (which reads the settings registry:
// SITE_NAME / THEME_COLOR / THEME_APPEARANCE, DB override > env >
// default). Server-side only, like the rest of the API layer.
//
// The fetch is cached per request (React cache) and revalidated on a
// timer, so a name/color change in the admin settings shows up
// within a minute without a web restart, and a down backend degrades
// to the built-in defaults instead of breaking every page.

import { cache } from "react";
import { API_BASE } from "./api";
import { asAppearance, type Appearance } from "./theme";

export interface SiteConfig {
  name: string;
  themeColor: string;
  themeAppearance: Appearance;
}

const FALLBACK: SiteConfig = {
  name: "DHTSearch",
  themeColor: "blue",
  themeAppearance: "system",
};

export const fetchSiteConfig = cache(async (): Promise<SiteConfig> => {
  try {
    const res = await fetch(`${API_BASE}/api/site`, {
      next: { revalidate: 60 },
    });
    if (!res.ok) return FALLBACK;
    const data = (await res.json()) as {
      name?: string;
      theme_color?: string;
      theme_appearance?: string;
    };
    return {
      name: data.name?.trim() || FALLBACK.name,
      themeColor: data.theme_color || FALLBACK.themeColor,
      themeAppearance: asAppearance(data.theme_appearance),
    };
  } catch {
    return FALLBACK;
  }
});
