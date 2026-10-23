import { useState } from "react";
import { api, bytes, short, since } from "../api";
import { useToast } from "../App";
import { Card, Empty, Meter, usePoll } from "../components/ui";

export default function Images() {
  const toast = useToast();
  const { data, reload } = usePoll(api.images, 5000);
  const [ref, setRef] = useState("");
  const [progress, setProgress] = useState<{ stage: string; cur: number; total: number } | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [importRef, setImportRef] = useState("");
  const [importCmd, setImportCmd] = useState("/bin/sh");
  const [importing, setImporting] = useState(false);

  const pull = async (e: React.FormEvent) => {
    e.preventDefault();
    setProgress({ stage: "conectando", cur: 0, total: 0 });
    try {
      const img = await api.pull(ref.trim(), (stage, cur, total) => setProgress({ stage, cur, total }));
      toast(`${img.reference} baixada`);
      setRef("");
    } catch (err) {
      toast((err as Error).message, "err");
    } finally {
      setProgress(null);
      reload();
    }
  };

  const doImport = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!file) return;
    setImporting(true);
    try {
      const img = await api.importImage(file, importRef.trim(), importCmd);
      toast(`${img.reference} importada`);
      setFile(null);
      setImportRef("");
    } catch (err) {
      toast((err as Error).message, "err");
    } finally {
      setImporting(false);
      reload();
    }
  };

  const remove = async (id: string, name: string) => {
    if (!window.confirm(`Remover a imagem ${name}?`)) return;
    try {
      await api.removeImage(id);
      toast(`${name} removida`);
    } catch (err) {
      toast((err as Error).message, "err");
    }
    reload();
  };

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>Imagens</h1>
          <p className="muted">Baixe imagens OCI/Docker de qualquer registro ou importe um rootfs em tar.</p>
        </div>
      </header>
      <div className="grid-2">
        <Card title="Baixar do registro">
          <form className="inline-form" onSubmit={pull}>
            <input value={ref} onChange={(e) => setRef(e.target.value)} placeholder="alpine:3.20, nginx:alpine, ghcr.io/org/app:1.0" required disabled={!!progress} />
            <button className="btn primary" disabled={!!progress}>
              Baixar
            </button>
          </form>
          {progress && (
            <div className="progress">
              <span className="small">
                {progress.stage}
                {progress.total ? `: ${bytes(progress.cur)} de ${bytes(progress.total)}` : ""}
              </span>
              <Meter value={progress.cur} max={progress.total || 1} />
            </div>
          )}
        </Card>
        <Card title="Importar rootfs (.tar ou .tar.gz)">
          <form className="form" onSubmit={doImport}>
            <input type="file" accept=".tar,.gz,.tgz" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
            <div className="inline-form">
              <input value={importRef} onChange={(e) => setImportRef(e.target.value)} placeholder="nome:tag" required />
              <input value={importCmd} onChange={(e) => setImportCmd(e.target.value)} placeholder="comando padrão" />
              <button className="btn" disabled={!file || importing}>
                {importing ? "Importando..." : "Importar"}
              </button>
            </div>
          </form>
        </Card>
      </div>
      <Card title="Imagens locais">
        {!data || data.length === 0 ? (
          <Empty>{data ? "Nenhuma imagem ainda." : "Carregando..."}</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Imagem</th>
                <th>ID</th>
                <th>Origem</th>
                <th>Comando padrão</th>
                <th>Camadas</th>
                <th>Tamanho</th>
                <th>Criada</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((i) => (
                <tr key={i.id}>
                  <td>
                    <strong className="mono">{i.reference}</strong>
                  </td>
                  <td className="mono small">{short(i.id)}</td>
                  <td>{i.source === "registry" ? "registro" : "importada"}</td>
                  <td className="mono small truncate">{[...(i.config.entrypoint ?? []), ...(i.config.cmd ?? [])].join(" ")}</td>
                  <td>{i.layers}</td>
                  <td>{bytes(i.sizeBytes)}</td>
                  <td className="small">há {since(i.createdAt)}</td>
                  <td>
                    <div className="row-actions">
                      <a className="btn" href={`#/containers?novo=1&imagem=${encodeURIComponent(i.reference)}`}>
                        Usar
                      </a>
                      <button className="btn ghost danger" onClick={() => remove(i.id, i.reference)}>
                        Remover
                      </button>
                    </div>
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
