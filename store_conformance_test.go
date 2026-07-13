package ihtml_test

import (
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/storetest"
)

// 两个内置 Store 用公开的 storetest 一致性套件验收——与第三方 Store 同一标准。
func TestBuiltinStoreConformance(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		storetest.Run(t, func(t *testing.T) ihtml.Store { return ihtml.NewMemoryStore() })
	})
	t.Run("file", func(t *testing.T) {
		storetest.Run(t, func(t *testing.T) ihtml.Store {
			fs, err := ihtml.NewFileStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return fs
		})
	})
}
