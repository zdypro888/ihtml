package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"strconv"

	"github.com/go-openapi/jsonpointer"
)

type taskResultReader struct {
	ctx            context.Context
	store          TaskResultStore
	scope, id, sha string
	offset, size   int64
	sum            hash.Hash
	closed         bool
}

// OpenResultInput 以受控范围流读取不可变结果，二进制也可使用；到 EOF 时校验完整摘要。
// 提前关闭只读取所需范围，存储适配器仍须核验块摘要；不访问宿主文件系统。
func (r *TaskRuntime) OpenResultInput(ctx context.Context, scope string, ref TaskResultInput, allowed map[string]bool) (io.ReadCloser, error) {
	if scope == "" || r.cfg.Scope != "" && r.cfg.Scope != scope || len(ref.SHA256) != 64 {
		return nil, ErrTaskNotFound
	}
	row, err := r.cfg.Store.GetTask(ctx, scope, ref.ID)
	if err != nil {
		return nil, WrapTaskStorage(err)
	}
	if !allowed[row.Info.Tool] {
		return nil, ErrTaskNotFound
	}
	meta, err := r.cfg.Results.GetResult(ctx, scope, ref.ID)
	if err != nil {
		return nil, WrapTaskStorage(err)
	}
	if meta.Digest != row.Digest || meta.SHA256 != ref.SHA256 {
		return nil, ErrTaskConflict
	}
	if meta.Size > int64(r.cfg.MaxStoredResultBytes) {
		return nil, ErrTaskResultRange
	}
	// Reuse store retention/availability enforcement even for an empty body.
	if _, err = r.cfg.Results.ReadResult(ctx, scope, ref.ID, 0, 0); err != nil {
		return nil, err
	}
	return &taskResultReader{ctx: ctx, store: r.cfg.Results, scope: scope, id: ref.ID, sha: ref.SHA256, size: meta.Size, sum: sha256.New()}, nil
}
func (s *taskResultReader) Read(out []byte) (int, error) {
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, nil
	}
	if s.offset == s.size {
		if hex.EncodeToString(s.sum.Sum(nil)) != s.sha {
			return 0, ErrTaskResultUnavailable
		}
		return 0, io.EOF
	}
	n := int(min(int64(len(out)), min(int64(64<<10), s.size-s.offset)))
	b, err := s.store.ReadResult(s.ctx, s.scope, s.id, s.offset, n)
	if err != nil {
		return 0, WrapTaskStorage(err)
	}
	if len(b) != n {
		return 0, ErrTaskResultUnavailable
	}
	copy(out, b)
	s.sum.Write(b)
	s.offset += int64(n)
	return n, nil
}
func (s *taskResultReader) Close() error { s.closed = true; return nil }

// selectJSONReader 使用标准 Decoder 和 JSON Pointer 的解码结果，只物化被选中值，跳过无关子树。
func selectJSONReader(reader io.Reader, pointer string) ([]byte, error) {
	p, err := jsonpointer.New(pointer)
	if err != nil {
		return nil, errResultPointer
	}
	dec := json.NewDecoder(reader)
	dec.UseNumber()
	value, found, err := walkJSONSelection(dec, p.DecodedTokens(), 0)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errResultPointer
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, errResultPointer
	}
	return json.Marshal(value)
}
func walkJSONSelection(dec *json.Decoder, path []string, depth int) (any, bool, error) {
	if depth > 256 {
		return nil, false, errResultPointer
	}
	if len(path) == 0 {
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, false, errResultPointer
		}
		return value, true, nil
	}
	token, err := dec.Token()
	if err != nil {
		return nil, false, errResultPointer
	}
	var selected any
	found := false
	switch token {
	case json.Delim('{'):
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, false, errResultPointer
			}
			if key == path[0] {
				selected, found, err = walkJSONSelection(dec, path[1:], depth+1)
			} else {
				err = skipJSONValue(dec, depth+1)
			}
			if err != nil {
				return nil, false, err
			}
		}
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			var err error
			if strconv.Itoa(i) == path[0] {
				selected, found, err = walkJSONSelection(dec, path[1:], depth+1)
			} else {
				err = skipJSONValue(dec, depth+1)
			}
			if err != nil {
				return nil, false, err
			}
		}
	default:
		return nil, false, errResultPointer
	}
	if _, err = dec.Token(); err != nil {
		return nil, false, errResultPointer
	}
	return selected, found, nil
}
func skipJSONValue(dec *json.Decoder, depth int) error {
	if depth > 256 {
		return errResultPointer
	}
	token, err := dec.Token()
	if err != nil {
		return errResultPointer
	}
	if delim, ok := token.(json.Delim); ok && (delim == '{' || delim == '[') {
		for dec.More() {
			if delim == '{' {
				if _, err = dec.Token(); err != nil {
					return errResultPointer
				}
			}
			if err = skipJSONValue(dec, depth+1); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		if err != nil {
			return errResultPointer
		}
	}
	return nil
}
