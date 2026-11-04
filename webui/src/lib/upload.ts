import { CHUNK, sealBytes, type Key } from './crypto';
import type { Share } from './api';

export interface PickedFile {
  file: File;
  path: string; // relative path, keeps folder structure
}

export interface Progress {
  sent: number;
  total: number;
  fileIndex: number;
  speed: number; // bytes per second
  status: 'uploading' | 'retrying' | 'done' | 'error';
  error?: string;
}

const PLAIN_PIECE = 8 * 1024 * 1024;
const SEALED_CHUNKS_PER_PIECE = 4;

function put(url: string, owner: string, body: Blob | Uint8Array, onSent: (n: number) => void, signal: AbortSignal) {
  return new Promise<{ received: number }>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.setRequestHeader('X-Owner', owner);
    xhr.setRequestHeader('Content-Type', 'application/octet-stream');
    xhr.upload.onprogress = (e) => onSent(e.loaded);
    xhr.onload = () => {
      let data: any = {};
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        /* ignore */
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(Object.assign(new Error(data.error || `Upload failed (${xhr.status})`), { status: xhr.status, data }));
    };
    xhr.onerror = () => reject(Object.assign(new Error('Connection lost'), { status: 0 }));
    xhr.onabort = () => reject(Object.assign(new Error('Cancelled'), { status: -1 }));
    signal.addEventListener('abort', () => xhr.abort(), { once: true });
    xhr.send(body as XMLHttpRequestBodyInit);
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Upload every file of a share; resumes from where the server left off after network hiccups. */
export async function uploadAll(
  share: Share,
  owner: string,
  files: PickedFile[],
  key: Key | null,
  onProgress: (p: Progress) => void,
  signal: AbortSignal,
) {
  const total = share.files.reduce((n, f) => n + f.storedSize, 0);
  let base = 0; // bytes fully done in previous files
  let lastT = performance.now();
  let lastBytes = 0;
  let speed = 0;
  const report = (sent: number, fileIndex: number, status: Progress['status'] = 'uploading') => {
    const now = performance.now();
    if (now - lastT > 400) {
      const inst = ((sent - lastBytes) * 1000) / (now - lastT);
      speed = speed ? speed * 0.6 + inst * 0.4 : inst;
      lastT = now;
      lastBytes = sent;
    }
    onProgress({ sent, total, fileIndex, speed: Math.max(speed, 0), status });
  };

  for (let i = 0; i < share.files.length; i++) {
    const meta = share.files[i];
    const file = files[i].file;
    let offset = meta.received; // stored-bytes offset
    let attempts = 0;
    while (offset < meta.storedSize) {
      if (signal.aborted) throw new Error('Cancelled');
      let body: Blob | Uint8Array;
      if (key) {
        // Encrypted: offset is always on a sealed-chunk boundary.
        const sealedChunk = CHUNK + 28;
        const chunkIndex = Math.floor(offset / sealedChunk);
        offset = chunkIndex * sealedChunk; // a dropped connection can leave a partial chunk; resend it whole
        const parts: Uint8Array[] = [];
        for (let c = 0; c < SEALED_CHUNKS_PER_PIECE; c++) {
          const start = (chunkIndex + c) * CHUNK;
          if (start >= file.size) break;
          const plain = await file.slice(start, Math.min(start + CHUNK, file.size)).arrayBuffer();
          parts.push(await sealBytes(key, plain));
        }
        const len = parts.reduce((n, p) => n + p.length, 0);
        const joined = new Uint8Array(len);
        let o = 0;
        for (const p of parts) {
          joined.set(p, o);
          o += p.length;
        }
        body = joined;
      } else {
        body = file.slice(offset, Math.min(offset + PLAIN_PIECE, meta.storedSize));
      }
      try {
        const res = await put(
          `/api/t/${share.code}/f/${meta.id}?offset=${offset}`,
          owner,
          body,
          (n) => report(base + offset + n, i),
          signal,
        );
        offset = res.received;
        attempts = 0;
        report(base + offset, i);
      } catch (e: any) {
        if (e.status === -1 || signal.aborted) throw new Error('Cancelled');
        if (e.status === 409 && typeof e.data?.received === 'number') {
          offset = e.data.received;
          continue;
        }
        if (e.status === 404 || e.status === 403 || e.status === 413) throw e;
        attempts++;
        if (attempts > 8) throw e;
        report(base + offset, i, 'retrying');
        await sleep(Math.min(1000 * 2 ** attempts, 15000));
      }
    }
    base += meta.storedSize;
  }
  onProgress({ sent: total, total, fileIndex: share.files.length - 1, speed, status: 'done' });
}

/** Turn a drop event into files, walking into dropped folders. */
export async function filesFromDrop(dt: DataTransfer): Promise<PickedFile[]> {
  const out: PickedFile[] = [];
  const items = Array.from(dt.items || []);
  const entries = items.map((it) => (it as any).webkitGetAsEntry?.()).filter(Boolean);
  if (!entries.length) {
    return Array.from(dt.files).map((f) => ({ file: f, path: f.name }));
  }
  const walk = async (entry: any, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file: File = await new Promise((res, rej) => entry.file(res, rej));
      out.push({ file, path: prefix + file.name });
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      for (;;) {
        const batch: any[] = await new Promise((res, rej) => reader.readEntries(res, rej));
        if (!batch.length) break;
        for (const e of batch) await walk(e, prefix + entry.name + '/');
      }
    }
  };
  for (const e of entries) await walk(e, '');
  return out;
}
