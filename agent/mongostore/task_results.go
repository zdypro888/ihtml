package mongostore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/zdypro888/ihtml/agent"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const ResultChunkBytes = 256 << 10

// 正文按固定块保存，最终元数据最后发布；Mongo 单文档限制不改变原调用的执行事实。
type ResultDocument struct {
	StorageVersion  int                `bson:"storageVersion,omitempty"`
	ID              string             `bson:"_id"`
	Subject         string             `bson:"subject"`
	Conversation    string             `bson:"conversation"`
	Scope           string             `bson:"scope"`
	TaskID          string             `bson:"taskID"`
	Digest          string             `bson:"digest"`
	State           string             `bson:"state"`
	Error           string             `bson:"error"`
	Failure         *agent.TaskFailure `bson:"failure,omitempty"`
	Started         bool               `bson:"started"`
	Size            int64              `bson:"size"`
	SHA256          string             `bson:"sha256"`
	Encoding        string             `bson:"encoding"`
	Availability    string             `bson:"availability"`
	CreatedAt       time.Time          `bson:"createdAt"`
	ExpiresAt       time.Time          `bson:"expiresAt"`
	LedgerExpiresAt time.Time          `bson:"ledgerExpiresAt"`
}

type ResultChunk struct {
	StorageVersion int       `bson:"storageVersion,omitempty"`
	ID             string    `bson:"_id"`
	Subject        string    `bson:"subject"`
	Conversation   string    `bson:"conversation"`
	Scope          string    `bson:"scope"`
	TaskID         string    `bson:"taskID"`
	SHA256         string    `bson:"sha256"`
	ChunkSHA256    string    `bson:"chunkSHA256"`
	Number         int       `bson:"number"`
	Data           []byte    `bson:"data"`
	ExpiresAt      time.Time `bson:"expiresAt"`
}

// ResultStore 只保存通用执行回执；命名空间来自可信绑定，模型不能选择集合或用户。
type ResultStore struct {
	Manifests, Chunks     *mongo.Collection
	Subject, Conversation string
}

func (s *ResultStore) Filter(Scope, id string) bson.M {
	return bson.M{"_id": ResultID(s.Subject, s.Conversation, Scope, id), "subject": s.Subject, "conversation": s.Conversation, "scope": Scope, "taskID": id}
}

func (s *ResultStore) ChunkID(Scope, id, sum string, number int) string {
	return digest(encode([]string{s.Subject, s.Conversation, Scope, id, sum, strconv.Itoa(number)}))
}

func ResultID(Subject, Conversation, Scope, id string) string {
	return digest(encode([]string{Subject, Conversation, Scope, id}))
}

func (d ResultDocument) record() agent.TaskResultRecord {
	return agent.TaskResultRecord{Scope: d.Scope, TaskID: d.TaskID, Digest: d.Digest, State: d.State, Error: d.Error, Failure: d.Failure, Started: d.Started, Size: d.Size, SHA256: d.SHA256, Encoding: d.Encoding, Availability: d.Availability, CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, LedgerExpiresAt: d.LedgerExpiresAt}
}

func resultDocument(Subject, Conversation string, r agent.TaskResultRecord) ResultDocument {
	return ResultDocument{StorageVersion: agent.TaskStorageVersion, ID: ResultID(Subject, Conversation, r.Scope, r.TaskID), Subject: Subject, Conversation: Conversation, Scope: r.Scope, TaskID: r.TaskID, Digest: r.Digest, State: r.State, Error: r.Error, Failure: r.Failure, Started: r.Started, Size: r.Size, SHA256: r.SHA256, Encoding: r.Encoding, Availability: r.Availability, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LedgerExpiresAt: r.LedgerExpiresAt}
}

func sameResultOutcome(a, b agent.TaskResultRecord) bool {
	return agent.TaskResultIdentity(a) == agent.TaskResultIdentity(b)
}

// PutResult 先持久全部不可变块，再原子发布元数据；未完成的写入不能被 GetResult 当作已提交回执。
func (s *ResultStore) PutResult(ctx context.Context, r agent.TaskResultRecord) (agent.TaskResultRecord, error) {
	if err := agent.ValidateTaskResult(r); err != nil {
		return agent.TaskResultRecord{}, err
	}
	if old, err := s.GetResult(ctx, r.Scope, r.TaskID); err == nil {
		if !sameResultOutcome(old, r) {
			return old, agent.ErrTaskConflict
		}
		return old, nil
	} else if !errors.Is(err, agent.ErrTaskNotFound) {
		return agent.TaskResultRecord{}, err
	}
	if r.Availability == "available" {
		sum := sha256.Sum256(r.Body)
		if int64(len(r.Body)) != r.Size || hex.EncodeToString(sum[:]) != r.SHA256 || len(r.Body) > 256<<20 {
			return agent.TaskResultRecord{}, agent.ErrTaskResultUnavailable
		}
		for offset, number := 0, 0; offset < len(r.Body); offset, number = offset+ResultChunkBytes, number+1 {
			data := r.Body[offset:min(offset+ResultChunkBytes, len(r.Body))]
			partSum := sha256.Sum256(data)
			chunk := ResultChunk{StorageVersion: agent.TaskStorageVersion, ID: s.ChunkID(r.Scope, r.TaskID, r.SHA256, number), Subject: s.Subject, Conversation: s.Conversation, Scope: r.Scope, TaskID: r.TaskID, SHA256: r.SHA256, ChunkSHA256: hex.EncodeToString(partSum[:]), Number: number, Data: data, ExpiresAt: r.ExpiresAt}
			_, err := s.Chunks.InsertOne(ctx, chunk)
			if mongo.IsDuplicateKeyError(err) {
				var prior ResultChunk
				err = decodeVersioned(s.Chunks.FindOne(ctx, bson.M{"_id": chunk.ID, "subject": s.Subject, "conversation": s.Conversation, "scope": r.Scope, "taskID": r.TaskID}), &prior)
				if err == nil && (prior.Number != number || prior.SHA256 != r.SHA256 || prior.ChunkSHA256 != chunk.ChunkSHA256 || !bytes.Equal(prior.Data, data)) {
					return agent.TaskResultRecord{}, agent.ErrTaskConflict
				}
				if err == nil && prior.ExpiresAt.Before(r.ExpiresAt) {
					r.ExpiresAt = prior.ExpiresAt
				}
			}
			if err != nil {
				return agent.TaskResultRecord{}, err
			}
		}
	} else if len(r.Body) != 0 {
		return agent.TaskResultRecord{}, agent.ErrTaskResultUnavailable
	}
	_, err := s.Manifests.InsertOne(ctx, resultDocument(s.Subject, s.Conversation, r))
	if mongo.IsDuplicateKeyError(err) {
		old, e := s.GetResult(ctx, r.Scope, r.TaskID)
		if e == nil && !sameResultOutcome(old, r) {
			e = agent.ErrTaskConflict
		}
		return old, e
	}
	r.Body = nil
	return r, err
}

// GetResult 只返回已发布的最终元数据；正文过期仍保留执行事实直到轻量账本保留期。
func (s *ResultStore) GetResult(ctx context.Context, Scope, id string) (agent.TaskResultRecord, error) {
	if Scope == "" || id == "" {
		return agent.TaskResultRecord{}, agent.ErrTaskNotFound
	}
	var d ResultDocument
	f := s.Filter(Scope, id)
	f["ledgerExpiresAt"] = bson.M{"$gt": time.Now()}
	err := decodeVersioned(s.Manifests.FindOne(ctx, f), &d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = agent.ErrTaskNotFound
	}
	return d.record(), err
}

// ReadResult 按字节精确读取所需块，不把整个大结果重新载入模型或绕过原命名空间。
func (s *ResultStore) ReadResult(ctx context.Context, Scope, id string, offset int64, limit int) ([]byte, error) {
	r, err := s.GetResult(ctx, Scope, id)
	if err != nil {
		return nil, err
	}
	if !r.ExpiresAt.After(time.Now()) {
		return nil, agent.ErrTaskResultExpired
	}
	if r.Availability != "available" {
		return nil, agent.ErrTaskResultUnavailable
	}
	if offset < 0 || offset > r.Size || limit < 0 || limit > 1<<20 {
		return nil, agent.ErrTaskResultRange
	}
	end := min(offset+int64(limit), r.Size)
	out := make([]byte, 0, end-offset)
	for pos := offset; pos < end; {
		number := int(pos / ResultChunkBytes)
		var chunk ResultChunk
		Filter := bson.M{"_id": s.ChunkID(Scope, id, r.SHA256, number), "subject": s.Subject, "conversation": s.Conversation, "scope": Scope, "taskID": id, "sha256": r.SHA256, "expiresAt": bson.M{"$gt": time.Now()}}
		if err = decodeVersioned(s.Chunks.FindOne(ctx, Filter), &chunk); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				err = agent.ErrTaskResultUnavailable
			}
			return nil, err
		}
		start := int(pos % ResultChunkBytes)
		n := min(int(end-pos), len(chunk.Data)-start)
		partSum := sha256.Sum256(chunk.Data)
		if start < 0 || n <= 0 || chunk.Number != number || len(chunk.Data) != min(ResultChunkBytes, int(r.Size-int64(number)*ResultChunkBytes)) || hex.EncodeToString(partSum[:]) != chunk.ChunkSHA256 {
			return nil, agent.ErrTaskResultUnavailable
		}
		out = append(out, chunk.Data[start:start+n]...)
		pos += int64(n)
	}
	return out, nil
}

// CollectResults 分别回收过期正文块与执行事实；TTL 只作无人在线时的补充，不承担读取正确性。
func (s *ResultStore) CollectResults(ctx context.Context, now time.Time) error {
	f := bson.M{"subject": s.Subject, "conversation": s.Conversation, "expiresAt": bson.M{"$lte": now}}
	if _, err := s.Chunks.DeleteMany(ctx, compatibleFilter(f)); err != nil {
		return err
	}
	f = bson.M{"subject": s.Subject, "conversation": s.Conversation, "ledgerExpiresAt": bson.M{"$lte": now}}
	_, err := s.Manifests.DeleteMany(ctx, compatibleFilter(f))
	return err
}
