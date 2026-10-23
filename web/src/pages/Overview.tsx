import { useEffect, useRef, useState } from "react";
import { api, bytes, since, Stats } from "../api";
import { Card, Empty, Meter, Sparkline, Stat, StateBadge, usePoll } from "../components/ui";

export default function Overview() {
  const info = usePoll(api.info, 5000);
  const containers = usePoll(api.containers, 4000);
  const stats = usePoll(api.allStats, 2000);
  const [cpuHist, setCpuHist] = useState<number[]>([]);
  const [memHist, setMemHist] = useState<number[]>([]);
  const last = useRef<Stats[] | null>(null);

  useEffect(() => {
    if (!stats.data || stats.data === last.current) return;
    last.current = stats.data;
    const cpu = stats.data.reduce((a, s) => a + s.cpuPercent, 0);
    const mem = stats.data.reduce((a, s) => a + s.cgroup.memoryUsageBytes, 0);
    setCpuHist((h) => [...h.slice(-59), cpu]);
    setMemHist((h) => [...h.slice(-59), mem]);
  }, [stats.data]);

  const i = info.data;
  const running = (containers.data ?? []).filter((c) => c.state === "running");
  const cpuNow = cpuHist[cpuHist.length - 1] ?? 0;
  const memNow = memHist[memHist.length - 1] ?? 0;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Visão geral</h1>
          {i ? (
            <dl className="meta">
              <div><dt>Host</dt><dd>{i.hostname}</dd></div>
              <div><dt>Kernel</dt><dd>{i.kernel}</dd></div>
              <div><dt>Cgroups</dt><dd>v{i.cgroupVersion}</dd></div>
              <div><dt>Armazenamento</dt><dd>{i.storageDriver}</dd></div>
            </dl>
          ) : (
            <p className="muted">Carregando...</p>
          )}
        </div>
        <a className="btn primary" href="#/containers?novo=1">
          Novo container
        </a>
      </header>
      {info.error && <p className="error-text">{info.error}</p>}
      <div className="stats-row">
        <Stat label="Rodando" value={i?.counts.running ?? "–"} hint={`de ${i?.counts.containers ?? 0} containers`} />
        <Stat label="Parados" value={i?.counts.exited ?? "–"} hint={`${i?.counts.created ?? 0} criados sem iniciar`} />
        <Stat label="Imagens" value={i?.counts.images ?? "–"} />
        <Stat label="Host" value={i ? `${i.cpus} CPUs` : "–"} hint={i ? `${bytes(i.memoryBytes)} de memória` : ""} />
      </div>
      <div className="grid-2">
        <Card title="CPU dos containers">
          <div className="metric-big">{cpuNow.toFixed(1)}%</div>
          <Sparkline values={cpuHist} max={Math.max(100, ...cpuHist)} />
          <p className="muted small">100% equivale a um núcleo inteiro. Atualiza a cada 2 segundos.</p>
        </Card>
        <Card title="Memória dos containers">
          <div className="metric-big">{bytes(memNow)}</div>
          <Sparkline values={memHist} />
          {i && <Meter value={memNow} max={i.memoryBytes} />}
        </Card>
      </div>
      <Card title="Em execução">
        {running.length === 0 ? (
          <Empty>Nenhum container rodando agora.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Nome</th>
                <th>Imagem</th>
                <th>Estado</th>
                <th>CPU</th>
                <th>Memória</th>
                <th>Processos</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              {running.map((c) => {
                const s = stats.data?.find((x) => x.id === c.id);
                return (
                  <tr key={c.id} className="clickable row-running" onClick={() => (location.hash = `/containers/${c.id}`)}>
                    <td>
                      <strong>{c.name}</strong>
                      <div className="muted small">há {since(c.startedAt)}</div>
                    </td>
                    <td className="mono">{c.image}</td>
                    <td>
                      <StateBadge state={c.state} />
                    </td>
                    <td className="cell-meter">
                      <span>{s ? `${s.cpuPercent.toFixed(1)}%` : "–"}</span>
                      <Meter value={s?.cpuPercent ?? 0} max={(c.spec.resources.cpus || 1) * 100} />
                    </td>
                    <td className="cell-meter">
                      <span>
                        {s ? bytes(s.cgroup.memoryUsageBytes) : "–"}
                        {s && s.cgroup.memoryLimitBytes ? ` / ${bytes(s.cgroup.memoryLimitBytes)}` : ""}
                      </span>
                      <Meter value={s?.memoryPercent ?? 0} />
                    </td>
                    <td>{s ? `${s.cgroup.pidsCurrent}${s.cgroup.pidsLimit ? ` / ${s.cgroup.pidsLimit}` : ""}` : "–"}</td>
                    <td className="mono">{c.ip || c.spec.network}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
