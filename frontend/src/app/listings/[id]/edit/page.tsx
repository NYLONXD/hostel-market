"use client";

import { useParams, useRouter } from "next/navigation";
import { useCallback, useEffect, useState } from "react";

import { ListingForm, type ListingBody } from "@/components/ListingForm";
import { PhotoManager } from "@/components/PhotoManager";
import { RequireAuth } from "@/components/RequireAuth";
import { Card, ErrorBox, LinkButton, Notice, PageTitle, Spinner } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText } from "@/lib/api";
import type { ListingDetail, User } from "@/lib/types";

export default function EditListingPage() {
  return <RequireAuth>{(user) => <EditListing user={user} />}</RequireAuth>;
}

function EditListing({ user }: { user: User }) {
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const { hostels, categories } = useMeta();
  const [listing, setListing] = useState<ListingDetail | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [justCreated] = useState(() => typeof window !== "undefined" && window.location.search.includes("new=1"));

  const load = useCallback(() => {
    api
      .get<ListingDetail>(`/listings/${id}`)
      .then(setListing)
      .catch((e) => setLoadError(errorText(e)));
  }, [id]);

  useEffect(load, [load]);

  async function save(body: ListingBody) {
    setBusy(true);
    setError(null);
    try {
      await api.patch(`/listings/${id}`, { ...body });
      router.push(`/listings/${id}`);
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  }

  if (loadError) return <ErrorBox error={loadError} />;
  if (!listing || !categories.length) return <Spinner />;
  if (listing.seller_id !== user.user_id) return <ErrorBox error="Only the seller can edit this listing." />;

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <PageTitle action={<LinkButton href={`/listings/${id}`} variant="secondary">View listing</LinkButton>}>
        Edit listing
      </PageTitle>
      {justCreated && <Notice tone="ok">Your listing is live! Add a few photos so buyers can see it.</Notice>}

      <Card>
        <h2 className="mb-3 font-display text-lg font-semibold">Photos</h2>
        <PhotoManager listingId={listing.listing_id} photos={listing.photos} onChange={load} />
      </Card>

      <Card>
        <h2 className="mb-3 font-display text-lg font-semibold">Details</h2>
        {listing.status === "active" ? (
          <ListingForm
            initial={listing}
            categories={categories}
            hostel={hostels.find((h) => h.hostel_id === user.hostel_id)}
            submitLabel="Save changes"
            error={error}
            busy={busy}
            onSubmit={save}
          />
        ) : (
          <Notice tone="warn">This listing is {listing.status}; its details can only be edited while it is on sale.</Notice>
        )}
      </Card>
    </div>
  );
}
