import type {GovernanceRun,MetadataResource} from "./governance";
import type {Resource} from "./types";
import {logicalPath} from "../app/basePath";
export interface GovernanceWorkspace {
 required_release_kinds?:string[];id:string;department_id:string;name:string;description:string;owner_id:string|null;state:"active"|"archived";is_legacy:boolean;max_concurrent_runs:number;version:number;created_at:string;updated_at:string}
export interface GovernanceWorkspaceSource {reference:{workspace_id:string;resource_type:Resource["resource_type"];resource_id:string;contract_version:number;active:boolean};resource:MetadataResource|null;available:boolean}
export interface GovernanceWorkspaceOverview {workspace:GovernanceWorkspace;source_count:number;model_count:number;dataflow_count:number;workflow_count:number;run_states:{state:string;count:number}[];recent_runs:GovernanceRun[];recent_results:{dataset_id:string;name:string;publication_status:string;created_at:string}[];quality_summary:{checked:number;passed:number;pass_rate:number|null;scope:string};observed_at:string}
export function governanceWorkspacePath(path:string|null):string|null {
 if(!path||typeof location==="undefined"||!logicalPath(location.pathname).startsWith("/platform/governance"))return path;
 if(!path.startsWith("/api/v1/governance/")&&!/^\/api\/v1\/datasets(?:[/?]|$)/.test(path))return path;
 if(path.startsWith("/api/v1/governance/workspaces"))return path;
 const scope=new URLSearchParams(location.search).get("workspace_id");if(!scope)return path;
 const url=new URL(path,location.origin);if(!url.searchParams.has("workspace_id"))url.searchParams.set("workspace_id",scope);return url.pathname+url.search;
}
