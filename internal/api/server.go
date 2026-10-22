package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/runtime"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/ui"
)

type Options struct {
	Addr       string
	Token      string
	CORSOrigin string
}

type Server struct {
	rt   *runtime.Runtime
	opts Options
	log  *slog.Logger
}

func Serve(ctx context.Context, rt *runtime.Runtime, opts Options) error {
	if opts.CORSOrigin == "" {
		opts.CORSOrigin = os.Getenv("SILO_CORS_ORIGIN")
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err != nil {
		return fmt.Errorf("endereço inválido: %w", err)
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback && opts.Token == "" && os.Getenv("SILO_ALLOW_INSECURE") != "1" {
		return errors.New("para expor a API fora do localhost defina um token (--token ou SILO_TOKEN)")
	}
	s := &Server{rt: rt, opts: opts, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	srv := &http.Server{Addr: opts.Addr, Handler: s.Router(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rt.Reconcile()
			}
		}
	}()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("Silo pronto", "endereco", "http://"+opts.Addr, "token", opts.Token != "")
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shut)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.logRequests)
	r.Use(s.cors)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	r.Get("/api/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(openapiSpec)
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/info", s.info)
		r.Get("/stats", s.allStats)
		r.Get("/metrics", s.metrics)
		r.Route("/containers", func(r chi.Router) {
			r.Get("/", s.listContainers)
			r.Post("/", s.createContainer)
			r.Post("/prune", s.prune)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", s.inspect)
				r.Delete("/", s.remove)
				r.Post("/start", s.start)
				r.Post("/stop", s.stop)
				r.Post("/restart", s.restart)
				r.Post("/kill", s.kill)
				r.Post("/exec", s.exec)
				r.Get("/logs", s.logs)
				r.Get("/logs/stream", s.logStream)
				r.Get("/stats", s.stats)
				r.Get("/top", s.top)
			})
		})
		r.Route("/images", func(r chi.Router) {
			r.Get("/", s.listImages)
			r.Post("/pull", s.pull)
			r.Post("/import", s.importImage)
			r.Get("/{id}", s.getImage)
			r.Delete("/{id}", s.removeImage)
		})
		r.Get("/events", s.events)
		r.Get("/events/stream", s.eventStream)
	})
	r.Handle("/*", ui.Handler())
	return r
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Info("http", "metodo", r.Method, "rota", r.URL.Path, "status", ww.Status(),
				"duracao", time.Since(start).Round(time.Millisecond).String(), "id", middleware.GetReqID(r.Context()))
		}
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.CORSOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", s.opts.CORSOrigin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "token ausente ou inválido")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, 404, "not_found", err.Error())
	case errors.Is(err, store.ErrConflict), errors.Is(err, runtime.ErrAlreadyRunning), errors.Is(err, runtime.ErrNotRunning):
		writeError(w, 409, "conflict", err.Error())
	case errors.Is(err, runtime.ErrInvalid), errors.Is(err, store.ErrAmbiguous):
		writeError(w, 400, "invalid", err.Error())
	default:
		writeError(w, 500, "internal", err.Error())
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: JSON inválido: %v", runtime.ErrInvalid, err)
	}
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
