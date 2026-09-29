package text

import (
	"testing"
	"time"
)

// Rails' answers, from ActiveSupport's own tests and a Rails console.
func TestInflections(t *testing.T) {
	for one, many := range map[string]string{
		"post": "posts", "category": "categories", "address": "addresses", "box": "boxes",
		"person": "people", "Person": "People", "salesperson": "salespeople", "man": "men", "woman": "women",
		"child": "children", "sheep": "sheep", "fish": "fish", "equipment": "equipment", "status": "statuses",
		"alias": "aliases", "bus": "buses", "tomato": "tomatoes", "medium": "media", "analysis": "analyses",
		"wife": "wives", "half": "halves", "hive": "hives", "query": "queries", "matrix": "matrices",
		"vertex": "vertices", "mouse": "mice", "ox": "oxen", "quiz": "quizzes", "octopus": "octopi",
		"axis": "axes", "news": "news", "movie": "movies", "shoe": "shoes", "zombie": "zombies",
		"series": "series", "species": "species", "day": "days", "switch": "switches", "wish": "wishes",
		"membership_option": "membership_options", "user_status": "user_statuses", "goldfish": "goldfishes",
		"move": "moves", "database": "databases", "pillar": "pillars", "faq": "faqs",
	} {
		if got := Pluralize(one); got != many {
			t.Errorf("Pluralize(%q) = %q, want %q", one, got, many)
		}
		if one == "goldfish" {
			continue // Rails: "goldfishes" goes back to "goldfish" only by luck of the rules
		}
		if got := Singularize(many); got != one {
			t.Errorf("Singularize(%q) = %q, want %q", many, got, one)
		}
	}
	for _, word := range []string{"", "gold fish", "people", "staff"} {
		if got := Pluralize(word); word == "people" && got != "people" || word == "gold fish" && got != "gold fish" || word == "" && got != "" {
			t.Errorf("Pluralize(%q) = %q", word, got)
		}
	}
	// A rule's match is replaced, and the rest kept: Rails' sub.
	if got := Singularize("oxenford"); got != "oxford" {
		t.Errorf("Singularize(oxenford) = %q", got)
	}
	if got := Singularize("staff"); got != "staff" {
		t.Errorf("Singularize(staff) = %q", got)
	}
	for n, want := range map[int]string{0: "0 people", 1: "1 person", 2: "2 people", -1: "-1 people"} {
		if got := Count(n, "person"); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDistance(t *testing.T) {
	base := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "less than a minute"},
		{29 * time.Second, "less than a minute"},
		{30 * time.Second, "1 minute"},
		{89 * time.Second, "1 minute"},
		{90 * time.Second, "2 minutes"},
		{44 * time.Minute, "44 minutes"},
		{44*time.Minute + 30*time.Second, "about 1 hour"},
		{89 * time.Minute, "about 1 hour"},
		{90 * time.Minute, "about 2 hours"},
		{23*time.Hour + 59*time.Minute, "about 24 hours"},
		{24 * time.Hour, "1 day"},
		{41*time.Hour + 59*time.Minute, "1 day"},
		{42 * time.Hour, "2 days"},
		{29*24*time.Hour + 23*time.Hour, "30 days"},
		{30 * 24 * time.Hour, "about 1 month"},
		{59 * 24 * time.Hour, "about 2 months"},
		{60 * 24 * time.Hour, "2 months"},
		{364 * 24 * time.Hour, "12 months"},
		{365 * 24 * time.Hour, "about 1 year"},
		{(365 + 91) * 24 * time.Hour, "about 1 year"},
		{(365 + 92) * 24 * time.Hour, "over 1 year"},
		{(365 + 274) * 24 * time.Hour, "almost 2 years"},
		{(10*365 + 2) * 24 * time.Hour, "about 10 years"},
	} {
		if got := Distance(base, base.Add(c.d)); got != c.want {
			t.Errorf("%v: %q, want %q", c.d, got, c.want)
		}
		if got := Distance(base.Add(c.d), base); got != c.want {
			t.Errorf("%v backwards: %q, want %q", c.d, got, c.want)
		}
	}
	// A leap day between doesn't make a year more than a year.
	leap := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := TimeAgo(leap, leap.AddDate(1, 0, 0)); got != "about 1 year" {
		t.Errorf("across a leap day: %q", got)
	}
	if got := TimeAgo(leap, leap.AddDate(0, 0, 639)); got != "over 1 year" {
		t.Errorf("639 days from a leap year's January: %q (without the leap day, it'd be almost 2)", got)
	}
	// From March on, that year's leap day is behind.
	march := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	if got := TimeAgo(march, march.AddDate(0, 0, 639)); got != "almost 2 years" {
		t.Errorf("639 days from a leap year's March: %q", got)
	}
}

func TestByteSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 Bytes", 1: "1 Byte", 123: "123 Bytes", 1023: "1023 Bytes", 1024: "1 KB", 1234: "1.21 KB",
		1536: "1.5 KB", 12345: "12.1 KB", 1048575: "1020 KB", 1048576: "1 MB", 1234567: "1.18 MB",
		12345678: "11.8 MB", 123456789: "118 MB", 1234567890: "1.15 GB", 1 << 40: "1 TB",
		1 << 50: "1 PB", 1 << 60: "1 EB", -1234: "-1.21 KB", -1: "-1 Byte",
	} {
		if got := ByteSize(n); got != want {
			t.Errorf("ByteSize(%d) = %q, want %q", n, got, want)
		}
	}
}
