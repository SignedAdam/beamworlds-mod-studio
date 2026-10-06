import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent,
  type MouseEventHandler,
  type ReactNode,
} from "react";
import "./IndexCardTabs.css";

export interface IndexCardTabItem {
  id: string;
  label: ReactNode;
  icon?: ReactNode;
  count?: number;
  disabled?: boolean;
  panel: ReactNode;
  tabId?: string;
  panelId?: string;
  onClose?: () => void;
  closeLabel?: string;
  title?: string;
  statusTone?: "accent" | "success" | "warning" | "danger";
  statusLabel?: string;
  onContextMenu?: MouseEventHandler<HTMLButtonElement>;
}

export type IndexCardTab = IndexCardTabItem;
export type IndexCardTabsActivationMode = "manual" | "automatic";

export interface IndexCardTabsProps {
  items: readonly IndexCardTabItem[];
  value?: string | null;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  ariaLabel?: string;
  activationMode?: IndexCardTabsActivationMode;
  compact?: boolean;
  actions?: ReactNode;
  mountInactivePanels?: boolean;
  emptyPanel?: ReactNode;
  className?: string;
}

function firstEnabledId(items: readonly IndexCardTabItem[], candidate?: string | null): string | undefined {
  if (candidate !== undefined && candidate !== null) {
    const candidateItem = items.find(item => item.id === candidate);
    if (candidateItem && !candidateItem.disabled) {
      return candidateItem.id;
    }
  }

  return items.find(item => !item.disabled)?.id;
}

function scrollTabIntoView(tab: HTMLButtonElement | undefined): void {
  if (tab && typeof tab.scrollIntoView === "function") {
    tab.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
}

export function IndexCardTabs({
  items,
  value,
  defaultValue,
  onValueChange,
  ariaLabel,
  activationMode = "manual",
  compact = false,
  actions,
  mountInactivePanels = true,
  emptyPanel,
  className,
}: IndexCardTabsProps) {
  const isControlled = value !== undefined;
  const generatedIdPrefix = useId().replace(/:/g, "");
  const tabRefs = useRef(new Map<string, HTMLButtonElement>());
  const itemTokens = useRef(new Map<string, string>());
  const nextItemToken = useRef(0);

  const initialSelection = isControlled && value === null
    ? undefined
    : firstEnabledId(items, isControlled ? value : defaultValue);
  const [uncontrolledValue, setUncontrolledValue] = useState<string | undefined>(() => (
    isControlled ? undefined : initialSelection
  ));
  const [focusedId, setFocusedId] = useState<string | undefined>(() => initialSelection);

  const getItemToken = useCallback((itemId: string): string => {
    const existingToken = itemTokens.current.get(itemId);
    if (existingToken !== undefined) {
      return existingToken;
    }

    const token = String(nextItemToken.current);
    nextItemToken.current += 1;
    itemTokens.current.set(itemId, token);
    return token;
  }, []);

  const itemIdentifiers = useMemo(() => items.map(item => {
    const token = getItemToken(item.id);
    const generatedBase = `index-card-tabs-${generatedIdPrefix}-${token}`;
    return {
      item,
      tabId: item.tabId ?? `${generatedBase}-tab`,
      panelId: item.panelId ?? `${generatedBase}-panel`,
    };
  }), [generatedIdPrefix, getItemToken, items]);

  const enabledIds = useMemo(
    () => items.filter(item => !item.disabled).map(item => item.id),
    [items],
  );

  const requestedId = isControlled ? value : uncontrolledValue;
  const activeId = isControlled && value === null
    ? undefined
    : firstEnabledId(items, requestedId);
  const controlledSelectionInvalid = isControlled && value !== null && activeId !== value;
  const rovingId = !controlledSelectionInvalid
    && focusedId !== undefined
    && enabledIds.includes(focusedId)
    ? focusedId
    : activeId !== undefined && enabledIds.includes(activeId)
      ? activeId
      : enabledIds[0];

  const scrollSelectedTab = useCallback((id: string): void => {
    scrollTabIntoView(tabRefs.current.get(id));
  }, []);

  const selectTab = useCallback((id: string): void => {
    if (!enabledIds.includes(id)) {
      return;
    }

    setFocusedId(id);
    scrollSelectedTab(id);

    if (!isControlled) {
      setUncontrolledValue(currentValue => currentValue === id ? currentValue : id);
    }

    if (activeId !== id) {
      onValueChange?.(id);
    }
  }, [activeId, enabledIds, isControlled, onValueChange, scrollSelectedTab]);

  const focusTab = useCallback((id: string): void => {
    const tab = tabRefs.current.get(id);
    if (tab?.disabled) {
      return;
    }

    setFocusedId(id);
    if (!tab) {
      return;
    }

    try {
      tab.focus({ preventScroll: true });
    } catch {
      tab.focus();
    }
  }, []);

  const handleTabFocus = useCallback((event: FocusEvent<HTMLButtonElement>, id: string): void => {
    if (event.currentTarget.disabled) {
      return;
    }

    setFocusedId(id);
    scrollTabIntoView(event.currentTarget);
    if (activationMode === "automatic") {
      selectTab(id);
    }
  }, [activationMode, selectTab]);

  const handleTabKeyDown = useCallback((event: KeyboardEvent<HTMLButtonElement>, id: string): void => {
    const currentIndex = enabledIds.indexOf(id);
    if (currentIndex < 0) {
      return;
    }

    let nextId: string | undefined;
    switch (event.key) {
      case "ArrowLeft":
        nextId = enabledIds[(currentIndex - 1 + enabledIds.length) % enabledIds.length];
        break;
      case "ArrowRight":
        nextId = enabledIds[(currentIndex + 1) % enabledIds.length];
        break;
      case "Home":
        nextId = enabledIds[0];
        break;
      case "End":
        nextId = enabledIds[enabledIds.length - 1];
        break;
      case "Enter":
      case " ":
      case "Spacebar":
        event.preventDefault();
        selectTab(id);
        return;
      default:
        return;
    }

    if (nextId !== undefined) {
      event.preventDefault();
      focusTab(nextId);
    }
  }, [enabledIds, focusTab, selectTab]);

  useEffect(() => {
    if (isControlled) {
      return;
    }

    const nextValue = firstEnabledId(items, uncontrolledValue);
    if (nextValue !== uncontrolledValue) {
      setUncontrolledValue(nextValue);
      if (nextValue !== undefined) {
        onValueChange?.(nextValue);
      }
    }
  }, [isControlled, items, onValueChange, uncontrolledValue]);

  useEffect(() => {
    setFocusedId(currentId => currentId === rovingId ? currentId : rovingId);
  }, [rovingId]);

  useEffect(() => {
    if (activeId !== undefined) {
      scrollSelectedTab(activeId);
    }
  }, [activeId, scrollSelectedTab]);

  useEffect(() => {
    if (focusedId === undefined || enabledIds.includes(focusedId) || rovingId === undefined) {
      return;
    }

    const activeElement = typeof document === "undefined" ? null : document.activeElement;
    const focusIsOnTab = activeElement !== null
      && Array.from(tabRefs.current.values()).some(tab => tab === activeElement);
    if (focusIsOnTab) {
      focusTab(rovingId);
    }
  }, [enabledIds, focusTab, focusedId, rovingId]);

  const rootClassName = [
    "index-card-tabs",
    compact ? "index-card-tabs--compact" : undefined,
    className,
  ].filter(Boolean).join(" ");

  return (
    <div className={rootClassName}>
      <div className="index-card-tabs__bar">
        <div
          className="index-card-tabs__list"
          role="tablist"
          aria-label={ariaLabel ?? "Tabs"}
          aria-orientation="horizontal"
        >
          {itemIdentifiers.map(({ item, tabId, panelId }) => {
            const selected = item.id === activeId;
            const tabClassName = [
              "index-card-tabs__item",
              selected ? "is-selected" : undefined,
              item.statusTone ? `status-${item.statusTone}` : undefined,
              item.disabled ? "is-disabled" : undefined,
            ].filter(Boolean).join(" ");

            return (
              <div key={item.id} className={tabClassName}>
                <button
                  ref={tab => {
                    if (tab === null) {
                      tabRefs.current.delete(item.id);
                    } else {
                      tabRefs.current.set(item.id, tab);
                    }
                  }}
                  className="index-card-tabs__tab"
                  type="button"
                  role="tab"
                  id={tabId}
                  aria-controls={panelId}
                  aria-selected={selected}
                  aria-disabled={item.disabled || undefined}
                  disabled={item.disabled}
                  tabIndex={item.disabled || item.id !== rovingId ? -1 : 0}
                  onFocus={event => handleTabFocus(event, item.id)}
                  onKeyDown={event => handleTabKeyDown(event, item.id)}
                  onClick={() => selectTab(item.id)}
                  title={item.title}
                  onContextMenu={item.onContextMenu}
                >
                  {item.icon !== undefined && item.icon !== null && (
                    <span className="index-card-tabs__icon">{item.icon}</span>
                  )}
                  <span className="index-card-tabs__label">{item.label}</span>
                  {item.count !== undefined && (
                    <span className="index-card-tabs__count" aria-label={`Count: ${item.count}`}>
                      {item.count}
                    </span>
                  )}
                  {item.statusLabel && (
                    <span className="index-card-tabs__status">{item.statusLabel}</span>
                  )}
                </button>
                {item.onClose && (
                  <button
                    className="index-card-tabs__close"
                    type="button"
                    aria-label={item.closeLabel ?? "Close tab"}
                    onPointerDown={event => event.stopPropagation()}
                    onClick={event => {
                      event.stopPropagation();
                      item.onClose?.();
                    }}
                  >
                    <span aria-hidden="true">×</span>
                  </button>
                )}
              </div>
            );
          })}
        </div>
        {actions !== undefined && actions !== null && (
          <div className="index-card-tabs__actions">{actions}</div>
        )}
      </div>
      {itemIdentifiers.map(({ item, tabId, panelId }) => {
        const selected = item.id === activeId;
        return (
          <div
            key={item.id}
            className="index-card-tabs__panel"
            role="tabpanel"
            id={panelId}
            aria-labelledby={tabId}
            hidden={!selected}
            tabIndex={selected ? 0 : -1}
          >
            {mountInactivePanels || selected ? item.panel : null}
          </div>
        );
      })}
      {activeId === undefined && emptyPanel !== undefined && emptyPanel !== null && (
        <div className="index-card-tabs__empty">{emptyPanel}</div>
      )}
    </div>
  );
}

export default IndexCardTabs;
