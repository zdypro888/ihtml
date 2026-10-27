package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyTurnFailure(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{context.DeadlineExceeded, "timeout"}, {fmt.Errorf("wrapper: %w", context.Canceled), "cancelled"}, {errors.New("exceeds max iterations"), "iteration_budget"}, {errors.New("private-key: network timeout"), "timeout"}, {errors.New("private-provider-value"), "model_incomplete"}, {nil, "model_incomplete"}} {
		if got := ClassifyTurnFailure(tc.err); got != tc.want {
			t.Errorf("classification=%s want=%s", got, tc.want)
		}
	}
}
