import { useEffect, useRef } from "react";
import "./Tooltip.css";

/*
 * One tooltip layer for the entire app.  Mount <TooltipLayer/> once at the
 * root; it never re-renders.  All positioning is imperative DOM work driven
 * by delegated pointer/focus/keyboard listeners on `document`.
 *
 * The native title bubble is suppressed by moving the attribute to
 * `data-tip-stashed` while the pointer is inside the element, and
 * restoring it on leave.  This means `title` stays in the DOM for
 * accessibility when the tooltip layer is not active.
 *
 * The div uses `popover="manual"` so it enters the browser top layer
 * and paints above `<dialog>` elements opened with `showModal()`.
 */

// ── Timing ──────────────────────────────────────────────────────────
const SHOW_DELAY = 380;    // ms before the first tooltip appears
const GRACE_WINDOW = 320;  // ms of free re-entry after hiding (warm cursor)

// ── Geometry ────────────────────────────────────────────────────────
const GAP = 6;             // px between control and tooltip
const VIEWPORT_PAD = 8;    // px inset from viewport edge

function closestTitle(el: Element | null): Element | null {
  while (el) {
    if (el.hasAttribute("title") || el.hasAttribute("data-tip-stashed")) return el;
    el = el.parentElement;
  }
  return null;
}

function readTipText(anchor: Element): string {
  return (
    anchor.getAttribute("data-tip-stashed") ||
    anchor.getAttribute("title") ||
    ""
  ).trim();
}

export function TooltipLayer() {
  const layerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const layer = layerRef.current!;
    if (!layer) return;

    let anchor: Element | null = null;
    let showTimer = 0;
    let graceTimer = 0;
    let warm = false;
    let visible = false;
    let popoverOpen = false;
    let source: "pointer" | "focus" = "pointer";

    // ── Stash / restore native title ──────────────────────────────
    function stashTitle(el: Element) {
      const raw = el.getAttribute("title");
      if (raw != null) {
        el.setAttribute("data-tip-stashed", raw);
        el.removeAttribute("title");
      }
    }

    function restoreTitle(el: Element) {
      const stashed = el.getAttribute("data-tip-stashed");
      if (stashed != null) {
        el.setAttribute("title", stashed);
        el.removeAttribute("data-tip-stashed");
      }
    }

    // ── Popover lifecycle ─────────────────────────────────────────
    function openPopover() {
      if (!popoverOpen) {
        try { layer.showPopover(); } catch { /* already open or unsupported */ }
        popoverOpen = true;
      }
    }

    function closePopover() {
      if (popoverOpen) {
        try { layer.hidePopover(); } catch { /* already closed */ }
        popoverOpen = false;
      }
    }

    // ── Position ──────────────────────────────────────────────────
    function position(anchorEl: Element) {
      const r = anchorEl.getBoundingClientRect();
      const vw = document.documentElement.clientWidth;
      const vh = document.documentElement.clientHeight;

      // Open the popover so the layer has layout for measurement
      openPopover();

      const lw = layer.offsetWidth;
      const lh = layer.offsetHeight;

      // Prefer below, centered horizontally
      let x = r.left + r.width / 2 - lw / 2;
      let y = r.bottom + GAP;

      // Flip above if below would clip
      if (y + lh > vh - VIEWPORT_PAD) {
        y = r.top - GAP - lh;
      }
      // If still clipping above, place below anyway
      if (y < VIEWPORT_PAD) {
        y = r.bottom + GAP;
      }

      // Clamp horizontally
      x = Math.max(VIEWPORT_PAD, Math.min(x, vw - lw - VIEWPORT_PAD));
      // Clamp vertically
      y = Math.max(VIEWPORT_PAD, Math.min(y, vh - lh - VIEWPORT_PAD));

      layer.style.left = `${Math.round(x)}px`;
      layer.style.top = `${Math.round(y)}px`;
    }

    // ── Show / Hide ────────────────────────────────────────────────
    function show(anchorEl: Element) {
      const text = readTipText(anchorEl);
      if (!text) return;

      anchor = anchorEl;
      layer.textContent = text;
      position(anchorEl);
      layer.classList.add("is-visible");
      visible = true;

      clearTimeout(graceTimer);
      warm = false;
    }

    function hide() {
      clearTimeout(showTimer);
      showTimer = 0;

      if (anchor) {
        restoreTitle(anchor);
      }

      if (visible) {
        layer.classList.remove("is-visible");
        closePopover();
        visible = false;
        warm = true;
        clearTimeout(graceTimer);
        graceTimer = window.setTimeout(() => { warm = false; }, GRACE_WINDOW);
      }

      anchor = null;
    }

    function scheduleShow(anchorEl: Element) {
      clearTimeout(showTimer);

      const delay = warm ? 0 : SHOW_DELAY;
      showTimer = window.setTimeout(() => {
        show(anchorEl);
        showTimer = 0;
      }, delay);
    }

    // ── Pointer events (delegated) ──────────────────────────────
    function onPointerOver(event: PointerEvent) {
      if (event.pointerType === "touch") return;

      const target = closestTitle(event.target as Element);
      if (!target) return;

      // Already showing for this exact element
      if (target === anchor && visible) return;

      // New anchor — hide the old one first
      if (anchor && anchor !== target) {
        hide();
      }

      source = "pointer";
      stashTitle(target);
      scheduleShow(target);
      anchor = target;
    }

    function onPointerOut(event: PointerEvent) {
      if (event.pointerType === "touch") return;

      const related = event.relatedTarget as Element | null;
      if (!anchor) return;

      // Still inside the anchor or a child of it — ignore
      if (related && anchor.contains(related)) return;

      hide();
    }

    // ── Focus events (delegated) ──────────────────────────────────
    function onFocusIn(event: FocusEvent) {
      const target = closestTitle(event.target as Element);
      if (!target) return;

      if (target === anchor && visible && source === "pointer") return;

      if (anchor && anchor !== target) hide();

      source = "focus";
      // Deliberately no stashTitle here.  The native bubble only appears on
      // hover, so there is nothing to suppress on focus - and 420 controls in
      // this app take their accessible name from `title` alone.  Removing it
      // while the control is focused is exactly when a screen reader needs it.
      scheduleShow(target);
      anchor = target;
    }

    function onFocusOut(_event: FocusEvent) {
      if (source !== "focus") return;
      hide();
    }

    // ── Dismissals ──────────────────────────────────────────────
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape" && (visible || showTimer)) {
        hide();
      }
    }

    function onScroll() {
      if (visible || showTimer) hide();
    }

    function onPointerDown() {
      if (visible || showTimer) hide();
    }

    // ── Bind ────────────────────────────────────────────────────
    document.addEventListener("pointerover", onPointerOver, true);
    document.addEventListener("pointerout", onPointerOut, true);
    document.addEventListener("focusin", onFocusIn, true);
    document.addEventListener("focusout", onFocusOut, true);
    document.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("scroll", onScroll, { capture: true, passive: true });
    document.addEventListener("pointerdown", onPointerDown, true);

    return () => {
      clearTimeout(showTimer);
      clearTimeout(graceTimer);
      if (anchor) restoreTitle(anchor);
      closePopover();
      document.removeEventListener("pointerover", onPointerOver, true);
      document.removeEventListener("pointerout", onPointerOut, true);
      document.removeEventListener("focusin", onFocusIn, true);
      document.removeEventListener("focusout", onFocusOut, true);
      document.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("scroll", onScroll, { capture: true } as EventListenerOptions);
      document.removeEventListener("pointerdown", onPointerDown, true);
    };
  }, []);

  return (
    <div
      ref={layerRef}
      className="tooltip-layer"
      aria-hidden="true"
      // @ts-expect-error popover is valid HTML but not yet in React's types
      popover="manual"
    />
  );
}
