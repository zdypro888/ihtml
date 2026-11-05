package ihtml

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ToolFactsFromSchema 从可信接口 Schema 的 title 和 x-display-values 生成可读事实。
// 每个实际参数都必须有声明；未知字段或未知枚举返回 false，让宿主保留完整原参数，不能静默隐藏变更。
// 支持 anyOf/oneOf 请求变体，不依赖工具名、业务名或字段名。
func ToolFactsFromSchema(schema map[string]any, request map[string]any) ([]ToolCallFact, bool) {
	for _, key := range []string{"anyOf", "oneOf"} {
		if choices, ok := schema[key].([]any); ok {
			for _, choice := range choices {
				if child, ok := choice.(map[string]any); ok {
					if facts, ok := ToolFactsFromSchema(child, request); ok {
						return facts, true
					}
				}
			}
			return nil, false
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	keys := make([]string, 0, len(request))
	for key := range request {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	facts := make([]ToolCallFact, 0, len(keys))
	for _, key := range keys {
		field, ok := properties[key].(map[string]any)
		if !ok {
			return nil, false
		}
		if field["x-display-hidden"] == true {
			continue
		}
		label, ok := field["title"].(string)
		if !ok || strings.TrimSpace(label) == "" {
			return nil, false
		}
		value := request[key]
		text := fmt.Sprint(value)
		if declared, exists := field["x-display-values"]; exists {
			// Schema 可能来自 Go 声明或 JSON 目录；两种表示遵守相同的枚举显示约束。
			raw, err := json.Marshal(declared)
			labels := map[string]string{}
			if err != nil || json.Unmarshal(raw, &labels) != nil {
				return nil, false
			}
			var found bool
			text, found = labels[text]
			if !found {
				prefix, _ := field["x-display-prefix"].(string)
				if prefix == "" {
					return nil, false
				}
				text = prefix + fmt.Sprint(value)
			}
		}
		switch value.(type) {
		case map[string]any, []any:
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			text = string(raw)
		}
		if countLabel, ok := field["x-display-count-title"].(string); ok && countLabel != "" {
			list := reflect.ValueOf(value)
			if list.Kind() != reflect.Slice && list.Kind() != reflect.Array {
				return nil, false
			}
			values := []string{}
			for i := 0; i < list.Len(); i++ {
				values = append(values, fmt.Sprint(list.Index(i).Interface()))
			}
			facts = append(facts, ToolCallFact{Label: countLabel, Value: fmt.Sprint(list.Len())})
			text = strings.Join(values, "\n")
		}
		facts = append(facts, ToolCallFact{Label: label, Value: text})
	}
	return facts, true
}
