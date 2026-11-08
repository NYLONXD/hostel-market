import Link from "next/link";

import { conditionLabel, rupees, timeAgo } from "@/lib/format";
import type { Listing } from "@/lib/types";

import { ListingBadge, Photo } from "./ui";

export function ListingCard({ l, showStatus = false }: { l: Listing; showStatus?: boolean }) {
  const restricted = l.allowed_block || l.allowed_year || l.available_from;
  return (
    <Link
      href={`/listings/${l.listing_id}`}
      className="group flex flex-col overflow-hidden rounded-xl border border-line bg-card transition hover:-translate-y-0.5 hover:shadow-md"
    >
      <div className="relative">
        <Photo src={l.cover_url} alt={l.title} className="aspect-[4/3] w-full" />
        <span className="price-tag absolute bottom-2 right-0 text-sm">{rupees(l.price)}</span>
      </div>
      <div className="flex flex-1 flex-col gap-1 p-3">
        <p className="line-clamp-2 font-semibold group-hover:text-brand">{l.title}</p>
        <p className="text-xs text-muted">
          {l.category_name} · {conditionLabel(l.condition)}
        </p>
        <div className="mt-auto flex items-center justify-between pt-2 text-xs text-muted">
          <span>
            {l.seller_name} · Block {l.seller_block}
          </span>
          <span>{timeAgo(l.created_at)}</span>
        </div>
        {(showStatus || restricted) && (
          <div className="flex flex-wrap gap-1 pt-1">
            {showStatus && <ListingBadge status={l.status} />}
            {restricted && <span className="rounded-full bg-warn-soft px-2 py-0.5 text-xs">Restricted</span>}
          </div>
        )}
      </div>
    </Link>
  );
}
