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

type RemovalMode = "forget" | "delete";
type PlanMode = RemovalMode | "files";

interface PlannedModRemoval {
  mode: RemovalMode;
  impact: ModRemovalImpact;
  targetIDs: string[];
}

interface PlannedArchiveFileRemoval {
  mode: "files";
  impact: ArchiveFileRemovalImpact;
  targetIDs: string[];
}

type PlannedRemoval = PlannedModRemoval | PlannedArchiveFileRemoval;

interface DuplicatesDialogProps {
  families: ModFamily[];
  focusFamilyID?: string;
  onClose: () => void;
  onRefresh: () => Promise<void>;
  onFamiliesChange: (families: ModFamily[]) => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
}

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
  return "The duplicate-family operation could not be completed.";
}

function membersOf(family: ModFamily): ModFamilyMember[] {
  return family.members ?? [];
}

function proposedKeeper(family: ModFamily): string {
  const members = membersOf(family);
  const keeper = members.find((member) => member.keeper) ?? members[0];
  return keeper ? memberSelectionID(family, keeper) : "";
}

function familyKindLabel(kind: string): string {
  return kind === "copies" ? "copies" : "versions";
}

function archiveFilename(path: string): string {
  const separator = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"))
  return separator >= 0 ? path.slice(separator + 1) : path
}

function archiveDirectory(path: string): string {
  const separator = Math.max(path.lastIndexOf("\\"), path.lastIndexOf("/"))
  return separator >= 0 ? path.slice(0, separator + 1) : ""
}

function familyReclaimableBytes(family: ModFamily, keeperID: string): number {
  return membersOf(family)
    .filter((member) => memberSelectionID(family, member) !== keeperID)
    .reduce((total, member) => total + Math.max(0, member.sizeBytes || 0), 0);
}

function memberCollections(member: ModFamilyMember): string {
  const collections = member.collections ?? [];
  return collections.length > 0
    ? collections.join(", ")
    : "No collections";
}

function memberSelectionID(family: ModFamily, member: ModFamilyMember): string {
  return family.confidence === "identical"
    ? member.linkId || member.entityId
    : member.entityId;
}

function memberIdentity(family: ModFamily, member: ModFamilyMember): string {
  if (family.confidence === "identical") {
    return archiveFilename(member.archivePath) || "Archive unavailable";
  }
  return member.displayName || member.entityId;
}

function versionsMatch(family: ModFamily): boolean {
  const members = membersOf(family);
  if (members.length < 2) return true;
  const version = members[0]?.version.trim() ?? "";
  return members.every((member) => member.version.trim() === version);
}

function emptyArchiveFileRemovalImpact(): ArchiveFileRemovalImpact {
  return {
    files: [],
    refusals: [],
    archiveCount: 0,
    archiveBytes: 0,
  };
}

function emptyModRemovalResult(): ModRemovalResult {
  return {
    forgotten: 0,
    recycled: 0,
    failures: [],
  };
}

function plannedFileLabel(count: number): string {
  return `${count.toLocaleString()} file${count === 1 ? "" : "s"}`;
}


function memberVersion(member: ModFamilyMember): string {
  return member.version.trim() || "no version declared";
}

function removalTargets(family: ModFamily, keeperID: string): string[] {
  return membersOf(family)
    .filter((member) => memberSelectionID(family, member) !== keeperID)
    .map((member) => memberSelectionID(family, member));
}

function plannedArchiveLabel(impact: ModRemovalImpact): string {
  const count = impact.archiveCount.toLocaleString();
  return `${count} archive${impact.archiveCount === 1 ? "" : "s"}`;
}

function plannedModLabel(count: number): string {
  return `${count.toLocaleString()} mod${count === 1 ? "" : "s"}`;
}

function FamilyMemberRow({
  family,
  member,
  keeperID,
  showVersion,
  onKeeperChange,
}: {
  family: ModFamily;
  member: ModFamilyMember;
  keeperID: string;
  showVersion: boolean;
  onKeeperChange: (selectionID: string) => void;
}) {
  const selectionID = memberSelectionID(family, member);
  const selected = selectionID === keeperID;
  const identity = memberIdentity(family, member);
  return (
    <div className={`duplicates-member${selected ? " is-keeper" : ""}`}>
      <label className="duplicates-member__choice">
        <input
          type="radio"
          name={`keeper-${family.id}`}
          value={selectionID}
          checked={selected}
          onChange={() => onKeeperChange(selectionID)}
          aria-label={`Keep ${identity}`}
        />
        <span className="duplicates-member__identity">
          <strong title={identity}>{identity}</strong>
          {selected && <span className="duplicates-member__keeper">Keeper</span>}
        </span>
      </label>
      <dl className="duplicates-member__details">
        {showVersion && (
          <div>
            <dt>Version</dt>
            <dd>{memberVersion(member)}</dd>
          </div>
        )}
        <div>
          <dt>Size</dt>
          <dd>{formatBytes(member.sizeBytes)}</dd>
        </div>
        <div>
          <dt>File date</dt>
          <dd>{formatDate(member.modifiedAt)}</dd>
        </div>
        <div>
          <dt>Source</dt>
          <dd>{member.sourceLabel || "Unknown source"}</dd>
        </div>
        <div className="duplicates-member__detail--wide">
          <dt>Archive</dt>
          {/* Copies of one mod usually sit under near-identical directories, so
              a normal end-truncation hides the only part that tells them
              apart. Keep the filename whole and ellipsize the directory from
              its left. */}
          <dd title={member.archivePath || "Archive unavailable"}>
            {member.archivePath ? (
              <code className="duplicates-member__path">
                <span className="duplicates-member__path-dir">{archiveDirectory(member.archivePath)}</span>
                <span className="duplicates-member__path-file">{archiveFilename(member.archivePath)}</span>
              </code>
            ) : (
              <code>Archive unavailable</code>
            )}
          </dd>
        </div>
        <div className="duplicates-member__detail--wide">
          <dt>Collections</dt>
          <dd>{memberCollections(member)}</dd>
        </div>
        {member.workspaceCount > 0 && (
          <div>
            <dt>ModMaker</dt>
            <dd>
              {member.workspaceCount.toLocaleString()} project
              {member.workspaceCount === 1 ? "" : "s"}
            </dd>
          </div>
        )}
      </dl>
      {member.keeperReason && (
        <p className="duplicates-member__reason">{member.keeperReason}</p>
      )}
    </div>
  );
}

function PlannedRemovalNotice({
  plan,
  busy,
  error,
  onConfirm,
  onCancel,
}: {
  plan: PlannedRemoval;
  busy: boolean;
  error?: string;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const filePlan = plan.mode === "files" ? plan : undefined;
  const modPlan = plan.mode === "files" ? null : plan;
  const collectionNames = modPlan?.impact.collections ?? [];
  const workspaceNames = modPlan?.impact.workspaces ?? [];
  const fileTargets = filePlan?.impact.files ?? [];
  const refusals = filePlan?.impact.refusals ?? [];
  const confirmLabel = filePlan
    ? `Delete ${plannedFileLabel(filePlan.impact.archiveCount)} to Recycle Bin`
    : plan.mode === "delete"
      ? `Delete ${plannedArchiveLabel(plan.impact)} to Recycle Bin`
      : `Forget ${plannedModLabel(plan.targetIDs.length)}`;
  return (
    <div className="duplicates-plan" role="status">
      <strong>
        {filePlan
          ? "Review which files will be removed"
          : "Review what this action will affect"}
      </strong>
      {filePlan ? (
        <>
          <p>
            Files: {plannedFileLabel(filePlan.impact.archiveCount)} ·{" "}
            {formatBytes(filePlan.impact.archiveBytes)}
          </p>
          {fileTargets.length > 0 && (
            <ul className="duplicates-plan__files">
              {fileTargets.map((file) => (
                <li key={`${file.linkId}-${file.archivePath}`}>
                  <strong
                    title={
                      file.archivePath ||
                      file.displayName ||
                      "Archive unavailable"
                    }
                  >
                    {archiveFilename(file.archivePath) ||
                      file.displayName ||
                      "Archive unavailable"}
                  </strong>
                  <span className="duplicates-plan__file-name">
                    {file.displayName || "Unnamed mod"}
                  </span>
                  <span className="duplicates-plan__file-path">
                    {file.archivePath ? (
                      <code className="duplicates-member__path">
                        <span className="duplicates-member__path-dir">
                          {archiveDirectory(file.archivePath)}
                        </span>
                        <span className="duplicates-member__path-file">
                          {archiveFilename(file.archivePath)}
                        </span>
                      </code>
                    ) : (
                      <code>Archive unavailable</code>
                    )}
                  </span>
                  <span>
                    {file.missing
                      ? "Archive already missing"
                      : formatBytes(file.sizeBytes)}
                  </span>
                </li>
              ))}
            </ul>
          )}
          {refusals.length > 0 && (
            <ul className="duplicates-plan__refusals" role="alert">
              {refusals.map((refusal, index) => (
                <li key={`${index}-${refusal}`}>{refusal}</li>
              ))}
            </ul>
          )}
        </>
      ) : modPlan ? (
        <>
          {collectionNames.length > 0 && (
            <p>
              Collections: <span>{collectionNames.join(", ")}</span>
            </p>
          )}
          {workspaceNames.length > 0 && (
            <p className="duplicates-plan__warning" role="alert">
              ModMaker projects: <span>{workspaceNames.join(", ")}</span>. The
              affected mod will be refused until its project is closed or deleted.
            </p>
          )}
          <p>
            Archives: {plannedArchiveLabel(modPlan.impact)} ·{" "}
            {formatBytes(modPlan.impact.archiveBytes)}
          </p>
          {modPlan.impact.mods?.some((mod) => mod.missing) && (
            <p className="duplicates-plan__warning">
              One or more archives are already missing; no file will be recycled for
              those members.
            </p>
          )}
        </>
      ) : null}
      {error && (
        <p className="duplicates-plan__error" role="alert">
          {error}
        </p>
      )}
      <div className="duplicates-plan__actions">
        <Button
          type="button"
          tone={filePlan || plan.mode === "delete" ? "danger" : "primary"}
          disabled={busy || Boolean(filePlan && filePlan.impact.archiveCount <= 0)}
          onClick={onConfirm}
        >
          {confirmLabel}
        </Button>
        <Button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}
function FamilyCard({
  family,
  keeperID,
  plan,
  busy,
  error,
  onKeeperChange,
  onPlan,
  onConfirm,
  onCancelPlan,
  onDismiss,
}: {
  family: ModFamily;
  keeperID: string;
  plan?: PlannedRemoval;
  busy: boolean;
  error?: string;
  onKeeperChange: (selectionID: string) => void;
  onPlan: (mode: PlanMode) => void;
  onConfirm: () => void;
  onCancelPlan: () => void;
  onDismiss: () => void;
}) {
  const members = membersOf(family);
  const identical = family.confidence === "identical";
  const reclaimableBytes =
    keeperID === proposedKeeper(family)
      ? family.reclaimableBytes
      : familyReclaimableBytes(family, keeperID);
  const targetIDs = removalTargets(family, keeperID);
  const kind = familyKindLabel(family.kind);
  const showVersion = !identical || !versionsMatch(family);
  return (
    <article className="duplicates-family" id={`duplicate-family-${family.id}`}>
      <header className="duplicates-family__header">
        <div>
          <h4>{family.title || "Untitled mod"}</h4>
          <p>{family.author || "Author not declared"}</p>
        </div>
        <p className="duplicates-family__summary">
          {members.length.toLocaleString()} {kind}
          {reclaimableBytes > 0 && <> · frees <strong>{formatBytes(reclaimableBytes)}</strong></>}
        </p>
      </header>
      <div
        className="duplicates-family__members"
        role="radiogroup"
        aria-label={
          identical
            ? `Choose the file to keep for ${family.title || "this mod family"}`
            : `Choose the keeper for ${family.title || "this mod family"}`
        }
      >
        {members.map((member) => (
          <FamilyMemberRow
            key={`${family.id}-${memberSelectionID(family, member)}`}
            family={family}
            member={member}
            keeperID={keeperID}
            showVersion={showVersion}
            onKeeperChange={onKeeperChange}
          />
        ))}
      </div>
      {error && !plan && (
        <p className="duplicates-family__error" role="alert">
          {error}
        </p>
      )}
      {plan ? (
        <PlannedRemovalNotice
          plan={plan}
          busy={busy}
          error={error}
          onConfirm={onConfirm}
          onCancel={onCancelPlan}
        />
      ) : (
        <div className="duplicates-family__actions">
          {identical ? (
            <Button
              type="button"
              tone="danger"
              disabled={busy || targetIDs.length === 0}
              onClick={() => onPlan("files")}
            >
              Delete other files…
            </Button>
          ) : (
            <>
              <Button
                type="button"
                disabled={busy || targetIDs.length === 0}
                onClick={() => onPlan("forget")}
              >
                Forget others
              </Button>
              <Button
                type="button"
                tone="danger"
                disabled={busy || targetIDs.length === 0}
                onClick={() => onPlan("delete")}
              >
                Delete others…
              </Button>
            </>
          )}
          <Button type="button" disabled={busy} onClick={onDismiss}>
            {identical ? "Not duplicates" : "Not the same mod"}
          </Button>
        </div>
      )}
    </article>
  );
}
function FamilySection({
  heading,
  confidence,
  families,
  familyRefs,
  keeperByFamilyID,
  plans,
  busyByFamilyID,
  errors,
  highlightedFamilyID,
  onKeeperChange,
  onPlan,
  onConfirm,
  onCancelPlan,
  onDismiss,
}: {
  heading: string;
  confidence: "identical" | "repo" | "metadata";
  families: ModFamily[];
  familyRefs: MutableRefObject<Record<string, HTMLElement | null>>;
  keeperByFamilyID: Record<string, string>;
  plans: Record<string, PlannedRemoval | undefined>;
  busyByFamilyID: Record<string, boolean | undefined>;
  errors: Record<string, string | undefined>;
  highlightedFamilyID: string;
  onKeeperChange: (familyID: string, selectionID: string) => void;
  onPlan: (family: ModFamily, mode: PlanMode) => void;
  onConfirm: (family: ModFamily) => void;
  onCancelPlan: (familyID: string) => void;
  onDismiss: (family: ModFamily) => void;
}) {
  const headingID = confidence;
  const note =
    confidence === "identical"
      ? "The same archive is in more than one place. Keep one path; the mod stays in your library."
      : confidence === "repo"
        ? "These carry the same BeamNG repository ID, so they are the same mod."
        : "These declare the same title and author. Check the files before deleting anything.";
  return (
    <section className="duplicates-section" aria-labelledby={`duplicates-${headingID}`}>
      <h3 id={`duplicates-${headingID}`}>{heading}</h3>
      <p className="duplicates-section__note">{note}</p>
      {families.length === 0 ? (
        <p className="duplicates-section__empty">No families in this group.</p>
      ) : (
        families.map((family) => (
          <div
            key={family.id}
            className={family.id === highlightedFamilyID ? "is-highlighted" : undefined}
            ref={(element) => {
              familyRefs.current[family.id] = element;
            }}
          >
            <FamilyCard
              family={family}
              keeperID={keeperByFamilyID[family.id] ?? proposedKeeper(family)}
              plan={plans[family.id]}
              busy={Boolean(busyByFamilyID[family.id])}
              error={errors[family.id]}
              onKeeperChange={(selectionID) =>
                onKeeperChange(family.id, selectionID)
              }
              onPlan={(mode) => onPlan(family, mode)}
              onConfirm={() => onConfirm(family)}
              onCancelPlan={() => onCancelPlan(family.id)}
              onDismiss={() => onDismiss(family)}
            />
          </div>
        ))
      )}
    </section>
  );
}

export function DuplicatesDialog({
  families,
  focusFamilyID,
  onClose,
  onRefresh,
  onFamiliesChange,
  onNotify,
}: DuplicatesDialogProps) {
  const familyRefs = useRef<Record<string, HTMLElement | null>>({});
  const [keeperByFamilyID, setKeeperByFamilyID] = useState<Record<string, string>>(
    () =>
      families.reduce<Record<string, string>>((lookup, family) => {
        lookup[family.id] = proposedKeeper(family);
        return lookup;
      }, {}),
  );
  const [plans, setPlans] = useState<Record<string, PlannedRemoval | undefined>>({});
  const [busyByFamilyID, setBusyByFamilyID] = useState<
    Record<string, boolean | undefined>
  >({});
  const [errors, setErrors] = useState<Record<string, string | undefined>>({});
  const [highlightedFamilyID, setHighlightedFamilyID] = useState("");

  useEffect(() => {
    setKeeperByFamilyID((current) => {
      const next: Record<string, string> = {};
      for (const family of families) {
        const currentKeeper = current[family.id];
        next[family.id] =
          currentKeeper &&
          membersOf(family).some(
            (member) => memberSelectionID(family, member) === currentKeeper,
          )
            ? currentKeeper
            : proposedKeeper(family);
      }
      return next;
    });
    setPlans((current) => {
      const next: Record<string, PlannedRemoval | undefined> = {};
      for (const family of families) {
        const plan = current[family.id];
        if (
          plan &&
          plan.targetIDs.every((targetID) =>
            membersOf(family).some(
              (member) => memberSelectionID(family, member) === targetID,
            ),
          )
        ) {
          next[family.id] = plan;
        }
      }
      return next;
    });
  }, [families]);

  useEffect(() => {
    if (!focusFamilyID) return;
    const frame = window.requestAnimationFrame(() => {
      const element = familyRefs.current[focusFamilyID];
      if (!element) return;
      element.scrollIntoView({ behavior: "smooth", block: "center" });
      setHighlightedFamilyID(focusFamilyID);
    });
    const timer = window.setTimeout(() => setHighlightedFamilyID(""), 2200);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, [families, focusFamilyID]);

  const changeKeeper = (familyID: string, entityID: string) => {
    setKeeperByFamilyID((current) => ({ ...current, [familyID]: entityID }));
    setPlans((current) => {
      const next = { ...current };
      delete next[familyID];
      return next;
    });
    setErrors((current) => {
      const next = { ...current };
      delete next[familyID];
      return next;
    });
  };

  const planRemoval = async (family: ModFamily, mode: PlanMode) => {
    const keeperID = keeperByFamilyID[family.id] ?? proposedKeeper(family);
    const targetIDs = removalTargets(family, keeperID);
    if (targetIDs.length === 0 || busyByFamilyID[family.id]) return;
    setBusyByFamilyID((current) => ({ ...current, [family.id]: true }));
    setErrors((current) => {
      const next = { ...current };
      delete next[family.id];
      return next;
    });
    try {
      if (mode === "files") {
        const impact =
          (await API.PlanArchiveFileRemoval(targetIDs)) ??
          emptyArchiveFileRemovalImpact();
        setPlans((current) => ({
          ...current,
          [family.id]: { mode, impact, targetIDs },
        }));
      } else {
        const impact = await API.PlanModRemoval(targetIDs);
        setPlans((current) => ({
          ...current,
          [family.id]: { mode, impact, targetIDs },
        }));
      }
    } catch (error) {
      setErrors((current) => ({ ...current, [family.id]: errorMessage(error) }));
    } finally {
      setBusyByFamilyID((current) => ({ ...current, [family.id]: false }));
    }
  };

  const cancelPlan = (familyID: string) => {
    setPlans((current) => {
      const next = { ...current };
      delete next[familyID];
      return next;
    });
    setErrors((current) => {
      const next = { ...current };
      delete next[familyID];
      return next;
    });
  };

  const confirmRemoval = async (family: ModFamily) => {
    const plan = plans[family.id];
    if (!plan || busyByFamilyID[family.id]) return;
    if (plan.mode === "files" && plan.impact.archiveCount <= 0) return;
    setBusyByFamilyID((current) => ({ ...current, [family.id]: true }));
    setErrors((current) => {
      const next = { ...current };
      delete next[family.id];
      return next;
    });
    let refreshError = "";
    try {
      let result: ModRemovalResult;
      if (plan.mode === "files") {
        result =
          (await API.DeleteArchiveFiles(plan.targetIDs)) ??
          emptyModRemovalResult();
      } else if (plan.mode === "delete") {
        result = await API.DeleteModArchives(plan.targetIDs);
      } else {
        result = await API.ForgetMods(plan.targetIDs);
      }
      const failures = result.failures ?? [];
      if (failures.length > 0) {
        setErrors((current) => ({
          ...current,
          [family.id]: failures.join(" · "),
        }));
      } else {
        setPlans((current) => {
          const next = { ...current };
          delete next[family.id];
          return next;
        });
      }
      try {
        await onRefresh();
      } catch (error) {
        refreshError = errorMessage(error);
        setErrors((current) => ({
          ...current,
          [family.id]: current[family.id]
            ? `${current[family.id]} · ${refreshError}`
            : refreshError,
        }));
      }
      if (failures.length === 0 && !refreshError) {
        onNotify(
          plan.mode === "files"
            ? `Deleted ${result.recycled.toLocaleString()} file${result.recycled === 1 ? "" : "s"} to the Recycle Bin`
            : plan.mode === "delete"
              ? `Deleted ${result.recycled.toLocaleString()} archive${result.recycled === 1 ? "" : "s"} to the Recycle Bin`
              : `Forgot ${result.forgotten.toLocaleString()} mod${result.forgotten === 1 ? "" : "s"}`,
          "success",
        );
      } else if (failures.length > 0) {
        onNotify("Some duplicate members could not be removed.", "error");
      }
    } catch (error) {
      setErrors((current) => ({ ...current, [family.id]: errorMessage(error) }));
    } finally {
      setBusyByFamilyID((current) => ({ ...current, [family.id]: false }));
    }
  };

  const dismissFamily = async (family: ModFamily) => {
    if (busyByFamilyID[family.id]) return;
    setBusyByFamilyID((current) => ({ ...current, [family.id]: true }));
    setErrors((current) => {
      const next = { ...current };
      delete next[family.id];
      return next;
    });
    try {
      const refreshed = await API.DismissModFamily(family.id);
      onFamiliesChange(refreshed ?? []);
      onNotify("Duplicate family dismissed.", "info");
    } catch (error) {
      setErrors((current) => ({ ...current, [family.id]: errorMessage(error) }));
    } finally {
      setBusyByFamilyID((current) => ({ ...current, [family.id]: false }));
    }
  };

  const identicalFamilies = families.filter(
    (family) => family.confidence === "identical",
  );
  const repoFamilies = families.filter((family) => family.confidence === "repo");
  const metadataFamilies = families.filter(
    (family) => family.confidence === "metadata",
  );

  return (
    <CollectionDialog
      title="Review duplicate mods"
      wide
      onClose={onClose}
      footer={
        <Button type="button" onClick={onClose} disabled={Object.values(busyByFamilyID).some(Boolean)}>
          Close
        </Button>
      }
    >
      <div className="duplicates-dialog">
        <p className="duplicates-dialog__intro">
          Choose one keeper in each family. Nothing changes until you review the
          impact and confirm a removal.
        </p>
        <FamilySection
          heading="Extra copies on disk"
          confidence="identical"
          families={identicalFamilies}
          familyRefs={familyRefs}
          keeperByFamilyID={keeperByFamilyID}
          plans={plans}
          busyByFamilyID={busyByFamilyID}
          errors={errors}
          highlightedFamilyID={highlightedFamilyID}
          onKeeperChange={changeKeeper}
          onPlan={(family, mode) => void planRemoval(family, mode)}
          onConfirm={(family) => void confirmRemoval(family)}
          onCancelPlan={cancelPlan}
          onDismiss={(family) => void dismissFamily(family)}
        />
        <FamilySection
          heading="Same mod (repository ID matched)"
          confidence="repo"
          families={repoFamilies}
          familyRefs={familyRefs}
          keeperByFamilyID={keeperByFamilyID}
          plans={plans}
          busyByFamilyID={busyByFamilyID}
          errors={errors}
          highlightedFamilyID={highlightedFamilyID}
          onKeeperChange={changeKeeper}
          onPlan={(family, mode) => void planRemoval(family, mode)}
          onConfirm={(family) => void confirmRemoval(family)}
          onCancelPlan={cancelPlan}
          onDismiss={(family) => void dismissFamily(family)}
        />
        <FamilySection
          heading="Looks like the same mod (title and author)"
          confidence="metadata"
          families={metadataFamilies}
          familyRefs={familyRefs}
          keeperByFamilyID={keeperByFamilyID}
          plans={plans}
          busyByFamilyID={busyByFamilyID}
          errors={errors}
          highlightedFamilyID={highlightedFamilyID}
          onKeeperChange={changeKeeper}
          onPlan={(family, mode) => void planRemoval(family, mode)}
          onConfirm={(family) => void confirmRemoval(family)}
          onCancelPlan={cancelPlan}
          onDismiss={(family) => void dismissFamily(family)}
        />
        {families.length === 0 && (
          <p className="duplicates-dialog__empty">No duplicate families remain.</p>
        )}
      </div>
    </CollectionDialog>
  );
}
