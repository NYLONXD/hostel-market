"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Empty, ErrorBox, OrderBadge, PageTitle, Photo, Spinner } from "@/components/ui";
import { api, errorText, qs } from "@/lib/api";
import { rupees, timeAgo } from "@/lib/format";
import type { Order } from "@/lib/types";

type Role = "buying" | "selling";

export default function OrdersPage() {
  return <RequireAuth>{() => <Orders />}</RequireAuth>;
}

function Orders() {
  const [role, setRole] = useState<Role>("buying");
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .get<Order[]>(`/orders${qs({ role })}`)
      .then((d) => alive && setOrders(d))
      .catch((e) => alive && setError(errorText(e)));
    return () => {
      alive = false;
    };
  }, [role]);

  return (
    <div>
      <PageTitle>Orders</PageTitle>
      <div className="mb-4 flex gap-2" role="tablist">
        {(["buying", "selling"] as Role[]).map((r) => (
          <button
            key={r}
            role="tab"
            aria-selected={role === r}
            onClick={() => (setOrders(null), setRole(r))}
            className={`rounded-full px-4 py-1.5 text-sm capitalize ${role === r ? "bg-brand text-brand-ink" : "border border-line bg-card"}`}
          >
            {r}
          </button>
        ))}
      </div>
      <ErrorBox error={error} />
      {!orders && !error && <Spinner />}
      {orders?.length === 0 && (
        <Empty title={role === "buying" ? "You haven't requested anything yet" : "No one has requested your items yet"} />
      )}
      <ul className="space-y-2">
        {orders?.map((o) => (
          <li key={o.order_id}>
            <Link
              href={`/orders/${o.order_id}`}
              className="flex items-center gap-3 rounded-xl border border-line bg-card p-3 hover:border-brand"
            >
              <Photo src={o.listing_cover_url} alt="" className="h-14 w-14 shrink-0 rounded-lg" />
              <div className="min-w-0 flex-1">
                <p className="truncate font-semibold">{o.listing_title}</p>
                <p className="text-xs text-muted">
                  {role === "buying" ? `from ${o.seller_name}` : `to ${o.buyer_name}`} · {timeAgo(o.created_at)}
                </p>
              </div>
              <div className="flex flex-col items-end gap-1">
                <span className="text-sm font-semibold">{rupees(o.price)}</span>
                <OrderBadge status={o.status} />
              </div>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
