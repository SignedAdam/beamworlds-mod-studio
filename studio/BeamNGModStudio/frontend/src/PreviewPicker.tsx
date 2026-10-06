import { useEffect, useRef, useState } from "react";
import { Dialogs } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ArchiveMemberPreview,
  EntityPreviewCandidate,
  LibraryItem,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { CollectionDialog } from "./CollectionUI";
import { Icon } from "./icons";
import { Badge, Button, EmptyState, Spinner, formatBytes, thumbUrl } from "./ui";
import "./PreviewPicker.css";

interface PreviewPickerProps {
  item: LibraryItem;
  disabled: boolean;
  onChanged: (item: LibraryItem) => void;
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>;
  onError: (error: unknown) => void;
}

const currentPreviewWidth = 180;
const currentPreviewHeight = 102;
const candidatePreviewWidth = 160;
const candidatePreviewHeight = 90;

export function PreviewPicker({ item, disabled, onChanged, onPreviewMember, onError }: PreviewPickerProps) {
  const [open, setOpen] = useState(false);
  const [candidates, setCandidates] = useState<EntityPreviewCandidate[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [automaticSelected, setAutomaticSelected] = useState(false);
  const [failedCandidates, setFailedCandidates] = useState<Set<string>>(new Set());
  const [currentFailed, setCurrentFailed] = useState(false);
  const currentURL = thumbUrl(item.thumbnailUrl);

  useEffect(() => {
    setOpen(false);
    setCandidates([]);
    setLoading(false);
    setBusy("");
    setError("");
    setAutomaticSelected(false);
    setFailedCandidates(new Set());
  }, [item.entityId]);

  useEffect(() => {
    setCurrentFailed(false);
  }, [currentURL]);

  useEffect(() => {
    if (!open) return;
    let active = true;
    setLoading(true);
    setCandidates([]);
    setFailedCandidates(new Set());
    setError("");
    API.EntityPreviewCandidates(item.entityId)
      .then((result) => {
        if (!active) return;
        const next = result ?? [];
        setCandidates(next);
        setAutomaticSelected(next.some((candidate) => candidate.automatic && candidate.selected));
      })
      .catch((reason) => {
        if (!active) return;
        setCandidates([]);
        reportFailure(reason, "Preview candidates could not be loaded.", onError, setError);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [item.entityId, onError, open]);

  const closePicker = () => {
    setOpen(false);
    setError("");
  };

  const chooseCandidate = async (candidate: EntityPreviewCandidate) => {
    if (disabled || busy || !item.linked) return;
    setBusy(candidate.memberPath);
    setError("");
    try {
      const next = await API.SetEntityPreviewFromArchive(item.entityId, candidate.memberPath);
      onChanged(next);
      setAutomaticSelected(false);
      setOpen(false);
    } catch (reason) {
      reportFailure(reason, "The selected preview could not be applied.", onError, setError);
    } finally {
      setBusy("");
    }
  };

  const uploadImage = async () => {
    if (disabled || busy) return;
    setBusy("upload");
    setError("");
    try {
      const selected = await Dialogs.OpenFile({
        CanChooseDirectories: false,
        CanChooseFiles: true,
        AllowsMultipleSelection: false,
        AllowsOtherFiletypes: false,
        Filters: [{ DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg" }],
        Title: "Choose a mod preview image",
        ButtonText: "Use image",
      });
      const sourcePath = Array.isArray(selected) ? selected[0] ?? "" : selected;
      if (!sourcePath) return;
      const next = await API.SetEntityPreviewFromFile(item.entityId, sourcePath);
      onChanged(next);
      setAutomaticSelected(false);
      setOpen(false);
    } catch (reason) {
      reportFailure(reason, "The selected image could not be applied.", onError, setError);
    } finally {
      setBusy("");
    }
  };

  const resetAutomatic = async () => {
    if (disabled || busy || automaticSelected) return;
    setBusy("reset");
    setError("");
    try {
      const next = await API.ResetEntityPreview(item.entityId);
      onChanged(next);
      setAutomaticSelected(true);
      setOpen(false);
    } catch (reason) {
      reportFailure(reason, "The automatic preview could not be restored.", onError, setError);
    } finally {
      setBusy("");
    }
  };

  const markCandidateFailed = (memberPath: string) => {
    setFailedCandidates((current) => {
      if (current.has(memberPath)) return current;
      const next = new Set(current);
      next.add(memberPath);
      return next;
    });
  };

  return (
    <fieldset className="inspector-fieldset preview-picker">
      <legend>Preview</legend>
      <div className="preview-picker__current">
        <span className="preview-picker__current-image">
          {currentURL && !currentFailed ? (
            <img
              src={currentURL}
              alt=""
              width={currentPreviewWidth}
              height={currentPreviewHeight}
              decoding="async"
              onError={() => setCurrentFailed(true)}
            />
          ) : (
            <Icon name={String(item.kind) === "vehicle" ? "vehicle" : "archive"} size={28} />
          )}
        </span>
        <div className="preview-picker__current-copy">
          <strong>{currentURL && !currentFailed ? "Current thumbnail" : "No thumbnail"}</strong>
          <span>Choose another indexed image or upload your own.</span>
          <Button
            type="button"
            icon="edit"
            className="inspector-button inspector-button--primary"
            disabled={disabled}
            aria-haspopup="dialog"
            onClick={() => setOpen(true)}
          >
            Change
          </Button>
        </div>
      </div>
      {open && (
        <CollectionDialog
          title={`Change preview · ${item.displayName}`}
          onClose={closePicker}
          wide
          footer={
            <div className="preview-picker__dialog-footer">
              <Button type="button" icon="filePlus" onClick={() => void uploadImage()} disabled={Boolean(busy) || disabled}>
                Upload an image…
              </Button>
              <span />
              <Button type="button" tone="quiet" onClick={() => void resetAutomatic()} disabled={Boolean(busy) || disabled || automaticSelected}>
                Reset to automatic
              </Button>
              <Button type="button" onClick={closePicker} disabled={Boolean(busy)}>
                Cancel
              </Button>
            </div>
          }
        >
          <div className="preview-picker__dialog">
            <p className="preview-picker__dialog-intro">
              Choose an image discovered inside this mod, or upload a file from your computer.
            </p>
            {loading ? (
              <div className="preview-picker__loading" role="status">
                <Spinner />
                <span>Loading preview candidates</span>
              </div>
            ) : candidates.length === 0 ? (
              <EmptyState
                icon="archive"
                title="No indexed preview candidates"
                detail="Upload an image to set a custom preview."
              />
            ) : (
              <div className="preview-picker__candidate-grid" role="list" aria-label="Mod preview candidates">
                {candidates.map((candidate) => {
                  const candidateKey = candidate.memberPath;
                  const candidateURL = candidate.assetSha
                    ? `/cache/${candidate.assetSha}`
                    : candidate.selected
                      ? item.thumbnailUrl
                      : "";
                  const thumbnail = thumbUrl(candidateURL);
                  const candidateBusy = busy === candidateKey;
                  const failed = failedCandidates.has(candidateKey);
                  const label = candidate.label || candidate.memberPath;
                  return (
                    <button
                      key={candidateKey}
                      type="button"
                      className={`preview-picker__candidate${candidate.selected ? " is-selected" : ""}`}
                      role="listitem"
                      aria-pressed={candidate.selected}
                      aria-label={`${label}, ${candidate.width} by ${candidate.height} pixels${candidate.selected ? ", current" : ""}`}
                      disabled={Boolean(busy) || disabled || !item.linked}
                      onClick={() => void chooseCandidate(candidate)}
                    >
                      <span className="preview-picker__candidate-image">
                        <PreviewCandidateImage
                          memberPath={candidate.memberPath}
                          thumbnailURL={thumbnail}
                          failed={failed}
                          onFailed={() => markCandidateFailed(candidateKey)}
                          onPreviewMember={onPreviewMember}
                        />
                        {candidateBusy && (
                          <span className="preview-picker__candidate-loading">
                            <Spinner small />
                          </span>
                        )}
                      </span>
                      <span className="preview-picker__candidate-copy">
                        <strong title={label}>{label}</strong>
                        <span className="preview-picker__candidate-dimensions">
                          {candidate.width} × {candidate.height} px · {formatBytes(candidate.sizeBytes)}
                        </span>
                        <span className="preview-picker__candidate-badges">
                          {candidate.selected && <Badge tone="accent">Current</Badge>}
                          {candidate.automatic && <Badge tone="warning">Automatic</Badge>}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
            {error && (
              <p className="preview-picker__error" role="alert">
                <Icon name="error" size={16} />
                <span>{error}</span>
              </p>
            )}
          </div>
        </CollectionDialog>
      )}
    </fieldset>
  );
}

function PreviewCandidateImage({
  memberPath,
  thumbnailURL,
  failed,
  onFailed,
  onPreviewMember,
}: {
  memberPath: string;
  thumbnailURL: string;
  failed: boolean;
  onFailed: () => void;
  onPreviewMember: (memberPath: string) => Promise<ArchiveMemberPreview | null>;
}) {
  const [previewURL, setPreviewURL] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const previewRef = useRef(onPreviewMember);
  const failedRef = useRef(onFailed);
  const imageRef = useRef<HTMLSpanElement>(null);
  previewRef.current = onPreviewMember;
  failedRef.current = onFailed;

  useEffect(() => {
    setPreviewURL(thumbnailURL);
    setPreviewLoading(false);
    if (thumbnailURL || failed) return;
    let cancelled = false;
    let requested = false;
    const load = () => {
      if (requested) return;
      requested = true;
      setPreviewLoading(true);
      void previewRef.current(memberPath)
        .then((preview) => {
          if (cancelled) return;
          const next = preview?.dataUrl ?? "";
          if (next) setPreviewURL(next);
          else failedRef.current();
        })
        .catch(() => {
          if (!cancelled) failedRef.current();
        })
        .finally(() => {
          if (!cancelled) setPreviewLoading(false);
        });
    };
    const element = imageRef.current;
    if (!element || typeof IntersectionObserver === "undefined") {
      load();
      return () => {
        cancelled = true;
      };
    }
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      observer.disconnect();
      load();
    }, { root: element.closest(".preview-picker__candidate-grid"), rootMargin: "120px 0px" });
    observer.observe(element);
    return () => {
      cancelled = true;
      observer.disconnect();
    };
  }, [failed, memberPath, thumbnailURL]);

  return (
    <span ref={imageRef} className="preview-picker__candidate-image-content">
      {previewURL && !failed ? (
        <img
          src={previewURL}
          alt=""
          width={candidatePreviewWidth}
          height={candidatePreviewHeight}
          loading="lazy"
          decoding="async"
          onError={onFailed}
        />
      ) : (
        <Icon name="archive" size={24} />
      )}
      {previewLoading && (
        <span className="preview-picker__candidate-preview-loading">
          <Spinner small />
        </span>
      )}
    </span>
  );
}

function reportFailure(
  reason: unknown,
  fallback: string,
  onError: (error: unknown) => void,
  setError: (message: string) => void,
) {
  onError(reason);
  setError(reason instanceof Error && reason.message ? reason.message : fallback);
}
