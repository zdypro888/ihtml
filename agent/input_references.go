package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/go-openapi/jsonpointer"
)

// ResolveToolInputReferences 仅向宿主声明的参数前缀填充不可变引用；确认与幂等仍冻结原引用参数。
// 不执行表达式、不选择 URL/方法/权限，展开后的参数仍由原工具 schema 和实时权限验证。
func ResolveToolInputReferences(ctx context.Context, raw json.RawMessage, prefixes []string, maxBytes int) (json.RawMessage, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, ErrTaskContract
	}
	refsRaw, ok := envelope["result_inputs"]
	if !ok {
		return raw, nil
	}
	var refs map[string]TaskResultInput
	if json.Unmarshal(refsRaw, &refs) != nil || len(refs) > 16 {
		return nil, ErrTaskContract
	}
	delete(envelope, "result_inputs")
	stripped, _ := json.Marshal(envelope)
	var value any
	decoder := json.NewDecoder(bytes.NewReader(stripped))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, ErrTaskContract
	}
	paths := make([]string, 0, len(refs))
	for path := range refs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for i, path := range paths {
		allowed := false
		for _, prefix := range prefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				allowed = true
			}
		}
		if !allowed || len(path) > 2048 || i > 0 && strings.HasPrefix(path, paths[i-1]+"/") {
			return nil, ErrTaskContract
		}
		body, err := ReadTaskResultInput(ctx, refs[path])
		if err != nil {
			return nil, err
		}
		var data any
		d := json.NewDecoder(bytes.NewReader(body))
		d.UseNumber()
		if d.Decode(&data) != nil {
			return nil, ErrTaskContract
		}
		p, err := jsonpointer.New(path)
		if err != nil {
			return nil, ErrTaskContract
		}
		value, err = p.Set(value, data)
		if err != nil {
			return nil, ErrTaskContract
		}
		result, _ := json.Marshal(value)
		if len(result) > maxBytes {
			return nil, ErrTaskResultRange
		}
	}
	out, err := json.Marshal(value)
	if len(out) > maxBytes {
		return nil, ErrTaskResultRange
	}
	return out, err
}
