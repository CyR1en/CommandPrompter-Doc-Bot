import type { ReactNode } from "react";

export function StatusBadge({ value }: { value: string }): ReactNode {
  const normalized = value.replaceAll("_", " ");
  return (
    <span className={`status status-${value}`}>
      <span aria-hidden="true" className="status-mark" />
      {normalized}
    </span>
  );
}

export function EmptyState({ children }: { children: ReactNode }): ReactNode {
  return <p className="empty-state">{children}</p>;
}

export function ErrorNotice({ message }: { message: string | null }): ReactNode {
  return message === null ? null : (
    <p className="notice error" role="alert">
      {message}
    </p>
  );
}
