import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Smartphone,
  Monitor,
  Link2,
  RefreshCw,
  X,
  ClipboardCopy,
  ShieldCheck,
  Wifi,
  Check,
  Trash2,
  MessageCircle,
  Power,
  Globe,
} from 'lucide-react';
import { api, type Device, type Me } from '../lib/api';
import { copyText, isAndroidApp } from '../lib/util';
import { CodeEntry, Sheet, toast } from './common';

export function PlatformIcon({ platform, size = 20 }: { platform: string; size?: number }) {
  if (platform === 'browser') return <Globe size={size} aria-hidden />;
  return platform === 'android' || platform === 'ios' ? (
    <Smartphone size={size} aria-hidden />
  ) : (
    <Monitor size={size} aria-hidden />
  );
}

/** Shows this device's pairing PIN until another device joins. */
function PairSheet({ me, onClose, onPaired }: { me: Me; onClose: () => void; onPaired: () => void }) {
  const [session, setSession] = useState<{ pin: string; qr: string; expiresAt: string } | null>(null);
  const [paired, setPaired] = useState<string | null>(null);
  const [left, setLeft] = useState(300);
  const pairedRef = useRef(false);

  // The parent re-renders often (device list refresh); keep its callbacks in refs so the
  // PIN is created once per opening, not on every refresh.
  const onCloseRef = useRef(onClose);
  const onPairedRef = useRef(onPaired);
  onCloseRef.current = onClose;
  onPairedRef.current = onPaired;

  const start = useCallback(async () => {
    try {
      setSession(await api.pairStart());
    } catch (e: any) {
      toast(e.message);
      onCloseRef.current();
    }
  }, []);

  useEffect(() => {
    start();
    return () => {
      if (!pairedRef.current) api.pairCancel().catch(() => {});
    };
  }, [start]);

  useEffect(() => {
    if (!session || paired) return;
    const t = setInterval(async () => {
      setLeft(Math.max(0, Math.round((new Date(session.expiresAt).getTime() - Date.now()) / 1000)));
      try {
        const st = await api.pairStatus();
        if (st.paired) {
          pairedRef.current = true;
          setPaired(st.paired.name);
          onPairedRef.current();
        }
      } catch {
        /* keep polling */
      }
    }, 1000);
    return () => clearInterval(t);
  }, [session, paired]);

  return (
    <Sheet title="Pair a device" onClose={onClose}>
      <div className="stack">
        <div className="sheet-head">
          <div>
            <h2>{paired ? `Paired with ${paired}` : 'Pair a device'}</h2>
            <p className="muted" style={{ margin: '6px 0 0' }}>
              {paired
                ? `${paired} can now send to ${me.name} directly, and you can send to it.`
                : `On the other device, open Ferry, go to Devices, tap Pair next to ${me.name} and type this PIN.`}
            </p>
          </div>
          <button className="btn ghost icon" aria-label="Close" onClick={onClose}>
            <X size={20} aria-hidden />
          </button>
        </div>
        {paired ? (
          <div className="paired-done" aria-hidden>
            <Check size={48} />
          </div>
        ) : session ? (
          <>
            <div className="pin-ticket" aria-label={`PIN ${session.pin.split('').join(' ')}`}>
              <span className="small">PIN</span>
              <b>
                {session.pin.slice(0, 3)} {session.pin.slice(3)}
              </b>
            </div>
            <p className="muted small" style={{ margin: 0, textAlign: 'center' }}>
              {left > 0 ? `Expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}` : 'Expired.'}{' '}
              {left === 0 ? (
                <button className="btn ghost small" style={{ minHeight: 30 }} onClick={start}>
                  Show a new code
                </button>
              ) : null}
            </p>
          </>
        ) : (
          <p className="muted">Opening pairing…</p>
        )}
        {paired ? (
          <button className="btn primary block" onClick={onClose}>
            Done
          </button>
        ) : null}
      </div>
    </Sheet>
  );
}

/** Pair with a nearby device by typing the PIN it shows. */
function PinPairSheet({ device, onClose, onPaired }: { device: Device; onClose: () => void; onPaired: () => void }) {
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (pin.length !== 6 || !device.url) return;
    setBusy(true);
    setError(null);
    api
      .pairJoin({ url: device.url, pin })
      .then(() => {
        toast(`Paired with ${device.name}`);
        onPaired();
        onClose();
      })
      .catch((e) => {
        setError(e.message);
        setPin('');
      })
      .finally(() => setBusy(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pin, device.id]);

  return (
    <Sheet title={`Pair with ${device.name}`} onClose={onClose}>
      <div className="stack">
        <div className="sheet-head">
          <div>
            <h2>Pair with {device.name}</h2>
            <p className="muted" style={{ margin: '6px 0 0' }}>
              On {device.name}, open Devices and tap Show pairing PIN. Type the PIN it shows.
            </p>
          </div>
          <button className="btn ghost icon" aria-label="Close" onClick={onClose}>
            <X size={20} aria-hidden />
          </button>
        </div>
        <CodeEntry value={pin} onChange={setPin} error={!!error} autoFocus label="Pairing PIN" />
        {busy ? <p className="muted">Pairing…</p> : null}
        {error ? <div className="notice error">{error}</div> : null}
      </div>
    </Sheet>
  );
}

function BackgroundCard({ me, setSetting }: { me: Me; setSetting: (p: Record<string, unknown>) => void }) {
  const bridge = (window as any).FerryAndroid;
  const android = !!bridge?.batteryRestricted;
  const [restricted, setRestricted] = useState<boolean>(() => (android ? !!bridge.batteryRestricted() : false));
  const [onBoot, setOnBoot] = useState<boolean>(() => (android ? !!bridge.startOnBoot() : false));
  useEffect(() => {
    if (!android) return;
    const check = () => setRestricted(!!bridge.batteryRestricted());
    window.addEventListener('focus', check);
    document.addEventListener('visibilitychange', check);
    return () => {
      window.removeEventListener('focus', check);
      document.removeEventListener('visibilitychange', check);
    };
  }, [android, bridge]);
  if (!android && !me.startAtLoginSupported) return null;
  return (
    <section className="panel stack" aria-labelledby="bg-h">
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <Power size={22} aria-hidden style={{ marginTop: 3 }} />
        <div style={{ flex: 1 }}>
          <h3 id="bg-h">Always ready to receive</h3>
          <p className="muted small" style={{ margin: '4px 0 0' }}>
            {android
              ? 'Ferry keeps running in the background with a small notification, so other devices can always find this phone.'
              : 'Ferry runs in the tray. Start it with your computer so other devices can always find it.'}
          </p>
        </div>
      </div>
      {android ? (
        <>
          <label className="option" style={{ paddingTop: 0 }}>
            <span className="otext">
              <b>Start when the phone starts</b>
            </span>
            <input
              type="checkbox"
              className="switch"
              checked={onBoot}
              onChange={(e) => {
                bridge.setStartOnBoot(e.target.checked);
                setOnBoot(e.target.checked);
              }}
            />
          </label>
          {restricted ? (
            <div className="stack" style={{ gap: 8 }}>
              <p className="small" style={{ margin: 0 }}>
                Battery saving may still pause Ferry when the screen is off. Allow it to run unrestricted:
              </p>
              <button className="btn" onClick={() => bridge.requestBatteryExemption()}>
                Allow background use
              </button>
            </div>
          ) : (
            <p className="small muted" style={{ margin: 0 }}>
              <Check size={14} aria-hidden /> Background use allowed.
            </p>
          )}
        </>
      ) : (
        <label className="option" style={{ paddingTop: 0 }}>
          <span className="otext">
            <b>Start Ferry when I log in</b>
            <span>Opens in the tray, without a window.</span>
          </span>
          <input
            type="checkbox"
            className="switch"
            checked={!!me.settings?.startAtLogin}
            onChange={(e) => setSetting({ startAtLogin: e.target.checked })}
          />
        </label>
      )}
    </section>
  );
}

interface Hotspot {
  state: 'off' | 'starting' | 'on' | 'error';
  ssid?: string;
  password?: string;
  message?: string;
}

function HotspotCard() {
  const bridge = (window as any).FerryAndroid;
  const supported = !!bridge?.hotspotSupported?.();
  const [hs, setHs] = useState<Hotspot>({ state: 'off' });

  useEffect(() => {
    const on = (e: Event) => setHs((e as CustomEvent).detail as Hotspot);
    window.addEventListener('ferry-hotspot', on);
    try {
      const cur = bridge?.hotspotState?.();
      if (cur) setHs(JSON.parse(cur));
    } catch {
      /* none */
    }
    return () => window.removeEventListener('ferry-hotspot', on);
  }, [bridge]);

  return (
    <section className="panel stack" aria-labelledby="hs-h">
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <Wifi size={22} aria-hidden style={{ marginTop: 3 }} />
        <div style={{ flex: 1 }}>
          <h3 id="hs-h">No Wi-Fi around?</h3>
          <p className="muted small" style={{ margin: '4px 0 0' }}>
            {supported
              ? 'This phone can start its own Wi-Fi network. Other devices join it, then send and receive as usual. Mobile data isn\'t shared.'
              : 'Open Ferry on an Android phone and start a hotspot from its Devices screen. Then join that Wi-Fi here.'}
          </p>
        </div>
      </div>
      {supported ? (
        hs.state === 'on' && hs.ssid ? (
          <div className="stack" style={{ gap: 10 }}>
            <p className="small" style={{ margin: 0 }}>
              On the other device, join this Wi-Fi network:
            </p>
            <div className="addr">
              <span>
                Network: {hs.ssid}
                <br />
                Password: {hs.password}
              </span>
              <button
                className="btn ghost icon"
                aria-label="Copy password"
                onClick={async () => {
                  if (await copyText(hs.password || '')) toast('Password copied');
                }}
              >
                <ClipboardCopy size={18} aria-hidden />
              </button>
            </div>
            <p className="muted small" style={{ margin: 0 }}>
              Once it has joined, the two devices see each other in their device lists.
            </p>
            <button className="btn danger" onClick={() => bridge.stopHotspot()}>
              Stop hotspot
            </button>
          </div>
        ) : (
          <>
            {hs.state === 'error' ? <div className="notice error">{hs.message}</div> : null}
            <button
              className="btn"
              disabled={hs.state === 'starting'}
              onClick={() => {
                setHs({ state: 'starting' });
                bridge.startHotspot();
              }}
            >
              <Wifi size={18} aria-hidden /> {hs.state === 'starting' ? 'Starting…' : 'Start hotspot'}
            </button>
          </>
        )
      ) : null}
    </section>
  );
}

export function DevicesPanel({
  me,
  refreshMe,
  version,
  bump,
  onOpenChat,
}: {
  me: Me;
  refreshMe: () => void;
  version: number;
  bump: () => void;
  onOpenChat: (t: { id: string; name: string; paired?: boolean }) => void;
}) {
  const [paired, setPaired] = useState<Device[]>([]);
  const [nearby, setNearby] = useState<Device[] | null>(null);
  const [looking, setLooking] = useState(false);
  const [pairOpen, setPairOpen] = useState(false);
  const [pinTarget, setPinTarget] = useState<Device | null>(null);
  const [confirmUnpair, setConfirmUnpair] = useState<string | null>(null);
  const [clipBusy, setClipBusy] = useState(false);

  const load = useCallback(async (refresh?: '1' | 'scan') => {
    if (refresh) setLooking(true);
    try {
      const d = await api.devices(refresh);
      setPaired(d.paired);
      setNearby(d.nearby);
    } catch {
      /* offline */
    } finally {
      setLooking(false);
    }
  }, []);

  useEffect(() => {
    load('1');
    const t = setInterval(() => load(), 5000);
    return () => clearInterval(t);
  }, [load, version]);

  const update = async (d: Device, patch: Partial<Device>) => {
    setPaired((ps) => ps.map((p) => (p.id === d.id ? { ...p, ...patch } : p)));
    try {
      await api.updateDevice(d.id, patch);
    } catch (e: any) {
      toast(e.message);
      load();
    }
  };

  const settings = me.settings!;
  const setSetting = async (patch: Record<string, unknown>) => {
    try {
      await api.settings(patch);
      refreshMe();
    } catch (e: any) {
      toast(e.message);
    }
  };

  async function sendClipboard() {
    setClipBusy(true);
    try {
      let text: string | undefined;
      const bridge = (window as any).FerryAndroid;
      if (bridge?.getClipboard) text = bridge.getClipboard();
      else if (!me.clipboardSupported) text = await navigator.clipboard.readText();
      const r = await api.clipboardSend(text !== undefined ? { text } : { fromSystem: true });
      if (r.sent?.length) toast(`Clipboard sent to ${r.sent.join(', ')}`);
      if (r.failed?.length) toast(r.failed[0].error);
    } catch (e: any) {
      toast(e?.message || "Couldn't read the clipboard");
    } finally {
      setClipBusy(false);
    }
  }

  return (
    <div className="col">
      <section className="panel stack" aria-labelledby="dev-h">
        <div>
          <h2 id="dev-h">Devices</h2>
          <p className="lede">
            Devices on this Wi-Fi with Ferry open appear here automatically. Pairing is optional: paired devices are
            verified and can receive without asking.
          </p>
        </div>
        <div className="row" style={{ flexWrap: 'wrap' }}>
          <button className="btn primary" style={{ flex: '1 1 180px' }} onClick={() => setPairOpen(true)}>
            <Link2 size={20} aria-hidden /> Show pairing PIN
          </button>

        </div>

        <div className="stack" style={{ gap: 4 }}>
          <h3>Paired devices</h3>
          {paired.length ? (
            <ul className="devices">
              {paired.map((d) => (
                <li key={d.id}>
                  <div className="row">
                    <span className="dev-icon">
                      <PlatformIcon platform={d.platform} />
                    </span>
                    <span style={{ flex: 1, minWidth: 0 }}>
                      <b className="ellipsis">{d.name}</b>
                      <span className="small muted row" style={{ gap: 6 }}>
                        <i className={`dot${d.online ? '' : ' idle'}`} aria-hidden />
                        {d.online ? 'On this network' : 'Not nearby right now'}
                      </span>
                    </span>
                    {d.online ? (
                      <button
                        className="btn ghost icon"
                        aria-label={`Message ${d.name}`}
                        onClick={() => onOpenChat({ id: d.id, name: d.name, paired: true })}
                      >
                        <MessageCircle size={18} aria-hidden />
                      </button>
                    ) : null}
                    <button
                      className={`btn ${confirmUnpair === d.id ? 'danger' : 'ghost'}`}
                      aria-label={`Unpair ${d.name}`}
                      onClick={async () => {
                        if (confirmUnpair !== d.id) {
                          setConfirmUnpair(d.id);
                          setTimeout(() => setConfirmUnpair((c) => (c === d.id ? null : c)), 3000);
                          return;
                        }
                        await api.unpair(d.id).catch(() => {});
                        toast(`Unpaired ${d.name}`);
                        load();
                        bump();
                      }}
                    >
                      {confirmUnpair === d.id ? 'Unpair' : <Trash2 size={18} aria-hidden />}
                    </button>
                  </div>
                  <label className="mini-option">
                    <span>Accept files without asking</span>
                    <input
                      type="checkbox"
                      className="switch sm"
                      checked={!!d.autoAccept}
                      onChange={(e) => update(d, { autoAccept: e.target.checked })}
                    />
                  </label>
                  <label className="mini-option">
                    <span>Share clipboard</span>
                    <input
                      type="checkbox"
                      className="switch sm"
                      checked={!!d.syncClipboard}
                      onChange={(e) => update(d, { syncClipboard: e.target.checked })}
                    />
                  </label>
                </li>
              ))}
            </ul>
          ) : (
            <p className="empty small">No paired devices yet.</p>
          )}
        </div>

        <div className="stack" style={{ gap: 4 }}>
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <h3>Other devices nearby</h3>
            <button className="btn ghost" disabled={looking} onClick={() => load('scan')}>
              <RefreshCw size={16} aria-hidden /> {looking ? 'Looking…' : 'Look again'}
            </button>
          </div>
          {nearby === null ? (
            <p className="muted small">Looking for devices running Ferry…</p>
          ) : nearby.length ? (
            <ul className="devices">
              {nearby.map((d) => (
                <li key={d.id} className="row">
                  <span className="dev-icon">
                    <PlatformIcon platform={d.platform} />
                  </span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <b className="ellipsis">{d.name}</b>
                    <span className="small muted">{d.platform === 'browser' ? 'Browser, connected to this device' : 'Nearby, not paired'}</span>
                  </span>
                  {d.platform !== 'browser' ? (
                    <>
                      <button
                        className="btn ghost icon"
                        aria-label={`Message ${d.name}`}
                        onClick={() => onOpenChat({ id: d.id, name: d.name, paired: false })}
                      >
                        <MessageCircle size={18} aria-hidden />
                      </button>
                      <button className="btn" onClick={() => setPinTarget(d)}>
                        <Link2 size={16} aria-hidden /> Pair
                      </button>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
          ) : (
            <p className="empty small">
              No other devices with Ferry on this Wi-Fi. Open it on your other device, or use a hotspot below.
            </p>
          )}
        </div>
      </section>

      <section className="panel stack" aria-labelledby="clip-h">
        <div className="row" style={{ alignItems: 'flex-start' }}>
          <ClipboardCopy size={22} aria-hidden style={{ marginTop: 3 }} />
          <div style={{ flex: 1 }}>
            <h3 id="clip-h">Clipboard</h3>
            <p className="muted small" style={{ margin: '4px 0 0' }}>
              Send what you copied to paired devices that have Share clipboard on.
            </p>
          </div>
        </div>
        <button className="btn" disabled={clipBusy || !paired.some((p) => p.syncClipboard)} onClick={sendClipboard}>
          <ClipboardCopy size={18} aria-hidden /> {clipBusy ? 'Sending…' : 'Send clipboard now'}
        </button>
        {me.clipboardSupported ? (
          <div>
            <label className="option">
              <span className="otext">
                <b>Send automatically when I copy</b>
                <span>Anything you copy on this computer goes to those devices within a second.</span>
              </span>
              <input
                type="checkbox"
                className="switch"
                checked={settings.watchClipboard}
                onChange={(e) => setSetting({ watchClipboard: e.target.checked })}
              />
            </label>
            <label className="option">
              <span className="otext">
                <b>Copy received clipboard here</b>
                <span>Text that arrives is put on this computer's clipboard right away.</span>
              </span>
              <input
                type="checkbox"
                className="switch"
                checked={settings.autoCopyClipboard}
                onChange={(e) => setSetting({ autoCopyClipboard: e.target.checked })}
              />
            </label>
          </div>
        ) : isAndroidApp() ? (
          <p className="muted small" style={{ margin: 0 }}>
            Text sent to this phone is copied to its clipboard automatically. Android doesn't let apps read the clipboard
            in the background, so sending from the phone is one tap here.
          </p>
        ) : null}
      </section>

      <BackgroundCard me={me} setSetting={setSetting} />

      <HotspotCard />

      <section className="panel stack" aria-labelledby="sec-h">
        <div className="row" style={{ alignItems: 'flex-start' }}>
          <ShieldCheck size={22} aria-hidden style={{ marginTop: 3 }} />
          <div style={{ flex: 1 }}>
            <h3 id="sec-h">Privacy on shared Wi-Fi</h3>
            <p className="muted small" style={{ margin: '4px 0 0' }}>
              At cafés, offices or campus networks, turn off requests from unpaired devices and ask other browsers for a
              PIN before they can open {me.name}.
            </p>
          </div>
        </div>
        <label className="option" style={{ paddingTop: 0 }}>
          <span className="otext">
            <b>Allow requests from unpaired devices</b>
            <span>Nearby devices can ask to send files or messages. You always decide whether to accept.</span>
          </span>
          <input
            type="checkbox"
            className="switch"
            checked={settings.allowNearby}
            onChange={(e) => setSetting({ allowNearby: e.target.checked })}
          />
        </label>
        <label className="option">
          <span className="otext">
            <b>Ask other browsers for a PIN</b>
            <span>Paired devices and this app aren't affected.</span>
          </span>
          <input
            type="checkbox"
            className="switch"
            checked={settings.requirePin}
            onChange={(e) => setSetting({ requirePin: e.target.checked })}
          />
        </label>
        {settings.requirePin ? (
          <div className="row" style={{ justifyContent: 'space-between' }}>
            <span className="mini-ticket" aria-label={`Access PIN ${settings.accessPin}`}>
              {settings.accessPin}
            </span>
            <button className="btn ghost" onClick={() => setSetting({ newPin: true })}>
              <RefreshCw size={16} aria-hidden /> New PIN
            </button>
          </div>
        ) : null}
      </section>

      {pairOpen ? (
        <PairSheet
          me={me}
          onClose={() => setPairOpen(false)}
          onPaired={() => {
            load();
            bump();
          }}
        />
      ) : null}
      {pinTarget ? (
        <PinPairSheet
          device={pinTarget}
          onClose={() => setPinTarget(null)}
          onPaired={() => {
            load();
            bump();
          }}
        />
      ) : null}
    </div>
  );
}
