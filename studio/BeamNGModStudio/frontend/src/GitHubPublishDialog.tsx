import { useEffect, useLayoutEffect, useRef, useState, type FormEvent, type ReactNode, type RefObject } from "react";
import { Browser } from "@wailsio/runtime";
import { AppService } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  GitHubPublishAuthSession,
  GitHubPublishConfiguration,
  GitHubPublishDraft,
  GitHubPublishOperation,
  GitHubPublishPartialState,
  GitHubPublishPreflight,
  GitHubPublishRemote,
  GitHubPublishStartRequest,
  GitHubPublishStep,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import { Icon } from "./icons";
import { Badge, Button, Spinner } from "./ui";
import "./GitHubPublishDialog.css";

export interface GitHubPublishEntryProps {
  workspaceID: string;
  disabled: boolean;
  onNotify: (
    message: string,
    tone: "success" | "info" | "warning" | "error",
  ) => void;
  onError: (error: unknown) => void;
}

type NoticeTone = "success" | "info" | "warning" | "error";
type Visibility = "private" | "public";
type DialogPhase =
  | "configuring"
  | "auth"
  | "draft"
  | "preflighting"
  | "public_confirmation"
  | "connect_confirmation"
  | "review"
  | "mutating"
  | "partial"
  | "complete"
  | "cancelled";
type AuthState =
  | "idle"
  | "auth_starting"
  | "device_waiting"
  | "authenticated"
  | "insufficient_permission"
  | "expired"
  | "denied"
  | "cancelled"
  | "error"
  | "session_ended";
type OperationState =
  | "queued"
  | "running"
  | "complete"
  | "failed"
  | "cancelled"
  | "needs_confirmation"
  | "unknown";
type StepState =
  | "pending"
  | "running"
  | "succeeded"
  | "failed"
  | "skipped"
  | "unknown"
  | "cancelled";

type PublishDraftState = Omit<GitHubPublishDraft, "visibility"> & { visibility: Visibility };
type PublishAuthState = Omit<GitHubPublishAuthSession, "grantedScopes"> & { grantedScopes: string[] };
type PublishOperationState = Omit<GitHubPublishOperation, "steps"> & { steps: GitHubPublishStep[] };

const DEFAULT_STEP_NAMES = [
  "authentication",
  "local_validation",
  "target_preflight",
  "create_or_connect",
  "remote_setup",
  "push",
  "upstream_verification",
  "completion",
] as const;
const STEP_LABELS: Record<string, string> = {
  authentication: "Authentication",
  local_validation: "Validate local repository",
  target_preflight: "Check GitHub target",
  create_or_connect: "Create or connect repository",
  remote_setup: "Configure local remote",
  push: "Push main",
  upstream_verification: "Verify upstream",
  completion: "Complete",
};
const OPERATION_POLL_DELAY_MS = 700;
const MIN_AUTH_INTERVAL_SECONDS = 1;

function safeText(value: string | undefined | null, fallback = "—"): string {
  const text = value?.trim() || "";
  if (!text) return fallback;
  return text
    .replace(/bearer\s+[A-Za-z0-9._~+\/-]+/gi, "Bearer [redacted]")
    .replace(/\b(?:gh[oprsu]|github_pat)_[A-Za-z0-9_]+/g, "[redacted]")
    .replace(/(?:device[_ -]?code|access[_ -]?token|refresh[_ -]?token)\s*[:=]\s*\S+/gi, "[credential redacted]")
    .replace(/\b[0-9a-f]{32,}\b/gi, "[identifier redacted]");
}

function safeError(error: unknown, fallback: string): string {
  if (error instanceof Error && error.message) return safeText(error.message, fallback);
  if (typeof error === "string" && error.trim()) return safeText(error, fallback);
  if (typeof error === "object" && error !== null && "message" in error && typeof error.message === "string") {
    return safeText(error.message, fallback);
  }
  return fallback;
}

function asAuthState(value: string): AuthState {
  switch (value) {
    case "idle":
    case "auth_starting":
    case "device_waiting":
    case "authenticated":
    case "insufficient_permission":
    case "expired":
    case "denied":
    case "cancelled":
    case "error":
    case "session_ended":
      return value;
    default:
      return "error";
  }
}

function asOperationState(value: string): OperationState {
  switch (value) {
    case "queued":
    case "running":
    case "complete":
    case "failed":
    case "cancelled":
    case "needs_confirmation":
    case "unknown":
      return value;
    default:
      return "unknown";
  }
}

function asStepState(value: string): StepState {
  switch (value) {
    case "pending":
    case "running":
    case "succeeded":
    case "failed":
    case "skipped":
    case "unknown":
    case "cancelled":
      return value;
    default:
      return "unknown";
  }
}

function asVisibility(value: string, fallback: Visibility = "private"): Visibility {
  return value === "public" ? "public" : value === "private" ? "private" : fallback;
}

function draftState(source: Partial<GitHubPublishDraft> = {}): PublishDraftState {
  const draft: GitHubPublishDraft = {
    sessionId: "",
    workspaceId: "",
    name: "",
    description: "",
    visibility: "private",
    publicConfirmed: false,
    ...source,
  };
  return { ...draft, visibility: asVisibility(draft.visibility) };
}

function authState(source: Partial<GitHubPublishAuthSession> = {}): PublishAuthState {
  return {
    sessionId: "",
    configured: false,
    state: "idle",
    login: "",
    userCode: "",
    verificationUri: "",
    expiresAt: "",
    expiresInSeconds: 0,
    pollIntervalSeconds: 0,
    nextPollAt: "",
    errorCode: "",
    error: "",
    action: "",
    retryable: false,
    ...source,
    grantedScopes: source.grantedScopes ?? [],
  };
}

function partialState(source: Partial<GitHubPublishPartialState> = {}): GitHubPublishPartialState {
  return {
    createdUrl: "",
    repositoryUrl: "",
    remoteName: "",
    remoteUrl: "",
    remoteState: "",
    remoteAdded: false,
    remoteReused: false,
    pushState: "",
    upstreamVerified: false,
    upstreamRemote: "",
    upstreamMerge: "",
    ...source,
  };
}

function stepState(source: Partial<GitHubPublishStep> = {}): GitHubPublishStep {
  return {
    name: "",
    state: "pending",
    attempt: 0,
    message: "",
    ...source,
  };
}

function operationState(source: Partial<GitHubPublishOperation> = {}): PublishOperationState {
  const operation: GitHubPublishOperation = {
    operationId: "",
    sessionId: "",
    state: "unknown",
    steps: [],
    partial: partialState(),
    error: "",
    action: "",
    retryable: false,
    reconciliationRequired: false,
    cancelled: false,
    completed: false,
    ...source,
  };
  return { ...operation, steps: operation.steps ?? [] };
}

function startRequest(source: Partial<GitHubPublishStartRequest> = {}): GitHubPublishStartRequest {
  return {
    planId: "",
    sessionId: "",
    confirmed: false,
    publicConfirmed: false,
    connectExisting: false,
    retryUnknownCreation: false,
    ...source,
  };
}

function normalizeAuth(value: GitHubPublishAuthSession): PublishAuthState {
  const normalized = authState(value);
  const state = asAuthState(normalized.state);
  if (state === "authenticated" && !normalized.login.trim()) {
    return {
      ...normalized,
      state: "error",
      error: "GitHub returned an authenticated session without an account identity. Sign in again.",
      retryable: true,
    };
  }
  return state === normalized.state ? normalized : { ...normalized, state };
}

function normalizeOperation(value: GitHubPublishOperation): PublishOperationState {
  const normalized = operationState(value);
  const state = asOperationState(normalized.state);
  return state === normalized.state ? normalized : { ...normalized, state };
}

function cleanGitHubURL(value: string | undefined | null): string | undefined {
  const text = value?.trim();
  if (!text) return undefined;
  try {
    const url = new URL(text);
    if (url.protocol !== "https:" || url.hostname.toLowerCase() !== "github.com") return undefined;
    if (url.username || url.password || url.search || url.hash) return undefined;
    return `${url.origin}${url.pathname}`.replace(/\/$/, "");
  } catch {
    return undefined;
  }
}

function safeSegment(value: string | undefined | null): string | undefined {
  const text = value?.trim();
  return text && /^[A-Za-z0-9._-]{1,100}$/.test(text) ? text : undefined;
}

function targetURL(preflight: GitHubPublishPreflight | null, draft: PublishDraftState, login?: string): string | undefined {
  const returned = cleanGitHubURL(preflight?.targetUrl);
  if (returned) return returned;
  const owner = safeSegment(preflight?.owner || login);
  const name = safeSegment(preflight?.name || draft.name);
  return owner && name ? `https://github.com/${owner}/${name}` : undefined;
}

function remoteURL(value: GitHubPublishRemote | string | undefined | null): string | undefined {
  const remote = typeof value === "string" ? value : value?.pushUrl || value?.fetchUrl;
  return cleanGitHubURL(remote);
}

function remoteName(value: GitHubPublishRemote | undefined | null): string | undefined {
  const name = value?.name.trim();
  return name && /^[A-Za-z0-9._-]+$/.test(name) ? name : undefined;
}

function targetIsExisting(preflight: GitHubPublishPreflight | null): boolean {
  if (!preflight) return false;
  if (preflight.requiresConnectConfirmation) return true;
  const target = preflight.target;
  const state = target.state.trim().toLowerCase();
  return target.exists || target.accessible || ["existing", "existing_accessible", "existing_no_push", "accessible", "collision", "already_exists", "exists"].includes(state);
}
function effectiveVisibility(preflight: GitHubPublishPreflight | null, draft: PublishDraftState): Visibility {
  if (preflight && targetIsExisting(preflight)) {
    return preflight.target.private ? "private" : "public";
  }
  return draft.visibility;
}

function localRepositoryLabel(preflight: GitHubPublishPreflight | null): string {
  const local = preflight?.local;
  if (!local) return "Not checked";
  if (local.root.trim()) return safeText(local.root);
  return local.repository ? "Active repository detected" : "No Git repository detected";
}

function localMainLabel(preflight: GitHubPublishPreflight | null): string {
  const local = preflight?.local;
  if (!local) return "Not checked";
  if (!local.repository) return "No Git repository detected";
  if (local.detached) return "Detached HEAD — local main is not suitable";
  return local.mainAvailable ? "main is ready" : "main is missing or unusable";
}

function parseExpiry(value: string, receivedAt: number, expiresInSeconds: number): number | undefined {
  const text = value.trim();
  if (text) {
    const numeric = Number(text);
    if (Number.isFinite(numeric)) return numeric > 100000000000 ? numeric : numeric * 1000;
    const parsed = Date.parse(text);
    if (Number.isFinite(parsed)) return parsed;
  }
  if (expiresInSeconds > 0) return receivedAt + expiresInSeconds * 1000;
  return undefined;
}

function expiryLabel(auth: PublishAuthState, now: number, receivedAt: number): string {
  const expires = parseExpiry(auth.expiresAt, receivedAt, auth.expiresInSeconds);
  if (!expires) return "GitHub will expire this code according to the device flow.";
  const remaining = Math.max(0, Math.ceil((expires - now) / 1000));
  if (remaining <= 0) return "This device code has expired.";
  const minutes = Math.floor(remaining / 60);
  const seconds = remaining % 60;
  return `Expires in ${minutes > 0 ? `${minutes}m ` : ""}${seconds}s`;
}

function authMessage(auth: PublishAuthState): string {
  if (auth.error) return safeText(auth.error);
  switch (asAuthState(auth.state)) {
    case "auth_starting":
      return "Starting secure GitHub device sign-in…";
    case "device_waiting":
      return "Finish sign-in in the browser. This window will check the authorization at GitHub’s requested interval.";
    case "authenticated":
      return auth.login ? `Verified GitHub account @${safeText(auth.login)}.` : "GitHub account verified.";
    case "insufficient_permission":
      return "GitHub did not grant the repository permission needed to create or push this repository.";
    case "expired":
      return "The GitHub device code expired. Start a new sign-in to continue.";
    case "denied":
      return "GitHub authorization was denied or cancelled. Start a fresh sign-in if you still want to publish.";
    case "cancelled":
      return "GitHub sign-in was cancelled. No repository mutation was made.";
    case "session_ended":
      return "The GitHub sign-in session ended. Start a new secure sign-in.";
    case "error":
      return "GitHub sign-in could not complete.";
    default:
      return "Sign in to GitHub before choosing a repository target.";
  }
}

function authErrorAction(auth: PublishAuthState): string {
  const code = auth.errorCode.toLowerCase();
  if (code === "expired_token") return "The code expired; request a new device code.";
  if (code === "access_denied") return "Authorize the app in GitHub, then request a new device code.";
  if (code === "device_flow_disabled") return "Enable the app’s device flow or contact the administrator.";
  if (code === "insufficient_permission") return "Authorize repository access and sign in again.";
  if (auth.action) return safeText(auth.action);
  return auth.retryable === false ? "Check the GitHub app configuration before trying again." : "Start a fresh secure sign-in.";
}

function operationStateLabel(state: OperationState): string {
  switch (state) {
    case "queued":
      return "Queued";
    case "running":
      return "Working";
    case "complete":
      return "Complete";
    case "failed":
      return "Stopped with an error";
    case "cancelled":
      return "Cancelled";
    case "needs_confirmation":
      return "Confirmation required";
    default:
      return "Status unknown";
  }
}

function stepStateLabel(state: StepState): string {
  switch (state) {
    case "running":
      return "In progress";
    case "succeeded":
      return "Done";
    case "failed":
      return "Failed";
    case "skipped":
      return "Skipped";
    case "cancelled":
      return "Cancelled";
    case "unknown":
      return "Unknown";
    default:
      return "Waiting";
  }
}

function pushStateLabel(value: string | undefined): string {
  if (!value) return "Not attempted";
  const normalized = value.toLowerCase().replace(/-/g, "_");
  if (normalized === "succeeded" || normalized === "success" || normalized === "complete") return "Succeeded";
  if (normalized === "unknown") return "Unknown — check GitHub before retrying";
  if (normalized === "cancelled" || normalized === "canceled") return "Cancelled";
  if (normalized === "failed") return "Failed";
  if (normalized === "not_started") return "Not started";
  return safeText(value);
}
function remoteStateLabel(value: string): string {
  switch (value.trim().toLowerCase()) {
    case "not_started":
      return "Not started";
    case "added":
      return "Added";
    case "reused":
      return "Existing exact target reused";
    case "unknown":
    default:
      return "Unknown — reconciliation required";
  }
}

function remoteStateNeedsReconciliation(value: string): boolean {
  const normalized = value.trim().toLowerCase();
  return normalized !== "not_started" && normalized !== "added" && normalized !== "reused";
}

function Notice({ tone, children }: { tone: NoticeTone; children: ReactNode }) {
  return (
    <div className={`github-publish-dialog__notice is-${tone}`} role={tone === "error" || tone === "warning" ? "alert" : "status"}>
      <Icon name={tone === "success" ? "check" : tone === "error" ? "error" : tone === "warning" ? "warning" : "link"} size={16} />
      <div>{children}</div>
    </div>
  );
}

function SummaryRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="github-publish-dialog__summary-row">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function StatusIcon({ state }: { state: StepState }) {
  if (state === "running") return <Spinner small />;
  if (state === "succeeded") return <Icon name="check" size={16} />;
  if (state === "failed" || state === "unknown") return <Icon name={state === "failed" ? "error" : "warning"} size={16} />;
  if (state === "cancelled") return <Icon name="close" size={16} />;
  return <span className="github-publish-dialog__step-dot" aria-hidden="true" />;
}

function AuthCard({
  auth,
  config,
  now,
  receivedAt,
  onOpenVerification,
  onRetry,
}: {
  auth: PublishAuthState;
  config: GitHubPublishConfiguration | null;
  now: number;
  receivedAt: number;
  onOpenVerification: () => void;
  onRetry: () => void;
}) {
  const state = asAuthState(auth.state);
  const waiting = state === "device_waiting";
  const terminal = state === "expired" || state === "denied" || state === "cancelled" || state === "error" || state === "insufficient_permission" || state === "session_ended";
  const verificationURL = cleanGitHubURL(auth.verificationUri);
  const pollIntervalSeconds = Math.max(MIN_AUTH_INTERVAL_SECONDS, Math.ceil(auth.pollIntervalSeconds || 5));
  return (
    <section className="github-publish-dialog__card github-publish-dialog__auth-card" aria-labelledby="github-publish-auth-heading">
      <div className="github-publish-dialog__card-heading">
        <div>
          <span className="github-publish-dialog__eyebrow">SECURE DEVICE SIGN-IN</span>
          <h3 id="github-publish-auth-heading">Sign in to GitHub</h3>
        </div>
        {waiting ? <Badge tone="cyan">Waiting for GitHub</Badge> : state === "authenticated" ? <Badge tone="success">Verified</Badge> : <Badge tone={terminal ? "warning" : "neutral"}>{state.replace(/_/g, " ")}</Badge>}
      </div>
      <div className={`github-publish-dialog__auth-status is-${state}`} role="status">
        {state === "authenticated" ? <Icon name="check" size={18} /> : terminal ? <Icon name="warning" size={18} /> : <Spinner small />}
        <span>{authMessage(auth)}</span>
      </div>
      {waiting && (
        <div className="github-publish-dialog__device-instructions">
          <p>GitHub opened its authorization page in your browser. If it did not open, use the button below. Do not paste a token into this app or the project.</p>
          {auth.userCode ? <div className="github-publish-dialog__device-code"><span>One-time code</span><code aria-label="GitHub device code">{safeText(auth.userCode)}</code></div> : <div className="github-publish-dialog__device-code is-missing"><span>Waiting for a device code…</span></div>}
          <div className="github-publish-dialog__device-meta">
            <span>{expiryLabel(auth, now, receivedAt)}</span>
            {auth.pollIntervalSeconds > 0 && <span>Checks GitHub every {pollIntervalSeconds} {pollIntervalSeconds === 1 ? "second" : "seconds"}</span>}
          </div>
          <div className="github-publish-dialog__scope-note">
            <strong>Requested access</strong>
            <span>{safeText(config?.scope || "Repository access needed to create and push", "Repository access needed to create and push")}</span>
          </div>
          <div className="github-publish-dialog__inline-actions">
            {verificationURL ? <Button type="button" icon="link" tone="quiet" className="github-publish-dialog__focus-target" onClick={onOpenVerification}>Open GitHub sign-in page</Button> : <span className="github-publish-dialog__muted">GitHub did not provide a safe verification URL.</span>}
          </div>
        </div>
      )}
      {state === "authenticated" && (
        <div className="github-publish-dialog__account-card">
          <Icon name="user" size={18} />
          <div><strong>{auth.login ? `@${safeText(auth.login)}` : "Verified GitHub account"}</strong><span>Account identity was checked after sign-in.</span><span>{auth.grantedScopes.length > 0 ? `Granted scopes: ${auth.grantedScopes.map((scope) => safeText(scope)).join(", ")}` : "Granted repository access was checked by GitHub."}</span></div>
        </div>
      )}
      {terminal && state !== "cancelled" && <div className="github-publish-dialog__auth-recovery"><strong>Next action</strong><span>{authErrorAction(auth)}</span></div>}
      {terminal && auth.retryable && <div className="github-publish-dialog__inline-actions"><Button type="button" icon="refresh" onClick={onRetry} className="github-publish-dialog__focus-target">Try again</Button></div>}
    </section>
  );
}

function DraftCard({
  draft,
  login,
  validationError,
  nameRef,
  onNameChange,
  onDescriptionChange,
  onVisibilityChange,
}: {
  draft: PublishDraftState;
  login?: string;
  validationError?: string;
  nameRef: RefObject<HTMLInputElement>;
  onNameChange: (value: string) => void;
  onDescriptionChange: (value: string) => void;
  onVisibilityChange: (visibility: Visibility) => void;
}) {
  return (
    <section className="github-publish-dialog__card" aria-labelledby="github-publish-details-heading">
      <div className="github-publish-dialog__card-heading"><div><span className="github-publish-dialog__eyebrow">REPOSITORY DETAILS</span><h3 id="github-publish-details-heading">Choose the GitHub target</h3></div><Badge tone="success">@{safeText(login, "verified account")}</Badge></div>
      <p className="github-publish-dialog__card-copy">The active repository will be checked before anything is created. This workflow never stores a token in the mod or Git configuration.</p>
      <div className="github-publish-dialog__form-grid">
        <label className="github-publish-dialog__field github-publish-dialog__field--wide"><span>Repository name</span><input ref={nameRef} value={draft.name} onChange={(event) => onNameChange(event.target.value)} autoComplete="off" spellCheck={false} maxLength={100} pattern="[A-Za-z0-9._-]+" aria-invalid={validationError ? true : undefined} aria-describedby={validationError ? "github-publish-name-error" : undefined} placeholder="my-beamng-mod" />{validationError && <small id="github-publish-name-error" className="github-publish-dialog__field-error">{validationError}</small>}<small>Use 1–100 letters, numbers, dots, hyphens, or underscores. The confirmed spelling is sent unchanged.</small></label>
        <label className="github-publish-dialog__field github-publish-dialog__field--wide"><span>Description <em>Optional</em></span><textarea value={draft.description} onChange={(event) => onDescriptionChange(event.target.value)} maxLength={500} rows={3} placeholder="What this repository contains" /></label>
      </div>
      <fieldset className="github-publish-dialog__visibility"><legend>Visibility</legend><div className="github-publish-dialog__visibility-options">
        <label className={`github-publish-dialog__visibility-option ${draft.visibility === "private" ? "is-selected" : ""}`}><input type="radio" name="github-publish-visibility" value="private" checked={draft.visibility === "private"} onChange={() => onVisibilityChange("private")} /><span><strong>Private</strong><small>Only people with access can see the repository.</small></span></label>
        <label className={`github-publish-dialog__visibility-option ${draft.visibility === "public" ? "is-selected" : ""}`}><input type="radio" name="github-publish-visibility" value="public" checked={draft.visibility === "public"} onChange={() => onVisibilityChange("public")} /><span><strong>Public</strong><small>Anyone on GitHub can see the repository.</small></span></label>
      </div><p className="github-publish-dialog__visibility-note">Private starts selected for every new Publish to GitHub invocation. Public requires a second confirmation after the target review.</p></fieldset>
    </section>
  );
}

function ReviewCard({
  draft,
  auth,
  preflight,
  connectExisting,
  retryUnknownCreation,
}: {
  draft: PublishDraftState;
  auth: PublishAuthState;
  preflight: GitHubPublishPreflight;
  connectExisting: boolean;
  retryUnknownCreation: boolean;
}) {
  const url = targetURL(preflight, draft, auth.login);
  const remote = remoteURL(preflight.proposedRemoteUrl);
  const existing = remoteURL(preflight.existingOrigin);
  const existingName = remoteName(preflight.existingOrigin) || "origin";
  const owner = safeText(preflight.owner || auth.login, "Verified account");
  const existingTarget = targetIsExisting(preflight);
  const localStatus = localMainLabel(preflight);
  return (
    <section className="github-publish-dialog__card github-publish-dialog__review-card" aria-labelledby="github-publish-review-heading">
      <div className="github-publish-dialog__card-heading"><div><span className="github-publish-dialog__eyebrow">PREFLIGHT REVIEW</span><h3 id="github-publish-review-heading">Review before any mutation</h3></div><Badge tone={preflight.ready && !preflight.error ? "success" : "warning"}>{preflight.ready && !preflight.error ? "Ready for confirmation" : "Needs attention"}</Badge></div>
      <p className="github-publish-dialog__card-copy">GitHub creation, local remote changes, and the initial push do not start until you choose the final confirmation below.</p>
      <dl className="github-publish-dialog__summary">
        <SummaryRow label="Verified account"><strong>@{safeText(auth.login, "account unavailable")}</strong></SummaryRow>
        <SummaryRow label="Local repository">{localRepositoryLabel(preflight)}</SummaryRow>
        <SummaryRow label="Local main">{localStatus}</SummaryRow>
        <SummaryRow label="Owner">{owner}</SummaryRow>
        <SummaryRow label="Repository">{safeText(preflight.name || draft.name)}</SummaryRow>
        <SummaryRow label="GitHub URL">{url ? <a href={url} target="_blank" rel="noreferrer" onClick={(event) => event.preventDefault()}>{url}</a> : "GitHub URL will be confirmed by the response"}</SummaryRow>
        <SummaryRow label="Description">{draft.description.trim() ? safeText(draft.description) : "No description"}</SummaryRow>
        <SummaryRow label="Visibility"><strong>{effectiveVisibility(preflight, draft) === "public" ? "Public" : "Private"}</strong>{existingTarget && <span className="github-publish-dialog__summary-subnote">{effectiveVisibility(preflight, draft) === "public" ? "Existing provider target is public; this workflow will not change its visibility." : "Existing provider target is private; this workflow will not change its visibility."}</span>}</SummaryRow>
        <SummaryRow label="Existing origin">{existing ? <><strong>{safeText(existingName)}</strong><span><code>{existing}</code></span><span className="github-publish-dialog__summary-subnote">Preserved; it will not be retargeted.</span></> : "None detected"}</SummaryRow>
        <SummaryRow label="Proposed remote">{preflight.proposedRemoteName ? <><strong>{safeText(preflight.proposedRemoteName)}</strong>{remote && <code>{remote}</code>}<span className="github-publish-dialog__summary-subnote">Added only if needed; an exact existing remote is reused.</span></> : "Will be determined without replacing an existing remote"}</SummaryRow>
      </dl>
      {existingTarget && connectExisting && <Notice tone="info">You explicitly chose to connect to the existing GitHub target. {effectiveVisibility(preflight, draft) === "public" ? "This repository is public; its metadata and visibility will not be changed." : "Its metadata and visibility will not be changed."}</Notice>}
      {retryUnknownCreation && <Notice tone="warning">You are requesting a user-directed retry after a previous creation response was unknown. The backend will reconcile the target before it creates anything again.</Notice>}
      {preflight.error && <Notice tone="error"><strong>{safeText(preflight.error)}</strong>{preflight.action && <span className="github-publish-dialog__notice-action">{safeText(preflight.action)}</span>}</Notice>}
      {!preflight.error && !preflight.ready && <Notice tone="warning">Local validation or target checks are not complete. {preflight.action ? safeText(preflight.action) : "Use Recheck preflight before continuing."}</Notice>}
      {preflight.creationUnknown && <Notice tone="warning">Creation status is unknown. Do not create another repository until the target has been reconciled.</Notice>}
    </section>
  );
}

function ProgressCard({ operation, auth, target }: { operation: PublishOperationState; auth: PublishAuthState; target?: string }) {
  const state = asOperationState(operation.state);
  const steps = operation.steps.length > 0 ? operation.steps : DEFAULT_STEP_NAMES.map((name) => stepState({ name, state: "pending" }));
  return (
    <section tabIndex={-1} className="github-publish-dialog__card github-publish-dialog__progress-card" aria-labelledby="github-publish-progress-heading">
      <div className="github-publish-dialog__card-heading"><div><span className="github-publish-dialog__eyebrow">ORDERED PROGRESS</span><h3 id="github-publish-progress-heading">Publishing {target || "the repository"}</h3></div><Badge tone={state === "running" || state === "queued" ? "cyan" : state === "complete" ? "success" : "warning"}>{operationStateLabel(state)}</Badge></div>
      <p className="github-publish-dialog__card-copy">Verified account: <strong>@{safeText(auth.login, "account unavailable")}</strong>. Steps remain in backend order; a stopped or unknown step is never reported as complete.</p>
      <ol className="github-publish-dialog__steps" aria-live="polite">
        {steps.map((step, index) => {
          const stepState = asStepState(step.state);
          const stateLabel = stepStateLabel(stepState);
          const message = step.message?.trim();
          const showMessage = Boolean(message) && message.toLowerCase() !== stateLabel.toLowerCase();
          return <li key={`${step.name}-${index}`} className={`is-${stepState}`}><span className="github-publish-dialog__step-icon"><StatusIcon state={stepState} /></span><span className="github-publish-dialog__step-content"><strong>{STEP_LABELS[step.name] || safeText(step.name)}</strong>{showMessage && <span>{safeText(message)}</span>}</span><span className="github-publish-dialog__step-state">{stateLabel}</span></li>;
        })}
      </ol>
    </section>
  );
}

function PartialCard({ operation, target }: { operation: PublishOperationState; target?: string }) {
  const partial = operation.partial;
  const state = asOperationState(operation.state);
  const repositoryURL = cleanGitHubURL(partial.createdUrl || partial.repositoryUrl || target);
  const remote = remoteURL(partial.remoteUrl);
  const remoteState = remoteStateLabel(partial.remoteState);
  const remoteNeedsReconciliation = remoteStateNeedsReconciliation(partial.remoteState);
  const upstream = partial.upstreamVerified ? "Verified" : "Not verified";
  return (
    <section className="github-publish-dialog__card github-publish-dialog__partial-card" aria-labelledby="github-publish-partial-heading">
      <div className="github-publish-dialog__card-heading"><div><span className="github-publish-dialog__eyebrow">RECOVERY STATE</span><h3 id="github-publish-partial-heading">Publish did not finish</h3></div><Badge tone="warning">Not complete</Badge></div>
      <Notice tone="warning">This flow was {state === "cancelled" ? "cancelled" : state === "unknown" ? "left with an unknown status" : "stopped"}. Nothing here implies rollback. Reconcile the state before retrying any mutation.</Notice>
      {remoteNeedsReconciliation && <Notice tone="warning">The local remote state is unknown; no remote is assumed absent. Reconcile the repository before retrying so an existing remote is not duplicated or replaced.</Notice>}
      <dl className="github-publish-dialog__summary github-publish-dialog__partial-summary">
        <SummaryRow label="Repository URL">{repositoryURL || "Not confirmed"}</SummaryRow>
        <SummaryRow label="Local remote">{partial.remoteName ? <><strong>{safeText(partial.remoteName)}</strong>{remote && <code>{remote}</code>}</> : remote || (remoteNeedsReconciliation ? "Remote state is unknown" : "No remote started")}<span className="github-publish-dialog__summary-subnote">{remoteState}</span></SummaryRow>
        <SummaryRow label="Push main">{pushStateLabel(partial.pushState)}</SummaryRow>
        <SummaryRow label="Upstream main">{upstream}{partial.upstreamRemote && <span className="github-publish-dialog__summary-subnote">Remote {safeText(partial.upstreamRemote)}{partial.upstreamMerge ? ` · ${safeText(partial.upstreamMerge)}` : ""}</span>}</SummaryRow>
        <SummaryRow label="Last backend state">{operationStateLabel(state)}</SummaryRow>
      </dl>
      {operation.error && <Notice tone="error">{safeText(operation.error)}{operation.action && <span className="github-publish-dialog__notice-action">{safeText(operation.action)}</span>}</Notice>}
      {operation.reconciliationRequired && <p className="github-publish-dialog__card-copy">Use Reconcile state to check whether the repository, remote, and remote main already exist. Retry uses the same confirmed name and never creates a duplicate without that check.</p>}
    </section>
  );
}

export function GitHubPublishEntry({ workspaceID, disabled, onNotify, onError }: GitHubPublishEntryProps) {
  const [open, setOpen] = useState(false);
  const entryRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef(false);

  useEffect(() => {
    if (open || !restoreFocusRef.current) return;
    restoreFocusRef.current = false;
    const entry = entryRef.current;
    const button = entry?.querySelector<HTMLButtonElement>("button");
    if (button && !button.disabled) button.focus();
    else entry?.focus();
  }, [open]);

  return (
    <div ref={entryRef} className="github-publish-entry" tabIndex={-1}>
      <Button type="button" icon="export" disabled={disabled} aria-haspopup="dialog" onClick={() => { restoreFocusRef.current = false; setOpen(true); }}>Publish to GitHub</Button>
      {open && <GitHubPublishDialog workspaceID={workspaceID} onNotify={onNotify} onError={onError} onClose={() => { restoreFocusRef.current = true; setOpen(false); }} />}
    </div>
  );
}

function GitHubPublishDialog({
  workspaceID,
  onNotify,
  onError,
  onClose,
}: {
  workspaceID: string;
  onNotify: GitHubPublishEntryProps["onNotify"];
  onError: GitHubPublishEntryProps["onError"];
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const aliveRef = useRef(true);
  const cancelledRef = useRef(false);
  const closingRef = useRef(false);
  const closeAfterCancelRef = useRef(false);
  const authPollAtRef = useRef(0);
  const authReceivedAtRef = useRef(Date.now());
  const openedSessionRef = useRef<string | null>(null);
  const sessionIDRef = useRef<string | null>(null);
  const operationRef = useRef<PublishOperationState | null>(null);
  const cancelRequestedRef = useRef(false);
  const normalCloseRef = useRef(false);
  const cleanupStartedRef = useRef(false);
  const lifecycleGenerationRef = useRef(0);
  const cleanupTimerRef = useRef<number | null>(null);
  const configurationStartedRef = useRef(false);
  const authCancelRequestedRef = useRef<string | null>(null);
  const operationCancelRequestedRef = useRef<string | null>(null);
  const sessionClearRequestedRef = useRef<string | null>(null);
  const pendingAuthRef = useRef<Promise<GitHubPublishAuthSession> | null>(null);
  const pendingMutationRef = useRef<Promise<GitHubPublishOperation> | null>(null);
  const operationPollRef = useRef<string | null>(null);
  const operationPollGenerationRef = useRef(0);
  const notifiedCompletionRef = useRef(false);
  const expiredSessionRef = useRef<string | null>(null);
  const [phase, setPhase] = useState<DialogPhase>("configuring");
  const [config, setConfig] = useState<GitHubPublishConfiguration | null>(null);
  const [auth, setAuth] = useState<PublishAuthState | null>(null);
  const authRef = useRef<PublishAuthState | null>(null);
  const [draft, setDraft] = useState<PublishDraftState>(() => draftState({ workspaceId: workspaceID, visibility: "private" }));
  const [preflight, setPreflight] = useState<GitHubPublishPreflight | null>(null);
  const [operation, setOperation] = useState<PublishOperationState | null>(null);
  const [notice, setNotice] = useState<{ tone: NoticeTone; message: string } | null>(null);
  const [busyAction, setBusyAction] = useState<string | null>("configuration");
  const [connectExisting, setConnectExisting] = useState(false);
  const [retryUnknownCreation, setRetryUnknownCreation] = useState(false);
  const [validationError, setValidationError] = useState("");
  const [now, setNow] = useState(() => Date.now());
  const [focusRevision, setFocusRevision] = useState(0);
  const [operationPollTick, setOperationPollTick] = useState(0);
  authRef.current = auth;

  const busy = busyAction !== null;

  const reportFailure = (error: unknown, fallback: string) => {
    if (!aliveRef.current) return;
    const message = safeError(error, fallback);
    setNotice({ tone: "error", message });
    onError(error);
  };

  const rememberAuthSession = (next: GitHubPublishAuthSession) => {
    if (!next.sessionId) return;
    const previous = sessionIDRef.current;
    sessionIDRef.current = next.sessionId;
    if (previous !== next.sessionId) {
      authPollAtRef.current = 0;
      authReceivedAtRef.current = Date.now();
      authCancelRequestedRef.current = null;
      operationCancelRequestedRef.current = null;
      expiredSessionRef.current = null;
    }
  };

  const clearSessionID = async (value?: string): Promise<boolean> => {
    const id = value || sessionIDRef.current || auth?.sessionId;
    if (!id) return true;
    if (sessionClearRequestedRef.current === id) return true;
    sessionClearRequestedRef.current = id;
    try {
      await AppService.ClearGitHubPublishSession(id);
      if (sessionIDRef.current === id) sessionIDRef.current = null;
      return true;
    } catch (error) {
      if (sessionClearRequestedRef.current === id) sessionClearRequestedRef.current = null;
      reportFailure(error, "The GitHub session could not be cleared safely.");
      return false;
    }
  };

  const loadConfiguration = async (force = false) => {
    if (closingRef.current || cancelledRef.current || (!force && configurationStartedRef.current)) return;
    configurationStartedRef.current = true;
    setBusyAction("configuration");
    setPhase("configuring");
    setConfig(null);
    setNotice(null);
    try {
      const result = await AppService.GitHubPublishConfiguration();
      if (!aliveRef.current || closingRef.current || cancelledRef.current) return;
      setConfig(result);
      if (result.configured === false || result.clientIdConfigured === false) {
        configurationStartedRef.current = false;
        setBusyAction(null);
        setNotice({ tone: "warning", message: `${result.message || "GitHub publishing is not configured for this desktop."}${result.action ? ` ${result.action}` : ""}` });
        return;
      }
      void startAuth();
    } catch (error) {
      configurationStartedRef.current = false;
      reportFailure(error, "GitHub publishing configuration could not be loaded.");
      setBusyAction(null);
    }
  };

  const startAuth = async () => {
    if (closingRef.current || busyAction === "auth") return;
    cancelledRef.current = false;
    cancelRequestedRef.current = false;
    setBusyAction("auth");
    setPhase("auth");
    setNotice(null);
    setValidationError("");
    const previous = sessionIDRef.current;
    openedSessionRef.current = null;
    if (previous) {
      const cleared = await clearSessionID(previous);
      if (!cleared) {
        setAuth(authState({
          state: "error",
          retryable: true,
          error: "The previous GitHub session could not be cleared safely.",
        }));
        setBusyAction(null);
        return;
      }
    }
    if (!aliveRef.current || closingRef.current) return;
    setAuth(authState({ state: "auth_starting", retryable: true }));
    authReceivedAtRef.current = Date.now();
    const request = Promise.resolve().then(() => AppService.StartGitHubPublishAuth());
    pendingAuthRef.current = request;
    try {
      const next = normalizeAuth(await request);
      if (!aliveRef.current || closingRef.current || cancelledRef.current) {
        if (next.sessionId) {
          try { await AppService.CancelGitHubPublishAuth(next.sessionId); } catch {}
          await clearSessionID(next.sessionId);
        }
        return;
      }
      rememberAuthSession(next);
      const state = asAuthState(next.state);
      if (next.sessionId) {
        setDraft((current) => draftState({ ...current, sessionId: next.sessionId }));
      }
      if (state === "authenticated" && next.login.trim()) {
        setAuth(authState({ ...next, userCode: "", verificationUri: "" }));
        setPhase("draft");
        setBusyAction(null);
      } else {
        setAuth(next);
        setBusyAction(state === "device_waiting" ? null : "auth");
      }
    } catch (error) {
      reportFailure(error, "GitHub secure sign-in could not start.");
      setAuth(authState({
        state: "error",
        retryable: true,
        error: "GitHub secure sign-in could not start.",
      }));
      setBusyAction(null);
    } finally {
      if (pendingAuthRef.current === request) pendingAuthRef.current = null;
    }
  };

  const openVerification = () => {
    const url = cleanGitHubURL(auth?.verificationUri);
    if (!url) {
      setNotice({ tone: "error", message: "GitHub did not provide a safe verification URL. Use Try again to request a new device code." });
      return;
    }
    void Browser.OpenURL(url).catch((error: unknown) => {
      reportFailure(error, "The GitHub sign-in page could not be opened.");
    });
  };

  const cancelAuthBackend = async () => {
    const id = sessionIDRef.current || auth?.sessionId;
    if (!id) return true;
    if (authCancelRequestedRef.current === id) return true;
    authCancelRequestedRef.current = id;
    try {
      await AppService.CancelGitHubPublishAuth(id);
      return true;
    } catch (error) {
      if (authCancelRequestedRef.current === id) authCancelRequestedRef.current = null;
      reportFailure(error, "GitHub sign-in could not be cancelled cleanly.");
      return false;
    }
  };

  const cancelBeforeMutation = async () => {
    if (busyAction === "cancel") return;
    cancelledRef.current = true;
    cancelRequestedRef.current = true;
    setBusyAction("cancel");
    if (auth && (asAuthState(auth.state) === "auth_starting" || asAuthState(auth.state) === "device_waiting")) await cancelAuthBackend();
    await clearSessionID();
    if (!aliveRef.current) return;
    setAuth((current) => current ? authState({ ...current, state: "cancelled", userCode: "", verificationUri: "", sessionId: "" }) : current);
    setPhase("cancelled");
    setNotice({ tone: "info", message: "Cancelled before repository mutation. No GitHub repository, remote, or push was changed." });
    setBusyAction(null);
  };

  const finishClose = async () => {
    if (!closingRef.current) return;
    const cleared = await clearSessionID();
    if (!aliveRef.current) return;
    if (!cleared) {
      closingRef.current = false;
      setBusyAction(null);
      return;
    }
    onClose();
  };
  const cancelOperation = async (closeAfter = false) => {
    const id = operationRef.current?.operationId;
    if (!id) {
      if (pendingMutationRef.current) {
        cancelRequestedRef.current = true;
        setBusyAction("cancel-operation");
        setFocusRevision((current) => current + 1);
        setNotice({ tone: "warning", message: "Cancellation will be sent as soon as the publish operation returns a status." });
      } else if (closeAfter) {
        await finishClose();
      } else if (aliveRef.current) {
        setNotice({ tone: "warning", message: "There is no cancellable operation status yet. Reconcile before closing." });
      }
      return;
    }
    closeAfterCancelRef.current = closeAfterCancelRef.current || closeAfter;
    cancelRequestedRef.current = true;
    setBusyAction("cancel-operation");
    setFocusRevision((current) => current + 1);
    if (operationCancelRequestedRef.current === id) return;
    operationCancelRequestedRef.current = id;
    try {
      await AppService.CancelGitHubPublish(id);
      if (aliveRef.current) setNotice({ tone: "warning", message: "Cancellation requested. Waiting for the backend to report which steps completed…" });
      if (!aliveRef.current && closeAfter) await clearSessionID();
    } catch (error) {
      if (operationCancelRequestedRef.current === id) operationCancelRequestedRef.current = null;
      reportFailure(error, "The publish cancellation request failed. The operation may still be running; retry cancellation or reconcile its state.");
      if (aliveRef.current) {
        cancelRequestedRef.current = false;
        closingRef.current = false;
        normalCloseRef.current = false;
        setBusyAction(null);
      } else if (closeAfter) {
        await clearSessionID();
      }
    }
  };

  const closeDialog = async () => {
    if (closingRef.current) return;
    normalCloseRef.current = true;
    const mutationWasActive = phase === "mutating" || Boolean(pendingMutationRef.current) || Boolean(operationRef.current && (asOperationState(operationRef.current.state) === "queued" || asOperationState(operationRef.current.state) === "running"));
    closingRef.current = true;
    setBusyAction("close");
    if (pendingAuthRef.current) {
      try { await pendingAuthRef.current; } catch { /* the start error is already surfaced */ }
    }
    if (pendingMutationRef.current) {
      try { await pendingMutationRef.current; } catch { /* the mutation outcome is handled below */ }
    }
    const currentOperation = operationRef.current;
    if (currentOperation && (asOperationState(currentOperation.state) === "queued" || asOperationState(currentOperation.state) === "running")) {
      await cancelOperation(true);
      return;
    }
    if (mutationWasActive && currentOperation && currentOperation.state !== "complete") {
      closingRef.current = false;
      normalCloseRef.current = false;
      closeAfterCancelRef.current = false;
      setBusyAction(null);
      setPhase("partial");
      return;
    }
    if (auth && (asAuthState(auth.state) === "auth_starting" || asAuthState(auth.state) === "device_waiting")) await cancelAuthBackend();
    await finishClose();
  };

  const validateDraft = (value: PublishDraftState): string => {
    const name = value.name.trim();
    if (!name) return "Enter a repository name.";
    if (name.length > 100 || !/^[A-Za-z0-9._-]+$/.test(name)) return "Use 1–100 ASCII letters, numbers, dots, hyphens, or underscores.";
    return "";
  };

  const runPreflight = async (value = draft) => {
    const normalized = draftState({
      ...value,
      sessionId: sessionIDRef.current || auth?.sessionId || value.sessionId,
      name: value.name.trim(),
      description: value.description.trim(),
    });
    const validation = validateDraft(normalized);
    if (validation) {
      setValidationError(validation);
      setPhase("draft");
      return;
    }
    if (!normalized.sessionId || !auth || asAuthState(auth.state) !== "authenticated" || !auth.login.trim()) {
      setNotice({ tone: "error", message: "Sign in to GitHub again before checking this target." });
      setPhase("auth");
      return;
    }
    setDraft(normalized);
    setValidationError("");
    setBusyAction("preflight");
    setPhase("preflighting");
    setNotice(null);
    try {
      const next = await AppService.PreflightGitHubPublish(workspaceID, normalized);
      if (!aliveRef.current || closingRef.current) return;
      const publicTarget = effectiveVisibility(next, normalized) === "public";
      setPreflight(next);
      setBusyAction(null);
      if (next.creationUnknown || next.reconciliationRequired) {
        setPhase("review");
      } else if (publicTarget && !normalized.publicConfirmed) {
        setPhase("public_confirmation");
      } else if (targetIsExisting(next) && !connectExisting) {
        setPhase("connect_confirmation");
      } else {
        setPhase("review");
      }
      if (next.error) setNotice({ tone: "error", message: safeText(next.error) });
    } catch (error) {
      reportFailure(error, "The local repository and GitHub target could not be checked.");
      setBusyAction(null);
      setPhase("review");
    }
  };

  const confirmPublic = () => {
    if (!preflight) return;
    const nextDraft = { ...draft, publicConfirmed: true };
    setDraft(nextDraft);
    void runPreflight(nextDraft);
  };

  const confirmExisting = () => {
    setConnectExisting(true);
    setPhase("review");
  };

  const editDraft = () => {
    setPreflight(null);
    setConnectExisting(false);
    setRetryUnknownCreation(false);
    setDraft((current) => ({ ...current, publicConfirmed: false }));
    setPhase("draft");
    setNotice(null);
  };

  const reconcile = () => {
    setConnectExisting(false);
    setRetryUnknownCreation(true);
    const nextDraft = { ...draft, publicConfirmed: draft.visibility === "public" ? false : draft.publicConfirmed };
    setDraft(nextDraft);
    setPreflight(null);
    if (operationRef.current) {
      setNotice({ tone: "info", message: "The previous operation ended its short-lived session. Reauthenticate before the read-only reconciliation check." });
      void startAuth();
      return;
    }
    void runPreflight(nextDraft);
  };
  const acknowledgeUnknownCreation = () => {
    setRetryUnknownCreation(true);
    setNotice({ tone: "warning", message: "User-directed retry acknowledged. The backend will reconcile the target before attempting another creation." });
  };
  const handleOperationResult = (next: GitHubPublishOperation) => {
    if (!aliveRef.current) return;
    const safeNext = normalizeOperation(next);
    const state = asOperationState(safeNext.state);
    operationRef.current = safeNext;
    setOperation(safeNext);
    if (state === "complete") {
      const upstreamStep = safeNext.steps.find((step) => step.name === "upstream_verification");
      if (!safeNext.partial.upstreamVerified && asStepState(upstreamStep?.state || "unknown") !== "succeeded") {
        setPhase("partial");
        setBusyAction(null);
        setNotice({ tone: "warning", message: "The backend reported completion without verified upstream tracking. This is not shown as success; reconcile before retrying." });
        return;
      }
      setPhase("complete");
      setBusyAction(null);
      setNotice(null);
      if (!notifiedCompletionRef.current) {
        notifiedCompletionRef.current = true;
        const url = cleanGitHubURL(safeNext.partial.createdUrl || safeNext.partial.repositoryUrl || targetURL(preflight, draft, auth?.login));
        onNotify(url ? `Published to ${url}. Main and upstream were verified.` : "Published to GitHub. Main and upstream were verified.", "success");
      }
      return;
    }
    if (state === "failed" || state === "cancelled" || state === "unknown" || state === "needs_confirmation") {
      setPhase("partial");
      setBusyAction(null);
      if (state === "cancelled") setNotice(null);
      else if (state === "unknown") setNotice({ tone: "warning", message: "Publish status is unknown. Reconcile the GitHub target and local remote before retrying; no duplicate creation will be attempted automatically." });
      else if (state === "needs_confirmation") setNotice({ tone: "warning", message: "The backend requires another explicit confirmation before continuing." });
      else if (safeNext.error) setNotice({ tone: "error", message: safeText(safeNext.error) });
      return;
    }
    setPhase("mutating");
    setBusyAction(null);
  };

  const startPublish = async () => {
    if (!preflight || !auth || !auth.sessionId || !auth.login.trim() || !canPublish(preflight, draft, connectExisting, retryUnknownCreation)) return;
    const request = startRequest({
      planId: preflight.planId,
      sessionId: auth.sessionId,
      confirmed: true,
      publicConfirmed: effectiveVisibility(preflight, draft) === "public" && draft.publicConfirmed,
      connectExisting,
      retryUnknownCreation,
    });
    operationRef.current = null;
    setOperation(null);
    setNotice(null);
    setBusyAction("publish");
    cancelRequestedRef.current = false;
    const pending = Promise.resolve().then(() => AppService.StartGitHubPublish(request));
    pendingMutationRef.current = pending;
    try {
      const next = normalizeOperation(await pending);
      if (!aliveRef.current) {
        operationRef.current = next;
        if (next.sessionId) sessionIDRef.current = next.sessionId;
        const nextState = asOperationState(next.state);
        if (normalCloseRef.current && next.operationId && (nextState === "queued" || nextState === "running")) {
          operationCancelRequestedRef.current = next.operationId;
          try { await AppService.CancelGitHubPublish(next.operationId); } catch {}
        }
        if (normalCloseRef.current) await clearSessionID(next.sessionId);
        return;
      }
      if (!next.operationId) {
        const unknown = operationState({
          ...next,
          state: "unknown",
          reconciliationRequired: true,
          retryable: true,
          error: next.error || "The publish operation did not return a safe operation status.",
        });
        handleOperationResult(unknown);
        return;
      }
      operationPollRef.current = next.operationId;
      if (next.sessionId) sessionIDRef.current = next.sessionId;
      handleOperationResult(next);
      if (cancelRequestedRef.current && (asOperationState(next.state) === "queued" || asOperationState(next.state) === "running")) void cancelOperation(closeAfterCancelRef.current);
    } catch (error) {
      reportFailure(error, "The publish request outcome is unknown. Reconcile the GitHub target before retrying.");
      const unknown = operationState({
        state: "unknown",
        retryable: true,
        reconciliationRequired: true,
        error: "The publish request outcome is unknown. Reconcile the GitHub target before retrying.",
      });
      handleOperationResult(unknown);
    } finally {
      if (pendingMutationRef.current === pending) pendingMutationRef.current = null;
    }
  };

  const canPublishNow = preflight ? canPublish(preflight, draft, connectExisting, retryUnknownCreation) : false;

  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (!dialog.open) dialog.showModal();
    if (!dialog.contains(document.activeElement)) dialog.focus();
  }, []);

  useEffect(() => {
    const generation = lifecycleGenerationRef.current + 1;
    lifecycleGenerationRef.current = generation;
    if (cleanupTimerRef.current !== null) {
      window.clearTimeout(cleanupTimerRef.current);
      cleanupTimerRef.current = null;
    }
    aliveRef.current = true;
    closingRef.current = false;
    if (!configurationStartedRef.current) void loadConfiguration();

    return () => {
      aliveRef.current = false;
      const cleanupGeneration = generation;
      if (cleanupTimerRef.current !== null) window.clearTimeout(cleanupTimerRef.current);
      cleanupTimerRef.current = window.setTimeout(() => {
        cleanupTimerRef.current = null;
        if (lifecycleGenerationRef.current !== cleanupGeneration) return;
        closingRef.current = true;
        if (normalCloseRef.current || cleanupStartedRef.current) return;
        cleanupStartedRef.current = true;

        const normalizeID = (value: string | null | undefined): string | undefined => value?.trim() || undefined;
        const sessionIDs: string[] = [];
        const rememberSession = (id: string | null | undefined) => {
          const normalized = normalizeID(id);
          if (normalized && !sessionIDs.includes(normalized)) sessionIDs.push(normalized);
        };
        rememberSession(sessionIDRef.current);
        const activeAuth = authRef.current;
        rememberSession(activeAuth?.sessionId);
        const activeOperation = operationRef.current;
        const activeOperationState = activeOperation ? asOperationState(activeOperation.state) : "unknown";
        const activeOperationID = activeOperationState === "queued" || activeOperationState === "running"
          ? normalizeID(activeOperation?.operationId)
          : undefined;
        const activeAuthState = activeAuth ? asAuthState(activeAuth.state) : "idle";
        const activeAuthID = activeAuthState === "auth_starting" || activeAuthState === "device_waiting"
          ? normalizeID(activeAuth?.sessionId) || normalizeID(sessionIDRef.current)
          : undefined;
        const pendingAuth = pendingAuthRef.current;
        const pendingMutation = pendingMutationRef.current;

        const cleanup = async (): Promise<void> => {
          const cancelOperationForCleanup = async (id?: string): Promise<void> => {
            if (!id || operationCancelRequestedRef.current === id) return;
            operationCancelRequestedRef.current = id;
            try {
              await AppService.CancelGitHubPublish(id);
            } catch {}
          };
          const cancelAuthForCleanup = async (id?: string): Promise<void> => {
            if (!id || authCancelRequestedRef.current === id) return;
            authCancelRequestedRef.current = id;
            try {
              await AppService.CancelGitHubPublishAuth(id);
            } catch {}
          };

          try {
            await cancelOperationForCleanup(activeOperationID);
            if (pendingMutation) {
              try {
                const next = await pendingMutation;
                rememberSession(next.sessionId);
                const nextState = asOperationState(next.state);
                const nextOperationID = normalizeID(next.operationId);
                if ((nextState === "queued" || nextState === "running") && nextOperationID) {
                  await cancelOperationForCleanup(nextOperationID);
                }
              } catch {}
            }
            await cancelAuthForCleanup(activeAuthID);
            if (pendingAuth) {
              try {
                const next = await pendingAuth;
                const nextID = normalizeID(next.sessionId) || activeAuthID;
                rememberSession(nextID);
                await cancelAuthForCleanup(nextID);
              } catch {
                await cancelAuthForCleanup(activeAuthID);
              }
            }
          } finally {
            for (const id of sessionIDs) {
              if (sessionClearRequestedRef.current === id) continue;
              sessionClearRequestedRef.current = id;
              try {
                await AppService.ClearGitHubPublishSession(id);
              } catch {}
            }
          }
        };
        void cleanup();
      }, 0);
    };
  }, []);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const target = phase === "draft"
      ? nameRef.current
      : phase === "mutating"
        ? dialog.querySelector<HTMLElement>(".github-publish-dialog__progress-card")
        : dialog.querySelector<HTMLElement>(".github-publish-dialog__focus-target:not(:disabled)") || dialog;
    if (!target) return;
    if (phase === "review") dialog.querySelector<HTMLElement>(".github-publish-dialog__final-confirm")?.scrollIntoView({ block: "nearest" });
    const activeElement = document.activeElement;
    const userIsTyping = activeElement instanceof HTMLInputElement
      || activeElement instanceof HTMLTextAreaElement
      || (activeElement instanceof HTMLElement && activeElement.isContentEditable);
    const closeButton = dialog.querySelector<HTMLElement>(".github-publish-dialog__close");
    if (!userIsTyping || activeElement === dialog || activeElement === closeButton) target.focus();
  }, [phase, auth?.state, focusRevision]);

  useEffect(() => {
    if (!auth || asAuthState(auth.state) !== "device_waiting") return;
    const id = auth.sessionId || sessionIDRef.current;
    if (!id) return;
    const receivedAt = authReceivedAtRef.current || Date.now();
    const expiry = parseExpiry(auth.expiresAt, receivedAt, auth.expiresInSeconds);
    const intervalSeconds = Math.max(MIN_AUTH_INTERVAL_SECONDS, auth.pollIntervalSeconds || 5);
    const nextPollAt = auth.nextPollAt ? Date.parse(auth.nextPollAt) : Number.NaN;
    const serverDelay = Number.isFinite(nextPollAt) ? Math.max(0, nextPollAt - Date.now()) : intervalSeconds * 1000;
    const elapsedSincePoll = Date.now() - authPollAtRef.current;
    const delay = Math.max(serverDelay, intervalSeconds * 1000 - Math.max(0, elapsedSincePoll));
    const timeout = window.setTimeout(async () => {
      if (!aliveRef.current || closingRef.current || cancelledRef.current || authPollAtRef.current === -1) return;
      if (expiry && Date.now() >= expiry) {
        if (expiredSessionRef.current !== id) {
          expiredSessionRef.current = id;
          setAuth((current) => current && (current.sessionId || sessionIDRef.current) === id
            ? authState({ ...current, state: "expired", userCode: "", error: "The device code expired.", retryable: true })
            : current);
          void clearSessionID(id);
        }
        return;
      }
      authPollAtRef.current = Date.now();
      try {
        const next = normalizeAuth(await AppService.PollGitHubPublishAuth(id));
        if (!aliveRef.current || closingRef.current || cancelledRef.current) return;
        const state = asAuthState(next.state);
        rememberAuthSession(next);
        if (state === "authenticated" && next.login.trim()) {
          setAuth(authState({ ...next, userCode: "", verificationUri: "" }));
          setDraft((current) => draftState({ ...current, sessionId: id }));
          setPhase("draft");
          setBusyAction(null);
        } else {
          setAuth(next);
          if (state !== "device_waiting") setBusyAction(state === "auth_starting" ? "auth" : null);
        }
      } catch (error) {
        reportFailure(error, "GitHub authorization polling failed. Try again to request a fresh device code.");
        setAuth((current) => current ? authState({ ...current, state: "error", error: "GitHub authorization polling failed.", retryable: true }) : current);
        setBusyAction(null);
      }
    }, delay);
    return () => window.clearTimeout(timeout);
  }, [auth?.state, auth?.sessionId, auth?.nextPollAt, auth?.pollIntervalSeconds, auth?.expiresAt, auth?.expiresInSeconds]);

  useEffect(() => {
    if (!auth || asAuthState(auth.state) !== "device_waiting") return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [auth?.state]);

  useEffect(() => {
    if (!auth || asAuthState(auth.state) !== "device_waiting" || !auth.sessionId || openedSessionRef.current === auth.sessionId) return;
    openedSessionRef.current = auth.sessionId;
    const url = cleanGitHubURL(auth.verificationUri);
    if (!url) {
      setNotice({ tone: "warning", message: "GitHub did not provide a safe verification URL. Use Open GitHub sign-in page after retrying." });
      return;
    }
    void Browser.OpenURL(url).catch((error: unknown) => reportFailure(error, "The GitHub sign-in page could not be opened automatically."));
  }, [auth?.state, auth?.sessionId, auth?.verificationUri]);

  useEffect(() => {
    const id = operation?.operationId;
    const state = operation ? asOperationState(operation.state) : "unknown";
    if (phase !== "mutating" || !id || !operation || (state !== "queued" && state !== "running")) return;
    if (operationPollRef.current === `inflight:${id}`) return;
    const generation = operationPollGenerationRef.current + 1;
    operationPollGenerationRef.current = generation;
    let timeout: number | null = null;
    const canContinue = () => aliveRef.current
      && operationPollGenerationRef.current === generation
      && !(closingRef.current && !closeAfterCancelRef.current);
    timeout = window.setTimeout(async () => {
      if (!canContinue()) return;
      operationPollRef.current = `inflight:${id}`;
      try {
        const next = await AppService.GetGitHubPublishOperation(id);
        if (!canContinue()) return;
        operationPollRef.current = id;
        handleOperationResult(next);
        const nextState = asOperationState(next.state);
        if ((nextState === "queued" || nextState === "running") && canContinue()) {
          setOperationPollTick((current) => current + 1);
        }
      } catch (error) {
        if (!canContinue()) return;
        operationPollRef.current = id;
        reportFailure(error, "Publish progress is unknown. Reconcile the target before retrying.");
        handleOperationResult(operation
          ? operationState({
              ...operation,
              state: "unknown",
              retryable: true,
              reconciliationRequired: true,
              error: "Publish progress is unknown. Reconcile the target before retrying.",
            })
          : operationState({
              state: "unknown",
              retryable: true,
              reconciliationRequired: true,
              error: "Publish progress is unknown. Reconcile the target before retrying.",
            }));
      }
    }, OPERATION_POLL_DELAY_MS);
    return () => {
      if (timeout !== null) window.clearTimeout(timeout);
      if (operationPollGenerationRef.current === generation) operationPollGenerationRef.current += 1;
    };
  }, [phase, operation?.operationId, operation?.sessionId, operation?.state, operationPollTick]);

  useEffect(() => {
    const current = operationRef.current;
    if (!current || !closeAfterCancelRef.current || asOperationState(current.state) === "queued" || asOperationState(current.state) === "running") return;
    closeAfterCancelRef.current = false;
    const upstreamStep = current.steps.find((step) => step.name === "upstream_verification");
    if (asOperationState(current.state) === "complete" && (current.partial.upstreamVerified || asStepState(upstreamStep?.state || "unknown") === "succeeded")) {
      void finishClose();
      return;
    }
    closingRef.current = false;
    setBusyAction(null);
    setPhase("partial");
  }, [operation?.state]);

  const renderAuth = () => auth ? <AuthCard auth={auth} config={config} now={now} receivedAt={authReceivedAtRef.current} onOpenVerification={openVerification} onRetry={() => void startAuth()} /> : <section className="github-publish-dialog__card"><Spinner /><p>Preparing secure GitHub sign-in…</p></section>;

  return (
    <dialog ref={dialogRef} className="github-publish-dialog" tabIndex={-1} aria-labelledby="github-publish-dialog-title" onCancel={(event) => { event.preventDefault(); void closeDialog(); }}>
      <form className="github-publish-dialog__form" onSubmit={(event: FormEvent) => event.preventDefault()}>
        <header className="github-publish-dialog__header">
          <div><span className="github-publish-dialog__eyebrow">ACTIVE MOD MAKER REPOSITORY</span><h2 id="github-publish-dialog-title">Publish to GitHub</h2><p>Securely create or connect a repository, preserve existing remotes, and push the local <code>main</code> branch.</p></div>
          <Button type="button" icon="close" tone="quiet" className="github-publish-dialog__close" onClick={() => void closeDialog()} aria-label="Close Publish to GitHub dialog">Close</Button>
        </header>
        <div className="github-publish-dialog__body" aria-busy={busy}>
          {notice && <Notice tone={notice.tone}>{notice.message}</Notice>}
          {config && phase !== "configuring" && (config.message || config.action) && <p className="github-publish-dialog__config-note">{safeText(config.message || "")}{config.action && <><span> </span>{safeText(config.action)}</>}</p>}
          {phase === "configuring" && <section className="github-publish-dialog__loading"><Spinner /><strong>Checking GitHub publishing configuration…</strong><span>No credentials are requested until secure device sign-in starts.</span></section>}
          {phase === "auth" && renderAuth()}
          {phase === "draft" && auth && <><div className="github-publish-dialog__verified-strip"><Icon name="check" size={16} /><span>Verified GitHub account <strong>@{safeText(auth.login, "unavailable")}</strong>. No repository mutation has happened.</span></div><DraftCard draft={draft} login={auth.login} validationError={validationError} nameRef={nameRef} onNameChange={(name) => { setDraft((current) => ({ ...current, name })); setValidationError(""); }} onDescriptionChange={(description) => setDraft((current) => ({ ...current, description }))} onVisibilityChange={(visibility) => { setDraft((current) => ({ ...current, visibility, publicConfirmed: false })); setValidationError(""); }} /></>}
          {phase === "preflighting" && <><section className="github-publish-dialog__loading"><Spinner /><strong>Checking local repository and GitHub target…</strong><span>Read-only checks run before any repository creation or remote change.</span></section>{preflight && auth && <ReviewCard draft={draft} auth={auth} preflight={preflight} connectExisting={connectExisting} retryUnknownCreation={retryUnknownCreation} />}</>}
          {(phase === "public_confirmation" || phase === "connect_confirmation" || phase === "review") && preflight && auth && <><ReviewCard draft={draft} auth={auth} preflight={preflight} connectExisting={connectExisting} retryUnknownCreation={retryUnknownCreation} />{phase === "public_confirmation" && <Notice tone="warning"><strong>Public repository confirmation required.</strong> This is a deliberate second confirmation. GitHub visibility cannot be changed by this workflow after creation.<br /><Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={confirmPublic}>Confirm Public visibility</Button></Notice>}{phase === "connect_confirmation" && <Notice tone="warning"><strong>This GitHub repository already exists or is accessible.</strong> {effectiveVisibility(preflight, draft) === "public" ? "This existing GitHub repository is public. Confirm an explicit connection only if you can push to that public target." : "It will not be treated as newly created. Confirm an explicit connection only if you can push to that target."}<br /><Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={confirmExisting}>Connect to existing repository</Button></Notice>}{phase === "review" && preflight.creationUnknown && !retryUnknownCreation && <div className="github-publish-dialog__inline-actions"><Button type="button" icon="refresh" onClick={reconcile}>Reconcile target</Button><Button type="button" onClick={acknowledgeUnknownCreation}>Allow user-directed retry</Button></div>}{phase === "review" && preflight.reconciliationRequired && !preflight.creationUnknown && <div className="github-publish-dialog__inline-actions"><Button type="button" icon="refresh" onClick={reconcile}>Reconcile state</Button></div>}</>}
          {phase === "review" && preflight && auth && canPublishNow && <div className="github-publish-dialog__final-confirm"><Icon name="warning" size={18} /><div><strong>Final confirmation</strong><span>{effectiveVisibility(preflight, draft) === "public" ? targetIsExisting(preflight) ? "Connect to the confirmed existing public repository and push local main." : "Create the confirmed public repository and push local main." : targetIsExisting(preflight) ? "Connect to the confirmed existing private repository and push local main." : "Create the confirmed private repository and push local main."}</span></div></div>}
          {phase === "mutating" && operation && auth && <ProgressCard operation={operation} auth={auth} target={targetURL(preflight, draft, auth.login)} />}
          {phase === "partial" && operation && <><PartialCard operation={operation} target={targetURL(preflight, draft, auth?.login)} /><div className="github-publish-dialog__recovery-actions">{(operation.reconciliationRequired || operation.retryable || asOperationState(operation.state) === "unknown" || asOperationState(operation.state) === "failed" || asOperationState(operation.state) === "cancelled") && <Button type="button" icon="refresh" className="github-publish-dialog__focus-target" onClick={reconcile}>Reconcile state before retry</Button>}<Button type="button" tone="quiet" onClick={editDraft}>Edit repository details</Button></div></>}
          {phase === "complete" && operation && auth && <><ProgressCard operation={operation} auth={auth} target={targetURL(preflight, draft, auth.login)} /><section className="github-publish-dialog__card github-publish-dialog__complete-card"><Icon name="check" size={24} /><div><h3>Published successfully</h3><p>{cleanGitHubURL(operation.partial.createdUrl || operation.partial.repositoryUrl || targetURL(preflight, draft, auth.login)) || "The confirmed GitHub repository"} is ready. Local <code>main</code> tracks the verified upstream; subsequent pushes use the established remote and normal credential helper.</p></div></section></>}
          {phase === "cancelled" && <section className="github-publish-dialog__card"><div className="github-publish-dialog__card-heading"><div><span className="github-publish-dialog__eyebrow">CANCELLED</span><h3>No repository mutation was made</h3></div><Badge tone="neutral">Cancelled</Badge></div><p className="github-publish-dialog__card-copy">The sign-in session was cleared and no GitHub repository, local remote, or push was changed. You can start a fresh sign-in without losing the draft.</p></section>}
        </div>
        <footer className="github-publish-dialog__footer">
          {(phase === "configuring" || phase === "auth" || phase === "draft" || phase === "preflighting" || phase === "public_confirmation" || phase === "connect_confirmation" || phase === "review") && <Button type="button" onClick={() => void (phase === "auth" || phase === "configuring" ? cancelBeforeMutation() : closeDialog())} disabled={busyAction === "close" || busyAction === "cancel"}>{phase === "auth" || phase === "configuring" ? "Cancel sign-in" : "Close"}</Button>}
          {phase === "configuring" && <Button type="button" tone="quiet" onClick={() => void loadConfiguration(true)} disabled={busy}>Retry configuration</Button>}
          {phase === "draft" && <Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={() => void runPreflight()} disabled={busy || !draft.name.trim()}>Review and preflight</Button>}
          {phase === "preflighting" && <span className="github-publish-dialog__footer-status"><Spinner small />Checking; no mutation has started.</span>}
          {phase === "mutating" && <><span className="github-publish-dialog__footer-status"><Spinner small />{cancelRequestedRef.current ? "Waiting for truthful cancellation state…" : "Mutation in progress…"}</span><Button type="button" tone="danger" className="github-publish-dialog__focus-target" onClick={() => void cancelOperation()} disabled={cancelRequestedRef.current || busyAction === "cancel-operation"}>{cancelRequestedRef.current ? "Cancellation requested" : "Cancel publish"}</Button></>}
          {(phase === "public_confirmation" || phase === "connect_confirmation") && <Button type="button" tone="quiet" onClick={editDraft}>Edit details</Button>}
          {phase === "review" && <><Button type="button" tone="quiet" onClick={editDraft}>Edit details</Button>{preflight && !preflight.creationUnknown && !preflight.reconciliationRequired && !preflight.ready && <Button type="button" icon="refresh" onClick={() => void runPreflight()}>Recheck preflight</Button>}{preflight && canPublishNow && <Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={() => void startPublish()} disabled={busy}>{effectiveVisibility(preflight, draft) === "public" ? targetIsExisting(preflight) ? "Connect to public repository and publish" : "Create public repository and publish" : targetIsExisting(preflight) ? "Connect and publish" : "Publish to GitHub"}</Button>}</>}
          {phase === "partial" && <Button type="button" onClick={() => void closeDialog()} disabled={busyAction === "close"}>Close</Button>}
          {phase === "complete" && <Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={() => void closeDialog()} disabled={busyAction === "close"}>Done</Button>}
          {phase === "cancelled" && <><Button type="button" onClick={() => void closeDialog()} disabled={busyAction === "close"}>Close</Button><Button type="button" tone="primary" className="github-publish-dialog__focus-target" onClick={() => void startAuth()} disabled={busy}>Try secure sign-in again</Button></>}
        </footer>
      </form>
    </dialog>
  );
}

function canPublish(preflight: GitHubPublishPreflight, draft: PublishDraftState, connectExisting: boolean, retryUnknownCreation: boolean): boolean {
  if (!preflight.ready || Boolean(preflight.error) || !preflight.planId) return false;
  if (preflight.creationUnknown && !retryUnknownCreation) return false;
  if (preflight.reconciliationRequired && !retryUnknownCreation) return false;
  if (effectiveVisibility(preflight, draft) === "public" && !draft.publicConfirmed) return false;
  return true;
}
