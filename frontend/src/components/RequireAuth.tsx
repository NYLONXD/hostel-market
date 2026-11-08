"use client";

import { useRouter } from "next/navigation";
import { useEffect, type ReactNode } from "react";

import type { User } from "@/lib/types";

import { useAuth } from "./AuthProvider";
import { Spinner } from "./ui";

/** Renders children only for a signed-in user (optionally an admin); otherwise redirects. */
export function RequireAuth({ children, admin = false }: { children: (user: User) => ReactNode; admin?: boolean }) {
  const { user, ready } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (!ready) return;
    if (!user) router.replace("/login");
    else if (admin && user.role !== "admin") router.replace("/listings");
  }, [ready, user, admin, router]);

  if (!ready || !user || (admin && user.role !== "admin")) return <Spinner />;
  return <>{children(user)}</>;
}
