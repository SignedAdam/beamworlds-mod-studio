import type { ArchiveCapability } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";


export type DeploymentMode = "auto" | "hardlink-only" | "copy";

export const DEPLOYMENT_MODE_LABELS: Record<DeploymentMode, string> = {
  auto: "Automatic (recommended)",
  "hardlink-only": "Links only",
  copy: "Always copy",
};

export const DEPLOYMENT_MODE_DETAILS: Record<DeploymentMode, string> = {
  auto: "Links mods when possible and copies the rest.",
  "hardlink-only": "Never copies. Play won't start if a mod can't be linked.",
  copy: "Copies every mod. Uses more disk space.",
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
      return "Drive doesn't support links";
    case "permission-denied":
      return "No access";
    case "drive-unavailable":
      return "Drive unavailable";
    case "read-only-source":
      return "Folder is read-only";
    case "not-checked":
      return "Not checked";
    case "unknown":
    case "io-error":
      return "Couldn't check";
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
        ? "Some folders couldn't be checked."
        : capabilityReasonLabel(unavailable?.reasonCode ?? "not-checked") || "Not checked",
    };
  }
  const allLink = checked.every((c) => c.hardlinks);
  const noneLink = checked.every((c) => !c.hardlinks);
  if (allLink) {
    return {
      hardlinks: true,
      mixed: false,
      label: "All folders support links. No extra disk space used.",
    };
  }
  if (noneLink) {
    return { hardlinks: false, mixed: false, label: "Links aren't available, so mods will be copied." };
  }
  return {
    hardlinks: true,
    mixed: true,
    label: "Some folders can't use links. Mods from those folders will be copied.",
  };
}
