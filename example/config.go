package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/zdypro888/ihtml/chat"
	chatconfig "github.com/zdypro888/ihtml/chat/config"
)

const (
	exampleConfigVersion  = 1
	maxExampleConfigBytes = 64 << 10
)

// exampleFileConfig contains only host-owned settings. The AI object remains
// raw and is delegated to the reusable chat/config SDK package; this keeps the
// host schema version independent from the AI schema version.
type exampleFileConfig struct {
	Version int                     `json:"version"`
	Server  exampleServerFileConfig `json:"server"`
	AI      json.RawMessage         `json:"ai"`

	path string
}

type exampleServerFileConfig struct {
	Addr    string `json:"addr"`
	DataDir string `json:"data_dir"`
}

func loadExampleFileConfig(path string) (*exampleFileConfig, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("配置路径无效: %w", err)
	}
	data, err := readCheckedConfigFile(abs, maxExampleConfigBytes)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("配置文件必须是 UTF-8 JSON")
	}
	if err := validateUniqueJSONObject(data); err != nil {
		return nil, fmt.Errorf("配置 JSON 无效: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg exampleFileConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("配置字段无效: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if cfg.Version != exampleConfigVersion {
		return nil, fmt.Errorf("不支持的宿主配置版本 %d（当前仅支持 %d）", cfg.Version, exampleConfigVersion)
	}
	cfg.path = abs
	return &cfg, nil
}

func (cfg *exampleFileConfig) serverValues() (addr, dataDir string, err error) {
	addr = strings.TrimSpace(cfg.Server.Addr)
	if addr != "" {
		if _, _, splitErr := net.SplitHostPort(addr); splitErr != nil {
			return "", "", fmt.Errorf("server.addr 无效: %w", splitErr)
		}
	}
	dataDir = strings.TrimSpace(cfg.Server.DataDir)
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			dataDir = filepath.Join(filepath.Dir(cfg.path), dataDir)
		}
		dataDir = filepath.Clean(dataDir)
	}
	return addr, dataDir, nil
}

func (cfg *exampleFileConfig) chatConfig(probeBase string, probeQuery []string) (chat.Config, bool, error) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: chatconfig.NewOSSecretResolver()})
	result, err := loader.DecodeSection(context.Background(), cfg.AI)
	if err != nil {
		return chat.Config{}, false, err
	}
	if !result.Enabled {
		return chat.Config{}, false, nil
	}
	resolved := result.Config
	// Probe identity and transport are executable host capabilities, never data
	// accepted from a JSON file.
	resolved.ProbeBaseURL = strings.TrimSpace(probeBase)
	resolved.ProbeForwardQuery = append([]string(nil), probeQuery...)
	if err := resolved.Validate(); err != nil {
		return chat.Config{}, false, err
	}
	return resolved, true, nil
}

func readCheckedConfigFile(path string, maxBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("不允许符号链接")
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("必须是普通文件")
	}
	if before.Size() > maxBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限", maxBytes)
	}
	if runtime.GOOS != "windows" && before.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("配置文件不能允许 group/other 写入")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, errors.New("文件在读取期间发生变化")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限", maxBytes)
	}
	return data, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("配置文件只能包含一个顶层 JSON 对象")
		}
		return fmt.Errorf("配置文件尾部无效: %w", err)
	}
	return nil
}

func validateUniqueJSONObject(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("顶层必须是 JSON 对象")
	}
	if err := consumeJSONObject(dec, "$"); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("配置文件包含尾随 JSON")
		}
		return err
	}
	return nil
}

func consumeJSONObject(dec *json.Decoder, path string) error {
	seen := make(map[string]struct{})
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("%s 的对象键无效", path)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s.%s 重复", path, key)
		}
		seen[key] = struct{}{}
		if err := consumeJSONValue(dec, path+"."+key); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return fmt.Errorf("%s 对象未正确结束", path)
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder, path string) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return consumeJSONObject(dec, path)
	case '[':
		for index := 0; dec.More(); index++ {
			if err := consumeJSONValue(dec, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("%s 数组未正确结束", path)
		}
		return nil
	default:
		return fmt.Errorf("%s 包含无效 JSON 分隔符", path)
	}
}
