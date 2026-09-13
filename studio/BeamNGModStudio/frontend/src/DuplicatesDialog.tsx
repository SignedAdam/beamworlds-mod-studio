import { useEffect, useRef, useState } from "react";
import type { MutableRefObject } from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ArchiveFileRemovalImpact,
  ModFamily,
  ModFamilyMember,
  ModRemovalImpact,
  ModRemovalResult,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { CollectionDialog } from "./CollectionUI";
import { Button, formatBytes, formatDate } from "./ui";
import "./DuplicatesDialog.css";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type PlanMode = "delete" | "files";
type FamilyImpact = ArchiveFileRemovalImpact | ModRemovalImpact;

type ResolvedFamily = {
  mode: PlanMode;
  keeperID: string;
  keeperLabel: string;
  impact: FamilyImpact;
  result: ModRemovalResult;
  error?: string;
};

interface DuplicatesDialogProps {
  families: ModFamily[];
  focusFamilyID?: string;
  onClose: () => void;
  onRefresh: () => Promise<void>;
  onFamiliesChange: (families: ModFamily[]) => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  if (
    error &&
    typeof error === "object" &&
    "message" in error &&
    typeof error.message === "string" &&
    error.message.trim()
  ) {
    return error.message;
  }
  return "Something went wrong.";
}

function membersOf(family: ModFamily): ModFamilyMember[] {
  return family.members ?? [];
}

function memberSelectionID(family: ModFamily, member: ModFamilyMember): string {
  return family.confidence === "identical"
    ? member.linkId || member.entityId
    : member.entityId;
}

function proposedKeeper(family: ModFamily): string {
  const members = membersOf(family);
  const keeper = members.find((m) => m.keeper) ?? members[0];
  return keeper ? memberSelectionID(family, keeper) : "";
}

function memberIdentity(family: ModFamily, member?: ModFamilyMember): string {
  if (!member) return "Unavailable";
  if (family.confidence === "identical") {
    return archiveFilename(member.archivePath || "") || "Unavailable";
  }
  return member.displayName || member.title || "Unnamed";
}

function archiveFilename(path: string): string {
  const sep = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"));
  return sep >= 0 ? path.slice(sep + 1) : path;
}

function archiveDirectory(path: string): string {
  const sep = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"));
  return sep >= 0 ? path.slice(0, sep + 1) : "";
}

function memberVersion(member: ModFamilyMember): string {
  return typeof member.version === "string" ? member.version.trim() : "";
}

function removalTargets(family: ModFamily, keeperID: string): string[] {
  return membersOf(family)
    .filter((m) => memberSelectionID(family, m) !== keeperID)
    .map((m) => memberSelectionID(family, m));
}

function familyReclaimableBytes(family: ModFamily, keeperID: string): number {
  return membersOf(family)
    .filter((m) => memberSelectionID(family, m) !== keeperID)
    .reduce(
      (sum, m) => sum + (Number.isFinite(m.sizeBytes) ? Math.max(0, m.sizeBytes) : 0),
      0,
    );
}

function emptyArchiveFileRemovalImpact(): ArchiveFileRemovalImpact {
  return { files: [], refusals: [], archiveCount: 0, archiveBytes: 0 };
}

function emptyModRemovalImpact(): ModRemovalImpact {
  return { mods: [], collections: [], workspaces: [], archiveCount: 0, archiveBytes: 0 };
}

function emptyModRemovalResult(): ModRemovalResult {
  return { forgotten: 0, recycled: 0, failures: [] };
}

function impactRefusal(family: ModFamily, impact: FamilyImpact): string | undefined {
  if (family.confidence === "identical") {
    const refusals = (impact as ArchiveFileRemovalImpact).refusals ?? [];
    return refusals.length > 0
      ? `Can't delete: ${refusals.join(", ")}`
      : undefined;
  }
  const mod = impact as ModRemovalImpact;
  const cols = mod.collections ?? [];
  if (cols.length > 0) {
    const names = cols.join(", ");
    return cols.length === 1
      ? `Part of collection ${names} \u2014 remove it first.`
      : `Part of collections ${names} \u2014 remove it first.`;
  }
  const ws = mod.workspaces ?? [];
  if (ws.length > 0) {
    return "Open in ModMaker \u2014 close the project first.";
  }
  return undefined;
}

function recycleLabel(family: ModFamily, impact: FamilyImpact): string {
  if (family.confidence === "identical") {
    const fi = impact as ArchiveFileRemovalImpact;
    const n = fi.archiveCount;
    return `Recycle ${n} ${n === 1 ? "file" : "files"} \u00b7 ${formatBytes(fi.archiveBytes)}`;
  }
  const mi = impact as ModRemovalImpact;
  const n = mi.archiveCount;
  return `Recycle ${n} ${n === 1 ? "mod" : "mods"} \u00b7 ${formatBytes(mi.archiveBytes)}`;
}

function memberMeta(family: ModFamily, member: ModFamilyMember): string {
  if (family.confidence === "identical") return "";
  const parts: string[] = [];
  const v = memberVersion(member);
  if (v) parts.push(v);
  parts.push(formatBytes(member.sizeBytes));
  if (member.modifiedAt) parts.push(formatDate(member.modifiedAt));
  return parts.join(" \u00b7 ");
}

// ---------------------------------------------------------------------------
// Member row
// ---------------------------------------------------------------------------

function MemberRow({
  family,
  member,
  index,
  isKeeper,
  disabled,
  inputRef,
  onSelect,
  onArrow,
}: {
  family: ModFamily;
  member: ModFamilyMember;
  index: number;
  isKeeper: boolean;
  disabled: boolean;
  inputRef: (el: HTMLInputElement | null) => void;
  onSelect: () => void;
  onArrow: (direction: -1 | 1) => void;
}) {
  const identical = family.confidence === "identical";
  const path = typeof member.archivePath === "string" ? member.archivePath.trim() : "";
  const name = archiveFilename(path) || member.displayName || member.title || "Unnamed";
  const meta = memberMeta(family, member);
  const dir = identical ? archiveDirectory(path) : "";
  const inGame = member.installedInGame === true;
  const reason = isKeeper ? (member.keeperReason || "") : "";

  return (
    <label className={`dup-member${isKeeper ? " is-keeper" : ""}`}>
      <input
        ref={inputRef}
        type="radio"
        name={`dup-keeper-${family.id}`}
        checked={isKeeper}
        disabled={disabled}
        onChange={onSelect}
        onKeyDown={(e) => {
          if (e.key === "ArrowLeft" || e.key === "ArrowUp") {
            e.preventDefault();
            onArrow(-1);
          } else if (e.key === "ArrowRight" || e.key === "ArrowDown") {
            e.preventDefault();
            onArrow(1);
          }
        }}
        aria-label={`Keep ${name}`}
      />
      <div className="dup-member__body">
        <span className="dup-member__name" title={path || name}>{name}</span>
        {dir && (
          <code className="dup-member__path" title={path}>
            <span className="dup-member__dir">{dir}</span>
          </code>
        )}
        {meta && (
          <span className="dup-member__meta">
            {meta}
            {inGame && <>{" \u00b7 "}<em className="dup-member__active">In game</em></>}
          </span>
        )}
        {!meta && inGame && (
          <span className="dup-member__meta">
            <em className="dup-member__active">In game</em>
          </span>
        )}
        {reason && <small className="dup-member__reason">{reason}</small>}
      </div>
    </label>
  );
}

// ---------------------------------------------------------------------------
// Action area
// ---------------------------------------------------------------------------

function ActionArea({
  family,
  keeperID,
  impact,
  impactLoading,
  impactError,
  busy,
  error,
  resolved,
  dismissed,
  onAction,
  onDismiss,
  onRetryImpact,
  onReviewRemaining,
}: {
  family: ModFamily;
  keeperID: string;
  impact?: FamilyImpact;
  impactLoading: boolean;
  impactError?: string;
  busy: boolean;
  error?: string;
  resolved?: ResolvedFamily;
  dismissed?: boolean;
  onAction: (mode: PlanMode) => void;
  onDismiss: () => void;
  onRetryImpact: () => void;
  onReviewRemaining: () => void;
}) {
  if (resolved) {
    const bytes =
      resolved.mode === "files"
        ? (resolved.impact as ArchiveFileRemovalImpact).archiveBytes
        : (resolved.impact as ModRemovalImpact).archiveBytes;
    return (
      <div className="dup-action dup-action--done" role="status">
        <span className="dup-action__summary">
          Kept {resolved.keeperLabel} &mdash; freed {formatBytes(bytes)}
        </span>
        {resolved.error && (
          <p className="dup-action__error" role="alert" title={resolved.error}>
            {resolved.error}
          </p>
        )}
        <div className="dup-action__buttons">
          <Button type="button" onClick={onReviewRemaining}>Review remaining</Button>
        </div>
      </div>
    );
  }

  if (dismissed) {
    return (
      <div className="dup-action dup-action--done" role="status">
        <span className="dup-action__summary">Skipped</span>
      </div>
    );
  }

  if (busy) {
    return (
      <div className="dup-action" role="status" aria-live="polite">
        <span className="dup-action__summary">Deleting&hellip;</span>
      </div>
    );
  }

  if (impactError && !impact) {
    return (
      <div className="dup-action" role="status">
        <p className="dup-action__error" role="alert" title={impactError}>
          {impactError}
        </p>
        <div className="dup-action__buttons">
          <Button type="button" onClick={onRetryImpact}>Retry</Button>
          <Button type="button" onClick={onDismiss}>Skip</Button>
        </div>
      </div>
    );
  }

  if (impactLoading || !impact) {
    return (
      <div className="dup-action" role="status" aria-live="polite">
        <span className="dup-action__summary">Checking&hellip;</span>
      </div>
    );
  }

  const identical = family.confidence === "identical";
  const refusal = impactRefusal(family, impact);

  if (refusal) {
    return (
      <div className="dup-action">
        <p className="dup-action__refusal" role="alert" title={refusal}>{refusal}</p>
        <div className="dup-action__buttons">
          <Button type="button" onClick={onDismiss}>Skip</Button>
        </div>
      </div>
    );
  }

  return (
    <div className="dup-action">
      {error && (
        <p className="dup-action__error" role="alert" title={error}>{error}</p>
      )}
      <div className="dup-action__buttons">
        <Button
          type="button"
          tone="danger"
          disabled={busy}
          onClick={() => onAction(identical ? "files" : "delete")}
        >
          {recycleLabel(family, impact)}
        </Button>
        <Button type="button" disabled={busy} onClick={onDismiss}>Skip</Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Family card
// ---------------------------------------------------------------------------

function FamilyCard({
  family,
  keeperID,
  impact,
  impactLoading,
  impactError,
  busy,
  error,
  resolved,
  dismissed,
  onKeeperChange,
  onAction,
  onDismiss,
  onRetryImpact,
  onReviewRemaining,
}: {
  family: ModFamily;
  keeperID: string;
  impact?: FamilyImpact;
  impactLoading: boolean;
  impactError?: string;
  busy: boolean;
  error?: string;
  resolved?: ResolvedFamily;
  dismissed?: boolean;
  onKeeperChange: (selectionID: string) => void;
  onAction: (mode: PlanMode) => void;
  onDismiss: () => void;
  onRetryImpact: () => void;
  onReviewRemaining: () => void;
}) {
  const members = membersOf(family);
  const inputRefs = useRef<Record<string, HTMLInputElement | null>>({});
  const isDisabled = Boolean(resolved || dismissed || busy);

  const moveKeeper = (index: number, direction: -1 | 1) => {
    if (isDisabled || members.length < 2) return;
    const nextIndex = (index + direction + members.length) % members.length;
    const nextID = memberSelectionID(family, members[nextIndex]);
    onKeeperChange(nextID);
    window.requestAnimationFrame(() => {
      inputRefs.current[nextID]?.focus({ preventScroll: true });
    });
  };

  const reclaimable =
    keeperID === proposedKeeper(family)
      ? family.reclaimableBytes
      : familyReclaimableBytes(family, keeperID);

  return (
    <article
      className="dup-card"
      id={`duplicate-family-${family.id}`}
      data-family-id={family.id}
    >
      <header className="dup-card__header">
        <h4>{family.title || "Unnamed"}</h4>
        {reclaimable > 0 && (
          <span className="dup-card__saves">
            Frees {formatBytes(reclaimable)}
          </span>
        )}
      </header>
      <div className="dup-members">
        {members.map((member, index) => {
          const selID = memberSelectionID(family, member);
          return (
            <MemberRow
              key={selID}
              family={family}
              member={member}
              index={index}
              isKeeper={selID === keeperID}
              disabled={isDisabled}
              inputRef={(el) => {
                inputRefs.current[selID] = el;
              }}
              onSelect={() => onKeeperChange(selID)}
              onArrow={(dir) => moveKeeper(index, dir)}
            />
          );
        })}
      </div>
      <ActionArea
        family={family}
        keeperID={keeperID}
        impact={impact}
        impactLoading={impactLoading}
        impactError={impactError}
        busy={busy}
        error={error}
        resolved={resolved}
        dismissed={dismissed}
        onAction={onAction}
        onDismiss={onDismiss}
        onRetryImpact={onRetryImpact}
        onReviewRemaining={onReviewRemaining}
      />
    </article>
  );
}

// ---------------------------------------------------------------------------
// Section
// ---------------------------------------------------------------------------

function sectionHeading(confidence: string): string {
  switch (confidence) {
    case "identical": return "Extra copies";
    case "repo":      return "Same mod";
    case "content":   return "Likely duplicates";
    case "metadata":  return "Possible duplicates";
    default:          return "Duplicates";
  }
}

function sectionNote(confidence: string): string | undefined {
  switch (confidence) {
    case "content":  return "Same name and vehicles \u2014 verify before deleting.";
    case "metadata": return "Same name and author \u2014 verify before deleting.";
    default:         return undefined;
  }
}

function FamilySection({
  confidence,
  families,
  familyRefs,
  keeperByFamilyID,
  impacts,
  impactLoadingByFamilyID,
  impactErrors,
  errors,
  resolvedByFamilyID,
  dismissedByFamilyID,
  busyByFamilyID,
  highlightedFamilyID,
  onKeeperChange,
  onAction,
  onDismiss,
  onRetryImpact,
  onReviewRemaining,
}: {
  confidence: "identical" | "repo" | "content" | "metadata";
  families: ModFamily[];
  familyRefs: MutableRefObject<Record<string, HTMLElement | null>>;
  keeperByFamilyID: Record<string, string>;
  impacts: Record<string, FamilyImpact | undefined>;
  impactLoadingByFamilyID: Record<string, boolean | undefined>;
  impactErrors: Record<string, string | undefined>;
  errors: Record<string, string | undefined>;
  resolvedByFamilyID: Record<string, ResolvedFamily | undefined>;
  dismissedByFamilyID: Record<string, boolean | undefined>;
  busyByFamilyID: Record<string, boolean | undefined>;
  highlightedFamilyID: string;
  onKeeperChange: (familyID: string, selectionID: string) => void;
  onAction: (family: ModFamily, mode: PlanMode) => void;
  onDismiss: (family: ModFamily) => void;
  onRetryImpact: (family: ModFamily) => void;
  onReviewRemaining: (familyID: string) => void;
}) {
  if (families.length === 0) return null;
  const heading = sectionHeading(confidence);
  const note = sectionNote(confidence);
  return (
    <section className="dup-section" aria-labelledby={`dup-${confidence}`}>
      <h3 id={`dup-${confidence}`}>{heading}</h3>
      {note && <p className="dup-section__note">{note}</p>}
      {families.map((family) => (
        <div
          key={family.id}
          tabIndex={-1}
          className={`dup-shell${family.id === highlightedFamilyID ? " is-highlighted" : ""}`}
          ref={(el) => {
            familyRefs.current[family.id] = el;
          }}
        >
          <FamilyCard
            family={family}
            keeperID={keeperByFamilyID[family.id] ?? proposedKeeper(family)}
            impact={impacts[family.id]}
            impactLoading={Boolean(impactLoadingByFamilyID[family.id])}
            impactError={impactErrors[family.id]}
            busy={Boolean(busyByFamilyID[family.id])}
            error={errors[family.id]}
            resolved={resolvedByFamilyID[family.id]}
            dismissed={Boolean(dismissedByFamilyID[family.id])}
            onKeeperChange={(selID) => onKeeperChange(family.id, selID)}
            onAction={(mode) => onAction(family, mode)}
            onDismiss={() => onDismiss(family)}
            onRetryImpact={() => onRetryImpact(family)}
            onReviewRemaining={() => onReviewRemaining(family.id)}
          />
        </div>
      ))}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Main dialog
// ---------------------------------------------------------------------------

export function DuplicatesDialog({
  families,
  focusFamilyID,
  onClose,
  onRefresh,
  onFamiliesChange,
  onNotify,
}: DuplicatesDialogProps) {
  const [visibleFamilies] = useState<ModFamily[]>(() => families);
  const familyRefs = useRef<Record<string, HTMLElement | null>>({});
  const [keeperByFamilyID, setKeeperByFamilyID] = useState<Record<string, string>>(
    () =>
      visibleFamilies.reduce<Record<string, string>>((acc, f) => {
        acc[f.id] = proposedKeeper(f);
        return acc;
      }, {}),
  );
  const [impacts, setImpacts] = useState<Record<string, FamilyImpact | undefined>>({});
  const [impactLoadingByFamilyID, setImpactLoadingByFamilyID] = useState<
    Record<string, boolean | undefined>
  >({});
  const [impactErrors, setImpactErrors] = useState<Record<string, string | undefined>>({});
  const [impactRetryByFamilyID, setImpactRetryByFamilyID] = useState<
    Record<string, number | undefined>
  >({});
  const [busyByFamilyID, setBusyByFamilyID] = useState<Record<string, boolean | undefined>>({});
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [resolvedByFamilyID, setResolvedByFamilyID] = useState<
    Record<string, ResolvedFamily | undefined>
  >({});
  const [dismissedByFamilyID, setDismissedByFamilyID] = useState<
    Record<string, boolean | undefined>
  >({});
  const [highlightedFamilyID, setHighlightedFamilyID] = useState("");

  // --- Impact loading ---

  useEffect(() => {
    let cancelled = false;
    const loading: Record<string, boolean | undefined> = {};
    for (const family of visibleFamilies) {
      if (resolvedByFamilyID[family.id] || dismissedByFamilyID[family.id]) continue;
      loading[family.id] = true;
    }
    setImpactLoadingByFamilyID((c) => ({ ...c, ...loading }));
    setImpactErrors((c) => {
      const next = { ...c };
      for (const family of visibleFamilies) {
        if (loading[family.id]) delete next[family.id];
      }
      return next;
    });
    for (const family of visibleFamilies) {
      if (resolvedByFamilyID[family.id] || dismissedByFamilyID[family.id]) continue;
      const kid = keeperByFamilyID[family.id] ?? proposedKeeper(family);
      const targets = removalTargets(family, kid);
      void (async () => {
        try {
          let imp: FamilyImpact;
          if (targets.length === 0) {
            imp =
              family.confidence === "identical"
                ? emptyArchiveFileRemovalImpact()
                : emptyModRemovalImpact();
          } else if (family.confidence === "identical") {
            imp =
              (await API.PlanArchiveFileRemoval(targets)) ??
              emptyArchiveFileRemovalImpact();
          } else {
            imp =
              (await API.PlanModRemoval(targets)) ?? emptyModRemovalImpact();
          }
          if (cancelled) return;
          setImpacts((c) => ({ ...c, [family.id]: imp }));
          setImpactLoadingByFamilyID((c) => ({ ...c, [family.id]: false }));
        } catch (err) {
          if (cancelled) return;
          setImpactErrors((c) => ({ ...c, [family.id]: errorMessage(err) }));
          setImpactLoadingByFamilyID((c) => ({ ...c, [family.id]: false }));
        }
      })();
    }
    return () => {
      cancelled = true;
    };
  }, [
    visibleFamilies,
    keeperByFamilyID,
    impactRetryByFamilyID,
    resolvedByFamilyID,
    dismissedByFamilyID,
  ]);

  // --- Focus / highlight ---

  useEffect(() => {
    if (!focusFamilyID) return;
    const frame = window.requestAnimationFrame(() => {
      familyRefs.current[focusFamilyID]?.scrollIntoView({
        behavior: "smooth",
        block: "center",
      });
      setHighlightedFamilyID(focusFamilyID);
    });
    const timer = window.setTimeout(() => setHighlightedFamilyID(""), 2200);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, [visibleFamilies, focusFamilyID]);

  const focusFamily = (familyID: string) => {
    window.requestAnimationFrame(() => {
      familyRefs.current[familyID]?.focus({ preventScroll: true });
    });
  };

  // --- Keeper change ---

  const changeKeeper = (familyID: string, selectionID: string) => {
    setKeeperByFamilyID((c) => ({ ...c, [familyID]: selectionID }));
    setImpacts((c) => {
      const next = { ...c };
      delete next[familyID];
      return next;
    });
    setImpactLoadingByFamilyID((c) => ({ ...c, [familyID]: true }));
    setImpactErrors((c) => {
      const next = { ...c };
      delete next[familyID];
      return next;
    });
    setErrors((c) => {
      const next = { ...c };
      delete next[familyID];
      return next;
    });
  };

  const retryImpact = (family: ModFamily) => {
    setImpacts((c) => {
      const next = { ...c };
      delete next[family.id];
      return next;
    });
    setImpactLoadingByFamilyID((c) => ({ ...c, [family.id]: true }));
    setImpactRetryByFamilyID((c) => ({
      ...c,
      [family.id]: (c[family.id] ?? 0) + 1,
    }));
  };

  // --- Removal ---

  const performRemoval = async (family: ModFamily, mode: PlanMode) => {
    const fid = family.id;
    if (busyByFamilyID[fid] || resolvedByFamilyID[fid] || dismissedByFamilyID[fid])
      return;
    const impact = impacts[fid];
    if (!impact || impactRefusal(family, impact)) return;
    const kid = keeperByFamilyID[fid] ?? proposedKeeper(family);
    const targets = removalTargets(family, kid);
    if (targets.length === 0) return;

    setBusyByFamilyID((c) => ({ ...c, [fid]: true }));
    setErrors((c) => {
      const next = { ...c };
      delete next[fid];
      return next;
    });

    try {
      const result =
        mode === "files"
          ? ((await API.DeleteArchiveFiles(targets)) ?? emptyModRemovalResult())
          : ((await API.DeleteModArchives(targets)) ?? emptyModRemovalResult());

      const failures = result.failures ?? [];
      let refreshError = "";
      try {
        await onRefresh();
      } catch (err) {
        refreshError = errorMessage(err);
      }

      if (failures.length > 0) {
        const msg =
          failures.join(" \u00b7 ") + (refreshError ? ` \u00b7 ${refreshError}` : "");
        setErrors((c) => ({ ...c, [fid]: msg }));
        onNotify("Some copies could not be removed.", "error");
      } else {
        setResolvedByFamilyID((c) => ({
          ...c,
          [fid]: {
            mode,
            keeperID: kid,
            keeperLabel: memberIdentity(
              family,
              membersOf(family).find(
                (m) => memberSelectionID(family, m) === kid,
              ) ?? membersOf(family)[0],
            ),
            impact,
            result,
            error: refreshError || undefined,
          },
        }));
        if (refreshError) {
          onNotify(`Deleted, but refresh failed: ${refreshError}`, "error");
        } else {
          const n = result.recycled;
          onNotify(
            mode === "files"
              ? `Recycled ${n} ${n === 1 ? "copy" : "copies"}`
              : `Recycled ${n} ${n === 1 ? "mod" : "mods"}`,
            "success",
          );
        }
        focusFamily(fid);
      }
    } catch (err) {
      const msg = errorMessage(err);
      setErrors((c) => ({ ...c, [fid]: msg }));
      onNotify(msg, "error");
    } finally {
      setBusyByFamilyID((c) => ({ ...c, [fid]: false }));
    }
  };

  // --- Dismiss ---

  const dismissFamily = async (family: ModFamily) => {
    const fid = family.id;
    if (busyByFamilyID[fid] || resolvedByFamilyID[fid] || dismissedByFamilyID[fid])
      return;
    setBusyByFamilyID((c) => ({ ...c, [fid]: true }));
    setErrors((c) => {
      const next = { ...c };
      delete next[fid];
      return next;
    });
    try {
      const refreshed = await API.DismissModFamily(fid);
      onFamiliesChange(refreshed ?? []);
      setDismissedByFamilyID((c) => ({ ...c, [fid]: true }));
      onNotify("Dismissed.", "info");
      focusFamily(fid);
    } catch (err) {
      const msg = errorMessage(err);
      setErrors((c) => ({ ...c, [fid]: msg }));
      onNotify(msg, "error");
    } finally {
      setBusyByFamilyID((c) => ({ ...c, [fid]: false }));
    }
  };

  // --- Render ---

  const identical = visibleFamilies.filter((f) => f.confidence === "identical");
  const repo = visibleFamilies.filter((f) => f.confidence === "repo");
  const content = visibleFamilies.filter((f) => f.confidence === "content");
  const metadata = visibleFamilies.filter((f) => f.confidence === "metadata");

  const sectionProps = {
    familyRefs,
    keeperByFamilyID,
    impacts,
    impactLoadingByFamilyID,
    impactErrors,
    errors,
    resolvedByFamilyID,
    dismissedByFamilyID,
    busyByFamilyID,
    highlightedFamilyID,
    onKeeperChange: changeKeeper,
    onAction: (family: ModFamily, mode: PlanMode) => void performRemoval(family, mode),
    onDismiss: (family: ModFamily) => void dismissFamily(family),
    onRetryImpact: retryImpact,
    onReviewRemaining: focusFamily,
  };

  return (
    <CollectionDialog
      title="Duplicates"
      wide
      onClose={onClose}
      footer={
        <Button
          type="button"
          onClick={onClose}
          disabled={Object.values(busyByFamilyID).some(Boolean)}
        >
          Close
        </Button>
      }
    >
      <div className="dup-dialog">
        <FamilySection confidence="identical" families={identical} {...sectionProps} />
        <FamilySection confidence="repo" families={repo} {...sectionProps} />
        <FamilySection confidence="content" families={content} {...sectionProps} />
        <FamilySection confidence="metadata" families={metadata} {...sectionProps} />
        {visibleFamilies.length === 0 && (
          <p className="dup-dialog__empty">No duplicates found.</p>
        )}
      </div>
    </CollectionDialog>
  );
}
