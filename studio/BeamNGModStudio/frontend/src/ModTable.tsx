import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  CSSProperties,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
} from "react";
import type {
  LibraryItem,
  WorkspaceRecord,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import { isTagIcon, tagColor } from "./tagIcons";
import "./ModTable.css";

import {
  Badge,
  Button,
  EmptyState,
  formatBytes,
  formatDate,
  kindIcon,
  kindLabel,
  Spinner,
  thumbUrl,
} from "./ui";

export type ModTableSortKey =
  | "enabled"
  | "name"
  | "path"
  | "kind"
  | "status"
  | "source"
  | "workspace"
  | "virgil"
  | "author"
  | "tags"
  | "files"
  | "variants"
  | "size"
  | "modified"
  | "lastScan"
  | "issues";
type SortKey = ModTableSortKey;
export type ModTableSortDirection = 1 | -1;
export interface ModTableSort {
  key: ModTableSortKey;
  direction: ModTableSortDirection;
}
type ColumnKey = "thumbnail" | SortKey;
type PageSize = 50 | 100 | 200 | 500 | "all";

interface ColumnState {
  key: ColumnKey;
  visible: boolean;
  width: number;
}

interface ColumnDefinition {
  label: string;
  defaultWidth: number;
  minWidth?: number;
  align?: "right";
}

export type ModTableInteraction =
  | {
      kind: "browse";
      selectedID: string;
      selectedIDs?: ReadonlySet<string>;
      disabled?: boolean;
      isSelectable?: (item: LibraryItem) => boolean;
      onToggle?: (item: LibraryItem) => void;
      onToggleAll?: () => void;
      selectAllLabel?: string;
      onActivate: (item: LibraryItem) => void;
      onContextMenu?: (
        item: LibraryItem,
        event: ReactMouseEvent<HTMLTableRowElement>,
      ) => void;
    }
  | {
      kind: "select";
      selectedIDs: ReadonlySet<string>;
      disabled?: boolean;
      isSelectable: (item: LibraryItem) => boolean;
      onToggle: (item: LibraryItem) => void;
      onToggleAll: () => void;
      selectAllLabel?: string;
      onContextMenu?: (
        item: LibraryItem,
        event: ReactMouseEvent<HTMLTableRowElement>,
      ) => void;
    };

export interface ModTableProps {
  items: LibraryItem[];
  workspaceRecords?: WorkspaceRecord[];
  interaction: ModTableInteraction;
  ariaLabel: string;
  surface: "library" | "virus-scanner" | "mod-maker" | "collection";
  className?: string;
  loading?: boolean;
  loadingLabel?: string;
  emptyTitle: string;
  resetKey?: string;
  sort?: ModTableSort;
  onSortChange?: (sort: ModTableSort) => void;
  enabledByEntityID?: ReadonlyMap<string, boolean>;
  onToggleEnabled?: (entityID: string, enabled: boolean) => void;
}

const columnDefinitions: Record<ColumnKey, ColumnDefinition> = {
  enabled: { label: "Enabled", defaultWidth: 104, minWidth: 86 },
  thumbnail: { label: "Thumbnail", defaultWidth: 88, minWidth: 52 },
  name: { label: "Name", defaultWidth: 270 },
  path: { label: "Path", defaultWidth: 360 },
  kind: { label: "Kind", defaultWidth: 110 },
  status: { label: "Status", defaultWidth: 146 },
  source: { label: "Source", defaultWidth: 170, minWidth: 116 },
  workspace: { label: "Workspace", defaultWidth: 130 },
  virgil: { label: "Status", defaultWidth: 146 },
  author: { label: "Author", defaultWidth: 180 },
  tags: { label: "Tags", defaultWidth: 250 },
  files: { label: "Files", defaultWidth: 78, align: "right" },
  variants: { label: "Variants", defaultWidth: 88, align: "right" },
  size: { label: "Size", defaultWidth: 92, align: "right" },
  modified: { label: "Modified", defaultWidth: 176 },
  lastScan: { label: "Last scan", defaultWidth: 176 },
  issues: { label: "Issues", defaultWidth: 76, align: "right" },
};
const allColumnOrder = Object.keys(columnDefinitions) as ColumnKey[];
const libraryColumnOrder = allColumnOrder.filter(
  (key) =>
    key !== "enabled" &&
    key !== "lastScan" &&
    key !== "workspace" &&
    key !== "virgil",
);
const collectionColumnOrder: ColumnKey[] = [
  "enabled",
  "thumbnail",
  "name",
  "kind",
  "tags",
  "size",
  "modified",
];
const scannerColumnOrder = allColumnOrder.filter(
  (key) =>
    key !== "enabled" &&
    key !== "source" &&
    key !== "workspace" &&
    key !== "virgil",
);
const modMakerColumnOrder: ColumnKey[] = [
  "thumbnail",
  "name",
  "path",
  "kind",
  "workspace",
  "virgil",
  "author",
  "tags",
  "files",
  "variants",
  "size",
  "modified",
  "issues",
];

const selectionColumnWidth = 40;
const pageSizes: PageSize[] = [50, 100, 200, 500, "all"];
const SOURCE_LABELS = {
  repository: "BeamNG Repository",
  userAdded: "User added",
} as const;


function sourceID(item: Pick<LibraryItem, "sourceId" | "source">): string {
  const id = String(item.sourceId ?? "").trim().toLowerCase();
  if (id === "beamng-repository" || id === "user-added") return id;
  const label = String(item.source ?? "").trim().toLowerCase();
  if (label === SOURCE_LABELS.repository.toLowerCase()) {
    return "beamng-repository";
  }
  return "user-added";
}

function sourceLabel(item: Pick<LibraryItem, "sourceId" | "source">): string {
  return sourceID(item) === "beamng-repository"
    ? SOURCE_LABELS.repository
    : SOURCE_LABELS.userAdded;
}

export function sortLibraryItems(
  items: readonly LibraryItem[],
  sort: ModTableSort,
): LibraryItem[] {
  return sortItems(items, sort, "library");
}

function sortItems(
  items: readonly LibraryItem[],
  sort: ModTableSort,
  surface: ModTableProps["surface"],
  workspaceByEntityID?: ReadonlyMap<string, WorkspaceRecord>,
  enabledByEntityID?: ReadonlyMap<string, boolean>,
): LibraryItem[] {
  const sorted = [...items];
  sorted.sort((left, right) =>
    compareTableItems(
      left,
      right,
      sort.key,
      sort.direction,
      surface,
      workspaceByEntityID,
      enabledByEntityID,
    ),
  );
  return sorted;
}

function compareTableItems(
  left: LibraryItem,
  right: LibraryItem,
  sortKey: SortKey,
  sortDirection: ModTableSortDirection,
  surface: ModTableProps["surface"],
  workspaceByEntityID?: ReadonlyMap<string, WorkspaceRecord>,
  enabledByEntityID?: ReadonlyMap<string, boolean>,
): number {
  const primary = compareValues(
    sortValue(
      left,
      sortKey,
      surface,
      workspaceByEntityID?.get(left.entityId),
      enabledByEntityID,
    ),
    sortValue(
      right,
      sortKey,
      surface,
      workspaceByEntityID?.get(right.entityId),
      enabledByEntityID,
    ),
  );
  if (primary !== 0 || sortKey !== "source")
    return primary * sortDirection;

  const nameTieBreak = compareValues(
    sortValue(
      left,
      "name",
      surface,
      workspaceByEntityID?.get(left.entityId),
      enabledByEntityID,
    ),
    sortValue(
      right,
      "name",
      surface,
      workspaceByEntityID?.get(right.entityId),
      enabledByEntityID,
    ),
  );
  if (nameTieBreak !== 0) return nameTieBreak * sortDirection;
  return compareValues(left.entityId, right.entityId) * sortDirection;
}

export function ModTable({
  items,
  workspaceRecords = [],
  interaction,
  ariaLabel,
  surface,
  className = "",
  loading = false,
  loadingLabel = "Updating results…",
  emptyTitle,
  resetKey = "",
  sort,
  onSortChange,
  enabledByEntityID,
  onToggleEnabled,
}: ModTableProps) {
  const preferenceKey =
    surface === "library"
      ? "beamworlds.library"
      : surface === "virus-scanner"
        ? "beamworlds.virus-scanner"
        : surface === "collection"
          ? "beamworlds.collection"
          : "beamworlds.modmaker";
  const columnOrder =
    surface === "library"
      ? libraryColumnOrder
      : surface === "mod-maker"
        ? modMakerColumnOrder
        : surface === "collection"
          ? collectionColumnOrder
          : scannerColumnOrder;
  const columnStorageKey = `${preferenceKey}-columns.v2`;
  const pageSizeStorageKey = `${preferenceKey}-page-size.v1`;
  const [page, setPage] = useState(0);
  const [pageSizeChoice, setPageSizeChoice] = useState<PageSize>(() =>
    readPageSize(pageSizeStorageKey),
  );
  const [internalSortKey, setInternalSortKey] = useState<SortKey | null>(() =>
    surface === "collection"
      ? null
      : surface === "mod-maker"
        ? "modified"
        : "name",
  );
  const [internalSortDirection, setInternalSortDirection] = useState<
    ModTableSortDirection
  >(() => (surface === "mod-maker" ? -1 : 1));
  const controlledSort = surface === "library" ? sort : undefined;
  const sortKey = controlledSort?.key ?? internalSortKey;
  const sortDirection = controlledSort?.direction ?? internalSortDirection;
  const [columns, setColumns] = useState<ColumnState[]>(() =>
    readColumns(columnStorageKey, columnOrder),
  );
  const [columnsOpen, setColumnsOpen] = useState(false);
  const [draggedColumn, setDraggedColumn] = useState<ColumnKey | null>(null);
  const lastResizePointer = useRef<{ key: ColumnKey; at: number } | null>(null);
  const interactionRef = useRef(interaction);
  interactionRef.current = interaction;
  const workspaceByEntityID = useMemo(
    () =>
      new Map(
        workspaceRecords.map((record) => [record.entityId, record] as const),
      ),
    [workspaceRecords],
  );

  const sorted = useMemo(
    () =>
      sortKey === null
        ? [...items]
        : sortItems(
            items,
            { key: sortKey, direction: sortDirection },
            surface,
            workspaceByEntityID,
            enabledByEntityID,
          ),
    [
      enabledByEntityID,
      items,
      sortDirection,
      sortKey,
      surface,
      workspaceByEntityID,
    ],
  );
  const pageSize =
    pageSizeChoice === "all" ? Math.max(1, sorted.length) : pageSizeChoice;
  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize));
  const visible = useMemo(
    () => sorted.slice(page * pageSize, (page + 1) * pageSize),
    [sorted, page, pageSize],
  );
  const visibleColumns = useMemo(
    () => columns.filter((column) => column.visible),
    [columns],
  );
  const selectionMode =
    interaction.kind === "select" || interaction.selectedIDs !== undefined;
  const selectionIDs = interaction.selectedIDs;
  const canSelect = interaction.kind === "select"
    ? interaction.isSelectable
    : interaction.isSelectable ?? (() => true);
  const selectionWidth = selectionMode ? selectionColumnWidth : 0;
  const tableWidth = useMemo(
    () =>
      visibleColumns.reduce(
        (total, column) => total + column.width,
        selectionWidth,
      ),
    [selectionWidth, visibleColumns],
  );
  const rangeStart = sorted.length === 0 ? 0 : page * pageSize + 1;
  const rangeEnd = Math.min(sorted.length, (page + 1) * pageSize);

  let selectableCount = 0;
  let selectedSelectableCount = 0;
  if (selectionMode && selectionIDs) {
    for (const item of items) {
      if (!canSelect(item)) continue;
      selectableCount++;
      if (selectionIDs.has(item.entityId)) selectedSelectableCount++;
    }
  }
  const allSelectableSelected =
    selectableCount > 0 && selectedSelectableCount === selectableCount;
  const someSelectableSelected =
    selectedSelectableCount > 0 && !allSelectableSelected;

  useEffect(
    () => setPage(0),
    [resetKey, sortKey, sortDirection, pageSizeChoice],
  );
  useEffect(() => {
    if (page >= pageCount) setPage(pageCount - 1);
  }, [page, pageCount]);
  useEffect(
    () =>
      window.localStorage.setItem(columnStorageKey, JSON.stringify(columns)),
    [columnStorageKey, columns],
  );
  useEffect(
    () =>
      window.localStorage.setItem(pageSizeStorageKey, String(pageSizeChoice)),
    [pageSizeChoice, pageSizeStorageKey],
  );

  const changeSort = useCallback(
    (key: SortKey) => {
      const direction: ModTableSortDirection =
        key === sortKey ? (sortDirection === 1 ? -1 : 1) : 1;
      const nextSort: ModTableSort = { key, direction };
      if (controlledSort) {
        onSortChange?.(nextSort);
        return;
      }
      setInternalSortKey(key);
      setInternalSortDirection(direction);
    },
    [controlledSort, onSortChange, sortDirection, sortKey],
  );

  const moveColumn = useCallback((source: ColumnKey, target: ColumnKey) => {
    if (source === target) return;
    setColumns((current) => {
      const next = [...current];
      const sourceIndex = next.findIndex((column) => column.key === source);
      const targetIndex = next.findIndex((column) => column.key === target);
      if (sourceIndex < 0 || targetIndex < 0) return current;
      const [moved] = next.splice(sourceIndex, 1);
      next.splice(targetIndex, 0, moved);
      return next;
    });
  }, []);

  const resizeColumn = useCallback(
    (event: ReactPointerEvent, key: ColumnKey, width: number) => {
      event.preventDefault();
      event.stopPropagation();
      const startX = event.clientX;
      const move = (pointer: PointerEvent) => {
        if (Math.abs(pointer.clientX - startX) > 2)
          lastResizePointer.current = null;
        setColumns((current) =>
          current.map((column) =>
            column.key === key
              ? {
                  ...column,
                  width: Math.max(
                    columnDefinitions[key].minWidth ?? 64,
                    Math.min(720, width + pointer.clientX - startX),
                  ),
                }
              : column,
          ),
        );
      };
      const finish = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", finish);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", finish);
    },
    [],
  );

  const autoSizeColumn = useCallback(
    (event: ReactPointerEvent<HTMLElement>, key: ColumnKey) => {
      event.preventDefault();
      event.stopPropagation();
      const header = event.currentTarget.closest(
        "th",
      ) as HTMLTableCellElement | null;
      const table = header?.closest("table") as HTMLTableElement | null;
      const sampleCell =
        header && table
          ? table.tBodies[0]?.rows[0]?.cells[header.cellIndex]
          : null;
      const headerContent = header?.querySelector<HTMLElement>(
        key === "thumbnail" ? ".mod-table__column-label" : "button",
      );
      if (!header || !sampleCell || !headerContent) return;
      const context = document.createElement("canvas").getContext("2d");
      if (!context) return;

      const headerStyle = window.getComputedStyle(headerContent);
      context.font = `${headerStyle.fontStyle} ${headerStyle.fontWeight} ${headerStyle.fontSize} ${headerStyle.fontFamily}`;
      const headerLabel = columnLabel(key, surface).toUpperCase();
      const headerLetterSpacing =
        Number.parseFloat(headerStyle.letterSpacing) || 0;
      const headerPadding =
        (Number.parseFloat(headerStyle.paddingLeft) || 0) +
        (Number.parseFloat(headerStyle.paddingRight) || 0);
      const sortMarkerWidth =
        key === "thumbnail" ? 0 : context.measureText("↕").width + 12;
      let optimalWidth =
        context.measureText(headerLabel).width +
        Math.max(0, headerLabel.length - 1) * headerLetterSpacing +
        headerPadding +
        sortMarkerWidth +
        2;

      const cellStyle = window.getComputedStyle(sampleCell);
      const cellPadding =
        (Number.parseFloat(cellStyle.paddingLeft) || 0) +
        (Number.parseFloat(cellStyle.paddingRight) || 0);
      if (key === "thumbnail") {
        const thumbnail =
          sampleCell.querySelector<HTMLElement>(".mod-table__icon");
        optimalWidth = Math.max(
          optimalWidth,
          (thumbnail?.getBoundingClientRect().width ?? 30) + cellPadding + 2,
        );
      } else {
        const textElement =
          key === "name"
            ? sampleCell.querySelector<HTMLElement>("strong")
            : key === "kind"
              ? sampleCell.querySelector<HTMLElement>(".badge")
              : (sampleCell.firstElementChild as HTMLElement | null);
        const textStyle = window.getComputedStyle(textElement ?? sampleCell);
        context.font = `${textStyle.fontStyle} ${textStyle.fontWeight} ${textStyle.fontSize} ${textStyle.fontFamily}`;
        const letterSpacing = Number.parseFloat(textStyle.letterSpacing) || 0;
        const contentPadding =
          key === "kind"
            ? (Number.parseFloat(textStyle.paddingLeft) || 0) +
              (Number.parseFloat(textStyle.paddingRight) || 0) +
              (Number.parseFloat(textStyle.borderLeftWidth) || 0) +
              (Number.parseFloat(textStyle.borderRightWidth) || 0)
            : 0;
        for (const item of items) {
          const text = cellText(
            item,
            key,
            surface,
            workspaceByEntityID.get(item.entityId),
            enabledByEntityID,
          );
          const textWidth =
            context.measureText(text).width +
            Math.max(0, text.length - 1) * letterSpacing;
          optimalWidth = Math.max(
            optimalWidth,
            textWidth + cellPadding + contentPadding + 2,
          );
        }
      }
      const width = Math.ceil(
        Math.max(
          columnDefinitions[key].minWidth ?? 64,
          Math.min(720, optimalWidth),
        ),
      );
      setColumns((current) =>
        current.map((column) =>
          column.key === key ? { ...column, width } : column,
        ),
      );
    },
    [enabledByEntityID, items, surface, workspaceByEntityID],
  );

  const beginColumnResize = useCallback(
    (event: ReactPointerEvent<HTMLElement>, key: ColumnKey, width: number) => {
      const previous = lastResizePointer.current;
      if (previous?.key === key && event.timeStamp - previous.at <= 500) {
        lastResizePointer.current = null;
        autoSizeColumn(event, key);
        return;
      }
      lastResizePointer.current = { key, at: event.timeStamp };
      resizeColumn(event, key, width);
    },
    [autoSizeColumn, resizeColumn],
  );

  const toggleColumn = (key: ColumnKey) =>
    setColumns((current) => {
      const shown = current.filter((column) => column.visible).length;
      return current.map((column) =>
        column.key === key
          ? {
              ...column,
              visible: column.visible ? (shown > 1 ? false : true) : true,
            }
          : column,
      );
    });

  const activateItem = useCallback((item: LibraryItem) => {
    const current = interactionRef.current;
    if (current.kind === "browse") current.onActivate(item);
    else if (!current.disabled && current.isSelectable(item))
      current.onToggle(item);
  }, []);

  const openContextMenu = useCallback(
    (item: LibraryItem, event: ReactMouseEvent<HTMLTableRowElement>) => {
      const current = interactionRef.current;
      if (!current.onContextMenu) return;
      current.onContextMenu(item, event);
    },
    [],
  );

  const rootClassName = `mod-table-panel${surface === "library" ? " mod-table-panel--library" : ""}${className ? ` ${className}` : ""}`;

  return (
    <div className={rootClassName} aria-busy={loading}>
      {visible.length === 0 ? (
        loading ? (
          <div className="center-loader" role="status">
            <Spinner />
            <span>{loadingLabel}</span>
          </div>
        ) : (
          <EmptyState icon="archive" title={emptyTitle} />
        )
      ) : (
        <div className="mod-table-wrap">
          <table
            className="mod-table"
            style={{ width: tableWidth }}
            aria-label={ariaLabel}
          >
            <colgroup>
              {selectionMode && (
                <col style={{ width: selectionColumnWidth }} />
              )}
              {visibleColumns.map((column) => (
                <col key={column.key} style={{ width: column.width }} />
              ))}
            </colgroup>
            <thead>
              <tr>
                {selectionMode && interaction.onToggleAll && (
                  <th className="mod-table__selection" scope="col">
                    <SelectAllCheckbox
                      checked={allSelectableSelected}
                      indeterminate={someSelectableSelected}
                      disabled={
                        Boolean(interaction.disabled) || selectableCount === 0
                      }
                      label={interaction.selectAllLabel ?? "Select all matching mods"}
                      onChange={interaction.onToggleAll}
                    />
                  </th>
                )}
                {visibleColumns.map((column) => (
                  <SortableHead
                    column={column}
                    surface={surface}
                    active={sortKey}
                    direction={sortDirection}
                    onSort={changeSort}
                    onDragStart={setDraggedColumn}
                    onDrop={(target) => {
                      if (draggedColumn) moveColumn(draggedColumn, target);
                      setDraggedColumn(null);
                    }}
                    onResize={beginColumnResize}
                  />
                ))}
              </tr>
            </thead>
            <tbody>
              {visible.map((item) => {
                const rowSelectable = selectionMode && canSelect(item);
                const selected = selectionMode
                  ? Boolean(selectionIDs?.has(item.entityId))
                  : item.entityId === interaction.selectedID;
                const disabled =
                  selectionMode &&
                  (Boolean(interaction.disabled) || !rowSelectable);
                return (
                  <ModRow
                    key={item.entityId}
                    item={item}
                    columns={visibleColumns}
                    surface={surface}
                    workspace={workspaceByEntityID.get(item.entityId)}
                    selectMode={selectionMode}
                    selected={selected}
                    disabled={disabled}
                    onActivate={activateItem}
                    onToggle={interaction.onToggle}
                    enabledByEntityID={enabledByEntityID}
                    onToggleEnabled={onToggleEnabled}
                    onContextMenu={
                      interaction.onContextMenu ? openContextMenu : undefined
                    }
                  />
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <footer className="pagination">
        <span className="pagination__summary" role="status" aria-live="polite">
          {loading
            ? "Updating results…"
            : sorted.length === 0
              ? "0 results"
              : `${rangeStart.toLocaleString()}–${rangeEnd.toLocaleString()} of ${sorted.length.toLocaleString()} results`}
        </span>
        <div className="pagination__pages">
          <Button
            tone="quiet"
            disabled={page === 0 || sorted.length === 0}
            onClick={() => setPage((value) => value - 1)}
          >
            Previous
          </Button>
          <span>
            {page + 1} / {pageCount}
          </span>
          <Button
            tone="quiet"
            disabled={page + 1 >= pageCount || sorted.length === 0}
            onClick={() => setPage((value) => value + 1)}
          >
            Next
          </Button>
        </div>
        <div className="pagination__settings">
          <div className="page-size">
            <span>Per page</span>
            {pageSizes.map((value) => (
              <button
                key={value}
                className={pageSizeChoice === value ? "is-active" : ""}
                onClick={() => setPageSizeChoice(value)}
              >
                {value}
              </button>
            ))}
          </div>
          <div className="column-control">
            <Button
              icon="columns"
              tone="quiet"
              onClick={() => setColumnsOpen((open) => !open)}
            >
              Columns
            </Button>
            {columnsOpen && (
              <div
                className="column-menu"
                role="dialog"
                aria-label="Visible and ordered columns"
              >
                <header>
                  <strong>Table columns</strong>
                  <button
                    className="icon-button"
                    onClick={() => setColumnsOpen(false)}
                    aria-label="Close column settings"
                  >
                    <Icon name="close" size={13} />
                  </button>
                </header>
                {columns.map((column) => (
                  <label
                    key={column.key}
                    draggable
                    onDragStart={() => setDraggedColumn(column.key)}
                    onDragOver={(event) => event.preventDefault()}
                    onDrop={() => {
                      if (draggedColumn) moveColumn(draggedColumn, column.key);
                      setDraggedColumn(null);
                    }}
                  >
                    <Icon name="more" size={14} />
                    <input
                      type="checkbox"
                      checked={column.visible}
                      disabled={column.visible && visibleColumns.length === 1}
                      onChange={() => toggleColumn(column.key)}
                    />
                    <span>{columnLabel(column.key, surface)}</span>
                  </label>
                ))}
              </div>
            )}
          </div>
        </div>
      </footer>
    </div>
  );
}

const SortableHead = memo(function SortableHead({
  column,
  surface,
  active,
  direction,
  onSort,
  onDragStart,
  onDrop,
  onResize,
}: {
  column: ColumnState;
  surface: ModTableProps["surface"];
  active: SortKey | null;
  direction: 1 | -1;
  onSort: (value: SortKey) => void;
  onDragStart: (value: ColumnKey) => void;
  onDrop: (value: ColumnKey) => void;
  onResize: (
    event: ReactPointerEvent<HTMLElement>,
    key: ColumnKey,
    width: number,
  ) => void;
}) {
  const key = column.key;
  if (key === "thumbnail") {
    return (
      <th
        draggable
        onDragStart={() => onDragStart(key)}
        onDragOver={(event) => event.preventDefault()}
        onDrop={() => onDrop(key)}
      >
        <span className="mod-table__column-label">
          {columnLabel(key, surface)}
        </span>
        <i
          className="column-resizer"
          title="Double-click to fit contents"
          onPointerDown={(event) => onResize(event, key, column.width)}
        />
      </th>
    );
  }
  const selected = key === active;
  return (
    <th
      draggable
      onDragStart={() => onDragStart(key)}
      onDragOver={(event) => event.preventDefault()}
      onDrop={() => onDrop(key)}
      aria-sort={
        selected ? (direction === 1 ? "ascending" : "descending") : "none"
      }
    >
      <button onClick={() => onSort(key)}>
        {columnLabel(key, surface)}
        <span>{selected ? (direction === 1 ? "▲" : "▼") : "↕"}</span>
      </button>
      <i
        className="column-resizer"
        title="Double-click to fit contents"
        onPointerDown={(event) => onResize(event, key, column.width)}
      />
    </th>
  );
});

const ModRow = memo(function ModRow({
  item,
  columns,
  surface,
  workspace,
  selectMode,
  selected,
  disabled,
  onActivate,
  onToggle,
  enabledByEntityID,
  onToggleEnabled,
  onContextMenu,
}: {
  item: LibraryItem;
  columns: ColumnState[];
  surface: ModTableProps["surface"];
  workspace?: WorkspaceRecord;
  selectMode: boolean;
  selected: boolean;
  disabled: boolean;
  onActivate: (item: LibraryItem) => void;
  onToggle?: (item: LibraryItem) => void;
  enabledByEntityID?: ReadonlyMap<string, boolean>;
  onToggleEnabled?: (entityID: string, enabled: boolean) => void;
  onContextMenu?: (
    item: LibraryItem,
    event: ReactMouseEvent<HTMLTableRowElement>,
  ) => void;
}) {
  const enabled =
    surface !== "collection" ||
    enabledByEntityID?.get(item.entityId) !== false;
  const activate = () => {
    if (!disabled) onActivate(item);
  };
  return (
    <tr
      className={`${selected ? "is-selected" : ""}${disabled ? " is-disabled" : ""}${!enabled ? " mod-row--disabled" : ""}`}
      onClick={activate}
      onDoubleClick={selectMode ? undefined : activate}
      onContextMenu={(event) => {
        if (!onContextMenu) return;
        event.preventDefault();
        onContextMenu(item, event);
      }}
      tabIndex={disabled ? undefined : 0}
      aria-disabled={disabled || undefined}
      onKeyDown={(event) => {
        if (!disabled && (event.key === "Enter" || event.key === " ")) {
          event.preventDefault();
          onActivate(item);
        }
      }}
    >
      {selectMode && (
        <td className="mod-table__selection">
          <input
            type="checkbox"
            checked={selected}
            disabled={disabled || !onToggle}
            aria-label={`Select ${item.displayName}`}
            onClick={(event) => event.stopPropagation()}
            onChange={() => onToggle?.(item)}
          />
        </td>
      )}
      {columns.map((column) => (
        <Cell
          key={column.key}
          item={item}
          column={column.key}
          surface={surface}
          workspace={workspace}
          enabled={enabled}
          disabled={disabled}
          onToggleEnabled={onToggleEnabled}
        />
      ))}
    </tr>
  );
});
function SelectAllCheckbox({
  checked,
  indeterminate,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  indeterminate: boolean;
  disabled: boolean;
  label: string;
  onChange: () => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = indeterminate;
  }, [indeterminate]);
  return (
    <input
      ref={ref}
      type="checkbox"
      checked={checked}
      disabled={disabled}
      aria-label={label}
      onChange={onChange}
    />
  );
}

function columnLabel(
  key: ColumnKey,
  surface: ModTableProps["surface"],
): string {
  if (surface === "mod-maker") {
    if (key === "virgil") return "Status";
    if (key === "modified") return "Last modified";
  }
  if (surface === "collection" && key === "modified") return "Updated";
  if (key === "status" && surface !== "library") return "Health";
  return columnDefinitions[key].label;
}

function friendlyWorkspaceValue(value: string): string {
  return value
    .replace(/([a-z])([A-Z])/g, "$1 $2")
    .replace(/[_.-]+/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function hasWorkspaceVirgilWork(record?: WorkspaceRecord): boolean {
  if (!record) return false;
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

function virgilStatusLabel(record?: WorkspaceRecord): string {
  if (!hasWorkspaceVirgilWork(record)) return "Idle/Ready";
  const status = friendlyWorkspaceValue(record?.agentStatus || "");
  return status && status.toLowerCase() !== "idle" ? status : "Ready";
}

function virgilStatusDetail(record?: WorkspaceRecord): string {
  if (!hasWorkspaceVirgilWork(record)) return "No Virgil work is active";
  return [record?.agentStatus, record?.agentProcess, record?.agentGoal]
    .filter(Boolean)
    .map((value) => friendlyWorkspaceValue(value as string))
    .join(" · ");
}

function Cell({
  item,
  column,
  surface,
  workspace,
  enabled,
  disabled,
  onToggleEnabled,
}: {
  item: LibraryItem;
  column: ColumnKey;
  surface: ModTableProps["surface"];
  workspace?: WorkspaceRecord;
  enabled: boolean;
  disabled: boolean;
  onToggleEnabled?: (entityID: string, enabled: boolean) => void;
}) {
  const tags = item.tags ?? [];
  switch (column) {
    case "enabled":
      return (
        <td className="mod-table__enabled">
          <label className="mod-table__enabled-control">
            <input
              type="checkbox"
              checked={enabled}
              disabled={disabled || !onToggleEnabled}
              aria-label={`${enabled ? "Disable" : "Enable"} ${item.displayName}`}
              onClick={(event) => event.stopPropagation()}
              onKeyDown={(event) => event.stopPropagation()}
              onChange={() => onToggleEnabled?.(item.entityId, !enabled)}
            />
            <span>{enabled ? "Enabled" : "Disabled"}</span>
          </label>
        </td>
      );
    case "thumbnail": {
      const thumbnail = thumbUrl(item.thumbnailUrl);
      return (
        <td className="mod-table__thumbnail">
          <span className="mod-table__icon">
            {thumbnail ? (
              <img
                src={thumbnail}
                alt=""
                width={30}
                height={30}
                loading="lazy"
                decoding="async"
                onError={(event) => {
                  event.currentTarget.style.display = "none";
                }}
              />
            ) : (
              <Icon name={kindIcon(String(item.kind))} size={16} />
            )}
          </span>
        </td>
      );
    }
    case "name":
      return (
        <td className="mod-table__name">
          <strong>{item.displayName}</strong>
        </td>
      );
    case "path":
      return (
        <td
          className={`mod-table__path ${item.linked ? "" : "is-unavailable"}`}
          title={item.archivePath || "Source archive unavailable"}
        >
          <span>{item.archivePath || "Source unavailable"}</span>
        </td>
      );
    case "kind":
      return (
        <td>
          <Badge tone={item.kind === "unknown" ? "warning" : "neutral"}>
            {kindLabel(String(item.kind))}
          </Badge>
        </td>
      );
    case "source": {
      const label = sourceLabel(item);
      return (
        <td className="mod-table__source" title={label}>
          <span>{label}</span>
        </td>
      );
    }
    case "status": {
      const label = item.healthLabel || "Not scanned";
      const description = healthDescription(item.healthStatus);
      return (
        <td
          className="mod-table__status"
          aria-label={label}
          title={`${label}: ${description}`}
        >
          <span
            className={`health-status health-status--${item.healthStatus || "unscanned"}`}
            title={label}
          >
            <Icon name={healthIcon(item.healthStatus)} size={14} />
            <span className="health-status__label">{label}</span>
          </span>
        </td>
      );
    }
    case "workspace":
      return (
        <td
          title={
            workspace?.status
              ? friendlyWorkspaceValue(workspace.status)
              : "Workspace status unavailable"
          }
        >
          <span className="mod-table__workspace-status">
            {workspace?.status
              ? friendlyWorkspaceValue(workspace.status)
              : "Unknown"}
          </span>
        </td>
      );
    case "virgil": {
      const label = virgilStatusLabel(workspace);
      return (
        <td
          className="mod-table__virgil-status"
          title={virgilStatusDetail(workspace)}
        >
          <span>{label}</span>
        </td>
      );
    }
    case "author":
      return (
        <td title={item.manifest?.author || ""}>
          {item.manifest?.author || "—"}
        </td>
      );
    case "tags":
      return (
        <td>
          <div className="mod-table__tags">
            {tags.length === 0 ? (
              <span>—</span>
            ) : (
              <>
                {tags.slice(0, 3).map((tag) => {
                  const icon = isTagIcon(tag.icon) ? tag.icon : null;
                  const color = tagColor(tag.color);
                  const style = color
                    ? ({ "--mod-tag-color": color } as CSSProperties)
                    : undefined;
                  return (
                    <span
                      className="mod-tag"
                      key={tag.id}
                      style={style}
                      title={tag.name}
                    >
                      {icon && <Icon name={icon} size={17} />}
                      <span className="mod-tag__label">{tag.name}</span>
                    </span>
                  );
                })}
                {tags.length > 3 && <em>+{tags.length - 3}</em>}
              </>
            )}
          </div>
        </td>
      );
    case "files":
      return (
        <td className="number-cell">{item.memberCount.toLocaleString()}</td>
      );
    case "variants":
      return (
        <td className="number-cell">{item.variantCount.toLocaleString()}</td>
      );
    case "size":
      return <td className="number-cell">{formatBytes(item.sizeBytes)}</td>;
    case "modified": {
      const value =
        surface === "mod-maker"
          ? workspace?.updatedAt || item.modifiedAt
          : item.modifiedAt;
      return <td>{value ? formatDate(value) : "—"}</td>;
    }
    case "lastScan":
      return (
        <td>
          {item.lastSecurityScanAt ? (
            <time dateTime={item.lastSecurityScanAt}>
              {formatDate(item.lastSecurityScanAt)}
            </time>
          ) : (
            "Never"
          )}
        </td>
      );
    case "issues":
      return (
        <td
          className={
            item.issueCount > 0 ? "number-cell issue-cell" : "number-cell"
          }
        >
          {item.issueCount.toLocaleString()}
        </td>
      );
  }
}

function readColumns(
  storageKey: string,
  columnOrder: readonly ColumnKey[],
): ColumnState[] {
  try {
    const parsed = JSON.parse(
      window.localStorage.getItem(storageKey) ?? "[]",
    ) as ColumnState[];
    const allowed = new Set(columnOrder);
    const known = new Set<ColumnKey>();
    const result = parsed
      .filter(
        (column) =>
          column &&
          allowed.has(column.key) &&
          !known.has(column.key) &&
          known.add(column.key),
      )
      .map((column) => ({
        key: column.key,
        visible: column.visible !== false,
        width: Math.max(
          columnDefinitions[column.key].minWidth ?? 64,
          Math.min(
            720,
            Number(column.width) || columnDefinitions[column.key].defaultWidth,
          ),
        ),
      }));
    for (const key of columnOrder) {
      if (known.has(key)) continue;
      const nextKnownKey = columnOrder
        .slice(columnOrder.indexOf(key) + 1)
        .find((candidate) => known.has(candidate));
      const nextIndex = nextKnownKey
        ? result.findIndex((column) => column.key === nextKnownKey)
        : result.length;
      result.splice(nextIndex < 0 ? result.length : nextIndex, 0, {
        key,
        visible: true,
        width: columnDefinitions[key].defaultWidth,
      });
      known.add(key);
    }
    if (result.some((column) => column.visible)) return result;
  } catch {
    // Invalid local UI state falls back to the complete default table.
  }
  return columnOrder.map((key) => ({
    key,
    visible: true,
    width: columnDefinitions[key].defaultWidth,
  }));
}

function readPageSize(storageKey: string): PageSize {
  const stored = window.localStorage.getItem(storageKey);
  if (stored === "all") return "all";
  const value = Number(stored);
  return value === 50 || value === 100 || value === 200 || value === 500
    ? value
    : 100;
}

function cellText(
  item: LibraryItem,
  key: SortKey,
  surface: ModTableProps["surface"],
  workspace?: WorkspaceRecord,
  enabledByEntityID?: ReadonlyMap<string, boolean>,
) {
  switch (key) {
    case "enabled":
      return enabledByEntityID?.get(item.entityId) === false
        ? "Disabled"
        : "Enabled";
    case "name":
      return item.displayName;
    case "path":
      return item.archivePath || "Source archive missing";
    case "kind":
      return kindLabel(String(item.kind)).toUpperCase();
    case "source":
      return sourceLabel(item);
    case "status":
      return item.healthLabel || "Not scanned";
    case "workspace":
      return workspace?.status
        ? friendlyWorkspaceValue(workspace.status)
        : "Unknown";
    case "virgil":
      return virgilStatusLabel(workspace);
    case "author":
      return item.manifest?.author || "—";
    case "tags":
      return (item.tags ?? []).map((tag) => tag.name).join(", ") || "—";
    case "files":
      return item.memberCount.toLocaleString();
    case "variants":
      return item.variantCount.toLocaleString();
    case "size":
      return formatBytes(item.sizeBytes);
    case "modified": {
      const value =
        surface === "mod-maker"
          ? workspace?.updatedAt || item.modifiedAt
          : item.modifiedAt;
      return value ? formatDate(value) : "—";
    }
    case "lastScan":
      return item.lastSecurityScanAt
        ? formatDate(item.lastSecurityScanAt)
        : "Never";
    case "issues":
      return item.issueCount.toLocaleString();
  }
}
 
function sortValue(
  item: LibraryItem,
  key: SortKey,
  surface: ModTableProps["surface"],
  workspace?: WorkspaceRecord,
  enabledByEntityID?: ReadonlyMap<string, boolean>,
): string | number {
  switch (key) {
    case "enabled":
      return enabledByEntityID?.get(item.entityId) === false ? 0 : 1;
    case "name":
      return item.displayName.toLowerCase();
    case "path":
      return item.archivePath.toLowerCase();
    case "kind":
      return String(item.kind);
    case "source":
      return sourceID(item);
    case "status":
      return healthRank(item.healthStatus);
    case "workspace":
      return workspace?.status
        ? friendlyWorkspaceValue(workspace.status).toLowerCase()
        : "";
    case "virgil":
      return virgilStatusLabel(workspace).toLowerCase();
    case "author":
      return (item.manifest?.author || "").toLowerCase();
    case "tags":
      return (item.tags ?? [])
        .map((tag) => tag.name)
        .join(" ")
        .toLowerCase();
    case "files":
      return item.memberCount;
    case "variants":
      return item.variantCount;
    case "size":
      return item.sizeBytes;
    case "modified": {
      const value =
        surface === "mod-maker"
          ? workspace?.updatedAt || item.modifiedAt
          : item.modifiedAt;
      return value ? new Date(value).valueOf() : 0;
    }
    case "lastScan":
      return item.lastSecurityScanAt
        ? new Date(item.lastSecurityScanAt).valueOf()
        : 0;
    case "issues":
      return item.issueCount;
  }
}


function healthRank(status: string) {
  switch (status) {
    case "threat":
      return 0;
    case "broken":
      return 1;
    case "review":
      return 2;
    case "scan_failed":
      return 3;
    case "scanning":
      return 4;
    case "unscanned":
      return 5;
    case "safe":
      return 6;
    default:
      return 5;
  }
}

function healthIcon(
  status: string,
): "check" | "warning" | "error" | "scan" | "shield" {
  switch (status) {
    case "safe":
      return "check";
    case "review":
      return "warning";
    case "threat":
    case "broken":
    case "scan_failed":
      return "error";
    case "scanning":
      return "scan";
    default:
      return "shield";
  }
}

function healthDescription(status: string) {
  switch (status) {
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

function compareValues(left: string | number, right: string | number) {
  return typeof left === "number" && typeof right === "number"
    ? left - right
    : String(left).localeCompare(String(right), undefined, {
        numeric: true,
        sensitivity: "base",
      });
}
