// Public RSS endpoint: /rss?q=<关键词>&category=<分类>.
//
// Feed readers fetch this URL from the public site, but the Go API
// stays on loopback — so this route fetches the feed server-side from
// the backend's /rss and streams it back. A short shared cache keeps a
// popular keyword's subscribers from each paying for a backend search.

import { API_BASE } from "@/lib/api";

export async function GET(request: Request) {
  const incoming = new URL(request.url).searchParams;
  const params = new URLSearchParams();
  const q = incoming.get("q");
  if (q) params.set("q", q);
  const category = incoming.get("category");
  if (category) params.set("category", category);

  let res: Response;
  try {
    res = await fetch(`${API_BASE}/rss?${params.toString()}`, {
      next: { revalidate: 60 },
    });
  } catch {
    return new Response("feed unavailable", { status: 502 });
  }
  const body = await res.text();
  return new Response(body, {
    status: res.status,
    headers: {
      "Content-Type":
        res.headers.get("Content-Type") ?? "application/rss+xml; charset=utf-8",
    },
  });
}
