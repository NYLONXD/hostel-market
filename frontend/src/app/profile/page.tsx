"use client";

import { useEffect, useState, type FormEvent } from "react";

import { useAuth } from "@/components/AuthProvider";
import { RequireAuth } from "@/components/RequireAuth";
import { Button, Card, ErrorBox, Field, Input, Notice, PageTitle, Select } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText } from "@/lib/api";
import { rupees } from "@/lib/format";
import type { SellerStats, User } from "@/lib/types";

export default function ProfilePage() {
  return <RequireAuth>{(user) => <Profile user={user} />}</RequireAuth>;
}

function Profile({ user }: { user: User }) {
  const { refresh } = useAuth();
  const { hostels } = useMeta();
  const [form, setForm] = useState({
    full_name: user.full_name,
    hostel_id: String(user.hostel_id),
    block_id: String(user.block_id),
    year: String(user.year_of_study),
    upi_id: user.upi_id ?? "",
  });
  const [stats, setStats] = useState<SellerStats | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .get<User & { stats: SellerStats }>("/users/me")
      .then((r) => setStats(r.stats))
      .catch(() => {});
  }, []);

  const blocks = hostels.find((h) => String(h.hostel_id) === form.hostel_id)?.blocks ?? [];

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      await api.patch("/users/me", {
        full_name: form.full_name,
        block_id: Number(form.block_id),
        year_of_study: Number(form.year),
        upi_id: form.upi_id.trim() || null,
      });
      await refresh();
      setSaved(true);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <PageTitle>Your profile</PageTitle>
      {!user.is_verified && <Notice tone="warn">Not verified yet: a hostel admin needs to approve your account.</Notice>}

      {stats && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {[
            ["On sale", stats.active_listings],
            ["Reserved", stats.reserved_listings],
            ["Sold", stats.sold_listings],
            ["Earned", rupees(stats.total_earned)],
          ].map(([label, value]) => (
            <Card key={label as string} className="text-center">
              <p className="font-display text-2xl font-bold">{value}</p>
              <p className="text-xs text-muted">{label}</p>
            </Card>
          ))}
        </div>
      )}

      <Card>
        <form onSubmit={save} className="space-y-4">
          <Field label="Email">
            <Input value={user.email} disabled />
          </Field>
          <Field label="Full name">
            <Input required maxLength={100} value={form.full_name} onChange={(e) => setForm({ ...form, full_name: e.target.value })} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Hostel">
              <Select value={form.hostel_id} onChange={(e) => setForm({ ...form, hostel_id: e.target.value, block_id: "" })}>
                {hostels.map((h) => (
                  <option key={h.hostel_id} value={h.hostel_id}>
                    {h.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Block">
              <Select required value={form.block_id} onChange={(e) => setForm({ ...form, block_id: e.target.value })}>
                <option value="">Choose…</option>
                {blocks.map((b) => (
                  <option key={b.block_id} value={b.block_id}>
                    {b.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Year">
              <Select value={form.year} onChange={(e) => setForm({ ...form, year: e.target.value })}>
                {[1, 2, 3, 4, 5, 6].map((y) => (
                  <option key={y}>{y}</option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label="UPI ID" hint="Buyers pay you here. Required to sell.">
            <Input placeholder="name@bank" value={form.upi_id} onChange={(e) => setForm({ ...form, upi_id: e.target.value })} />
          </Field>
          <ErrorBox error={error} />
          {saved && <Notice tone="ok">Saved.</Notice>}
          <Button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save profile"}
          </Button>
        </form>
      </Card>
    </div>
  );
}
