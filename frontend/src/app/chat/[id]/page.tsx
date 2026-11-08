"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useRef, useState, type FormEvent } from "react";

import { RequireAuth } from "@/components/RequireAuth";
import { Button, ErrorBox, Input, Spinner } from "@/components/ui";
import { api, errorText, qs } from "@/lib/api";
import type { Conversation, Message, User } from "@/lib/types";

const POLL_MS = 4000;

export default function ChatPage() {
  return <RequireAuth>{(user) => <Chat user={user} />}</RequireAuth>;
}

function Chat({ user }: { user: User }) {
  const { id } = useParams<{ id: string }>();
  const [conv, setConv] = useState<Conversation | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [body, setBody] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const lastId = useRef(0);
  const bottom = useRef<HTMLDivElement>(null);

  // Initial load, then poll for anything newer than the last message we have.
  useEffect(() => {
    let alive = true;
    lastId.current = 0;
    const poll = async () => {
      try {
        const res = await api.get<{ conversation: Conversation; messages: Message[] }>(
          `/conversations/${id}/messages${qs({ after_id: lastId.current || undefined })}`,
        );
        if (!alive) return;
        setConv(res.conversation);
        if (res.messages.length) {
          setMessages((m) => {
            const seen = new Set(m.map((x) => x.message_id));
            return [...m, ...res.messages.filter((x) => !seen.has(x.message_id))];
          });
          lastId.current = res.messages[res.messages.length - 1].message_id;
          api.post(`/conversations/${id}/read`, { up_to_id: lastId.current }).catch(() => {});
        }
      } catch (e) {
        if (alive) setError(errorText(e));
      }
    };
    poll();
    const t = setInterval(poll, POLL_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [id]);

  useEffect(() => {
    bottom.current?.scrollIntoView({ block: "end" });
  }, [messages.length]);

  async function send(e: FormEvent) {
    e.preventDefault();
    const text = body.trim();
    if (!text) return;
    setSending(true);
    try {
      const m = await api.post<Message>(`/conversations/${id}/messages`, { body: text });
      setBody("");
      setMessages((list) => (list.some((x) => x.message_id === m.message_id) ? list : [...list, m]));
      lastId.current = Math.max(lastId.current, m.message_id);
      setError(null);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setSending(false);
    }
  }

  if (!conv) return error ? <ErrorBox error={error} /> : <Spinner />;

  return (
    <div className="mx-auto flex h-[calc(100vh-11rem)] max-w-2xl flex-col rounded-xl border border-line bg-card">
      <div className="border-b border-line p-3">
        <p className="font-semibold">{conv.other_name}</p>
        <Link href={`/listings/${conv.listing_id}`} className="text-xs text-brand hover:underline">
          {conv.listing_title}
        </Link>
      </div>
      <div className="flex-1 space-y-2 overflow-y-auto p-3" aria-live="polite">
        {messages.length === 0 && <p className="py-8 text-center text-sm text-muted">Say hello 👋</p>}
        {messages.map((m) => {
          const mine = m.sender_id === user.user_id;
          return (
            <div key={m.message_id} className={`flex ${mine ? "justify-end" : "justify-start"}`}>
              <div
                className={`max-w-[80%] rounded-2xl px-3 py-2 text-sm ${mine ? "bg-brand text-brand-ink" : "bg-brand-soft"}`}
              >
                <p className="whitespace-pre-line break-words">{m.body}</p>
                <p className={`mt-0.5 text-[10px] ${mine ? "opacity-80" : "text-muted"}`}>
                  {new Date(m.created_at).toLocaleTimeString("en-IN", { hour: "numeric", minute: "2-digit" })}
                  {mine && m.message_id <= conv.other_last_read_id && " · seen"}
                </p>
              </div>
            </div>
          );
        })}
        <div ref={bottom} />
      </div>
      <form onSubmit={send} className="flex gap-2 border-t border-line p-3">
        <Input
          aria-label="Message"
          placeholder="Type a message"
          maxLength={2000}
          value={body}
          onChange={(e) => setBody(e.target.value)}
        />
        <Button type="submit" disabled={sending || !body.trim()}>
          Send
        </Button>
      </form>
      {error && (
        <div className="px-3 pb-3">
          <ErrorBox error={error} />
        </div>
      )}
    </div>
  );
}
