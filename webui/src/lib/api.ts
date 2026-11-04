export interface ShareFile {
  id: string;
  name: string;
  size: number;
  storedSize: number;
  type?: string;
  received: number;
}

export interface Share {
  code: string;
  sender: string;
  createdAt: string;
  expiresAt: string;
  text: string;
  encrypted: boolean;
  salt: string;
  check: string;
  burnAfter: boolean;
  downloads: number;
  views: number;
  files: ShareFile[];
  ready: boolean;
  totalSize: number;
  isOwner: boolean;
  owner?: string;
}

export interface Info {
  app: string;
  name: string;
  version: string;
  platform: string;
  port: number;
  local: boolean;
  mdnsUrl: string;
  lanUrls: string[];
  transfers: number;
}

export interface Peer {
  name: string;
  url: string;
}

export class ApiError extends Error {
  status: number;
  body: any;
  constructor(status: number, message: string, body?: any) {
    super(message);
    this.status = status;
    this.body = body;
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch {
    throw new ApiError(0, "Can't reach Ferry. Check that the app is still running and you're on the same network.");
  }
  const text = await res.text();
  let body: any = null;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    /* not JSON */
  }
  if (!res.ok) throw new ApiError(res.status, body?.error || `Request failed (${res.status})`, body);
  return body as T;
}

export interface Me {
  id: string;
  name: string;
  platform: string;
  owner: boolean;
  locked: boolean;
  settings?: {
    requirePin: boolean;
    accessPin: string;
    watchClipboard: boolean;
    autoCopyClipboard: boolean;
    allowNearby: boolean;
    startAtLogin: boolean;
  };
  startAtLoginSupported?: boolean;
  clipboardSupported?: boolean;
  saveDir?: string;
  canOpenFolder?: boolean;
}

export interface Device {
  id: string;
  name: string;
  platform: string;
  url?: string;
  online: boolean;
  paired: boolean;
  locked?: boolean;
  autoAccept?: boolean;
  syncClipboard?: boolean;
}

export interface Offer {
  id: string;
  fromId: string;
  fromName: string;
  fromPlatform: string;
  kind: 'files' | 'text';
  code: string;
  url: string;
  summary: string;
  count: number;
  size: number;
  text?: string;
  files?: ShareFile[];
  createdAt: string;
  state: 'pending' | 'accepted' | 'saving' | 'done' | 'declined' | 'failed' | 'cancelled';
  progress: number;
  savedPath?: string;
  error?: string;
  auto?: boolean;
  unpaired?: boolean;
  token?: string;
  local?: boolean;
}

export interface Outgoing {
  id: string;
  toId: string;
  toName: string;
  code: string;
  unpaired?: boolean;
  state: 'waiting' | 'accepted' | 'saving' | 'done' | 'declined' | 'failed' | 'cancelled';
  progress: number;
  error?: string;
}

export interface Clip {
  seq: number;
  fromName: string;
  text: string;
  at: string;
}

export interface HistoryEntry {
  id: string;
  at: string;
  direction: 'sent' | 'received';
  kind: 'files' | 'text' | 'clipboard';
  summary: string;
  count: number;
  size: number;
  peer?: string;
  code?: string;
  path?: string;
  text?: string;
  status?: string;
}

export interface Message {
  id: string;
  peerId: string;
  peerName: string;
  out: boolean;
  text: string;
  at: string;
  read: boolean;
  unpaired?: boolean;
  failed?: boolean;
}

export interface Conversation {
  peerId: string;
  peerName: string;
  unread: number;
  last: Message;
}

const json = (method: string, body?: unknown): RequestInit => ({
  method,
  headers: { 'Content-Type': 'application/json' },
  body: body === undefined ? undefined : JSON.stringify(body),
});

export const api = {
  info: () => req<Info>('/api/info'),
  me: () => req<Me>('/api/me'),
  unlock: (pin: string) => req<{ ok: boolean }>('/api/unlock', json('POST', { pin })),
  settings: (patch: Record<string, unknown>) => req<NonNullable<Me['settings']>>('/api/settings', json('PUT', patch)),
  create: (payload: unknown) => req<Share>('/api/t', json('POST', payload)),
  get: (code: string, owner?: string) =>
    req<Share>(`/api/t/${encodeURIComponent(code)}`, owner ? { headers: { 'X-Owner': owner } } : undefined),
  remove: (code: string, owner: string) =>
    req<{ deleted: boolean }>(`/api/t/${encodeURIComponent(code)}`, { method: 'DELETE', headers: { 'X-Owner': owner } }),
  find: (code: string) =>
    req<{ where: 'here' | 'peer'; url?: string; name?: string; id?: string }>(`/api/find/${encodeURIComponent(code)}`),
  remoteShare: (base: string, code: string) =>
    req<Share>(`/api/remote/share?u=${encodeURIComponent(base)}&code=${code}`),
  devices: (refresh?: '1' | 'scan') =>
    req<{ paired: Device[]; nearby: Device[] }>(`/api/devices${refresh ? `?refresh=${refresh}` : ''}`),
  updateDevice: (id: string, patch: Partial<Device>) => req(`/api/devices/${id}`, json('PATCH', patch)),
  unpair: (id: string) => req(`/api/devices/${id}`, { method: 'DELETE' }),
  pairStart: () => req<{ pin: string; qr: string; expiresAt: string }>('/api/pair/start', json('POST', {})),
  pairStatus: () =>
    req<{ active: boolean; expiresAt: string; paired?: { id: string; name: string; platform: string } }>('/api/pair/status'),
  pairCancel: () => req('/api/pair', { method: 'DELETE' }),
  pairJoin: (p: { url: string; token?: string; pin?: string; fp?: string }) =>
    req<{ device: Device }>('/api/pair/join', json('POST', p)),
  send: (code: string, owner: string, deviceId: string) =>
    req<Outgoing>('/api/send', json('POST', { code, owner, deviceId })),
  outbox: () => req<{ outgoing: Outgoing[] }>('/api/outbox'),
  inbox: () => req<{ offers: Offer[] }>('/api/inbox'),
  accept: (id: string) => req<{ state: string; saveDir: string }>(`/api/inbox/${id}/accept`, json('POST', {})),
  decline: (id: string) => req(`/api/inbox/${id}/decline`, json('POST', {})),
  offerDone: (id: string, ok: boolean, error?: string) => req(`/api/inbox/${id}/done`, json('POST', { ok, error })),
  dismiss: (id: string) => req(`/api/inbox/${id}`, { method: 'DELETE' }),
  clipboardSend: (p: { text?: string; fromSystem?: boolean; deviceIds?: string[] }) =>
    req<{ sent: string[] | null; failed: { name: string; error: string }[] | null }>('/api/clipboard', json('POST', p)),
  clipboard: (since: number) => req<{ seq: number; clips: Clip[]; autoCopied: boolean }>(`/api/clipboard?since=${since}`),
  history: () => req<{ entries: HistoryEntry[] }>('/api/history'),
  logHistory: (e: Partial<HistoryEntry>) => req('/api/history', json('POST', e)),
  clearHistory: () => req('/api/history', { method: 'DELETE' }),
  openFolder: (path?: string) => req('/api/open-folder', json('POST', { path })),
  cancelSend: (id: string) => req(`/api/outbox/${id}/cancel`, json('POST', {})),
  conversations: () => req<{ conversations: Conversation[] }>('/api/messages'),
  thread: (peerId: string) => req<{ messages: Message[] }>(`/api/messages?peer=${encodeURIComponent(peerId)}`),
  sendMessage: (peerId: string, text: string) => req<{ message: Message }>('/api/messages', json('POST', { peerId, text })),
  markRead: (peerId: string) => req('/api/messages/read', json('POST', { peerId })),
  zipIndex: (src: Source, id: string) =>
    req<{ entries: { name: string; size: number; dir: boolean }[]; total: number }>(
      src.remote
        ? `/api/remote/zipindex?u=${encodeURIComponent(src.remote)}&code=${src.code}&id=${id}${src.token ? `&t=${src.token}` : ''}`
        : `/api/t/${src.code}/zipindex/${id}`,
    ),
};

/** Where a share's files are served from: this device, or another device via the proxy. */
export interface Source {
  code: string;
  remote?: string; // base URL of the device that holds the share
  token?: string; // lets a direct send be fetched from a device that's locked with a PIN
}

export function fileUrl(src: Source | string, f: ShareFile, inline = false) {
  const s: Source = typeof src === 'string' ? { code: src } : src;
  const base = f.name.split('/').pop() || 'file';
  if (s.remote) {
    return `/api/remote/file?u=${encodeURIComponent(s.remote)}&code=${s.code}&id=${f.id}${inline ? '&inline=1' : ''}${s.token ? `&t=${s.token}` : ''}&n=${encodeURIComponent(base)}`;
  }
  return `/api/t/${s.code}/f/${f.id}/${encodeURIComponent(base)}${inline ? '?inline=1' : ''}`;
}

export function zipUrl(src: Source | string) {
  const s: Source = typeof src === 'string' ? { code: src } : src;
  return s.remote
    ? `/api/remote/zip?u=${encodeURIComponent(s.remote)}&code=${s.code}${s.token ? `&t=${s.token}` : ''}`
    : `/api/t/${s.code}/zip`;
}

// ---------- browsers without the app ----------

export interface GuestPoll {
  offers: Offer[];
  outgoing: Outgoing[];
}

export const guestApi = {
  hello: (name: string) =>
    req<{ id: string; name: string; host: { id: string; name: string; platform: string } }>(
      '/api/guest/hello',
      json('POST', { name }),
    ),
  poll: () => req<GuestPoll>('/api/guest/poll'),
  answer: (id: string, action: 'accept' | 'decline' | 'done' | 'dismiss') =>
    req(`/api/guest/offer/${id}/${action}`, json('POST', {})),
  send: (code: string, owner: string) =>
    req<{ id: string; toName: string; state: string }>('/api/guest/send', json('POST', { code, owner })),
  cancel: (id: string) => req(`/api/guest/cancel/${id}`, json('POST', {})),
};

/** A friendly name for this browser, e.g. "Chrome on Android". */
export function browserName() {
  const ua = navigator.userAgent;
  const os = /Android/.test(ua)
    ? 'Android'
    : /iPhone/.test(ua)
      ? 'iPhone'
      : /iPad/.test(ua)
        ? 'iPad'
        : /Windows/.test(ua)
          ? 'Windows'
          : /Mac OS X/.test(ua)
            ? 'Mac'
            : /Linux/.test(ua)
              ? 'Linux'
              : 'a device';
  const b = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\//.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : 'Browser';
  return `${b} on ${os}`;
}
