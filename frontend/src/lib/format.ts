import type { Condition, ListingStatus, OrderStatus } from "./types";

const inr = new Intl.NumberFormat("en-IN", { style: "currency", currency: "INR", maximumFractionDigits: 0 });

export const rupees = (n: number) => inr.format(n);

export function timeAgo(iso: string): string {
  const secs = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 60) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins} min ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs} h ago`;
  const days = Math.round(hrs / 24);
  if (days < 30) return `${days} d ago`;
  return new Date(iso).toLocaleDateString("en-IN", { day: "numeric", month: "short", year: "numeric" });
}

export const dateTime = (iso: string) =>
  new Date(iso).toLocaleString("en-IN", { day: "numeric", month: "short", hour: "numeric", minute: "2-digit" });

/** "18:00:00" -> "18:00" */
export const hhmm = (t: string | null) => (t ? t.slice(0, 5) : "");

export const CONDITIONS: { value: Condition; label: string }[] = [
  { value: "new", label: "New" },
  { value: "like_new", label: "Like new" },
  { value: "good", label: "Good" },
  { value: "fair", label: "Fair" },
  { value: "poor", label: "Poor" },
];

export const conditionLabel = (c: Condition) => CONDITIONS.find((x) => x.value === c)?.label ?? c;

export const LISTING_STATUS_LABEL: Record<ListingStatus, string> = {
  active: "On sale",
  reserved: "Reserved",
  sold: "Sold",
  removed: "Removed",
};

export const ORDER_STATUS_LABEL: Record<OrderStatus, string> = {
  requested: "Requested",
  accepted: "Accepted",
  awaiting_payment: "Awaiting payment",
  paid: "Paid",
  handed_over: "Handed over",
  completed: "Completed",
  rejected: "Rejected",
  cancelled: "Cancelled",
  expired: "Expired",
};

export const yearLabel = (y: number) => `${y}${["st", "nd", "rd"][y - 1] ?? "th"} year`;
