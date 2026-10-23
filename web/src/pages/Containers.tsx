import { useState } from "react";
import { api, Container, Image, PortMapping, short, since, Spec } from "../api";
import { useToast } from "../App";
import { Card, Empty, Modal, StateBadge, usePoll } from "../components/ui";

type Filter = "all" | "running" | "exited";

export function useActions(reload: () => void) {
  const toast = useToast();
  const [busy, setBusy] = useState<string>("");
  const run = async (key: string, fn: () => Promise<unknown>, ok: string) => {
    setBusy(key);
    try {
      await fn();
      toast(ok);
    } catch (e) {
      toast((e as Error).message, "err");
    } finally {
      setBusy("");
      reload();
    }
  };
  return { busy, run };
}

export function ContainerActions({ c, reload, compact }: { c: Container; reload: () => void; compact?: boolean }) {
  const { busy, run } = useActions(reload);
  const live = c.state === "running" || c.state === "stopping";
  return (
    <div className="row-actions" onClick={(e) => e.stopPropagation()}>
      {live ? (
        <>
          <button className="btn" disabled={!!busy} onClick={() => run("stop", () => api.stop(c.id), `${c.name} parado`)}>
            {busy === "stop" ? "Parando..." : "Parar"}
          </button>
          <button className="btn" disabled={!!busy} onClick={() => run("restart", () => api.restart(c.id), `${c.name} reiniciado`)}>
            Reiniciar
          </button>
          {!compact && (
            <button className="btn danger" disabled={!!busy} onClick={() => run("kill", () => api.kill(c.id), `SIGKILL enviado a ${c.name}`)}>
              Matar
            </button>
          )}
        </>
      ) : (
        <button className="btn" disabled={!!busy} onClick={() => run("start", () => api.start(c.id), `${c.name} iniciado`)}>
          {busy === "start" ? "Iniciando..." : "Iniciar"}
        </button>
      )}
      <button
        className="btn ghost danger"
        disabled={!!busy}
        onClick={() => {
          if (!confirmRemove(c)) return;
          run("rm", () => api.remove(c.id, live), `${c.name} removido`).then(() => {
            if (location.hash.includes(c.id)) location.hash = "/containers";
          });
        }}
      >
        Remover
      </button>
    </div>
  );
}

function confirmRemove(c: Container) {
  const live = c.state === "running";
  return window.confirm(live ? `${c.name} está rodando. Parar e remover?` : `Remover ${c.name}?`);
}

export default function Containers() {
  const { data, error, reload } = usePoll(api.containers, 3000);
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(location.hash.includes("novo=1"));
  const { run } = useActions(reload);

  const list = (data ?? []).filter((c) => {
    if (filter === "running" && c.state !== "running") return false;
    if (filter === "exited" && c.state === "running") return false;
    const q = query.toLowerCase();
    return !q || c.name.includes(q) || c.image.includes(q) || c.id.startsWith(q);
  });

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Containers</h1>
          <p className="muted">Cada container roda em namespaces próprios de PID, montagem, UTS, IPC e rede, com limites aplicados por cgroups.</p>
        </div>
        <div className="head-actions">
          <button className="btn" onClick={() => run("prune", () => api.prune(), "Containers parados removidos")}>
            Limpar parados
          </button>
          <button className="btn primary" onClick={() => setCreating(true)}>
            Novo container
          </button>
        </div>
      </header>
      <div className="toolbar">
        <div className="segmented">
          {(["all", "running", "exited"] as Filter[]).map((f) => (
            <button key={f} className={filter === f ? "on" : ""} onClick={() => setFilter(f)}>
              {f === "all" ? "Todos" : f === "running" ? "Rodando" : "Parados"}
            </button>
          ))}
        </div>
        <input className="search" placeholder="Buscar por nome, imagem ou ID" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>
      {error && <p className="error-text">{error}</p>}
      <Card>
        {list.length === 0 ? (
          <Empty>{data ? "Nenhum container encontrado." : "Carregando..."}</Empty>
        ) : (
          <table className="table table-stack">
            <thead>
              <tr>
                <th>Nome</th>
                <th>Imagem</th>
                <th>Comando</th>
                <th>Estado</th>
                <th>Rede</th>
                <th>Limites</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((c) => (
                <tr key={c.id} className={`clickable row-${c.state}`} onClick={() => (location.hash = `/containers/${c.id}`)}>
                  <td>
                    <strong>{c.name}</strong>
                    <div className="muted small mono">{short(c.id)}</div>
                  </td>
                  <td className="mono">{c.image}</td>
                  <td className="mono truncate" title={c.command.join(" ")}>
                    {c.command.join(" ")}
                  </td>
                  <td>
                    <StateBadge state={c.state} code={c.exitCode} />
                    <div className="muted small">
                      {c.state === "running" ? `há ${since(c.startedAt)}` : c.finishedAt ? `há ${since(c.finishedAt)}` : `criado há ${since(c.createdAt)}`}
                    </div>
                  </td>
                  <td className="mono small">
                    {c.state === "running" && c.ip ? c.ip : c.spec.network}
                    {(c.spec.ports ?? []).map((p) => (
                      <div key={`${p.hostPort}${p.protocol}`}>
                        {p.hostPort}→{p.containerPort}/{p.protocol}
                      </div>
                    ))}
                  </td>
                  <td className="small">{limitsText(c.spec)}</td>
                  <td>
                    <ContainerActions c={c} reload={reload} compact />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
      {creating && (
        <NewContainer
          onClose={() => {
            setCreating(false);
            if (location.hash.includes("novo")) location.hash = "/containers";
          }}
          onCreated={(c) => {
            setCreating(false);
            location.hash = `/containers/${c.id}`;
          }}
        />
      )}
    </div>
  );
}

export function limitsText(s: Spec) {
  const r = s.resources;
  const parts = [];
  if (r.cpus) parts.push(`${r.cpus} CPU`);
  if (r.memoryMb) parts.push(`${r.memoryMb} MB`);
  if (r.pidsLimit) parts.push(`${r.pidsLimit} proc.`);
  return parts.length ? parts.join(", ") : "sem limites";
}

function lines(s: string) {
  return s
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

function splitArgs(s: string): string[] {
  const out: string[] = [];
  const re = /"([^"]*)"|'([^']*)'|(\S+)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(s))) out.push(m[1] ?? m[2] ?? m[3]);
  return out;
}

function NewContainer({ onClose, onCreated }: { onClose: () => void; onCreated: (c: Container) => void }) {
  const toast = useToast();
  const images = usePoll(api.images, 10000);
  const [form, setForm] = useState({
    name: "",
    image: new URLSearchParams(location.hash.split("?")[1] ?? "").get("imagem") ?? "",
    cmd: "",
    env: "",
    ports: "",
    volumes: "",
    network: "bridge",
    cpus: "",
    memory: "",
    pids: "",
    restart: "no",
    workingDir: "",
    hostname: "",
    readOnly: false,
    autoRemove: false,
  });
  const [saving, setSaving] = useState(false);
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) =>
    setForm({ ...form, [k]: e.target.type === "checkbox" ? (e.target as HTMLInputElement).checked : e.target.value });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      const ports: PortMapping[] = lines(form.ports).map((l) => {
        const [main, proto = "tcp"] = l.split("/");
        const [h, c] = main.split(":");
        return { hostIp: "0.0.0.0", hostPort: Number(h), containerPort: Number(c ?? h), protocol: proto as "tcp" | "udp" };
      });
      const mounts = lines(form.volumes).map((l) => {
        const [source, target, mode] = l.split(":");
        return { source, target, readOnly: mode === "ro" };
      });
      const res = await api.create({
        name: form.name || undefined,
        image: form.image,
        cmd: form.cmd ? splitArgs(form.cmd) : undefined,
        env: lines(form.env),
        ports,
        mounts,
        network: form.network as Spec["network"],
        workingDir: form.workingDir || undefined,
        hostname: form.hostname || undefined,
        resources: { cpus: Number(form.cpus) || 0, memoryMb: Number(form.memory) || 0, pidsLimit: Number(form.pids) || 0, cpuWeight: 0 },
        restart: { name: form.restart, maxRetries: 0 },
        readOnlyRootfs: form.readOnly,
        autoRemove: form.autoRemove,
      });
      if (res.warning) toast(res.warning, "err");
      else toast(`${res.container.name} criado`);
      onCreated(res.container);
    } catch (err) {
      toast((err as Error).message, "err");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal title="Novo container" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <div className="form-grid">
          <label className="field">
            <span>Imagem</span>
            <input list="imgs" required value={form.image} onChange={set("image")} placeholder="alpine:3.20" />
            <datalist id="imgs">
              {(images.data ?? []).map((i: Image) => (
                <option key={i.id} value={i.reference} />
              ))}
            </datalist>
            <small>Se não existir localmente, o Silo baixa do registro.</small>
          </label>
          <label className="field">
            <span>Nome</span>
            <input value={form.name} onChange={set("name")} placeholder="gerado automaticamente" />
          </label>
          <label className="field wide">
            <span>Comando</span>
            <input value={form.cmd} onChange={set("cmd")} placeholder='sh -c "echo olá; sleep 3600"' />
            <small>Vazio usa o comando padrão da imagem.</small>
          </label>
          <label className="field">
            <span>CPU (núcleos)</span>
            <input type="number" step="0.1" min="0" value={form.cpus} onChange={set("cpus")} placeholder="sem limite" />
          </label>
          <label className="field">
            <span>Memória (MB)</span>
            <input type="number" min="0" value={form.memory} onChange={set("memory")} placeholder="sem limite" />
          </label>
          <label className="field">
            <span>Máx. de processos</span>
            <input type="number" min="0" value={form.pids} onChange={set("pids")} placeholder="sem limite" />
          </label>
          <label className="field">
            <span>Rede</span>
            <select value={form.network} onChange={set("network")}>
              <option value="bridge">bridge (IP próprio)</option>
              <option value="none">none (só loopback)</option>
              <option value="host">host (rede do host)</option>
            </select>
          </label>
          <label className="field">
            <span>Portas</span>
            <textarea rows={2} value={form.ports} onChange={set("ports")} placeholder={"8080:80\n5353:53/udp"} />
          </label>
          <label className="field">
            <span>Volumes</span>
            <textarea rows={2} value={form.volumes} onChange={set("volumes")} placeholder="/srv/dados:/dados:ro" />
          </label>
          <label className="field">
            <span>Variáveis de ambiente</span>
            <textarea rows={2} value={form.env} onChange={set("env")} placeholder="CHAVE=valor" />
          </label>
          <label className="field">
            <span>Reinício</span>
            <select value={form.restart} onChange={set("restart")}>
              <option value="no">nunca</option>
              <option value="on-failure">quando falhar</option>
              <option value="always">sempre</option>
            </select>
          </label>
          <label className="field">
            <span>Diretório de trabalho</span>
            <input value={form.workingDir} onChange={set("workingDir")} placeholder="/" />
          </label>
          <label className="field">
            <span>Hostname</span>
            <input value={form.hostname} onChange={set("hostname")} placeholder="ID curto" />
          </label>
          <label className="check">
            <input type="checkbox" checked={form.readOnly} onChange={set("readOnly")} /> Raiz somente leitura
          </label>
          <label className="check">
            <input type="checkbox" checked={form.autoRemove} onChange={set("autoRemove")} /> Remover ao terminar
          </label>
        </div>
        <footer className="form-foot">
          <button type="button" className="btn ghost" onClick={onClose}>
            Cancelar
          </button>
          <button className="btn primary" disabled={saving}>
            {saving ? "Criando..." : "Criar e iniciar"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}
