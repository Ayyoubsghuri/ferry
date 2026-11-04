import { useCallback, useEffect, useState } from 'react';
import { ArrowUpFromLine, ArrowDownToLine, MonitorSmartphone, History as HistoryIcon, Lock, Moon, Sun, Minus, Square, Copy as Restore, X, LogOut } from 'lucide-react';
import { api, browserName, guestApi, type Info, type Me } from './lib/api';
import { SendPanel } from './components/SendPanel';
import { ReceivePanel } from './components/ReceivePanel';
import { DeviceSheet } from './components/DeviceSheet';
import { DevicesPanel } from './components/DevicesPanel';
import { HistoryPanel } from './components/HistoryPanel';
import { InboxHost } from './components/InboxHost';
import { ChatSheet, type ChatTarget } from './components/ChatSheet';
import { CodeEntry, ToastHost } from './components/common';

type Tab = 'send' | 'receive' | 'devices' | 'history';

// ---------- theme: light unless the person turns dark mode on ----------

function readTheme(): 'light' | 'dark' {
  try {
    return localStorage.getItem('ferry.theme') === 'dark' ? 'dark' : 'light';
  } catch {
    return 'light';
  }
}

function applyTheme(t: 'light' | 'dark') {
  document.documentElement.dataset.theme = t;
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', t === 'dark' ? '#111815' : '#E8ECE5');
  try {
    (window as any).FerryAndroid?.setDarkMode?.(t === 'dark');
  } catch {
    /* older app */
  }
}

// ---------- Windows app window: the page is the title bar ----------

const win = window as any;
const framed = typeof win.ferryWinDrag === 'function';

function WindowControls() {
  const [max, setMax] = useState(false);
  useEffect(() => {
    if (!framed) return;
    const sync = () => win.ferryWinIsMax?.().then((m: boolean) => setMax(!!m)).catch(() => {});
    sync();
    window.addEventListener('resize', sync);
    return () => window.removeEventListener('resize', sync);
  }, []);
  if (!framed) return null;
  return (
    <div className="winctl">
      <button aria-label="Minimize window" onClick={() => win.ferryWinMin()}>
        <Minus size={16} aria-hidden />
      </button>
      <button aria-label={max ? 'Restore window' : 'Maximize window'} onClick={() => win.ferryWinMax().then((m: boolean) => setMax(!!m))}>
        {max ? <Restore size={14} aria-hidden /> : <Square size={13} aria-hidden />}
      </button>
      <button className="close" aria-label="Close window" onClick={() => win.ferryWinClose()}>
        <X size={17} aria-hidden />
      </button>
    </div>
  );
}

// Dragging empty parts of the header moves the window; double-click maximizes.
function titleBarHandlers() {
  if (!framed) return {};
  const empty = (e: React.MouseEvent) => !(e.target as HTMLElement).closest('button, a, input, textarea, nav');
  return {
    onMouseDown: (e: React.MouseEvent) => {
      if (e.button === 0 && empty(e) && e.detail === 1) win.ferryWinDrag();
    },
    onDoubleClick: (e: React.MouseEvent) => {
      if (empty(e)) win.ferryWinMax();
    },
  };
}

function PinGate({ name, onUnlocked }: { name: string; onUnlocked: () => void }) {
  const [pin, setPin] = useState('');
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (pin.length !== 6) return;
    api
      .unlock(pin)
      .then(onUnlocked)
      .catch((e) => {
        setError(e.message);
        setPin('');
      });
  }, [pin, onUnlocked]);
  return (
    <main className="main gate">
      <section className="panel stack">
        <Lock size={28} aria-hidden />
        <div>
          <h2>{name} is locked</h2>
          <p className="lede">Enter the access PIN shown in Ferry on {name}, under Devices.</p>
        </div>
        <CodeEntry value={pin} onChange={setPin} error={!!error} autoFocus label="Access PIN" />
        {error ? <div className="notice error">{error}</div> : null}
      </section>
    </main>
  );
}

export default function App() {
  const [tab, setTab] = useState<Tab>('send');
  const [info, setInfo] = useState<Info | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [online, setOnline] = useState(true);
  const [sheet, setSheet] = useState(false);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [devicesVersion, setDevicesVersion] = useState(0);
  const [chat, setChat] = useState<ChatTarget | null>(null);
  const [theme, setTheme] = useState<'light' | 'dark'>(readTheme);
  const [guestReady, setGuestReady] = useState(false);

  useEffect(() => {
    applyTheme(theme);
    try {
      localStorage.setItem('ferry.theme', theme);
    } catch {
      /* private mode */
    }
  }, [theme]);

  const refreshMe = useCallback(() => {
    api
      .me()
      .then(setMe)
      .catch(() => {});
  }, []);

  useEffect(() => {
    let alive = true;
    const ping = async () => {
      try {
        const [i, m] = await Promise.all([api.info(), api.me()]);
        if (!alive) return;
        setInfo(i);
        setMe(m);
        setOnline(true);
      } catch {
        if (alive) setOnline(false);
      }
    };
    ping();
    const t = setInterval(ping, 15000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  // A browser without the app registers itself so it shows up as a device.
  const guest = !!me && !me.owner && !me.locked;
  useEffect(() => {
    if (!guest) return;
    guestApi
      .hello(browserName())
      .then(() => setGuestReady(true))
      .catch(() => {});
  }, [guest]);

  useEffect(() => {
    if (location.pathname !== '/') history.replaceState(null, '', '/');
    const show = () => setTab('send');
    window.addEventListener('ferry-show-send', show);
    return () => window.removeEventListener('ferry-show-send', show);
  }, []);

  const activity = useCallback(() => setHistoryVersion((v) => v + 1), []);
  const bumpDevices = useCallback(() => setDevicesVersion((v) => v + 1), []);

  const themeButton = (
    <button
      className="btn ghost icon theme-toggle"
      aria-label={theme === 'dark' ? 'Turn off dark mode' : 'Turn on dark mode'}
      aria-pressed={theme === 'dark'}
      onClick={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
    >
      {theme === 'dark' ? <Sun size={20} aria-hidden /> : <Moon size={20} aria-hidden />}
    </button>
  );

  if (me?.locked) {
    return (
      <div className={`app${framed ? ' framed' : ''}`}>
        <WindowControls />
        <header className="topbar" {...titleBarHandlers()}>
          <div className="brand">
            <img src="/icon-192.png" alt="" />
            Ferry
          </div>
          {themeButton}
        </header>
        <PinGate name={me.name} onUnlocked={refreshMe} />
      </div>
    );
  }

  const owner = !!me?.owner;
  const allTabs: { id: Tab; label: string; icon: React.ReactNode }[] = [
    { id: 'send', label: 'Send', icon: <ArrowUpFromLine size={22} aria-hidden /> },
    { id: 'receive', label: 'Receive', icon: <ArrowDownToLine size={22} aria-hidden /> },
    { id: 'devices', label: 'Devices', icon: <MonitorSmartphone size={22} aria-hidden /> },
    { id: 'history', label: 'History', icon: <HistoryIcon size={22} aria-hidden /> },
  ];
  const tabs = owner ? allTabs : allTabs.slice(0, 2);
  const bridge = win.FerryAndroid;
  const joined = !owner && !!bridge?.joinedHotspot?.();

  return (
    <div className={`app${framed ? ' framed' : ''}`}>
      <WindowControls />
      <header className="topbar" {...titleBarHandlers()}>
        <div className="brand">
          <img src="/icon-192.png" alt="" />
          Ferry
        </div>
        {tabs.length > 1 ? (
          <nav className="topnav desktop-only" aria-label="Sections">
            {tabs.map((t) => (
              <button key={t.id} aria-current={tab === t.id ? 'page' : undefined} onClick={() => setTab(t.id)}>
                {t.icon}
                {t.label}
              </button>
            ))}
          </nav>
        ) : null}
        {themeButton}
        {joined ? (
          <button className="btn ghost" onClick={() => bridge.leaveHotspot()}>
            <LogOut size={18} aria-hidden /> Leave
          </button>
        ) : null}
        <button className="device-chip" onClick={() => setSheet(true)} aria-label="About this device">
          <i className={`dot${online ? '' : ' off'}`} aria-hidden />
          <span>{online ? (me ? (owner ? me.name : `Connected to ${me.name}`) : 'Connecting…') : 'Not connected'}</span>
        </button>
      </header>

      <main className="main">
        {!online ? (
          <div className="notice error" style={{ marginBottom: 16 }}>
            Lost contact with Ferry. Make sure it's running and this device is on the same Wi-Fi.
          </div>
        ) : null}
        <div className="narrow col">
          {tab === 'send' ? <SendPanel me={me} active devicesVersion={devicesVersion} /> : null}
          {tab === 'receive' ? <ReceivePanel owner={owner} onReceived={activity} /> : null}
          {tab === 'devices' && me && owner ? (
            <DevicesPanel me={me} refreshMe={refreshMe} version={devicesVersion} bump={bumpDevices} onOpenChat={setChat} />
          ) : null}
          {tab === 'history' && me && owner ? <HistoryPanel me={me} version={historyVersion} /> : null}
        </div>
      </main>

      {tabs.length > 1 ? (
        <nav className="tabbar" aria-label="Sections" style={{ gridTemplateColumns: `repeat(${tabs.length}, 1fr)` }}>
          {tabs.map((t) => (
            <button key={t.id} aria-current={tab === t.id ? 'page' : undefined} onClick={() => setTab(t.id)}>
              {t.icon}
              {t.label}
            </button>
          ))}
        </nav>
      ) : null}

      {me && (owner || guestReady) ? (
        <InboxHost me={me} guest={!owner} onActivity={activity} onOpenChat={setChat} />
      ) : null}
      {chat ? <ChatSheet target={chat} onClose={() => setChat(null)} /> : null}
      {sheet ? <DeviceSheet info={info} onClose={() => setSheet(false)} /> : null}
      <ToastHost />
    </div>
  );
}
