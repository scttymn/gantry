// Package text is Rails' words for pages: inflections (Pluralize, Count),
// how long ago (TimeAgo, Distance) and how big (ByteSize), in Rails' own
// English, so a page reads as the Rails one did.
package text

import (
	"regexp"
	"strconv"
	"strings"
)

type rule struct {
	re   *regexp.Regexp
	with string
}

// Rails' inflections (ActiveSupport's inflections.rb), tried newest first
// as Rails does: the irregulars last, so they win.
var plurals, singulars []rule

var uncountable = []string{"equipment", "information", "rice", "money", "species", "series", "fish", "sheep", "jeans", "police"}

func plural(re, with string) { plurals = append(plurals, rule{regexp.MustCompile("(?i)" + re), with}) }
func singular(re, with string) {
	singulars = append(singulars, rule{regexp.MustCompile("(?i)" + re), with})
}

// irregular is Rails' irregular: the first letters kept, the rest swapped.
func irregular(one, many string) {
	s0, srest, prest := one[:1], one[1:], many[1:]
	plural("("+s0+")"+srest+"$", "${1}"+prest)
	plural("("+many[:1]+")"+prest+"$", "${1}"+prest)
	singular("("+s0+")"+srest+"$", "${1}"+srest)
	singular("("+many[:1]+")"+prest+"$", "${1}"+srest)
}

func init() {
	plural("$", "s")
	plural("s$", "s")
	plural("^(ax|test)is$", "${1}es")
	plural("(octop|vir)us$", "${1}i")
	plural("(octop|vir)i$", "${1}i")
	plural("(alias|status)$", "${1}es")
	plural("(bu)s$", "${1}ses")
	plural("(buffal|tomat)o$", "${1}oes")
	plural("([ti])um$", "${1}a")
	plural("([ti])a$", "${1}a")
	plural("sis$", "ses")
	plural("(?:([^f])fe|([lr])f)$", "${1}${2}ves")
	plural("(hive)$", "${1}s")
	plural("([^aeiouy]|qu)y$", "${1}ies")
	plural("(x|ch|ss|sh)$", "${1}es")
	plural("(matr|vert|ind)(?:ix|ex)$", "${1}ices")
	plural("^(m|l)ouse$", "${1}ice")
	plural("^(m|l)ice$", "${1}ice")
	plural("^(ox)$", "${1}en")
	plural("^(oxen)$", "${1}")
	plural("(quiz)$", "${1}zes")

	singular("s$", "")
	singular("(ss)$", "${1}")
	singular("(n)ews$", "${1}ews")
	singular("([ti])a$", "${1}um")
	singular("((a)naly|(b)a|(d)iagno|(p)arenthe|(p)rogno|(s)ynop|(t)he)(sis|ses)$", "${1}sis")
	singular("(^analy)(sis|ses)$", "${1}sis")
	singular("([^f])ves$", "${1}fe")
	singular("(hive)s$", "${1}")
	singular("(tive)s$", "${1}")
	singular("([lr])ves$", "${1}f")
	singular("([^aeiouy]|qu)ies$", "${1}y")
	singular("(s)eries$", "${1}eries")
	singular("(m)ovies$", "${1}ovie")
	singular("(x|ch|ss|sh)es$", "${1}")
	singular("^(m|l)ice$", "${1}ouse")
	singular("(bus)(es)?$", "${1}")
	singular("(o)es$", "${1}")
	singular("(shoe)s$", "${1}")
	singular("(cris|test)(is|es)$", "${1}is")
	singular("^(a)x[ie]s$", "${1}xis")
	singular("(octop|vir)(us|i)$", "${1}us")
	singular("(alias|status)(es)?$", "${1}")
	singular("^(ox)en", "${1}")
	singular("(vert|ind)ices$", "${1}ex")
	singular("(matr)ices$", "${1}ix")
	singular("(quiz)zes$", "${1}")
	singular("(database)s$", "${1}")

	irregular("person", "people")
	irregular("man", "men")
	irregular("child", "children")
	irregular("sex", "sexes")
	irregular("move", "moves")
	irregular("zombie", "zombies")
}

// Pluralize is Rails' String#pluralize: "post" is "posts", "person"
// "people", "category" "categories"; "sheep" stays "sheep".
func Pluralize(word string) string { return inflect(word, plurals) }

// Singularize is Rails' String#singularize: "posts" is "post", "people"
// "person", "addresses" "address".
func Singularize(word string) string { return inflect(word, singulars) }

func inflect(word string, rules []rule) string {
	if word == "" || isUncountable(word) {
		return word
	}
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		if loc := r.re.FindStringSubmatchIndex(word); loc != nil {
			var out []byte
			out = r.re.ExpandString(out, r.with, word, loc)
			return word[:loc[0]] + string(out) + word[loc[1]:]
		}
	}
	return word
}

// isUncountable is Rails' check: the word, or its last word, is one.
func isUncountable(word string) bool {
	lower := strings.ToLower(word)
	for _, u := range uncountable {
		if lower == u || strings.HasSuffix(lower, u) && !isWordChar(lower[len(lower)-len(u)-1]) {
			return true
		}
	}
	return false
}

func isWordChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Count is Rails' pluralize helper: "1 person", "2 people", "0 people".
func Count(n int, word string) string {
	if n != 1 {
		word = Pluralize(word)
	}
	return strconv.Itoa(n) + " " + word
}
