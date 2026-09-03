import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
} from "react";
import { Events } from "@wailsio/runtime";
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
import type { WorkspaceChange } from "../bindings/github.com/SignedAdam/beamworlds-modkit/models.js";
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
import { VirgilChoiceDialog, VirgilSessionView } from "./VirgilSessionView";
import { ReplaceableUIPlaceholder } from "./replaceableUi";
import { WorkspaceStatusBar } from "./WorkspaceStatusBar";
import {
  WorkspaceUtilityPanel,
  type WorkspaceTool,
} from "./WorkspaceUtilityPanel";

interface EditorDocument {
  path: string;
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

const virgilSessionTabsStoragePrefix =
  "beamworlds.modmaker-virgil-tabs.v1";

function virgilSessionTabsStorageKey(workspaceID: string) {
  return `${virgilSessionTabsStoragePrefix}:${workspaceID}`;
}

function createSessionTabID(existing?: ReadonlySet<string>) {
  let id = "";
  do {
    const token =
      typeof crypto !== "undefined" &&
      typeof crypto.randomUUID === "function"
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
    const order =
      (left.record.tabOrder ?? 0) - (right.record.tabOrder ?? 0);
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
    .filter(
      (tab) => tab.transient && tab.record.workspaceId === workspaceID,
    )
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
  { key: "changes", icon: "diff", label: "Changes" },
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
    return Number.isFinite(stored)
      ? clampFileBrowserWidth(stored)
      : fallback;
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
    regex
      ? query
      : query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
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
const AUTO_FORMAT_DELAY_DEFAULT_MS = 200
const AUTO_FORMAT_DELAY_MIN_MS = 50
const AUTO_FORMAT_DELAY_MAX_MS = 2000

function clampAutoFormatDelay(value: number) {
  if (!Number.isFinite(value) || value <= 0) return AUTO_FORMAT_DELAY_DEFAULT_MS
  return Math.min(AUTO_FORMAT_DELAY_MAX_MS, Math.max(AUTO_FORMAT_DELAY_MIN_MS, Math.round(value)))
}

function severityFromSave(formatFailed: boolean, diagnostics: readonly CodeEditorDiagnostic[]): TreeSeverity | undefined {
  if (formatFailed || diagnostics.some(diagnostic => diagnostic.severity === 'error')) return 'error'
  if (diagnostics.some(diagnostic => diagnostic.severity === 'warning')) return 'warning'
  return undefined
}

function formatSaveIssue(path: string, error: unknown) {
  const record = error && typeof error === 'object' ? error as Record<string, unknown> : {}
  const rawMessage = typeof record.message === 'string' ? record.message : error instanceof Error ? error.message : String(error)
  const message = rawMessage.split(/\r?\n/, 1)[0].replace(/^(?:Syntax)?Error:\s*/i, '').trim() || 'The formatter rejected this source.'
  const location = record.loc && typeof record.loc === 'object' ? record.loc as Record<string, unknown> : {}
  const start = location.start && typeof location.start === 'object' ? location.start as Record<string, unknown> : location
  const line = typeof start.line === 'number' && Number.isFinite(start.line) ? Math.max(1, Math.trunc(start.line)) : undefined
  const column = typeof start.column === 'number' && Number.isFinite(start.column) ? Math.max(1, Math.trunc(start.column)) : undefined
  const suffix = line ? ` (line ${line}${column ? `, column ${column}` : ''})` : ''
  return `${path}: Saved, but formatting failed — ${message}${suffix}`
}
function validationSaveIssue(path: string, source: string, diagnostics: readonly CodeEditorDiagnostic[]) {
  const diagnostic = diagnostics.find(item => item.severity === 'error') ?? diagnostics.find(item => item.severity === 'warning')
  if (!diagnostic) return undefined
  const severity: 'error' | 'warning' = diagnostic.severity === 'error' ? 'error' : 'warning'
  const message = diagnostic.message.split(/\r?\n/, 1)[0].trim() || 'Validation reported an issue.'
  const from = Math.max(0, Math.min(source.length, diagnostic.from))
  const lineStart = source.lastIndexOf('\n', Math.max(0, from - 1)) + 1
  const line = source.slice(0, from).split(/\r?\n/).length
  const column = from - lineStart + 1
  return { message: `${path}: Saved with ${severity} — ${message} (line ${line}, column ${column})`, severity }
}

interface ModMakerProps {
  allItems: LibraryItem[];
  workspaces: WorkspaceRecord[];
  detail: WorkspaceDetail | null;
  selectedID: string;
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
  const [changes, setChanges] = useState<WorkspaceChange[]>([]);
  const [diffLoading, setDiffLoading] = useState(false);
  const [busy, setBusy] = useState("");
  const [exportLabel, setExportLabel] = useState("");
  const [runtime, setRuntime] = useState<RuntimeReport | null>(null);
  const [utility, setUtility] = useState<WorkspaceTool | null>(null);
  const [preferenceBusy, setPreferenceBusy] = useState(false);
  const [newModOpen, setNewModOpen] = useState(false);
  const [sourceSaveToast, setSourceSaveToast] = useState("");
  const [sourceSaveToastTone, setSourceSaveToastTone] = useState<'success' | 'error' | 'warning'>('success');
  const initializedWorkspace = useRef("");
  const draftTimer = useRef<number | undefined>(undefined);
  const [fileSeverity, setFileSeverity] = useState<Record<string, TreeSeverity>>({});
  const documentsRef = useRef<EditorDocument[]>([]);
  const documentRevisions = useRef<Record<string, number>>({});
  const diagnosticsByPath = useRef<Record<string, CodeEditorDiagnostic[]>>({});
  const formatTimers = useRef(new Map<string, number>());
  const formatGeneration = useRef(0);
  const externalReadVersion = useRef<Record<string, number>>({});
  const sessionTabsRef = useRef<SessionTab[]>([]);
  const fileOpenVersion = useRef(0);
  const transientSequence = useRef(0);
  const closedTransientSessions = useRef(new Set<string>());
  const sessionTabsInitializedWorkspace = useRef("");
  const sessionTabsWorkspace = useRef("");
  const sessionTabsHydrationPending = useRef("");
  const activeEditorPathRef = useRef(activePath);
  const activeWorkspaceIDRef = useRef(selectedID);
  const activeTabRef = useRef<ActiveEditorTab | null>(activeTab);
  const editorInteractionVersion = useRef(0);
  const preferenceRequestVersion = useRef(0);
  const savesInFlight = useRef(new Set<string>());
  const sourceSaveToastTimer = useRef<number | undefined>(undefined);
  const workspaceMainRef = useRef<HTMLElement>(null);
  const [fileBrowserWidth, setFileBrowserWidth] = useState(
    readFileBrowserWidth,
  );
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
  const showSourceSaveToast = (message: string, tone: 'success' | 'error' | 'warning' = 'success') => {
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
    for (const timer of formatTimers.current.values()) window.clearTimeout(timer);
    formatTimers.current.clear();
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
    (document) => document.content !== document.savedContent,
  );
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
    const availableWidth = editorLayoutRef.current?.getBoundingClientRect()
      .width;
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
    const availableWidth = editorLayoutRef.current?.getBoundingClientRect()
      .width;
    setFileBrowserWidth(
      clampFileBrowserWidth(
        active.startWidth + event.clientX - active.startX,
        availableWidth,
      ),
    );
  };

  const endFileBrowserResize = (
    event: React.PointerEvent<HTMLDivElement>,
  ) => {
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
    const availableWidth = editorLayoutRef.current?.getBoundingClientRect()
      .width;
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
    editorInteractionVersion.current += 1;
    preferenceRequestVersion.current += 1;
    transientSequence.current = 0;
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
    setChanges([]);
    setDiffLoading(false);
    setBusy("");
    setExportLabel("");
    setPreferenceBusy(false);
    setRuntime(null);
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
        for (const document of restored) documentRevisions.current[document.path] = Math.max(0, Math.trunc(documentRevisions.current[document.path] ?? 0));
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
    const isInitial =
      sessionTabsInitializedWorkspace.current !== workspace.id;
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
    writePersistedSessionTabDescriptors(workspace.id, sessionTabs);
  }, [sessionTabs, workspace?.id, selectedID]);

  useEffect(() => {
    onEditorStatus({
      path: activeSession ? "" : activePath,
      dirty,
      sizeBytes: activeSession ? 0 : activeSizeBytes,
    });
    return () => onEditorStatus({ path: "", dirty: false, sizeBytes: 0 });
  }, [
    activePath,
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

  useEffect(() => {
    if (!workspace) return;
    let active = true;
    const stop = Events.On("agent:event", (event) => {
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
      if (refreshTimer !== undefined) return;
      refreshTimer = window.setTimeout(() => {
        refreshTimer = undefined;
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
    if (!workspace || documents.length === 0) return;

    const snapshot = documents.map((document) => ({ ...document }));
    draftTimer.current = window.setTimeout(() => {
      void Promise.all(
        snapshot.map((document) =>
          document.content === document.savedContent
            ? API.DeleteWorkspaceDraft(workspace.id, document.path)
            : API.SaveWorkspaceDraft(
                workspace.id,
                document.path,
                document.content,
                document.savedSHA256,
              ),
        ),
      ).catch(onError);
    }, 450);

    return () => {
      if (draftTimer.current !== undefined)
        window.clearTimeout(draftTimer.current);
    };
  }, [documents, workspace?.id]);

  const refreshDiff = async () => {
    if (!workspace) return;
    setDiffLoading(true);
    try {
      setChanges((await API.WorkspaceDiff(workspace.id)) ?? []);
    } catch (error) {
      onError(error);
    } finally {
      setDiffLoading(false);
    }
  };

  useEffect(() => {
    if (utility === "changes" && workspace) void refreshDiff();
  }, [utility, workspace?.id]);

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
      documentRevisions.current[path] = Math.max(0, Math.trunc(documentRevisions.current[path] ?? 0));
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
      documentRevisions.current[file.path] = Math.max(0, Math.trunc(documentRevisions.current[file.path] ?? 0));
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

  const runAutoFormat = async (snapshot: { workspaceID: string; path: string; content: string; revision: number; generation: number }) => {
    if (formatGeneration.current !== snapshot.generation || activeWorkspaceIDRef.current !== snapshot.workspaceID || activeEditorPathRef.current !== snapshot.path) return
    const current = documentsRef.current.find(document => document.path === snapshot.path)
    if (!current || current.content !== snapshot.content || documentRevisions.current[snapshot.path] !== snapshot.revision) return
    let formatted: string
    try {
      formatted = await formatSource(snapshot.path, snapshot.content)
    } catch {
      return
    }
    if (formatGeneration.current !== snapshot.generation || activeWorkspaceIDRef.current !== snapshot.workspaceID || activeEditorPathRef.current !== snapshot.path) return
    const latest = documentsRef.current.find(document => document.path === snapshot.path)
    if (!latest || latest.content !== snapshot.content || documentRevisions.current[snapshot.path] !== snapshot.revision) return
    if (formatted === snapshot.content) return
    bumpDocumentRevision(snapshot.path)
    const next = documentsRef.current.map(document => document.path === snapshot.path ? { ...document, content: formatted, restored: false } : document)
    documentsRef.current = next
    setDocuments(next)
  }

  const scheduleAutoFormat = (snapshot: { workspaceID: string; path: string; content: string; revision: number }) => {
    clearFormatTimer(snapshot.path)
    if (!canFormatSource(snapshot.path)) return
    const generation = formatGeneration.current
    const timer = window.setTimeout(() => {
      formatTimers.current.delete(snapshot.path)
      void runAutoFormat({ ...snapshot, generation })
    }, clampAutoFormatDelay(autoFormatDelayMs))
    formatTimers.current.set(snapshot.path, timer)
  }

  const updateDocument = (content: string) => {
    const path = activeEditorPathRef.current || activePath
    if (!workspace || !path) return
    const current = documentsRef.current.find(document => document.path === path)
    if (!current) return
    const revision = bumpDocumentRevision(path)
    const next = documentsRef.current.map(document => document.path === path ? { ...document, content, restored: false } : document)
    documentsRef.current = next
    setDocuments(next)
    scheduleAutoFormat({ workspaceID: workspace.id, path, content, revision })
  }

  const saveFile = async (editorSnapshot?: CodeEditorSaveSnapshot) => {
    if (!workspace) return;
    const workspaceID = workspace.id;
    const targetPath = activeEditorPathRef.current;
    if (!targetPath || savesInFlight.current.has(workspaceID)) return;
    let attemptPath = "";
    savesInFlight.current.add(workspaceID);
    setBusy("save");
    try {
      let requestedSnapshot = editorSnapshot;
      for (;;) {
        const path = targetPath;
        const document = documentsRef.current.find(item => item.path === path);
        if (!document || activeWorkspaceIDRef.current !== workspaceID || document.content === document.savedContent) return;
        clearFormatTimer(path);
        formatGeneration.current += 1;
        const source = requestedSnapshot?.value === document.content ? requestedSnapshot.value : document.content;
        const revision = documentRevisions.current[path] ?? 0;
        const diagnostics = requestedSnapshot && requestedSnapshot.value === source ? requestedSnapshot.diagnostics : diagnosticsByPath.current[path] ?? [];
        requestedSnapshot = undefined;
        let contentToWrite = source;
        let formatError: unknown;
        if (canFormatSource(path)) {
          try {
            contentToWrite = await formatSource(path, source);
          } catch (error) {
            formatError = error;
          }
        }
        const latest = documentsRef.current.find(item => item.path === path);
        if (activeWorkspaceIDRef.current !== workspaceID) return;
        if (!latest || latest.content !== source || documentRevisions.current[path] !== revision) continue;
        const expectedSHA256 = latest.externalContent !== undefined ? (latest.externalSHA256 ?? latest.savedSHA256) : latest.savedSHA256;
        if (latest.externalContent !== undefined && !window.confirm(`Virgil changed ${path} on disk. Save your version and overwrite Virgil's change?`)) return;
        attemptPath = path;
        await API.WriteWorkspaceFile(workspaceID, path, contentToWrite, expectedSHA256);
        const savedSHA256 = await sha256Text(contentToWrite);
        const saveSeverity = severityFromSave(Boolean(formatError), diagnostics);
        const validationIssue = validationSaveIssue(path, source, diagnostics);
        externalReadVersion.current[path] = (externalReadVersion.current[path] ?? 0) + 1;
        const currentAfter = documentsRef.current.find(item => item.path === path);
        const sameSnapshot = activeWorkspaceIDRef.current === workspaceID && currentAfter?.content === source && documentRevisions.current[path] === revision;
        const next = documentsRef.current.map(item => item.path === path ? {
          ...item,
          ...(sameSnapshot ? { content: contentToWrite } : {}),
          savedContent: contentToWrite,
          savedSHA256,
          restored: false,
          externalContent: undefined,
          externalSHA256: undefined,
        } : item);
        documentsRef.current = next;
        setDocuments(next);
        if (activeWorkspaceIDRef.current === workspaceID) {
          setFileSeverity(current => {
            const nextSeverity = { ...current };
            if (saveSeverity) nextSeverity[path] = saveSeverity;
            else delete nextSeverity[path];
            return nextSeverity;
          });
          if (sameSnapshot && activeEditorPathRef.current === targetPath) {
            const message = formatError
              ? formatSaveIssue(path, formatError)
              : validationIssue
                ? validationIssue.message
                : `Saved ${path}`;
            const tone = formatError ? "error" : validationIssue?.severity ?? "success";
            showSourceSaveToast(message, tone);
          }
        }
        if (activeWorkspaceIDRef.current === workspaceID) await onReload();
        return;
      }
    } catch (error) {
      if (attemptPath) {
        try {
          const file = await API.ReadWorkspaceFile(workspaceID, attemptPath);
          if (activeWorkspaceIDRef.current === workspaceID) {
            const next = documentsRef.current.map(item => item.path === attemptPath && item.savedSHA256 !== file.sha256 ? { ...item, externalContent: file.content, externalSHA256: file.sha256 } : item);
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

  const reloadExternalChange = () => {
    if (!activeDocument || activeDocument.externalContent === undefined) return;
    if (
      activeDocument.content !== activeDocument.savedContent &&
      !window.confirm(
        `Discard your unsaved changes to ${activeDocument.path} and load Virgil's version?`,
      )
    )
      return;
    const externalContent = activeDocument.externalContent;
    const externalSHA256 =
      activeDocument.externalSHA256 ?? activeDocument.savedSHA256;
    cancelFormatTasks();
    bumpDocumentRevision(activeDocument.path);
    setDocuments((current) =>
      current.map((document) =>
        document.path === activeDocument.path
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
    onNotify(`Loaded Virgil's version of ${activeDocument.path}`, "info");
  };

  const closeDocument = (path: string) => {
    clearFormatTimer(path);
    formatGeneration.current += 1;
    delete documentRevisions.current[path];
    delete diagnosticsByPath.current[path];
    setFileSeverity(current => {
      if (!(path in current)) return current;
      const next = { ...current };
      delete next[path];
      return next;
    });
    externalReadVersion.current[path] =
      (externalReadVersion.current[path] ?? 0) + 1;
    const document = documents.find((item) => item.path === path);
    if (workspace && document) {
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

    const remaining = documents.filter((item) => item.path !== path);
    setDocuments(remaining);
    if (activePath === path) {
      setActivePath(remaining[remaining.length - 1]?.path ?? "");
    }
    if (activeTab?.kind === "file" && activeTab.path === path) {
      const next = remaining[remaining.length - 1];
      if (next) {
        setActivePath(next.path);
        setActiveTab({ kind: "file", path: next.path });
        setTreeSelection({ path: next.path, kind: "file" });
      } else {
        const nextSession = sessionTabs[sessionTabs.length - 1];
        setActiveTab(
          nextSession ? { kind: "session", id: nextSession.record.id } : null,
        );
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

  const createFile = async () => {
    if (!workspace) return;
    const base = parentDirectory();
    const suggested = base ? `${base}/new-file.lua` : "new-file.lua";
    const path = window
      .prompt("New workspace-relative file path", suggested)
      ?.trim();
    if (!path) return;

    try {
      await API.WriteWorkspaceFile(workspace.id, path, "", "");
      await onReload();
      await selectFile(path);
      onNotify(`Created ${path}`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const createDirectory = async () => {
    if (!workspace) return;
    const base = parentDirectory();
    const path = window
      .prompt(
        "New workspace-relative folder",
        base ? `${base}/new-folder` : "new-folder",
      )
      ?.trim();
    if (!path) return;

    try {
      await API.CreateWorkspaceDirectory(workspace.id, path);
      await onReload();
      onNotify(`Created ${path}`, "success");
    } catch (error) {
      onError(error);
    }
  };

  const movePath = async (oldPath: string, newPath: string) => {
    if (!workspace || !newPath || oldPath === newPath) return;
    cancelFormatTasks();

    try {
      await API.RenameWorkspacePath(workspace.id, oldPath, newPath);
      const migrate = (path: string) =>
        path === oldPath
          ? newPath
          : path.startsWith(`${oldPath}/`)
            ? `${newPath}${path.slice(oldPath.length)}`
            : path;
      const migratedRevisions: Record<string, number> = {};
      for (const [path, revision] of Object.entries(documentRevisions.current)) migratedRevisions[migrate(path)] = revision;
      documentRevisions.current = migratedRevisions;
      const migratedDiagnostics: Record<string, CodeEditorDiagnostic[]> = {};
      for (const [path, diagnostics] of Object.entries(diagnosticsByPath.current)) migratedDiagnostics[migrate(path)] = diagnostics;
      diagnosticsByPath.current = migratedDiagnostics;
      setFileSeverity(current => {
        const next: Record<string, TreeSeverity> = {};
        for (const [path, severity] of Object.entries(current)) next[migrate(path)] = severity;
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

  const renamePath = (selection: TreeSelection) => {
    const nextPath = window
      .prompt(`Rename workspace ${selection.kind}`, selection.path)
      ?.trim();
    if (nextPath) void movePath(selection.path, nextPath);
  };

  const deletePath = async (selection: TreeSelection) => {
    if (!workspace) return;
    const scope =
      selection.kind === "directory" ? " and every path inside it" : "";
    if (
      !window.confirm(`Delete ${selection.path} from this workspace${scope}?`)
    )
      return;

    const deleted = selection.path;
    cancelFormatTasks();
    try {
      await API.DeleteWorkspacePath(workspace.id, deleted);
      for (const path of Object.keys(documentRevisions.current)) {
        if (path === deleted || path.startsWith(`${deleted}/`)) delete documentRevisions.current[path];
      }
      for (const path of Object.keys(diagnosticsByPath.current)) {
        if (path === deleted || path.startsWith(`${deleted}/`)) delete diagnosticsByPath.current[path];
      }
      setFileSeverity(current => {
        const next = { ...current };
        for (const path of Object.keys(next)) {
          if (path === deleted || path.startsWith(`${deleted}/`)) delete next[path];
        }
        return next;
      });
      const remaining = documents.filter(
        (document) =>
          document.path !== deleted && !document.path.startsWith(`${deleted}/`),
      );
      setDocuments(remaining);
      if (
        activeTab?.kind === "file" &&
        (activeTab.path === deleted || activeTab.path.startsWith(`${deleted}/`))
      ) {
        const nextDocument = remaining[remaining.length - 1];
        if (nextDocument) {
          setActivePath(nextDocument.path);
          setActiveTab({ kind: "file", path: nextDocument.path });
          setTreeSelection({ path: nextDocument.path, kind: "file" });
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
    if (!workspace) return;
    void API.RevealWorkspacePath(workspace.id, selection.path).catch(onError);
  };


  const rememberSearchFocus = () => {
    const focused = document.activeElement;
    searchReturnFocus.current =
      focused instanceof HTMLElement ? focused : null;
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
        new RegExp(
          query,
          workspaceSearchCaseSensitive ? "g" : "gi",
        );
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
      const target =
        event.target instanceof Element ? event.target : null;
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
  }, [
    activeDocument?.content,
    activeDocument?.path,
    pendingEditorReveal,
  ]);
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
  }, [
    activeFileSearchMatch?.from,
    activeFileSearchMatch?.to,
    fileSearchOpen,
  ]);

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
    if (!workspace) return;
    setBusy("validate");
    try {
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
    if (!workspace) return;
    setBusy("export");
    try {
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
    const exports = detail?.exports ?? [];
    if (!workspace || exports.length === 0) return;
    setBusy("install");
    try {
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
    if (!workspace) return;
    setBusy("uninstall");
    try {
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
    if (!workspace) return;
    setBusy("launch");
    try {
      const launch = await API.LaunchBeamNG(workspace.id);
      onNotify(`BeamNG launched as process ${launch.pid}`, "success");
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const analyzeRuntime = async () => {
    if (!workspace) return;
    setBusy("runtime");
    try {
      setRuntime(await API.AnalyzeRuntime(workspace.id));
    } catch (error) {
      onError(error);
    } finally {
      setBusy("");
    }
  };

  const cloneSelectedVariant = async () => {
    if (!workspace || !activePath.toLowerCase().endsWith(".pc")) return;
    const baseName = window
      .prompt("New variant basename (without .pc)")
      ?.trim();
    if (!baseName) return;
    const displayName =
      window.prompt("New variant display name", baseName)?.trim() ?? baseName;

    try {
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
    if (!workspace) return;
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

  const updateSession = (
    record: VirgilSessionRecord,
    replaceID?: string,
  ): boolean => {
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
        setActivePath(nextDocument.path);
        setTreeSelection({ path: nextDocument.path, kind: "file" });
        return { kind: "file", path: nextDocument.path };
      }
      return null;
    });
  };

  const closeSession = async (id: string) => {
    const tab = sessionTabsRef.current.find((item) => item.record.id === id);
    if (!tab) return;
    const workspaceID = tab.record.workspaceId;
    if (
      !tab.transient &&
      !window.confirm(
        `Forget Virgil session "${tab.record.title || "Virgil session"}"?`,
      )
    )
      return;
    if (tab.transient) {
      if (tab.busy) closedTransientSessions.current.add(id);
      removeSessionTab(id);
      onNotify("Virgil session closed", "info");
      return;
    }
    try {
      await API.ForgetVirgilSession(id);
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
    const tab = sessionTabsRef.current.find((item) => item.record.id === id);
    if (!tab || (tab.transient && tab.busy)) return;
    const title = window
      .prompt(
        "Rename Virgil session",
        tab.record.userTitle || tab.record.title || "Virgil session",
      )
      ?.trim();
    if (!title) return;
    try {
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
        const accepted = updateSession(
          await API.RenameVirgilSession(id, title),
        );
        await onReload();
        if (!accepted) return;
      }
      setSessionMenu(null);
      onNotify("Virgil session renamed", "success");
    } catch (error) {
      onError(error);
    }
  };

  const configureVirgil = async (
    enabled: boolean,
    source: "choice",
  ) => {
    if (!workspace) return;
    const workspaceID = workspace.id;
    const requestVersion = ++preferenceRequestVersion.current;
    setPreferenceBusy(true);
    try {
      await API.ConfigureWorkspaceVirgil(workspaceID, enabled);
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
    setUtility((current) => (current === tool ? null : tool));
  };

  if (!selectedID) {
    return (
      <section className="project-browser-view">
        <ProjectBrowser
          workspaces={workspaces}
          allItems={allItems}
          onOpen={onSelect}
          onNew={() => setNewModOpen(true)}
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
                defaultAuthor={defaultAuthor}
                onCreate={async (request, prompt, model) => {
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
        locked={Boolean(activeSession.busy)}
        prompt={sessionPrompts[activeSession.record.id] ?? ""}
        activeFilePath={activePath}
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
        onSessionChange={(record) =>
          updateSession(
            record,
            activeSession.transient ? activeSession.record.id : undefined,
          )
        }
        onReload={onReload}
        onNotify={onNotify}
        onError={onError}
        onBusyChange={(busy) =>
          setSessionTabBusy(activeSession.record.id, busy)
        }
      />
    ) : activeDocument ? (
      <>
        <header className="source-editor__header">
          <div>
            <code>{activeDocument.path}</code>
            {activeDocument.restored && (
              <Badge tone="cyan">Draft restored</Badge>
            )}
            {activeDocument.content !== activeDocument.savedContent && (
              <Badge tone="warning">Unsaved</Badge>
            )}
            {activeDocument.externalContent !== undefined && (
              <Badge tone="warning">Changed by Virgil</Badge>
            )}
          </div>
          <div>
            {activePath.toLowerCase().endsWith(".pc") && (
              <Button
                tone="quiet"
                icon="copy"
                onClick={() => void cloneSelectedVariant()}
              >
                Clone variant
              </Button>
            )}
            {activeDocument.externalContent !== undefined && (
              <Button
                tone="quiet"
                icon="refresh"
                onClick={reloadExternalChange}
              >
                Reload
              </Button>
            )}
            <Button
              className={
                activeDocument.content === activeDocument.savedContent &&
                busy !== "save"
                  ? "source-save-button--saved"
                  : ""
              }
              tone={
                activeDocument.content !== activeDocument.savedContent
                  ? "primary"
                  : "default"
              }
              icon="save"
              disabled={
                activeDocument.content === activeDocument.savedContent ||
                busy !== ""
              }
              aria-label={
                busy === "save"
                  ? "Saving…"
                  : activeDocument.content !== activeDocument.savedContent
                    ? "Save"
                    : "Saved"
              }
              onClick={() => void saveFile()}
            >
              {busy === "save"
                ? "Saving…"
                : activeDocument.content !== activeDocument.savedContent
                  ? "Save"
                  : "Saved"}
            </Button>
          </div>
        </header>
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
          path={activeDocument.path}
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

  const fileTabItems: IndexCardTabItem[] = documents.map((document) => ({
    id: `file:${document.path}`,
    label: (
      <>
        {document.path.split("/").pop()}
        {document.content !== document.savedContent && (
          <i className="modmaker-tab__dirty" aria-label="Unsaved" />
        )}
      </>
    ),
    icon: <Icon name="files" size={14} />,
    panel: null,
    onClose: () => closeDocument(document.path),
    closeLabel: `Close ${document.path}`,
    title: document.path,
  }));
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
  const tabItems: IndexCardTabItem[] = [
    ...fileTabItems,
    ...sessionTabs.map((tab) => {
      const status = tab.record.status || "idle";
      return {
        id: `session:${tab.record.id}`,
        label: (
          <>
            {tab.record.title || "Virgil session"}
            <i
              className={`modmaker-tab__status modmaker-tab__status--${status}`}
              aria-label={status}
            />
          </>
        ),
        icon: <ReplaceableUIPlaceholder entity="virgil-logo" />,
        panel: null,
        onClose: () => {
          void closeSession(tab.record.id);
        },
        closeLabel: `Close ${tab.record.title || "Virgil session"}`,
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
              Math.min(
                event.clientX,
                window.innerWidth - menuWidth - margin,
              ),
            ),
            y: Math.max(
              margin,
              Math.min(
                event.clientY,
                window.innerHeight - menuHeight - margin,
              ),
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
    <section className="maker-shell">
      <main ref={workspaceMainRef} className="maker-main">
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
        </header>

        <nav className="workspace-tool-toggles" aria-label="Workspace tools">
          {workspaceTools.map((tool) => (
            <button
              type="button"
              key={tool.key}
              className={utility === tool.key ? "is-active" : ""}
              aria-pressed={utility === tool.key}
              onClick={() => toggleUtility(tool.key)}
            >
              <Icon name={tool.icon} size={14} />
              {tool.label}
            </button>
          ))}
        </nav>

        <div className={`maker-content${utility ? " has-utility" : ""}`}>
          <div className="ide-workspace">
            <div
              className={`editor-layout${fileBrowserResizing ? " is-resizing" : ""}`}
              ref={editorLayoutRef}
              style={
                { "--file-browser-width": `${fileBrowserWidth}px` } as React.CSSProperties
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
                    onClick={() => void createFile()}
                    title="New File"
                    aria-label="New File"
                  >
                    <Icon name="filePlus" size={16} />
                  </button>
                  <button
                    type="button"
                    className="icon-button"
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
                      editorInteractionVersion.current += 1;
                      cancelFormatTasks();
                      setActivePath(path);
                      setActiveTab({ kind: "file", path });
                      setTreeSelection({ path, kind: "file" });
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
                  actions={
                    <Button
                      type="button"
                      icon="plus"
                      disabled={preferenceBusy || busy !== ""}
                      onClick={openNewSession}
                    >
                      New Virgil Session
                    </Button>
                  }
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
                    disabled={Boolean(contextTab.busy)}
                    onClick={() => void renameSession(contextTab.record.id)}
                  >
                    Rename
                  </button>
                  <span />
                  <button
                    type="button"
                    role="menuitem"
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
              aria-label={`Find in ${activeDocument.path}`}
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
                  aria-label={`Find in ${activeDocument.path}`}
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
                        workspaceSearchLoading ||
                        !workspaceSearchQuery.trim()
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
                          setWorkspaceSearchCaseSensitive(
                            event.target.checked,
                          );
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

          {utility && (
            <div className="workspace-utility-drawer">
              <WorkspaceUtilityPanel
                active={utility}
                detail={loadedDetail}
                changes={changes}
                diffLoading={diffLoading}
                runtime={runtime}
                exportLabel={exportLabel}
                busy={busy}
                onClose={() => setUtility(null)}
                onExportLabelChange={setExportLabel}
                onRefreshDiff={refreshDiff}
                onValidate={validate}
                onExport={exportWorkspace}
                onInstall={installLatest}
                onUninstall={uninstallTest}
                onLaunch={launchGame}
                onAnalyze={analyzeRuntime}
              />
            </div>
          )}
        </div>
      </main>

      {!loadedWorkspace.virgilConfigured && (
        <VirgilChoiceDialog
          modName={loadedDetail.entity.displayName}
          busy={preferenceBusy}
          onChoose={(enabled) => void configureVirgil(enabled, "choice")}
        />
      )}
    </section>
  );
}

function NewModStart({
  defaultAuthor,
  onCreate,
  onError,
}: {
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
              disabled={!prompt.trim() || creating}
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
              !name.trim() || !modID.trim() || !version.trim() || creating
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
