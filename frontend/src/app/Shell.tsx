import { Link, Outlet } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";

import { connectEventStream } from "../api/events";
import { useAuth } from "./auth";
import { NavIcon, type NavIconName } from "./NavIcon";

const MAX_RAIL_WIDTH = 420;
/* Dragging this far past the point where the longest label stops fitting
   snaps the rail closed. */
const COLLAPSE_SLACK = 24;
const KEYBOARD_STEP = 16;
const FALLBACK_MIN_RAIL_WIDTH = 200;

function px(value: string): number {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

/* A block's scrollWidth is its layout width, not its text width, so measure the
   group headings with a Range. Environments without layout (jsdom) omit
   Range.getBoundingClientRect entirely. */
function textWidth(element: HTMLElement): number {
  const range = document.createRange();
  range.selectNodeContents(element);
  if (typeof range.getBoundingClientRect !== "function") return 0;
  return range.getBoundingClientRect().width;
}

/* The narrowest the rail can be while every label still fits, derived from the
   rendered text rather than hard-coded, so it tracks font and copy changes. */
function measureIntrinsicRailWidth(nav: HTMLElement): number {
  let widest = 0;
  for (const link of Array.from(nav.querySelectorAll("a"))) {
    const label = link.querySelector<HTMLElement>(".nav-label");
    const glyph = link.querySelector<HTMLElement>(".nav-icon");
    if (label === null || glyph === null) continue;
    const styles = window.getComputedStyle(link);
    widest = Math.max(
      widest,
      px(styles.paddingLeft) +
        glyph.getBoundingClientRect().width +
        px(styles.columnGap) +
        label.scrollWidth +
        px(styles.paddingRight),
    );
  }
  for (const title of Array.from(nav.querySelectorAll<HTMLElement>(".nav-group-title"))) {
    const styles = window.getComputedStyle(title);
    widest = Math.max(widest, px(styles.paddingLeft) + textWidth(title) + px(styles.paddingRight));
  }
  if (widest === 0) return FALLBACK_MIN_RAIL_WIDTH;
  const navStyles = window.getComputedStyle(nav);
  return Math.ceil(widest + px(navStyles.paddingLeft) + px(navStyles.paddingRight));
}

interface NavigationItem {
  readonly icon: NavIconName;
  readonly label: string;
  readonly to: string;
}

interface NavigationGroup {
  readonly items: readonly NavigationItem[];
  readonly title: string;
}

const navigation: readonly NavigationGroup[] = [
  {
    title: "Operate",
    items: [
      { icon: "overview", label: "Overview", to: "/" },
      { icon: "jobs", label: "Jobs", to: "/jobs" },
      { icon: "runs", label: "Runs", to: "/runs" },
    ],
  },
  {
    title: "Knowledge",
    items: [
      { icon: "knowledge", label: "Knowledge bases", to: "/knowledge-bases" },
      { icon: "sources", label: "Sources", to: "/sources" },
      { icon: "wiki", label: "Wiki", to: "/wiki" },
    ],
  },
  {
    title: "Intelligence",
    items: [
      { icon: "providers", label: "Providers", to: "/providers" },
      { icon: "models", label: "Models", to: "/models" },
    ],
  },
  {
    title: "Delivery",
    items: [
      { icon: "agents", label: "Agents", to: "/agents" },
      { icon: "discord", label: "Discord", to: "/discord" },
      { icon: "settings", label: "Settings", to: "/settings" },
    ],
  },
];

export function Shell(): ReactNode {
  const queryClient = useQueryClient();
  const { state } = useAuth();
  const [menuOpen, setMenuOpen] = useState(false);
  const [railCollapsed, setRailCollapsed] = useState(false);
  /* null means "use the stylesheet's responsive default"; a number is a width
     the operator chose by dragging. */
  const [railWidth, setRailWidth] = useState<number | null>(null);
  const [minRailWidth, setMinRailWidth] = useState(FALLBACK_MIN_RAIL_WIDTH);
  const [resizing, setResizing] = useState(false);
  const shellRef = useRef<HTMLDivElement>(null);
  const navRef = useRef<HTMLElement>(null);
  const operator = state.kind === "authenticated" ? state.session.operator.username : "—";

  useEffect(() => connectEventStream(queryClient), [queryClient]);

  useEffect(() => {
    if (!menuOpen) return undefined;
    function close(event: KeyboardEvent): void {
      if (event.key === "Escape") setMenuOpen(false);
    }
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [menuOpen]);

  /* Labels are screen-reader-only while collapsed, so only measure expanded.
     Re-measure once webfonts land, since metrics shift when Outfit swaps in. */
  useEffect(() => {
    if (railCollapsed) return undefined;
    let cancelled = false;
    function measure(): void {
      const nav = navRef.current;
      if (cancelled || nav === null) return;
      setMinRailWidth(measureIntrinsicRailWidth(nav));
    }
    measure();
    void document.fonts?.ready.then(measure);
    return () => {
      cancelled = true;
    };
  }, [railCollapsed]);

  useEffect(() => {
    if (railWidth === null) return;
    shellRef.current?.style.setProperty("--rail-w", `${railWidth}px`);
  }, [railWidth]);

  const applyWidth = useCallback((next: number): void => {
    shellRef.current?.style.setProperty("--rail-w", `${next}px`);
  }, []);

  function beginResize(event: ReactPointerEvent<HTMLDivElement>): void {
    const nav = navRef.current;
    if (nav === null || event.button !== 0) return;
    event.preventDefault();
    const originLeft = nav.getBoundingClientRect().left;
    event.currentTarget.setPointerCapture(event.pointerId);
    setResizing(true);

    /* Width and collapsed are tracked locally through the drag: the width is
       written straight to CSS so pointermove never re-renders the page, and
       only threshold crossings reach React state. */
    let width = railWidth ?? nav.getBoundingClientRect().width;
    let collapsed = railCollapsed;

    function onMove(moveEvent: PointerEvent): void {
      const reach = moveEvent.clientX - originLeft;
      const shouldCollapse = reach < minRailWidth - COLLAPSE_SLACK;
      if (shouldCollapse !== collapsed) {
        collapsed = shouldCollapse;
        setRailCollapsed(shouldCollapse);
      }
      if (shouldCollapse) return;
      width = Math.round(Math.min(Math.max(reach, minRailWidth), MAX_RAIL_WIDTH));
      applyWidth(width);
    }

    function onEnd(): void {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onEnd);
      window.removeEventListener("pointercancel", onEnd);
      setRailWidth(width);
      setResizing(false);
    }

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onEnd);
    window.addEventListener("pointercancel", onEnd);
  }

  function resizeByKey(event: ReactKeyboardEvent<HTMLDivElement>): void {
    const nav = navRef.current;
    if (nav === null) return;
    if (event.key === "Home" || event.key === "End") {
      event.preventDefault();
      setRailCollapsed(false);
      setRailWidth(event.key === "Home" ? minRailWidth : MAX_RAIL_WIDTH);
      return;
    }
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    const step = event.key === "ArrowLeft" ? -KEYBOARD_STEP : KEYBOARD_STEP;
    if (railCollapsed) {
      if (step < 0) return;
      setRailCollapsed(false);
      setRailWidth(minRailWidth);
      return;
    }
    const next = (railWidth ?? nav.getBoundingClientRect().width) + step;
    if (next < minRailWidth) {
      setRailCollapsed(true);
      return;
    }
    setRailWidth(Math.min(next, MAX_RAIL_WIDTH));
  }

  return (
    <div
      className={`folio-shell${railCollapsed ? " is-rail-collapsed" : ""}${resizing ? " is-resizing" : ""}`}
      ref={shellRef}
    >
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <header className="masthead">
        <div className="masthead-lead">
          <button
            aria-controls="primary-navigation"
            aria-expanded={menuOpen}
            className="nav-toggle"
            onClick={() => setMenuOpen((current) => !current)}
            type="button"
          >
            <span aria-hidden="true"><SidebarGlyph /></span>
            <span className="sr-only">Operations menu</span>
          </button>
          <div className="masthead-brand">
            <span aria-hidden="true" className="brand-mark"><span /></span>
            <div>
              <p className="issue-line">Control plane</p>
              <p className="wordmark">ref0</p>
            </div>
          </div>
        </div>
        <div className="operator-stamp">
          <span aria-hidden="true" className="operator-presence" />
          <strong>{operator}</strong>
        </div>
      </header>
      <div className="folio-body">
        <div
          aria-hidden="true"
          className={`nav-scrim${menuOpen ? " is-visible" : ""}`}
          onClick={() => setMenuOpen(false)}
        />
        <nav aria-label="Primary" className={`folio-nav${menuOpen ? " is-open" : ""}`} ref={navRef}>
          <div className="rail-head">
            <button
              className="rail-toggle"
              onClick={() => setRailCollapsed((current) => !current)}
              title={railCollapsed ? "Expand sidebar" : "Collapse sidebar"}
              type="button"
            >
              <span aria-hidden="true"><SidebarGlyph /></span>
              <span className="sr-only">{railCollapsed ? "Expand sidebar" : "Collapse sidebar"}</span>
            </button>
          </div>
          <div className="nav-groups" id="primary-navigation">
            {navigation.map((group) => (
              <div className="nav-group" key={group.title}>
                <p className="nav-group-title">{group.title}</p>
                <ol>
                  {group.items.map((item) => (
                    <li key={item.to}>
                      <Link
                        activeOptions={{ exact: item.to === "/" }}
                        activeProps={{ "aria-current": "page" }}
                        onClick={() => setMenuOpen(false)}
                        title={railCollapsed ? item.label : undefined}
                        to={item.to}
                      >
                        <span aria-hidden="true" className="nav-icon"><NavIcon name={item.icon} /></span>
                        <span className="nav-label">{item.label}</span>
                      </Link>
                    </li>
                  ))}
                </ol>
              </div>
            ))}
          </div>
        </nav>
        <div
          aria-label="Resize sidebar"
          aria-orientation="vertical"
          aria-valuemax={MAX_RAIL_WIDTH}
          aria-valuemin={minRailWidth}
          aria-valuenow={railCollapsed ? 0 : Math.round(railWidth ?? minRailWidth)}
          className="rail-resizer"
          onKeyDown={resizeByKey}
          onPointerDown={beginResize}
          role="separator"
          tabIndex={0}
        />
        <main id="main-content" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
      <footer>
        <span>Local operations console</span>
        <span>Durable state · explicit actions</span>
      </footer>
    </div>
  );
}

function SidebarGlyph(): ReactNode {
  return (
    <svg fill="none" focusable="false" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5" viewBox="0 0 24 24">
      <rect height="18" rx="2" width="18" x="3" y="3" />
      <path d="M9 3v18" />
    </svg>
  );
}
