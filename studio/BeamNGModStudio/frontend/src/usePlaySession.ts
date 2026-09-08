import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
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
  setSelection: (ids: string[]) => Promise<void>;
  toggleCollection: (id: string) => Promise<void>;
  addCollections: (ids: string[]) => Promise<void>;
  clearSelection: () => Promise<void>;
  switchProfile: (profileId: string) => Promise<void>;
  createProfile: () => Promise<ModProfile>;
  saveAsNew: () => Promise<ModProfile>;
  updateProfile: () => Promise<ModProfile>;
  revertProfile: () => Promise<void>;
  renameProfile: (profileId: string, name: string) => Promise<ModProfile>;
  deleteProfile: (profileId: string) => Promise<void>;
  launchSelection: () => Promise<PlayResult>;
  refreshPreview: () => Promise<void>;
  reloadState: () => Promise<void>;
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

function statePayload(
  profileId: string,
  selection: readonly string[],
  defaultSelection: readonly string[],
  notices: readonly string[],
): PlayState {
  return {
    profileId,
    collectionIds: uniqueIDs(selection),
    defaultCollectionIds: uniqueIDs(defaultSelection),
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
): PlaySession {
  const organizationRef = useRef<OrganizationState | null>(organization);
  const onOrganizationRef = useRef(onOrganization);
  const onErrorRef = useRef(onError);
  onOrganizationRef.current = onOrganization;
  onErrorRef.current = onError;
  organizationRef.current = organization;

  const [selection, setSelectionState] = useState<string[]>([]);
  const [defaultSelection, setDefaultSelectionState] = useState<string[]>([]);
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
  const [baselineOverrides, setBaselineOverrides] = useState<Record<string, string[]>>({});

  const selectionRef = useRef<string[]>([]);
  const defaultSelectionRef = useRef<string[]>([]);
  const profileIdRef = useRef("");
  const previewRef = useRef<PlaySelection | null>(null);
  const noticesRef = useRef<string[]>([]);
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
    const deduped = Array.from(new Set(next.filter(Boolean)));
    noticesRef.current = deduped;
    setNoticesState(deduped);
  }, []);

  const appendNotice = useCallback((notice: string) => {
    if (!notice || noticesRef.current.includes(notice)) return;
    setNotices([...noticesRef.current, notice]);
  }, [setNotices]);

  const persistState = useCallback(async () => {
    const version = ++persistVersionRef.current;
    const payload = statePayload(
      profileIdRef.current,
      selectionRef.current,
      defaultSelectionRef.current,
      noticesRef.current,
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

  const refreshPreviewFor = useCallback(async (ids: readonly string[]) => {
    const requested = uniqueIDs(ids);
    const version = ++resolveVersionRef.current;
    setPreviewLoading(true);
    setPreviewError("");
    try {
      const resolved = await API.ResolvePlaySelection(requested);
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

  const setDraft = useCallback(async (
    ids: readonly string[],
    options: { userEdit?: boolean } = {},
  ) => {
    const next = uniqueIDs(ids);
    if (options.userEdit !== false) userEditVersionRef.current += 1;
    selectionRef.current = next;
    setSelectionState(next);
    if (!profileIdRef.current) {
      defaultSelectionRef.current = next;
      setDefaultSelectionState(next);
    }
    resultRef.current = null;
    setResult(null);
    await Promise.all([persistState(), refreshPreviewFor(next)]);
  }, [persistState, refreshPreviewFor]);

  const setSelection = useCallback(async (ids: string[]) => {
    if (operationBusyRef.current) return;
    await setDraft(ids);
  }, [setDraft]);

  const toggleCollection = useCallback(async (id: string) => {
    if (operationBusyRef.current || !id) return;
    const current = new Set(selectionRef.current);
    if (current.has(id)) current.delete(id);
    else current.add(id);
    await setDraft(Array.from(current));
  }, [setDraft]);

  const addCollections = useCallback(async (ids: string[]) => {
    if (operationBusyRef.current) return;
    await setDraft([...selectionRef.current, ...ids]);
  }, [setDraft]);

  const clearSelection = useCallback(async () => {
    if (operationBusyRef.current) return;
    await setDraft([]);
  }, [setDraft]);

  const selectedProfile = useMemo(() => {
    if (!profileId || !organization) return null;
    return (organization.profiles ?? []).find((profile) => profile.id === profileId) ?? null;
  }, [organization, profileId]);

  const dirty = Boolean(
    profileId &&
    selectedProfile &&
    !sameSet(
      selection,
      baselineOverrides[profileId] ?? uniqueIDs(selectedProfile.collectionIds),
    ),
  );

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
      }
      profileIdRef.current = nextProfileId;
      setProfileIdState(nextProfileId);
      const nextSelection = target ? uniqueIDs(target.collectionIds) : [...defaultSelectionRef.current];
      await setDraft(nextSelection, { userEdit: false });
      if (target) {
        setBaselineOverrides((current) => ({ ...current, [target.id]: uniqueIDs(target.collectionIds) }));
      }
    } finally {
      finishBusy();
    }
  }, [beginBusy, finishBusy, setDraft]);

  const createProfile = useCallback(async () => {
    if (!beginBusy("profile-create")) throw new Error("Another Play change is still being saved.");
    try {
      const created = await API.CreatePlayProfile([...selectionRef.current]);
      profileIdRef.current = created.id;
      setProfileIdState(created.id);
      setBaselineOverrides((current) => ({ ...current, [created.id]: uniqueIDs(created.collectionIds) }));
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
      const created = await API.CreatePlayProfile([...selectionRef.current]);
      profileIdRef.current = created.id;
      setProfileIdState(created.id);
      setBaselineOverrides((current) => ({ ...current, [created.id]: uniqueIDs(created.collectionIds) }));
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
      const updated = await API.UpdatePlayProfile(currentProfileId, [...selectionRef.current]);
      setBaselineOverrides((current) => ({ ...current, [updated.id]: uniqueIDs(updated.collectionIds) }));
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
    await setDraft(uniqueIDs(target.collectionIds), { userEdit: false });
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
        profileIdRef.current = "";
        setProfileIdState("");
        defaultSelectionRef.current = preserved;
        setDefaultSelectionState(preserved);
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

  const runPlayOperation = useCallback(async () => {
    if (operationBusyRef.current || busyRef.current) throw new Error("Another Play operation is already in progress.");
    const capturedSelection = [...selectionRef.current];
    operationBusyRef.current = "launch";
    setOperationBusy("launch");
    setResult(null);
    resultRef.current = null;
    try {
      let resolved = previewRef.current;
      if (!resolved || !sameSet(resolved.collectionIds ?? [], capturedSelection)) {
        resolved = await API.ResolvePlaySelection(capturedSelection);
        if (!sameSet(capturedSelection, selectionRef.current)) {
          throw new Error("The Play selection changed while it was being resolved. Review it and try again.");
        }
        previewRef.current = resolved;
        setPreviewState(resolved);
        setPreviewError("");
      }
      const request: PlayRequest = {
        collectionIds: capturedSelection,
        fingerprint: resolved.fingerprint,
      };
      const nextResult = await API.LaunchPlaySelection(request);
      if (!mountedRef.current) return nextResult;
      resultRef.current = nextResult;
      setResult(nextResult);
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
        const refreshed = await API.ResolvePlaySelection(capturedSelection).catch(() => null);
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

  const launchSelection = useCallback(() => runPlayOperation(), [runPlayOperation]);

  const refreshPreview = useCallback(async () => {
    await refreshPreviewFor(selectionRef.current);
  }, [refreshPreviewFor]);

  useEffect(() => {
    let active = true;
    hydrationEditVersionRef.current = userEditVersionRef.current;
    API.GetPlayState()
      .then((state) => {
        if (!active) return;
        hydrationStateRef.current = state;
        setHydrationReady(true);
      })
      .catch((error) => {
        if (!active) return;
        hydrationStateRef.current = null;
        setHydrationReady(true);
        onErrorRef.current(error);
      });
    return () => {
      active = false;
    };
  }, []);

  // The first indexed library can seed a collection and profile on the backend
  // after this hook already hydrated an empty state. Re-running hydration is
  // safe only while nothing is chosen here, so a seed is never able to
  // overwrite a selection the user made in the meantime.
  const reloadState = useCallback(async () => {
    if (operationBusyRef.current || busyRef.current) return;
    if (selectionRef.current.length > 0 || profileIdRef.current) return;
    const state = await API.GetPlayState();
    if (!mountedRef.current) return;
    hydrationStateRef.current = state;
    hydrationEditVersionRef.current = userEditVersionRef.current;
    setHydrationApplied(false);
    setHydrationReady(true);
  }, []);

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
    if (!hydrationReady || hydrationApplied || !organization) return;
    if (userEditVersionRef.current !== hydrationEditVersionRef.current) {
      appendNotice("A saved Play draft was not loaded because it changed before restore completed.");
      setHydrationApplied(true);
      void refreshPreviewFor(selectionRef.current);
      return;
    }
    const state = hydrationStateRef.current;
    const known = collectionIDs(organization);
    const validate = (ids: readonly string[]) => uniqueIDs(ids).filter((id) => known.has(id));
    const rawDefault = state?.defaultCollectionIds ?? [];
    const rawCurrent = state?.profileId ? state.collectionIds ?? [] : rawDefault.length ? rawDefault : state?.collectionIds ?? [];
    const validDefault = validate(rawDefault);
    const validCurrent = validate(rawCurrent);
    const missing = uniqueIDs([...rawDefault, ...rawCurrent]).filter((id) => !known.has(id));
    const savedProfile = state?.profileId
      ? (organization.profiles ?? []).find((profile) => profile.id === state.profileId)
      : null;
    const nextProfileId = savedProfile?.id ?? "";
    if (state?.profileId && !savedProfile) {
      appendNotice("A saved profile was removed; its working selection is now Default.");
    }
    if (missing.length) {
      appendNotice(`${missing.length} saved collection reference${missing.length === 1 ? " was" : "s were"} removed because the collection no longer exists.`);
    }
    const nextNotices = [...(state?.notices ?? []), ...noticesRef.current];
    setNotices(nextNotices);
    profileIdRef.current = nextProfileId;
    setProfileIdState(nextProfileId);
    defaultSelectionRef.current = validDefault;
    setDefaultSelectionState(validDefault);
    selectionRef.current = validCurrent;
    setSelectionState(validCurrent);
    setHydrationApplied(true);
    void refreshPreviewFor(validCurrent);
    void persistState();
  }, [appendNotice, hydrationApplied, hydrationReady, organization, persistState, refreshPreviewFor, setNotices]);

  useEffect(() => {
    if (!organization) return;
    const profiles = organization.profiles ?? [];
    setBaselineOverrides((current) => {
      const next: Record<string, string[]> = {};
      let changed = false;
      for (const profile of profiles) {
        const ids = uniqueIDs(profile.collectionIds);
        next[profile.id] = ids;
        if (!sameList(current[profile.id] ?? [], ids)) changed = true;
      }
      if (Object.keys(current).length !== Object.keys(next).length) changed = true;
      return changed ? next : current;
    });
    if (!hydrationApplied) return;
    const known = collectionIDs(organization);
    const nextSelection = selectionRef.current.filter((id) => known.has(id));
    const nextDefault = defaultSelectionRef.current.filter((id) => known.has(id));
    if (!sameList(nextSelection, selectionRef.current)) {
      const removed = selectionRef.current.filter((id) => !known.has(id));
      selectionRef.current = nextSelection;
      setSelectionState(nextSelection);
      appendNotice(`${removed.length} selected collection${removed.length === 1 ? " was" : "s were"} removed from Play because it no longer exists.`);
      void persistState();
      void refreshPreviewFor(nextSelection);
    }
    if (!sameList(nextDefault, defaultSelectionRef.current)) {
      defaultSelectionRef.current = nextDefault;
      setDefaultSelectionState(nextDefault);
      void persistState();
    }
    if (profileIdRef.current && !profiles.some((profile) => profile.id === profileIdRef.current)) {
      profileIdRef.current = "";
      setProfileIdState("");
      defaultSelectionRef.current = [...selectionRef.current];
      setDefaultSelectionState([...selectionRef.current]);
      appendNotice("The active profile was removed; the current selection is now Default.");
      void persistState();
    }
    void refreshPreviewFor(selectionRef.current);
  }, [appendNotice, hydrationApplied, organization, persistState, refreshPreviewFor]);

  const session = useMemo<PlaySession>(() => ({
    selection,
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
    setSelection,
    toggleCollection,
    addCollections,
    clearSelection,
    switchProfile,
    createProfile,
    saveAsNew,
    updateProfile,
    revertProfile,
    renameProfile,
    deleteProfile,
    launchSelection,
    refreshPreview,
    reloadState,
  }), [
    addCollections,
    busy,
    clearSelection,
    createProfile,
    defaultSelection,
    deleteProfile,
    dirty,
    hydrationReady,
    launchSelection,
    notices,
    operationBusy,
    preview,
    previewError,
    previewLoading,
    profileId,
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
    updateProfile,
    progress,
  ]);

  return session;
}
