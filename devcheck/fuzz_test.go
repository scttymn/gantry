package devcheck

import "testing"

func FuzzJSAndHTMLRules(f *testing.F) {
	f.Add("assets/js/controllers/a_controller.js", `import { Controller } from "@hotwired/stimulus"
export default class extends Controller {
  connect() { setTimeout(() => {}, 10); this.element.innerHTML = x; document.addEventListener("click", fn) }
}`)
	f.Add("app/posts/index.templ", `<script src="/app.js"></script><link rel="stylesheet" href="/app.css">`)
	f.Add("assets/js/re.js", `const r = /foo\/"bar'/; const s = `+"`hi ${setTimeout(fn, 1)}`"+`; // setTimeout`)
	f.Fuzz(func(t *testing.T, path, text string) {
		s := &Source{Path: path, Text: text, Module: "example.com/shop"}
		report := func(offset int, _ string) {
			if offset < 0 || offset > len(text) {
				t.Fatalf("offset %d out of range [0, %d] for %q", offset, len(text), path)
			}
		}
		_ = checkForms(s, report)
		_ = checkJSTimers(s, report)
		_ = checkRawDOMHTML(s, report)
		_ = checkStimulusListeners(s, report)
		_ = checkAssetURLs(s, report)
		_ = checkComments(s, report)
	})
}
