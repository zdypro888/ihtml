package ihtml

import (
	"encoding/json"
	"testing"
)

// 虚构业务和任意字段验证同一机制，改工具名或数据域不需要新增展示分支。
func TestToolFactsFromSchemaUsesOnlyDeclaredSemantics(t *testing.T) {
	for _, domain := range []string{"shipping", "lighting", "allocation"} {
		schema := map[string]any{"properties": map[string]any{"target": map[string]any{"title": domain + " target"}, "setting": map[string]any{"title": "Setting", "x-display-values": map[string]string{"1": "First", "2": "Second"}}}}
		facts, ok := ToolFactsFromSchema(schema, map[string]any{"target": "arbitrary-id", "setting": float64(1)})
		if !ok || len(facts) != 2 || facts[0].Value != "First" || facts[1].Label != domain+" target" {
			t.Fatal(domain, facts, ok)
		}
		for _, request := range []map[string]any{{"unknown": "hidden"}, {"setting": 3}} {
			if _, ok := ToolFactsFromSchema(schema, request); ok {
				t.Fatal("undeclared change was hidden")
			}
		}
		raw, _ := json.Marshal(schema)
		var decoded map[string]any
		json.Unmarshal(raw, &decoded)
		if facts, ok := ToolFactsFromSchema(decoded, map[string]any{"target": "id", "setting": 1}); !ok || facts[0].Value != "First" {
			t.Fatal("JSON catalog lost declared values", facts, ok)
		}
		choices := map[string]any{"anyOf": []any{map[string]any{"properties": map[string]any{"other": map[string]any{"title": "Other"}}}, schema}}
		if _, ok := ToolFactsFromSchema(choices, map[string]any{"target": "id", "setting": 1}); !ok {
			t.Fatal("request variant lost")
		}
	}
}

// 相同字段名在不同领域有不同含义，渲染器只接受各自的声明，不能把 all 猜成某个产品范围。
func TestToolFactsFromSchemaDoesNotGuessFieldMeanings(t *testing.T) {
	for _, label := range []string{"整栋建筑", "全部章节", "整个云盘"} {
		schema := map[string]any{"properties": map[string]any{
			"scope":   map[string]any{"title": "范围", "x-display-values": map[string]string{"all": label}},
			"opaque":  map[string]any{"x-display-hidden": true},
			"targets": map[string]any{"title": "完整目标", "x-display-count-title": "目标数量"},
		}}
		facts, ok := ToolFactsFromSchema(schema, map[string]any{"scope": "all", "opaque": "server-journal", "targets": []any{"one", "two"}})
		if !ok || len(facts) != 3 || facts[0].Value != label || facts[1].Value != "2" || facts[2].Value != "one\ntwo" {
			t.Fatal(label, facts, ok)
		}
	}
	if _, ok := ToolFactsFromSchema(map[string]any{"properties": map[string]any{"scope": map[string]any{"type": "string"}}}, map[string]any{"scope": "all"}); ok {
		t.Fatal("guessed meaning of an undeclared field")
	}
}
