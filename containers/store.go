package containers

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Container struct {
	CTID        int       `json:"ctid"`
	IP          string    `json:"ip"`
	Hostname    string    `json:"hostname"`
	HasRelaySSH bool      `json:"has_relay_ssh"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS containers (
			ctid INTEGER PRIMARY KEY,
			ip TEXT NOT NULL,
			hostname TEXT NOT NULL,
			has_relay_ssh BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create table: %w", err)
	}

	return &Store{db: db}, nil
}

// Register upserts a container record.
func (s *Store) Register(ctid int, ip, hostname string, hasRelaySSH bool) (*Container, error) {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO containers (ctid, ip, hostname, has_relay_ssh, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(ctid) DO UPDATE SET
			ip = excluded.ip,
			hostname = excluded.hostname,
			has_relay_ssh = excluded.has_relay_ssh,
			updated_at = excluded.updated_at
	`, ctid, ip, hostname, hasRelaySSH, now, now)
	if err != nil {
		return nil, fmt.Errorf("upsert container %d: %w", ctid, err)
	}
	return s.Get(ctid)
}

// Get retrieves a single container by CTID. Returns nil if not found.
func (s *Store) Get(ctid int) (*Container, error) {
	c := &Container{}
	err := s.db.QueryRow(`
		SELECT ctid, ip, hostname, has_relay_ssh, created_at, updated_at
		FROM containers WHERE ctid = ?
	`, ctid).Scan(&c.CTID, &c.IP, &c.Hostname, &c.HasRelaySSH, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get container %d: %w", ctid, err)
	}
	return c, nil
}

// List returns all registered containers ordered by CTID.
func (s *Store) List() ([]*Container, error) {
	rows, err := s.db.Query(`
		SELECT ctid, ip, hostname, has_relay_ssh, created_at, updated_at
		FROM containers ORDER BY ctid
	`)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer rows.Close()

	var result []*Container
	for rows.Next() {
		c := &Container{}
		if err := rows.Scan(&c.CTID, &c.IP, &c.Hostname, &c.HasRelaySSH, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// Delete removes a container from the registry.
func (s *Store) Delete(ctid int) error {
	result, err := s.db.Exec("DELETE FROM containers WHERE ctid = ?", ctid)
	if err != nil {
		return fmt.Errorf("delete container %d: %w", ctid, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("container %d not found", ctid)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
