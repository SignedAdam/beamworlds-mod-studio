import { useCallback, useEffect, useRef, useState } from "react";
import { CancelError, Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import { CollectionDialog } from "./CollectionUI";
import { useFileManagerLabel } from "./fileManager";
import { Icon } from "./icons";
import { Button, Spinner, formatBytes } from "./ui";
import type {
  StorageAudit,
  StorageAuditItem,
  StorageCleanupResult,
  StorageProgress,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import "./StorageReview.css";

/* ── Helpers ── */

function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return typeof error === "string" ? error : "Unexpected error";
}

function fileName(path: string): string {
  return path.split(/[\\/]/).pop() || path;
}

function itemName(item: StorageAuditItem): string {
  return item.displayName || fileName(item.path);
}

// Earlier versions cached archives under their SHA-256; that name tells the
// user nothing, so only meaningful file names are shown under the mod name.
function itemFileLabel(item: StorageAuditItem): string {
  const name = fileName(item.path);
  return /^[0-9a-f]{64}\.zip$/i.test(name) || name === itemName(item) ? "" : name;
}

function count(value: number, one: string, many = `${one}s`): string {
  return `${value.toLocaleString()} ${value === 1 ? one : many}`;
}

// Only these classifications are ever cleanable; anything else the backend
// marks cleanable still gets a group rather than disappearing.
const removableGroupTitles: Record<string, string> = {
  "legacy-cache-redundant": "Copies from earlier versions",
  "collection-mirror": "Collection folder leftovers",
};

function groupRemovable(items: StorageAuditItem[]): { title: string; items: StorageAuditItem[] }[] {
  const groups = new Map<string, StorageAuditItem[]>();
  for (const item of items) {
    const title = removableGroupTitles[item.classification] ?? "Other leftovers";
    groups.set(title, [...(groups.get(title) ?? []), item]);
  }
  return [...groups].map(([title, groupItems]) => ({ title, items: groupItems }));
}

function resultHeading(result: StorageCleanupResult, operation: Operation): string {
  const removed = result.removedLinks + result.removedCopies;
  if ((result.failures ?? []).length > 0) {
    if (removed > 0 || result.recovered > 0) return "Partly done";
    return operation === "recover" ? "Not added to library" : "Nothing removed";
  }
  if (result.recovered > 0 && removed === 0) return "Added to library";
  return `${count(removed, "file")} removed`;
}

/* ── Types ── */

interface StorageReviewProps {
  onClose: () => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
  onRefreshLibrary: () => void;
}

type Phase = "idle" | "auditing" | "ready" | "confirming" | "recover-confirm" | "applying" | "done";
type Operation = "remove" | "recover";

/* ── Component ── */

export function StorageReview({
  onClose,
  onNotify,
  onRefreshLibrary,
}: StorageReviewProps) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [audit, setAudit] = useState<StorageAudit | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [progress, setProgress] = useState<StorageProgress | null>(null);
  const [result, setResult] = useState<StorageCleanupResult | null>(null);
  const [recoveryReview, setRecoveryReview] = useState<StorageAuditItem | null>(null);
  const [operation, setOperation] = useState<Operation>("remove");
  const mountedRef = useRef(true);
  const auditVersionRef = useRef(0);
  const requestRef = useRef<{ cancel(): void } | null>(null);
  const operationRef = useRef<"audit" | "mutation" | null>(null);
  const operationIDRef = useRef("");
  const operationKindRef = useRef<Operation>("remove");
  const handledResultRef = useRef(false);
  const [cancelling, setCancelling] = useState(false);
  const fileManagerLabel = useFileManagerLabel();

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      auditVersionRef.current += 1;
      requestRef.current?.cancel();
    };
  }, []);

  const finishMutation = useCallback((completed: StorageCleanupResult) => {
    if (!mountedRef.current || handledResultRef.current) return;
    handledResultRef.current = true;
    operationRef.current = null;
    setResult(completed);
    setPhase("done");
    setCancelling(false);
    onRefreshLibrary();
    const failed = (completed.failures ?? []).length > 0;
    onNotify(resultHeading(completed, operationKindRef.current), failed ? "error" : "success");
  }, [onNotify, onRefreshLibrary]);

  useEffect(() => {
    return Events.On("storage:progress", (event) => {
      const data = event.data;
      if (!mountedRef.current || !operationRef.current) return;
      if (!operationIDRef.current) operationIDRef.current = data.operationId;
      if (data.operationId !== operationIDRef.current) return;
      setProgress(data);
      if (data.done && data.result && operationRef.current === "mutation") {
        finishMutation(data.result);
      }
    });
  }, [finishMutation]);

  const cancelOperation = () => {
    if (operationRef.current === "audit") {
      auditVersionRef.current += 1;
      operationRef.current = null;
      requestRef.current?.cancel();
      setPhase("idle");
      setError("Scan cancelled");
    } else if (operationRef.current === "mutation") {
      setCancelling(true);
      requestRef.current?.cancel();
    }
  };

  const runAudit = useCallback(async () => {
    requestRef.current?.cancel();
    const version = ++auditVersionRef.current;
    operationRef.current = "audit";
    operationIDRef.current = "";
    setPhase("auditing");
    setError("");
    setAudit(null);
    setSelected(new Set());
    setResult(null);
    setProgress(null);
    const request = API.AuditArchiveStorage();
    requestRef.current = request;
    try {
      const auditResult = await request;
      if (!mountedRef.current || version !== auditVersionRef.current) return;
      setAudit(auditResult);
      setPhase("ready");
    } catch (err) {
      if (!mountedRef.current || version !== auditVersionRef.current) return;
      if (!(err instanceof CancelError)) setError(errorMessage(err));
      setPhase("idle");
    } finally {
      if (requestRef.current === request) {
        requestRef.current = null;
        operationRef.current = null;
      }
    }
  }, []);

  // Start audit on mount.
  useEffect(() => {
    void runAudit();
  }, [runAudit]);

  const toggleItems = (ids: string[], on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (on) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  const beginMutation = (kind: Operation) => {
    handledResultRef.current = false;
    operationRef.current = "mutation";
    operationIDRef.current = "";
    operationKindRef.current = kind;
    setOperation(kind);
    setPhase("applying");
    setError("");
    setProgress(null);
    setCancelling(false);
  };

  const applyCleanup = async () => {
    if (!audit || selected.size === 0 || operationRef.current) return;
    beginMutation("remove");
    const request = API.ApplyStorageCleanup(audit.fingerprint, Array.from(selected));
    requestRef.current = request;
    try {
      finishMutation(await request);
    } catch (err) {
      if (!mountedRef.current || err instanceof CancelError) return;
      if (!handledResultRef.current) {
        operationRef.current = null;
        setError(errorMessage(err));
        setPhase("ready");
      }
    } finally {
      if (requestRef.current === request) requestRef.current = null;
    }
  };

  const recoverItem = async (itemId: string) => {
    if (!audit || operationRef.current) return;
    beginMutation("recover");
    const request = API.RecoverStorageArchive(audit.fingerprint, itemId);
    requestRef.current = request;
    try {
      finishMutation(await request);
    } catch (err) {
      if (!mountedRef.current || err instanceof CancelError) return;
      if (!handledResultRef.current) {
        operationRef.current = null;
        setError(errorMessage(err));
        setPhase("ready");
      }
    } finally {
      if (requestRef.current === request) requestRef.current = null;
    }
  };

  const reveal = (path: string) => {
    API.RevealStoragePath(path).catch((err: unknown) => onNotify(errorMessage(err), "error"));
  };

  const items = audit?.items ?? [];
  const removable = items.filter((item) => item.cleanupAllowed);
  const recoverable = items.filter((item) => item.recoveryAllowed && !item.cleanupAllowed);
  const selectedItems = removable.filter((item) => selected.has(item.id));
  const selectedBytes = selectedItems.reduce((sum, item) => sum + item.reclaimableBytes, 0);
  const reclaimable = removable.reduce((sum, item) => sum + item.reclaimableBytes, 0);
  const warnings = audit?.warnings ?? [];

  const close = () => {
    if (phase === "applying") return;
    onClose();
  };

  const footer = phase === "done" ? (
    <>
      <Button icon="refresh" onClick={() => void runAudit()}>Scan again</Button>
      <Button tone="primary" onClick={close}>Close</Button>
    </>
  ) : phase === "confirming" ? (
    <>
      <Button tone="quiet" onClick={() => setPhase("ready")}>Back</Button>
      <Button tone="danger" icon="trash" onClick={() => void applyCleanup()}>
        Remove {count(selected.size, "file")}
      </Button>
    </>
  ) : phase === "recover-confirm" && recoveryReview ? (
    <>
      <Button tone="quiet" onClick={() => setPhase("ready")}>Back</Button>
      <Button tone="primary" icon="plus" onClick={() => void recoverItem(recoveryReview.id)}>Add to library</Button>
    </>
  ) : (
    <>
      <span className="storage-footer-status" role="status" aria-live="polite">
        {phase === "ready" && selected.size > 0
          ? `${selected.size.toLocaleString()} selected (${formatBytes(selectedBytes)})`
          : ""}
      </span>
      {(phase === "auditing" || phase === "applying") && (
        <Button tone="quiet" disabled={cancelling} onClick={cancelOperation}>
          {cancelling ? "Stopping…" : "Cancel"}
        </Button>
      )}
      <Button onClick={close} disabled={phase === "applying"}>Close</Button>
      {phase === "ready" && removable.length > 0 && (
        <Button
          tone="primary"
          icon="trash"
          disabled={selected.size === 0}
          onClick={() => setPhase("confirming")}
        >
          Remove selected
        </Button>
      )}
    </>
  );

  return (
    <CollectionDialog title="Review storage" wide onClose={close} footer={footer}>
      <div className="storage-review">
        {error && (
          <div className="storage-error" role="alert">
            <Icon name="error" size={15} />
            <span>{error}</span>
            {phase !== "ready" && (
              <Button tone="quiet" icon="refresh" onClick={() => void runAudit()}>
                Scan again
              </Button>
            )}
          </div>
        )}

        {phase === "auditing" && (
          <div className="storage-loading" role="status">
            <Spinner />
            <span>Scanning storage…</span>
          </div>
        )}

        {phase === "applying" && (
          <div className="storage-loading" role="status">
            <Spinner />
            <span>
              {cancelling
                ? "Stopping…"
                : operation === "recover"
                  ? "Adding to library…"
                  : "Removing files…"}
            </span>
            {progress && progress.total > 0 && (
              <>
                <progress value={progress.completed} max={progress.total} />
                <small>{progress.completed.toLocaleString()} of {progress.total.toLocaleString()}</small>
              </>
            )}
            {progress?.error && <p className="storage-loading__error">{progress.error}</p>}
          </div>
        )}

        {phase === "ready" && audit && (
          <>
            <div className="storage-summary">
              <div className="storage-summary__value">
                <strong>
                  {reclaimable > 0
                    ? formatBytes(reclaimable)
                    : removable.length + recoverable.length > 0
                      ? "No space to free"
                      : "Nothing to clean up"}
                </strong>
                {reclaimable > 0 && <span>Can be freed</span>}
              </div>
              <Button tone="quiet" icon="refresh" onClick={() => void runAudit()}>Scan again</Button>
            </div>

            {groupRemovable(removable).map((group) => {
              const ids = group.items.map((item) => item.id);
              const allSelected = ids.every((id) => selected.has(id));
              const groupBytes = group.items.reduce((sum, item) => sum + item.reclaimableBytes, 0);
              return (
                <section className="storage-group" key={group.title} aria-label={group.title}>
                  <header className="storage-group__header">
                    <label className="storage-group__title">
                      <input
                        type="checkbox"
                        checked={allSelected}
                        onChange={() => toggleItems(ids, !allSelected)}
                        aria-label={`Select all: ${group.title}`}
                      />
                      <span>{group.title}</span>
                    </label>
                    <span className="storage-size">{formatBytes(groupBytes)}</span>
                  </header>
                  <ul className="storage-rows">
                    {group.items.map((item) => (
                      <li className={`storage-row${selected.has(item.id) ? " is-selected" : ""}`} key={item.id}>
                        <label className="storage-row__main">
                          <input
                            type="checkbox"
                            checked={selected.has(item.id)}
                            onChange={() => toggleItems([item.id], !selected.has(item.id))}
                          />
                          <span className="storage-row__text" title={item.path}>
                            <strong>{itemName(item)}</strong>
                            {itemFileLabel(item) && <small>{itemFileLabel(item)}</small>}
                          </span>
                        </label>
                        <span className="storage-size">{formatBytes(item.reclaimableBytes)}</span>
                        <RevealButton label={fileManagerLabel} name={itemName(item)} onClick={() => reveal(item.path)} />
                      </li>
                    ))}
                  </ul>
                </section>
              );
            })}

            {recoverable.length > 0 && (
              <section className="storage-group" aria-label="Mods not in library">
                <header className="storage-group__header">
                  <span className="storage-group__title"><span>Mods not in library</span></span>
                </header>
                <ul className="storage-rows">
                  {recoverable.map((item) => (
                    <li className="storage-row" key={item.id}>
                      <span className="storage-row__main">
                        <span className="storage-row__text" title={item.path}>
                          <strong>{itemName(item)}</strong>
                          {itemFileLabel(item) && <small>{itemFileLabel(item)}</small>}
                        </span>
                      </span>
                      <span className="storage-size">{formatBytes(item.logicalBytes)}</span>
                      <RevealButton label={fileManagerLabel} name={itemName(item)} onClick={() => reveal(item.path)} />
                      <Button
                        tone="quiet"
                        icon="plus"
                        onClick={() => { setRecoveryReview(item); setPhase("recover-confirm"); }}
                      >
                        Add to library
                      </Button>
                    </li>
                  ))}
                </ul>
              </section>
            )}

            {warnings.length > 0 && (
              <details className="storage-problems">
                <summary>
                  <Icon name="warning" size={14} />
                  Scan problems ({warnings.length.toLocaleString()})
                </summary>
                <ul>
                  {warnings.map((warning) => <li key={warning}>{warning}</li>)}
                </ul>
              </details>
            )}
          </>
        )}

        {phase === "confirming" && (
          <div className="storage-confirm">
            <strong>Remove {count(selected.size, "file")}</strong>
            <p>
              {selectedBytes > 0
                ? `Deleted permanently, frees up to ${formatBytes(selectedBytes)}`
                : "Deleted permanently"}
            </p>
            <p>Library mods unaffected</p>
            <ul className="storage-confirm__list">
              {selectedItems.map((item) => (
                <li key={item.id}>
                  <span title={item.path}>{itemName(item)}</span>
                  <span className="storage-size">{formatBytes(item.reclaimableBytes)}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {phase === "recover-confirm" && recoveryReview && (
          <div className="storage-confirm">
            <strong>Add {itemName(recoveryReview)} to library</strong>
            {recoveryReview.logicalBytes > 0 && (
              <p>Uses up to {formatBytes(recoveryReview.logicalBytes)} of disk space</p>
            )}
          </div>
        )}

        {phase === "done" && result && <CleanupResult result={result} operation={operation} />}
      </div>
    </CollectionDialog>
  );
}

function RevealButton({ label, name, onClick }: { label: string; name: string; onClick: () => void }) {
  return (
    <button type="button" className="icon-button storage-row__reveal" title={label} aria-label={`${label}: ${name}`} onClick={onClick}>
      <Icon name="folder" size={15} />
    </button>
  );
}

/* ── Cleanup result ── */

function CleanupResult({ result, operation }: { result: StorageCleanupResult; operation: Operation }) {
  const failures = result.failures ?? [];
  return (
    <div className="storage-result">
      <div className="storage-result__header">
        <Icon name={failures.length > 0 ? "warning" : "check"} size={18} />
        <strong>{resultHeading(result, operation)}</strong>
      </div>
      {result.reclaimedBytes > 0 && (
        <p className="storage-result__freed">
          {result.reclaimedEstimate ? "About " : ""}{formatBytes(result.reclaimedBytes)} freed
        </p>
      )}
      {failures.length > 0 && (
        <details className="storage-problems" open>
          <summary>
            <Icon name="warning" size={14} />
            Problems ({failures.length.toLocaleString()})
          </summary>
          <ul>
            {failures.map((failure) => <li key={failure}>{failure}</li>)}
          </ul>
        </details>
      )}
    </div>
  );
}
