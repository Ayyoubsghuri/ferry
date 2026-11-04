// End-to-end encryption: everything is locked in the sender's browser and unlocked in
// the receiver's. The server only ever stores ciphertext.
//
// File format: the plaintext is split into 1 MiB chunks; each is stored as
// [12-byte IV][AES-256-GCM ciphertext + 16-byte tag].
//
// Browsers only offer WebCrypto on https:// or localhost. Ferry serves plain http:// to
// other browsers (so there's no certificate warning), so the same algorithms are also
// implemented in JavaScript and used whenever WebCrypto isn't available. Both produce
// identical output, so a share locked on one can be unlocked on the other.

import { gcm } from '@noble/ciphers/aes';
import { pbkdf2Async } from '@noble/hashes/pbkdf2';
import { sha256 } from '@noble/hashes/sha256';

export const CHUNK = 1024 * 1024;
const OVERHEAD = 12 + 16;
const CHECK_TEXT = 'ferry-ok';
const ITERATIONS = 250_000;

export interface Key {
  raw: Uint8Array; // 32 bytes
  subtle?: CryptoKey;
}

const hasSubtle = () => typeof crypto !== 'undefined' && !!crypto.subtle;

/** Locking works everywhere now; kept for callers that still ask. */
export const cryptoAvailable = () => typeof crypto !== 'undefined' && !!crypto.getRandomValues;

export function encryptedSize(size: number) {
  return size + Math.ceil(size / CHUNK) * OVERHEAD;
}

const enc = new TextEncoder();
const dec = new TextDecoder();

export function toB64(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

export function fromB64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export async function deriveKey(password: string, saltB64: string): Promise<Key> {
  const salt = fromB64(saltB64);
  let raw: Uint8Array;
  if (hasSubtle()) {
    const base = await crypto.subtle.importKey('raw', enc.encode(password), 'PBKDF2', false, ['deriveBits']);
    raw = new Uint8Array(
      await crypto.subtle.deriveBits({ name: 'PBKDF2', salt, iterations: ITERATIONS, hash: 'SHA-256' }, base, 256),
    );
  } else {
    raw = await pbkdf2Async(sha256, enc.encode(password), salt, { c: ITERATIONS, dkLen: 32, asyncTick: 20 });
  }
  const key: Key = { raw };
  if (hasSubtle()) {
    key.subtle = await crypto.subtle.importKey('raw', raw, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt']);
  }
  return key;
}

export function newSalt() {
  return toB64(crypto.getRandomValues(new Uint8Array(16)));
}

export async function sealBytes(key: Key, plain: ArrayBuffer | Uint8Array): Promise<Uint8Array> {
  const iv = crypto.getRandomValues(new Uint8Array(12));
  const data = plain instanceof Uint8Array ? plain : new Uint8Array(plain);
  const ct = key.subtle
    ? new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key.subtle, data))
    : gcm(key.raw, iv).encrypt(data);
  const out = new Uint8Array(12 + ct.length);
  out.set(iv);
  out.set(ct, 12);
  return out;
}

export async function openBytes(key: Key, sealed: Uint8Array): Promise<Uint8Array> {
  const iv = sealed.subarray(0, 12);
  const body = sealed.subarray(12);
  if (key.subtle) return new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv }, key.subtle, body));
  return gcm(key.raw, iv.slice()).decrypt(body.slice());
}

export async function sealText(key: Key, text: string) {
  return toB64(await sealBytes(key, enc.encode(text)));
}

export async function openText(key: Key, b64: string) {
  return dec.decode(await openBytes(key, fromB64(b64)));
}

export const makeCheck = (key: Key) => sealText(key, CHECK_TEXT);

export async function verifyPassword(key: Key, check: string) {
  try {
    return (await openText(key, check)) === CHECK_TEXT;
  } catch {
    return false;
  }
}

/** Download an encrypted file and unlock it chunk by chunk. */
export async function fetchAndDecrypt(
  url: string,
  key: Key,
  storedSize: number,
  onProgress: (done: number) => void,
  signal?: AbortSignal,
): Promise<Blob> {
  const res = await fetch(url, { signal });
  if (!res.ok || !res.body) throw new Error(`Download failed (${res.status})`);
  const reader = res.body.getReader();
  const SEALED = CHUNK + OVERHEAD;
  const parts: BlobPart[] = [];
  let buf = new Uint8Array(0);
  let got = 0;
  const flush = async (final: boolean) => {
    while (buf.length >= SEALED || (final && buf.length > 0)) {
      const take = Math.min(SEALED, buf.length);
      parts.push(await openBytes(key, buf.subarray(0, take)));
      buf = buf.slice(take);
    }
  };
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    got += value.length;
    const next = new Uint8Array(buf.length + value.length);
    next.set(buf);
    next.set(value, buf.length);
    buf = next;
    await flush(false);
    onProgress(got / Math.max(storedSize, 1));
  }
  await flush(true);
  return new Blob(parts);
}
