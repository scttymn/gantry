package devcheck

import (
	"reflect"
	"strings"
	"testing"
)

func TestStaleExceptions(t *testing.T) {
	root := t.TempDir()
	put(t, root, "app/page.html", `<script src="/custom.js"></script>`)
	put(t, root, ".gantry-check.json", `{"exceptions":[
		{"rule":"asset-url","file":"app/page.html","reason":"Custom handler revalidates this script (#7)."},
		{"rule":"raw-assets","file":"app/page.html","reason":"Served elsewhere (#8)."},
		{"rule":"asset-url","file":"app/gone.html","reason":"Deleted page (#9)."}
	]}`)
	got, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []Warning{
		{File: ".gantry-check.json", Rule: "stale-exception", Severity: Advice, Message: "exception raw-assets app/page.html matched nothing; remove it so it can't hide a future violation"},
		{File: ".gantry-check.json", Rule: "stale-exception", Severity: Advice, Message: "exception asset-url app/gone.html matched nothing; remove it so it can't hide a future violation"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	put(t, root, ".gantry-check.json", `{"exceptions":[{"rule":"stale-exception","file":"app/page.html","reason":"Quiet."}]}`)
	if _, err := Scan(root); err == nil || !strings.Contains(err.Error(), `unknown rule "stale-exception"`) {
		t.Fatal("an exception for stale-exception should be an unknown rule:", err)
	}
}

func TestRequireIssue(t *testing.T) {
	root := t.TempDir()
	put(t, root, "app/page.html", `<script src="/custom.js"></script>`)
	config := func(requireIssue bool, reason string) string {
		r := ""
		if requireIssue {
			r = `"requireIssue":true,`
		}
		return `{` + r + `"exceptions":[{"rule":"asset-url","file":"app/page.html","reason":"` + reason + `"}]}`
	}
	for _, reason := range []string{"Custom handler revalidates this script; see #12.", "Decided in https://example.com/decisions/7"} {
		put(t, root, ".gantry-check.json", config(true, reason))
		if got, err := Scan(root); err != nil || len(got) != 0 {
			t.Fatalf("reason %q: %+v %v", reason, got, err)
		}
	}
	for _, reason := range []string{"Custom handler revalidates this script.", "Tracked in # 12"} {
		put(t, root, ".gantry-check.json", config(true, reason))
		_, err := Scan(root)
		if err == nil || err.Error() != `.gantry-check.json: exception asset-url app/page.html: reason must cite an issue (#123) or link an issue or decision record; "requireIssue" is set` {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	put(t, root, ".gantry-check.json", config(false, "Custom handler revalidates this script."))
	got, err := Scan(root)
	want := []Warning{{File: ".gantry-check.json", Rule: "exception-reason", Severity: Advice, Message: "exception asset-url app/page.html: the reason cites no issue (#123) or link to an issue or decision record; add one so the exception points at where it was decided"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("without requireIssue a plain reason is advice:\ngot %+v %v\nwant %+v", got, err, want)
	}
	put(t, root, ".gantry-check.json", config(false, "Custom handler revalidates this script; see #12."))
	if got, err := Scan(root); err != nil || len(got) != 0 {
		t.Fatalf("a cited reason without requireIssue: %+v %v", got, err)
	}
	put(t, root, ".gantry-check.json", `{"requireIssue":true,"exceptions":[{"rule":"exception-reason","file":"app/page.html","reason":"#1"}]}`)
	if _, err := Scan(root); err == nil || !strings.Contains(err.Error(), `unknown rule "exception-reason"`) {
		t.Fatal("an exception for exception-reason should be an unknown rule:", err)
	}
}
