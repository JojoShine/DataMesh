// @vitest-environment jsdom
// Explicit opt-in: this creates new model/dataflow versions and a published snapshot in the local main environment.
import {afterEach,expect,it,vi} from "vitest";
import {readFile,writeFile} from "node:fs/promises";
import {resolve} from "node:path";
import {pathToFileURL} from "node:url";
import {request as httpsRequest} from "node:https";
import {cleanup,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {GovernanceCatalog} from "../src/pages/GovernanceCatalog";
import {GovernanceDataflows} from "../src/pages/GovernanceDataflows";
import {QualityPage} from "../src/pages/Governance";
import {Catalog} from "../src/pages/Catalog";
import {useSession} from "../src/stores/session";
import type {Principal} from "../src/api/types";
import type {GovernanceRecord,GovernanceFlowRun} from "../src/api/governance";

const enabled=process.env.DATAMESH_LIVE_GOVERNANCE==="1";
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it.skipIf(!enabled)("真实前端表单连接主环境：模型版本→数据流→1000行质量核验→Dialog发布",async()=>{
 const root=pathToFileURL(resolve(process.cwd(),"..")+"/");const ca=await readFile(new URL(".local/certs/server.crt",root));
 const auth: {username:string;password:string}=JSON.parse(await readFile(new URL(".local/bootstrap-admin.json",root),"utf8"));
 const evidenceFile=new URL(".local/governance-live-flow.json",root);
 const evidence: {live:{model_id:string;flow_id:string;ui?:{model_version:number;flow_version:number;run_id:string;published:boolean}}}=JSON.parse(await readFile(evidenceFile,"utf8"));
 let cookies="";
 const calls:{path:string;method:string;status:number}[]=[];
 const liveFetch=async(input:RequestInfo|URL,init?:RequestInit):Promise<Response>=>{
  const path=String(input);const url=new URL(path,"https://localhost:18443");
  if(url.origin!=="https://localhost:18443")throw new Error("Live validation only permits the named local main environment");
  const headers=new Headers(init?.headers);if(cookies)headers.set("Cookie",cookies);headers.set("Origin",url.origin);
  return new Promise((resolve,reject)=>{
   const req=httpsRequest(url,{method:init?.method??"GET",ca,headers:Object.fromEntries(headers.entries())},res=>{
    const chunks:Buffer[]=[];res.on("data",(chunk:Buffer)=>chunks.push(chunk));res.on("end",()=>{
     if(res.headers["set-cookie"])cookies=res.headers["set-cookie"].map(item=>item.split(";",1)[0]).join("; ");
     calls.push({path:url.pathname,method:init?.method??"GET",status:res.statusCode??500});
     resolve(new Response(res.statusCode===204?null:Buffer.concat(chunks),{status:res.statusCode??500,headers:{"Content-Type":"application/json"}}));
    });
   });req.on("error",reject);req.setTimeout(20000,()=>req.destroy(new Error("Main request timed out")));if(init?.body)req.write(String(init.body));req.end();
  });
 };
 vi.stubGlobal("fetch",liveFetch);
 const login=await liveFetch("/auth/login",{method:"POST",body:JSON.stringify({username:auth.username,password:auth.password}),headers:{"Content-Type":"application/json"}});expect(login.status).toBe(200);const principal=await login.json() as Principal;
 useSession.setState({user:principal,ready:true,epoch:0,identityError:null});
 Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}});
 const show=(child:React.ReactNode)=>render(<ConfigProvider theme={{token:{motion:false}}}><AntApp>{child}</AntApp></ConfigProvider>);
 const get=async<T,>(path:string):Promise<T>=>{const res=await liveFetch(path);expect(res.status).toBe(200);return res.json() as Promise<T>};
 const persist=async()=>{
  const latest=JSON.parse(await readFile(evidenceFile,"utf8"));
  await writeFile(evidenceFile,JSON.stringify({...latest,ui_calls:calls,live:{...latest.live,...evidence.live}},null,2),{mode:0o600});
 };
 try{
  const models=await get<{items:GovernanceRecord[]}>(`/api/v1/governance/models/${evidence.live.model_id}/versions`);const model2=models.items.find(row=>row.version===2);
  show(<GovernanceCatalog kind="model"/>);await userEvent.click(await screen.findByRole("button",{name:"客户基础标准模型（主环境实跑）"}));
  if(!model2){
   await userEvent.click(screen.getByRole("button",{name:"创建新版本"}));expect(screen.getAllByRole("dialog")).toHaveLength(1);
   await userEvent.click(within(screen.getByRole("dialog")).getByText("使用指南 · 模型、输出契约与建表"));expect(within(screen.getByRole("dialog")).getByText(/金额等精确小数字段需核对精度及小数位/)).toBeTruthy();
   await userEvent.click(screen.getAllByRole("button",{name:"保存草稿"})[0]);await waitFor(()=>expect(screen.queryByRole("dialog")).toBeNull());
   await userEvent.click(await screen.findByRole("button",{name:"客户基础标准模型（主环境实跑）"}));
  }
  if(model2?.state!=="published")await userEvent.click(screen.getByRole("button",{name:"发布此版本"}));
  await waitFor(async()=>expect((await get<GovernanceRecord>(`/api/v1/governance/models/${evidence.live.model_id}/versions/2`)).state).toBe("published"));cleanup();
  const priorRuns=await get<{items:GovernanceFlowRun[]}>(`/api/v1/governance/runs?kind=dataflow&definition_id=${evidence.live.flow_id}`);
  let runId=evidence.live.ui?.run_id??priorRuns.items.find(row=>row.definition.flow.version===2&&row.state==="succeeded")?.id;
  if(runId&&!evidence.live.ui){evidence.live.ui={model_version:2,flow_version:2,run_id:runId,published:false};await persist();}
  if(!runId){
   const flows=await get<{items:GovernanceRecord[]}>(`/api/v1/governance/dataflows/${evidence.live.flow_id}/versions`);const flow2=flows.items.find(row=>row.version===2);
   show(<GovernanceDataflows/>);await userEvent.click(await screen.findByRole("button",{name:"客户基础清洗与质量校验（主环境实跑）"}));
   if(!flow2){
    await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
    await userEvent.click(screen.getByRole("button",{name:"数据来源 source_1"}));await waitFor(()=>expect(screen.getByLabelText("来源资源").closest(".ant-select")?.textContent).toContain("数据库表"));
    await userEvent.click(screen.getByRole("button",{name:"模型输出 output_1"}));await userEvent.click(screen.getByLabelText("输出模型版本"));
    await userEvent.click(await screen.findByText("客户基础标准模型（主环境实跑） · v2",{selector:".ant-select-item-option-content"}));
    await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await screen.findByRole("button",{name:"发布此版本"});
   }
   if(flow2?.state!=="published"){
    await userEvent.type(screen.getByLabelText("破坏性变更迁移说明"),"本次仅切换到字段及约束一致的模型v2；保留v1及其已发布成果，现有下游继续固定旧版本，后续切换逐项核验。");
    await userEvent.click(screen.getByRole("button",{name:"发布此版本"}));
   }
   await screen.findByRole("button",{name:"运行已发布版本"});expect(screen.getAllByRole("dialog")).toHaveLength(1);await userEvent.click(screen.getByRole("button",{name:"运行已发布版本"}));
   await screen.findByText("合格输出：1000 行",{}, {timeout:20000});
   const runs=await get<{items:GovernanceFlowRun[]}>(`/api/v1/governance/runs?kind=dataflow&definition_id=${evidence.live.flow_id}`);const run=runs.items.find(row=>row.definition.flow.version===2&&row.state==="succeeded");if(!run)throw new Error("Missing main UI run");const actual=await get<GovernanceFlowRun>(`/api/v1/governance/runs/${run.id}`);expect(actual.result?.rejected_rows).toBe(0);
   runId=run.id;evidence.live.ui={model_version:2,flow_version:2,run_id:runId,published:false};await persist();cleanup();
  }
  show(<QualityPage/>);await screen.findAllByText("客户基础清洗与质量校验（主环境实跑）");
  const row=screen.getAllByRole("row").find(item=>item.textContent?.includes("客户基础清洗与质量校验（主环境实跑）"));if(!row)throw new Error("Missing main quality row");await userEvent.click(within(row).getByRole("button",{name:"运行检查"}));await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"运行检查"}));await waitFor(()=>expect(calls.some(call=>call.path===`/api/v1/datasets/${runId}/check`&&call.status===200)).toBe(true));cleanup();
  // The same deep link is offered in the real dataflow result.
  window.history.replaceState(null,"",`/platform/assets/publications?dataset_id=${runId}`);show(<Catalog publications/>);await screen.findByRole("dialog");await userEvent.click(within(screen.getByRole("dialog")).getByText("使用指南 · 治理成果核验与发布"));expect(screen.getAllByRole("dialog")).toHaveLength(1);
  if(!evidence.live.ui?.published)await userEvent.click(within(screen.getByRole("dialog")).getByRole("button",{name:"核验并发布"}));
  await waitFor(async()=>expect((await get<{publication_status:string}>(`/api/v1/datasets/${runId}`)).publication_status).toBe("published"));evidence.live.ui={model_version:2,flow_version:2,run_id:runId,published:true};await persist();
 }finally{await persist();await liveFetch("/auth/logout",{method:"POST",headers:{"X-CSRF-Token":principal.csrf_token}})}
},90000);
