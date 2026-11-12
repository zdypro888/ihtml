// Package mongostore 持久化有限范围授权；集合由宿主固定，模型不能选择命名空间。
package mongostore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/zdypro888/ihtml/authorization"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Store 把授权与领取墓碑放在同一文档，以 Mongo 单文档原子更新排序领取和撤销。
// 逐项结果独立保存，避免大量操作的回执超过 Mongo 文档大小；结果保存失败也不能释放领取。
type Store struct{ Grants, Results *mongo.Collection }

var _ authorization.Store = (*Store)(nil)

const version = 1
const receiptRetention = 24 * time.Hour

type grantDocument struct {
	ID        string                         `bson:"_id"`
	Version   int                            `bson:"storageVersion"`
	Principal string                         `bson:"principal"`
	Record    authorization.Record           `bson:"record"`
	Claims    map[string]authorization.Claim `bson:"claims,omitempty"`
	DeleteAt  time.Time                      `bson:"deleteAt"`
}
type resultDocument struct {
	ID        string              `bson:"_id"`
	Version   int                 `bson:"storageVersion"`
	Principal string              `bson:"principal"`
	Claim     authorization.Claim `bson:"claim"`
	DeleteAt  time.Time           `bson:"deleteAt"`
}

func hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func principal(b authorization.Binding) string {
	return hash([]string{b.Owner, b.Conversation, b.Capability})
}
func key(b authorization.Binding, id string) string { return hash([]string{principal(b), id}) }
func resultKey(b authorization.Binding, c authorization.Claim) string {
	return hash([]string{key(b, c.GrantID), c.Key, c.Invocation})
}
func (s *Store) valid() bool { return s != nil && s.Grants != nil && s.Results != nil }
func filter(b authorization.Binding, id string) bson.M {
	return bson.M{"_id": key(b, id), "principal": principal(b)}
}
func readDocument(result *mongo.SingleResult) (grantDocument, error) {
	var d grantDocument
	err := result.Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return d, authorization.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if d.Version != version {
		return d, authorization.ErrInvalid
	}
	return d, nil
}
func (s *Store) load(ctx context.Context, b authorization.Binding, id string, claimKey ...string) (grantDocument, error) {
	if !s.valid() || !b.Valid() || id == "" {
		return grantDocument{}, authorization.ErrInvalid
	}
	// 普通授权读取不拉取所有领取墓碑；单项目只投影自己的记录，避免千项目产生平方级传输。
	projection := bson.M{"claims": 0}
	if len(claimKey) > 0 {
		if len(claimKey[0]) != 64 {
			return grantDocument{}, authorization.ErrInvalid
		}
		if _, err := hex.DecodeString(claimKey[0]); err != nil {
			return grantDocument{}, authorization.ErrInvalid
		}
		projection = bson.M{"record": 1, "storageVersion": 1, "deleteAt": 1, "claims." + claimKey[0]: 1}
	}
	return readDocument(s.Grants.FindOne(ctx, filter(b, id), options.FindOne().SetProjection(projection)))
}

// EnsureIndexes 只建立固定集合的索引；授权失效立即由查询条件执行，TTL 仅延后回收墓碑。
func (s *Store) EnsureIndexes(ctx context.Context) error {
	if !s.valid() {
		return authorization.ErrInvalid
	}
	_, err := s.Grants.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "principal", Value: 1}, {Key: "record.createdAt", Value: 1}}},
		{Keys: bson.D{{Key: "deleteAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0).SetPartialFilterExpression(bson.M{"storageVersion": version})},
	})
	if err != nil {
		return err
	}
	_, err = s.Results.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "deleteAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0).SetPartialFilterExpression(bson.M{"storageVersion": version})})
	return err
}

// Create 只插入经过协议校验的不可变范围；相同授权编号不能覆盖原有批准或领取。
func (s *Store) Create(ctx context.Context, r authorization.Record) error {
	if !s.valid() {
		return authorization.ErrInvalid
	}
	if err := authorization.ValidateRecord(r); err != nil {
		return err
	}
	// 持久存储不以历史授权总数限流；当前待确认窗口由会话确认机制管理。
	_, err := s.Grants.InsertOne(ctx, grantDocument{ID: key(r.Binding, r.ID), Version: version, Principal: principal(r.Binding), Record: r, DeleteAt: r.ExpiresAt.Add(receiptRetention)})
	if mongo.IsDuplicateKeyError(err) {
		return authorization.ErrConflict
	}
	return err
}

// Get 通过主体、会话和权限代次隔离读取，不因调用方的新 Goal 改写原任务绑定。
func (s *Store) Get(ctx context.Context, b authorization.Binding, id string) (authorization.Record, error) {
	d, err := s.load(ctx, b, id)
	return d.Record, err
}

// List 完整读取本主体的授权，停止时不能把展示分页变成遗漏撤销。
func (s *Store) List(ctx context.Context, b authorization.Binding) ([]authorization.Record, error) {
	if !s.valid() || !b.Valid() {
		return nil, authorization.ErrInvalid
	}
	cursor, err := s.Grants.Find(ctx, bson.M{"principal": principal(b), "storageVersion": version}, options.Find().SetSort(bson.D{{Key: "record.createdAt", Value: -1}}).SetProjection(bson.M{"claims": 0}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	out := []authorization.Record{}
	for cursor.Next(ctx) {
		var d grantDocument
		if err = cursor.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, d.Record)
	}
	return out, cursor.Err()
}

// Activate 与 Claim/Revoke 在同一文档排序；重复批准不会复活已撤销或到期的授权。
func (s *Store) Activate(ctx context.Context, b authorization.Binding, id, digest string, now time.Time) (authorization.Record, error) {
	if !s.valid() || !b.Valid() || id == "" || digest == "" || now.IsZero() {
		return authorization.Record{}, authorization.ErrInvalid
	}
	f := filter(b, id)
	f["storageVersion"] = version
	f["record.digest"] = digest
	f["record.state"] = authorization.Pending
	f["record.expiresAt"] = bson.M{"$gt": now}
	f["$expr"] = bson.M{"$gt": bson.A{"$record.expiresAt", "$$NOW"}}
	d, err := readDocument(s.Grants.FindOneAndUpdate(ctx, f, bson.M{"$set": bson.M{"record.state": authorization.Active, "record.updatedAt": now}}, options.FindOneAndUpdate().SetReturnDocument(options.After)))
	if err == nil {
		return d.Record, nil
	}
	if !errors.Is(err, authorization.ErrNotFound) {
		return authorization.Record{}, err
	}
	// 重复收到同一批准只返回原授权，撤销/到期均不复活。
	d, err = s.load(ctx, b, id)
	if err != nil {
		return authorization.Record{}, err
	}
	if d.Record.State == authorization.Active && d.Record.Digest == digest && d.Record.ExpiresAt.After(now) && d.Record.ExpiresAt.After(time.Now()) {
		return d.Record, nil
	}
	return authorization.Record{}, authorization.ErrDenied
}

// Claim 原子占用固定目标；已占用即使没有结果也只返回原记录，不恢复执行资格。
func (s *Store) Claim(ctx context.Context, q authorization.ClaimRequest) (authorization.Claim, error) {
	d, err := s.load(ctx, q.Binding, q.ID)
	if err != nil {
		return authorization.Claim{}, err
	}
	q.Now = maxTime(q.Now, time.Now())
	if err = authorization.ValidateClaimRequest(d.Record, q); err != nil {
		return authorization.Claim{}, err
	}
	path := "claims." + q.Key
	c := authorization.Claim{GrantID: q.ID, Key: q.Key, Invocation: q.Invocation, State: authorization.Claimed}
	f := filter(q.Binding, q.ID)
	f["storageVersion"] = version
	f["record.digest"] = q.Digest
	f["record.state"] = authorization.Active
	f["record.expiresAt"] = bson.M{"$gt": q.Now}
	f["$expr"] = bson.M{"$gt": bson.A{"$record.expiresAt", "$$NOW"}}
	f[path] = bson.M{"$exists": false}
	result, err := s.Grants.UpdateOne(ctx, f, bson.M{"$set": bson.M{path: c, "record.updatedAt": q.Now}})
	if err != nil {
		return c, err
	}
	if result.ModifiedCount == 1 {
		c.Fresh = true
		return c, nil
	}
	d, err = s.load(ctx, q.Binding, q.ID, q.Key)
	if err != nil {
		return c, err
	}
	q.Now = maxTime(q.Now, time.Now())
	if err = authorization.ValidateClaimRequest(d.Record, q); err != nil {
		return c, err
	}
	old, ok := d.Claims[q.Key]
	if !ok || old.Invocation != q.Invocation {
		return c, authorization.ErrDenied
	}
	old.Fresh = false
	var receipt resultDocument
	err = s.Results.FindOne(ctx, bson.M{"_id": resultKey(q.Binding, old), "principal": principal(q.Binding), "storageVersion": version}).Decode(&receipt)
	if err == nil {
		if receipt.Claim.Invocation != old.Invocation || receipt.Claim.Key != old.Key || receipt.Claim.GrantID != old.GrantID {
			return old, authorization.ErrInvalid
		}
		return receipt.Claim, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return old, err
	}
	// 有领取但无回执是未知状态；永不把失联或保存失败当成可重试。
	old.State = authorization.Claimed
	old.Result = ""
	return old, nil
}

// Finish 保存已在途调用的真实结果，撤销只拦新调用，不丢弃原调用回执。
func (s *Store) Finish(ctx context.Context, b authorization.Binding, c authorization.Claim, o authorization.Outcome, now time.Time) error {
	if !authorization.ValidOutcome(o) || now.IsZero() {
		return authorization.ErrInvalid
	}
	d, err := s.load(ctx, b, c.GrantID, c.Key)
	if err != nil {
		return err
	}
	if d.Record.Binding != b {
		return authorization.ErrDenied
	}
	old, ok := d.Claims[c.Key]
	if !ok || old.Invocation != c.Invocation || old.GrantID != c.GrantID {
		return authorization.ErrDenied
	}
	receipt := old
	receipt.State = o.State
	receipt.Result = o.Result
	receipt.Fresh = false
	// 结果先插入不可变文档，再标记领取完成；任一步失败都保留原领取墓碑。
	_, err = s.Results.InsertOne(ctx, resultDocument{ID: resultKey(b, c), Version: version, Principal: principal(b), Claim: receipt, DeleteAt: maxTime(d.DeleteAt, now.Add(receiptRetention))})
	if mongo.IsDuplicateKeyError(err) {
		var existing resultDocument
		err = s.Results.FindOne(ctx, bson.M{"_id": resultKey(b, c), "principal": principal(b), "storageVersion": version}).Decode(&existing)
		if err == nil && (existing.Claim.State != o.State || existing.Claim.Result != o.Result) {
			return authorization.ErrConflict
		}
	}
	if err != nil {
		return err
	}
	f := filter(b, c.GrantID)
	f["storageVersion"] = version
	f["claims."+c.Key+".invocation"] = c.Invocation
	result, err := s.Grants.UpdateOne(ctx, f, bson.M{"$set": bson.M{"claims." + c.Key + ".state": o.State, "record.updatedAt": now}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return authorization.ErrConflict
	}
	return nil
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Revoke 立即阻止该授权的新领取；原目标的领取墓碑和结果继续保留。
func (s *Store) Revoke(ctx context.Context, b authorization.Binding, id string, now time.Time) error {
	if !s.valid() || !b.Valid() || id == "" || now.IsZero() {
		return authorization.ErrInvalid
	}
	f := filter(b, id)
	f["storageVersion"] = version
	result, err := s.Grants.UpdateOne(ctx, f, bson.M{"$set": bson.M{"record.state": authorization.Revoked, "record.updatedAt": now}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return authorization.ErrNotFound
	}
	return nil
}

// RevokePending 仅回收待批准状态，与 Activate 的状态更新原子决胜。
func (s *Store) RevokePending(ctx context.Context, b authorization.Binding, id string, now time.Time) error {
	if !s.valid() || !b.Valid() || id == "" || now.IsZero() {
		return authorization.ErrInvalid
	}
	f := filter(b, id)
	f["storageVersion"] = version
	f["record.state"] = authorization.Pending
	_, err := s.Grants.UpdateOne(ctx, f, bson.M{"$set": bson.M{"record.state": authorization.Revoked, "record.updatedAt": now}})
	return err
}
