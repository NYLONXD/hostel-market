"use client";

import { useEffect, useState } from "react";

import { ListingCard } from "@/components/ListingCard";
import { RequireAuth } from "@/components/RequireAuth";
import { Empty, ErrorBox, LinkButton, PageTitle, Spinner } from "@/components/ui";
import { api, errorText, qs } from "@/lib/api";
import type { Listing, ListingStatus } from "@/lib/types";

const TABS: { value: ListingStatus | ""; label: string }[] = [
  { value: "", label: "All" },
  { value: "active", label: "On sale" },
  { value: "reserved", label: "Reserved" },
  { value: "sold", label: "Sold" },
  { value: "removed", label: "Removed" },
];

export default function MyListingsPage() {
  return <RequireAuth>{() => <MyListings />}</RequireAuth>;
}

function MyListings() {
  const [tab, setTab] = useState<ListingStatus | "">("");
  const [items, setItems] = useState<Listing[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .get<Listing[]>(`/users/me/listings${qs({ status: tab })}`)
      .then((d) => alive && setItems(d))
      .catch((e) => alive && setError(errorText(e)));
    return () => {
      alive = false;
    };
  }, [tab]);

  return (
    <div>
      <PageTitle action={<LinkButton href="/listings/new">+ New listing</LinkButton>}>My listings</PageTitle>
      <div className="mb-4 flex flex-wrap gap-2" role="tablist">
        {TABS.map((t) => (
          <button
            key={t.value}
            role="tab"
            aria-selected={tab === t.value}
            onClick={() => (setItems(null), setTab(t.value))}
            className={`rounded-full px-3 py-1 text-sm ${tab === t.value ? "bg-brand text-brand-ink" : "bg-card border border-line"}`}
          >
            {t.label}
          </button>
        ))}
      </div>
      <ErrorBox error={error} />
      {!items && !error && <Spinner />}
      {items?.length === 0 && <Empty title="Nothing here yet" />}
      <div className="grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 lg:grid-cols-4">
        {items?.map((l) => (
          <div key={l.listing_id} className="relative">
            <ListingCard l={l} showStatus />
            {!!l.pending_requests && (
              <span className="absolute left-2 top-2 rounded-full bg-tag px-2 py-0.5 text-xs font-bold text-tag-ink">
                {l.pending_requests} request{l.pending_requests > 1 ? "s" : ""}
              </span>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
