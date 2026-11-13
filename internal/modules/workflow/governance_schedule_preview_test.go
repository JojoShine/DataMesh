package workflow

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGovernanceScheduleIntervalPreview(t *testing.T) {
	router := gin.New()
	router.GET("/preview", (&Server{}).governanceSchedulePreview)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/preview?timezone=Asia%2FShanghai&interval_seconds=3600&after=2026-10-28T01:29:59Z", nil))
	if w.Code != 200 {
		t.Fatalf("interval preview: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []time.Time `json:"items"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if len(got.Items) != 5 || got.Items[0].Format(time.RFC3339) != "2026-10-28T02:29:59Z" || got.Items[4].Sub(got.Items[0]) != 4*time.Hour {
		t.Fatalf("preview=%s", w.Body.String())
	}
}
