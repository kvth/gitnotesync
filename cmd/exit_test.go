package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kvth/gitnotesync/internal/syncer"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("boom"), ExitFailed},
		{fmt.Errorf("%w: rebase", syncer.ErrBusy), ExitBusy},
		{syncer.ErrDetached, ExitBusy},
		{fmt.Errorf("%w: diverged", syncer.ErrConflict), ExitConflict},
		{fmt.Errorf("%w: not a directory", syncer.ErrConfig), ExitConfig},
		{fmt.Errorf("%w: fetch origin", syncer.ErrRemote), ExitRemote},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}

// Every status has to name exactly one kind of failure, or a caller keying off
// them cannot tell two apart.
func TestExitCodesAreDistinct(t *testing.T) {
	seen := map[int]bool{}
	for _, c := range []int{ExitOK, ExitFailed, ExitBusy, ExitConflict, ExitConfig, ExitRemote} {
		if seen[c] {
			t.Errorf("exit status %d is used twice", c)
		}
		seen[c] = true
	}
}

func TestHint(t *testing.T) {
	if Hint(fmt.Errorf("%w: diverged", syncer.ErrConflict)) == "" {
		t.Error("a conflict should come with a hint")
	}
	if got := Hint(errors.New("boom")); got != "" {
		t.Errorf("Hint on an unclassified error = %q, want empty", got)
	}
}
