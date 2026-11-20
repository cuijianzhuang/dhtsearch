"use client";

// A count that tweens to its new value when the prop changes, so the
// dashboard's 8-second poll reads as motion instead of a jump cut.
// The first render shows the server-rendered value verbatim (no
// count-up from zero — the number is real from the first paint), and
// prefers-reduced-motion disables the tween entirely.
//
// The tween writes straight to the DOM node's textContent instead of
// setState: the dashboard carries ~15 of these, and a state write per
// frame per instance meant ~15 React re-renders per frame for the
// whole 650ms tween after every poll — measurable long tasks on a
// throttled phone CPU. With a ref write the only React render is the
// one the poll itself causes; the frames in between are plain DOM
// text updates. The span renders the formatted value as its initial
// content, so SSR and no-JS still show the real number.

import { useEffect, useRef, useState } from "react";
import { formatCount, formatExactCount } from "@/lib/format";

export default function AnimatedNumber({ value }: { value: number }) {
  const ref = useRef<HTMLSpanElement>(null);
  // The mount-time value is the span's only React-rendered content:
  // it never changes across renders, so React never rewrites the text
  // node after mount and the tween (which writes textContent itself)
  // fully owns it. Rendering the live prop instead would flash the
  // final value for one frame before the tween's first tick.
  const [firstValue] = useState(value);
  // The value currently on screen; a tween interrupted by the next
  // poll restarts from here, not from its original target.
  const shownRef = useRef(value);
  const rafRef = useRef(0);

  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    const from = shownRef.current;
    const to = value;
    if (
      from === to ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      shownRef.current = to;
      node.textContent = formatCount(to);
      return;
    }
    const start = performance.now();
    const duration = 650;
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / duration);
      const eased = 1 - Math.pow(1 - p, 3);
      const current = Math.round(from + (to - from) * eased);
      shownRef.current = current;
      node.textContent = formatCount(current);
      if (p < 1) rafRef.current = requestAnimationFrame(tick);
    };
    rafRef.current = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(rafRef.current);
  }, [value]);

  // Big values render abbreviated (万/亿) so they fit their slot;
  // the exact grouped figure rides in the title, one hover away.
  // The attribute updates with the prop even though the text node
  // stays mount-owned — React only rewrites what changed.
  const exact = formatExactCount(value);
  const title = formatCount(value) !== exact ? exact : undefined;
  return (
    <span ref={ref} title={title}>
      {formatCount(firstValue)}
    </span>
  );
}
