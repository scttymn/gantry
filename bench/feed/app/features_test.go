package app_test

import (
	"testing"
	"time"

	"github.com/scttymn/gantry/testkit"
)

// TestFeatures checks every route is in a feature of test/features.yaml,
// then visits each feature's pages as each role (testkit.Verify):
// bin/gantry verify runs it. A signed-in visitor is the API's: a bearer
// token for user 1, whichever of the two test users it's asked for, as
// nothing the API serves is cached for one user to leak to another.
func TestFeatures(t *testing.T) {
	rt := newApp(t).Router()
	testkit.Verify(t, rt.Handler(), rt.Routes(), "../test/features.yaml", testkit.Roles{
		testkit.SignedIn: func(v *testkit.Visitor, n int) string {
			v.Header.Set("Authorization", "Bearer "+token("test-secret", time.Now().Add(time.Hour)))
			return ""
		},
	})
}
