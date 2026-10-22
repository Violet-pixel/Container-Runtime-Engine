package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/runtime"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	info, err := s.rt.Info()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") != "false"
	list, err := s.rt.List(all)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

type createRequest struct {
	store.Spec
	Name  string `json:"name"`
	Start *bool  `json:"start"`
	Pull  *bool  `json:"pull"`
}

func (s *Server) createContainer(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	if req.Image == "" {
		writeError(w, 400, "invalid", "informe a imagem")
		return
	}
	if _, err := s.rt.Images.Lookup(req.Image); err != nil {
		if req.Pull != nil && !*req.Pull {
			s.fail(w, err)
			return
		}
		if _, err := s.rt.Images.Pull(req.Image, nil); err != nil {
			s.fail(w, fmt.Errorf("%w: %v", runtime.ErrInvalid, err))
			return
		}
	}
	c, err := s.rt.Create(req.Spec, req.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if req.Start == nil || *req.Start {
		started, err := s.rt.Start(c.ID)
		if err != nil {
			writeJSON(w, 201, map[string]any{"container": started, "warning": err.Error()})
			return
		}
		c = started
	}
	writeJSON(w, 201, map[string]any{"container": c})
}

func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	in, err := s.rt.Inspect(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, in)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	c, err := s.rt.Remove(chi.URLParam(r, "id"), r.URL.Query().Get("force") == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": c.ID})
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	c, err := s.rt.Start(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func timeoutParam(r *http.Request) time.Duration {
	t, _ := strconv.Atoi(r.URL.Query().Get("t"))
	return time.Duration(t) * time.Second
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	c, err := s.rt.Stop(chi.URLParam(r, "id"), timeoutParam(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	c, err := s.rt.Restart(chi.URLParam(r, "id"), timeoutParam(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) kill(w http.ResponseWriter, r *http.Request) {
	sig := syscall.SIGKILL
	if v := r.URL.Query().Get("signal"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 64 {
			writeError(w, 400, "invalid", "sinal inválido")
			return
		}
		sig = syscall.Signal(n)
	}
	c, err := s.rt.Kill(chi.URLParam(r, "id"), sig)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, c)
}

type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cmd     []string `json:"cmd"`
		Env     []string `json:"env"`
		WorkDir string   `json:"workDir"`
		User    string   `json:"user"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	out := &limitedBuffer{max: 1 << 20}
	done := make(chan struct{})
	var code int
	var err error
	go func() {
		code, err = s.rt.Exec(chi.URLParam(r, "id"), runtime.ExecOptions{
			Cmd: req.Cmd, Env: req.Env, WorkDir: req.WorkDir, User: req.User,
			Stdin: strings.NewReader(""), Stdout: out, Stderr: out,
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		writeError(w, 504, "timeout", "o comando passou de 60 segundos; use silo exec no terminal para comandos longos")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"exitCode": code, "output": out.buf.String()})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail == 0 {
		tail = 500
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.rt.Logs(r.Context(), chi.URLParam(r, "id"), tail, false, w); err != nil {
		s.fail(w, err)
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		return strings.HasSuffix(origin, "://"+r.Host)
	},
}

type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := w.conn.WriteMessage(websocket.TextMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func readUntilClose(conn *websocket.Conn, cancel context.CancelFunc) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			cancel()
			return
		}
	}
}

func (s *Server) logStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.rt.Get(id); err != nil {
		s.fail(w, err)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go readUntilClose(conn, cancel)
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	if tail == 0 {
		tail = 300
	}
	s.rt.Logs(ctx, id, tail, true, &wsWriter{conn: conn})
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "fim"), time.Now().Add(time.Second))
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.rt.Stats(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) allStats(w http.ResponseWriter, r *http.Request) {
	list, err := s.rt.List(false)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := []*runtime.Stats{}
	for _, c := range list {
		if st, err := s.rt.Stats(c.ID); err == nil {
			out = append(out, st)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) top(w http.ResponseWriter, r *http.Request) {
	procs, err := s.rt.Top(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, procs)
}

func (s *Server) prune(w http.ResponseWriter, r *http.Request) {
	removed, err := s.rt.Prune()
	if err != nil {
		s.fail(w, err)
		return
	}
	if removed == nil {
		removed = []string{}
	}
	writeJSON(w, 200, map[string]any{"removed": removed})
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	list, err := s.rt.Store.ListImages()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	img, err := s.rt.Images.Lookup(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, img)
}

func (s *Server) removeImage(w http.ResponseWriter, r *http.Request) {
	img, err := s.rt.Images.Lookup(chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.rt.Images.Remove(img.ID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": img.ID})
}

func (s *Server) pull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reference string `json:"reference"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	send := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		enc.Encode(v)
		if flusher != nil {
			flusher.Flush()
		}
	}
	img, err := s.rt.Images.Pull(req.Reference, func(stage string, cur, total int64) {
		send(map[string]any{"stage": stage, "current": cur, "total": total})
	})
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}
	send(map[string]any{"done": true, "image": img})
}

func (s *Server) importImage(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("reference")
	var cmd []string
	if c := r.URL.Query().Get("cmd"); c != "" {
		cmd = strings.Fields(c)
	}
	body := http.MaxBytesReader(w, r.Body, 4<<30)
	img, err := s.rt.Images.Import(body, ref, cmd)
	if err != nil {
		writeError(w, 400, "invalid", err.Error())
		return
	}
	io.Copy(io.Discard, body)
	writeJSON(w, 201, img)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.rt.Store.Events(after, r.URL.Query().Get("actor"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go readUntilClose(conn, cancel)
	last, _ := s.rt.Store.LastEventID()
	ww := &wsWriter{conn: conn}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			ww.mu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			ww.mu.Unlock()
			if err != nil {
				return
			}
		case <-time.After(400 * time.Millisecond):
		}
		list, err := s.rt.Store.Events(last, "", 500)
		if err != nil {
			return
		}
		for _, e := range list {
			b, _ := json.Marshal(e)
			if _, err := ww.Write(b); err != nil {
				return
			}
			last = e.ID
		}
	}
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	info, err := s.rt.Info()
	if err != nil {
		s.fail(w, err)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP silo_containers Containers por estado.\n# TYPE silo_containers gauge\n")
	fmt.Fprintf(&b, "silo_containers{state=\"running\"} %d\n", info.Counts.Running)
	fmt.Fprintf(&b, "silo_containers{state=\"exited\"} %d\n", info.Counts.Exited)
	fmt.Fprintf(&b, "silo_containers{state=\"created\"} %d\n", info.Counts.Created)
	fmt.Fprintf(&b, "# HELP silo_images Imagens armazenadas.\n# TYPE silo_images gauge\nsilo_images %d\n", info.Counts.Images)
	list, _ := s.rt.List(false)
	fmt.Fprintf(&b, "# HELP silo_container_cpu_seconds_total Tempo de CPU consumido.\n# TYPE silo_container_cpu_seconds_total counter\n")
	var mem strings.Builder
	var pids strings.Builder
	for _, c := range list {
		st, err := s.rt.Stats(c.ID)
		if err != nil {
			continue
		}
		lbl := fmt.Sprintf("{id=\"%s\",name=\"%s\"}", c.ID[:12], c.Name)
		fmt.Fprintf(&b, "silo_container_cpu_seconds_total%s %.6f\n", lbl, float64(st.Cgroup.CPUUsageUsec)/1e6)
		fmt.Fprintf(&mem, "silo_container_memory_bytes%s %d\n", lbl, st.Cgroup.MemoryUsage)
		fmt.Fprintf(&pids, "silo_container_pids%s %d\n", lbl, st.Cgroup.PidsCurrent)
	}
	fmt.Fprintf(&b, "# HELP silo_container_memory_bytes Memória em uso.\n# TYPE silo_container_memory_bytes gauge\n%s", mem.String())
	fmt.Fprintf(&b, "# HELP silo_container_pids Processos em execução.\n# TYPE silo_container_pids gauge\n%s", pids.String())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(b.String()))
}
