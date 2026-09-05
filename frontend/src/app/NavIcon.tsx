import type { ReactNode } from "react";

/* Minimal 16px stroke icons drawn on a 24 grid, matching micro1's thin-line
   iconography: 1.5 stroke, round caps, no fills. */

export type NavIconName =
  | "agents"
  | "discord"
  | "jobs"
  | "knowledge"
  | "models"
  | "overview"
  | "providers"
  | "runs"
  | "settings"
  | "sources"
  | "wiki";

const paths: Record<NavIconName, ReactNode> = {
  agents: (
    <>
      <circle cx="12" cy="8" r="3.5" />
      <path d="M5 20a7 7 0 0 1 14 0" />
      <path d="M18.5 5.5 20 4M5.5 5.5 4 4M12 3V1" />
    </>
  ),
  overview: (
    <>
      <rect height="7" rx="1.5" width="7" x="3" y="3" />
      <rect height="7" rx="1.5" width="7" x="14" y="3" />
      <rect height="7" rx="1.5" width="7" x="3" y="14" />
      <rect height="7" rx="1.5" width="7" x="14" y="14" />
    </>
  ),
  jobs: (
    <>
      <path d="M3 7h11" />
      <path d="M3 12h7" />
      <path d="M3 17h11" />
      <circle cx="18" cy="9.5" r="3" />
    </>
  ),
  runs: (
    <>
      <path d="M12 21a9 9 0 1 1 9-9" />
      <path d="M12 7v5l3 2" />
      <path d="M21 8v4h-4" />
    </>
  ),
  knowledge: (
    <>
      <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4H11v16H5.5A1.5 1.5 0 0 1 4 18.5Z" />
      <path d="M20 5.5A1.5 1.5 0 0 0 18.5 4H13v16h5.5a1.5 1.5 0 0 0 1.5-1.5Z" />
    </>
  ),
  sources: (
    <>
      <ellipse cx="12" cy="6" rx="7" ry="3" />
      <path d="M5 6v6c0 1.66 3.13 3 7 3s7-1.34 7-3V6" />
      <path d="M5 12v6c0 1.66 3.13 3 7 3s7-1.34 7-3v-6" />
    </>
  ),
  wiki: (
    <>
      <path d="M6 3h8l5 5v13H6z" />
      <path d="M14 3v5h5" />
      <path d="M9.5 12.5h6M9.5 16h4" />
    </>
  ),
  providers: (
    <>
      <rect height="6" rx="1.5" width="18" x="3" y="4" />
      <rect height="6" rx="1.5" width="18" x="3" y="14" />
      <path d="M7 7h.01M7 17h.01" />
    </>
  ),
  models: (
    <>
      <path d="M12 3 20 7.5v9L12 21l-8-4.5v-9z" />
      <path d="m4 7.5 8 4.5 8-4.5" />
      <path d="M12 12v9" />
    </>
  ),
  discord: (
    <>
      <path d="M8.5 8.5A11 11 0 0 1 12 8a11 11 0 0 1 3.5.5" />
      <path d="M9 17.5c-1.2-.3-2.3-.8-3.2-1.5" />
      <path d="M15 17.5c1.2-.3 2.3-.8 3.2-1.5" />
      <path d="M8.7 5.5 7.4 5A15 15 0 0 0 4.3 7.4C3 10.6 2.7 13.8 3.3 17a14 14 0 0 0 4 2l1-1.7" />
      <path d="M15.3 5.5 16.6 5a15 15 0 0 1 3.1 2.4c1.3 3.2 1.6 6.4 1 9.6a14 14 0 0 1-4 2l-1-1.7" />
      <path d="M9.5 13h.01M14.5 13h.01" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 14.5a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.2a1.6 1.6 0 0 0-1-1.5 1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.2a1.6 1.6 0 0 0 1.5-1 1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.2a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.2a1.6 1.6 0 0 0-1.4 1Z" />
    </>
  ),
};

export function NavIcon({ name }: { name: NavIconName }): ReactNode {
  return (
    <svg
      aria-hidden="true"
      fill="none"
      focusable="false"
      stroke="currentColor"
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth="1.5"
      viewBox="0 0 24 24"
    >
      {paths[name]}
    </svg>
  );
}
