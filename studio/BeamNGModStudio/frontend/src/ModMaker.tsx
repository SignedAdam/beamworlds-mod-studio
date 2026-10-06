import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
} from "react";
import { Events } from "@wailsio/runtime";
import { CollectionDialog } from "./CollectionUI";
import { confirmAction, requestText } from "./AppDialogs";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  AgentActivity,
  LibraryItem,
  AgentModelOption,
  VirgilSessionRecord,
  NewModRequest,
  RuntimeReport,
  WorkspaceDetail,
  WorkspaceRecord,
  WorkspaceSearchMatch,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import {
  CodeEditor,
  canFormatSource,
  formatSource,
  type CodeEditorDiagnostic,
  type CodeEditorSaveSnapshot,
  type CodeEditorSelection,
} from "./CodeEditor";
import {
  FileTree,
  countMatchingFiles,
  type TreeSelection,
  type TreeSeverity,
} from "./FileTree";
import { IndexCardTabs, type IndexCardTabItem } from "./IndexCardTabs";
import { Icon } from "./icons";
import { Badge, Button, Spinner, formatBytes, kindIcon, kindLabel } from "./ui";
import { ProjectBrowser } from "./ProjectBrowser";
import {
  SourceControlView,
  type SourceControlWorkingTreeChange,
} from "./SourceControlView";
import { VirgilChoiceDialog, VirgilSessionView } from "./VirgilSessionView";
import { ReplaceableUIPlaceholder } from "./replaceableUi";
import { WorkspaceStatusBar } from "./WorkspaceStatusBar";
import {
  WorkspaceUtilityPanel,
  type WorkspaceTool,
} from "./WorkspaceUtilityPanel";
import "./ModMaker.css";

interface EditorDocument {
  path: string;
  name?: string;
  untitled?: boolean;
  content: string;
  savedContent: string;
  savedSHA256: string;
  restored: boolean;
  externalContent?: string;
  externalSHA256?: string;
}

async function sha256Text(content: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(content),
  );
  return Array.from(new Uint8Array(digest), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}

function documentFromFile(
  file: { path: string; content: string; sha256: string },
  draft?: { content: string; baseSha256: string },
): EditorDocument {
  const conflict = Boolean(
    draft?.baseSha256 &&
    draft.baseSha256 !== file.sha256 &&
    draft.content !== file.content,
  );
  return {
    path: file.path,
    content: draft?.content ?? file.content,
    savedContent: file.content,
    savedSHA256: conflict && draft ? draft.baseSha256 : file.sha256,
    restored: Boolean(draft),
    externalContent: conflict ? file.content : undefined,
    externalSHA256: conflict ? file.sha256 : undefined,
  };
}

function editorDocumentLabel(document: EditorDocument) {
  return document.untitled ? document.name || "Untitled" : document.path;
}

type ActiveEditorTab =
  { kind: "file"; path: string } | { kind: "session"; id: string };

interface SessionTab {
  record: VirgilSessionRecord;
  transient: boolean;
  busy?: boolean;
  seededDefault?: boolean;
}

interface SessionContextMenu {
  id: string;
  x: number;
  y: number;
}

interface EditorIconActionProps {
  label: string;
  icon: Parameters<typeof Icon>[0]["name"];
  shortcut?: string;
  className?: string;
  disabled?: boolean;
  loading?: boolean;
  onClick: () => void;
}

function EditorIconAction({
  label,
  icon,
  shortcut,
  className = "",
  disabled = false,
  loading = false,
  onClick,
}: EditorIconActionProps) {
  return (
    <span className="modmaker-editor-action-shell">
      <button
        type="button"
        className={`modmaker-editor-action modmaker-editor-action--icon${loading ? " is-loading" : ""} ${className}`.trim()}
        aria-label={label}
        aria-keyshortcuts={shortcut?.replace(/^Ctrl/, "Control")}
        aria-busy={loading || undefined}
        disabled={disabled}
        onClick={onClick}
      >
        {loading ? <Spinner small /> : <Icon name={icon} size={20} />}
      </button>
      <span className="modmaker-editor-action-tooltip" role="tooltip">
        <span>{label}</span>
        {shortcut && <kbd>{shortcut}</kbd>}
      </span>
    </span>
  );
}

interface PersistedSessionTabDescriptor {
  id: string;
  workspaceId: string;
  title: string;
  userTitle: string;
  tabOrder: number;
  createdAt: string;
  updatedAt: string;
  seededDefault: boolean;
}

const virgilSessionTabsStoragePrefix = "beamworlds.modmaker-virgil-tabs.v1";

function virgilSessionTabsStorageKey(workspaceID: string) {
  return `${virgilSessionTabsStoragePrefix}:${workspaceID}`;
}

function createSessionTabID(existing?: ReadonlySet<string>) {
  let id = "";
  do {
    const token =
      typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    id = `draft:${token}`;
  } while (existing?.has(id));
  return id;
}

function readPersistedSessionTabDescriptors(workspaceID: string): {
  present: boolean;
  tabs: PersistedSessionTabDescriptor[];
} {
  if (typeof window === "undefined") return { present: false, tabs: [] };
  let raw: string | null;
  try {
    raw = window.localStorage.getItem(virgilSessionTabsStorageKey(workspaceID));
  } catch {
    return { present: false, tabs: [] };
  }
  if (raw === null) return { present: false, tabs: [] };

  try {
    const parsed = JSON.parse(raw) as { tabs?: unknown };
    const entries = Array.isArray(parsed?.tabs)
      ? (parsed.tabs as unknown[])
      : [];
    const seen = new Set<string>();
    const tabs: PersistedSessionTabDescriptor[] = [];
    for (const entry of entries) {
      if (!entry || typeof entry !== "object") continue;
      const value = entry as Record<string, unknown>;
      const id = typeof value.id === "string" ? value.id.trim() : "";
      const storedWorkspaceID =
        typeof value.workspaceId === "string" ? value.workspaceId.trim() : "";
      if (
        !id ||
        seen.has(id) ||
        (storedWorkspaceID && storedWorkspaceID !== workspaceID)
      )
        continue;
      const now = new Date().toISOString();
      const createdAt =
        typeof value.createdAt === "string" && value.createdAt
          ? value.createdAt
          : now;
      tabs.push({
        id,
        workspaceId: workspaceID,
        title:
          typeof value.title === "string" && value.title.trim()
            ? value.title.trim()
            : "Virgil session",
        userTitle:
          typeof value.userTitle === "string" ? value.userTitle.trim() : "",
        tabOrder:
          typeof value.tabOrder === "number" && Number.isFinite(value.tabOrder)
            ? value.tabOrder
            : tabs.length,
        createdAt,
        updatedAt:
          typeof value.updatedAt === "string" && value.updatedAt
            ? value.updatedAt
            : createdAt,
        seededDefault: value.seededDefault === true,
      });
      seen.add(id);
    }
    return { present: true, tabs };
  } catch {
    return { present: true, tabs: [] };
  }
}

function transientSessionTab(
  workspaceID: string,
  descriptor: Partial<PersistedSessionTabDescriptor> = {},
): SessionTab {
  const now = new Date().toISOString();
  const title = descriptor.title?.trim() || "Virgil session";
  const createdAt = descriptor.createdAt || now;
  return {
    record: {
      id: descriptor.id?.trim() || createSessionTabID(),
      workspaceId: workspaceID,
      profile: "",
      runtimeSessionId: "",
      title,
      runtimeTitle: "",
      userTitle: descriptor.userTitle?.trim() || "",
      status: "idle",
      lastError: "",
      tabOrder: descriptor.tabOrder ?? 0,
      createdAt,
      updatedAt: descriptor.updatedAt || createdAt,
      runs: [],
    } as VirgilSessionRecord,
    transient: true,
    busy: false,
    seededDefault: descriptor.seededDefault === true,
  };
}

function sortSessionTabs(tabs: SessionTab[]) {
  return [...tabs].sort((left, right) => {
    const order = (left.record.tabOrder ?? 0) - (right.record.tabOrder ?? 0);
    if (order) return order;
    const leftCreated = Date.parse(left.record.createdAt);
    const rightCreated = Date.parse(right.record.createdAt);
    if (Number.isFinite(leftCreated) && Number.isFinite(rightCreated)) {
      if (leftCreated !== rightCreated) return leftCreated - rightCreated;
    }
    return left.record.id.localeCompare(right.record.id);
  });
}

function writePersistedSessionTabDescriptors(
  workspaceID: string,
  tabs: readonly SessionTab[],
) {
  if (typeof window === "undefined") return;
  const descriptors = tabs
    .filter((tab) => tab.transient && tab.record.workspaceId === workspaceID)
    .map(({ record, seededDefault }) => ({
      id: record.id,
      workspaceId: workspaceID,
      title: record.title || "Virgil session",
      userTitle: record.userTitle || "",
      tabOrder: record.tabOrder ?? 0,
      createdAt: record.createdAt,
      updatedAt: record.updatedAt,
      seededDefault: seededDefault === true,
    }));
  try {
    window.localStorage.setItem(
      virgilSessionTabsStorageKey(workspaceID),
      JSON.stringify({ version: 1, tabs: descriptors }),
    );
  } catch {
    // Storage is optional; the durable session records remain authoritative.
  }
}

const editableExtensions: Record<string, true> = {
  ".cfg": true,
  ".cs": true,
  ".css": true,
  ".html": true,
  ".ini": true,
  ".jbeam": true,
  ".js": true,
  ".json": true,
  ".json5": true,
  ".lua": true,
  ".md": true,
  ".mis": true,
  ".pc": true,
  ".scss": true,
  ".ts": true,
  ".txt": true,
  ".xml": true,
  ".yaml": true,
  ".yml": true,
};

const workspaceTools: Array<{
  key: WorkspaceTool;
  icon: Parameters<typeof Icon>[0]["name"];
  label: string;
}> = [
  { key: "source", icon: "diff", label: "Source Control" },
  { key: "build", icon: "check", label: "Build" },
  { key: "context", icon: "book", label: "Context" },
  { key: "game", icon: "terminal", label: "Game test" },
];

const FILE_BROWSER_DEFAULT_WIDTH = 300;
const FILE_BROWSER_MIN_WIDTH = 200;
const FILE_BROWSER_MAX_WIDTH = 480;
const FILE_BROWSER_RESIZER_WIDTH = 8;
const EDITOR_MIN_WIDTH = 260;
const fileBrowserWidthStorageKey = "beamworlds.editor-file-browser-width.v1";

function defaultFileBrowserWidth() {
  if (typeof window !== "undefined") {
    if (window.innerWidth <= 1000) return FILE_BROWSER_MIN_WIDTH;
    if (window.innerWidth <= 1120) return 240;
  }
  return FILE_BROWSER_DEFAULT_WIDTH;
}

function fileBrowserWidthBounds(availableWidth?: number) {
  if (!Number.isFinite(availableWidth)) {
    return { min: FILE_BROWSER_MIN_WIDTH, max: FILE_BROWSER_MAX_WIDTH };
  }
  const room = Math.max(
    0,
    Number(availableWidth) - FILE_BROWSER_RESIZER_WIDTH - EDITOR_MIN_WIDTH,
  );
  const max = Math.min(FILE_BROWSER_MAX_WIDTH, room);
  return { min: Math.min(FILE_BROWSER_MIN_WIDTH, max), max };
}

function clampFileBrowserWidth(width: number, availableWidth?: number) {
  const bounds = fileBrowserWidthBounds(availableWidth);
  return Math.min(bounds.max, Math.max(bounds.min, width));
}

function readFileBrowserWidth() {
  const fallback = defaultFileBrowserWidth();
  try {
    const value = window.localStorage.getItem(fileBrowserWidthStorageKey);
    if (!value) return fallback;
    const stored = Number(value);
    return Number.isFinite(stored) ? clampFileBrowserWidth(stored) : fallback;
  } catch {
    return fallback;
  }
}

interface FileBrowserResize {
  pointerId: number;
  startX: number;
  startWidth: number;
  divider: HTMLDivElement;
}

function isEditableSource(path: string) {
  const dot = path.toLowerCase().lastIndexOf(".");
  return dot >= 0 && editableExtensions[path.toLowerCase().slice(dot)] === true;
}

interface SearchMatchRange {
  from: number;
  to: number;
}

interface PendingEditorReveal {
  path: string;
  line: number;
  column: number;
  matchLength: number;
  query: string;
  regex: boolean;
  caseSensitive: boolean;
}

function searchRanges(
  content: string,
  query: string,
  regex: boolean,
  caseSensitive: boolean,
): SearchMatchRange[] {
  if (!query) return [];
  const expression = new RegExp(
    regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
    caseSensitive ? "g" : "gi",
  );
  const ranges: SearchMatchRange[] = [];
  let match: RegExpExecArray | null;
  while ((match = expression.exec(content)) !== null) {
    ranges.push({ from: match.index, to: match.index + match[0].length });
    if (match[0].length === 0) expression.lastIndex = match.index + 1;
    if (ranges.length >= 10_000) break;
  }
  return ranges;
}

function lineStartOffset(content: string, lineNumber: number) {
  let line = 1;
  let offset = 0;
  const targetLine = Math.max(1, lineNumber);
  while (line < targetLine) {
    const next = content.indexOf("\n", offset);
    if (next < 0) return content.length;
    offset = next + 1;
    line += 1;
  }
  return offset;
}

function revealRangeForSearchResult(
  content: string,
  result: PendingEditorReveal,
): SearchMatchRange {
  const start = lineStartOffset(content, result.line);
  const newline = content.indexOf("\n", start);
  const lineEnd = newline >= 0 ? newline : content.length;
  const lineContent = content.slice(start, lineEnd);
  let ranges: SearchMatchRange[] = [];
  try {
    ranges = searchRanges(
      lineContent,
      result.query,
      result.regex,
      result.caseSensitive,
    );
  } catch {
    ranges = [];
  }
  const expectedFrom = Math.max(0, result.column - 1);
  const match =
    ranges.find((range) => range.from === expectedFrom) ?? ranges[0];
  if (match) {
    return { from: start + match.from, to: start + match.to };
  }
  const from = Math.min(lineEnd, start + expectedFrom);
  return {
    from,
    to: Math.min(lineEnd, from + Math.max(1, result.matchLength)),
  };
}

function searchErrorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}
const AUTO_FORMAT_DELAY_DEFAULT_MS = 200;
const AUTO_FORMAT_DELAY_MIN_MS = 50;
const AUTO_FORMAT_DELAY_MAX_MS = 2000;

function clampAutoFormatDelay(value: number) {
  if (!Number.isFinite(value) || value <= 0)
    return AUTO_FORMAT_DELAY_DEFAULT_MS;
  return Math.min(
    AUTO_FORMAT_DELAY_MAX_MS,
    Math.max(AUTO_FORMAT_DELAY_MIN_MS, Math.round(value)),
  );
}

function severityFromSave(
  formatFailed: boolean,
  diagnostics: readonly CodeEditorDiagnostic[],
): TreeSeverity | undefined {
  if (
    formatFailed ||
    diagnostics.some((diagnostic) => diagnostic.severity === "error")
  )
    return "error";
  if (diagnostics.some((diagnostic) => diagnostic.severity === "warning"))
    return "warning";
  return undefined;
}

function formatSaveIssue(path: string, error: unknown) {
  const record =
    error && typeof error === "object"
      ? (error as Record<string, unknown>)
      : {};
  const rawMessage =
    typeof record.message === "string"
      ? record.message
      : error instanceof Error
        ? error.message
        : String(error);
  const message =
    rawMessage
      .split(/\r?\n/, 1)[0]
      .replace(/^(?:Syntax)?Error:\s*/i, "")
      .trim() || "The formatter rejected this source.";
  const location =
    record.loc && typeof record.loc === "object"
      ? (record.loc as Record<string, unknown>)
      : {};
  const start =
    location.start && typeof location.start === "object"
      ? (location.start as Record<string, unknown>)
      : location;
  const line =
    typeof start.line === "number" && Number.isFinite(start.line)
      ? Math.max(1, Math.trunc(start.line))
      : undefined;
  const column =
    typeof start.column === "number" && Number.isFinite(start.column)
      ? Math.max(1, Math.trunc(start.column))
      : undefined;
  const suffix = line
    ? ` (line ${line}${column ? `, column ${column}` : ""})`
    : "";
  return `${path}: Saved, but formatting failed — ${message}${suffix}`;
}
function validationSaveIssue(
  path: string,
  source: string,
  diagnostics: readonly CodeEditorDiagnostic[],
) {
  const diagnostic =
    diagnostics.find((item) => item.severity === "error") ??
    diagnostics.find((item) => item.severity === "warning");
  if (!diagnostic) return undefined;
  const severity: "error" | "warning" =
    diagnostic.severity === "error" ? "error" : "warning";
  const message =
    diagnostic.message.split(/\r?\n/, 1)[0].trim() ||
    "Validation reported an issue.";
  const from = Math.max(0, Math.min(source.length, diagnostic.from));
  const lineStart = source.lastIndexOf("\n", Math.max(0, from - 1)) + 1;
  const line = source.slice(0, from).split(/\r?\n/).length;
  const column = from - lineStart + 1;
  return {
    message: `${path}: Saved with ${severity} — ${message} (line ${line}, column ${column})`,
    severity,
  };
}
const yieldToQueuedWork = () =>
  new Promise<void>((resolve) => {
    window.setTimeout(resolve, 0);
  });

interface ModMakerProps {
  allItems: LibraryItem[];
  workspaces: WorkspaceRecord[];
  detail: WorkspaceDetail | null;
  selectedID: string;
  stale: boolean;
  writeBlocked: boolean;
  isWriteBlocked: () => boolean;
  loading: boolean;
  defaultAuthor: string;
  showFileSizes: boolean;
  autoFormatDelayMs: number;
  agentActivityBuffer: MutableRefObject<Record<string, AgentActivity[]>>;
  onSelect: (workspaceID: string) => void;
  onReload: () => Promise<void>;
  onCreateMod: (
    request: NewModRequest,
    virgilPrompt?: string,
    modelOverride?: string,
  ) => Promise<void>;
  onEditorStatus: (status: {
    path: string;
    dirty: boolean;
    sizeBytes: number;
  }) => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
  onError: (error: unknown) => void;
}

export function ModMaker({
  workspaces,
  allItems,
  detail,
  selectedID,
  stale,
  writeBlocked,
  isWriteBlocked,
  loading,
  defaultAuthor,
  showFileSizes,
  autoFormatDelayMs,
  agentActivityBuffer,
  onSelect,
  onReload,
  onCreateMod,
  onEditorStatus,
  onNotify,
  onError,
}: ModMakerProps) {
  const [documents, setDocuments] = useState<EditorDocument[]>([]);
  const [activePath, setActivePath] = useState("");
  const [activeTab, setActiveTab] = useState<ActiveEditorTab | null>(null);
  const [sessionTabs, setSessionTabs] = useState<SessionTab[]>([]);
  const [sessionPrompts, setSessionPrompts] = useState<Record<string, string>>(
    {},
  );
  const [sessionMenu, setSessionMenu] = useState<SessionContextMenu | null>(
    null,
  );
  const [treeSelection, setTreeSelection] = useState<TreeSelection | null>(
    null,
  );
  const [fileLoadingPath, setFileLoadingPath] = useState("");
  const [fileQuery, setFileQuery] = useState("");
  const [workspaceSearchOpen, setWorkspaceSearchOpen] = useState(false);
  const [workspaceSearchQuery, setWorkspaceSearchQuery] = useState("");
  const [workspaceSearchRegex, setWorkspaceSearchRegex] = useState(false);
  const [workspaceSearchCaseSensitive, setWorkspaceSearchCaseSensitive] =
    useState(false);
  const [workspaceSearchResults, setWorkspaceSearchResults] = useState<
    WorkspaceSearchMatch[]
  >([]);
  const [workspaceSearchError, setWorkspaceSearchError] = useState("");
  const [workspaceSearchLoading, setWorkspaceSearchLoading] = useState(false);
  const [fileSearchOpen, setFileSearchOpen] = useState(false);
  const [fileSearchQuery, setFileSearchQuery] = useState("");
  const [fileSearchMatchIndex, setFileSearchMatchIndex] = useState(0);
  const [editorSelection, setEditorSelection] =
    useState<CodeEditorSelection | null>(null);
  const [pendingEditorReveal, setPendingEditorReveal] =
    useState<PendingEditorReveal | null>(null);
  const [utility, setUtility] = useState<WorkspaceTool | null>(null);
  const [sourceBusy, setSourceBusy] = useState(false);
  const [busy, setBusy] = useState("");
  const [exportLabel, setExportLabel] = useState("");
  const [runtime, setRuntime] = useState<RuntimeReport | null>(null);
  const [preferenceBusy, setPreferenceBusy] = useState(false);
  const [newModOpen, setNewModOpen] = useState(false);
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [deletedWorkspaceIDs, setDeletedWorkspaceIDs] = useState<
    Set<string>
  >(() => new Set());
  const [sourceSaveToast, setSourceSaveToast] = useState("");
  const [sourceSaveToastTone, setSourceSaveToastTone] = useState<
    "success" | "error" | "warning"
  >("success");
  const [fileManagerLabel, setFileManagerLabel] = useState<string | null>(null);
  const [fileManagerLabelError, setFileManagerLabelError] = useState("");
  const initializedWorkspace = useRef("");
  const draftTimer = useRef<number | undefined>(undefined);
  const [fileSeverity, setFileSeverity] = useState<
    Record<string, TreeSeverity>
  >({});
  const documentsRef = useRef<EditorDocument[]>([]);
  const documentRevisions = useRef<Record<string, number>>({});
  const diagnosticsByPath = useRef<Record<string, CodeEditorDiagnostic[]>>({});
  const formatTimers = useRef(new Map<string, number>());
  const workingTreeChangeVersion = useRef(0);
  const formatRequestVersion = useRef(0);
  const formatGeneration = useRef(0);
  const externalReadVersion = useRef<Record<string, number>>({});
  const sessionTabsRef = useRef<SessionTab[]>([]);
  const fileOpenVersion = useRef(0);
  const untitledSequence = useRef(0);
  const transientSequence = useRef(0);
  const closedTransientSessions = useRef(new Set<string>());
  const sessionTabsInitializedWorkspace = useRef("");
  const sessionTabsWorkspace = useRef("");
  const sessionTabsHydrationPending = useRef("");
  const activeEditorPathRef = useRef(activePath);
  const activeWorkspaceIDRef = useRef(selectedID);
  const staleRef = useRef(stale);
  staleRef.current = stale;
  const mutationBlocked = () =>
    staleRef.current || writeBlocked || isWriteBlocked();
  const activeTabRef = useRef<ActiveEditorTab | null>(activeTab);
  const editorInteractionVersion = useRef(0);
  const preferenceRequestVersion = useRef(0);
  const savesInFlight = useRef(new Set<string>());
  const sourceSaveToastTimer = useRef<number | undefined>(undefined);
  const workspaceMainRef = useRef<HTMLElement>(null);
  const [fileBrowserWidth, setFileBrowserWidth] =
    useState(readFileBrowserWidth);
  const [fileBrowserResizing, setFileBrowserResizing] = useState(false);
  const editorLayoutRef = useRef<HTMLDivElement>(null);
  const fileBrowserResizeRef = useRef<FileBrowserResize | null>(null);
  const workspaceSearchInputRef = useRef<HTMLInputElement>(null);
  const fileSearchInputRef = useRef<HTMLInputElement>(null);
  const workspaceSearchRequestVersion = useRef(0);
  const editorSelectionRequest = useRef(0);
  const dismissSourceSaveToast = () => {
    setSourceSaveToast("");
    setSourceSaveToastTone("success");
    if (sourceSaveToastTimer.current !== undefined) {
      window.clearTimeout(sourceSaveToastTimer.current);
      sourceSaveToastTimer.current = undefined;
    }
  };
  const showSourceSaveToast = (
    message: string,
    tone: "success" | "error" | "warning" = "success",
  ) => {
    if (sourceSaveToastTimer.current !== undefined) {
      window.clearTimeout(sourceSaveToastTimer.current);
    }
    setSourceSaveToast(message);
    setSourceSaveToastTone(tone);
    sourceSaveToastTimer.current = window.setTimeout(() => {
      setSourceSaveToast("");
      setSourceSaveToastTone("success");
      sourceSaveToastTimer.current = undefined;
    }, 5000);
  };
  const clearFormatTimer = (path: string) => {
    const timer = formatTimers.current.get(path);
    if (timer === undefined) return;
    window.clearTimeout(timer);
    formatTimers.current.delete(path);
  };
  const cancelFormatTasks = () => {
    formatGeneration.current += 1;
    const cancelledRequestVersion = formatRequestVersion.current;
    formatRequestVersion.current += 1;
    for (const timer of formatTimers.current.values())
      window.clearTimeout(timer);
    formatTimers.current.clear();
    setBusy((current) =>
      current === "format" &&
      formatRequestVersion.current === cancelledRequestVersion + 1
        ? ""
        : current,
    );
  };
  const bumpDocumentRevision = (path: string) => {
    const next = (documentRevisions.current[path] ?? 0) + 1;
    documentRevisions.current[path] = next;
    return next;
  };
  useEffect(() => {
    dismissSourceSaveToast();
    return () => {
      if (sourceSaveToastTimer.current !== undefined) {
        window.clearTimeout(sourceSaveToastTimer.current);
        sourceSaveToastTimer.current = undefined;
      }
      cancelFormatTasks();
    };
  }, [selectedID]);

  useEffect(() => {
    let active = true;
    const unavailable =
      "File manager action unavailable; reload this workspace to try again.";
    const labelAPI = API as typeof API & {
      FileManagerActionLabel?: () => Promise<string>;
    };
    if (typeof labelAPI.FileManagerActionLabel !== "function") {
      setFileManagerLabelError(unavailable);
      return () => {
        active = false;
      };
    }
    void labelAPI
      .FileManagerActionLabel()
      .then((label) => {
        if (
          label === "Open in Explorer" ||
          label === "Reveal in Finder" ||
          label === "Open in File Manager"
        ) {
          setFileManagerLabel(label);
          setFileManagerLabelError("");
        } else {
          setFileManagerLabelError(unavailable);
        }
      })
      .catch(() => {
        if (active) setFileManagerLabelError(unavailable);
      });
    return () => {
      active = false;
    };
  }, []);

  const searchReturnFocus = useRef<HTMLElement | null>(null);
  activeWorkspaceIDRef.current = selectedID;
  activeTabRef.current = activeTab;

  const workspace = detail?.workspace;
  const files = detail?.files ?? [];
  const directories = detail?.directories ?? [];
  const activeDocument = documents.find(
    (document) => document.path === activePath,
  );
  const activeSession =
    activeTab?.kind === "session"
      ? sessionTabs.find((tab) => tab.record.id === activeTab.id)
      : undefined;
  const dirty = documents.some(
    (document) =>
      document.untitled || document.content !== document.savedContent,
  );
  const dirtyPaths = useMemo(
    () =>
      new Set(
        documents
          .filter(
            (document) =>
              !document.untitled && document.content !== document.savedContent,
          )
          .map((document) => document.path),
      ),
    [documents],
  );
  const activeDocumentLabel = activeDocument
    ? editorDocumentLabel(activeDocument)
    : "";
  const activeSizeBytes = useMemo(
    () =>
      activeDocument
        ? new TextEncoder().encode(activeDocument.content).byteLength
        : 0,
    [activeDocument?.content],
  );
  const activeEditorIdentity = activeSession
    ? `session:${activeSession.record.id}`
    : activeDocument
      ? `file:${activeDocument.path}`
      : "";
  activeEditorPathRef.current = activeSession ? "" : activePath;
  useEffect(() => {
    dismissSourceSaveToast();
    cancelFormatTasks();
  }, [activeEditorIdentity]);
  const fileSearchMatches = useMemo(
    () =>
      activeDocument && fileSearchQuery.trim()
        ? searchRanges(
            activeDocument.content,
            fileSearchQuery.trim(),
            false,
            false,
          )
        : [],
    [activeDocument?.content, fileSearchQuery],
  );
  const activeFileSearchMatch = fileSearchMatches[fileSearchMatchIndex];
  useEffect(() => {
    const layout = editorLayoutRef.current;
    if (!layout) return;
    const syncWidth = () => {
      const availableWidth = layout.getBoundingClientRect().width;
      if (availableWidth <= 0) return;
      setFileBrowserWidth((current) =>
        clampFileBrowserWidth(current, availableWidth),
      );
    };
    syncWidth();
    let observer: ResizeObserver | undefined;
    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(syncWidth);
      observer.observe(layout);
    }
    return () => {
      observer?.disconnect();
      const active = fileBrowserResizeRef.current;
      if (!active) return;
      if (active.divider.hasPointerCapture(active.pointerId)) {
        active.divider.releasePointerCapture(active.pointerId);
      }
      fileBrowserResizeRef.current = null;
      setFileBrowserResizing(false);
    };
  }, [selectedID, workspace?.id, loading]);

  useEffect(() => {
    try {
      window.localStorage.setItem(
        fileBrowserWidthStorageKey,
        String(fileBrowserWidth),
      );
    } catch {}
  }, [fileBrowserWidth]);

  const beginFileBrowserResize = (
    event: React.PointerEvent<HTMLDivElement>,
  ) => {
    if (event.button !== 0 || !event.isPrimary) return;
    if (fileBrowserResizeRef.current) return;
    event.preventDefault();
    event.stopPropagation();
    const divider = event.currentTarget;
    const availableWidth =
      editorLayoutRef.current?.getBoundingClientRect().width;
    fileBrowserResizeRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startWidth: clampFileBrowserWidth(fileBrowserWidth, availableWidth),
      divider,
    };
    divider.setPointerCapture(event.pointerId);
    setFileBrowserResizing(true);
  };

  const updateFileBrowserResize = (
    event: React.PointerEvent<HTMLDivElement>,
  ) => {
    const active = fileBrowserResizeRef.current;
    if (!active || active.pointerId !== event.pointerId) return;
    event.preventDefault();
    const availableWidth =
      editorLayoutRef.current?.getBoundingClientRect().width;
    setFileBrowserWidth(
      clampFileBrowserWidth(
        active.startWidth + event.clientX - active.startX,
        availableWidth,
      ),
    );
  };

  const endFileBrowserResize = (event: React.PointerEvent<HTMLDivElement>) => {
    const active = fileBrowserResizeRef.current;
    if (!active || active.pointerId !== event.pointerId) return;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    fileBrowserResizeRef.current = null;
    setFileBrowserResizing(false);
  };

  const resizeFileBrowserWithKeyboard = (
    event: React.KeyboardEvent<HTMLDivElement>,
  ) => {
    if (
      event.key !== "ArrowLeft" &&
      event.key !== "ArrowRight" &&
      event.key !== "Home" &&
      event.key !== "End"
    )
      return;
    event.preventDefault();
    event.stopPropagation();
    const step = event.shiftKey ? 32 : 16;
    const availableWidth =
      editorLayoutRef.current?.getBoundingClientRect().width;
    setFileBrowserWidth((current) => {
      const delta =
        event.key === "ArrowLeft"
          ? -step
          : event.key === "ArrowRight"
            ? step
            : event.key === "Home"
              ? FILE_BROWSER_MIN_WIDTH - current
              : FILE_BROWSER_MAX_WIDTH - current;
      return clampFileBrowserWidth(current + delta, availableWidth);
    });
  };

  const currentFileBrowserBounds = fileBrowserWidthBounds(
    editorLayoutRef.current?.getBoundingClientRect().width,
  );

  useEffect(() => {
    cancelFormatTasks();
    documentRevisions.current = {};
    diagnosticsByPath.current = {};
    setFileSeverity({});
    initializedWorkspace.current = "";
    sessionTabsInitializedWorkspace.current = "";
    sessionTabsWorkspace.current = "";
    sessionTabsHydrationPending.current = "";
    fileOpenVersion.current += 1;
    workingTreeChangeVersion.current += 1;
    editorInteractionVersion.current += 1;
    preferenceRequestVersion.current += 1;
    transientSequence.current = 0;
    untitledSequence.current = 0;
    sessionTabsRef.current = [];
    activeTabRef.current = null;
    setDocuments([]);
    setActivePath("");
    setActiveTab(null);
    setSessionTabs([]);
    setSessionPrompts({});
    setSessionMenu(null);
    setTreeSelection(null);
    setFileLoadingPath("");
    setFileQuery("");
    workspaceSearchRequestVersion.current += 1;
    setWorkspaceSearchOpen(false);
    setWorkspaceSearchQuery("");
    setWorkspaceSearchRegex(false);
    setWorkspaceSearchCaseSensitive(false);
    setWorkspaceSearchResults([]);
    setWorkspaceSearchError("");
    setWorkspaceSearchLoading(false);
    setFileSearchOpen(false);
    setFileSearchQuery("");
    setFileSearchMatchIndex(0);
    setEditorSelection(null);
    setPendingEditorReveal(null);
    searchReturnFocus.current = null;
    setBusy("");
    setExportLabel("");
    setPreferenceBusy(false);
    setRuntime(null);
    setSourceBusy(false);
    setUtility(null);
  }, [selectedID, workspace?.id]);

  useEffect(() => {
    if (!sessionMenu) return;
    const dismiss = () => setSessionMenu(null);
    const dismissKeyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") dismiss();
    };
    window.addEventListener("pointerdown", dismiss);
    window.addEventListener("keydown", dismissKeyboard);
    return () => {
      window.removeEventListener("pointerdown", dismiss);
      window.removeEventListener("keydown", dismissKeyboard);
    };
  }, [sessionMenu]);

  useEffect(() => {
    if (!newModOpen) return;
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") setNewModOpen(false);
    };
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [newModOpen]);

  useEffect(() => {
    workspaceMainRef.current?.toggleAttribute(
      "inert",
      Boolean(workspace && !workspace.virgilConfigured),
    );
  }, [workspace?.id, workspace?.virgilConfigured]);

  useEffect(() => {
    if (
      !workspace ||
      workspace.id !== selectedID ||
      initializedWorkspace.current === workspace.id
    )
      return;
    initializedWorkspace.current = workspace.id;
    const interactionVersion = editorInteractionVersion.current;

    const drafts = detail?.drafts ?? [];
    if (!drafts.length) return;

    let cancelled = false;
    void Promise.all(
      drafts.map(async (draft) => {
        try {
          return documentFromFile(
            await API.ReadWorkspaceFile(workspace.id, draft.path),
            draft,
          );
        } catch {
          return {
            path: draft.path,
            content: draft.content,
            savedContent: "",
            savedSHA256: draft.baseSha256,
            restored: true,
            externalContent: draft.baseSha256 ? "" : undefined,
            externalSHA256: draft.baseSha256 ? "" : undefined,
          } satisfies EditorDocument;
        }
      }),
    )
      .then((restored) => {
        if (cancelled) return;
        for (const document of restored)
          documentRevisions.current[document.path] = Math.max(
            0,
            Math.trunc(documentRevisions.current[document.path] ?? 0),
          );
        setDocuments((current) => {
          const openPaths = new Set(current.map((document) => document.path));
          const next = [
            ...current,
            ...restored.filter((document) => !openPaths.has(document.path)),
          ];
          documentsRef.current = next;
          return next;
        });
        if (
          restored[0] &&
          editorInteractionVersion.current === interactionVersion &&
          activeTabRef.current === null
        ) {
          setActivePath(restored[0].path);
          setActiveTab({ kind: "file", path: restored[0].path });
          setTreeSelection({ path: restored[0].path, kind: "file" });
        }
        if (restored.length) {
          onNotify(
            `Restored ${restored.length} unsaved ${restored.length === 1 ? "draft" : "drafts"}`,
            "info",
          );
        }
      })
      .catch(onError);

    return () => {
      cancelled = true;
    };
  }, [workspace?.id, selectedID]);

  useEffect(() => {
    if (!workspace || workspace.id !== selectedID) return;
    const durable = (detail?.virgilSessions ?? [])
      .map((record) => ({ record, transient: false }))
      .sort((left, right) => {
        const order =
          (left.record.tabOrder ?? 0) - (right.record.tabOrder ?? 0);
        return (
          order ||
          Date.parse(left.record.createdAt) - Date.parse(right.record.createdAt)
        );
      });
    const isInitial = sessionTabsInitializedWorkspace.current !== workspace.id;
    const persisted = isInitial
      ? readPersistedSessionTabDescriptors(workspace.id)
      : undefined;
    let restoredTransient = isInitial
      ? (persisted?.tabs ?? [])
          .filter(
            (descriptor) =>
              !durable.some((tab) => tab.record.id === descriptor.id),
          )
          .map((descriptor) => transientSessionTab(workspace.id, descriptor))
      : [];
    if (
      isInitial &&
      durable.length === 0 &&
      !persisted?.present &&
      restoredTransient.length === 0
    ) {
      restoredTransient = [
        transientSessionTab(workspace.id, {
          title: `Virgil session ${++transientSequence.current}`,
          tabOrder: 0,
          seededDefault: true,
        }),
      ];
    }
    if (isInitial) {
      sessionTabsInitializedWorkspace.current = workspace.id;
      sessionTabsWorkspace.current = workspace.id;
      sessionTabsHydrationPending.current = workspace.id;
      transientSequence.current = Math.max(
        transientSequence.current,
        restoredTransient.filter((tab) => tab.transient).length,
      );
    }

    const availableTransient = (
      isInitial
        ? restoredTransient
        : sessionTabsRef.current.filter((tab) => tab.transient)
    ).filter((tab) => durable.length === 0 || !tab.seededDefault);
    const availableIDs = new Set([
      ...durable.map((tab) => tab.record.id),
      ...availableTransient.map((tab) => tab.record.id),
    ]);
    const firstSession = sortSessionTabs([
      ...durable,
      ...availableTransient,
    ])[0];
    if (isInitial && sessionTabsRef.current.length === 0) {
      sessionTabsRef.current = sortSessionTabs([
        ...durable,
        ...availableTransient,
      ]);
    }

    setSessionTabs((current) => {
      const transient = (
        isInitial
          ? [
              ...restoredTransient,
              ...current.filter(
                (tab) =>
                  tab.transient &&
                  !restoredTransient.some(
                    (restored) => restored.record.id === tab.record.id,
                  ),
              ),
            ]
          : current.filter((tab) => tab.transient)
      ).filter((tab) => durable.length === 0 || !tab.seededDefault);
      const currentByID = new Map(
        current
          .filter((tab) => !tab.transient)
          .map((tab) => [tab.record.id, tab]),
      );
      const merged = durable.map((tab) => {
        const local = currentByID.get(tab.record.id);
        return local
          ? { ...tab, record: { ...local.record, ...tab.record } }
          : tab;
      });
      const next = sortSessionTabs([...merged, ...transient]);
      sessionTabsRef.current = next;
      return next;
    });

    const currentActive = activeTabRef.current;
    if (
      currentActive?.kind === "session" &&
      !availableIDs.has(currentActive.id)
    ) {
      const nextActive = firstSession
        ? { kind: "session" as const, id: firstSession.record.id }
        : null;
      activeTabRef.current = nextActive;
      setActiveTab(nextActive);
    } else if (!currentActive && firstSession) {
      const nextActive = {
        kind: "session" as const,
        id: firstSession.record.id,
      };
      activeTabRef.current = nextActive;
      setActiveTab(nextActive);
    }
  }, [workspace?.id, selectedID, detail?.virgilSessions]);
  useEffect(() => {
    if (!workspace || workspace.id !== selectedID) return;
    if (sessionTabsWorkspace.current !== workspace.id) return;
    if (sessionTabsHydrationPending.current === workspace.id) {
      sessionTabsHydrationPending.current = "";
      return;
    }
    if (mutationBlocked()) return;
    writePersistedSessionTabDescriptors(workspace.id, sessionTabs);
  }, [sessionTabs, workspace?.id, selectedID]);

  useEffect(() => {
    onEditorStatus({
      path: activeSession ? "" : activeDocumentLabel,
      dirty,
      sizeBytes: activeSession ? 0 : activeSizeBytes,
    });
    return () => onEditorStatus({ path: "", dirty: false, sizeBytes: 0 });
  }, [
    activeDocumentLabel,
    activeSession?.record.id,
    dirty,
    activeSizeBytes,
    onEditorStatus,
  ]);

  useEffect(() => {
    documentsRef.current = documents;
  }, [documents]);

  useEffect(() => {
    sessionTabsRef.current = sessionTabs;
  }, [sessionTabs]);
  const reconcileWorkingTree = async (
    change: SourceControlWorkingTreeChange,
  ) => {
    const currentWorkspace = workspace;
    if (
      !currentWorkspace ||
      currentWorkspace.id !== selectedID ||
      staleRef.current
    )
      return;

    const workspaceID = currentWorkspace.id;
    const requestVersion = ++workingTreeChangeVersion.current;
    const isCurrentRequest = () =>
      requestVersion === workingTreeChangeVersion.current &&
      activeWorkspaceIDRef.current === workspaceID &&
      !staleRef.current;
    const normalizePath = (path: string) =>
      path
        .replace(/\\/g, "/")
        .replace(/^\.\/+/, "")
        .toLowerCase();
    const changedPaths =
      change.paths && change.paths.length > 0
        ? new Set(change.paths.map(normalizePath))
        : null;
    const targets = documentsRef.current.filter(
      (document) =>
        !document.untitled &&
        (!changedPaths || changedPaths.has(normalizePath(document.path))),
    );
    const readVersions = new Map<string, number>();
    for (const document of targets) {
      const request = (externalReadVersion.current[document.path] ?? 0) + 1;
      externalReadVersion.current[document.path] = request;
      readVersions.set(document.path, request);
    }

    const reads = await Promise.all(
      targets.map(async (document) => {
        try {
          return {
            path: document.path,
            file: await API.ReadWorkspaceFile(workspaceID, document.path),
          };
        } catch (error) {
          return { path: document.path, error };
        }
      }),
    );
    if (!isCurrentRequest()) return;

    const updates = new Map<string, EditorDocument>();
    const missingPaths = new Set<string>();
    const isMissingWorkspaceFile = (error: unknown) => {
      if (error && typeof error === "object" && "code" in error) {
        const code = error.code;
        if (code === "ENOENT" || code === "NOT_FOUND") return true;
      }
      const message = error instanceof Error ? error.message : String(error);
      return /not[\s_-]*found|does not exist|no such file|cannot find|could not find|missing|deleted|removed/i.test(
        message,
      );
    };

    for (const result of reads) {
      const currentDocument = documentsRef.current.find(
        (document) => document.path === result.path,
      );
      if (!currentDocument) continue;
      if (
        readVersions.get(currentDocument.path) !==
        externalReadVersion.current[currentDocument.path]
      )
        continue;
      if ("file" in result) {
        const file = result.file;
        if (!file) {
          onError(
            new Error(
              `Git ${change.reason} changed ${result.path}, but the workspace file could not be read.`,
            ),
          );
          continue;
        }
        const dirtyBuffer =
          currentDocument.content !== currentDocument.savedContent;
        bumpDocumentRevision(currentDocument.path);
        updates.set(
          currentDocument.path,
          dirtyBuffer
            ? file.sha256 === currentDocument.savedSHA256
              ? {
                  ...currentDocument,
                  externalContent: undefined,
                  externalSHA256: undefined,
                }
              : {
                  ...currentDocument,
                  externalContent: file.content,
                  externalSHA256: file.sha256,
                }
            : {
                ...currentDocument,
                content: file.content,
                savedContent: file.content,
                savedSHA256: file.sha256,
                restored: false,
                externalContent: undefined,
                externalSHA256: undefined,
              },
        );
        continue;
      }

      if (!isMissingWorkspaceFile(result.error)) {
        onError(result.error);
        continue;
      }
      bumpDocumentRevision(currentDocument.path);
      if (currentDocument.content !== currentDocument.savedContent) {
        updates.set(currentDocument.path, {
          ...currentDocument,
          externalContent: "",
          externalSHA256: "",
        });
      } else {
        missingPaths.add(currentDocument.path);
      }
    }

    if (updates.size) {
      const next = documentsRef.current.map(
        (document) => updates.get(document.path) ?? document,
      );
      documentsRef.current = next;
      setDocuments(next);
    }

    try {
      await onReload();
    } catch (error) {
      if (isCurrentRequest()) onError(error);
      return;
    }
    if (!isCurrentRequest() || missingPaths.size === 0) return;

    const removablePaths = new Set(
      documentsRef.current
        .filter(
          (document) =>
            missingPaths.has(document.path) &&
            readVersions.get(document.path) ===
              externalReadVersion.current[document.path] &&
            document.content === document.savedContent,
        )
        .map((document) => document.path),
    );
    if (removablePaths.size === 0) return;

    for (const path of removablePaths) {
      clearFormatTimer(path);
      delete documentRevisions.current[path];
      delete diagnosticsByPath.current[path];
      externalReadVersion.current[path] =
        (externalReadVersion.current[path] ?? 0) + 1;
    }
    const next = documentsRef.current.filter(
      (document) => !removablePaths.has(document.path),
    );
    documentsRef.current = next;
    setDocuments(next);

    const activeTabAtRemoval = activeTabRef.current;
    if (
      activeTabAtRemoval?.kind === "file" &&
      removablePaths.has(activeTabAtRemoval.path)
    ) {
      const replacement = next[0];
      const nextActiveTab = replacement
        ? { kind: "file" as const, path: replacement.path }
        : null;
      activeTabRef.current = nextActiveTab;
      setActiveTab(nextActiveTab);
      setActivePath(replacement?.path ?? "");
      setTreeSelection(
        replacement ? { path: replacement.path, kind: "file" } : null,
      );
    }
  };

  useEffect(() => {
    if (!workspace) return;
    let active = true;
    const stop = Events.On("agent:event", (event) => {
      if (staleRef.current) return;
      const activity = event.data as AgentActivity & { workspaceId?: string };
      if (activity.workspaceId && activity.workspaceId !== workspace.id) return;
      if (
        activity.type !== "host_tool_end" ||
        !["workspace_write", "workspace_replace"].includes(
          activity.toolName ?? "",
        )
      )
        return;

      const rawPath = activity.data?.path;
      if (typeof rawPath !== "string") return;
      const normalizedPath = rawPath.replace(/\\/g, "/").replace(/^\.\/+/, "");
      const openDocument = documentsRef.current.find(
        (document) =>
          document.path.toLowerCase() === normalizedPath.toLowerCase(),
      );
      if (!openDocument) return;
      const requestVersion =
        (externalReadVersion.current[openDocument.path] ?? 0) + 1;
      externalReadVersion.current[openDocument.path] = requestVersion;

      void API.ReadWorkspaceFile(workspace.id, openDocument.path)
        .then((file) => {
          if (
            !active ||
            externalReadVersion.current[openDocument.path] !== requestVersion
          )
            return;
          const currentDocument = documentsRef.current.find(
            (document) => document.path === openDocument.path,
          );
          if (!currentDocument) return;
          const hadUnsavedChanges =
            currentDocument.content !== currentDocument.savedContent;
          const conflicts =
            hadUnsavedChanges &&
            file.sha256 !== currentDocument.savedSHA256 &&
            file.content !== currentDocument.content;
          if (
            file.sha256 === currentDocument.savedSHA256 &&
            currentDocument.externalContent === undefined
          )
            return;

          bumpDocumentRevision(openDocument.path);
          setDocuments((current) => {
            const next = current.map((document) => {
              if (document.path !== openDocument.path) return document;
              if (
                document.content === file.content ||
                document.content === document.savedContent
              ) {
                return {
                  ...document,
                  content: file.content,
                  savedContent: file.content,
                  savedSHA256: file.sha256,
                  restored: false,
                  externalContent: undefined,
                  externalSHA256: undefined,
                };
              }
              if (file.sha256 === document.savedSHA256) {
                return {
                  ...document,
                  externalContent: undefined,
                  externalSHA256: undefined,
                };
              }
              return {
                ...document,
                externalContent: file.content,
                externalSHA256: file.sha256,
              };
            });
            documentsRef.current = next;
            return next;
          });
          onNotify(
            conflicts
              ? `Virgil changed ${openDocument.path} on disk; your unsaved edit was preserved`
              : `Reloaded ${openDocument.path} after Virgil's change`,
            "info",
          );
        })
        .catch(onError);
    });
    return () => {
      active = false;
      stop();
    };
  }, [workspace?.id, onNotify, onError]);
  useEffect(() => {
    if (!workspace) return;
    let refreshTimer: number | undefined;
    const stop = Events.On("agent:event", (event) => {
      const activity = event.data as AgentActivity & { workspaceId?: string };
      if (
        (activity.workspaceId && activity.workspaceId !== workspace.id) ||
        activity.type !== "turn_end"
      )
        return;
      if (staleRef.current) return;
      if (refreshTimer !== undefined) return;
      refreshTimer = window.setTimeout(() => {
        refreshTimer = undefined;
        if (staleRef.current) return;
        void onReload().catch(onError);
      }, 180);
    });
    return () => {
      stop();
      if (refreshTimer !== undefined) {
        window.clearTimeout(refreshTimer);
        refreshTimer = undefined;
      }
    };
  }, [workspace?.id, onReload, onError]);

  useEffect(() => {
    if (draftTimer.current !== undefined)
      window.clearTimeout(draftTimer.current);
    if (mutationBlocked() || !workspace || documents.length === 0) return;

    const snapshot = documents
      .filter((document) => !document.untitled)
      .map((document) => ({ ...document }));
    draftTimer.current = window.setTimeout(() => {
      if (mutationBlocked()) return;
      void Promise.all(
        snapshot.map((document) => {
          if (mutationBlocked()) return Promise.resolve();
          return document.content === document.savedContent
            ? API.DeleteWorkspaceDraft(workspace.id, document.path)
            : API.SaveWorkspaceDraft(
                workspace.id,
                document.path,
                document.content,
                document.savedSHA256,
              );
        }),
      ).catch(onError);
    }, 450);

    return () => {
      if (draftTimer.current !== undefined)
        window.clearTimeout(draftTimer.current);
    };
  }, [documents, stale, writeBlocked, workspace?.id]);

  const selectFile = async (
    path: string,
    switchToEditor = true,
    allowSearchText = false,
  ) => {
    if (!workspace) return;
    if (switchToEditor) {
      editorInteractionVersion.current += 1;
      cancelFormatTasks();
    }
    setTreeSelection({ path, kind: "file" });
    if (switchToEditor) setUtility(null);
    if (switchToEditor && !isEditableSource(path) && !allowSearchText) {
      fileOpenVersion.current += 1;
      setFileLoadingPath("");
      setActivePath("");
      setActiveTab(null);
      return;
    }
    if (!isEditableSource(path) && !allowSearchText) return;

    const requestVersion = ++fileOpenVersion.current;
    const workspaceID = workspace.id;
    const existing = documents.find((document) => document.path === path);
    if (existing) {
      documentRevisions.current[path] = Math.max(
        0,
        Math.trunc(documentRevisions.current[path] ?? 0),
      );
      setFileLoadingPath("");
      setActivePath(path);
      setActiveTab({ kind: "file", path });
      return;
    }

    if (switchToEditor) {
      setActivePath(path);
      setActiveTab({ kind: "file", path });
    }
    setFileLoadingPath(path);
    try {
      const file = await API.ReadWorkspaceFile(workspaceID, path);
      if (requestVersion !== fileOpenVersion.current) return;
      const draft = detail?.drafts?.find((item) => item.path === path);
      const document = documentFromFile(file, draft);
      documentRevisions.current[file.path] = Math.max(
        0,
        Math.trunc(documentRevisions.current[file.path] ?? 0),
      );
      setDocuments((current) =>
        current.some((item) => item.path === file.path)
          ? current
          : [...current, document],
      );
      setActivePath((current) => (current === path ? file.path : current));
      setActiveTab((current) =>
        current?.kind === "file" && current.path === path
          ? { kind: "file", path: file.path }
          : current,
      );
    } catch (error) {
      if (requestVersion === fileOpenVersion.current) onError(error);
    } finally {
      if (requestVersion === fileOpenVersion.current) setFileLoadingPath("");
    }
  };

  const runAutoFormat = async (snapshot: {
    workspaceID: string;
    path: string;
    content: string;
    revision: number;
    generation: number;
  }) => {
    if (
      formatGeneration.current !== snapshot.generation ||
      activeWorkspaceIDRef.current !== snapshot.workspaceID ||
      activeEditorPathRef.current !== snapshot.path
    )
      return;
    const current = documentsRef.current.find(
      (document) => document.path === snapshot.path,
    );
    if (
      !current ||
      current.content !== snapshot.content ||
      documentRevisions.current[snapshot.path] !== snapshot.revision
    )
      return;
    let formatted: string;
    try {
      formatted = await formatSource(snapshot.path, snapshot.content);
    } catch {
      return;
    }
    if (
      formatGeneration.current !== snapshot.generation ||
      activeWorkspaceIDRef.current !== snapshot.workspaceID ||
      activeEditorPathRef.current !== snapshot.path
    )
      return;
    const latest = documentsRef.current.find(
      (document) => document.path === snapshot.path,
    );
    if (
      !latest ||
      latest.content !== snapshot.content ||
      documentRevisions.current[snapshot.path] !== snapshot.revision
    )
      return;
    if (formatted === snapshot.content) return;
    bumpDocumentRevision(snapshot.path);
    const next = documentsRef.current.map((document) =>
      document.path === snapshot.path
        ? { ...document, content: formatted, restored: false }
        : document,
    );
    documentsRef.current = next;
    setDocuments(next);
  };

  const scheduleAutoFormat = (snapshot: {
    workspaceID: string;
    path: string;
    content: string;
    revision: number;
  }) => {
    clearFormatTimer(snapshot.path);
    if (!canFormatSource(snapshot.path)) return;
    const generation = formatGeneration.current;
    const timer = window.setTimeout(() => {
      formatTimers.current.delete(snapshot.path);
      void runAutoFormat({ ...snapshot, generation });
    }, clampAutoFormatDelay(autoFormatDelayMs));
    formatTimers.current.set(snapshot.path, timer);
  };
  const formatDocument = async () => {
    if (mutationBlocked() || !workspace || !activeDocument || busy !== "")
      return;
    const workspaceID = workspace.id;
    const path = activeDocument.path;
    if (!canFormatSource(path)) return;
    const source = activeDocument.content;
    const requestVersion = ++formatRequestVersion.current;
    clearFormatTimer(path);
    formatGeneration.current += 1;
    setBusy("format");
    try {
      const formatted = await formatSource(path, source);
      if (
        requestVersion !== formatRequestVersion.current ||
        activeWorkspaceIDRef.current !== workspaceID ||
        activeEditorPathRef.current !== path ||
        mutationBlocked()
      ) {
        return;
      }
      const latest = documentsRef.current.find(
        (document) => document.path === path,
      );
      if (!latest || latest.content !== source) return;
      if (formatted !== source) {
        bumpDocumentRevision(path);
        const next = documentsRef.current.map((document) =>
          document.path === path
            ? { ...document, content: formatted, restored: false }
            : document,
        );
        documentsRef.current = next;
        setDocuments(next);
      }
      showSourceSaveToast(
        formatted === source
          ? `${path}: Already formatted`
          : `${path}: Formatted`,
        "success",
      );
    } catch (error) {
      if (
        requestVersion !== formatRequestVersion.current ||
        activeWorkspaceIDRef.current !== workspaceID ||
        activeEditorPathRef.current !== path
      ) {
        return;
      }
      const message = error instanceof Error ? error.message : String(error);
      showSourceSaveToast(`${path}: Formatting failed — ${message}`, "error");
    } finally {
      if (requestVersion === formatRequestVersion.current) setBusy("");
    }
  };

  const updateDocument = (content: string) => {
    const path = activeEditorPathRef.current || activePath;
    if (!workspace || !path) return;
    const current = documentsRef.current.find(
      (document) => document.path === path,
    );
    if (!current) return;
    const revision = bumpDocumentRevision(path);
    const next = documentsRef.current.map((document) =>
      document.path === path
        ? { ...document, content, restored: false }
        : document,
    );
    documentsRef.current = next;
    setDocuments(next);
    scheduleAutoFormat({ workspaceID: workspace.id, path, content, revision });
  };

  const saveFile = async (editorSnapshot?: CodeEditorSaveSnapshot) => {
    if (mutationBlocked() || !workspace) return;
    const workspaceID = workspace.id;
    const targetPath = activeEditorPathRef.current;
    const targetDocument = documentsRef.current.find(
      (document) => document.path === targetPath,
    );
    if (
      !targetPath ||
      !targetDocument ||
      savesInFlight.current.has(workspaceID)
    )
      return;

    let writePath = targetPath;
    if (targetDocument.untitled) {
      const requestedPath = (
        await requestText({
          title: "Save new file",
          label: "Workspace-relative path",
          initialValue: `${editorDocumentLabel(targetDocument)}.txt`,
          confirmLabel: "Save",
        })
      )?.trim();
      if (!requestedPath) return;
      writePath = requestedPath.replace(/\\/g, "/").replace(/^\.\/+/, "");
      if (!writePath) return;
      if (
        documentsRef.current.some(
          (document) =>
            document.path !== targetPath &&
            !document.untitled &&
            document.path.toLowerCase() === writePath.toLowerCase(),
        )
      ) {
        onError(new Error(`${writePath} is already open in another tab`));
        return;
      }
      await yieldToQueuedWork();
      if (
        mutationBlocked() ||
        activeWorkspaceIDRef.current !== workspaceID ||
        !documentsRef.current.some((document) => document.path === targetPath)
      )
        return;
    }

    let attemptPath = "";
    let attemptWasUntitled = false;
    savesInFlight.current.add(workspaceID);
    setBusy("save");
    try {
      let requestedSnapshot = editorSnapshot;
      for (;;) {
        const document = documentsRef.current.find(
          (item) => item.path === targetPath,
        );
        if (
          !document ||
          activeWorkspaceIDRef.current !== workspaceID ||
          (!document.untitled && document.content === document.savedContent)
        )
          return;
        clearFormatTimer(targetPath);
        formatGeneration.current += 1;
        const source =
          requestedSnapshot?.value === document.content
            ? requestedSnapshot.value
            : document.content;
        const revision = documentRevisions.current[targetPath] ?? 0;
        const diagnostics =
          requestedSnapshot && requestedSnapshot.value === source
            ? requestedSnapshot.diagnostics
            : (diagnosticsByPath.current[targetPath] ?? []);
        requestedSnapshot = undefined;
        let contentToWrite = source;
        let formatError: unknown;
        if (canFormatSource(writePath)) {
          try {
            contentToWrite = await formatSource(writePath, source);
          } catch (error) {
            formatError = error;
          }
        }
        const latest = documentsRef.current.find(
          (item) => item.path === targetPath,
        );
        if (activeWorkspaceIDRef.current !== workspaceID) return;
        if (
          !latest ||
          latest.content !== source ||
          documentRevisions.current[targetPath] !== revision
        )
          continue;
        const expectedSHA256 = latest.untitled
          ? ""
          : latest.externalContent !== undefined
            ? (latest.externalSHA256 ?? latest.savedSHA256)
            : latest.savedSHA256;
        if (mutationBlocked()) return;
        if (latest.externalContent !== undefined) {
          const overwrite = await confirmAction({
            title: "File changed on disk",
            message: `${writePath} was changed outside ModMaker since you opened it. Saving replaces that version with yours.`,
            confirmLabel: "Overwrite with my version",
            cancelLabel: "Don't save",
          });
          if (!overwrite) return;
          await yieldToQueuedWork();
          if (mutationBlocked()) return;
        }
        if (mutationBlocked()) return;
        attemptPath = writePath;
        attemptWasUntitled = Boolean(latest.untitled);
        await API.WriteWorkspaceFile(
          workspaceID,
          writePath,
          contentToWrite,
          expectedSHA256,
        );
        if (mutationBlocked()) return;
        const savedSHA256 = await sha256Text(contentToWrite);
        const saveSeverity = severityFromSave(
          Boolean(formatError),
          diagnostics,
        );
        const validationIssue = validationSaveIssue(
          writePath,
          source,
          diagnostics,
        );
        externalReadVersion.current[writePath] =
          (externalReadVersion.current[writePath] ?? 0) + 1;
        const currentAfter = documentsRef.current.find(
          (item) => item.path === targetPath,
        );
        const sameSnapshot =
          activeWorkspaceIDRef.current === workspaceID &&
          currentAfter?.content === source &&
          documentRevisions.current[targetPath] === revision;
        const wasActive = activeEditorPathRef.current === targetPath;
        const next = documentsRef.current.map((item) =>
          item.path === targetPath
            ? {
                ...item,
                path: writePath,
                name: undefined,
                untitled: false,
                ...(sameSnapshot ? { content: contentToWrite } : {}),
                savedContent: contentToWrite,
                savedSHA256,
                restored: false,
                externalContent: undefined,
                externalSHA256: undefined,
              }
            : item,
        );
        if (latest.untitled) {
          clearFormatTimer(targetPath);
          formatGeneration.current += 1;
          documentRevisions.current[writePath] =
            documentRevisions.current[targetPath] ?? revision;
          delete documentRevisions.current[targetPath];
          if (diagnosticsByPath.current[targetPath]) {
            diagnosticsByPath.current[writePath] =
              diagnosticsByPath.current[targetPath];
          }
          delete diagnosticsByPath.current[targetPath];
          delete externalReadVersion.current[targetPath];
        }
        documentsRef.current = next;
        setDocuments(next);
        if (activeWorkspaceIDRef.current === workspaceID) {
          setFileSeverity((current) => {
            const nextSeverity = { ...current };
            if (targetPath !== writePath) delete nextSeverity[targetPath];
            if (saveSeverity) nextSeverity[writePath] = saveSeverity;
            else delete nextSeverity[writePath];
            return nextSeverity;
          });
          if (latest.untitled) {
            setActivePath((current) =>
              current === targetPath ? writePath : current,
            );
            if (wasActive) {
              const nextActive = {
                kind: "file" as const,
                path: writePath,
              };
              activeEditorPathRef.current = writePath;
              activeTabRef.current = nextActive;
              setActiveTab(nextActive);
              setTreeSelection({ path: writePath, kind: "file" });
            }
          }
          if (sameSnapshot && wasActive) {
            const message = formatError
              ? formatSaveIssue(writePath, formatError)
              : validationIssue
                ? validationIssue.message
                : `Saved ${writePath}`;
            const tone = formatError
              ? "error"
              : (validationIssue?.severity ?? "success");
            showSourceSaveToast(message, tone);
          }
        }
        if (mutationBlocked()) return;
        if (activeWorkspaceIDRef.current === workspaceID) await onReload();
        return;
      }
    } catch (error) {
      if (attemptPath && !attemptWasUntitled) {
        try {
          const file = await API.ReadWorkspaceFile(workspaceID, attemptPath);
          if (activeWorkspaceIDRef.current === workspaceID) {
            const next = documentsRef.current.map((item) =>
              item.path === attemptPath && item.savedSHA256 !== file.sha256
                ? {
                    ...item,
                    externalContent: file.content,
                    externalSHA256: file.sha256,
                  }
                : item,
            );
            documentsRef.current = next;
            setDocuments(next);
          }
        } catch {
          // The original write error is the actionable failure.
        }
      }
      onError(error);
    } finally {
      savesInFlight.current.delete(workspaceID);
      if (activeWorkspaceIDRef.current === workspaceID) setBusy("");
    }
  };

  const reloadExternalChange = async () => {
    if (!activeDocument || activeDocument.externalContent === undefined) return;
    const path = activeDocument.path;
    if (
      activeDocument.content !== activeDocument.savedContent &&
      !(await confirmAction({
        title: "Load the version on disk?",
        message: `Your unsaved changes to ${path} will be discarded.`,
        confirmLabel: "Discard and load",
        cancelLabel: "Keep my changes",
      }))
    )
      return;
    const current = documentsRef.current.find((document) => document.path === path);
    if (!current || current.externalContent === undefined) return;
    const externalContent = current.externalContent;
    const externalSHA256 = current.externalSHA256 ?? current.savedSHA256;
    cancelFormatTasks();
    bumpDocumentRevision(path);
    setDocuments((current) =>
      current.map((document) =>
        document.path === path
          ? {
              ...document,
              content: externalContent,
              savedContent: externalContent,
              savedSHA256: externalSHA256,
              restored: false,
              externalContent: undefined,
              externalSHA256: undefined,
            }
          : document,
      ),
    );
    onNotify(`Loaded the external version of ${path}`, "info");
  };

  const closeDocument = async (path: string) => {
    const pending = documentsRef.current.find((item) => item.path === path);
    if (!pending) return;
    if (
      pending.untitled &&
      pending.content.length > 0 &&
      !(await confirmAction({
        title: `Discard ${editorDocumentLabel(pending)}?`,
        message: "This file has never been saved.",
        confirmLabel: "Discard file",
      }))
    )
      return;
    const document = documentsRef.current.find((item) => item.path === path);
    if (!document) return;

    clearFormatTimer(path);
    formatGeneration.current += 1;
    delete documentRevisions.current[path];
    delete diagnosticsByPath.current[path];
    setFileSeverity((current) => {
      if (!(path in current)) return current;
      const next = { ...current };
      delete next[path];
      return next;
    });
    externalReadVersion.current[path] =
      (externalReadVersion.current[path] ?? 0) + 1;
    if (!document.untitled && !mutationBlocked() && workspace) {
      const draftRequest =
        document.content === document.savedContent
          ? API.DeleteWorkspaceDraft(workspace.id, path)
          : API.SaveWorkspaceDraft(
              workspace.id,
              path,
              document.content,
              document.savedSHA256,
            );
      void draftRequest.catch(onError);
    }

    const remaining = documentsRef.current.filter((item) => item.path !== path);
    const nextDocument = remaining[remaining.length - 1];
    documentsRef.current = remaining;
    setDocuments(remaining);
    if (activePath === path) {
      setActivePath(nextDocument?.path ?? "");
    }
    const currentActive = activeTabRef.current;
    if (currentActive?.kind === "file" && currentActive.path === path) {
      if (nextDocument) {
        const nextActive = {
          kind: "file" as const,
          path: nextDocument.path,
        };
        activeEditorPathRef.current = nextDocument.path;
        activeTabRef.current = nextActive;
        setActivePath(nextDocument.path);
        setActiveTab(nextActive);
        setTreeSelection(
          nextDocument.untitled
            ? null
            : { path: nextDocument.path, kind: "file" },
        );
      } else {
        const nextSession =
          sessionTabsRef.current[sessionTabsRef.current.length - 1];
        const nextActive = nextSession
          ? { kind: "session" as const, id: nextSession.record.id }
          : null;
        activeEditorPathRef.current = "";
        activeTabRef.current = nextActive;
        setActiveTab(nextActive);
        setActivePath("");
        setTreeSelection(null);
      }
    } else if (treeSelection?.path === path) {
      setTreeSelection(null);
    }
  };

  const parentDirectory = () => {
    if (!treeSelection) return "";
    return treeSelection.path.includes("/")
      ? treeSelection.path.slice(0, treeSelection.path.lastIndexOf("/"))
      : "";
  };

  const openNewDocument = () => {
    if (mutationBlocked() || !workspace || busy !== "") return;
    editorInteractionVersion.current += 1;
    cancelFormatTasks();
    fileOpenVersion.current += 1;
    const sequence = ++untitledSequence.current;
    const path = `untitled:${sequence}`;
    const document: EditorDocument = {
      path,
      name: `Untitled-${sequence}`,
      untitled: true,
      content: "",
      savedContent: "",
      savedSHA256: "",
      restored: false,
    };
    const next = [...documentsRef.current, document];
    documentsRef.current = next;
    documentRevisions.current[path] = 0;
    setDocuments(next);
    const nextActive = { kind: "file" as const, path };
    activeEditorPathRef.current = path;
    activeTabRef.current = nextActive;
    setActivePath(path);
    setActiveTab(nextActive);
    setTreeSelection(null);
    setUtility(null);
    setSessionMenu(null);
    setWorkspaceSearchOpen(false);
    setFileSearchOpen(false);
    setPendingEditorReveal(null);
    setEditorSelection({
      from: 0,
      to: 0,
      requestId: ++editorSelectionRequest.current,
    });
  };

  const createFile = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    const base = parentDirectory();
    const suggested = base ? `${base}/new-file.lua` : "new-file.lua";
    const path = (
      await requestText({
        title: "New file",
        label: "Workspace-relative path",
        initialValue: suggested,
        confirmLabel: "Create file",
      })
    )?.trim();
    if (!path) return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;

    try {
      if (mutationBlocked()) return;
      await API.WriteWorkspaceFile(workspace.id, path, "", "");
      if (mutationBlocked()) return;
      await onReload();
      await selectFile(path);
      onNotify(`Created ${path}`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const createDirectory = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    const base = parentDirectory();
    const path = (
      await requestText({
        title: "New folder",
        label: "Workspace-relative path",
        initialValue: base ? `${base}/new-folder` : "new-folder",
        confirmLabel: "Create folder",
      })
    )?.trim();
    if (!path) return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;

    try {
      if (mutationBlocked()) return;
      await API.CreateWorkspaceDirectory(workspace.id, path);
      if (mutationBlocked()) return;
      await onReload();
      onNotify(`Created ${path}`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const movePath = async (oldPath: string, newPath: string) => {
    if (mutationBlocked()) return;
    if (!workspace || !newPath || oldPath === newPath) return;
    cancelFormatTasks();

    try {
      if (mutationBlocked()) return;
      await API.RenameWorkspacePath(workspace.id, oldPath, newPath);
      if (mutationBlocked()) return;
      const migrate = (path: string) =>
        path === oldPath
          ? newPath
          : path.startsWith(`${oldPath}/`)
            ? `${newPath}${path.slice(oldPath.length)}`
            : path;
      const migratedRevisions: Record<string, number> = {};
      for (const [path, revision] of Object.entries(documentRevisions.current))
        migratedRevisions[migrate(path)] = revision;
      documentRevisions.current = migratedRevisions;
      const migratedDiagnostics: Record<string, CodeEditorDiagnostic[]> = {};
      for (const [path, diagnostics] of Object.entries(
        diagnosticsByPath.current,
      ))
        migratedDiagnostics[migrate(path)] = diagnostics;
      diagnosticsByPath.current = migratedDiagnostics;
      setFileSeverity((current) => {
        const next: Record<string, TreeSeverity> = {};
        for (const [path, severity] of Object.entries(current))
          next[migrate(path)] = severity;
        return next;
      });
      setDocuments((current) =>
        current.map((document) => ({
          ...document,
          path: migrate(document.path),
        })),
      );
      setActivePath((current) => migrate(current));
      setActiveTab((current) =>
        current?.kind === "file"
          ? { kind: "file", path: migrate(current.path) }
          : current,
      );
      setTreeSelection((current) =>
        current ? { ...current, path: migrate(current.path) } : null,
      );
      await onReload();
      onNotify(`Moved to ${newPath}`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const renamePath = async (selection: TreeSelection) => {
    if (mutationBlocked()) return;
    const nextPath = (
      await requestText({
        title: `Rename ${selection.kind}`,
        label: "Workspace-relative path",
        initialValue: selection.path,
        confirmLabel: "Rename",
      })
    )?.trim();
    if (!nextPath) return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;
    await movePath(selection.path, nextPath);
  };

  const deletePath = async (selection: TreeSelection) => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    const scope =
      selection.kind === "directory" ? " and every path inside it" : "";
    if (
      !(await confirmAction({
        title: `Delete ${selection.path}?`,
        message: `It will be removed from this workspace${scope}.`,
        confirmLabel: selection.kind === "directory" ? "Delete folder" : "Delete file",
        icon: "trash",
      }))
    )
      return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;

    const deleted = selection.path;
    cancelFormatTasks();
    try {
      if (mutationBlocked()) return;
      await API.DeleteWorkspacePath(workspace.id, deleted);
      if (mutationBlocked()) return;
      for (const path of Object.keys(documentRevisions.current)) {
        if (path === deleted || path.startsWith(`${deleted}/`))
          delete documentRevisions.current[path];
      }
      for (const path of Object.keys(diagnosticsByPath.current)) {
        if (path === deleted || path.startsWith(`${deleted}/`))
          delete diagnosticsByPath.current[path];
      }
      setFileSeverity((current) => {
        const next = { ...current };
        for (const path of Object.keys(next)) {
          if (path === deleted || path.startsWith(`${deleted}/`))
            delete next[path];
        }
        return next;
      });
      const remaining = documents.filter(
        (document) =>
          document.path !== deleted && !document.path.startsWith(`${deleted}/`),
      );
      documentsRef.current = remaining;
      setDocuments(remaining);
      if (
        activeTab?.kind === "file" &&
        (activeTab.path === deleted || activeTab.path.startsWith(`${deleted}/`))
      ) {
        const nextDocument = remaining[remaining.length - 1];
        if (nextDocument) {
          const nextActive = {
            kind: "file" as const,
            path: nextDocument.path,
          };
          activeEditorPathRef.current = nextDocument.path;
          activeTabRef.current = nextActive;
          setActivePath(nextDocument.path);
          setActiveTab(nextActive);
          setTreeSelection(
            nextDocument.untitled
              ? null
              : { path: nextDocument.path, kind: "file" },
          );
        } else {
          const nextSession = sessionTabs[sessionTabs.length - 1];
          setActivePath("");
          setActiveTab(
            nextSession ? { kind: "session", id: nextSession.record.id } : null,
          );
          setTreeSelection(null);
        }
      } else if (
        treeSelection?.path === deleted ||
        treeSelection?.path.startsWith(`${deleted}/`)
      )
        setTreeSelection(null);
      await onReload();
      onNotify("Workspace path deleted", "success");
    } catch (error) {
      onError(error);
    }
  };

  const revealPath = (selection: TreeSelection) => {
    if (staleRef.current) return;
    if (!workspace) return;
    void API.RevealWorkspacePath(workspace.id, selection.path).catch(onError);
  };

  const rememberSearchFocus = () => {
    const focused = document.activeElement;
    searchReturnFocus.current = focused instanceof HTMLElement ? focused : null;
  };

  const restoreSearchFocus = () => {
    const target = searchReturnFocus.current;
    searchReturnFocus.current = null;
    if (!target) return;
    window.setTimeout(() => {
      if (target.isConnected) target.focus();
    }, 0);
  };

  const closeWorkspaceSearch = () => {
    workspaceSearchRequestVersion.current += 1;
    setWorkspaceSearchLoading(false);
    setWorkspaceSearchOpen(false);
    setWorkspaceSearchResults([]);
    setWorkspaceSearchError("");
    restoreSearchFocus();
  };

  const closeFileSearch = () => {
    setFileSearchOpen(false);
    setFileSearchQuery("");
    setFileSearchMatchIndex(0);
    setEditorSelection(null);
    restoreSearchFocus();
  };

  const openWorkspaceSearch = () => {
    if (!workspace) return;
    rememberSearchFocus();
    workspaceSearchRequestVersion.current += 1;
    setWorkspaceSearchLoading(false);
    setFileSearchOpen(false);
    setEditorSelection(null);
    setWorkspaceSearchOpen(true);
    setWorkspaceSearchError("");
  };

  const openFileSearch = () => {
    if (!activeDocument) return;
    rememberSearchFocus();
    workspaceSearchRequestVersion.current += 1;
    setWorkspaceSearchLoading(false);
    setWorkspaceSearchOpen(false);
    setFileSearchOpen(true);
    setFileSearchMatchIndex(0);
    setEditorSelection(null);
  };

  const runWorkspaceSearch = async () => {
    if (!workspace) return;
    const query = workspaceSearchQuery.trim();
    if (!query) {
      workspaceSearchRequestVersion.current += 1;
      setWorkspaceSearchLoading(false);
      setWorkspaceSearchResults([]);
      setWorkspaceSearchError("");
      return;
    }
    if (workspaceSearchRegex) {
      try {
        new RegExp(query, workspaceSearchCaseSensitive ? "g" : "gi");
      } catch (error) {
        workspaceSearchRequestVersion.current += 1;
        setWorkspaceSearchLoading(false);
        setWorkspaceSearchResults([]);
        setWorkspaceSearchError(searchErrorMessage(error));
        return;
      }
    }

    const requestVersion = ++workspaceSearchRequestVersion.current;
    const workspaceID = workspace.id;
    setWorkspaceSearchLoading(true);
    setWorkspaceSearchError("");
    try {
      const matches =
        (await API.SearchWorkspace(
          workspaceID,
          {
            query,
            regex: workspaceSearchRegex,
            caseSensitive: workspaceSearchCaseSensitive,
          },
          300,
        )) ?? [];
      if (
        requestVersion !== workspaceSearchRequestVersion.current ||
        activeWorkspaceIDRef.current !== workspaceID
      )
        return;
      setWorkspaceSearchResults(matches);
    } catch (error) {
      if (requestVersion !== workspaceSearchRequestVersion.current) return;
      setWorkspaceSearchResults([]);
      setWorkspaceSearchError(searchErrorMessage(error));
    } finally {
      if (requestVersion === workspaceSearchRequestVersion.current) {
        setWorkspaceSearchLoading(false);
      }
    }
  };

  const moveFileSearch = (direction: number) => {
    if (!fileSearchMatches.length) return;
    setFileSearchMatchIndex(
      (current) =>
        (current + direction + fileSearchMatches.length) %
        fileSearchMatches.length,
    );
  };

  const openWorkspaceMatch = (match: WorkspaceSearchMatch) => {
    if (!workspace) return;
    searchReturnFocus.current = null;
    setWorkspaceSearchOpen(false);
    setWorkspaceSearchResults([]);
    setWorkspaceSearchError("");
    setFileSearchOpen(false);
    setPendingEditorReveal({
      path: match.relativePath,
      line: match.line,
      column: match.column,
      matchLength: match.matchLength,
      query: workspaceSearchQuery.trim(),
      regex: workspaceSearchRegex,
      caseSensitive: workspaceSearchCaseSensitive,
    });
    void selectFile(match.relativePath, true, true);
  };

  useEffect(() => {
    if (!workspaceSearchOpen) return;
    const timer = window.setTimeout(() => {
      workspaceSearchInputRef.current?.focus();
      workspaceSearchInputRef.current?.select();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [workspaceSearchOpen]);

  useEffect(() => {
    if (!fileSearchOpen) return;
    const timer = window.setTimeout(() => {
      fileSearchInputRef.current?.focus();
      fileSearchInputRef.current?.select();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [fileSearchOpen]);

  useEffect(() => {
    if (!workspace) return;
    const handleShortcut = (event: KeyboardEvent) => {
      if (workspace.id !== selectedID) return;
      const target = event.target instanceof Element ? event.target : null;
      const inEditorScope = Boolean(
        target?.closest(
          ".ide-workspace, .workspace-search-overlay, .editor-find-panel",
        ),
      );
      if (!inEditorScope) return;
      const key = event.key.toLowerCase();
      if (!(event.ctrlKey || event.metaKey) || event.altKey || key !== "f")
        return;
      const isCodeMirror = Boolean(target?.closest(".cm-editor"));
      const isTextInput =
        target instanceof HTMLInputElement ||
        target instanceof HTMLTextAreaElement ||
        target instanceof HTMLSelectElement ||
        Boolean(target instanceof HTMLElement && target.isContentEditable);
      const isSearchSurface = Boolean(
        target?.closest(".workspace-search-dialog, .editor-find-panel"),
      );
      if (isTextInput && !isCodeMirror && !isSearchSurface) return;
      if (event.shiftKey) {
        event.preventDefault();
        event.stopPropagation();
        openWorkspaceSearch();
        return;
      }
      if (!activeDocument) return;
      event.preventDefault();
      event.stopPropagation();
      openFileSearch();
    };
    window.addEventListener("keydown", handleShortcut, true);
    return () => window.removeEventListener("keydown", handleShortcut, true);
  }, [workspace?.id, selectedID, activeDocument?.path]);

  useEffect(() => {
    if (!pendingEditorReveal || !activeDocument) return;
    if (
      pendingEditorReveal.path.toLowerCase() !==
      activeDocument.path.toLowerCase()
    )
      return;
    const range = revealRangeForSearchResult(
      activeDocument.content,
      pendingEditorReveal,
    );
    setEditorSelection({
      ...range,
      requestId: ++editorSelectionRequest.current,
    });
    setPendingEditorReveal(null);
  }, [activeDocument?.content, activeDocument?.path, pendingEditorReveal]);
  useEffect(() => {
    if (!fileSearchOpen) return;
    if (!activeFileSearchMatch) {
      setEditorSelection(null);
      return;
    }
    setEditorSelection({
      from: activeFileSearchMatch.from,
      to: activeFileSearchMatch.to,
      requestId: ++editorSelectionRequest.current,
    });
  }, [activeFileSearchMatch?.from, activeFileSearchMatch?.to, fileSearchOpen]);

  useEffect(() => {
    setEditorSelection(null);
    setFileSearchOpen(false);
    setFileSearchQuery("");
    setFileSearchMatchIndex(0);
  }, [activePath]);

  useEffect(() => {
    setFileSearchMatchIndex((current) =>
      fileSearchMatches.length
        ? Math.min(current, fileSearchMatches.length - 1)
        : 0,
    );
  }, [fileSearchMatches.length]);

  useEffect(
    () => () => {
      workspaceSearchRequestVersion.current += 1;
    },
    [],
  );

  const validate = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    setBusy("validate");
    try {
      if (mutationBlocked()) return;
      const result = await API.ValidateWorkspace(workspace.id);
      await onReload();
      onNotify(
        result.valid
          ? "Workspace validation passed"
          : `Validation found ${(result.issues ?? []).length} review items`,
        result.valid ? "success" : "error",
      );
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const exportWorkspace = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    setBusy("export");
    try {
      if (mutationBlocked()) return;
      const response = await API.ExportWorkspace(workspace.id, exportLabel);
      await onReload();
      onNotify(`Exported ${response.record.path}`, "success");
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const installLatest = async () => {
    if (mutationBlocked()) return;
    const exports = detail?.exports ?? [];
    if (!workspace || exports.length === 0) return;
    setBusy("install");
    try {
      if (mutationBlocked()) return;
      const installed = await API.InstallExportForTest(
        workspace.id,
        exports[0].id,
      );
      await onReload();
      onNotify(`Test-installed ${installed.path}`, "success");
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const uninstallTest = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    setBusy("uninstall");
    try {
      if (mutationBlocked()) return;
      await API.UninstallTest(workspace.id);
      await onReload();
      onNotify("Managed test archive removed", "success");
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const launchGame = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    setBusy("launch");
    try {
      if (mutationBlocked()) return;
      const launch = await API.LaunchBeamNG(workspace.id);
      onNotify(`BeamNG launched as process ${launch.pid}`, "success");
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const analyzeRuntime = async () => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    setBusy("runtime");
    try {
      if (mutationBlocked()) return;
      setRuntime(await API.AnalyzeRuntime(workspace.id));
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const cloneSelectedVariant = async () => {
    if (mutationBlocked()) return;
    if (!workspace || !activePath.toLowerCase().endsWith(".pc")) return;
    const baseName = (
      await requestText({
        title: "New variant",
        label: "Basename (without .pc)",
        confirmLabel: "Next",
      })
    )?.trim();
    if (!baseName) return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;
    const displayName =
      (
        await requestText({
          title: "New variant",
          label: "Display name",
          initialValue: baseName,
          confirmLabel: "Create variant",
        })
      )?.trim() ?? baseName;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;

    try {
      if (mutationBlocked()) return;
      const created =
        (await API.CloneVehicleVariant(
          workspace.id,
          activePath,
          baseName,
          displayName,
        )) ?? [];
      await onReload();
      onNotify(`Created ${created.length} variant files`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const openNewSession = () => {
    if (mutationBlocked() || !workspace || preferenceBusy || busy !== "")
      return;
    editorInteractionVersion.current += 1;
    const currentIDs = new Set(
      sessionTabsRef.current.map((tab) => tab.record.id),
    );
    const sequence = ++transientSequence.current;
    const id = createSessionTabID(currentIDs);
    const tab = transientSessionTab(workspace.id, {
      id,
      title: `Virgil session ${sequence}`,
      tabOrder: sessionTabsRef.current.length,
    });
    sessionTabsWorkspace.current = workspace.id;
    setSessionTabs((current) => {
      const next = [...current, tab];
      sessionTabsRef.current = next;
      return next;
    });
    const nextActive = { kind: "session" as const, id };
    activeTabRef.current = nextActive;
    setActiveTab(nextActive);
    setSessionMenu(null);
  };

  useEffect(() => {
    if (!workspace) return;
    const handleNewShortcut = (event: KeyboardEvent) => {
      if (
        workspace.id !== selectedID ||
        event.isComposing ||
        !(event.ctrlKey || event.metaKey) ||
        event.altKey ||
        event.key.toLowerCase() !== "n"
      )
        return;
      event.preventDefault();
      event.stopPropagation();
      if (event.repeat) return;
      if (event.shiftKey) openNewSession();
      else openNewDocument();
    };
    window.addEventListener("keydown", handleNewShortcut, true);
    return () => window.removeEventListener("keydown", handleNewShortcut, true);
  }, [workspace?.id, selectedID, busy, preferenceBusy, stale, writeBlocked]);

  const updateSession = (
    record: VirgilSessionRecord,
    replaceID?: string,
  ): boolean => {
    if (mutationBlocked()) return false;
    if (replaceID && closedTransientSessions.current.has(replaceID)) {
      closedTransientSessions.current.delete(replaceID);
      if (record.workspaceId === activeWorkspaceIDRef.current) {
        setSessionTabs((current) => {
          const next = current.filter(
            (tab) => tab.record.id !== replaceID && tab.record.id !== record.id,
          );
          sessionTabsRef.current = next;
          return next;
        });
      }
      if (mutationBlocked()) return false;
      void API.ForgetVirgilSession(record.id).then(onReload).catch(onError);
      return false;
    }
    if (record.workspaceId !== activeWorkspaceIDRef.current) return false;

    setSessionTabs((current) => {
      const targetID = replaceID ?? record.id;
      const targetIndex = current.findIndex(
        (tab) => tab.record.id === targetID,
      );
      if (targetIndex < 0) {
        const existingIndex = current.findIndex(
          (tab) => tab.record.id === record.id,
        );
        const next =
          existingIndex < 0
            ? [...current, { record, transient: false }]
            : current.map((tab, index) =>
                index === existingIndex ? { record, transient: false } : tab,
              );
        sessionTabsRef.current = next;
        return next;
      }

      const next = current.filter(
        (tab) => tab.record.id !== targetID && tab.record.id !== record.id,
      );
      next.splice(Math.min(targetIndex, next.length), 0, {
        record,
        transient: false,
      });
      sessionTabsRef.current = next;
      return next;
    });
    setActiveTab((current) =>
      current?.kind === "session" &&
      ((replaceID && current.id === replaceID) || current.id === record.id)
        ? { kind: "session", id: record.id }
        : current,
    );
    return true;
  };

  const setSessionTabBusy = (id: string, busy: boolean) => {
    if (!sessionTabsRef.current.some((tab) => tab.record.id === id)) {
      if (!busy) closedTransientSessions.current.delete(id);
      return;
    }
    sessionTabsRef.current = sessionTabsRef.current.map((tab) =>
      tab.record.id === id ? { ...tab, busy } : tab,
    );
    setSessionTabs((current) => {
      const next = current.map((tab) =>
        tab.record.id === id ? { ...tab, busy } : tab,
      );
      sessionTabsRef.current = next;
      return next;
    });
    if (!busy && !sessionTabsRef.current.some((tab) => tab.record.id === id)) {
      closedTransientSessions.current.delete(id);
    }
  };

  const removeSessionTab = (id: string) => {
    editorInteractionVersion.current += 1;
    const remaining = sessionTabsRef.current.filter(
      (item) => item.record.id !== id,
    );
    sessionTabsRef.current = remaining;
    setSessionTabs((current) => {
      const next = current.filter((item) => item.record.id !== id);
      sessionTabsRef.current = next;
      return next;
    });
    setSessionPrompts((current) => {
      if (!(id in current)) return current;
      const next = { ...current };
      delete next[id];
      return next;
    });
    setSessionMenu(null);
    setActiveTab((current) => {
      if (current?.kind !== "session" || current.id !== id) return current;
      const nextSession = remaining[remaining.length - 1];
      if (nextSession) return { kind: "session", id: nextSession.record.id };
      const nextDocument =
        documentsRef.current[documentsRef.current.length - 1];
      if (nextDocument) {
        activeEditorPathRef.current = nextDocument.path;
        setActivePath(nextDocument.path);
        setTreeSelection(
          nextDocument.untitled
            ? null
            : { path: nextDocument.path, kind: "file" },
        );
        return { kind: "file", path: nextDocument.path };
      }
      return null;
    });
  };

  const closeSession = async (id: string) => {
    if (mutationBlocked()) return;
    const tab = sessionTabsRef.current.find((item) => item.record.id === id);
    if (!tab) return;
    const workspaceID = tab.record.workspaceId;
    if (
      !tab.transient &&
      !(await confirmAction({
        title: `Forget “${tab.record.title || "Virgil session"}”?`,
        message: "The session and its conversation history will be removed from this project.",
        confirmLabel: "Forget session",
        icon: "trash",
      }))
    )
      return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;
    if (tab.transient) {
      if (tab.busy) closedTransientSessions.current.add(id);
      removeSessionTab(id);
      onNotify("Virgil session closed", "info");
      return;
    }
    try {
      if (mutationBlocked()) return;
      await API.ForgetVirgilSession(id);
      if (mutationBlocked()) return;
      if (activeWorkspaceIDRef.current === workspaceID) {
        removeSessionTab(id);
        onNotify("Virgil session closed", "info");
      }
      await onReload();
    } catch (error) {
      onError(error);
    }
  };

  const renameSession = async (id: string) => {
    if (mutationBlocked()) return;
    const tab = sessionTabsRef.current.find((item) => item.record.id === id);
    if (!tab || (tab.transient && tab.busy)) return;
    const title = (
      await requestText({
        title: "Rename Virgil session",
        label: "Title",
        initialValue: tab.record.userTitle || tab.record.title || "Virgil session",
        confirmLabel: "Rename",
      })
    )?.trim();
    if (!title) return;
    await yieldToQueuedWork();
    if (mutationBlocked()) return;
    try {
      if (mutationBlocked()) return;
      if (tab.transient) {
        setSessionTabs((current) => {
          const next = current.map((item) =>
            item.record.id === id
              ? {
                  ...item,
                  seededDefault: false,
                  record: {
                    ...item.record,
                    title,
                    userTitle: title,
                    updatedAt: new Date().toISOString(),
                  },
                }
              : item,
          );
          sessionTabsRef.current = next;
          return next;
        });
      } else {
        if (mutationBlocked()) return;
        const renamed = await API.RenameVirgilSession(id, title);
        const accepted = updateSession(renamed);
        await onReload();
        if (!accepted) return;
      }
      setSessionMenu(null);
      onNotify("Virgil session renamed", "success");
    } catch (error) {
      onError(error);
    }
  };

  const configureVirgil = async (enabled: boolean, source: "choice") => {
    if (mutationBlocked()) return;
    if (!workspace) return;
    const workspaceID = workspace.id;
    const requestVersion = ++preferenceRequestVersion.current;
    setPreferenceBusy(true);
    try {
      if (mutationBlocked()) return;
      await API.ConfigureWorkspaceVirgil(workspaceID, enabled);
      if (mutationBlocked()) return;
      await onReload();
      if (
        requestVersion !== preferenceRequestVersion.current ||
        activeWorkspaceIDRef.current !== workspaceID
      )
        return;
      if (enabled) {
        const existingSession = sessionTabsRef.current[0];
        if (existingSession) {
          const nextActive = {
            kind: "session" as const,
            id: existingSession.record.id,
          };
          activeTabRef.current = nextActive;
          setActiveTab(nextActive);
        } else {
          openNewSession();
        }
      }
      const message =
        source === "choice" && !enabled
          ? "Manual editing selected for this workspace"
          : enabled
            ? "Virgil enabled for this workspace"
            : "Virgil disabled for this workspace";
      onNotify(message, "info");
    } catch (error) {
      if (
        requestVersion === preferenceRequestVersion.current &&
        activeWorkspaceIDRef.current === workspaceID
      )
        onError(error);
    } finally {
      if (requestVersion === preferenceRequestVersion.current)
        setPreferenceBusy(false);
    }
  };

  const toggleUtility = (tool: WorkspaceTool) => {
    if (sourceBusy) {
      if (utility !== "source") setUtility("source");
      onNotify(
        tool === "source"
          ? "Source Control is busy. Cancel the Git operation before closing it."
          : "Source Control is busy. Cancel the Git operation before switching utilities.",
        "info",
      );
      return;
    }
    setUtility((current) => (current === tool ? null : tool));
  };

  const visibleWorkspaces = useMemo(
    () =>
      deletedWorkspaceIDs.size
        ? workspaces.filter((ws) => !deletedWorkspaceIDs.has(ws.id))
        : workspaces,
    [workspaces, deletedWorkspaceIDs],
  );

  const handleDeleteProject = async (workspaceID: string) => {
    await API.DeleteWorkspace(workspaceID);
    setDeletedWorkspaceIDs((prev) => new Set([...prev, workspaceID]));
    if (selectedID === workspaceID) {
      onSelect("");
    }
    onNotify("Project deleted", "success");
  };


  if (!selectedID) {
    return (
      <section className="project-browser-view">
        <ProjectBrowser
          workspaces={visibleWorkspaces}
          allItems={allItems}
          onOpen={onSelect}
          onNew={() => setNewModOpen(true)}
          onDelete={handleDeleteProject}
        />
        {newModOpen && (
          <div
            className="new-mod-overlay"
            role="presentation"
            onClick={(event) => {
              if (event.target === event.currentTarget) setNewModOpen(false);
            }}
          >
            <div
              className="new-mod-dialog"
              role="dialog"
              aria-modal="true"
              aria-labelledby="new-mod-title"
            >
              <button
                className="new-mod-overlay__close"
                type="button"
                onClick={() => setNewModOpen(false)}
                aria-label="Close"
              >
                <Icon name="close" size={17} />
              </button>
              <NewModStart
                blocked={mutationBlocked()}
                defaultAuthor={defaultAuthor}
                onCreate={async (request, prompt, model) => {
                  if (mutationBlocked()) return;
                  await onCreateMod(request, prompt, model);
                  setNewModOpen(false);
                }}
                onError={onError}
              />
            </div>
          </div>
        )}
      </section>
    );
  }

  if (loading || !detail || detail.workspace.id !== selectedID) {
    return (
      <div className="center-loader center-loader--full">
        <Spinner />
        <span>Loading mod workspace</span>
      </div>
    );
  }

  const loadedDetail = detail;
  const loadedWorkspace = loadedDetail.workspace;
  const validationLabel = loadedDetail.validation.valid
    ? "Validated"
    : loadedWorkspace.lastValidation
      ? "Validation failed"
      : "Not validated";
  const contextTab = sessionMenu
    ? sessionTabs.find((tab) => tab.record.id === sessionMenu.id)
    : undefined;
  const activeItemID: string | null =
    activeTab?.kind === "file"
      ? `file:${activeTab.path}`
      : activeTab?.kind === "session"
        ? `session:${activeTab.id}`
        : null;
  const emptySurface = (
    <div className="editor-empty-message">Click a file to open it</div>
  );
  const activeSurface =
    fileLoadingPath &&
    activeTab?.kind === "file" &&
    activeTab.path === fileLoadingPath ? (
      <div className="center-loader">
        <Spinner />
        <span>Opening file</span>
      </div>
    ) : activeSession ? (
      <VirgilSessionView
        key={activeSession.record.id}
        workspaceID={loadedWorkspace.id}
        session={activeSession.record}
        transient={activeSession.transient}
        locked={Boolean(activeSession.busy) || mutationBlocked()}
        prompt={sessionPrompts[activeSession.record.id] ?? ""}
        activityBuffer={agentActivityBuffer}
        onPromptChange={(value) => {
          const id = activeSession.record.id;
          setSessionPrompts((current) => {
            if (!value) {
              if (!(id in current)) return current;
              const next = { ...current };
              delete next[id];
              return next;
            }
            return { ...current, [id]: value };
          });
        }}
        onSessionChange={(record) => {
          if (mutationBlocked()) return false;
          return updateSession(
            record,
            activeSession.transient ? activeSession.record.id : undefined,
          );
        }}
        onReload={() => {
          if (mutationBlocked()) return Promise.resolve();
          return onReload();
        }}
        onNotify={onNotify}
        onError={onError}
        onBusyChange={(busy) =>
          setSessionTabBusy(activeSession.record.id, busy)
        }
      />
    ) : activeDocument ? (
      <>
        {sourceSaveToast && (
          <div
            className={`source-save-toast source-save-toast--${sourceSaveToastTone}`}
            role="status"
            aria-live="polite"
          >
            <span>{sourceSaveToast}</span>
            <button
              type="button"
              className="source-save-toast__dismiss"
              onClick={dismissSourceSaveToast}
              aria-label="Dismiss save notification"
            >
              <Icon name="close" size={13} />
            </button>
          </div>
        )}
        <CodeEditor
          path={activeDocumentLabel}
          value={activeDocument.content}
          onChange={updateDocument}
          onSave={(snapshot) => void saveFile(snapshot)}
          onDiagnostics={(diagnostics) => {
            const path = activeDocument.path;
            diagnosticsByPath.current[path] = diagnostics;
            const severity = severityFromSave(false, diagnostics);
            setFileSeverity((current) => {
              if (current[path] === severity) return current;
              const next = { ...current };
              if (severity) next[path] = severity;
              else delete next[path];
              return next;
            });
          }}
          selection={editorSelection}
        />
      </>
    ) : (
      emptySurface
    );

  const fileTabItems: IndexCardTabItem[] = documents.map((document) => {
    const isDirty =
      document.untitled || document.content !== document.savedContent;
    const statusLabel = document.restored
      ? isDirty
        ? "Draft restored; unsaved changes"
        : "Draft restored"
      : isDirty
        ? "Unsaved"
        : undefined;

    return {
      id: `file:${document.path}`,
      label: editorDocumentLabel(document),
      statusTone: statusLabel ? "warning" : undefined,
      statusLabel,
      icon: <Icon name={document.untitled ? "filePlus" : "files"} size={14} />,
      panel: null,
      onClose: () => void closeDocument(document.path),
      closeLabel: `Close ${editorDocumentLabel(document)}`,
      title: document.untitled
        ? `${editorDocumentLabel(document)} (unsaved)`
        : document.path,
    };
  });
  if (
    activeTab?.kind === "file" &&
    !documents.some((document) => document.path === activeTab.path)
  ) {
    fileTabItems.push({
      id: `file:${activeTab.path}`,
      label: activeTab.path.split("/").pop(),
      icon: <Icon name="files" size={14} />,
      panel: null,
      title: activeTab.path,
    });
  }
  const activeDocumentNeedsSave = Boolean(
    activeDocument &&
    (activeDocument.untitled ||
      activeDocument.content !== activeDocument.savedContent),
  );
  const editorActions = (
    <div
      className="modmaker-editor-actions"
      role="group"
      aria-label="Editor actions"
    >
      {!activeSession && activeDocument && (
        <>
          <EditorIconAction
            label={activeDocumentNeedsSave ? "Save file" : "File saved"}
            shortcut="Ctrl+S"
            icon="save"
            className={`modmaker-editor-action--save ${
              activeDocumentNeedsSave ? "is-dirty" : "is-saved"
            }`}
            loading={busy === "save"}
            disabled={
              mutationBlocked() || !activeDocumentNeedsSave || busy !== ""
            }
            onClick={() => void saveFile()}
          />
          <EditorIconAction
            label="Format document"
            icon="code"
            className="modmaker-editor-action--format"
            loading={busy === "format"}
            disabled={
              mutationBlocked() ||
              busy !== "" ||
              !canFormatSource(activeDocument.path)
            }
            onClick={() => void formatDocument()}
          />
          {!activeDocument.untitled &&
            activeDocument.path.toLowerCase().endsWith(".pc") && (
              <EditorIconAction
                label="Clone variant"
                icon="copy"
                className="modmaker-editor-action--contextual modmaker-editor-action--clone"
                disabled={mutationBlocked() || busy !== ""}
                onClick={() => void cloneSelectedVariant()}
              />
            )}
          {activeDocument.externalContent !== undefined && (
            <EditorIconAction
              label="Reload external changes"
              icon="refresh"
              className="modmaker-editor-action--contextual modmaker-editor-action--reload"
              disabled={busy !== ""}
              onClick={() => void reloadExternalChange()}
            />
          )}
        </>
      )}
      <EditorIconAction
        label="New untitled file"
        shortcut="Ctrl+N"
        icon="filePlus"
        className="modmaker-editor-action--new-file"
        disabled={busy !== "" || mutationBlocked()}
        onClick={openNewDocument}
      />
      <EditorIconAction
        label="New Virgil session"
        shortcut="Ctrl+Shift+N"
        icon="plus"
        className="modmaker-editor-action--new-session"
        disabled={preferenceBusy || busy !== "" || mutationBlocked()}
        onClick={openNewSession}
      />
    </div>
  );

  const tabItems: IndexCardTabItem[] = [
    ...fileTabItems,
    ...sessionTabs.map((tab) => {
      const status = (tab.record.status || "idle").trim().toLowerCase();
      const title = tab.record.title || "Virgil session";
      const statusTone: IndexCardTabItem["statusTone"] =
        status === "idle"
          ? "success"
          : status === "paused"
            ? "warning"
            : status === "error"
              ? "danger"
              : "accent";
      const statusLabel = status
        ? `${status.charAt(0).toUpperCase()}${status.slice(1)}`
        : "Idle";

      return {
        id: `session:${tab.record.id}`,
        label: title,
        statusTone,
        statusLabel,
        icon: <ReplaceableUIPlaceholder entity="virgil-logo" />,
        panel: null,
        onClose: () => {
          void closeSession(tab.record.id);
        },
        closeLabel: `Close ${title}`,
        title: tab.transient
          ? "Transient Virgil session"
          : tab.record.runtimeSessionId || "Legacy Virgil history",
        onContextMenu: (event: React.MouseEvent<HTMLButtonElement>) => {
          event.preventDefault();
          const margin = 8;
          const menuWidth = 184;
          const menuHeight = 84;
          setSessionMenu({
            id: tab.record.id,
            x: Math.max(
              margin,
              Math.min(event.clientX, window.innerWidth - menuWidth - margin),
            ),
            y: Math.max(
              margin,
              Math.min(event.clientY, window.innerHeight - menuHeight - margin),
            ),
          });
        },
      };
    }),
  ].map((item) => ({
    ...item,
    panel: item.id === activeItemID ? activeSurface : null,
  }));

  return (
    <section className={`maker-shell${stale ? " maker-shell--stale" : ""}`}>
      <main
        ref={workspaceMainRef}
        className={`maker-main${stale ? " is-stale" : ""}`}
      >
        <header className="maker-header">
          <Button
            className="maker-back"
            tone="quiet"
            icon="arrow"
            onClick={() => onSelect("")}
          >
            Back to all mods
          </Button>
          <div className="maker-title">
            <Icon name={kindIcon(String(loadedDetail.entity.kind))} size={18} />
            <strong>{loadedDetail.entity.displayName}</strong>
            <Badge tone="neutral">
              {kindLabel(String(loadedDetail.entity.kind))}
            </Badge>
          </div>
          <div className="maker-header__metrics">
            <span>{files.length.toLocaleString()} files</span>
            <span>{formatBytes(loadedDetail.diskBytes)}</span>
            <span
              className={loadedDetail.validation.valid ? "status-good" : ""}
            >
              {validationLabel}
            </span>
            {loadedDetail.activeTest && (
              <span className="status-test">Test installed</span>
            )}
            {dirty && <span className="status-test">Unsaved changes</span>}
          </div>
          <button
            type="button"
            className="maker-header__delete"
            aria-label="Delete project"
            title="Delete project"
            disabled={stale || deleteBusy}
            onClick={() => {
              setDeleteConfirmOpen(true);
              setDeleteError("");
            }}
          >
            <Icon name="trash" size={16} />
          </button>
        </header>
        {stale && (
          <div
            className="maker-stale-warning"
            role="alert"
            aria-live="assertive"
          >
            <Icon name="warning" size={18} />
            <strong>
              Changed. Please close and reopen this mod&apos;s details.
            </strong>
          </div>
        )}

        <nav className="workspace-tool-toggles" aria-label="Workspace tools">
          {workspaceTools.map((tool) => (
            <button
              type="button"
              key={tool.key}
              className={utility === tool.key ? "is-active" : ""}
              aria-pressed={utility === tool.key}
              disabled={sourceBusy && tool.key !== "source"}
              title={
                sourceBusy && tool.key !== "source"
                  ? "Source Control is busy; cancel the Git operation first."
                  : undefined
              }
              onClick={() => toggleUtility(tool.key)}
            >
              <Icon name={tool.icon} size={14} />
              {tool.label}
            </button>
          ))}
        </nav>

        <div
          className={`maker-content${utility ? " has-utility" : ""}${utility === "source" ? " has-source-utility" : ""}`}
        >
          <div className="ide-workspace">
            <div
              className={`editor-layout${fileBrowserResizing ? " is-resizing" : ""}`}
              ref={editorLayoutRef}
              style={
                {
                  "--file-browser-width": `${fileBrowserWidth}px`,
                } as React.CSSProperties
              }
            >
              <aside className="file-browser">
                <div className="file-browser__toolbar">
                  <label className="search-box">
                    <Icon name="search" size={15} />
                    <input
                      value={fileQuery}
                      onChange={(event) => setFileQuery(event.target.value)}
                      placeholder="Filter"
                      aria-label="Filter workspace files"
                    />
                    <span>{countMatchingFiles(files, fileQuery)}</span>
                  </label>
                  <button
                    type="button"
                    className="icon-button"
                    disabled={mutationBlocked()}
                    onClick={() => void createFile()}
                    title="New File"
                    aria-label="New File"
                  >
                    <Icon name="filePlus" size={16} />
                  </button>
                  <button
                    type="button"
                    className="icon-button"
                    disabled={mutationBlocked()}
                    onClick={() => void createDirectory()}
                    title="New Folder"
                    aria-label="New Folder"
                  >
                    <Icon name="folderPlus" size={16} />
                  </button>
                </div>
                <FileTree
                  files={files}
                  directories={directories}
                  query={fileQuery}
                  selected={treeSelection}
                  showSizes={showFileSizes}
                  severityByPath={fileSeverity}
                  onSelectFile={(path) => void selectFile(path)}
                  onMove={(oldPath, newPath) => void movePath(oldPath, newPath)}
                  onRename={renamePath}
                  onDelete={(selection) => void deletePath(selection)}
                  onReveal={revealPath}
                  revealLabel={fileManagerLabel}
                  revealLabelError={fileManagerLabelError}
                />
              </aside>
              <div
                className="editor-layout__resizer"
                role="separator"
                aria-orientation="vertical"
                aria-label="Resize file browser"
                aria-valuemin={Math.round(currentFileBrowserBounds.min)}
                aria-valuemax={Math.round(currentFileBrowserBounds.max)}
                aria-valuenow={Math.round(fileBrowserWidth)}
                aria-valuetext={`${Math.round(fileBrowserWidth)} pixels wide`}
                tabIndex={0}
                onPointerDown={beginFileBrowserResize}
                onPointerMove={updateFileBrowserResize}
                onPointerUp={endFileBrowserResize}
                onPointerCancel={endFileBrowserResize}
                onLostPointerCapture={endFileBrowserResize}
                onKeyDown={resizeFileBrowserWithKeyboard}
              />

              <section
                className={`source-editor source-editor--shared-tabs ${activeSession ? "source-editor--session" : documents.length > 0 || sessionTabs.length > 0 ? "source-editor--open" : "source-editor--empty"}`}
              >
                <IndexCardTabs
                  items={tabItems}
                  value={activeItemID}
                  onValueChange={(id) => {
                    if (id.startsWith("file:")) {
                      const path = id.slice("file:".length);
                      const document = documentsRef.current.find(
                        (item) => item.path === path,
                      );
                      editorInteractionVersion.current += 1;
                      cancelFormatTasks();
                      const nextActive = { kind: "file" as const, path };
                      activeEditorPathRef.current = path;
                      activeTabRef.current = nextActive;
                      setActivePath(path);
                      setActiveTab(nextActive);
                      setTreeSelection(
                        document?.untitled ? null : { path, kind: "file" },
                      );
                      return;
                    }
                    if (!id.startsWith("session:")) return;
                    const sessionID = id.slice("session:".length);
                    editorInteractionVersion.current += 1;
                    cancelFormatTasks();
                    setActiveTab({ kind: "session", id: sessionID });
                    setTreeSelection(null);
                  }}
                  ariaLabel="Open workspace files and Virgil sessions"
                  compact
                  actions={editorActions}
                  mountInactivePanels={false}
                  emptyPanel={emptySurface}
                  className="modmaker-tabs"
                />
              </section>
              {sessionMenu && contextTab && (
                <div
                  className="context-menu virgil-session-context-menu"
                  role="menu"
                  style={{ left: sessionMenu.x, top: sessionMenu.y }}
                  onPointerDown={(event) => event.stopPropagation()}
                >
                  <button
                    type="button"
                    role="menuitem"
                    disabled={Boolean(contextTab.busy) || mutationBlocked()}
                    onClick={() => void renameSession(contextTab.record.id)}
                  >
                    Rename
                  </button>
                  <span />
                  <button
                    type="button"
                    role="menuitem"
                    disabled={mutationBlocked()}
                    className="context-menu__danger"
                    onClick={() => void closeSession(contextTab.record.id)}
                  >
                    {contextTab.transient ? "Close" : "Close / Forget"}
                  </button>
                </div>
              )}
            </div>
            <WorkspaceStatusBar
              source={
                !activeSession && activeDocument
                  ? {
                      lineCount: activeDocument.content.split("\n").length,
                      sizeBytes: activeSizeBytes,
                    }
                  : undefined
              }
              session={activeSession?.record}
            />
          </div>
          {fileSearchOpen && activeDocument && (
            <section
              className="editor-find-panel"
              role="dialog"
              aria-label={`Find in ${activeDocumentLabel}`}
            >
              <form
                className="editor-find-panel__form"
                onSubmit={(event) => {
                  event.preventDefault();
                  moveFileSearch(1);
                }}
              >
                <input
                  ref={fileSearchInputRef}
                  value={fileSearchQuery}
                  onChange={(event) => {
                    setFileSearchQuery(event.target.value);
                    setFileSearchMatchIndex(0);
                    setEditorSelection(null);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Escape") {
                      event.preventDefault();
                      closeFileSearch();
                    } else if (event.key === "Enter") {
                      event.preventDefault();
                      moveFileSearch(event.shiftKey ? -1 : 1);
                    }
                  }}
                  placeholder="Find in file"
                  aria-label={`Find in ${activeDocumentLabel}`}
                  autoComplete="off"
                  spellCheck={false}
                />
                <span className="editor-find-panel__count" aria-live="polite">
                  {!fileSearchQuery.trim()
                    ? "Find"
                    : fileSearchMatches.length
                      ? `${fileSearchMatchIndex + 1} of ${fileSearchMatches.length}`
                      : "No matches"}
                </span>
                <button
                  type="button"
                  className="editor-find-panel__button editor-find-panel__button--previous"
                  onClick={() => moveFileSearch(-1)}
                  disabled={!fileSearchMatches.length}
                  aria-label="Previous match"
                >
                  <Icon name="chevron" size={14} />
                </button>
                <button
                  type="button"
                  className="editor-find-panel__button editor-find-panel__button--next"
                  onClick={() => moveFileSearch(1)}
                  disabled={!fileSearchMatches.length}
                  aria-label="Next match"
                >
                  <Icon name="chevron" size={14} />
                </button>
                <button
                  type="button"
                  className="editor-find-panel__close"
                  onClick={closeFileSearch}
                  aria-label="Close find"
                >
                  <Icon name="close" size={14} />
                </button>
              </form>
            </section>
          )}
          {workspaceSearchOpen && (
            <div
              className="workspace-search-overlay"
              role="presentation"
              onMouseDown={(event) => {
                if (event.target === event.currentTarget) {
                  closeWorkspaceSearch();
                }
              }}
            >
              <section
                className="workspace-search-dialog"
                role="dialog"
                aria-modal="true"
                aria-labelledby="workspace-search-title"
                aria-describedby="workspace-search-status"
              >
                <header>
                  <div>
                    <h2 id="workspace-search-title">Search workspace</h2>
                    <p>Search text files in the opened mod</p>
                  </div>
                  <button
                    type="button"
                    className="workspace-search-dialog__close"
                    onClick={closeWorkspaceSearch}
                    aria-label="Close workspace search"
                  >
                    <Icon name="close" size={16} />
                  </button>
                </header>
                <form
                  className="workspace-search-dialog__form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    void runWorkspaceSearch();
                  }}
                >
                  <div className="workspace-search-dialog__query">
                    <input
                      ref={workspaceSearchInputRef}
                      value={workspaceSearchQuery}
                      onChange={(event) => {
                        workspaceSearchRequestVersion.current += 1;
                        setWorkspaceSearchLoading(false);
                        setWorkspaceSearchQuery(event.target.value);
                        setWorkspaceSearchResults([]);
                        setWorkspaceSearchError("");
                      }}
                      placeholder="Search text"
                      aria-label="Search workspace text"
                      autoComplete="off"
                      spellCheck={false}
                      onKeyDown={(event) => {
                        if (event.key === "Escape") {
                          event.preventDefault();
                          closeWorkspaceSearch();
                        }
                      }}
                    />
                    <Button
                      type="submit"
                      tone="primary"
                      icon="search"
                      disabled={
                        workspaceSearchLoading || !workspaceSearchQuery.trim()
                      }
                    >
                      Search
                    </Button>
                  </div>
                  <div className="workspace-search-dialog__options">
                    <label>
                      <input
                        type="checkbox"
                        checked={workspaceSearchRegex}
                        onChange={(event) => {
                          workspaceSearchRequestVersion.current += 1;
                          setWorkspaceSearchLoading(false);
                          setWorkspaceSearchRegex(event.target.checked);
                          setWorkspaceSearchResults([]);
                          setWorkspaceSearchError("");
                        }}
                      />
                      Regular expression
                    </label>
                    <label>
                      <input
                        type="checkbox"
                        checked={workspaceSearchCaseSensitive}
                        onChange={(event) => {
                          workspaceSearchRequestVersion.current += 1;
                          setWorkspaceSearchLoading(false);
                          setWorkspaceSearchCaseSensitive(event.target.checked);
                          setWorkspaceSearchResults([]);
                          setWorkspaceSearchError("");
                        }}
                      />
                      Case sensitive
                    </label>
                  </div>
                  <div
                    id="workspace-search-status"
                    className={`workspace-search-dialog__status${workspaceSearchError ? " workspace-search-dialog__error" : ""}`}
                    aria-live="polite"
                    role={workspaceSearchError ? "alert" : undefined}
                  >
                    {workspaceSearchError ||
                      (workspaceSearchLoading
                        ? "Searching text files…"
                        : workspaceSearchQuery.trim()
                          ? `${workspaceSearchResults.length.toLocaleString()} matches`
                          : "Enter a literal or regular-expression query")}
                  </div>
                </form>
                <div
                  className="workspace-search-dialog__results"
                  role="listbox"
                  aria-label="Workspace search results"
                >
                  {workspaceSearchResults.slice(0, 100).map((match, index) => (
                    <button
                      type="button"
                      role="option"
                      aria-selected={false}
                      className="workspace-search-dialog__result"
                      key={`${match.relativePath}:${match.line}:${match.column}:${index}`}
                      onClick={() => openWorkspaceMatch(match)}
                      aria-label={`${match.relativePath}, line ${match.line}, column ${match.column}: ${match.preview || "empty matching line"}`}
                    >
                      <span className="workspace-search-dialog__result-path">
                        {match.relativePath}
                      </span>
                      <span className="workspace-search-dialog__result-location">
                        {match.line}:{match.column}
                      </span>
                      <span className="workspace-search-dialog__result-preview">
                        {match.preview || "(empty matching line)"}
                      </span>
                    </button>
                  ))}
                  {workspaceSearchQuery.trim() &&
                    !workspaceSearchLoading &&
                    !workspaceSearchError &&
                    workspaceSearchResults.length === 0 && (
                      <div className="workspace-search-dialog__empty">
                        No text-file matches
                      </div>
                    )}
                </div>
              </section>
            </div>
          )}

          <div className="workspace-utility-drawer">
            <div
              className="source-control-utility"
              hidden={utility !== "source"}
              aria-hidden={utility !== "source"}
            >
              <SourceControlView
                key={loadedWorkspace.id}
                workspaceID={loadedWorkspace.id}
                active={utility === "source"}
                dirtyPaths={dirtyPaths}
                writeBlocked={mutationBlocked()}
                onOpenFile={(path) => {
                  if (sourceBusy) {
                    onNotify(
                      "Source Control is busy. Cancel the Git operation before opening an editor file.",
                      "info",
                    );
                    return;
                  }
                  setUtility(null);
                  void selectFile(path);
                }}
                onWorkingTreeChanged={reconcileWorkingTree}
                onBusyChange={(busyState) => {
                  setSourceBusy(busyState);
                  if (busyState && utility !== "source") setUtility("source");
                }}
                onNotify={(message, tone) =>
                  onNotify(message, tone === "warning" ? "info" : tone)
                }
                onError={onError}
              />
            </div>
            {utility && utility !== "source" && (
              <WorkspaceUtilityPanel
                active={utility}
                detail={loadedDetail}
                runtime={runtime}
                exportLabel={exportLabel}
                busy={mutationBlocked() ? "blocked" : busy}
                onClose={() => {
                  if (sourceBusy) {
                    setUtility("source");
                    onNotify(
                      "Source Control is busy. Cancel the Git operation before closing it.",
                      "info",
                    );
                    return;
                  }
                  setUtility(null);
                }}
                onExportLabelChange={(value) => {
                  if (!staleRef.current) setExportLabel(value);
                }}
                onValidate={validate}
                onExport={exportWorkspace}
                onInstall={installLatest}
                onUninstall={uninstallTest}
                onLaunch={launchGame}
                onAnalyze={analyzeRuntime}
              />
            )}
          </div>
        </div>
      </main>

      {!loadedWorkspace.virgilConfigured && (
        <VirgilChoiceDialog
          modName={loadedDetail.entity.displayName}
          busy={preferenceBusy || mutationBlocked()}
          onChoose={(enabled) => {
            if (!mutationBlocked()) void configureVirgil(enabled, "choice");
          }}
        />
      )}
      {deleteConfirmOpen && (
        <CollectionDialog
          title="Delete this project?"
          onClose={() => {
            if (!deleteBusy) {
              setDeleteConfirmOpen(false);
              setDeleteError("");
            }
          }}
          footer={
            <>
              <Button
                type="button"
                onClick={() => {
                  setDeleteConfirmOpen(false);
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
                onClick={() => {
                  setDeleteBusy(true);
                  setDeleteError("");
                  void handleDeleteProject(loadedWorkspace.id)
                    .then(() => setDeleteConfirmOpen(false))
                    .catch((error) =>
                      setDeleteError(
                        error instanceof Error
                          ? error.message
                          : "The project could not be deleted.",
                      ),
                    )
                    .finally(() => setDeleteBusy(false));
                }}
              >
                Delete project
              </Button>
            </>
          }
        >
          <p className="library-removal__copy">
            The editable working copy of{" "}
            <strong>{loadedDetail.entity.displayName}</strong> and any unsaved
            work will be permanently deleted. The library mod is not affected.
          </p>
          {deleteError && (
            <p className="collection-add__error" role="alert">
              {deleteError}
            </p>
          )}
        </CollectionDialog>
      )}
    </section>
  );
}

function NewModStart({
  blocked,
  defaultAuthor,
  onCreate,
  onError,
}: {
  blocked: boolean;
  defaultAuthor: string;
  onCreate: (
    request: NewModRequest,
    prompt?: string,
    modelOverride?: string,
  ) => Promise<void>;
  onError: (error: unknown) => void;
}) {
  const [mode, setMode] = useState<"prompt" | "manual">("prompt");
  const [prompt, setPrompt] = useState("");
  const [name, setName] = useState("");
  const [modID, setModID] = useState("");
  const [kind, setKind] = useState("vehicle");
  const [author, setAuthor] = useState(defaultAuthor);
  const [version, setVersion] = useState("0.1.0");
  const [description, setDescription] = useState("");
  const [creating, setCreating] = useState(false);
  const creatingRef = useRef(false);
  const [manualID, setManualID] = useState(false);
  const [showModels, setShowModels] = useState(false);
  const [models, setModels] = useState<AgentModelOption[]>([]);
  const [model, setModel] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);

  const changeName = (value: string) => {
    setName(value);
    if (!manualID) setModID(slugify(value));
  };

  const create = async (request: NewModRequest, virgilPrompt = "") => {
    if (blocked) return;
    if (creatingRef.current) return;
    creatingRef.current = true;
    setCreating(true);
    try {
      await onCreate(request, virgilPrompt, model);
    } catch (error) {
      onError(error);
    } finally {
      creatingRef.current = false;
      setCreating(false);
    }
  };

  const createFromPrompt = () => {
    void create(inferNewMod(prompt, defaultAuthor), prompt.trim());
  };

  const createManual = () => {
    void create({
      name: name.trim(),
      modId: modID.trim(),
      kind,
      author: author.trim(),
      version: version.trim(),
      description: description.trim(),
    });
  };

  const revealModels = async () => {
    const next = !showModels;
    setShowModels(next);
    if (!next || models.length) return;

    setModelsLoading(true);
    try {
      setModels((await API.ListAgentModels()) ?? []);
    } catch (error) {
      onError(error);
    } finally {
      setModelsLoading(false);
    }
  };

  return (
    <section className="new-mod-start">
      <header className="new-mod-start__header">
        <div>
          <h1 id="new-mod-title">New mod</h1>
          <p>
            {mode === "prompt"
              ? "Describe what you want to build, or set up the mod yourself."
              : "Choose the mod details yourself, then create your workspace."}
          </p>
        </div>
      </header>

      {mode === "prompt" ? (
        <div className="prompt-create">
          <div className="virgil-create-bar">
            <Icon name="agent" size={22} />
            <textarea
              value={prompt}
              onChange={(event) => setPrompt(event.target.value)}
              onKeyDown={(event) => {
                if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
                  event.preventDefault();
                  createFromPrompt();
                }
              }}
              placeholder="Describe your BeamNG mod and the behavior it should have"
              autoFocus
            />
            <Button
              icon="arrow"
              tone="primary"
              disabled={blocked || !prompt.trim() || creating}
              onClick={createFromPrompt}
            >
              {creating ? "Creating mod" : "Create mod"}
            </Button>
          </div>
          <div className="creation-options">
            <button
              type="button"
              className="creation-link"
              onClick={() => setMode("manual")}
            >
              Set up manually
            </button>
            <button
              type="button"
              className="creation-link"
              onClick={() => void revealModels()}
            >
              {showModels ? "Hide model options" : "Choose a model (optional)"}
            </button>
            {showModels && (
              <label className="creation-model">
                <span>Virgil model</span>
                {modelsLoading ? (
                  <Spinner small />
                ) : (
                  <select
                    value={model}
                    onChange={(event) => setModel(event.target.value)}
                  >
                    <option value="">Use application settings</option>
                    {models.map((option) => (
                      <option key={option.selector} value={option.selector}>
                        {option.name || option.id} · {option.provider}
                      </option>
                    ))}
                  </select>
                )}
              </label>
            )}
            <span>Ctrl+Enter to create</span>
          </div>
        </div>
      ) : (
        <div className="manual-create">
          <button
            type="button"
            className="creation-link creation-back"
            onClick={() => setMode("prompt")}
          >
            ← Back to Virgil
          </button>
          <div className="form-grid">
            <label className="field">
              <span>Mod kind</span>
              <select
                value={kind}
                onChange={(event) => setKind(event.target.value)}
              >
                <option value="vehicle">Vehicle</option>
                <option value="map">Map</option>
                <option value="ui">UI app</option>
                <option value="script">Game-engine script</option>
              </select>
            </label>
            <label className="field">
              <span>Name</span>
              <input
                value={name}
                onChange={(event) => changeName(event.target.value)}
                placeholder="My BeamNG mod"
              />
            </label>
            <label className="field">
              <span>Mod ID</span>
              <input
                value={modID}
                onChange={(event) => {
                  setManualID(true);
                  setModID(event.target.value);
                }}
                placeholder="my_beamng_mod"
              />
            </label>
            <label className="field">
              <span>Author</span>
              <input
                value={author}
                onChange={(event) => setAuthor(event.target.value)}
                placeholder="Your name"
              />
            </label>
            <label className="field">
              <span>Version</span>
              <input
                value={version}
                onChange={(event) => setVersion(event.target.value)}
                placeholder="0.1.0"
              />
            </label>
            <label className="field field--wide">
              <span>Description</span>
              <textarea
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="What this mod changes"
              />
            </label>
          </div>
          <Button
            icon="plus"
            tone="primary"
            disabled={
              blocked ||
              !name.trim() ||
              !modID.trim() ||
              !version.trim() ||
              creating
            }
            onClick={createManual}
          >
            {creating ? "Creating mod" : "Create mod"}
          </Button>
        </div>
      )}
    </section>
  );
}

function inferNewMod(prompt: string, defaultAuthor: string): NewModRequest {
  const source = prompt.trim();
  const lower = source.toLowerCase();
  const kind = /\b(map|level|terrain|road|track)\b/.test(lower)
    ? "map"
    : /\b(ui|interface|dashboard|app|widget)\b/.test(lower)
      ? "ui"
      : /\b(lua|script|extension|game engine)\b/.test(lower)
        ? "script"
        : "vehicle";
  const cleaned = source
    .replace(/^(please\s+)?(create|make|build|start)\s+(me\s+)?(an?\s+)?/i, "")
    .split(/[.!?\n]/)[0]
    .trim();
  const fallback =
    kind === "map"
      ? "New Map"
      : kind === "ui"
        ? "New UI App"
        : kind === "script"
          ? "New Script Mod"
          : "New Vehicle Mod";
  const name = (cleaned || fallback).slice(0, 64);
  return {
    name,
    modId: slugify(name) || `new_${kind}_mod`,
    kind,
    author: defaultAuthor,
    version: "0.1.0",
    description: source,
  };
}

function slugify(value: string) {
  return value
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 64);
}
