import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ModImportDirectory,
  ModImportEntry,
  ModImportLocation,
  ModImportResult,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { CollectionDialog } from "./CollectionUI";
import { Icon, type IconName } from "./icons";
import { Button, EmptyState, Spinner, formatBytes, formatDate } from "./ui";
import "./AddModDialog.css";

export interface AddModDialogProps {
  onClose: () => void;
  onImported: (result: ModImportResult) => Promise<void>;
}

type NavigationMode = "replace" | "push" | "back" | "forward";
type SortColumn = "provided" | "name" | "modifiedAt" | "sizeBytes";
type SortDirection = "asc" | "desc";

interface ImportSort {
  column: SortColumn;
  direction: SortDirection;
}
interface SelectionModifiers {
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
}

function compareImportEntries(
  left: ModImportEntry,
  right: ModImportEntry,
  sort: ImportSort,
): number {
  if (left.isDirectory !== right.isDirectory) return left.isDirectory ? -1 : 1;
  if (sort.column === "provided") return 0;
  let comparison = 0;
  if (sort.column === "name") {
    comparison = left.name.localeCompare(right.name, undefined, {
      sensitivity: "base",
      numeric: true,
    });
  } else if (sort.column === "modifiedAt") {
    const leftDate = Date.parse(left.modifiedAt);
    const rightDate = Date.parse(right.modifiedAt);
    comparison =
      (Number.isFinite(leftDate) ? leftDate : 0) -
      (Number.isFinite(rightDate) ? rightDate : 0);
  } else {
    comparison = (Number(left.sizeBytes) || 0) - (Number(right.sizeBytes) || 0);
  }
  return comparison * (sort.direction === "asc" ? 1 : -1);
}

function pathKey(path: string): string {
  const normalized = String(path ?? "")
    .trim()
    .replace(/[\\/]+/g, "\\");
  if (!normalized) return "";
  const withoutTrailingSlash =
    normalized.length > 1 ? normalized.replace(/\\+$/, "") : normalized;
  return withoutTrailingSlash.toLocaleLowerCase();
}

function samePath(left: string, right: string): boolean {
  return pathKey(left) === pathKey(right);
}

function errorMessage(value: unknown, fallback: string): string {
  if (value instanceof Error && value.message.trim())
    return value.message.trim();
  if (typeof value === "string" && value.trim()) return value.trim();
  if (value && typeof value === "object" && "message" in value) {
    const message = value.message;
    if (typeof message === "string" && message.trim()) return message.trim();
  }
  return fallback;
}

interface LoadedImportDirectory extends ModImportDirectory {
  breadcrumbs: ModImportLocation[];
  locations: ModImportLocation[];
  recentLocations: ModImportLocation[];
  entries: ModImportEntry[];
}

interface CompletedModImport extends ModImportResult {
  items: NonNullable<ModImportResult["items"]>;
  failures: NonNullable<ModImportResult["failures"]>;
}

function normalizeDirectory(value: ModImportDirectory): LoadedImportDirectory {
  return {
    ...value,
    path: value.path ?? "",
    parentPath: value.parentPath ?? "",
    destinationPath: value.destinationPath ?? "",
    warning: value.warning ?? "",
    breadcrumbs: value.breadcrumbs ?? [],
    locations: value.locations ?? [],
    recentLocations: value.recentLocations ?? [],
    entries: value.entries ?? [],
  };
}

function normalizeResult(value: ModImportResult): CompletedModImport {
  return {
    ...value,
    items: value.items ?? [],
    failures: value.failures ?? [],
    importedCount: Number(value.importedCount ?? 0),
    existingCount: Number(value.existingCount ?? 0),
  };
}

function locationIcon(kind: string): IconName {
  if (kind === "home") return "user";
  if (kind === "library") return "library";
  if (kind === "recent") return "refresh";
  if (kind === "drive") return "archive";
  return "folder";
}

function locationLabel(location: ModImportLocation): string {
  const name = String(location.name ?? "").trim();
  if (name) return name;
  if (location.kind === "downloads") return "Downloads";
  if (location.kind === "home") return "Home";
  if (location.kind === "library") return "Library";
  if (location.kind === "drive") return "Drive";
  if (location.kind === "recent") return "Recent folder";
  return location.path || "Folder";
}

function isControlTarget(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target instanceof HTMLButtonElement
  );
}

export function AddModDialog({ onClose, onImported }: AddModDialogProps) {
  const [directory, setDirectory] = useState<LoadedImportDirectory | null>(null);
  const [browseError, setBrowseError] = useState("");
  const [loading, setLoading] = useState(true);
  const [sort, setSort] = useState<ImportSort>({
    column: "provided",
    direction: "asc",
  });
  const [pathDraft, setPathDraft] = useState("");
  const [filter, setFilter] = useState("");
  const [selectedPaths, setSelectedPaths] = useState<Set<string>>(
    () => new Set(),
  );
  const [focusedPath, setFocusedPath] = useState("");
  const [anchorPath, setAnchorPath] = useState("");
  const [backHistory, setBackHistory] = useState<string[]>([]);
  const [forwardHistory, setForwardHistory] = useState<string[]>([]);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState("");
  const [importResult, setImportResult] = useState<CompletedModImport | null>(
    null,
  );
  const [completedPaths, setCompletedPaths] = useState<Set<string>>(
    () => new Set(),
  );
  const [refreshError, setRefreshError] = useState("");

  const mountedRef = useRef(true);
  const requestSequenceRef = useRef(0);
  const currentPathRef = useRef("");
  const importResultRef = useRef<ModImportResult | null>(null);
  const pathInputRef = useRef<HTMLInputElement>(null);
  const entryRefs = useRef<Map<string, HTMLDivElement>>(new Map());
  const focusRequestRef = useRef(false);

  const entries = directory?.entries ?? [];
  const normalizedFilter = filter.trim().toLocaleLowerCase();
  const visibleEntries = useMemo(() => {
    const matching = entries.filter(
      (entry) =>
        !normalizedFilter ||
        entry.name.toLocaleLowerCase().includes(normalizedFilter),
    );
    if (sort.column === "provided") return matching;
    return [...matching].sort((left, right) =>
      compareImportEntries(left, right, sort),
    );
  }, [entries, normalizedFilter, sort]);
  const visibleFiles = useMemo(
    () => visibleEntries.filter((entry) => !entry.isDirectory),
    [visibleEntries],
  );
  const visibleDirectories = useMemo(
    () => visibleEntries.filter((entry) => entry.isDirectory),
    [visibleEntries],
  );
  const allFileCount = entries.reduce(
    (count, entry) => count + (entry.isDirectory ? 0 : 1),
    0,
  );
  const allDirectoryCount = entries.reduce(
    (count, entry) => count + (entry.isDirectory ? 1 : 0),
    0,
  );
  const failedByPath = useMemo(
    () =>
      new Map(
        (importResult?.failures ?? []).map((failure) => [
          failure.path,
          failure,
        ]),
      ),
    [importResult],
  );
  const selectedEntries = useMemo(
    () => entries.filter((entry) => selectedPaths.has(entry.path)),
    [entries, selectedPaths],
  );
  const selectedBytes = useMemo(
    () =>
      selectedEntries.reduce(
        (total, entry) => total + Math.max(0, Number(entry.sizeBytes) || 0),
        0,
      ),
    [selectedEntries],
  );
  const locations = directory?.locations ?? [];
  const recentLocations = useMemo(() => {
    const pinned = new Set(locations.map((location) => pathKey(location.path)));
    return (directory?.recentLocations ?? []).filter(
      (location) => !pinned.has(pathKey(location.path)),
    );
  }, [directory?.recentLocations, locations]);
  const currentPath = directory?.path ?? currentPathRef.current;
  const destinationPath = directory?.destinationPath ?? "";

  const focusEntry = useCallback((path: string) => {
    setFocusedPath(path);
    focusRequestRef.current = Boolean(path);
  }, []);

  const navigateTo = useCallback(
    async (requestedPath: string, mode: NavigationMode = "push") => {
      const requestID = ++requestSequenceRef.current;
      const previousPath = currentPathRef.current;
      const requested = requestedPath.trim();
      setLoading(true);
      setBrowseError("");
      try {
        const response = await API.BrowseModImportDirectory(requested);
        if (!mountedRef.current || requestID !== requestSequenceRef.current)
          return;
        const next = normalizeDirectory(response);
        const nextPath = next.path.trim();
        if (mode === "push") {
          if (previousPath && !samePath(previousPath, nextPath)) {
            setBackHistory((history) => {
              if (
                history.length > 0 &&
                samePath(history[history.length - 1], previousPath)
              )
                return history;
              return [...history, previousPath];
            });
            setForwardHistory([]);
          }
        } else if (mode === "back") {
          setBackHistory((history) =>
            history.slice(0, Math.max(0, history.length - 1)),
          );
          if (previousPath && !samePath(previousPath, nextPath)) {
            setForwardHistory((history) => [previousPath, ...history]);
          }
        } else if (mode === "forward") {
          setForwardHistory((history) => history.slice(1));
          if (previousPath && !samePath(previousPath, nextPath)) {
            setBackHistory((history) => [...history, previousPath]);
          }
        }
        currentPathRef.current = nextPath;
        setDirectory(next);
        setPathDraft(nextPath);
        setSort({ column: "provided", direction: "asc" });
        setFilter("");
        setBrowseError("");
        const retryPaths = new Set(
          (importResultRef.current?.failures ?? []).map(
            (failure) => failure.path,
          ),
        );
        const nextFailed = next.entries.filter((entry) =>
          retryPaths.has(entry.path),
        );
        setSelectedPaths(new Set(nextFailed.map((entry) => entry.path)));
        setAnchorPath(nextFailed.length > 0 ? nextFailed[0].path : "");
        focusEntry(next.entries.length > 0 ? next.entries[0].path : "");
      } catch (error) {
        if (!mountedRef.current || requestID !== requestSequenceRef.current)
          return;
        setBrowseError(errorMessage(error, "The folder could not be opened."));
      } finally {
        if (mountedRef.current && requestID === requestSequenceRef.current)
          setLoading(false);
      }
    },
    [focusEntry],
  );

  useEffect(() => {
    mountedRef.current = true;
    void navigateTo("", "replace");
    return () => {
      mountedRef.current = false;
      requestSequenceRef.current += 1;
    };
  }, [navigateTo]);

  useEffect(() => {
    if (!focusRequestRef.current || !focusedPath) return;
    const node = entryRefs.current.get(focusedPath);
    if (!node) return;
    focusRequestRef.current = false;
    node.focus({ preventScroll: true });
    node.scrollIntoView({ block: "nearest" });
  }, [focusedPath, visibleEntries]);

  useEffect(() => {
    if (
      focusedPath &&
      visibleEntries.some((entry) => entry.path === focusedPath)
    )
      return;
    if (visibleEntries.length > 0) {
      setFocusedPath(visibleEntries[0].path);
    } else if (focusedPath) {
      setFocusedPath("");
    }
  }, [focusedPath, visibleEntries]);

  const requestClose = useCallback(() => {
    if (importing) return;
    onClose();
  }, [importing, onClose]);

  const handleLocation = useCallback(
    (location: ModImportLocation) => {
      if (importing) return;
      void navigateTo(location.path, "push");
    },
    [importing, navigateTo],
  );

  const goBack = useCallback(() => {
    if (importing || backHistory.length === 0) return;
    const target = backHistory[backHistory.length - 1];
    void navigateTo(target, "back");
  }, [backHistory, importing, navigateTo]);

  const goForward = useCallback(() => {
    if (importing || forwardHistory.length === 0) return;
    const target = forwardHistory[0];
    void navigateTo(target, "forward");
  }, [forwardHistory, importing, navigateTo]);

  const goUp = useCallback(() => {
    if (importing || !directory?.parentPath) return;
    void navigateTo(directory.parentPath, "push");
  }, [directory?.parentPath, importing, navigateTo]);

  const handlePathSubmit = useCallback(
    (event: FormEvent<HTMLFormElement>) => {
      event.preventDefault();
      if (importing) return;
      void navigateTo(pathDraft, "push");
    },
    [importing, navigateTo, pathDraft],
  );
  const chooseSort = useCallback((column: SortColumn) => {
    setSort((current) => {
      if (current.column === column) {
        return {
          column,
          direction: current.direction === "asc" ? "desc" : "asc",
        };
      }
      return {
        column,
        direction:
          column === "modifiedAt" || column === "sizeBytes" ? "desc" : "asc",
      };
    });
  }, []);

  const selectEntry = useCallback(
    (
      entry: ModImportEntry,
      modifiers: SelectionModifiers,
      toggle = false,
      focus = true,
    ) => {
      if (loading || importing) return;
      const additive = modifiers.ctrlKey || modifiers.metaKey || toggle;
      const next = additive ? new Set(selectedPaths) : new Set<string>();
      if (modifiers.shiftKey) {
        const targetIndex = visibleEntries.findIndex(
          (candidate) => candidate.path === entry.path,
        );
        if (targetIndex < 0) return;
        let anchorIndex = visibleEntries.findIndex(
          (candidate) => candidate.path === anchorPath,
        );
        if (anchorIndex < 0) {
          anchorIndex = targetIndex;
          setAnchorPath(entry.path);
        }
        for (
          let index = Math.min(anchorIndex, targetIndex);
          index <= Math.max(anchorIndex, targetIndex);
          index += 1
        ) {
          next.add(visibleEntries[index].path);
        }
      } else {
        if (additive && next.has(entry.path)) next.delete(entry.path);
        else next.add(entry.path);
        setAnchorPath(entry.path);
      }
      setSelectedPaths(next);
      if (focus) focusEntry(entry.path);
    },
    [anchorPath, focusEntry, importing, loading, selectedPaths, visibleEntries],
  );

  const selectVisibleEntries = useCallback(() => {
    if (loading || importing) return;
    const next = new Set(selectedPaths);
    for (const entry of visibleEntries) next.add(entry.path);
    setSelectedPaths(next);
    setAnchorPath("");
  }, [importing, loading, selectedPaths, visibleEntries]);


  const handleEntryClick = useCallback(
    (entry: ModImportEntry, event: ReactMouseEvent<HTMLDivElement>) => {
      if (loading || importing) return;
      setFocusedPath(entry.path);
      if (event.detail > 1) return;
      if (entry.isDirectory) {
        if (!event.ctrlKey && !event.metaKey && !event.shiftKey) {
          setSelectedPaths(new Set());
          setAnchorPath("");
        }
        return;
      }
      selectEntry(entry, event);
    },
    [importing, loading, selectEntry],
  );

  const moveFocus = useCallback(
    (index: number, event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (visibleEntries.length === 0) return;
      const bounded = Math.max(0, Math.min(index, visibleEntries.length - 1));
      const target = visibleEntries[bounded];
      focusEntry(target.path);
      if (event.altKey || (!event.shiftKey && (event.ctrlKey || event.metaKey)))
        return;
      selectEntry(target, event, false, false);
    },
    [focusEntry, selectEntry, visibleEntries],
  );

  const handleEntryKeyDown = useCallback(
    (
      entry: ModImportEntry,
      index: number,
      event: ReactKeyboardEvent<HTMLDivElement>,
    ) => {
      if (event.target !== event.currentTarget) return;
      if (
        (event.ctrlKey || event.metaKey) &&
        event.key.toLocaleLowerCase() === "a"
      ) {
        event.preventDefault();
        event.stopPropagation();
        selectVisibleEntries();
        return;
      }
      if (
        event.key === "ArrowDown" ||
        event.key === "ArrowUp" ||
        event.key === "Home" ||
        event.key === "End"
      ) {
        event.preventDefault();
        event.stopPropagation();
        moveFocus(
          event.key === "ArrowDown"
            ? index + 1
            : event.key === "ArrowUp"
              ? index - 1
              : event.key === "Home"
                ? 0
                : visibleEntries.length - 1,
          event,
        );
      } else if (event.key === " " || event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        if (event.repeat) return;
        if (event.key === "Enter" && entry.isDirectory) {
          if (!loading && !importing) void navigateTo(entry.path, "push");
        } else {
          selectEntry(entry, event, !event.shiftKey, false);
        }
      }
    },
    [
      importing,
      loading,
      moveFocus,
      navigateTo,
      selectEntry,
      selectVisibleEntries,
      visibleEntries.length,
    ],
  );

  const handleListKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (
        (event.ctrlKey || event.metaKey) &&
        event.key.toLocaleLowerCase() === "a"
      ) {
        event.preventDefault();
        selectVisibleEntries();
      }
    },
    [selectVisibleEntries],
  );

  const handleDialogKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (
        (event.ctrlKey || event.metaKey) &&
        event.key.toLocaleLowerCase() === "l"
      ) {
        event.preventDefault();
        pathInputRef.current?.focus();
        pathInputRef.current?.select();
        return;
      }
      if (isControlTarget(event.target)) return;
      if (
        (event.ctrlKey || event.metaKey) &&
        event.key.toLocaleLowerCase() === "a"
      ) {
        event.preventDefault();
        selectVisibleEntries();
      }
    },
    [selectVisibleEntries],
  );

  const handleControlKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLElement>) => {
      if (
        (event.ctrlKey || event.metaKey) &&
        event.key.toLocaleLowerCase() === "l"
      )
        return;
      event.stopPropagation();
    },
    [],
  );

  const retryBrowse = useCallback(() => {
    void navigateTo(directory?.path ?? "", "replace");
  }, [directory?.path, navigateTo]);

  const importSelected = useCallback(async () => {
    if (loading || importing || selectedEntries.length === 0) return;
    const requestedPaths = selectedEntries.map((entry) => entry.path);
    setImporting(true);
    setImportError("");
    setRefreshError("");
    setCompletedPaths(new Set());
    try {
      const response = normalizeResult(await API.ImportMods(requestedPaths));
      if (!mountedRef.current) return;
      importResultRef.current = response;
      setImportResult(response);
      const failurePaths = new Set(
        response.failures.map((failure) => failure.path),
      );
      setCompletedPaths(
        new Set(requestedPaths.filter((path) => !failurePaths.has(path))),
      );
      setSelectedPaths(failurePaths);
      setAnchorPath(
        response.failures.length > 0 ? response.failures[0].path : "",
      );
      let refreshFailed = false;
      if (response.items.length > 0) {
        try {
          await onImported(response);
        } catch (error) {
          refreshFailed = true;
          if (mountedRef.current)
            setRefreshError(
              errorMessage(
                error,
                "The library view could not be refreshed. Imported files are safe and were not retried.",
              ),
            );
        }
      }
      if (response.failures.length === 0 && !refreshFailed) onClose();
    } catch (error) {
      if (mountedRef.current)
        setImportError(
          errorMessage(error, "The selected mods could not be imported."),
        );
    } finally {
      if (mountedRef.current) setImporting(false);
    }
  }, [importing, loading, onClose, onImported, selectedEntries]);

  const renderLocation = (
    location: ModImportLocation,
    section: "pinned" | "recent",
  ) => {
    const active = samePath(currentPath, location.path);
    return (
      <button
        key={`${section}-${pathKey(location.path)}`}
        type="button"
        className={`add-mod-dialog__location${active ? " is-active" : ""}`}
        title={location.path}
        aria-current={active ? "page" : undefined}
        onClick={() => handleLocation(location)}
        onKeyDown={handleControlKeyDown}
        disabled={importing}
      >
        <Icon name={locationIcon(location.kind)} size={16} />
        <span>{locationLabel(location)}</span>
      </button>
    );
  };

  const renderEntry = (entry: ModImportEntry, index: number) => {
    const selected = selectedPaths.has(entry.path);
    const failed = failedByPath.get(entry.path);
    const completed = completedPaths.has(entry.path);
    return (
      <div
        key={entry.path}
        ref={(node) => {
          if (node) entryRefs.current.set(entry.path, node);
          else entryRefs.current.delete(entry.path);
        }}
        className={`add-mod-dialog__entry${selected ? " is-selected" : ""}${focusedPath === entry.path ? " is-focused" : ""}${failed ? " is-failed" : ""}${completed ? " is-completed" : ""}${entry.isDirectory ? " is-directory" : " is-file"}`}
        role="option"
        aria-selected={selected}
        aria-label={
          entry.isDirectory
            ? `Folder ${entry.name}; select to add as an unpacked mod`
            : `File ${entry.name}`
        }
        tabIndex={focusedPath === entry.path ? 0 : -1}
        title={entry.path}
        onFocus={() => setFocusedPath(entry.path)}
        onClick={(event) => handleEntryClick(entry, event)}
        onDoubleClick={(event) => {
          event.stopPropagation();
          if (entry.isDirectory && !loading && !importing)
            void navigateTo(entry.path, "push");
        }}
        onKeyDown={(event) => handleEntryKeyDown(entry, index, event)}
      >
        <span
          className="add-mod-dialog__entry-check"
          onClick={(event) => event.stopPropagation()}
          onDoubleClick={(event) => event.stopPropagation()}
        >
          <input
            type="checkbox"
            checked={selected}
            readOnly
            aria-label={
              entry.isDirectory
                ? `Select ${entry.name} as an unpacked mod`
                : `Select ${entry.name} as a mod`
            }
            onClick={(event) => {
              event.stopPropagation();
              selectEntry(entry, event, true, false);
            }}
            onKeyDown={handleControlKeyDown}
          />
        </span>
        <Icon
          name={entry.isDirectory ? "folder" : "archive"}
          size={17}
          className="add-mod-dialog__entry-icon"
        />
        <span className="add-mod-dialog__entry-name">
          <strong>{entry.name}</strong>
          {failed && (
            <small className="add-mod-dialog__entry-status is-error">
              Failed · {failed.message || "Unable to import this mod."}
            </small>
          )}
          {!failed && completed && (
            <small className="add-mod-dialog__entry-status is-success">
              Completed
            </small>
          )}
        </span>
        <span className="add-mod-dialog__entry-size">
          {entry.isDirectory ? "—" : formatBytes(entry.sizeBytes)}
        </span>
        <time
          className="add-mod-dialog__entry-date"
          dateTime={entry.modifiedAt || undefined}
        >
          {entry.isDirectory ? "—" : formatDate(entry.modifiedAt)}
        </time>
      </div>
    );
  };

  const resultFailures = importResult?.failures ?? [];
  const resultImportedCount = importResult?.importedCount ?? 0;
  const resultExistingCount = importResult?.existingCount ?? 0;
  const matchingSelectedCount = selectedEntries.length;
  const selectedLabel = matchingSelectedCount === 1 ? "mod" : "mods";

  return (
    <CollectionDialog
      title="Add mods from your computer"
      onClose={requestClose}
      wide
    >
      <div
        className="add-mod-dialog"
        aria-busy={loading || importing}
        onKeyDown={handleDialogKeyDown}
      >
        <aside className="add-mod-dialog__sidebar" aria-label="Places">
          <div className="add-mod-dialog__sidebar-heading">Places</div>
          <div className="add-mod-dialog__locations">
            {locations.length > 0 ? (
              locations.map((location) => renderLocation(location, "pinned"))
            ) : (
              <span className="add-mod-dialog__sidebar-empty">
                Loading places…
              </span>
            )}
          </div>
          {recentLocations.length > 0 && (
            <>
              <div className="add-mod-dialog__sidebar-heading add-mod-dialog__sidebar-heading--recent">
                Recent folders
              </div>
              <div className="add-mod-dialog__locations">
                {recentLocations.map((location) =>
                  renderLocation(location, "recent"),
                )}
              </div>
            </>
          )}
        </aside>

        <section
          className="add-mod-dialog__main"
          aria-label="Filesystem browser"
        >
          <div className="add-mod-dialog__toolbar">
            <nav
              className="add-mod-dialog__history"
              aria-label="Folder navigation"
            >
              <button
                type="button"
                className="add-mod-dialog__nav-button"
                title="Back"
                aria-label="Back"
                disabled={backHistory.length === 0 || importing}
                onClick={goBack}
                onKeyDown={handleControlKeyDown}
              >
                <Icon name="arrow" size={15} className="is-back" />
              </button>
              <button
                type="button"
                className="add-mod-dialog__nav-button"
                title="Forward"
                aria-label="Forward"
                disabled={forwardHistory.length === 0 || importing}
                onClick={goForward}
                onKeyDown={handleControlKeyDown}
              >
                <Icon name="arrow" size={15} />
              </button>
              <button
                type="button"
                className="add-mod-dialog__nav-button"
                title="Up one level"
                aria-label="Up one level"
                disabled={!directory?.parentPath || importing}
                onClick={goUp}
                onKeyDown={handleControlKeyDown}
              >
                <Icon name="arrow" size={15} className="is-up" />
              </button>
            </nav>
            <form
              className="add-mod-dialog__path-form"
              onSubmit={handlePathSubmit}
            >
              <Icon name="folder" size={15} />
              <input
                ref={pathInputRef}
                value={pathDraft}
                onChange={(event) => setPathDraft(event.target.value)}
                aria-label="Current folder path"
                placeholder="Type a folder path"
                spellCheck={false}
                onKeyDown={handleControlKeyDown}
              />
              <kbd title="Focus the path field">Ctrl L</kbd>
            </form>
            <label className="add-mod-dialog__filter">
              <Icon name="search" size={15} />
              <input
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
                placeholder="Filter this folder"
                aria-label="Filter this folder"
                onKeyDown={handleControlKeyDown}
              />
            </label>
          </div>

          {directory && directory.breadcrumbs.length > 0 && (
            <nav
              className="add-mod-dialog__breadcrumbs"
              aria-label="Breadcrumbs"
            >
              {directory.breadcrumbs.map((breadcrumb, index) => {
                const last = index === directory.breadcrumbs.length - 1;
                return (
                  <span key={`${breadcrumb.path}-${index}`}>
                    {index > 0 && <b aria-hidden="true">›</b>}
                    {last ? (
                      <strong title={breadcrumb.path}>
                        {breadcrumb.name || breadcrumb.path}
                      </strong>
                    ) : (
                      <button
                        type="button"
                        onClick={() => handleLocation(breadcrumb)}
                        onKeyDown={handleControlKeyDown}
                        title={breadcrumb.path}
                      >
                        {breadcrumb.name || breadcrumb.path}
                      </button>
                    )}
                  </span>
                );
              })}
            </nav>
          )}

          {browseError && (
            <div
              className="add-mod-dialog__message add-mod-dialog__message--error"
              role="alert"
            >
              <Icon name="error" size={16} />
              <span>{browseError}</span>
              <Button
                type="button"
                tone="quiet"
                onClick={retryBrowse}
                onKeyDown={handleControlKeyDown}
              >
                Try again
              </Button>
            </div>
          )}
          {directory?.warning && (
            <div
              className="add-mod-dialog__message add-mod-dialog__message--warning"
              role="status"
            >
              <Icon name="warning" size={16} />
              <span>{directory.warning}</span>
            </div>
          )}

          <div className="add-mod-dialog__list-heading">
            <span>
              {normalizedFilter
                ? `${visibleDirectories.length} folders · ${visibleFiles.length} files · ${visibleEntries.length} matching`
                : `${allDirectoryCount} folders · ${allFileCount} files`}
            </span>
            <span className="add-mod-dialog__list-hint">
              Double-click a folder to open · Select files or folders to add ·
              Ctrl/⌘-click for more · Shift-click for a range
            </span>
          </div>

          <div className="add-mod-dialog__list-wrap">
            <div className="add-mod-dialog__list-columns">
              <button
                type="button"
                className={sort.column === "name" ? "is-active" : ""}
                aria-sort={
                  sort.column === "name"
                    ? sort.direction === "asc"
                      ? "ascending"
                      : "descending"
                    : "none"
                }
                onClick={() => chooseSort("name")}
                onKeyDown={handleControlKeyDown}
              >
                Name
                <Icon
                  name="chevron"
                  size={12}
                  className={
                    sort.column === "name" && sort.direction === "desc"
                      ? "is-desc"
                      : ""
                  }
                />
              </button>
              <button
                type="button"
                className={sort.column === "sizeBytes" ? "is-active" : ""}
                aria-sort={
                  sort.column === "sizeBytes"
                    ? sort.direction === "asc"
                      ? "ascending"
                      : "descending"
                    : "none"
                }
                onClick={() => chooseSort("sizeBytes")}
                onKeyDown={handleControlKeyDown}
              >
                Size
                <Icon
                  name="chevron"
                  size={12}
                  className={
                    sort.column === "sizeBytes" && sort.direction === "desc"
                      ? "is-desc"
                      : ""
                  }
                />
              </button>
              <button
                type="button"
                className={sort.column === "modifiedAt" ? "is-active" : ""}
                aria-sort={
                  sort.column === "modifiedAt"
                    ? sort.direction === "asc"
                      ? "ascending"
                      : "descending"
                    : "none"
                }
                onClick={() => chooseSort("modifiedAt")}
                onKeyDown={handleControlKeyDown}
              >
                Date modified
                <Icon
                  name="chevron"
                  size={12}
                  className={
                    sort.column === "modifiedAt" && sort.direction === "desc"
                      ? "is-desc"
                      : ""
                  }
                />
              </button>
            </div>
            <div
              className="add-mod-dialog__list"
              role="listbox"
              aria-label="Folders and files"
              aria-multiselectable="true"
              tabIndex={0}
              onKeyDown={handleListKeyDown}
            >
              {!directory && loading ? (
                <div className="add-mod-dialog__loading">
                  <Spinner />
                  <span>Opening Downloads…</span>
                </div>
              ) : !directory ? (
                <EmptyState
                  icon="folder"
                  title="No folder open"
                  detail={
                    browseError ||
                    "Enter a path or choose a place to start browsing."
                  }
                  action={
                    <Button
                      type="button"
                      onClick={retryBrowse}
                      onKeyDown={handleControlKeyDown}
                    >
                      Open Downloads again
                    </Button>
                  }
                />
              ) : visibleEntries.length === 0 ? (
                <EmptyState
                  icon={normalizedFilter ? "search" : "folder"}
                  title={
                    normalizedFilter
                      ? "Nothing matches this filter"
                      : "This folder is empty"
                  }
                  detail={
                    normalizedFilter
                      ? "Try a different name or clear the filter."
                      : "Only folders and files are shown here."
                  }
                />
              ) : (
                visibleEntries.map((entry, index) => renderEntry(entry, index))
              )}
              {loading && directory && (
                <div className="add-mod-dialog__loading-overlay" role="status">
                  <Spinner small />
                  <span>Opening folder…</span>
                </div>
              )}
            </div>
          </div>

          {importError && (
            <div
              className="add-mod-dialog__message add-mod-dialog__message--error"
              role="alert"
            >
              <Icon name="error" size={16} />
              <span>{importError}</span>
              <Button
                type="button"
                tone="quiet"
                onClick={() => setImportError("")}
                onKeyDown={handleControlKeyDown}
              >
                Dismiss
              </Button>
            </div>
          )}
          {importResult && (
            <section
              className="add-mod-dialog__results"
              aria-live="polite"
              aria-label="Import results"
            >
              <div className="add-mod-dialog__results-heading">
                <Icon
                  name={resultFailures.length > 0 ? "warning" : "check"}
                  size={16}
                  className={
                    resultFailures.length > 0 ? "is-error" : "is-success"
                  }
                />
                <strong>
                  {resultFailures.length > 0
                    ? "Import finished with some failures"
                    : "Import complete"}
                </strong>
                <span>{importResult.items.length} completed</span>
              </div>
              <div className="add-mod-dialog__result-summary">
                <span className="is-success">
                  <strong>{resultImportedCount}</strong> added
                </span>
                <span className="is-neutral">
                  <strong>{resultExistingCount}</strong> already present
                </span>
                {resultFailures.length > 0 && (
                  <span className="is-error">
                    <strong>{resultFailures.length}</strong> failed · selected
                    again for retry
                  </span>
                )}
              </div>
              {resultFailures.length > 0 && (
                <ul className="add-mod-dialog__failure-list">
                  {resultFailures.map((failure) => (
                    <li key={failure.path}>
                      <Icon name="error" size={14} />
                      <span>
                        <strong>{failure.path}</strong>
                        <small>
                          {failure.message || "Unable to import this mod."}
                        </small>
                      </span>
                    </li>
                  ))}
                </ul>
              )}
              {refreshError && (
                <p className="add-mod-dialog__refresh-error" role="status">
                  <Icon name="warning" size={14} />
                  {refreshError}
                </p>
              )}
            </section>
          )}
        </section>
      </div>

      <div className="add-mod-dialog__footer-status" aria-live="polite">
        <div className="add-mod-dialog__selection-summary">
          <strong>
            {matchingSelectedCount} {selectedLabel} selected
          </strong>
          {matchingSelectedCount > 0 && (
            <span>{formatBytes(selectedBytes)}</span>
          )}
        </div>
        <div className="add-mod-dialog__destination" title={destinationPath}>
          <Icon name="library" size={15} />
          <span>
            Copy to <strong>{destinationPath || "Configured Library"}</strong>
          </span>
        </div>
        <p className="add-mod-dialog__preservation-note">
          <Icon name="copy" size={14} />
          Your original files stay where they are.
        </p>
      </div>
      <div className="add-mod-dialog__footer-actions">
        {importing && (
          <span className="add-mod-dialog__busy">
            <Spinner small />
            Adding mods…
          </span>
        )}
        <Button
          type="button"
          tone="quiet"
          onClick={requestClose}
          disabled={importing}
          onKeyDown={handleControlKeyDown}
        >
          Cancel
        </Button>
        <Button
          type="button"
          tone="primary"
          icon="install"
          disabled={loading || importing || matchingSelectedCount === 0}
          onClick={() => void importSelected()}
          onKeyDown={handleControlKeyDown}
        >
          {importing
            ? "Adding mods…"
            : `Add ${matchingSelectedCount} ${selectedLabel}`}
        </Button>
      </div>
    </CollectionDialog>
  );
}
