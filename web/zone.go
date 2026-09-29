package web

import (
	"net/http"
	"time"
)

var zoneKey = NewKey[*time.Location]("time zone")

// Zone is the request's time zone (Rails' Time.zone): what SetZone gave it,
// else the router's TimeZone, else UTC.
func Zone(r *http.Request) *time.Location {
	if z, ok := Get(r, zoneKey); ok && z != nil {
		return z
	}
	return time.UTC
}

// SetZone is the request's time zone from here on (Rails' Time.use_zone):
// a filter's, from the signed-in user's setting, say.
func SetZone(r *http.Request, loc *time.Location) { Set(r, zoneKey, loc) }

// Local is t in the request's time zone, for showing.
func Local(r *http.Request, t time.Time) time.Time { return t.In(Zone(r)) }
