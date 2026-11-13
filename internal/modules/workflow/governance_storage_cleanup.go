package workflow

import (
	"context"
	"github.com/minio/minio-go/v7"
	"time"
)

// The authorization transaction retires the result first. External deletion is
// retried from that committed receipt, never before a PostgreSQL commit.
func (s *Server) cleanupGovernanceStorage(ctx context.Context) error {
	var purges []GovernanceSnapshotPurge
	if e := s.db.WithContext(ctx).Where("storage_cleaned_at IS NULL").Order("created_at").Limit(20).Find(&purges).Error; e != nil {
		return e
	}
	for _, purge := range purges {
		var run GovernanceRun
		if e := s.db.WithContext(ctx).Where("id=?", purge.RunID).Take(&run).Error; e != nil {
			return e
		}
		report, e := decode[governanceFlowReport](run.Result)
		if e != nil {
			return e
		}
		if report.Storage != nil {
			if !validGovernanceStorage(report.Storage, run.ID) {
				return invalid
			}
			db, e := dorisOpen(ctx, true)
			if e != nil {
				return e
			}
			tables := []string{report.Storage.Database + "." + report.Storage.Table, dorisDatabase("ods") + ".input_" + run.ID, dorisDatabase("quality") + ".rejected_" + run.ID, dorisDatabase("internal") + ".stage_" + run.ID}
			for _, table := range tables {
				if _, e = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); e != nil {
					db.Close()
					return e
				}
			}
			db.Close()
		}
		var exports []GovernanceExport
		if e = s.db.WithContext(ctx).Where("run_id=?", run.ID).Find(&exports).Error; e != nil {
			return e
		}
		for _, export := range exports {
			// A leased exporter must finish or expire before deleting its objects.
			if export.State == "running" && export.LeaseExpiresAt.After(time.Now()) {
				return nil
			}
			if !dorisObjectID.MatchString(export.ID) || s.runtime.store == nil {
				return invalid
			}
			for object := range s.runtime.store.ListObjects(ctx, "datamesh-raw", minio.ListObjectsOptions{Prefix: "governance_exports/" + export.ID + "/", Recursive: true}) {
				if object.Err != nil {
					return object.Err
				}
				if e = s.runtime.store.RemoveObject(ctx, "datamesh-raw", object.Key, minio.RemoveObjectOptions{}); e != nil {
					return e
				}
			}
		}
		if e = s.db.WithContext(ctx).Model(&GovernanceSnapshotPurge{}).Where("run_id=?", run.ID).Update("storage_cleaned_at", time.Now()).Error; e != nil {
			return e
		}
	}
	return nil
}
