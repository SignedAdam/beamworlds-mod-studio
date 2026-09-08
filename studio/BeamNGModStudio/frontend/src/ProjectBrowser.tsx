import { useEffect, useMemo, useState } from "react";
import type {
  LibraryItem,
  WorkspaceRecord,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import {
  Button,
  EmptyState,
  Page,
  kindIcon,
  kindLabel,
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
}: {
  record: WorkspaceRecord;
  item?: LibraryItem;
  onOpen: (workspaceID: string) => void;
}) {
  const thumbnailUrl = item?.thumbnailUrl?.trim() || "";
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
}: {
  workspaces: WorkspaceRecord[];
  allItems: LibraryItem[];
  onOpen: (workspaceID: string) => void;
  onNew: () => void;
}) {
  const [query, setQuery] = useState("");
  const [display, setDisplay] = useState<DisplayMode>(readDisplayMode);

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
              />
            ))}
          </div>
        ) : (
          <ModTable
            items={tableItems}
            workspaceRecords={visible}
            interaction={{
              kind: "browse",
              selectedID: "",
              onActivate: (item) => {
                const record = visible.find(
                  (candidate) => candidate.entityId === item.entityId,
                );
                if (record) onOpen(record.id);
              },
            }}
            ariaLabel="Mod Maker workspaces"
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
    </Page>
  );
}
