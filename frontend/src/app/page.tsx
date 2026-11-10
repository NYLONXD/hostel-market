"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

import { useAuth } from "@/components/AuthProvider";
import { LinkButton } from "@/components/ui";

const steps = [
  { t: "List your stash", d: "Got extra Maggi, chips or cold drinks? Set a price and the hours you're awake to sell." },
  { t: "Get a request", d: "Hungry hostel-mates send requests. Accept one; the rest wait in line in case it falls through." },
  { t: "Hand over & pay", d: "Pay by UPI or cash at the door. Both of you confirm each step, so nothing gets lost." },
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
            Craving snacks at 2 AM? Someone down the corridor has them.
          </h1>
          <p className="max-w-xl text-lg text-muted">
            Midnight Munchies is a late-night snack board for your hostel: every listing stays inside your hostel,
            buyers are verified students, and every order is tracked from request to handover.
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
            ["Maggi Masala x2", "₹30", "-rotate-3 left-4 top-2"],
            ["Lay's Magic Masala", "₹20", "rotate-2 right-2 top-24"],
            ["Cold Coffee (chilled)", "₹40", "-rotate-1 left-12 bottom-0"],
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
