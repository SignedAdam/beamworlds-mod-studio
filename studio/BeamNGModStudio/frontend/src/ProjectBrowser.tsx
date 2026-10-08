import { useEffect, useMemo, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import type {
  LibraryItem,
  WorkspaceRecord,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import {
  CollectionDialog,
  CollectionMenuPopup,
  type CollectionMenuAction,
} from "./CollectionUI";
import {
  Button,
  EmptyState,
  Page,
  kindIcon,
  kindLabel,
  thumbUrl,
} from "./ui";
import { Icon } from "./icons";
import { ModTable } from "./ModTable";

type DisplayMode = "cards" | "table";
const displayStorageKey = "beamworlds.modmaker-display.v1";

const compactDateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
});

function readDisplayMode(): DisplayMode {
  try {
    const value = window.localStorage.getItem(displayStorageKey);
    return value === "cards" || value === "table" ? value : "table";
  } catch {
    return "table";
  }
}

function compactDate(value?: string): string {
  if (!value) return "Not recorded";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : compactDateFormatter.format(date);
}

function friendlyProcess(process: string): string {
  if (!process) return "";
  return process
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/[_.-]+/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function hasVirgilWork(record: WorkspaceRecord): boolean {
  const hasNonIdleStatus = Boolean(
    record.agentStatus && record.agentStatus.toLowerCase() !== "idle",
  );
  return (
    hasNonIdleStatus ||
    Boolean(record.agentGoal) ||
    Boolean(record.agentProcess) ||
    Boolean(record.agentUpdatedAt)
  );
}

function processLabel(record: WorkspaceRecord): string {
  const process = friendlyProcess(record.agentProcess || "");
  const status = friendlyProcess(record.agentStatus || "");
  return process && process.toLowerCase() !== status.toLowerCase()
    ? process
    : "";
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  return "The project could not be deleted.";
}

function VirgilWork({ record }: { record: WorkspaceRecord }) {
  const running = record.agentStatus?.toLowerCase() === "running";
  const process = processLabel(record);
  if (!hasVirgilWork(record)) return null;
  return (
    <div className="project-browser__virgil-work">
      <strong>{running ? "Virgil working" : "Latest Virgil run"}</strong>
      {record.agentStatus && (
        <span>Status: {friendlyProcess(record.agentStatus)}</span>
      )}
      {record.agentGoal && <span>{record.agentGoal}</span>}
      {process && <span>{process}</span>}
      {record.agentUpdatedAt && (
        <time dateTime={record.agentUpdatedAt}>
          Updated {compactDate(record.agentUpdatedAt)}
        </time>
      )}
      {running && <progress aria-label="Virgil is working" />}
    </div>
  );
}

function ProjectCard({
  record,
  item,
  onOpen,
  onContextMenu,
  onShowMenu,
}: {
  record: WorkspaceRecord;
  item?: LibraryItem;
  onOpen: (workspaceID: string) => void;
  onContextMenu: (
    record: WorkspaceRecord,
    event: ReactMouseEvent<HTMLElement>,
  ) => void;
  onShowMenu: (record: WorkspaceRecord, x: number, y: number) => void;
}) {
  const thumbnailUrl = thumbUrl(item?.thumbnailUrl);
  const [thumbnailFailed, setThumbnailFailed] = useState(false);
  const hasThumbnail = Boolean(thumbnailUrl) && !thumbnailFailed;

  useEffect(() => {
    setThumbnailFailed(false);
  }, [thumbnailUrl]);

  return (
    <button
      type="button"
      className="project-card"
      onClick={() => onOpen(record.id)}
      onContextMenu={(event) => onContextMenu(record, event)}
      onKeyDown={(event) => {
        if (
          event.key === "ContextMenu" ||
          (event.shiftKey && event.key === "F10")
        ) {
          event.preventDefault();
          event.stopPropagation();
          const rect = event.currentTarget.getBoundingClientRect();
          onShowMenu(record, rect.right - 8, rect.top + 8);
        }
      }}
      aria-label={`Open ${record.displayName}`}
    >
      <div
        className={`project-card__cover${hasThumbnail ? " has-thumbnail" : ""}`}
        style={
          hasThumbnail
            ? { backgroundImage: `url(${JSON.stringify(thumbnailUrl)})` }
            : undefined
        }
      >
        {hasThumbnail ? (
          <img
            src={thumbnailUrl}
            alt=""
            loading="lazy"
            onError={() => setThumbnailFailed(true)}
          />
        ) : (
          <div className="project-card__fallback">
            <span className="project-card__fallback-mark">
              <Icon name={kindIcon(String(record.kind))} size={25} />
            </span>
            <span>No preview available</span>
          </div>
        )}
        <div className="project-card__gradient" aria-hidden="true" />
        <span className="project-card__type">
          {kindLabel(String(record.kind))}
        </span>
      </div>
      <div className="project-card__body">
        <h2 title={record.displayName}>{record.displayName}</h2>
        <dl className="project-card__details">
          <div>
            <dt>Last modified</dt>
            <dd>
              <time dateTime={record.updatedAt}>
                {compactDate(record.updatedAt)}
              </time>
            </dd>
          </div>
        </dl>
        <VirgilWork record={record} />
      </div>
    </button>
  );
}


export function ProjectBrowser({
  workspaces,
  allItems,
  onOpen,
  onNew,
  onDelete,
}: {
  workspaces: WorkspaceRecord[];
  allItems: LibraryItem[];
  onOpen: (workspaceID: string) => void;
  onNew: () => void;
  onDelete: (workspaceID: string) => Promise<void>;
}) {
  const [query, setQuery] = useState("");
  const [display, setDisplay] = useState<DisplayMode>(readDisplayMode);
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(() => new Set());
  const [contextMenu, setContextMenu] = useState<{
    record: WorkspaceRecord;
    x: number;
    y: number;
  } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<WorkspaceRecord | null>(
    null,
  );
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState("");

  useEffect(() => {
    try {
      window.localStorage.setItem(displayStorageKey, display);
    } catch {}
  }, [display]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return workspaces.filter(
      (record) =>
        !needle ||
        [record.displayName, record.kind, record.status, record.root].some(
          (value) => String(value).toLowerCase().includes(needle),
        ),
    );
  }, [workspaces, query]);

  const tableItems = useMemo(
    () =>
      visible
        .map((record) =>
          allItems.find((item) => item.entityId === record.entityId),
        )
        .filter((item): item is LibraryItem => Boolean(item)),
    [allItems, visible],
  );

  const openContextMenu = (
    record: WorkspaceRecord,
    event: ReactMouseEvent<HTMLElement>,
  ) => {
    event.preventDefault();
    const target = event.currentTarget;
    const rect =
      target instanceof HTMLElement ? target.getBoundingClientRect() : null;
    const x = event.clientX || rect?.right || 0;
    const y = event.clientY || rect?.bottom || 0;
    setContextMenu({
      record,
      x: Math.max(8, Math.min(x, window.innerWidth - 16)),
      y: Math.max(8, Math.min(y, window.innerHeight - 16)),
    });
  };

  const showMenuAt = (record: WorkspaceRecord, x: number, y: number) => {
    setContextMenu({
      record,
      x: Math.max(8, Math.min(x, window.innerWidth - 16)),
      y: Math.max(8, Math.min(y, window.innerHeight - 16)),
    });
  };

  const openTableContextMenu = (
    item: LibraryItem,
    event: ReactMouseEvent<HTMLTableRowElement>,
  ) => {
    const record = visible.find(
      (candidate) => candidate.entityId === item.entityId,
    );
    if (!record) return;
    openContextMenu(record, event);
  };

  const projectActions = (
    record: WorkspaceRecord,
  ): CollectionMenuAction[] => [
    {
      label: "Open",
      icon: "arrow",
      onClick: () => onOpen(record.id),
    },
    {
      label: "Delete project",
      icon: "trash",
      danger: true,
      onClick: () => {
        setDeleteTarget(record);
        setDeleteError("");
      },
    },
  ];

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleteBusy(true);
    setDeleteError("");
    try {
      await onDelete(deleteTarget.id);
      setDeleteTarget(null);
    } catch (error) {
      setDeleteError(errorMessage(error));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <Page
      title="ModMaker"
      actions={[
        {
          key: "new-mod",
          label: "New mod",
          icon: "plus",
          role: "primary",
          onClick: onNew,
        },
      ]}
      className="project-browser"
    >
      <div className="page-toolbar">
        <label className="search-box">
          <Icon name="search" size={16} />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Find a mod"
            aria-label="Find a mod"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              aria-label="Clear search"
            >
              <Icon name="close" size={13} />
            </button>
          )}
        </label>
        <div className="segmented" role="group" aria-label="Display mode">
          <button
            type="button"
            aria-pressed={display === "cards"}
            className={display === "cards" ? "is-active" : ""}
            onClick={() => setDisplay("cards")}
          >
            Cards
          </button>
          <button
            type="button"
            aria-pressed={display === "table"}
            className={display === "table" ? "is-active" : ""}
            onClick={() => setDisplay("table")}
          >
            Table
          </button>
        </div>
      </div>
      <div className="project-browser__content">
        {visible.length === 0 ? (
          <EmptyState
            icon="workspace"
            title={workspaces.length ? "No matching mods" : "No mods yet"}
            detail={
              workspaces.length
                ? "Try a different search."
                : "Create a mod to begin working in ModMaker."
            }
          />
        ) : display === "cards" ? (
          <div className="project-grid">
            {visible.map((record) => (
              <ProjectCard
                key={record.id}
                record={record}
                item={allItems.find(
                  (item) => item.entityId === record.entityId,
                )}
                onOpen={onOpen}
                onContextMenu={openContextMenu}
                onShowMenu={showMenuAt}
              />
            ))}
          </div>
        ) : (
          <ModTable
            items={tableItems}
            workspaceRecords={visible}
            interaction={{
              kind: "browse",
              selectedIDs,
              onSelectionChange: setSelectedIDs,
              onActivate: (item) => {
                const record = visible.find(
                  (candidate) => candidate.entityId === item.entityId,
                );
                if (record) onOpen(record.id);
              },
              onContextMenu: openTableContextMenu,
            }}
            ariaLabel="ModMaker workspaces"
            surface="mod-maker"
            className="project-browser__table"
            emptyTitle="No matching mods"
            resetKey={query}
          />
        )}
      </div>
      <div className="project-browser__create-action">
        <Button icon="plus" tone="primary" onClick={onNew}>
          Create New Mod
        </Button>
      </div>
      {contextMenu && (
        <CollectionMenuPopup
          label={`Actions for ${contextMenu.record.displayName}`}
          x={contextMenu.x}
          y={contextMenu.y}
          actions={projectActions(contextMenu.record)}
          onClose={() => setContextMenu(null)}
        />
      )}
      {deleteTarget && (
        <CollectionDialog
          title={`Delete this project?`}
          onClose={() => {
            if (!deleteBusy) {
              setDeleteTarget(null);
              setDeleteError("");
            }
          }}
          footer={
            <>
              <Button
                type="button"
                onClick={() => {
                  setDeleteTarget(null);
                  setDeleteError("");
                }}
                disabled={deleteBusy}
              >
                Cancel
              </Button>
              <Button
                type="button"
                tone="danger"
                disabled={deleteBusy}
                onClick={() => void confirmDelete()}
              >
                Delete project
              </Button>
            </>
          }
        >
          <p className="library-removal__copy">
            <strong>{deleteTarget.displayName}</strong> stays in your library exactly as it is now.
            Its ModMaker project, saved versions, and the original copy Studio kept go to the Recycle
            Bin, so you can no longer restore earlier versions here.
          </p>
          {deleteError && (
            <p className="collection-add__error" role="alert">
              {deleteError}
            </p>
          )}
        </CollectionDialog>
      )}
    </Page>
  );
}
