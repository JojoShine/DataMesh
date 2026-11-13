package workflow

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type governanceCron struct {
	fields          [5][]int
	dayAny, weekAny bool
}

func parseGovernanceCron(value string) (governanceCron, error) {
	var spec governanceCron
	parts := strings.Fields(value)
	if len(value) > 100 || len(parts) != 5 {
		return spec, invalid
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	for i, part := range parts {
		seen := map[int]bool{}
		for _, item := range strings.Split(part, ",") {
			step := 1
			pair := strings.Split(item, "/")
			if len(pair) > 2 {
				return spec, invalid
			}
			if len(pair) == 2 {
				n, e := strconv.Atoi(pair[1])
				if e != nil || n < 1 || n > bounds[i][1]-bounds[i][0]+1 {
					return spec, invalid
				}
				step = n
			}
			low, high := bounds[i][0], bounds[i][1]
			if pair[0] != "*" {
				rangeParts := strings.Split(pair[0], "-")
				if len(rangeParts) > 2 {
					return spec, invalid
				}
				var e error
				low, e = strconv.Atoi(rangeParts[0])
				if e != nil {
					return spec, invalid
				}
				high = low
				if len(rangeParts) == 2 {
					high, e = strconv.Atoi(rangeParts[1])
					if e != nil {
						return spec, invalid
					}
				} else if len(pair) == 2 {
					high = bounds[i][1]
				}
			}
			if low < bounds[i][0] || high > bounds[i][1] || low > high {
				return spec, invalid
			}
			for n := low; n <= high; n += step {
				seen[n] = true
			}
		}
		for n := bounds[i][0]; n <= bounds[i][1]; n++ {
			if seen[n] {
				spec.fields[i] = append(spec.fields[i], n)
			}
		}
		if len(spec.fields[i]) == 0 {
			return spec, invalid
		}
	}
	spec.dayAny = parts[2] == "*"
	spec.weekAny = parts[4] == "*"
	return spec, nil
}

func (spec governanceCron) dayMatches(day time.Time) bool {
	if !slices.Contains(spec.fields[3], int(day.Month())) {
		return false
	}
	dom := slices.Contains(spec.fields[2], day.Day())
	dow := slices.Contains(spec.fields[4], int(day.Weekday()))
	if spec.dayAny {
		return dow
	}
	if spec.weekAny {
		return dom
	}
	return dom || dow
}

// Pick the first real occurrence of a wall-clock minute. Nonexistent DST
// minutes are skipped; a repeated fall-back minute is dispatched only once.
func governanceWallMinute(day time.Time, hour, minute int, loc *time.Location) (time.Time, bool) {
	y, m, d := day.Date()
	wall := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
	var first time.Time
	for _, delta := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, offset := day.Add(delta).Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		if local.Year() == y && local.Month() == m && local.Day() == d && local.Hour() == hour && local.Minute() == minute && (first.IsZero() || candidate.Before(first)) {
			first = candidate
		}
	}
	return first, !first.IsZero()
}

func governanceCronTime(value, zone string, at time.Time, forward bool) (time.Time, error) {
	spec, e := parseGovernanceCron(value)
	if e != nil {
		return time.Time{}, e
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, invalid
	}
	y, m, d := at.In(loc).Date()
	day := time.Date(y, m, d, 12, 0, 0, 0, loc)
	direction := 1
	if !forward {
		direction = -1
	}
	for days := 0; days < 366*8; days++ {
		if spec.dayMatches(day) {
			var selected time.Time
			for _, hour := range spec.fields[1] {
				for _, minute := range spec.fields[0] {
					candidate, ok := governanceWallMinute(day, hour, minute, loc)
					if !ok {
						continue
					}
					if forward && candidate.After(at) && (selected.IsZero() || candidate.Before(selected)) {
						selected = candidate
					}
					if !forward && !candidate.After(at) && (selected.IsZero() || candidate.After(selected)) {
						selected = candidate
					}
				}
			}
			if !selected.IsZero() {
				return selected.UTC(), nil
			}
		}
		day = day.AddDate(0, 0, direction)
	}
	return time.Time{}, problem(400, "GOVERNANCE_CRON_UNREACHABLE", "日历规则在未来八年内没有有效触发时刻")
}

func (s *Server) governanceSchedulePreview(c *gin.Context) {
	zone, value := c.Query("timezone"), c.Query("cron")
	if zone == "Local" || (!strings.Contains(zone, "/") && zone != "UTC") {
		s.fail(c, invalid)
		return
	}
	at, e := time.Parse(time.RFC3339, c.Query("after"))
	if e != nil {
		s.fail(c, invalid)
		return
	}
	interval := 0
	if c.Query("interval_seconds") != "" {
		interval, e = strconv.Atoi(c.Query("interval_seconds"))
		if e != nil || interval < 60 || interval > 31536000 || value != "" {
			s.fail(c, invalid)
			return
		}
	}
	if _, e = time.LoadLocation(zone); e != nil {
		s.fail(c, invalid)
		return
	}
	items := []time.Time{}
	for i := 0; i < 5; i++ {
		if interval > 0 {
			at = at.Add(time.Duration(interval) * time.Second)
		} else {
			at, e = governanceCronTime(value, zone, at, true)
		}
		if e != nil {
			s.fail(c, e)
			return
		}
		items = append(items, at)
	}
	c.JSON(200, gin.H{"items": items, "timezone": zone, "dst_policy": "skip_nonexistent_first_repeated"})
}
