package chat

import (
	"context"
	"errors"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
)

type modelFactoryContextKey struct{}

func TestModelFactoryDelegation(t *testing.T) {
	want := &stubToolCallingModel{}
	key := modelFactoryContextKey{}
	ctx := context.WithValue(context.Background(), key, "host")
	cfg := Config{Model: "host-model", ModelFactory: func(ctx context.Context, cfg Config) (einomodel.ToolCallingChatModel, error) {
		if ctx.Value(key) != "host" || cfg.Model != "host-model" {
			t.Fatal("factory lost context or effective configuration")
		}
		return want, nil
	}}
	got, err := newChatModel(ctx, cfg)
	if err != nil || got != want {
		t.Fatalf("model = %v, error = %v", got, err)
	}
}

func TestModelFactoryRejectsNilAndPropagatesError(t *testing.T) {
	want := errors.New("host model failure")
	for _, tc := range []struct {
		name  string
		model einomodel.ToolCallingChatModel
		err   error
	}{
		{name: "nil"},
		{name: "typed nil", model: (*stubToolCallingModel)(nil)},
		{name: "error", err: want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, err := newChatModel(context.Background(), Config{ModelFactory: func(context.Context, Config) (einomodel.ToolCallingChatModel, error) {
				return tc.model, tc.err
			}})
			if model != nil || err == nil {
				t.Fatalf("model = %v, error = %v", model, err)
			}
			if tc.err != nil && !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
}
