import { useCallback, useEffect, useState } from "react";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ModVersion,
  WorkspaceDetail,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Badge, Button, Spinner, formatDate } from "./ui";
import { queryClient, queryKeys, replaceCachedLibraryItem } from "./queries";

// Restorable states of a mod edited in ModMaker: every save goes straight into
// the library mod, and these are the points it can be returned to.

export interface VersionsPanelProps {
  entityId: string;
  entityName: string;
  onError: (error: unknown) => void;
  onNotify: (message: string, tone: "success" | "error" | "info") => void;
  /** Called after a restore with the refreshed workspace. */
  onRestored?: (detail: WorkspaceDetail) => void;
  /** Changes whenever the mod's history may have changed, to reload the list. */
  refreshKey?: string;
}

function versionLabel(version: ModVersion): string {
  if (version.kind === "original") return "Original";
  return version.author === "virgil" ? "Changes by Virgil" : "Your changes";
}

export function VersionsPanel({ entityId, entityName, onError, onNotify, onRestored, refreshKey }: VersionsPanelProps) {
  const [versions, setVersions] = useState<ModVersion[] | null>(null);
  const [restoring, setRestoring] = useState("");

  const load = useCallback(async () => {
    try {
      setVersions((await API.ListModVersions(entityId)) ?? []);
    } catch (error) {
      onError(error);
      setVersions([]);
    }
  }, [entityId, onError]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const restore = async (version: ModVersion) => {
    setRestoring(version.id);
    try {
      const detail = await API.RestoreModVersion(entityId, version.id);
      replaceCachedLibraryItem(detail.entity);
      void queryClient.invalidateQueries({ queryKey: queryKeys.library });
      void queryClient.invalidateQueries({ queryKey: queryKeys.workspaces });
      onRestored?.(detail);
      await load();
      onNotify(`${entityName} is back to “${versionLabel(version)}”. What you had before is kept here too.`, "success");
    } catch (error) {
      onError(error);
    } finally {
      setRestoring("");
    }
  };

  if (versions === null) {
    return (
      <div className="versions-panel versions-panel--loading">
        <Spinner />
        <span>Loading versions</span>
      </div>
    );
  }
  if (versions.length === 0) {
    return (
      <div className="versions-panel">
        <p className="versions-panel__empty">No changes yet. Each time you save in ModMaker, the library mod updates and a version is kept here.</p>
      </div>
    );
  }
  return (
    <div className="versions-panel">
      <ul className="versions-list" role="list">
        {versions.map((version) => (
          <li key={version.id} className={`versions-list__row${version.current ? " is-active" : ""}`}>
            <div className="versions-list__info">
              <div className="versions-list__heading">
                <strong>{versionLabel(version)}</strong>
                {version.current && <Badge tone="accent">In your library</Badge>}
              </div>
              <div className="versions-list__meta">
                {version.kind === "original" ? (
                  <span>The mod as it was before any edits</span>
                ) : (
                  <>
                    <span>{formatDate(version.savedAt)}</span>
                    <span>
                      {version.changedFiles} {version.changedFiles === 1 ? "file" : "files"} changed
                    </span>
                  </>
                )}
              </div>
            </div>
            {!version.current && (
              <div className="versions-list__actions">
                <Button disabled={restoring !== ""} onClick={() => void restore(version)}>
                  {restoring === version.id ? "Restoring…" : "Restore"}
                </Button>
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
