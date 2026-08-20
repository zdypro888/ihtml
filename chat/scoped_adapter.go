package chat

import (
	"context"
	"encoding/json"

	"github.com/zdypro888/ihtml"
)

// scopedServiceAdapter lets the legacy user-parameterized internal runtime
// consume the safer request-bound SDK capability. Every user argument is
// intentionally ignored; identity remains owned by ScopedService.
type scopedServiceAdapter struct{ ihtml.ScopedService }

type scopedPublishingServiceAdapter struct {
	scopedServiceAdapter
	publisher ihtml.ScopedPagePublicationService
}

func (s scopedServiceAdapter) ListItems(ctx context.Context, _ string) ([]ihtml.Item, error) {
	return s.ScopedService.ListItems(ctx)
}
func (s scopedServiceAdapter) PutItems(ctx context.Context, _ string, items []ihtml.Item, by, note string) error {
	return s.ScopedService.PutItems(ctx, items, by, note)
}
func (s scopedServiceAdapter) DeleteItems(ctx context.Context, _ string, ids []string, by, note string) ([]string, error) {
	return s.ScopedService.DeleteItems(ctx, ids, by, note)
}
func (s scopedServiceAdapter) ReplaceItems(ctx context.Context, _ string, items []ihtml.Item, by, note string) error {
	return s.ScopedService.ReplaceItems(ctx, items, by, note)
}
func (s scopedServiceAdapter) ListRevisions(ctx context.Context, _ string) ([]ihtml.RevisionInfo, error) {
	return s.ScopedService.ListRevisions(ctx)
}
func (s scopedServiceAdapter) RevisionItems(ctx context.Context, _, id string) ([]ihtml.Item, error) {
	return s.ScopedService.RevisionItems(ctx, id)
}
func (s scopedServiceAdapter) Rollback(ctx context.Context, _, revisionID, by string) error {
	return s.ScopedService.Rollback(ctx, revisionID, by)
}
func (s scopedServiceAdapter) KVGet(ctx context.Context, _, key string) (json.RawMessage, error) {
	return s.ScopedService.KVGet(ctx, key)
}
func (s scopedServiceAdapter) KVSet(ctx context.Context, _ string, key string, value json.RawMessage) error {
	return s.ScopedService.KVSet(ctx, key, value)
}
func (s scopedServiceAdapter) KVDelete(ctx context.Context, _, key string) error {
	return s.ScopedService.KVDelete(ctx, key)
}
func (s scopedServiceAdapter) PageErrors(ctx context.Context, _ string) ([]ihtml.PageError, error) {
	return s.ScopedService.PageErrors(ctx)
}
func (s scopedServiceAdapter) ReportErrors(ctx context.Context, _ string, errs []ihtml.PageError) error {
	return s.ScopedService.ReportErrors(ctx, errs)
}
func (s scopedServiceAdapter) ListPages(ctx context.Context, _ string) ([]ihtml.Page, error) {
	return s.ScopedService.ListPages(ctx)
}
func (s scopedServiceAdapter) SetPage(ctx context.Context, _ string, page ihtml.Page) error {
	return s.ScopedService.SetPage(ctx, page)
}
func (s scopedPublishingServiceAdapter) PublishPage(ctx context.Context, _ string, page ihtml.Page, items []ihtml.Item, by, note string) error {
	return s.publisher.PublishPage(ctx, page, items, by, note)
}
func (s scopedServiceAdapter) DeletePage(ctx context.Context, _, name, by string) ([]string, error) {
	return s.ScopedService.DeletePage(ctx, name, by)
}
func (s scopedServiceAdapter) ListTemplates(ctx context.Context, _ string) ([]ihtml.Template, error) {
	return s.ScopedService.ListTemplates(ctx)
}
func (s scopedServiceAdapter) GetTemplate(ctx context.Context, _, id string) (ihtml.Template, error) {
	return s.ScopedService.GetTemplate(ctx, id)
}
func (s scopedServiceAdapter) GetCurrentTemplate(ctx context.Context, _, page string) (ihtml.CurrentTemplate, error) {
	return s.ScopedService.GetCurrentTemplate(ctx, page)
}
func (s scopedServiceAdapter) PutTemplate(ctx context.Context, _ string, template ihtml.Template, revision uint64, by string) (ihtml.Template, error) {
	return s.ScopedService.PutTemplate(ctx, template, revision, by)
}
func (s scopedServiceAdapter) PutCurrentTemplate(ctx context.Context, _, page string, template ihtml.Template, revision uint64, by string) (ihtml.Template, error) {
	return s.ScopedService.PutCurrentTemplate(ctx, page, template, revision, by)
}
func (s scopedServiceAdapter) PublishTemplate(ctx context.Context, _, id string, revision uint64, by string) (ihtml.Template, error) {
	return s.ScopedService.PublishTemplate(ctx, id, revision, by)
}
func (s scopedServiceAdapter) DeleteTemplateSlot(ctx context.Context, _, id, slotID string, revision uint64, by string) (ihtml.Template, error) {
	return s.ScopedService.DeleteTemplateSlot(ctx, id, slotID, revision, by)
}
func (s scopedServiceAdapter) DeleteTemplate(ctx context.Context, _, id string, revision uint64, by string) error {
	return s.ScopedService.DeleteTemplate(ctx, id, revision, by)
}

var _ ihtml.Service = scopedServiceAdapter{}
var _ ihtml.PagePublicationService = scopedPublishingServiceAdapter{}
