import {usePagination} from "../hooks/usePagination";
import {useCallback,useEffect,useState} from "react";
import {Alert,App,Button,Checkbox,Col,Descriptions,Form,Input,InputNumber,Row,Select,Switch,Table,Tabs,Tag} from "antd";
import type {MetadataChangeReview,MetadataCollectionPage,MetadataCollectionPolicy,MetadataObservation,MetadataResource} from "../api/governance";
import {governanceChangeNames,metadataCollectionState} from "../api/governance";
import {errorMessage} from "../api/client";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {GovernanceDialogActions} from "../components/GovernanceDialog";
import {GovernanceImpact} from "../components/GovernanceImpact";
import {governanceFieldTooltip} from "../components/GovernanceFieldHelp";
import {Failure,Stamp,Title} from "../components/common";

function ChangeReview({object,review,onClose,onSaved,active,onBusyChange}:{object:MetadataResource;review:MetadataChangeReview;onClose:()=>void;onSaved:()=>void;active:boolean;onBusyChange?:(busy:boolean)=>void}){
 const [form]=Form.useForm<{state:"acknowledged"|"deferred";note:string;impact_checked:boolean}>();
 const action=useAction();const {message}=App.useApp();const [impactReady,setImpactReady]=useState(false);
 const impactLoaded=useCallback((ready:boolean)=>setImpactReady(ready),[]);
 useEffect(()=>{onBusyChange?.(active&&action.busy);return()=>onBusyChange?.(false)},[active,action.busy,onBusyChange]);
 const snapshot=useQuery<MetadataObservation>(`/api/v1/governance/metadata/${object.object_id}/observations/${review.observation_id}`);
 async function save(value:{state:"acknowledged"|"deferred";note:string;impact_checked:boolean}){try{await action.run(`/api/v1/governance/metadata/${object.object_id}/review`,{...value,observation_id:review.observation_id});message.success("已保存处理意见；共享交换登记内容和运行任务仍需分别核对处理");onSaved()}catch(e){message.error(errorMessage(e))}}
 return <><Title title="核查字段变化" description="确认哪些字段发生变化，以及相关模型、数据流和使用方是否需要调整。"/><GovernanceDialogActions visible={active}><Button disabled={action.busy||action.readOnly} onClick={onClose}>返回更新记录</Button><Button type="primary" form="metadata-change-review" htmlType="submit" disabled={!snapshot.data||!impactReady} loading={action.busy}>保存处理意见</Button></GovernanceDialogActions>
  <Alert className="section-gap" showIcon type="info" title="先确认变化，再处理下游" description="这里记录核查结论，不会自动修改共享交换登记内容、业务说明、模型或正在运行的任务。来源字段改名时，应人工核对原业务含义和引用，不能只按名称自动迁移。"/>
  <div className="governance-editor-panel"><h3>本次结构变化</h3>{snapshot.error?<Failure error={snapshot.error} retry={snapshot.reload}/>:<><Descriptions items={[{key:"version",label:"结构版本",children:snapshot.data?`v${snapshot.data.version}`:"读取中"},{key:"time",label:"发现时间",children:<Stamp value={snapshot.data?.created_at}/>} ]}/><Table rowKey={r=>`${r.code}:${r.field??""}`} loading={snapshot.loading} pagination={false} dataSource={snapshot.data?.changes} scroll={{x:640}} columns={[{title:"变化",render:(_,r)=>governanceChangeNames[r.code]??r.code},{title:"字段",dataIndex:"field"},{title:"影响",render:(_,r)=>r.breaking?"需要确认后续使用是否受影响":"通常可继续使用，仍需确认业务含义"}]}/></>}</div>
  <div className="governance-editor-panel"><h3>哪些模型、数据流或使用方可能受影响</h3><GovernanceImpact id={object.object_id} kind="resource" onReady={impactLoaded}/></div>
  <div className="governance-editor-panel"><Form id="metadata-change-review" layout="vertical" form={form} onFinish={save} initialValues={{state:"deferred",note:review.note}} disabled={action.busy||action.readOnly}>
   <Row gutter={16}><Col xs={24} md={8}><Form.Item label="核查结论" name="state" tooltip={governanceFieldTooltip("元数据核查结论")} rules={[{required:true}]}><Select placeholder="请选择核查结论" options={[{value:"deferred",label:"待协调处理（保留待办）"},{value:"acknowledged",label:"已核查并记录处理意见"}]}/></Form.Item></Col><Col xs={24} md={16}><Form.Item label="处理说明" name="note" rules={[{required:true,whitespace:true,message:"请说明影响、处理安排或无需调整的理由"},{max:4000}]}><Input.TextArea rows={3} maxLength={4000} placeholder="例如：客户等级新增为可空字段，现有数据流未引用；销售部补充含义后再调整登记的字段约定。"/></Form.Item></Col></Row>
   <Form.Item name="impact_checked" valuePropName="checked" rules={[{validator:(_,v:unknown)=>v===true?Promise.resolve():Promise.reject(new Error("请先核查影响范围并确认"))}]}><Checkbox disabled={!impactReady}>已核查当前可见的模型、数据流和使用方；无法查看的相关对象需联系对应责任人</Checkbox></Form.Item>
  </Form></div>
 </>
}

function CollectionSettings({object,policy,onClose,onSaved,active,onBusyChange}:{object:MetadataResource;policy:MetadataCollectionPolicy|null;onClose:()=>void;onSaved:()=>void;active:boolean;onBusyChange?:(busy:boolean)=>void}){
 const action=useAction();const {message}=App.useApp();
 useEffect(()=>{onBusyChange?.(active&&action.busy);return()=>onBusyChange?.(false)},[active,action.busy,onBusyChange]);
 async function save(value:{enabled:boolean;interval_minutes:number}){try{await action.run(`/api/v1/governance/metadata/${object.object_id}/collection`,{...value,expected_version:policy?.version??0});message.success(value.enabled?"自动更新设置已保存，后台将按计划检查字段结构":"自动更新已暂停");onSaved()}catch(e){message.error(errorMessage(e))}}
 return <><Title title="设置自动更新" description="安排平台多久检查一次字段结构，省去人工重复核对。"/><GovernanceDialogActions visible={active}><Button disabled={action.busy||action.readOnly} onClick={onClose}>返回更新记录</Button><Button type="primary" htmlType="submit" form="metadata-collection-settings" loading={action.busy}>保存更新设置</Button></GovernanceDialogActions>
  <Alert className="section-gap" showIcon type="info" title="检查字段结构，不同步业务数据" description="例如客户表增加了客户等级字段，平台会在下次检查时记录变化。默认每天检查一次；使用保存人的有效治理权限，权限到期后停止读取并提示。此设置不会同步、清洗或修改业务数据。"/>
  <Form id="metadata-collection-settings" layout="vertical" onFinish={save} initialValues={{enabled:policy?.enabled??true,interval_minutes:policy?.interval_minutes??1440}} disabled={action.busy||action.readOnly}>
   <Row gutter={16}><Col xs={24} md={12}><Form.Item name="enabled" label="自动检查字段结构" valuePropName="checked"><Switch checkedChildren="开启" unCheckedChildren="暂停"/></Form.Item></Col><Col xs={24} md={12}><Form.Item name="interval_minutes" label="每隔多久检查（分钟）" tooltip={governanceFieldTooltip("元数据采集间隔")} extra="例如：60 为每小时，1440 为每天，10080 为每周。" rules={[{required:true,message:"请输入15至43200分钟的整数"},{type:"integer",min:15,max:43200,message:"请输入15至43200分钟的整数"}]}><InputNumber className="full-width" min={15} max={43200} precision={0} placeholder="请输入15至43200，1440表示每天"/></Form.Item></Col></Row>
  </Form>
 </>
}

export function MetadataMaintenance({object,active=true,onBusyChange}:{object:MetadataResource;active?:boolean;onBusyChange?:(busy:boolean)=>void}){
 const [page,setPage,pagePagination]=usePagination();const [changesPage,setChangesPage,changesPagePagination]=usePagination();const [settings,setSettings]=useState(false);const [review,setReview]=useState<MetadataChangeReview|null>(null);
 const data=useQuery<MetadataCollectionPage>(`/api/v1/governance/metadata/${object.object_id}/collection?page=${page}&page_size=${pagePagination.pageSize}&changes_page=${changesPage}&changes_page_size=${changesPagePagination.pageSize}`,true);
 const action=useAction();const {message}=App.useApp();
 useEffect(()=>{onBusyChange?.(active&&action.busy);return()=>onBusyChange?.(false)},[active,action.busy,onBusyChange]);
 const policy=data.data?.policy;const status=metadataCollectionState(policy);
 useEffect(()=>{if(!active)return;const timer=window.setInterval(data.reload,10000);return ()=>window.clearInterval(timer)},[object.object_id,active]);
 async function collect(){try{await action.run(`/api/v1/governance/metadata/${object.object_id}/collect`,{expected_version:policy?.version??0});message.success("已申请检查字段结构，通常在一分钟内开始；结果会出现在更新记录中");data.reload()}catch(e){message.error(errorMessage(e));data.reload()}}
 if(settings)return <CollectionSettings active={active} onBusyChange={onBusyChange} object={object} policy={policy??null} onClose={()=>setSettings(false)} onSaved={()=>{setSettings(false);data.reload()}}/>;
 if(review)return <ChangeReview active={active} onBusyChange={onBusyChange} object={object} review={review} onClose={()=>setReview(null)} onSaved={()=>{setReview(null);data.reload()}}/>;
 return <><GovernanceDialogActions visible={active}><Button disabled={!data.data||action.busy} onClick={()=>setSettings(true)}>设置自动更新</Button><Button type="primary" disabled={!data.data||policy?.requested} loading={action.busy} onClick={collect}>{object.observation_kind==="registered_contract"?"记录当前登记结构":"立即检查一次"}</Button></GovernanceDialogActions>
  <p>这里检查字段有没有增加、删除或改变类型，避免使用方继续依赖过时的结构。发现变化后，需要确认相关模型、数据流和使用方是否受影响。</p>
  {data.error?<Failure error={data.error} retry={data.reload}/>:<>
   <div className="governance-editor-panel"><h3>字段结构是否保持更新</h3><Descriptions column={{xs:1,sm:2,lg:3}} items={[{key:"state",label:"更新状态",children:<Tag color={status.color}>{status.label}</Tag>},{key:"mode",label:"结构来自哪里",children:object.observation_kind==="registered_contract"?"共享交换登记的字段说明":"源数据库中的实际字段"},{key:"interval",label:"检查周期",children:policy?(policy.interval_minutes===1440?"每天一次":policy.interval_minutes===60?"每小时一次":`每${policy.interval_minutes}分钟一次`):"默认每天"},{key:"success",label:"最近成功",children:<Stamp value={policy?.last_success_at??undefined}/>},{key:"attempt",label:"最近尝试",children:<Stamp value={policy?.last_attempt_at??undefined}/>},{key:"next",label:"下次检查",children:policy&&(policy.enabled||policy.requested)?<Stamp value={policy.next_at}/>:"未安排"}]}/>
    {!policy&&<Alert showIcon type="info" title="等待安排自动检查" description="平台会在有效治理授权内安排每日检查；也可以由当前有权限的人员设置更新周期。没有有效授权时不会读取来源。"/>}
    {Boolean(policy?.failures)&&<Alert showIcon type="warning" title="本次检查未成功，以前的结构记录仍保留" description={`错误码：${policy?.error_code}；连续失败${policy?.failures}次。请核查执行人授权、来源状态和连接；连接失败或对象不可见不会被当作源表已删除。开启的计划按5至60分钟退避重试。`}/>}
    {object.observation_kind==="registered_contract"&&<Alert showIcon type="info" title="这里只记录登记的字段，不检查实际内容" description="文件、API及平台成果按已登记字段保存结构版本。这里不读取文件内容或调用API；要了解数据是否缺失、重复，需要在有效加工授权内单独检查数据情况。"/>}
   </div>
   <Tabs items={[{key:"changes",label:`待核查的变化（${data.data?.pending_total??data.data?.pending_reviews.length??0}）`,children:<div className="governance-editor-panel"><Table<MetadataChangeReview> rowKey="observation_id" loading={data.loading} dataSource={data.data?.pending_reviews} pagination={{...changesPagePagination,total:data.data?.pending_total??data.data?.pending_reviews.length}} scroll={{x:640}} locale={{emptyText:"暂无待处理变化；尚未检查时，不代表结构已经确认一致"}} columns={[{title:"变更记录",render:(_,r)=><Button type="link" onClick={()=>setReview(r)}>{`变化记录 · ${new Date(r.created_at).toLocaleDateString("zh-CN")}`}</Button>},{title:"发现时间",render:(_,r)=><Stamp value={r.created_at}/>},{title:"状态",render:(_,r)=>r.state==="deferred"?"待协调处理":"待核查"},{title:"已记录说明",render:(_,r)=>r.note||"尚未记录"}]}/></div>},{key:"history",label:"更新记录",children:<div className="governance-editor-panel"><Table rowKey="id" loading={data.loading} dataSource={data.data?.items} scroll={{x:800}} pagination={{...pagePagination,total:data.data?.total}} columns={[{title:"检查时间",render:(_,r)=><Stamp value={r.created_at}/>},{title:"触发原因",render:(_,r)=>({initial:"首次检查",scheduled:"定时检查",resource_changed:"资源或来源版本变化",manual:"手动申请"}[r.trigger])},{title:"结果",render:(_,r)=>r.state==="succeeded"?<Tag color="success">成功</Tag>:<Tag color="error">{r.state==="blocked"?"权限待核查":"失败"}</Tag>},{title:"结构版本",render:(_,r)=>r.state==="succeeded"?(r.changed?"已记录新结构版本":"字段未变化，沿用原结构版本"):"保留以前的结构版本"},{title:"错误码",render:(_,r)=>r.error_code||"—"}]}/></div>}]}/>
  </>}
 </>
}
