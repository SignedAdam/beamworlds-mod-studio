import { Icon } from "./icons";
import { Button, Spinner, formatBytes } from "./ui";
import type { ArchiveCapability, ArchiveDeploymentPlan } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import {
  DEPLOYMENT_MODES,
  DEPLOYMENT_MODE_LABELS,
  capabilityReasonLabel,
  capabilitySummary,
  type DeploymentMode,
} from "./archiveTypes";
import "./ArchiveDeployment.css";

/* ── Deployment mode radio group ── */

interface DeploymentModeSelectorProps {
  mode: DeploymentMode;
  capabilities: ArchiveCapability[];
  mixed: boolean;
  warning: string;
  loading: boolean;
  error: string;
  disabled?: boolean;
  onChange: (mode: DeploymentMode) => void;
  onRefresh?: () => void;
}

/**
 * Radio group for Automatic / Hardlinks only / Copies.
 * Shows capability status, disabled reasons, and warnings inline.
 */
export function DeploymentModeSelector({
  mode,
  capabilities,
  mixed,
  warning,
  loading,
  error,
  disabled,
  onChange,
  onRefresh,
}: DeploymentModeSelectorProps) {
  const summary = capabilitySummary(capabilities);
  const hardlinksAvailable = summary.hardlinks;
  const checked = capabilities.filter((c) => c.checked);

  // Hardlinks-only is disabled when no root supports hardlinks.
  const hardlinkDisabledReason = (() => {
    if (loading || checked.length === 0) return "";
    if (!hardlinksAvailable) {
      const first = checked[0];
      return first
        ? capabilityReasonLabel(first.reasonCode) || "Not supported"
        : "Not supported";
    }
    return "";
  })();

  return (
    <fieldset
      className="deployment-mode"
      disabled={disabled}
      aria-label="Archive deployment"
    >
      <legend className="deployment-mode__legend">Archive deployment</legend>
      <div className="deployment-mode__options" role="radiogroup">
        {DEPLOYMENT_MODES.map((value) => {
          const isHardlinkOnly = value === "hardlink-only";
          const definitivelyDisabled =
            isHardlinkOnly && Boolean(hardlinkDisabledReason);
          return (
            <label
              key={value}
              className={`deployment-mode__option${mode === value ? " is-selected" : ""}${definitivelyDisabled ? " is-disabled" : ""}`}
            >
              <input
                type="radio"
                name="deployment-mode"
                value={value}
                checked={mode === value}
                disabled={definitivelyDisabled || disabled}
                onChange={() => onChange(value)}
              />
              <span className="deployment-mode__label">
                {DEPLOYMENT_MODE_LABELS[value]}
              </span>
              {definitivelyDisabled && (
                <span className="deployment-mode__disabled-reason">
                  {hardlinkDisabledReason}
                </span>
              )}
            </label>
          );
        })}
      </div>

      <div className="deployment-mode__status" role="status" aria-live="polite">
        {loading ? (
          <span className="deployment-mode__checking">
            <Spinner small /> Checking volumes…
          </span>
        ) : error ? (
          <span className="deployment-mode__error">
            <Icon name="warning" size={14} />
            {error}
            {onRefresh && (
              <Button tone="quiet" icon="refresh" onClick={onRefresh}>
                Retry
              </Button>
            )}
          </span>
        ) : (
          <>
            <span className="deployment-mode__summary">
              {summary.label}
            </span>
            {mixed && (
              <CapabilityDetails capabilities={checked} />
            )}
          </>
        )}
      </div>

      {warning && (
        <div className="deployment-mode__warning">
          <Icon name="warning" size={14} />
          <span>{warning}</span>
        </div>
      )}
    </fieldset>
  );
}

/* ── Per-root capability details (mixed roots) ── */

function CapabilityDetails({
  capabilities,
}: {
  capabilities: ArchiveCapability[];
}) {
  if (capabilities.length <= 1) return null;
  return (
    <details className="deployment-capability-details">
      <summary>Per-root details</summary>
      <ul>
        {capabilities.map((cap, i) => (
          <li key={`${cap.sourceRoot}-${cap.destinationRoot}-${i}`}>
            <span className="deployment-capability-path" title={cap.sourceRoot}>
              {shortenPath(cap.sourceRoot)}
            </span>
            <span> → </span>
            <span className="deployment-capability-path" title={cap.destinationRoot}>
              {shortenPath(cap.destinationRoot)}
            </span>
            <span className="deployment-capability-badge">
              {cap.hardlinks ? (
                <span className="badge badge--success">Hardlinks</span>
              ) : (
                <span className="badge badge--neutral">
                  {capabilityReasonLabel(cap.reasonCode) || "Copy"}
                </span>
              )}
            </span>
            {cap.freeBytes > 0 && (
              <span className="deployment-capability-free">
                {formatBytes(cap.freeBytes)} free
              </span>
            )}
          </li>
        ))}
      </ul>
    </details>
  );
}

/* ── Copy confirmation dialog ── */

interface CopyConfirmationProps {
  plan: ArchiveDeploymentPlan;
  purpose: string;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * Shows blockers or asks the user to accept a copy cost before proceeding.
 * Reused by Play and collection folder flows.
 */
export function CopyConfirmation({
  plan,
  purpose,
  busy,
  onConfirm,
  onCancel,
}: CopyConfirmationProps) {
  const blockers = plan.blockers?.filter(Boolean) ?? [];
  const blocked = blockers.length > 0;
  const copyCount =
    (plan.entries ?? []).filter((e) => e.method === "copy" && !e.reuse).length;
  const reuseCount = plan.reusedCount;

  return (
    <div className="copy-confirmation" role="alertdialog" aria-label={purpose}>
      {blocked ? (
        <>
          <div className="copy-confirmation__header">
            <Icon name="error" size={18} />
            <strong>Cannot {purpose.toLowerCase()}</strong>
          </div>
          <ul className="copy-confirmation__blockers">
            {blockers.map((b) => (
              <li key={b}>{b}</li>
            ))}
          </ul>
          <footer className="copy-confirmation__footer">
            <Button onClick={onCancel}>Close</Button>
          </footer>
        </>
      ) : (
        <>
          <div className="copy-confirmation__header">
            <Icon name="archive" size={18} />
            <strong>
              {copyCount > 0
                ? `${copyCount.toLocaleString()} archive${copyCount === 1 ? "" : "s"} will be copied`
                : `Ready to ${purpose.toLowerCase()}`}
            </strong>
          </div>
          <dl className="copy-confirmation__stats">
            {plan.copyBytes > 0 && (
              <div>
                <dt>Copy size</dt>
                <dd>{formatBytes(plan.copyBytes)}</dd>
              </div>
            )}
            {plan.additionalBytes > 0 && (
              <div>
                <dt>Additional space needed</dt>
                <dd>{formatBytes(plan.additionalBytes)}</dd>
              </div>
            )}
            {plan.peakBytes > 0 && plan.peakBytes !== plan.additionalBytes && (
              <div>
                <dt>Peak space during operation</dt>
                <dd>{formatBytes(plan.peakBytes)}</dd>
              </div>
            )}
            {reuseCount > 0 && (
              <div>
                <dt>Reused (already deployed)</dt>
                <dd>{reuseCount.toLocaleString()}</dd>
              </div>
            )}
            {plan.linkedCount > 0 && (
              <div>
                <dt>Hardlinked</dt>
                <dd>{plan.linkedCount.toLocaleString()}</dd>
              </div>
            )}
            {plan.inPlaceCount > 0 && (
              <div>
                <dt>In place (native)</dt>
                <dd>{plan.inPlaceCount.toLocaleString()}</dd>
              </div>
            )}
          </dl>
          {plan.copyBytes > 0 && (
            <p className="copy-confirmation__note">
              {plan.mode === "copy"
                ? "Copies mode keeps separate game files. Unchanged copies are reused on later launches."
                : "These archives are copied because hardlinks are unavailable between their folder and the game's mods folder."}
            </p>
          )}
          <footer className="copy-confirmation__footer">
            <Button tone="quiet" onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
            <Button
              tone="primary"
              icon="play"
              onClick={onConfirm}
              disabled={busy}
            >
              {busy
                ? "Applying…"
                : plan.copyBytes > 0
                  ? `Copy ${formatBytes(plan.copyBytes)} and continue`
                  : "Continue"}
            </Button>
          </footer>
        </>
      )}
    </div>
  );
}

/* ── Helpers ── */

function shortenPath(path: string): string {
  if (path.length <= 40) return path;
  const parts = path.replace(/\\/g, "/").split("/");
  if (parts.length <= 3) return path;
  return `${parts[0]}/…/${parts[parts.length - 2]}/${parts[parts.length - 1]}`;
}
