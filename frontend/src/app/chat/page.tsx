"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Empty, ErrorBox, PageTitle, Photo, Spinner } from "@/components/ui";
import { api, errorText } from "@/lib/api";
import { timeAgo } from "@/lib/format";
import type { Conversation } from "@/lib/types";

export default function ChatsPage() {
  return <RequireAuth>{() => <Chats />}</RequireAuth>;
}

function Chats() {
  const [items, setItems] = useState<Conversation[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .get<Conversation[]>("/conversations")
        .then((d) => alive && setItems(d))
        .catch((e) => alive && setError(errorText(e)));
    load();
    const t = setInterval(load, 10000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  return (
    <div className="mx-auto max-w-2xl">
      <PageTitle>Chats</PageTitle>
      <ErrorBox error={error} />
      {!items && !error && <Spinner />}
      {items?.length === 0 && <Empty title="No chats yet">Open a listing and tap “Message the seller”.</Empty>}
      <ul className="space-y-2">
        {items?.map((c) => (
          <li key={c.conversation_id}>
            <Link
              href={`/chat/${c.conversation_id}`}
              className="flex items-center gap-3 rounded-xl border border-line bg-card p-3 hover:border-brand"
            >
              <Photo src={c.listing_cover_url} alt="" className="h-12 w-12 shrink-0 rounded-lg" />
              <div className="min-w-0 flex-1">
                <p className="flex justify-between gap-2">
                  <span className="truncate font-semibold">{c.other_name}</span>
                  <span className="shrink-0 text-xs text-muted">{timeAgo(c.last_message_at ?? c.created_at)}</span>
                </p>
                <p className="truncate text-xs text-muted">{c.listing_title}</p>
                <p className={`truncate text-sm ${c.unread ? "font-semibold" : "text-muted"}`}>
                  {c.last_message ?? "No messages yet"}
                </p>
              </div>
              {c.unread > 0 && (
                <span className="rounded-full bg-brand px-2 py-0.5 text-xs font-bold text-brand-ink">{c.unread}</span>
              )}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
