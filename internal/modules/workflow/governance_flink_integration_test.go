package workflow

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

func TestFlinkGovernanceActualEngine(t *testing.T) {
	if os.Getenv("DATAMESH_FLINK_TEST") != "1" {
		t.Skip("requires dedicated Kafka/Flink/Doris runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	id, e := newID()
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{runtime: &Runtime{client: &http.Client{Timeout: 15 * time.Second}}}
	fields := []Field{{Name: "id", LogicalType: "integer"}, {Name: "name", LogicalType: "string", Nullable: true}}
	graph := GovernanceDataflow{Nodes: []GovernanceFlowNode{
		{ID: "source", Kind: "source", Source: &GovernanceFlowSource{Resource: Resource{Type: "raw_object", ID: "fixture", Version: 1}, Fields: []string{"id", "name"}}},
		{ID: "clean", Kind: "transform", Inputs: []GovernanceFlowEdge{{Node: "source"}}, Columns: []GovernanceFlowColumn{{Source: "id", Target: "id"}, {Source: "name", Target: "name", Rule: "trim"}}},
		{ID: "quality", Kind: "quality", Inputs: []GovernanceFlowEdge{{Node: "clean"}}, Checks: []Check{{Kind: "required", Field: "name", Blocking: true}}},
		{ID: "output", Kind: "output", InferModel: true, Inputs: []GovernanceFlowEdge{{Node: "quality", Port: "good"}}},
	}}
	def := governanceFlowExecution{Flow: GovernanceRef{ID: "fixture", Version: 1}, Pinned: []governanceFlowPinned{{Kind: "dataflow", Ref: GovernanceRef{ID: "fixture", Version: 1}, Definition: GovernanceDefinition{Dataflow: &graph}}}, Inputs: map[string]governanceInput{"source": {Resource: Resource{Type: "raw_object", ID: "fixture", Version: 1}, Fields: fields}}}
	row := GovernanceStream{ID: id, Generation: 1}
	sources := map[string]governanceStreamSource{"source": {Fields: fields}}
	if e = s.streamPrepare(ctx, row, def, sources); e != nil {
		t.Fatal(e)
	}
	f := s.flink()
	name := streamJobName(id, 1)
	session, e := f.session(ctx, name, "")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		defer func() {
			if session != "" {
				if err := f.closeSession(context.Background(), session); err != nil {
					t.Error(err)
				}
			}
		}()
		jobs, err := f.jobs(context.Background(), name)
		if err != nil {
			t.Error(err)
			return
		}
		for _, job := range jobs {
			if job.State == "FINISHED" || job.State == "CANCELED" || job.State == "FAILED" {
				continue
			}
			if err = f.cancel(context.Background(), job.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	if e = f.statement(ctx, session, "ADD JAR 'file:///opt/flink/usrlib/flink-sql-connector-kafka-3.4.0-1.20.jar'"); e != nil {
		t.Fatal(e)
	}
	compiled, e := compileGovernanceFlink(def, map[string]string{"source": streamTopic(id, "source")}, streamOutputTopic(id), name)
	if e != nil {
		t.Fatal(e)
	}
	for i, query := range compiled.Setup {
		if e = f.statement(ctx, session, query); e != nil {
			t.Fatalf("setup %d: %v", i, e)
		}
	}
	op, e := f.submit(ctx, session, compiled.Insert)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.wait(ctx, session, op); e != nil {
		var result map[string]interface{}
		_ = f.call(ctx, false, "GET", "/v1/sessions/"+session+"/operations/"+op+"/result/0", nil, &result)
		t.Fatalf("insert: %v; diagnostic=%v", e, result)
	}
	writer := &kafka.Writer{Addr: kafka.TCP("kafka:9092"), Topic: streamTopic(id, "source"), RequiredAcks: kafka.RequireAll}
	defer writer.Close()
	send := func(seq int64, deleted bool, name string) {
		t.Helper()
		var value interface{} = name
		if name == "" {
			value = nil
		}
		if e := writer.WriteMessages(ctx, kafka.Message{Key: []byte("one"), Value: encode(map[string]interface{}{"_dm_key": "one", "_dm_seq": seq, "_dm_deleted": deleted, "id": "1", "name": value})}); e != nil {
			t.Fatal(e)
		}
	}
	await := func(want string, count int) {
		t.Helper()
		for tries := 0; tries < 35; tries++ {
			if _, err := s.streamMaterialize(ctx, &row, def, false); err != nil {
				t.Fatal(err)
			}
			rows, err := s.streamLivePage(ctx, row, def, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == count && (count == 0 || flowLiteralText(rows[0]["name"]) == want) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		t.Fatalf("result did not reach %q / %d rows", want, count)
	}
	send(1, false, " A ")
	await("A", 1)
	send(2, false, " B ")
	await("B", 1)
	send(1, false, " A ")
	await("B", 1)
	send(3, false, "")
	await("", 0)
	if row.RejectedRows != 1 {
		t.Fatalf("quality count=%d", row.RejectedRows)
	}
	send(4, false, "C")
	await("C", 1)
	jobs, e := f.jobs(ctx, name)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("jobs=%v err=%v", jobs, e)
	}
	trigger, e := newID()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.stop(ctx, jobs[0].ID, trigger); e != nil {
		t.Fatal(e)
	}
	var savepoint string
	for savepoint == "" {
		location, done, err := f.savepoint(ctx, jobs[0].ID, trigger)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			savepoint = location
		} else {
			time.Sleep(time.Second)
		}
	}
	for {
		job, err := f.job(ctx, jobs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == "FINISHED" {
			break
		}
		time.Sleep(time.Second)
	}
	for {
		done, err := s.streamMaterialize(ctx, &row, def, true)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		time.Sleep(time.Second)
	}
	if !row.CaughtUp {
		t.Fatal("pause did not reach committed boundary")
	}
	if e = f.call(ctx, false, "DELETE", "/v1/sessions/"+session, nil, nil); e != nil {
		t.Fatal(e)
	}
	name = streamJobName(id, 2)
	session, e = f.session(ctx, name, savepoint)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.statement(ctx, session, "ADD JAR 'file:///opt/flink/usrlib/flink-sql-connector-kafka-3.4.0-1.20.jar'"); e != nil {
		t.Fatal(e)
	}
	compiled, e = compileGovernanceFlink(def, map[string]string{"source": streamTopic(id, "source")}, streamOutputTopic(id), name)
	if e != nil {
		t.Fatal(e)
	}
	for _, query := range compiled.Setup {
		if e = f.statement(ctx, session, query); e != nil {
			t.Fatal(e)
		}
	}
	op, e = f.submit(ctx, session, compiled.Insert)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.wait(ctx, session, op); e != nil {
		t.Fatal(e)
	}
	send(5, false, "D")
	await("D", 1)
	send(6, true, "")
	await("", 0)
}
