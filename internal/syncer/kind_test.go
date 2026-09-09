package syncer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kvth/gitnotesync/internal/gitx"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"nil", nil, KindNone},
		{"conflict", fmt.Errorf("%w: diverged", ErrConflict), KindConflict},
		{"busy", fmt.Errorf("%w: rebase", ErrBusy), KindBusy},
		{"detached", ErrDetached, KindBusy},
		{"config", fmt.Errorf("%w: %w", ErrConfig, errors.New("user.name")), KindConfig},
		{"remote", fmt.Errorf("%w: fetch origin: %w", ErrRemote, errors.New("128")), KindRemote},
		{"bare git error", &gitx.Error{Args: []string{"status"}, Code: 128}, KindUnknown},
		{"unrelated", errors.New("boom"), KindUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// A push rejected by the remote is retried through a pull, so the conflict
// that pull may hit has to outrank the remote failure that led to it.
func TestClassifyPrefersConflictOverRemote(t *testing.T) {
	err := fmt.Errorf("%w: push origin/main: %w", ErrRemote, ErrConflict)
	if got := Classify(err); got != KindConflict {
		t.Errorf("Classify = %q, want %q", got, KindConflict)
	}
}

func TestNeedsHuman(t *testing.T) {
	for _, k := range []Kind{KindConflict, KindBusy, KindConfig} {
		if !k.NeedsHuman() {
			t.Errorf("%q should need a human", k)
		}
	}
	for _, k := range []Kind{KindNone, KindRemote, KindUnknown} {
		if k.NeedsHuman() {
			t.Errorf("%q should not need a human", k)
		}
	}
}
