import { useEffect, useState } from "react";
import { api, SiloEvent, socket } from "../api";
import { Card, Empty } from "../components/ui";

const actionLabel: Record<string, string> = {
  create: "criado",
  start: "iniciado",
  stop: "parada pedida",
  die: "terminou",
  kill: "sinal enviado",
  restart: "reiniciado",
  destroy: "removido",
  exec: "comando executado",
  oom: "sem memória",
  error: "erro",
  pull: "baixada",
  "pull.start": "download iniciado",
  "pull.error": "falha no download",
  import: "importada",
  delete: "removida",
  "port-error": "porta indisponível",
};

export default function Events() {
  const [events, setEvents] = useState<SiloEvent[]>([]);
  const [live, setLive] = useState(false);

  useEffect(() => {
    api.events(300).then((list) => setEvents(list.reverse())).catch(() => undefined);
    const ws = socket("/events/stream");
    ws.onopen = () => setLive(true);
    ws.onclose = () => setLive(false);
    ws.onmessage = (e) => {
      const ev = JSON.parse(e.data) as SiloEvent;
      setEvents((list) => [ev, ...list].slice(0, 500));
    };
    return () => ws.close();
  }, []);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Eventos</h1>
          <p className="muted">Tudo o que acontece com containers e imagens, inclusive o que foi feito pela linha de comando.</p>
        </div>
        <span className={`live ${live ? "on" : ""}`}>{live ? "ao vivo" : "desconectado"}</span>
      </header>
      <Card>
        {events.length === 0 ? (
          <Empty>Nenhum evento registrado.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Quando</th>
                <th>Tipo</th>
                <th>Ação</th>
                <th>Alvo</th>
                <th>Detalhes</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td className="small mono">{new Date(e.time).toLocaleString("pt-BR")}</td>
                  <td>{e.type === "container" ? "container" : "imagem"}</td>
                  <td>
                    <span className={`ev ev-${e.action.replace(".", "-")}`}>{actionLabel[e.action] ?? e.action}</span>
                  </td>
                  <td>
                    {e.type === "container" ? <a href={`#/containers/${e.actorId}`}>{e.actorName}</a> : <span className="mono">{e.actorName}</span>}
                  </td>
                  <td className="small mono truncate">
                    {Object.entries(e.attributes ?? {})
                      .map(([k, v]) => `${k}=${v}`)
                      .join("  ")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
