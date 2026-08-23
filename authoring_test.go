package ihtml

import (
	"strings"
	"testing"
)

func TestPageAuthoringGuideIsReusableByHostAgents(t *testing.T) {
	for _, contract := range []string{
		"ui-page", "ui-table-wrap", ".ihtml-root.ihtml-mobile", "--ui-*", "多个职责清晰的 Item", "ihtml.env.mode",
	} {
		if !strings.Contains(PageAuthoringGuide, contract) {
			t.Fatalf("page authoring guide is missing %q", contract)
		}
	}
	for _, forbidden := range []string{"员工", "全局概览", "Telegram"} {
		if strings.Contains(PageAuthoringGuide, forbidden) {
			t.Fatalf("page authoring guide contains business-specific term %q", forbidden)
		}
	}
}
