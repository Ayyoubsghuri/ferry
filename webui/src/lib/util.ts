export function formatBytes(n: number) {
  if (!isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function formatSpeed(bps: number) {
  return `${formatBytes(bps)}/s`;
}

export function timeLeft(iso: string, now = Date.now()) {
  const ms = new Date(iso).getTime() - now;
  if (ms <= 0) return 'Expired';
  const m = Math.round(ms / 60000);
  if (m < 1) return 'Less than a minute left';
  if (m < 60) return `${m} min left`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h left`;
  return `${Math.round(h / 24)} days left`;
}

export const isAndroidApp = () => typeof (window as any).FerryAndroid !== 'undefined';

export const isTouchDevice = () =>
  /Android|iPhone|iPad|iPod/i.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

export function supportsFolderPick() {
  if (isTouchDevice()) return false;
  const input = document.createElement('input');
  return 'webkitdirectory' in input;
}

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  }
}

/** Save a blob, using the Android app bridge when running inside the app. */
export async function saveBlob(blob: Blob, name: string) {
  const bridge = (window as any).FerryAndroid;
  if (bridge?.beginSave) {
    const id = bridge.beginSave(name, blob.type || 'application/octet-stream');
    const step = 3 * 256 * 1024; // multiple of 3 keeps base64 chunks independent
    for (let o = 0; o < blob.size; o += step) {
      const buf = new Uint8Array(await blob.slice(o, o + step).arrayBuffer());
      let s = '';
      for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode(...buf.subarray(i, i + 0x8000));
      bridge.appendChunk(id, btoa(s));
    }
    bridge.finishSave(id);
    return;
  }
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

export function triggerDownload(href: string) {
  const a = document.createElement('a');
  a.href = href;
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// Shares created on this device, so they can be watched and deleted later.
export interface MyShare {
  code: string;
  owner: string;
  createdAt: string;
  expiresAt: string;
  summary: string;
}

const KEY = 'ferry.myShares';

export function loadMyShares(): MyShare[] {
  try {
    const list: MyShare[] = JSON.parse(localStorage.getItem(KEY) || '[]');
    return list.filter((s) => new Date(s.expiresAt).getTime() > Date.now());
  } catch {
    return [];
  }
}

export function saveMyShares(list: MyShare[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(list.slice(0, 50)));
  } catch {
    /* storage unavailable */
  }
}

export function summarize(count: number, hasText: boolean, firstName?: string) {
  if (!count) return 'Text';
  const what = count === 1 ? firstName || '1 file' : `${count} files`;
  return hasText ? `${what} and text` : what;
}

/** Files/text another Android app shared to Ferry (via the app bridge). */
export async function takeAndroidShared(): Promise<{ files: File[]; text: string } | null> {
  const bridge = (window as any).FerryAndroid;
  if (!bridge?.sharedItems) return null;
  let info: { files: { name: string; size: number; type: string }[]; text: string };
  try {
    info = JSON.parse(bridge.sharedItems());
  } catch {
    return null;
  }
  if (!info.files.length && !info.text) return null;
  const files: File[] = [];
  for (let i = 0; i < info.files.length; i++) {
    const meta = info.files[i];
    const parts: BlobPart[] = [];
    let offset = 0;
    for (;;) {
      const b64: string = bridge.readShared(i, offset, 3 * 1024 * 1024);
      if (!b64) break;
      const bin = atob(b64);
      const bytes = new Uint8Array(bin.length);
      for (let j = 0; j < bin.length; j++) bytes[j] = bin.charCodeAt(j);
      parts.push(bytes);
      offset += bytes.length;
      await new Promise((r) => setTimeout(r, 0)); // keep the UI responsive
    }
    files.push(new File(parts, meta.name, { type: meta.type || 'application/octet-stream' }));
  }
  bridge.clearShared();
  return { files, text: info.text };
}
