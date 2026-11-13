package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"datamesh/internal/modules/workflow"
)

func TestListPageSizeSelection(t *testing.T) {
	db, service, router := fixture(t)
	owner, name := businessUser(t, service, "pagination_", "pagination", []string{"provider"})
	cookie, _ := login(t, router, name)
	for i := 0; i < 127; i++ {
		body := workflow.ManagedResource{
			Name:   fmt.Sprintf("查询服务%03d", i),
			Config: jsonBody(t, workflow.APIConfig{DataService: &workflow.DataServiceQuery{Mode: "snapshot", SQL: "SELECT id FROM result", MaxPageSize: 100}}),
		}
		row := workflow.Record{Kind: "api_resource", ID: fmt.Sprintf("%s-%03d", owner, i), OwnerID: owner, DepartmentID: "pagination", Version: 1, State: "ready", Body: jsonBody(t, body), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{15, 20, 25, 50, 100} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			for _, page := range []int{1, 2} {
				path := fmt.Sprintf("/api/v1/data-services?page=%d&page_size=%d", page, size)
				w := workflowRequest(router, "GET", path, nil, cookie, "", "")
				if w.Code != 200 {
					t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
				}
				var out struct {
					Items []struct {
						ID string `json:"id"`
					} `json:"items"`
					Total    int `json:"total"`
					PageSize int `json:"page_size"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				want := min(size, 127-(page-1)*size)
				if len(out.Items) != want || out.Total != 127 || out.PageSize != size {
					t.Fatalf("incorrect page: rows=%d total=%d size=%d", len(out.Items), out.Total, out.PageSize)
				}
				if out.Items[0].ID != fmt.Sprintf("%s-%03d", owner, 126-(page-1)*size) {
					t.Fatal("page offset does not follow selected size")
				}
			}
		})
	}
	for _, size := range []string{"0", "101", "-1", "invalid"} {
		w := workflowRequest(router, "GET", "/api/v1/data-services?page_size="+size, nil, cookie, "", "")
		if w.Code != 400 {
			t.Fatalf("invalid size %s returned %d", size, w.Code)
		}
	}
	w := workflowRequest(router, "GET", "/api/v1/data-services?page_size=100", nil, nil, "", "")
	if w.Code != 401 {
		t.Fatalf("anonymous list returned %d", w.Code)
	}
}
