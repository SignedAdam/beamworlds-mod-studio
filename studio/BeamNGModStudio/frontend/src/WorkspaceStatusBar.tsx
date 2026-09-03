import { useEffect, useState, type ReactNode } from "react";
import type {
  VirgilSessionRecord,
  VirgilSessionSummary,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { formatBytes } from "./ui";

interface WorkspaceStatusBarProps {
  source?: {
    lineCount: number;
    sizeBytes: number;
  };
  session?: VirgilSessionRecord;
}

export function WorkspaceStatusBar({
  source,
  session,
}: WorkspaceStatusBarProps) {
  const summary = session?.summary;
  const running = Boolean(
    session &&
      (session.status === "running" ||
        session.status === "starting" ||
        session.runs?.some(
          (run) => run.status === "running" || run.status === "starting",
        )),
  );
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!running) {
      setNow(Date.now());
      return;
    }
    const tick = () => setNow(Date.now());
    tick();
    const timer = window.setInterval(tick, 1000);
    return () => window.clearInterval(timer);
  }, [running, summary?.startedAt, summary?.finishedAt]);

  return (
    <footer className="workspace-statusbar" aria-label="Workspace editor status">
      <div className="workspace-statusbar__shortcuts" aria-label="Editor shortcuts">
        {source && <Shortcut label="Save" keys="Ctrl+S" />}
        <Shortcut label="Search all files" keys="Ctrl+Shift+F" />
        {source && <Shortcut label="Find in file" keys="Ctrl+F" />}
      </div>
      <div
        className="workspace-statusbar__metrics"
        aria-label={source ? "Source file metrics" : "Virgil session metrics"}
      >
        {source ? <SourceMetrics source={source} /> : session ? <SessionMetrics summary={summary} now={now} running={running} /> : null}
      </div>
    </footer>
  );
}

function Shortcut({ label, keys }: { label: string; keys: string }) {
  return (
    <span className="workspace-statusbar__shortcut">
      <span>{label}</span>
      <kbd>{keys}</kbd>
    </span>
  );
}

function SourceMetrics({
  source,
}: {
  source: NonNullable<WorkspaceStatusBarProps["source"]>;
}) {
  return (
    <>
      <Metric
        label="Lines"
        value={source.lineCount.toLocaleString()}
        ariaLabel={`${source.lineCount.toLocaleString()} lines`}
      />
      <Metric
        label="Size"
        value={formatBytes(source.sizeBytes)}
        ariaLabel={`File size ${formatBytes(source.sizeBytes)}`}
      />
    </>
  );
}

function SessionMetrics({
  summary,
  now,
  running,
}: {
  summary?: VirgilSessionSummary | null;
  now: number;
  running: boolean;
}) {
  const added = formatDiff(summary?.addedLines, "+");
  const removed = formatDiff(summary?.removedLines, "−");
  const tokenValue = formatTokens(summary);
  const context = formatContext(summary);
  const elapsed = formatElapsed(summary, now, running);
  const tokenTitle = formatTokenTitle(summary);
  const contextTitle = formatContextTitle(summary);
  const changeLabel = summary?.changeMode === "net" ? "Changes" : "Edits";
  return (
    <>
      <Metric
        label={changeLabel}
        value={
          <span className="workspace-statusbar__diff" aria-label={`Lines added ${added}; lines removed ${removed}`}>
            <span className="workspace-statusbar__diff-add">{added}</span>
            <span className="workspace-statusbar__diff-remove">{removed}</span>
          </span>
        }
        ariaLabel={`Session ${changeLabel.toLowerCase()}: ${added} added, ${removed} removed`}
      />
      <Metric label="Elapsed" value={elapsed} ariaLabel={`Session elapsed time ${elapsed}`} />
      <Metric label="Tokens" value={tokenValue} ariaLabel={tokenTitle} title={tokenTitle} />
      <Metric label="Context" value={context} ariaLabel={contextTitle} title={contextTitle} />
    </>
  );
}

function Metric({
  label,
  value,
  ariaLabel,
  title,
}: {
  label: string;
  value: ReactNode;
  ariaLabel: string;
  title?: string;
}) {
  return (
    <span className="workspace-statusbar__metric" aria-label={ariaLabel} title={title}>
      <span className="workspace-statusbar__metric-label">{label}</span>
      <strong>{value}</strong>
    </span>
  );
}

function formatDiff(value: number | null | undefined, prefix: "+" | "−") {
  return typeof value === "number" && Number.isFinite(value)
    ? `${prefix}${Math.max(0, Math.trunc(value)).toLocaleString()}`
    : "—";
}

function formatTokens(summary?: VirgilSessionSummary | null) {
  const parts: string[] = [];
  if (typeof summary?.totalTokens === "number" && Number.isFinite(summary.totalTokens)) {
    parts.push(`${formatCompactNumber(summary.totalTokens)} total`);
  }
  if (typeof summary?.inputTokens === "number" && Number.isFinite(summary.inputTokens)) {
    parts.push(`in ${formatCompactNumber(summary.inputTokens)}`);
  }
  if (typeof summary?.outputTokens === "number" && Number.isFinite(summary.outputTokens)) {
    parts.push(`out ${formatCompactNumber(summary.outputTokens)}`);
  }
  return parts.length ? parts.join(" · ") : "—";
}

function formatTokenTitle(summary?: VirgilSessionSummary | null) {
  const parts: string[] = [];
  if (typeof summary?.totalTokens === "number" && Number.isFinite(summary.totalTokens)) {
    parts.push(`${summary.totalTokens.toLocaleString()} total tokens`);
  }
  if (typeof summary?.inputTokens === "number" && Number.isFinite(summary.inputTokens)) {
    parts.push(`${summary.inputTokens.toLocaleString()} input`);
  }
  if (typeof summary?.outputTokens === "number" && Number.isFinite(summary.outputTokens)) {
    parts.push(`${summary.outputTokens.toLocaleString()} output`);
  }
  return parts.length ? parts.join(", ") : "Token usage unavailable";
}

function formatContext(summary?: VirgilSessionSummary | null) {
  if (
    typeof summary?.contextUsed !== "number" ||
    !Number.isFinite(summary.contextUsed) ||
    typeof summary.contextLimit !== "number" ||
    !Number.isFinite(summary.contextLimit) ||
    summary.contextLimit <= 0
  )
    return "—";
  const fullness = Math.min(100, Math.max(0, (summary.contextUsed / summary.contextLimit) * 100));
  return `${Math.round(fullness)}%`;
}

function formatContextTitle(summary?: VirgilSessionSummary | null) {
  if (
    typeof summary?.contextUsed !== "number" ||
    !Number.isFinite(summary.contextUsed) ||
    typeof summary.contextLimit !== "number" ||
    !Number.isFinite(summary.contextLimit) ||
    summary.contextLimit <= 0
  )
    return "Context fullness unavailable";
  return `Context fullness: ${summary.contextUsed.toLocaleString()} of ${summary.contextLimit.toLocaleString()} tokens`;
}

function formatElapsed(summary: VirgilSessionSummary | null | undefined, now: number, running: boolean) {
  if (!summary?.startedAt) return "—";
  const startedAt = Date.parse(summary.startedAt);
  if (!Number.isFinite(startedAt)) return "—";
  const finishedAt = summary.finishedAt ? Date.parse(summary.finishedAt) : Number.NaN;
  if (!running && !Number.isFinite(finishedAt)) return "—";
  const end = Number.isFinite(finishedAt) ? finishedAt : now;
  const seconds = Math.max(0, Math.floor((end - startedAt) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${String(minutes % 60).padStart(2, "0")}m`;
}

function formatCompactNumber(value: number) {
  const whole = Math.max(0, Math.trunc(value));
  if (whole < 1000) return whole.toLocaleString();
  const units = ["k", "M", "B"];
  let scaled = whole;
  let unitIndex = -1;
  while (scaled >= 1000 && unitIndex < units.length - 1) {
    scaled /= 1000;
    unitIndex += 1;
  }
  const precision = scaled >= 100 ? 0 : scaled >= 10 ? 1 : 1;
  return `${scaled.toFixed(precision)}${units[unitIndex]}`;
}
