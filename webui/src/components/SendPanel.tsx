import { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowUpFromLine, FolderUp, Plus, X, Send, Check, RefreshCw, Globe } from 'lucide-react';
import { api, guestApi, type Device, type Me, type Outgoing } from '../lib/api';
import { filesFromDrop, uploadAll, type PickedFile, type Progress } from '../lib/upload';
import { formatBytes, isAndroidApp, supportsFolderPick, takeAndroidShared } from '../lib/util';
import { KindIcon, Progress as Bar } from './common';
import { PlatformIcon } from './DevicesPanel';

type Phase = 'compose' | 'uploading' | 'sending' | Outgoing['state'];

interface Job {
  to: Device;
  phase: Phase;
  progress: number; // 0..1 of the current phase
  outId?: string;
  error?: string;
  summary: string;
}

/**
 * Pick files or write a note, choose a device, send. The other device gets an
 * Accept / Refuse prompt; this screen follows along until it's received.
 */
export function SendPanel({ me, active, devicesVersion }: { me: Me | null; active: boolean; devicesVersion: number }) {
  const owner = !!me?.owner;
  const [files, setFiles] = useState<PickedFile[]>([]);
  const [text, setText] = useState('');
  const [over, setOver] = useState(false);
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [looking, setLooking] = useState(false);
  const [target, setTarget] = useState<string | null>(null);
  const [job, setJob] = useState<Job | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const abortRef = useRef<AbortController | null>(null);
  const canFolder = supportsFolderPick() && !isAndroidApp();

  const addFiles = useCallback((list: PickedFile[]) => {
    if (!list.length) return;
    setFiles((prev) => {
      const seen = new Set(prev.map((p) => p.path + p.file.size));
      return [...prev, ...list.filter((p) => !seen.has(p.path + p.file.size))];
    });
  }, []);

  // Devices on the network.
  const load = useCallback(async (refresh?: '1' | 'scan') => {
    if (refresh) setLooking(true);
    try {
      const d = await api.devices(refresh);
      const list = [...d.paired.filter((p) => p.online), ...d.nearby];
      setDevices(list);
      setTarget((t) => (t && list.some((x) => x.id === t) ? t : list.length === 1 ? list[0].id : t));
    } catch {
      /* offline */
    } finally {
      setLooking(false);
    }
  }, []);
  useEffect(() => {
    load('1');
    const t = setInterval(() => load(), 4000);
    return () => clearInterval(t);
  }, [load, devicesVersion]);

  // Paste files or text anywhere.
  useEffect(() => {
    if (!active || job) return;
    const onPaste = (e: ClipboardEvent) => {
      const dt = e.clipboardData;
      if (!dt) return;
      const pasted = Array.from(dt.files);
      if (pasted.length) {
        e.preventDefault();
        addFiles(pasted.map((f) => ({ file: f, path: f.name })));
        return;
      }
      if ((e.target as HTMLElement)?.closest('input, textarea')) return;
      const t = dt.getData('text');
      if (t) {
        e.preventDefault();
        setText((cur) => (cur ? cur + '\n' : '') + t);
      }
    };
    document.addEventListener('paste', onPaste);
    return () => document.removeEventListener('paste', onPaste);
  }, [active, job, addFiles]);

  // Files shared to the Android app from other apps.
  useEffect(() => {
    if (!isAndroidApp()) return;
    let reading = false;
    const pull = async () => {
      if (reading) return;
      reading = true;
      try {
        const got = await takeAndroidShared();
        if (!got) return;
        setJob(null);
        if (got.files.length) addFiles(got.files.map((f) => ({ file: f, path: f.name })));
        if (got.text) setText(got.text);
        window.dispatchEvent(new Event('ferry-show-send'));
      } finally {
        reading = false;
      }
    };
    pull();
    window.addEventListener('ferry-shared', pull);
    return () => window.removeEventListener('ferry-shared', pull);
  }, [addFiles]);

  // Follow the request until it's answered.
  useEffect(() => {
    if (!job?.outId || ['done', 'declined', 'failed', 'cancelled'].includes(job.phase)) return;
    const t = setInterval(async () => {
      try {
        const list = owner ? (await api.outbox()).outgoing : (await guestApi.poll()).outgoing;
        const o = list.find((x) => x.id === job.outId);
        if (!o) return;
        setJob((j) => (j ? { ...j, phase: o.state, progress: o.progress, error: o.error } : j));
      } catch {
        /* keep polling */
      }
    }, 900);
    return () => clearInterval(t);
  }, [job, owner]);

  const totalSize = files.reduce((n, f) => n + f.file.size, 0);
  const hasContent = files.length > 0 || text.trim().length > 0;
  const chosen = devices?.find((d) => d.id === target) || null;

  async function send() {
    if (!chosen || !hasContent) return;
    const summary =
      files.length === 0 ? 'Text' : files.length === 1 ? files[0].path.split('/').pop()! : `${files.length} files`;
    setJob({ to: chosen, phase: files.length ? 'uploading' : 'sending', progress: 0, summary });
    try {
      const share = await api.create({
        text,
        expiresMinutes: 60,
        sender: me?.name || '',
        files: files.map((f) => ({ name: f.path, size: f.file.size, storedSize: f.file.size, type: f.file.type })),
      });
      if (files.length) {
        const ac = new AbortController();
        abortRef.current = ac;
        await uploadAll(share, share.owner!, files, null, (p: Progress) => {
          setJob((j) => (j ? { ...j, progress: p.sent / Math.max(p.total, 1) } : j));
        }, ac.signal);
      }
      setJob((j) => (j ? { ...j, phase: 'sending', progress: 0 } : j));
      const out =
        chosen.id === 'host'
          ? await guestApi.send(share.code, share.owner!)
          : await api.send(share.code, share.owner!, chosen.id);
      setJob((j) => (j ? { ...j, phase: (out.state as Phase) || 'waiting', outId: out.id } : j));
    } catch (e: any) {
      if (e.message === 'Cancelled') {
        setJob(null);
        return;
      }
      setJob((j) => (j ? { ...j, phase: 'failed', error: e.message } : j));
    }
  }

  async function cancel() {
    abortRef.current?.abort();
    if (job?.outId) {
      await (owner ? api.cancelSend(job.outId) : guestApi.cancel(job.outId)).catch(() => {});
    }
    setJob((j) => (j ? { ...j, phase: 'cancelled' } : j));
  }

  function reset(keep = false) {
    setJob(null);
    if (!keep) {
      setFiles([]);
      setText('');
    }
  }

  if (job) {
    const finished = ['done', 'declined', 'failed', 'cancelled'].includes(job.phase);
    const line =
      job.phase === 'uploading'
        ? `Preparing ${job.summary}…`
        : job.phase === 'sending'
          ? `Contacting ${job.to.name}…`
          : job.phase === 'waiting'
            ? `Waiting for ${job.to.name} to accept`
            : job.phase === 'accepted' || job.phase === 'saving'
              ? `${job.to.name} is receiving`
              : job.phase === 'done'
                ? `${job.to.name} received it`
                : job.phase === 'declined'
                  ? `${job.to.name} refused`
                  : job.phase === 'cancelled'
                    ? 'Cancelled'
                    : job.error || `Couldn't send to ${job.to.name}`;
    const showBar = job.phase === 'uploading' || job.phase === 'saving' || (job.phase === 'accepted' && job.progress > 0);
    return (
      <section className="panel stack" aria-labelledby="send-h">
        <div className={`delivery ${job.phase === 'done' ? 'ok' : job.phase === 'declined' || job.phase === 'failed' ? 'bad' : ''}`}>
          <span className="delivery-icon">
            {job.phase === 'done' ? <Check size={30} aria-hidden /> : <PlatformIcon platform={job.to.platform} size={30} />}
          </span>
          <h2 id="send-h">{line}</h2>
          <p className="muted" style={{ margin: 0 }}>
            {job.summary}
            {files.length ? `, ${formatBytes(totalSize)}` : ''}
          </p>
          {showBar ? (
            <div style={{ width: '100%', maxWidth: 360 }}>
              <Bar value={job.progress} />
            </div>
          ) : null}
          {job.phase === 'waiting' ? <span className="pulse" aria-hidden /> : null}
        </div>
        {finished ? (
          <div className="row">
            {job.phase !== 'done' ? (
              <button className="btn" style={{ flex: 1 }} onClick={() => reset(true)}>
                Back
              </button>
            ) : null}
            <button className="btn primary" style={{ flex: 2 }} onClick={() => reset()}>
              <Plus size={20} aria-hidden /> Send something else
            </button>
          </div>
        ) : (
          <button className="btn" onClick={cancel}>
            Cancel
          </button>
        )}
      </section>
    );
  }

  return (
    <section className="panel stack" aria-labelledby="send-h">
      <h2 id="send-h">Send</h2>

      <div
        className="drop"
        data-over={over}
        onDragOver={(e) => {
          e.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={async (e) => {
          e.preventDefault();
          setOver(false);
          addFiles(await filesFromDrop(e.dataTransfer));
        }}
      >
        <div className="drop-icon" aria-hidden>
          <ArrowUpFromLine size={24} />
        </div>
        <span className="drop-title">{files.length ? 'Add more' : 'Choose files'}</span>
        <span className="muted small desktop-only">or drag them here</span>
        <div className="drop-actions">
          <button className="btn" onClick={() => fileInput.current?.click()}>
            <Plus size={18} aria-hidden /> Files
          </button>
          {canFolder ? (
            <button className="btn" onClick={() => folderInput.current?.click()}>
              <FolderUp size={18} aria-hidden /> Folder
            </button>
          ) : null}
        </div>
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            addFiles(Array.from(e.target.files || []).map((f) => ({ file: f, path: f.name })));
            e.target.value = '';
          }}
        />
        <input
          ref={folderInput}
          type="file"
          hidden
          {...({ webkitdirectory: '', directory: '' } as any)}
          onChange={(e) => {
            addFiles(Array.from(e.target.files || []).map((f) => ({ file: f, path: (f as any).webkitRelativePath || f.name })));
            e.target.value = '';
          }}
        />
      </div>

      {files.length ? (
        <div className="stack" style={{ gap: 8 }}>
          <div className="files-summary">
            <span>
              {files.length} {files.length === 1 ? 'file' : 'files'}, {formatBytes(totalSize)}
            </span>
            <button className="btn ghost small" style={{ minHeight: 32 }} onClick={() => setFiles([])}>
              Clear all
            </button>
          </div>
          <ul className="files" style={{ maxHeight: 240, overflow: 'auto' }}>
            {files.slice(0, 200).map((f, i) => (
              <li key={f.path + i}>
                <span className="ficon">
                  <KindIcon name={f.path} type={f.file.type} />
                </span>
                <span className="fname">
                  <b>{f.path.split('/').pop()}</b>
                  <small>
                    {formatBytes(f.file.size)}
                    {f.path.includes('/') ? ` in ${f.path.split('/').slice(0, -1).join('/')}` : ''}
                  </small>
                </span>
                <button
                  className="btn ghost icon"
                  aria-label={`Remove ${f.path}`}
                  onClick={() => setFiles((prev) => prev.filter((_, j) => j !== i))}
                >
                  <X size={18} aria-hidden />
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      <div className="field">
        <label htmlFor="send-text">{files.length ? 'Note (optional)' : 'Or send text'}</label>
        <textarea
          id="send-text"
          className="textarea"
          placeholder={files.length ? 'Add a note for the receiver' : 'A link, a note, a Wi-Fi password…'}
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={2}
        />
      </div>

      <div className="stack" style={{ gap: 8 }}>
        <div className="row" style={{ justifyContent: 'space-between' }}>
          <span className="label" id="to-l">
            Send to
          </span>
          {owner ? (
            <button className="btn ghost" style={{ minHeight: 34 }} disabled={looking} onClick={() => load('scan')}>
              <RefreshCw size={15} aria-hidden /> {looking ? 'Looking…' : 'Refresh'}
            </button>
          ) : null}
        </div>
        {devices === null ? (
          <p className="muted small">Looking for devices…</p>
        ) : devices.length === 0 ? (
          <div className="empty-devices">
            <b>No devices found</b>
            <span className="small muted">
              Open Ferry on the other device, on the same Wi-Fi. A phone or computer without Ferry can open this
              device's address in a browser and will show up here too.
            </span>
          </div>
        ) : (
          <div className="device-pick" role="radiogroup" aria-labelledby="to-l">
            {devices.map((d) => (
              <button
                key={d.id}
                role="radio"
                aria-checked={target === d.id}
                className="device-option"
                onClick={() => setTarget(d.id)}
              >
                <span className="dev-icon">
                  {d.platform === 'browser' ? <Globe size={20} aria-hidden /> : <PlatformIcon platform={d.platform} />}
                </span>
                <span className="device-option-text">
                  <b className="ellipsis">{d.name}</b>
                  <small>
                    {d.id === 'host' ? 'Connected' : d.platform === 'browser' ? 'Browser' : d.paired ? 'Paired' : 'Nearby'}
                    {d.autoAccept ? ', receives without asking' : ''}
                  </small>
                </span>
                <i className="radio" aria-hidden />
              </button>
            ))}
          </div>
        )}
      </div>

      <button className="btn primary block" disabled={!hasContent || !chosen} onClick={send}>
        <Send size={20} aria-hidden />
        {!hasContent ? 'Choose files or write something' : !chosen ? 'Choose a device' : `Send to ${chosen.name}`}
      </button>
    </section>
  );
}
