import type {SourceKind} from "./sourceTypes";
export type Platform = "exchange" | "governance" | "assets" | "security";
export interface Principal {
  public_test?:boolean;
  full_experience?:boolean;
  user_id: string;
  username: string;
  display_name?: string;
  department_id: string;
  platforms: Platform[];
  roles: string[];
  csrf_token: string;
}
export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}
export interface Field {
  business_name?: string;
  name: string;
  logical_type: string;
  nullable: boolean;
  primary_key: boolean;
  sensitive: boolean;
  precision: number | null;
  scale: number | null;
}
export interface Source {
  scheme?:string;
  auth_database?:string;
  source_version?:number;
  capture_template?:boolean;
  source_id: string;
  name: string;
  kind: string;
  owner_id: string;
  department_id: string;
  configured_version: string;
  credential_configured: boolean;
  connection_status: string;
  cdc_status: string;
}
export interface SourceObject {
  object_id: string;
  source_id: string;
  qualified_name: string;
  schema_version: number;
  fields: Field[];
}
export interface Progress {
  status: string;
  last_success_at: string | null;
  position: Record<string, unknown> | null;
  error_code: string | null;
}
export interface Task {
  capture_binding?:{connector_name:string;schema:string;table:string;topic:string;kind?:"postgres"|"mysql"|"oracle";slot?:string;pdb?:string;publication?:string};
  snapshot?:{state:string;object_version:number;partitions:{topic:string;partition:number;start:number;end:number}[]};
  task_id: string;
  run_id: string | null;
  source_id: string;
  object_id: string;
  config_version: number;
  state: string;
  ingestion: Progress;
  processing: Progress;
  result: Progress;
  archive: Progress;
}
export interface RuleDefinition {
  name: string;
  object_id: string;
  schema_version: number;
  checks: Check[];
}
export interface Check {
  field: string;
  kind: string;
  blocking: boolean;
  parameters: Record<string, unknown>;
}
export interface Rule {
  rule_id: string;
  version: number;
  state: string;
  definition: RuleDefinition;
  created_at: string;
}
export interface GovernanceTreatment {kind:string;name:string;description:string}
export interface GovernanceProvenance {
  single_source:boolean;
  source?:Resource;
  source_name?:string;
  provider_department_id?:string;
  original_directory_id?:string;
  original_directory_name?:string;
  steps:GovernanceTreatment[];
}
export interface Dataset {
 data_layer?:import("./governance").GovernanceDataLayer;
 storage?:import("./governance").GovernanceStorage;
  governance?:GovernanceProvenance|null;
  workspace_id?:string;
  governance_run_id?: string;
  dataset_id: string;
  contract_version: number;
  provider_department_id: string;
  definition: {
    name: string;
    description: string;
    source_object_id: string;
    owner_id: string;
    fields: Field[];
    usage_restrictions: string;
  };
  publication_status: string;
  quality_status: string;
  completeness_status: string;
  latest_watermark: Record<string, unknown> | null;
}
export interface Asset {
  dataset_id: string;
  name: string;
  description: string;
  provider_department_id: string;
  field_count: number;
  usage_restrictions: string;
  contract_version: number;
  updated_at: string;
  resource_type: string;
}
export interface AssetPage extends Page<Asset> {
  departments: string[];
}
export interface PublicAssetDetail extends Asset {
  details: Pick<DirectoryDefinition, "resource_code" | "provider_name" | "classification" | "classification_path" | "update_frequency" | "format" | "data_layer" | "items" | "sharing_type" | "sharing_method" | "sharing_conditions" | "open_conditions" | "security_classification" | "legal_basis">;
}
export interface Resource {
  resource_type: "raw_object" | "governed_dataset" | "table_resource" | "file_resource" | "api_resource" | "master_snapshot";
  resource_id: string;
  contract_version: number;
}
export interface GrantRequest {
  materials?: ApplicationMaterials;
  resource: Resource;
  department_id: string;
  purpose: string;
  fields: string[];
  expires_at: string;
}
export interface Application {
  resource_name?: string;
  sharing_snapshot?: SharingSnapshot;
  supplements?: {action:string;actor_id:string;reason:string;materials?:ApplicationMaterials;at:string}[];
  application_id: string;
  applicant_id: string;
  state: string;
  request: GrantRequest;
  created_at: string;
  decision_reason: string | null;
}
export interface Grant {
  resource_name?: string; available?: boolean; availability_reason?: string;
  sharing_snapshot?: SharingSnapshot;
  grant_id: string;
  application_id: string;
  grantee_user_id: string;
  scope: GrantRequest;
  state: string;
  approved_by: string;
  approved_at: string;
}
export interface Audit {
  audit_id: string;
  actor_id: string | null;
  action: string;
  object_id: string;
  result: string;
  trace_id: string;
  created_at: string;
}
export interface OrganizationUnit {id:string;parent_id:string|null;kind:"organization"|"department";name:string;code:string;active:boolean;version:number}
export interface DirectoryProvider {department_id:string;department_name:string;organization_id:string;provider_name:string;provider_code:string;active:boolean}
export interface Account {
 user_id:string;username:string;display_name?:string;department_id:string;active:boolean;roles:string[];platforms?:Platform[];version?:number;
}
export interface Operation {
  operation_id: string;
  status: "accepted" | "running" | "succeeded" | "failed" | "unknown";
  resource_id: string;
  result: Record<string, unknown> | null;
}
export interface Quality {
  dataset_id: string;
  contract_version: number;
  rule_version: number;
  status: string;
  passed_count: number;
  failed_count: number;
  checked_range: Record<string, unknown> | null;
  failure_reasons: { code: string; count: number }[];
}
export interface Preflight {
  source_id: string;
  kind: string;
  passed: boolean;
  checks: {
    check: string;
    passed: boolean;
    reason: string | null;
    suggestion: string | null;
    label?: string;
    status?: "passed" | "failed" | "unknown" | "unsupported" | "skipped";
    owner?: string;
    actual?: string;
    expected?: string;
    steps?: string[];
    verify_sql?: string;
  }[];
  checked_at: string;
}
export interface Delivery {
  resource: Resource;
  records?: Record<string, unknown>[];
  events?: Record<string, unknown>[];
  watermark?: Record<string, unknown>;
  cutoff?: Record<string, unknown>;
  next_cursor: string | null;
}

export interface ApplicationMaterials {
 legal_basis: string; scenario: string; scope: string; method: "query" | "download" | "invoke"; department_approval_ref: string;
}
export interface SharingSnapshot {directory_id:string;directory_revision_id:string;mount_id:string;mount_revision_id:string}
export interface DirectoryItem {name:string;business_name:string;logical_type:string;length:number|null;precision:number|null;scale:number|null;nullable:boolean;sensitive:boolean;description:string}
export interface DirectoryDefinition {
 name:string;description:string;classification:"base"|"theme"|"department";classification_path:string;resource_code:string;dictionary_id:string;
 provider_name:string;provider_code:string;responsible_user_id:string;update_frequency:string;format:"database"|"api"|"file";data_layer:"raw_object"|"governed_dataset";
 items:DirectoryItem[];sharing_type:"unconditional"|"conditional"|"not_shared";sharing_conditions:string;non_sharing_reason:string;legal_basis:string;sharing_method:string;
 usage_restrictions:string;security_classification:string;open_to_public:boolean;open_conditions:string;department_approval_ref:string;assessment_ref:string;related_resource_codes:string[];
}
export interface ResourceDirectory {
 directory_id:string;version:number;state:string;department_id:string;revision_id:string;revision_state:string;published_revision_id:string;definition:DirectoryDefinition;
 reason:string;available:boolean;sharing_snapshot:SharingSnapshot|null;permissions:Record<string,boolean>;resource:Resource|null;fields:Field[];
 resources?:DirectoryResource[];
}
export interface DirectoryResource {
 updated_at?:string; data_layer?:string; quality_status?:string;
 resource:Resource;fields:Field[];available:boolean;sharing_snapshot:SharingSnapshot|null;name?:string;governance?:GovernanceProvenance;
}
export function directoryResources(directory:ResourceDirectory):DirectoryResource[] {
 return directory.resources??(directory.resource?[{resource:directory.resource,fields:directory.fields,available:directory.available,sharing_snapshot:directory.sharing_snapshot,name:directory.definition.name}]:[]);
}
export interface DirectoryDictionary {id:string;version?:number;definition:{name:string;version:string;source:string;verified:boolean;test_only:boolean;code_pattern:string;code_template?:string;sequence_width?:number;classifications:string[];verification_ref:string}}
export interface MountDefinition {
 directory_revision_id:string;resource:Resource;mapping:Record<string,string>;validation:{checked_at:string;passed:boolean;checks:{name:string;passed:boolean;message:string}[];watermark:Record<string,unknown>|null}|null;
 submitted_by:string;approved_by:string;reason:string;
}
export interface ResourceMount {can_operate:boolean;mount_id:string;directory_id:string;state:string;version:number;revision_id:string;revision_state:string;active_revision_id:string;definition:MountDefinition}
export interface DirectoryReview {
 review_id:string;version:number;state:string;department_id:string;definition:{target_kind:"directory"|"mount";target_id:string;revision_id:string;stage:string;submitted_by:string;reviewer_id:string;reason:string};
 snapshot:{definition?:DirectoryDefinition;reason?:string}&Partial<MountDefinition>;previous:({definition?:DirectoryDefinition}&Partial<MountDefinition>)|null;created_at:string;can_review:boolean;
}
export interface AssignmentDefinition {user_id:string;department_id:string;resource_id:string;capability:string;expires_at:string;reason:string}
export interface Assignment {id:string;state:string;version:number;department_id:string;definition:AssignmentDefinition}
export interface ProcessingDefinition {resource:Resource;user_id:string;fields:string[];purpose:string;expires_at:string;allow_derived:boolean;allow_sharing:boolean;sharing_basis:string;approval_ref:string}
export interface ProcessingAuthorization {id:string;state:string;version:number;department_id:string;definition:ProcessingDefinition}

export function deliveryMethod(resource: Resource): "query" | "download" | "invoke" {
  return resource.resource_type === "file_resource" ? "download" : resource.resource_type === "api_resource" ? "invoke" : "query";
}
export function resourceLabel(resource: Resource): string {
  return {master_snapshot:"主数据确认快照（私有）",raw_object:"原始数据",governed_dataset:"治理成果",table_resource:"库表资源",file_resource:"文件资源",api_resource:"API 资源"}[resource.resource_type];
}

export interface SourceTarget {
 kind: "postgres" | "mysql" | "oracle";
 host: string;
 port: number;
 database: string;
}
export interface SourceAllowedTarget extends SourceTarget {
 target_id: string;
 origin: "platform" | "environment" | "builtin";
 created_at: string | null;
}

export interface ETLAuthorizationBasis {
 sharing_snapshot:SharingSnapshot;grant_id:string;grant_version:number;grant_fingerprint:string;policy_fingerprint:string;
}
export interface ETLInputSnapshot {
 authorization?:ETLAuthorizationBasis;
 kind:"database"|"table_resource"|"raw_object"|"governed_dataset";
 resource_id:string;contract_version:number;snapshot_id:string;fields:string[];
 partitions?:{topic:string;partition:number;start:number;end:number}[];
}
export interface ETLTimings {
 prepare_ms:number;read_transform_ms:number;write_ms:number;index_ms:number;
 before_check_ms:number;commit_ms:number;total_ms:number;
}
export interface DeliveryReconciliationReport {
 id:string;run_id:string;version:number;state:"pending"|"checking"|"passed"|"mismatched"|"failed";
 input_snapshot_id:string;input_scope:ETLInputSnapshot|null;task_version:number;
 read_rows:number;processed_rows:number;key_count:number;checked_rows:number;mismatch_count:number;
 duration_ms?:number;error_code?:string;checked_at:string|null;created_at:string;updated_at:string;
}
export interface DeliveryReconciliationPage {
 run_id:string;latest:DeliveryReconciliationReport|null;history:DeliveryReconciliationReport[];
 total:number;page:number;page_size:number;can_retry:boolean;recipient_id:string;execution_state:string;
}
export interface DeliveryReceipt {
 id:string;run_id:string;version:number;reconciliation_id:string;decision:"accepted"|"rejected";
 reason:string;actor_id:string;recipient_id:string;on_behalf_reason?:string;created_at:string;
}
export interface DeliveryReceiptPage {
 run_id:string;version:number;state:"pending"|"accepted"|"rejected"|"not_applicable";
 items:DeliveryReceipt[];total:number;page:number;page_size:number;recipient_id:string;
 can_accept:boolean;can_reject:boolean;delegate:boolean;
}
