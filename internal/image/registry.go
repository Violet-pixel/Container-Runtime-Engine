package image

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	mediaOCIIndex    = "application/vnd.oci.image.index.v1+json"
	mediaDockerList  = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerManif = "application/vnd.docker.distribution.manifest.v2+json"
	defaultRegistry  = "registry-1.docker.io"
)

type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

func (r Reference) String() string {
	repo := r.Repository
	if r.Registry == defaultRegistry {
		repo = strings.TrimPrefix(repo, "library/")
	} else {
		repo = r.Registry + "/" + repo
	}
	if r.Digest != "" {
		return repo + "@" + r.Digest
	}
	return repo + ":" + r.Tag
}

func ParseReference(s string) (Reference, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Reference{}, errors.New("referência de imagem vazia")
	}
	ref := Reference{Registry: defaultRegistry, Tag: "latest"}
	if i := strings.Index(s, "@"); i >= 0 {
		ref.Digest = s[i+1:]
		s = s[:i]
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		ref.Registry = parts[0]
		if ref.Registry == "docker.io" || ref.Registry == "index.docker.io" {
			ref.Registry = defaultRegistry
		}
		s = parts[1]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "/") {
		ref.Tag = s[i+1:]
		s = s[:i]
	}
	if s == "" {
		return Reference{}, errors.New("referência de imagem inválida")
	}
	if ref.Registry == defaultRegistry && !strings.Contains(s, "/") {
		s = "library/" + s
	}
	ref.Repository = strings.ToLower(s)
	return ref, nil
}

type Progress func(stage string, current, total int64)

type registryClient struct {
	http   *http.Client
	ref    Reference
	token  string
	scheme string
}

func newClient(ref Reference) *registryClient {
	scheme := "https"
	if strings.HasPrefix(ref.Registry, "localhost") || strings.HasPrefix(ref.Registry, "127.0.0.1") {
		scheme = "http"
	}
	if v := os.Getenv("SILO_INSECURE_REGISTRIES"); v != "" {
		for _, r := range strings.Split(v, ",") {
			if strings.TrimSpace(r) == ref.Registry {
				scheme = "http"
			}
		}
	}
	return &registryClient{http: &http.Client{Timeout: 30 * time.Minute}, ref: ref, scheme: scheme}
}

func (c *registryClient) url(kind, ref string) string {
	return fmt.Sprintf("%s://%s/v2/%s/%s/%s", c.scheme, c.ref.Registry, c.ref.Repository, kind, ref)
}

func parseChallenge(h string) map[string]string {
	out := map[string]string{}
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return out
	}
	h = h[7:]
	for _, part := range splitParams(h) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			out[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
	}
	return out
}

func splitParams(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func (c *registryClient) authenticate(challenge string) error {
	p := parseChallenge(challenge)
	realm := p["realm"]
	if realm == "" {
		return fmt.Errorf("o registro pediu autenticação não suportada: %s", challenge)
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + c.ref.Repository + ":pull"
	}
	q.Set("scope", scope)
	req, _ := http.NewRequest("GET", realm+"?"+q.Encode(), nil)
	if u, pw := os.Getenv("SILO_REGISTRY_USER"), os.Getenv("SILO_REGISTRY_PASSWORD"); u != "" {
		req.SetBasicAuth(u, pw)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("falha ao obter token do registro: HTTP %d", resp.StatusCode)
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return err
	}
	c.token = tok.Token
	if c.token == "" {
		c.token = tok.AccessToken
	}
	return nil
}

func (c *registryClient) get(u string, accept []string) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest("GET", u, nil)
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			ch := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if err := c.authenticate(ch); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == 404 {
				return nil, fmt.Errorf("imagem não encontrada no registro (%s)", c.ref.String())
			}
			return nil, fmt.Errorf("registro respondeu HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return resp, nil
	}
	return nil, errors.New("não autorizado pelo registro")
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		Variant      string `json:"variant"`
	} `json:"platform,omitempty"`
}

type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"`
}

func (c *registryClient) manifest(ref string) (*manifest, string, error) {
	resp, err := c.get(c.url("manifests", ref), []string{mediaOCIIndex, mediaDockerList, mediaOCIManifest, mediaDockerManif})
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, "", err
	}
	if m.MediaType == "" {
		m.MediaType = resp.Header.Get("Content-Type")
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return &m, digest, nil
}

func pickPlatform(list []descriptor) (descriptor, error) {
	arch := runtime.GOARCH
	if a := os.Getenv("SILO_ARCH"); a != "" {
		arch = a
	}
	var fallback *descriptor
	for i, d := range list {
		if d.Platform == nil || d.Platform.OS != "linux" || d.Platform.Architecture != arch {
			continue
		}
		if arch == "arm64" && d.Platform.Variant != "" && d.Platform.Variant != "v8" {
			continue
		}
		if fallback == nil {
			fallback = &list[i]
		}
	}
	if fallback == nil {
		return descriptor{}, fmt.Errorf("a imagem não tem versão para linux/%s", arch)
	}
	return *fallback, nil
}

type pulled struct {
	Digest string
	Config ImageConfigBlob
	Size   int64
	Layers int
}

type ImageConfigBlob struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Env        []string          `json:"Env"`
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		WorkingDir string            `json:"WorkingDir"`
		User       string            `json:"User"`
		Labels     map[string]string `json:"Labels"`
	} `json:"config"`
}

func pull(ref Reference, rootfs, tmp string, progress Progress) (*pulled, error) {
	c := newClient(ref)
	target := ref.Tag
	if ref.Digest != "" {
		target = ref.Digest
	}
	progress("manifesto", 0, 0)
	m, digest, err := c.manifest(target)
	if err != nil {
		return nil, err
	}
	if len(m.Manifests) > 0 || m.MediaType == mediaOCIIndex || m.MediaType == mediaDockerList {
		d, err := pickPlatform(m.Manifests)
		if err != nil {
			return nil, err
		}
		m, _, err = c.manifest(d.Digest)
		if err != nil {
			return nil, err
		}
	}
	if len(m.Layers) == 0 {
		return nil, errors.New("manifesto sem camadas")
	}
	cfgResp, err := c.get(c.url("blobs", m.Config.Digest), nil)
	if err != nil {
		return nil, err
	}
	var cfg ImageConfigBlob
	err = json.NewDecoder(io.LimitReader(cfgResp.Body, 8<<20)).Decode(&cfg)
	cfgResp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("configuração da imagem: %w", err)
	}
	var total int64
	for _, l := range m.Layers {
		total += l.Size
	}
	var done int64
	for i, l := range m.Layers {
		stage := fmt.Sprintf("camada %d/%d", i+1, len(m.Layers))
		if err := c.fetchLayer(l, rootfs, tmp, func(n int64) { progress(stage, done+n, total) }); err != nil {
			return nil, fmt.Errorf("%s: %w", stage, err)
		}
		done += l.Size
		progress(stage, done, total)
	}
	return &pulled{Digest: digest, Config: cfg, Size: total, Layers: len(m.Layers)}, nil
}

type countingReader struct {
	r    io.Reader
	n    int64
	tick func(int64)
	last time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if time.Since(c.last) > 300*time.Millisecond {
		c.last = time.Now()
		c.tick(c.n)
	}
	return n, err
}

func (c *registryClient) fetchLayer(d descriptor, rootfs, tmp string, tick func(int64)) error {
	resp, err := c.get(c.url("blobs", d.Digest), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(tmp, "layer-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	cr := &countingReader{r: resp.Body, tick: tick}
	if _, err := io.Copy(io.MultiWriter(f, h), cr); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if strings.HasPrefix(d.Digest, "sha256:") && got != d.Digest {
		return fmt.Errorf("digest não confere (esperado %s, recebido %s)", d.Digest, got)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = ApplyLayer(rootfs, f)
	return err
}
