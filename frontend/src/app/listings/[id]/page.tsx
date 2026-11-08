"use client";

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Button, Card, ErrorBox, LinkButton, ListingBadge, Notice, OrderBadge, Photo, Spinner, Textarea } from "@/components/ui";
import { api, errorText, qs } from "@/lib/api";
import { conditionLabel, hhmm, rupees, timeAgo, yearLabel } from "@/lib/format";
import { local } from "@/lib/local";
import type { Conversation, ListingDetail, Order, User } from "@/lib/types";

export default function ListingPage() {
  return <RequireAuth>{(user) => <ListingView user={user} />}</RequireAuth>;
}

function ListingView({ user }: { user: User }) {
  const { id } = useParams<{ id: string }>();
  const [l, setL] = useState<ListingDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [active, setActive] = useState(0);

  const load = useCallback(() => {
    api
      .get<ListingDetail>(`/listings/${id}`)
      .then((d) => {
        setL(d);
        if (d.status !== "removed") local.addRecent(d);
      })
      .catch((e) => setError(errorText(e)));
  }, [id]);

  useEffect(load, [load]);

  if (error) return <ErrorBox error={error} />;
  if (!l) return <Spinner />;

  const rules = [
    l.allowed_block && `Only block ${l.allowed_block} residents`,
    l.allowed_year && `Only ${yearLabel(l.allowed_year)} students`,
    l.available_from && `Requests between ${hhmm(l.available_from)} and ${hhmm(l.available_until)} IST`,
  ].filter(Boolean) as string[];

  return (
    <div className="grid gap-6 lg:grid-cols-[1.3fr_1fr]">
      <div className="space-y-3">
        <Photo src={l.photos[active]?.url ?? null} alt={l.title} className="aspect-[4/3] w-full rounded-xl border border-line" />
        {l.photos.length > 1 && (
          <div className="flex gap-2">
            {l.photos.map((p, i) => (
              <button
                key={p.photo_id}
                onClick={() => setActive(i)}
                className={`overflow-hidden rounded-lg border-2 ${i === active ? "border-brand" : "border-transparent"}`}
                aria-label={`Show photo ${i + 1}`}
              >
                <Photo src={p.url} alt="" className="h-16 w-16" />
              </button>
            ))}
          </div>
        )}
        <Card>
          <h2 className="mb-2 font-display text-lg font-semibold">Description</h2>
          <p className="whitespace-pre-line text-sm">{l.description || "No description."}</p>
        </Card>
      </div>

      <div className="space-y-4">
        <Card className="space-y-3">
          <div className="flex items-start justify-between gap-3">
            <h1 className="font-display text-2xl font-bold">{l.title}</h1>
            <ListingBadge status={l.status} />
          </div>
          <span className="price-tag inline-block text-xl">{rupees(l.price)}</span>
          <dl className="grid grid-cols-2 gap-2 text-sm">
            <dt className="text-muted">Category</dt>
            <dd>{l.category_name}</dd>
            <dt className="text-muted">Condition</dt>
            <dd>{conditionLabel(l.condition)}</dd>
            <dt className="text-muted">Seller</dt>
            <dd>
              {l.seller_name} · Block {l.seller_block}
            </dd>
            <dt className="text-muted">Listed</dt>
            <dd>{timeAgo(l.created_at)}</dd>
          </dl>
          {rules.length > 0 && (
            <ul className="space-y-1 rounded-lg bg-warn-soft p-3 text-sm">
              {rules.map((r) => (
                <li key={r}>• {r}</li>
              ))}
            </ul>
          )}
        </Card>

        {l.viewer.is_seller ? <SellerPanel l={l} reload={load} /> : <BuyerPanel l={l} user={user} reload={load} />}
      </div>
    </div>
  );
}

function BuyerPanel({ l, user, reload }: { l: ListingDetail; user: User; reload: () => void }) {
  const router = useRouter();
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const v = l.viewer;
  const openOrder = v.my_order && !["rejected", "cancelled", "expired"].includes(v.my_order.status) ? v.my_order : null;

  async function request() {
    setBusy(true);
    setError(null);
    try {
      const o = await api.post<Order>(`/listings/${l.listing_id}/orders`, note.trim() ? { note: note.trim() } : undefined);
      router.push(`/orders/${o.order_id}`);
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
      reload();
    }
  }

  async function chat() {
    setError(null);
    try {
      const c = await api.post<Conversation>(`/listings/${l.listing_id}/conversations`);
      router.push(`/chat/${c.conversation_id}`);
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <Card className="space-y-3">
      {openOrder ? (
        <Notice tone="ok">
          You have an order for this item: <OrderBadge status={openOrder.status} />{" "}
          <Link href={`/orders/${openOrder.order_id}`} className="font-semibold underline">
            Open order
          </Link>
        </Notice>
      ) : v.can_buy ? (
        <>
          <h2 className="font-display text-lg font-semibold">Want it?</h2>
          <Textarea
            rows={2}
            maxLength={500}
            placeholder="Add a note for the seller (optional), e.g. when you can collect it"
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <Button onClick={request} disabled={busy} className="w-full">
            {busy ? "Sending…" : `Request to buy for ${rupees(l.price)}`}
          </Button>
          <p className="text-xs text-muted">The seller has 48 hours to accept. You pay by UPI or cash when you meet.</p>
        </>
      ) : (
        <Notice tone="warn">{v.block_message ?? "You can't buy this item."}</Notice>
      )}
      {user.is_verified && l.status !== "removed" && (
        <Button variant="secondary" onClick={chat} className="w-full">
          {v.conversation_id ? "Open chat with seller" : "Message the seller"}
        </Button>
      )}
      <ErrorBox error={error} />
    </Card>
  );
}

function SellerPanel({ l, reload }: { l: ListingDetail; reload: () => void }) {
  const router = useRouter();
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  const loadOrders = useCallback(() => {
    api
      .get<Order[]>(`/orders${qs({ role: "selling", listing_id: l.listing_id })}`)
      .then(setOrders)
      .catch((e) => setError(errorText(e)));
  }, [l.listing_id]);

  useEffect(loadOrders, [loadOrders]);

  async function act(orderId: number, action: "accept" | "reject") {
    setError(null);
    try {
      await api.post(`/orders/${orderId}/${action}`);
      loadOrders();
      reload();
    } catch (e) {
      setError(errorText(e));
    }
  }

  async function remove() {
    try {
      await api.del(`/listings/${l.listing_id}`);
      router.push("/my-listings");
    } catch (e) {
      setError(errorText(e));
      setConfirmRemove(false);
    }
  }

  const live = orders?.find((o) => ["accepted", "awaiting_payment", "paid", "handed_over"].includes(o.status));
  const waiting = orders?.filter((o) => o.status === "requested") ?? [];

  return (
    <Card className="space-y-4">
      <div className="flex flex-wrap gap-2">
        {l.status !== "removed" && (
          <LinkButton href={`/listings/${l.listing_id}/edit`} variant="secondary">
            Edit / photos
          </LinkButton>
        )}
        {(l.status === "active" || l.status === "sold") &&
          (confirmRemove ? (
            <>
              <Button variant="danger" onClick={remove}>
                Yes, remove it
              </Button>
              <Button variant="ghost" onClick={() => setConfirmRemove(false)}>
                Keep
              </Button>
            </>
          ) : (
            <Button variant="danger" onClick={() => setConfirmRemove(true)}>
              Remove listing
            </Button>
          ))}
      </div>

      {live && (
        <Notice tone="ok">
          Reserved for {live.buyer_name}: <OrderBadge status={live.status} />{" "}
          <Link href={`/orders/${live.order_id}`} className="font-semibold underline">
            Open order
          </Link>
        </Notice>
      )}

      <div>
        <h2 className="mb-2 font-display text-lg font-semibold">Buy requests ({waiting.length})</h2>
        {!orders && <Spinner />}
        {orders && waiting.length === 0 && <p className="text-sm text-muted">No pending requests.</p>}
        <ul className="space-y-2">
          {waiting.map((o) => (
            <li key={o.order_id} className="rounded-lg border border-line p-3 text-sm">
              <div className="flex items-center justify-between gap-2">
                <span className="font-semibold">{o.buyer_name}</span>
                <span className="text-xs text-muted">{timeAgo(o.created_at)}</span>
              </div>
              {o.buyer_note && <p className="mt-1 text-muted">“{o.buyer_note}”</p>}
              <div className="mt-2 flex gap-2">
                <Button onClick={() => act(o.order_id, "accept")} disabled={!!live}>
                  Accept
                </Button>
                <Button variant="secondary" onClick={() => act(o.order_id, "reject")}>
                  Reject
                </Button>
              </div>
            </li>
          ))}
        </ul>
        {live && waiting.length > 0 && (
          <p className="mt-2 text-xs text-muted">Others stay on the waitlist in case the current order is cancelled.</p>
        )}
      </div>
      <ErrorBox error={error} />
    </Card>
  );
}
