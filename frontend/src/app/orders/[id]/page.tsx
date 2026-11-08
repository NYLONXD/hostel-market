"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useEffect, useState } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Button, Card, ErrorBox, Input, Notice, OrderBadge, Photo, Spinner } from "@/components/ui";
import { api, errorText } from "@/lib/api";
import { dateTime, ORDER_STATUS_LABEL, rupees } from "@/lib/format";
import type { Order, OrderAction } from "@/lib/types";

export default function OrderPage() {
  return <RequireAuth>{() => <OrderView />}</RequireAuth>;
}

interface Step {
  action: OrderAction;
  label: string;
  variant?: "primary" | "secondary" | "danger";
  method?: "upi" | "cash";
  needsReason?: boolean;
}

/** What the current user can do next, mirroring fn_transition_order in the database. */
function nextSteps(o: Order): { steps: Step[]; hint: string } {
  const buyer = o.my_role === "buyer";
  const cancel: Step = { action: "cancel", label: "Cancel order", variant: "danger", needsReason: true };
  switch (o.status) {
    case "requested":
      return buyer
        ? { steps: [{ ...cancel, label: "Withdraw request" }], hint: "Waiting for the seller to accept your request." }
        : {
            steps: [
              { action: "accept", label: "Accept request" },
              { action: "reject", label: "Reject", variant: "secondary", needsReason: true },
            ],
            hint: "Accepting reserves the item for this buyer.",
          };
    case "accepted":
      return buyer
        ? {
            steps: [
              { action: "choose_payment", label: "I'll pay by UPI", method: "upi" },
              { action: "choose_payment", label: "I'll pay cash", method: "cash", variant: "secondary" },
              cancel,
            ],
            hint: "The seller accepted! Choose how you will pay.",
          }
        : { steps: [cancel], hint: "Waiting for the buyer to choose a payment method." };
    case "awaiting_payment":
      return buyer
        ? {
            steps: [cancel],
            hint:
              o.payment_method === "upi"
                ? "Pay the seller by UPI, then tell them. They will confirm once the money arrives."
                : "Pay the seller in cash when you meet. They will confirm the payment.",
          }
        : {
            steps: [{ action: "confirm_payment", label: "I received the payment" }, cancel],
            hint: `The buyer will pay ${o.payment_method === "upi" ? "to your UPI ID" : "in cash"}. Confirm once you have it.`,
          };
    case "paid":
      return buyer
        ? { steps: [], hint: "Payment confirmed. Collect the item from the seller." }
        : { steps: [{ action: "handover", label: "I handed it over" }], hint: "Hand the item to the buyer." };
    case "handed_over":
      return buyer
        ? { steps: [{ action: "complete", label: "I received the item" }], hint: "Confirm you got the item to finish." }
        : { steps: [], hint: "Waiting for the buyer to confirm they received it." };
    case "completed":
      return { steps: [], hint: "This deal is done. 🎉" };
    default:
      return { steps: [], hint: o.close_reason ? `Closed: ${o.close_reason}` : "This order is closed." };
  }
}

function OrderView() {
  const { id } = useParams<{ id: string }>();
  const [o, setO] = useState<Order | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [reasonFor, setReasonFor] = useState<Step | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(() => {
    api
      .get<Order>(`/orders/${id}`)
      .then(setO)
      .catch((e) => setError(errorText(e)));
  }, [id]);

  useEffect(load, [load]);

  async function run(step: Step, why?: string) {
    setBusy(true);
    setError(null);
    try {
      const body: Record<string, string> = {};
      if (step.method) body.payment_method = step.method;
      if (why?.trim()) body.reason = why.trim();
      setO(await api.post<Order>(`/orders/${id}/${step.action}`, body));
      setReasonFor(null);
      setReason("");
    } catch (e) {
      setError(errorText(e));
      load();
    } finally {
      setBusy(false);
    }
  }

  if (!o) return error ? <ErrorBox error={error} /> : <Spinner />;
  const { steps, hint } = nextSteps(o);
  const upiLink =
    o.seller_upi_id && `upi://pay?pa=${encodeURIComponent(o.seller_upi_id)}&pn=${encodeURIComponent(o.seller_name)}&am=${o.price}&cu=INR&tn=${encodeURIComponent(o.listing_title)}`;

  return (
    <div className="mx-auto grid max-w-4xl gap-6 md:grid-cols-[1fr_280px]">
      <div className="space-y-4">
        <Card className="flex items-center gap-4">
          <Photo src={o.listing_cover_url} alt="" className="h-20 w-20 shrink-0 rounded-lg" />
          <div className="min-w-0 flex-1">
            <p className="text-xs text-muted">Order #{o.order_id}</p>
            <Link href={`/listings/${o.listing_id}`} className="block truncate font-display text-xl font-bold hover:text-brand">
              {o.listing_title}
            </Link>
            <p className="text-sm text-muted">
              {o.my_role === "buyer" ? `Seller: ${o.seller_name}` : `Buyer: ${o.buyer_name}`}
            </p>
          </div>
          <div className="text-right">
            <span className="price-tag inline-block">{rupees(o.price)}</span>
            <div className="mt-2">
              <OrderBadge status={o.status} />
            </div>
          </div>
        </Card>

        <Card className="space-y-3">
          <p className="text-sm">{hint}</p>
          {o.my_role === "buyer" && o.status === "awaiting_payment" && o.payment_method === "upi" && o.seller_upi_id && (
            <Notice>
              Pay <strong>{rupees(o.price)}</strong> to <strong className="font-mono">{o.seller_upi_id}</strong>{" "}
              {upiLink && (
                <a href={upiLink} className="ml-1 font-semibold underline">
                  Open UPI app
                </a>
              )}
            </Notice>
          )}
          {o.buyer_note && <p className="text-sm text-muted">Buyer&apos;s note: “{o.buyer_note}”</p>}

          {reasonFor ? (
            <div className="space-y-2">
              <Input
                autoFocus
                maxLength={200}
                placeholder="Reason (optional)"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
              <div className="flex gap-2">
                <Button variant="danger" disabled={busy} onClick={() => run(reasonFor, reason)}>
                  {reasonFor.label}
                </Button>
                <Button variant="ghost" onClick={() => setReasonFor(null)}>
                  Back
                </Button>
              </div>
            </div>
          ) : (
            steps.length > 0 && (
              <div className="flex flex-wrap gap-2">
                {steps.map((s) => (
                  <Button
                    key={s.label}
                    variant={s.variant}
                    disabled={busy}
                    onClick={() => (s.needsReason ? setReasonFor(s) : run(s))}
                  >
                    {s.label}
                  </Button>
                ))}
              </div>
            )
          )}
          <ErrorBox error={error} />
        </Card>
      </div>

      <Card>
        <h2 className="mb-3 font-display font-semibold">Timeline</h2>
        <ol className="space-y-3 border-l-2 border-line pl-4">
          {o.history?.map((h) => (
            <li key={h.history_id} className="relative text-sm">
              <span className="absolute -left-[1.4rem] top-1.5 h-2.5 w-2.5 rounded-full bg-brand" />
              <p className="font-semibold">{ORDER_STATUS_LABEL[h.new_status]}</p>
              <p className="text-xs text-muted">
                {dateTime(h.changed_at)} · {h.changed_by_name ?? "System"}
              </p>
              {h.note && <p className="text-xs text-muted">“{h.note}”</p>}
            </li>
          ))}
        </ol>
      </Card>
    </div>
  );
}
