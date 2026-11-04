import { useEffect, useState } from 'react';
import { ChevronLeft, ChevronRight, Download, X } from 'lucide-react';
import { api, fileUrl, type ShareFile, type Source } from '../lib/api';
import { formatBytes, isAndroidApp } from '../lib/util';
import { fileKind } from './common';

export function previewKind(f: ShareFile): 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'zip' | null {
  const k = fileKind(f.name, f.type);
  const ext = f.name.split('.').pop()?.toLowerCase() || '';
  if (k === 'image' && ext !== 'heic' && ext !== 'svg') return 'image';
  if (k === 'video' && ['mp4', 'webm', 'mov', 'm4v'].includes(ext)) return 'video';
  if (k === 'audio') return 'audio';
  if (ext === 'pdf' && !isAndroidApp()) return 'pdf';
  if (ext === 'zip') return 'zip';
  if (['txt', 'md', 'csv', 'log', 'json', 'xml', 'yml', 'yaml', 'ini', 'srt'].includes(ext) || f.type?.startsWith('text/'))
    return 'text';
  return null;
}

/** Plays or shows a file straight from the sender, before (or instead of) saving it. */
export function Preview({
  src,
  files,
  start,
  onClose,
}: {
  src: Source;
  files: ShareFile[];
  start: number;
  onClose: () => void;
}) {
  const viewable = files.filter((f) => previewKind(f));
  const [i, setI] = useState(Math.max(0, viewable.findIndex((f) => f.id === files[start]?.id)));
  const f = viewable[i];
  const kind = f ? previewKind(f) : null;
  const url = f ? fileUrl(src, f, true) : '';
  const [text, setText] = useState<string | null>(null);
  const [zip, setZip] = useState<{ entries: { name: string; size: number; dir: boolean }[]; total: number } | null>(null);
  const [zipErr, setZipErr] = useState<string | null>(null);

  useEffect(() => {
    setZip(null);
    setZipErr(null);
    if (kind !== 'zip' || !f) return;
    api
      .zipIndex(src, f.id)
      .then(setZip)
      .catch((e) => setZipErr(e.message));
  }, [kind, f, src]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
      if (e.key === 'ArrowRight') setI((n) => Math.min(viewable.length - 1, n + 1));
      if (e.key === 'ArrowLeft') setI((n) => Math.max(0, n - 1));
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose, viewable.length]);

  useEffect(() => {
    setText(null);
    if (kind !== 'text') return;
    const ac = new AbortController();
    fetch(url, { headers: { Range: 'bytes=0-262143' }, signal: ac.signal })
      .then((r) => r.text())
      .then(setText)
      .catch(() => setText("Couldn't load a preview."));
    return () => ac.abort();
  }, [url, kind]);

  if (!f) return null;
  return (
    <div className="viewer" role="dialog" aria-modal="true" aria-label={`Preview of ${f.name}`}>
      <header className="viewer-bar">
        <div style={{ minWidth: 0, flex: 1 }}>
          <b className="viewer-name">{f.name.split('/').pop()}</b>
          <span className="small" style={{ opacity: 0.75 }}>
            {formatBytes(f.size)}
            {viewable.length > 1 ? `, ${i + 1} of ${viewable.length}` : ''}
          </span>
        </div>
        <a className="btn ghost icon viewer-btn" href={fileUrl(src, f)} download aria-label="Save this file">
          <Download size={20} aria-hidden />
        </a>
        <button className="btn ghost icon viewer-btn" onClick={onClose} aria-label="Close preview">
          <X size={22} aria-hidden />
        </button>
      </header>
      <div className="viewer-stage">
        {kind === 'image' ? <img src={url} alt={f.name} /> : null}
        {kind === 'video' ? <video key={url} src={url} controls autoPlay playsInline /> : null}
        {kind === 'audio' ? <audio key={url} src={url} controls autoPlay /> : null}
        {kind === 'pdf' ? <iframe title={f.name} src={url} /> : null}
        {kind === 'text' ? <pre className="viewer-text">{text ?? 'Loading…'}</pre> : null}
        {kind === 'zip' ? (
          <div className="viewer-text">
            {zipErr ? (
              zipErr
            ) : !zip ? (
              'Reading the archive…'
            ) : (
              <>
                <p style={{ margin: '0 0 10px', opacity: 0.8 }}>
                  {zip.total} {zip.total === 1 ? 'item' : 'items'} inside
                  {zip.total > zip.entries.length ? `, showing the first ${zip.entries.length}` : ''}
                </p>
                <ul className="zip-list">
                  {zip.entries.map((e) => (
                    <li key={e.name}>
                      <span>{e.name}</span>
                      <span style={{ opacity: 0.7 }}>{e.dir ? '' : formatBytes(e.size)}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </div>
        ) : null}
        {viewable.length > 1 ? (
          <>
            <button
              className="viewer-nav prev"
              aria-label="Previous"
              disabled={i === 0}
              onClick={() => setI((n) => Math.max(0, n - 1))}
            >
              <ChevronLeft size={28} aria-hidden />
            </button>
            <button
              className="viewer-nav next"
              aria-label="Next"
              disabled={i === viewable.length - 1}
              onClick={() => setI((n) => Math.min(viewable.length - 1, n + 1))}
            >
              <ChevronRight size={28} aria-hidden />
            </button>
          </>
        ) : null}
      </div>
    </div>
  );
}
