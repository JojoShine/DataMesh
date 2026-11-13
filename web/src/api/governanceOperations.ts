import type {GovernanceFlowNodeResult,GovernanceFieldOrigin,GovernanceRef,GovernanceRun} from "./governance";
export interface GovernanceQualityIssue {
 origin_kind?:"dataflow"|"monitor"|"standard"; monitor_id?:string; observation_id?:string;
 id:string;department_id:string;actor_id:string;assignee_id:string;run_id:string;flow_id:string;flow_version:number;
 due_at:string;state:"open"|"in_progress"|"retesting"|"closed"|"superseded";version:number;retest_run_id:string|null;replacement_run_id?:string;replacement_issue_id?:string;created_at:string;closed_at:string|null;
 events?:{id:number;actor_id:string;action:string;note:string;run_id:string|null;created_at:string}[];
}
export interface GovernanceOperationsReport {run_states:{state:string;count:number}[];definitions:{kind:string;count:number}[];open_issues:number;overdue_issues:number;recent_runs:GovernanceRun[];observed_at:string}
export interface GovernanceDashboardState {state:string;count:number}
export interface GovernanceDashboardReport {
 capabilities?:{monitors:number;open_observations:number;audits:number;audit_violations:number;master_conflicts:number;confirmed_entities:number;metrics:number;scenarios:number;pending_releases:number};
 flow_modes?:{kind:string;count:number}[];streams?:GovernanceDashboardState[];schedule_targets?:{kind:string;state:string;count:number}[];quality_runs?:number;
 lightweight?:{units:number;files:number;runs:GovernanceDashboardState[];checked:number;passed:number;rejected:number};
 days:number;timezone:string;scope:"all"|"visible";started_at:string;observed_at:string;
 definitions:{kind:string;state:string;count:number}[];
 metadata_collection?:GovernanceDashboardState[];metadata_pending?:number;resources:{kind:string;count:number}[];observations:number;rules:GovernanceDashboardState[];
 outputs:{state:string;quality:string;completeness:string;count:number}[];
 runs:{kind:string;state:string;count:number}[];
 run_trend:{day:string;succeeded:number;failed:number;other:number}[];
 quality_trend:{day:string;checked:number;passed:number;rejected:number;runs:number}[];
 schedules:GovernanceDashboardState[];issues:GovernanceDashboardState[];overdue_issues:number;
 alerts:GovernanceDashboardState[];alert_rules:number;retention_policies:number;purged_snapshots:number;
 lifecycle_outputs:number;migrations:GovernanceDashboardState[];model_deployments:GovernanceDashboardState[];lineage_runs:number;
}
export interface GovernanceLineageEntry {data_layer?:import("./governance").GovernanceDataLayer;run_id:string;flow:GovernanceRef;model:GovernanceRef;fields:Record<string,GovernanceFieldOrigin[]>;nodes:GovernanceFlowNodeResult[];outputs:{dataset_id:string;contract_version:number;publication_status:string}[];created_at:string}
export type GovernanceSchedule = {target_kind?:"workflow"|"dataflow";parameters?:Record<string,string|number|boolean>;id:string;timezone:string;cron?:string;interval_seconds:number;concurrency_policy:"forbid";missed_policy:"latest"|"skip";next_run_at:string;state:"active"|"paused";version:number;last_run_id:string|null;last_error_code:string} & ({workflow:GovernanceRef;dataflow?:never}|{dataflow:GovernanceRef;workflow?:never})
