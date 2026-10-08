import { useEffect, useRef, useState } from "react";
import type { WorkspaceImageFile } from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";
import { Icon } from "./icons";
import { formatBytes } from "./ui";
import "./WorkspaceImageViewer.css";

const imageExtensions: Record<string, true> = {
  ".bmp": true,
  ".dds": true,
  ".gif": true,
  ".jpeg": true,
  ".jpg": true,
  ".png": true,
  ".svg": true,
  ".webp": true,
};
const MIN_ZOOM = 0.05;
const MAX_ZOOM = 32;

export function isWorkspaceImagePath(path: string): boolean {
  const lower = path.toLowerCase();
  const dot = lower.lastIndexOf(".");
  return dot >= 0 && imageExtensions[lower.slice(dot)] === true;
}

type Zoom = "fit" | number;

export function WorkspaceImageViewer({ image }: { image: WorkspaceImageFile }) {
  const stageRef = useRef<HTMLDivElement>(null);
  const [zoom, setZoom] = useState<Zoom>("fit");
  const [natural, setNatural] = useState({ width: 0, height: 0 });
  const [stage, setStage] = useState({ width: 0, height: 0 });

  // Width/height describe the source; the rendition can be smaller (downscaled DDS).
  const sourceWidth = image.width || natural.width;
  const sourceHeight = image.height || natural.height;
  const fitScale = sourceWidth && sourceHeight && stage.width && stage.height
    ? Math.min(1, (stage.width - 32) / sourceWidth, (stage.height - 32) / sourceHeight)
    : 1;
  const scale = zoom === "fit" ? Math.max(MIN_ZOOM, fitScale) : zoom;

  useEffect(() => {
    setZoom("fit");
  }, [image.path]);

  useEffect(() => {
    const element = stageRef.current;
    if (!element) return;
    const measure = () => setStage({ width: element.clientWidth, height: element.clientHeight });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    const element = stageRef.current;
    if (!element) return;
    const handleWheel = (event: WheelEvent) => {
      if (!event.ctrlKey) return;
      event.preventDefault();
      setZoom((current) => {
        const base = current === "fit" ? scale : current;
        const next = base * Math.exp(-event.deltaY * 0.0015);
        return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next));
      });
    };
    element.addEventListener("wheel", handleWheel, { passive: false });
    return () => element.removeEventListener("wheel", handleWheel);
  }, [scale]);

  const displayWidth = sourceWidth ? Math.max(1, Math.round(sourceWidth * scale)) : undefined;
  const displayHeight = sourceHeight ? Math.max(1, Math.round(sourceHeight * scale)) : undefined;
  const name = image.path.split("/").pop() ?? image.path;

  return (
    <div className="workspace-image-viewer">
      <div className="workspace-image-viewer__toolbar" role="toolbar" aria-label="Image view">
        <span className="workspace-image-viewer__name" title={image.path}>
          <Icon name="image" size={15} />
          {name}
        </span>
        <div className="workspace-image-viewer__zoom">
          <button
            type="button"
            onClick={() => setZoom(Math.max(MIN_ZOOM, scale / 1.25))}
            aria-label="Zoom out"
            title="Zoom out (Ctrl+wheel)"
          >
            −
          </button>
          <span className="workspace-image-viewer__zoom-value" aria-live="polite">
            {Math.round(scale * 100)}%
          </span>
          <button
            type="button"
            onClick={() => setZoom(Math.min(MAX_ZOOM, scale * 1.25))}
            aria-label="Zoom in"
            title="Zoom in (Ctrl+wheel)"
          >
            +
          </button>
          <button
            type="button"
            className={zoom === "fit" ? "is-active" : undefined}
            aria-pressed={zoom === "fit"}
            onClick={() => setZoom("fit")}
          >
            Fit
          </button>
          <button
            type="button"
            className={zoom === 1 ? "is-active" : undefined}
            aria-pressed={zoom === 1}
            onClick={() => setZoom(1)}
          >
            1:1
          </button>
        </div>
      </div>
      <div
        ref={stageRef}
        className="workspace-image-viewer__stage"
        onDoubleClick={() => setZoom((current) => (current === "fit" ? 1 : "fit"))}
      >
        <img
          className={`workspace-image-viewer__image${scale > 1 ? " is-pixelated" : ""}`}
          src={image.dataUrl}
          alt={name}
          draggable={false}
          width={displayWidth}
          height={displayHeight}
          onLoad={(event) =>
            setNatural({
              width: event.currentTarget.naturalWidth,
              height: event.currentTarget.naturalHeight,
            })
          }
        />
      </div>
      <dl className="workspace-image-viewer__facts">
        <div>
          <dt>Format</dt>
          <dd>{image.format}</dd>
        </div>
        {sourceWidth > 0 && sourceHeight > 0 && (
          <div>
            <dt>Dimensions</dt>
            <dd>
              {sourceWidth} × {sourceHeight}
            </dd>
          </div>
        )}
        <div>
          <dt>Size</dt>
          <dd>{formatBytes(image.sizeBytes)}</dd>
        </div>
        {image.downscaled && (
          <div>
            <dt>Preview</dt>
            <dd>
              {natural.width > 0 ? `${natural.width} × ${natural.height} mip` : "Reduced mip level"}
            </dd>
          </div>
        )}
      </dl>
    </div>
  );
}
