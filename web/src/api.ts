export type State = "created" | "running" | "stopping" | "exited";

export interface PortMapping {
  hostIp: string;
  hostPort: number;
  containerPort: number;
  protocol: "tcp" | "udp";
}

export interface Mount {
  source: string;
  target: string;
  readOnly: boolean;
}

export interface Resources {
  cpus: number;
  cpuWeight: number;
  memoryMb: number;
  pidsLimit: number;
}

export interface Spec {
  image: string;
  cmd?: string[];
  entrypoint?: string[];
  env?: string[];
  workingDir?: string;
  hostname?: string;
  user?: string;
  network: "bridge" | "host" | "none";
  ports?: PortMapping[];
  mounts?: Mount[];
  resources: Resources;
  restart: { name: string; maxRetries: number };
  readOnlyRootfs: boolean;
  autoRemove: boolean;
  privileged: boolean;
}

export interface Container {
  id: string;
  name: string;
  image: string;
  imageId: string;
  spec: Spec;
  command: string[];
  state: State;
  pid: number;
  ip: string;
  storage: string;
  exitCode: number;
  error: string;
  restartCount: number;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface Inspect extends Container {
  paths: { dir: string; rootfs: string; log: string; cgroup: string };
  namespaces?: Record<string, string>;
}

export interface Image {
  id: string;
  reference: string;
  digest: string;
  source: string;
  sizeBytes: number;
  layers: number;
  config: { env: string[] | null; entrypoint: string[] | null; cmd: string[] | null; workingDir: string; architecture: string };
  createdAt: string;
}

export interface Stats {
  id: string;
  name: string;
  time: string;
  cpuPercent: number;
  cpuLimit: number;
  memoryPercent: number;
  cgroup: {
    cpuUsageUsec: number;
    cpuThrottledUsec: number;
    memoryUsageBytes: number;
    memoryLimitBytes: number;
    memoryPeakBytes: number;
    oomKills: number;
    pidsCurrent: number;
    pidsLimit: number;
  };
  network: { rxBytes: number; txBytes: number; rxPackets: number; txPackets: number };
}

export interface Process {
  pid: number;
  ppid: number;
  state: string;
  uid: string;
  rssKb: number;
  threads: number;
  command: string;
}

export interface SystemInfo {
  version: string;
  kernel: string;
  arch: string;
  cgroupVersion: number;
  storageDriver: string;
  root: string;
  bridge: string;
  subnet: string;
  cpus: number;
  memoryBytes: number;
  hostname: string;
  counts: { containers: number; running: number; exited: number; created: number; images: number };
}

export interface SiloEvent {
  id: number;
  time: string;
  type: string;
  action: string;
  actorId: string;
  actorName: string;
  attributes?: Record<string, string>;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

const TOKEN_KEY = "silo.token";

export function getToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setToken(t: string) {
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    return;
  }
}

function headers(json = false): HeadersInit {
  const h: Record<string, string> = {};
  const t = getToken();
  if (t) h.Authorization = `Bearer ${t}`;
  if (json) h["Content-Type"] = "application/json";
  return h;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    headers: headers(body !== undefined),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? safeJSON(text) : null;
  if (!res.ok) {
    const msg = (data as { error?: { message?: string } } | null)?.error?.message ?? (text || res.statusText);
    throw new ApiError(res.status, msg);
  }
  return (data ?? text) as T;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export const api = {
  info: () => request<SystemInfo>("GET", "/info"),
  containers: () => request<Container[]>("GET", "/containers?all=true"),
  inspect: (id: string) => request<Inspect>("GET", `/containers/${id}`),
  create: (spec: Partial<Spec> & { name?: string; start?: boolean }) =>
    request<{ container: Container; warning?: string }>("POST", "/containers", spec),
  start: (id: string) => request<Container>("POST", `/containers/${id}/start`),
  stop: (id: string) => request<Container>("POST", `/containers/${id}/stop`),
  restart: (id: string) => request<Container>("POST", `/containers/${id}/restart`),
  kill: (id: string, signal = 9) => request<Container>("POST", `/containers/${id}/kill?signal=${signal}`),
  remove: (id: string, force = false) => request<{ removed: string }>("DELETE", `/containers/${id}?force=${force}`),
  prune: () => request<{ removed: string[] }>("POST", "/containers/prune"),
  exec: (id: string, cmd: string[]) => request<{ exitCode: number; output: string }>("POST", `/containers/${id}/exec`, { cmd }),
  logs: (id: string, tail = 500) => request<string>("GET", `/containers/${id}/logs?tail=${tail}`),
  stats: (id: string) => request<Stats>("GET", `/containers/${id}/stats`),
  allStats: () => request<Stats[]>("GET", "/stats"),
  top: (id: string) => request<Process[]>("GET", `/containers/${id}/top`),
  images: () => request<Image[]>("GET", "/images"),
  removeImage: (id: string) => request<{ removed: string }>("DELETE", `/images/${id}`),
  events: (limit = 200) => request<SiloEvent[]>("GET", `/events?limit=${limit}`),
  importImage: async (file: File, reference: string, cmd: string) => {
    const q = new URLSearchParams({ reference, cmd });
    const res = await fetch(`/api/v1/images/import?${q}`, { method: "POST", headers: headers(), body: file });
    const data = await res.json();
    if (!res.ok) throw new ApiError(res.status, data?.error?.message ?? res.statusText);
    return data as Image;
  },
  pull: async (reference: string, onProgress: (stage: string, cur: number, total: number) => void) => {
    const res = await fetch("/api/v1/images/pull", { method: "POST", headers: headers(true), body: JSON.stringify({ reference }) });
    if (!res.ok || !res.body) {
      const data = await res.json().catch(() => null);
      throw new ApiError(res.status, data?.error?.message ?? res.statusText);
    }
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let nl: number;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        const msg = JSON.parse(line);
        if (msg.error) throw new ApiError(500, msg.error);
        if (msg.done) return msg.image as Image;
        onProgress(msg.stage, msg.current, msg.total);
      }
    }
    throw new ApiError(500, "download interrompido");
  },
};

export function socket(path: string): WebSocket {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const t = getToken();
  const sep = path.includes("?") ? "&" : "?";
  return new WebSocket(`${proto}://${location.host}/api/v1${path}${t ? `${sep}token=${encodeURIComponent(t)}` : ""}`);
}

export function bytes(n: number): string {
  if (!n) return "0 B";
  const u = ["B", "KiB", "MiB", "GiB", "TiB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${(n / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`;
}

export function since(iso: string | null): string {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s`;
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 172800) return `${Math.floor(s / 3600)} h`;
  return `${Math.floor(s / 86400)} dias`;
}

export const short = (id: string) => id.slice(0, 12);
