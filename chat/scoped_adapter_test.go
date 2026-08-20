package chat

import (
	"context"
	"testing"

	"github.com/zdypro888/ihtml"
)

func TestScopedServiceAdapterIgnoresLegacyUserArguments(t *testing.T) {
	base := ihtml.New(ihtml.NewMemoryStore())
	defer base.Close()
	scoped, err := ihtml.ScopeService(base, "alice")
	if err != nil {
		t.Fatal(err)
	}
	adapter := scopedServiceAdapter{ScopedService: scoped}
	if err := adapter.PutItems(context.Background(), "mallory", []ihtml.Item{{
		ID: "safe", Type: ihtml.ItemHTML, Content: "ok",
	}}, "test", "scope"); err != nil {
		t.Fatal(err)
	}
	alice, err := base.ListItems(context.Background(), "alice")
	if err != nil || len(alice) != 1 || alice[0].ID != "safe" {
		t.Fatalf("bound user did not receive item: %+v, %v", alice, err)
	}
	mallory, err := base.ListItems(context.Background(), "mallory")
	if err != nil || len(mallory) != 0 {
		t.Fatalf("legacy user argument escaped scope: %+v, %v", mallory, err)
	}
}

func TestScopedPublishingAdapterKeepsBoundIdentity(t *testing.T) {
	base := ihtml.New(ihtml.NewMemoryStore())
	defer base.Close()
	scoped, err := ihtml.ScopeService(base, "alice")
	if err != nil {
		t.Fatal(err)
	}
	publisher, ok := scoped.(ihtml.ScopedPagePublicationService)
	if !ok {
		t.Fatal("Handler scope is missing page publication capability")
	}
	adapter := scopedPublishingServiceAdapter{
		scopedServiceAdapter: scopedServiceAdapter{ScopedService: scoped},
		publisher:            publisher,
	}
	if err := adapter.PublishPage(context.Background(), "mallory", ihtml.Page{Name: "report", Title: "Report"}, []ihtml.Item{
		{ID: "body", Type: ihtml.ItemHTML, Page: "report", Content: "ready"},
	}, "test", "publish"); err != nil {
		t.Fatal(err)
	}
	alice, _ := base.ListPages(context.Background(), "alice")
	mallory, _ := base.ListPages(context.Background(), "mallory")
	if len(alice) != 1 || alice[0].Name != "report" || len(mallory) != 0 {
		t.Fatalf("publishing adapter escaped scope: alice=%+v mallory=%+v", alice, mallory)
	}
}
