import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { CollectionDialog } from "./CollectionUI";
import type { IconName } from "./icons";
import { Button } from "./ui";

export interface ConfirmOptions {
  title: string;
  message: ReactNode;
  /** Names the action, e.g. "Discard changes"; never a bare "OK". */
  confirmLabel: string;
  cancelLabel?: string;
  tone?: "danger" | "primary";
  icon?: IconName;
}

export interface TextRequestOptions {
  title: string;
  label: string;
  initialValue?: string;
  confirmLabel: string;
}

type PendingDialog = { serial: number } & (
  | ({ kind: "confirm"; resolve: (confirmed: boolean) => void } & ConfirmOptions)
  | ({ kind: "text"; resolve: (value: string | null) => void } & TextRequestOptions)
);

let pending: PendingDialog | null = null;
let serial = 0;
const listeners = new Set<() => void>();

function open(next: PendingDialog) {
  if (pending?.kind === "confirm") pending.resolve(false);
  else pending?.resolve(null);
  pending = next;
  listeners.forEach((listener) => listener());
}

function close() {
  pending = null;
  listeners.forEach((listener) => listener());
}

/**
 * Asks the user to confirm an action in the app's own dialog. Escape, the
 * close button and the backdrop cancel. A newer dialog supersedes an open
 * one, which resolves as cancelled.
 */
export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  const { promise, resolve } = Promise.withResolvers<boolean>();
  open({ kind: "confirm", serial: ++serial, ...options, resolve });
  return promise;
}

/** Asks for one line of text; resolves with the entered text, or null when cancelled. */
export function requestText(options: TextRequestOptions): Promise<string | null> {
  const { promise, resolve } = Promise.withResolvers<string | null>();
  open({ kind: "text", serial: ++serial, ...options, resolve });
  return promise;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Renders the open dialog. Mount once, beside the app shell. */
export function DialogHost() {
  const request = useSyncExternalStore(subscribe, () => pending);
  if (!request) return null;
  return request.kind === "text"
    ? <TextDialog key={request.serial} request={request} />
    : <ConfirmDialog key={request.serial} request={request} />;
}

// CollectionDialog calls showModal() in its own effect, and showModal() moves
// focus to the first focusable control: the header's close button. React's
// autoFocus runs before that, so each dialog below focuses its control from
// an effect of its own, which runs after the child's.

function ConfirmDialog({ request }: { request: Extract<PendingDialog, { kind: "confirm" }> }) {
  const message = useRef<HTMLParagraphElement>(null);
  useEffect(() => {
    // The quiet Cancel is the first footer button; destructive actions start there.
    message.current?.closest("dialog")?.querySelector<HTMLButtonElement>(".collection-dialog__footer button")?.focus();
  }, []);
  const settle = (confirmed: boolean) => {
    close();
    request.resolve(confirmed);
  };
  return (
    <CollectionDialog
      title={request.title}
      onClose={() => settle(false)}
      footer={
        <>
          <Button type="button" tone="quiet" onClick={() => settle(false)}>
            {request.cancelLabel ?? "Cancel"}
          </Button>
          <Button type="button" tone={request.tone ?? "danger"} icon={request.icon} onClick={() => settle(true)}>
            {request.confirmLabel}
          </Button>
        </>
      }
    >
      <p ref={message} className="collection-dialog__copy">{request.message}</p>
    </CollectionDialog>
  );
}

function TextDialog({ request }: { request: Extract<PendingDialog, { kind: "text" }> }) {
  const [value, setValue] = useState(request.initialValue ?? "");
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, []);
  const settle = (result: string | null) => {
    close();
    request.resolve(result);
  };
  const formID = `app-dialog-${request.serial}`;
  return (
    <CollectionDialog
      title={request.title}
      onClose={() => settle(null)}
      footer={
        <>
          <Button type="button" tone="quiet" onClick={() => settle(null)}>Cancel</Button>
          <Button type="submit" form={formID} tone="primary" disabled={!value.trim()}>{request.confirmLabel}</Button>
        </>
      }
    >
      <form
        id={formID}
        className="collections-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (value.trim()) settle(value);
        }}
      >
        <label>
          <span>{request.label}</span>
          <input ref={input} value={value} onChange={(event) => setValue(event.target.value)} />
        </label>
      </form>
    </CollectionDialog>
  );
}
