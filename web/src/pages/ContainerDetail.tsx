import { useEffect, useRef, useState } from "react";
import { api, bytes, Process, short, since, socket, Stats } from "../api";
import { Card, Empty, Meter, Sparkline, Stat, StateBadge, usePoll } from "../components/ui";
import { ContainerActions, limitsText } from "./Containers";

type Tab = "logs" | "stats" | "processes" | "terminal" | "inspect";

export default function ContainerDetail({ id }: { id: string }) {
  const { data: c, error, reload } = usePoll(() => api.inspect(id), 2500, [id]);
  const [tab, setTab] = useState<Tab>("logs");

  if (error && !c) return <div className="page"><p className="error-text">{error}</p></div>;
  if (!c) return <div className="page"><p className="muted">Carregando...</p></div>;
  const running = c.state === "running";

  return (
    <div className="page">
      <a href="#/containers" className="back">← Containers</a>
      <header className={`page-head${running ? " head-running" : ""}`}>
        <div>
          <h1>
            {c.name} <StateBadge state={c.state} code={c.exitCode} />
          </h1>
          <dl className="meta">
            <div><dt>ID</dt><dd>{short(c.id)}</dd></div>
            <div><dt>Imagem</dt><dd>{c.image}</dd></div>
            <div><dt>Comando</dt><dd>{c.command.join(" ")}</dd></div>
          </dl>
          {c.error && <p className="error-text">{c.error}</p>}
        </div>
        <ContainerActions c={c} reload={reload} />
      </header>
      <div className="stats-row">
        <Stat label="PID no host" value={running ? c.pid : "–"} hint={running ? `iniciado há ${since(c.startedAt)}` : c.finishedAt ? `parou há ${since(c.finishedAt)}` : ""} />
        <Stat label="Rede" value={running && c.ip ? c.ip : c.spec.network} hint={(c.spec.ports ?? []).map((p) => `${p.hostPort}→${p.containerPort}/${p.protocol}`).join(", ")} />
        <Stat label="Limites" value={limitsText(c.spec)} hint={`reinício: ${c.spec.restart.name}${c.restartCount ? `, ${c.restartCount}x` : ""}`} />
        <Stat label="Armazenamento" value={c.storage} hint={c.spec.readOnlyRootfs ? "raiz somente leitura" : "raiz gravável"} />
      </div>
      <div className="tabs">
        {(
          [
            ["logs", "Saída"],
            ["stats", "Recursos"],
            ["processes", "Processos"],
            ["terminal", "Comando"],
            ["inspect", "Detalhes"],
          ] as [Tab, string][]
        ).map(([k, label]) => (
          <button key={k} className={tab === k ? "on" : ""} onClick={() => setTab(k)}>
            {label}
          </button>
        ))}
      </div>
      {tab === "logs" && <Logs id={c.id} running={running} />}
      {tab === "stats" && (running ? <LiveStats id={c.id} /> : <Card><Empty>Inicie o container para ver o uso de recursos.</Empty></Card>)}
      {tab === "processes" && (running ? <Processes id={c.id} /> : <Card><Empty>Nenhum processo: o container está parado.</Empty></Card>)}
      {tab === "terminal" && <Terminal id={c.id} running={running} />}
      {tab === "inspect" && (
        <Card title="Inspeção">
          {c.namespaces && (
            <div className="ns-list">
              {Object.entries(c.namespaces).map(([k, v]) => (
                <div key={k} className="ns">
                  <span>{k}</span>
                  <code>{v}</code>
                </div>
              ))}
            </div>
          )}
          <pre className="code">{JSON.stringify(c, null, 2)}</pre>
        </Card>
      )}
    </div>
  );
}

function Logs({ id, running }: { id: string; running: boolean }) {
  const [text, setText] = useState("");
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLPreElement>(null);

  useEffect(() => {
    setText("");
    if (!running) {
      api.logs(id, 1000).then(setText).catch(() => setText(""));
      return;
    }
    const ws = socket(`/containers/${id}/logs/stream?tail=500`);
    ws.onmessage = (e) => setText((t) => (t + e.data).slice(-400000));
    return () => ws.close();
  }, [id, running]);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [text, follow]);

  return (
    <Card
      title={running ? "Saída em tempo real" : "Última saída"}
      actions={
        <label className="check">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Rolar junto
        </label>
      }
    >
      <pre ref={box} className="terminal">{text || "Sem saída ainda."}</pre>
    </Card>
  );
}

function LiveStats({ id }: { id: string }) {
  const [hist, setHist] = useState<Stats[]>([]);
  useEffect(() => {
    let alive = true;
    const tick = () =>
      api
        .stats(id)
        .then((s) => alive && setHist((h) => [...h.slice(-59), s]))
        .catch(() => undefined);
    tick();
    const t = window.setInterval(tick, 1500);
    return () => {
      alive = false;
      window.clearInterval(t);
    };
  }, [id]);
  const s = hist[hist.length - 1];
  if (!s) return <Card><Empty>Lendo cgroups...</Empty></Card>;
  const cpuMax = (s.cpuLimit || 1) * 100;
  return (
    <div className="grid-2">
      <Card title="CPU">
        <div className="metric-big">{s.cpuPercent.toFixed(1)}%</div>
        <Sparkline values={hist.map((h) => h.cpuPercent)} max={cpuMax} />
        <Meter value={s.cpuPercent} max={cpuMax} />
        <p className="muted small limit-notes">
          <span>Limite: {s.cpuLimit ? `${s.cpuLimit} núcleo(s)` : "sem limite"}</span>
          <span>Tempo total: {(s.cgroup.cpuUsageUsec / 1e6).toFixed(1)} s</span>
          <span>Contido por: {(s.cgroup.cpuThrottledUsec / 1e6).toFixed(1)} s</span>
        </p>
      </Card>
      <Card title="Memória">
        <div className="metric-big">{bytes(s.cgroup.memoryUsageBytes)}</div>
        <Sparkline values={hist.map((h) => h.cgroup.memoryUsageBytes)} max={s.cgroup.memoryLimitBytes || undefined} />
        <Meter value={s.memoryPercent} />
        <p className="muted small limit-notes">
          <span>Limite: {s.cgroup.memoryLimitBytes ? bytes(s.cgroup.memoryLimitBytes) : "sem limite"}</span>
          <span>Pico: {bytes(s.cgroup.memoryPeakBytes)}</span>
          <span>OOM: {s.cgroup.oomKills}</span>
        </p>
      </Card>
      <Card title="Processos">
        <div className="metric-big">
          {s.cgroup.pidsCurrent}
          {s.cgroup.pidsLimit ? <span className="muted"> / {s.cgroup.pidsLimit}</span> : null}
        </div>
        <Meter value={s.cgroup.pidsCurrent} max={s.cgroup.pidsLimit || Math.max(s.cgroup.pidsCurrent, 1) * 4} />
      </Card>
      <Card title="Rede">
        <div className="net">
          <div>
            <span className="muted small">Recebido</span>
            <strong>{bytes(s.network.rxBytes)}</strong>
            <span className="muted small">{s.network.rxPackets} pacotes</span>
          </div>
          <div>
            <span className="muted small">Enviado</span>
            <strong>{bytes(s.network.txBytes)}</strong>
            <span className="muted small">{s.network.txPackets} pacotes</span>
          </div>
        </div>
      </Card>
    </div>
  );
}

function Processes({ id }: { id: string }) {
  const { data } = usePoll<Process[]>(() => api.top(id), 2500, [id]);
  return (
    <Card title="Processos no cgroup">
      <table className="table">
        <thead>
          <tr>
            <th>PID</th>
            <th>PPID</th>
            <th>UID</th>
            <th>Estado</th>
            <th>RSS</th>
            <th>Threads</th>
            <th>Comando</th>
          </tr>
        </thead>
        <tbody>
          {(data ?? []).map((p) => (
            <tr key={p.pid}>
              <td className="mono">{p.pid}</td>
              <td className="mono">{p.ppid}</td>
              <td>{p.uid}</td>
              <td>{p.state}</td>
              <td>{bytes(p.rssKb * 1024)}</td>
              <td>{p.threads}</td>
              <td className="mono truncate">{p.command}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Card>
  );
}

function Terminal({ id, running }: { id: string; running: boolean }) {
  const [cmd, setCmd] = useState("");
  const [history, setHistory] = useState<{ cmd: string; out: string; code: number }[]>([]);
  const [busy, setBusy] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [history]);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!cmd.trim()) return;
    setBusy(true);
    try {
      const res = await api.exec(id, ["/bin/sh", "-c", cmd]);
      setHistory((h) => [...h, { cmd, out: res.output, code: res.exitCode }]);
    } catch (err) {
      setHistory((h) => [...h, { cmd, out: (err as Error).message, code: -1 }]);
    } finally {
      setBusy(false);
      setCmd("");
    }
  };
  if (!running) return <Card><Empty>Inicie o container para executar comandos dentro dele.</Empty></Card>;
  return (
    <Card title="Executar comando">
      <div className="terminal" ref={box}>
        {history.length === 0 && <span className="muted">Os comandos rodam com /bin/sh -c dentro dos namespaces do container.</span>}
        {history.map((h, i) => (
          <div key={i} className="exec-entry">
            <div className="prompt">$ {h.cmd}</div>
            <pre>{h.out}</pre>
            {h.code !== 0 && <div className="error-text small">código {h.code}</div>}
          </div>
        ))}
      </div>
      <form className="exec-form" onSubmit={submit}>
        <input className="mono" value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="ls -la /" disabled={busy} />
        <button className="btn primary" disabled={busy}>
          {busy ? "Rodando..." : "Executar"}
        </button>
      </form>
    </Card>
  );
}
