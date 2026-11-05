// Package mongostore 为 agent 的持久协议提供 Mongo 适配。宿主固定集合和身份，模型不能选择命名空间。
// 适配器复用 Mongo 原生 CAS/事务；业务事务和真实提供者语义由宿主注入。
package mongostore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func encode(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
