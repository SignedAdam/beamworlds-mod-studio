import { useRef, useEffect } from "react";
import "./NewBadge.css";

/**
 * A polished "New" pill shown next to mod names.
 * Plays a one-shot shine animation on first render, respects prefers-reduced-motion.
 */
export function NewBadge() {
  const ref = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.addEventListener("animationend", () => el.classList.add("is-settled"), { once: true });
  }, []);
  return (
    <span ref={ref} className="new-badge" aria-label="New">
      New
    </span>
  );
}
