"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { ListingForm, type ListingBody } from "@/components/ListingForm";
import { RequireAuth } from "@/components/RequireAuth";
import { Card, Notice, PageTitle } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText } from "@/lib/api";
import { local } from "@/lib/local";
import type { ListingDetail, User } from "@/lib/types";

export default function NewListingPage() {
  return <RequireAuth>{(user) => <NewListing user={user} />}</RequireAuth>;
}

function NewListing({ user }: { user: User }) {
  const router = useRouter();
  const { hostels, categories } = useMeta();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function create(body: ListingBody) {
    setBusy(true);
    setError(null);
    try {
      const l = await api.post<ListingDetail>("/listings", { ...body });
      local.setDraft(null);
      router.push(`/listings/${l.listing_id}/edit?new=1`);
    } catch (e) {
      setError(errorText(e));
      setBusy(false);
    }
  }

  const blocked = !user.is_verified
    ? "Your account must be verified by a hostel admin before you can sell."
    : !user.upi_id
      ? "Add a UPI ID to your profile first, so buyers know where to pay you."
      : null;

  return (
    <div className="mx-auto max-w-2xl">
      <PageTitle>Sell something</PageTitle>
      {blocked ? (
        <Notice tone="warn">
          {blocked}{" "}
          {user.is_verified && (
            <Link href="/profile" className="font-semibold underline">
              Go to profile
            </Link>
          )}
        </Notice>
      ) : (
        <Card>
          <ListingForm
            categories={categories}
            hostel={hostels.find((h) => h.hostel_id === user.hostel_id)}
            submitLabel="Publish and add photos"
            saveDraft
            error={error}
            busy={busy}
            onSubmit={create}
          />
        </Card>
      )}
    </div>
  );
}
