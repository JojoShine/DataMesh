import {pageSizeOptions,usePagination,localPagination} from "../hooks/usePagination";
import {Alert,App,Button,Card,Form,Input,InputNumber,Pagination,Select,Space,Table,Tag,Typography} from "antd";
import {PlusOutlined,ApiOutlined} from "@ant-design/icons";
import {useEffect,useState} from "react";
import type {Field,Page,ResourceDirectory} from "../api/types";
import {errorMessage,query} from "../api/client";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {Title,Failure} from "../components/common";
import {PlatformDialog,DialogActions} from "../components/PlatformDialog";
import {navigate} from "../app/navigation";

type Parameter={name:string;type:string;required:boolean};
type ServiceQuery={dataset_id:string;mode:"snapshot"|"latest";sql:string;max_page_size:number;source_fields:Field[]};
type Service={id:string;name:string;directory_id:string;query:ServiceQuery;parameters:Parameter[];fields:Field[];created_at:string};
type Source={id:string;name:string;fields:Field[];created_at:string};
type Values={name:string;dataset_id:string;mode:"snapshot"|"latest";sql:string;max_page_size:number;directory_id?:string;parameters:string};
type Preview={records:Record<string,unknown>[];fields:Field[];parameters:Parameter[];dataset_id:string};

export function DataServices(){
 const [page,setPage,pagePagination]=usePagination(),[editor,setEditor]=useState<{seed?:Service}|null>(null),[selected,setSelected]=useState<Service|null>(null);
 const list=useQuery<Page<Service>>(`/api/v1/data-services?page=${page}&page_size=${pagePagination.pageSize}`);
 return <><Title title="数据服务" description="把已发布的治理成果提供为查询接口，复用目录审批和数据使用授权。" actions={<Space size={16}><Button onClick={list.reload}>刷新</Button><Button type="primary" icon={<PlusOutlined/>} onClick={()=>setEditor({})}>新建数据服务</Button></Space>}/>
 <Alert style={{marginBottom:24}} showIcon title="选择成果 → 定义查询 → 校验并登记 → 目录挂载与审批 → 授权调用" description="接口登记后仍须完成目录、挂载审核及启用共享。修改查询时复制为新接口，重新审批授权；既有接口与授权保持原范围。"/>
 {list.error?<Failure error={list.error}/>:<Table<Service> rowKey="id" loading={list.loading} dataSource={list.data?.items} scroll={{x:900}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"服务名称",render:(_,r)=><Button type="link" onClick={()=>setSelected(r)}>{r.name}</Button>},{title:"成果更新方式",render:(_,r)=>r.query.mode==="latest"?"最新兼容已发布成果":"固定成果"},{title:"返回字段",render:(_,r)=>r.fields.map(f=>f.name).join("、")},{title:"每页上限",render:(_,r)=>r.query.max_page_size},{title:"操作",render:(_,r)=><Space size={16} wrap><Button onClick={()=>navigate(`/platform/exchange/directory?view=manage&action=mount&directory_id=${r.directory_id}`)}>目录挂载与审核</Button><Button onClick={()=>setEditor({seed:r})}>复制为新接口</Button></Space>}]}/>}
 {editor&&<ServiceEditor seed={editor.seed} onClose={()=>setEditor(null)} onSaved={()=>{setEditor(null);setPage(1);list.reload()}}/>}
 <PlatformDialog open={Boolean(selected)} title={selected?.name??"接口详情"} width={960} onCancel={()=>setSelected(null)} destroyOnHidden>{selected&&<Space orientation="vertical" size={24} style={{width:"100%"}}><Alert showIcon title="登记不代表开放调用" description="先完成目录挂载、审核及共享启用，再申请数据使用授权。被授权人可在“数据使用授权 → 调用 API”创建外部调用凭据。"/><Typography.Paragraph copyable>{`/delivery/v1/services/${selected.id}/invoke`}</Typography.Paragraph><pre style={{whiteSpace:"pre-wrap",overflowWrap:"anywhere"}}>{selected.query.sql}</pre><ServiceAccess key={selected.id} serviceID={selected.id} parameters={selected.parameters} maxPageSize={selected.query.max_page_size}/></Space>}</PlatformDialog>
 </>;
}
function ServiceEditor({seed,onClose,onSaved}:{seed?:Service;onClose:()=>void;onSaved:()=>void}){
 const [form]=Form.useForm<Values>();const action=useAction();const {message}=App.useApp();
 const [sourcePage,setSourcePage,sourcePagePagination]=usePagination(),[directoryPage,setDirectoryPage,directoryPagePagination]=usePagination(),[directorySearch,setDirectorySearch]=useState("");const [source,setSource]=useState<Source|null>(seed?{id:seed.query.dataset_id,name:"原接口来源成果",fields:seed.query.source_fields,created_at:""}:null);
 const [failure,setFailure]=useState<string|null>(null);
 const [directory,setDirectory]=useState<ResourceDirectory|null>(null),[preview,setPreview]=useState<Preview|null>(null);
 const sources=useQuery<{items:Source[];has_more:boolean}>(`/api/v1/data-services/sources?page=${sourcePage}&page_size=${sourcePagePagination.pageSize}`);
 const directories=useQuery<Page<ResourceDirectory>>("/api/v1/resource-directories?"+query({scope:"mine",page:directoryPage,search:directorySearch,resource_id:source?.id,page_size:directoryPagePagination.pageSize}));
 const sourceOptions=[...(source?[source]:[]),...(sources.data?.items??[]).filter(s=>s.id!==source?.id)];
 const directoryOptions=[...(directory?[directory]:[]),...(directories.data?.items??[]).filter(d=>d.directory_id!==directory?.directory_id&&d.state==="published")];
 async function run(create:boolean){setFailure(null);try{const values=await form.validateFields();if(create&&!directory){message.warning("请选择来源成果所属的业务目录");return}let parameters:unknown;try{parameters=JSON.parse(values.parameters)}catch{throw new Error("校验参数须为 JSON 对象，值使用文本")};if(!parameters||typeof parameters!=="object"||Array.isArray(parameters))throw new Error("校验参数须为 JSON 对象");const body={...values,parameters,directory_id:directory?.directory_id??"",expected_directory_version:directory?.version??0};if(create){await action.run("/api/v1/data-services",body);message.success("接口已登记，请继续完成目录挂载及审核");onSaved()}else{setPreview(await action.run<Preview>("/api/v1/data-services/validate",body));message.success("实际查询通过")}}catch(e){setFailure(errorMessage(e));message.error(errorMessage(e))}}
 useEffect(()=>{if(!directory&&directories.data?.items.length===1)setDirectory(directories.data.items[0])},[directory,directories.data]);
 return <PlatformDialog open title={seed?"复制为新接口":"新建数据服务"} width={960} onCancel={onClose} busy={action.busy} footer={<><Button loading={action.busy} onClick={()=>void run(false)}>校验并预览</Button><Button type="primary" loading={action.busy} disabled={!preview||!directory} onClick={()=>void run(true)}>登记接口</Button></>}>
 {failure&&<Alert type="error" showIcon title="尚未完成登记" description={failure} style={{marginBottom:24}}/>}
 <Form form={form} layout="vertical" initialValues={{name:seed?seed.name+"（新版本）":"",dataset_id:seed?.query.dataset_id,mode:seed?.query.mode??"snapshot",sql:seed?.query.sql??"",max_page_size:seed?.query.max_page_size??100,parameters:"{}"}} onValuesChange={()=>setPreview(null)}>
 <Form.Item label="服务名称" name="name" rules={[{required:true}]}><Input maxLength={200} placeholder="填写对外提供的数据服务名称"/></Form.Item>
 <Form.Item label="已发布的治理成果" name="dataset_id" rules={[{required:true}]} extra="仅列出有管理权限、已发布并存储在 Doris 的成果。"><Select showSearch optionFilterProp="label" loading={sources.loading} options={sourceOptions.map(s=>({value:s.id,label:`${s.name} · ${s.id.slice(0,8)}`}))} onChange={id=>{const selected=sourceOptions.find(s=>s.id===id)??null;setSource(selected);setDirectory(null);setDirectoryPage(1);form.setFieldValue("sql",selected?`SELECT ${selected.fields.map(f=>f.name).join(", ")} FROM result`:"");setPreview(null)}} placeholder="选择接口的数据来源"/></Form.Item>
 <Space size={16} style={{marginBottom:24}}><Button disabled={sourcePage===1||sources.loading} onClick={()=>setSourcePage(p=>p-1)}>上一页成果</Button><span>第 {sourcePage} 页</span><Button disabled={!sources.data?.has_more||sources.loading} onClick={()=>setSourcePage(p=>p+1)}>下一页成果</Button><Select aria-label="成果每页记录数" value={sourcePagePagination.pageSize} options={pageSizeOptions.map(value=>({value,label:`${value} 条/页`}))} onChange={size=>sourcePagePagination.onChange(1,size)}/></Space>
 {Boolean(sources.error)&&<Failure error={sources.error}/>}
 <Form.Item label="成果更新方式" name="mode"><Select options={[{value:"snapshot",label:"固定本次成果"},{value:"latest",label:"跟随同一数据流最新兼容的已发布成果"}]}/></Form.Item>
 <Form.Item label="查询 SQL" name="sql" rules={[{required:true}]} extra="result 代表所选成果。支持 SELECT *、字段选择、结尾分号、AND 条件、= / != / > / >= / < / <= 和单字段 ORDER BY；不支持跨表、函数或写操作。条件使用 :参数名，敏感字段不能作为筛选或排序条件。"><Input.TextArea rows={5} placeholder="SELECT code, name FROM result WHERE code = :code"/></Form.Item>
 {source&&<Typography.Paragraph type="secondary">可用字段：{source.fields.map(f=>`${f.name}（${f.logical_type}${f.sensitive?"，敏感":""}）`).join("、")}</Typography.Paragraph>}
 <Form.Item label="校验参数" name="parameters" extra={'使用 JSON 对象，值为文本。例如 {"code":"001"}；没有条件则填写 {}。'} rules={[{required:true}]}><Input.TextArea rows={3} placeholder={'{"code":"001"}，无条件时填写 {}'}/></Form.Item>
 <Form.Item label="每页最多返回条数" name="max_page_size" rules={[{required:true}]} extra="支持 1–1000 条；查询超时 8 秒、每页最多 8 MiB、每账号每接口每分钟 60 次。"><InputNumber min={1} max={1000} precision={0}/></Form.Item>
 <Form.Item label="关联业务目录" extra="接口与原库表共用业务目录；返回字段只能来自已审核共享的数据项。登记后单独完成接口挂载审核和授权，不继承库表使用权限。">
 <Alert type="info" showIcon title="同一目录，多种交付形式" description="选择所用治理成果已挂载的业务目录，接口作为该目录下的查询服务，不需要新建 API 目录。" style={{marginBottom:16}}/>
 <Input.Search style={{marginBottom:16}} placeholder="搜索目录名称" onSearch={value=>{setDirectorySearch(value);setDirectoryPage(1)}}/>
 <Select aria-label="关联业务目录" value={directory?.directory_id} loading={directories.loading} disabled={!source} placeholder="请选择来源成果所属业务目录" options={directoryOptions.map(d=>({value:d.directory_id,label:d.definition.name}))} onChange={id=>setDirectory(directoryOptions.find(d=>d.directory_id===id)??null)}/>
 </Form.Item><Pagination {...directoryPagePagination} size="small" total={directories.data?.total??0}/>
 {Boolean(directories.error)&&<Failure error={directories.error}/>}
 </Form>
 {preview&&<Card title="实际查询结果" style={{marginTop:24}}><Typography.Paragraph>识别参数：{preview.parameters.map(p=>`${p.name}（${p.type}，必填）`).join("、")||"无"}；最多预览 20 行。</Typography.Paragraph><Table rowKey={(_,index)=>String(index)} scroll={{x:true}} pagination={false} dataSource={preview.records} columns={preview.fields.map(f=>({title:f.name,dataIndex:f.name,render:(v:unknown)=>v==null?"—":String(v)}))}/></Card>}
 </PlatformDialog>;
}

export function ServiceAccess({serviceID,parameters,maxPageSize=50}:{serviceID:string;parameters:Parameter[];maxPageSize?:number}){
 const keys=useQuery<{items:{id:string;expires_at:string;revoked:boolean}[]}>(`/api/v1/data-services/${serviceID}/keys`);const action=useAction();const {message}=App.useApp();const [days,setDays]=useState(30);const [token,setToken]=useState("");
 async function create(){try{const result=await action.run<{token:string}>(`/api/v1/data-services/${serviceID}/keys`,{days});setToken(result.token);keys.reload()}catch(e){message.error(errorMessage(e))}}
 async function revoke(id:string){try{await action.run(`/api/v1/data-services/${serviceID}/keys/${id}/revoke`);if(token.startsWith(id+"."))setToken("");keys.reload()}catch(e){message.error(errorMessage(e))}}
 const payload={contract_version:1,parameters:Object.fromEntries(parameters.map(p=>[p.name,"填写参数值"])),page_size:Math.min(50,maxPageSize)};
 return <Space orientation="vertical" size={24} style={{width:"100%"}}><Alert showIcon title="外部调用凭据" description="仅已获数据使用授权的账号可创建。凭据绑定当前账号及本接口，调用时复查账号、字段授权、期限和共享状态；撤权或下架立即阻止后续调用。凭据不能赋予额外权限。"/>
 <label>凭据有效天数 <InputNumber aria-label="凭据有效天数" min={1} max={90} precision={0} value={days} onChange={v=>setDays(v??30)}/></label>
 <DialogActions><Button icon={<ApiOutlined aria-hidden/>} loading={action.busy} onClick={()=>void create()}>创建调用凭据</Button></DialogActions>
 {token&&<Alert type="warning" showIcon title="完整凭据仅显示本次，请妥善保存" description={<Typography.Paragraph copyable style={{overflowWrap:"anywhere",marginBottom:0}}>{token}</Typography.Paragraph>}/>}
 {keys.error?<Failure error={keys.error}/>:<Table rowKey="id" size="small" loading={keys.loading} dataSource={keys.data?.items} pagination={localPagination} scroll={{x:540}} columns={[{title:"凭据编号",render:(_,r)=>r.id.slice(0,12)},{title:"到期时间",render:(_,r)=>new Date(r.expires_at).toLocaleString()},{title:"状态",render:(_,r)=><Tag>{r.revoked?"已撤销":Date.parse(r.expires_at)<=Date.now()?"已过期":"有效"}</Tag>},{title:"操作",render:(_,r)=><Button danger disabled={r.revoked||action.busy} onClick={()=>void revoke(r.id)}>撤销</Button>}]}/>}
 <Typography.Title level={5}>调用说明</Typography.Title><Typography.Paragraph>POST {`/delivery/v1/services/${serviceID}/invoke`}<br/>请求头：Authorization: Bearer &lt;调用凭据&gt;，Content-Type: application/json</Typography.Paragraph><pre style={{whiteSpace:"pre-wrap",overflowWrap:"anywhere"}}>{JSON.stringify(payload,null,2)}</pre><Typography.Paragraph type="secondary">响应 records 为获批字段，敏感值按授权策略脱敏。next_cursor 非空时，原参数不变并传入 cursor 继续查询；分页锁定同一成果。401 表示凭据失效，403 表示授权不足，409 表示成果或共享状态变化，429 表示限流。接口不接收调用者 SQL。</Typography.Paragraph>
 </Space>;
}
