package text

import (
	"math"
	"strconv"
	"time"
)

// TimeAgo is Rails' time_ago_in_words: how long before now t was, "less
// than a minute", "about 1 hour", "3 days", "over 2 years".
func TimeAgo(t, now time.Time) string { return Distance(t, now) }

// Distance is Rails' distance_of_time_in_words between two times, either
// way round, without seconds.
func Distance(from, to time.Time) string {
	if from.After(to) {
		from, to = to, from
	}
	secs := to.Sub(from).Seconds()
	minutes := int(math.Round(secs / 60))
	count := func(unit float64) int { return int(math.Round(float64(minutes) / unit)) }
	switch {
	case minutes == 0:
		return "less than a minute"
	case minutes == 1:
		return "1 minute"
	case minutes < 45:
		return Count(minutes, "minute")
	case minutes < 90:
		return "about 1 hour"
	case minutes < 1440:
		return "about " + Count(count(60), "hour")
	case minutes < 2520:
		return "1 day"
	case minutes < 43200:
		return Count(count(1440), "day")
	case minutes < 86400:
		return "about " + Count(count(43200), "month")
	case minutes < 525600:
		return Count(count(43200), "month")
	}
	// Years, as Rails counts them: a leap day between is taken off first.
	fromYear, toYear := from.Year(), to.Year()
	if from.Month() >= time.March {
		fromYear++
	}
	if to.Month() < time.March {
		toYear--
	}
	leaps := 0
	for y := fromYear; y <= toYear; y++ {
		if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
			leaps++
		}
	}
	m := minutes - leaps*1440
	years, rest := m/525600, m%525600
	switch {
	case rest < 131400:
		return "about " + Count(years, "year")
	case rest < 394200:
		return "over " + Count(years, "year")
	}
	return "almost " + Count(years+1, "year")
}

var sizeUnits = []string{"KB", "MB", "GB", "TB", "PB", "EB", "ZB"}

// ByteSize is Rails' number_to_human_size: 1024-based, three significant
// digits, insignificant zeros dropped: "1 Byte", "123 Bytes", "1.21 KB",
// "118 MB".
func ByteSize(n int64) string {
	if n < 1024 && n > -1024 {
		if n == 1 || n == -1 {
			return strconv.FormatInt(n, 10) + " Byte"
		}
		return strconv.FormatInt(n, 10) + " Bytes"
	}
	x := math.Abs(float64(n))
	exp := min(int(math.Log(x)/math.Log(1024)), len(sizeUnits))
	x /= math.Pow(1024, float64(exp))
	// Three significant digits, half up, as Rails' rounding.
	digits := int(math.Floor(math.Log10(x))) + 1
	scale := math.Pow(10, float64(3-digits))
	x = math.Round(x*scale) / scale
	s := strconv.FormatFloat(x, 'f', -1, 64)
	if n < 0 {
		s = "-" + s
	}
	return s + " " + sizeUnits[exp-1]
}
