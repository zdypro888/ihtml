// Package sqlstore persists Eino events and opaque checkpoints in PostgreSQL.
// The host owns the database, schema migration, authorization and retention.
// Model working memory must never be served as a product transcript.
// Appending is atomic, but does not lock execution of a model turn. Hosts must
// enforce single-writer session ownership before running, deleting or resuming.
package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const MaxPayloadBytes = 16 << 20

type Config struct {
	Namespace       string
	EventSerializer schema.Serializer
	// BeforeWrite runs inside every mutation transaction before storage changes.
	// Hosts can lock and verify their execution-owner row here, making fencing
	// atomic with event/checkpoint writes. Return an error to roll back the write.
	// Read methods do not call it; read authorization remains the host's job.
	BeforeWrite func(context.Context, *sql.Tx) error
}

type Store[M adk.MessageType] struct {
	db          *sql.DB
	namespace   string
	serializer  schema.Serializer
	beforeWrite func(context.Context, *sql.Tx) error
}

func New[M adk.MessageType](db *sql.DB, cfg Config) (*Store[M], error) {
	if db == nil || strings.TrimSpace(cfg.Namespace) == "" || len(cfg.Namespace) > 128 {
		return nil, errors.New("eino sqlstore: database and explicit namespace are required")
	}
	if cfg.EventSerializer == nil {
		cfg.EventSerializer = &schema.HumanReadableSerializer{}
	}
	return &Store[M]{db: db, namespace: cfg.Namespace, serializer: cfg.EventSerializer, beforeWrite: cfg.BeforeWrite}, nil
}

// Init is an explicit migration, never called by reads or New.
func (s *Store[M]) Init(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS ihtml_agent_sessions (namespace TEXT NOT NULL, session_id TEXT NOT NULL, next_seq BIGINT NOT NULL DEFAULT 0, PRIMARY KEY(namespace,session_id))`,
		`CREATE TABLE IF NOT EXISTS ihtml_agent_events (namespace TEXT NOT NULL, session_id TEXT NOT NULL, seq BIGINT NOT NULL, event_id TEXT NOT NULL, kind TEXT NOT NULL, payload BYTEA NOT NULL, PRIMARY KEY(namespace,session_id,seq), UNIQUE(namespace,session_id,event_id), FOREIGN KEY(namespace,session_id) REFERENCES ihtml_agent_sessions(namespace,session_id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS ihtml_agent_checkpoints (namespace TEXT NOT NULL, checkpoint_id TEXT NOT NULL, payload BYTEA NOT NULL, PRIMARY KEY(namespace,checkpoint_id))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validID(id string) error {
	if id == "" || len(id) > 2048 || strings.ContainsAny(id, "\x00\r\n") {
		return errors.New("eino sqlstore: invalid identifier")
	}
	return nil
}

func (s *Store[M]) write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx, tx); err != nil {
			return err
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store[M]) AppendEvents(ctx context.Context, id string, events []*adk.SessionEvent[M]) error {
	if err := validID(id); err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	type encoded struct {
		id   string
		kind adk.SessionEventKind
		body []byte
	}
	batch := make([]encoded, 0, len(events))
	seen := map[string]bool{}
	for _, e := range events {
		if e == nil || e.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if seen[e.EventID] {
			return adk.ErrDuplicateEventID
		}
		seen[e.EventID] = true
		// Normalize a detached envelope; no mutable caller values are retained.
		clone := *e
		if err := adk.NormalizeSessionEventKind(&clone); err != nil {
			return err
		}
		body, err := s.serializer.Marshal(&clone)
		if err != nil {
			return err
		}
		if len(body) > MaxPayloadBytes {
			return errors.New("eino sqlstore: event exceeds payload limit")
		}
		batch = append(batch, encoded{e.EventID, clone.Kind, body})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx, tx); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ihtml_agent_sessions(namespace,session_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, s.namespace, id); err != nil {
		return err
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT next_seq FROM ihtml_agent_sessions WHERE namespace=$1 AND session_id=$2 FOR UPDATE`, s.namespace, id).Scan(&seq); err != nil {
		return err
	}
	for _, e := range batch {
		seq++
		_, err = tx.ExecContext(ctx, `INSERT INTO ihtml_agent_events(namespace,session_id,seq,event_id,kind,payload) VALUES($1,$2,$3,$4,$5,$6)`, s.namespace, id, seq, e.id, string(e.kind), e.body)
		if err != nil {
			var state interface{ SQLState() string }
			if errors.As(err, &state) && state.SQLState() == "23505" {
				return adk.ErrDuplicateEventID
			}
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ihtml_agent_sessions SET next_seq=$3 WHERE namespace=$1 AND session_id=$2`, s.namespace, id, seq); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store[M]) LoadEvents(ctx context.Context, id string, req *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	if err := validID(id); err != nil {
		return nil, err
	}
	if req == nil {
		req = &adk.LoadSessionEventsRequest{}
	}
	if req.Limit < 0 || req.Limit == math.MaxInt {
		return nil, errors.New("eino sqlstore: invalid page limit")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := `SELECT event_id,kind,payload FROM ihtml_agent_events WHERE namespace=$1 AND session_id=$2`
	args := []any{s.namespace, id}
	if req.After != "" {
		var seq int64
		err = tx.QueryRowContext(ctx, `SELECT seq FROM ihtml_agent_events WHERE namespace=$1 AND session_id=$2 AND event_id=$3`, s.namespace, id, req.After).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, adk.ErrEventIDOutOfRange
		}
		if err != nil {
			return nil, err
		}
		op := ">"
		if req.Reverse {
			op = "<"
		}
		args = append(args, seq)
		q += fmt.Sprintf(" AND seq%s$%d", op, len(args))
	}
	if len(req.Kinds) > 0 {
		holders := make([]string, 0, len(req.Kinds))
		for _, kind := range req.Kinds {
			args = append(args, string(kind))
			holders = append(holders, fmt.Sprintf("$%d", len(args)))
		}
		q += " AND kind IN (" + strings.Join(holders, ",") + ")"
	}
	q += " ORDER BY seq"
	if req.Reverse {
		q += " DESC"
	}
	if req.Limit > 0 {
		args = append(args, req.Limit+1)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := &adk.LoadSessionEventsResult[M]{Events: []*adk.SessionEvent[M]{}}
	for rows.Next() {
		var b []byte
		var eventID, kind string
		if err = rows.Scan(&eventID, &kind, &b); err != nil {
			rows.Close()
			return nil, err
		}
		if len(b) > MaxPayloadBytes {
			rows.Close()
			return nil, errors.New("eino sqlstore: stored event exceeds payload limit")
		}
		var e adk.SessionEvent[M]
		if err = s.serializer.Unmarshal(b, &e); err != nil {
			rows.Close()
			return nil, err
		}
		if e.EventID != eventID || string(e.Kind) != kind {
			rows.Close()
			return nil, errors.New("eino sqlstore: inconsistent stored event envelope")
		}
		out.Events = append(out.Events, &e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if req.Limit > 0 && len(out.Events) > req.Limit {
		out.Events = out.Events[:req.Limit]
		out.Next = out.Events[len(out.Events)-1].EventID
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store[M]) Get(ctx context.Context, id string) ([]byte, bool, error) {
	if err := validID(id); err != nil {
		return nil, false, err
	}
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM ihtml_agent_checkpoints WHERE namespace=$1 AND checkpoint_id=$2`, s.namespace, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(b) > MaxPayloadBytes {
		return nil, false, errors.New("eino sqlstore: stored checkpoint exceeds payload limit")
	}
	return b, true, nil
}
func (s *Store[M]) Set(ctx context.Context, id string, b []byte) error {
	if err := validID(id); err != nil {
		return err
	}
	if len(b) > MaxPayloadBytes {
		return errors.New("eino sqlstore: checkpoint exceeds payload limit")
	}
	if b == nil {
		b = []byte{}
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO ihtml_agent_checkpoints(namespace,checkpoint_id,payload) VALUES($1,$2,$3) ON CONFLICT(namespace,checkpoint_id) DO UPDATE SET payload=EXCLUDED.payload`, s.namespace, id, b)
		return err
	})
}
func (s *Store[M]) Delete(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM ihtml_agent_checkpoints WHERE namespace=$1 AND checkpoint_id=$2`, s.namespace, id)
		return err
	})
}
func (s *Store[M]) DeleteSession(ctx context.Context, id string) error {
	if err := validID(id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.beforeWrite != nil {
		if err := s.beforeWrite(ctx, tx); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ihtml_agent_sessions WHERE namespace=$1 AND session_id=$2`, s.namespace, id); err != nil {
		return err
	}
	// Eino's session checkpoint and this runtime's approval checkpoint have exact identities.
	if _, err = tx.ExecContext(ctx, `DELETE FROM ihtml_agent_checkpoints WHERE namespace=$1 AND checkpoint_id IN($2,$3)`, s.namespace, id+"/runner_checkpoint", fmt.Sprintf("ihtml:approval:%x", sha256.Sum256([]byte(id)))); err != nil {
		return err
	}
	return tx.Commit()
}
