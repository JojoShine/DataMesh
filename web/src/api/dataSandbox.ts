import type {Field} from './types';
export const sandboxBase='/api/v1/data-sandboxes';
export interface Sandbox {id:string;name:string;state:'active'|'closing'|'closed'|'expired';version:number;expires_at:string;created_at:string;updated_at:string}
export interface SandboxTable {id:string;sandbox_id:string;name:string;state:'queued'|'running'|'ready'|'failed'|'canceled'|'unknown'|'removed';fields:Field[];field_labels:Record<string,string>;rows:number;bytes:number;error_code?:string}
export interface SandboxQuery {id:string;sql:string;mode:'preview'|'materialize';state:'queued'|'running'|'succeeded'|'failed'|'canceled'|'unknown';error_code?:string;truncated:boolean;row_count:number;fields:Field[]}
export interface SandboxRecords {items:Record<string,string|null>[];total:number;fields:Field[];truncated?:boolean}
