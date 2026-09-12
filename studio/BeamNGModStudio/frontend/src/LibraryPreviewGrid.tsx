import { useEffect, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import type { LibraryItem } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import type { ModFamilyBadge } from "./ModTable";
import { EmptyState, kindIcon, Spinner, thumbUrl } from "./ui";
import "./LibraryPreviewGrid.css";

export type LibraryPreviewSize = "small" | "medium" | "large";

const thumbnailDimensions: Record<LibraryPreviewSize, { width: number; height: number }> = {
  small: { width: 200, height: 116 },
  medium: { width: 260, height: 148 },
  large: { width: 340, height: 194 },
};

export interface LibraryPreviewGridProps {
  items: LibraryItem[];
  selectedID: string;
  selectedIDs?: ReadonlySet<string>;
  previewSize: LibraryPreviewSize;
  loading?: boolean;
  loadingLabel?: string;
  emptyTitle: string;
  ariaLabel: string;
  onSelect: (item: LibraryItem) => void;
  onToggle?: (item: LibraryItem) => void;
  onContextMenu: (
    item: LibraryItem,
    event: ReactMouseEvent<HTMLElement>,
  ) => void;
  familyByEntityID?: Record<string, ModFamilyBadge>;
  onReviewFamily?: (familyID: string) => void;
}

const healthIcons = {
  safe: "check",
  review: "warning",
  threat: "error",
  broken: "error",
  scan_failed: "error",
  scanning: "scan",
  unscanned: "shield",
} as const;

type HealthIconName = (typeof healthIcons)[keyof typeof healthIcons];

const healthLabels: Record<string, string> = {
  safe: "Safe",
  review: "Needs review",
  threat: "Threat detected",
  broken: "Broken",
  scan_failed: "Scan failed",
  scanning: "Scanning",
  unscanned: "Not scanned",
};

function normalizedHealthStatus(status: string): keyof typeof healthIcons {
  return status in healthIcons
    ? (status as keyof typeof healthIcons)
    : "unscanned";
}

function healthLabel(item: LibraryItem): string {
  const status = normalizedHealthStatus(item.healthStatus);
  return item.healthLabel || healthLabels[status];
}

function sourceLabel(item: LibraryItem): string {
  if (item.source === "BeamNG Repository" || item.source === "User added") {
    return item.source;
  }
  if (item.sourceId === "beamng-repository") return "BeamNG Repository";
  return "User added";
}

function healthDescription(status: string): string {
  switch (normalizedHealthStatus(status)) {
    case "safe":
      return "Latest scan found no threat signals";
    case "review":
      return "Latest scan found items that need review";
    case "threat":
      return "Latest scan found a high-risk threat";
    case "broken":
      return "The mod has structural errors";
    case "scan_failed":
      return "The latest scan failed";
    case "scanning":
      return "A virus scan is running";
    default:
      return "No virus scan has run";
  }
}
export function LibraryPreviewGrid({
  items,
  selectedID,
  selectedIDs,
  previewSize,
  loading = false,
  loadingLabel = "Filtering mods",
  emptyTitle,
  ariaLabel,
  onSelect,
  onToggle,
  onContextMenu,
  familyByEntityID,
  onReviewFamily,
}: LibraryPreviewGridProps) {
  return (
    <div className="library-preview-panel" aria-busy={loading}>
      {items.length === 0 ? (
        loading ? (
          <div className="center-loader" role="status">
            <Spinner />
            <span>{loadingLabel}</span>
          </div>
        ) : (
          <EmptyState icon="archive" title={emptyTitle} />
        )
      ) : (
        <div
          className={`library-preview-grid library-preview-grid--${previewSize}`}
          role="list"
          aria-label={ariaLabel}
        >
          {items.map((item) => (
            <LibraryPreviewCard
              key={item.entityId}
              item={item}
              thumbnailDimensions={thumbnailDimensions[previewSize]}
              selected={item.entityId === selectedID}
              bulkSelected={Boolean(selectedIDs?.has(item.entityId))}
              onSelect={onSelect}
              onToggle={onToggle}
              onContextMenu={onContextMenu}
              familyBadge={familyByEntityID?.[item.entityId]}
              onReviewFamily={onReviewFamily}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function LibraryPreviewCard({
  item,
  thumbnailDimensions,
  selected,
  bulkSelected,
  onSelect,
  onToggle,
  onContextMenu,
  familyBadge,
  onReviewFamily,
}: {
  item: LibraryItem;
  thumbnailDimensions: { width: number; height: number };
  selected: boolean;
  bulkSelected: boolean;
  onSelect: (item: LibraryItem) => void;
  onToggle?: (item: LibraryItem) => void;
  onContextMenu: (
    item: LibraryItem,
    event: ReactMouseEvent<HTMLElement>,
  ) => void;
  familyBadge?: ModFamilyBadge;
  onReviewFamily?: (familyID: string) => void;
}) {
  const thumbnailUrl = thumbUrl(item.thumbnailUrl);
  const [thumbnailFailed, setThumbnailFailed] = useState(false);
  const hasThumbnail = Boolean(thumbnailUrl) && !thumbnailFailed;
  const status = normalizedHealthStatus(item.healthStatus);
  const statusIcon = healthIcons[status] as HealthIconName;
  const name = item.displayName || "Unnamed mod";
  const source = sourceLabel(item);
  const statusLabel = healthLabel(item);
  const cardAccessibleName = `${name}, Source: ${source}, Status: ${statusLabel}`;
  useEffect(() => {
    setThumbnailFailed(false);
  }, [thumbnailUrl]);

  return (
    <article
      className={`library-preview-card${selected ? " is-selected" : ""}${bulkSelected ? " is-bulk-selected" : ""}`}
      role="listitem"
      aria-current={selected ? "true" : undefined}
    >
      {onToggle && <label className="library-preview-card__selection" onClick={(event) => event.stopPropagation()} onMouseDown={(event) => event.stopPropagation()}>
        <input type="checkbox" checked={bulkSelected} onChange={() => onToggle(item)} aria-label={`Select ${name}`} />
      </label>}
      <button
        type="button"
        className="library-preview-card__open"
        aria-label={`Open ${cardAccessibleName}`}
        aria-pressed={selected}
        onClick={() => onSelect(item)}
        onContextMenu={(event) => {
          event.preventDefault();
          onContextMenu(item, event);
        }}
      >
        <span
          className={`library-preview-card__thumbnail${hasThumbnail ? " has-thumbnail" : ""}`}
        >
          {hasThumbnail ? (
            <img
              src={thumbnailUrl}
              alt=""
              width={thumbnailDimensions.width}
              height={thumbnailDimensions.height}
              loading="lazy"
              decoding="async"
              onError={() => setThumbnailFailed(true)}
            />
          ) : (
            <span className="library-preview-card__fallback">
              <span className="library-preview-card__fallback-mark">
                <Icon name={kindIcon(String(item.kind))} size={26} />
              </span>
              <span>No preview</span>
            </span>
          )}
          <span className="library-preview-card__thumbnail-shade" aria-hidden="true" />
          <span
            className={`library-preview-card__status library-preview-card__status--${status}`}
            title={healthDescription(item.healthStatus)}
          >
            <Icon name={statusIcon} size={14} />
            <span>{statusLabel}</span>
          </span>
        </span>
        <span className={`library-preview-card__body${familyBadge ? " has-family-badge" : ""}`}>
          <span className="library-preview-card__name-row">
            <span className="library-preview-card__name" title={name}>
              {name}
            </span>
          </span>
          <span className="library-preview-card__source">{source}</span>
        </span>
      </button>
      {familyBadge && (
        <button
          type="button"
          className="library-preview-card__family-badge"
          aria-label={`Review duplicate family for ${name}: ${familyBadge.count} ${familyBadge.kind}`}
          title={`Review ${familyBadge.count} ${familyBadge.kind}`}
          onClick={() => onReviewFamily?.(familyBadge.familyId)}
          onContextMenu={(event) => {
            event.preventDefault();
            event.stopPropagation();
          }}
        >
          {familyBadge.count} {familyBadge.kind}
        </button>
      )}
      <button
        type="button"
        className="library-preview-card__actions"
        aria-label={`More actions for ${cardAccessibleName}`}
        aria-haspopup="menu"
        title="More actions"
        onClick={(event) => {
          event.stopPropagation();
          onContextMenu(item, event);
        }}
        onContextMenu={(event) => {
          event.preventDefault();
          event.stopPropagation();
          onContextMenu(item, event);
        }}
      >
        <Icon name="more" size={17} />
      </button>
    </article>
  );
}
