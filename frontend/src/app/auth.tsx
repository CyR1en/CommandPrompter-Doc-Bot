import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";

import {
  bootstrap as bootstrapRequest,
  currentSession,
  installUnauthorizedHandler,
  login as loginRequest,
  logout as logoutRequest,
  type AuthSession,
} from "../api/client";
import { clearEventStreamCursor } from "../api/events";

type AuthState =
  | { kind: "checking" }
  | { kind: "anonymous" }
  | { kind: "authenticated"; session: AuthSession };

interface AuthContextValue {
  bootstrap(username: string, password: string, token: string): Promise<void>;
  login(username: string, password: string): Promise<void>;
  logout(): Promise<void>;
  state: AuthState;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }): ReactNode {
  const queryClient = useQueryClient();
  const [state, setState] = useState<AuthState>({ kind: "checking" });

  useEffect(
    () => installUnauthorizedHandler(() => {
      clearEventStreamCursor();
      queryClient.clear();
      setState({ kind: "anonymous" });
    }),
    [queryClient],
  );

  useEffect(() => {
    let current = true;
    currentSession()
      .then((session) => {
        if (current) setState({ kind: "authenticated", session });
      })
      .catch(() => {
        if (current) setState({ kind: "anonymous" });
      });
    return () => {
      current = false;
    };
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      async bootstrap(username, password, token) {
        const session = await bootstrapRequest(username, password, token);
        clearEventStreamCursor();
        queryClient.clear();
        setState({ kind: "authenticated", session });
      },
      async login(username, password) {
        const session = await loginRequest(username, password);
        clearEventStreamCursor();
        queryClient.clear();
        setState({ kind: "authenticated", session });
      },
      async logout() {
        if (state.kind === "authenticated") {
          await logoutRequest(state.session.csrf_token);
        }
        clearEventStreamCursor();
        queryClient.clear();
        setState({ kind: "anonymous" });
      },
      state,
    }),
    [queryClient, state],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (context === undefined) throw new Error("AuthProvider is required");
  return context;
}

export function useCsrfToken(): string {
  const { state } = useAuth();
  if (state.kind !== "authenticated") throw new Error("Authentication is required");
  return state.session.csrf_token;
}
