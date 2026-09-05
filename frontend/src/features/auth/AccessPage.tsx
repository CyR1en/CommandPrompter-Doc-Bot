import { Link, Navigate } from "@tanstack/react-router";
import { useState, type FormEvent, type ReactNode } from "react";

import { safeErrorMessage } from "../../api/client";
import { ErrorNotice } from "../../app/StatusBadge";
import { useAuth } from "../../app/auth";

export function LoginPage(): ReactNode {
  return <AccessPage mode="login" />;
}

export function BootstrapPage(): ReactNode {
  return <AccessPage mode="bootstrap" />;
}

function AccessPage({ mode }: { mode: "bootstrap" | "login" }): ReactNode {
  const auth = useAuth();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [bootstrapToken, setBootstrapToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  if (auth.state.kind === "authenticated") return <Navigate to="/" />;

  async function submit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (mode === "bootstrap") {
        await auth.bootstrap(username, password, bootstrapToken);
        setBootstrapToken("");
      } else {
        await auth.login(username, password);
      }
      setPassword("");
    } catch (caught: unknown) {
      setError(safeErrorMessage(caught));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <main className="access-page" id="main-content">
      <section className="access-intro">
        <p className="issue-line">ref0 · private control plane</p>
        <h1>{mode === "login" ? "Welcome back." : "Set up the control plane."}</h1>
        <p>
          {mode === "login"
            ? "Sign in to inspect durable operations and make deliberate changes."
            : "Create the one operator account. Bootstrap closes permanently after setup."}
        </p>
      </section>
      <section aria-labelledby="access-form-title" className="access-card">
        <p className="eyebrow">{mode === "login" ? "Operator access" : "First-run setup"}</p>
        <h2 id="access-form-title">{mode === "login" ? "Sign in" : "Create operator"}</h2>
        <form onSubmit={(event) => void submit(event)}>
          <label>
            Username
            <input
              autoComplete="username"
              maxLength={255}
              onChange={(event) => setUsername(event.currentTarget.value)}
              required
              value={username}
            />
          </label>
          <label>
            Password
            <input
              autoComplete={mode === "login" ? "current-password" : "new-password"}
              onChange={(event) => setPassword(event.currentTarget.value)}
              required
              type="password"
              value={password}
            />
          </label>
          {mode === "bootstrap" ? (
            <label>
              Bootstrap token
              <input
                autoComplete="off"
                onChange={(event) => setBootstrapToken(event.currentTarget.value)}
                required
                type="password"
                value={bootstrapToken}
              />
            </label>
          ) : null}
          <ErrorNotice message={error} />
          <button className="button primary" disabled={submitting} type="submit">
            {submitting ? "Checking…" : mode === "login" ? "Sign in" : "Create operator"}
          </button>
        </form>
        <p className="alternate-access">
          {mode === "login" ? "Fresh installation?" : "Already initialized?"}{" "}
          <Link to={mode === "login" ? "/bootstrap" : "/login"}>
            {mode === "login" ? "First-run setup" : "Sign in"}
          </Link>
        </p>
      </section>
    </main>
  );
}
