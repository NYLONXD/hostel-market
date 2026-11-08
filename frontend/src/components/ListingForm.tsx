"use client";

import { useEffect, useState, type FormEvent } from "react";

import { CONDITIONS, hhmm } from "@/lib/format";
import { local } from "@/lib/local";
import type { Category, Condition, Hostel, Listing } from "@/lib/types";

import { Button, ErrorBox, Field, Input, Select, Textarea } from "./ui";

export interface ListingBody {
  title: string;
  description: string;
  category: string;
  condition: Condition;
  price: number;
  allowed_block_id: number | null;
  allowed_year: number | null;
  available_from: string | null;
  available_until: string | null;
}

interface FormState {
  title: string;
  description: string;
  category: string;
  condition: Condition;
  price: string;
  allowed_block_id: string;
  allowed_year: string;
  timeWindow: boolean;
  available_from: string;
  available_until: string;
}

const blank: FormState = {
  title: "",
  description: "",
  category: "",
  condition: "good",
  price: "",
  allowed_block_id: "",
  allowed_year: "",
  timeWindow: false,
  available_from: "18:00",
  available_until: "22:00",
};

function fromListing(l: Listing): FormState {
  return {
    title: l.title,
    description: l.description,
    category: l.category,
    condition: l.condition,
    price: String(l.price),
    allowed_block_id: l.allowed_block_id ? String(l.allowed_block_id) : "",
    allowed_year: l.allowed_year ? String(l.allowed_year) : "",
    timeWindow: !!l.available_from,
    available_from: hhmm(l.available_from) || blank.available_from,
    available_until: hhmm(l.available_until) || blank.available_until,
  };
}

export function ListingForm({
  initial,
  categories,
  hostel,
  submitLabel,
  saveDraft = false,
  error,
  busy,
  onSubmit,
}: {
  initial?: Listing;
  categories: Category[];
  hostel: Hostel | undefined;
  submitLabel: string;
  saveDraft?: boolean;
  error: string | null;
  busy: boolean;
  onSubmit: (body: ListingBody) => void;
}) {
  // A new listing's unfinished form is kept in this browser so a refresh doesn't lose it.
  const [f, setF] = useState<FormState>(() =>
    initial ? fromListing(initial) : { ...blank, ...((saveDraft && local.getDraft<FormState>()) || {}) },
  );

  useEffect(() => {
    if (saveDraft) local.setDraft(f);
  }, [f, saveDraft]);

  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((s) => ({ ...s, [k]: v }));

  function submit(e: FormEvent) {
    e.preventDefault();
    onSubmit({
      title: f.title,
      description: f.description,
      category: f.category,
      condition: f.condition,
      price: Number(f.price),
      allowed_block_id: f.allowed_block_id ? Number(f.allowed_block_id) : null,
      allowed_year: f.allowed_year ? Number(f.allowed_year) : null,
      available_from: f.timeWindow ? f.available_from : null,
      available_until: f.timeWindow ? f.available_until : null,
    });
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <Field label="Title">
        <Input required minLength={3} maxLength={120} value={f.title} onChange={(e) => set("title", e.target.value)} />
      </Field>
      <Field label="Description" hint="Condition details, edition, where to pick it up…">
        <Textarea rows={4} maxLength={4000} value={f.description} onChange={(e) => set("description", e.target.value)} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Category">
          <Select required value={f.category} onChange={(e) => set("category", e.target.value)}>
            <option value="">Choose…</option>
            {categories.map((c) => (
              <option key={c.slug} value={c.slug}>
                {c.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Condition">
          <Select value={f.condition} onChange={(e) => set("condition", e.target.value as Condition)}>
            {CONDITIONS.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Price (₹)">
          <Input
            required
            type="number"
            min={1}
            max={1000000}
            step={1}
            value={f.price}
            onChange={(e) => set("price", e.target.value)}
          />
        </Field>
      </div>

      <fieldset className="space-y-4 rounded-xl border border-line p-4">
        <legend className="px-1 text-sm font-semibold">Who can buy? (optional)</legend>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Only residents of block">
            <Select value={f.allowed_block_id} onChange={(e) => set("allowed_block_id", e.target.value)}>
              <option value="">Any block in {hostel?.name ?? "my hostel"}</option>
              {hostel?.blocks.map((b) => (
                <option key={b.block_id} value={b.block_id}>
                  Block {b.name}
                </option>
              ))}
            </Select>
          </Field>
          <Field label="Only students in year">
            <Select value={f.allowed_year} onChange={(e) => set("allowed_year", e.target.value)}>
              <option value="">Any year</option>
              {[1, 2, 3, 4, 5, 6].map((y) => (
                <option key={y} value={y}>
                  Year {y}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={f.timeWindow} onChange={(e) => set("timeWindow", e.target.checked)} />
          Only accept requests during certain hours (IST)
        </label>
        {f.timeWindow && (
          <div className="grid grid-cols-2 gap-4">
            <Field label="From">
              <Input type="time" required value={f.available_from} onChange={(e) => set("available_from", e.target.value)} />
            </Field>
            <Field label="Until" hint="Earlier than “From” means overnight.">
              <Input type="time" required value={f.available_until} onChange={(e) => set("available_until", e.target.value)} />
            </Field>
          </div>
        )}
      </fieldset>

      <ErrorBox error={error} />
      <Button type="submit" disabled={busy}>
        {busy ? "Saving…" : submitLabel}
      </Button>
    </form>
  );
}
