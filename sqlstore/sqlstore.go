// Package sqlstore 提供基于 database/sql 的 ihtml.Store 实现, 用于生产部署。
// 不绑定任何驱动与 ORM; 支持 postgres / mysql / sqlite 三种方言。
//
//	db, _ := sql.Open("pgx", dsn)
//	store, _ := sqlstore.New(db, sqlstore.Postgres)
//	_ = store.Init(ctx) // 建表(幂等)
//	handler := ihtml.New(store, ...)
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zdypro888/ihtml"
)

// Dialect 是 SQL 方言。
type Dialect string

const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	SQLite   Dialect = "sqlite"
)

// Store 实现 ihtml.Store。
type Store struct {
	db      *sql.DB
	dialect Dialect
}

// New 创建 Store。db 由调用方负责打开与关闭。
func New(db *sql.DB, dialect Dialect) (*Store, error) {
	switch dialect {
	case Postgres, MySQL, SQLite:
	default:
		return nil, fmt.Errorf("sqlstore: unsupported dialect %q", dialect)
	}
	return &Store{db: db, dialect: dialect}, nil
}

// rebind 把 ? 占位符按方言重写(postgres 用 $N)。
func (s *Store) rebind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, s.rebind(query), args...)
	return err
}

// Init 创建数据表(幂等)。也可以不调用, 由 DBA 按此结构自行建表。
func (s *Store) Init(ctx context.Context) error {
	text := "TEXT"
	if s.dialect == MySQL {
		text = "VARCHAR(255)" // mysql 主键列不能用无长度 TEXT
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ihtml_items (
			usr ` + text + ` NOT NULL,
			id ` + text + ` NOT NULL,
			typ TEXT NOT NULL,
			title TEXT NOT NULL,
			page TEXT NOT NULL,
			ord INTEGER NOT NULL,
			content TEXT NOT NULL,
			meta TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (usr, id)
		)`,
		`CREATE TABLE IF NOT EXISTS ihtml_kv (
			usr ` + text + ` NOT NULL,
			k ` + text + ` NOT NULL,
			v TEXT NOT NULL,
			PRIMARY KEY (usr, k)
		)`,
		`CREATE TABLE IF NOT EXISTS ihtml_revisions (
			usr ` + text + ` NOT NULL,
			id ` + text + ` NOT NULL,
			note TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			item_count INTEGER NOT NULL,
			items TEXT NOT NULL,
			PRIMARY KEY (usr, id)
		)`,
	}
	for _, stmt := range stmts {
		if err := s.exec(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: init: %w", err)
		}
	}
	return nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Items 实现 ihtml.Store。
func (s *Store) Items(ctx context.Context, user string) ([]ihtml.Item, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id, typ, title, page, ord, content, meta, updated_at FROM ihtml_items WHERE usr = ?`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ihtml.Item
	for rows.Next() {
		var it ihtml.Item
		var typ, meta, updated string
		if err := rows.Scan(&it.ID, &typ, &it.Title, &it.Page, &it.Order, &it.Content, &meta, &updated); err != nil {
			return nil, err
		}
		it.Type = ihtml.ItemType(typ)
		it.UpdatedAt = parseTS(updated)
		if meta != "" && meta != "null" {
			_ = json.Unmarshal([]byte(meta), &it.Meta)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ReplaceItems 实现 ihtml.Store。
func (s *Store) ReplaceItems(ctx context.Context, user string, items []ihtml.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit 成功后回滚是空操作
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM ihtml_items WHERE usr = ?`), user); err != nil {
		return err
	}
	for _, it := range items {
		meta, _ := json.Marshal(it.Meta)
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO ihtml_items (usr, id, typ, title, page, ord, content, meta, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			user, it.ID, string(it.Type), it.Title, it.Page, it.Order, it.Content, string(meta), ts(it.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// KVGet 实现 ihtml.Store。
func (s *Store) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	var v string
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT v FROM ihtml_kv WHERE usr = ? AND k = ?`), user, key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, ihtml.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(v), nil
}

// KVSet 实现 ihtml.Store。删+插在事务里完成, 跨方言可移植。
func (s *Store) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM ihtml_kv WHERE usr = ? AND k = ?`), user, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO ihtml_kv (usr, k, v) VALUES (?, ?, ?)`),
		user, key, string(value)); err != nil {
		return err
	}
	return tx.Commit()
}

// KVDelete 实现 ihtml.Store。
func (s *Store) KVDelete(ctx context.Context, user, key string) error {
	return s.exec(ctx, `DELETE FROM ihtml_kv WHERE usr = ? AND k = ?`, user, key)
}

// KVKeys 实现 ihtml.Store。
func (s *Store) KVKeys(ctx context.Context, user string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT k FROM ihtml_kv WHERE usr = ?`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// EraseUser 实现 ihtml.Store。
func (s *Store) EraseUser(ctx context.Context, user string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, table := range []string{"ihtml_items", "ihtml_kv", "ihtml_revisions"} {
		if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM `+table+` WHERE usr = ?`), user); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Revisions 实现 ihtml.Store。
func (s *Store) Revisions(ctx context.Context, user string) ([]ihtml.RevisionInfo, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id, note, created_by, created_at, item_count FROM ihtml_revisions
		 WHERE usr = ? ORDER BY created_at DESC`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ihtml.RevisionInfo
	for rows.Next() {
		var info ihtml.RevisionInfo
		var created string
		if err := rows.Scan(&info.ID, &info.Note, &info.By, &created, &info.ItemCount); err != nil {
			return nil, err
		}
		info.CreatedAt = parseTS(created)
		out = append(out, info)
	}
	return out, rows.Err()
}

// RevisionItems 实现 ihtml.Store。
func (s *Store) RevisionItems(ctx context.Context, user, id string) ([]ihtml.Item, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT items FROM ihtml_revisions WHERE usr = ? AND id = ?`), user, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, ihtml.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var items []ihtml.Item
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("sqlstore: corrupt revision %s: %w", id, err)
	}
	return items, nil
}

// AddRevision 实现 ihtml.Store。
func (s *Store) AddRevision(ctx context.Context, user string, rev ihtml.Revision, keep int) error {
	raw, err := json.Marshal(rev.Items)
	if err != nil {
		return err
	}
	if err := s.exec(ctx,
		`INSERT INTO ihtml_revisions (usr, id, note, created_by, created_at, item_count, items)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		user, rev.ID, rev.Note, rev.By, ts(rev.CreatedAt), len(rev.Items), string(raw)); err != nil {
		return err
	}
	if keep <= 0 {
		return nil
	}
	// 裁剪: 先查出超额的旧快照 id 再删, 避免各方言 DELETE+LIMIT 语法差异
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id FROM ihtml_revisions WHERE usr = ? ORDER BY created_at DESC`), user)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids[min(keep, len(ids)):] {
		if err := s.exec(ctx, `DELETE FROM ihtml_revisions WHERE usr = ? AND id = ?`, user, id); err != nil {
			return err
		}
	}
	return nil
}
