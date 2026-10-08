import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type RefCallback,
} from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  GitBranch,
  GitCommitResult,
  GitDiff,
  GitOperationResult,
  GitStatus,
  GitStatusEntry,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import { Badge, Button, EmptyState, Spinner } from "./ui";
import { GitHubPublishEntry } from "./GitHubPublishDialog";
import "./SourceControlView.css";

export type SourceControlWorkingTreeChange = {
  reason: "discard" | "branch" | "pull" | "sync" | "status" | "restore";
  paths?: readonly string[];
};

export interface SourceControlViewProps {
  workspaceID: string;
  active: boolean;
  dirtyPaths: ReadonlySet<string>;
  writeBlocked: boolean;
  onOpenFile: (path: string) => void;
  onNotify: (
    message: string,
    tone: "success" | "info" | "warning" | "error",
  ) => void;
  onError: (error: unknown) => void;
  onWorkingTreeChanged: (
    change: SourceControlWorkingTreeChange,
  ) => void | Promise<void>;
  onBusyChange: (busy: boolean) => void;
}

type GitComparison = "working" | "index" | "untracked";
type ChangeGroup = "conflicts" | "staged" | "unstaged" | "untracked";
type NoticeTone = "success" | "info" | "warning" | "error";

type Selection = {
  path: string;
  comparison: GitComparison;
  group: ChangeGroup;
};

type Notice = {
  tone: NoticeTone;
  message: string;
};

type DiscardConfirmation = {
  paths: string[];
  originalPaths: (string | undefined)[];
  fingerprint: string;
  untracked: boolean;
};

type GitDiscardFailureLike = {
  path?: string;
  error?: string;
};

type OutputTruncatedLike = {
  outputTruncated?: boolean;
};

type GitDiscardResultLike = OutputTruncatedLike & {
  success?: boolean;
  stale?: boolean;
  completed?: string[];
  failed?: GitDiscardFailureLike[];
  error?: string;
  status?: GitStatus;
};

type RemoteOperation = "fetch" | "pull" | "push" | "sync";
type PathComparisonMode = "case-sensitive" | "case-insensitive";
type BranchPanelPosition = {
  top: number;
  left: number;
  maxHeight: number;
};

type NormalizedGitStatus = Omit<
  GitStatus,
  "remotes" | "conflicts" | "staged" | "unstaged" | "untracked"
> & {
  remotes: NonNullable<GitStatus["remotes"]>;
  conflicts: NonNullable<GitStatus["conflicts"]>;
  staged: NonNullable<GitStatus["staged"]>;
  unstaged: NonNullable<GitStatus["unstaged"]>;
  untracked: NonNullable<GitStatus["untracked"]>;
};

type NormalizedGitOperationResult = Omit<GitOperationResult, "status" | "steps"> & {
  status: NormalizedGitStatus;
  steps: NonNullable<GitOperationResult["steps"]>;
};

function normalizeGitStatus(rawStatus: GitStatus): NormalizedGitStatus {
  return {
    ...rawStatus,
    remotes: rawStatus.remotes ?? [],
    conflicts: rawStatus.conflicts ?? [],
    staged: rawStatus.staged ?? [],
    unstaged: rawStatus.unstaged ?? [],
    untracked: rawStatus.untracked ?? [],
  };
}

function normalizeGitOperationResult(
  rawResult: GitOperationResult,
): NormalizedGitOperationResult {
  return {
    ...rawResult,
    status: normalizeGitStatus(rawResult.status),
    steps: rawResult.steps ?? [],
  };
}

type GitApiContract = {
  GetWorkspaceGitStatus: (workspaceID: string) => Promise<GitStatus>;
  GetWorkspaceGitDiff: (
    workspaceID: string,
    relativePath: string,
    comparison: GitComparison,
  ) => Promise<GitDiff>;
  StageWorkspaceGitPaths: (
    workspaceID: string,
    paths: string[],
    expectedFingerprint: string,
  ) => Promise<GitStatus>;
  UnstageWorkspaceGitPaths: (
    workspaceID: string,
    paths: string[],
    expectedFingerprint: string,
  ) => Promise<GitStatus>;
  DiscardWorkspaceGitPaths: (
    workspaceID: string,
    paths: string[],
    expectedFingerprint: string,
  ) => Promise<GitDiscardResultLike>;
  CommitWorkspaceGit: (
    workspaceID: string,
    message: string,
    amend: boolean,
    expectedFingerprint: string,
  ) => Promise<GitCommitResult>;
  ListWorkspaceGitBranches: (workspaceID: string) => Promise<GitBranch[]>;
  CreateWorkspaceGitBranch: (
    workspaceID: string,
    name: string,
    startPoint: string,
    expectedFingerprint: string,
  ) => Promise<GitStatus>;
  SwitchWorkspaceGitBranch: (
    workspaceID: string,
    name: string,
    expectedFingerprint: string,
  ) => Promise<GitStatus>;
  FetchWorkspaceGit: (workspaceID: string) => Promise<GitOperationResult>;
  PullWorkspaceGit: (
    workspaceID: string,
    expectedFingerprint: string,
  ) => Promise<GitOperationResult>;
  PushWorkspaceGit: (workspaceID: string) => Promise<GitOperationResult>;
  SyncWorkspaceGit: (
    workspaceID: string,
    expectedFingerprint: string,
  ) => Promise<GitOperationResult>;
  CancelWorkspaceGitOperation: (workspaceID: string) => Promise<boolean>;
  FileManagerActionLabel?: () => Promise<string>;
};

// The generated binding is refreshed with the backend contract. Keeping the
// local view contract here also lets the source compile against an older
// generated directory while that refresh is in flight.
const gitAPI = API as unknown as GitApiContract;

const GROUPS: readonly {
  key: ChangeGroup;
  label: string;
  description: string;
}[] = [
  {
    key: "conflicts",
    label: "Merge conflicts",
    description: "Unresolved paths stay here until normal editing resolves them.",
  },
  {
    key: "staged",
    label: "Staged changes",
    description: "Changes already in the index and ready for commit.",
  },
  {
    key: "unstaged",
    label: "Unstaged changes",
    description: "Tracked working-tree changes not currently in the index.",
  },
  {
    key: "untracked",
    label: "Untracked files",
    description: "New working-tree files that Git has not started tracking.",
  },
];

const INITIAL_EXPANDED: Record<ChangeGroup, boolean> = {
  conflicts: true,
  staged: true,
  unstaged: true,
  untracked: true,
};

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  if (error && typeof error === "object" && "message" in error) {
    const message = (error as { message?: unknown }).message;
    if (typeof message === "string" && message) return message;
  }
  return String(error || "Git operation failed");
}

function groupEntries(
  status: NormalizedGitStatus | null,
  group: ChangeGroup,
): GitStatusEntry[] {
  if (!status) return [];
  if (group === "conflicts") return status.conflicts;
  if (group === "staged") return status.staged;
  if (group === "unstaged") return status.unstaged;
  return status.untracked;
}

function normalizePath(path: string): string {
  return path.split("\\").join("/");
}

function dirtyPathMatches(
  dirtyPaths: ReadonlySet<string>,
  path: string,
  caseInsensitive: boolean,
): boolean {
  const normalized = normalizePath(path);
  for (const dirtyPath of dirtyPaths) {
    const normalizedDirtyPath = normalizePath(dirtyPath);
    if (normalizedDirtyPath === normalized) return true;
    if (caseInsensitive && normalizedDirtyPath.toLowerCase() === normalized.toLowerCase()) {
      return true;
    }
  }
  return false;
}


function comparisonForGroup(group: ChangeGroup): GitComparison {
  if (group === "staged") return "index";
  if (group === "untracked") return "untracked";
  return "working";
}

function selectionKey(group: ChangeGroup, path: string): string {
  return `file:${group}:${path}`;
}

function groupKey(group: ChangeGroup): string {
  return `group:${group}`;
}

function hasEntry(status: NormalizedGitStatus, group: ChangeGroup, path: string): boolean {
  return groupEntries(status, group).some((entry) => entry.path === path);
}

function reconcileSelection(
  status: NormalizedGitStatus,
  previous: Selection | null,
): Selection | null {
  if (!previous) return null;
  if (hasEntry(status, previous.group, previous.path)) return previous;

  const groupOrder: ChangeGroup[] = [
    previous.group,
    ...GROUPS.map((group) => group.key).filter((group) => group !== previous.group),
  ];
  for (const group of groupOrder) {
    if (hasEntry(status, group, previous.path)) {
      return {
        path: previous.path,
        comparison: comparisonForGroup(group),
        group,
      };
    }
  }
  return null;
}

function isDiscardable(entry: GitStatusEntry): boolean {
  if (entry.conflict) return false;
  if (entry.untracked) return !entry.staged;
  return entry.unstaged;
}

function statusCodeForGroup(entry: GitStatusEntry, group: ChangeGroup): string {
  if (group === "conflicts") return "U";
  if (group === "untracked") return "?";
  return group === "staged" ? entry.indexCode : entry.worktreeCode;
}

function entryStatusLabel(entry: GitStatusEntry, group: ChangeGroup): string {
  if (group === "conflicts") return "Conflict";
  const code = statusCodeForGroup(entry, group).toUpperCase();
  if (group === "untracked" || code === "?") return "Untracked";
  if (code === "A") return "Added";
  if (code === "D") return "Deleted";
  if (code === "M") return "Modified";
  if (code === "R") return "Renamed";
  if (code === "C") return "Copied";
  if (code === "T") return "Type changed";
  if (code === "U") return "Unmerged";
  return entry.status || "Changed";
}

function entryBadgeTone(
  entry: GitStatusEntry,
  group: ChangeGroup,
): "neutral" | "success" | "warning" | "danger" | "accent" | "cyan" {
  if (group === "conflicts") return "danger";
  if (group === "untracked") return "cyan";
  const code = statusCodeForGroup(entry, group).toUpperCase();
  if (code === "D") return "warning";
  if (code === "R" || code === "C") return "accent";
  return "neutral";
}

function comparisonLabel(comparison: GitComparison): string {
  if (comparison === "index") return "Index versus HEAD";
  if (comparison === "untracked") return "Untracked content versus no prior file";
  return "Working tree versus index";
}

function remoteLabel(operation: RemoteOperation): string {
  if (operation === "fetch") return "Fetching";
  if (operation === "pull") return "Pulling";
  if (operation === "push") return "Pushing";
  return "Synchronizing";
}

function operationFunction(
  operation: RemoteOperation,
): (workspaceID: string, expectedFingerprint: string) => Promise<GitOperationResult> {
  if (operation === "fetch") return (workspaceID) => gitAPI.FetchWorkspaceGit(workspaceID);
  if (operation === "pull") return (workspaceID, expectedFingerprint) => gitAPI.PullWorkspaceGit(workspaceID, expectedFingerprint);
  if (operation === "push") return (workspaceID) => gitAPI.PushWorkspaceGit(workspaceID);
  return (workspaceID, expectedFingerprint) => gitAPI.SyncWorkspaceGit(workspaceID, expectedFingerprint);
}

function usefulStatus(status?: GitStatus): status is GitStatus {
  return Boolean(status && (status.repository || status.root || status.fingerprint));
}

function outputWasTruncated(value: OutputTruncatedLike | null | undefined): boolean {
  return value?.outputTruncated === true;
}

function patchLineClass(line: string): string {
  if (line.startsWith("+++") || line.startsWith("---")) return "source-control-diff-line--header";
  if (line.startsWith("+")) return "source-control-diff-line--added";
  if (line.startsWith("-")) return "source-control-diff-line--removed";
  if (line.startsWith("@@")) return "source-control-diff-line--hunk";
  return "";
}

function renderPatch(patch: string) {
  return patch.split("\n").map((line, index) => (
    <span className={`source-control-diff-line ${patchLineClass(line)}`} key={`${index}:${line}`}>
      {line || " "}
    </span>
  ));
}

function failedDiscardText(result: GitDiscardResultLike): string {
  const failures = (result.failed ?? [])
    .map((failure) => `${failure.path || "Path"}: ${failure.error || "discard failed"}`)
    .join("; ");
  const truncation = outputWasTruncated(result)
    ? " Git capped the discard response; verify the refreshed status."
    : "";
  return [result.error, failures].filter(Boolean).join(" ") + truncation || "Git did not discard every selected path.";
}
function discardReconciliationPaths(
  completedPaths: readonly string[],
  confirmation: DiscardConfirmation,
  caseInsensitive: boolean,
): string[] {
  const affected: string[] = [];
  const seen = new Set<string>();
  const pathKey = (path: string) => {
    const normalized = normalizePath(path);
    return caseInsensitive ? normalized.toLowerCase() : normalized;
  };
  for (const completedPath of completedPaths) {
    const completedKey = pathKey(completedPath);
    const selectedIndex = confirmation.paths.findIndex(
      (selectedPath) => pathKey(selectedPath) === completedKey,
    );
    if (selectedIndex < 0) continue;
    for (const path of [
      completedPath,
      confirmation.originalPaths[selectedIndex],
    ]) {
      if (!path) continue;
      const key = pathKey(path);
      if (seen.has(key)) continue;
      seen.add(key);
      affected.push(path);
    }
  }
  return affected;
}

export function SourceControlView({
  workspaceID,
  active,
  dirtyPaths,
  writeBlocked,
  onOpenFile,
  onNotify,
  onError,
  onWorkingTreeChanged,
  onBusyChange,
}: SourceControlViewProps) {
  const [status, setStatus] = useState<NormalizedGitStatus | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);
  const [statusRefreshing, setStatusRefreshing] = useState(false);
  const [statusError, setStatusError] = useState("");
  const [selected, setSelected] = useState<Selection | null>(null);
  const [diff, setDiff] = useState<GitDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState("");
  const [diffRetry, setDiffRetry] = useState(0);
  const [expandedGroups, setExpandedGroups] =
    useState<Record<ChangeGroup, boolean>>(INITIAL_EXPANDED);
  const [commitDraft, setCommitDraft] = useState("");
  const [amend, setAmend] = useState(false);
  const [commitOutputTruncated, setCommitOutputTruncated] = useState(false);
  const [busy, setBusy] = useState("");
  const [operationNotice, setOperationNotice] = useState<Notice | null>(null);
  const [operationResult, setOperationResult] =
    useState<NormalizedGitOperationResult | null>(null);
  const [remoteOperation, setRemoteOperation] =
    useState<{ kind: RemoteOperation; cancelRequested: boolean } | null>(null);
  const [branches, setBranches] = useState<GitBranch[]>([]);
  const [branchPanelOpen, setBranchPanelOpen] = useState(false);
  const [branchPanelPosition, setBranchPanelPosition] =
    useState<BranchPanelPosition | null>(null);
  const [branchLoading, setBranchLoading] = useState(false);
  const [branchError, setBranchError] = useState("");
  const [newBranchName, setNewBranchName] = useState("");
  const [newBranchStartPoint, setNewBranchStartPoint] = useState("");
  const [discardConfirmation, setDiscardConfirmation] =
    useState<DiscardConfirmation | null>(null);
  const [discardPhrase, setDiscardPhrase] = useState("");
  const [discardError, setDiscardError] = useState("");
  const [focusKey, setFocusKey] = useState(groupKey("conflicts"));
  const [pathComparisonMode, setPathComparisonMode] =
    useState<PathComparisonMode | null>(null);
  const [pathComparisonRetry, setPathComparisonRetry] = useState(0);

  const mountedRef = useRef(true);
  const activeRef = useRef(active);
  const workspaceIDRef = useRef(workspaceID);
  const statusRef = useRef<NormalizedGitStatus | null>(null);
  const selectedRef = useRef<Selection | null>(null);
  const lastAcceptedStatusFingerprintRef = useRef<string | null>(null);
  const lastReconciledStatusFingerprintRef = useRef<string | null>(null);
  const workspaceEpochRef = useRef(0);
  const statusRequestRef = useRef(0);
  const diffRequestRef = useRef(0);
  const branchRequestRef = useRef(0);
  const remoteOperationWorkspaceIDRef = useRef<string | null>(null);
  const onErrorRef = useRef(onError);
  const onWorkingTreeChangedRef = useRef(onWorkingTreeChanged);
  const onBusyChangeRef = useRef(onBusyChange);
  const branchButtonRef = useRef<HTMLButtonElement | null>(null);
  const branchPanelRef = useRef<HTMLDivElement | null>(null);
  const diffCardRef = useRef<HTMLElement | null>(null);
  const discardDialogRef = useRef<HTMLFormElement | null>(null);
  const discardReturnFocusRef = useRef<HTMLElement | null>(null);
  const focusNodesRef = useRef(new Map<string, HTMLElement>());
  const commitInputRef = useRef<HTMLTextAreaElement | null>(null);
  const discardInputRef = useRef<HTMLInputElement | null>(null);

  activeRef.current = active;
  workspaceIDRef.current = workspaceID;
  statusRef.current = status;
  selectedRef.current = selected;
  onErrorRef.current = onError;
  onWorkingTreeChangedRef.current = onWorkingTreeChanged;
  onBusyChangeRef.current = onBusyChange;
  const closeBranchPanel = () => {
    setBranchPanelOpen(false);
    setBranchPanelPosition(null);
    window.setTimeout(() => {
      const button = branchButtonRef.current;
      if (button && !button.disabled) button.focus();
    }, 0);
  };


  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      onBusyChangeRef.current(false);
    };
  }, []);

  useEffect(() => {
    let current = true;
    const readLabel = gitAPI.FileManagerActionLabel;
    if (typeof readLabel !== "function") {
      return () => {
        current = false;
      };
    }
    void readLabel()
      .then((label) => {
        if (!current) return;
        if (label === "Open in Explorer") {
          setPathComparisonMode("case-insensitive");
        } else if (
          label === "Reveal in Finder" ||
          label === "Open in File Manager"
        ) {
          setPathComparisonMode("case-sensitive");
        }
      })
      .catch(() => {
        if (current) setPathComparisonMode(null);
      });
    return () => {
      current = false;
    };
  }, [pathComparisonRetry]);
  useEffect(() => {
    if (!branchPanelOpen) {
      setBranchPanelPosition(null);
      return;
    }
    const reposition = () => {
      const button = branchButtonRef.current;
      if (!button) return;
      const rect = button.getBoundingClientRect();
      const margin = 12;
      const gap = 8;
      const width = Math.min(420, Math.max(180, window.innerWidth - margin * 2));
      const left = Math.min(
        Math.max(margin, rect.left),
        Math.max(margin, window.innerWidth - width - margin),
      );
      const below = window.innerHeight - rect.bottom - gap - margin;
      const above = rect.top - gap - margin;
      const openAbove = below < 320 && above > below;
      const maxHeight = Math.min(
        620,
        Math.max(120, openAbove ? above : below),
      );
      const top = openAbove
        ? Math.max(margin, rect.top - gap - maxHeight)
        : Math.min(
            Math.max(margin, rect.bottom + gap),
            Math.max(margin, window.innerHeight - maxHeight - margin),
          );
      setBranchPanelPosition({ top, left, maxHeight });
    };
    reposition();
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => {
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [branchPanelOpen]);
  useEffect(() => {
    if (!branchPanelOpen) return;
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (
        branchButtonRef.current?.contains(target) ||
        branchPanelRef.current?.contains(target)
      ) {
        return;
      }
      closeBranchPanel();
    };
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      closeBranchPanel();
    };
    document.addEventListener("pointerdown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [branchPanelOpen]);


  useEffect(() => {
    const operationWorkspaceID = workspaceID;
    return () => {
      if (remoteOperationWorkspaceIDRef.current !== operationWorkspaceID) return;
      remoteOperationWorkspaceIDRef.current = null;
      void gitAPI.CancelWorkspaceGitOperation(operationWorkspaceID).catch((error) => {
        if (mountedRef.current && workspaceIDRef.current === operationWorkspaceID) {
          onErrorRef.current(error);
        }
      });
    };
  }, [workspaceID]);

  useEffect(() => {
    onBusyChangeRef.current(Boolean(busy || remoteOperation));
  }, [busy, remoteOperation !== null]);


  useEffect(() => {
    workspaceEpochRef.current += 1;
    statusRequestRef.current += 1;
    diffRequestRef.current += 1;
    branchRequestRef.current += 1;
    remoteOperationWorkspaceIDRef.current = null;
    lastAcceptedStatusFingerprintRef.current = null;
    lastReconciledStatusFingerprintRef.current = null;
    statusRef.current = null;
    selectedRef.current = null;
    setStatus(null);
    setStatusError("");
    setStatusLoading(false);
    setStatusRefreshing(false);
    setSelected(null);
    setDiff(null);
    setDiffError("");
    setDiffRetry(0);
    setExpandedGroups(INITIAL_EXPANDED);
    setBranchLoading(false);
    setBranchError("");
    setBranches([]);
    setBranchPanelOpen(false);
    setBusy("");
    setRemoteOperation(null);
    setOperationResult(null);
    setOperationNotice(null);
    setDiscardConfirmation(null);
    setDiscardPhrase("");
    setDiscardError("");
    setCommitDraft("");
    setCommitOutputTruncated(false);
    setAmend(false);
    setFocusKey(groupKey("conflicts"));
  }, [workspaceID]);

  const applyStatus = (
    nextStatus: NormalizedGitStatus,
    markReconciled = true,
  ) => {
    if (!mountedRef.current || workspaceIDRef.current !== workspaceID) return;
    lastAcceptedStatusFingerprintRef.current = nextStatus.fingerprint;
    if (markReconciled) {
      lastReconciledStatusFingerprintRef.current = nextStatus.fingerprint;
    }
    setStatusError("");
    setStatus(nextStatus);
    setSelected((previous) => reconcileSelection(nextStatus, previous));
  };

  const isCurrentOperation = (
    operationWorkspaceID: string,
    epoch: number,
  ): boolean =>
    mountedRef.current &&
    workspaceIDRef.current === operationWorkspaceID &&
    workspaceEpochRef.current === epoch;

  const reconcileWorkingTree = async (
    change: SourceControlWorkingTreeChange,
    operationWorkspaceID: string,
    epoch: number,
  ): Promise<void> => {
    if (!isCurrentOperation(operationWorkspaceID, epoch)) return;
    try {
      await onWorkingTreeChangedRef.current(change);
    } catch (error) {
      if (!isCurrentOperation(operationWorkspaceID, epoch)) return;
      try {
        onErrorRef.current(error);
      } catch {
        // Reconciliation reporting must not change the Git operation result.
      }
    }
  };

  const refreshStatus = async (): Promise<NormalizedGitStatus | null> => {
    if (!workspaceID) return null;
    const epoch = workspaceEpochRef.current;
    const request = ++statusRequestRef.current;
    if (statusRef.current) setStatusRefreshing(true);
    else setStatusLoading(true);
    try {
      const nextStatus = normalizeGitStatus(await gitAPI.GetWorkspaceGitStatus(workspaceID));
      if (
        !mountedRef.current ||
        workspaceIDRef.current !== workspaceID ||
        workspaceEpochRef.current !== epoch ||
        statusRequestRef.current !== request
      ) {
        return null;
      }
      const previousFingerprint = lastAcceptedStatusFingerprintRef.current;
      const firstAcceptedStatus = previousFingerprint === null;
      lastAcceptedStatusFingerprintRef.current = nextStatus.fingerprint;
      if (firstAcceptedStatus && !activeRef.current) {
        lastReconciledStatusFingerprintRef.current = nextStatus.fingerprint;
      }
      applyStatus(nextStatus, false);
      if (branchPanelOpen && nextStatus.repository) {
        void refreshBranches();
      }
      if (
        activeRef.current &&
        (firstAcceptedStatus ||
          lastReconciledStatusFingerprintRef.current !== nextStatus.fingerprint)
      ) {
        await reconcileWorkingTree(
          { reason: "status" },
          workspaceID,
          epoch,
        );
        if (isCurrentOperation(workspaceID, epoch)) {
          lastReconciledStatusFingerprintRef.current = nextStatus.fingerprint;
        }
      }
      return nextStatus;
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === workspaceID &&
        workspaceEpochRef.current === epoch &&
        statusRequestRef.current === request
      ) {
        setStatusError(errorMessage(error));
        onError(error);
      }
      return null;
    } finally {
      if (
        mountedRef.current &&
        workspaceIDRef.current === workspaceID &&
        workspaceEpochRef.current === epoch &&
        statusRequestRef.current === request
      ) {
        setStatusLoading(false);
        setStatusRefreshing(false);
      }
    }
  };

  const refreshBranches = async () => {
    if (!workspaceID || !statusRef.current?.repository) return;
    const epoch = workspaceEpochRef.current;
    const request = ++branchRequestRef.current;
    setBranchLoading(true);
    setBranchError("");
    try {
      const nextBranches = await gitAPI.ListWorkspaceGitBranches(workspaceID);
      if (
        !mountedRef.current ||
        workspaceIDRef.current !== workspaceID ||
        workspaceEpochRef.current !== epoch ||
        branchRequestRef.current !== request
      ) {
        return;
      }
      setBranches(nextBranches ?? []);
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === workspaceID &&
        workspaceEpochRef.current === epoch &&
        branchRequestRef.current === request
      ) {
        setBranchError(errorMessage(error));
        onError(error);
      }
    } finally {
      if (
        mountedRef.current &&
        workspaceIDRef.current === workspaceID &&
        workspaceEpochRef.current === epoch &&
        branchRequestRef.current === request
      ) {
        setBranchLoading(false);
      }
    }
  };

  useEffect(() => {
    if (!active && !remoteOperation) return;
    void refreshStatus();
    const timer = window.setInterval(() => {
      void refreshStatus();
    }, 15000);
    return () => window.clearInterval(timer);
  }, [active, workspaceID, remoteOperation?.kind, branchPanelOpen]);

  useEffect(() => {
    if (!active || !status?.repository) return;
    void refreshBranches();
  }, [active, workspaceID, status?.repository]);

  useEffect(() => {
    if (!status || !selected || selectedRef.current !== selected) {
      diffRequestRef.current += 1;
      setDiff(null);
      setDiffError("");
      setDiffLoading(false);
      return;
    }
    if (!active) return;
    const epoch = workspaceEpochRef.current;
    const request = ++diffRequestRef.current;
    const selectionAtRequest = selected;
    setDiffLoading(true);
    setDiffError("");
    void (async () => {
      try {
        const nextDiff = await gitAPI.GetWorkspaceGitDiff(
          workspaceID,
          selectionAtRequest.path,
          selectionAtRequest.comparison,
        );
        const currentSelection = selectedRef.current;
        if (
          !mountedRef.current ||
          workspaceIDRef.current !== workspaceID ||
          workspaceEpochRef.current !== epoch ||
          diffRequestRef.current !== request ||
          !currentSelection ||
          currentSelection.path !== selectionAtRequest.path ||
          currentSelection.group !== selectionAtRequest.group ||
          currentSelection.comparison !== selectionAtRequest.comparison
        ) {
          return;
        }
        setDiff(nextDiff);
      } catch (error) {
        const currentSelection = selectedRef.current;
        if (
          mountedRef.current &&
          workspaceIDRef.current === workspaceID &&
          workspaceEpochRef.current === epoch &&
          diffRequestRef.current === request &&
          currentSelection?.path === selectionAtRequest.path &&
          currentSelection.group === selectionAtRequest.group &&
          currentSelection.comparison === selectionAtRequest.comparison
        ) {
          setDiffError(errorMessage(error));
          onError(error);
        }
      } finally {
        if (
          mountedRef.current &&
          workspaceIDRef.current === workspaceID &&
          workspaceEpochRef.current === epoch &&
          diffRequestRef.current === request
        ) {
          setDiffLoading(false);
        }
      }
    })();
    return () => {
      diffRequestRef.current += 1;
    };
  }, [active, workspaceID, selected?.path, selected?.comparison, selected?.group, status?.fingerprint, diffRetry]);

  useEffect(() => {
    if (!discardConfirmation) return;
    const dialog = discardDialogRef.current;
    if (!dialog) return;
    const focusable = () =>
      Array.from(
        dialog.querySelectorAll<HTMLElement>(
          "button, input, textarea, select, [tabindex]:not([tabindex='-1'])",
        ),
      ).filter((node) => !node.hasAttribute("disabled"));
    const timer = window.setTimeout(() => discardInputRef.current?.focus(), 0);
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeDiscardDialog();
        return;
      }
      if (event.key !== "Tab") return;
      const nodes = focusable();
      if (nodes.length === 0) {
        event.preventDefault();
        return;
      }
      const first = nodes[0];
      const last = nodes[nodes.length - 1];
      const current = document.activeElement;
      const outside = !(current instanceof Node) || !dialog.contains(current);
      if (
        outside ||
        (event.shiftKey ? current === first : current === last)
      ) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      }
    };
    dialog.addEventListener("keydown", handleKeyDown);
    return () => {
      window.clearTimeout(timer);
      dialog.removeEventListener("keydown", handleKeyDown);
    };
  }, [discardConfirmation]);

  useEffect(() => {
    if (!active || !selected || !diffCardRef.current) return;
    diffCardRef.current.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [active, selected?.path, selected?.comparison, selected?.group, diff]);

  const runStatusOperation = async (
    label: string,
    action: () => Promise<GitStatus>,
    successMessage: string,
  ) => {
    if (busy || writeBlocked || !statusRef.current?.repository) return;
    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    setBusy(label);
    setOperationNotice(null);
    setOperationResult(null);
    try {
      const nextStatus = normalizeGitStatus(await action());
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        applyStatus(nextStatus);
        onNotify(successMessage, "success");
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setOperationNotice({
          tone: "error",
          message: `${label}: ${errorMessage(error)}`,
        });
        onError(error);
      }
    } finally {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        void refreshStatus();
      }
    }
  };

  const stagePath = (group: ChangeGroup, entry: GitStatusEntry) => {
    if (busy || writeBlocked) return;
    const expectedFingerprint = statusRef.current?.fingerprint || "";
    const paths = entry.originalPath
      ? [entry.path, entry.originalPath]
      : [entry.path];
    if (group === "staged") {
      void runStatusOperation(
        `unstage:${entry.path}`,
        () => gitAPI.UnstageWorkspaceGitPaths(workspaceID, paths, expectedFingerprint),
        `Unstaged ${entry.path}`,
      );
      return;
    }
    void runStatusOperation(
      `stage:${entry.path}`,
      () => gitAPI.StageWorkspaceGitPaths(workspaceID, paths, expectedFingerprint),
      `Staged ${entry.path}`,
    );
  };

  const stageAll = () => {
    if (
      !status ||
      (status.unstaged.length === 0 &&
        status.untracked.length === 0 &&
        status.conflicts.length === 0)
    ) return;
    const expectedFingerprint = status.fingerprint;
    void runStatusOperation(
      "stage-all",
      // Git intentionally receives the exact repository-wide path requested by
      // the API contract. Discard never uses this path.
      () => gitAPI.StageWorkspaceGitPaths(workspaceID, ["."], expectedFingerprint),
      "Staged all working-tree changes",
    );
  };

  const unstageAll = () => {
    if (!status || status.staged.length === 0) return;
    const expectedFingerprint = status.fingerprint;
    void runStatusOperation(
      "unstage-all",
      () => gitAPI.UnstageWorkspaceGitPaths(workspaceID, ["."], expectedFingerprint),
      "Unstaged all indexed changes",
    );
  };

  const closeDiscardDialog = () => {
    setDiscardConfirmation(null);
    setDiscardPhrase("");
    setDiscardError("");
    const returnFocus = discardReturnFocusRef.current;
    discardReturnFocusRef.current = null;
    if (!returnFocus) return;
    window.setTimeout(() => {
      if (returnFocus.isConnected) returnFocus.focus();
    }, 0);
  };

  const beginDiscard = (entry: GitStatusEntry) => {
    if (busy || writeBlocked || !status || !status.repository || !isDiscardable(entry)) return;
    if (pathComparisonMode === null) {
      onNotify("Discard is temporarily unavailable; try refreshing the workspace.", "warning");
      return;
    }
    const affectedPaths = entry.originalPath
      ? [entry.path, entry.originalPath]
      : [entry.path];
    const dirtyPath = affectedPaths.find((path) =>
      dirtyPathMatches(dirtyPaths, path, pathComparisonMode === "case-insensitive"),
    );
    if (dirtyPath) {
      onNotify(
        `Cannot discard ${entry.path} while its editor buffer has unsaved changes. Save or reconcile it first.`,
        "warning",
      );
      return;
    }
    discardReturnFocusRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setDiscardError("");
    setDiscardPhrase("");
    setDiscardConfirmation({
      paths: [entry.path],
      originalPaths: [entry.originalPath],
      fingerprint: status.fingerprint,
      untracked: affectedPaths.some((path) =>
        status.untracked.some((candidate) => candidate.path === path),
      ),
    });
  };

  const confirmDiscard = async () => {
    const confirmation = discardConfirmation;
    if (!confirmation) return;
    if (discardPhrase !== confirmation.paths[0]) {
      setDiscardError("Type the path exactly as shown to confirm discard.");
      return;
    }
    if (pathComparisonMode === null) {
      setDiscardError("Discard is temporarily unavailable; refresh the workspace and try again.");
      return;
    }
    const currentStatus = statusRef.current;
    if (!currentStatus || currentStatus.fingerprint !== confirmation.fingerprint) {
      setDiscardError("Git status changed since confirmation. Refresh and review the path again.");
      closeDiscardDialog();
      setOperationNotice({
        tone: "warning",
        message: "Discard was not run because the confirmed Git status is stale.",
      });
      void refreshStatus();
      return;
    }
    const dirtyPathsForConfirmation = confirmation.paths.flatMap((path, index) => {
      const originalPath = confirmation.originalPaths[index];
      return originalPath ? [path, originalPath] : [path];
    });
    const dirtyPath = dirtyPathsForConfirmation.find((path) =>
      dirtyPathMatches(dirtyPaths, path, pathComparisonMode === "case-insensitive"),
    );
    if (dirtyPath) {
      closeDiscardDialog();
      onNotify(
        `Cannot discard ${dirtyPath} while its editor buffer has unsaved changes. Save or reconcile it first.`,
        "warning",
      );
      return;
    }
    if (busy || writeBlocked || !currentStatus.repository) return;

    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    setBusy(`discard:${confirmation.paths.join(",")}`);
    closeDiscardDialog();
    setOperationNotice(null);
    setOperationResult(null);
    try {
      const rawResult = await gitAPI.DiscardWorkspaceGitPaths(
        operationWorkspaceID,
        confirmation.paths,
        confirmation.fingerprint,
      );
      const result = rawResult ?? {};
      const reconciledPaths = discardReconciliationPaths(
        result.completed ?? [],
        confirmation,
        pathComparisonMode === "case-insensitive",
      );
      if (reconciledPaths.length > 0) {
        await reconcileWorkingTree(
          { reason: "discard", paths: reconciledPaths },
          operationWorkspaceID,
          epoch,
        );
      }
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        if (usefulStatus(result.status)) applyStatus(normalizeGitStatus(result.status));
        const failed = result.failed ?? [];
        if (result.success && !result.stale && failed.length === 0) {
          if (outputWasTruncated(result)) {
            setOperationNotice({
              tone: "warning",
              message: "Discard completed, but Git capped its response. Verify the refreshed status before continuing.",
            });
          }
          onNotify(
            `Discarded ${result.completed?.join(", ") || confirmation.paths.join(", ")}`,
            "success",
          );
        } else {
          const message = result.stale
            ? `Discard was not completed because Git status became stale. ${failedDiscardText(result)}`
            : failedDiscardText(result);
          setOperationNotice({ tone: result.stale ? "warning" : "error", message });
          onNotify(message, result.stale ? "warning" : "error");
        }
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setOperationNotice({
          tone: "error",
          message: `Discard failed: ${errorMessage(error)}`,
        });
        onError(error);
      }
    } finally {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        void refreshStatus();
      }
    }
  };

  const commit = async () => {
    if (!status?.repository || busy || writeBlocked) return;
    const message = commitDraft.trim();
    if (!message) {
      setOperationNotice({ tone: "warning", message: "Enter a commit message before committing." });
      return;
    }
    if (status.staged.length === 0) {
      setOperationNotice({
        tone: "warning",
        message: "Commit is disabled until at least one change is staged. Amend is explicit and does not bypass this safeguard.",
      });
      return;
    }
    if (status.conflicts.length > 0) {
      setOperationNotice({
        tone: "warning",
        message: "Resolve all conflicts before committing staged changes.",
      });
      return;
    }

    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    const expectedFingerprint = status.fingerprint;
    setBusy(amend ? "commit-amend" : "commit");
    setOperationNotice(null);
    setOperationResult(null);
    setCommitOutputTruncated(false);
    try {
      const result = await gitAPI.CommitWorkspaceGit(
        operationWorkspaceID,
        message,
        amend,
        expectedFingerprint,
      );
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        if (usefulStatus(result.status)) applyStatus(normalizeGitStatus(result.status), false);
        setCommitOutputTruncated(outputWasTruncated(result));
        setCommitDraft("");
        setAmend(false);
        const location = result.detached
          ? "detached HEAD"
          : result.branch || result.status.branch || "current branch";
        onNotify(
          `${result.amended ? "Amended" : "Committed"} ${result.shortHash || result.hash || "commit"} on ${location}`,
          "success",
        );
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setOperationNotice({
          tone: "error",
          message: `Commit failed: ${errorMessage(error)}`,
        });
        onError(error);
      }
    } finally {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        void refreshStatus();
      }
    }
  };

  const createBranch = async () => {
    if (!status?.repository || busy || writeBlocked) return;
    const name = newBranchName.trim();
    const startPoint = newBranchStartPoint.trim();
    if (!name) {
      setBranchError("Enter a local branch name.");
      return;
    }
    if (name === "-" || name.startsWith("-") || /[\u0000\r\n]/.test(name)) {
      setBranchError("Git branch names cannot be empty or begin with '-'.");
      return;
    }
    if (startPoint.startsWith("-") || /[\u0000\r\n]/.test(startPoint)) {
      setBranchError("The starting point is not a valid Git ref.");
      return;
    }

    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    const expectedFingerprint = status.fingerprint;
    setBusy(`branch-create:${name}`);
    setBranchError("");
    setOperationNotice(null);
    try {
      const nextStatus = await gitAPI.CreateWorkspaceGitBranch(
        operationWorkspaceID,
        name,
        startPoint,
        expectedFingerprint,
      );
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        applyStatus(normalizeGitStatus(nextStatus));
        setNewBranchName("");
        setNewBranchStartPoint("");
        onNotify(`Created and switched to ${name}`, "success");
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBranchError(errorMessage(error));
        setOperationNotice({ tone: "error", message: `Create branch failed: ${errorMessage(error)}` });
        onError(error);
      }
    } finally {
      if (isCurrentOperation(operationWorkspaceID, epoch)) {
        await reconcileWorkingTree({ reason: "branch" }, operationWorkspaceID, epoch);
      }
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        void refreshStatus();
        void refreshBranches();
      }
    }
  };

  const switchBranch = async (name: string) => {
    if (!status?.repository || busy || writeBlocked) return;
    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    const expectedFingerprint = status.fingerprint;
    setBusy(`branch-switch:${name}`);
    setBranchError("");
    setOperationNotice(null);
    try {
      const nextStatus = await gitAPI.SwitchWorkspaceGitBranch(
        operationWorkspaceID,
        name,
        expectedFingerprint,
      );
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        applyStatus(normalizeGitStatus(nextStatus));
        onNotify(`Switched to ${name}`, "success");
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBranchError(errorMessage(error));
        setOperationNotice({ tone: "error", message: `Switch branch failed: ${errorMessage(error)}` });
        onError(error);
      }
    } finally {
      if (isCurrentOperation(operationWorkspaceID, epoch)) {
        await reconcileWorkingTree({ reason: "branch" }, operationWorkspaceID, epoch);
      }
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        void refreshStatus();
        void refreshBranches();
      }
    }
  };

  const runRemoteOperation = async (kind: RemoteOperation) => {
    if (!status?.repository || busy || writeBlocked || status.remotes.length === 0) return;
    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID = workspaceID;
    const expectedFingerprint = status.fingerprint;
    setBusy(kind);
    remoteOperationWorkspaceIDRef.current = operationWorkspaceID;
    setRemoteOperation({ kind, cancelRequested: false });
    setOperationResult(null);
    setOperationNotice(null);
    try {
      const result = normalizeGitOperationResult(await operationFunction(kind)(operationWorkspaceID, expectedFingerprint));
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setOperationResult(result);
        applyStatus(result.status);
        if (result.success) {
          onNotify(`${kind[0].toUpperCase()}${kind.slice(1)} completed`, "success");
        } else {
          const message = result.error || result.stderr || result.output || `${kind} failed`;
          setOperationNotice({ tone: "error", message });
          onNotify(message, "error");
        }
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setOperationNotice({
          tone: "error",
          message: `${kind[0].toUpperCase()}${kind.slice(1)} failed: ${errorMessage(error)}`,
        });
        onError(error);
      }
    } finally {
      const shouldReconcile =
        (kind === "pull" || kind === "sync") &&
        isCurrentOperation(operationWorkspaceID, epoch);
      if (remoteOperationWorkspaceIDRef.current === operationWorkspaceID) {
        remoteOperationWorkspaceIDRef.current = null;
      }
      if (shouldReconcile) {
        await reconcileWorkingTree({ reason: kind }, operationWorkspaceID, epoch);
      }
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setBusy("");
        setRemoteOperation(null);
        void refreshStatus();
      }
    }
  };

  const cancelRemoteOperation = async () => {
    if (!remoteOperation || !workspaceID) return;
    const epoch = workspaceEpochRef.current;
    const operationWorkspaceID =
      remoteOperationWorkspaceIDRef.current || workspaceID;
    setRemoteOperation((current) =>
      current ? { ...current, cancelRequested: true } : current,
    );
    try {
      const accepted = await gitAPI.CancelWorkspaceGitOperation(operationWorkspaceID);
      if (
        !mountedRef.current ||
        workspaceIDRef.current !== operationWorkspaceID ||
        workspaceEpochRef.current !== epoch
      ) {
        return;
      }
      if (accepted) {
        onNotify("Cancellation requested; waiting for Git to stop", "info");
      } else {
        setRemoteOperation((current) =>
          current ? { ...current, cancelRequested: false } : current,
        );
        onNotify("Git did not accept a cancellation request", "warning");
      }
    } catch (error) {
      if (
        mountedRef.current &&
        workspaceIDRef.current === operationWorkspaceID &&
        workspaceEpochRef.current === epoch
      ) {
        setRemoteOperation((current) =>
          current ? { ...current, cancelRequested: false } : current,
        );
        setOperationNotice({ tone: "error", message: `Cancel failed: ${errorMessage(error)}` });
        onError(error);
      }
    }
  };

  const navigationItems = useMemo(() => {
    const items: { key: string; kind: "group" | "file" }[] = [];
    for (const group of GROUPS) {
      items.push({ key: groupKey(group.key), kind: "group" });
      if (expandedGroups[group.key]) {
        for (const entry of groupEntries(status, group.key)) {
          items.push({ key: selectionKey(group.key, entry.path), kind: "file" });
        }
      }
    }
    return items;
  }, [expandedGroups, status]);

  useEffect(() => {
    if (navigationItems.some((item) => item.key === focusKey)) return;
    setFocusKey(navigationItems[0]?.key ?? "");
  }, [focusKey, navigationItems]);

  const focusNavigationItem = (key: string) => {
    setFocusKey(key);
    window.setTimeout(() => focusNodesRef.current.get(key)?.focus(), 0);
  };

  const moveFocus = (currentKey: string, direction: -1 | 1) => {
    const currentIndex = navigationItems.findIndex((item) => item.key === currentKey);
    const startIndex = currentIndex < 0 ? (direction > 0 ? -1 : navigationItems.length) : currentIndex;
    const nextIndex = Math.max(0, Math.min(navigationItems.length - 1, startIndex + direction));
    const next = navigationItems[nextIndex];
    if (next) focusNavigationItem(next.key);
  };

  const registerFocusNode = (key: string): RefCallback<HTMLElement> => (node) => {
    if (node) focusNodesRef.current.set(key, node);
    else focusNodesRef.current.delete(key);
  };

  const handleTreeKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.defaultPrevented) return;
    const target = event.target as HTMLElement;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      const item = target.closest<HTMLElement>("[data-source-control-nav]");
      if (item) {
        event.preventDefault();
        moveFocus(
          item.dataset.sourceControlNav || "",
          event.key === "ArrowDown" ? 1 : -1,
        );
      } else if (event.key === "ArrowDown") {
        event.preventDefault();
        const first = navigationItems[0];
        if (first) focusNavigationItem(first.key);
      }
      return;
    }
    if (
      event.key.toLowerCase() === "c" &&
      !target.closest("input,textarea,select")
    ) {
      event.preventDefault();
      commitInputRef.current?.focus();
    }
  };

  const selectFile = (group: ChangeGroup, entry: GitStatusEntry) => {
    setFocusKey(selectionKey(group, entry.path));
    setSelected({ path: entry.path, comparison: comparisonForGroup(group), group });
  };

  const selectedEntry = selected
    ? groupEntries(status, selected.group).find((entry) => entry.path === selected.path) || null
    : null;
  const pendingCount = GROUPS.reduce(
    (total, group) => total + groupEntries(status, group.key).length,
    0,
  );
  const hasRemote = Boolean(status?.repository && status.remotes.length > 0);
  const mutationDisabled = Boolean(!status?.repository || busy || writeBlocked);
  const canCommit = Boolean(
    status?.repository &&
      status.staged.length > 0 &&
      status.conflicts.length === 0 &&
      commitDraft.trim() &&
      !busy &&
      !writeBlocked,
  );
  const branchTitle = status?.detached
    ? "Detached HEAD"
    : status?.branch || (status?.state === "unborn" ? "Unborn branch" : "No branch");
  const discardedPath = discardConfirmation?.paths[0] || "";
  const discardConfirmed = Boolean(
    discardConfirmation && discardPhrase === discardedPath,
  );

  return (
    <section className="view source-control-view" aria-label="Source Control">
      <header className="source-control-header">
        <div className="source-control-branch-wrap">
          <button
            type="button"
            className="source-control-branch-button"
            ref={branchButtonRef}
            aria-expanded={branchPanelOpen}
            aria-controls="source-control-branch-panel"
            disabled={!status?.repository || busy !== ""}
            onClick={() => {
              if (branchPanelOpen) {
                closeBranchPanel();
              } else {
                setBranchPanelOpen(true);
                void refreshBranches();
              }
            }}
          >
            <Icon name="link" size={15} />
            <span>{branchTitle}</span>
            <Icon name="chevron" size={13} />
          </button>
          {status?.state === "merge" && <Badge tone="danger">Conflicts</Badge>}
          {status?.state === "unborn" && <Badge tone="cyan">Unborn</Badge>}
          {branchPanelOpen && status?.repository && (
            <div
              ref={branchPanelRef}
              className="source-control-branch-panel"
              id="source-control-branch-panel"
              role="region"
              aria-label="Local Git branches"
              style={
                branchPanelPosition
                  ? {
                      top: branchPanelPosition.top,
                      left: branchPanelPosition.left,
                      maxHeight: branchPanelPosition.maxHeight,
                    }
                  : undefined
              }
            >
              <header>
                <strong>Local branches</strong>
                <button
                  type="button"
                  className="icon-button"
                  aria-label="Close branch list"
                  onClick={closeBranchPanel}
                >
                  <Icon name="close" size={15} />
                </button>
              </header>
              {branchError && (
                <div className="source-control-inline-message source-control-inline-message--error">
                  <Icon name="error" size={14} />
                  <span>{branchError}</span>
                </div>
              )}
              {branchLoading ? (
                <div className="source-control-branch-loading"><Spinner small /><span>Reading local branches</span></div>
              ) : branches.length === 0 ? (
                <p className="source-control-branch-empty">No local branches were returned by Git.</p>
              ) : (
                <div className="source-control-branch-list" role="listbox" aria-label="Local branches">
                  {branches.map((branch) => (
                    <button
                      type="button"
                      role="option"
                      aria-selected={branch.current}
                      className={branch.current ? "is-current" : ""}
                      key={branch.name}
                      disabled={mutationDisabled || branch.current}
                      onClick={() => void switchBranch(branch.name)}
                      title={branch.commit || branch.name}
                    >
                      <Icon name={branch.current ? "check" : "arrow"} size={14} />
                      <span>{branch.name}</span>
                      {branch.current && <Badge tone="success">Current</Badge>}
                    </button>
                  ))}
                </div>
              )}
              <form
                className="source-control-branch-create"
                onSubmit={(event) => {
                  event.preventDefault();
                  void createBranch();
                }}
              >
                <strong>Create and switch</strong>
                <label>
                  <span>Branch name</span>
                  <input
                    value={newBranchName}
                    onChange={(event) => setNewBranchName(event.target.value)}
                    placeholder="feature/my-change"
                    disabled={mutationDisabled}
                  />
                </label>
                <label>
                  <span>Starting point <small>(blank uses current HEAD)</small></span>
                  <input
                    value={newBranchStartPoint}
                    onChange={(event) => setNewBranchStartPoint(event.target.value)}
                    placeholder="HEAD"
                    disabled={mutationDisabled}
                  />
                </label>
                <Button
                  icon="plus"
                  tone="primary"
                  type="submit"
                  disabled={mutationDisabled || !newBranchName.trim()}
                >
                  Create branch
                </Button>
              </form>
            </div>
          )}
        </div>
        <div className="source-control-repository-meta">
          {status?.root && <code title={status.root}>{status.root}</code>}
          {status?.upstream ? <span className="source-control-upstream" title="Configured upstream">upstream {status.upstream}</span> : <span className="source-control-upstream">No upstream</span>}
          <span
            className="source-control-ahead-behind"
            title="Ahead and behind counts"
            aria-label={`${status?.ahead ?? 0} ahead, ${status?.behind ?? 0} behind`}
          >
            <span className={status?.ahead ? "has-count" : ""}>↑ {status?.ahead ?? 0}</span>
            <span className={status?.behind ? "has-count" : ""}>↓ {status?.behind ?? 0}</span>
          </span>
          <span className="source-control-remotes" title={status?.remotes.map((remote) => `${remote.name}: ${remote.fetchURL}`).join("\n") || "No configured remotes"}>
            <Icon name="link" size={13} />
            {status?.remotes.length ? status.remotes.map((remote) => remote.name).join(", ") : "No remotes"}
          </span>
        </div>
        <div className="source-control-header__actions">
          <Button
            icon="refresh"
            className="source-control-refresh"
            disabled={!active || statusLoading}
            onClick={() => {
              setPathComparisonRetry((retry) => retry + 1);
              void refreshStatus();
            }}
            title="Refresh Git status"
          >
            {statusRefreshing ? "Refreshing" : "Refresh"}
          </Button>
          <Button
            icon="install"
            disabled={mutationDisabled || !hasRemote}
            onClick={() => void runRemoteOperation("fetch")}
            title={hasRemote ? "Fetch remote references without merging" : "Configure a Git remote first"}
          >
            Fetch
          </Button>
          <Button
            icon="arrow"
            disabled={mutationDisabled || !hasRemote}
            onClick={() => void runRemoteOperation("pull")}
            title={hasRemote ? "Fetch and integrate according to Git configuration" : "Configure a Git remote first"}
          >
            Pull
          </Button>
          <Button
            icon="export"
            disabled={mutationDisabled || !hasRemote}
            onClick={() => void runRemoteOperation("push")}
            title={hasRemote ? "Push the current branch to its configured remote" : "Configure a Git remote first"}
          >
            Push
          </Button>
          <Button
            icon="refresh"
            disabled={mutationDisabled || !hasRemote}
            onClick={() => void runRemoteOperation("sync")}
            title={hasRemote ? "Fetch, pull, then push using conventional Git operations" : "Configure a Git remote first"}
          >
            Sync
          </Button>
          {remoteOperation && (
            <Button
              icon="close"
              tone="danger"
              disabled={remoteOperation.cancelRequested}
              onClick={() => void cancelRemoteOperation()}
              title="Cancel Git operation"
            >
              {remoteOperation.cancelRequested ? "Cancel requested" : "Cancel"}
            </Button>
          )}
          <div
            className="source-control-publish-action"
          >
            <GitHubPublishEntry
              workspaceID={workspaceID}
              disabled={mutationDisabled}
              onNotify={onNotify}
              onError={onError}
            />
          </div>
        </div>
      </header>
      <div className="source-control-message-stack">


      {writeBlocked && (
        <div className="source-control-blocked-banner" role="status">
          <Icon name="warning" size={15} />
          <span>Workspace writes are currently blocked. Git status and diffs remain read-only.</span>
        </div>
      )}
      {statusError && status && (
        <div className="source-control-inline-message source-control-inline-message--error" role="status">
          <Icon name="error" size={14} />
          <span>Git status refresh failed: {statusError}</span>
          <Button icon="refresh" className="source-control-status-retry" onClick={() => void refreshStatus()}>Retry</Button>
        </div>
      )}
      {status && outputWasTruncated(status) && (
        <div className="source-control-inline-message source-control-inline-message--warning" role="status">
          <Icon name="warning" size={14} />
          <span>Git status output was capped. Verify important paths with the Git CLI before destructive actions.</span>
        </div>
      )}
      {remoteOperation && (
        <div className="source-control-operation-progress" role="status" aria-live="polite">
          <Spinner small />
          <strong>{remoteLabel(remoteOperation.kind)}</strong>
          <span>{remoteOperation.cancelRequested ? "Waiting for the Git process to stop…" : "Git is running; other operations are disabled."}</span>
        </div>
      )}
      {operationNotice && (
        <div className={`source-control-inline-message source-control-inline-message--${operationNotice.tone}`} role="status">
          <Icon name={operationNotice.tone === "error" ? "error" : operationNotice.tone === "warning" ? "warning" : "check"} size={14} />
          <span>{operationNotice.message}</span>
          <button type="button" className="icon-button" aria-label="Dismiss message" onClick={() => setOperationNotice(null)}><Icon name="close" size={14} /></button>
        </div>
      )}
      {operationResult && !remoteOperation && (
        <section className="source-control-operation-result" aria-label="Last Git operation output">
          <header><strong>{operationResult.operation || "Git operation"} output</strong><button type="button" className="icon-button" aria-label="Hide operation output" onClick={() => setOperationResult(null)}><Icon name="close" size={14} /></button></header>
          {outputWasTruncated(operationResult) && <div className="source-control-inline-message source-control-inline-message--warning"><Icon name="warning" size={14} /><span>Git capped this operation output; the steps or log shown here may be incomplete. The operation result itself is not being treated as a failure.</span></div>}
          {operationResult.steps.length > 0 && <div className="source-control-operation-steps">{operationResult.steps.map((step, index) => <div key={`${step.operation}:${index}`} className={step.success ? "is-success" : "is-error"}><Icon name={step.success ? "check" : "error"} size={13} /><strong>{step.operation}</strong><span>{step.error || step.stderr || step.output || (step.success ? "Completed" : "Failed")}</span></div>)}</div>}
          {(operationResult.output || operationResult.stderr || operationResult.error) && <pre>{[operationResult.output, operationResult.stderr, operationResult.error].filter(Boolean).join("\n")}</pre>}
        </section>
      )}

      </div>
      {!status && statusLoading ? (
        <div className="source-control-centered"><Spinner /><span>Reading Git status</span></div>
      ) : !status && statusError ? (
        <div className="source-control-centered">
          <EmptyState icon="error" title="Git status could not be read" detail={statusError} action={<Button icon="refresh" onClick={() => void refreshStatus()}>Try again</Button>} />
        </div>
      ) : status && !status.repository ? (
        <div className="source-control-centered source-control-no-repository">
          <EmptyState
            icon="terminal"
            title="This workspace is not a Git repository"
            detail={`Git was checked at ${status.root || "the workspace FilesRoot"}. Initialize a repository there with “git init”, or open a workspace whose FilesRoot is already a repository. Source Control does not create a substitute state.`}
            action={
              <div className="source-control-empty-actions">
                <Button
                  icon="copy"
                  onClick={() => {
                    if (!status.root || !navigator.clipboard) {
                      if (status.root) onNotify("Clipboard access is unavailable; copy the repository path manually.", "warning");
                      return;
                    }
                    void navigator.clipboard.writeText(status.root).then(() => onNotify("Copied the workspace repository path", "info")).catch((error) => {
                      onError(error);
                      onNotify("Could not copy the workspace repository path.", "warning");
                    });
                  }}
                  disabled={!status.root}
                >
                  Copy repository path
                </Button>
                <Button icon="refresh" onClick={() => void refreshStatus()}>Check again</Button>
              </div>
            }
          />
        </div>
      ) : status ? (
        <div className="source-control-main">
          <section className="source-control-change-pane" aria-label="Git changes">
            <div className="source-control-change-toolbar">
              <strong>Changes</strong>
              <Badge tone={pendingCount ? "warning" : "success"}>{pendingCount} pending</Badge>
              <small id="source-control-keyboard-hint" className="source-control-keyboard-hint" title="Keyboard shortcuts: Up/Down move, Enter diff, O open, S or U stage or unstage, D discard, C focus commit.">↑↓ move · Enter diff · O open · S/U stage · D discard · C commit</small>
              <span />
              <Button
                icon="plus"
                className="source-control-toolbar-button"
                disabled={
                  mutationDisabled ||
                  (status.unstaged.length === 0 &&
                    status.untracked.length === 0 &&
                    status.conflicts.length === 0)
                }
                onClick={stageAll}
                title="Stage all by passing the exact path . to Git"
              >
                Stage all
              </Button>
              <Button
                icon="arrow"
                className="source-control-toolbar-button source-control-toolbar-button--unstage"
                disabled={mutationDisabled || status.staged.length === 0}
                onClick={unstageAll}
                title="Unstage all by passing the exact path . to Git"
              >
                Unstage all
              </Button>
            </div>
            {status.clean && (
              <div className="source-control-clean-banner" role="status">
                <Icon name="check" size={15} />
                <span>Working tree clean — no pending changes.</span>
              </div>
            )}
            <div
              className="source-control-change-tree"
              role="tree"
              aria-label="Git change groups"
              aria-describedby="source-control-keyboard-hint"
              tabIndex={0}
              onKeyDown={handleTreeKeyDown}
            >
              {GROUPS.map((group) => {
                const entries = groupEntries(status, group.key);
                const expanded = expandedGroups[group.key];
                return (
                  <section className={`source-control-group source-control-group--${group.key}`} key={group.key}>
                    <div className="source-control-group-header">
                      <button
                        type="button"
                        className="source-control-group-toggle"
                        data-source-control-nav={groupKey(group.key)}
                        ref={registerFocusNode(groupKey(group.key))}
                        tabIndex={focusKey === groupKey(group.key) ? 0 : -1}
                        role="treeitem"
                        aria-level={1}
                        aria-expanded={expanded}
                        onClick={() => setExpandedGroups((current) => ({ ...current, [group.key]: !current[group.key] }))}
                        onKeyDown={(event) => {
                          if (event.defaultPrevented) return;
                          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                            event.preventDefault();
                            moveFocus(groupKey(group.key), event.key === "ArrowDown" ? 1 : -1);
                          }
                        }}
                      >
                        <Icon name="chevron" size={13} />
                        <strong>{group.label}</strong>
                        <Badge tone={group.key === "conflicts" && entries.length ? "danger" : "neutral"}>{entries.length}</Badge>
                      </button>
                      <span title={group.description}>{group.description}</span>
                    </div>
                    {expanded && entries.length === 0 && <p className="source-control-group-empty">No {group.label.toLowerCase()}.</p>}
                    {expanded && entries.map((entry) => {
                      const itemKey = selectionKey(group.key, entry.path);
                      const isSelected = selected?.group === group.key && selected.path === entry.path;
                      const actionLabel =
                        group.key === "staged"
                          ? `Unstage ${entry.path}`
                          : `Stage ${entry.path}`;
                      const discardAffectedPaths = entry.originalPath
                        ? [entry.path, entry.originalPath]
                        : [entry.path];
                      const dirtyDiscardPath =
                        pathComparisonMode === null
                          ? undefined
                          : discardAffectedPaths.find((path) =>
                              dirtyPathMatches(
                                dirtyPaths,
                                path,
                                pathComparisonMode === "case-insensitive",
                              ),
                            );
                      return (
                        <div
                          className={`source-control-file-row ${isSelected ? "is-selected" : ""}`}
                          key={`${group.key}:${entry.path}:${entry.originalPath || ""}`}
                          data-source-control-nav={itemKey}
                          ref={registerFocusNode(itemKey)}
                          role="treeitem"
                          aria-level={2}
                          aria-selected={isSelected}
                          aria-keyshortcuts="ArrowDown ArrowUp Enter Space S U D C O"
                          aria-label={`${entry.path}, ${entryStatusLabel(entry, group.key)}`}
                          tabIndex={focusKey === itemKey ? 0 : -1}
                          onClick={() => selectFile(group.key, entry)}
                          onKeyDown={(event) => {
                            if (event.defaultPrevented) return;
                            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                              event.preventDefault();
                              moveFocus(itemKey, event.key === "ArrowDown" ? 1 : -1);
                              return;
                            }
                            if (event.key === "Enter" || event.key === " ") {
                              event.preventDefault();
                              selectFile(group.key, entry);
                              return;
                            }
                            if (event.target !== event.currentTarget) return;
                            const key = event.key.toLowerCase();
                            if (key === "s" && group.key !== "staged") {
                              event.preventDefault();
                              stagePath(group.key, entry);
                            } else if (key === "u" && group.key === "staged") {
                              event.preventDefault();
                              stagePath(group.key, entry);
                            } else if (key === "d" && isDiscardable(entry)) {
                              event.preventDefault();
                              beginDiscard(entry);
                            } else if (key === "c") {
                              event.preventDefault();
                              commitInputRef.current?.focus();
                            } else if (key === "o") {
                              event.preventDefault();
                              selectFile(group.key, entry);
                              onOpenFile(entry.path);
                            }
                          }}
                        >
                          <span className="source-control-file-status" title={statusCodeForGroup(entry, group.key) || entry.statusCode || entry.status}>
                            <Badge tone={entryBadgeTone(entry, group.key)}>{entryStatusLabel(entry, group.key)}</Badge>
                            {entry.staged && entry.unstaged && <Badge tone="accent">Partial</Badge>}
                          </span>
                          <span className="source-control-file-copy">
                            <code title={entry.path}>{entry.path}</code>
                            {entry.originalPath && <small>from {entry.originalPath}</small>}
                          </span>
                          <span className="source-control-file-actions">
                            <Button
                              icon={group.key === "staged" ? "arrow" : "plus"}
                              tone="quiet"
                              className={group.key === "staged" ? "source-control-unstage-button" : ""}
                              aria-label={actionLabel}
                              title={`${actionLabel} (${group.key === "staged" ? "U" : "S"})`}
                              tabIndex={-1}
                              disabled={mutationDisabled}
                              onClick={(event) => {
                                event.stopPropagation();
                                stagePath(group.key, entry);
                              }}
                            >
                              <span className="source-control-action-label">{group.key === "staged" ? "Unstage" : "Stage"}</span>
                            </Button>
                            {isDiscardable(entry) && (
                              <Button
                                icon="trash"
                                tone="danger"
                                className="source-control-discard-button"
                                aria-label={`Discard ${entry.path}`}
                                title={
                                  pathComparisonMode === null
                                    ? "Discard temporarily unavailable; refresh and try again."
                                    : dirtyDiscardPath
                                      ? "Editor buffer has unsaved changes"
                                      : `Discard ${entry.path} (D)`
                                }
                                tabIndex={-1}
                                disabled={
                                  mutationDisabled ||
                                  pathComparisonMode === null ||
                                  dirtyDiscardPath !== undefined
                                }
                                onClick={(event) => {
                                  event.stopPropagation();
                                  beginDiscard(entry);
                                }}
                              >
                                <span className="source-control-action-label">Discard</span>
                              </Button>
                            )}
                            <Button
                              icon="diff"
                              tone="quiet"
                              aria-label="Open file"
                              title="Open file (O)"
                              tabIndex={-1}
                              onClick={(event) => {
                                event.stopPropagation();
                                selectFile(group.key, entry);
                                onOpenFile(entry.path);
                              }}
                            >
                              <span className="source-control-action-label">Open file</span>
                            </Button>
                          </span>
                        </div>
                      );
                    })}
                  </section>
                );
              })}
            </div>
          </section>

          <aside className="source-control-right-pane" aria-label="Commit and diff review">
            <section className="source-control-commit-card" aria-label="Commit staged changes">
              <header>
                <div>
                  <strong>Commit</strong>
                  <small>Only staged content is recorded</small>
                </div>
                <Badge tone={status.staged.length ? "accent" : "neutral"}>{status.staged.length} staged</Badge>
              </header>
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  void commit();
                }}
              >
                <label htmlFor="source-control-commit-message">Commit message</label>
                <textarea
                  id="source-control-commit-message"
                  ref={commitInputRef}
                  aria-keyshortcuts="Control+Enter Meta+Enter"
                  value={commitDraft}
                  onChange={(event) => setCommitDraft(event.target.value)}
                  placeholder="Describe the staged changes"
                  rows={3}
                  disabled={mutationDisabled}
                  onKeyDown={(event) => {
                    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
                      event.preventDefault();
                      void commit();
                    }
                  }}
                />
                <div className="source-control-commit-actions">
                  <label className="source-control-amend-toggle">
                    <input
                      type="checkbox"
                      checked={amend}
                      disabled={mutationDisabled}
                      onChange={(event) => setAmend(event.target.checked)}
                    />
                    <span>Amend last commit</span>
                  </label>
                  <Button icon="save" tone="primary" type="submit" disabled={!canCommit} title="Commit staged changes (Ctrl+Enter)">
                    {amend ? "Amend commit" : "Commit staged"}
                  </Button>
                </div>
                {status.conflicts.length > 0 && <small className="source-control-commit-warning">Resolve {status.conflicts.length} conflict{status.conflicts.length === 1 ? "" : "s"} before committing.</small>}
                {!status.staged.length && <small className="source-control-commit-warning">Stage at least one change to enable commit.</small>}
                {commitOutputTruncated && <small className="source-control-commit-warning">Git reported capped commit output; the commit succeeded, but response details may be incomplete.</small>}
              </form>
            </section>

            <section ref={diffCardRef} className="source-control-diff-card" aria-label="Selected Git diff">
              <header>
                <div>
                  <strong>Diff review</strong>
                  {selected && <code title={selected.path}>{selected.path}</code>}
                </div>
                {selected && <button type="button" className="icon-button" aria-label="Clear selected diff" onClick={() => setSelected(null)}><Icon name="close" size={14} /></button>}
              </header>
              {!selected ? (
                <EmptyState icon="diff" title="Select a changed file" detail="The selected path will be compared using Git's working-tree, index, or untracked comparison." />
              ) : diffLoading ? (
                <div className="source-control-diff-loading"><Spinner /><span>Reading Git diff</span></div>
              ) : diffError ? (
                <div className="source-control-diff-error"><Icon name="error" size={17} /><strong>Diff unavailable</strong><p>{diffError}</p><Button icon="refresh" onClick={() => { setDiffRetry((current) => current + 1); }}>Retry diff</Button></div>
              ) : diff ? (
                <div className="source-control-diff-body">
                  <div className="source-control-diff-meta">
                    <Badge tone={diff.conflict ? "danger" : diff.binary || diff.unsupported ? "warning" : "cyan"}>{comparisonLabel(diff.comparison as GitComparison)}</Badge>
                    <span>{selectedEntry ? entryStatusLabel(selectedEntry, selected.group) : diff.status || "Git change"}</span>
                    {diff.originalPath && <span title={diff.originalPath}>from {diff.originalPath}</span>}
                  </div>
                  {outputWasTruncated(diff) && <div className="source-control-diff-message source-control-diff-message--warning"><Icon name="warning" size={15} /><span>Git capped this diff response; the textual preview may be incomplete.</span></div>}
                  {diff.conflict && <div className="source-control-diff-message source-control-diff-message--danger"><Icon name="warning" size={15} /><span>This path is unresolved. Git has not selected either side; resolve it through normal editing and staging.</span></div>}
                  {diff.binary && <div className="source-control-diff-message source-control-diff-message--warning"><Icon name="files" size={15} /><span>Git reports binary content; a textual diff is unavailable.</span></div>}
                  {selectedEntry && statusCodeForGroup(selectedEntry, selected.group).toUpperCase() === "D" && <div className="source-control-diff-message source-control-diff-message--warning"><Icon name="trash" size={15} /><span>This path is deleted in the selected comparison; Git's removal is shown when textual content is available.</span></div>}
                  {selectedEntry && ["R", "C"].includes(statusCodeForGroup(selectedEntry, selected.group).toUpperCase()) && selectedEntry.originalPath && <div className="source-control-diff-message"><Icon name="arrow" size={15} /><span>{statusCodeForGroup(selectedEntry, selected.group).toUpperCase() === "C" ? "Copied from" : "Renamed from"} <code>{selectedEntry.originalPath}</code> to <code>{selectedEntry.path}</code>.</span></div>}
                  {diff.unsupported && !diff.binary && !diff.conflict && <div className="source-control-diff-message source-control-diff-message--warning"><Icon name="warning" size={15} /><span>{diff.message || "Git cannot provide a textual diff for this path."}</span></div>}
                  {diff.patch && !diff.binary ? <pre className="source-control-diff-patch">{renderPatch(diff.patch)}</pre> : !diff.binary && !diff.conflict && !diff.message && <div className="source-control-diff-message"><Icon name="activity" size={15} /><span>No textual patch was returned by Git.</span></div>}
                  {diff.message && !diff.unsupported && !diff.binary && !diff.conflict && <div className="source-control-diff-message"><span>{diff.message}</span></div>}
                </div>
              ) : (
                <div className="source-control-diff-loading"><Spinner small /><span>Preparing comparison</span></div>
              )}
            </section>
          </aside>
        </div>
      ) : null}

      {discardConfirmation && (
        <div className="source-control-modal-backdrop" role="presentation">
          <form
            ref={discardDialogRef}
            className="source-control-discard-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="source-control-discard-title"
            aria-describedby="source-control-discard-description"
            aria-keyshortcuts="Escape Tab Enter"
            onSubmit={(event) => {
              event.preventDefault();
              void confirmDiscard();
            }}
          >
            <header>
              <div>
                <Icon name="warning" size={19} />
                <h2 id="source-control-discard-title">Discard working-tree content?</h2>
              </div>
              <button type="button" className="icon-button" aria-label="Cancel discard" onClick={closeDiscardDialog}><Icon name="close" size={16} /></button>
            </header>
            <p id="source-control-discard-description">
              {discardConfirmation.untracked
                ? "This permanently deletes the selected untracked file; Git cannot recover it."
                : "This destructive Git operation removes the selected working-tree change without touching staged content."} Confirm the exact path.
            </p>
            <ul className="source-control-discard-paths">{discardConfirmation.paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul>
            <label className="source-control-discard-confirmation">
              <span>Type the path exactly to continue</span>
              <input
                ref={discardInputRef}
                value={discardPhrase}
                onChange={(event) => { setDiscardPhrase(event.target.value); setDiscardError(""); }}
                spellCheck={false}
                autoComplete="off"
                aria-invalid={Boolean(discardError)}
                placeholder={discardedPath}
              />
            </label>
            {discardError && <p className="source-control-discard-error" role="alert">{discardError}</p>}
            <footer>
              <Button type="button" tone="quiet" onClick={closeDiscardDialog}>Cancel</Button>
              <Button type="submit" icon="trash" tone="danger" disabled={!discardConfirmed || pathComparisonMode === null || busy !== ""}>Discard listed path</Button>
            </footer>
          </form>
        </div>
      )}
    </section>
  );
}
