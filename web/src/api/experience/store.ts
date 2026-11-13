import type {Principal} from "../types";
export type Value=null|string|number|boolean|Value[]|ObjectValue;
export interface ObjectValue {[key:string]:Value}
export interface ExperienceState {schema:1;owner:string;collections:Record<string,ObjectValue[]>}
export class ExperienceError extends Error {
 constructor(public status:number,public code:string,message:string){super(message)}
}
export function invalid(message:string):never{throw new ExperienceError(400,"EXPERIENCE_INVALID",message)}
export function object(value:unknown):value is Record<string,unknown>{return typeof value==="object"&&value!==null&&!Array.isArray(value)}
export function json(value:unknown,depth=0):Value {
 if(depth>45)invalid("体验配置嵌套过深");
 if(value===null||typeof value==="string"||typeof value==="boolean")return value;
 if(typeof value==="number"&&Number.isFinite(value))return value;
 if(Array.isArray(value))return value.map(item=>json(item,depth+1));
 if(object(value))return Object.fromEntries(Object.entries(value).filter(([,item])=>item!==undefined).map(([key,item])=>[key,json(item,depth+1)]));
 return invalid("体验数据格式不正确");
}
export function record(value:unknown):ObjectValue {const result=json(value);if(!object(result))return invalid("请提供完整的配置对象");return result}
export function text(value:unknown,fallback=""){return typeof value==="string"?value:fallback}
export function numeric(value:unknown,fallback=0){return typeof value==="number"&&Number.isFinite(value)?value:fallback}
export function list(state:ExperienceState,kind:string){return state.collections[kind]??[]}
export function find(state:ExperienceState,kind:string,id:string){const row=list(state,kind).find(row=>row.id===id||row.file_id===id||row.dataset_id===id||row.object_id===id);if(!row)throw new ExperienceError(404,"EXPERIENCE_NOT_FOUND","体验空间中没有这条记录");return row}
export function put(state:ExperienceState,kind:string,row:ObjectValue){const rows=list(state,kind);const id=row.id??row.file_id??row.dataset_id??row.object_id;state.collections[kind]=[row,...rows.filter(previous=>(previous.id??previous.file_id??previous.dataset_id??previous.object_id)!==id)]}
export function key(){return "exp-"+crypto.randomUUID()}
export function now(){return new Date().toISOString()}
export function storageKey(user:Principal){return "datamesh.governance-experience.v1."+user.user_id}
export function read(user:Principal,seed:()=>ExperienceState):ExperienceState {
 let raw:string|null;try{raw=localStorage.getItem(storageKey(user))}catch{throw new ExperienceError(507,"EXPERIENCE_STORAGE_UNAVAILABLE","当前浏览器不允许保存体验数据，请检查隐私设置")}if(!raw)return seed();
 let parsed:unknown;try{parsed=JSON.parse(raw)}catch{return invalid("本浏览器的体验数据无法读取，请使用“重置体验空间”重新开始")}
 if(!object(parsed)||parsed.schema!==1||parsed.owner!==user.user_id||!object(parsed.collections))return invalid("体验数据版本不匹配，请重置体验空间");
 const collections:Record<string,ObjectValue[]>={};
 for(const [name,rows] of Object.entries(parsed.collections)){
  if(!Array.isArray(rows)||rows.length>2000||!rows.every(object))return invalid("体验记录格式不正确，请重置体验空间");
  collections[name]=rows.map(row=>record(row));
 }
 return {schema:1,owner:user.user_id,collections};
}
export function persist(user:Principal,state:ExperienceState){
 if(Object.values(state.collections).some(rows=>rows.length>2000))invalid("体验记录数量已满，请重置体验空间");
 const serialized=JSON.stringify(state);if(new Blob([serialized]).size>3*1024*1024)invalid("当前浏览器的体验空间已满，请删除旧运行或重置体验空间");
 try{localStorage.setItem(storageKey(user),serialized)}catch{throw new ExperienceError(507,"EXPERIENCE_STORAGE_FULL","无法保存体验数据，请检查浏览器存储空间和隐私设置")}
}
export function page(rows:ObjectValue[],params:URLSearchParams){const page=Math.max(1,Math.floor(Number(params.get("page"))||1)),size=Math.min(100,Math.max(1,Math.floor(Number(params.get("page_size"))||15)));return {items:rows.slice((page-1)*size,page*size),total:rows.length,page,page_size:size}}
