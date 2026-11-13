import type {Resource,Field} from "./types";
export type SecurityKind="classification"|"policy"|"watermark"|"recognition"|"risk_rule";
export interface SecurityRowFilter{field:string;operator:string;value?:string|number|boolean|(string|number|boolean)[]|null}
export interface SecurityDefinition{name:string;description:string;resource:Resource|null;field:string;level:string;actions:string[];department:string;roles:string[];deny:boolean;mask_fields:string[];require_proof:boolean;visible:boolean;file_mark:boolean;invisible?:boolean;row_filters?:SecurityRowFilter[];template:{id:string;version:number}|null;text:string;pattern:string;threshold:number;window_minutes:number}
export interface SecurityControl{id:string;kind:SecurityKind;owner_id:string;created_by:string;generation:number;version:number;published_version:number;state:string;definition:SecurityDefinition;reviewer_id:string;reason:string;updated_at:string}
export interface SecurityResource{resource:Resource;name:string;fields:Field[]}
export interface SecurityOverviewData{counts:Record<string,number>;checked_at:string;scope:string;signing_available:boolean;signing_key_id:string;capabilities:{excel_content_mark:boolean;pdf_content_mark:boolean;invisible_watermark:boolean;row_filters:boolean}}
export interface SecurityScan{id:string;resource:Resource;candidates:{field:string;level:string;rule:string;state:string}[];created_at:string}
export interface SecurityIncident{id:string;resource:Resource;resource_id:string;title:string;reason:string;state:string;owner_id:string;frozen_by:string;generation:number;evidence:Record<string,unknown>;created_at:string}
export interface SecurityProof{manifest?:Record<string,unknown>;id:string;resource:Resource;actor_id:string;department_id:string;action:string;grant_id:string;fields:string[];controls:{id:string;version:number}[];text:string;sha256:string;key_id:string;signature:string;state:string;size_bytes:number;run_id:string;created_at:string}
export interface SecurityContextData{resource:Resource;level:string;blocked:boolean;reason?:string;mask_fields:string[];require_proof?:boolean;visible?:boolean;file_mark?:boolean;invisible?:boolean;row_filters?:SecurityRowFilter[]}
export const securityLevelNames:Record<string,string>={unclassified:"未分级",pending_review:"待复核",public:"公开",internal:"内部",sensitive:"敏感",restricted:"高敏"};
export const securityActionNames:Record<string,string>={query:"查询",download:"下载",invoke:"API调用",etl:"ETL交付"};
export const securityKindNames:Record<SecurityKind,string>={classification:"分类分级",policy:"防护策略",watermark:"水印管理",recognition:"识别规则",risk_rule:"风险规则"};
export const emptySecurityDefinition:SecurityDefinition={name:"",description:"",resource:null,field:"",level:"",actions:["query"],department:"",roles:[],deny:false,mask_fields:[],require_proof:false,visible:false,file_mark:false,invisible:false,row_filters:[],template:null,text:"{recipient} · {department} · {time} · {delivery_id}",pattern:"",threshold:5,window_minutes:60};

export function parseSecurityRowFilters(text:string):SecurityRowFilter[]{
 // Reject numeric literals that JSON.parse would silently round before submission.
 const decimal=(literal:string)=>{const m=/^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(literal);if(!m)return literal;let digits=(m[2]+(m[3]??"")).replace(/^0+/,"");let exponent=Number(m[4]??0)-(m[3]?.length??0);if(!digits)return "0";while(digits.endsWith("0")){digits=digits.slice(0,-1);exponent++}return `${m[1]}${digits}e${exponent}`};
 for(const token of text.matchAll(/"(?:\\.|[^"\\])*"|(-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/g)){
  if(token[1]===undefined)continue;
  const number=Number(token[1]);
  if(!Number.isFinite(number)||(Number.isInteger(number)&&!Number.isSafeInteger(number))||decimal(token[1])!==decimal(JSON.stringify(number)))throw new Error("数值精度超出浏览器范围，请将高精度整数或小数加双引号");
 }
 const value:unknown=JSON.parse(text);
 if(!Array.isArray(value)||value.length>32)throw new Error("最多配置32个行过滤条件");
 const scalar=(v:unknown):v is string|number|boolean=>typeof v==="string"||typeof v==="boolean"||(typeof v==="number"&&Number.isFinite(v));
 return value.map((item:unknown)=>{
  if(typeof item!=="object"||item===null||!("field" in item)||!("operator" in item)||typeof item.field!=="string"||!item.field.trim()||typeof item.operator!=="string"||!["eq","ne","lt","lte","gt","gte","in","is_null","not_null"].includes(item.operator))throw new Error("请检查字段与比较方式");
  if(item.operator==="is_null"||item.operator==="not_null")return {field:item.field,operator:item.operator};
  if(!("value" in item))throw new Error("请填写比较值");
  const v:unknown=item.value;
  if(item.operator==="in"){
   if(!Array.isArray(v)||v.length===0||!v.every(scalar))throw new Error("包含于的比较值应为非空数组");
   return {field:item.field,operator:item.operator,value:v};
  }
  if(!scalar(v))throw new Error("比较值须为文字、数字或布尔值");
  return {field:item.field,operator:item.operator,value:v};
 });
}
