/**
 * The site wordmark. The name comes from the settings registry
 * (SITE_NAME); the accent split keeps the historical look for the
 * default name — "DHT" + accented "Search" — and generalises: a
 * trailing "Search" or the part after the first space is accented,
 * anything else renders plain.
 */
export default function SiteLogo({
  name,
  className = "",
}: {
  name: string;
  className?: string;
}) {
  let head = name;
  let tail = "";
  if (name.length > "Search".length && name.endsWith("Search")) {
    head = name.slice(0, -"Search".length);
    tail = "Search";
  } else {
    const i = name.indexOf(" ");
    if (i > 0) {
      head = name.slice(0, i);
      tail = name.slice(i);
    }
  }
  return (
    <span className={className}>
      {head}
      {tail && <span className="text-accent-ink">{tail}</span>}
    </span>
  );
}
