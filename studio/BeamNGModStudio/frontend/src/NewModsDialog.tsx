import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ModCollection, NewModsReview } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import { CollectionDialog } from "./CollectionUI";
import { Icon } from "./icons";
import { Button, formatBytes, kindIcon, kindLabel, thumbUrl } from "./ui";
import { NewBadge } from "./NewBadge";
import "./NewModsDialog.css";

interface NewModsDialogProps {
  review: NewModsReview;
  collections: ModCollection[];
  onResolve: (reviewedIDs: string[], addIDs: string[], collectionIDs: string[]) => Promise<void>;
  onOrganizationChanged: () => void;
  onError: (error: unknown) => void;
}

function originLabel(origin: string): string {
  if (origin === "beamng") return "Installed in BeamNG";
  return "Added to your library folder";
}

export function NewModsDialog({
  review,
  collections,
  onResolve,
  onOrganizationChanged,
  onError,
}: NewModsDialogProps) {
  const arrivals = review.arrivals ?? [];
  const reviewedIDs = useMemo(() => arrivals.map((a) => a.item.entityId), [arrivals]);
  const [checkedIDs, setCheckedIDs] = useState<Set<string>>(() => new Set(reviewedIDs));
  const [selectedCollectionIDs, setSelectedCollectionIDs] = useState<Set<string>>(
    () => new Set(review.suggestedCollectionIds ?? []),
  );
  const [creatingCollection, setCreatingCollection] = useState(false);
  const [newCollectionName, setNewCollectionName] = useState("");
  const [resolving, setResolving] = useState(false);
  const createInputRef = useRef<HTMLInputElement>(null);
  const knownIDsRef = useRef<Set<string>>(new Set(reviewedIDs));

  // Arrivals detected while the dialog is open start checked; the user's
  // choices for mods already shown are kept.
  useEffect(() => {
    const fresh = reviewedIDs.filter((id) => !knownIDsRef.current.has(id));
    if (fresh.length === 0) return;
    for (const id of fresh) knownIDsRef.current.add(id);
    setCheckedIDs((prev) => new Set([...prev, ...fresh]));
  }, [reviewedIDs]);

  const toggleMod = useCallback((entityId: string) => {
    setCheckedIDs((prev) => {
      const next = new Set(prev);
      if (next.has(entityId)) next.delete(entityId);
      else next.add(entityId);
      return next;
    });
  }, []);

  const toggleCollection = useCallback((id: string) => {
    setSelectedCollectionIDs((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const handleCreateCollection = useCallback(async () => {
    const name = newCollectionName.trim();
    if (!name) return;
    try {
      const detail = await API.CreateCollection(name, "", "");
      setNewCollectionName("");
      setCreatingCollection(false);
      setSelectedCollectionIDs((prev) => new Set([...prev, detail.collection.id]));
      onOrganizationChanged();
    } catch (error) {
      onError(error);
    }
  }, [newCollectionName, onOrganizationChanged, onError]);

  const checkedCount = arrivals.filter((a) => checkedIDs.has(a.item.entityId)).length;
  const collectionCount = selectedCollectionIDs.size;
  const canSubmit = checkedCount > 0 && collectionCount > 0;

  const handleAdd = useCallback(async () => {
    if (!canSubmit || resolving) return;
    setResolving(true);
    const addIDs = arrivals.filter((a) => checkedIDs.has(a.item.entityId)).map((a) => a.item.entityId);
    try {
      await onResolve(reviewedIDs, addIDs, [...selectedCollectionIDs]);
    } finally {
      setResolving(false);
    }
  }, [canSubmit, resolving, arrivals, checkedIDs, reviewedIDs, selectedCollectionIDs, onResolve]);

  const handleCancel = useCallback(() => {
    if (resolving) return;
    setResolving(true);
    void onResolve(reviewedIDs, [], []).finally(() => setResolving(false));
  }, [resolving, reviewedIDs, onResolve]);

  const addLabel =
    checkedCount === 0 || collectionCount === 0
      ? "Add to collections"
      : `Add ${checkedCount} ${checkedCount === 1 ? "mod" : "mods"} to ${collectionCount} ${collectionCount === 1 ? "collection" : "collections"}`;

  const suggested = useMemo(() => new Set(review.suggestedCollectionIds ?? []), [review.suggestedCollectionIds]);

  return (
    <CollectionDialog
      title={`Studio found ${arrivals.length} new ${arrivals.length === 1 ? "mod" : "mods"}`}
      onClose={handleCancel}
      wide
      footer={
        <div className="new-mods-dialog__actions">
          <Button tone="quiet" onClick={handleCancel} disabled={resolving}>
            Don't add
          </Button>
          <Button tone="primary" onClick={handleAdd} disabled={!canSubmit || resolving}>
            {addLabel}
          </Button>
        </div>
      }
    >
      <div className="new-mods-dialog">
        <section className="new-mods-dialog__mods">
          {arrivals.map((arrival) => {
            const item = arrival.item;
            const checked = checkedIDs.has(item.entityId);
            const thumbnail = thumbUrl(item.thumbnailUrl);
            return (
              <label key={item.entityId} className={`new-mods-dialog__mod${checked ? " is-checked" : ""}`}>
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => toggleMod(item.entityId)}
                />
                <span className="new-mods-dialog__mod-thumb">
                  {thumbnail ? (
                    <img src={thumbnail} alt="" loading="lazy" decoding="async" />
                  ) : (
                    <Icon name={kindIcon(String(item.kind))} size={18} />
                  )}
                </span>
                <span className="new-mods-dialog__mod-info">
                  <span className="new-mods-dialog__mod-name">
                    <strong>{item.displayName}</strong>
                    <NewBadge />
                  </span>
                  <span className="new-mods-dialog__mod-meta">
                    {kindLabel(String(item.kind))} · {formatBytes(item.sizeBytes)} · {originLabel(arrival.origin)}
                  </span>
                </span>
              </label>
            );
          })}
        </section>

        <section className="new-mods-dialog__collections">
          <h3>Add to collections</h3>
          <div className="new-mods-dialog__collection-list">
            {collections.map((collection) => {
              const selected = selectedCollectionIDs.has(collection.id);
              const isSuggested = suggested.has(collection.id);
              return (
                <label key={collection.id} className={`new-mods-dialog__collection${selected ? " is-selected" : ""}`}>
                  <input
                    type="checkbox"
                    checked={selected}
                    onChange={() => toggleCollection(collection.id)}
                  />
                  <span>
                    {collection.name}
                    {isSuggested && <small> · Play selection</small>}
                  </span>
                </label>
              );
            })}
          </div>
          {creatingCollection ? (
            <form
              className="new-mods-dialog__create-form"
              onSubmit={(e) => { e.preventDefault(); void handleCreateCollection(); }}
            >
              <input
                ref={createInputRef}
                type="text"
                placeholder="Collection name"
                value={newCollectionName}
                onChange={(e) => setNewCollectionName(e.target.value)}
                onKeyDown={(e) => {
                  // Escape leaves the name field, not the whole review.
                  if (e.key !== "Escape") return;
                  e.preventDefault();
                  setCreatingCollection(false);
                }}
                autoFocus
              />
              <Button
                tone="primary"
                disabled={!newCollectionName.trim()}
                onClick={() => void handleCreateCollection()}
              >
                Create
              </Button>
              <Button tone="quiet" onClick={() => setCreatingCollection(false)}>
                Cancel
              </Button>
            </form>
          ) : (
            <button
              type="button"
              className="new-mods-dialog__create-btn"
              onClick={() => { setCreatingCollection(true); requestAnimationFrame(() => createInputRef.current?.focus()); }}
            >
              <Icon name="plus" size={14} />
              New collection
            </button>
          )}
        </section>
      </div>
    </CollectionDialog>
  );
}
