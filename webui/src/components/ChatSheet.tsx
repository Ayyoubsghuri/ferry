import { useEffect, useRef, useState } from 'react';
import { Send, X, AlertCircle } from 'lucide-react';
import { api, type Message } from '../lib/api';
import { Sheet } from './common';

export interface ChatTarget {
  id: string;
  name: string;
  paired?: boolean;
}

/** A small message thread with one device: notes and context alongside transfers. */
export function ChatSheet({ target, onClose }: { target: ChatTarget; onClose: () => void }) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const end = useRef<HTMLDivElement>(null);
  const count = useRef(0);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const { messages } = await api.thread(target.id);
        if (!alive) return;
        setMessages(messages);
        if (messages.some((m) => !m.out && !m.read)) api.markRead(target.id).catch(() => {});
      } catch {
        /* keep last */
      }
    };
    load();
    const t = setInterval(load, 1500);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [target.id]);

  useEffect(() => {
    if (messages.length !== count.current) {
      count.current = messages.length;
      end.current?.scrollIntoView({ block: 'end' });
    }
  }, [messages]);

  async function send(e: React.FormEvent) {
    e.preventDefault();
    const t = text.trim();
    if (!t) return;
    setSending(true);
    setError(null);
    try {
      const { message } = await api.sendMessage(target.id, t);
      setMessages((m) => [...m, message]);
      setText('');
    } catch (err: any) {
      setError(err.message);
    } finally {
      setSending(false);
    }
  }

  const unpaired = messages.some((m) => m.unpaired) || target.paired === false;
  return (
    <Sheet title={`Messages with ${target.name}`} onClose={onClose}>
      <div className="chat">
        <div className="sheet-head">
          <div>
            <h2>{target.name}</h2>
            {unpaired ? (
              <p className="muted small" style={{ margin: '4px 0 0' }}>
                Not paired, so its name can't be verified.
              </p>
            ) : null}
          </div>
          <button className="btn ghost icon" aria-label="Close" onClick={onClose}>
            <X size={20} aria-hidden />
          </button>
        </div>
        <div className="chat-thread" aria-live="polite">
          {messages.length === 0 ? (
            <p className="empty small" style={{ textAlign: 'center', margin: 'auto 0' }}>
              Send a quick note, like what a file is for or which part to look at.
            </p>
          ) : (
            messages.map((m) => (
              <div key={m.id} className={`bubble ${m.out ? 'out' : 'in'}${m.failed ? ' failed' : ''}`}>
                <span>{m.text}</span>
                <time dateTime={m.at}>
                  {m.failed ? (
                    <>
                      <AlertCircle size={12} aria-hidden /> Not delivered
                    </>
                  ) : (
                    new Date(m.at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
                  )}
                </time>
              </div>
            ))
          )}
          <div ref={end} />
        </div>
        {error ? <div className="notice error">{error}</div> : null}
        <form className="chat-input" onSubmit={send}>
          <input
            className="input"
            placeholder={`Message ${target.name}`}
            aria-label="Message"
            value={text}
            maxLength={4000}
            autoFocus={window.innerWidth >= 900}
            onChange={(e) => setText(e.target.value)}
          />
          <button className="btn primary icon" style={{ minHeight: 46, width: 52 }} aria-label="Send" disabled={!text.trim() || sending}>
            <Send size={18} aria-hidden />
          </button>
        </form>
      </div>
    </Sheet>
  );
}
