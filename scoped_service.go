package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// ErrInvalidServiceScope is returned when a user-scoped service cannot be
// constructed safely.
var ErrInvalidServiceScope = errors.New("ihtml: invalid service scope")

// ScopedItemService is ItemService with the authenticated user already bound.
type ScopedItemService interface {
	ListItems(context.Context) ([]Item, error)
	PutItems(context.Context, []Item, string, string) error
	DeleteItems(context.Context, []string, string, string) ([]string, error)
	ReplaceItems(context.Context, []Item, string, string) error
}

// ScopedRevisionService is RevisionService with the user already bound.
type ScopedRevisionService interface {
	ListRevisions(context.Context) ([]RevisionInfo, error)
	RevisionItems(context.Context, string) ([]Item, error)
	Rollback(context.Context, string, string) error
}

// ScopedKVService is KVService with the user already bound.
type ScopedKVService interface {
	KVGet(context.Context, string) (json.RawMessage, error)
	KVSet(context.Context, string, json.RawMessage) error
	KVDelete(context.Context, string) error
}

// ScopedVersionedKVService 是绑定用户后的可选条件 KV 能力。底层 Service 同时
// 实现 VersionedKVService 时，ScopeService 返回值才可通过类型断言取得它，
// 因而无需扩展既有 ScopedService 或破坏第三方旧实现。
type ScopedVersionedKVService interface {
	KVGetWithRevision(context.Context, string) (json.RawMessage, string, error)
	KVCompareAndSet(context.Context, string, json.RawMessage, string) (string, error)
}

// ScopedErrorService is ErrorService with the user already bound.
type ScopedErrorService interface {
	PageErrors(context.Context) ([]PageError, error)
	ReportErrors(context.Context, []PageError) error
}

// ScopedPageService is PageService with the user already bound.
type ScopedPageService interface {
	ListPages(context.Context) ([]Page, error)
	SetPage(context.Context, Page) error
	DeletePage(context.Context, string, string) ([]string, error)
}

// ScopedPagePublicationService is the user-bound form of the optional atomic
// page publication capability.
type ScopedPagePublicationService interface {
	PublishPage(context.Context, Page, []Item, string, string) error
}

// ScopedTemplateService is TemplateService with the user already bound.
type ScopedTemplateService interface {
	ListTemplates(context.Context) ([]Template, error)
	GetTemplate(context.Context, string) (Template, error)
	GetCurrentTemplate(context.Context, string) (CurrentTemplate, error)
	PutTemplate(context.Context, Template, uint64, string) (Template, error)
	PutCurrentTemplate(context.Context, string, Template, uint64, string) (Template, error)
	PublishTemplate(context.Context, string, uint64, string) (Template, error)
	DeleteTemplateSlot(context.Context, string, string, uint64, string) (Template, error)
	DeleteTemplate(context.Context, string, uint64, string) error
}

// ScopedService is the cross-tenant-safe form of Service. It removes every
// user argument, preventing accidental reads or writes to a different tenant.
type ScopedService interface {
	ScopedItemService
	ScopedRevisionService
	ScopedKVService
	ScopedErrorService
	ScopedPageService
	ScopedTemplateService
}

type boundService struct {
	service Service
	user    string
}

type boundVersionedService struct {
	*boundService
	versioned VersionedKVService
}

type boundPublishingService struct {
	*boundService
	publisher PagePublicationService
}

type boundVersionedPublishingService struct {
	*boundService
	versioned VersionedKVService
	publisher PagePublicationService
}

// ScopeService binds a trusted Service to one user. It does not add an access
// policy by itself; use Handler.ServiceForRequest when handling an HTTP request.
func ScopeService(service Service, user string) (ScopedService, error) {
	user = strings.TrimSpace(user)
	if nilcheck.IsNil(service) {
		return nil, errors.Join(ErrInvalidServiceScope, errors.New("service is required"))
	}
	if user == "" {
		return nil, errors.Join(ErrInvalidServiceScope, errors.New("user is required"))
	}
	bound := &boundService{service: service, user: user}
	versioned, hasVersioned := service.(VersionedKVService)
	publisher, hasPublisher := service.(PagePublicationService)
	switch {
	case hasVersioned && hasPublisher:
		return &boundVersionedPublishingService{boundService: bound, versioned: versioned, publisher: publisher}, nil
	case hasVersioned:
		return &boundVersionedService{boundService: bound, versioned: versioned}, nil
	case hasPublisher:
		return &boundPublishingService{boundService: bound, publisher: publisher}, nil
	}
	return bound, nil
}

// ServiceForRequest resolves and binds the authenticated user, then returns a
// service that rechecks identity, AccessPolicy, Handler.Close state, and shared
// write limits on every call. This is the recommended API for host HTTP routes.
func (h *Handler) ServiceForRequest(r *http.Request) (ScopedService, error) {
	if h == nil || r == nil || h.closed.Load() {
		return nil, ErrAccessDenied
	}
	user, err := h.opts.resolver(r)
	user = strings.TrimSpace(user)
	if err != nil || user == "" {
		return nil, ErrAccessDenied
	}
	governed := &governedService{h: h, request: r, user: user, source: "sdk"}
	return ScopeService(governed, user)
}

func (s *boundService) ListItems(ctx context.Context) ([]Item, error) {
	return s.service.ListItems(ctx, s.user)
}

func (s *boundService) PutItems(ctx context.Context, items []Item, by, note string) error {
	return s.service.PutItems(ctx, s.user, items, by, note)
}

func (s *boundService) DeleteItems(ctx context.Context, ids []string, by, note string) ([]string, error) {
	return s.service.DeleteItems(ctx, s.user, ids, by, note)
}

func (s *boundService) ReplaceItems(ctx context.Context, items []Item, by, note string) error {
	return s.service.ReplaceItems(ctx, s.user, items, by, note)
}

func (s *boundService) ListRevisions(ctx context.Context) ([]RevisionInfo, error) {
	return s.service.ListRevisions(ctx, s.user)
}

func (s *boundService) RevisionItems(ctx context.Context, id string) ([]Item, error) {
	return s.service.RevisionItems(ctx, s.user, id)
}

func (s *boundService) Rollback(ctx context.Context, revisionID, by string) error {
	return s.service.Rollback(ctx, s.user, revisionID, by)
}

func (s *boundService) KVGet(ctx context.Context, key string) (json.RawMessage, error) {
	return s.service.KVGet(ctx, s.user, key)
}

func (s *boundService) KVSet(ctx context.Context, key string, value json.RawMessage) error {
	return s.service.KVSet(ctx, s.user, key, value)
}

func (s *boundService) KVDelete(ctx context.Context, key string) error {
	return s.service.KVDelete(ctx, s.user, key)
}

func (s *boundVersionedService) KVGetWithRevision(ctx context.Context, key string) (json.RawMessage, string, error) {
	return s.versioned.KVGetWithRevision(ctx, s.user, key)
}

func (s *boundVersionedService) KVCompareAndSet(ctx context.Context, key string, value json.RawMessage, expectedRevision string) (string, error) {
	return s.versioned.KVCompareAndSet(ctx, s.user, key, value, expectedRevision)
}

func (s *boundVersionedPublishingService) KVGetWithRevision(ctx context.Context, key string) (json.RawMessage, string, error) {
	return s.versioned.KVGetWithRevision(ctx, s.user, key)
}

func (s *boundVersionedPublishingService) KVCompareAndSet(ctx context.Context, key string, value json.RawMessage, expectedRevision string) (string, error) {
	return s.versioned.KVCompareAndSet(ctx, s.user, key, value, expectedRevision)
}

func (s *boundService) PageErrors(ctx context.Context) ([]PageError, error) {
	return s.service.PageErrors(ctx, s.user)
}

func (s *boundService) ReportErrors(ctx context.Context, pageErrors []PageError) error {
	return s.service.ReportErrors(ctx, s.user, pageErrors)
}

func (s *boundService) ListPages(ctx context.Context) ([]Page, error) {
	return s.service.ListPages(ctx, s.user)
}

func (s *boundService) SetPage(ctx context.Context, page Page) error {
	return s.service.SetPage(ctx, s.user, page)
}

func (s *boundPublishingService) PublishPage(ctx context.Context, page Page, items []Item, by, note string) error {
	return s.publisher.PublishPage(ctx, s.user, page, items, by, note)
}

func (s *boundVersionedPublishingService) PublishPage(ctx context.Context, page Page, items []Item, by, note string) error {
	return s.publisher.PublishPage(ctx, s.user, page, items, by, note)
}

func (s *boundService) DeletePage(ctx context.Context, name, by string) ([]string, error) {
	return s.service.DeletePage(ctx, s.user, name, by)
}

func (s *boundService) ListTemplates(ctx context.Context) ([]Template, error) {
	return s.service.ListTemplates(ctx, s.user)
}

func (s *boundService) GetTemplate(ctx context.Context, id string) (Template, error) {
	return s.service.GetTemplate(ctx, s.user, id)
}

func (s *boundService) GetCurrentTemplate(ctx context.Context, page string) (CurrentTemplate, error) {
	return s.service.GetCurrentTemplate(ctx, s.user, page)
}

func (s *boundService) PutTemplate(ctx context.Context, template Template, expectedRevision uint64, by string) (Template, error) {
	return s.service.PutTemplate(ctx, s.user, template, expectedRevision, by)
}

func (s *boundService) PutCurrentTemplate(ctx context.Context, page string, template Template, expectedRevision uint64, by string) (Template, error) {
	return s.service.PutCurrentTemplate(ctx, s.user, page, template, expectedRevision, by)
}

func (s *boundService) PublishTemplate(ctx context.Context, id string, expectedRevision uint64, by string) (Template, error) {
	return s.service.PublishTemplate(ctx, s.user, id, expectedRevision, by)
}

func (s *boundService) DeleteTemplateSlot(ctx context.Context, id, slotID string, expectedRevision uint64, by string) (Template, error) {
	return s.service.DeleteTemplateSlot(ctx, s.user, id, slotID, expectedRevision, by)
}

func (s *boundService) DeleteTemplate(ctx context.Context, id string, expectedRevision uint64, by string) error {
	return s.service.DeleteTemplate(ctx, s.user, id, expectedRevision, by)
}
