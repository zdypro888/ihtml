package einoengine

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
	ai "github.com/zdypro888/ihtml/agent"
)

// ResultBackend 适配 Eino 原生文件协议，只读取现有不可变结果，不访问宿主磁盘。
// 路径固定为 /results/<taskID>/<sha256>；每次读取均由当前源权限重新核验。
type ResultBackend struct {
	Runtime *ai.TaskRuntime
	Scope   string
	Allowed map[string]bool
}

var _ filesystem.Backend = (*ResultBackend)(nil)

// Read 使用 Eino 的行范围协议，64KiB 输出上限独立于持久正文；更大内容用 task_result 字节分页。
func (b *ResultBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	if b.Runtime == nil || req == nil {
		return nil, ai.ErrTaskNotFound
	}
	parts := strings.Split(req.FilePath, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "results" || parts[2] == "" || strings.ContainsAny(parts[2], ".\\") || len(parts[3]) != 64 {
		return nil, ai.ErrTaskNotFound
	}
	if _, err := hex.DecodeString(parts[3]); err != nil {
		return nil, ai.ErrTaskNotFound
	}
	body, err := b.Runtime.ResolveResultInput(ctx, b.Scope, ai.TaskResultInput{ID: parts[2], SHA256: parts[3]}, b.Allowed)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(body), "\n")
	offset := max(1, req.Offset) - 1
	if offset > len(lines) || req.Limit < 0 {
		return nil, ai.ErrTaskResultRange
	}
	end := len(lines)
	if req.Limit > 0 {
		end = min(end, offset+req.Limit)
	}
	content := strings.Join(lines[offset:end], "\n")
	if len(content) > 64<<10 {
		return nil, ai.ErrTaskResultRange
	}
	return &filesystem.FileContent{Content: content}, nil
}

// 其余操作显式拒绝；目录枚举仍使用原有带游标 task_read，不能静默返回不完整清单。
func (*ResultBackend) LsInfo(context.Context, *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	return nil, ai.ErrTaskNotFound
}
func (*ResultBackend) GrepRaw(context.Context, *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	return nil, ai.ErrTaskNotFound
}
func (*ResultBackend) GlobInfo(context.Context, *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	return nil, ai.ErrTaskNotFound
}
func (*ResultBackend) Write(context.Context, *filesystem.WriteRequest) error {
	return ai.ErrTaskConflict
}
func (*ResultBackend) Edit(context.Context, *filesystem.EditRequest) error { return ai.ErrTaskConflict }
