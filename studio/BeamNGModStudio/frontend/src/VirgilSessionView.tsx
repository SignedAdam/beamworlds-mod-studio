import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
} from "react";
import { Events } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  AgentActivity,
  VirgilSessionRecord,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import { Button } from "./ui";
import { ReplaceableUIPlaceholder } from "./replaceableUi";

type NoticeTone = "info" | "success" | "error";
type RoutedActivity = AgentActivity & {
  sessionId?: string;
  workspaceId?: string;
};

export function VirgilChoiceDialog({
  modName,
  busy,
  onChoose,
}: {
  modName: string;
  busy?: boolean;
  onChoose: (useAI: boolean) => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const previouslyFocused = document.activeElement as HTMLElement | null;
    if (!dialog.open) dialog.showModal();
    return () => {
      if (dialog.open) dialog.close();
      previouslyFocused?.focus();
    };
  }, []);

  return (
    <dialog
      ref={dialogRef}
      className="virgil-choice-dialog"
      aria-labelledby="virgil-choice-title"
      onCancel={(event) => event.preventDefault()}
    >
      <div className="virgil-choice-dialog__body">
        <h2 id="virgil-choice-title">Open {modName}</h2>
        <p>
          Both choices open the same editable workspace. Choose whether Virgil
          should help you get started.
        </p>
        <div className="virgil-choice-dialog__actions">
          <Button
            tone="primary"
            autoFocus
            disabled={busy}
            onClick={() => onChoose(true)}
          >
            Use Virgil AI
          </Button>
          <Button disabled={busy} onClick={() => onChoose(false)}>
            Edit without AI
          </Button>
        </div>
      </div>
    </dialog>
  );
}

export interface VirgilSessionViewProps {
  workspaceID: string;
  session: VirgilSessionRecord;
  transient?: boolean;
  prompt: string;
  activeFilePath?: string;
  activityBuffer: MutableRefObject<Record<string, AgentActivity[]>>;
  modelOverride?: string;
  locked?: boolean;
  onPromptChange: (value: string) => void;
  onSessionChange: (record: VirgilSessionRecord) => boolean;
  onReload: () => Promise<void>;
  onNotify: (message: string, tone?: NoticeTone) => void;
  onError: (error: unknown) => void;
  onBusyChange?: (busy: boolean) => void;
}

export function VirgilSessionView({
  workspaceID,
  session,
  transient = false,
  prompt,
  activeFilePath,
  activityBuffer,
  locked = false,
  modelOverride = "",
  onPromptChange,
  onSessionChange,
  onReload,
  onNotify,
  onError,
  onBusyChange,
}: VirgilSessionViewProps) {
  const [live, setLive] = useState<Record<string, AgentActivity[]>>(() =>
    copyActivityBuffer(activityBuffer.current, workspaceID, session.id),
  );
  const [history, setHistory] = useState<Record<string, AgentActivity[]>>({});
  const [busy, setBusy] = useState(false);
  const operationBusy = busy || locked;
  const feedRef = useRef<HTMLDivElement>(null);
  const runs = useMemo(() => session.runs ?? [], [session.runs]);
  const runIDs = useMemo(() => new Set(runs.map((run) => run.id)), [runs]);
  const activeRun = useMemo(
    () =>
      runs.find((run) => run.status === "running" || run.status === "starting"),
    [runs],
  );
  const activeID = activeRun?.id;
  const effectiveStatus = activeRun ? "running" : session.status;
  const resumable = !transient && Boolean(session.runtimeSessionId);
  const canResume =
    resumable && (effectiveStatus === "paused" || effectiveStatus === "error");
  const canSend = transient || effectiveStatus === "idle";
  useEffect(() => {
    setLive(
      copyActivityBuffer(activityBuffer.current, workspaceID, session.id),
    );
    setHistory({});
  }, [workspaceID, session.id, activityBuffer]);

  useEffect(() => {
    let cancelled = false;
    void Promise.all(
      runs.map(async (run) => {
        const records = await API.ListAgentEvents(run.id);
        const activities = (records ?? []).map(
          (record) =>
            ({
              ...record,
              runId: record.runId || run.id,
              sessionId: session.id,
              workspaceId: workspaceID,
            }) satisfies AgentActivity,
        );
        return [run.id, activities] as const;
      }),
    )
      .then((entries) => {
        if (cancelled) return;
        setHistory(Object.fromEntries(entries));
      })
      .catch((error) => {
        if (!cancelled) onError(error);
      });
    return () => {
      cancelled = true;
    };
  }, [workspaceID, session.id, runs, onError]);

  useEffect(() => {
    const stop = Events.On("agent:event", (event) => {
      const activity = event.data as RoutedActivity;
      if (!activity.runId) return;
      if (activity.sessionId) {
        if (activity.sessionId !== session.id) return;
      } else if (!runIDs.has(activity.runId)) {
        return;
      }
      if (activity.workspaceId && activity.workspaceId !== workspaceID) return;
      setLive((current) => appendActivity(current, activity));
    });
    return stop;
  }, [workspaceID, session.id, runIDs]);

  const allEvents = useMemo(
    () =>
      runs.flatMap((run) =>
        mergeActivities(history[run.id] ?? [], live[run.id] ?? []),
      ),
    [runs, history, live],
  );

  useEffect(() => {
    const feed = feedRef.current;
    if (feed) feed.scrollTop = feed.scrollHeight;
  }, [allEvents, runs]);

  const updateAfter = (record: VirgilSessionRecord) => {
    const accepted = onSessionChange(record);
    void onReload().catch(onError);
    return accepted;
  };

  const send = async () => {
    const value = prompt.trim();
    if (!value || operationBusy || activeRun || !canSend) return;
    setBusy(true);
    if (transient) onBusyChange?.(true);
    try {
      if (transient) {
        const started = await API.StartVirgilSession(
          workspaceID,
          value,
          modelOverride,
          session.userTitle || "",
        );
        if (!onSessionChange(started)) return;
        onPromptChange("");
        onNotify("Virgil started", "info");
        void onReload().catch(onError);
      } else {
        const run = await API.SendVirgilMessage(
          session.id,
          value,
          modelOverride,
        );
        const nextRuns = [
          ...runs.filter((existing) => existing.id !== run.id),
          run,
        ].sort(
          (left, right) =>
            Date.parse(left.startedAt) - Date.parse(right.startedAt),
        );
        if (
          !updateAfter({
            ...session,
            runs: nextRuns,
            status: "running",
            lastError: "",
            updatedAt: new Date().toISOString(),
          })
        )
          return;
        onPromptChange("");
        onNotify("Virgil started", "info");
      }
    } catch (error) {
      onError(error);
      if (!transient) {
        try {
          await onReload();
        } catch (reloadError) {
          onError(reloadError);
        }
      }
    } finally {
      setBusy(false);
      if (transient) onBusyChange?.(false);
    }
  };

  const stop = async () => {
    if (!activeID || operationBusy) return;
    setBusy(true);
    try {
      await API.StopAgent(activeID);
      await onReload();
    } catch (error) {
      onError(error);
    } finally {
      setBusy(false);
    }
  };

  const resume = async () => {
    if (operationBusy || transient) return;
    setBusy(true);
    try {
      const resumed = await API.ResumeVirgilSession(session.id);
      const accepted = onSessionChange(resumed);
      void onReload().catch(onError);
      if (!accepted) return;
      onNotify("Virgil session resumed", "info");
    } catch (error) {
      onError(error);
    } finally {
      setBusy(false);
    }
  };

  const ordered = [...runs].sort(
    (a, b) => Date.parse(a.startedAt) - Date.parse(b.startedAt),
  );
  const latestActivity = [...allEvents]
    .sort((left, right) => Date.parse(left.at) - Date.parse(right.at))
    .reverse()
    .find(
      (activity) => activity.message || activity.toolName || activity.delta,
    );
  const announcement = activeRun
    ? latestActivity?.message ||
      latestActivity?.toolName?.replace(/_/g, " ") ||
      "Virgil is working"
    : effectiveStatus === "error"
      ? session.lastError || "Virgil encountered an error"
      : effectiveStatus === "paused"
        ? "Virgil session is paused"
        : ordered.length > 0
          ? "Virgil is ready"
          : "Start a conversation with Virgil";

  if (canResume) {
    return (
      <section
        className="virgil-session-view virgil-session-view--paused"
        aria-label="Paused Virgil session"
      >
        <p className="sr-only" role="status" aria-live="polite">
          {announcement}
        </p>
        <div className="virgil-session-resume">
          <h2>resume this session?</h2>
          <code>{session.runtimeSessionId}</code>
          {effectiveStatus === "error" && session.lastError && (
            <p>{session.lastError}</p>
          )}
          <Button
            type="button"
            tone="primary"
            disabled={operationBusy}
            onClick={() => void resume()}
          >
            Resume
          </Button>
        </div>
      </section>
    );
  }

  return (
    <section
      className="virgil-session-view"
      aria-label={`Virgil session ${session.title || "Virgil session"}`}
    >
      <header className="virgil-session-view__header">
        <div className="virgil-session-view__identity">
          <ReplaceableUIPlaceholder entity="virgil-logo" />
          <strong>{session.title || "Virgil session"}</strong>
          <span
            className={`virgil-session-status virgil-session-status--${effectiveStatus}`}
          >
            {statusLabel(effectiveStatus)}
          </span>
          {activeFilePath && (
            <code title={activeFilePath}>{activeFilePath}</code>
          )}
        </div>
        {session.runtimeSessionId && (
          <code
            className="virgil-session-view__runtime-id"
            title={`Virgil session ${session.runtimeSessionId}`}
          >
            {session.runtimeSessionId}
          </code>
        )}
      </header>
      <p className="sr-only" role="status" aria-atomic="true">
        {announcement}
      </p>
      <div
        className="virgil-session-view__feed"
        ref={feedRef}
        role="log"
        aria-label="Virgil conversation"
        aria-relevant="additions text"
        aria-busy={Boolean(activeRun)}
      >
        {!transient && !resumable && (
          <p className="virgil-session-view__notice" role="note">
            {session.lastError ||
              "This imported conversation has no resumable session ID and cannot be resumed."}
          </p>
        )}
        {ordered.length === 0 ? (
          <p className="virgil-session-view__empty">
            {canSend
              ? "Start a conversation with Virgil to edit this workspace."
              : "No resumable conversation is available."}
          </p>
        ) : (
          ordered.map((run) => {
            const events = mergeActivities(
              history[run.id] ?? [],
              live[run.id] ?? [],
            );
            const streamed = events
              .filter((event) => event.delta)
              .map((event) => event.delta)
              .join("");
            const assistant =
              run.status === "running" || run.status === "starting"
                ? streamed || "Virgil is working…"
                : run.finalText ||
                  run.error ||
                  streamed ||
                  "No response recorded";
            return (
              <article className="virgil-turn" key={run.id}>
                <div className="virgil-turn__prompt">
                  <b>You</b>
                  <p>{run.prompt}</p>
                </div>
                <div className="virgil-turn__assistant">
                  <b>Virgil</b>
                  <p>{assistant}</p>
                  {(run.status === "running" || run.status === "starting") &&
                    events
                      .filter((event) => event.toolName || event.message)
                      .slice(-3)
                      .map((event, index) => (
                        <small key={`${event.at}-${index}`}>
                          {event.toolName || event.type}:{" "}
                          {event.message || "in progress"}
                        </small>
                      ))}
                </div>
              </article>
            );
          })
        )}
      </div>
      <form
        className="virgil-session-view__composer"
        onSubmit={(event) => {
          event.preventDefault();
          void send();
        }}
      >
        <textarea
          value={prompt}
          disabled={!canSend || Boolean(activeRun) || operationBusy}
          placeholder="Ask Virgil to change the workspace…"
          aria-label="Message Virgil"
          onChange={(event) => onPromptChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              void send();
            }
          }}
        />
        {activeRun ? (
          <Button
            type="button"
            tone="danger"
            disabled={operationBusy}
            onClick={() => void stop()}
          >
            <Icon name="close" size={14} />
            Stop
          </Button>
        ) : (
          <Button
            type="submit"
            tone="primary"
            disabled={!canSend || !prompt.trim() || operationBusy}
          >
            Send
          </Button>
        )}
      </form>
    </section>
  );
}

function statusLabel(status: string) {
  switch (status) {
    case "starting":
      return "Starting";
    case "running":
      return "Working";
    case "paused":
      return "Paused";
    case "error":
      return "Error";
    default:
      return "Ready";
  }
}

function copyActivityBuffer(
  source: Record<string, AgentActivity[]>,
  workspaceID: string,
  sessionID: string,
): Record<string, AgentActivity[]> {
  const copied: Record<string, AgentActivity[]> = {};
  for (const [runID, records] of Object.entries(source)) {
    const filtered = records.filter((record) => {
      const activity = record as RoutedActivity;
      return (
        (!activity.workspaceId || activity.workspaceId === workspaceID) &&
        (!activity.sessionId || activity.sessionId === sessionID)
      );
    });
    if (filtered.length > 0) copied[runID] = [...filtered];
  }
  return copied;
}

function appendActivity(
  current: Record<string, AgentActivity[]>,
  activity: AgentActivity,
): Record<string, AgentActivity[]> {
  const records = current[activity.runId] ?? [];
  const previous = records[records.length - 1];
  let nextRecords: AgentActivity[];
  if (activity.delta && previous?.delta && previous.type === activity.type) {
    const remaining = (4 << 20) - previous.delta.length;
    if (remaining <= 0) return current;
    nextRecords = [
      ...records.slice(0, -1),
      {
        ...activity,
        delta: previous.delta + activity.delta.slice(0, remaining),
      },
    ];
  } else {
    nextRecords = [...records, activity];
    if (nextRecords.length > 500) {
      nextRecords = nextRecords.slice(nextRecords.length - 500);
    }
  }
  return { ...current, [activity.runId]: nextRecords };
}

function mergeActivities(
  history: AgentActivity[],
  live: AgentActivity[],
): AgentActivity[] {
  const seen = new Set<string>();
  return [...history, ...live]
    .sort((left, right) => Date.parse(left.at) - Date.parse(right.at))
    .filter((activity) => {
      const key = `${activity.at}\u0000${activity.type}\u0000${activity.message ?? ""}\u0000${activity.toolName ?? ""}\u0000${activity.delta ?? ""}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}
