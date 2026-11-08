"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

import { ListingCard } from "@/components/ListingCard";
import { RequireAuth } from "@/components/RequireAuth";
import { Button, Empty, ErrorBox, Input, LinkButton, Notice, Photo, Select, Spinner } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText, qs } from "@/lib/api";
import { CONDITIONS, rupees } from "@/lib/format";
import { local, type RecentItem } from "@/lib/local";
import type { Listing, Page, User } from "@/lib/types";

interface Filters {
  q: string;
  category: string;
  condition: string;
  min_price: string;
  max_price: string;
  sort: "newest" | "price_asc" | "price_desc";
}

const EMPTY: Filters = { q: "", category: "", condition: "", min_price: "", max_price: "", sort: "newest" };
const LIMIT = 12;

export default function FeedPage() {
  return <RequireAuth>{(user) => <Feed user={user} />}</RequireAuth>;
}

function Feed({ user }: { user: User }) {
  const { categories } = useMeta();
  // Filters are remembered in this browser between visits.
  const [filters, setFilters] = useState<Filters>(() => ({ ...EMPTY, ...(local.getFilters<Filters>() ?? {}) }));
  const [search, setSearch] = useState(filters.q);
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Page<Listing> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [recent] = useState<RecentItem[]>(() => local.getRecent());

  useEffect(() => {
    local.setFilters(filters);
  }, [filters]);

  // Debounce the search box.
  useEffect(() => {
    const t = setTimeout(() => {
      setFilters((f) => (f.q === search ? f : { ...f, q: search }));
      setPage(1);
    }, 350);
    return () => clearTimeout(t);
  }, [search]);

  useEffect(() => {
    let alive = true;
    api
      .get<Page<Listing>>(`/listings${qs({ ...filters, page, limit: LIMIT })}`)
      .then((d) => {
        if (alive) {
          setData(d);
          setError(null);
        }
      })
      .catch((e) => alive && setError(errorText(e)));
    return () => {
      alive = false;
    };
  }, [filters, page]);

  const update = (patch: Partial<Filters>) => {
    setFilters((f) => ({ ...f, ...patch }));
    setPage(1);
  };
  const pages = data ? Math.max(1, Math.ceil(data.total / LIMIT)) : 1;
  const filtered = JSON.stringify({ ...filters, sort: "newest" }) !== JSON.stringify(EMPTY);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <p className="text-sm text-muted">Listings in</p>
          <h1 className="font-display text-3xl font-bold">{user.hostel_name}</h1>
        </div>
        <LinkButton href="/listings/new">+ Sell something</LinkButton>
      </div>

      {!user.is_verified && (
        <Notice tone="warn">
          Your account isn&apos;t verified yet. You can browse, but buying, selling and chatting unlock once a hostel
          admin verifies you.
        </Notice>
      )}

      <div className="grid gap-2 rounded-xl border border-line bg-card p-3 sm:grid-cols-2 lg:grid-cols-6">
        <Input
          className="lg:col-span-2"
          type="search"
          placeholder="Search books, cycles, kettles…"
          aria-label="Search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Select aria-label="Category" value={filters.category} onChange={(e) => update({ category: e.target.value })}>
          <option value="">All categories</option>
          {categories.map((c) => (
            <option key={c.slug} value={c.slug}>
              {c.name}
            </option>
          ))}
        </Select>
        <Select aria-label="Condition" value={filters.condition} onChange={(e) => update({ condition: e.target.value })}>
          <option value="">Any condition</option>
          {CONDITIONS.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </Select>
        <div className="flex gap-2">
          <Input
            type="number"
            min={0}
            placeholder="Min ₹"
            aria-label="Minimum price"
            value={filters.min_price}
            onChange={(e) => update({ min_price: e.target.value })}
          />
          <Input
            type="number"
            min={0}
            placeholder="Max ₹"
            aria-label="Maximum price"
            value={filters.max_price}
            onChange={(e) => update({ max_price: e.target.value })}
          />
        </div>
        <Select aria-label="Sort" value={filters.sort} onChange={(e) => update({ sort: e.target.value as Filters["sort"] })}>
          <option value="newest">Newest first</option>
          <option value="price_asc">Price: low to high</option>
          <option value="price_desc">Price: high to low</option>
        </Select>
      </div>

      {recent.length > 0 && !filtered && page === 1 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold text-muted">Recently viewed</h2>
          <div className="flex gap-3 overflow-x-auto pb-2">
            {recent.map((r) => (
              <Link
                key={r.listing_id}
                href={`/listings/${r.listing_id}`}
                className="flex w-56 shrink-0 items-center gap-3 rounded-lg border border-line bg-card p-2 hover:border-brand"
              >
                <Photo src={r.cover_url} alt={r.title} className="h-12 w-12 shrink-0 rounded" />
                <span className="min-w-0">
                  <span className="block truncate text-sm font-medium">{r.title}</span>
                  <span className="text-xs text-muted">{rupees(r.price)}</span>
                </span>
              </Link>
            ))}
          </div>
        </section>
      )}

      <ErrorBox error={error} />
      {!data && !error && <Spinner />}
      {data && data.items.length === 0 && (
        <Empty title={filtered ? "Nothing matches those filters" : "No listings yet"}>
          {filtered ? (
            <Button variant="ghost" onClick={() => (setSearch(""), update(EMPTY))}>
              Clear filters
            </Button>
          ) : (
            "Be the first to sell something in your hostel."
          )}
        </Empty>
      )}
      {data && data.items.length > 0 && (
        <>
          <p className="text-sm text-muted">{data.total} item(s)</p>
          <div className="grid grid-cols-2 gap-3 sm:gap-4 md:grid-cols-3 lg:grid-cols-4">
            {data.items.map((l) => (
              <ListingCard key={l.listing_id} l={l} />
            ))}
          </div>
          {pages > 1 && (
            <div className="flex items-center justify-center gap-3">
              <Button variant="secondary" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
                Previous
              </Button>
              <span className="text-sm text-muted">
                Page {page} of {pages}
              </span>
              <Button variant="secondary" disabled={page >= pages} onClick={() => setPage((p) => p + 1)}>
                Next
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
