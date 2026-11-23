//go:build !nodevcheck

package devcheck_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/scttymn/gantry/devcheck"
	"github.com/scttymn/gantry/web"
)

// The map references each function, so renaming or removing one in web stops
// this compiling.
func TestDevelopmentOnlyMatchesWeb(t *testing.T) {
	functions := map[string]any{
		"Development":          web.Development,
		"DevelopmentWithRules": web.DevelopmentWithRules,
		"Timing":               web.Timing,
		"Reload":               web.Reload,
	}
	got := slices.Sorted(slices.Values(devcheck.DevelopmentOnly))
	want := slices.Sorted(maps.Keys(functions))
	if !slices.Equal(got, want) {
		t.Fatalf("development-only table %v, web's development-only functions %v", got, want)
	}
}
