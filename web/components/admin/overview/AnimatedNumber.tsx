"use client";

// A count that tweens to its new value when the prop changes, so the
// dashboard's 8-second poll reads as motion instead of a jump cut.
// The first render shows the server-rendered value verbatim (no
// count-up from zero — the number is real from the first paint), and
// prefers-reduced-motion disables the tween entirely. All state
// writes happen inside requestAnimationFrame callbacks, never
// synchronously in the effect.

import { useEffect, useRef, useState } from "react";
import { formatCount } from "@/lib/format";

export default function AnimatedNumber({ value }: { value: number }) {
  const [display, setDisplay] = useState(value);
  // The value currently on screen; a tween interrupted by the next
  // poll restarts from here, not from its original target.
  const shownRef = useRef(value);
  const rafRef = useRef(0);

  useEffect(() => {
    const from = shownRef.current;
    const to = value;
    if (
      from === to ||
      window.matchMedia("(prefers-reduced-motion: reduce)").matches
    ) {
      rafRef.current = requestAnimationFrame(() => {
        shownRef.current = to;
        setDisplay(to);
      });
      return () => cancelAnimationFrame(rafRef.current);
    }
    const start = performance.now();
    const duration = 650;
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / duration);
      const eased = 1 - Math.pow(1 - p, 3);
      const current = Math.round(from + (to - from) * eased);
      shownRef.current = current;
      setDisplay(current);
      if (p < 1) rafRef.current = requestAnimationFrame(tick);
    };
    rafRef.current = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(rafRef.current);
  }, [value]);

  return <>{formatCount(display)}</>;
}
