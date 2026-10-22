package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Violet-pixel/Container-Runtime-Engine/internal/config"
	"github.com/Violet-pixel/Container-Runtime-Engine/internal/store"
)

type Service struct {
	cfg   config.Config
	store *store.Store
}

func NewService(cfg config.Config, st *store.Store) *Service {
	return &Service{cfg: cfg, store: st}
}

func idFor(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Service) Pull(refStr string, progress Progress) (*store.Image, error) {
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	ref, err := ParseReference(refStr)
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(s.cfg.TmpDir(), "pull-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	rootfs := filepath.Join(staging, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return nil, err
	}
	s.event("pull.start", ref.String(), "", nil)
	p, err := pull(ref, rootfs, staging, progress)
	if err != nil {
		s.event("pull.error", ref.String(), "", map[string]string{"error": err.Error()})
		return nil, err
	}
	id := idFor(ref.String() + p.Digest)
	img := &store.Image{
		ID:        id,
		Reference: ref.String(),
		Digest:    p.Digest,
		Source:    "registry",
		SizeBytes: DirSize(rootfs),
		Layers:    p.Layers,
		Config: store.ImageConfig{
			Env: p.Config.Config.Env, Entrypoint: p.Config.Config.Entrypoint, Cmd: p.Config.Config.Cmd,
			WorkingDir: p.Config.Config.WorkingDir, User: p.Config.Config.User, Labels: p.Config.Config.Labels,
			Arch: p.Config.Architecture, OS: p.Config.OS,
		},
		CreatedAt: time.Now(),
	}
	if err := s.commit(img, rootfs); err != nil {
		return nil, err
	}
	s.event("pull", img.Reference, img.ID, map[string]string{"digest": img.Digest})
	return img, nil
}

func (s *Service) Import(r io.Reader, reference string, cmd []string) (*store.Image, error) {
	if reference == "" {
		return nil, errors.New("informe um nome para a imagem")
	}
	if !strings.Contains(reference, ":") {
		reference += ":latest"
	}
	staging, err := os.MkdirTemp(s.cfg.TmpDir(), "import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	rootfs := filepath.Join(staging, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := ApplyLayer(rootfs, io.TeeReader(r, h)); err != nil {
		return nil, fmt.Errorf("arquivo inválido: %w", err)
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh"}
	}
	img := &store.Image{
		ID:        idFor(reference + digest),
		Reference: reference,
		Digest:    digest,
		Source:    "import",
		SizeBytes: DirSize(rootfs),
		Layers:    1,
		Config: store.ImageConfig{
			Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Cmd: cmd, Arch: "", OS: "linux",
		},
		CreatedAt: time.Now(),
	}
	if err := s.commit(img, rootfs); err != nil {
		return nil, err
	}
	s.event("import", img.Reference, img.ID, map[string]string{"digest": img.Digest})
	return img, nil
}

func (s *Service) commit(img *store.Image, staged string) error {
	dest := filepath.Join(s.cfg.ImagesDir(), img.ID)
	if _, err := os.Stat(filepath.Join(dest, "rootfs")); err == nil {
		img.Rootfs = filepath.Join(dest, "rootfs")
	} else {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := os.Rename(staged, filepath.Join(dest, "rootfs")); err != nil {
			return err
		}
		img.Rootfs = filepath.Join(dest, "rootfs")
	}
	if old, err := s.store.GetImage(img.Reference); err == nil && old.ID != img.ID {
		if err := s.store.DeleteImage(old.ID); err == nil {
			os.RemoveAll(filepath.Dir(old.Rootfs))
		} else {
			img.Reference = img.Reference + "-" + img.ID[:8]
		}
	}
	return s.store.PutImage(img)
}

func (s *Service) Remove(ref string) (*store.Image, error) {
	img, err := s.store.GetImage(ref)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteImage(img.ID); err != nil {
		return nil, err
	}
	os.RemoveAll(filepath.Join(s.cfg.ImagesDir(), img.ID))
	s.event("delete", img.Reference, img.ID, nil)
	return img, nil
}

func (s *Service) event(action, name, id string, attrs map[string]string) {
	s.store.AddEvent(store.Event{Type: "image", Action: action, ActorID: id, ActorName: name, Attributes: attrs})
}

func (s *Service) Lookup(ref string) (*store.Image, error) {
	img, err := s.store.GetImage(ref)
	if err == nil {
		return img, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if parsed, perr := ParseReference(ref); perr == nil {
		if img, err := s.store.GetImage(parsed.String()); err == nil {
			return img, nil
		}
	}
	if !strings.Contains(ref, ":") {
		if img, err := s.store.GetImage(ref + ":latest"); err == nil {
			return img, nil
		}
	}
	return nil, fmt.Errorf("imagem %q não encontrada (use silo pull %s): %w", ref, ref, store.ErrNotFound)
}
