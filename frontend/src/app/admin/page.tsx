"use client";

import Link from "next/link";
import { useCallback, useEffect, useState, type FormEvent } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Button, Card, ErrorBox, Input, ListingBadge, PageTitle, Select, Spinner } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText, qs } from "@/lib/api";
import { ORDER_STATUS_LABEL, rupees, timeAgo } from "@/lib/format";
import type { AdminStats, Listing, ListingStatus, Page, User } from "@/lib/types";

export default function AdminPage() {
  return <RequireAuth admin>{() => <Admin />}</RequireAuth>;
}

function Admin() {
  const [tab, setTab] = useState<"overview" | "users" | "listings" | "setup">("overview");
  return (
    <div>
      <PageTitle>Admin</PageTitle>
      <div className="mb-6 flex flex-wrap gap-2" role="tablist">
        {(["overview", "users", "listings", "setup"] as const).map((t) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`rounded-full px-4 py-1.5 text-sm capitalize ${tab === t ? "bg-brand text-brand-ink" : "border border-line bg-card"}`}
          >
            {t}
          </button>
        ))}
      </div>
      {tab === "overview" && <Overview />}
      {tab === "users" && <Users />}
      {tab === "listings" && <Listings />}
      {tab === "setup" && <Setup />}
    </div>
  );
}

function Overview() {
  const [s, setS] = useState<AdminStats | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api.get<AdminStats>("/admin/stats").then(setS).catch((e) => setError(errorText(e)));
  }, []);
  if (error) return <ErrorBox error={error} />;
  if (!s) return <Spinner />;

  const tiles: [string, string | number][] = [
    ["Users", s.totals.users],
    ["Awaiting verification", s.totals.unverified_users],
    ["Active listings", s.totals.active_listings],
    ["Open requests", s.totals.open_requests],
    ["Completed deals", s.totals.completed_orders],
    ["Sales value", rupees(s.totals.total_sales_value)],
  ];
  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-6">
        {tiles.map(([label, value]) => (
          <Card key={label}>
            <p className="font-display text-2xl font-bold">{value}</p>
            <p className="text-xs text-muted">{label}</p>
          </Card>
        ))}
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <Table title="Hostels" head={["Hostel", "Residents", "Active listings"]}
          rows={s.hostels.map((h) => [h.hostel_name, h.residents, h.active_listings])} />
        <Table title="Orders by status" head={["Status", "Orders"]}
          rows={s.orders_by_status.map((o) => [ORDER_STATUS_LABEL[o.status], o.count])} />
        <Table title="Categories (active)" head={["Category", "Listings", "Avg price"]}
          rows={s.categories.map((c) => [c.category_name, c.active_listings, rupees(Number(c.avg_price))])} />
        <Table title="Top sellers" head={["Seller", "Sold", "Earned"]}
          rows={s.top_sellers.map((t) => [t.full_name, t.sold_listings, rupees(t.total_earned)])} />
      </div>
    </div>
  );
}

function Table({ title, head, rows }: { title: string; head: string[]; rows: (string | number)[][] }) {
  return (
    <Card>
      <h2 className="mb-2 font-display font-semibold">{title}</h2>
      {rows.length === 0 ? (
        <p className="text-sm text-muted">No data yet.</p>
      ) : (
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-muted">
              {head.map((h, i) => (
                <th key={h} className={`pb-1 font-medium ${i ? "text-right" : ""}`}>
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i} className="border-t border-line">
                {r.map((c, j) => (
                  <td key={j} className={`py-1.5 ${j ? "text-right tabular-nums" : ""}`}>
                    {c}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function Users() {
  const [filter, setFilter] = useState<"false" | "true" | "">("false");
  const [q, setQ] = useState("");
  const [data, setData] = useState<Page<User> | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .get<Page<User>>(`/admin/users${qs({ verified: filter, q })}`)
      .then(setData)
      .catch((e) => setError(errorText(e)));
  }, [filter, q]);
  useEffect(load, [load]);

  async function setVerified(u: User, verified: boolean) {
    try {
      await api.post(`/admin/users/${u.user_id}/verify`, { verified });
      load();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-2">
        <Select className="w-auto" value={filter} onChange={(e) => setFilter(e.target.value as typeof filter)} aria-label="Filter">
          <option value="false">Awaiting verification</option>
          <option value="true">Verified</option>
          <option value="">Everyone</option>
        </Select>
        <Input className="max-w-xs" type="search" placeholder="Name or email" value={q} onChange={(e) => setQ(e.target.value)} />
      </div>
      <ErrorBox error={error} />
      {!data ? (
        <Spinner />
      ) : (
        <Card className="overflow-x-auto p-0 sm:p-0">
          <table className="w-full min-w-[640px] text-sm">
            <thead>
              <tr className="text-left text-xs text-muted">
                {["Name", "Email", "Hostel / block", "Year", "Joined", ""].map((h) => (
                  <th key={h} className="px-4 py-2 font-medium">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.items.map((u) => (
                <tr key={u.user_id} className="border-t border-line">
                  <td className="px-4 py-2 font-medium">
                    {u.full_name} {u.role === "admin" && <span className="text-xs text-brand">(admin)</span>}
                  </td>
                  <td className="px-4 py-2">{u.email}</td>
                  <td className="px-4 py-2">
                    {u.hostel_name} / {u.block_name}
                  </td>
                  <td className="px-4 py-2">{u.year_of_study}</td>
                  <td className="px-4 py-2 text-muted">{timeAgo(u.created_at)}</td>
                  <td className="px-4 py-2 text-right">
                    {u.is_verified ? (
                      <Button variant="ghost" onClick={() => setVerified(u, false)}>
                        Unverify
                      </Button>
                    ) : (
                      <Button onClick={() => setVerified(u, true)}>Verify</Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.items.length === 0 && <p className="p-4 text-sm text-muted">No users.</p>}
        </Card>
      )}
    </div>
  );
}

function Listings() {
  const [status, setStatus] = useState<ListingStatus | "">("active");
  const [data, setData] = useState<Page<Listing> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [removing, setRemoving] = useState<number | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(() => {
    api
      .get<Page<Listing>>(`/admin/listings${qs({ status })}`)
      .then(setData)
      .catch((e) => setError(errorText(e)));
  }, [status]);
  useEffect(load, [load]);

  async function remove(id: number) {
    try {
      await api.post(`/admin/listings/${id}/remove`, reason.trim() ? { reason: reason.trim() } : undefined);
      setRemoving(null);
      setReason("");
      load();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <div className="space-y-3">
      <Select className="w-auto" value={status} onChange={(e) => setStatus(e.target.value as ListingStatus | "")} aria-label="Status">
        <option value="">All</option>
        <option value="active">On sale</option>
        <option value="reserved">Reserved</option>
        <option value="sold">Sold</option>
        <option value="removed">Removed</option>
      </Select>
      <ErrorBox error={error} />
      {!data ? (
        <Spinner />
      ) : (
        <ul className="space-y-2">
          {data.items.map((l) => (
            <li key={l.listing_id} className="rounded-xl border border-line bg-card p-3 text-sm">
              <div className="flex flex-wrap items-center gap-3">
                <Link href={`/listings/${l.listing_id}`} className="min-w-0 flex-1 truncate font-semibold hover:text-brand">
                  {l.title}
                </Link>
                <span className="text-muted">
                  {l.seller_name} · {l.hostel_name}
                </span>
                <span>{rupees(l.price)}</span>
                <ListingBadge status={l.status} />
                {l.status !== "removed" && removing !== l.listing_id && (
                  <Button variant="danger" onClick={() => setRemoving(l.listing_id)}>
                    Remove
                  </Button>
                )}
              </div>
              {removing === l.listing_id && (
                <div className="mt-2 flex flex-wrap gap-2">
                  <Input className="max-w-sm" placeholder="Reason (shown to buyers)" value={reason} onChange={(e) => setReason(e.target.value)} />
                  <Button variant="danger" onClick={() => remove(l.listing_id)}>
                    Confirm removal
                  </Button>
                  <Button variant="ghost" onClick={() => setRemoving(null)}>
                    Cancel
                  </Button>
                </div>
              )}
            </li>
          ))}
          {data.items.length === 0 && <p className="text-sm text-muted">No listings.</p>}
        </ul>
      )}
    </div>
  );
}

function Setup() {
  const { hostels, categories } = useMeta();
  const [msg, setMsg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [hostel, setHostel] = useState("");
  const [block, setBlock] = useState({ hostel_id: "", name: "" });
  const [cat, setCat] = useState({ slug: "", name: "" });

  const submit = (path: string, body: Record<string, unknown>, done: string, reset: () => void) => async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    setMsg(null);
    try {
      await api.post(path, body);
      setMsg(done);
      reset();
    } catch (err) {
      setError(errorText(err));
    }
  };

  return (
    <div className="grid gap-4 md:grid-cols-3">
      <Card>
        <h2 className="mb-3 font-display font-semibold">Add hostel</h2>
        <form onSubmit={submit("/admin/hostels", { name: hostel }, "Hostel added.", () => setHostel(""))} className="space-y-2">
          <Input required placeholder="Hostel name" value={hostel} onChange={(e) => setHostel(e.target.value)} />
          <Button type="submit">Add</Button>
        </form>
        <p className="mt-3 text-xs text-muted">{hostels.map((h) => h.name).join(", ")}</p>
      </Card>
      <Card>
        <h2 className="mb-3 font-display font-semibold">Add block</h2>
        <form
          onSubmit={submit(`/admin/hostels/${block.hostel_id}/blocks`, { name: block.name }, "Block added.", () =>
            setBlock({ ...block, name: "" }),
          )}
          className="space-y-2"
        >
          <Select required value={block.hostel_id} onChange={(e) => setBlock({ ...block, hostel_id: e.target.value })}>
            <option value="">Hostel…</option>
            {hostels.map((h) => (
              <option key={h.hostel_id} value={h.hostel_id}>
                {h.name}
              </option>
            ))}
          </Select>
          <Input required maxLength={20} placeholder="Block name, e.g. D" value={block.name} onChange={(e) => setBlock({ ...block, name: e.target.value })} />
          <Button type="submit">Add</Button>
        </form>
      </Card>
      <Card>
        <h2 className="mb-3 font-display font-semibold">Add category</h2>
        <form onSubmit={submit("/admin/categories", cat, "Category added.", () => setCat({ slug: "", name: "" }))} className="space-y-2">
          <Input required placeholder="Name, e.g. Musical instruments" value={cat.name} onChange={(e) => setCat({ ...cat, name: e.target.value })} />
          <Input required pattern="[a-z_]+" placeholder="slug, e.g. music" value={cat.slug} onChange={(e) => setCat({ ...cat, slug: e.target.value })} />
          <Button type="submit">Add</Button>
        </form>
        <p className="mt-3 text-xs text-muted">{categories.map((c) => c.name).join(", ")}</p>
      </Card>
      <div className="md:col-span-3">
        <ErrorBox error={error} />
        {msg && <p className="text-sm text-ok">{msg} Refresh to see it in the lists.</p>}
      </div>
    </div>
  );
}
