import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

/**
 * A dropdown drawn by the app instead of the operating system.
 *
 * The native <select> stays in the DOM — invisible, but focused, labelled and
 * validated as before — so forms, assistive technology and constraint
 * validation keep working. Pointer and arrow keys are intercepted before the
 * platform popup can open, and the themed list below is what people see.
 */

export interface SelectOption {
  disabled?: boolean;
  label: string;
  value: string;
}

interface SelectProps {
  "aria-label"?: string;
  className?: string;
  disabled?: boolean;
  id?: string;
  name?: string;
  onChange(value: string): void;
  options: readonly SelectOption[];
  placeholder?: string;
  required?: boolean;
  title?: string;
  value: string;
}

interface MenuPlacement {
  left: number;
  maxHeight: number;
  top: number;
  up: boolean;
  width: number;
}

const MENU_GAP = 6;
const MENU_MAX_HEIGHT = 288;
const MENU_MIN_HEIGHT = 132;
const VIEWPORT_MARGIN = 8;

export function Select({
  "aria-label": ariaLabel,
  className,
  disabled,
  id,
  name,
  onChange,
  options,
  placeholder,
  required,
  title,
  value,
}: SelectProps): ReactNode {
  const menuRef = useRef<HTMLDivElement>(null);
  const nativeRef = useRef<HTMLSelectElement>(null);
  const shellRef = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<MenuPlacement | null>(null);

  const selectedIndex = options.findIndex((option) => option.value === value);
  const selected = selectedIndex === -1 ? undefined : options[selectedIndex];

  useLayoutEffect(() => {
    if (!open) return;
    setPlacement(measure(shellRef.current));
  }, [open]);

  useEffect(() => {
    if (!open) return;

    function track(): void {
      setPlacement(measure(shellRef.current));
    }

    function dismiss(event: Event): void {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (shellRef.current?.contains(target) === true) return;
      if (menuRef.current?.contains(target) === true) return;
      setOpen(false);
    }

    window.addEventListener("resize", track);
    window.addEventListener("scroll", track, true);
    document.addEventListener("pointerdown", dismiss, true);
    return () => {
      window.removeEventListener("resize", track);
      window.removeEventListener("scroll", track, true);
      document.removeEventListener("pointerdown", dismiss, true);
    };
  }, [open]);

  // Keep the chosen row in view when the list opens on a long option set.
  useEffect(() => {
    if (!open) return;
    const chosen = menuRef.current?.querySelector<HTMLElement>("[data-selected='true']");
    chosen?.scrollIntoView?.({ block: "nearest" });
  }, [open, value]);

  function commit(index: number): void {
    const option = options[index];
    if (option === undefined || option.disabled === true) return;
    if (option.value !== value) onChange(option.value);
  }

  function choose(option: SelectOption): void {
    if (option.disabled === true) return;
    setOpen(false);
    nativeRef.current?.focus();
    if (option.value !== value) onChange(option.value);
  }

  function toggle(event: ReactMouseEvent<HTMLButtonElement>): void {
    if (disabled === true) return;
    // Suppress the platform popup, then take focus back for the keyboard path.
    event.preventDefault();
    nativeRef.current?.focus();
    setOpen((current) => !current);
  }

  // A click forwarded from the wrapping <label> reaches the native control and
  // would show the platform picker; focus is all that should survive it.
  function onNativeClick(event: ReactMouseEvent<HTMLSelectElement>): void {
    event.preventDefault();
  }

  function onKeyDown(event: ReactKeyboardEvent<HTMLSelectElement>): void {
    if (disabled === true) return;
    const key = event.key;
    if (key === "Escape") {
      if (!open) return;
      event.preventDefault();
      setOpen(false);
      return;
    }
    if (key === "Tab") {
      setOpen(false);
      return;
    }
    if (key === "Enter") {
      // Closed, Enter submits the surrounding form the way a native select does.
      if (!open) return;
      event.preventDefault();
      setOpen(false);
      return;
    }
    if (key === " ") {
      event.preventDefault();
      setOpen((current) => !current);
      return;
    }
    if (key !== "ArrowDown" && key !== "ArrowUp") return;
    event.preventDefault();
    if (event.altKey) {
      setOpen(key === "ArrowDown");
      return;
    }
    setOpen(true);
    commit(step(options, selectedIndex, key === "ArrowDown" ? 1 : -1));
  }

  const menu =
    placement === null ? null : (
      <div
        aria-hidden="true"
        className={`select-menu-layer${placement.up ? " is-up" : ""}`}
        onMouseDown={(event) => event.preventDefault()}
        ref={menuRef}
        style={{ left: placement.left, top: placement.top, width: placement.width }}
      >
        <ul className="select-menu" style={{ maxHeight: placement.maxHeight }}>
          {options.map((option) => (
            <li key={option.value}>
              <span
                className="select-option"
                data-disabled={option.disabled === true ? "true" : undefined}
                data-selected={option.value === value ? "true" : undefined}
                onClick={() => choose(option)}
              >
                <span>{option.label}</span>
                <CheckGlyph />
              </span>
            </li>
          ))}
        </ul>
      </div>
    );

  return (
    <span
      className={`select${open ? " is-open" : ""}${disabled === true ? " is-disabled" : ""}${className === undefined ? "" : ` ${className}`}`}
      ref={shellRef}
    >
      <select
        aria-label={ariaLabel}
        className="select-native"
        disabled={disabled}
        id={id}
        name={name}
        onBlur={() => setOpen(false)}
        onChange={(event) => onChange(event.currentTarget.value)}
        onClick={onNativeClick}
        onKeyDown={onKeyDown}
        ref={nativeRef}
        required={required}
        value={value}
      >
        {options.map((option) => (
          <option disabled={option.disabled} key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <button aria-hidden="true" className="select-trigger" onMouseDown={toggle} tabIndex={-1} title={title} type="button">
        <span className={`select-value${selected === undefined ? " is-placeholder" : ""}`}>
          {selected?.label ?? placeholder ?? ""}
        </span>
        <CaretGlyph />
      </button>
      {open && menu !== null ? createPortal(menu, host(shellRef.current)) : null}
    </span>
  );
}

/** Nearest enabled option in a direction, stopping at the ends like a native list. */
function step(options: readonly SelectOption[], from: number, delta: number): number {
  let index = from;
  for (let hop = 0; hop < options.length; hop += 1) {
    index += delta;
    if (index < 0 || index >= options.length) return from;
    if (options[index]?.disabled !== true) return index;
  }
  return from;
}

function measure(shell: HTMLElement | null): MenuPlacement | null {
  if (shell === null) return null;
  const rect = shell.getBoundingClientRect();
  const below = window.innerHeight - rect.bottom - MENU_GAP - VIEWPORT_MARGIN;
  const above = rect.top - MENU_GAP - VIEWPORT_MARGIN;
  const up = below < MENU_MIN_HEIGHT && above > below;
  return {
    left: rect.left,
    maxHeight: Math.max(MENU_MIN_HEIGHT, Math.min(MENU_MAX_HEIGHT, up ? above : below)),
    top: up ? rect.top - MENU_GAP : rect.bottom + MENU_GAP,
    up,
    width: rect.width,
  };
}

/** Modal dialogs sit in the top layer, so the list has to render inside them. */
function host(shell: HTMLElement | null): HTMLElement {
  return shell?.closest("dialog") ?? document.body;
}

function CaretGlyph(): ReactNode {
  return (
    <svg className="select-caret" fill="none" focusable="false" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.6" viewBox="0 0 24 24">
      <path d="m6 9.5 6 6 6-6" />
    </svg>
  );
}

function CheckGlyph(): ReactNode {
  return (
    <svg className="select-check" fill="none" focusable="false" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24">
      <path d="m5 12.5 5 5 9-11" />
    </svg>
  );
}
