import { useEffect, useRef, useState } from 'react';
import { X } from 'lucide-react';
import jsQR from 'jsqr';
import { Sheet } from './common';

export interface JoinCode {
  ssid: string;
  password: string;
  url: string;
  name: string;
}

/** Reads the QR a sending phone shows for its hotspot: ferry://join?s=…&p=…&h=…&port=…&n=… */
export function parseJoin(text: string): JoinCode | null {
  try {
    const u = new URL(text.trim().replace(/^ferry:\/\//, 'https://ferry/'));
    if (!u.pathname.endsWith('/join')) return null;
    const s = u.searchParams.get('s'), p = u.searchParams.get('p'), h = u.searchParams.get('h');
    if (!s || !p || !h) return null;
    const port = u.searchParams.get('port') || '7070';
    return { ssid: s, password: p, url: `http://${h}:${port}/`, name: u.searchParams.get('n') || 'the other phone' };
  } catch {
    return null;
  }
}

export const cameraAvailable = () => !!navigator.mediaDevices?.getUserMedia;

export function Scanner({ title, onText, onClose }: { title: string; onText: (t: string) => void; onClose: () => void }) {
  const video = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState<string | null>(null);
  const cb = useRef(onText);
  cb.current = onText;

  useEffect(() => {
    let stream: MediaStream | null = null;
    let raf = 0;
    let stopped = false;
    const canvas = document.createElement('canvas');
    const ctx = canvas.getContext('2d', { willReadFrequently: true });
    const Detector = (window as any).BarcodeDetector;
    const detector = Detector ? new Detector({ formats: ['qr_code'] }) : null;
    const found = (t: string) => {
      if (stopped) return;
      stopped = true;
      navigator.vibrate?.(30);
      cb.current(t);
    };
    const tick = async () => {
      if (stopped) return;
      const v = video.current;
      if (v && v.readyState >= 2) {
        try {
          if (detector) {
            const codes = await detector.detect(v);
            if (codes.length) return found(codes[0].rawValue);
          } else if (ctx) {
            const w = 480, h = Math.round((v.videoHeight / v.videoWidth) * w) || 360;
            canvas.width = w;
            canvas.height = h;
            ctx.drawImage(v, 0, 0, w, h);
            const code = jsQR(ctx.getImageData(0, 0, w, h).data, w, h, { inversionAttempts: 'dontInvert' });
            if (code?.data) return found(code.data);
          }
        } catch {
          /* keep trying */
        }
      }
      raf = requestAnimationFrame(tick);
    };
    (async () => {
      try {
        stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' }, audio: false });
        if (video.current) {
          video.current.srcObject = stream;
          await video.current.play();
        }
        raf = requestAnimationFrame(tick);
      } catch (e: any) {
        setError(e?.name === 'NotAllowedError' ? 'Allow the camera for Ferry and try again.' : "Couldn't open the camera.");
      }
    })();
    return () => {
      stopped = true;
      cancelAnimationFrame(raf);
      stream?.getTracks().forEach((t) => t.stop());
    };
  }, []);

  return (
    <Sheet title={title} onClose={onClose}>
      <div className="stack">
        <div className="sheet-head">
          <h2>{title}</h2>
          <button className="btn ghost icon" aria-label="Close" onClick={onClose}>
            <X size={20} aria-hidden />
          </button>
        </div>
        {error ? (
          <div className="notice error">{error}</div>
        ) : (
          <div className="scanner">
            <video ref={video} playsInline muted />
            <div className="scanner-frame" aria-hidden />
          </div>
        )}
        <p className="muted small" style={{ margin: 0 }}>
          Point the camera at the code on the sending phone.
        </p>
      </div>
    </Sheet>
  );
}
