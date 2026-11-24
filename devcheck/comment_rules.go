package devcheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"strings"
)

const gantryModule = "github.com/scttymn/gantry"

const (
	commentIntent      = "Express the intent in names, types, or a test; if this explains a workaround, fix the root cause instead."
	commentInBody      = "Comment inside function body. " + commentIntent
	commentTrailing    = "Trailing comment. " + commentIntent
	commentOnNoName    = "Comment that documents no exported name. Framework code has doc comments on exported names and the package clause only. " + commentIntent
	commentInApp       = "Comments aren't allowed in Gantry apps. Put the intent in a name, type, or test; if this explains a workaround, fix the root cause instead."
	commentOnTest      = "Doc comment in a test. Tests aren't documented API; say what the test checks in its name or its failure message."
	commentedOutCode   = "Commented-out code. Delete it; git keeps the old version."
	nolintWithoutWhy   = "nolint directive without a linter and a reason. Write //nolint:<linter> // <reason>, or fix what the linter reports."
	commentGuidance    = "a comment outside a Go doc comment on an exported framework name or package clause, or any comment in an app or a generator's template; put the intent in a name, type or test, and fix a workaround's root cause instead of explaining it. Directives stay: //go:, //nolint:<linter> // <reason>, sqlc's -- name: and goose's -- +goose lines"
	commentRulePurpose = "Keep intent in names, types and tests, which the compiler and CI check, not in comments, which go stale and give a workaround a place to explain itself instead of being fixed"
)

func unexportedDoc(name string) string {
	return "Doc comment on unexported name " + name + ". Unexported code is read with its callers; rename it or extract a type so it explains itself."
}

func planMarker(marker string) string {
	return marker + " marker. Open an issue and link it from the PR; code doesn't carry plans."
}

var (
	planMarkers      = regexp.MustCompile(`\b(TODO|FIXME|HACK|XXX)\b`)
	goDirective      = regexp.MustCompile(`^//go:[a-z]`)
	nolintDirective  = regexp.MustCompile(`^//\s*nolint\b`)
	nolintWithReason = regexp.MustCompile(`^//nolint:[a-z0-9-]+(,[a-z0-9-]+)* // \S`)
	sqlDirective     = regexp.MustCompile(`^-- (name: \S+ :(one|many|exec|execrows|execresult|execlastid|copyfrom|batchexec|batchmany|batchone)|\+goose (Up|Down|StatementBegin|StatementEnd))\s*$`)
	goModDirective   = regexp.MustCompile(`^// indirect\s*$`)
)

type commentPolicy int

const (
	commentsUnchecked commentPolicy = iota
	exportedDocsOnly
	directivesOnly
)

func commentPolicyFor(s *Source) (commentPolicy, string) {
	if !within(s.Module, gantryModule) {
		if strings.HasPrefix(s.Path, "assets/js/vendor/") {
			return commentsUnchecked, ""
		}
		return directivesOnly, s.Path
	}
	inGantry := path.Join(strings.TrimPrefix(strings.TrimPrefix(s.Module, gantryModule), "/"), s.Path)
	switch {
	case strings.HasPrefix(inGantry, "cmd/gantry/templates/"):
		if written, ok := strings.CutSuffix(s.Path, ".tmpl"); ok {
			return directivesOnly, written
		}
		return commentsUnchecked, ""
	case strings.HasPrefix(inGantry, "bench/"), path.Ext(s.Path) != ".go":
		return commentsUnchecked, ""
	}
	return exportedDocsOnly, s.Path
}

func checkComments(s *Source, report func(int, string)) error {
	policy, written := commentPolicyFor(s)
	if policy == commentsUnchecked {
		return nil
	}
	if path.Ext(s.Path) == ".go" {
		return checkGoComments(s, policy, report)
	}
	syntax, ok := commentSyntaxes[path.Base(written)]
	if !ok {
		syntax, ok = commentSyntaxes[path.Ext(written)]
	}
	if ok {
		syntax.scan(s.Text, strings.HasSuffix(s.Path, ".tmpl"), report)
	}
	return nil
}

type documentedName struct {
	name     string
	exported bool
}

func checkGoComments(s *Source, policy commentPolicy, report func(int, string)) error {
	set, file, err := s.GoAST()
	if err != nil {
		return err
	}
	if ast.IsGenerated(file) {
		return nil
	}
	offset := func(p token.Pos) int { return set.Position(p).Offset }
	docs, cgoPreamble := docComments(file)
	var bodies []*ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		switch f := n.(type) {
		case *ast.FuncDecl:
			if f.Body != nil {
				bodies = append(bodies, f.Body)
			}
		case *ast.FuncLit:
			bodies = append(bodies, f.Body)
		}
		return true
	})
	inBody := func(g *ast.CommentGroup) bool {
		for _, b := range bodies {
			if g.Pos() > b.Lbrace && g.End() <= b.Rbrace {
				return true
			}
		}
		return false
	}
	test := strings.HasSuffix(s.Path, "_test.go")
	for _, group := range file.Comments {
		if group == cgoPreamble {
			continue
		}
		var prose []*ast.Comment
		for _, c := range group.List {
			switch {
			case goDirective.MatchString(c.Text), nolintWithReason.MatchString(c.Text):
			case nolintDirective.MatchString(c.Text):
				report(offset(c.Pos()), nolintWithoutWhy)
			default:
				prose = append(prose, c)
			}
		}
		if len(prose) == 0 {
			continue
		}
		at := offset(prose[0].Pos())
		if c, marker := markedComment(prose); c != nil {
			report(offset(c.Pos()), planMarker(marker))
			continue
		}
		doc, documents := docs[group]
		lineStart := strings.LastIndexByte(s.Text[:at], '\n') + 1
		switch {
		case isCode(prose):
			report(at, commentedOutCode)
		case policy == directivesOnly:
			report(at, commentInApp)
		case inBody(group):
			report(at, commentInBody)
		case strings.TrimSpace(s.Text[lineStart:at]) != "":
			report(at, commentTrailing)
		case documents && test:
			report(at, commentOnTest)
		case documents && !doc.exported:
			report(at, unexportedDoc(doc.name))
		case !documents:
			report(at, commentOnNoName)
		}
	}
	return nil
}

func docComments(file *ast.File) (map[*ast.CommentGroup]documentedName, *ast.CommentGroup) {
	docs := map[*ast.CommentGroup]documentedName{}
	var cgoPreamble *ast.CommentGroup
	add := func(doc *ast.CommentGroup, names ...*ast.Ident) {
		if doc == nil || len(names) == 0 {
			return
		}
		for _, n := range names {
			if n.IsExported() {
				docs[doc] = documentedName{n.Name, true}
				return
			}
		}
		docs[doc] = documentedName{names[0].Name, false}
	}
	if file.Doc != nil {
		docs[file.Doc] = documentedName{"package " + file.Name.Name, true}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BlockStmt:
			return false
		case *ast.FuncDecl:
			add(n.Doc, n.Name)
		case *ast.GenDecl:
			var names []*ast.Ident
			for _, spec := range n.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					names = append(names, spec.Name)
				case *ast.ValueSpec:
					names = append(names, spec.Names...)
				case *ast.ImportSpec:
					if spec.Path.Value == `"C"` {
						cgoPreamble = n.Doc
						if spec.Doc != nil {
							cgoPreamble = spec.Doc
						}
					}
				}
			}
			add(n.Doc, names...)
		case *ast.TypeSpec:
			add(n.Doc, n.Name)
		case *ast.ValueSpec:
			add(n.Doc, n.Names...)
		case *ast.Field:
			if len(n.Names) > 0 {
				add(n.Doc, n.Names...)
			} else if name := embeddedName(n.Type); name != nil {
				add(n.Doc, name)
			}
		}
		return true
	})
	return docs, cgoPreamble
}

func embeddedName(t ast.Expr) *ast.Ident {
	switch t := t.(type) {
	case *ast.Ident:
		return t
	case *ast.StarExpr:
		return embeddedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel
	case *ast.IndexExpr:
		return embeddedName(t.X)
	case *ast.IndexListExpr:
		return embeddedName(t.X)
	}
	return nil
}

func markedComment(comments []*ast.Comment) (*ast.Comment, string) {
	for _, c := range comments {
		if m := planMarkers.FindString(c.Text); m != "" {
			return c, m
		}
	}
	return nil, ""
}

func isCode(comments []*ast.Comment) bool {
	var b strings.Builder
	for _, c := range comments {
		text, ok := strings.CutPrefix(c.Text, "//")
		if !ok {
			text = strings.TrimSuffix(strings.TrimPrefix(c.Text, "/*"), "*/")
		}
		b.WriteString(text + "\n")
	}
	code := b.String()
	if !strings.ContainsAny(code, "(){}=") {
		return false
	}
	if f, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+code, parser.SkipObjectResolution); err == nil && len(f.Decls) > 0 {
		return true
	}
	_, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _() {\n"+code+"\n}", parser.SkipObjectResolution)
	return err == nil
}

type delimiters struct{ open, close string }

type commentSyntax struct {
	untilLineEnd    []string
	blocks          []delimiters
	onlyAtLineStart bool
	shebang         bool
	directive       *regexp.Regexp
}

var (
	slashComments   = commentSyntax{untilLineEnd: []string{"//"}, blocks: []delimiters{{"/*", "*/"}}, directive: goDirective}
	hashComments    = commentSyntax{untilLineEnd: []string{"#"}}
	commentSyntaxes = map[string]commentSyntax{
		".go":           slashComments,
		".js":           slashComments,
		".mod":          {untilLineEnd: []string{"//"}, directive: goModDirective},
		".templ":        {untilLineEnd: []string{"//"}, blocks: []delimiters{{"/*", "*/"}, {"<!--", "-->"}}, directive: goDirective},
		".css":          {blocks: []delimiters{{"/*", "*/"}}},
		".html":         {blocks: []delimiters{{"<!--", "-->"}}},
		".sql":          {untilLineEnd: []string{"--"}, blocks: []delimiters{{"/*", "*/"}}, directive: sqlDirective},
		".yml":          hashComments,
		".yaml":         hashComments,
		".txt":          hashComments,
		".sh":           {untilLineEnd: []string{"#"}, shebang: true},
		"gantry":        {untilLineEnd: []string{"#"}, shebang: true},
		"Dockerfile":    {untilLineEnd: []string{"#"}, onlyAtLineStart: true},
		"dockerignore":  {untilLineEnd: []string{"#"}, onlyAtLineStart: true},
		"gitignore":     {untilLineEnd: []string{"#"}, onlyAtLineStart: true},
		".dockerignore": {untilLineEnd: []string{"#"}, onlyAtLineStart: true},
		".gitignore":    {untilLineEnd: []string{"#"}, onlyAtLineStart: true},
	}
	templateActionComment = regexp.MustCompile(`\{\{-?\s*/\*`)
)

func (c commentSyntax) scan(text string, template bool, report func(int, string)) {
	groupEndLine := -2
	for pos := 0; pos < len(text); {
		start, end := c.next(text, pos, template)
		if start < 0 {
			return
		}
		pos = end
		comment := text[start:end]
		if c.shebang && start == 0 && strings.HasPrefix(comment, "#!") || c.directive != nil && c.directive.MatchString(comment) {
			continue
		}
		lineStart := strings.LastIndexByte(text[:start], '\n') + 1
		line := strings.Count(text[:start], "\n")
		wholeLine := strings.TrimSpace(text[lineStart:start]) == ""
		if !wholeLine || line != groupEndLine+1 {
			report(start, commentInApp)
		}
		groupEndLine = strings.Count(text[:end], "\n")
		if !wholeLine {
			groupEndLine = -2
		}
	}
}

func (c commentSyntax) next(text string, from int, template bool) (int, int) {
	start, end := -1, -1
	consider := func(at, until int) {
		if at >= 0 && (start < 0 || at < start) {
			start, end = at, until
		}
	}
	for _, marker := range c.untilLineEnd {
		if at := c.markerAt(text, from, marker); at >= 0 {
			lineEnd := strings.IndexByte(text[at:], '\n')
			if lineEnd < 0 {
				lineEnd = len(text) - at
			}
			consider(at, at+lineEnd)
		}
	}
	for _, d := range c.blocks {
		at := c.markerAt(text, from, d.open)
		if d.open == "<!--" {
			if at = strings.Index(text[from:], d.open); at >= 0 {
				at += from
			}
		}
		if at >= 0 {
			consider(at, closing(text, at+len(d.open), d.close))
		}
	}
	if template {
		if m := templateActionComment.FindStringIndex(text[from:]); m != nil {
			consider(from+m[0], closing(text, from+m[1], "}}"))
		}
	}
	return start, end
}

func closing(text string, from int, close string) int {
	if i := strings.Index(text[from:], close); i >= 0 {
		return from + i + len(close)
	}
	return len(text)
}

func (c commentSyntax) markerAt(text string, from int, marker string) int {
	for at := from; ; at++ {
		i := strings.Index(text[at:], marker)
		if i < 0 {
			return -1
		}
		at += i
		lineStart := strings.LastIndexByte(text[:at], '\n') + 1
		before := text[lineStart:at]
		if strings.TrimSpace(before) == "" || !c.onlyAtLineStart && (before[len(before)-1] == ' ' || before[len(before)-1] == '\t') {
			return at
		}
	}
}
