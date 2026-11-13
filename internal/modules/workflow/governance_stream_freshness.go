package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/segmentio/kafka-go"
)

type GovernanceStreamHealth struct {
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
}
type GovernanceStreamSourceFreshness struct {
	GovernanceStreamHealth
	NodeID         string     `json:"node_id"`
	LatestEventAt  *time.Time `json:"latest_event_at"`
	ForwardedToAt  *time.Time `json:"forwarded_to_at"`
	BacklogOffsets *int64     `json:"backlog_offsets"`
}
type GovernanceStreamFreshness struct {
	SourceCapture     GovernanceStreamHealth            `json:"source_capture"`
	FlinkCompute      GovernanceStreamHealth            `json:"flink_compute"`
	DorisWrite        GovernanceStreamHealth            `json:"doris_write"`
	Sources           []GovernanceStreamSourceFreshness `json:"sources"`
	ProcessedToAt     *time.Time                        `json:"processed_to_at"`
	EndToEndLatencyMS *int64                            `json:"end_to_end_latency_ms"`
	DorisOutputOffset *int64                            `json:"doris_output_offset"`
}

func emptyStreamFreshness() *GovernanceStreamFreshness {
	return &GovernanceStreamFreshness{SourceCapture: GovernanceStreamHealth{State: "unknown"}, FlinkCompute: GovernanceStreamHealth{State: "unknown"}, DorisWrite: GovernanceStreamHealth{State: "unknown"}, Sources: []GovernanceStreamSourceFreshness{}}
}
func streamInt64(value int64) *int64 { return &value }
func streamForwardingBacklog(r CDCPartitionRange, cursor int64) *int64 {
	if cursor < r.Start || cursor > r.End {
		return nil
	}
	return streamInt64(r.End - cursor)
}
func streamSourceEventTime(raw []byte) *time.Time {
	event, err := decodeCaptureEvent(raw)
	if err != nil || (event.Op != "c" && event.Op != "u" && event.Op != "d") {
		return nil
	}
	var source struct {
		Timestamp int64 `json:"ts_ms"`
	}
	if json.Unmarshal(event.Source, &source) != nil || source.Timestamp <= 0 {
		return nil
	}
	stamp := time.UnixMilli(source.Timestamp).UTC()
	if stamp.Year() > 9999 {
		return nil
	}
	return &stamp
}
func streamEventTimeAt(ctx context.Context, r CDCPartitionRange, offset int64) *time.Time {
	if offset < r.Start || offset >= r.End {
		return nil
	}
	r.Start = offset
	r.End = offset + 1
	var stamp *time.Time
	_, err := cdcReadRange(ctx, r, 1, func(msg kafka.Message) error { stamp = streamSourceEventTime(msg.Value); return nil })
	if err != nil {
		return nil
	}
	return stamp
}

// Freshness probes are read-only and best effort. They never advance a source or
// result cursor; failure of an observation must not prevent controlling a stream.
func (s *Server) observeStreamFreshness(parent context.Context, row GovernanceStream) *GovernanceStreamFreshness {
	out := emptyStreamFreshness()
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	type observation struct {
		section  string
		evidence *GovernanceStreamFreshness
	}
	results := make(chan observation, 3)
	for section, probe := range map[string]func(context.Context, GovernanceStream) *GovernanceStreamFreshness{
		"sources": s.observeStreamSources, "flink": s.observeStreamCompute, "doris": s.observeStreamDoris,
	} {
		go func(section string, probe func(context.Context, GovernanceStream) *GovernanceStreamFreshness) {
			results <- observation{section, probe(ctx, row)}
		}(section, probe)
	}
	for n := 0; n < 3; n++ {
		select {
		case result := <-results:
			switch result.section {
			case "sources":
				out.Sources = result.evidence.Sources
				out.SourceCapture = result.evidence.SourceCapture
			case "flink":
				out.FlinkCompute = result.evidence.FlinkCompute
			case "doris":
				out.DorisWrite = result.evidence.DorisWrite
				out.DorisOutputOffset = result.evidence.DorisOutputOffset
			}
		case <-ctx.Done():
			return out
		}
	}
	return out
}
func (s *Server) observeStreamSources(ctx context.Context, row GovernanceStream) *GovernanceStreamFreshness {
	out := emptyStreamFreshness()
	sources, err := decode[map[string]governanceStreamSource](row.Sources)
	if err != nil {
		return out
	}
	positions, positionsErr := decode[map[string]int64](row.Positions)
	for node, source := range sources {
		evidence := GovernanceStreamSourceFreshness{NodeID: node, GovernanceStreamHealth: GovernanceStreamHealth{State: "unknown"}}
		if s.runtime != nil {
			status, e := s.runtime.connectorByName(ctx, source.Binding.ConnectorName)
			if e == nil {
				now := time.Now().UTC()
				evidence.ObservedAt = &now
				evidence.State = "unhealthy"
				if running(status) {
					evidence.State = "running"
				}
			}
		}
		ranges, e := cdcTopicRanges(ctx, source.Binding.Topic)
		if e == nil && len(ranges) == 1 {
			r := ranges[0]
			evidence.LatestEventAt = streamEventTimeAt(ctx, r, r.End-1)
			if positionsErr == nil {
				evidence.BacklogOffsets = streamForwardingBacklog(r, positions[node])
				if evidence.BacklogOffsets != nil {
					evidence.ForwardedToAt = streamEventTimeAt(ctx, r, positions[node]-1)
				}
			}
		}
		out.Sources = append(out.Sources, evidence)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].NodeID < out.Sources[j].NodeID })
	if len(out.Sources) > 0 {
		out.SourceCapture.State = "running"
		for _, source := range out.Sources {
			if source.State == "unhealthy" {
				out.SourceCapture.State = "unhealthy"
				break
			}
			if source.State == "unknown" {
				out.SourceCapture.State = "unknown"
			}
		}
		now := time.Now().UTC()
		out.SourceCapture.ObservedAt = &now
	}
	return out
}
func (s *Server) observeStreamCompute(ctx context.Context, row GovernanceStream) *GovernanceStreamFreshness {
	out := emptyStreamFreshness()
	if row.JobID != "" && s.runtime != nil {
		job, e := s.flink().job(ctx, row.JobID)
		if e == nil {
			now := time.Now().UTC()
			out.FlinkCompute.ObservedAt = &now
			switch job.State {
			case "RUNNING":
				out.FlinkCompute.State = "running"
			case "FINISHED":
				if row.State == "paused" {
					out.FlinkCompute.State = "paused"
				} else {
					out.FlinkCompute.State = "stopped"
				}
			case "FAILED", "CANCELED":
				out.FlinkCompute.State = "stopped"
			default:
				out.FlinkCompute.State = "unknown"
			}
		}
	}
	return out
}
func (s *Server) observeStreamDoris(ctx context.Context, row GovernanceStream) *GovernanceStreamFreshness {
	out := emptyStreamFreshness()
	def, e := decode[governanceFlowExecution](row.Definition)
	if e == nil && dorisObjectID.MatchString(row.ID) && validDataLayer(streamResultLayer(def)) {
		db, e := dorisOpen(ctx, false)
		if e == nil {
			defer db.Close()
			var offset sql.NullInt64
			// The existing marker is atomic with the result write. Reading it establishes
			// durable progress, but supplies neither a write time nor source-time lineage.
			e = db.QueryRowContext(ctx, "SELECT MAX(source_offset) FROM "+streamResultTable(def, row.ID)+" WHERE node_id='_dm_progress'").Scan(&offset)
			if e == nil && offset.Valid {
				now := time.Now().UTC()
				out.DorisWrite = GovernanceStreamHealth{State: "readable", ObservedAt: &now}
				out.DorisOutputOffset = streamInt64(offset.Int64)
			}
		}
	}
	return out
}
