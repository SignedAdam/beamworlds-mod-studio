import { useCallback, useEffect, useRef, useState } from "react";
import { CancelError } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  ArchiveCapability,
  ArchiveDeploymentState,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import type { DeploymentMode } from "./archiveTypes";

export interface ArchiveCapabilityState {
  mode: DeploymentMode;
  capabilities: ArchiveCapability[];
  mixed: boolean;
  warning: string;
  loading: boolean;
  error: string;
}

const EMPTY: ArchiveCapabilityState = {
  mode: "auto",
  capabilities: [],
  mixed: false,
  warning: "",
  loading: false,
  error: "",
};

function applyResult(result: ArchiveDeploymentState): ArchiveCapabilityState {
  return {
    mode: (result.mode || "auto") as DeploymentMode,
    capabilities: result.capabilities ?? [],
    mixed: result.mixed,
    warning: result.warning,
    loading: false,
    error: "",
  };
}

function errorMsg(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

/**
 * Probes archive deployment capabilities and tracks the current mode.
 *
 * When both `sourceRoot` and `destinationRoot` are non-empty, the hook calls
 * `ProbeArchiveDeployment(sourceRoot, destinationRoot)` for a targeted check
 * of that specific pair. When either is empty (Settings mode) it calls
 * `GetArchiveDeploymentState()` for the full picture.
 *
 * Every probe uses Wails `CancellablePromise.cancel()` when superseded or
 * when the component unmounts, so stale responses never land.
 */
export function useArchiveCapability(
  sourceRoot: string,
  destinationRoot: string,
): ArchiveCapabilityState & {
  refresh: () => void;
  setMode: (mode: DeploymentMode) => Promise<ArchiveDeploymentState | null>;
} {
  const [state, setState] = useState<ArchiveCapabilityState>(EMPTY);
  const versionRef = useRef(0);
  // Track the in-flight cancellable so we can cancel it on supersede/unmount.
  const inflightRef = useRef<{ cancel(): void } | null>(null);

  const cancelInflight = useCallback(() => {
    versionRef.current += 1;
    if (inflightRef.current) {
      inflightRef.current.cancel();
      inflightRef.current = null;
    }
  }, []);

  const probe = useCallback(
    (src: string, dst: string) => {
      cancelInflight();
      const version = ++versionRef.current;
      setState((prev) => ({ ...prev, capabilities: [], mixed: false, warning: "", loading: true, error: "" }));

      // Targeted probe when both paths are present; full-state otherwise.
      const request =
        src && dst
          ? API.ProbeArchiveDeployment(src, dst)
          : API.GetArchiveDeploymentState();
      inflightRef.current = request;

      request
        .then((result: ArchiveDeploymentState | ArchiveCapability) => {
          if (version !== versionRef.current) return;
          inflightRef.current = null;
          // ProbeArchiveDeployment returns a single ArchiveCapability;
          // GetArchiveDeploymentState returns ArchiveDeploymentState.
          if ("mode" in result) {
            setState(applyResult(result as ArchiveDeploymentState));
          } else {
            // Single capability probe — merge into state, keep current mode.
            const cap = result as ArchiveCapability;
            setState((prev) => ({
              ...prev,
              capabilities: [cap],
              mixed: false,
              loading: false,
              error: "",
            }));
          }
        })
        .catch((error: unknown) => {
          if (version !== versionRef.current) return;
          inflightRef.current = null;
          // Wails cancellation errors are not user-facing failures.
          if (error instanceof CancelError) {
            setState((prev) => ({ ...prev, loading: false }));
            return;
          }
          setState((prev) => ({
            ...prev,
            loading: false,
            error: errorMsg(error, "Could not check deployment capabilities."),
          }));
        });
    },
    [cancelInflight],
  );

  // Probe on mount and whenever paths change.
  useEffect(() => {
    probe(sourceRoot, destinationRoot);
    return cancelInflight;
  }, [sourceRoot, destinationRoot, probe, cancelInflight]);

  const setMode = useCallback(
    async (mode: DeploymentMode): Promise<ArchiveDeploymentState | null> => {
      cancelInflight();
      const version = ++versionRef.current;
      setState((prev) => ({ ...prev, loading: true, error: "" }));
      const request = API.SetArchiveDeploymentMode(mode);
      inflightRef.current = request;
      try {
        const result = await request;
        if (version !== versionRef.current) return result;
        inflightRef.current = null;
        setState(applyResult(result));
        return result;
      } catch (error: unknown) {
        if (version === versionRef.current) {
          inflightRef.current = null;
          if (!(error instanceof CancelError)) {
            setState((prev) => ({
              ...prev,
              loading: false,
              error: errorMsg(error, "Could not change deployment mode."),
            }));
          } else {
            setState((prev) => ({ ...prev, loading: false }));
          }
        }
        return null;
      }
    },
    [cancelInflight],
  );

  const refresh = useCallback(() => {
    probe(sourceRoot, destinationRoot);
  }, [probe, sourceRoot, destinationRoot]);

  return { ...state, refresh, setMode };
}
