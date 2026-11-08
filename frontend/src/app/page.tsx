"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

import { useAuth } from "@/components/AuthProvider";
import { LinkButton } from "@/components/ui";

const steps = [
  { t: "List it", d: "Snap a photo, set a price, and choose who can buy: your block, your year, or the whole hostel." },
  { t: "Get a request", d: "Hostel-mates send buy requests. Accept one; the rest wait in line in case it falls through." },
  { t: "Meet & pay", d: "Pay by UPI or cash when you meet. Both of you confirm each step, so nothing gets lost." },
];

export default function Home() {
  const { user, ready } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (ready && user) router.replace("/listings");
  }, [ready, user, router]);

  return (
    <div className="space-y-14 py-6">
      <section className="grid items-center gap-10 md:grid-cols-[1.2fr_1fr]">
        <div className="space-y-5">
          <p className="text-sm font-semibold uppercase tracking-widest text-brand">For hostel residents only</p>
          <h1 className="font-display text-4xl font-extrabold leading-tight sm:text-5xl">
            Sell your old books, cycles and kettles to the people down the corridor.
          </h1>
          <p className="max-w-xl text-lg text-muted">
            Hostel Market is a noticeboard for your hostel: every listing stays inside your hostel, buyers are
            verified students, and every deal is tracked from request to handover.
          </p>
          <div className="flex flex-wrap gap-3">
            <LinkButton href="/register">Create an account</LinkButton>
            <LinkButton href="/login" variant="secondary">
              Sign in
            </LinkButton>
          </div>
        </div>
        <div className="relative hidden h-72 md:block" aria-hidden>
          {[
            ["Engineering Mathematics", "₹350", "-rotate-3 left-4 top-2"],
            ["Hero Sprint cycle", "₹2,500", "rotate-2 right-2 top-24"],
            ["Electric kettle 1.5L", "₹400", "-rotate-1 left-12 bottom-0"],
          ].map(([t, p, pos]) => (
            <div key={t} className={`absolute w-60 rounded-lg border border-line bg-card p-4 shadow-md ${pos}`}>
              <div className="mb-3 h-2 w-10 rounded bg-tag" />
              <p className="font-display font-semibold">{t}</p>
              <span className="price-tag mt-2 inline-block text-sm">{p}</span>
            </div>
          ))}
        </div>
      </section>

      <section className="grid gap-4 sm:grid-cols-3">
        {steps.map((s, i) => (
          <div key={s.t} className="rounded-xl border border-line bg-card p-5">
            <span className="font-display text-3xl font-extrabold text-tag">{i + 1}</span>
            <h2 className="mt-1 font-display text-lg font-semibold">{s.t}</h2>
            <p className="mt-1 text-sm text-muted">{s.d}</p>
          </div>
        ))}
      </section>
    </div>
  );
}
