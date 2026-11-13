import {governanceFieldLabel} from "../api/governanceFieldLabels";
import {governanceFieldTooltip,GovernanceFieldHelp} from "./GovernanceFieldHelp";
import {GovernanceDialog,GovernanceDialogActions} from "./GovernanceDialog";
import {Alert,App,Button,Descriptions,Form,Select,Space,Table} from "antd";
import {useEffect,useRef,useState} from "react";
import {ApiError,errorMessage,mutation,query} from "../api/client";
import type {Dataset,Operation,Page,Source} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {Failure,Status} from "./common";

interface SourceTable {schema:string;name:string;kind:string}
type Mapping={source:string;target:string;rule:string};
interface BindingInput {source_id:string;source_version:number;schema:string;table:string;input_archive_id:string;contract_version:number;rule_version:number;mapping:Mapping[]}
interface Binding extends BindingInput {dataset_id:string;input_snapshot_id:string;result_snapshot_id:string}
interface ResultCheck {check_id:string;state:string;processed_rows:number;error_code?:string;created_at:string}
interface Report {binding:Binding|null;checks:ResultCheck[];rule_version:number;contract_version:number;input_archive_id:string}
interface Values {source_id:string;table_key:string;rules:Record<string,string>}
const key=(table:SourceTable)=>JSON.stringify([table.schema,table.name]);
const reason:Record<string,string>={QUALITY_BLOCKED:"完整范围质量检查未通过，请查看质量检查结果",RESULT_RECONCILIATION_FAILED:"实际结果与所选原始快照或字段转换不一致",RESULT_INPUT_CHANGED:"来源归档范围已变化，请重新选择后核验",SOURCE_VERSION_CHANGED:"结果数据源配置已变化，请重新选择",RESULT_LIMIT:"结果数据超过本次配置的容量上限",RESULT_VERSION_CHANGED:"目录契约或规则版本已变化，请刷新",FORBIDDEN:"执行账号的核验权限已失效"};

export function GovernedResultBindingEditor({dataset,onClose,onQueued}:{dataset:Dataset;onClose:()=>void;onQueued:()=>void}){
 const {message}=App.useApp();
 const [form]=Form.useForm<Values>();
 const report=useQuery<Report>(`/api/v1/datasets/${dataset.dataset_id}/result-binding`,true);
 const [search,setSearch]=useState("");
 const sources=useQuery<Page<Source>>("/api/v1/sources?"+query({page_size:100,search}));
 const sourceID=Form.useWatch("source_id",form);
 const tableKey=Form.useWatch("table_key",form);
 const [cachedSource,setCachedSource]=useState<Source|null>(null);
 const selectedSource=sources.data?.items.find(s=>s.source_id===sourceID)??(cachedSource?.source_id===sourceID?cachedSource:null);
 const tables=useQuery<{items:SourceTable[]}>(sourceID?`/api/v1/sources/${sourceID}/tables`:null);
 const chosenTable=tables.data?.items.find(t=>key(t)===tableKey);
 const [busy,setBusy]=useState(false);
 const [queued,setQueued]=useState(false);
 const initialized=useRef(false);
 const pending=useRef<{body:string;key:string}|null>(null);
 const latest=report.data?.checks[0];
 const active=latest?.state==="queued"||latest?.state==="checking";
 const reload=useRef(report.reload);reload.current=report.reload;
 const refreshParent=useRef(onQueued);refreshParent.current=onQueued;
 useEffect(()=>{if(!active&&!queued)return;const timer=setInterval(()=>reload.current(),2000);return()=>clearInterval(timer)},[active,queued]);
 useEffect(()=>{if(latest?.state==="passed"||latest?.state==="blocked"){setQueued(false);refreshParent.current()}},[latest?.check_id,latest?.state]);
 useEffect(()=>{
  if(!report.data||initialized.current)return;initialized.current=true;
  const binding=report.data.binding;
  if(binding)form.setFieldsValue({source_id:binding.source_id,table_key:JSON.stringify([binding.schema,binding.table]),rules:Object.fromEntries(binding.mapping.map(m=>[m.source,m.rule]))});
 },[report.data,form]);
 const choices=(sources.data?.items??[]).filter(s=>s.kind==="postgres"||s.kind==="mysql");
 if(cachedSource&&!choices.some(s=>s.source_id===cachedSource.source_id))choices.push(cachedSource);
 async function save(values:Values){
  if(!selectedSource?.source_version||!chosenTable||!report.data?.input_archive_id)return;
  const body:BindingInput={source_id:sourceID,source_version:selectedSource.source_version,schema:chosenTable.schema,table:chosenTable.name,input_archive_id:report.data.input_archive_id,contract_version:report.data.contract_version,rule_version:report.data.rule_version,mapping:dataset.definition.fields.map(f=>({source:f.name,target:f.name,rule:values.rules[f.name]??"copy"}))};
  const fingerprint=JSON.stringify(body);if(pending.current?.body!==fingerprint)pending.current={body:fingerprint,key:crypto.randomUUID()};
  setBusy(true);
  try{await mutation<Operation>(`/api/v1/datasets/${dataset.dataset_id}/result-binding`,body,pending.current.key);pending.current=null;setQueued(true);report.reload();onQueued();message.success("核验已在后台排队，关闭窗口后仍会继续")}catch(e){if(e instanceof ApiError&&e.status>=400&&e.status<500&&e.status!==408)pending.current=null;message.error(errorMessage(e))}finally{setBusy(false)}
 }
 return <GovernanceDialog header="绑定治理结果库表" onClose={onClose} busy={busy} width={880} actions={<Button type="primary" loading={busy} disabled={active||queued||!report.data?.input_archive_id||!selectedSource?.source_version||!chosenTable} onClick={()=>form.submit()}>保存并核验</Button>}>
  <p className="helper-text">指定实际生成的治理结果，核对完整原始归档、字段转换和质量规则。核验通过后保存固定结果快照，再发布供申请使用。</p>
  {report.error?<Failure error={report.error}/>:<>
   <Descriptions size="small" column={2} items={[{key:"name",label:"治理成果",children:dataset.definition.name},{key:"version",label:"契约 / 规则版本",children:`${report.data?.contract_version??dataset.contract_version} / ${report.data?.rule_version??"—"}`}]}/>
   {report.data&&!report.data.input_archive_id&&<Alert type="warning" showIcon title="尚无完整原始归档，请先在接入任务中归档来源数据"/>}
   {queued&&<Alert type="info" showIcon title="核验已在后台排队，窗口内会自动更新进度"/>}
   <Form<Values> form={form} layout="vertical" onFinish={save} initialValues={{rules:Object.fromEntries(dataset.definition.fields.map(f=>[f.name,"copy"]))}} disabled={busy||active||queued}>
    <Form.Item name="source_id" label="结果数据源" tooltip={governanceFieldTooltip("结果数据源")} rules={[{required:true}]}>
     <Select showSearch filterOption={false} onSearch={setSearch} loading={sources.loading} placeholder="搜索 PostgreSQL / MySQL 数据源" options={choices.map(s=>({value:s.source_id,label:s.name}))} onChange={id=>{setCachedSource(choices.find(s=>s.source_id===id)??null);form.setFieldValue("table_key",undefined)}}/>
    </Form.Item>
    {Boolean(sources.error)&&<Failure error={sources.error} retry={sources.reload}/>}
    <Form.Item name="table_key" label="结果库表" tooltip={governanceFieldTooltip("结果库表")} rules={[{required:true}]}>
     <Select disabled={!sourceID||tables.loading||busy||active||queued} loading={tables.loading} showSearch optionFilterProp="label" placeholder="选择已生成治理结果的库表" options={tables.data?.items.map(t=>({value:key(t),label:`${t.schema}.${t.name}`}))}/>
    </Form.Item>
    {Boolean(tables.error)&&<Failure error={tables.error} retry={tables.reload}/>}
    <GovernanceDialogActions><Button onClick={sources.reload} disabled={busy||active||queued}>刷新数据源</Button><Button onClick={()=>{form.setFieldValue("table_key",undefined);tables.reload()}} disabled={!sourceID||busy||active||queued}>刷新库表</Button></GovernanceDialogActions>
    <Table rowKey="name" size="small" pagination={false} dataSource={dataset.definition.fields} columns={[{title:"原始字段 → 结果字段",render:(_,f)=>`${governanceFieldLabel(f)} → ${governanceFieldLabel(f)}`},{title:<span>转换规则<GovernanceFieldHelp label="转换规则"/></span>,render:(_,f)=><Form.Item name={["rules",f.name]} noStyle><Select aria-label={`${f.name} 的转换规则`} disabled={f.primary_key||f.logical_type!=="string"||busy||active||queued} options={[{value:"copy",label:"直接映射"},{value:"trim",label:"去除首尾空白"},{value:"upper",label:"转为大写"},{value:"lower",label:"转为小写"}]}/></Form.Item>}]}/>
   </Form>
   <p className="helper-text">主键保留原值；结果须与当前完整归档逐条对齐。来源范围、契约或规则变化后须重新核验。</p>
   {report.data?.checks.length?<Table<ResultCheck> size="small" rowKey="check_id" pagination={false} dataSource={report.data.checks} columns={[{title:"核验状态",dataIndex:"state",render:v=><Status value={v}/>},{title:"已检查行数",dataIndex:"processed_rows"},{title:"结果",render:(_,r)=>r.error_code?(reason[r.error_code]??"核验未完成，请按检查编号联系管理员核查"):r.state==="passed"?"完整范围核验通过":"等待核验完成"},{title:"检查编号",dataIndex:"check_id",ellipsis:true}]}/>:null}
  </>}
 </GovernanceDialog>
}
