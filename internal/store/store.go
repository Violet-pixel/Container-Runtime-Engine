package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var (
	ErrNotFound  = errors.New("não encontrado")
	ErrConflict  = errors.New("conflito")
	ErrAmbiguous = errors.New("identificador ambíguo")
)

type Store struct {
	db     *sql.DB
	retain int
}

var migrations = []string{
	`CREATE TABLE images (
		id          TEXT PRIMARY KEY,
		reference   TEXT NOT NULL UNIQUE,
		digest      TEXT NOT NULL DEFAULT '',
		source      TEXT NOT NULL DEFAULT 'registry',
		size_bytes  INTEGER NOT NULL DEFAULT 0,
		layers      INTEGER NOT NULL DEFAULT 0,
		config_json TEXT NOT NULL DEFAULT '{}',
		rootfs      TEXT NOT NULL,
		created_at  TEXT NOT NULL
	);
	CREATE TABLE containers (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL UNIQUE,
		image_id       TEXT NOT NULL REFERENCES images(id) ON DELETE RESTRICT,
		image_ref      TEXT NOT NULL,
		spec_json      TEXT NOT NULL,
		command_json   TEXT NOT NULL DEFAULT '[]',
		state          TEXT NOT NULL CHECK (state IN ('created','running','stopping','exited')),
		pid            INTEGER NOT NULL DEFAULT 0,
		pid_start      INTEGER NOT NULL DEFAULT 0,
		shim_pid       INTEGER NOT NULL DEFAULT 0,
		ip             TEXT NOT NULL DEFAULT '',
		storage        TEXT NOT NULL DEFAULT 'overlay',
		exit_code      INTEGER NOT NULL DEFAULT 0,
		error          TEXT NOT NULL DEFAULT '',
		restart_count  INTEGER NOT NULL DEFAULT 0,
		stop_requested INTEGER NOT NULL DEFAULT 0,
		created_at     TEXT NOT NULL,
		started_at     TEXT,
		finished_at    TEXT
	);
	CREATE INDEX idx_containers_state ON containers(state);
	CREATE INDEX idx_containers_image ON containers(image_id);
	CREATE TABLE port_mappings (
		container_id   TEXT NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
		host_ip        TEXT NOT NULL DEFAULT '0.0.0.0',
		host_port      INTEGER NOT NULL CHECK (host_port BETWEEN 1 AND 65535),
		container_port INTEGER NOT NULL CHECK (container_port BETWEEN 1 AND 65535),
		protocol       TEXT NOT NULL DEFAULT 'tcp' CHECK (protocol IN ('tcp','udp')),
		PRIMARY KEY (container_id, host_port, protocol)
	);
	CREATE INDEX idx_ports_host ON port_mappings(host_port, protocol);
	CREATE TABLE events (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		time            TEXT NOT NULL,
		type            TEXT NOT NULL,
		action          TEXT NOT NULL,
		actor_id        TEXT NOT NULL DEFAULT '',
		actor_name      TEXT NOT NULL DEFAULT '',
		attributes_json TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX idx_events_actor ON events(actor_id);`,
}

func Open(path string, retain int) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=10000&_journal_mode=WAL&_foreign_keys=on&_synchronous=NORMAL", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, retain: retain}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	for i := current; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migração %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, i+1, ts(time.Now())); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseTSPtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTS(ns.String)
	return &t
}

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Store) PutImage(img *Image) error {
	cfg, _ := json.Marshal(img.Config)
	_, err := s.db.Exec(`INSERT INTO images(id, reference, digest, source, size_bytes, layers, config_json, rootfs, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(reference) DO UPDATE SET id=excluded.id, digest=excluded.digest, source=excluded.source,
			size_bytes=excluded.size_bytes, layers=excluded.layers, config_json=excluded.config_json,
			rootfs=excluded.rootfs, created_at=excluded.created_at`,
		img.ID, img.Reference, img.Digest, img.Source, img.SizeBytes, img.Layers, string(cfg), img.Rootfs, ts(img.CreatedAt))
	return err
}

const imageCols = `id, reference, digest, source, size_bytes, layers, config_json, rootfs, created_at`

func scanImage(row interface{ Scan(...any) error }) (*Image, error) {
	var img Image
	var cfg, created string
	if err := row.Scan(&img.ID, &img.Reference, &img.Digest, &img.Source, &img.SizeBytes, &img.Layers, &cfg, &img.Rootfs, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(cfg), &img.Config)
	img.CreatedAt = parseTS(created)
	return &img, nil
}

func (s *Store) ListImages() ([]Image, error) {
	rows, err := s.db.Query(`SELECT ` + imageCols + ` FROM images ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Image{}
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *img)
	}
	return out, rows.Err()
}

func (s *Store) GetImage(ref string) (*Image, error) {
	img, err := scanImage(s.db.QueryRow(`SELECT `+imageCols+` FROM images WHERE reference = ? OR id = ?`, ref, ref))
	if err == nil {
		return img, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT `+imageCols+` FROM images WHERE id LIKE ? LIMIT 2`, ref+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, img)
	}
	switch len(found) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return found[0], nil
	}
	return nil, ErrAmbiguous
}

func (s *Store) DeleteImage(id string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM containers WHERE image_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: a imagem está em uso por %d container(s)", ErrConflict, n)
	}
	res, err := s.db.Exec(`DELETE FROM images WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ImageUsage(id string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM images WHERE rootfs = (SELECT rootfs FROM images WHERE id = ?)`, id).Scan(&n)
	return n, err
}

const containerCols = `id, name, image_id, image_ref, spec_json, command_json, state, pid, pid_start, shim_pid, ip, storage,
	exit_code, error, restart_count, stop_requested, created_at, started_at, finished_at`

func scanContainer(row interface{ Scan(...any) error }) (*Container, error) {
	var c Container
	var spec, cmd, created string
	var started, finished sql.NullString
	var stopReq int
	if err := row.Scan(&c.ID, &c.Name, &c.ImageID, &c.ImageRef, &spec, &cmd, &c.State, &c.Pid, &c.PidStart, &c.ShimPid, &c.IP,
		&c.Storage, &c.ExitCode, &c.Error, &c.RestartCount, &stopReq, &created, &started, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(spec), &c.Spec)
	_ = json.Unmarshal([]byte(cmd), &c.Command)
	c.StopRequested = stopReq == 1
	c.CreatedAt = parseTS(created)
	c.StartedAt = parseTSPtr(started)
	c.FinishedAt = parseTSPtr(finished)
	return &c, nil
}

func (s *Store) CreateContainer(c *Container) error {
	spec, _ := json.Marshal(c.Spec)
	cmd, _ := json.Marshal(c.Command)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO containers(`+containerCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.ImageID, c.ImageRef, string(spec), string(cmd), c.State, c.Pid, c.PidStart, c.ShimPid, c.IP, c.Storage,
		c.ExitCode, c.Error, c.RestartCount, 0, ts(c.CreatedAt), tsPtr(c.StartedAt), tsPtr(c.FinishedAt))
	if isUnique(err) {
		return fmt.Errorf("%w: já existe um container chamado %q", ErrConflict, c.Name)
	}
	if err != nil {
		return err
	}
	for _, p := range c.Spec.Ports {
		if _, err := tx.Exec(`INSERT INTO port_mappings(container_id, host_ip, host_port, container_port, protocol) VALUES (?,?,?,?,?)`,
			c.ID, p.HostIP, p.HostPort, p.ContainerPort, p.Protocol); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateContainer(c *Container) error {
	stop := 0
	if c.StopRequested {
		stop = 1
	}
	res, err := s.db.Exec(`UPDATE containers SET state=?, pid=?, pid_start=?, shim_pid=?, ip=?, storage=?, exit_code=?, error=?,
		restart_count=?, stop_requested=?, started_at=?, finished_at=? WHERE id=?`,
		c.State, c.Pid, c.PidStart, c.ShimPid, c.IP, c.Storage, c.ExitCode, c.Error, c.RestartCount, stop,
		tsPtr(c.StartedAt), tsPtr(c.FinishedAt), c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetStopRequested(id string, v bool) error {
	n := 0
	if v {
		n = 1
	}
	_, err := s.db.Exec(`UPDATE containers SET stop_requested=? WHERE id=?`, n, id)
	return err
}

func (s *Store) GetContainer(ref string) (*Container, error) {
	c, err := scanContainer(s.db.QueryRow(`SELECT `+containerCols+` FROM containers WHERE id = ? OR name = ?`, ref, ref))
	if err == nil || !errors.Is(err, ErrNotFound) {
		return c, err
	}
	if len(ref) < 3 {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(`SELECT `+containerCols+` FROM containers WHERE id LIKE ? LIMIT 2`, ref+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, c)
	}
	switch len(found) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return found[0], nil
	}
	return nil, ErrAmbiguous
}

func (s *Store) ListContainers(all bool) ([]Container, error) {
	q := `SELECT ` + containerCols + ` FROM containers`
	if !all {
		q += ` WHERE state IN ('running','stopping')`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Container{}
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteContainer(id string) error {
	res, err := s.db.Exec(`DELETE FROM containers WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UsedIPs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT ip FROM containers WHERE ip <> '' AND state IN ('running','stopping')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		out[ip] = true
	}
	return out, rows.Err()
}

func (s *Store) PortInUse(port int, proto, except string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT c.name FROM port_mappings p JOIN containers c ON c.id = p.container_id
		WHERE p.host_port=? AND p.protocol=? AND c.state IN ('running','stopping') AND c.id <> ? LIMIT 1`, port, proto, except).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func (s *Store) AddEvent(e Event) (int64, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	attrs, _ := json.Marshal(e.Attributes)
	res, err := s.db.Exec(`INSERT INTO events(time, type, action, actor_id, actor_name, attributes_json) VALUES (?,?,?,?,?,?)`,
		ts(e.Time), e.Type, e.Action, e.ActorID, e.ActorName, string(attrs))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if s.retain > 0 && id%100 == 0 {
		s.db.Exec(`DELETE FROM events WHERE id <= ?`, id-int64(s.retain))
	}
	return id, nil
}

func (s *Store) Events(afterID int64, actor string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if afterID > 0 {
		rows, err = s.db.Query(`SELECT id, time, type, action, actor_id, actor_name, attributes_json FROM events
			WHERE id > ? AND (? = '' OR actor_id = ?) ORDER BY id ASC LIMIT ?`, afterID, actor, actor, limit)
	} else {
		rows, err = s.db.Query(`SELECT * FROM (SELECT id, time, type, action, actor_id, actor_name, attributes_json FROM events
			WHERE (? = '' OR actor_id = ?) ORDER BY id DESC LIMIT ?) ORDER BY id ASC`, actor, actor, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var t, attrs string
		if err := rows.Scan(&e.ID, &t, &e.Type, &e.Action, &e.ActorID, &e.ActorName, &attrs); err != nil {
			return nil, err
		}
		e.Time = parseTS(t)
		_ = json.Unmarshal([]byte(attrs), &e.Attributes)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) LastEventID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM events`).Scan(&id)
	return id, err
}

type Counts struct {
	Containers int `json:"containers"`
	Running    int `json:"running"`
	Exited     int `json:"exited"`
	Created    int `json:"created"`
	Images     int `json:"images"`
}

func (s *Store) Counts() (Counts, error) {
	var c Counts
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM containers),
		(SELECT COUNT(*) FROM containers WHERE state IN ('running','stopping')),
		(SELECT COUNT(*) FROM containers WHERE state='exited'),
		(SELECT COUNT(*) FROM containers WHERE state='created'),
		(SELECT COUNT(*) FROM images)`).Scan(&c.Containers, &c.Running, &c.Exited, &c.Created, &c.Images)
	return c, err
}
