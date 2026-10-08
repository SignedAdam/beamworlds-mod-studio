import { useEffect, useState } from "react";
import type { GameStatus } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import "./GameRunningBanner.css";

function elapsedClock(since: string, now: number): string {
  const started = Date.parse(since);
  if (Number.isNaN(started)) return "";
  const total = Math.max(0, Math.floor((now - started) / 1000));
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${pad(Math.floor(total / 3600))}:${pad(Math.floor(total / 60) % 60)}:${pad(total % 60)}`;
}

/** Shown in place of the Play status line while BeamNG is running. */
export function GameRunningBanner({ status }: { status: GameStatus }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const names = status.collectionNames ?? [];
  const detail = status.studioSession
    ? `${names.length ? names.join(", ") : "Your Play selection"} · ${status.modCount.toLocaleString()} ${status.modCount === 1 ? "mod" : "mods"}`
    : "Started outside Studio · using your normal mods folder";
  const clock = status.since ? elapsedClock(status.since, now) : "";
  return (
    <div className="game-running" role="status" aria-live="polite">
      <strong className="game-running__title">BeamNG is running</strong>
      <span className="game-running__detail">{detail}</span>
      {status.arrivingMods > 0 && (
        <span className="game-running__arriving">
          Adding {status.arrivingMods} new {status.arrivingMods === 1 ? "mod" : "mods"}…
        </span>
      )}
      <span className="game-running__stripes" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      {clock && (
        <time className="game-running__clock" dateTime={status.since} aria-label={`Running for ${clock}`}>
          {clock}
        </time>
      )}
    </div>
  );
}
