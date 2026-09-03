import type {
  RuntimeReport,
  WorkspaceDetail,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import {
  Badge,
  Button,
  EmptyState,
  HelpTip,
  formatDate,
  issueTone,
} from "./ui";

export type WorkspaceTool = "source" | "build" | "context" | "game";
type WorkspaceUtilityTool = Exclude<WorkspaceTool, "source">;

type BusyKey = string;

export interface WorkspaceUtilityPanelProps {
  active: WorkspaceUtilityTool;
  detail: WorkspaceDetail;
  runtime: RuntimeReport | null;
  exportLabel: string;
  busy: BusyKey;
  onClose: () => void;
  onExportLabelChange: (label: string) => void;
  onValidate: () => void | Promise<void>;
  onExport: () => void | Promise<void>;
  onInstall: () => void | Promise<void>;
  onUninstall: () => void | Promise<void>;
  onLaunch: () => void | Promise<void>;
  onAnalyze: () => void | Promise<void>;
}

const titles: Record<WorkspaceUtilityTool, string> = {
  build: "Build",
  context: "Context",
  game: "Game test",
};

export function WorkspaceUtilityPanel({
  active,
  detail,
  runtime,
  exportLabel,
  busy,
  onClose,
  onExportLabelChange,
  onValidate,
  onExport,
  onInstall,
  onUninstall,
  onLaunch,
  onAnalyze,
}: WorkspaceUtilityPanelProps) {
  const issues = detail.validation.issues ?? [];
  const exports = detail.exports ?? [];
  const knowledge = detail.knowledge ?? [];
  const hasValidation = Boolean(detail.workspace.lastValidation);
  return (
    <aside
      className="workspace-utility"
      aria-label={`${titles[active]} workspace utility`}
    >
      <header className="workspace-utility__header">
        <strong>{titles[active]}</strong>
        <HelpTip label={`About ${titles[active]}`}>
          {active === "build"
            ? "Validation checks workspace structure. Export creates a verified immutable ZIP without modifying the original source."
            : active === "context"
              ? "Reference material supplied to Virgil according to context depth in settings."
              : "Install an exact export into the managed test location, exercise it in BeamNG, then analyze only fresh log bytes."}
        </HelpTip>
        <span />
        <Button tone="quiet" icon="close" onClick={onClose}>
          Close
        </Button>
      </header>
      {active === "build" && (
        <div className="maker-pane">
          <section className="build-section">
            <div className="build-row">
              <div>
                <Icon
                  name={
                    detail.validation.valid
                      ? "check"
                      : hasValidation
                        ? "warning"
                        : "activity"
                  }
                  size={17}
                />
                <strong>Validation</strong>
                <span>
                  {detail.validation.valid
                    ? "Passed"
                    : hasValidation
                      ? `${issues.length} review items`
                      : "Not run"}
                </span>
              </div>
              <Button onClick={() => void onValidate()} disabled={busy !== ""}>
                Run validation
              </Button>
            </div>
            {issues.length > 0 && (
              <div className="review-issues">
                {issues.map((issue, index) => (
                  <div key={`${issue.code}-${index}`}>
                    <Badge tone={issueTone(issue.severity)}>
                      {issue.severity}
                    </Badge>
                    <strong>{issue.code}</strong>
                    <span>{issue.message}</span>
                    {issue.path && <code>{issue.path}</code>}
                  </div>
                ))}
              </div>
            )}
            <div className="export-controls">
              <label>
                Export label
                <input
                  value={exportLabel}
                  onChange={(event) => onExportLabelChange(event.target.value)}
                  placeholder="Optional label"
                />
              </label>
              <Button
                tone="primary"
                disabled={busy !== ""}
                onClick={() => void onExport()}
              >
                {busy === "export" ? "Exporting" : "Export ZIP"}
              </Button>
            </div>
            {exports.length > 0 && (
              <div className="export-history">
                {exports.map((item) => (
                  <div key={item.id}>
                    <code>{item.path}</code>
                    <span>{formatDate(item.createdAt)}</span>
                    <code>{item.sha256}</code>
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>
      )}
      {active === "context" && (
        <div className="maker-pane">
          <div className="knowledge-list">
            {knowledge.length === 0 ? (
              <EmptyState
                icon="book"
                title="No context documents"
                detail="This workspace does not currently include reference material for Virgil."
              />
            ) : (
              knowledge.map((document, index) => (
                <details key={document.id} open={index === 0}>
                  <summary>
                    <span>
                      <Icon name="book" size={15} />
                      {document.title}
                    </span>
                    <code>{document.id}</code>
                  </summary>
                  <pre>{document.content}</pre>
                </details>
              ))
            )}
          </div>
        </div>
      )}
      {active === "game" && (
        <div className="maker-pane">
          <div className="game-test-toolbar">
            <div
              className={
                detail.activeTest ? "test-state is-active" : "test-state"
              }
            >
              <Icon name="install" size={16} />
              <div>
                <strong>
                  {detail.activeTest
                    ? "Test ZIP installed"
                    : "No test ZIP installed"}
                </strong>
                <span>
                  {detail.activeTest?.path ||
                    "Build and install an export before launching the game."}
                </span>
              </div>
            </div>
            <Button
              onClick={detail.activeTest ? onUninstall : onInstall}
              disabled={
                (!detail.activeTest && exports.length === 0) || busy !== ""
              }
              tone={detail.activeTest ? "danger" : "default"}
            >
              {detail.activeTest ? "Uninstall test" : "Install latest export"}
            </Button>
            <Button
              onClick={() => void onLaunch()}
              disabled={!detail.activeTest || busy !== ""}
            >
              Launch BeamNG
            </Button>
            <Button
              onClick={() => void onAnalyze()}
              disabled={!detail.activeTest || busy !== ""}
            >
              Analyze runtime
            </Button>
          </div>
          {runtime ? (
            <section className="runtime-report">
              <p>
                <strong>{runtime.errors} errors</strong> · {runtime.warnings}{" "}
                warnings · {runtime.freshBytes.toLocaleString()} fresh bytes
              </p>
              <code>{runtime.logPath}</code>
              {(runtime.diagnostics ?? []).map((diagnostic, index) => (
                <div key={`${diagnostic.component}-${index}`}>
                  <Badge
                    tone={
                      diagnostic.severity === "error" ? "danger" : "warning"
                    }
                  >
                    {diagnostic.severity}
                  </Badge>
                  <strong>{diagnostic.component}</strong>
                  <span>{diagnostic.message}</span>
                  {diagnostic.line > 0 && <code>Line {diagnostic.line}</code>}
                </div>
              ))}
            </section>
          ) : (
            <EmptyState
              icon="activity"
              title="No runtime report"
              detail="Launch BeamNG, then analyze the fresh runtime log to see diagnostics here."
            />
          )}
        </div>
      )}
    </aside>
  );
}
