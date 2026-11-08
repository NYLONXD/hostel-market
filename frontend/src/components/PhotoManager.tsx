"use client";

import { useRef, useState } from "react";

import { api, errorText } from "@/lib/api";
import type { Photo as PhotoT } from "@/lib/types";

import { Button, ErrorBox, Photo } from "./ui";

const MAX = 5;

export function PhotoManager({ listingId, photos, onChange }: { listingId: number; photos: PhotoT[]; onChange: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function upload(files: FileList | null) {
    if (!files?.length) return;
    setBusy(true);
    setError(null);
    try {
      for (const file of Array.from(files).slice(0, MAX - photos.length)) {
        const fd = new FormData();
        fd.append("file", file);
        await api.post(`/listings/${listingId}/photos`, fd);
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
      if (input.current) input.current.value = "";
      onChange();
    }
  }

  async function remove(photoId: number) {
    setError(null);
    try {
      await api.del(`/listings/${listingId}/photos/${photoId}`);
      onChange();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-3 gap-2 sm:grid-cols-5">
        {photos.map((p) => (
          <div key={p.photo_id} className="relative">
            <Photo src={p.url} alt={`Photo ${p.position + 1}`} className="aspect-square w-full rounded-lg" />
            {p.position === 0 && (
              <span className="absolute left-1 top-1 rounded bg-ink/70 px-1.5 text-[10px] text-paper">Cover</span>
            )}
            <button
              onClick={() => remove(p.photo_id)}
              className="absolute right-1 top-1 rounded-full bg-card px-2 text-sm shadow"
              aria-label={`Delete photo ${p.position + 1}`}
            >
              ×
            </button>
          </div>
        ))}
      </div>
      {photos.length < MAX && (
        <>
          <input
            ref={input}
            type="file"
            accept="image/jpeg,image/png,image/webp"
            multiple
            hidden
            onChange={(e) => upload(e.target.files)}
          />
          <Button type="button" variant="secondary" disabled={busy} onClick={() => input.current?.click()}>
            {busy ? "Uploading…" : `Add photos (${photos.length}/${MAX})`}
          </Button>
        </>
      )}
      <p className="text-xs text-muted">JPEG, PNG or WebP, up to 5 MB each. The first photo is the cover.</p>
      <ErrorBox error={error} />
    </div>
  );
}
