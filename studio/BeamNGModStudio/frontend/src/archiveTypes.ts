import type { ArchiveCapability } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";


export type DeploymentMode = "auto" | "hardlink-only" | "copy";

export const DEPLOYMENT_MODE_LABELS: Record<DeploymentMode, string> = {
  auto: "Automatic (recommended)",
  "hardlink-only": "Links only",
  copy: "Copies only",
};

export const DEPLOYMENT_MODE_DETAILS: Partial<Record<DeploymentMode, string>> = {
  "hardlink-only": "Blocks Play for mods that cannot be linked",
  copy: "Uses more disk space",
};

export const DEPLOYMENT_MODES: readonly DeploymentMode[] = [
  "auto",
  "hardlink-only",
  "copy",
] as const;

/** User-facing reason labels for capability reasonCodes. Unknown codes map to "". */
export function capabilityReasonLabel(reasonCode: string): string {
  switch (reasonCode) {
    case "different-volume":
      return "Different drive";
    case "filesystem-unsupported":
      return "Links unsupported";
    case "permission-denied":
      return "No access";
    case "drive-unavailable":
      return "Drive unavailable";
    case "read-only-source":
      return "Read-only folder";
    case "not-checked":
      return "Not checked";
    case "unknown":
    case "io-error":
      return "Check failed";
    default:
      return "";
  }
}

/** Short summary of a capability set. */
export function capabilitySummary(
  capabilities: ArchiveCapability[] | null | undefined,
): { hardlinks: boolean; mixed: boolean; label: string } {
  if (!capabilities || capabilities.length === 0) {
    return { hardlinks: false, mixed: false, label: "Not checked" };
  }
  const checked = capabilities.filter((c) => c.checked);
  if (checked.length !== capabilities.length) {
    const unavailable = capabilities.find((capability) => !capability.checked);
    return {
      hardlinks: checked.some((capability) => capability.hardlinks),
      mixed: checked.length > 0,
      label: checked.length > 0
        ? "Some folders not checked"
        : capabilityReasonLabel(unavailable?.reasonCode ?? "not-checked") || "Not checked",
    };
  }
  const allLink = checked.every((c) => c.hardlinks);
  const noneLink = checked.every((c) => !c.hardlinks);
  if (allLink) {
    return {
      hardlinks: true,
      mixed: false,
      label: "Links available",
    };
  }
  if (noneLink) {
    return { hardlinks: false, mixed: false, label: "Copies required" };
  }
  return {
    hardlinks: true,
    mixed: true,
    label: "Copies required for some folders",
  };
}
