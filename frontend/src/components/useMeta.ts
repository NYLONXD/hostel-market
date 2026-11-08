"use client";

import { useEffect, useState } from "react";

import { api } from "@/lib/api";
import type { Category, Hostel } from "@/lib/types";

/** Hostels (with blocks) and categories: small lookup lists used by forms and filters. */
export function useMeta() {
  const [hostels, setHostels] = useState<Hostel[]>([]);
  const [categories, setCategories] = useState<Category[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    Promise.all([api.get<Hostel[]>("/meta/hostels"), api.get<Category[]>("/meta/categories")])
      .then(([h, c]) => {
        setHostels(h);
        setCategories(c);
      })
      .catch(() => setError("Could not load hostels and categories."));
  }, []);

  return { hostels, categories, error };
}
