import { useEffect, useMemo, useState } from 'react';
import { ArrowUpRight, ArrowDownLeft, ClipboardCopy, FileText, Files, FolderOpen, Copy, Trash2 } from 'lucide-react';
import { api, type HistoryEntry, type Me } from '../lib/api';
import { copyText, formatBytes } from '../lib/util';
import { toast } from './common';

type Filter = 'all' | 'sent' | 'received';

function dayLabel(d: Date) {
  const today = new Date();
  const y = new Date();
  y.setDate(today.getDate() - 1);
  if (d.toDateString() === today.toDateString()) return 'Today';
  if (d.toDateString() === y.toDateString()) return 'Yesterday';
  return d.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' });
}

export function HistoryPanel({ me, version }: { me: Me; version: number }) {
  const [entries, setEntries] = useState<HistoryEntry[] | null>(null);
  const [filter, setFilter] = useState<Filter>('all');
  const [open, setOpen] = useState<string | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const bridge = (window as any).FerryAndroid;
  const canOpen = me.canOpenFolder || !!bridge?.openDownloads;

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .history()
        .then((h) => alive && setEntries(h.entries || []))
        .catch(() => alive && setEntries([]));
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [version]);

  const groups = useMemo(() => {
    const list = (entries || []).filter((e) => filter === 'all' || e.direction === filter);
    const out: { day: string; items: HistoryEntry[] }[] = [];
    for (const e of list) {
      const day = dayLabel(new Date(e.at));
      if (!out.length || out[out.length - 1].day !== day) out.push({ day, items: [] });
      out[out.length - 1].items.push(e);
    }
    return out;
  }, [entries, filter]);

  const openFolder = async (path?: string) => {
    if (bridge?.openDownloads) {
      bridge.openDownloads();
      return;
    }
    try {
      await api.openFolder(path);
    } catch (e: any) {
      toast(e.message);
    }
  };

  return (
    <section className="panel stack" aria-labelledby="hist-h">
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <div>
          <h2 id="hist-h">History</h2>
          <p className="lede">Everything sent and received on {me.name}.</p>
        </div>
        {canOpen ? (
          <button className="btn" onClick={() => openFolder()}>
            <FolderOpen size={18} aria-hidden /> Open Downloads
          </button>
        ) : null}
      </div>
      <div className="segmented" role="group" aria-label="Show">
        {(['all', 'sent', 'received'] as Filter[]).map((f) => (
          <button key={f} aria-pressed={filter === f} onClick={() => setFilter(f)}>
            {f === 'all' ? 'All' : f === 'sent' ? 'Sent' : 'Received'}
          </button>
        ))}
      </div>
      {entries === null ? (
        <p className="muted">Loading…</p>
      ) : groups.length === 0 ? (
        <p className="empty">Nothing here yet. Files, text and clipboard you send or receive will be listed here.</p>
      ) : (
        groups.map((g) => (
          <div key={g.day} className="stack" style={{ gap: 2 }}>
            <h3 className="day">{g.day}</h3>
            <ul className="history">
              {g.items.map((e) => {
                const Icon = e.kind === 'clipboard' ? ClipboardCopy : e.kind === 'text' ? FileText : Files;
                const expanded = open === e.id;
                return (
                  <li key={e.id}>
                    <button className="h-row" aria-expanded={expanded} onClick={() => setOpen(expanded ? null : e.id)}>
                      <span className={`h-icon ${e.direction}`}>
                        <Icon size={18} aria-hidden />
                        <span className="h-dir">
                          {e.direction === 'sent' ? (
                            <ArrowUpRight size={12} aria-label="Sent" />
                          ) : (
                            <ArrowDownLeft size={12} aria-label="Received" />
                          )}
                        </span>
                      </span>
                      <span className="h-main">
                        <b className="ellipsis">{e.summary || (e.kind === 'clipboard' ? 'Clipboard' : 'Text')}</b>
                        <span className="small muted ellipsis">
                          {e.direction === 'sent' ? 'To ' : 'From '}
                          {e.peer || 'a browser'}
                          {e.kind === 'files' && e.size ? `, ${formatBytes(e.size)}` : ''}
                          {e.status ? `. ${e.status}` : ''}
                        </span>
                      </span>
                      <time className="small muted" dateTime={e.at}>
                        {new Date(e.at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
                      </time>
                    </button>
                    {expanded ? (
                      <div className="h-detail stack" style={{ gap: 8 }}>
                        {e.text ? <div className="textbox">{e.text}</div> : null}
                        <div className="row" style={{ flexWrap: 'wrap' }}>
                          {e.text ? (
                            <button
                              className="btn"
                              onClick={async () => {
                                if (await copyText(e.text!)) toast('Copied');
                              }}
                            >
                              <Copy size={16} aria-hidden /> Copy
                            </button>
                          ) : null}
                          {e.direction === 'received' && e.kind === 'files' && canOpen ? (
                            <button className="btn" onClick={() => openFolder(e.path)}>
                              <FolderOpen size={16} aria-hidden /> Show in folder
                            </button>
                          ) : null}
                          {e.path ? <span className="small muted">{e.path}</span> : null}
                        </div>
                      </div>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          </div>
        ))
      )}
      {entries && entries.length ? (
        <button
          className={`btn ${confirmClear ? 'danger' : 'ghost'}`}
          style={{ alignSelf: 'flex-start' }}
          onClick={async () => {
            if (!confirmClear) {
              setConfirmClear(true);
              setTimeout(() => setConfirmClear(false), 3000);
              return;
            }
            await api.clearHistory().catch(() => {});
            setEntries([]);
            setConfirmClear(false);
          }}
        >
          <Trash2 size={16} aria-hidden /> {confirmClear ? 'Tap again to clear history' : 'Clear history'}
        </button>
      ) : null}
    </section>
  );
}
