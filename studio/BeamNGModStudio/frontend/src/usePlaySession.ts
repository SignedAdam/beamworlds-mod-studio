import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ArchiveDeploymentPlan,
  ModProfile,
  OrganizationState,
  PlayProgress,
  PlayRequest,
  PlayResult,
  PlayRuntimeState,
  PlaySelection,
  PlayState,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";

export type PlayBusyState =
  | ""
  | "profile-create"
  | "profile-update"
  | "profile-save-as-new"
  | "profile-rename"
  | "profile-delete"
  | "profile-switch";

export interface PlaySession {
  selection: string[];
  excludedSelection: string[];
  defaultSelection: string[];
  profileId: string;
  preview: PlaySelection | null;
  previewLoading: boolean;
  previewError: string;
  notices: string[];
  dirty: boolean;
  busy: PlayBusyState;
  operationBusy: "" | "launch";
  progress: PlayProgress | null;
  runtime: PlayRuntimeState | null;
  result: PlayResult | null;
  hydrationReady: boolean;
  /** Deployment plan from PlanPlayDeployment; null until planned. */
  deploymentPlan: ArchiveDeploymentPlan | null;
  /** True while PlanPlayDeployment is in flight. */
  planningDeployment: boolean;
  /** Error from the last PlanPlayDeployment call. */
  deploymentPlanError: string;
  setSelection: (ids: string[]) => Promise<void>;
  toggleCollection: (id: string) => Promise<void>;
  toggleExclusion: (id: string) => Promise<void>;
  addCollections: (ids: string[]) => Promise<void>;
  clearSelection: () => Promise<void>;
  switchProfile: (profileId: string) => Promise<void>;
  createProfile: () => Promise<ModProfile>;
  saveAsNew: () => Promise<ModProfile>;
  updateProfile: () => Promise<ModProfile>;
  revertProfile: () => Promise<void>;
  renameProfile: (profileId: string, name: string) => Promise<ModProfile>;
  deleteProfile: (profileId: string) => Promise<void>;
  /** Plan deployment and return the plan. The plan is also stored in session state. */
  planDeployment: () => Promise<ArchiveDeploymentPlan | null>;
  /** Launch with an explicit deployment fingerprint and copy allowance. */
  launchSelection: (deploymentFingerprint?: string, allowCopy?: boolean) => Promise<PlayResult>;
  refreshPreview: () => Promise<void>;
  reloadState: () => Promise<void>;
  /** Dismiss one persisted or session-only Play notice. */
  dismissNotice: (notice: string) => Promise<void>;
  /** Clear the current deployment plan (e.g. after selection change). */
  clearDeploymentPlan: () => void;
}

function uniqueNotices(notices: readonly string[] | null | undefined): string[] {
  return Array.from(new Set((notices ?? []).map((notice) => notice.trim()).filter(Boolean)));
}

// These frontend-only messages have no collection identity, so an old saved
// copy can never be reconciled with the current organization.
function legacyFrontendNotice(notice: string): boolean {
  return (
    /^\d+ saved collection references? (?:was|were) removed because the collection no longer exists\.$/.test(notice) ||
    /^\d+ selected collections? (?:was|were) removed from Play because it no longer exists\.$/.test(notice) ||
    notice === "A saved profile was removed; its working selection is now Default." ||
    notice === "The active profile was removed; the current selection is now Default."
  );
}

function persistedNotices(notices: readonly string[] | null | undefined): string[] {
  return uniqueNotices(notices).filter((notice) => !legacyFrontendNotice(notice));
}

function quotedList(values: readonly string[]): string {
  const quoted = values.map((value) => `“${value}”`);
  if (quoted.length <= 1) return quoted[0] ?? "";
  if (quoted.length === 2) return `${quoted[0]} and ${quoted[1]}`;
  return `${quoted.slice(0, -1).join(", ")}, and ${quoted[quoted.length - 1]}`;
}

function collectionRemovalNotice(
  ids: readonly string[],
  names: ReadonlyMap<string, string>,
): string {
  const labels = ids.map((id) => names.get(id));
  if (labels.some((name) => !name)) {
    return "A collection in your Play selection is no longer available. Review your selection before playing.";
  }
  const subject = quotedList(labels as string[]);
  return `${subject} ${labels.length === 1 ? "was" : "were"} deleted. Your Play selection has been updated; your mod files are unchanged.`;
}

function profileRemovalNotice(profileID: string, names: ReadonlyMap<string, string>): string {
  const name = names.get(profileID);
  return name
    ? `Profile “${name}” was deleted. Your current mod selection is now under Default.`
    : "Your saved profile was deleted. Your current mod selection is now under Default.";
}

function uniqueIDs(ids: readonly string[] | null | undefined): string[] {
  return Array.from(new Set((ids ?? []).filter((id): id is string => Boolean(id))));
}

function sameSet(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false;
  const rightSet = new Set(right);
  return left.every((id) => rightSet.has(id));
}

function sameList(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((id, index) => id === right[index]);
}

function errorMessage(error: unknown): string {
  if (typeof error === "string") return error;
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "object" && error !== null) {
    const record = error as Record<string, unknown>;
    for (const key of ["error", "message", "details"]) {
      if (typeof record[key] === "string" && record[key]) return record[key] as string;
    }
  }
  return "The Play operation could not be completed.";
}

function collectionIDs(organization: OrganizationState | null): Set<string> {
  return new Set((organization?.collections ?? []).map((collection) => collection.id));
}

/** Baseline for dirty-checking a profile: what it contained when loaded. */
type ProfileBaseline = { included: string[]; excluded: string[] };

function statePayload(
  profileId: string,
  selection: readonly string[],
  excludedSelection: readonly string[],
  defaultSelection: readonly string[],
  defaultExcludedSelection: readonly string[],
  notices: readonly string[],
): PlayState {
  return {
    profileId,
    collectionIds: uniqueIDs(selection),
    excludedCollectionIds: uniqueIDs(excludedSelection),
    defaultCollectionIds: uniqueIDs(defaultSelection),
    defaultExcludedCollectionIds: uniqueIDs(defaultExcludedSelection),
    notices: [...notices],
  };
}

/**
 * The Play draft is intentionally independent from the organization object.
 * Organization refreshes can update collection/profile metadata without
 * replacing a newer selection, and this hook is mounted once by App so a
 * navigation change never loses that draft.
 */
export function usePlaySession(
  organization: OrganizationState | null,
  onOrganization: (state: OrganizationState) => void,
  onError: (error: unknown) => void,
  // App keeps this false while startup scan/refetch can expose a transient
  // organization snapshot; no reference validation runs before it is true.
  organizationReady: boolean,
): PlaySession {
  const organizationRef = useRef<OrganizationState | null>(organization);
  const onOrganizationRef = useRef(onOrganization);
  const onErrorRef = useRef(onError);
  onOrganizationRef.current = onOrganization;
  onErrorRef.current = onError;
  organizationRef.current = organization;

  const [selection, setSelectionState] = useState<string[]>([]);
  const [excludedSelection, setExcludedSelectionState] = useState<string[]>([]);
  const [defaultSelection, setDefaultSelectionState] = useState<string[]>([]);
  const [, setDefaultExcludedSelectionState] = useState<string[]>([]);
  const [profileId, setProfileIdState] = useState("");
  const [preview, setPreviewState] = useState<PlaySelection | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [notices, setNoticesState] = useState<string[]>([]);
  const [busy, setBusy] = useState<PlayBusyState>("");
  const [operationBusy, setOperationBusy] = useState<"" | "launch">("");
  const [progress, setProgress] = useState<PlayProgress | null>(null);
  const [runtime, setRuntime] = useState<PlayRuntimeState | null>(null);
  const [result, setResult] = useState<PlayResult | null>(null);
  const [hydrationReady, setHydrationReady] = useState(false);
  const [hydrationApplied, setHydrationApplied] = useState(false);
  const [baselineOverrides, setBaselineOverrides] = useState<Record<string, ProfileBaseline>>({});
  const [deploymentPlan, setDeploymentPlan] = useState<ArchiveDeploymentPlan | null>(null);
  const [planningDeployment, setPlanningDeployment] = useState(false);
  const [deploymentPlanError, setDeploymentPlanError] = useState("");
  const deploymentPlanRef = useRef<ArchiveDeploymentPlan | null>(null);
  const deploymentPlanVersionRef = useRef(0);

  const selectionRef = useRef<string[]>([]);
  const excludedSelectionRef = useRef<string[]>([]);
  const defaultSelectionRef = useRef<string[]>([]);
  const defaultExcludedSelectionRef = useRef<string[]>([]);
  const profileIdRef = useRef("");
  const previewRef = useRef<PlaySelection | null>(null);
  const noticesRef = useRef<string[]>([]);
  const persistentNoticesRef = useRef<string[]>([]);
  const transientNoticesRef = useRef<string[]>([]);
  const collectionNamesRef = useRef(new Map<string, string>());
  const profileNamesRef = useRef(new Map<string, string>());
  const busyRef = useRef<PlayBusyState>("");
  const operationBusyRef = useRef<"" | "launch">("");
  const resultRef = useRef<PlayResult | null>(null);
  const userEditVersionRef = useRef(0);
  const hydrationStateRef = useRef<PlayState | null>(null);
  const hydrationEditVersionRef = useRef(0);
  const resolveVersionRef = useRef(0);
  const persistVersionRef = useRef(0);
  const persistTailRef = useRef<Promise<void>>(Promise.resolve());
  const mountedRef = useRef(true);

  useEffect(() => () => {
    mountedRef.current = false;
  }, []);

  const setNotices = useCallback((next: string[]) => {
    const deduped = uniqueNotices(next);
    noticesRef.current = deduped;
    setNoticesState(deduped);
  }, []);

  const replaceHydratedNotices = useCallback((saved: readonly string[] | null | undefined) => {
    persistentNoticesRef.current = persistedNotices(saved);
    setNotices([...persistentNoticesRef.current, ...transientNoticesRef.current]);
  }, [setNotices]);

  const appendNotice = useCallback((notice: string) => {
    const trimmed = notice.trim();
    if (!trimmed || noticesRef.current.includes(trimmed)) return;
    transientNoticesRef.current = [...transientNoticesRef.current, trimmed];
    setNotices([...noticesRef.current, trimmed]);
  }, [setNotices]);

  const persistState = useCallback(async () => {
    const version = ++persistVersionRef.current;
    const payload = statePayload(
      profileIdRef.current,
      selectionRef.current,
      excludedSelectionRef.current,
      defaultSelectionRef.current,
      defaultExcludedSelectionRef.current,
      persistentNoticesRef.current,
    );
    const write = persistTailRef.current
      .catch(() => undefined)
      .then(() => API.SavePlayState(payload))
      .then(() => undefined);
    persistTailRef.current = write.catch(() => undefined);
    try {
      await write;
    } catch (error) {
      if (version === persistVersionRef.current) onErrorRef.current(error);
    }
  }, []);

  const dismissNotice = useCallback(async (notice: string) => {
    const trimmed = notice.trim();
    if (!trimmed || !noticesRef.current.includes(trimmed)) return;
    const next = noticesRef.current.filter((current) => current !== trimmed);
    noticesRef.current = next;
    transientNoticesRef.current = transientNoticesRef.current.filter((current) => current !== trimmed);
    persistentNoticesRef.current = persistentNoticesRef.current.filter((current) => current !== trimmed);
    setNoticesState(next);
    await persistState();
  }, [persistState]);

  const refreshPreviewFor = useCallback(async (
    ids: readonly string[],
    excludedIds: readonly string[],
  ) => {
    const requested = uniqueIDs(ids);
    const requestedExcluded = uniqueIDs(excludedIds);
    const version = ++resolveVersionRef.current;
    setPreviewLoading(true);
    setPreviewError("");
    try {
      const resolved = await API.ResolvePlaySelection(requested, requestedExcluded);
      if (
        !mountedRef.current ||
        version !== resolveVersionRef.current ||
        !sameSet(requested, selectionRef.current)
      ) return null;
      previewRef.current = resolved;
      setPreviewState(resolved);
      return resolved;
    } catch (error) {
      if (mountedRef.current && version === resolveVersionRef.current) {
        setPreviewError(errorMessage(error));
        onErrorRef.current(error);
      }
      return null;
    } finally {
      if (mountedRef.current && version === resolveVersionRef.current) setPreviewLoading(false);
    }
  }, []);

  const refreshOrganization = useCallback(async () => {
    try {
      const refreshed = await API.Organization();
      if (mountedRef.current) onOrganizationRef.current(refreshed);
      return refreshed;
    } catch (error) {
      onErrorRef.current(error);
      return null;
    }
  }, []);

  const finishBusy = useCallback(() => {
    busyRef.current = "";
    setBusy("");
  }, []);

  const beginBusy = useCallback((next: Exclude<PlayBusyState, "">): boolean => {
    if (busyRef.current || operationBusyRef.current) return false;
    busyRef.current = next;
    setBusy(next);
    return true;
  }, []);

  /** Replace the entire draft (included + excluded). Pass `undefined` for
   *  excludedIds to keep the current excluded set unchanged. */
  const setDraft = useCallback(async (
    ids: readonly string[],
    excludedIds: readonly string[] | undefined,
    options: { userEdit?: boolean } = {},
  ) => {
    const next = uniqueIDs(ids);
    const nextExcluded = excludedIds !== undefined
      ? uniqueIDs(excludedIds)
      : excludedSelectionRef.current;
    if (options.userEdit !== false) userEditVersionRef.current += 1;
    selectionRef.current = next;
    setSelectionState(next);
    excludedSelectionRef.current = nextExcluded;
    setExcludedSelectionState(nextExcluded);
    if (!profileIdRef.current) {
      defaultSelectionRef.current = next;
      setDefaultSelectionState(next);
      defaultExcludedSelectionRef.current = nextExcluded;
      setDefaultExcludedSelectionState(nextExcluded);
    }
    resultRef.current = null;
    setResult(null);
    deploymentPlanRef.current = null;
    setDeploymentPlan(null);
    setDeploymentPlanError("");
    await Promise.all([persistState(), refreshPreviewFor(next, nextExcluded)]);
  }, [persistState, refreshPreviewFor]);

  const setSelection = useCallback(async (ids: string[]) => {
    if (operationBusyRef.current) return;
    await setDraft(ids, undefined);
  }, [setDraft]);

  const toggleCollection = useCallback(async (id: string) => {
    if (operationBusyRef.current || !id) return;
    const current = new Set(selectionRef.current);
    let nextExcluded: string[] | undefined;
    if (current.has(id)) {
      current.delete(id);
    } else {
      current.add(id);
      // Including a collection removes it from exclusions.
      if (excludedSelectionRef.current.includes(id)) {
        nextExcluded = excludedSelectionRef.current.filter((eid) => eid !== id);
      }
    }
    await setDraft(Array.from(current), nextExcluded);
  }, [setDraft]);

  const toggleExclusion = useCallback(async (id: string) => {
    if (operationBusyRef.current || !id) return;
    const current = new Set(excludedSelectionRef.current);
    let nextSelection: readonly string[] | undefined;
    if (current.has(id)) {
      current.delete(id);
    } else {
      current.add(id);
      // Excluding a collection removes it from inclusion.
      if (selectionRef.current.includes(id)) {
        nextSelection = selectionRef.current.filter((sid) => sid !== id);
      }
    }
    await setDraft(
      nextSelection ?? selectionRef.current,
      Array.from(current),
    );
  }, [setDraft]);

  const addCollections = useCallback(async (ids: string[]) => {
    if (operationBusyRef.current) return;
    // Adding also clears these from exclusions.
    const adding = new Set(ids);
    const nextExcluded = excludedSelectionRef.current.filter((id) => !adding.has(id));
    const excludedChanged = nextExcluded.length !== excludedSelectionRef.current.length;
    await setDraft([...selectionRef.current, ...ids], excludedChanged ? nextExcluded : undefined);
  }, [setDraft]);

  const clearSelection = useCallback(async () => {
    if (operationBusyRef.current) return;
    await setDraft([], []);
  }, [setDraft]);

  const selectedProfile = useMemo(() => {
    if (!profileId || !organization) return null;
    return (organization.profiles ?? []).find((profile) => profile.id === profileId) ?? null;
  }, [organization, profileId]);

  const dirty = useMemo(() => {
    if (!profileId || !selectedProfile) return false;
    const baseline = baselineOverrides[profileId];
    const baseIncluded = baseline?.included ?? uniqueIDs(selectedProfile.collectionIds);
    const baseExcluded = baseline?.excluded ?? uniqueIDs(selectedProfile.excludedCollectionIds);
    return !sameSet(selection, baseIncluded) || !sameSet(excludedSelection, baseExcluded);
  }, [baselineOverrides, excludedSelection, profileId, selectedProfile, selection]);

  const switchProfile = useCallback(async (nextProfileId: string) => {
    if (operationBusyRef.current) return;
    if (nextProfileId === profileIdRef.current) return;
    const profiles = organizationRef.current?.profiles ?? [];
    const target = nextProfileId ? profiles.find((profile) => profile.id === nextProfileId) : null;
    if (nextProfileId && !target) throw new Error("That saved profile is no longer available.");
    if (!beginBusy("profile-switch")) throw new Error("Another Play change is still being saved.");
    try {
      if (!profileIdRef.current) {
        defaultSelectionRef.current = [...selectionRef.current];
        setDefaultSelectionState([...selectionRef.current]);
        defaultExcludedSelectionRef.current = [...excludedSelectionRef.current];
        setDefaultExcludedSelectionState([...excludedSelectionRef.current]);
      }
      profileIdRef.current = nextProfileId;
      setProfileIdState(nextProfileId);
      const nextSelection = target ? uniqueIDs(target.collectionIds) : [...defaultSelectionRef.current];
      const nextExcluded = target ? uniqueIDs(target.excludedCollectionIds) : [...defaultExcludedSelectionRef.current];
      await setDraft(nextSelection, nextExcluded, { userEdit: false });
      if (target) {
        setBaselineOverrides((current) => ({
          ...current,
          [target.id]: {
            included: uniqueIDs(target.collectionIds),
            excluded: uniqueIDs(target.excludedCollectionIds),
          },
        }));
      }
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, setDraft]);

  const createProfile = useCallback(async () => {
    if (!beginBusy("profile-create")) throw new Error("Another Play change is still being saved.");
    try {
      const created = await API.CreatePlayProfile(
        [...selectionRef.current],
        [...excludedSelectionRef.current],
      );
      profileIdRef.current = created.id;
      setProfileIdState(created.id);
      setBaselineOverrides((current) => ({
        ...current,
        [created.id]: {
          included: uniqueIDs(created.collectionIds),
          excluded: uniqueIDs(created.excludedCollectionIds),
        },
      }));
      await refreshOrganization();
      await persistState();
      return created;
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, persistState, refreshOrganization]);

  const saveAsNew = useCallback(async () => {
    if (!beginBusy("profile-save-as-new")) throw new Error("Another Play change is still being saved.");
    try {
      const created = await API.CreatePlayProfile(
        [...selectionRef.current],
        [...excludedSelectionRef.current],
      );
      profileIdRef.current = created.id;
      setProfileIdState(created.id);
      setBaselineOverrides((current) => ({
        ...current,
        [created.id]: {
          included: uniqueIDs(created.collectionIds),
          excluded: uniqueIDs(created.excludedCollectionIds),
        },
      }));
      await refreshOrganization();
      await persistState();
      return created;
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, persistState, refreshOrganization]);

  const updateProfile = useCallback(async () => {
    const currentProfileId = profileIdRef.current;
    if (!currentProfileId) throw new Error("Default is an unnamed working selection and cannot be updated.");
    if (!beginBusy("profile-update")) throw new Error("Another Play change is still being saved.");
    try {
      const updated = await API.UpdatePlayProfile(
        currentProfileId,
        [...selectionRef.current],
        [...excludedSelectionRef.current],
      );
      setBaselineOverrides((current) => ({
        ...current,
        [updated.id]: {
          included: uniqueIDs(updated.collectionIds),
          excluded: uniqueIDs(updated.excludedCollectionIds),
        },
      }));
      await refreshOrganization();
      await persistState();
      return updated;
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, persistState, refreshOrganization]);

  const revertProfile = useCallback(async () => {
    const currentProfileId = profileIdRef.current;
    const target = (organizationRef.current?.profiles ?? []).find((profile) => profile.id === currentProfileId);
    if (!currentProfileId || !target) throw new Error("The saved profile is no longer available.");
    await setDraft(
      uniqueIDs(target.collectionIds),
      uniqueIDs(target.excludedCollectionIds),
      { userEdit: false },
    );
  }, [setDraft]);

  const renameProfile = useCallback(async (targetProfileId: string, name: string) => {
    const trimmed = name.trim();
    if (!trimmed) throw new Error("Profile names cannot be empty.");
    if (!beginBusy("profile-rename")) throw new Error("Another Play change is still being saved.");
    try {
      const renamed = await API.RenamePlayProfile(targetProfileId, trimmed);
      await refreshOrganization();
      return renamed;
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, refreshOrganization]);

  const deleteProfile = useCallback(async (targetProfileId: string) => {
    if (!beginBusy("profile-delete")) throw new Error("Another Play change is still being saved.");
    const wasActive = profileIdRef.current === targetProfileId;
    try {
      const nextOrganization = await API.DeletePlayProfile(targetProfileId);
      if (mountedRef.current) onOrganizationRef.current(nextOrganization);
      if (wasActive) {
        const preserved = [...selectionRef.current];
        const preservedExcluded = [...excludedSelectionRef.current];
        profileIdRef.current = "";
        setProfileIdState("");
        defaultSelectionRef.current = preserved;
        setDefaultSelectionState(preserved);
        defaultExcludedSelectionRef.current = preservedExcluded;
        setDefaultExcludedSelectionState(preservedExcluded);
        setBaselineOverrides((current) => {
          const next = { ...current };
          delete next[targetProfileId];
          return next;
        });
        await persistState();
      }
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, persistState]);

  const planDeployment = useCallback(async (): Promise<ArchiveDeploymentPlan | null> => {
    if (operationBusyRef.current || busyRef.current) throw new Error("Another Play operation is already in progress.");
    const capturedSelection = [...selectionRef.current];
    const capturedExcluded = [...excludedSelectionRef.current];
    const version = ++deploymentPlanVersionRef.current;
    setPlanningDeployment(true);
    setDeploymentPlanError("");
    try {
      // Ensure the selection is resolved first.
      let resolved = previewRef.current;
      if (
        !resolved ||
        !sameSet(resolved.collectionIds ?? [], capturedSelection) ||
        !sameSet(resolved.excludedCollectionIds ?? [], capturedExcluded)
      ) {
        resolved = await API.ResolvePlaySelection(capturedSelection, capturedExcluded);
        if (!mountedRef.current || version !== deploymentPlanVersionRef.current) return null;
        if (
          !sameSet(capturedSelection, selectionRef.current) ||
          !sameSet(capturedExcluded, excludedSelectionRef.current)
        ) {
          throw new Error("The Play selection changed while planning. Review it and try again.");
        }
        previewRef.current = resolved;
        setPreviewState(resolved);
        setPreviewError("");
      }
      const request: PlayRequest = {
        collectionIds: capturedSelection,
        excludedCollectionIds: capturedExcluded,
        fingerprint: resolved.fingerprint,
        deploymentFingerprint: "",
        allowCopy: false,
      };
      const plan: ArchiveDeploymentPlan = await API.PlanPlayDeployment(request);
      if (!mountedRef.current || version !== deploymentPlanVersionRef.current) return null;
      deploymentPlanRef.current = plan;
      setDeploymentPlan(plan);
      return plan;
    } catch (error) {
      if (mountedRef.current && version === deploymentPlanVersionRef.current) {
        setDeploymentPlanError(errorMessage(error));
      }
      return null;
    } finally {
      if (mountedRef.current && version === deploymentPlanVersionRef.current) {
        setPlanningDeployment(false);
      }
    }
  }, []);

  const clearDeploymentPlan = useCallback(() => {
    deploymentPlanVersionRef.current += 1;
    deploymentPlanRef.current = null;
    setDeploymentPlan(null);
    setDeploymentPlanError("");
    setPlanningDeployment(false);
  }, []);

  const runPlayOperation = useCallback(async (
    deploymentFingerprint?: string,
    allowCopy?: boolean,
  ) => {
    if (operationBusyRef.current || busyRef.current) throw new Error("Another Play operation is already in progress.");
    const capturedSelection = [...selectionRef.current];
    const capturedExcluded = [...excludedSelectionRef.current];
    operationBusyRef.current = "launch";
    setOperationBusy("launch");
    setResult(null);
    resultRef.current = null;
    try {
      let resolved = previewRef.current;
      if (
        !resolved ||
        !sameSet(resolved.collectionIds ?? [], capturedSelection) ||
        !sameSet(resolved.excludedCollectionIds ?? [], capturedExcluded)
      ) {
        resolved = await API.ResolvePlaySelection(capturedSelection, capturedExcluded);
        if (
          !sameSet(capturedSelection, selectionRef.current) ||
          !sameSet(capturedExcluded, excludedSelectionRef.current)
        ) {
          throw new Error("The Play selection changed while it was being resolved. Review it and try again.");
        }
        previewRef.current = resolved;
        setPreviewState(resolved);
        setPreviewError("");
      }
      const request: PlayRequest = {
        collectionIds: capturedSelection,
        excludedCollectionIds: capturedExcluded,
        fingerprint: resolved.fingerprint,
        deploymentFingerprint: deploymentFingerprint ?? "",
        allowCopy: allowCopy ?? false,
      };
      const nextResult = await API.LaunchPlaySelection(request);
      if (!mountedRef.current) return nextResult;
      resultRef.current = nextResult;
      setResult(nextResult);
      // Clear the deployment plan after successful launch.
      deploymentPlanRef.current = null;
      setDeploymentPlan(null);
      try {
        const nextRuntime = await API.GetPlayRuntimeState();
        if (mountedRef.current) setRuntime(nextRuntime);
      } catch (error) {
        onErrorRef.current(error);
      }
      return nextResult;
    } catch (error) {
      const message = errorMessage(error);
      // The backend refuses to activate a selection whose content changed since
      // the reviewed preview. Refresh the preview here and make the user press
      // Play again, so an unreviewed selection is never activated silently.
      if (/changed since it was reviewed/i.test(message)) {
        const refreshed = await API.ResolvePlaySelection(capturedSelection, capturedExcluded).catch(() => null);
        if (refreshed && mountedRef.current && sameSet(capturedSelection, selectionRef.current)) {
          previewRef.current = refreshed;
          setPreviewState(refreshed);
          setPreviewError("");
        }
        const staleError = new Error(
          "Your mods changed since this selection was reviewed. The preview has been refreshed - press Play again to use it.",
        );
        appendNotice(staleError.message);
        throw staleError;
      }
      throw error;
    } finally {
      operationBusyRef.current = "";
      if (mountedRef.current) setOperationBusy("");
    }
  }, [appendNotice]);

  const launchSelection = useCallback((depFingerprint?: string, depAllowCopy?: boolean) => runPlayOperation(depFingerprint, depAllowCopy), [runPlayOperation]);

  const refreshPreview = useCallback(async () => {
    await refreshPreviewFor(selectionRef.current, excludedSelectionRef.current);
  }, [refreshPreviewFor]);

  useEffect(() => {
    let active = true;
    hydrationEditVersionRef.current = userEditVersionRef.current;
    API.GetPlayState()
      .then((state) => {
        if (!active) return;
        hydrationStateRef.current = state;
        persistentNoticesRef.current = persistedNotices(state?.notices);
        setHydrationReady(true);
      })
      .catch((error) => {
        if (!active) return;
        hydrationStateRef.current = null;
        persistentNoticesRef.current = [];
        setHydrationReady(true);
        onErrorRef.current(error);
      });
    return () => {
      active = false;
    };
  }, []);

  // A scan can create or reconcile backend Play state after this hook has
  // mounted. Always refresh durable notices, but only re-run selection
  // hydration when the draft has not changed since the last restore.
  const reloadState = useCallback(async () => {
    if (operationBusyRef.current || busyRef.current) return;
    const requestEditVersion = userEditVersionRef.current;
    const state = await API.GetPlayState();
    if (!mountedRef.current) return;
    const draftChanged =
      requestEditVersion !== hydrationEditVersionRef.current ||
      userEditVersionRef.current !== requestEditVersion ||
      selectionRef.current.length > 0 ||
      Boolean(profileIdRef.current);
    replaceHydratedNotices(state?.notices);
    if (draftChanged) return;
    hydrationStateRef.current = state;
    hydrationEditVersionRef.current = requestEditVersion;
    setHydrationApplied(false);
    setHydrationReady(true);
  }, [replaceHydratedNotices]);

  useEffect(() => {
    let active = true;
    API.GetPlayRuntimeState()
      .then((state) => {
        if (active && mountedRef.current) setRuntime(state);
      })
      .catch((error) => {
        if (active) onErrorRef.current(error);
      });
    const stop = Events.On("play:progress", (event) => {
      if (mountedRef.current) setProgress(event.data as PlayProgress);
    });
    return () => {
      active = false;
      stop();
    };
  }, []);

  useEffect(() => {
    if (!organizationReady || !hydrationReady || hydrationApplied || !organization) return;
    for (const collection of organization.collections ?? []) {
      if (collection.id && collection.name.trim()) collectionNamesRef.current.set(collection.id, collection.name.trim());
    }
    for (const profile of organization.profiles ?? []) {
      if (profile.id && profile.name.trim()) profileNamesRef.current.set(profile.id, profile.name.trim());
    }
    const state = hydrationStateRef.current;
    if (userEditVersionRef.current !== hydrationEditVersionRef.current) {
      replaceHydratedNotices(state?.notices);
      appendNotice("Kept your new selection instead of restoring the previous one.");
      setHydrationApplied(true);
      void refreshPreviewFor(selectionRef.current, excludedSelectionRef.current);
      void persistState();
      return;
    }
    const known = collectionIDs(organization);
    const isValid = (id: string) => id === "all-mods" || known.has(id);
    const validate = (ids: readonly string[]) => uniqueIDs(ids).filter(isValid);
    const rawDefault = state?.defaultCollectionIds ?? [];
    const rawCurrent = state?.profileId ? state.collectionIds ?? [] : rawDefault.length ? rawDefault : state?.collectionIds ?? [];
    const rawDefaultExcluded = state?.defaultExcludedCollectionIds ?? [];
    const rawCurrentExcluded = state?.profileId
      ? state.excludedCollectionIds ?? []
      : rawDefaultExcluded.length ? rawDefaultExcluded : state?.excludedCollectionIds ?? [];
    const validDefault = validate(rawDefault);
    const validCurrent = validate(rawCurrent);
    const validDefaultExcluded = validate(rawDefaultExcluded);
    const validCurrentExcluded = validate(rawCurrentExcluded);
    const missing = uniqueIDs([
      ...rawDefault,
      ...rawCurrent,
      ...rawDefaultExcluded,
      ...rawCurrentExcluded,
    ]).filter((id) => !isValid(id));
    const savedProfile = state?.profileId
      ? (organization.profiles ?? []).find((profile) => profile.id === state.profileId)
      : null;
    const nextProfileId = savedProfile?.id ?? "";
    if (state?.profileId && !savedProfile) {
      appendNotice(profileRemovalNotice(state.profileId, profileNamesRef.current));
    }
    if (missing.length) appendNotice(collectionRemovalNotice(missing, collectionNamesRef.current));
    replaceHydratedNotices(state?.notices);
    profileIdRef.current = nextProfileId;
    setProfileIdState(nextProfileId);
    defaultSelectionRef.current = validDefault;
    setDefaultSelectionState(validDefault);
    defaultExcludedSelectionRef.current = validDefaultExcluded;
    setDefaultExcludedSelectionState(validDefaultExcluded);
    selectionRef.current = validCurrent;
    setSelectionState(validCurrent);
    excludedSelectionRef.current = validCurrentExcluded;
    setExcludedSelectionState(validCurrentExcluded);
    setHydrationApplied(true);
    void refreshPreviewFor(validCurrent, validCurrentExcluded);
    void persistState();
  }, [
    appendNotice,
    hydrationApplied,
    hydrationReady,
    organization,
    organizationReady,
    persistState,
    refreshPreviewFor,
    replaceHydratedNotices,
  ]);

  useEffect(() => {
    if (!organizationReady || !organization) return;
    for (const collection of organization.collections ?? []) {
      if (collection.id && collection.name.trim()) collectionNamesRef.current.set(collection.id, collection.name.trim());
    }
    const profiles = organization.profiles ?? [];
    for (const profile of profiles) {
      if (profile.id && profile.name.trim()) profileNamesRef.current.set(profile.id, profile.name.trim());
    }
    setBaselineOverrides((current) => {
      const next: Record<string, ProfileBaseline> = {};
      let changed = false;
      for (const profile of profiles) {
        const included = uniqueIDs(profile.collectionIds);
        const excluded = uniqueIDs(profile.excludedCollectionIds);
        next[profile.id] = { included, excluded };
        const prev = current[profile.id];
        if (!prev || !sameList(prev.included, included) || !sameList(prev.excluded, excluded)) changed = true;
      }
      if (Object.keys(current).length !== Object.keys(next).length) changed = true;
      return changed ? next : current;
    });
    if (!hydrationApplied) return;
    const known = collectionIDs(organization);
    const isValid = (id: string) => id === "all-mods" || known.has(id);
    const nextSelection = selectionRef.current.filter(isValid);
    const nextDefault = defaultSelectionRef.current.filter(isValid);
    const nextExcluded = excludedSelectionRef.current.filter(isValid);
    const nextDefaultExcluded = defaultExcludedSelectionRef.current.filter(isValid);
    if (!sameList(nextSelection, selectionRef.current)) {
      const removed = selectionRef.current.filter((id) => !isValid(id));
      selectionRef.current = nextSelection;
      setSelectionState(nextSelection);
      appendNotice(collectionRemovalNotice(removed, collectionNamesRef.current));
      void persistState();
    }
    if (!sameList(nextExcluded, excludedSelectionRef.current)) {
      excludedSelectionRef.current = nextExcluded;
      setExcludedSelectionState(nextExcluded);
      void persistState();
    }
    if (!sameList(nextDefault, defaultSelectionRef.current)) {
      defaultSelectionRef.current = nextDefault;
      setDefaultSelectionState(nextDefault);
      void persistState();
    }
    if (!sameList(nextDefaultExcluded, defaultExcludedSelectionRef.current)) {
      defaultExcludedSelectionRef.current = nextDefaultExcluded;
      setDefaultExcludedSelectionState(nextDefaultExcluded);
      void persistState();
    }
    if (profileIdRef.current && !profiles.some((profile) => profile.id === profileIdRef.current)) {
      const removedProfileID = profileIdRef.current;
      profileIdRef.current = "";
      setProfileIdState("");
      defaultSelectionRef.current = [...selectionRef.current];
      setDefaultSelectionState([...selectionRef.current]);
      defaultExcludedSelectionRef.current = [...excludedSelectionRef.current];
      setDefaultExcludedSelectionState([...excludedSelectionRef.current]);
      appendNotice(profileRemovalNotice(removedProfileID, profileNamesRef.current));
      void persistState();
    }
    void refreshPreviewFor(selectionRef.current, excludedSelectionRef.current);
  }, [appendNotice, hydrationApplied, organization, organizationReady, persistState, refreshPreviewFor]);

  const session = useMemo<PlaySession>(() => ({
    selection,
    excludedSelection,
    defaultSelection,
    profileId,
    preview,
    previewLoading,
    previewError,
    notices,
    dirty,
    busy,
    operationBusy,
    progress,
    runtime,
    result,
    hydrationReady,
    deploymentPlan,
    planningDeployment,
    deploymentPlanError,
    setSelection,
    toggleCollection,
    toggleExclusion,
    addCollections,
    clearSelection,
    switchProfile,
    createProfile,
    saveAsNew,
    updateProfile,
    revertProfile,
    renameProfile,
    deleteProfile,
    planDeployment,
    launchSelection,
    refreshPreview,
    reloadState,
    dismissNotice,
    clearDeploymentPlan,
  }), [
    dismissNotice,
    busy,
    clearDeploymentPlan,
    clearSelection,
    createProfile,
    defaultSelection,
    deleteProfile,
    deploymentPlan,
    deploymentPlanError,
    dirty,
    excludedSelection,
    hydrationReady,
    launchSelection,
    notices,
    operationBusy,
    planDeployment,
    planningDeployment,
    preview,
    previewError,
    previewLoading,
    profileId,
    progress,
    refreshPreview,
    reloadState,
    renameProfile,
    result,
    revertProfile,
    runtime,
    saveAsNew,
    selection,
    setSelection,
    switchProfile,
    toggleCollection,
    toggleExclusion,
    updateProfile,
  ]);

  return session;
}
