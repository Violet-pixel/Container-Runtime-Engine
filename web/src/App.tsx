import { createContext, useCallback, useContext, useEffect, useState } from "react";
import { ApiError, api, getToken, setToken } from "./api";
import { Logo } from "./components/ui";
import Overview from "./pages/Overview";
import Containers from "./pages/Containers";
import ContainerDetail from "./pages/ContainerDetail";
import Images from "./pages/Images";
import Events from "./pages/Events";

type Toast = { id: number; text: string; kind: "ok" | "err" };
const ToastCtx = createContext<(text: string, kind?: "ok" | "err") => void>(() => undefined);
export const useToast = () => useContext(ToastCtx);

function useHash() {
  const [hash, setHash] = useState(location.hash.slice(1) || "/");
  useEffect(() => {
    const on = () => setHash(location.hash.slice(1) || "/");
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return hash;
}

export const go = (path: string) => {
  location.hash = path;
};

const nav = [
  { path: "/", label: "Visão geral" },
  { path: "/containers", label: "Containers" },
  { path: "/images", label: "Imagens" },
  { path: "/events", label: "Eventos" },
];

function Login({ onDone }: { onDone: () => void }) {
  const [value, setValue] = useState("");
  const [err, setErr] = useState("");
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setToken(value.trim());
    try {
      await api.info();
      onDone();
    } catch {
      setErr("Token recusado pelo servidor.");
    }
  };
  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <div className="brand big">
          <Logo size={44} />
          <span>Silo</span>
        </div>
        <p className="muted">Este servidor exige um token. Use o mesmo valor passado em --token ou SILO_TOKEN.</p>
        <label className="field">
          <span>Token</span>
          <input type="password" value={value} onChange={(e) => setValue(e.target.value)} autoFocus />
        </label>
        {err && <p className="error-text">{err}</p>}
        <button className="btn primary" type="submit">
          Entrar
        </button>
      </form>
    </div>
  );
}

export default function App() {
  const hash = useHash();
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [auth, setAuth] = useState<"checking" | "ok" | "needed">("checking");

  const toast = useCallback((text: string, kind: "ok" | "err" = "ok") => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t, { id, text, kind }]);
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4200);
  }, []);

  useEffect(() => {
    api
      .info()
      .then(() => setAuth("ok"))
      .catch((e) => setAuth(e instanceof ApiError && e.status === 401 ? "needed" : "ok"));
  }, []);

  if (auth === "checking") return null;
  if (auth === "needed") return <Login onDone={() => setAuth("ok")} />;

  const detail = hash.match(/^\/containers\/([^/]+)/);
  let page;
  if (detail) page = <ContainerDetail id={detail[1]} />;
  else if (hash.startsWith("/containers")) page = <Containers />;
  else if (hash.startsWith("/images")) page = <Images />;
  else if (hash.startsWith("/events")) page = <Events />;
  else page = <Overview />;

  return (
    <ToastCtx.Provider value={toast}>
      <div className="shell">
        <aside className="side">
          <a className="brand" href="#/">
            <Logo />
            <span>Silo</span>
          </a>
          <nav>
            {nav.map((n) => {
              const active = n.path === "/" ? hash === "/" : hash.startsWith(n.path);
              return (
                <a key={n.path} href={`#${n.path}`} className={active ? "active" : ""}>
                  {n.label}
                </a>
              );
            })}
          </nav>
          <div className="side-foot">
            <a href="/api/openapi.yaml" target="_blank" rel="noreferrer">
              OpenAPI
            </a>
            {getToken() && (
              <button
                className="linklike"
                onClick={() => {
                  setToken("");
                  location.reload();
                }}
              >
                Sair
              </button>
            )}
          </div>
        </aside>
        <main className="main">{page}</main>
        <div className="toasts" aria-live="polite">
          {toasts.map((t) => (
            <div key={t.id} className={`toast toast-${t.kind}`}>
              {t.text}
            </div>
          ))}
        </div>
      </div>
    </ToastCtx.Provider>
  );
}
