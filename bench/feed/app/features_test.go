package app_test

import (
	"testing"
	"time"

	"github.com/scttymn/gantry/testkit"
)

func TestFeatures(t *testing.T) {
	rt := newApp(t).Router()
	testkit.Verify(t, rt.Handler(), rt.Routes(), "../test/features.yaml", testkit.Roles{
		testkit.SignedIn: func(v *testkit.Visitor, n int) string {
			v.Header.Set("Authorization", "Bearer "+token("test-secret", time.Now().Add(time.Hour)))
			return ""
		},
	})
}
