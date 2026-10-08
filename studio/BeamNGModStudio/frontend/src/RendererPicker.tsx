import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import { Icon } from "./icons";

// The renderers BeamNG's own launcher offers. "default" lets BeamNG choose:
// DirectX 12, falling back to DirectX 11, like the launcher's main button.
const RENDERERS = [
  { id: "default", name: "Default (DX12)", label: "Default renderer (DX12)" },
  { id: "vulkan", name: "Vulkan", label: "Vulkan renderer" },
  { id: "d3d12", name: "DX12", label: "DX12 renderer" },
  { id: "d3d11", name: "DX11", label: "DX11 renderer" },
];

/** A one-line "Default renderer (DX12) ⌄" control; the choice applies to every launch. */
export function RendererPicker({ onError }: { onError: (error: unknown) => void }) {
  const [renderer, setRenderer] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    let active = true;
    API.GameRenderer()
      .then((saved) => { if (active) setRenderer(saved); })
      .catch(onError);
    return () => { active = false; };
  }, [onError]);

  useEffect(() => {
    if (!open) return;
    root.current?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus();
    const dismiss = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [open]);

  const choose = (next: string) => {
    const previous = renderer;
    setOpen(false);
    trigger.current?.focus();
    if (next === previous) return;
    setRenderer(next);
    API.SetGameRenderer(next).catch((error: unknown) => {
      setRenderer(previous);
      onError(error);
    });
  };

  const navigate = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      setOpen(false);
      trigger.current?.focus();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const options = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("[role=menuitemradio]"));
    const current = options.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === "Home" ? 0
      : event.key === "End" ? options.length - 1
      : (current + (event.key === "ArrowDown" ? 1 : -1) + options.length) % options.length;
    options[next]?.focus();
  };

  const selected = RENDERERS.find((option) => option.id === renderer);
  return (
    <div className="play-renderer" ref={root}>
      <button
        ref={trigger}
        type="button"
        className="play-renderer__trigger"
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={!selected}
        onClick={() => setOpen((value) => !value)}
      >
        {selected?.label ?? "Renderer"}
        <Icon name="chevron" size={13} />
      </button>
      {open && (
        <div className="play-renderer__menu" role="menu" aria-label="Renderer" onKeyDown={navigate}>
          {RENDERERS.map((option) => (
            <button
              key={option.id}
              type="button"
              role="menuitemradio"
              aria-checked={option.id === renderer}
              className="play-renderer__option"
              onClick={() => choose(option.id)}
            >
              <span className="play-renderer__check" aria-hidden="true">
                {option.id === renderer && <Icon name="check" size={14} />}
              </span>
              <span className="play-renderer__name">{option.name}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
