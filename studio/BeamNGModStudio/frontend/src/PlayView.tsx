import { useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent } from "react";
import type {
  CollectionMod,
  ModCollection,
  ModProfile,
  OrganizationState,
  PlayResult,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import {
  CollectionCard,
  CollectionDialog,
  CollectionMenuPopup,
} from "./CollectionUI";
import { Icon } from "./icons";
import { Badge, Button, Page, Spinner, thumbUrl } from "./ui";
import type { PlaySession } from "./usePlaySession";
import "./PlayView.css";

export interface PlayViewProps {
  organization: OrganizationState | null;
  session: PlaySession;
  onOrganization: (state: OrganizationState) => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
  onError: (error: unknown) => void;
  onOpenCollection: (id: string) => void;
}

type MenuPoint = { id: string; x: number; y: number };
type SwitchRequest = { id: string; name: string };

/* Launch-button scenes. The button cycles through them so the bar never looks
   like a static screenshot; the crossfade is CSS. */
const LAUNCH_ART = [
  "/art/night-highway.jpg",
  "/art/police-chase.jpg",
  "/art/towed.jpg",
  "/art/crash.jpg",
  "/art/rusty-pumps.jpg",
  "/art/gas-station.jpg",
] as const;
const LAUNCH_ART_INTERVAL_MS = 16000;

const ALL_MODS_ID = "all-mods";

function useLaunchArt(): { current: string; previous: string | null; step: number } {
  const [step, setStep] = useState(() => Math.floor(Math.random() * LAUNCH_ART.length));
  const firstStep = useRef(step);
  useEffect(() => {
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
    const timer = window.setInterval(() => setStep((current) => current + 1), LAUNCH_ART_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, []);
  return {
    current: LAUNCH_ART[step % LAUNCH_ART.length],
    // Nothing fades out on the first paint: the opening scene is simply there.
    previous: step === firstStep.current ? null : LAUNCH_ART[(step - 1) % LAUNCH_ART.length],
    step,
  };
}

function messageFor(error: unknown): string {
  if (typeof error === "string") return error;
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "object" && error !== null) {
    const record = error as Record<string, unknown>;
    for (const key of ["error", "message", "details"]) {
      if (typeof record[key] === "string" && record[key]) return record[key] as string;
    }
  }
  return "The Play action could not be completed.";
}

function uniqueIDs(ids: readonly string[]): string[] {
  return Array.from(new Set(ids.filter(Boolean)));
}

// collectionPathIDs returns the ids of selected collections that include
// collectionID somewhere beneath them, nearest selected ancestor first.
function collectionPathIDs(
  collectionID: string,
  byID: Record<string, ModCollection>,
  selected: Set<string>,
): string[] {
  const found: string[] = [];
  for (const rootID of selected) {
    if (rootID === ALL_MODS_ID) continue;
    const queue = [rootID];
    const seen = new Set<string>();
    while (queue.length) {
      const currentID = queue.shift();
      if (!currentID || seen.has(currentID)) continue;
      seen.add(currentID);
      const current = byID[currentID];
      if (!current) continue;
      if (current.childIds?.includes(collectionID)) {
        found.push(rootID);
        break;
      }
      for (const childID of current.childIds ?? []) queue.push(childID);
    }
  }
  return found;
}

function resultSummary(result: PlayResult): string {
  if (result.error) return result.error;
  if (!result.applied && !result.started) return "BeamNG did not apply the requested selection.";
  if (result.applied && result.started) return "Selection applied and BeamNG started.";
  if (result.applied && !result.started) {
    return result.processUncertain
      ? "The selection was applied, but BeamNG process state is uncertain. Inspect it before retrying."
      : "The selection was applied, but BeamNG did not launch. Retry launch when it is safe.";
  }
  return "BeamNG did not start and the selection was not applied.";
}

function modProvenance(mod: CollectionMod, byID: Record<string, ModCollection>): string {
  const ids = uniqueIDs(mod.rootIds?.length ? mod.rootIds : mod.collectionIds ?? []);
  const names = ids.map((id) => id === ALL_MODS_ID ? "All mods" : byID[id]?.name ?? id).filter(Boolean);
  return names.length ? `Included via ${names.join(", ")}` : "Included by the selected collection graph";
}

export function PlayView({
  organization,
  session,
  onOrganization,
  onNotify,
  onError,
  onOpenCollection,
}: PlayViewProps) {
  const collections = organization?.collections ?? [];
  const profiles = organization?.profiles ?? [];
  const query = "";
  const [reviewOpen, setReviewOpen] = useState(false);
  const [managerOpen, setManagerOpen] = useState(false);
  const launchArt = useLaunchArt();
  const [cardMenu, setCardMenu] = useState<MenuPoint | null>(null);
  const [switchRequest, setSwitchRequest] = useState<SwitchRequest | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ModProfile | null>(null);
  const [renameID, setRenameID] = useState("");
  const [renameDraft, setRenameDraft] = useState("");
  const [renameError, setRenameError] = useState("");

  const byID = useMemo(() => {
    const next: Record<string, ModCollection> = {};
    for (const collection of collections) next[collection.id] = collection;
    return next;
  }, [collections]);
  const selectedIDs = useMemo(() => new Set(session.selection), [session.selection]);
  const excludedIDs = useMemo(() => new Set(session.excludedSelection), [session.excludedSelection]);
  const allModsSelected = selectedIDs.has(ALL_MODS_ID);
  // Child id -> the selected ancestor that pulls it in, so inherited cards can
  // name that parent and offer a route to inspect it.
  const inheritedParentIDByID = useMemo(() => {
    const next: Record<string, string> = {};
    for (const collection of collections) {
      if (selectedIDs.has(collection.id)) continue;
      const parentID = collectionPathIDs(collection.id, byID, selectedIDs)[0];
      if (parentID) next[collection.id] = parentID;
    }
    return next;
  }, [byID, collections, selectedIDs]);
  const filteredCollections = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    return [...collections]
      .sort((left, right) => (left.position - right.position) || left.name.localeCompare(right.name))
      .filter((collection) => !normalized || collection.name.toLowerCase().includes(normalized) || collection.description.toLowerCase().includes(normalized));
  }, [collections, query]);
  const selectionBusy = Boolean(session.operationBusy || session.busy);
  const profilesExist = profiles.length > 0;
  const activeProfileName = profiles.find((profile) => profile.id === session.profileId)?.name ?? "Default";
  const dirtyProfileLabel = (profile: ModProfile) =>
    profile.id === session.profileId && session.dirty ? `${profile.name}*` : profile.name;
  const noModsSelected = !session.selection.length || (session.preview?.modCount ?? 0) === 0;

  // Readiness line: shows arithmetic from the resolved selection.
  const readinessText = session.previewLoading
    ? "Checking mods\u2026"
    : noModsSelected
      ? "No mods selected"
      : (session.preview?.excludedModCount ?? 0) > 0
        ? `${(session.preview!.modCount + session.preview!.excludedModCount).toLocaleString()} included \u2212 ${session.preview!.excludedModCount.toLocaleString()} excluded = ${session.preview!.modCount.toLocaleString()} mods`
        : `${session.preview?.modCount ?? 0} mod${session.preview?.modCount === 1 ? "" : "s"} from ${session.selection.length} collection${session.selection.length === 1 ? "" : "s"}`;

  useEffect(() => {
    if (!cardMenu) return;
    const closeMenus = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") setCardMenu(null);
    };
    document.addEventListener("keydown", closeMenus);
    return () => document.removeEventListener("keydown", closeMenus);
  }, [cardMenu]);

  const selectProfile = (nextID: string) => {
    if (nextID === session.profileId || selectionBusy) return;
    const target = profiles.find((profile) => profile.id === nextID);
    if (session.dirty && session.profileId) {
      setSwitchRequest({ id: nextID, name: target?.name ?? "Default" });
      return;
    }
    void session.switchProfile(nextID).catch(onError);
  };

  const updateAndSwitch = async () => {
    if (!switchRequest) return;
    try {
      await session.updateProfile();
      await session.switchProfile(switchRequest.id);
      onNotify("Profile updated and selection switched.", "success");
      setSwitchRequest(null);
    } catch (error) {
      onError(error);
    }
  };

  const discardAndSwitch = async () => {
    if (!switchRequest) return;
    try {
      await session.switchProfile(switchRequest.id);
      onNotify(`Discarded unsaved changes and switched to ${switchRequest.name}.`, "info");
      setSwitchRequest(null);
    } catch (error) {
      onError(error);
    }
  };

  const handleCreateProfile = async (asNew: boolean) => {
    try {
      const created = asNew ? await session.saveAsNew() : await session.createProfile();
      onNotify(`${created.name} saved with the current collection selection.`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const handleUpdate = async () => {
    try {
      const updated = await session.updateProfile();
      onNotify(`${updated.name} updated.`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const handleRevert = async () => {
    try {
      await session.revertProfile();
      onNotify("Reverted to the saved collection selection.", "info");
    } catch (error) {
      onError(error);
    }
  };

  const handleRename = async (profile: ModProfile) => {
    const name = renameDraft.trim();
    if (!name) {
      setRenameError("Profile names cannot be empty.");
      return;
    }
    try {
      await session.renameProfile(profile.id, name);
      setRenameID("");
      setRenameError("");
      onNotify("Profile renamed.", "success");
    } catch (error) {
      setRenameError(messageFor(error));
      onError(error);
    }
  };

  const handleDelete = async () => {
    if (!deleteTarget) return;
    const deleting = deleteTarget;
    try {
      await session.deleteProfile(deleting.id);
      setDeleteTarget(null);
      onNotify(
        deleting.id === session.profileId
          ? "Profile deleted; the current selection is now Default."
          : "Profile deleted; collections and mod files were not changed.",
        "success",
      );
    } catch (error) {
      onError(error);
    }
  };

  const handlePlay = async () => {
    try {
      const nextResult = await session.launchSelection();
      if (nextResult.error || !nextResult.applied || !nextResult.started) {
        onNotify(resultSummary(nextResult), nextResult.processUncertain ? "error" : "info");
      } else {
        onNotify(resultSummary(nextResult), "success");
      }
    } catch (error) {
      onError(error);
    }
  };

  const openCardMenu = (collectionOrId: ModCollection | string, event: MouseEvent<HTMLElement>) => {
    event.preventDefault();
    const id = typeof collectionOrId === "string" ? collectionOrId : collectionOrId.id;
    setCardMenu({ id, x: event.clientX, y: event.clientY });
  };

  const cardMenuIsExcluded = cardMenu ? excludedIDs.has(cardMenu.id) : false;
  const cardMenuCollection = cardMenu ? byID[cardMenu.id] : null;
  const cardActions = cardMenuCollection
    ? [
        {
          label: "Open collection details",
          icon: "folder" as const,
          onClick: () => {
            onOpenCollection(cardMenuCollection.id);
            setCardMenu(null);
          },
        },
        // Include/remove — hidden when the card is excluded.
        ...(cardMenuIsExcluded ? [] : [{
          label: selectedIDs.has(cardMenuCollection.id) ? "Remove from Play selection" : "Add to Play selection",
          icon: selectedIDs.has(cardMenuCollection.id) ? ("unlink" as const) : ("play" as const),
          disabled: selectionBusy,
          onClick: () => {
            void session.toggleCollection(cardMenuCollection.id).catch(onError);
            setCardMenu(null);
          },
        }]),
        // Exclude / stop excluding.
        {
          label: cardMenuIsExcluded ? "Stop excluding" : "Exclude from Play",
          icon: cardMenuIsExcluded ? ("plus" as const) : ("close" as const),
          danger: !cardMenuIsExcluded,
          disabled: selectionBusy,
          onClick: () => {
            void session.toggleExclusion(cardMenuCollection.id).catch(onError);
            setCardMenu(null);
          },
        },
      ]
    : [];

  // Explains the profile controls, including why they are unavailable.
  const profileHelp = !profilesExist
    ? "A profile remembers a set of collections so you can switch between setups. You have none yet: pick collections, then use Save as profile."
    : session.profileId
      ? "Switch between saved collection sets. Play always uses what is selected right now."
      : "Default is your current, unsaved selection. Choose a saved profile to load its collections.";

  const hasAnySelection = session.selection.length > 0 || session.excludedSelection.length > 0;

  return (
    <Page title="Play" className="play-view" ariaLabel="Play">
      <div className="play-body">
        <section className="play-main" aria-label="Collections">
          {session.notices.length > 0 && (
            <div className="play-notices" role="status" aria-live="polite">
              <Icon name="warning" size={15} />
              <div>{session.notices.map((notice) => <p key={notice}>{notice}</p>)}</div>
            </div>
          )}
          {session.previewError && (
            <div className="play-error" role="alert"><Icon name="error" size={15} /><span>{session.previewError}</span><Button tone="quiet" icon="refresh" onClick={() => void session.refreshPreview()}>Retry</Button></div>
          )}

          {!organization ? (
            <div className="play-loading"><Spinner /><span>Loading collections</span></div>
          ) : filteredCollections.length === 0 ? (
            <p className="play-empty">Group mods into a collection, then pick it here.</p>
          ) : (
            <div className="play-card-grid" aria-label="Collections">
              {/* All mods: a first-class entry backed by the all-mods sentinel. */}
              <article
                className={`collection-card play-all-mods-card${allModsSelected ? " is-selected" : ""}`}
              >
                <button
                  type="button"
                  className={`collection-card__check${allModsSelected ? " is-checked" : ""}`}
                  aria-label={allModsSelected ? "Deselect All mods" : "Select All mods"}
                  aria-pressed={allModsSelected}
                  disabled={selectionBusy}
                  onClick={() => void session.toggleCollection(ALL_MODS_ID).catch(onError)}
                >
                  {allModsSelected && <Icon name="check" size={15} />}
                </button>
                <button
                  type="button"
                  className="collection-card__main"
                  aria-label="All mods — your entire library"
                  aria-pressed={allModsSelected}
                  disabled={selectionBusy}
                  onClick={() => void session.toggleCollection(ALL_MODS_ID).catch(onError)}
                >
                  <span className="collection-card__art">
                    <span className="collection-card__fallback play-all-mods-card__icon" aria-hidden="true">
                      <Icon name="globe" size={32} />
                    </span>
                    <span className="collection-card__shade" />
                  </span>
                  <span className="collection-card__identity">
                    <strong>All mods</strong>
                    <span>Your entire library</span>
                  </span>
                </button>
              </article>

              {filteredCollections.map((collection) => {
                const excluded = excludedIDs.has(collection.id);
                const explicit = selectedIDs.has(collection.id);
                // Under "All mods" every collection is already in, so a click
                // leaves it out: "everything except these".
                const underAllMods = allModsSelected && !explicit && !excluded;
                const inherited = excluded
                  ? undefined
                  : underAllMods
                    ? "All mods"
                    : byID[inheritedParentIDByID[collection.id] ?? ""]?.name;
                const toggleExclusion = () => void session.toggleExclusion(collection.id).catch(onError);
                return (
                  <div key={collection.id} className={excluded ? "play-card-excluded" : undefined}>
                    <CollectionCard
                      collection={collection}
                      selected={excluded ? false : explicit}
                      inherited={excluded ? undefined : inherited}
                      toggleLabel={excluded ? `Include ${collection.name} again` : underAllMods ? `Leave out ${collection.name}` : undefined}
                      onOpen={() => onOpenCollection(collection.id)}
                      onToggle={
                        selectionBusy ? undefined
                        : excluded || underAllMods ? toggleExclusion
                        : inherited ? undefined
                        : () => void session.toggleCollection(collection.id).catch(onError)
                      }
                      onContextMenu={(event) => openCardMenu(collection, event)}
                      onMenu={(event) => openCardMenu(collection, event)}
                    />
                  </div>
                );
              })}
            </div>
          )}
        </section>

      </div>

      <footer className="play-launch-bar" aria-label="Play controls">
        {(() => {
          const running = session.progress && !session.progress.done;
          const finishedResult = Boolean(session.progress?.done && session.result);
          // A backend-emitted failure has terminal progress but no result.
          const failure = session.progress?.done && !session.result ? session.progress.error : "";
          const warning = session.runtime?.warning ?? "";
          if (!running && !finishedResult && !failure && !warning) return null;
          return <div className={`play-launch-status${session.result?.error || failure ? " is-error" : ""}`} role="status" aria-live="polite">
            {running && session.progress && <><Spinner small /><span>{session.progress.current || session.progress.phase || "Applying selection"}</span><progress value={session.progress.total ? session.progress.completed : 0} max={Math.max(session.progress.total, 1)} /></>}
            {finishedResult && session.result && <span>{resultSummary(session.result)}</span>}
            {failure && <span>{failure}</span>}
            {warning && !running && !failure && <span><Icon name="warning" size={13} /> {warning}</span>}
          </div>;
        })()}
        <div className="play-launch-controls">
          {/* Disabled controls never receive hover events, so the explanation
              lives on an always-enabled wrapper. */}
          <div className="play-profile" title={profileHelp}>
            <select
              id="play-profile-select"
              value={session.profileId}
              onChange={(event) => selectProfile(event.target.value)}
              disabled={!profilesExist || selectionBusy}
              aria-label="Profile"
              aria-describedby="play-profile-help"
            >
              <option value="">Default</option>
              {profiles.map((profile) => <option key={profile.id} value={profile.id}>{dirtyProfileLabel(profile)}</option>)}
            </select>
            <div className="play-profile__meta">
              <button type="button" className="play-link" onClick={() => setManagerOpen(true)} disabled={!profilesExist || selectionBusy}>Manage profiles</button>
              {session.profileId && session.dirty && <span className="play-unsaved">Not saved</span>}
            </div>
            <span id="play-profile-help" className="sr-only">{profileHelp}</span>
          </div>
          <div className="play-launch-actions">
            {hasAnySelection && <button type="button" className="play-link" disabled={session.previewLoading || !session.preview} onClick={() => setReviewOpen(true)}>View mods</button>}
            {hasAnySelection && <button type="button" className="play-link" disabled={selectionBusy} onClick={() => void session.clearSelection().catch(onError)}>Clear</button>}
            {!session.profileId && hasAnySelection && <button type="button" className="play-link" disabled={selectionBusy} onClick={() => void handleCreateProfile(false)}>Save as profile</button>}
            {session.profileId && session.dirty && <>
              <button type="button" className="play-link" disabled={selectionBusy} onClick={() => void handleUpdate()}>Update</button>
              <button type="button" className="play-link" disabled={selectionBusy} onClick={() => void handleRevert()}>Revert</button>
            </>}
            {session.profileId && <button type="button" className="play-link" disabled={selectionBusy} onClick={() => void handleCreateProfile(true)}>Save as new</button>}
          </div>

          <div className="play-launch-primary">
            <span className="play-readiness">{readinessText}</span>
            <button
              type="button"
              className="play-launch-button"
              disabled={selectionBusy || session.previewLoading || !session.preview}
              aria-busy={session.operationBusy === "launch"}
              onClick={() => void handlePlay()}
            >
              {launchArt.previous && (
                <span
                  key={`leaving-${launchArt.step}`}
                  className="play-launch-button__art is-leaving"
                  style={{ backgroundImage: `url(${launchArt.previous})` }}
                  aria-hidden="true"
                />
              )}
              <span
                key={`entering-${launchArt.step}`}
                className="play-launch-button__art is-entering"
                style={{ backgroundImage: `url(${launchArt.current})` }}
                aria-hidden="true"
              />
              <span className="play-launch-button__glass" aria-hidden="true" />
              <span className="play-launch-button__label"><Icon name="play" size={17} />Play</span>
            </button>
          </div>
        </div>
      </footer>

      {cardMenu && cardMenuCollection && <CollectionMenuPopup label={cardMenuCollection.name} x={cardMenu.x} y={cardMenu.y} actions={cardActions} onClose={() => setCardMenu(null)} />}

      {reviewOpen && <CollectionDialog title="Included mods" onClose={() => setReviewOpen(false)} wide footer={<Button onClick={() => setReviewOpen(false)}>Done</Button>}>
        <div className="play-dialog-intro">{session.preview?.modCount ?? 0} mods{(session.preview?.excludedModCount ?? 0) > 0 ? ` (${session.preview!.excludedModCount} excluded)` : ""}</div>
        {session.preview?.warnings?.map((warning) => <div className="play-dialog-warning" key={warning}><Icon name="warning" size={14} /><span>{warning}</span></div>)}
        <div className="play-mod-review">
          {(session.preview?.mods ?? []).map((mod) => <article className={`play-mod-review__row${mod.available ? "" : " is-missing"}`} key={mod.entityId}>
            <div className="play-mod-review__image">{mod.thumbnailUrl ? <img src={thumbUrl(mod.thumbnailUrl)} alt="" width={44} height={28} decoding="async" loading="lazy" /> : <Icon name="archive" size={18} />}</div>
            <div className="play-mod-review__copy"><strong>{mod.displayName}</strong><span>{mod.archivePath || mod.entityId}</span><small>{modProvenance(mod, byID)}</small></div>
            <Badge tone={mod.available ? "success" : "danger"}>{mod.available ? "Available" : "Missing"}</Badge>
          </article>)}
        </div>
        {!session.preview?.mods?.length && <p className="play-dialog-copy">No mods in this selection.</p>}
      </CollectionDialog>}

      {switchRequest && <CollectionDialog title="Unsaved Play changes" onClose={() => setSwitchRequest(null)} footer={<><Button tone="quiet" disabled={selectionBusy} onClick={() => setSwitchRequest(null)}>Cancel</Button><Button tone="quiet" icon="trash" disabled={selectionBusy} onClick={() => void discardAndSwitch()}>Discard changes and switch</Button><Button tone="primary" icon="save" disabled={selectionBusy} onClick={() => void updateAndSwitch()}>Update and switch</Button></>}>
        <p className="play-dialog-copy">{activeProfileName} has unsaved changes.</p>
      </CollectionDialog>}

      {managerOpen && <CollectionDialog title="Manage profiles" onClose={() => setManagerOpen(false)} footer={<Button onClick={() => setManagerOpen(false)}>Close</Button>}>
        
        <div className="play-profile-list">
          {profiles.map((profile) => renameID === profile.id ? <div className="play-profile-row is-editing" key={profile.id}><input autoFocus value={renameDraft} onChange={(event) => { setRenameDraft(event.target.value); setRenameError(""); }} onKeyDown={(event) => { if (event.key === "Enter") void handleRename(profile); if (event.key === "Escape") { setRenameID(""); setRenameError(""); } }} aria-label={`Rename ${profile.name}`} /><div><Button tone="quiet" icon="close" onClick={() => { setRenameID(""); setRenameError(""); }}>Cancel</Button><Button tone="primary" icon="save" onClick={() => void handleRename(profile)}>Save</Button></div>{renameError && <small className="play-inline-error">{renameError}</small>}</div> : <div className="play-profile-row" key={profile.id}><div><strong>{dirtyProfileLabel(profile)}</strong><span>{profile.modCount} mod{profile.modCount === 1 ? "" : "s"}, {profile.collectionCount} collection{profile.collectionCount === 1 ? "" : "s"}</span></div><div><Button tone="quiet" icon="edit" onClick={() => { setRenameID(profile.id); setRenameDraft(profile.name); setRenameError(""); }}>Rename</Button>{profile.id !== session.profileId && <Button tone="quiet" icon="trash" onClick={() => setDeleteTarget(profile)}>Delete</Button>}</div></div>)}
          {!profiles.length && <div className="play-manager-empty"><Icon name="user" size={20} /><strong>No saved profiles</strong></div>}
        </div>
      </CollectionDialog>}

      {deleteTarget && <CollectionDialog title={`Delete ${deleteTarget.name}?`} onClose={() => setDeleteTarget(null)} footer={<><Button tone="quiet" onClick={() => setDeleteTarget(null)}>Cancel</Button><Button tone="danger" icon="trash" disabled={selectionBusy} onClick={() => void handleDelete()}>Delete profile</Button></>}>
        <p className="play-dialog-copy">Only this saved selection will be deleted. Collections, mod archives, and the currently applied BeamNG selection remain untouched.</p>
        {deleteTarget.id === session.profileId && <div className="play-dialog-warning"><Icon name="warning" size={14} /><span>This is the active profile. Its current visible draft, including unsaved changes, will be detached into Default.</span></div>}
      </CollectionDialog>}

    </Page>
  );
}
