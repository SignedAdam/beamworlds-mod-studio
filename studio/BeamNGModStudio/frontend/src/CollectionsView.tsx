import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  CSSProperties,
  FormEvent,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
} from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  CollectionDetail,
  CollectionReference,
  CollectionUsage,
  LibraryItem,
  ModCollection,
  OrganizationState,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { CollectionCoverEditor } from "./CollectionArtwork";
import {
  CollectionCard,
  CollectionDialog,
  CollectionMenuPopup,
  type CollectionMenuAction,
} from "./CollectionUI";
import { Icon } from "./icons";
import { ModTable } from "./ModTable";
import {
  Badge,
  Button,
  Page,
  Spinner,
  formatBytes,
  formatDate,
  kindIcon,
  kindLabel,
} from "./ui";
import "./CollectionsView.css";

interface CollectionsViewProps {
  organization: OrganizationState | null;
  items: LibraryItem[];
  collectionID: string;
  onOpenCollection: (id: string) => void;
  onOrganization: (state: OrganizationState) => void;
  onAddToPlay: (ids: string[]) => void;
  onInspectMod: (entityID: string) => void;
  onNotify: (message: string, tone?: "success" | "error" | "info") => void;
  onError: (error: unknown) => void;
}

type SortKey = "name" | "updated" | "mods";
type DialogState =
  | { kind: "new"; parentID: string }
  | { kind: "rename"; collection: ModCollection }
  | { kind: "description"; collection: ModCollection }
  | { kind: "mods"; collectionID: string }
  | { kind: "children"; collectionID: string }
  | { kind: "parents"; childIDs: string[] }
  | {
      kind: "mod-destination";
      sourceCollectionID: string;
      entityIDs: string[];
    }
  | { kind: "usage"; collection: ModCollection; usage: CollectionUsage }
  | { kind: "delete"; ids: string[] };

type ContextState =
  | { kind: "collection"; ids: string[]; x: number; y: number }
  | {
      kind: "child";
      parentID: string;
      childID: string;
      x: number;
      y: number;
    }
  | {
      kind: "mod";
      collectionID: string;
      ids: string[];
      mod: LibraryItem;
      x: number;
      y: number;
    };

interface PickerSelection {
  query: string;
  selected: Set<string>;
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error.trim()) return error;
  if (error && typeof error === "object" && "message" in error) {
    const message = error.message;
    if (typeof message === "string" && message.trim()) return message;
  }
  return "The collection operation could not be completed.";
}

function collectionList(organization: OrganizationState | null): ModCollection[] {
  return organization?.collections ?? [];
}


function hasPath(
  collections: readonly ModCollection[],
  fromID: string,
  targetID: string,
): boolean {
  if (fromID === targetID) return true;
  const byID = new Map(collections.map((collection) => [collection.id, collection]));
  const visited = new Set<string>();
  const visit = (id: string): boolean => {
    if (id === targetID) return true;
    if (visited.has(id)) return false;
    visited.add(id);
    return (byID.get(id)?.childIds ?? []).some(visit);
  };
  return visit(fromID);
}

function childEligibility(
  collections: readonly ModCollection[],
  parentID: string,
  childID: string,
): string | undefined {
  if (parentID === childID) return "A collection cannot include itself.";
  const parent = collections.find((collection) => collection.id === parentID);
  if (parent?.childIds?.includes(childID)) return "Already included in this collection.";
  if (hasPath(collections, childID, parentID)) {
    return "Invalid relationship: this collection already reaches the parent, so adding it would create a cycle.";
  }
  return undefined;
}

function references(
  usage: CollectionUsage | undefined,
  key: "collections" | "profiles",
): CollectionReference[] {
  return usage?.[key] ?? [];
}

function unique(values: readonly string[]): string[] {
  return [...new Set(values.filter(Boolean))];
}

function formatCount(value: number): string {
  return Number.isFinite(value) ? value.toLocaleString() : "0";
}

function collectionCoverStyle(collection: ModCollection): CSSProperties {
  const url = collection.coverUrl?.trim();
  return url ? { backgroundImage: `url(${JSON.stringify(url)})` } : {};
}

function collectionName(
  collections: readonly ModCollection[],
  id: string,
): string {
  return collections.find((collection) => collection.id === id)?.name ?? "Unknown collection";
}

function moveValue(values: readonly string[], value: string, offset: -1 | 1): string[] {
  const index = values.indexOf(value);
  if (index < 0) return [...values];
  const next = Math.max(0, Math.min(values.length - 1, index + offset));
  if (next === index) return [...values];
  const result = [...values];
  result.splice(index, 1);
  result.splice(next, 0, value);
  return result;
}

function OperationError({ message }: { message: string }) {
  if (!message) return null;
  return (
    <div className="collections-inline-error" role="alert">
      <Icon name="error" size={16} />
      {message}
    </div>
  );
}

export function CollectionsView({
  organization,
  items,
  collectionID,
  onOpenCollection,
  onOrganization,
  onAddToPlay,
  onInspectMod,
  onNotify,
  onError,
}: CollectionsViewProps) {
  const collections = useMemo(() => collectionList(organization), [organization]);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<SortKey>("name");
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set());
  // Direct members are selected independently of the collection cards, so the
  // bulk actions in the editor cannot act on the wrong list.
  const [selectedModIDs, setSelectedModIDs] = useState<Set<string>>(new Set());
  const [detail, setDetail] = useState<CollectionDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState("");
  const [busy, setBusy] = useState("");
  const [operationError, setOperationError] = useState("");
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const [contextMenu, setContextMenu] = useState<ContextState | null>(null);
  const [coverEditorCollection, setCoverEditorCollection] =
    useState<ModCollection | null>(null);
  const [routeStack, setRouteStack] = useState<string[]>([]);
  const [memberQuery, setMemberQuery] = useState("");
  const [nameDraft, setNameDraft] = useState("");
  const [descriptionDraft, setDescriptionDraft] = useState("");
  const [editError, setEditError] = useState("");
  const [newName, setNewName] = useState("");
  const [newDescription, setNewDescription] = useState("");
  const [newError, setNewError] = useState("");
  const [modPicker, setModPicker] = useState<PickerSelection>({
    query: "",
    selected: new Set(),
  });
  const [childPicker, setChildPicker] = useState<PickerSelection>({
    query: "",
    selected: new Set(),
  });
  const [parentPicker, setParentPicker] = useState<PickerSelection>({
    query: "",
    selected: new Set(),
  });
  const [destinationPicker, setDestinationPicker] =
    useState<PickerSelection>({ query: "", selected: new Set() });
  const [deleteImpact, setDeleteImpact] = useState<CollectionUsage | null>(null);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  const [deleteConfirming, setDeleteConfirming] = useState(false);
  // The opened collection is owned by the parent view; routeStack only records
  // the breadcrumb path used to reach it.
  const currentCollectionID = collectionID;
  // Guards against a slow GetCollection response overwriting a newer selection.
  const detailLoadVersion = useRef(0);
  const detailMatchesRoute = detail?.collection.id === currentCollectionID;
  const activeDetail = detailMatchesRoute ? detail : null;
  const currentCollection =
    activeDetail?.collection ??
    collections.find((collection) => collection.id === currentCollectionID);

  const refreshOrganization = useCallback(async () => {
    const next = await API.Organization();
    onOrganization(next);
    return next;
  }, [onOrganization]);

  const refreshDetail = useCallback(
    async (id = currentCollectionID) => {
      if (!id) {
        setDetail(null);
        setDetailError("");
        return null;
      }
      const version = ++detailLoadVersion.current;
      setDetailLoading(true);
      setDetailError("");
      try {
        const next = await API.GetCollection(id);
        if (version === detailLoadVersion.current) setDetail(next);
        return next;
      } catch (error) {
        if (version === detailLoadVersion.current) {
          setDetail(null);
          setDetailError(errorMessage(error));
        }
        onError(error);
        return null;
      } finally {
        if (version === detailLoadVersion.current) setDetailLoading(false);
      }
    },
    [currentCollectionID, onError],
  );

  useEffect(() => {
    if (!currentCollectionID) {
      setRouteStack([]);
      setDetail(null);
      setDetailError("");
      return;
    }
    setRouteStack((previous) =>
      previous.length > 0 && previous[previous.length - 1] === currentCollectionID
        ? previous
        : [currentCollectionID],
    );
    void refreshDetail(currentCollectionID);
    setMemberQuery("");
    setSelectedModIDs(new Set());
  }, [currentCollectionID, refreshDetail]);

  useEffect(() => {
    if (!activeDetail) return;
    setNameDraft(activeDetail.collection.name);
    setDescriptionDraft(activeDetail.collection.description ?? "");
    setEditError("");
  }, [activeDetail?.collection.id]);

  useEffect(() => {
    const available = new Set(collections.map((collection) => collection.id));
    setSelectedIDs((previous) => {
      const next = new Set([...previous].filter((id) => available.has(id)));
      return next.size === previous.size ? previous : next;
    });
  }, [collections]);

  useEffect(() => {
    const direct = new Set(
      (activeDetail?.members ?? []).map((member) => member.entityId),
    );
    setSelectedModIDs((previous) => {
      const next = new Set([...previous].filter((id) => direct.has(id)));
      return next.size === previous.size ? previous : next;
    });
  }, [activeDetail?.members]);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || dialog || coverEditorCollection) return;
      setContextMenu(null);
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => document.removeEventListener("keydown", closeOnEscape);
  }, [coverEditorCollection, dialog]);

  const openCollection = useCallback(
    (id: string) => {
      setContextMenu(null);
      setOperationError("");
      if (!id) {
        setRouteStack([]);
        onOpenCollection("");
        return;
      }
      setRouteStack((previous) => {
        const existing = previous.indexOf(id);
        if (existing >= 0) return previous.slice(0, existing + 1);
        if (previous.length > 0 && previous[previous.length - 1] === currentCollectionID) {
          return [...previous, id];
        }
        return [id];
      });
      onOpenCollection(id);
    },
    [currentCollectionID, onOpenCollection],
  );

  const runOperation = useCallback(
    async <T,>(label: string, action: () => Promise<T>): Promise<T | null> => {
      if (busy) return null;
      setBusy(label);
      setOperationError("");
      try {
        return await action();
      } catch (error) {
        const message = errorMessage(error);
        setOperationError(message);
        onError(error);
        return null;
      } finally {
        setBusy("");
      }
    },
    [busy, onError],
  );

  const commitDetail = useCallback(
    async (next: CollectionDetail | null, notice?: string) => {
      if (!next) return;
      if (next.collection.id === currentCollectionID) {
        setDetail(next);
        setNameDraft(next.collection.name);
        setDescriptionDraft(next.collection.description ?? "");
      }
      await refreshOrganization();
      if (notice) onNotify(notice, "success");
    },
    [currentCollectionID, onNotify, refreshOrganization],
  );

  const toggleCollection = useCallback((id: string) => {
    setSelectedIDs((previous) => {
      const next = new Set(previous);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const filteredCollections = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase();
    const result = collections.filter((collection) => {
      if (!normalized) return true;
      return `${collection.name} ${collection.description ?? ""}`
        .toLocaleLowerCase()
        .includes(normalized);
    });
    result.sort((left, right) => {
      let comparison = 0;
      if (sort === "updated") {
        comparison = String(right.updatedAt ?? "").localeCompare(String(left.updatedAt ?? ""));
      } else if (sort === "mods") {
        comparison = (right.modCount ?? 0) - (left.modCount ?? 0);
      } else {
        comparison = left.name.localeCompare(right.name, undefined, { sensitivity: "base" });
      }
      return comparison || left.name.localeCompare(right.name, undefined, { sensitivity: "base" });
    });
    return result;
  }, [collections, query, sort]);

  const openNewCollection = useCallback((parentID = "") => {
    // Prefilled so creating a collection never demands naming work.
    const taken = new Set(collections.map((collection) => collection.name.trim().toLowerCase()));
    let suggestion = "Collection 1";
    for (let index = 1; taken.has(suggestion.toLowerCase()); index++) {
      suggestion = `Collection ${index + 1}`;
    }
    setNewName(suggestion);
    setNewDescription("");
    setNewError("");
    setDialog({ kind: "new", parentID });
    setContextMenu(null);
  }, [collections]);
  const openModPicker = useCallback(
    (id: string) => {
      setContextMenu(null);
      setOperationError("");
      const existing =
        detail?.collection.id === id
          ? (detail.members ?? []).map((member) => member.entityId)
          : [];
      setModPicker({ query: "", selected: new Set(existing) });
      setDialog({ kind: "mods", collectionID: id });
    },
    [detail],
  );

  const openChildPicker = useCallback(
    (id: string) => {
      setContextMenu(null);
      setOperationError("");
      const existing =
        collections.find((collection) => collection.id === id)?.childIds ?? [];
      setChildPicker({ query: "", selected: new Set(existing) });
      setDialog({ kind: "children", collectionID: id });
    },
    [collections],
  );

  const createCollection = async (event?: FormEvent) => {
    event?.preventDefault();
    const name = newName.trim();
    if (!name) {
      setNewError("Enter a collection name.");
      return;
    }
    if (!dialog || dialog.kind !== "new") return;
    const result = await runOperation("Creating collection", () =>
      API.CreateCollection(name, newDescription.trim(), dialog.parentID),
    );
    if (!result) return;
    setDialog(null);
    setNewName("");
    setNewDescription("");
    await refreshOrganization();
    onNotify(
      dialog.parentID
        ? `Created “${result.collection.name}” and added it to ${collectionName(collections, dialog.parentID)}.`
        : `Created “${result.collection.name}”.`,
      "success",
    );
    if (dialog.parentID && currentCollectionID === dialog.parentID) {
      setDetail(result);
    }
  };

  const openEditDialog = useCallback(
    (collection: ModCollection, kind: "rename" | "description") => {
      setNameDraft(collection.name);
      setDescriptionDraft(collection.description ?? "");
      setEditError("");
      setDialog({ kind, collection });
      setContextMenu(null);
    },
    [],
  );

  const updateCollection = async (event?: FormEvent) => {
    event?.preventDefault();
    if (!dialog || (dialog.kind !== "rename" && dialog.kind !== "description")) return;
    const name = nameDraft.trim();
    if (!name) {
      setEditError("Collection names cannot be empty.");
      return;
    }
    const targetID = dialog.collection.id;
    const result = await runOperation("Saving collection", () =>
      API.UpdateCollection(targetID, name, descriptionDraft.trim()),
    );
    if (!result) return;
    setDialog(null);
    await commitDetail(result, `Saved changes to “${result.collection.name}”.`);
  };

  const duplicateCollection = async (collection: ModCollection) => {
    setContextMenu(null);
    const result = await runOperation("Duplicating collection", () =>
      API.DuplicateCollection(collection.id),
    );
    if (!result) return;
    await refreshOrganization();
    onNotify(
      `Created “${result.collection.name}” with copied direct members and shared child references.`,
      "success",
    );
  };

  const openUsage = async (collection: ModCollection) => {
    setContextMenu(null);
    const result = await runOperation("Loading collection usage", () =>
      API.GetCollectionDeleteImpact([collection.id]),
    );
    if (result) setDialog({ kind: "usage", collection, usage: result });
  };

  const requestDelete = async (ids: string[]) => {
    const uniqueIDs = unique(ids);
    if (uniqueIDs.length === 0) return;
    setContextMenu(null);
    setDeleteImpact(null);
    setDeleteError("");
    setDeleteConfirming(false);
    setDialog({ kind: "delete", ids: uniqueIDs });
    setDeleteLoading(true);
    try {
      setDeleteImpact(await API.GetCollectionDeleteImpact(uniqueIDs));
    } catch (error) {
      setDeleteError(errorMessage(error));
      onError(error);
    } finally {
      setDeleteLoading(false);
    }
  };

  const confirmDelete = async () => {
    if (!dialog || dialog.kind !== "delete" || !deleteImpact) return;
    setDeleteConfirming(true);
    const result = await runOperation("Deleting collections", () =>
      API.DeleteCollections(dialog.ids),
    );
    setDeleteConfirming(false);
    if (!result) return;
    const removed = new Set(dialog.ids);
    setSelectedIDs((previous) => new Set([...previous].filter((id) => !removed.has(id))));
    setDialog(null);
    await onOrganization(result);
    onNotify(
      `${dialog.ids.length === 1 ? "Collection" : "Collections"} deleted. References were removed; mods and child collections were kept.`,
      "success",
    );
    if (removed.has(currentCollectionID)) openCollection("");
    else if (currentCollectionID) await refreshDetail(currentCollectionID);
  };

  const addSelectedMods = async () => {
    if (!dialog || dialog.kind !== "mods") return;
    const existing = new Set(
      dialog.collectionID === currentCollectionID
        ? (activeDetail?.members ?? []).map((member) => member.entityId)
        : [],
    );
    const selected = new Set(modPicker.selected);
    const added = [...selected].filter((id) => !existing.has(id));
    const removed = [...existing].filter((id) => !selected.has(id));
    if (added.length === 0 && removed.length === 0) {
      setDialog(null);
      return;
    }
    const result = await runOperation("Saving mod membership", async () => {
      let next: CollectionDetail | null = null;
      if (added.length > 0) {
        next = await API.SetCollectionMods(dialog.collectionID, added, true);
      }
      if (removed.length > 0) {
        next = await API.SetCollectionMods(dialog.collectionID, removed, false);
      }
      return next;
    });
    if (!result) return;
    setDialog(null);
    await commitDetail(
      result,
      `${added.length.toLocaleString()} added and ${removed.length.toLocaleString()} removed.`,
    );
  };

  const removeMods = async (ids: string[]) => {
    if (!currentCollectionID || ids.length === 0) return;
    const result = await runOperation("Removing mods", () =>
      API.SetCollectionMods(currentCollectionID, ids, false),
    );
    if (!result) return;
    setSelectedModIDs(new Set());
    await commitDetail(
      result,
      `${ids.length.toLocaleString()} direct mod${ids.length === 1 ? "" : "s"} removed.`,
    );
  };

  const removeSelectedMods = async () => {
    await removeMods([...selectedModIDs]);
  };
  const setModsEnabled = async (ids: string[], enabled: boolean) => {
    if (!currentCollectionID || ids.length === 0) return;
    const result = await runOperation(
      enabled ? "Enabling mods" : "Disabling mods",
      () => API.SetCollectionModsEnabled(currentCollectionID, ids, enabled),
    );
    if (!result) return;
    setSelectedModIDs(new Set());
    await commitDetail(
      result,
      `${ids.length.toLocaleString()} direct mod${ids.length === 1 ? "" : "s"} ${enabled ? "enabled" : "disabled"}.`,
    );
  };

  const setSelectedModsEnabled = async (enabled: boolean) => {
    await setModsEnabled([...selectedModIDs], enabled);
  };

  const toggleModEnabled = async (entityID: string, enabled: boolean) => {
    if (!currentCollectionID) return;
    const result = await runOperation(
      enabled ? "Enabling mod" : "Disabling mod",
      () => API.SetCollectionModsEnabled(currentCollectionID, [entityID], enabled),
    );
    if (result) await commitDetail(result);
  };

  const toggleChildEnabled = async (childID: string, enabled: boolean) => {
    if (!currentCollectionID) return;
    const result = await runOperation(
      enabled ? "Enabling included collection" : "Disabling included collection",
      () => API.SetCollectionChildrenEnabled(currentCollectionID, [childID], enabled),
    );
    if (result) await commitDetail(result);
  };

  const reorderMembers = async (
    kind: "mods" | "children",
    id: string,
    offset: -1 | 1,
  ) => {
    if (!activeDetail || !currentCollectionID) return;
    const directEntityIDs = (activeDetail.members ?? []).map(
      (member) => member.entityId,
    );
    const childIDs = (activeDetail.children ?? []).map(
      (child) => child.collectionId,
    );
    const entityIDs = moveValue(
      directEntityIDs,
      kind === "mods" ? id : "",
      offset,
    );
    const nextChildIDs = moveValue(
      childIDs,
      kind === "children" ? id : "",
      offset,
    );
    const result = await runOperation("Reordering members", () =>
      API.ReorderCollectionMembers(
        currentCollectionID,
        kind === "mods" ? entityIDs : directEntityIDs,
        kind === "children" ? nextChildIDs : childIDs,
      ),
    );
    if (result) await commitDetail(result, "Member order saved.");
  };

  const addSelectedChildren = async () => {
    if (!dialog || dialog.kind !== "children") return;
    const parentID = dialog.collectionID;
    const existing = new Set(
      collections.find((collection) => collection.id === parentID)?.childIds ?? [],
    );
    const selected = new Set(childPicker.selected);
    const added = [...selected].filter((id) => !existing.has(id));
    const removed = [...existing].filter((id) => !selected.has(id));
    if (added.length === 0 && removed.length === 0) {
      setDialog(null);
      return;
    }
    const result = await runOperation("Saving included collections", async () => {
      let next: CollectionDetail | null = null;
      if (added.length > 0) {
        next = await API.SetCollectionChildren(parentID, added, true);
      }
      if (removed.length > 0) {
        next = await API.SetCollectionChildren(parentID, removed, false);
      }
      return next;
    });
    if (!result) return;
    setDialog(null);
    await commitDetail(
      result,
      `${added.length.toLocaleString()} added and ${removed.length.toLocaleString()} removed.`,
    );
  };

  const removeChild = async (parentID: string, childID: string) => {
    const result = await runOperation("Removing included collection", () =>
      API.SetCollectionChildren(parentID, [childID], false),
    );
    if (!result) return;
    if (parentID === currentCollectionID) await commitDetail(result, "Collection reference removed.");
    else await refreshOrganization();
  };

  const openParentPicker = (childIDs: string[]) => {
    setParentPicker({ query: "", selected: new Set() });
    setDialog({ kind: "parents", childIDs: unique(childIDs) });
    setContextMenu(null);
  };

  const addChildrenToParent = async () => {
    if (!dialog || dialog.kind !== "parents") return;
    const parentID = [...parentPicker.selected][0] ?? "";
    if (!parentID) return;
    const invalid = dialog.childIDs
      .map((childID) => childEligibility(collections, parentID, childID))
      .find(Boolean);
    if (invalid) return;
    const result = await runOperation("Adding to parent", () =>
      API.SetCollectionChildren(parentID, dialog.childIDs, true),
    );
    if (!result) return;
    setDialog(null);
    await refreshOrganization();
    if (parentID === currentCollectionID) setDetail(result);
    onNotify(
      `${dialog.childIDs.length === 1 ? "Collection added" : "Collections added"} to “${collectionName(collections, parentID)}”.`,
      "success",
    );
  };

  const openDestinationPicker = (entityIDs: string[], sourceCollectionID: string) => {
    setDestinationPicker({ query: "", selected: new Set() });
    setDialog({
      kind: "mod-destination",
      sourceCollectionID,
      entityIDs: unique(entityIDs),
    });
    setContextMenu(null);
  };

  const addModsToDestinations = async () => {
    if (!dialog || dialog.kind !== "mod-destination") return;
    const destinationIDs = [...destinationPicker.selected];
    if (destinationIDs.length === 0) return;
    const result = await runOperation("Adding mods to collections", async () => {
      for (const destinationID of destinationIDs) {
        await API.SetCollectionMods(destinationID, dialog.entityIDs, true);
      }
      return true;
    });
    if (!result) return;
    setDialog(null);
    await refreshOrganization();
    if (dialog.sourceCollectionID === currentCollectionID) await refreshDetail(currentCollectionID);
    onNotify(
      `Added ${dialog.entityIDs.length.toLocaleString()} mod${dialog.entityIDs.length === 1 ? "" : "s"} to ${destinationIDs.length.toLocaleString()} collection${destinationIDs.length === 1 ? "" : "s"}.`,
      "success",
    );
  };

  const updateCoverDetail = async (next: CollectionDetail) => {
    setCoverEditorCollection(null);
    await commitDetail(next, "Collection cover saved.");
  };

  const directMembers = activeDetail?.members ?? [];
  const directEntityIDs = directMembers.map((member) => member.entityId);
  const childEdges = activeDetail?.children ?? [];
  const childIDs = childEdges.map((child) => child.collectionId);
  const itemByEntityID = useMemo(
    () => new Map(items.map((item) => [item.entityId, item] as const)),
    [items],
  );
  const enabledByEntityID = useMemo(
    () =>
      new Map(
        directMembers.map((member) => [member.entityId, member.enabled] as const),
      ),
    [directMembers],
  );
  const resolvedDirectMods = useMemo(
    () =>
      directMembers
        .map((member) => itemByEntityID.get(member.entityId))
        .filter((item): item is LibraryItem => Boolean(item)),
    [directMembers, itemByEntityID],
  );
  const visibleDirectMods = useMemo(() => {
    const normalized = memberQuery.trim().toLocaleLowerCase();
    return resolvedDirectMods.filter(
      (item) =>
        !normalized ||
        `${item.displayName} ${item.archivePath} ${item.rootPath}`
          .toLocaleLowerCase()
          .includes(normalized),
    );
  }, [memberQuery, resolvedDirectMods]);
  const staleMemberCount = directMembers.length - resolvedDirectMods.length;
  const childObjects = useMemo(
    () =>
      childEdges
        .map((edge) => {
          const child = collections.find(
            (collection) => collection.id === edge.collectionId,
          );
          return child ? { child, enabled: edge.enabled } : null;
        })
        .filter(
          (
            value,
          ): value is { child: ModCollection; enabled: boolean } =>
            Boolean(value),
        ),
    [childEdges, collections],
  );
  const visibleChildren = useMemo(() => {
    const normalized = memberQuery.trim().toLocaleLowerCase();
    if (!normalized) return childObjects;
    return childObjects.filter(({ child }) =>
      `${child.name} ${child.description ?? ""}`
        .toLocaleLowerCase()
        .includes(normalized),
    );
  }, [childObjects, memberQuery]);

  const contextActions = useMemo<CollectionMenuAction[]>(() => {
    if (!contextMenu) return [];
    if (contextMenu.kind === "collection") {
      const ids = contextMenu.ids;
      const first = collections.find((collection) => collection.id === ids[0]);
      if (!first) return [];
      const multiple = ids.length > 1;
      return [
        { label: "Open", icon: "folder", onClick: () => openCollection(first.id) },
        ...(multiple
          ? []
          : ([
              { label: "Rename", icon: "edit", onClick: () => openEditDialog(first, "rename") },
              { label: "Edit description", icon: "edit", onClick: () => openEditDialog(first, "description") },
              { label: "Edit cover", icon: "columns", onClick: () => { setCoverEditorCollection(first); setContextMenu(null); } },
              { label: "Duplicate", icon: "copy", onClick: () => void duplicateCollection(first) },
              { label: "Show where used", icon: "link", onClick: () => void openUsage(first) },
            ] as CollectionMenuAction[])),
        { label: "Add to another collection", icon: "folderPlus", onClick: () => openParentPicker(ids) },
        { label: "Add to Play selection", icon: "play", onClick: () => { setContextMenu(null); onAddToPlay(ids); } },
        { label: "Delete", icon: "trash", danger: true, onClick: () => void requestDelete(ids) },
      ];
    }
    if (contextMenu.kind === "child") {
      const child = collections.find((collection) => collection.id === contextMenu.childID);
      if (!child) return [];
      const parent = collections.find((collection) => collection.id === contextMenu.parentID);
      const edge = activeDetail?.children?.find(
        (candidate) => candidate.collectionId === child.id,
      );
      const enabled = edge?.enabled !== false;
      const index = parent?.childIds?.indexOf(child.id) ?? -1;
      return [
        { label: "Open", icon: "folder", onClick: () => openCollection(child.id) },
        { label: "Enable", icon: "check", disabled: Boolean(busy) || enabled, onClick: () => { setContextMenu(null); void toggleChildEnabled(child.id, true); } },
        { label: "Disable", icon: "close", disabled: Boolean(busy) || !enabled, onClick: () => { setContextMenu(null); void toggleChildEnabled(child.id, false); } },
        { label: "Remove from this collection", icon: "unlink", onClick: () => void removeChild(contextMenu.parentID, child.id) },
        { label: "Move up", icon: "arrow", disabled: index <= 0, detail: index <= 0 ? "Already first" : undefined, onClick: () => void reorderMembers("children", child.id, -1) },
        { label: "Move down", icon: "arrow", disabled: index < 0 || index >= (parent?.childIds?.length ?? 1) - 1, detail: "Local order only", onClick: () => void reorderMembers("children", child.id, 1) },
        { label: "Rename", icon: "edit", onClick: () => openEditDialog(child, "rename") },
        { label: "Edit cover", icon: "columns", onClick: () => { setCoverEditorCollection(child); setContextMenu(null); } },
        { label: "Duplicate", icon: "copy", onClick: () => void duplicateCollection(child) },
        { label: "Add to Play selection", icon: "play", onClick: () => { setContextMenu(null); onAddToPlay([child.id]); } },
        { label: "Show where used", icon: "link", onClick: () => void openUsage(child) },
        { label: "Delete collection", icon: "trash", danger: true, onClick: () => void requestDelete([child.id]) },
      ];
    }
    const mod = contextMenu.mod;
    const ids = contextMenu.ids;
    const enabled = ids.every((id) => enabledByEntityID.get(id) !== false);
    const disabled = ids.every((id) => enabledByEntityID.get(id) === false);
    const index = directEntityIDs.indexOf(mod.entityId);
    return [
      { label: "Inspect mod", icon: kindIcon(String(mod.kind)), onClick: () => { setContextMenu(null); onInspectMod(mod.entityId); } },
      { label: "Enable", icon: "check", disabled: Boolean(busy) || enabled, onClick: () => { setContextMenu(null); void setModsEnabled(ids, true); } },
      { label: "Disable", icon: "close", disabled: Boolean(busy) || disabled, onClick: () => { setContextMenu(null); void setModsEnabled(ids, false); } },
      { label: "Remove from this collection", icon: "unlink", disabled: Boolean(busy), onClick: () => { setContextMenu(null); void removeMods(ids); } },
      { label: "Add to another collection", icon: "folderPlus", onClick: () => openDestinationPicker(ids, currentCollectionID) },
      { label: "Move up", icon: "arrow", disabled: ids.length !== 1 || index <= 0, onClick: () => void reorderMembers("mods", mod.entityId, -1) },
      { label: "Move down", icon: "arrow", disabled: ids.length !== 1 || index < 0 || index >= directEntityIDs.length - 1, onClick: () => void reorderMembers("mods", mod.entityId, 1) },
    ];
  }, [
    activeDetail,
    busy,
    collections,
    contextMenu,
    currentCollectionID,
    directEntityIDs,
    enabledByEntityID,
    onAddToPlay,
    onInspectMod,
    openCollection,
    openDestinationPicker,
    openEditDialog,
    openParentPicker,
    reorderMembers,
    removeChild,
    removeMods,
    requestDelete,
    setModsEnabled,
    toggleChildEnabled,
  ]);
  const renderCollectionCards = () => {
    if (!organization) {
      return (
        <div className="collections-detail-loading" role="status">
          <Spinner />
          <span>Loading collections…</span>
        </div>
      );
    }
    if (filteredCollections.length === 0) {
      return (
        <p className="collections-empty">
          {collections.length === 0 ? "No collections." : "No collections match your search."}
        </p>
      );
    }
    return (
      <div className="collections-grid" role="list" aria-label="Collections">
        {filteredCollections.map((collection) => (
          <CollectionCard
            key={collection.id}
            collection={collection}
            selected={selectedIDs.has(collection.id)}
            primaryAction="open"
            onOpen={() => openCollection(collection.id)}
            onToggle={() => toggleCollection(collection.id)}
            onContextMenu={(event) => {
              event.preventDefault();
              const ids = selectedIDs.has(collection.id) && selectedIDs.size > 1 ? [...selectedIDs] : [collection.id];
              setContextMenu({ kind: "collection", ids, x: event.clientX, y: event.clientY });
            }}
            onMenu={(event) => {
              event.stopPropagation();
              const ids = selectedIDs.has(collection.id) && selectedIDs.size > 1 ? [...selectedIDs] : [collection.id];
              setContextMenu({ kind: "collection", ids, x: event.clientX, y: event.clientY });
            }}
          />
        ))}
      </div>
    );
  };
  const toggleModSelection = (item: LibraryItem) => {
    setSelectedModIDs((previous) => {
      const next = new Set(previous);
      if (next.has(item.entityId)) next.delete(item.entityId);
      else next.add(item.entityId);
      return next;
    });
  };

  const toggleAllVisibleMods = () => {
    const ids = visibleDirectMods.map((item) => item.entityId);
    if (ids.length === 0) return;
    setSelectedModIDs((previous) => {
      const next = new Set(previous);
      const allSelected = ids.every((id) => next.has(id));
      for (const id of ids) {
        if (allSelected) next.delete(id);
        else next.add(id);
      }
      return next;
    });
  };

  const openModContextMenu = (
    item: LibraryItem,
    event: ReactMouseEvent<HTMLTableRowElement>,
  ) => {
    const ids = selectedModIDs.has(item.entityId)
      ? [...selectedModIDs]
      : [item.entityId];
    if (!selectedModIDs.has(item.entityId)) setSelectedModIDs(new Set(ids));
    setContextMenu({
      kind: "mod",
      collectionID: currentCollectionID,
      ids,
      mod: item,
      x: event.clientX,
      y: event.clientY,
    });
  };

  const renderDeleteDialog = (ids: string[]) => {
    const names = ids.map((id) => collectionName(collections, id));
    const parentRefs = references(deleteImpact ?? undefined, "collections");
    const profileRefs = references(deleteImpact ?? undefined, "profiles");
    return (
      <CollectionDialog
        title={ids.length === 1 ? `Delete “${names[0]}”?` : `Delete ${ids.length.toLocaleString()} collections?`}
        onClose={() => { if (!deleteConfirming) setDialog(null); }}
        wide
        footer={
          <>
            <Button onClick={() => setDialog(null)} disabled={deleteConfirming}>Cancel</Button>
            <Button tone="danger" icon="trash" disabled={!deleteImpact || deleteLoading || deleteConfirming} onClick={() => void confirmDelete()}>
              {deleteConfirming ? "Deleting…" : "Delete collections"}
            </Button>
          </>
        }
      >
        {deleteLoading ? (
          <div className="collections-dialog__loading"><Spinner /><span>Calculating deletion impact…</span></div>
        ) : deleteError ? (
          <div className="collections-inline-error" role="alert"><Icon name="error" size={16} />{deleteError}</div>
        ) : (
          <div className="collections-impact">
            <p>This removes the organizational object and its references. Child collections and mod files are not deleted.</p>
            <p className="collections-impact__warning"><Icon name="warning" size={16} />Selected profiles lose these collection references. Any working Play selection will drop deleted roots.</p>
            <div className="collections-impact__columns">
              <ImpactList title="Affected parent collections" items={parentRefs} empty="No parent collection references." />
              <ImpactList title="Affected saved profiles" items={profileRefs} empty="No saved profile references." />
            </div>
          </div>
        )}
      </CollectionDialog>
    );
  };

  const renderPickerDialog = (state: Extract<DialogState, { kind: "mods" | "children" | "parents" | "mod-destination" }>) => {
    if (state.kind === "mods") {
      const normalized = modPicker.query.trim().toLocaleLowerCase();
      const filtered = items.filter((item) => `${item.displayName} ${item.archivePath} ${item.rootPath}`.toLocaleLowerCase().includes(normalized));
      const selectedVisible = filtered.filter((item) => modPicker.selected.has(item.entityId)).length;
      const toggle = (id: string) => setModPicker((previous) => {
        const selected = new Set(previous.selected);
        if (selected.has(id)) selected.delete(id); else selected.add(id);
        return { ...previous, selected };
      });
      return (
        <CollectionDialog
          title={`Add mods to “${collectionName(collections, state.collectionID)}”`}
          onClose={() => setDialog(null)}
          wide
          footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="save" disabled={Boolean(busy)} onClick={() => void addSelectedMods()}>Save membership ({modPicker.selected.size.toLocaleString()} selected)</Button></>}
        >
          <OperationError message={operationError} />
          <div className="collections-picker__toolbar">
            <label className="collections-search"><Icon name="search" size={16} /><input autoFocus value={modPicker.query} onChange={(event) => setModPicker((previous) => ({ ...previous, query: event.target.value }))} placeholder="Search every indexed mod…" aria-label="Search mods to add" /></label>
            <span>{modPicker.selected.size.toLocaleString()} selected · {filtered.length.toLocaleString()} matching</span>
            <Button onClick={() => setModPicker((previous) => ({ ...previous, selected: new Set([...previous.selected, ...filtered.map((item) => item.entityId)]) }))} disabled={filtered.length === 0 || selectedVisible === filtered.length}>Select all matching</Button>
            <Button onClick={() => setModPicker((previous) => ({ ...previous, selected: new Set([...previous.selected].filter((id) => !filtered.some((item) => item.entityId === id))) }))} disabled={selectedVisible === 0}>Clear matching</Button>
          </div>
          <div className="collections-picker-list" role="listbox" aria-label="Mods to add" aria-multiselectable="true">
            {filtered.length === 0 ? <p className="collections-empty">No mods match.</p> : filtered.map((item) => (
              <label className={`collections-picker-row${modPicker.selected.has(item.entityId) ? " is-selected" : ""}`} key={item.entityId}>
                <input type="checkbox" checked={modPicker.selected.has(item.entityId)} onChange={() => toggle(item.entityId)} />
                <span className="collections-picker-thumb">{item.thumbnailUrl ? <img src={item.thumbnailUrl} alt="" loading="lazy" /> : <Icon name={kindIcon(String(item.kind))} size={18} />}</span>
                <span className="collections-picker-row__text"><strong>{item.displayName || item.archivePath || "Unnamed mod"}</strong><small>{kindLabel(String(item.kind))} · {item.archivePath || "No archive path"} · {formatBytes(item.sizeBytes)}</small></span>
                {!item.linked && <Badge tone="warning">Unavailable</Badge>}
              </label>
            ))}
          </div>
          
        </CollectionDialog>
      );
    }
    if (state.kind === "children") {
      const parentID = state.collectionID;
      const parent = collections.find((collection) => collection.id === parentID);
      const normalized = childPicker.query.trim().toLocaleLowerCase();
      const candidates = collections.filter((collection) => collection.id !== parentID && `${collection.name} ${collection.description ?? ""}`.toLocaleLowerCase().includes(normalized));
      const toggle = (id: string) => setChildPicker((previous) => {
        const selected = new Set(previous.selected);
        if (selected.has(id)) selected.delete(id); else selected.add(id);
        return { ...previous, selected };
      });
      return (
        <CollectionDialog title={`Include collections in “${parent?.name ?? "collection"}”`} onClose={() => setDialog(null)} wide footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="save" disabled={Boolean(busy)} onClick={() => void addSelectedChildren()}>Save included collections</Button></>}>
          <OperationError message={operationError} />
          <div className="collections-picker__toolbar"><label className="collections-search"><Icon name="search" size={16} /><input autoFocus value={childPicker.query} onChange={(event) => setChildPicker((previous) => ({ ...previous, query: event.target.value }))} placeholder="Search collections…" aria-label="Search included collections" /></label><span>{childPicker.selected.size.toLocaleString()} selected · {candidates.length.toLocaleString()} matching</span><Button icon="plus" onClick={() => openNewCollection(parentID)}>New child</Button></div>
          <div className="collections-picker-list" role="listbox" aria-label="Collections to include" aria-multiselectable="true">
            {candidates.length === 0 ? <p className="collections-empty">No collections available.</p> : candidates.map((candidate) => {
              const reason = childEligibility(collections, parentID, candidate.id);
              const checked = childPicker.selected.has(candidate.id);
              const alreadyIncluded = parent?.childIds?.includes(candidate.id) ?? false;
              const cycleReason = reason && !alreadyIncluded ? reason : undefined;
              const explanation = alreadyIncluded ? "Already included; uncheck to remove." : cycleReason;
              return <label className={`collections-picker-row collections-picker-row--collection${checked ? " is-selected" : ""}${cycleReason ? " is-disabled" : ""}`} key={candidate.id} title={explanation}><input type="checkbox" checked={checked} disabled={Boolean(cycleReason)} onChange={() => toggle(candidate.id)} /><span className="collections-picker-cover" style={collectionCoverStyle(candidate)}>{!candidate.coverUrl && <Icon name="folder" size={21} />}</span><span className="collections-picker-row__text"><strong>{candidate.name}</strong><small>{formatCount(candidate.modCount)} effective mods · {candidate.childCount ?? 0} included collections</small>{explanation && <em>{explanation}</em>}</span></label>;
            })}
          </div>
          
        </CollectionDialog>
      );
    }
    if (state.kind === "parents") {
      const normalized = parentPicker.query.trim().toLocaleLowerCase();
      const candidates = collections.filter((collection) => `${collection.name} ${collection.description ?? ""}`.toLocaleLowerCase().includes(normalized));
      const selectedParentID = [...parentPicker.selected][0] ?? "";
      const select = (id: string) => setParentPicker((previous) => ({ ...previous, selected: previous.selected.has(id) ? new Set() : new Set([id]) }));
      return (
        <CollectionDialog title={state.childIDs.length === 1 ? "Add collection to a parent" : "Add collections to a parent"} onClose={() => setDialog(null)} wide footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="link" disabled={!selectedParentID || Boolean(state.childIDs.map((id) => childEligibility(collections, selectedParentID, id)).find(Boolean)) || Boolean(busy)} onClick={() => void addChildrenToParent()}>Add to parent</Button></>}>
          <OperationError message={operationError} />
          <div className="collections-picker__toolbar"><label className="collections-search"><Icon name="search" size={16} /><input autoFocus value={parentPicker.query} onChange={(event) => setParentPicker((previous) => ({ ...previous, query: event.target.value }))} placeholder="Search parent collections…" aria-label="Search parent collections" /></label><span>{state.childIDs.length.toLocaleString()} child{state.childIDs.length === 1 ? "" : "ren"} selected</span></div>
          <div className="collections-picker-list" role="listbox" aria-label="Parent collection destinations">
            {candidates.length === 0 ? <p className="collections-empty">No collections available.</p> : candidates.map((candidate) => { const reason = state.childIDs.map((id) => childEligibility(collections, candidate.id, id)).find(Boolean); const checked = selectedParentID === candidate.id; return <button type="button" className={`collections-picker-row collections-picker-row--choice${checked ? " is-selected" : ""}${reason ? " is-disabled" : ""}`} disabled={Boolean(reason)} onClick={() => select(candidate.id)} key={candidate.id} title={reason}><span className="collections-picker-cover" style={collectionCoverStyle(candidate)}>{!candidate.coverUrl && <Icon name="folder" size={21} />}</span><span className="collections-picker-row__text"><strong>{candidate.name}</strong><small>{formatCount(candidate.modCount)} effective mods · {candidate.childCount ?? 0} included collections</small>{reason && <em>{reason}</em>}</span><Icon name={checked ? "check" : "chevron"} size={16} /></button>; })}
          </div>
          
        </CollectionDialog>
      );
    }
    const normalized = destinationPicker.query.trim().toLocaleLowerCase();
    const sourceID = state.sourceCollectionID;
    const candidates = collections.filter((collection) => collection.id !== sourceID && `${collection.name} ${collection.description ?? ""}`.toLocaleLowerCase().includes(normalized));
    const toggle = (id: string) => setDestinationPicker((previous) => { const selected = new Set(previous.selected); if (selected.has(id)) selected.delete(id); else selected.add(id); return { ...previous, selected }; });
    return (
      <CollectionDialog title={`Add ${state.entityIDs.length.toLocaleString()} mod${state.entityIDs.length === 1 ? "" : "s"} to collections`} onClose={() => setDialog(null)} wide footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="folderPlus" disabled={destinationPicker.selected.size === 0 || Boolean(busy)} onClick={() => void addModsToDestinations()}>Add to selected collections</Button></>}>
        <OperationError message={operationError} />
        <div className="collections-picker__toolbar"><label className="collections-search"><Icon name="search" size={16} /><input autoFocus value={destinationPicker.query} onChange={(event) => setDestinationPicker((previous) => ({ ...previous, query: event.target.value }))} placeholder="Search destination collections…" aria-label="Search destination collections" /></label><span>{destinationPicker.selected.size.toLocaleString()} selected</span></div>
        <div className="collections-picker-list" role="listbox" aria-label="Destination collections" aria-multiselectable="true">
          {candidates.length === 0 ? <p className="collections-empty">No collections available.</p> : candidates.map((candidate) => { const checked = destinationPicker.selected.has(candidate.id); return <label className={`collections-picker-row collections-picker-row--collection${checked ? " is-selected" : ""}`} key={candidate.id}><input type="checkbox" checked={checked} onChange={() => toggle(candidate.id)} /><span className="collections-picker-cover" style={collectionCoverStyle(candidate)}>{!candidate.coverUrl && <Icon name="folder" size={21} />}</span><span className="collections-picker-row__text"><strong>{candidate.name}</strong><small>{formatCount(candidate.modCount)} effective mods</small></span></label>; })}
        </div>
      </CollectionDialog>
    );
  };

  const renderDialog = () => {
    if (!dialog) return null;
    if (dialog.kind === "new") {
      return <CollectionDialog title={dialog.parentID ? `New child in “${collectionName(collections, dialog.parentID)}”` : "New collection"} onClose={() => setDialog(null)} footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="plus" onClick={() => void createCollection()} disabled={Boolean(busy)}>Create collection</Button></>}>
        <form className="collections-form" onSubmit={createCollection}><label><span>Name</span><input autoFocus value={newName} onChange={(event) => { setNewName(event.target.value); setNewError(""); }} placeholder="Collection name" /></label><label><span>Description <small>Optional</small></span><textarea value={newDescription} onChange={(event) => setNewDescription(event.target.value)} placeholder="Optional" rows={4} /></label>{newError && <div className="collections-inline-error" role="alert"><Icon name="error" size={16} />{newError}</div>}</form>
        <OperationError message={operationError} />
      </CollectionDialog>;
    }
    if (dialog.kind === "rename" || dialog.kind === "description") {
      return <CollectionDialog title={dialog.kind === "rename" ? `Rename “${dialog.collection.name}”` : `Edit “${dialog.collection.name}”`} onClose={() => setDialog(null)} footer={<><Button onClick={() => setDialog(null)}>Cancel</Button><Button tone="primary" icon="save" onClick={() => void updateCollection()} disabled={Boolean(busy)}>Save changes</Button></>}>
        <form className="collections-form" onSubmit={updateCollection}><label><span>Name</span><input autoFocus={dialog.kind === "rename"} value={nameDraft} onChange={(event) => { setNameDraft(event.target.value); setEditError(""); }} /></label><label><span>Description <small>Optional</small></span><textarea value={descriptionDraft} onChange={(event) => setDescriptionDraft(event.target.value)} rows={5} /></label>{editError && <div className="collections-inline-error" role="alert"><Icon name="error" size={16} />{editError}</div>}</form>
        <OperationError message={operationError} />
      </CollectionDialog>;
    }
    if (dialog.kind === "mods" || dialog.kind === "children" || dialog.kind === "parents" || dialog.kind === "mod-destination") return renderPickerDialog(dialog);
    if (dialog.kind === "usage") {
      const parentRefs = references(dialog.usage, "collections");
      const profileRefs = references(dialog.usage, "profiles");
      return <CollectionDialog title={`Where “${dialog.collection.name}” is used`} onClose={() => setDialog(null)} footer={<Button onClick={() => setDialog(null)}>Close</Button>}><div className="collections-usage-dialog"><p>References are live: edits to this collection update every parent and saved profile that includes it.</p><ImpactList title="Parent collections" items={parentRefs} empty="No parent collections reference this collection." onOpen={openCollection} /><ImpactList title="Saved profiles" items={profileRefs} empty="No saved profiles reference this collection." /></div></CollectionDialog>;
    }
    return renderDeleteDialog(dialog.ids);
  };

  if (currentCollectionID) {
    if (detailLoading && !activeDetail) return <Page title="Collection" className="collections-view collections-view--detail" ariaLabel="Collection detail"><div className="collections-detail-loading" role="status"><Spinner /><span>Loading collection…</span></div></Page>;
    if (detailError && !activeDetail) return <Page title="Collection" className="collections-view collections-view--detail" ariaLabel="Collection detail"><p className="collections-empty">{detailError} <button type="button" className="collections-empty__link" onClick={() => void refreshDetail(currentCollectionID)}>Retry</button> <button type="button" className="collections-empty__link" onClick={() => openCollection("")}>All collections</button></p></Page>;
    if (!currentCollection) {
      return (
        <Page title="Collection" className="collections-view collections-view--detail" ariaLabel="Collection detail">
          <p className="collections-empty">Collection not found. <button type="button" className="collections-empty__link" onClick={() => openCollection("")}>All collections</button></p>
        </Page>
      );
    }
    return <Page title={currentCollection.name} className="collections-view collections-view--detail" ariaLabel={`Collection ${currentCollection.name}`} actions={[
      { key: "add-mods", label: "Add mods", icon: "plus", onClick: () => openModPicker(currentCollection.id) },
      { key: "add-children", label: "Add collections", icon: "link", onClick: () => openChildPicker(currentCollection.id) },
      { key: "edit-cover", label: "Edit cover", icon: "columns", onClick: () => setCoverEditorCollection(currentCollection) },
      { key: "delete", label: "Delete", icon: "trash", role: "danger", onClick: () => void requestDelete([currentCollection.id]) },
    ]}>
      <div className="collections-breadcrumb-wrap"><nav className="collections-breadcrumb" aria-label="Collection breadcrumb"><button type="button" onClick={() => openCollection("")}><Icon name="library" size={14} />All collections</button>{routeStack.map((id, index) => <span key={`${id}-${index}`}><Icon name="chevron" size={12} /><button type="button" className={index === routeStack.length - 1 ? "is-current" : ""} onClick={() => openCollection(id)}>{collectionName(collections, id)}</button></span>)}</nav></div>
      {operationError && <div className="collections-operation-error" role="alert"><Icon name="error" size={16} /><span>{operationError}</span><button type="button" onClick={() => setOperationError("")}>Dismiss</button></div>}
      <div className="collection-detail-scroll">
        <header className="collection-detail-hero"><div className="collection-detail-cover" style={collectionCoverStyle(currentCollection)}>{!currentCollection.coverUrl && <span><Icon name="folder" size={28} /></span>}</div><div className="collection-detail-heading"><h2>{currentCollection.name}</h2><p>{currentCollection.description}</p><div className="collection-detail-metrics"><span>{formatCount(currentCollection.directModCount)} mods · {formatCount(currentCollection.directEnabledCount)} enabled</span><span>{formatCount(currentCollection.modCount)} effective mods</span><span>{formatCount(currentCollection.childCount)} included collections</span><span>Updated {formatDate(currentCollection.updatedAt)}</span></div></div><div className="collection-detail-actions"><Button icon="edit" onClick={() => openEditDialog(currentCollection, "description")}>Edit details</Button><Button icon="play" onClick={() => onAddToPlay([currentCollection.id])}>Add to Play</Button><Button icon="link" onClick={() => void openUsage(currentCollection)}>Where used</Button></div></header>
        <section className="collection-members-section">
          <header className="collection-members-header"><div><h3>Mods in this collection</h3></div><div className="collection-members-tools"><label className="collections-search"><Icon name="search" size={15} /><input value={memberQuery} onChange={(event) => setMemberQuery(event.target.value)} placeholder="Filter members…" aria-label="Filter collection members" /></label></div></header>
          <div className="member-bulk-toolbar"><strong>{selectedModIDs.size.toLocaleString()} direct selected</strong><Button icon="check" disabled={selectedModIDs.size === 0 || Boolean(busy)} onClick={() => void setSelectedModsEnabled(true)}>Enable</Button><Button icon="close" disabled={selectedModIDs.size === 0 || Boolean(busy)} onClick={() => void setSelectedModsEnabled(false)}>Disable</Button><Button icon="unlink" disabled={selectedModIDs.size === 0 || Boolean(busy)} onClick={() => void removeSelectedMods()}>Remove from collection</Button><Button icon="plus" disabled={Boolean(busy)} onClick={() => openModPicker(currentCollection.id)}>Add mods</Button></div>
          {staleMemberCount > 0 && <p className="collection-members-stale" role="status">{staleMemberCount.toLocaleString()} direct member{staleMemberCount === 1 ? "" : "s"} are not in the indexed mod library and cannot be edited.</p>}
          <ModTable
            className="collection-members-table"
            surface="collection"
            ariaLabel="Direct collection mods"
            items={visibleDirectMods}
            interaction={{
              kind: "browse",
              selectedID: "",
              selectedIDs: selectedModIDs,
              disabled: Boolean(busy),
              isSelectable: () => true,
              onToggle: toggleModSelection,
              onToggleAll: toggleAllVisibleMods,
              selectAllLabel: "Select all matching direct mods",
              onActivate: (item) => onInspectMod(item.entityId),
              onContextMenu: openModContextMenu,
            }}
            emptyTitle={memberQuery ? "No direct mods match this filter." : "No direct mods in this collection."}
            resetKey={`${currentCollection.id}:${memberQuery}`}
            enabledByEntityID={enabledByEntityID}
            onToggleEnabled={(entityID, enabled) => void toggleModEnabled(entityID, enabled)}
          />
        </section>
        <section className="collection-children-section">
          <header className="collection-children-header"><div><h3>Included collections</h3><span>{formatCount(visibleChildren.length)} shown</span></div><Button icon="plus" onClick={() => openChildPicker(currentCollection.id)}>Include a collection</Button></header>
          <div className="collection-children-list" role="list" aria-label="Included child collections">{visibleChildren.length === 0 ? <p className="collection-children-empty">No child collections match this filter.</p> : visibleChildren.map(({ child, enabled }) => { const index = childIDs.indexOf(child.id); return <ChildMemberRow key={child.id} child={child} enabled={enabled} busy={Boolean(busy)} index={index} total={childIDs.length} onOpen={() => openCollection(child.id)} onRemove={() => void removeChild(currentCollection.id, child.id)} onMove={(offset) => void reorderMembers("children", child.id, offset)} onToggleEnabled={(next) => void toggleChildEnabled(child.id, next)} onContextMenu={(event) => { event.preventDefault(); setContextMenu({ kind: "child", parentID: currentCollection.id, childID: child.id, x: event.clientX, y: event.clientY }); }} />; })}</div>
        </section>
      </div>
      {renderDialog()}
      {coverEditorCollection && <CollectionCoverEditor collection={coverEditorCollection} items={items} onSaved={(next) => void updateCoverDetail(next)} onClose={() => setCoverEditorCollection(null)} onError={onError} />}
      {contextMenu && <CollectionMenuPopup label="Collection actions" x={contextMenu.x} y={contextMenu.y} actions={contextActions} onClose={() => setContextMenu(null)} />}
    </Page>;
  }

  return <Page title="Collections" className="collections-view" ariaLabel="Collections" actions={[{ key: "new", label: "New collection", icon: "plus", role: "primary", onClick: () => openNewCollection() }]}>
    {operationError && <div className="collections-operation-error" role="alert"><Icon name="error" size={16} /><span>{operationError}</span><button type="button" onClick={() => setOperationError("")}>Dismiss</button></div>}
    <div className="page-toolbar"><label className="collections-search collections-search--large"><Icon name="search" size={16} /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search collections…" aria-label="Search collections" /></label><label className="collections-sort"><span>Sort</span><select value={sort} onChange={(event) => setSort(event.target.value as SortKey)} aria-label="Sort collections"><option value="name">Name</option><option value="updated">Recently edited</option><option value="mods">Effective mod count</option></select></label><span className="page-toolbar__meta">{filteredCollections.length.toLocaleString()} of {collections.length.toLocaleString()} collections</span></div>
    {selectedIDs.size > 0 && <div className="collections-bulk-toolbar"><strong>{selectedIDs.size.toLocaleString()} selected</strong><Button icon="link" onClick={() => openParentPicker([...selectedIDs])}>Add to parent</Button><Button icon="play" onClick={() => { onAddToPlay([...selectedIDs]); onNotify(`${selectedIDs.size.toLocaleString()} collection${selectedIDs.size === 1 ? "" : "s"} added to Play.`, "success"); }}>Add to Play</Button><Button icon="trash" tone="danger" onClick={() => void requestDelete([...selectedIDs])}>Delete selected</Button><Button className="collections-clear-selection" onClick={() => setSelectedIDs(new Set())}>Clear</Button></div>}
    <div className="collections-browse-scroll">{renderCollectionCards()}</div>
    {dialog && renderDialog()}
    {coverEditorCollection && <CollectionCoverEditor collection={coverEditorCollection} items={items} onSaved={(next) => void updateCoverDetail(next)} onClose={() => setCoverEditorCollection(null)} onError={onError} />}
    {contextMenu && <CollectionMenuPopup label="Collection actions" x={contextMenu.x} y={contextMenu.y} actions={contextActions} onClose={() => setContextMenu(null)} />}
  </Page>;
}

function ImpactList({
  title,
  items,
  empty,
  onOpen,
}: {
  title: string;
  items: CollectionReference[];
  empty: string;
  onOpen?: (id: string) => void;
}) {
  return <section className="collections-impact-list"><h4>{title}</h4>{items.length === 0 ? <p>{empty}</p> : <ul>{items.map((item) => <li key={item.id}>{onOpen ? <button type="button" onClick={() => onOpen(item.id)}>{item.name}</button> : <span>{item.name}</span>}</li>)}</ul>}</section>;
}


function ChildMemberRow({
  child,
  enabled,
  busy,
  index,
  total,
  onOpen,
  onRemove,
  onMove,
  onToggleEnabled,
  onContextMenu,
}: {
  child: ModCollection;
  enabled: boolean;
  busy: boolean;
  index: number;
  total: number;
  onOpen: () => void;
  onRemove: () => void;
  onMove: (offset: -1 | 1) => void;
  onToggleEnabled: (enabled: boolean) => void;
  onContextMenu: (event: ReactMouseEvent<HTMLElement>) => void;
}) {
  const handleKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key === "ArrowUp" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      onMove(-1);
    } else if (event.key === "ArrowDown" && (event.ctrlKey || event.metaKey)) {
      event.preventDefault();
      onMove(1);
    }
  };
  return (
    <article
      className={`collection-child-row${enabled ? "" : " collection-child-row--disabled"}`}
      role="listitem"
      onContextMenu={onContextMenu}
      onKeyDown={handleKeyDown}
    >
      <button
        type="button"
        className="collection-child-cover"
        style={collectionCoverStyle(child)}
        onClick={onOpen}
        aria-label={`Open ${child.name}`}
      >
        {!child.coverUrl && <Icon name="folder" size={18} />}
      </button>
      <div className="collection-child-info">
        <button type="button" className="collection-child-name" onClick={onOpen}>
          <strong>{child.name}</strong>
          <small>{formatCount(child.modCount)} effective mods · {formatCount(child.directModCount)} direct · {formatCount(child.childCount)} children</small>
        </button>
        <button type="button" className="collection-child-open" onClick={onOpen}>Open</button>
      </div>
      <label className="collection-child-enabled">
        <input
          type="checkbox"
          checked={enabled}
          disabled={busy}
          aria-label={`${enabled ? "Disable" : "Enable"} ${child.name}`}
          onClick={(event) => event.stopPropagation()}
          onKeyDown={(event) => event.stopPropagation()}
          onChange={() => onToggleEnabled(!enabled)}
        />
        <span>{enabled ? "Enabled" : "Disabled"}</span>
      </label>
      <span className="collection-child-reorder">
        <button type="button" title="Move up" aria-label={`Move ${child.name} up`} disabled={busy || index <= 0} onClick={() => onMove(-1)}><Icon name="chevron" size={13} /></button>
        <button type="button" title="Move down" aria-label={`Move ${child.name} down`} disabled={busy || index >= total - 1} onClick={() => onMove(1)}><Icon name="chevron" size={13} /></button>
      </span>
      <button type="button" className="collection-child-remove" title="Remove from this collection" aria-label={`Remove ${child.name} from this collection`} disabled={busy} onClick={onRemove}><Icon name="unlink" size={15} /></button>
      <button type="button" className="collection-child-menu" aria-label={`More actions for ${child.name}`} disabled={busy} onClick={(event) => { event.stopPropagation(); onContextMenu(event); }}><Icon name="more" size={16} /></button>
    </article>
  );
}
