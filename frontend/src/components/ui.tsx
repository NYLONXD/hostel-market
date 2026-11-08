// Small shared UI pieces. Plain Tailwind, no component library.

import Link from "next/link";
import type { ComponentProps, ReactNode } from "react";

import { LISTING_STATUS_LABEL, ORDER_STATUS_LABEL } from "@/lib/format";
import type { ListingStatus, OrderStatus } from "@/lib/types";

type Variant = "primary" | "secondary" | "danger" | "ghost";

const variants: Record<Variant, string> = {
  primary: "bg-brand text-brand-ink hover:opacity-90",
  secondary: "bg-card text-ink border border-line hover:border-brand",
  danger: "bg-danger-soft text-danger border border-transparent hover:border-danger",
  ghost: "text-brand hover:underline px-1",
};

const buttonBase =
  "inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2 text-sm font-semibold transition disabled:opacity-50 disabled:cursor-not-allowed";

export function Button({ variant = "primary", className = "", ...props }: ComponentProps<"button"> & { variant?: Variant }) {
  return <button className={`${buttonBase} ${variants[variant]} ${className}`} {...props} />;
}

export function LinkButton({
  variant = "primary",
  className = "",
  ...props
}: ComponentProps<typeof Link> & { variant?: Variant }) {
  return <Link className={`${buttonBase} ${variants[variant]} ${className}`} {...props} />;
}

const fieldBase =
  "w-full rounded-lg border border-line bg-card px-3 py-2 text-sm text-ink placeholder:text-muted focus:border-brand focus:outline-none";

export const Input = ({ className = "", ...p }: ComponentProps<"input">) => <input className={`${fieldBase} ${className}`} {...p} />;
export const Select = ({ className = "", ...p }: ComponentProps<"select">) => <select className={`${fieldBase} ${className}`} {...p} />;
export const Textarea = ({ className = "", ...p }: ComponentProps<"textarea">) => (
  <textarea className={`${fieldBase} ${className}`} {...p} />
);

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium">{label}</span>
      {children}
      {hint && <span className="block text-xs text-muted">{hint}</span>}
    </label>
  );
}

export function Card({ className = "", children }: { className?: string; children: ReactNode }) {
  return <div className={`rounded-xl border border-line bg-card p-4 sm:p-5 ${className}`}>{children}</div>;
}

export function ErrorBox({ error }: { error: string | null }) {
  if (!error) return null;
  return (
    <p role="alert" className="rounded-lg bg-danger-soft px-3 py-2 text-sm text-danger">
      {error}
    </p>
  );
}

export function Notice({ children, tone = "info" }: { children: ReactNode; tone?: "info" | "ok" | "warn" }) {
  const tones = { info: "bg-brand-soft", ok: "bg-ok-soft text-ok", warn: "bg-warn-soft" };
  return <div className={`rounded-lg px-3 py-2 text-sm ${tones[tone]}`}>{children}</div>;
}

export function Spinner({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 py-10 text-sm text-muted" role="status">
      <span className="h-4 w-4 animate-spin rounded-full border-2 border-line border-t-brand" />
      {label}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-line px-6 py-12 text-center">
      <p className="font-display text-lg font-semibold">{title}</p>
      {children && <div className="mt-2 text-sm text-muted">{children}</div>}
    </div>
  );
}

export function PageTitle({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <h1 className="font-display text-2xl font-bold sm:text-3xl">{children}</h1>
      {action}
    </div>
  );
}

const listingTone: Record<ListingStatus, string> = {
  active: "bg-ok-soft text-ok",
  reserved: "bg-warn-soft",
  sold: "bg-brand-soft",
  removed: "bg-danger-soft text-danger",
};

const orderTone: Partial<Record<OrderStatus, string>> = {
  requested: "bg-warn-soft",
  completed: "bg-ok-soft text-ok",
  rejected: "bg-danger-soft text-danger",
  cancelled: "bg-danger-soft text-danger",
  expired: "bg-danger-soft text-danger",
};

const pill = "inline-block rounded-full px-2.5 py-0.5 text-xs font-semibold";

export const ListingBadge = ({ status }: { status: ListingStatus }) => (
  <span className={`${pill} ${listingTone[status]}`}>{LISTING_STATUS_LABEL[status]}</span>
);

export const OrderBadge = ({ status }: { status: OrderStatus }) => (
  <span className={`${pill} ${orderTone[status] ?? "bg-brand-soft"}`}>{ORDER_STATUS_LABEL[status]}</span>
);

export function Photo({ src, alt, className = "" }: { src: string | null; alt: string; className?: string }) {
  if (!src) {
    return (
      <div className={`flex items-center justify-center bg-brand-soft text-3xl text-muted ${className}`} aria-label={alt}>
        📦
      </div>
    );
  }
  // eslint-disable-next-line @next/next/no-img-element -- Cloudinary/local URLs, no remote image config needed
  return <img src={src} alt={alt} className={`object-cover ${className}`} loading="lazy" />;
}
