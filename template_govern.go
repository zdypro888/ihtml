package ihtml

import "context"

func (s *governedService) ListTemplates(ctx context.Context, user string) ([]Template, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return nil, err
	}
	return s.h.ListTemplates(ctx, user)
}

func (s *governedService) GetTemplate(ctx context.Context, user, id string) (Template, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return Template{}, err
	}
	return s.h.GetTemplate(ctx, user, id)
}

func (s *governedService) GetCurrentTemplate(ctx context.Context, user, page string) (CurrentTemplate, error) {
	ctx, err := s.check(ctx, user, false)
	if err != nil {
		return CurrentTemplate{}, err
	}
	return s.h.GetCurrentTemplate(ctx, user, page)
}

func (s *governedService) PutTemplate(ctx context.Context, user string, template Template, expectedRevision uint64, _ string) (Template, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return Template{}, err
	}
	return s.h.PutTemplate(ctx, user, template, expectedRevision, s.source)
}

func (s *governedService) PutCurrentTemplate(ctx context.Context, user, page string, template Template, expectedRevision uint64, _ string) (Template, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return Template{}, err
	}
	return s.h.PutCurrentTemplate(ctx, user, page, template, expectedRevision, s.source)
}

func (s *governedService) PublishTemplate(ctx context.Context, user, id string, expectedRevision uint64, _ string) (Template, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return Template{}, err
	}
	return s.h.PublishTemplate(ctx, user, id, expectedRevision, s.source)
}

func (s *governedService) DeleteTemplateSlot(ctx context.Context, user, id, slotID string, expectedRevision uint64, _ string) (Template, error) {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return Template{}, err
	}
	return s.h.DeleteTemplateSlot(ctx, user, id, slotID, expectedRevision, s.source)
}

func (s *governedService) DeleteTemplate(ctx context.Context, user, id string, expectedRevision uint64, _ string) error {
	ctx, err := s.check(ctx, user, true)
	if err != nil {
		return err
	}
	return s.h.DeleteTemplate(ctx, user, id, expectedRevision, s.source)
}
