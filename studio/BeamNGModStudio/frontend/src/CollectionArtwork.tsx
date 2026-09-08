import { useEffect, useMemo, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  CollectionArtworkCandidate,
  CollectionCover,
  CollectionCoverImage,
  CollectionDetail,
  LibraryItem,
  ModCollection,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import { Button, Spinner } from "./ui";
import { CollectionDialog } from "./CollectionUI";
import "./CollectionArtwork.css";

type CoverMode = "automatic" | "single" | "collage";

type ArtworkChoice = CollectionCoverImage & {
  entityID?: string;
  path?: string;
  name?: string;
  url?: string;
};

interface CollectionArtworkProps {
  collection: ModCollection;
  items: LibraryItem[];
  onSaved: (detail: CollectionDetail) => void;
  onClose: () => void;
  onError: (error: unknown) => void;
}

const MAX_COLLAGE_IMAGES = 9;
const assetIDPattern = /^[a-f0-9]{64}$/i;

export function CollectionCoverEditor({
  collection,
  items,
  onSaved,
  onClose,
  onError,
}: CollectionArtworkProps) {
  const initialCover = normalizeCover(collection.cover);
  const [mode, setMode] = useState<CoverMode>(initialCover.mode);
  const [selected, setSelected] = useState<ArtworkChoice[]>(() =>
    initialCover.images.map((image) => ({
      ...image,
      url: `/cache/${image.assetId}`,
      name: "Saved artwork",
    })),
  );
  const [query, setQuery] = useState("");
  const [entityID, setEntityID] = useState(items[0]?.entityId ?? "");
  const [candidates, setCandidates] = useState<CollectionArtworkCandidate[]>([]);
  const [candidateBusy, setCandidateBusy] = useState("");
  const [loadingCandidates, setLoadingCandidates] = useState(false);
  const [replaceIndex, setReplaceIndex] = useState<number | null>(null);
  const [candidateOverrides, setCandidateOverrides] = useState<Record<string, CollectionArtworkCandidate>>({});
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [failedAssets, setFailedAssets] = useState<Set<string>>(new Set());

  const filteredItems = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return items
      .filter((item) => !needle || item.displayName.toLocaleLowerCase().includes(needle))
      .sort((left, right) => left.displayName.localeCompare(right.displayName));
  }, [items, query]);

  useEffect(() => {
    if (!entityID || filteredItems.length === 0) return;
    if (!filteredItems.some((item) => item.entityId === entityID)) {
      setEntityID(filteredItems[0].entityId);
    }
  }, [entityID, filteredItems]);

  useEffect(() => {
    let active = true;
    if (!entityID) {
      setCandidates([]);
      return () => {
        active = false;
      };
    }
    setLoadingCandidates(true);
    API.GetModArtwork(entityID)
      .then((result) => {
        if (active) setCandidates(result ?? []);
      })
      .catch((reason) => {
        if (!active) return;
        setCandidates([]);
        reportError(reason, onError, setError);
      })
      .finally(() => {
        if (active) setLoadingCandidates(false);
      });
    return () => {
      active = false;
    };
  }, [entityID, onError]);

  const currentItem = items.find((item) => item.entityId === entityID);
  const automaticURL = collection.coverUrl || currentItem?.thumbnailUrl || "";
  const previewImages = mode === "automatic" ? [] : selected;
  const failedSelectedCount = selected.filter((image) => failedAssets.has(image.assetId)).length;
  const markAssetFailed = (assetID: string) => {
    setFailedAssets((previous) => {
      if (previous.has(assetID)) return previous;
      const next = new Set(previous);
      next.add(assetID);
      return next;
    });
  };

  const chooseMode = (next: CoverMode) => {
    setError("");
    setReplaceIndex(null);
    setMode(next);
    if (next === "automatic") {
      setSelected([]);
      return;
    }
    if (next === "single" && selected.length > 1) {
      setSelected((images) => images.slice(0, 1));
    }
  };

  const chooseCandidate = async (candidate: CollectionArtworkCandidate) => {
    if (!entityID) return;
    const candidateKey = `${entityID}:${candidate.path}`;
    const known = candidateOverrides[candidateKey] ?? candidate;
    if (mode === "collage" && selected.length >= MAX_COLLAGE_IMAGES && replaceIndex === null) {
      setError(`A collage can contain at most ${MAX_COLLAGE_IMAGES} distinct images.`);
      return;
    }
    setCandidateBusy(candidateKey);
    setError("");
    try {
      let image: CollectionCoverImage;
      if (known.assetId && assetIDPattern.test(known.assetId)) {
        image = { assetId: known.assetId, focalX: 0.5, focalY: 0.5 };
      } else {
        image = await API.CacheModArtwork(entityID, candidate.path);
      }
      const choice: ArtworkChoice = {
        ...image,
        entityID,
        path: candidate.path,
        name: candidate.name || candidate.path,
        url: `/cache/${image.assetId}`,
      };
      setCandidateOverrides((previous) => ({
        ...previous,
        [candidateKey]: { ...known, ...candidate, assetId: image.assetId, url: `/cache/${image.assetId}` },
      }));
      setSelected((previous) => {
        const duplicateIndex = previous.findIndex((entry) => entry.assetId === choice.assetId);
        if (duplicateIndex >= 0 && duplicateIndex !== replaceIndex) {
          setError("That cached image is already selected. Choose a different preview.");
          return previous;
        }
        if (mode === "single") return [choice];
        if (replaceIndex !== null) {
          const next = [...previous];
          next[replaceIndex] = choice;
          return next;
        }
        return [...previous, choice];
      });
      setFailedAssets((previous) => {
        if (!previous.has(choice.assetId)) return previous;
        const next = new Set(previous);
        next.delete(choice.assetId);
        return next;
      });
      setReplaceIndex(null);
    } catch (reason) {
      reportError(reason, onError, setError);
    } finally {
      setCandidateBusy("");
    }
  };

  const removeImage = (index: number) => {
    setSelected((previous) => previous.filter((_, current) => current !== index));
    setReplaceIndex(null);
  };

  const moveImage = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= selected.length) return;
    setSelected((previous) => {
      const next = [...previous];
      [next[index], next[target]] = [next[target], next[index]];
      return next;
    });
  };

  const handleSelectedKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>, index: number) => {
    if (event.key === "ArrowUp" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      moveImage(index, -1);
    } else if (event.key === "ArrowDown" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      moveImage(index, 1);
    }
  };

  const updateFocal = (index: number, axis: "focalX" | "focalY", value: number) => {
    setSelected((previous) =>
      previous.map((image, current) => (current === index ? { ...image, [axis]: value } : image)),
    );
  };

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      const images: CollectionCoverImage[] = selected.map(({ assetId, focalX, focalY }) => ({
        assetId,
        focalX,
        focalY,
      }));
      if (mode === "single" && images.length !== 1) {
        throw new Error("Choose one image for Single image mode.");
      }
      if (mode === "collage" && (images.length < 2 || images.length > MAX_COLLAGE_IMAGES)) {
        throw new Error(`Choose two through ${MAX_COLLAGE_IMAGES} images for Collage mode.`);
      }
      const detail = await API.SetCollectionCover(collection.id, { mode, images });
      onSaved(detail);
      onClose();
    } catch (reason) {
      reportError(reason, onError, setError);
    } finally {
      setSaving(false);
    }
  };

  const resetAutomatic = () => {
    setMode("automatic");
    setSelected([]);
    setReplaceIndex(null);
    setError("");
  };

  return (
    <CollectionDialog
      title={`Edit cover · ${collection.name}`}
      onClose={onClose}
      wide
      footer={
        <div className="collection-artwork-editor__footer">
          <Button type="button" tone="quiet" onClick={resetAutomatic} disabled={saving}>
            Reset to automatic
          </Button>
          <span className="collection-artwork-editor__footer-spacer" />
          <Button type="button" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button type="button" tone="primary" icon="save" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save cover"}
          </Button>
        </div>
      }
    >
      <div className="collection-artwork-editor">
        <div className="collection-artwork-editor__intro">
          <span className="collection-artwork-editor__ratio">16:9</span>
        </div>

        <section className="collection-artwork-editor__preview-panel" aria-label="Live cover preview">
          <div
            className={`collection-artwork-preview collection-artwork-preview--${mode} collection-artwork-preview--count-${previewImages.length}`}
            aria-label={`${mode} cover preview`}
          >
            {mode === "automatic" ? (
              automaticURL ? (
                <img src={automaticURL} alt="" loading="lazy" />
              ) : (
                <FallbackCover label="Automatic preview" />
              )
            ) : previewImages.length > 0 ? (
              previewImages.map((image, index) => (
                <div className="collection-artwork-preview__tile" key={`${image.assetId}-${index}`}>
                  {failedAssets.has(image.assetId) ? (
                    <UnavailableArtwork />
                  ) : (
                    <img
                      src={image.url || `/cache/${image.assetId}`}
                      alt=""
                      loading="lazy"
                      style={{ objectPosition: `${image.focalX * 100}% ${image.focalY * 100}%` }}
                      onError={() => markAssetFailed(image.assetId)}
                    />
                  )}
                  <span>{index + 1}</span>
                </div>
              ))
            ) : (
              <FallbackCover label={mode === "single" ? "Choose one preview" : "Choose two to nine previews"} />
            )}
          </div>
          <div className="collection-artwork-editor__preview-caption">
            <span>Same crop as collection cards and Play.</span>
          </div>
        </section>

        <div className="collection-artwork-editor__modes" role="tablist" aria-label="Cover mode">
          {(["automatic", "single", "collage"] as CoverMode[]).map((option) => (
            <button
              key={option}
              type="button"
              className={`collection-artwork-mode${mode === option ? " is-active" : ""}`}
              role="tab"
              aria-selected={mode === option}
              onClick={() => chooseMode(option)}
            >
              <span>{option === "automatic" ? "Automatic" : option === "single" ? "Single image" : "Collage"}</span>
              <small>
                {option === "automatic"
                  ? "Deterministic member preview"
                  : option === "single"
                    ? "One image, any indexed mod"
                    : `Two–${MAX_COLLAGE_IMAGES} ordered images`}
              </small>
            </button>
          ))}
        </div>

        {mode !== "automatic" && (
          <div className="collection-artwork-editor__workspace">
            <section className="collection-artwork-editor__library" aria-label="Preview library">
              <div className="collection-artwork-editor__section-heading">
                <div>
                  <h3>Indexed previews</h3>
                  <p>Search the library, then choose a discovered preview or vehicle variant.</p>
                </div>
                <span className="collection-artwork-editor__limit">
                  {mode === "collage" ? `${selected.length}/${MAX_COLLAGE_IMAGES}` : `${selected.length}/1`}
                </span>
              </div>
              <label className="collection-artwork-editor__search">
                <Icon name="search" size={16} />
                <span className="sr-only">Search mods</span>
                <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search mods" />
              </label>
              <div className="collection-artwork-editor__mod-list" role="listbox" aria-label="Mods with discovered previews">
                {filteredItems.length === 0 ? (
                  <p className="collection-artwork-editor__empty">No indexed mods match this search.</p>
                ) : (
                  filteredItems.map((item) => (
                    <button
                      key={item.entityId}
                      type="button"
                      role="option"
                      aria-selected={item.entityId === entityID}
                      className={`collection-artwork-editor__mod${item.entityId === entityID ? " is-active" : ""}`}
                      onClick={() => setEntityID(item.entityId)}
                    >
                      <span className="collection-artwork-editor__mod-image">
                        {item.thumbnailUrl ? <img src={item.thumbnailUrl} alt="" loading="lazy" /> : <Icon name="archive" size={16} />}
                      </span>
                      <span className="collection-artwork-editor__mod-name">{item.displayName || "Unnamed mod"}</span>
                      <span className="collection-artwork-editor__mod-kind">{String(item.kind)}</span>
                    </button>
                  ))
                )}
              </div>
              <div className="collection-artwork-editor__candidate-heading">
                <h4>Discovered images</h4>
                {loadingCandidates && <Spinner small />}
              </div>
              <div className="collection-artwork-editor__candidate-grid" role="list" aria-label="Discovered image candidates">
                {!loadingCandidates && candidates.length === 0 ? (
                  <p className="collection-artwork-editor__empty">This mod has no indexed image candidates.</p>
                ) : (
                  candidates.map((candidate) => {
                    const key = `${entityID}:${candidate.path}`;
                    const shown = candidateOverrides[key] ?? candidate;
                    const busy = candidateBusy === key;
                    return (
                      <button
                        key={key}
                        type="button"
                        className="collection-artwork-candidate"
                        onClick={() => chooseCandidate(candidate)}
                        disabled={busy || saving}
                        aria-label={`Choose ${candidate.name || candidate.path}`}
                      >
                        <span className="collection-artwork-candidate__image">
                          {shown.url ? <img src={shown.url} alt="" loading="lazy" /> : <Icon name="archive" size={22} />}
                          {busy && <span className="collection-artwork-candidate__loading"><Spinner small /></span>}
                        </span>
                        <span className="collection-artwork-candidate__name" title={candidate.path}>{candidate.name || candidate.path}</span>
                        <span className="collection-artwork-candidate__path">{candidate.path}</span>
                      </button>
                    );
                  })
                )}
              </div>
            </section>

            <section className="collection-artwork-editor__selection" aria-label="Selected artwork">
              <div className="collection-artwork-editor__section-heading">
                <div>
                  <h3>{mode === "single" ? "Selected image" : "Collage order"}</h3>
                  <p>{mode === "single" ? "Replace the image at any time." : "Order is preserved. Use buttons or Ctrl/⌘ + arrow keys to move."}</p>
                  {failedSelectedCount > 0 && <span className="collection-artwork-editor__repair" role="status">{failedSelectedCount} saved image{failedSelectedCount === 1 ? "" : "s"} unavailable — replace before saving.</span>}
                </div>
              </div>
              {selected.length === 0 ? (
                <div className="collection-artwork-editor__selection-empty">
                  <Icon name="archive" size={22} />
                  <span>{mode === "single" ? "Choose one preview on the left." : "Choose at least two previews on the left."}</span>
                </div>
              ) : (
                <div className="collection-artwork-editor__selected-list">
                  {selected.map((image, index) => (
                    <div
                      className={`collection-artwork-selected${replaceIndex === index ? " is-replacing" : ""}`}
                      key={`${image.assetId}-${index}`}
                      tabIndex={0}
                      onKeyDown={(event) => handleSelectedKeyDown(event, index)}
                    >
                      <div className="collection-artwork-selected__thumb">
                        {failedAssets.has(image.assetId) ? (
                          <UnavailableArtwork />
                        ) : (
                          <img
                            src={image.url || `/cache/${image.assetId}`}
                            alt=""
                            loading="lazy"
                            style={{ objectPosition: `${image.focalX * 100}% ${image.focalY * 100}%` }}
                            onError={() => markAssetFailed(image.assetId)}
                          />
                        )}
                        <span>{index + 1}</span>
                      </div>
                      <div className="collection-artwork-selected__body">
                        <strong>{image.name || "Saved artwork"}</strong>
                        <code>{image.assetId.slice(0, 12)}…</code>
                        <label>
                          <span>Focal horizontal</span>
                          <input
                            type="range"
                            min="0"
                            max="1"
                            step="0.01"
                            value={image.focalX}
                            aria-label={`Image ${index + 1} focal horizontal position`}
                            onChange={(event) => updateFocal(index, "focalX", Number(event.target.value))}
                          />
                        </label>
                        <label>
                          <span>Focal vertical</span>
                          <input
                            type="range"
                            min="0"
                            max="1"
                            step="0.01"
                            value={image.focalY}
                            aria-label={`Image ${index + 1} focal vertical position`}
                            onChange={(event) => updateFocal(index, "focalY", Number(event.target.value))}
                          />
                        </label>
                      </div>
                      <div className="collection-artwork-selected__actions">
                        {mode === "collage" && (
                          <>
                            <Button type="button" className="collection-artwork-icon-button" aria-label={`Move image ${index + 1} earlier`} onClick={() => moveImage(index, -1)} disabled={index === 0 || saving}>
                              <Icon name="arrow" size={14} className="collection-artwork-arrow-up" />
                            </Button>
                            <Button type="button" className="collection-artwork-icon-button" aria-label={`Move image ${index + 1} later`} onClick={() => moveImage(index, 1)} disabled={index === selected.length - 1 || saving}>
                              <Icon name="arrow" size={14} className="collection-artwork-arrow-down" />
                            </Button>
                          </>
                        )}
                        <Button type="button" className="collection-artwork-icon-button" onClick={() => setReplaceIndex(index)} disabled={saving}>
                          Replace
                        </Button>
                        <Button type="button" className="collection-artwork-icon-button" tone="danger" aria-label={`Remove image ${index + 1}`} onClick={() => removeImage(index)} disabled={saving}>
                          <Icon name="trash" size={14} />
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </section>
          </div>
        )}
        {error && <p className="collection-artwork-editor__error" role="alert">{error}</p>}
      </div>
    </CollectionDialog>
  );
}

function normalizeCover(cover: CollectionCover | undefined): { mode: CoverMode; images: CollectionCoverImage[] } {
  const mode: CoverMode = cover?.mode === "single" || cover?.mode === "collage" ? cover.mode : "automatic";
  return { mode, images: Array.isArray(cover?.images) ? cover.images : [] };
}

function reportError(reason: unknown, onError: (error: unknown) => void, setError: (message: string) => void) {
  onError(reason);
  const message = reason instanceof Error ? reason.message : String(reason || "Artwork operation failed");
  setError(message);
}

function FallbackCover({ label }: { label: string }) {
  return (
    <div className="collection-artwork-preview__fallback">
      <span className="collection-artwork-preview__fallback-mark"><Icon name="archive" size={28} /></span>
      <strong>{label}</strong>
      <small>BeamWorlds collection cover</small>
    </div>
  );
}
function UnavailableArtwork() {
  return (
    <span className="collection-artwork-unavailable" role="img" aria-label="Cached artwork unavailable">
      <Icon name="warning" size={20} />
      <small>Unavailable</small>
    </span>
  );
}
