import { useState } from 'react';
import { Copy, Gauge, X } from 'lucide-react';
import { type Info } from '../lib/api';
import { copyText, formatSpeed } from '../lib/util';
import { Sheet, toast } from './common';

async function measure(): Promise<{ down: number; up: number; ping: number }> {
  const t0 = performance.now();
  await fetch('/api/info', { cache: 'no-store' });
  const ping = performance.now() - t0;
  const mb = 16;
  let t = performance.now();
  const res = await fetch(`/api/speed?mb=${mb}`, { cache: 'no-store' });
  await res.arrayBuffer();
  const down = (mb * 1024 * 1024) / ((performance.now() - t) / 1000);
  const body = new Uint8Array(8 * 1024 * 1024);
  t = performance.now();
  await fetch('/api/speed', { method: 'POST', body });
  const up = body.length / ((performance.now() - t) / 1000);
  return { down, up, ping };
}

/** This device's name and the addresses a browser can open to reach it. */
export function DeviceSheet({ info, onClose }: { info: Info | null; onClose: () => void }) {
  const [speed, setSpeed] = useState<{ down: number; up: number; ping: number } | null>(null);
  const [testing, setTesting] = useState(false);
  const addresses = [...(info?.mdnsUrl ? [info.mdnsUrl] : []), ...(info?.lanUrls || [])];

  return (
    <Sheet title={info?.name || 'This device'} onClose={onClose}>
      <div className="stack">
        <div className="sheet-head">
          <div>
            <h2>{info?.name || 'This device'}</h2>
            <p className="muted" style={{ margin: '6px 0 0' }}>
              A phone or computer without Ferry can open one of these addresses in its browser. It then appears in your
              device list and can send and receive.
            </p>
          </div>
          <button className="btn ghost icon" aria-label="Close" onClick={onClose}>
            <X size={20} aria-hidden />
          </button>
        </div>

        <div className="stack" style={{ gap: 8 }}>
          {addresses.length ? (
            addresses.map((a) => (
              <div className="addr" key={a}>
                <span>{a.replace(/^https?:\/\//, '')}</span>
                <button
                  className="btn ghost icon"
                  aria-label={`Copy ${a}`}
                  onClick={async () => {
                    if (await copyText(a)) toast('Address copied');
                  }}
                >
                  <Copy size={18} aria-hidden />
                </button>
              </div>
            ))
          ) : (
            <div className="notice error">This device isn't on a network. Connect to Wi-Fi to share.</div>
          )}
        </div>

        <div className="stack" style={{ gap: 6 }}>
          <h3 style={{ margin: 0, fontSize: 'var(--step-1)', fontStretch: '108%' }}>Connection speed</h3>
          {speed ? (
            <p style={{ margin: 0 }}>
              Download {formatSpeed(speed.down)}, upload {formatSpeed(speed.up)}, response {Math.round(speed.ping)} ms
            </p>
          ) : (
            <p className="muted small" style={{ margin: 0 }}>
              Measures this screen's connection to {info?.name || 'Ferry'}.
            </p>
          )}
          <button
            className="btn"
            disabled={testing}
            onClick={async () => {
              setTesting(true);
              try {
                setSpeed(await measure());
              } catch {
                toast("Speed test didn't finish");
              } finally {
                setTesting(false);
              }
            }}
          >
            <Gauge size={18} aria-hidden /> {testing ? 'Testing…' : 'Test speed'}
          </button>
        </div>
      </div>
    </Sheet>
  );
}
