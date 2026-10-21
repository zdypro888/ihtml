package uitools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zdypro888/ihtml"
)

// DecodeItems parses a non-empty complete item list. It does not normalize
// names, check executable code, authorize a write or install the items.
func DecodeItems(raw json.RawMessage) ([]ihtml.Item, error) {
	var items []ihtml.Item
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("items 参数格式不正确: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("items 为空")
	}
	return items, nil
}

// ValidatePublication requires every card to belong to the validated page.
// Hosts may fill empty page names before this call; global/other-page items
// are never silently moved. Publication still needs a single atomic service call.
func ValidatePublication(page ihtml.Page, items []ihtml.Item) error {
	if err := page.Validate(); err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("items 为空")
	}
	for _, item := range items {
		if item.Page != page.Name {
			return fmt.Errorf("item %q 的 page 必须是 %q", item.ID, page.Name)
		}
	}
	return nil
}
