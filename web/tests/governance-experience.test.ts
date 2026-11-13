// @vitest-environment jsdom
import {beforeEach,expect,it,vi} from "vitest";
import {request} from "../src/api/client";
import {useSession} from "../src/stores/session";
import type {GovernanceRecord,GovernanceFlowRun,GovernanceDataflow} from "../src/api/governance";
import type {Page,Principal} from "../src/api/types";
import type {LightweightFile} from "../src/api/governanceLightweight";
import {governanceExperience,parseCSV} from "../src/api/experience/governance";
import {sampleGraph} from "../src/api/experience/seed";
const user:Principal={user_id:"local-visitor",username:"experience",department_id:"体验部门",roles:["consumer"],platforms:["exchange","assets"],csrf_token:"fixture",public_test:true};
const root="/api/v1/governance";
const post=<T>(path:string,body:unknown={})=>request<T>(path,{method:"POST",body:JSON.stringify(body)});
beforeEach(()=>{localStorage.clear();history.replaceState(null,"","/platform/governance/dataflows");useSession.setState({user,ready:true,epoch:0});vi.stubGlobal("fetch",vi.fn(()=>{throw new Error("治理体验不能调用正式业务接口")}))});
it("真实保存、修订、发布并运行体验样本，保留结果和质量分流，不触碰后台",async()=>{
 const models=await request<Page<GovernanceRecord>>(root+"/models");expect(models.total).toBe(2);
 const original=(await request<Page<GovernanceRecord>>(root+"/dataflows")).items[0];
 const revised=await post<GovernanceRecord>(`${root}/dataflows/${original.id}/versions`,{definition:{...original.definition,name:"居民资料整理"},base_version:1,change_note:"体验修订"});expect(revised.version).toBe(2);
 await post(`${root}/dataflows/${original.id}/versions/2/publish`);
 const run=await post<GovernanceFlowRun>(`${root}/dataflows/${original.id}/versions/2/run`);
 expect(run.state).toBe("succeeded");expect(run.processed).toBe(80);expect(run.result).toMatchObject({output_rows:78,rejected_rows:2});
 const output=await request<Page<Record<string,unknown>>>(`${root}/runs/${run.id}/records?page_size=100`);expect(output.total).toBe(78);expect(output.items.every(row=>typeof row.person_name==="string"&&row.person_name===row.person_name.trim())).toBe(true);
 const history=await request<Page<GovernanceRecord>>(`${root}/dataflows/${original.id}/versions`);expect(history.total).toBe(2);
 expect((await request<GovernanceFlowRun>(`${root}/runs/${run.id}`)).id).toBe(run.id);
 expect(fetch).not.toHaveBeenCalled();expect(useSession.getState().user?.roles).toEqual(["consumer"]);
});
it("两个样本可关联，结果由实际数据计算，错误或自定义代码不会生成成功运行",async()=>{
 const design:GovernanceDataflow={nodes:[
 {id:"left",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"exp-residents",contract_version:1},fields:["person_id","person_name"]}},
 {id:"right",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"exp-enrollment",contract_version:1},fields:["person_id","registration_id"]}},
 {id:"join",kind:"join",inputs:[{node:"left"},{node:"right"}],join:{kind:"inner",left_keys:["person_id"],right_keys:["person_id"],columns:[{side:"left",source:"person_id",target:"person_id"},{side:"right",source:"registration_id",target:"registration_id"}]}},
 {id:"out",kind:"output",inputs:[{node:"join"}],infer_model:true}]};
 const definition={name:"报名与居民关联",description:"浏览器样本",owner_id:user.user_id,steward_id:user.user_id,dataflow:design};
 const flow=await post<GovernanceRecord>(root+"/dataflows",{definition,change_note:"创建"});await post(`${root}/dataflows/${flow.id}/versions/1/publish`);
 expect((await post<GovernanceFlowRun>(`${root}/dataflows/${flow.id}/versions/1/run`)).result?.output_rows).toBe(100);
 const code=structuredClone(sampleGraph());code.nodes.splice(1,0,{id:"script",kind:"script",inputs:[{node:"source_1"}],script:{language:"javascript",runtime_version:"anything",source:"fetch('https://example.invalid')",fields:[]}});code.nodes.find(node=>node.id==="clean_1")!.inputs=[{node:"script"}];
 const scripted=await post<GovernanceRecord>(root+"/dataflows",{definition:{...definition,name:"代码草稿",dataflow:code}});await post(`${root}/dataflows/${scripted.id}/versions/1/publish`);
 await expect(post(`${root}/dataflows/${scripted.id}/versions/1/run`)).rejects.toMatchObject({code:"EXPERIENCE_CODE_BLOCKED"});
 expect((await request<Page<GovernanceFlowRun>>(root+"/runs")).total).toBe(1);expect(fetch).not.toHaveBeenCalled();
});
it("CSV在浏览器内导入，可保存轻量处理并运行，隔离其他访问者",async()=>{
 expect(parseCSV('id,name\r\n1,"王,明"\r\n2,"李""华"\r\n')).toEqual([["id","name"],["1","王,明"],["2",'李"华']]);
 expect(()=>parseCSV('id,name\n1,"未闭合')).toThrow();
 const raw='id,name\n1, 王明 \n2, 李华 \n';const file=new File([raw],"居民.csv",{type:"text/csv"});Object.defineProperty(file,"text",{value:async()=>raw});
 const body=new FormData();body.append("file",file);const uploaded=await request<LightweightFile>("/api/v1/lightweight-governance/files",{method:"POST",body});expect(uploaded.rows).toBe(2);
 const units=await request<Page<{id:string;version:number}>>("/api/v1/lightweight-governance/units");const unit=units.items[0];const run=await post<GovernanceFlowRun>(`/api/v1/lightweight-governance/units/${unit.id}/run`,{expected_version:unit.version});expect(run.result?.output_rows).toBe(78);
 const other={...user,user_id:"another-browser-user"};const separate=await governanceExperience(root+"/runs",{},other);expect(separate).toMatchObject({total:0});
 expect(fetch).not.toHaveBeenCalled();
});
it("版本冲突和归档空间禁止写入；配置不调用外网或后台任务",async()=>{
 const flow=(await request<Page<GovernanceRecord>>(root+"/dataflows")).items[0];
 await expect(post(`${root}/dataflows/${flow.id}/versions`,{definition:flow.definition,base_version:99})).rejects.toMatchObject({code:"EXPERIENCE_STATE_CONFLICT"});
 await expect(post(root+"/alert-rules",{name:"回调",webhook:"http://127.0.0.1"})).rejects.toThrow("不请求外部回调");
 await expect(post(root+"/schedules",{name:"定时"})).rejects.toThrow("不创建后台定时任务");
 await expect(request(`${root}/dataflows/${flow.id}/versions/1/run`)).rejects.toMatchObject({status:405});
 await post(root+"/workspaces/exp-workspace/archive",{base_version:1});await expect(post(`${root}/dataflows/${flow.id}/versions/1/run`)).rejects.toMatchObject({code:"EXPERIENCE_STATE_CONFLICT"});
 expect(fetch).not.toHaveBeenCalled();
});
it("检查结果进入看板及质量趋势，模型草稿和CSV下载均来自本次运行",async()=>{
 const flow=(await request<Page<GovernanceRecord>>(root+"/dataflows")).items[0];const run=await post<GovernanceFlowRun>(`${root}/dataflows/${flow.id}/versions/1/run`);
 const report=await request<{quality_trend:{checked:number;passed:number;rejected:number}[]}>(root+"/dashboard");expect(report.quality_trend[0]).toMatchObject({checked:80,passed:78,rejected:2});
 const trends=await request<{items:{checked:number}[]}>(root+"/quality-trends");expect(trends.items[0].checked).toBe(80);
 expect((await request<Page<GovernanceRecord>>(root+"/models")).total).toBe(3);
 const {experienceCSV}=await import("../src/api/experience/governance");expect(experienceCSV(user,run.id).size).toBeGreaterThan(100);
 const profile=await post<{result:{rows:number;fields:{name:string;null_count:number}[]}}>(root+"/profiles",{resource:{resource_type:"table_resource",resource_id:"exp-residents",contract_version:1},fields:["person_name"],mode:"full"});expect(profile.result.rows).toBe(80);expect(profile.result.fields[0].null_count).toBe(1);
 await post(root+"/experience-reset");expect((await request<Page<GovernanceFlowRun>>(root+"/runs")).total).toBe(0);
});
it("工作流按依赖运行本地数据流，版本和归档要求保留",async()=>{
 const flow=(await request<Page<GovernanceRecord>>(root+"/dataflows")).items[0];const workflow=await post<GovernanceRecord>(root+"/workflows",{definition:{name:"居民数据检查工作流",description:"体验工作流",owner_id:user.user_id,steward_id:user.user_id,workflow:{tasks:[{id:"clean",kind:"dataflow",dataflow:{id:flow.id,version:1},depends_on:[],condition:"all_success"}]}}});await post(`${root}/workflows/${workflow.id}/versions/1/publish`);
 const run=await post<{id:string;state:string}>(`${root}/workflows/${workflow.id}/versions/1/run`);expect(run.state).toBe("succeeded");expect(await request(`${root}/runs/${run.id}/tasks`)).toMatchObject({items:[{task_id:"clean",state:"succeeded"}]});
 expect((await request<Page<GovernanceFlowRun>>(root+"/runs")).total).toBe(2);expect(fetch).not.toHaveBeenCalled();
});
it("清洗检查后筛选并分组汇总，输出计数与整数合计正确",async()=>{
 const design=sampleGraph();design.nodes=design.nodes.filter(node=>node.kind!=="output");design.nodes.push({id:"filter",kind:"filter",inputs:[{node:"quality_1",port:"good"}],condition:{field:"age",operator:"gte",value:30}},{id:"aggregate",kind:"aggregate",inputs:[{node:"filter"}],group_by:["district"],aggregates:[{operation:"count",source:"person_id",target:"people"},{operation:"sum",source:"age",target:"total_age"}]},{id:"out",kind:"output",inputs:[{node:"aggregate"}],infer_model:true});
 const flow=await post<GovernanceRecord>(root+"/dataflows",{definition:{name:"街道居民统计",description:"体验",owner_id:user.user_id,steward_id:user.user_id,dataflow:design}});await post(`${root}/dataflows/${flow.id}/versions/1/publish`);const run=await post<GovernanceFlowRun>(`${root}/dataflows/${flow.id}/versions/1/run`);const output=await request<Page<{people:number;total_age:number}>>(`${root}/runs/${run.id}/records`);
 expect(output.total).toBe(4);expect(output.items.reduce((n,row)=>n+row.people,0)).toBe(55);expect(output.items.reduce((n,row)=>n+row.total_age,0)).toBe(2806);
});
it("指标口径不会混入普通清洗流程，尚未开放的指标执行明确拒绝",async()=>{
 expect(await request(root+"/dataflows?purpose=metric")).toMatchObject({total:0});
 const metric=await post<GovernanceRecord>(root+"/dataflows",{definition:{name:"报名人数口径",description:"按年度统计",owner_id:user.user_id,steward_id:user.user_id,metric:{source:{resource:{resource_type:"table_resource",resource_id:"exp-enrollment",contract_version:1},fields:["enrollment_year","person_id"]},dimensions:["enrollment_year"],filters:[],aggregates:[{operation:"count",source:"person_id",target:"people"}],period:"all_time",time_field:"",from:"",until:""}}});
 expect(await request(root+"/dataflows?purpose=metric")).toMatchObject({total:1});
 await post(`${root}/dataflows/${metric.id}/versions/1/publish`);await expect(post(`${root}/dataflows/${metric.id}/versions/1/run`)).rejects.toThrow("指标执行需要正式治理环境");expect(await request(root+"/runs")).toMatchObject({total:0});
});
