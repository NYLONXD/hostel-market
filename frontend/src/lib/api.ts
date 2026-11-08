// Tiny fetch wrapper for the FastAPI backend.

import { local } from "./local";

export const API_URL = (process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8000/api").replace(/\/$/, "");

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public fields?: { field: string; message: string }[],
  ) {
    super(message);
  }
}

export const AUTH_EXPIRED_EVENT = "hm:auth-expired";

type Body = Record<string, unknown> | FormData | undefined;

async function request<T>(method: string, path: string, body?: Body): Promise<T> {
  const headers: Record<string, string> = {};
  const token = local.getSession()?.access_token;
  if (token) headers.Authorization = `Bearer ${token}`;

  let payload: BodyInit | undefined;
  if (body instanceof FormData) payload = body;
  else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }

  let res: Response;
  try {
    res = await fetch(`${API_URL}${path}`, { method, headers, body: payload });
  } catch {
    throw new ApiError(0, "network", "Can't reach the server. Is the backend running?");
  }

  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) {
    const err = data?.error ?? {};
    if (res.status === 401 && token) window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    throw new ApiError(res.status, err.code ?? "error", err.message ?? `Request failed (${res.status})`, err.fields);
  }
  return data as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: Body) => request<T>("POST", path, body),
  patch: <T>(path: string, body?: Body) => request<T>("PATCH", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};

/** A readable message for any thrown value. */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.fields?.length) return e.fields.map((f) => (f.field ? `${f.field}: ${f.message}` : f.message)).join("; ");
    return e.message;
  }
  return e instanceof Error ? e.message : "Something went wrong";
}

/** Build a query string, skipping empty values. */
export function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== "") sp.set(k, String(v));
  }
  const s = sp.toString();
  return s ? `?${s}` : "";
}
