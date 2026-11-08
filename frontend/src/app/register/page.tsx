"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";

import { useAuth } from "@/components/AuthProvider";
import { Button, Card, ErrorBox, Field, Input, Select } from "@/components/ui";
import { useMeta } from "@/components/useMeta";
import { api, errorText } from "@/lib/api";
import type { Session } from "@/lib/types";

export default function RegisterPage() {
  const { signIn } = useAuth();
  const router = useRouter();
  const { hostels, error: metaError } = useMeta();
  const [form, setForm] = useState({ full_name: "", email: "", password: "", hostel_id: "", block_id: "", year: "1", upi_id: "" });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const blocks = hostels.find((h) => String(h.hostel_id) === form.hostel_id)?.blocks ?? [];

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const session = await api.post<Session>("/auth/register", {
        full_name: form.full_name,
        email: form.email,
        password: form.password,
        block_id: Number(form.block_id),
        year_of_study: Number(form.year),
        ...(form.upi_id.trim() ? { upi_id: form.upi_id.trim() } : {}),
      });
      signIn(session);
      router.push("/listings");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto max-w-lg">
      <h1 className="mb-2 font-display text-3xl font-bold">Join your hostel market</h1>
      <p className="mb-6 text-sm text-muted">
        Sign up with your college email to be verified instantly. Other emails are verified by a hostel admin.
      </p>
      <Card>
        <form onSubmit={submit} className="space-y-4">
          <Field label="Full name">
            <Input required maxLength={100} value={form.full_name} onChange={set("full_name")} autoComplete="name" />
          </Field>
          <Field label="Email">
            <Input type="email" required value={form.email} onChange={set("email")} autoComplete="email" />
          </Field>
          <Field label="Password" hint="At least 8 characters.">
            <Input
              type="password"
              required
              minLength={8}
              maxLength={72}
              value={form.password}
              onChange={set("password")}
              autoComplete="new-password"
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Hostel">
              <Select required value={form.hostel_id} onChange={(e) => setForm((f) => ({ ...f, hostel_id: e.target.value, block_id: "" }))}>
                <option value="">Choose…</option>
                {hostels.map((h) => (
                  <option key={h.hostel_id} value={h.hostel_id}>
                    {h.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Block">
              <Select required value={form.block_id} onChange={set("block_id")} disabled={!blocks.length}>
                <option value="">Choose…</option>
                {blocks.map((b) => (
                  <option key={b.block_id} value={b.block_id}>
                    {b.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Year">
              <Select value={form.year} onChange={set("year")}>
                {[1, 2, 3, 4, 5, 6].map((y) => (
                  <option key={y}>{y}</option>
                ))}
              </Select>
            </Field>
          </div>
          <Field label="UPI ID (optional)" hint="Needed only if you want to sell, so buyers can pay you. e.g. name@okaxis">
            <Input value={form.upi_id} onChange={set("upi_id")} placeholder="name@bank" />
          </Field>
          <ErrorBox error={error ?? metaError} />
          <Button type="submit" disabled={busy} className="w-full">
            {busy ? "Creating account…" : "Create account"}
          </Button>
        </form>
      </Card>
      <p className="mt-4 text-center text-sm text-muted">
        Already have an account?{" "}
        <Link href="/login" className="font-semibold text-brand hover:underline">
          Sign in
        </Link>
      </p>
    </div>
  );
}
