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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zdypro888/ihtml"
)

// ErrNilDB is returned when New is called without a database handle.
var ErrNilDB = errors.New("sqlstore: db is required")

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

var (
	_ ihtml.Store                   = (*Store)(nil)
	_ ihtml.UserStateStore          = (*Store)(nil)
	_ ihtml.VersionedUserStateStore = (*Store)(nil)
	_ ihtml.RevisionStore           = (*Store)(nil)
)

// New 创建 Store。db 由调用方负责打开与关闭。
func New(db *sql.DB, dialect Dialect) (*Store, error) {
	if db == nil {
		return nil, ErrNilDB
	}
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

func (s *Store) mysqlSchemaNeedsMigration(ctx context.Context) (bool, error) {
	want := map[string]map[string]string{
		"ihtml_items":     {"usr": "varbinary", "id": "varbinary", "content": "longtext", "meta": "longtext"},
		"ihtml_kv":        {"usr": "varbinary", "k": "varbinary", "v": "longtext"},
		"ihtml_revisions": {"usr": "varbinary", "id": "varbinary", "items": "longtext"},
	}
	for table, columns := range want {
		for column, dataType := range columns {
			var got string
			err := s.db.QueryRowContext(ctx,
				`SELECT DATA_TYPE FROM information_schema.COLUMNS
				 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`,
				table, column).Scan(&got)
			if err != nil {
				return false, err
			}
			if !strings.EqualFold(got, dataType) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Init 创建数据表(幂等)。也可以不调用, 由 DBA 按此结构自行建表。
func (s *Store) Init(ctx context.Context) error {
	keyText := "TEXT"
	payloadText := "TEXT"
	if s.dialect == MySQL {
		// VARBINARY 让租户、Item ID 与 KV key 使用逐字节大小写语义；
		// LONGTEXT 覆盖 ihtml 允许的 512KiB Item 与完整快照。
		keyText = "VARBINARY(255)"
		payloadText = "LONGTEXT"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ihtml_items (
			usr ` + keyText + ` NOT NULL,
			id ` + keyText + ` NOT NULL,
			typ TEXT NOT NULL,
			title TEXT NOT NULL,
			page TEXT NOT NULL,
			ord INTEGER NOT NULL,
			content ` + payloadText + ` NOT NULL,
			meta ` + payloadText + ` NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (usr, id)
		)`,
		`CREATE TABLE IF NOT EXISTS ihtml_kv (
			usr ` + keyText + ` NOT NULL,
			k ` + keyText + ` NOT NULL,
			v ` + payloadText + ` NOT NULL,
			PRIMARY KEY (usr, k)
		)`,
		`CREATE TABLE IF NOT EXISTS ihtml_revisions (
			usr ` + keyText + ` NOT NULL,
			id ` + keyText + ` NOT NULL,
			note TEXT NOT NULL,
			created_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			item_count INTEGER NOT NULL,
			items ` + payloadText + ` NOT NULL,
			PRIMARY KEY (usr, id)
		)`,
		// 用户状态版本行。每个写事务开头先 UPDATE 此行:
		// 既做乐观并发令牌，也让并发写同一用户的事务在数据库层互相串行化。
		`CREATE TABLE IF NOT EXISTS ihtml_user_version (
			usr ` + keyText + ` NOT NULL,
			version BIGINT NOT NULL,
			PRIMARY KEY (usr)
		)`,
	}
	for _, stmt := range stmts {
		if err := s.exec(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: init: %w", err)
		}
	}
	if s.dialect == MySQL {
		// CREATE TABLE IF NOT EXISTS 不会修正旧版表结构。只在检测到旧字段时执行
		// ALTER，避免每次启动都让 MySQL 重建或锁住大表。
		needsMigration, err := s.mysqlSchemaNeedsMigration(ctx)
		if err != nil {
			return fmt.Errorf("sqlstore: inspect mysql schema: %w", err)
		}
		if !needsMigration {
			return nil
		}
		migrations := []string{
			`ALTER TABLE ihtml_items MODIFY usr VARBINARY(255) NOT NULL, MODIFY id VARBINARY(255) NOT NULL, MODIFY content LONGTEXT NOT NULL, MODIFY meta LONGTEXT NOT NULL`,
			`ALTER TABLE ihtml_kv MODIFY usr VARBINARY(255) NOT NULL, MODIFY k VARBINARY(255) NOT NULL, MODIFY v LONGTEXT NOT NULL`,
			`ALTER TABLE ihtml_revisions MODIFY usr VARBINARY(255) NOT NULL, MODIFY id VARBINARY(255) NOT NULL, MODIFY items LONGTEXT NOT NULL`,
		}
		for _, stmt := range migrations {
			if err := s.exec(ctx, stmt); err != nil {
				return fmt.Errorf("sqlstore: migrate mysql schema: %w", err)
			}
		}
	}
	return nil
}

// bumpUserVersionTx 在写事务开头原子推进用户版本(不存在则从 1 建行)。
// 它必须是事务的第一条写语句: 版本行锁统一了并发写同一用户的加锁顺序,
// 让不同副本的写事务在数据库层串行化, 也是乐观并发令牌的推进点。
func (s *Store) bumpUserVersionTx(ctx context.Context, tx *sql.Tx, user string) error {
	query := `INSERT INTO ihtml_user_version (usr, version) VALUES (?, 1)
	          ON CONFLICT (usr) DO UPDATE SET version = ihtml_user_version.version + 1`
	if s.dialect == MySQL {
		query = `INSERT INTO ihtml_user_version (usr, version) VALUES (?, 1)
		         ON DUPLICATE KEY UPDATE version = version + 1`
	}
	if _, err := tx.ExecContext(ctx, s.rebind(query), user); err != nil {
		return fmt.Errorf("sqlstore: bump user version (升级到含 ihtml_user_version 的版本后需重新执行 Init): %w", err)
	}
	return nil
}

// casUserVersionTx 仅当当前版本等于 expected 时把版本推进到 expected+1;
// 否则返回 ihtml.ErrVersionConflict。与 bumpUserVersionTx 一样必须最先执行。
func (s *Store) casUserVersionTx(ctx context.Context, tx *sql.Tx, user string, expected uint64) error {
	if expected == 0 {
		// 版本行不存在(新用户或未迁移的旧数据)时建行为 1; 行已存在说明版本 ≥ 1,
		// 唯一键冲突即并发写抢先——按版本冲突处理并保留底层错误细节。
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO ihtml_user_version (usr, version) VALUES (?, 1)`), user); err != nil {
			return errors.Join(ihtml.ErrVersionConflict, err)
		}
		return nil
	}
	res, err := tx.ExecContext(ctx, s.rebind(
		`UPDATE ihtml_user_version SET version = ? WHERE usr = ? AND version = ?`),
		expected+1, user, expected)
	if err != nil {
		return fmt.Errorf("sqlstore: cas user version (升级到含 ihtml_user_version 的版本后需重新执行 Init): %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ihtml.ErrVersionConflict
	}
	return nil
}

func (s *Store) userVersion(ctx context.Context, tx *sql.Tx, user string) (uint64, error) {
	var version uint64
	err := tx.QueryRowContext(ctx, s.rebind(
		`SELECT version FROM ihtml_user_version WHERE usr = ?`), user).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		// 旧库尚未建 ihtml_user_version 表时按无版本处理, 读路径保持可用;
		// 首次写入会返回明确的迁移提示。
		return 0, nil
	}
	return version, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

type revisionPayload struct {
	Version               int              `json:"version,omitempty"`
	Items                 []ihtml.Item     `json:"items"`
	Pages                 []ihtml.Page     `json:"pages,omitempty"`
	Templates             []ihtml.Template `json:"templates,omitempty"`
	RootTemplateID        string           `json:"root_template_id,omitempty"`
	TemplateRevisionClock uint64           `json:"template_revision_clock,omitempty"`
}

func encodeRevision(rev ihtml.Revision) (string, error) {
	raw, err := json.Marshal(revisionPayload{
		Version: rev.Version, Items: rev.Items, Pages: rev.Pages,
		Templates: rev.Templates, RootTemplateID: rev.RootTemplateID,
		TemplateRevisionClock: rev.TemplateRevisionClock,
	})
	return string(raw), err
}

// decodeRevisionPayload 同时接受旧版直接存储的 []Item 和新版对象载荷。
func decodeRevisionPayload(raw string) (revisionPayload, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") || trimmed == "null" {
		var items []ihtml.Item
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return revisionPayload{}, err
		}
		return revisionPayload{Items: items}, nil
	}
	var payload revisionPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return revisionPayload{}, err
	}
	return payload, nil
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
		it.UpdatedAt, err = parseTS(updated)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: corrupt item timestamp for %q: %w", it.ID, err)
		}
		if meta != "" && meta != "null" {
			if err := json.Unmarshal([]byte(meta), &it.Meta); err != nil {
				return nil, fmt.Errorf("sqlstore: corrupt item meta for %q: %w", it.ID, err)
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type itemRow struct {
	typ, title, page, content, meta, updated string
	order                                    int
}

func (s *Store) syncItemsTx(ctx context.Context, tx *sql.Tx, user string, items []ihtml.Item) error {
	rows, err := tx.QueryContext(ctx, s.rebind(
		`SELECT id, typ, title, page, ord, content, meta, updated_at FROM ihtml_items WHERE usr = ?`), user)
	if err != nil {
		return err
	}
	current := make(map[string]itemRow)
	for rows.Next() {
		var id string
		var row itemRow
		if err := rows.Scan(&id, &row.typ, &row.title, &row.page, &row.order, &row.content, &row.meta, &row.updated); err != nil {
			rows.Close()
			return err
		}
		current[id] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	target := make(map[string]itemRow, len(items))
	for _, it := range items {
		if _, exists := target[it.ID]; exists {
			return fmt.Errorf("sqlstore: duplicate item id %q", it.ID)
		}
		meta, err := json.Marshal(it.Meta)
		if err != nil {
			return err
		}
		target[it.ID] = itemRow{
			typ: string(it.Type), title: it.Title, page: it.Page, order: it.Order,
			content: it.Content, meta: string(meta), updated: ts(it.UpdatedAt),
		}
	}
	for id := range current {
		if _, keep := target[id]; !keep {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_items WHERE usr = ? AND id = ?`), user, id); err != nil {
				return err
			}
		}
	}
	for id, row := range target {
		if old, exists := current[id]; exists && old == row {
			continue
		}
		if _, exists := current[id]; exists {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_items WHERE usr = ? AND id = ?`), user, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO ihtml_items (usr, id, typ, title, page, ord, content, meta, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			user, id, row.typ, row.title, row.page, row.order, row.content, row.meta, row.updated); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceItems 实现 ihtml.Store。
func (s *Store) ReplaceItems(ctx context.Context, user string, items []ihtml.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit 成功后回滚是空操作
	if err := s.bumpUserVersionTx(ctx, tx, user); err != nil {
		return err
	}
	if err := s.syncItemsTx(ctx, tx, user, items); err != nil {
		return err
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
	if err := s.bumpUserVersionTx(ctx, tx, user); err != nil {
		return err
	}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.bumpUserVersionTx(ctx, tx, user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM ihtml_kv WHERE usr = ? AND k = ?`), user, key); err != nil {
		return err
	}
	return tx.Commit()
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
	// 版本行最先删除(与写事务的加锁顺序一致), 用户回到"无版本"状态。
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM ihtml_user_version WHERE usr = ?`), user); err != nil {
		return fmt.Errorf("sqlstore: erase user version (升级到含 ihtml_user_version 的版本后需重新执行 Init): %w", err)
	}
	for _, table := range []string{"ihtml_items", "ihtml_kv", "ihtml_revisions"} {
		if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM `+table+` WHERE usr = ?`), user); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UserState 在一个事务中读取用户的完整状态。
func (s *Store) UserState(ctx context.Context, user string) (ihtml.UserState, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ihtml.UserState{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	state := ihtml.UserState{KV: make(map[string]json.RawMessage)}
	rows, err := tx.QueryContext(ctx, s.rebind(
		`SELECT id, typ, title, page, ord, content, meta, updated_at FROM ihtml_items WHERE usr = ?`), user)
	if err != nil {
		return ihtml.UserState{}, err
	}
	for rows.Next() {
		var it ihtml.Item
		var typ, meta, updated string
		if err := rows.Scan(&it.ID, &typ, &it.Title, &it.Page, &it.Order, &it.Content, &meta, &updated); err != nil {
			rows.Close()
			return ihtml.UserState{}, err
		}
		it.Type = ihtml.ItemType(typ)
		it.UpdatedAt, err = parseTS(updated)
		if err != nil {
			rows.Close()
			return ihtml.UserState{}, fmt.Errorf("sqlstore: corrupt item timestamp for %q: %w", it.ID, err)
		}
		if meta != "" && meta != "null" {
			if err := json.Unmarshal([]byte(meta), &it.Meta); err != nil {
				rows.Close()
				return ihtml.UserState{}, fmt.Errorf("sqlstore: corrupt item meta for %q: %w", it.ID, err)
			}
		}
		state.Items = append(state.Items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ihtml.UserState{}, err
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, s.rebind(`SELECT k, v FROM ihtml_kv WHERE usr = ?`), user)
	if err != nil {
		return ihtml.UserState{}, err
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return ihtml.UserState{}, err
		}
		state.KV[key] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ihtml.UserState{}, err
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, s.rebind(
		`SELECT id, note, created_by, created_at, items FROM ihtml_revisions
		 WHERE usr = ?`), user)
	if err != nil {
		return ihtml.UserState{}, err
	}
	for rows.Next() {
		var rev ihtml.Revision
		var created, raw string
		if err := rows.Scan(&rev.ID, &rev.Note, &rev.By, &created, &raw); err != nil {
			rows.Close()
			return ihtml.UserState{}, err
		}
		payload, err := decodeRevisionPayload(raw)
		if err != nil {
			rows.Close()
			return ihtml.UserState{}, fmt.Errorf("sqlstore: corrupt revision %s: %w", rev.ID, err)
		}
		rev.CreatedAt, err = parseTS(created)
		if err != nil {
			rows.Close()
			return ihtml.UserState{}, fmt.Errorf("sqlstore: corrupt revision timestamp for %q: %w", rev.ID, err)
		}
		rev.Version, rev.Items, rev.Pages = payload.Version, payload.Items, payload.Pages
		rev.Templates, rev.RootTemplateID = payload.Templates, payload.RootTemplateID
		rev.TemplateRevisionClock = payload.TemplateRevisionClock
		state.Revisions = append(state.Revisions, rev)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ihtml.UserState{}, err
	}
	rows.Close()
	sort.SliceStable(state.Revisions, func(i, j int) bool {
		if !state.Revisions[i].CreatedAt.Equal(state.Revisions[j].CreatedAt) {
			return state.Revisions[i].CreatedAt.After(state.Revisions[j].CreatedAt)
		}
		return state.Revisions[i].ID > state.Revisions[j].ID
	})
	// 版本最后读: 若连接已坏, 前面的数据查询会先暴露错误;
	// 走到这里仍失败基本只剩"旧库未建版本表", 按 0(无版本)处理保持读路径可用。
	state.Version, err = s.userVersion(ctx, tx, user)
	if err != nil {
		return ihtml.UserState{}, err
	}
	if err := tx.Commit(); err != nil {
		return ihtml.UserState{}, err
	}
	return state, nil
}

func (s *Store) syncKVTx(ctx context.Context, tx *sql.Tx, user string, kv map[string]json.RawMessage) error {
	rows, err := tx.QueryContext(ctx, s.rebind(`SELECT k, v FROM ihtml_kv WHERE usr = ?`), user)
	if err != nil {
		return err
	}
	current := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return err
		}
		current[key] = value
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for key := range current {
		if _, keep := kv[key]; !keep {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_kv WHERE usr = ? AND k = ?`), user, key); err != nil {
				return err
			}
		}
	}
	for key, raw := range kv {
		value := string(raw)
		if old, exists := current[key]; exists && old == value {
			continue
		}
		if _, exists := current[key]; exists {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_kv WHERE usr = ? AND k = ?`), user, key); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO ihtml_kv (usr, k, v) VALUES (?, ?, ?)`), user, key, value); err != nil {
			return err
		}
	}
	return nil
}

type revisionRow struct {
	note, by, created, raw string
	itemCount              int
}

func (s *Store) syncRevisionsTx(ctx context.Context, tx *sql.Tx, user string, revisions []ihtml.Revision) error {
	rows, err := tx.QueryContext(ctx, s.rebind(
		`SELECT id, note, created_by, created_at, item_count, items FROM ihtml_revisions WHERE usr = ?`), user)
	if err != nil {
		return err
	}
	current := make(map[string]revisionRow)
	for rows.Next() {
		var id string
		var row revisionRow
		if err := rows.Scan(&id, &row.note, &row.by, &row.created, &row.itemCount, &row.raw); err != nil {
			rows.Close()
			return err
		}
		current[id] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	target := make(map[string]revisionRow, len(revisions))
	for _, rev := range revisions {
		if _, exists := target[rev.ID]; exists {
			return fmt.Errorf("sqlstore: duplicate revision id %q", rev.ID)
		}
		raw, err := encodeRevision(rev)
		if err != nil {
			return err
		}
		target[rev.ID] = revisionRow{
			note: rev.Note, by: rev.By, created: ts(rev.CreatedAt), itemCount: len(rev.Items), raw: raw,
		}
	}
	for id := range current {
		if _, keep := target[id]; !keep {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_revisions WHERE usr = ? AND id = ?`), user, id); err != nil {
				return err
			}
		}
	}
	for id, row := range target {
		if old, exists := current[id]; exists && old == row {
			continue
		}
		if _, exists := current[id]; exists {
			if _, err := tx.ExecContext(ctx, s.rebind(
				`DELETE FROM ihtml_revisions WHERE usr = ? AND id = ?`), user, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO ihtml_revisions (usr, id, note, created_by, created_at, item_count, items)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`),
			user, id, row.note, row.by, row.created, row.itemCount, row.raw); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceUserState 在一个事务中整体替换用户状态。实现按键同步差异，避免页面
// 删除/回滚时重写该用户未变化的所有 Item、KV 与大体积快照。
func (s *Store) ReplaceUserState(ctx context.Context, user string, state ihtml.UserState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.bumpUserVersionTx(ctx, tx, user); err != nil {
		return err
	}
	if err := s.replaceUserStateTx(ctx, tx, user, state); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceUserStateVersioned 实现 ihtml.VersionedUserStateStore: 版本条件写,
// 供多副本部署在数据库层做真正的跨进程 CAS。
func (s *Store) ReplaceUserStateVersioned(ctx context.Context, user string, state ihtml.UserState, expected uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.casUserVersionTx(ctx, tx, user, expected); err != nil {
		return err
	}
	if err := s.replaceUserStateTx(ctx, tx, user, state); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) replaceUserStateTx(ctx context.Context, tx *sql.Tx, user string, state ihtml.UserState) error {
	if err := s.syncItemsTx(ctx, tx, user, state.Items); err != nil {
		return err
	}
	if err := s.syncKVTx(ctx, tx, user, state.KV); err != nil {
		return err
	}
	return s.syncRevisionsTx(ctx, tx, user, state.Revisions)
}

// Revisions 实现 ihtml.Store。
func (s *Store) Revisions(ctx context.Context, user string) ([]ihtml.RevisionInfo, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(
		`SELECT id, note, created_by, created_at, item_count, items FROM ihtml_revisions
		 WHERE usr = ?`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ihtml.RevisionInfo
	for rows.Next() {
		var info ihtml.RevisionInfo
		var created, raw string
		if err := rows.Scan(&info.ID, &info.Note, &info.By, &created, &info.ItemCount, &raw); err != nil {
			return nil, err
		}
		payload, err := decodeRevisionPayload(raw)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: corrupt revision %s: %w", info.ID, err)
		}
		info.TemplateCount = len(payload.Templates)
		info.CreatedAt, err = parseTS(created)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: corrupt revision timestamp for %q: %w", info.ID, err)
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// RevisionItems 实现 ihtml.Store。
func (s *Store) RevisionItems(ctx context.Context, user, id string) ([]ihtml.Item, error) {
	rev, err := s.Revision(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return rev.Items, nil
}

// Revision 返回指定完整快照，并兼容旧版仅含 []Item 的 SQL 载荷。
func (s *Store) Revision(ctx context.Context, user, id string) (ihtml.Revision, error) {
	var rev ihtml.Revision
	var created, raw string
	err := s.db.QueryRowContext(ctx, s.rebind(
		`SELECT id, note, created_by, created_at, items FROM ihtml_revisions WHERE usr = ? AND id = ?`),
		user, id).Scan(&rev.ID, &rev.Note, &rev.By, &created, &raw)
	if err == sql.ErrNoRows {
		return ihtml.Revision{}, ihtml.ErrNotFound
	}
	if err != nil {
		return ihtml.Revision{}, err
	}
	payload, err := decodeRevisionPayload(raw)
	if err != nil {
		return ihtml.Revision{}, fmt.Errorf("sqlstore: corrupt revision %s: %w", id, err)
	}
	rev.CreatedAt, err = parseTS(created)
	if err != nil {
		return ihtml.Revision{}, fmt.Errorf("sqlstore: corrupt revision timestamp for %q: %w", id, err)
	}
	rev.Version = payload.Version
	rev.Items = payload.Items
	rev.Pages = payload.Pages
	rev.Templates = payload.Templates
	rev.RootTemplateID = payload.RootTemplateID
	rev.TemplateRevisionClock = payload.TemplateRevisionClock
	return rev, nil
}

// AddRevision 实现 ihtml.Store。
func (s *Store) AddRevision(ctx context.Context, user string, rev ihtml.Revision, keep int) error {
	raw, err := encodeRevision(rev)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := s.bumpUserVersionTx(ctx, tx, user); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(
		`INSERT INTO ihtml_revisions (usr, id, note, created_by, created_at, item_count, items)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		user, rev.ID, rev.Note, rev.By, ts(rev.CreatedAt), len(rev.Items), raw); err != nil {
		return err
	}
	if keep <= 0 {
		return tx.Commit()
	}
	// 裁剪: 先查出超额的旧快照 id 再删, 避免各方言 DELETE+LIMIT 语法差异
	rows, err := tx.QueryContext(ctx, s.rebind(
		`SELECT id, created_at FROM ihtml_revisions WHERE usr = ?`), user)
	if err != nil {
		return err
	}
	type revisionOrder struct {
		id string
		at time.Time
	}
	var ordered []revisionOrder
	for rows.Next() {
		var id, created string
		if err := rows.Scan(&id, &created); err != nil {
			rows.Close()
			return err
		}
		at, err := parseTS(created)
		if err != nil {
			rows.Close()
			return fmt.Errorf("sqlstore: corrupt revision timestamp for %q: %w", id, err)
		}
		ordered = append(ordered, revisionOrder{id: id, at: at})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].at.Equal(ordered[j].at) {
			return ordered[i].at.After(ordered[j].at)
		}
		return ordered[i].id > ordered[j].id
	})
	for _, old := range ordered[min(keep, len(ordered)):] {
		if _, err := tx.ExecContext(ctx, s.rebind(
			`DELETE FROM ihtml_revisions WHERE usr = ? AND id = ?`), user, old.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
