import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent, ReactNode } from "react";
import { createPortal } from "react-dom";
import type { ModCollection } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon, type IconName } from "./icons";
import "./CollectionUI.css";

export function CollectionDialog({ title, onClose, children, footer, wide = false }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleID = useId();
  useEffect(() => {
    const element = dialog.current;
    const previous = document.activeElement;
    element?.showModal();
    return () => {
      element?.close();
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);
  return createPortal(
    <dialog
      ref={dialog}
      className={`collection-dialog${wide ? " collection-dialog--wide" : ""}`}
      aria-labelledby={titleID}
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onClick={(event) => {
        // A click on the dialog element itself is a click on the backdrop:
        // the panel content is inside header/body/footer children.
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <header className="collection-dialog__header">
        <h2 id={titleID}>{title}</h2>
        <button type="button" className="icon-button" aria-label={`Close ${title}`} onClick={onClose}><Icon name="close" /></button>
      </header>
      <div className="collection-dialog__body">{children}</div>
      {footer && <footer className="collection-dialog__footer">{footer}</footer>}
    </dialog>,
    document.body,
  );
}

export function CollectionCard({ collection, selected = false, inherited, primaryAction = "toggle", toggleLabel, onOpen, onToggle, onContextMenu, onMenu }: {
  collection: ModCollection;
  selected?: boolean;
  inherited?: string;
  /** What a click on the card body does. Play toggles the selection; the
   *  Collections page opens the collection so it can be edited. */
  primaryAction?: "open" | "toggle";
  /** Names what the toggle does when it isn't plain select/deselect, e.g. "Leave out X". */
  toggleLabel?: string;
  onOpen?: () => void;
  onToggle?: () => void;
  onContextMenu?: (event: MouseEvent<HTMLElement>) => void;
  onMenu?: (event: MouseEvent<HTMLButtonElement>) => void;
}) {
  const [failed, setFailed] = useState(false);
  const imageURL = collection.coverUrl || "";
  useEffect(() => setFailed(false), [imageURL]);
  const included = selected || Boolean(inherited);
  const inheritedOnly = Boolean(inherited) && !selected;
  const opens = primaryAction === "open" && Boolean(onOpen);
  const bodyAction = opens ? onOpen : onToggle ?? onOpen;
  return (
    <article className={`collection-card${selected ? " is-selected" : ""}${inheritedOnly ? " is-inherited" : ""}`} onContextMenu={onContextMenu}>
      {onToggle && (
        <button
          type="button"
          className={`collection-card__check${included ? " is-checked" : ""}`}
          aria-label={toggleLabel ?? `${included ? "Deselect" : "Select"} ${collection.name}`}
          title={toggleLabel}
          aria-pressed={included}
          onClick={onToggle}
        >
          {included && <Icon name={inheritedOnly ? "link" : "check"} size={15} />}
        </button>
      )}
      <button
        type="button"
        className="collection-card__main"
        aria-label={`${opens ? "Open " : ""}${collection.name}, ${collection.modCount} mods${inheritedOnly ? `, included via ${inherited}` : ""}${toggleLabel ? `. ${toggleLabel}` : ""}`}
        aria-pressed={onToggle && !opens ? included : undefined}
        title={toggleLabel}
        onClick={bodyAction}
        onKeyDown={(event) => {
          if (event.shiftKey && event.key === "F10" && onContextMenu) {
            event.preventDefault();
            const rect = event.currentTarget.getBoundingClientRect();
            event.currentTarget.dispatchEvent(new window.MouseEvent("contextmenu", { bubbles: true, clientX: rect.left + 24, clientY: rect.top + 24 }));
          }
        }}
      >
        <span className="collection-card__art">
          {imageURL && !failed ? <img src={imageURL} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} /> : (
            <span className="collection-card__fallback" aria-hidden="true"><Icon name="mixed" size={30} /></span>
          )}
          <span className="collection-card__shade" />
          <span className="collection-card__count">{collection.modCount.toLocaleString()} <span>{collection.modCount === 1 ? "mod" : "mods"}</span></span>
        </span>
        <span className="collection-card__identity">
          <strong title={collection.name}>{collection.name}</strong>
          <span>{inheritedOnly ? `Included via ${inherited}` : collection.description || (collection.childCount ? `${collection.childCount} included collection${collection.childCount === 1 ? "" : "s"}` : "")}</span>
        </span>
      </button>
      {(onMenu || (onToggle && onOpen && !opens)) && <div className="collection-card__actions">
        {onToggle && onOpen && !opens && <button type="button" className="icon-button" title={`View ${collection.name}`} aria-label={`View ${collection.name}`} onClick={onOpen}><Icon name="files" size={16} /></button>}
        {onMenu && <button type="button" className="icon-button" aria-label={`Actions for ${collection.name}`} aria-haspopup="menu" onClick={onMenu}><Icon name="more" size={18} /></button>}
      </div>}
    </article>
  );
}

export interface CollectionMenuAction {
  label: string;
  icon?: IconName;
  danger?: boolean;
  disabled?: boolean;
  detail?: string;
  onClick: () => void;
}

export function CollectionMenuPopup({ label, x, y, actions, onClose }: {
  label: string;
  x: number;
  y: number;
  actions: CollectionMenuAction[];
  onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const element = dialog.current;
    const previous = document.activeElement;
    element?.showModal();
    if (element) {
      const rect = element.getBoundingClientRect();
      element.style.left = `${Math.max(8, Math.min(x, window.innerWidth - rect.width - 8))}px`;
      element.style.top = `${Math.max(8, Math.min(y, window.innerHeight - rect.height - 8))}px`;
      element.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    }
    const dismiss = () => close.current();
    window.addEventListener("resize", dismiss);
    return () => {
      window.removeEventListener("resize", dismiss);
      element?.close();
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus({ preventScroll: true });
    };
  }, [x, y]);
  return createPortal(
    <dialog
      ref={dialog}
      className="collection-context-menu"
      aria-label={label}
      onCancel={(event) => { event.preventDefault(); onClose(); }}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) onClose();
      }}
      onKeyDown={(event) => {
        if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const buttons = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
        if (!buttons.length) return;
        const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1 : (current + (event.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
        buttons[next].focus();
      }}
    >
      <div role="menu" aria-label={label}>
        {actions.map((action, index) => <button
          key={`${action.label}-${index}`}
          type="button"
          role="menuitem"
          className={action.danger ? "is-danger" : ""}
          disabled={action.disabled}
          title={action.disabled ? action.detail : undefined}
          onClick={() => { onClose(); action.onClick(); }}
        >
          <Icon name={action.icon ?? "arrow"} size={17} />
          <span>{action.label}</span>
        </button>)}
      </div>
    </dialog>,
    document.body,
  );
}
