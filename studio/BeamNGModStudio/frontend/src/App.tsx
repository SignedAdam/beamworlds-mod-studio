import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  AIUsage,
  AgentActivity,
  AppConfig,
  AppSettings,
  ArchiveMemberPreview,
  Dashboard,
  EntityDetail,
  LibraryItem,
  LibraryItemDetailsUpdate,
  LibraryVariantUpdate,
  ModTag,
  NewModRequest,
  OrganizationState,
  ScanProgress,
  SettingsUpdate,
  SetupState,
  WorkspaceDetail,
  WorkspaceRecord,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { ActivityView } from "./ActivityView";
import { Inspector } from "./Inspector";
import { LibraryView } from "./LibraryView";
import { ModMaker } from "./ModMaker";
import { CollectionsView } from "./CollectionsView";
import { PlayView } from "./PlayView";
import { usePlaySession } from "./usePlaySession";
import { VirusScannerView } from "./VirusScannerView";
import { SettingsView, settingsUpdate } from "./SettingsView";
import { SetupWizard } from "./SetupWizard";
import { BeamWorldsMark, Icon } from "./icons";
import { formatBytes, formatDate } from "./ui";
import "./AppArt.css";

type View =
  "library" | "collections" | "play" | "workspaces" | "scanner" | "activity" | "settings";
type ToastTone = "success" | "error" | "info";
type InterfaceSize = "compact" | "default" | "comfortable" | "large";
type TextSize = "small" | "default" | "large" | "extra-large";

const INTERFACE_SIZE_STORAGE_KEY = "beamworlds.interface-size";
const TEXT_SIZE_STORAGE_KEY = "beamworlds.text-size";
const INSPECTOR_WIDTH_STORAGE_KEY = "beamworlds.inspector-width";
const INSPECTOR_DEFAULT_WIDTH = 480;
const INSPECTOR_MIN_WIDTH = 320;
const INSPECTOR_MAX_WIDTH = 720;

function readInspectorWidth(): number {
  try {
    const raw = window.localStorage.getItem(INSPECTOR_WIDTH_STORAGE_KEY);
    if (raw === null || raw.trim() === "") return INSPECTOR_DEFAULT_WIDTH;
    const stored = Number(raw);
    if (Number.isFinite(stored))
      return Math.max(INSPECTOR_MIN_WIDTH, Math.min(INSPECTOR_MAX_WIDTH, Math.round(stored)));
  } catch {
    // The default remains authoritative when storage is unavailable.
  }
  return INSPECTOR_DEFAULT_WIDTH;
}

function normalizeInterfaceSize(value: unknown): InterfaceSize {
  return value === "compact" ||
    value === "comfortable" ||
    value === "large"
    ? value
    : "default";
}

function normalizeTextSize(value: unknown): TextSize {
  return value === "small" || value === "large" || value === "extra-large"
    ? value
    : "default";
}

function mirrorSizing(interfaceSize: unknown, textSize: unknown) {
  try {
    window.localStorage.setItem(
      INTERFACE_SIZE_STORAGE_KEY,
      normalizeInterfaceSize(interfaceSize),
    );
    window.localStorage.setItem(
      TEXT_SIZE_STORAGE_KEY,
      normalizeTextSize(textSize),
    );
  } catch {
    // Sizing remains authoritative in the backend when storage is unavailable.
  }
}

interface ToastState {
  message: string;
  tone: ToastTone;
  visible: boolean;
}

interface EditorStatus {
  path: string;
  dirty: boolean;
  sizeBytes: number;
}

function App() {
  const [view, setView] = useState<View>("library");
  // Bumped on every accepted view change so the incoming page animates in
  // without remounting it: the two classes alternate, restarting the keyframes.
  const [viewSwap, setViewSwap] = useState(0);
  const [config, setConfig] = useState<AppConfig | null>(null);
  const [settings, setSettings] = useState<AppSettings | null>(null);
  const [usage, setUsage] = useState<AIUsage | null>(null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(
    () =>
      window.localStorage.getItem("beamworlds.sidebar-collapsed") === "true",
  );
  const [setupState, setSetupState] = useState<SetupState | null>(null);
  const [inspectorPreferredWidth, setInspectorPreferredWidth] = useState(readInspectorWidth);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  const [setupOpen, setSetupOpen] = useState(false);
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [lastSuccessfulScanAt, setLastSuccessfulScanAt] = useState("");
  const [items, setItems] = useState<LibraryItem[]>([]);
  const [libraryLoading, setLibraryLoading] = useState(false);
  const [workspaces, setWorkspaces] = useState<WorkspaceRecord[]>([]);
  const [allItems, setAllItems] = useState<LibraryItem[]>([]);
  const [organization, setOrganization] = useState<OrganizationState | null>(
    null,
  );
  const [collectionID, setCollectionID] = useState("all");
  const [openedCollectionID, setOpenedCollectionID] = useState("");
  const [query, setQuery] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const [selectedItem, setSelectedItem] = useState<LibraryItem | null>(null);
  const [entityDetail, setEntityDetail] = useState<EntityDetail | null>(null);
  const [entityLoading, setEntityLoading] = useState(false);
  const [inspectorStale, setInspectorStale] = useState(false);
  const [creatingWorkspace, setCreatingWorkspace] = useState(false);
  const [selectedWorkspaceID, setSelectedWorkspaceID] = useState("");
  const [workspaceDetail, setWorkspaceDetail] =
    useState<WorkspaceDetail | null>(null);
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [workspaceStale, setWorkspaceStale] = useState(false);
  const [editorStatus, setEditorStatus] = useState<EditorStatus>({
    path: "",
    dirty: false,
    sizeBytes: 0,
  });
  const [scan, setScan] = useState<ScanProgress | null>(null);
  const [scanning, setScanning] = useState(false);
  const [writeBlocked, setWriteBlocked] = useState(false);
  const [virusScanRequest, setVirusScanRequest] = useState<{
    entityIDs: string[];
    nonce: number;
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [toast, setToast] = useState<ToastState>({
    message: "",
    tone: "info",
    visible: false,
  });
  const toastTimer = useRef<number>();
  const autoScanStarted = useRef(false);
  const libraryLoadVersion = useRef(0);
  const organizationLoadVersion = useRef(0);
  const virusScanRequestVersion = useRef(0);
  const workspaceDetailLoadVersion = useRef(0);
  const agentListRefreshTimer = useRef<number>();
  const agentDetailRefreshTimer = useRef<number>();
  const agentActivityBuffer = useRef<Record<string, AgentActivity[]>>({});
  const selectedItemRef = useRef<LibraryItem | null>(null);
  const inspectorBaselineRef = useRef<{
    entityID: string;
    revision: string;
  } | null>(null);
  const inspectorStaleRef = useRef(false);
  const workspaceBaselineRef = useRef<{
    workspaceID: string;
    entityID: string;
    revision: string;
  } | null>(null);
  const workspaceStaleRef = useRef(false);
  const writeBlockedRef = useRef(false);
  const scanInFlightRef = useRef(false);
  const scanRunVersionRef = useRef(0);
  const selectedWorkspaceIDRef = useRef("");
  const workspaceSetupID = useRef("");
  const inspectorDirtyRef = useRef(false);
  const isWriteBlocked = useCallback(() => writeBlockedRef.current, []);
  const inspectorMaxWidth = Math.min(
    INSPECTOR_MAX_WIDTH,
    Math.max(INSPECTOR_MIN_WIDTH, Math.floor(viewportWidth * 0.6)),
  );
  const inspectorWidth = Math.max(
    INSPECTOR_MIN_WIDTH,
    Math.min(inspectorMaxWidth, inspectorPreferredWidth),
  );
  const inspectorResizeDisabled = viewportWidth < 720;
  const commitInspectorResize = useCallback(
    (width: number) => {
      const next = Math.max(
        INSPECTOR_MIN_WIDTH,
        Math.min(INSPECTOR_MAX_WIDTH, Math.round(width)),
      );
      setInspectorPreferredWidth(next);
      try {
        window.localStorage.setItem(
          INSPECTOR_WIDTH_STORAGE_KEY,
          String(next),
        );
      } catch {
        // The in-memory preference remains authoritative when storage is unavailable.
      }
    },
    [],
  );
  const resetInspectorWidth = useCallback(() => {
    commitInspectorResize(INSPECTOR_DEFAULT_WIDTH);
  }, [commitInspectorResize]);
  const advanceInspectorBaseline = useCallback(
    (item: LibraryItem, onlyIfNewer = false) => {
      if (inspectorStaleRef.current) return;
      const selected = selectedItemRef.current;
      if (!selected || selected.entityId !== item.entityId) return;
      const baseline = inspectorBaselineRef.current;
      if (
        baseline &&
        baseline.entityID === item.entityId &&
        baseline.revision &&
        item.revision < baseline.revision
      )
        return;
      if (
        onlyIfNewer &&
        baseline &&
        baseline.entityID === item.entityId &&
        baseline.revision &&
        item.revision <= baseline.revision
      )
        return;
      inspectorBaselineRef.current = {
        entityID: item.entityId,
        revision: item.revision,
      };
    },
    [],
  );

  const notify = useCallback((message: string, tone: ToastTone = "info") => {
    setToast({ message, tone, visible: true });
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(
      () => setToast((current) => ({ ...current, visible: false })),
      5000,
    );
  }, []);
  const handleError = useCallback(
    (error: unknown) => {
      let message = "Unexpected application error";
      if (error instanceof Error) message = error.message;
      else if (typeof error === "string") message = error;
      else if (
        error &&
        typeof error === "object" &&
        "message" in error &&
        typeof error.message === "string"
      )
        message = error.message;
      notify(message, "error");
    },
    [notify],
  );
  const confirmInspectorLeave = useCallback(() => {
    if (!inspectorDirtyRef.current) return true;
    return window.confirm("Discard unsaved inspector changes?");
  }, []);

  useEffect(() => {
    const handleResize = () => setViewportWidth(window.innerWidth);
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);


  useEffect(() => {
    selectedItemRef.current = selectedItem;
  }, [selectedItem]);

  useEffect(() => {
    inspectorStaleRef.current = inspectorStale;
  }, [inspectorStale]);

  useEffect(() => {
    workspaceStaleRef.current = workspaceStale;
  }, [workspaceStale]);

  useEffect(() => {
    selectedWorkspaceIDRef.current = selectedWorkspaceID;
  }, [selectedWorkspaceID]);
  const loadShell = useCallback(async (updateLastSuccessful = true) => {
    const [nextConfig, nextDashboard, workspaceResult] = await Promise.all([
      API.Config(),
      API.Dashboard(),
      API.ListWorkspaces(),
    ]);
    const nextWorkspaces = workspaceResult ?? [];
    setConfig(nextConfig);
    setDashboard(nextDashboard);
    if (updateLastSuccessful && nextDashboard.lastSuccessfulScanAt)
      setLastSuccessfulScanAt(nextDashboard.lastSuccessfulScanAt);
    setWorkspaces(nextWorkspaces);
    return nextDashboard;
  }, []);
  const loadLibrary = useCallback(
    async (
      requestedQuery = query,
      requestedCollectionID = collectionID,
      requestVersion = libraryLoadVersion.current,
    ) => {
      const nextItems =
        (await API.ListLibrary(
          "all",
          "all",
          requestedQuery,
          requestedCollectionID,
        )) ?? [];
      if (requestVersion !== libraryLoadVersion.current) return;
      setItems(nextItems);
    },
    [collectionID, query],
  );
  const loadOrganization = useCallback(async () => {
    const version = ++organizationLoadVersion.current;
    const [nextOrganization, nextItems] = await Promise.all([
      API.Organization(),
      API.ListLibrary("all", "all", "", "all"),
    ]);
    if (version !== organizationLoadVersion.current) return;
    setOrganization(nextOrganization);
    setAllItems(nextItems ?? []);
  }, []);
  const handleOrganizationChange = useCallback((state: OrganizationState) => {
    setOrganization(state);
    void Promise.all([loadOrganization(), loadLibrary()]).catch(handleError);
  }, [loadOrganization, loadLibrary, handleError]);
  const playSession = usePlaySession(organization, handleOrganizationChange, handleError);
  // Held in a ref so callbacks can reach the session without taking on its
  // identity, which changes on every selection or preview update.
  const playSessionRef = useRef(playSession);
  playSessionRef.current = playSession;
  useEffect(() => {
    if (!organization) return;
    const known = organization.collections ?? [];
    if (collectionID !== "all" && collectionID !== "unfiled" &&
        !known.some((collection) => collection.id === collectionID)) setCollectionID("all");
    if (openedCollectionID && !known.some((collection) => collection.id === openedCollectionID)) setOpenedCollectionID("");
  }, [organization, collectionID, openedCollectionID]);
  const markOpenEntitiesStale = useCallback((snapshot: LibraryItem[]) => {
    const byEntityID: Record<string, LibraryItem> = {};
    for (const item of snapshot) byEntityID[item.entityId] = item;
    const inspectorBaseline = inspectorBaselineRef.current;
    if (
      inspectorBaseline?.entityID &&
      selectedItemRef.current?.entityId === inspectorBaseline.entityID &&
      inspectorBaselineRef.current === inspectorBaseline
    ) {
      const item = byEntityID[inspectorBaseline.entityID];
      if (!item || item.revision !== inspectorBaseline.revision) {
        inspectorStaleRef.current = true;
        setInspectorStale(true);
      }
    }
    const workspaceBaseline = workspaceBaselineRef.current;
    if (
      workspaceBaseline?.entityID &&
      selectedWorkspaceIDRef.current === workspaceBaseline.workspaceID &&
      workspaceBaselineRef.current === workspaceBaseline
    ) {
      const item = byEntityID[workspaceBaseline.entityID];
      if (!item || item.revision !== workspaceBaseline.revision) {
        workspaceStaleRef.current = true;
        setWorkspaceStale(true);
      }
    }
  }, []);
  const reloadWorkspace = useCallback(async () => {
    if (workspaceStaleRef.current) return;
    const workspaceID = selectedWorkspaceIDRef.current || selectedWorkspaceID;
    if (!workspaceID) return;
    const requestVersion = ++workspaceDetailLoadVersion.current;
    const [nextDetail, workspaceResult, nextDashboard] = await Promise.all([
      API.GetWorkspace(workspaceID),
      API.ListWorkspaces(),
      API.Dashboard(),
    ]);
    if (
      requestVersion === workspaceDetailLoadVersion.current &&
      selectedWorkspaceIDRef.current === workspaceID &&
      !workspaceStaleRef.current
    ) {
      setWorkspaceDetail(nextDetail);
    }
    setWorkspaces(workspaceResult ?? []);
    setDashboard(nextDashboard);
    if (nextDashboard.lastSuccessfulScanAt)
      setLastSuccessfulScanAt(nextDashboard.lastSuccessfulScanAt);
  }, [selectedWorkspaceID]);

  const startScan = useCallback(async () => {
    if (scanInFlightRef.current) return;
    scanInFlightRef.current = true;
    const scanRunVersion = ++scanRunVersionRef.current;
    setScanning(true);
    setScan((current) =>
      current
        ? { ...current, done: false, error: "" }
        : {
            scanId: "",
            phase: "discovering",
            path: "",
            discovered: 0,
            analyzed: 0,
            cached: 0,
            failed: 0,
            done: false,
          },
    );
    let successful = false;
    try {
      const summary = await API.ScanLibrary();
      successful = !summary.cancelled && !summary.error;
      if (!successful) {
        scanInFlightRef.current = false;
        setScanning(false);
      }
      if (successful) {
        writeBlockedRef.current = true;
        setWriteBlocked(true);
        try {
          const committedItems =
            (await API.ListLibrary("all", "all", "", "all")) ?? [];
          markOpenEntitiesStale(committedItems);
        } catch (error) {
          handleError(error);
        } finally {
          writeBlockedRef.current = false;
          setWriteBlocked(false);
        }
      }
      notify(
        `Processed ${summary.analyzed.toLocaleString()} archives${summary.cached > 0 ? ` · ${summary.cached.toLocaleString()} cached` : ""}`,
        summary.failed ? "info" : "success",
      );
    } catch (error) {
      scanInFlightRef.current = false;
      setScanning(false);
      if (
        !(error instanceof Error) ||
        !error.message.toLowerCase().includes("canceled")
      )
        handleError(error);
    }

    try {
      const [libraryResult, shellResult, organizationResult] =
        await Promise.allSettled([
          loadLibrary(),
          loadShell(successful),
          loadOrganization(),
        ]);
      if (libraryResult.status === "rejected")
        handleError(libraryResult.reason);
      if (shellResult.status === "rejected")
        handleError(shellResult.reason);
      if (organizationResult.status === "rejected")
        handleError(organizationResult.reason);
      // The first indexed library may have seeded a collection and profile on
      // the backend; pick that up instead of leaving the pre-scan empty state.
      if (successful) await playSessionRef.current?.reloadState().catch(handleError);
    } catch (error) {
      handleError(error);
    } finally {
      if (scanRunVersionRef.current === scanRunVersion) {
        scanInFlightRef.current = false;
        setScanning(false);
      }
    }
  }, [
    handleError,
    loadLibrary,
    loadOrganization,
    loadShell,
    markOpenEntitiesStale,
    notify,
  ]);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const nextSetup = await API.GetSetupState();
        if (!active) return;
        setSetupState(nextSetup);
        if (nextSetup.required) {
          setLoading(false);
          return;
        }
        const nextDashboard = await API.Dashboard();
        if (!active) return;
        setDashboard(nextDashboard);
        if (nextDashboard.lastSuccessfulScanAt)
          setLastSuccessfulScanAt(nextDashboard.lastSuccessfulScanAt);
        const [
          nextConfig,
          workspaceResult,
          itemResult,
          nextOrganization,
          nextSettings,
          nextUsage,
        ] = await Promise.all([
          API.Config(),
          API.ListWorkspaces(),
          API.ListLibrary("all", "all", "", "all"),
          API.Organization(),
          API.Settings(),
          API.AIUsage(),
        ]);
        if (!active) return;
        const nextWorkspaces = workspaceResult ?? [];
        setConfig(nextConfig);
        setWorkspaces(nextWorkspaces);
        setItems(itemResult ?? []);
        setAllItems(itemResult ?? []);
        setOrganization(nextOrganization);
        setSettings(nextSettings);
        setUsage(nextUsage);
        setLoading(false);
        if (nextDashboard.entities === 0 && !autoScanStarted.current) {
          autoScanStarted.current = true;
          void startScan();
        }
      } catch (error) {
        if (active) {
          setLoading(false);
          handleError(error);
        }
      }
    })();
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!setupState || setupState.required) return;
    const trimmedInput = searchInput.trim();
    if (/^(?:tag|tags):$/i.test(trimmedInput)) return;
    const requestVersion = ++libraryLoadVersion.current;
    setLibraryLoading(true);
    if (!trimmedInput) {
      setQuery("");
      void loadLibrary("", collectionID, requestVersion)
        .catch((error) => {
          if (requestVersion === libraryLoadVersion.current) handleError(error);
        })
        .finally(() => {
          if (requestVersion === libraryLoadVersion.current)
            setLibraryLoading(false);
        });
      return;
    }
    const timer = window.setTimeout(() => {
      setQuery(searchInput);
      void loadLibrary(searchInput, collectionID, requestVersion)
        .catch((error) => {
          if (requestVersion === libraryLoadVersion.current) handleError(error);
        })
        .finally(() => {
          if (requestVersion === libraryLoadVersion.current)
            setLibraryLoading(false);
        });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [collectionID, searchInput, setupState?.required]);

  useEffect(() => {
    const root = document.documentElement;
    root.dataset.theme = settings?.theme ?? "dark";
    if (!settings) return;
    const interfaceSize = normalizeInterfaceSize(settings.interfaceSize);
    const textSize = normalizeTextSize(settings.textSize);
    root.dataset.interfaceSize = interfaceSize;
    root.dataset.textSize = textSize;
    mirrorSizing(interfaceSize, textSize);
    const colors: Record<string, string> = {
      "--user-emphasis": settings.emphasisColor,
      "--user-active-tab": settings.activeTabColor,
      "--user-subsection-title": settings.subsectionTitleColor,
      "--user-dark-surface": settings.darkSurfaceColor,
      "--user-dark-border": settings.darkBorderColor,
      "--user-dark-text": settings.darkTextColor,
      "--user-light-surface": settings.lightSurfaceColor,
      "--user-light-border": settings.lightBorderColor,
      "--user-light-text": settings.lightTextColor,
      "--user-scrollbar-color": settings.scrollbarColor,
    };
    for (const [name, value] of Object.entries(colors))
      root.style.setProperty(name, value);
  }, [settings]);

  useEffect(() => {
    const stopScan = Events.On("library:scan", (event) => {
      setScan(event.data);
      if (!event.data.done) setScanning(true);
      else if (event.data.error) setScanning(false);
    });
    const stopItem = Events.On("library:item", (event) => {
      setItems((current) => {
        const index = current.findIndex(
          (item) => item.entityId === event.data.entityId,
        );
        if (index < 0) return [event.data, ...current];
        const next = [...current];
        next[index] = event.data;
        return next;
      });
    });
    return () => {
      stopScan();
      stopItem();
      clearTimeout(toastTimer.current);
    };
  }, []);

  useEffect(() => {
    let active = true;
    const refreshWorkspaceList = async () => {
      try {
        const next = await API.ListWorkspaces();
        if (active) setWorkspaces(next ?? []);
      } catch (error) {
        if (active) handleError(error);
      }
    };
    const refreshSelectedWorkspace = async () => {
      if (workspaceStaleRef.current) return;
      const workspaceID = selectedWorkspaceIDRef.current;
      if (!workspaceID) return;
      const requestVersion = ++workspaceDetailLoadVersion.current;
      try {
        const next = await API.GetWorkspace(workspaceID);
        if (
          active &&
          requestVersion === workspaceDetailLoadVersion.current &&
          selectedWorkspaceIDRef.current === workspaceID &&
          !workspaceStaleRef.current
        ) {
          setWorkspaceDetail(next);
        }
      } catch (error) {
        if (active && requestVersion === workspaceDetailLoadVersion.current)
          handleError(error);
      }
    };
    const stopAgent = Events.On("agent:event", (event) => {
      const activity = event.data as AgentActivity & {
        sessionId?: string;
        workspaceId?: string;
      };
      if (!activity.runId) return;
      bufferAgentActivity(agentActivityBuffer.current, activity);
      const updatesWorkspaceSummary = ![
        "message_update",
        "message_start",
        "tool_execution_update",
      ].includes(activity.type);
      if (
        updatesWorkspaceSummary &&
        agentListRefreshTimer.current === undefined
      ) {
        agentListRefreshTimer.current = window.setTimeout(() => {
          agentListRefreshTimer.current = undefined;
          void refreshWorkspaceList();
        }, 180);
      }
      const workspaceChanged =
        activity.type === "finished" ||
        (activity.type === "host_tool_end" &&
          [
            "workspace_write",
            "workspace_replace",
            "session_set_title",
          ].includes(activity.toolName ?? ""));
      const selectedWorkspaceChanged =
        !activity.workspaceId || activity.workspaceId === selectedWorkspaceID;
      if (
        workspaceChanged &&
        selectedWorkspaceID &&
        selectedWorkspaceChanged &&
        agentDetailRefreshTimer.current === undefined
      ) {
        agentDetailRefreshTimer.current = window.setTimeout(() => {
          agentDetailRefreshTimer.current = undefined;
          void refreshSelectedWorkspace();
        }, 180);
      }
    });
    return () => {
      active = false;
      stopAgent();
      window.clearTimeout(agentListRefreshTimer.current);
      window.clearTimeout(agentDetailRefreshTimer.current);
      agentListRefreshTimer.current = undefined;
      agentDetailRefreshTimer.current = undefined;
    };
  }, [selectedWorkspaceID, handleError]);

  useEffect(() => {
    if (view !== "workspaces") return;
    if (!selectedWorkspaceID) {
      workspaceDetailLoadVersion.current += 1;
      workspaceBaselineRef.current = null;
      workspaceStaleRef.current = false;
      setWorkspaceStale(false);
      setWorkspaceDetail(null);
      setWorkspaceLoading(false);
      return;
    }
    if (workspaceDetail?.workspace.id === selectedWorkspaceID) {
      if (workspaceSetupID.current !== selectedWorkspaceID)
        setWorkspaceLoading(false);
      return;
    }

    const workspaceID = selectedWorkspaceID;
    const requestVersion = ++workspaceDetailLoadVersion.current;
    let cancelled = false;
    setWorkspaceLoading(true);
    API.GetWorkspace(workspaceID)
      .then((detail) => {
        if (
          cancelled ||
          requestVersion !== workspaceDetailLoadVersion.current ||
          selectedWorkspaceIDRef.current !== workspaceID
        )
          return;
        const baseline = workspaceBaselineRef.current;
        if (
          !workspaceStaleRef.current &&
          baseline &&
          baseline.workspaceID === workspaceID &&
          (!baseline.entityID || baseline.entityID === detail.entity.entityId) &&
          (!baseline.revision || detail.entity.revision > baseline.revision)
        ) {
          workspaceBaselineRef.current = {
            ...baseline,
            entityID: detail.entity.entityId,
            revision: detail.entity.revision,
          };
        }
        if (!workspaceStaleRef.current) setWorkspaceDetail(detail);
      })
      .catch((error) => {
        if (
          cancelled ||
          requestVersion !== workspaceDetailLoadVersion.current ||
          selectedWorkspaceIDRef.current !== workspaceID
        )
          return;
        handleError(error);
        selectedWorkspaceIDRef.current = "";
        setSelectedWorkspaceID("");
        setWorkspaceDetail(null);
      })
      .finally(() => {
        if (!cancelled && requestVersion === workspaceDetailLoadVersion.current)
          setWorkspaceLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [view, selectedWorkspaceID, workspaceDetail?.workspace.id]);

  useEffect(() => {
    if (view !== "workspaces")
      setEditorStatus({ path: "", dirty: false, sizeBytes: 0 });
  }, [view]);
  const selectItem = async (item: LibraryItem) => {
    if (
      inspectorStaleRef.current &&
      selectedItemRef.current?.entityId === item.entityId
    )
      return;
    if (selectedItemRef.current?.entityId !== item.entityId) {
      if (!confirmInspectorLeave()) return;
      inspectorDirtyRef.current = false;
    }
    inspectorBaselineRef.current = {
      entityID: item.entityId,
      revision: item.revision,
    };
    inspectorStaleRef.current = false;
    setInspectorStale(false);
    setSelectedItem(item);
    setEntityDetail(null);
    setEntityLoading(true);
    try {
      const nextDetail = await API.GetEntity(item.entityId);
      if (
        selectedItemRef.current?.entityId === item.entityId &&
        !inspectorStaleRef.current
      ) {
        advanceInspectorBaseline(nextDetail.item, true);
        setEntityDetail(nextDetail);
      }
    } catch (error) {
      handleError(error);
    } finally {
      if (selectedItemRef.current?.entityId === item.entityId)
        setEntityLoading(false);
    }
  };
  const selectWorkspace = (workspaceID: string) => {
    if (
      workspaceStaleRef.current &&
      selectedWorkspaceIDRef.current === workspaceID
    )
      return;
    const record = workspaces.find((workspace) => workspace.id === workspaceID);
    const item = record
      ? allItems.find((candidate) => candidate.entityId === record.entityId)
      : undefined;
    workspaceBaselineRef.current = {
      workspaceID,
      entityID: record?.entityId ?? item?.entityId ?? "",
      revision: item?.revision ?? "",
    };
    workspaceStaleRef.current = false;
    setWorkspaceStale(false);
    workspaceDetailLoadVersion.current += 1;
    selectedWorkspaceIDRef.current = workspaceID;
    setWorkspaceDetail(null);
    setSelectedWorkspaceID(workspaceID);
  };

  const createWorkspace = async () => {
    if (
      writeBlockedRef.current ||
      !selectedItem ||
      inspectorStaleRef.current
    )
      return;
    if (!confirmInspectorLeave()) return;
    const entityID = selectedItem.entityId;
    setCreatingWorkspace(true);
    try {
      const detail = await API.CreateWorkspace(entityID);
      if (
        inspectorStaleRef.current ||
        selectedItemRef.current?.entityId !== entityID
      )
        return;
      const nextWorkspaces = await API.ListWorkspaces();
      if (
        inspectorStaleRef.current ||
        selectedItemRef.current?.entityId !== entityID
      )
        return;
      workspaceBaselineRef.current = {
        workspaceID: detail.workspace.id,
        entityID: detail.entity.entityId,
        revision: detail.entity.revision,
      };
      workspaceStaleRef.current = false;
      setWorkspaceStale(false);
      workspaceDetailLoadVersion.current += 1;
      selectedWorkspaceIDRef.current = detail.workspace.id;
      setWorkspaceDetail(detail);
      setSelectedWorkspaceID(detail.workspace.id);
      setWorkspaces(nextWorkspaces ?? []);
      selectedItemRef.current = null;
      inspectorBaselineRef.current = null;
      inspectorStaleRef.current = false;
      setSelectedItem(null);
      setEntityDetail(null);
      inspectorDirtyRef.current = false;
      setInspectorStale(false);
      setView("workspaces");
      notify(`${detail.entity.displayName} mod workspace opened`, "success");
    } catch (error) {
      handleError(error);
    } finally {
      setCreatingWorkspace(false);
    }
  };
  const openVirusScanner = (item: LibraryItem) => {
    if (writeBlockedRef.current) return;
    if (
      inspectorStaleRef.current &&
      selectedItemRef.current?.entityId === item.entityId
    )
      return;
    if (!confirmInspectorLeave()) return;
    inspectorDirtyRef.current = false;
    setVirusScanRequest({
      entityIDs: [item.entityId],
      nonce: ++virusScanRequestVersion.current,
    });
    selectedItemRef.current = null;
    inspectorBaselineRef.current = null;
    inspectorStaleRef.current = false;
    setSelectedItem(null);
    setEntityDetail(null);
    setInspectorStale(false);
    setView("scanner");
  };

  const refreshAfterVirusScan = async () => {
    await Promise.all([loadLibrary(), loadOrganization(), loadShell()]);
  };

  const createNewMod = async (
    request: NewModRequest,
    virgilPrompt = "",
    modelOverride = "",
  ) => {
    if (writeBlockedRef.current) return;
    const detail = await API.CreateNewMod(request);
    const prompt = virgilPrompt.trim();
    const workspaceID = detail.workspace.id;
    if (prompt) {
      workspaceSetupID.current = workspaceID;
      setWorkspaceLoading(true);
    }

    // The workspace starts with the entity revision present when it opens.
    workspaceBaselineRef.current = {
      workspaceID,
      entityID: detail.entity.entityId,
      revision: detail.entity.revision,
    };
    workspaceStaleRef.current = false;
    setWorkspaceStale(false);
    selectedWorkspaceIDRef.current = workspaceID;
    workspaceDetailLoadVersion.current += 1;
    setWorkspaceDetail(detail);
    setSelectedWorkspaceID(workspaceID);
    setView("workspaces");

    if (prompt) {
      try {
        const configuredDetail = await API.ConfigureWorkspaceVirgil(
          workspaceID,
          true,
        );
        if (selectedWorkspaceIDRef.current === workspaceID) {
          workspaceDetailLoadVersion.current += 1;
          setWorkspaceDetail(configuredDetail);
        }
        const session = await API.StartVirgilSession(
          workspaceID,
          prompt,
          modelOverride,
          "",
        );
        if (selectedWorkspaceIDRef.current === workspaceID) {
          workspaceDetailLoadVersion.current += 1;
          setWorkspaceDetail((current) =>
            current?.workspace.id === workspaceID
              ? {
                  ...current,
                  virgilSessions: [
                    session,
                    ...(current.virgilSessions ?? []).filter(
                      (existing) => existing.id !== session.id,
                    ),
                  ],
                }
              : current,
          );
        }
        notify(
          `Virgil started on ${configuredDetail.entity.displayName}`,
          "info",
        );
        try {
          setUsage(await API.AIUsage());
        } catch (error) {
          handleError(error);
        }
      } catch (error) {
        handleError(error);
      } finally {
        if (workspaceSetupID.current === workspaceID)
          workspaceSetupID.current = "";
        if (selectedWorkspaceIDRef.current === workspaceID)
          setWorkspaceLoading(false);
      }
    } else {
      notify(`${detail.entity.displayName} mod created`, "success");
    }

    const [
      workspaceResult,
      dashboardResult,
      settingsResult,
      organizationResult,
    ] = await Promise.allSettled([
      API.ListWorkspaces(),
      API.Dashboard(),
      API.Settings(),
      loadOrganization(),
    ]);
    if (workspaceResult.status === "fulfilled")
      setWorkspaces(workspaceResult.value ?? []);
    else handleError(workspaceResult.reason);
    if (dashboardResult.status === "fulfilled") {
      setDashboard(dashboardResult.value);
      if (dashboardResult.value.lastSuccessfulScanAt)
        setLastSuccessfulScanAt(dashboardResult.value.lastSuccessfulScanAt);
    } else handleError(dashboardResult.reason);
    if (settingsResult.status === "fulfilled")
      setSettings(settingsResult.value);
    else handleError(settingsResult.reason);
    if (organizationResult.status === "rejected")
      handleError(organizationResult.reason);
  };

  const saveSettings = async (update: SettingsUpdate) => {
    try {
      const next = await API.SaveSettings(update);
      mirrorSizing(next.interfaceSize, next.textSize);
      setSettings(next);
      if (next.showAIUsage) setUsage(await API.AIUsage());
      notify("Settings saved", "success");
      return true;
    } catch (error) {
      handleError(error);
      return false;
    }
  };

  const createTag = async (
    name: string,
    color: string,
    icon: string,
  ): Promise<ModTag | null> => {
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    const next = await API.CreateModTag(name, color, icon);
    const createdTag =
      (next.tags ?? []).find(
        (tag) =>
          tag.name.localeCompare(name, undefined, { sensitivity: "base" }) ===
          0,
      ) ?? null;
    if (writeBlockedRef.current || inspectorStaleRef.current) return createdTag;
    setOrganization(next);
    notify(`Created tag ${name}`, "success");
    return createdTag;
  };
  const updateSelectedTag = (tagID: string, replacement: ModTag | null) => {
    if (inspectorStaleRef.current) return;
    const current = selectedItemRef.current;
    if (!current) return;
    const updateItem = (item: LibraryItem) => ({
      ...item,
      tags: replacement
        ? (item.tags ?? []).map((tag) => (tag.id === tagID ? replacement : tag))
        : (item.tags ?? []).filter((tag) => tag.id !== tagID),
    });
    const nextItem = updateItem(current);
    selectedItemRef.current = nextItem;
    setSelectedItem(nextItem);
    setItems((currentItems) =>
      currentItems.map((item) =>
        item.entityId === nextItem.entityId ? updateItem(item) : item,
      ),
    );
    setAllItems((currentItems) =>
      currentItems.map((item) =>
        item.entityId === nextItem.entityId ? updateItem(item) : item,
      ),
    );
    setEntityDetail((currentDetail) =>
      currentDetail?.item.entityId === nextItem.entityId
        ? { ...currentDetail, item: updateItem(currentDetail.item) }
        : currentDetail,
    );
    advanceInspectorBaseline(nextItem);
  };
  const updateTagVisual = async (
    tagID: string,
    color: string,
    icon: string,
  ): Promise<void> => {
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    if (!selectedItemRef.current) return;
    const entityID = selectedItemRef.current.entityId;
    const next = await API.UpdateModTagVisual(tagID, color, icon);
    if (writeBlockedRef.current || inspectorStaleRef.current) return;
    setOrganization(next);
    if (selectedItemRef.current?.entityId !== entityID)
      throw new Error("Inspector selection changed before the tag update completed.");
    const currentTag = selectedItemRef.current.tags?.find(
      (tag) => tag.id === tagID,
    );
    const updatedTag = (next.tags ?? []).find((tag) => tag.id === tagID);
    updateSelectedTag(
      tagID,
      updatedTag ??
        (currentTag
          ? { ...currentTag, color, icon }
          : { id: tagID, name: "", modCount: 0, color, icon }),
    );
  };
  const deleteTag = async (tagID: string): Promise<void> => {
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    if (!selectedItemRef.current) return;
    const next = await API.DeleteModTag(tagID);
    if (writeBlockedRef.current || inspectorStaleRef.current) return;
    setOrganization(next);
    setItems((currentItems) =>
      currentItems.map((item) => ({
        ...item,
        tags: (item.tags ?? []).filter((tag) => tag.id !== tagID),
      })),
    );
    setAllItems((currentItems) =>
      currentItems.map((item) => ({
        ...item,
        tags: (item.tags ?? []).filter((tag) => tag.id !== tagID),
      })),
    );
    updateSelectedTag(tagID, null);
    notify("Tag deleted", "success");
  };
  const setSelectedTags = async (tagIDs: string[]): Promise<ModTag[]> => {
    const current = selectedItemRef.current;
    if (!current) return [];
    const confirmedTags = [...(current.tags ?? [])];
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    const entityID = current.entityId;
    const next = await API.SetLibraryItemTags(entityID, tagIDs);
    const assignedTags = next.tags ?? [];
    if (writeBlockedRef.current || inspectorStaleRef.current) return confirmedTags;
    if (selectedItemRef.current?.entityId !== entityID) return assignedTags;
    advanceInspectorBaseline(next);
    selectedItemRef.current = next;
    setSelectedItem(next);
    setItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next : item)),
    );
    setAllItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next : item)),
    );
    setEntityDetail((currentDetail) =>
      currentDetail?.item.entityId === entityID
        ? { ...currentDetail, item: next }
        : currentDetail,
    );
    const previousTagIDs = new Set((current.tags ?? []).map((tag) => tag.id));
    const assignedTagIDs = new Set(assignedTags.map((tag) => tag.id));
    setOrganization((currentOrganization) => {
      if (!currentOrganization) return currentOrganization;
      const assignedByID = new Map(assignedTags.map((tag) => [tag.id, tag]));
      return {
        ...currentOrganization,
        tags: (currentOrganization.tags ?? []).map((tag) => {
          const wasAssigned = previousTagIDs.has(tag.id);
          const isAssigned = assignedTagIDs.has(tag.id);
          const nextModCount =
            tag.modCount + (isAssigned ? 1 : 0) - (wasAssigned ? 1 : 0);
          const assigned = assignedByID.get(tag.id);
          return {
            ...tag,
            ...(assigned ? { color: assigned.color, icon: assigned.icon } : {}),
            modCount: Math.max(0, nextModCount),
          };
        }),
      };
    });
    return assignedTags;
  };
  const saveDetails = async (
    update: LibraryItemDetailsUpdate,
  ): Promise<void> => {
    const current = selectedItemRef.current;
    if (!current) return;
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    const entityID = current.entityId;
    const next = await API.UpdateLibraryItemDetails(entityID, update);
    if (writeBlockedRef.current || inspectorStaleRef.current) return;
    if (selectedItemRef.current?.entityId !== entityID) return;
    advanceInspectorBaseline(next.item);
    selectedItemRef.current = next.item;
    setSelectedItem(next.item);
    setItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next.item : item)),
    );
    setAllItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next.item : item)),
    );
    setEntityDetail(next);
  };
  const saveVariant = async (update: LibraryVariantUpdate): Promise<void> => {
    const current = selectedItemRef.current;
    if (!current) return;
    if (writeBlockedRef.current || inspectorStaleRef.current)
      throw new Error("Inspector changes are locked while the selected mod is being rescanned.");
    const entityID = current.entityId;
    const next = await API.UpdateLibraryVariant(entityID, update);
    if (writeBlockedRef.current || inspectorStaleRef.current) return;
    if (selectedItemRef.current?.entityId !== entityID) return;
    advanceInspectorBaseline(next.item);
    selectedItemRef.current = next.item;
    setSelectedItem(next.item);
    setItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next.item : item)),
    );
    setAllItems((currentItems) =>
      currentItems.map((item) => (item.entityId === entityID ? next.item : item)),
    );
    setEntityDetail(next);
  };

  const previewMember = async (
    path: string,
  ): Promise<ArchiveMemberPreview | null> => {
    const current = selectedItemRef.current;
    if (!current) return null;
    try {
      return await API.PreviewLibraryArchiveMember(current.entityId, path);
    } catch (error) {
      handleError(error);
      return null;
    }
  };
  const cancelScan = async () => {
    try {
      await API.CancelScan();
    } catch (error) {
      handleError(error);
    }
  };

  const openSetup = async () => {
    try {
      setSetupState(await API.GetSetupState());
      setSetupOpen(true);
    } catch (error) {
      handleError(error);
    }
  };
  const changeView = (next: View) => {
    if (next === view) return true;
    if (view === "library" && next !== "library" && !confirmInspectorLeave())
      return false;
    inspectorDirtyRef.current = false;
    setView(next);
    setViewSwap((count) => count + 1);
    selectedItemRef.current = null;
    inspectorBaselineRef.current = null;
    inspectorStaleRef.current = false;
    setSelectedItem(null);
    setEntityDetail(null);
    setInspectorStale(false);
    if (next === "scanner") setVirusScanRequest(null);
    return true;
  };
  const openCollection = (id: string) => {
    if (changeView("collections")) setOpenedCollectionID(id);
  };
  const inspectCollectionMod = (entityID: string) => {
    if (!changeView("library")) return;
    const item = allItems.find((candidate) => candidate.entityId === entityID);
    if (item) void selectItem(item);
    else handleError("This mod is no longer in the current library. Refresh the library to inspect it.");
  };

  const toggleSidebar = () => {
    setSidebarCollapsed((current) => {
      window.localStorage.setItem(
        "beamworlds.sidebar-collapsed",
        String(!current),
      );
      return !current;
    });
  };

  const toggleTheme = () => {
    if (!settings) return;
    void saveSettings(
      settingsUpdate(settings, {
        theme: settings.theme === "dark" ? "light" : "dark",
      }),
    );
  };

  if (setupState && (setupState.required || setupOpen)) {
    return (
      <SetupWizard
        state={setupState}
        required={setupState.required}
        onCancel={setupState.required ? undefined : () => setSetupOpen(false)}
        onError={handleError}
      />
    );
  }

  const startupCount = dashboard?.entities ?? 0;
  const activeWorkspace = workspaces.find(
    (workspace) => workspace.id === selectedWorkspaceID,
  );
  const usageLimits =
    usage?.limits?.filter((limit) => limit.status === "ok").slice(0, 2) ?? [];

  return (
    <div
      className={`app-shell ${sidebarCollapsed ? "app-shell--sidebar-collapsed" : ""}`}
      style={{ "--inspector-width": `${inspectorWidth}px` } as CSSProperties}
    >
      <aside id="app-sidebar" className="app-sidebar">
        <div className="brand-lockup" aria-label="BeamWorlds Mod Studio">
          <BeamWorldsMark />
          <div className="brand-lockup__text">
            <strong>BeamWorlds</strong>
            <span>Mod Studio</span>
          </div>
          <button
            className="sidebar-collapse"
            onClick={toggleSidebar}
            aria-controls="app-sidebar"
            aria-expanded={!sidebarCollapsed}
            aria-label={
              sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"
            }
            title={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            <Icon name="collapse" size={15} />
          </button>
        </div>
        <nav className="main-nav" aria-label="Primary navigation">
          <button
            className={view === "library" ? "is-active" : ""}
            onClick={() => changeView("library")}
            aria-label="Mod Library"
          >
            <Icon name="library" />
            <span className="nav-label">Mod Library</span>
            <span className="nav-tooltip">Mod Library</span>
          </button>
          <button
            className={view === "collections" ? "is-active" : ""}
            onClick={() => changeView("collections")}
            aria-label="Collections"
          >
            <Icon name="mixed" />
            <span className="nav-label">Collections</span>
            <span className="nav-tooltip">Collections</span>
          </button>
          <button
            className={view === "workspaces" ? "is-active" : ""}
            onClick={() => changeView("workspaces")}
            aria-label="ModMaker"
          >
            <Icon name="workspace" />
            <span className="nav-label">ModMaker</span>
            <span className="nav-tooltip">ModMaker</span>
          </button>
          <button
            className={view === "scanner" ? "is-active" : ""}
            onClick={() => changeView("scanner")}
            aria-label="Virus Scanner"
          >
            <Icon name="shield" />
            <span className="nav-label">Virus Scanner</span>
            <span className="nav-tooltip">Virus Scanner</span>
          </button>
          <button
            className={`play-nav${view === "play" ? " is-active" : ""}`}
            onClick={() => changeView("play")}
            aria-label="Play"
          >
            <Icon name="play" />
            <span className="nav-label">Play</span>
            <span className="nav-tooltip">Play</span>
          </button>
          <button
            className={view === "activity" ? "is-active" : ""}
            onClick={() => changeView("activity")}
            aria-label="Activity"
          >
            <Icon name="activity" />
            <span className="nav-label">Activity</span>
            <span className="nav-tooltip">Activity</span>
          </button>
        </nav>
        <div className="sidebar-actions">
          <button
            className="sidebar-theme"
            onClick={toggleTheme}
            aria-label={`Use ${settings?.theme === "dark" ? "light" : "dark"} theme`}
          >
            <Icon
              name={settings?.theme === "dark" ? "sun" : "moon"}
              size={17}
            />
            <span>
              {settings?.theme === "dark" ? "Light theme" : "Dark theme"}
            </span>
            <span className="nav-tooltip">
              {settings?.theme === "dark" ? "Light theme" : "Dark theme"}
            </span>
          </button>
          <button
            className={`sidebar-settings ${view === "settings" ? "is-active" : ""}`}
            onClick={() => changeView("settings")}
            aria-label="Settings"
          >
            <Icon name="settings" size={17} />
            <span>Settings</span>
            <span className="nav-tooltip">Settings</span>
          </button>
        </div>
      </aside>

      <main
        className={`app-main ${selectedItem && view === "library" ? "has-inspector" : ""}`}
      >
        {loading ? (
          <div className="splash">
            <div className="splash__panels" aria-hidden="true">
              <span style={{ backgroundImage: "url(/art/night-highway.jpg)" }} />
              <span style={{ backgroundImage: "url(/art/forest-recovery.jpg)" }} />
              <span style={{ backgroundImage: "url(/art/night-station.jpg)" }} />
              <span style={{ backgroundImage: "url(/art/city-street.jpg)" }} />
              <span style={{ backgroundImage: "url(/art/crash.jpg)" }} />
            </div>
            <div className="splash__veil" aria-hidden="true" />
            <div className="splash__copy">
              <BeamWorldsMark size={64} />
              <div className="splash__line" />
              <p>
                {startupCount > 0
                  ? `Loading ${startupCount.toLocaleString()} mods`
                  : "Loading mod library"}
              </p>
            </div>
          </div>
        ) : (
          <div
            className={`view-swap view-swap--${viewSwap % 2 === 0 ? "a" : "b"}`}
          >
            {view === "scanner" && (
              <VirusScannerView
                items={allItems}
                request={virusScanRequest}
                onLibraryChange={refreshAfterVirusScan}
                onNotify={notify}
                onError={handleError}
              />
            )}
            {view === "library" && (
              <LibraryView
                items={items}
                catalogItems={allItems}
                collections={organization?.collections ?? []}
                tags={organization?.tags ?? []}
                scan={scan}
                scanning={scanning}
                loading={libraryLoading}
                query={searchInput}
                collectionID={collectionID}
                selectedID={selectedItem?.entityId ?? ""}
                onQueryChange={setSearchInput}
                onCollectionChange={setCollectionID}
                onManageCollections={() => openCollection("")}
                onOrganization={handleOrganizationChange}
                onError={handleError}
                onSelect={(item) => void selectItem(item)}
                onVirusScan={openVirusScanner}
                onScan={() => void startScan()}
                onCancelScan={() => void cancelScan()}
              />
            )}
            {view === "workspaces" && (
              <ModMaker
                workspaces={workspaces}
                allItems={allItems}
                detail={workspaceDetail}
                selectedID={selectedWorkspaceID}
                stale={workspaceStale}
                writeBlocked={writeBlocked}
                isWriteBlocked={isWriteBlocked}
                loading={workspaceLoading}
                defaultAuthor={settings?.defaultAuthor ?? ""}
                showFileSizes={settings?.showFileSizes ?? true}
                autoFormatDelayMs={settings?.autoFormatDelayMs ?? 200}
                agentActivityBuffer={agentActivityBuffer}
                onSelect={(id) => {
                  if (id) void selectWorkspace(id);
                  else {
                    workspaceDetailLoadVersion.current += 1;
                    selectedWorkspaceIDRef.current = "";
                    workspaceBaselineRef.current = null;
                    workspaceStaleRef.current = false;
                    setWorkspaceStale(false);
                    setSelectedWorkspaceID("");
                    setWorkspaceDetail(null);
                  }
                }}
                onReload={reloadWorkspace}
                onCreateMod={createNewMod}
                onEditorStatus={setEditorStatus}
                onNotify={notify}
                onError={handleError}
              />
            )}
            {view === "collections" && (
              <CollectionsView
                organization={organization}
                items={allItems}
                collectionID={openedCollectionID}
                onOpenCollection={setOpenedCollectionID}
                onOrganization={handleOrganizationChange}
                onAddToPlay={(ids) => {
                  void playSession.addCollections(ids).then(() => changeView("play")).catch(handleError);
                }}
                onInspectMod={inspectCollectionMod}
                onNotify={notify}
                onError={handleError}
              />
            )}
            {view === "play" && (
              <PlayView
                organization={organization}
                session={playSession}
                onOrganization={handleOrganizationChange}
                onNotify={notify}
                onError={handleError}
                onOpenCollection={openCollection}
              />
            )}
            {view === "activity" && (
              <ActivityView
                config={config}
                dashboard={dashboard}
                onScan={() => void startScan()}
              />
            )}
            {view === "settings" && (
              <SettingsView
                settings={settings}
                usage={usage}
                onSave={saveSettings}
                onOpenSetup={() => void openSetup()}
                onNotify={notify}
              />
            )}
          </div>
        )}
      </main>
      {selectedItem && view === "library" && (
        <Inspector
          item={selectedItem}
          writeBlocked={writeBlocked}
          detail={entityDetail}
          stale={inspectorStale}
          tags={organization?.tags ?? []}
          loading={entityLoading}
          creatingWorkspace={creatingWorkspace}
          inspectorWidth={inspectorWidth}
          inspectorMinWidth={INSPECTOR_MIN_WIDTH}
          inspectorMaxWidth={inspectorMaxWidth}
          inspectorResizeDisabled={inspectorResizeDisabled}
          onResizeCommit={commitInspectorResize}
          onResetWidth={resetInspectorWidth}
          onDirtyChange={(dirty) => {
            inspectorDirtyRef.current = dirty;
          }}
          onClose={() => {
            if (!confirmInspectorLeave()) return;
            inspectorDirtyRef.current = false;
            selectedItemRef.current = null;
            inspectorBaselineRef.current = null;
            inspectorStaleRef.current = false;
            setSelectedItem(null);
            setEntityDetail(null);
            setInspectorStale(false);
          }}
          onSetTags={setSelectedTags}
          onCreateTag={createTag}
          onUpdateTagVisual={updateTagVisual}
          onDeleteTag={deleteTag}
          onSaveDetails={saveDetails}
          onSaveVariant={saveVariant}
          onPreviewMember={previewMember}
          onCreateWorkspace={() => void createWorkspace()}
          onVirusScan={openVirusScanner}
          onError={handleError}
        />
      )}

      <footer className="app-statusbar" aria-label="Application status">
        <div
          className={`app-statusbar__scan${scanning ? " app-statusbar__scan--active" : ""}`}
        >
          <Icon name={scanning ? "scan" : "check"} size={14} />
          <span>
            {scanning
              ? `Scanning ${scan?.analyzed ?? 0}/${scan?.discovered ?? 0}`
              : lastSuccessfulScanAt
                ? `Last scan: ${formatDate(lastSuccessfulScanAt)}`
                : "No successful scan"}
          </span>
        </div>
        <div>
          {view === "workspaces" && activeWorkspace ? (
            <>
              <strong>{activeWorkspace.displayName}</strong>
              {editorStatus.path && (
                <span title={editorStatus.path}>
                  {editorStatus.dirty ? "Unsaved" : "Saved"} ·{" "}
                  {editorStatus.path} · {formatBytes(editorStatus.sizeBytes)}
                </span>
              )}
            </>
          ) : view === "settings" ? (
            <span>Application settings</span>
          ) : (
            <span>
              {(dashboard?.entities ?? items.length).toLocaleString()} mods
            </span>
          )}
        </div>
        {settings?.showAIUsage && usage?.hasRuns && (
          <div className="usage-compact">
            <Icon name="agent" size={13} />
            {usageLimits.map((limit) => (
              <UsageChip
                key={`${limit.provider}-${limit.label}`}
                limit={limit}
              />
            ))}
            {usage.totalTokens > 0 && (
              <span>{usage.totalTokens.toLocaleString()} tokens</span>
            )}
          </div>
        )}
      </footer>

      <div
        className={`toast toast--${toast.tone} ${toast.visible ? "is-visible" : ""}`}
        role="status"
        aria-live="polite"
      >
        <Icon
          name={
            toast.tone === "error"
              ? "error"
              : toast.tone === "success"
                ? "check"
                : "activity"
          }
          size={17}
        />
        <span>{toast.message}</span>
        <button
          onClick={() =>
            setToast((current) => ({ ...current, visible: false }))
          }
          aria-label="Dismiss"
        >
          <Icon name="close" size={14} />
        </button>
      </div>
    </div>
  );
}

function bufferAgentActivity(
  buffer: Record<string, AgentActivity[]>,
  activity: AgentActivity,
) {
  let records = buffer[activity.runId];
  if (!records) {
    records = [];
    buffer[activity.runId] = records;
  }
  const previous = records[records.length - 1];
  if (activity.delta && previous?.delta && previous.type === activity.type) {
    const remaining = (4 << 20) - previous.delta.length;
    records[records.length - 1] =
      remaining > 0
        ? {
            ...activity,
            delta: previous.delta + activity.delta.slice(0, remaining),
          }
        : previous;
  } else {
    records.push(activity);
    if (records.length > 500) records.splice(0, records.length - 500);
  }
  if (activity.type === "finished") {
    window.setTimeout(() => {
      delete buffer[activity.runId];
    }, 30_000);
  }
}

function UsageChip({
  limit,
}: {
  limit: NonNullable<AIUsage["limits"]>[number];
}) {
  const amount =
    limit.unit === "percent"
      ? `${Math.round(limit.used)}%`
      : `${Math.round(limit.remaining)} ${limit.unit}`;
  return (
    <span
      title={`${limit.provider} · resets ${new Date(limit.resetsAt).toLocaleString()}`}
    >
      {limit.windowId || limit.label} {amount}
    </span>
  );
}

export default App;
