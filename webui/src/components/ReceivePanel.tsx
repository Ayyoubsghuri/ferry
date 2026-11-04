import { useEffect, useRef, useState } from 'react';
import { Download, Copy, ExternalLink, PackageOpen, Play, Eye, Radar, ScanLine, WifiOff } from 'lucide-react';
import { api, ApiError, fileUrl, zipUrl, type Share, type Source } from '../lib/api';
import { copyText, formatBytes, isAndroidApp, timeLeft, triggerDownload } from '../lib/util';
import { CodeEntry, KindIcon, Progress, toast, useNow } from './common';
import { Preview, previewKind } from './Preview';
import { Scanner, parseJoin } from './Scanner';

type State =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'searching' }
  | { kind: 'found'; share: Share; src: Source; deviceName?: string }
  | { kind: 'error'; message: string };

const isUrl = (t: string) => /^https?:\/\/\S+$/i.test(t.trim());

/**
 * Two optional ways to receive besides Accept/Refuse prompts: type a code the sender
 * created, or (Android app) scan the QR a sending phone shows for its own hotspot.
 */
export function ReceivePanel({ owner, onReceived }: { owner: boolean; onReceived: () => void }) {
  const [code, setCode] = useState('');
  const [state, setState] = useState<State>({ kind: 'idle' });
  const [preview, setPreview] = useState<number | null>(null);
  const [scanning, setScanning] = useState(false);
  const [join, setJoin] = useState<{ state: string; message?: string; name?: string } | null>(null);
  const now = useNow();
  const last = useRef('');
  const logged = useRef(new Set<string>());
  const bridge = (window as any).FerryAndroid;
  const canJoin = isAndroidApp() && !!bridge?.joinHotspot;

  useEffect(() => {
    const on = (e: Event) => setJoin((j) => ({ ...(j || {}), ...(e as CustomEvent).detail }));
    window.addEventListener('ferry-join', on);
    return () => window.removeEventListener('ferry-join', on);
  }, []);

  async function lookup(c: string) {
    last.current = c;
    setState({ kind: 'loading' });
    try {
      const share = await api.get(c);
      if (last.current === c) setState({ kind: 'found', share, src: { code: c } });
    } catch (e) {
      if (last.current !== c) return;
      if (!(e instanceof ApiError && e.status === 404)) return setState({ kind: 'error', message: (e as Error).message });
      setState({ kind: 'searching' });
      try {
        const f = await api.find(c);
        if (last.current !== c) return;
        if (f.where === 'peer' && f.url) {
          const share = await api.remoteShare(f.url, c);
          if (last.current === c) setState({ kind: 'found', share, src: { code: c, remote: f.url }, deviceName: f.name });
        } else lookup(c);
      } catch (e2: any) {
        if (last.current === c) setState({ kind: 'error', message: e2.message });
      }
    }
  }

  useEffect(() => {
    if (code.length === 6) lookup(code);
    else setState({ kind: 'idle' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code]);

  const found = state.kind === 'found' ? state : null;
  const share = found?.share;
  const src = found?.src;

  useEffect(() => {
    if (!share || share.ready || !src) return;
    const t = setInterval(async () => {
      try {
        const s = src.remote ? await api.remoteShare(src.remote, src.code) : await api.get(src.code);
        setState((st) => (st.kind === 'found' ? { ...st, share: s } : st));
      } catch {
        /* keep last */
      }
    }, 1500);
    return () => clearInterval(t);
  }, [share, src]);

  function logReceived() {
    if (!owner || !share || logged.current.has(share.code)) return;
    logged.current.add(share.code);
    api
      .logHistory({
        direction: 'received',
        kind: share.files.length ? 'files' : 'text',
        summary: share.files.length === 1 ? share.files[0].name : share.files.length ? `${share.files.length} files` : share.text.slice(0, 80),
        count: share.files.length,
        size: share.totalSize,
        peer: share.sender || found?.deviceName || `Code ${share.code}`,
        text: share.files.length ? undefined : share.text,
      })
      .then(onReceived)
      .catch(() => {});
  }

  function onScanned(text: string) {
    setScanning(false);
    const j = parseJoin(text);
    if (!j) {
      toast("That isn't a Ferry hotspot code");
      return;
    }
    setJoin({ state: 'connecting', name: j.name });
    bridge.joinHotspot(j.ssid, j.password, j.url);
  }

  return (
    <section className="panel stack" aria-labelledby="recv-h">
      <h2 id="recv-h">Receive</h2>
      <p className="lede" style={{ marginTop: -8 }}>
        Things sent to this device ask you to accept them. You can also type a code the sender created.
      </p>

      <CodeEntry value={code} onChange={setCode} error={state.kind === 'error'} autoFocus={false} />
      {state.kind === 'loading' ? <p className="muted">Looking up {code}…</p> : null}
      {state.kind === 'searching' ? (
        <div className="notice">
          <Radar size={18} aria-hidden />
          <span>Checking other devices on this Wi-Fi…</span>
        </div>
      ) : null}
      {state.kind === 'error' ? <div className="notice error">{state.message}</div> : null}

      {share && src ? (
        <div className="stack">
          <div>
            <h3>{share.sender ? `From ${share.sender}` : found?.deviceName ? `On ${found.deviceName}` : 'Shared with you'}</h3>
            <span className="muted small">{timeLeft(share.expiresAt, now)}</span>
          </div>
          {share.encrypted ? (
            <div className="notice">This share is locked with a password. Open it with the Ferry command line: ferry receive {share.code} --password …</div>
          ) : null}
          {!share.encrypted && share.text ? (
            <div className="stack" style={{ gap: 8 }}>
              <div className="textbox">{share.text}</div>
              <div className="row">
                <button
                  className="btn"
                  onClick={async () => {
                    if (await copyText(share.text)) toast('Copied');
                    if (!share.files.length) logReceived();
                  }}
                >
                  <Copy size={18} aria-hidden /> Copy
                </button>
                {isUrl(share.text) ? (
                  <a className="btn" href={share.text.trim()} target="_blank" rel="noreferrer">
                    <ExternalLink size={18} aria-hidden /> Open link
                  </a>
                ) : null}
              </div>
            </div>
          ) : null}
          {!share.encrypted && share.files.length ? (
            <div className="stack" style={{ gap: 8 }}>
              <ul className="files">
                {share.files.slice(0, 300).map((f, i) => {
                  const done = f.received >= f.storedSize;
                  const pk = done ? previewKind(f) : null;
                  return (
                    <li key={f.id}>
                      <span className="ficon">
                        <KindIcon name={f.name} type={f.type} />
                      </span>
                      <span className="fname">
                        <b>{f.name.split('/').pop()}</b>
                        <small>{formatBytes(f.size)}</small>
                        {!done ? (
                          <span style={{ marginTop: 6 }}>
                            <Progress value={f.received / Math.max(f.storedSize, 1)} />
                          </span>
                        ) : null}
                      </span>
                      {pk ? (
                        <button className="btn ghost icon" aria-label={`Preview ${f.name}`} onClick={() => setPreview(i)}>
                          {pk === 'video' || pk === 'audio' ? <Play size={18} aria-hidden /> : <Eye size={18} aria-hidden />}
                        </button>
                      ) : null}
                      {done ? (
                        <a className="btn icon" aria-label={`Save ${f.name}`} href={fileUrl(src, f)} download onClick={logReceived}>
                          <Download size={18} aria-hidden />
                        </a>
                      ) : null}
                    </li>
                  );
                })}
              </ul>
              <button
                className="btn primary block"
                disabled={!share.ready}
                onClick={() => {
                  triggerDownload(share.files.length === 1 ? fileUrl(src, share.files[0]) : zipUrl(src));
                  logReceived();
                }}
              >
                {share.files.length === 1 ? <Download size={20} aria-hidden /> : <PackageOpen size={20} aria-hidden />}
                {share.files.length === 1 ? 'Save file' : 'Save all as .zip'}
              </button>
            </div>
          ) : null}
        </div>
      ) : null}

      {canJoin ? (
        <div className="stack alt-box" style={{ gap: 10 }}>
          <div className="row" style={{ alignItems: 'flex-start' }}>
            <WifiOff size={22} aria-hidden style={{ marginTop: 2 }} />
            <div>
              <b>No Wi-Fi?</b>
              <p className="muted small" style={{ margin: '2px 0 0' }}>
                On the sending phone, choose Send over a hotspot. Then scan the code it shows.
              </p>
            </div>
          </div>
          {join?.state === 'connecting' ? (
            <div className="notice">Connecting to {join.name || 'the other phone'}… Android may ask you to confirm.</div>
          ) : null}
          {join?.state === 'error' ? <div className="notice error">{join.message}</div> : null}
          <button className="btn" onClick={() => setScanning(true)}>
            <ScanLine size={18} aria-hidden /> Scan the sender's code
          </button>
        </div>
      ) : null}

      {preview !== null && share && src ? (
        <Preview src={src} files={share.files} start={preview} onClose={() => setPreview(null)} />
      ) : null}
      {scanning ? <Scanner title="Scan the sender's code" onText={onScanned} onClose={() => setScanning(false)} /> : null}
    </section>
  );
}
