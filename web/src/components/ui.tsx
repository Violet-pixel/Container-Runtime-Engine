import { ReactNode, useEffect, useRef, useState } from "react";
import type { State } from "../api";

export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 200 200" aria-hidden="true">
      <line x1="45" y1="62" x2="45" y2="145" stroke="currentColor" strokeWidth="12" strokeLinecap="round" />
      <line x1="155" y1="62" x2="155" y2="145" stroke="currentColor" strokeWidth="12" strokeLinecap="round" />
      <path d="M45 100 A55 20 0 0 0 155 100" fill="none" stroke="currentColor" strokeWidth="9" strokeLinecap="round" />
      <path d="M45 145 A55 20 0 0 0 155 145" fill="none" stroke="currentColor" strokeWidth="12" strokeLinecap="round" />
      <ellipse cx="100" cy="55" rx="55" ry="20" fill="currentColor" />
    </svg>
  );
}

const labels: Record<State, string> = { running: "Rodando", stopping: "Parando", exited: "Parado", created: "Criado" };

export function StateBadge({ state, code }: { state: State; code?: number }) {
  const text = state === "exited" && code !== undefined ? `${labels[state]} (${code})` : labels[state];
  const failed = state === "exited" && !!code;
  return <span className={`badge badge-${state}${failed ? " badge-failed" : ""}`}>{text}</span>;
}

export function Card({ title, children, actions }: { title?: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && (
        <header className="card-head">
          {title && <h3>{title}</h3>}
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: string }) {
  return (
    <div className="stat">
      <span className="stat-label">{label}</span>
      <strong className={`stat-value${typeof value === "string" && value.length > 11 ? " long" : ""}`}>{value}</strong>
      {hint && <span className="stat-hint">{hint}</span>}
    </div>
  );
}

export function Meter({ value, max = 100 }: { value: number; max?: number }) {
  const pct = Math.max(0, Math.min(100, (value / (max || 1)) * 100));
  return (
    <div className="meter" role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
      <div className="meter-fill" style={{ width: `${pct}%` }} />
    </div>
  );
}

export function Sparkline({ values, max, height = 56 }: { values: number[]; max?: number; height?: number }) {
  const w = 300;
  const top = Math.max(max ?? 0, ...values, 1);
  const pts = values.map((v, i) => `${(i / Math.max(values.length - 1, 1)) * w},${height - (v / top) * (height - 4) - 2}`);
  return (
    <svg className="spark" viewBox={`0 0 ${w} ${height}`} preserveAspectRatio="none" height={height}>
      {values.length > 1 && (
        <>
          <polygon points={`0,${height} ${pts.join(" ")} ${w},${height}`} className="spark-area" />
          <polyline points={pts.join(" ")} className="spark-line" />
        </>
      )}
    </svg>
  );
}

export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}>
        <header className="modal-head">
          <h2>{title}</h2>
          <button className="btn ghost" onClick={onClose} aria-label="Fechar">
            ✕
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function usePoll<T>(fn: () => Promise<T>, ms: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string>("");
  const fnRef = useRef(fn);
  fnRef.current = fn;
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let alive = true;
    const run = () =>
      fnRef
        .current()
        .then((d) => alive && (setData(d), setError("")))
        .catch((e: Error) => alive && setError(e.message));
    run();
    const t = window.setInterval(run, ms);
    return () => {
      alive = false;
      window.clearInterval(t);
    };
  }, [ms, tick, ...deps]);
  return { data, error, reload: () => setTick((n) => n + 1) };
}
