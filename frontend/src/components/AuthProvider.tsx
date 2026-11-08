"use client";

import { useRouter } from "next/navigation";
import { createContext, useCallback, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from "react";

import { api, AUTH_EXPIRED_EVENT } from "@/lib/api";
import { local, sessionStore } from "@/lib/local";
import type { Session, User } from "@/lib/types";

interface AuthState {
  user: User | null;
  ready: boolean; // false during server rendering and the first client render
  signIn: (s: Session) => void;
  signOut: () => void;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);
const noopSubscribe = () => () => {};

function parse(raw: string | null): Session | null {
  try {
    return raw ? (JSON.parse(raw) as Session) : null;
  } catch {
    return null;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const router = useRouter();
  const raw = useSyncExternalStore(sessionStore.subscribe, sessionStore.getSnapshot, sessionStore.getServerSnapshot);
  const ready = useSyncExternalStore(noopSubscribe, () => true, () => false);
  const user = useMemo(() => parse(raw)?.user ?? null, [raw]);

  const refresh = useCallback(async () => {
    const session = local.getSession();
    if (!session) return;
    try {
      local.setSession({ ...session, user: await api.get<User>("/users/me") });
    } catch {
      // a 401 is handled by the AUTH_EXPIRED_EVENT listener; network errors keep the cached user
    }
  }, []);

  // Once in the browser: drop an expired session, otherwise refresh the cached user.
  useEffect(() => {
    const session = local.getSession();
    if (session && new Date(session.expires_at) <= new Date()) local.setSession(null);
    else if (session) refresh();
  }, [refresh]);

  useEffect(() => {
    const onExpired = () => {
      local.setSession(null);
      router.push("/login");
    };
    window.addEventListener(AUTH_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, onExpired);
  }, [router]);

  const signIn = useCallback((s: Session) => local.setSession(s), []);
  const signOut = useCallback(() => {
    local.setSession(null);
    router.push("/login");
  }, [router]);

  return <AuthContext.Provider value={{ user, ready, signIn, signOut, refresh }}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}
