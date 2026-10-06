import { useCallback, useEffect, useRef, useState } from "react";
import { CancelError, Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import { CollectionDialog } from "./CollectionUI";
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
  return typeof error === "string" ? error : "An unexpected error occurred.";
}

const classificationLabels: Record<string, string> = {
  "canonical-source": "Canonical source",
  "legacy-cache-redundant": "Redundant legacy cache",
  "legacy-cache-only": "Retained recovery archive",
  "collection-mirror": "Collection mirror",
  "managed-deployment": "Game deployment",
  "user-export": "User export",
  unknown: "Unrecognized — preserved",
};
const protectedCategoryLabels: Record<string, string> = {
  metadata: "App data and migration snapshots",
  thumbnails: "Thumbnails",
  workspaces: "Editable workspaces",
  exports: "Explicit exports",
  backups: "Backups",
  "game-data": "BeamNG user data",
};

function classificationTone(classification: string): "danger" | "warning" | "neutral" | "success" | "cyan" {
  if (classification === "legacy-cache-redundant") return "warning";
  if (classification === "legacy-cache-only") return "cyan";
  if (classification === "canonical-source") return "success";
  return "neutral";
}

function shortenPath(path: string): string {
  if (path.length <= 60) return path;
  const parts = path.replace(/\\/g, "/").split("/");
  if (parts.length <= 3) return path;
  return `${parts[0]}/\u2026/${parts.slice(-2).join("/")}`;
}

/* ── Types ── */

interface StorageReviewProps {
  onClose: () => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
  onRefreshLibrary: () => void;
}

type Phase = "idle" | "auditing" | "ready" | "confirming" | "recover-confirm" | "applying" | "done";

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
  const mountedRef = useRef(true);
  const auditVersionRef = useRef(0);
  const requestRef = useRef<{ cancel(): void } | null>(null);
  const operationRef = useRef<"audit" | "mutation" | null>(null);
  const operationIDRef = useRef("");
  const handledResultRef = useRef(false);
  const [cancelling, setCancelling] = useState(false);

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
    const failures = completed.failures ?? [];
    onNotify(
      failures.length > 0
        ? `Storage operation partially complete. ${failures.length} issue${failures.length === 1 ? "" : "s"} require review.`
        : completed.recovered > 0
          ? "Archive recovered to the canonical library."
          : "Storage cleanup complete.",
      failures.length > 0 ? "error" : "success",
    );
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
      setError("Storage audit cancelled. No files were changed.");
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

  const toggleItem = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const selectAllCleanable = () => {
    if (!audit?.items) return;
    const ids = audit.items
      .filter((item) => item.cleanupAllowed)
      .map((item) => item.id);
    setSelected(new Set(ids));
  };

  const clearSelection = () => {
    setSelected(new Set());
  };

  const confirmCleanup = () => {
    if (selected.size === 0) return;
    setPhase("confirming");
  };

  const beginMutation = () => {
    handledResultRef.current = false;
    operationRef.current = "mutation";
    operationIDRef.current = "";
    setPhase("applying");
    setError("");
    setProgress(null);
    setCancelling(false);
  };

  const applyCleanup = async () => {
    if (!audit || selected.size === 0 || operationRef.current) return;
    beginMutation();
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
    beginMutation();
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

  const items = audit?.items ?? [];
  const cleanableItems = items.filter((item) => item.cleanupAllowed);
  const selectedBytes = items
    .filter((item) => selected.has(item.id))
    .reduce((sum, item) => sum + item.reclaimableBytes, 0);

  const close = () => {
    if (phase === "applying") return;
    onClose();
  };

  return (
    <CollectionDialog
      title="Review storage"
      wide
      onClose={close}
      footer={
        phase === "done" ? (
          <>
            <Button onClick={() => void runAudit()}>Audit again</Button>
            <Button onClick={close}>Close</Button>
          </>
        ) : phase === "confirming" ? (
          <>
            <Button
              tone="quiet"
              onClick={() => setPhase("ready")}
            >
              Back
            </Button>
            <Button
              tone="danger"
              icon="trash"
              onClick={() => void applyCleanup()}
            >
              Remove {selected.size.toLocaleString()} item
              {selected.size === 1 ? "" : "s"}
            </Button>
          </>
        ) : (
          <>
            <span className="storage-footer-status" role="status" aria-live="polite">
              {phase === "auditing"
                ? "Scanning storage\u2026"
                : phase === "applying"
                  ? cancelling ? "Stopping safely\u2026" : "Updating storage\u2026"
                  : selected.size > 0
                    ? `${selected.size.toLocaleString()} selected \u00b7 ${formatBytes(selectedBytes)} potentially reclaimable`
                    : items.length > 0
                      ? `${items.length.toLocaleString()} items found`
                      : ""}
            </span>
            {(phase === "auditing" || phase === "applying") && (
              <Button tone="quiet" disabled={cancelling} onClick={cancelOperation}>
                {cancelling ? "Stopping safely\u2026" : "Cancel"}
              </Button>
            )}
            {phase === "recover-confirm" && recoveryReview && (
              <>
                <Button tone="quiet" onClick={() => setPhase("ready")}>Back</Button>
                <Button tone="primary" onClick={() => void recoverItem(recoveryReview.id)}>Recover archive</Button>
              </>
            )}
            <Button onClick={close} disabled={phase === "applying"}>
              Close
            </Button>
            {phase === "ready" && (
              <Button
                tone="primary"
                icon="trash"
                disabled={selected.size === 0}
                onClick={confirmCleanup}
              >
                Review selection
              </Button>
            )}
          </>
        )
      }
    >
      <div className="storage-review">
        {/* Error */}
        {error && (
          <div className="storage-error" role="alert">
            <Icon name="error" size={15} />
            <span>{error}</span>
            <Button tone="quiet" icon="refresh" onClick={() => void runAudit()}>
              Retry
            </Button>
          </div>
        )}

        {/* Auditing spinner */}
        {phase === "auditing" && (
          <div className="storage-loading">
            <Spinner />
            <span>Auditing archive storage\u2026</span>
            <p className="storage-loading__note">
              This may check file identities and hashes. It does not modify any
              files.
            </p>
          </div>
        )}

        {/* Applying progress */}
        {phase === "applying" && progress && (
          <div className="storage-progress">
            <Spinner />
            <span>{progress.current || progress.phase || "Working\u2026"}</span>
            {progress.total > 0 && (
              <progress
                value={progress.completed}
                max={progress.total}
              />
            )}
            {progress.error && (
              <p className="storage-progress__error">{progress.error}</p>
            )}
          </div>
        )}
        {phase === "applying" && !progress && (
          <div className="storage-loading">
            <Spinner />
            <span>Updating archive storage\u2026</span>
          </div>
        )}

        {/* Audit summary */}
        {audit && phase !== "auditing" && phase !== "applying" && (
          <>
            {phase === "ready" && <StorageMetrics audit={audit} />}

            {phase === "ready" && (audit.warnings ?? []).length > 0 && (
              <div className="storage-warnings">
                {(audit.warnings ?? []).map((w) => (
                  <div className="storage-warning-row" key={w}>
                    <Icon name="warning" size={14} />
                    <span>{w}</span>
                  </div>
                ))}
              </div>
            )}

            {phase === "recover-confirm" && recoveryReview && (
              <div className="storage-confirm">
                <div className="storage-confirm__header">
                  <Icon name="archive" size={18} />
                  <div>
                    <strong>Recover this archive to the library?</strong>
                    <p>
                      {recoveryReview.logicalBytes > 0
                        ? `Needs up to ${formatBytes(recoveryReview.logicalBytes)} of free space if it cannot be hardlinked. `
                        : ""}
                      The original stays in place until the library copy is verified and indexed. Nothing is enabled or launched.
                    </p>
                  </div>
                </div>
                <ul className="storage-confirm__list">
                  <li><span title={recoveryReview.path}>{shortenPath(recoveryReview.path)}</span></li>
                </ul>
              </div>
            )}
            {/* Confirmation screen */}
            {phase === "confirming" && (
              <div className="storage-confirm">
                <div className="storage-confirm__header">
                  <Icon name="warning" size={18} />
                  <div>
                    <strong>
                      Remove {selected.size.toLocaleString()} item
                      {selected.size === 1 ? "" : "s"}?
                    </strong>
                    <p>
                      {selectedBytes > 0
                        ? `Permanently removes the selected derived archives. Up to ${formatBytes(selectedBytes)} may be reclaimed; other hardlinks or open handles can retain the data.`
                        : "Selected items will be permanently unlinked."}
                    </p>
                  </div>
                </div>
                <ul className="storage-confirm__list">
                  {items
                    .filter((item) => selected.has(item.id))
                    .map((item) => (
                      <li key={item.id}>
                        <span title={item.path}>{shortenPath(item.path)}</span>
                        <span className={`badge badge--${classificationTone(item.classification)}`}>
                          {classificationLabels[item.classification] ?? "Retained archive"}
                        </span>
                        {item.reclaimableBytes > 0 && (
                          <span className="storage-item-size">
                            {formatBytes(item.reclaimableBytes)}
                          </span>
                        )}
                      </li>
                    ))}
                </ul>
              </div>
            )}

            {/* Done result */}
            {phase === "done" && result && (
              <CleanupResult result={result} />
            )}

            {/* Item list */}
            {(phase === "ready") && (
              <>
                <div className="storage-toolbar">
                  <Button
                    tone="quiet"
                    disabled={cleanableItems.length === 0}
                    onClick={selectAllCleanable}
                  >
                    Select all cleanable
                  </Button>
                  <Button
                    tone="quiet"
                    disabled={selected.size === 0}
                    onClick={clearSelection}
                  >
                    Clear selection
                  </Button>
                </div>

                {items.length === 0 ? (
                  <p className="storage-empty">
                    No archive files were found in the audited locations. Check any warnings above for unavailable or excluded paths.
                  </p>
                ) : (
                  <div className="storage-item-list" role="list">
                    {items.map((item) => (
                      <StorageItemRow
                        key={item.id}
                        item={item}
                        checked={selected.has(item.id)}
                        onToggle={() => toggleItem(item.id)}
                        onRecover={item.recoveryAllowed
                          ? () => { setRecoveryReview(item); setPhase("recover-confirm"); }
                          : undefined}
                      />
                    ))}
                  </div>
                )}
              </>
            )}
          </>
        )}
      </div>
    </CollectionDialog>
  );
}

/* ── Metrics ── */

function StorageMetrics({ audit }: { audit: StorageAudit }) {
  const estimated = audit.allocationEstimated;
  return (
    <><dl className="storage-metrics">
      <MetricCard
        label="Apparent size"
        value={formatBytes(audit.apparentBytes)}
        detail="Total path-counted size"
      />
      <MetricCard
        label="Unique allocated"
        value={`${estimated ? "≈ " : ""}${formatBytes(audit.uniqueAllocatedBytes)}`}
        detail={estimated ? "Estimate; some identities or allocations unavailable" : "Deduplicated by file identity"}
      />
      <MetricCard
        label="Shared"
        value={formatBytes(audit.sharedBytes)}
        detail="Data shared via hardlinks"
      />
      <MetricCard
        label="Required copies"
        value={formatBytes(audit.requiredCopyBytes)}
        detail="Independent deployment copies"
      />
      <MetricCard
        label="Redundant"
        value={formatBytes(audit.redundantBytes)}
        detail="Potentially reclaimable"
        tone="danger"
      />
      <MetricCard
        label="Retained"
        value={formatBytes(audit.retainedBytes)}
        detail="Preserved for review"
        tone="warning"
      />
    </dl>
    <details className="storage-protected">
      <summary>Protected locations included in this inventory</summary>
      <p>These locations are not offered for cleanup. Sizes below count file names; shared data is counted only once in the overall allocation total. Links outside the audited locations may retain data.</p>
      {(audit.protectedCategories ?? []).map((category) => (
        <div className="storage-protected__row" key={`${category.category}:${category.root}`}>
          <span><strong>{protectedCategoryLabels[category.category] ?? category.category}</strong><small>{category.root}</small></span>
          <span>{formatBytes(category.apparentBytes)}<small>{category.fileCount.toLocaleString()} files{category.unknownFiles > 0 ? ` · ${category.unknownFiles} unmeasured` : ""}</small></span>
        </div>
      ))}
    </details></>
  );
}

function MetricCard({
  label,
  value,
  detail,
  tone,
}: {
  label: string;
  value: string;
  detail: string;
  tone?: "danger" | "warning";
}) {
  return (
    <div className={`storage-metric${tone ? ` storage-metric--${tone}` : ""}`}>
      <dt>{label}</dt>
      <dd>{value}</dd>
      <small>{detail}</small>
    </div>
  );
}

/* ── Item row ── */

function StorageItemRow({
  item,
  checked,
  onToggle,
  onRecover,
}: {
  item: StorageAuditItem;
  checked: boolean;
  onToggle: () => void;
  onRecover?: () => void;
}) {
  const tone = classificationTone(item.classification);
  return (
    <div
      className={`storage-item${checked ? " is-selected" : ""}${!item.cleanupAllowed && !item.recoveryAllowed ? " is-protected" : ""}`}
      role="listitem"
    >
      <label className="storage-item__check">
        <input
          type="checkbox"
          checked={checked}
          disabled={!item.cleanupAllowed}
          onChange={onToggle}
          aria-label={`Select ${item.path}`}
        />
      </label>
      <div className="storage-item__info">
        <span className="storage-item__path" title={item.path}>
          {shortenPath(item.path)}
        </span>
        <span className="storage-item__meta">
          <span className={`badge badge--${tone}`}>
            {classificationLabels[item.classification] ?? "Retained archive"}
          </span>
          {item.logicalBytes > 0 && (
            <span>{formatBytes(item.logicalBytes)}</span>
          )}
          {item.linkCount > 1 && (
            <span>{item.linkCount} links</span>
          )}
        </span>
        {item.reason && (
          <span className="storage-item__reason">{item.reason}</span>
        )}
        {item.sourcePath && item.sourcePath !== item.path && (
          <span className="storage-item__source" title={item.sourcePath}>
            Source: {shortenPath(item.sourcePath)}
          </span>
        )}
      </div>
      <div className="storage-item__actions">
        {item.reclaimableBytes > 0 && (
          <span className="storage-item__reclaim">
            {formatBytes(item.reclaimableBytes)}
          </span>
        )}
        {onRecover && (
          <Button tone="quiet" onClick={onRecover}>
            Recover
          </Button>
        )}
      </div>
    </div>
  );
}

/* ── Cleanup result ── */

function CleanupResult({ result }: { result: StorageCleanupResult }) {
  const failures = result.failures ?? [];
  const retained = result.retainedPaths ?? [];
  return (
    <div className="storage-result">
      <div className="storage-result__header">
        <Icon
          name={failures.length > 0 ? "warning" : "check"}
          size={18}
        />
        <strong>
          {failures.length > 0
            ? "Partially complete"
            : result.recovered > 0 && result.removedLinks + result.removedCopies === 0
              ? "Recovery complete"
              : "Cleanup complete"}
        </strong>
      </div>
      <dl className="storage-result__stats">
        {result.removedLinks > 0 && (
          <div>
            <dt>Links removed</dt>
            <dd>{result.removedLinks.toLocaleString()}</dd>
          </div>
        )}
        {result.removedCopies > 0 && (
          <div>
            <dt>Copies removed</dt>
            <dd>{result.removedCopies.toLocaleString()}</dd>
          </div>
        )}
        {result.recovered > 0 && (
          <div>
            <dt>Recovered</dt>
            <dd>{result.recovered.toLocaleString()}</dd>
          </div>
        )}
        {result.reclaimedBytes > 0 && (
          <div>
            <dt>{result.reclaimedEstimate ? "Estimated reclaim" : "Reclaimed"}</dt>
            <dd>{formatBytes(result.reclaimedBytes)}</dd>
          </div>
        )}
      </dl>
      {result.reclaimedEstimate && result.reclaimedBytes > 0 && (
        <p className="storage-result__note">
          Actual disk space freed depends on remaining hardlink references and
          Recycle Bin contents.
        </p>
      )}
      {failures.length > 0 && (
        <details className="storage-result__failures">
          <summary>
            {failures.length} item{failures.length === 1 ? "" : "s"} need
            review
          </summary>
          <ul>
            {failures.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </details>
      )}
      {retained.length > 0 && (
        <details className="storage-result__retained">
          <summary>
            {retained.length} path{retained.length === 1 ? "" : "s"} retained
          </summary>
          <ul>
            {retained.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
