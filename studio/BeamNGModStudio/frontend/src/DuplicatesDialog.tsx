import { useCallback, useEffect, useRef, useState } from "react";
import type { MutableRefObject, ReactNode } from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ArchiveFileRemovalImpact,
  ModFamily,
  ModFamilyMember,
  ModReplacementImpact,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { CollectionDialog } from "./CollectionUI";
import { Icon } from "./icons";
import type { IconName } from "./icons";
import { Button, Spinner, formatBytes, formatDate } from "./ui";
import "./DuplicatesDialog.css";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/**
 * What happens to the versions that are not kept:
 * - replace: delete them; the kept version takes over their collections,
 *   groups, and tags.
 * - remove: delete them along with their collection, group, and tag entries.
 * - keep: delete nothing and stop flagging the set.
 */
type Decision = "replace" | "remove" | "keep";

type Confidence = "identical" | "repo" | "content" | "metadata";

type FamilyImpact = ArchiveFileRemovalImpact | ModReplacementImpact;

type PlanView =
  | { status: "loading" }
  | { status: "ready"; impact: FamilyImpact }
  | { status: "error"; error: string };

type RunView = {
  state: "queued" | "running" | "done" | "failed";
  decision: Decision;
  keeperID: string;
  summary?: string;
  warning?: string;
  error?: string;
};

type Job = { family: ModFamily; decision: Decision; keeperID: string };

type Usage = { collections: string[]; groups: string[]; tags: string[] };

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

function plural(count: number, one: string, many: string): string {
  return count === 1 ? one : many;
}

function membersOf(family: ModFamily): ModFamilyMember[] {
  return family.members ?? [];
}

/** Identical files of one mod: collections belong to the mod, not the file. */
function isCopies(family: ModFamily): boolean {
  return family.confidence === "identical";
}

function memberSelectionID(family: ModFamily, member: ModFamilyMember): string {
  return isCopies(family) ? member.linkId || member.entityId : member.entityId;
}

function proposedKeeper(family: ModFamily): string {
  const members = membersOf(family);
  const keeper = members.find((m) => m.keeper) ?? members[0];
  return keeper ? memberSelectionID(family, keeper) : "";
}

function archiveFilename(path: string): string {
  const sep = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"));
  return sep >= 0 ? path.slice(sep + 1) : path;
}

function archiveDirectory(path: string): string {
  const sep = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"));
  return sep >= 0 ? path.slice(0, sep + 1) : "";
}

function memberName(member: ModFamilyMember): string {
  const path = (member.archivePath ?? "").trim();
  return archiveFilename(path) || member.displayName || member.title || "Unnamed";
}

function memberVersion(member: ModFamilyMember): string {
  const version =
    typeof member.version === "string" ? member.version.trim() : "";
  return /^\d/.test(version) ? `v${version}` : version;
}

function removalTargets(family: ModFamily, keeperID: string): string[] {
  return membersOf(family)
    .map((m) => memberSelectionID(family, m))
    .filter((id) => id !== keeperID);
}

function otherBytes(family: ModFamily, keeperID: string): number {
  return membersOf(family)
    .filter((m) => memberSelectionID(family, m) !== keeperID)
    .reduce(
      (sum, m) =>
        sum + (Number.isFinite(m.sizeBytes) ? Math.max(0, m.sizeBytes) : 0),
      0,
    );
}

function planKey(familyID: string, keeperID: string): string {
  return `${familyID}\u0000${keeperID}`;
}

function impactRefusal(
  family: ModFamily,
  impact: FamilyImpact,
): string | undefined {
  const refusals = impact.refusals ?? [];
  if (refusals.length === 0) return undefined;
  return isCopies(family) ? refusals.join(", ") : refusals.join(" \u00b7 ");
}

function usageOf(member: ModFamilyMember): Usage {
  return {
    collections: member.collections ?? [],
    groups: member.groups ?? [],
    tags: member.tags ?? [],
  };
}

function usageCount(usage: Usage): number {
  return usage.collections.length + usage.groups.length + usage.tags.length;
}

/**
 * Places the other versions are in and the kept version is not. Replace hands
 * exactly these to the kept version; Remove makes the mod leave them.
 */
function placesOnlyOthersHave(family: ModFamily, keeperID: string): Usage {
  const members = membersOf(family);
  const keeper = members.find((m) => memberSelectionID(family, m) === keeperID);
  const kept = keeper ? usageOf(keeper) : { collections: [], groups: [], tags: [] };
  const result: Usage = { collections: [], groups: [], tags: [] };
  for (const member of members) {
    if (memberSelectionID(family, member) === keeperID) continue;
    const usage = usageOf(member);
    for (const kind of ["collections", "groups", "tags"] as const) {
      for (const name of usage[kind]) {
        if (!kept[kind].includes(name) && !result[kind].includes(name)) {
          result[kind].push(name);
        }
      }
    }
  }
  return result;
}

function namesList(names: string[], max = 3): ReactNode {
  const shown = names.slice(0, max);
  const hidden = names.length - shown.length;
  const parts: ReactNode[] = shown.map((name) => (
    <strong key={name}>{name}</strong>
  ));
  if (hidden > 0) parts.push(`${hidden} more`);
  return parts.map((part, index) => (
    <span key={index}>
      {index > 0 && (index === parts.length - 1 ? " and " : ", ")}
      {part}
    </span>
  ));
}

/** "RLS and Terrain collections, Pixar Cars group and Car tag". */
function placesPhrase(usage: Usage): ReactNode {
  const phrases: ReactNode[] = [];
  if (usage.collections.length > 0) {
    phrases.push(
      <>
        the {namesList(usage.collections)}{" "}
        {plural(usage.collections.length, "collection", "collections")}
      </>,
    );
  }
  if (usage.groups.length > 0) {
    phrases.push(
      <>
        the {namesList(usage.groups)}{" "}
        {plural(usage.groups.length, "group", "groups")}
      </>,
    );
  }
  if (usage.tags.length > 0) {
    phrases.push(
      <>
        the {namesList(usage.tags)} {plural(usage.tags.length, "tag", "tags")}
      </>,
    );
  }
  return phrases.map((phrase, index) => (
    <span key={index}>
      {index > 0 && (index === phrases.length - 1 ? " and " : ", ")}
      {phrase}
    </span>
  ));
}

type DecisionOption = { value: Decision; label: string; title: string };

function decisionOptions(family: ModFamily): DecisionOption[] {
  const count = membersOf(family).length;
  const keep: DecisionOption = {
    value: "keep",
    label: count === 2 ? "Keep both" : `Keep all ${count}`,
    title: "Delete nothing and stop flagging this set.",
  };
  if (isCopies(family)) {
    return [
      {
        value: "remove",
        label: "Remove copies",
        title:
          "Delete the extra copies of this file. Collections, groups and tags are not affected.",
      },
      keep,
    ];
  }
  return [
    {
      value: "replace",
      label: "Replace",
      title:
        "Delete the other versions. The kept version takes their place in collections, groups and tags.",
    },
    {
      value: "remove",
      label: "Remove",
      title:
        "Delete the other versions and take them out of their collections, groups and tags. The kept version stays as it is.",
    },
    keep,
  ];
}

function isLocked(run?: RunView): boolean {
  return Boolean(run && run.state !== "failed");
}

// ---------------------------------------------------------------------------
// Usage chips (on each version row)
// ---------------------------------------------------------------------------

const USAGE_KINDS: {
  kind: keyof Usage;
  icon: IconName;
  label: string;
}[] = [
  { kind: "collections", icon: "mixed", label: "Collection" },
  { kind: "groups", icon: "folder", label: "Group" },
  { kind: "tags", icon: "tag", label: "Tag" },
];

function UsageChips({ usage, gained }: { usage: Usage; gained?: Usage }) {
  const total = usageCount(usage) + (gained ? usageCount(gained) : 0);
  if (total === 0) {
    return (
      <span className="dup-usage dup-usage--none">
        Not in any collection, group or tag
      </span>
    );
  }
  return (
    <span className="dup-usage">
      {USAGE_KINDS.map(({ kind, icon, label }) => (
        <span key={kind} className="dup-usage__kind">
          {usage[kind].map((name) => (
            <span
              key={name}
              className={`dup-chip dup-chip--${kind}`}
              title={`${label}: ${name}`}
            >
              <Icon name={icon} size={12} />
              <span>{name}</span>
            </span>
          ))}
          {gained?.[kind].map((name) => (
            <span
              key={`+${name}`}
              className={`dup-chip dup-chip--${kind} dup-chip--gained`}
              title={`${label}: ${name} (taken over from the other versions)`}
            >
              <Icon name={icon} size={12} />
              <span>+ {name}</span>
            </span>
          ))}
        </span>
      ))}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Version row
// ---------------------------------------------------------------------------

function MemberRow({
  family,
  member,
  isKeeper,
  decision,
  gained,
  locked,
  onSelect,
}: {
  family: ModFamily;
  member: ModFamilyMember;
  isKeeper: boolean;
  decision?: Decision;
  gained?: Usage;
  locked: boolean;
  onSelect: () => void;
}) {
  const copies = isCopies(family);
  const path = (member.archivePath ?? "").trim();
  const name = memberName(member);
  const deleted = !isKeeper && (decision === "replace" || decision === "remove");
  const meta: string[] = [];
  if (!copies) {
    const version = memberVersion(member);
    if (version) meta.push(version);
    meta.push(formatBytes(member.sizeBytes));
    if (member.modifiedAt) meta.push(formatDate(member.modifiedAt));
  }
  const suggestion = member.keeper && member.keeperReason
    ? `Suggested: ${member.keeperReason}`
    : "";

  return (
    <label
      className={`dup-member${isKeeper ? " is-keeper" : ""}${deleted ? " is-deleted" : ""}`}
    >
      <input
        type="radio"
        name={`dup-keeper-${family.id}`}
        checked={isKeeper}
        disabled={locked}
        onChange={onSelect}
        aria-label={`Keep ${name}`}
      />
      <span className="dup-member__body">
        <span className="dup-member__line">
          <span className="dup-member__name" title={path || name}>
            {name}
          </span>
          {meta.length > 0 && (
            <span className="dup-member__meta">{meta.join(" \u00b7 ")}</span>
          )}
          {member.installedInGame && (
            <span className="dup-member__active">In game</span>
          )}
          {suggestion && (
            <span className="dup-member__reason">{suggestion}</span>
          )}
        </span>
        {copies ? (
          <code className="dup-member__path" title={path}>
            <span className="dup-member__dir">{archiveDirectory(path)}</span>
          </code>
        ) : (
          <UsageChips usage={usageOf(member)} gained={gained} />
        )}
      </span>
      <span
        className={`dup-member__fate${deleted ? " dup-member__fate--delete" : ""}`}
      >
        {isKeeper || decision === "keep" ? "Keep" : deleted ? "Delete" : ""}
      </span>
    </label>
  );
}

// ---------------------------------------------------------------------------
// Outcome line: what the chosen action will do, in plain words
// ---------------------------------------------------------------------------

function Outcome({
  family,
  decision,
  keeperID,
  plan,
  onRetry,
}: {
  family: ModFamily;
  decision?: Decision;
  keeperID: string;
  plan?: PlanView;
  onRetry: () => void;
}) {
  if (plan?.status === "error") {
    return (
      <p className="dup-outcome dup-outcome--error" role="alert">
        Couldn&rsquo;t check this set: {plan.error}{" "}
        <button type="button" className="dup-link" onClick={onRetry}>
          Try again
        </button>
      </p>
    );
  }
  const refusal =
    plan?.status === "ready" ? impactRefusal(family, plan.impact) : undefined;
  if (refusal) {
    return (
      <p className="dup-outcome dup-outcome--blocked">
        Can&rsquo;t delete from this set: {refusal}
      </p>
    );
  }
  if (!decision) return null;

  const count = membersOf(family).length;
  if (decision === "keep") {
    return (
      <p className="dup-outcome">
        Nothing is deleted. {count === 2 ? "This pair" : `These ${count}`}{" "}
        won&rsquo;t be flagged again unless the set changes.
      </p>
    );
  }

  const others = count - 1;
  const bytes =
    plan?.status === "ready"
      ? plan.impact.archiveBytes
      : otherBytes(family, keeperID);
  const size = bytes > 0 ? ` (${formatBytes(bytes)})` : "";
  if (isCopies(family)) {
    return (
      <p className="dup-outcome">
        Deletes {others} extra {plural(others, "copy", "copies")}
        {size}. Collections, groups and tags are not affected.
      </p>
    );
  }

  const subject =
    others === 1 ? "the other version" : `the other ${others} versions`;
  const places = placesOnlyOthersHave(family, keeperID);
  if (usageCount(places) === 0) {
    return (
      <p className="dup-outcome">
        Deletes {subject}
        {size}. {others === 1 ? "It isn\u2019t" : "They aren\u2019t"} in any
        collection, group or tag the kept version isn&rsquo;t already in.
      </p>
    );
  }
  if (decision === "replace") {
    return (
      <p className="dup-outcome">
        Deletes {subject}
        {size}. The kept version takes {others === 1 ? "its" : "their"} place
        in {placesPhrase(places)}.
      </p>
    );
  }
  return (
    <p className="dup-outcome">
      Deletes {subject}
      {size} and takes this mod out of {placesPhrase(places)}. The kept
      version stays where it is.
    </p>
  );
}

// ---------------------------------------------------------------------------
// Family card
// ---------------------------------------------------------------------------

function FamilyCard({
  family,
  keeperID,
  decision,
  plan,
  run,
  busyReloading,
  onKeeperChange,
  onDecisionChange,
  onRetryPlan,
  onCheckAgain,
}: {
  family: ModFamily;
  keeperID: string;
  decision?: Decision;
  plan?: PlanView;
  run?: RunView;
  busyReloading: boolean;
  onKeeperChange: (selectionID: string) => void;
  onDecisionChange: (decision?: Decision) => void;
  onRetryPlan: () => void;
  onCheckAgain: () => void;
}) {
  const members = membersOf(family);
  const locked = isLocked(run);
  const copies = isCopies(family);
  const refused =
    plan?.status === "ready" && Boolean(impactRefusal(family, plan.impact));
  const gained =
    decision === "replace" && !copies
      ? placesOnlyOthersHave(family, keeperID)
      : undefined;

  if (run?.state === "done") {
    return (
      <article className="dup-card dup-card--done" data-family-id={family.id}>
        <header className="dup-card__header">
          <h4>{family.title || "Unnamed"}</h4>
          <span className="dup-card__status dup-card__status--done">
            <Icon name="check" size={14} />
            Done
          </span>
        </header>
        <p className="dup-card__result">{run.summary}</p>
        {run.warning && (
          <p className="dup-card__result dup-card__result--warning" role="alert">
            {run.warning}{" "}
            <button
              type="button"
              className="dup-link"
              disabled={busyReloading}
              onClick={onCheckAgain}
            >
              {busyReloading ? "Checking\u2026" : "Check again"}
            </button>
          </p>
        )}
      </article>
    );
  }

  let status: ReactNode = (
    <span className="dup-card__count">
      {members.length}{" "}
      {copies
        ? plural(members.length, "copy", "copies")
        : plural(members.length, "version", "versions")}
    </span>
  );
  if (run?.state === "queued") {
    status = <span className="dup-card__status">Queued</span>;
  } else if (run?.state === "running") {
    status = (
      <span className="dup-card__status">
        <Spinner small />
        Working&hellip;
      </span>
    );
  } else if (run?.state === "failed") {
    status = (
      <span className="dup-card__status dup-card__status--failed">Failed</span>
    );
  }

  return (
    <article
      className={`dup-card${run?.state === "failed" ? " dup-card--failed" : ""}`}
      data-family-id={family.id}
    >
      <header className="dup-card__header">
        <h4>
          {family.title || "Unnamed"}
          {family.author && (
            <span className="dup-card__author"> by {family.author}</span>
          )}
        </h4>
        {status}
      </header>
      <div
        className="dup-members"
        role="radiogroup"
        aria-label={`Version to keep of ${family.title || "this mod"}`}
      >
        {members.map((member) => {
          const selID = memberSelectionID(family, member);
          const isKeeper = selID === keeperID;
          return (
            <MemberRow
              key={selID}
              family={family}
              member={member}
              isKeeper={isKeeper}
              decision={decision}
              gained={isKeeper ? gained : undefined}
              locked={locked}
              onSelect={() => onKeeperChange(selID)}
            />
          );
        })}
      </div>
      <footer className="dup-card__actions">
        <div
          className="segmented dup-decision"
          role="group"
          aria-label={`What to do with ${family.title || "this set"}`}
        >
          {decisionOptions(family).map((option) => {
            const active = decision === option.value;
            const blocked = option.value !== "keep" && refused;
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                className={`${active ? "is-active" : ""} dup-decision--${option.value}`}
                disabled={locked || (blocked && !active)}
                title={
                  active
                    ? `${option.title} Click again to undo.`
                    : option.title
                }
                onClick={() => onDecisionChange(active ? undefined : option.value)}
              >
                {option.label}
              </button>
            );
          })}
        </div>
        {run?.state === "failed" && run.error && (
          <p className="dup-outcome dup-outcome--error" role="alert">
            {run.error}
          </p>
        )}
        <Outcome
          family={family}
          decision={decision}
          keeperID={keeperID}
          plan={plan}
          onRetry={onRetryPlan}
        />
      </footer>
    </article>
  );
}

// ---------------------------------------------------------------------------
// Section
// ---------------------------------------------------------------------------

function sectionHeading(confidence: Confidence): string {
  switch (confidence) {
    case "identical":
      return "Identical files";
    case "repo":
      return "Same mod, several versions";
    case "content":
      return "Likely the same mod";
    case "metadata":
      return "Possibly the same mod";
  }
}

function sectionNote(confidence: Confidence): string {
  switch (confidence) {
    case "identical":
      return "The same file stored more than once. Removing copies never changes collections, groups or tags.";
    case "repo":
      return "Matched by their BeamNG repository ID.";
    case "content":
      return "Same name and vehicles. Check the versions before deleting.";
    case "metadata":
      return "Only the name and author match. Check before deleting.";
  }
}

function FamilySection({
  confidence,
  families,
  familyRefs,
  highlightedFamilyID,
  keeperFor,
  decisions,
  plans,
  runs,
  busyReloading,
  onKeeperChange,
  onDecisionChange,
  onBulkDecision,
  onRetryPlan,
  onCheckAgain,
}: {
  confidence: Confidence;
  families: ModFamily[];
  familyRefs: MutableRefObject<Record<string, HTMLElement | null>>;
  highlightedFamilyID: string;
  keeperFor: (family: ModFamily) => string;
  decisions: Record<string, Decision | undefined>;
  plans: Record<string, PlanView | undefined>;
  runs: Record<string, RunView | undefined>;
  busyReloading: boolean;
  onKeeperChange: (family: ModFamily, selectionID: string) => void;
  onDecisionChange: (family: ModFamily, decision?: Decision) => void;
  onBulkDecision: (families: ModFamily[], decision?: Decision) => void;
  onRetryPlan: (family: ModFamily) => void;
  onCheckAgain: () => void;
}) {
  if (families.length === 0) return null;
  const heading = sectionHeading(confidence);
  const open = families.filter((f) => !isLocked(runs[f.id]));
  const bulkOptions = decisionOptions(families[0]).map((option) =>
    option.value === "keep" ? { ...option, label: "Keep" } : option,
  );
  return (
    <section className="dup-section" aria-labelledby={`dup-${confidence}`}>
      <header className="dup-section__header">
        <div>
          <h3 id={`dup-${confidence}`}>
            {heading} <span>{families.length}</span>
          </h3>
          <p className="dup-section__note">{sectionNote(confidence)}</p>
        </div>
        {open.length > 1 && (
          <div
            className="dup-section__bulk"
            role="group"
            aria-label={`Choose for every set in ${heading}`}
          >
            <span>All {open.length}:</span>
            {bulkOptions.map((option) => (
              <button
                key={option.value}
                type="button"
                className="dup-link"
                title={option.title}
                onClick={() => onBulkDecision(open, option.value)}
              >
                {option.label}
              </button>
            ))}
            <button
              type="button"
              className="dup-link"
              title="Undo the choice for every set in this section."
              onClick={() => onBulkDecision(open, undefined)}
            >
              Clear
            </button>
          </div>
        )}
      </header>
      {families.map((family) => {
        const keeperID = keeperFor(family);
        return (
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
              keeperID={keeperID}
              decision={decisions[family.id]}
              plan={plans[planKey(family.id, keeperID)]}
              run={runs[family.id]}
              busyReloading={busyReloading}
              onKeeperChange={(selID) => onKeeperChange(family, selID)}
              onDecisionChange={(decision) => onDecisionChange(family, decision)}
              onRetryPlan={() => onRetryPlan(family)}
              onCheckAgain={onCheckAgain}
            />
          </div>
        );
      })}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Main dialog
// ---------------------------------------------------------------------------

const CONFIDENCES: Confidence[] = ["identical", "repo", "content", "metadata"];

export function DuplicatesDialog({
  families,
  focusFamilyID,
  onClose,
  onRefresh,
  onFamiliesChange,
  onNotify,
}: DuplicatesDialogProps) {
  const [visibleFamilies, setVisibleFamilies] = useState<ModFamily[]>(
    () => families,
  );
  const familyRefs = useRef<Record<string, HTMLElement | null>>({});
  const [highlightedFamilyID, setHighlightedFamilyID] = useState("");
  const [keepers, setKeepers] = useState<Record<string, string>>({});
  const [decisions, setDecisions] = useState<
    Record<string, Decision | undefined>
  >({});
  const [plans, setPlans] = useState<Record<string, PlanView | undefined>>({});
  const [runs, setRuns] = useState<Record<string, RunView | undefined>>({});
  const [applying, setApplying] = useState(false);
  const [reloading, setReloading] = useState(false);

  const planCache = useRef(new Map<string, Promise<FamilyImpact>>());
  const queue = useRef<Job[]>([]);
  const running = useRef(false);

  const keeperFor = (family: ModFamily) =>
    keepers[family.id] ?? proposedKeeper(family);

  // --- Plans: fetched in the background, cached per (set, kept version) ---

  const ensurePlan = useCallback(
    (family: ModFamily, keeperID: string, force = false) => {
      const key = planKey(family.id, keeperID);
      const cached = planCache.current.get(key);
      if (cached && !force) return cached;
      const targets = removalTargets(family, keeperID);
      const promise: Promise<FamilyImpact> = isCopies(family)
        ? API.PlanArchiveFileRemoval(targets).then(
            (impact) =>
              impact ?? {
                files: [],
                refusals: [],
                archiveCount: 0,
                archiveBytes: 0,
              },
          )
        : API.PlanModReplacement(keeperID, targets);
      planCache.current.set(key, promise);
      setPlans((current) => ({ ...current, [key]: { status: "loading" } }));
      promise.then(
        (impact) => {
          if (planCache.current.get(key) !== promise) return;
          setPlans((current) => ({
            ...current,
            [key]: { status: "ready", impact },
          }));
        },
        (err) => {
          if (planCache.current.get(key) !== promise) return;
          setPlans((current) => ({
            ...current,
            [key]: { status: "error", error: errorMessage(err) },
          }));
        },
      );
      return promise;
    },
    [],
  );

  // The list changes on open and on reload; keeper changes plan on their own.
  useEffect(() => {
    for (const family of visibleFamilies) {
      void ensurePlan(family, keeperFor(family)).catch(() => undefined);
    }
  }, [visibleFamilies]);

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
  }, [focusFamilyID]);

  // --- Choices (instant; nothing touches the library until Apply) ---

  const changeKeeper = (family: ModFamily, selectionID: string) => {
    if (isLocked(runs[family.id])) return;
    setKeepers((current) => ({ ...current, [family.id]: selectionID }));
    void ensurePlan(family, selectionID).catch(() => undefined);
  };

  const changeDecision = (family: ModFamily, decision?: Decision) => {
    if (isLocked(runs[family.id])) return;
    setDecisions((current) => ({ ...current, [family.id]: decision }));
  };

  const bulkDecision = (targets: ModFamily[], decision?: Decision) => {
    setDecisions((current) => {
      const next = { ...current };
      for (const family of targets) {
        if (isLocked(runs[family.id])) continue;
        const plan = plans[planKey(family.id, keeperFor(family))];
        const refused =
          plan?.status === "ready" && Boolean(impactRefusal(family, plan.impact));
        if (decision && decision !== "keep" && refused) continue;
        next[family.id] = decision;
      }
      return next;
    });
  };

  const isReady = (family: ModFamily): boolean => {
    const decision = decisions[family.id];
    if (!decision || isLocked(runs[family.id])) return false;
    if (decision === "keep") return true;
    const plan = plans[planKey(family.id, keeperFor(family))];
    if (!plan || plan.status === "loading") return true;
    if (plan.status === "error") return false;
    return !impactRefusal(family, plan.impact);
  };

  // --- Apply: a serial queue so choices stay instant while work runs ---

  const execute = async (
    job: Job,
    touched: Set<string>,
    retired: Set<string>,
  ): Promise<{ summary: string; warning?: string; changed: boolean }> => {
    const { family, decision, keeperID } = job;
    const members = membersOf(family);
    const entityIDs = members.map((m) => m.entityId);
    const keeper = members.find(
      (m) => memberSelectionID(family, m) === keeperID,
    );
    const keptName = keeper ? memberName(keeper) : "the chosen version";

    if (decision === "keep") {
      const refreshed = await API.DismissModFamily(family.id);
      onFamiliesChange(refreshed ?? []);
      return {
        summary: `Kept ${members.length === 2 ? "both" : `all ${members.length}`}. This set won\u2019t be flagged again unless it changes.`,
        changed: false,
      };
    }

    const targets = removalTargets(family, keeperID);

    if (isCopies(family)) {
      const entityID = keeper?.entityId ?? "";
      if (retired.has(entityID)) {
        return {
          summary: "Already deleted together with its other versions.",
          changed: false,
        };
      }
      const impact = await ensurePlan(family, keeperID);
      const refusal = impactRefusal(family, impact);
      if (refusal) throw new Error(refusal);
      const result = await API.DeleteArchiveFiles(targets);
      touched.add(entityID);
      const failures = result?.failures ?? [];
      const recycled = result?.recycled ?? 0;
      return {
        summary: `Kept ${keptName}. Deleted ${recycled} extra ${plural(recycled, "copy", "copies")}.`,
        warning:
          failures.length > 0
            ? `${failures.length} ${plural(failures.length, "copy", "copies")} couldn\u2019t be deleted: ${failures.join("; ")}`
            : undefined,
        changed: true,
      };
    }

    // An earlier job in this batch may have changed these mods (for example,
    // removed an identical copy); plan again so the review matches the library.
    const stale = entityIDs.some((id) => touched.has(id));
    const impact = (await ensurePlan(
      family,
      keeperID,
      stale,
    )) as ModReplacementImpact;
    const refusal = impactRefusal(family, impact);
    if (refusal) throw new Error(refusal);
    const places = placesOnlyOthersHave(family, keeperID);
    const result =
      decision === "replace"
        ? await API.ReplaceModArchives(keeperID, targets, impact.fingerprint)
        : await API.RemoveModVersions(keeperID, targets, impact.fingerprint);
    for (const id of entityIDs) touched.add(id);
    for (const id of targets) retired.add(id);
    const failures = result.failures ?? [];
    const deleted = result.forgotten;
    const moved =
      decision === "replace" && usageCount(places) > 0
        ? " It took over their collections, groups and tags."
        : "";
    return {
      summary: `Kept ${keptName}. Deleted ${deleted} ${plural(deleted, "version", "versions")}${result.recycledBytes > 0 ? ` (${formatBytes(result.recycledBytes)})` : ""}.${moved}`,
      warning:
        failures.length > 0
          ? `${failures.length} ${plural(failures.length, "version", "versions")} couldn\u2019t be deleted and ${plural(failures.length, "stays", "stay")} in the library: ${failures.join("; ")}`
          : undefined,
      changed: true,
    };
  };

  const drain = async () => {
    if (running.current) return;
    running.current = true;
    setApplying(true);
    const touched = new Set<string>();
    const retired = new Set<string>();
    let applied = 0;
    let failed = 0;
    let partial = 0;
    try {
      while (queue.current.length > 0) {
        let changed = false;
        while (queue.current.length > 0) {
          const job = queue.current.shift()!;
          const fid = job.family.id;
          setRuns((current) => ({
            ...current,
            [fid]: {
              decision: job.decision,
              keeperID: job.keeperID,
              state: "running",
            },
          }));
          try {
            const outcome = await execute(job, touched, retired);
            changed ||= outcome.changed;
            applied += 1;
            if (outcome.warning) partial += 1;
            setRuns((current) => ({
              ...current,
              [fid]: {
                decision: job.decision,
                keeperID: job.keeperID,
                state: "done",
                summary: outcome.summary,
                warning: outcome.warning,
              },
            }));
          } catch (err) {
            failed += 1;
            setRuns((current) => ({
              ...current,
              [fid]: {
                decision: job.decision,
                keeperID: job.keeperID,
                state: "failed",
                error: errorMessage(err),
              },
            }));
            if (job.decision !== "keep") {
              void ensurePlan(job.family, job.keeperID, true).catch(
                () => undefined,
              );
            }
          }
        }
        if (changed) {
          try {
            await onRefresh();
          } catch (err) {
            onNotify(
              `Changes were applied, but the library didn\u2019t refresh: ${errorMessage(err)}`,
              "error",
            );
          }
        }
      }
    } finally {
      running.current = false;
      setApplying(false);
    }
    const total = applied + failed;
    if (failed > 0) {
      onNotify(
        `${failed} of ${total} ${plural(total, "change", "changes")} couldn\u2019t be applied. The sets marked Failed explain why.`,
        "error",
      );
    } else if (partial > 0) {
      onNotify(
        "Applied, but some files couldn\u2019t be deleted. Check the sets that say so.",
        "error",
      );
    } else if (applied > 0) {
      onNotify(`Applied ${applied} ${plural(applied, "change", "changes")}.`, "success");
    }
  };

  const apply = () => {
    const jobs: Job[] = visibleFamilies.filter(isReady).map((family) => ({
      family,
      decision: decisions[family.id]!,
      keeperID: keeperFor(family),
    }));
    if (jobs.length === 0) return;
    setRuns((current) => {
      const next = { ...current };
      for (const job of jobs) {
        next[job.family.id] = {
          decision: job.decision,
          keeperID: job.keeperID,
          state: "queued",
        };
      }
      return next;
    });
    queue.current.push(...jobs);
    void drain();
  };

  // --- Reload after partial cleanup; unfinished choices survive ---

  const reloadFamilies = async () => {
    if (running.current || reloading) return;
    setReloading(true);
    try {
      const current = (await API.ModFamilies()) ?? [];
      onFamiliesChange(current);
      planCache.current.clear();
      setPlans({});
      setRuns({});
      setKeepers((previous) => {
        const next: Record<string, string> = {};
        for (const family of current) {
          const ids = membersOf(family).map((m) => memberSelectionID(family, m));
          const kept = previous[family.id];
          if (kept && ids.includes(kept)) next[family.id] = kept;
        }
        return next;
      });
      setDecisions((previous) => {
        const next: Record<string, Decision | undefined> = {};
        for (const family of current) next[family.id] = previous[family.id];
        return next;
      });
      setVisibleFamilies(current);
    } catch (err) {
      onNotify(errorMessage(err), "error");
    } finally {
      setReloading(false);
    }
  };

  const closeDialog = () => {
    if (running.current) return;
    onClose();
  };

  // --- Render ---

  const ready = visibleFamilies.filter(isReady).length;
  const blocked = visibleFamilies.filter(
    (f) => decisions[f.id] && !isLocked(runs[f.id]) && !isReady(f),
  ).length;
  const pending = Object.values(runs).filter(
    (run) => run?.state === "queued" || run?.state === "running",
  ).length;

  let footerStatus: string;
  if (applying) {
    footerStatus = `Applying\u2026 ${pending} left`;
  } else if (ready > 0) {
    footerStatus = `${ready} ${plural(ready, "set", "sets")} ready`;
  } else if (
    visibleFamilies.length > 0 &&
    visibleFamilies.every((f) => runs[f.id]?.state === "done")
  ) {
    footerStatus = "All sets handled";
  } else {
    footerStatus = "Nothing chosen yet";
  }
  if (blocked > 0) footerStatus += ` \u00b7 ${blocked} can\u2019t be applied`;

  return (
    <CollectionDialog
      title="Duplicates"
      wide
      onClose={closeDialog}
      footer={
        <>
          <span className="dup-footer__status" role="status" aria-live="polite">
            {footerStatus}
          </span>
          <Button
            type="button"
            onClick={closeDialog}
            disabled={applying}
            title={applying ? "Wait for the changes to finish" : undefined}
          >
            Close
          </Button>
          <Button
            type="button"
            tone="primary"
            disabled={ready === 0}
            onClick={apply}
          >
            {ready === 0
              ? "Apply"
              : `Apply ${ready} ${plural(ready, "change", "changes")}`}
          </Button>
        </>
      }
    >
      <div className="dup-dialog">
        {visibleFamilies.length === 0 ? (
          <p className="dup-dialog__empty">No duplicates found.</p>
        ) : (
          <div className="dup-intro">
            <p>
              In each set, pick the version to keep, then choose what happens
              to the others. Nothing changes until you press{" "}
              <strong>Apply</strong>; deleted files go to the Recycle Bin.
            </p>
            <p className="dup-legend">
              <span>
                <b>Replace</b> hands the others&rsquo; collections, groups and
                tags to the kept version.
              </span>
              <span>
                <b>Remove</b> drops them.
              </span>
              <span className="dup-legend__chips">
                <span className="dup-chip dup-chip--collections">
                  <Icon name="mixed" size={12} />
                  <span>Collection</span>
                </span>
                <span className="dup-chip dup-chip--groups">
                  <Icon name="folder" size={12} />
                  <span>Group</span>
                </span>
                <span className="dup-chip dup-chip--tags">
                  <Icon name="tag" size={12} />
                  <span>Tag</span>
                </span>
              </span>
            </p>
          </div>
        )}
        {CONFIDENCES.map((confidence) => (
          <FamilySection
            key={confidence}
            confidence={confidence}
            families={visibleFamilies.filter(
              (f) => f.confidence === confidence,
            )}
            familyRefs={familyRefs}
            highlightedFamilyID={highlightedFamilyID}
            keeperFor={keeperFor}
            decisions={decisions}
            plans={plans}
            runs={runs}
            busyReloading={reloading || applying}
            onKeeperChange={changeKeeper}
            onDecisionChange={changeDecision}
            onBulkDecision={bulkDecision}
            onRetryPlan={(family) =>
              void ensurePlan(family, keeperFor(family), true).catch(
                () => undefined,
              )
            }
            onCheckAgain={() => void reloadFamilies()}
          />
        ))}
      </div>
    </CollectionDialog>
  );
}
