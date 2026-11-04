import { useEffect, useState } from 'react';
import { File as FileIcon, FileImage, FileVideo, FileAudio, FileArchive, FileText, Folder } from 'lucide-react';

export function fileKind(name: string, type?: string) {
  const ext = name.split('.').pop()?.toLowerCase() || '';
  if (type?.startsWith('image/') || ['jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'avif', 'svg', 'bmp'].includes(ext))
    return 'image';
  if (type?.startsWith('video/') || ['mp4', 'mov', 'mkv', 'webm', 'avi'].includes(ext)) return 'video';
  if (type?.startsWith('audio/') || ['mp3', 'wav', 'flac', 'm4a', 'ogg', 'aac'].includes(ext)) return 'audio';
  if (['zip', 'rar', '7z', 'gz', 'tar', 'xz', 'apk', 'dmg', 'iso'].includes(ext)) return 'archive';
  if (['pdf', 'doc', 'docx', 'txt', 'md', 'rtf', 'odt', 'xls', 'xlsx', 'ppt', 'pptx', 'csv'].includes(ext)) return 'doc';
  return 'file';
}

export function KindIcon({ name, type, size = 18 }: { name: string; type?: string; size?: number }) {
  const k = fileKind(name, type);
  const I =
    k === 'image'
      ? FileImage
      : k === 'video'
        ? FileVideo
        : k === 'audio'
          ? FileAudio
          : k === 'archive'
            ? FileArchive
            : k === 'doc'
              ? FileText
              : FileIcon;
  return <I size={size} aria-hidden />;
}

export function FolderIcon({ size = 18 }: { size?: number }) {
  return <Folder size={size} aria-hidden />;
}

export function Progress({ value }: { value: number }) {
  return (
    <div className="progress" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(value * 100)}>
      <i style={{ width: `${Math.min(100, Math.max(0, value * 100))}%` }} />
    </div>
  );
}

export function Sheet({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);
  return (
    <div className="scrim" onClick={onClose}>
      <div className="sheet" role="dialog" aria-modal="true" aria-label={title} onClick={(e) => e.stopPropagation()}>
        {children}
      </div>
    </div>
  );
}

let toastSetter: ((s: string | null) => void) | null = null;

export function toast(msg: string) {
  toastSetter?.(msg);
}

export function ToastHost() {
  const [msg, setMsg] = useState<string | null>(null);
  useEffect(() => {
    toastSetter = setMsg;
    return () => {
      toastSetter = null;
    };
  }, []);
  useEffect(() => {
    if (!msg) return;
    const t = setTimeout(() => setMsg(null), 2400);
    return () => clearTimeout(t);
  }, [msg]);
  return msg ? (
    <div className="toast" role="status">
      {msg}
    </div>
  ) : null;
}

export function useNow(interval = 15000) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), interval);
    return () => clearInterval(t);
  }, [interval]);
  return now;
}

export function CodeEntry({
  value,
  onChange,
  error,
  autoFocus,
  label = '6-digit code',
}: {
  value: string;
  onChange: (v: string) => void;
  error: boolean;
  autoFocus: boolean;
  label?: string;
}) {
  const [focused, setFocused] = useState(false);
  return (
    <div className="code-entry" data-error={error}>
      {Array.from({ length: 6 }).map((_, i) => (
        <div
          key={i}
          className="cell"
          aria-hidden
          data-filled={i < value.length}
          data-active={focused && (i === value.length || (i === 5 && value.length === 6))}
        >
          {value[i] || ''}
        </div>
      ))}
      <input
        aria-label={label}
        inputMode="numeric"
        autoComplete="one-time-code"
        pattern="[0-9]*"
        maxLength={6}
        value={value}
        autoFocus={autoFocus}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onChange={(e) => onChange(e.target.value.replace(/\D/g, '').slice(0, 6))}
      />
    </div>
  );
}

