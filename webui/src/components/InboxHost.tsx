import { useCallback, useEffect, useRef, useState } from 'react';
import { Check, ClipboardCopy, Copy, Download, FolderOpen, MessageCircle, Play, Eye, X } from 'lucide-react';
import { api, guestApi, fileUrl, zipUrl, type Clip, type Me, type Offer, type Source } from '../lib/api';
import { copyText, formatBytes, triggerDownload } from '../lib/util';
import { KindIcon, Progress, Sheet, toast } from './common';
import { PlatformIcon } from './DevicesPanel';
import { Preview, previewKind } from './Preview';

const srcOf = (o: Offer): Source => (o.local ? { code: o.code } : { code: o.code, remote: o.url, token: o.token });

/**
 * Everything sent to this device: an Accept / Refuse prompt for files and text, plus
 * clipboard text and message notices. Works in the app and in plain browsers.
 */
export function InboxHost({
  me,
  guest,
  onActivity,
  onOpenChat,
}: {
  me: Me;
  guest: boolean;
  onActivity: () => void;
  onOpenChat: (t: { id: string; name: string }) => void;
}) {
  const [offers, setOffers] = useState<Offer[]>([]);
  const [clip, setClip] = useState<Clip | null>(null);
  const [msg, setMsg] = useState<{ peerId: string; name: string; text: string } | null>(null);
  const [preview, setPreview] = useState<number | null>(null);
  const clipSeq = useRef<number | null>(null);
  const seenMsg = useRef<Set<string> | null>(null);
  const handled = useRef(new Set<string>());
  const watched = useRef(new Set<string>());
  const toasted = useRef(new Set<string>());
  const bridge = (window as any).FerryAndroid;

  const saveViaBrowser = useCallback(
    async (o: Offer) => {
      const src = srcOf(o);
      if (o.kind === 'files') {
        if (o.count === 1 && o.files?.[0]) triggerDownload(fileUrl(src, o.files[0]));
        else triggerDownload(zipUrl(src));
      }
      if (guest) await guestApi.answer(o.id, 'done');
      else await api.offerDone(o.id, true);
      onActivity();
    },
    [guest, onActivity],
  );

  useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const list = guest ? (await guestApi.poll()).offers : (await api.inbox()).offers;
        if (!alive) return;
        setOffers(list || []);
        for (const o of list || []) {
          if (o.auto && o.state === 'accepted' && !handled.current.has(o.id)) {
            handled.current.add(o.id);
            saveViaBrowser(o).catch(() => {});
          }
        }
      } catch {
        /* offline */
      }
      if (guest) return;
      try {
        const { conversations } = await api.conversations();
        if (!alive) return;
        const first = seenMsg.current === null;
        if (first) seenMsg.current = new Set();
        for (const c of conversations) {
          if (seenMsg.current!.has(c.last.id)) continue;
          seenMsg.current!.add(c.last.id);
          if (!first && !c.last.out && c.unread > 0 && !document.querySelector('.chat')) {
            setMsg({ peerId: c.peerId, name: c.peerName, text: c.last.text });
          }
        }
      } catch {
        /* offline */
      }
      try {
        const r = await api.clipboard(clipSeq.current ?? 0);
        if (!alive) return;
        if (clipSeq.current === null) {
          clipSeq.current = r.seq;
        } else if (r.clips.length) {
          clipSeq.current = r.seq;
          const c = r.clips[r.clips.length - 1];
          let copied = r.autoCopied;
          if (!copied && bridge?.setClipboard) {
            bridge.setClipboard(c.text);
            copied = true;
          }
          if (!copied) copied = await copyText(c.text).catch(() => false);
          setClip({ ...c, copied } as Clip & { copied: boolean });
          onActivity();
        }
      } catch {
        /* offline */
      }
    };
    poll();
    const t = setInterval(poll, 1500);
    return () => {
      alive = false;
      clearInterval(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guest]);

  useEffect(() => {
    if (!clip) return;
    const t = setTimeout(() => setClip(null), 8000);
    return () => clearTimeout(t);
  }, [clip]);
  useEffect(() => {
    if (!msg) return;
    const t = setTimeout(() => setMsg(null), 12000);
    return () => clearTimeout(t);
  }, [msg]);

  // Quietly finished automatic transfers become a toast.
  useEffect(() => {
    for (const o of offers) {
      if (o.auto && o.state === 'done' && !toasted.current.has(o.id)) {
        toasted.current.add(o.id);
        toast(`Received ${o.summary} from ${o.fromName}`);
        onActivity();
        api.dismiss(o.id).catch(() => {});
      }
    }
  }, [offers, onActivity]);

  for (const o of offers) if (o.state === 'pending' || o.state === 'saving') watched.current.add(o.id);
  const visible = offers.filter(
    (o) =>
      o.state === 'pending' ||
      o.state === 'saving' ||
      ((o.state === 'done' || o.state === 'failed') && !o.auto && watched.current.has(o.id)),
  );
  const current = visible[0];

  const close = (o: Offer) => {
    (guest ? guestApi.answer(o.id, 'dismiss') : api.dismiss(o.id)).catch(() => {});
    setOffers((os) => os.filter((x) => x.id !== o.id));
    setPreview(null);
  };

  async function accept(o: Offer) {
    try {
      handled.current.add(o.id);
      if (guest) {
        await guestApi.answer(o.id, 'accept');
        await saveViaBrowser(o);
        if (o.kind === 'files') toast(`Saving ${o.summary}`);
        if (o.kind === 'files') close(o);
        return;
      }
      const r = await api.accept(o.id);
      if (o.kind === 'files' && !r.saveDir) {
        await saveViaBrowser(o);
        toast(`Saving ${o.summary} to Downloads`);
        close(o);
      }
      onActivity();
    } catch (e: any) {
      toast(e.message);
    }
  }

  async function refuse(o: Offer) {
    await (guest ? guestApi.answer(o.id, 'decline') : api.decline(o.id)).catch(() => {});
    close(o);
  }

  return (
    <>
      {current ? (
        <Sheet title={`From ${current.fromName}`} onClose={() => (current.state === 'pending' ? undefined : close(current))}>
          <div className="stack">
            <div className="sheet-head">
              <div className="row" style={{ alignItems: 'flex-start' }}>
                <span className="dev-icon">
                  <PlatformIcon platform={current.fromPlatform} />
                </span>
                <div>
                  <h2>
                    {current.state === 'pending'
                      ? `${current.fromName} wants to send you ${current.kind === 'text' ? 'a message' : current.count === 1 ? 'a file' : `${current.count} files`}`
                      : current.state === 'saving'
                        ? `Receiving from ${current.fromName}`
                        : current.state === 'done'
                          ? 'Received'
                          : "Couldn't receive"}
                  </h2>
                  {current.kind === 'files' ? (
                    <p className="muted" style={{ margin: '6px 0 0' }}>
                      {formatBytes(current.size)}
                    </p>
                  ) : null}
                  {!guest && current.unpaired && current.state === 'pending' && current.fromPlatform !== 'browser' ? (
                    <p className="small" style={{ margin: '6px 0 0', color: 'var(--ink-2)' }}>
                      Not paired, so the name can't be verified.
                    </p>
                  ) : null}
                </div>
              </div>
              {current.state !== 'pending' ? (
                <button className="btn ghost icon" aria-label="Close" onClick={() => close(current)}>
                  <X size={20} aria-hidden />
                </button>
              ) : null}
            </div>

            {current.text ? <div className="textbox">{current.text}</div> : null}

            {current.kind === 'files' && current.files?.length && current.state === 'pending' ? (
              <ul className="files" style={{ maxHeight: 260, overflow: 'auto' }}>
                {current.files.slice(0, 100).map((f, i) => {
                  const pk = previewKind(f);
                  return (
                    <li key={f.id}>
                      <span className="ficon">
                        <KindIcon name={f.name} type={f.type} />
                      </span>
                      <span className="fname">
                        <b>{f.name.split('/').pop()}</b>
                        <small>{formatBytes(f.size)}</small>
                      </span>
                      {pk ? (
                        <button
                          className="btn ghost icon"
                          aria-label={`${pk === 'video' || pk === 'audio' ? 'Play' : 'Preview'} ${f.name}`}
                          onClick={() => setPreview(i)}
                        >
                          {pk === 'video' || pk === 'audio' ? <Play size={18} aria-hidden /> : <Eye size={18} aria-hidden />}
                        </button>
                      ) : null}
                    </li>
                  );
                })}
              </ul>
            ) : null}

            {current.state === 'saving' ? <Progress value={current.progress} /> : null}
            {current.state === 'failed' ? <div className="notice error">{current.error || 'The transfer stopped.'}</div> : null}
            {current.state === 'done' && current.savedPath ? (
              <div className="notice ok">
                <Check size={18} aria-hidden />
                <span>Saved to {current.savedPath}</span>
              </div>
            ) : null}

            {current.state === 'pending' ? (
              <div className="row">
                <button className="btn" style={{ flex: 1, minHeight: 54 }} onClick={() => refuse(current)}>
                  Refuse
                </button>
                <button className="btn primary" style={{ flex: 2 }} onClick={() => accept(current)}>
                  <Download size={20} aria-hidden /> Accept
                </button>
              </div>
            ) : current.state === 'done' ? (
              <div className="row">
                {current.kind === 'text' && current.text ? (
                  <button
                    className="btn"
                    style={{ flex: 1 }}
                    onClick={async () => {
                      if (await copyText(current.text!)) toast('Copied');
                    }}
                  >
                    <Copy size={18} aria-hidden /> Copy
                  </button>
                ) : null}
                {current.savedPath && me.canOpenFolder ? (
                  <button className="btn" style={{ flex: 1 }} onClick={() => api.openFolder(current.savedPath)}>
                    <FolderOpen size={18} aria-hidden /> Show in folder
                  </button>
                ) : null}
                <button className="btn primary" style={{ flex: 1 }} onClick={() => close(current)}>
                  Done
                </button>
              </div>
            ) : null}
          </div>
        </Sheet>
      ) : null}

      {preview !== null && current?.files ? (
        <Preview src={srcOf(current)} files={current.files} start={preview} onClose={() => setPreview(null)} />
      ) : null}

      {msg && !current ? (
        <div className="clip-card" role="status" style={clip ? { bottom: 'calc(var(--tabbar) + 84px)' } : undefined}>
          <MessageCircle size={18} aria-hidden />
          <button
            className="msg-preview"
            onClick={() => {
              onOpenChat({ id: msg.peerId, name: msg.name });
              setMsg(null);
            }}
          >
            <b>{msg.name}:</b> {msg.text}
          </button>
          <button className="btn ghost icon" aria-label="Dismiss" onClick={() => setMsg(null)}>
            <X size={16} aria-hidden />
          </button>
        </div>
      ) : null}

      {clip && !current ? (
        <div className="clip-card" role="status">
          <ClipboardCopy size={18} aria-hidden />
          <span className="ellipsis" style={{ flex: 1 }}>
            <b>{(clip as any).copied ? `Copied from ${clip.fromName}:` : `${clip.fromName}:`}</b> {clip.text}
          </span>
          <button
            className="btn ghost icon"
            aria-label="Copy again"
            onClick={async () => {
              if (await copyText(clip.text)) toast('Copied');
            }}
          >
            <Copy size={16} aria-hidden />
          </button>
          <button className="btn ghost icon" aria-label="Dismiss" onClick={() => setClip(null)}>
            <X size={16} aria-hidden />
          </button>
        </div>
      ) : null}
    </>
  );
}
