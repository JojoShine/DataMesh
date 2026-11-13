package workflow

import (
	"testing"
	"time"
)

func TestGovernanceCalendar(t *testing.T) {
	for _, tc := range []struct{ cron, zone, after, want string }{
		{"0 9 * * 1-5", "Asia/Shanghai", "2026-10-23T01:00:00Z", "2026-10-26T01:00:00Z"},
		{"30 2 * * *", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "America/New_York", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		{"0 0 29 2 *", "UTC", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"*/15 9-17 * * 1,3,5", "UTC", "2026-10-23T16:59:00Z", "2026-10-23T17:00:00Z"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.after)
		next, e := governanceCronTime(tc.cron, tc.zone, at, true)
		if e != nil || next.Format(time.RFC3339) != tc.want {
			t.Fatalf("%+v got %v %v", tc, next, e)
		}
		previous, e := governanceCronTime(tc.cron, tc.zone, next, false)
		if e != nil || !previous.Equal(next) {
			t.Fatalf("reverse lookup failed %v %v", previous, e)
		}
	}
	for _, value := range []string{"* * * *", "* * * * * *", "60 * * * *", "*/0 * * * *", "0 0 30 2 *", "0 24 * * *", "0 0 * * MON"} {
		if _, e := governanceCronTime(value, "UTC", time.Now(), true); e == nil {
			t.Fatalf("accepted invalid cron %q", value)
		}
	}
	at, _ := time.Parse(time.RFC3339, "2026-10-22T01:00:00Z")
	row := GovernanceSchedule{Cron: "0 9 * * *", Timezone: "Asia/Shanghai", NextRunAt: at, MissedPolicy: "skip"}
	fire, next, due := governanceScheduleDue(row, at.Add(48*time.Hour+time.Minute))
	if due || !fire.Equal(at.Add(48*time.Hour)) || !next.Equal(at.Add(72*time.Hour)) {
		t.Fatal("missed calendar policy failed")
	}
	row.MissedPolicy = "latest"
	_, _, due = governanceScheduleDue(row, at.Add(48*time.Hour+time.Minute))
	if !due {
		t.Fatal("latest calendar catch-up missing")
	}
}
