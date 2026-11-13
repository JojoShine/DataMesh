import type {Resource} from "./types";
import type {GovernanceRef} from "./governance";
export interface ProductDefinition {name:string;description:string;domain:GovernanceRef|null;terms:GovernanceRef[];resources:Resource[];contract:{freshness_seconds:number;max_failure_rate:number|null};usage_restrictions:string}
export interface ProductSemantic extends GovernanceRef {name:string;description:string}
export interface AssetProduct {id:string;department_id:string;owner_id:string;state:string;version:number;draft_id:string;published_id:string;revision_id:string;revision_state:string;definition:ProductDefinition;members:{resource:Resource;name:string;fields:{name:string;logical_type:string;sensitive:boolean}[]}[];semantics:{domain:ProductSemantic|null;terms:ProductSemantic[]};reviewer_id:string;review_note:string;valid_until:string|null;certification_status:"valid"|"expired"|"unreviewed";replacement_id:string;reason:string;can_manage:boolean;can_review:boolean}
export type CheckState="passed"|"violated"|"unknown";
export interface ProductCheck {id:string;product_id:string;revision_id:string;state:CheckState;checked_at:string;items:{resource:Resource;name:string;state:CheckState;checks:{code:string;status:CheckState;message:string}[]}[]}
export interface AssetUsageSummary {days:number;from:string;to:string;total:number;items:{action:string;count:number}[];daily:{date:string;count:number}[];cost:null;value:null;scope:string}
