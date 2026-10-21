package featureflags

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestPublicDelegationsDefaultOff(t *testing.T) {
	if PublicDelegationsV1Enabled(context.Background(), nil) {
		t.Fatal("nil flag service enabled delegation")
	}
	if PublicDelegationsV1Enabled(context.Background(), featureflag.NewService(nil)) {
		t.Fatal("missing flag enabled delegation")
	}
}
