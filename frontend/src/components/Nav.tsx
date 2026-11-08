"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";

import { api } from "@/lib/api";

import { useAuth } from "./AuthProvider";

const POLL_MS = 15000;

export function Nav() {
  const { user, signOut } = useAuth();
  const pathname = usePathname();
  const [unread, setUnread] = useState(0);
  // The mobile menu is open only on the page where it was opened, so navigating closes it.
  const [openOn, setOpenOn] = useState<string | null>(null);
  const open = openOn === pathname;
  const setOpen = (fn: (o: boolean) => boolean) => setOpenOn(fn(open) ? pathname : null);

  useEffect(() => {
    if (!user) return;
    let alive = true;
    const load = () =>
      api
        .get<{ unread: number }>("/conversations/unread-count")
        .then((r) => alive && setUnread(r.unread))
        .catch(() => {});
    load();
    const t = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [user, pathname]);

  const links = user
    ? [
        { href: "/listings", label: "Browse" },
        { href: "/listings/new", label: "Sell" },
        { href: "/my-listings", label: "My listings" },
        { href: "/orders", label: "Orders" },
        { href: "/chat", label: unread ? `Chats (${unread})` : "Chats" },
        ...(user.role === "admin" ? [{ href: "/admin", label: "Admin" }] : []),
      ]
    : [];

  const linkClass = (href: string) =>
    `rounded-md px-3 py-2 text-sm font-medium ${
      pathname === href || (href !== "/listings" && pathname.startsWith(href + "/"))
        ? "bg-brand-soft text-ink"
        : "text-muted hover:text-ink"
    }`;

  return (
    <header className="sticky top-0 z-20 border-b border-line bg-paper/90 backdrop-blur">
      <nav className="mx-auto flex max-w-6xl items-center gap-2 px-4 py-3">
        <Link href={user ? "/listings" : "/"} className="mr-auto flex items-center gap-2 font-display text-lg font-bold">
          <span className="price-tag text-sm">₹</span> Hostel Market
        </Link>

        {user ? (
          <>
            <div className="hidden items-center gap-1 md:flex">
              {links.map((l) => (
                <Link key={l.href} href={l.href} className={linkClass(l.href)}>
                  {l.label}
                </Link>
              ))}
              <Link href="/profile" className={linkClass("/profile")}>
                {user.full_name.split(" ")[0]}
              </Link>
              <button onClick={signOut} className="px-2 text-sm text-muted hover:text-ink">
                Sign out
              </button>
            </div>
            <button
              className="rounded-md border border-line px-3 py-1.5 text-sm md:hidden"
              aria-expanded={open}
              onClick={() => setOpen((o) => !o)}
            >
              Menu{unread ? ` (${unread})` : ""}
            </button>
          </>
        ) : (
          <div className="flex gap-2">
            <Link href="/login" className="rounded-md px-3 py-2 text-sm font-medium text-muted hover:text-ink">
              Sign in
            </Link>
            <Link href="/register" className="rounded-lg bg-brand px-3 py-2 text-sm font-semibold text-brand-ink">
              Join
            </Link>
          </div>
        )}
      </nav>

      {user && open && (
        <div className="flex flex-col border-t border-line px-4 py-2 md:hidden">
          {[...links, { href: "/profile", label: "Profile" }].map((l) => (
            <Link key={l.href} href={l.href} className={linkClass(l.href)}>
              {l.label}
            </Link>
          ))}
          <button onClick={signOut} className="px-3 py-2 text-left text-sm text-muted">
            Sign out
          </button>
        </div>
      )}
    </header>
  );
}
