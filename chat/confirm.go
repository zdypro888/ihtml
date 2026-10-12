package chat

import (
	"context"
	"errors"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/confirmation"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

// Keep chat's constructors compatible while sharing the signed wire contract.
type confirmationScope confirmation.Scope

func newConfirmationScope(sessionID, capabilityScope string) confirmationScope {
	return confirmationScope(confirmation.NewScope(sessionID, capabilityScope))
}

type ConfirmNonceStore = confirmation.NonceStore
type memoryConfirmNonceStore = confirmation.MemoryNonceStore

func newMemoryConfirmNonceStore(maxNonces ...int) *memoryConfirmNonceStore {
	return confirmation.NewMemoryNonceStore(maxNonces...)
}

type confirmSigner struct {
	signer *confirmation.Signer
	keyErr error
	nonces ConfirmNonceStore
}

// Legacy chat treats typed-nil stores as the memory default. The public
// constructor deliberately rejects explicitly typed-nil dependencies.
func newConfirmSigner(secret []byte, nonceStores ...ConfirmNonceStore) *confirmSigner {
	var store ConfirmNonceStore
	if len(nonceStores) > 0 {
		store = nonceStores[0]
	}
	if nilcheck.IsNil(store) {
		store = newMemoryConfirmNonceStore()
	}
	signer, err := confirmation.NewChecked(confirmation.Config{Secret: secret, NonceStore: store})
	return &confirmSigner{signer: signer, keyErr: err, nonces: store}
}

func (s *confirmSigner) sign(user string, scope confirmationScope, call ihtml.ProposedToolCall) string {
	if s == nil {
		return ""
	}
	return s.signer.Sign(user, confirmation.Scope(scope), call)
}

func (s *confirmSigner) check() error {
	if s == nil {
		return errors.New("confirmation signer is unavailable")
	}
	return s.keyErr
}

func (s *confirmSigner) prepareContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (ihtml.ProposedToolCall, error) {
	if err := s.check(); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	return s.signer.Prepare(ctx, user, confirmation.Scope(scope), call)
}

func (s *confirmSigner) consumeContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (bool, error) {
	if err := s.check(); err != nil {
		return false, err
	}
	return s.signer.Consume(ctx, user, confirmation.Scope(scope), call)
}

func (s *confirmSigner) consume(user string, scope confirmationScope, call ihtml.ProposedToolCall) bool {
	ok, _ := s.consumeContext(context.Background(), user, scope, call)
	return ok
}

func (s *confirmSigner) revokeContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (bool, error) {
	return s.consumeContext(ctx, user, scope, call)
}
