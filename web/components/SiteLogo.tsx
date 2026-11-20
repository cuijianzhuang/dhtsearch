import Image from "next/image";

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
    <span className={`inline-flex items-center gap-[0.38em] ${className}`}>
      {/* Brand icon (magnet + node constellation). Sized in em so it
          tracks the wordmark at every call site — hero, page headers,
          admin shell — and rounded like the app-icon tile it ships as. */}
      <Image
        src="/icon-192.png"
        alt=""
        aria-hidden
        width={192}
        height={192}
        className="h-[1.3em] w-[1.3em] shrink-0 rounded-[0.3em]"
      />
      <span>
        {head}
        {tail && <span className="text-accent-ink">{tail}</span>}
      </span>
    </span>
  );
}
