// @vitest-environment jsdom
import {navigate} from "../src/app/navigation";
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,fireEvent,render,screen,waitFor,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {initializeFlowColumns,flowEditorFields} from "../src/pages/GovernanceFlowGraph";
import type {GovernanceDataflow,MetadataResource} from "../src/api/governance";
import {GovernanceDataflows,GovernanceFlowRunView} from "../src/pages/GovernanceDataflows";
import {useSession} from "../src/stores/session";
const field={name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null};
const source:MetadataResource={object_id:"source-resource",source_id:"source",qualified_name:"public.customers",schema_version:3,resource_type:"table_resource",adapter:"database",observation_kind:"live_schema",fields:[field,{...field,name:"name",logical_type:"string",primary_key:false}]};
const graph={nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"source-resource",contract_version:3},fields:["id","name"]}},{id:"output_1",kind:"output",inputs:[{node:"source_1"}],model:{id:"model",version:2}}]};
const record={id:"flow",kind:"dataflow",department_id:"test",latest_version:1,version:1,state:"published",definition:{name:"客户治理",owner_id:"governor",steward_id:"governor",dataflow:graph},created_at:"2026-10-21T01:00:00Z"};
const model={id:"model",kind:"model",department_id:"test",latest_version:2,version:2,state:"published",definition:{name:"客户输出模型",model:{layer:"logical",fields:source.fields}}};
vi.setConfig({testTimeout:15000});
beforeEach(()=>{history.replaceState(null,"","/platform/governance/dataflows");useSession.setState({user:{user_id:"governor",username:"治理人员",department_id:"test",roles:["governor"],platforms:["governance"],csrf_token:"test"},ready:true,epoch:0,identityError:null});Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener:vi.fn(),removeListener:vi.fn(),addEventListener:vi.fn(),removeEventListener:vi.fn()}))});vi.stubGlobal("ResizeObserver",class{observe(){}unobserve(){}disconnect(){}})});
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
function show(){render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceDataflows/></AntApp></ConfigProvider>)}
function fixture(posts:{path:string;body:unknown}[]){vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
 if(path.includes("/schedules/preview"))return Response.json({items:["2030-01-01T01:00:00Z","2030-01-01T02:00:00Z","2030-01-01T03:00:00Z","2030-01-01T04:00:00Z","2030-01-01T05:00:00Z"],timezone:"Asia/Shanghai"});
 if(init?.method==="POST"){posts.push({path,body:JSON.parse(String(init.body))});return new Response(JSON.stringify(path.endsWith("/run")?{id:"flow-run",kind:"dataflow",state:"queued"}:{...record,state:"draft"}),{status:201})}
 if(path==="/api/v1/governance/dataflows/flow/versions/1")return Response.json(record);
 const run={id:"flow-run",kind:"dataflow",state:"succeeded",processed:2,definition:{flow:{id:"flow",version:1},pinned:[{kind:"dataflow",ref:{id:"flow",version:1},definition:record.definition}]},result:{model:{id:"model",version:2},output_rows:2,rejected_rows:0,nodes:[{node_id:"source_1",input_count:2,output_count:2,duration_ms:5}],lineage:{},snapshot_scope:"per_source_statement"}};
 const page=(items:unknown[])=>({items,total:items.length,page:1,page_size:20});
 return new Response(JSON.stringify(path==="/api/v1/governance/runs/flow-run"?run:path.includes("/records")?{...page([{id:1,name:"Alice"}]),model:{id:"model",version:2}}:path.includes("/events")?page([{id:1,code:"FLOW_COMPLETED",node_id:"",processed:2}]):path.startsWith("/api/v1/governance/runs?")?page([]):path.includes("/metadata/source-resource/observations")?{object:source}:path.startsWith("/api/v1/governance/metadata?")?page([source]):path.endsWith("/members")?{items:[{user_id:"governor",username:"治理人员",department_id:"test"}]}:path.startsWith("/api/v1/governance/models?")?page([model]):path.includes("/models/model/versions/2")?model:path.endsWith("/dataflows/flow/versions")?page([record]):path.startsWith("/api/v1/governance/dataflows?")?page([record]):page([])));
}));}
it("运行节点统计和日志同时展示固定选择信息，质量摘要预留统一间距",async()=>{
 fixture([]);const original=globalThis.fetch;
 const runGraph={nodes:[graph.nodes[0],{id:"aggregate_1",kind:"aggregate",inputs:[{node:"source_1"}],group_by:["name"],aggregates:[{source:"id",target:"count",operation:"count"}]},{...graph.nodes[1],inputs:[{node:"aggregate_1"}]}]};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(path==="/api/v1/governance/runs/flow-run")return Response.json({id:"flow-run",kind:"dataflow",state:"succeeded",definition:{flow:{id:"flow",version:1},pinned:[{kind:"dataflow",ref:{id:"flow",version:1},definition:{...record.definition,dataflow:runGraph}},{kind:"model",ref:{id:"model",version:2},definition:model.definition}],inputs:{source_1:{resource:graph.nodes[0].source?.resource,fields:source.fields}}},result:{model:{id:"model",version:2},output_rows:2,rejected_rows:0,nodes:[{node_id:"source_1",input_count:2,output_count:2},{node_id:"aggregate_1",input_count:2,output_count:2},{node_id:"output_1",input_count:2,output_count:2}],lineage:{}}});
  if(path.includes("/runs/flow-run/events"))return Response.json({items:[{id:1,node_id:"aggregate_1",code:"FLOW_NODE_COMPLETED",processed:2}],total:1});
  return original(path,init);
 }));
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceFlowRunView id="flow-run" initialTab="summary"/></AntApp></ConfigProvider>);
 await screen.findByText("按name分组；id计数 → count");expect(screen.getByText(/输出模型：客户输出模型 · v2/)).toBeTruthy();expect(await screen.findByText(/public.customers · v3；选取：id、name/)).toBeTruthy();
 expect(screen.getByRole("region",{name:"质量检查与治理"}).classList.contains("governance-quality-run-summary")).toBe(true);
 await userEvent.click(screen.getByRole("tab",{name:"执行日志"}));expect(await screen.findByText("按name分组；id计数 → count")).toBeTruthy();
});
it("质量步骤保存检查方案自动带入真实字段，不提交数据流或发布",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加整理步骤"}));await userEvent.click(await screen.findByRole("menuitem",{name:"检查数据质量"}));
 await userEvent.click(screen.getByRole("button",{name:"保存为检查方案"}));const dialog=screen.getByRole("dialog");await userEvent.type(within(dialog).getByLabelText("方案名称"),"通用编号要求");
 await userEvent.click(within(dialog).getByRole("button",{name:"保存方案草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/quality-templates",body:{base_version:0,definition:{quality_template:{fields:source.fields,checks:[{field:"id",kind:"required",blocking:true}]}}}});
 expect(screen.queryByRole("dialog")).toBeNull();expect(screen.getByLabelText("数据流名称")).toBeTruthy();
});
it("检查方案经明确应用后只替换当前检查步骤，其他处理保持原样",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 history.replaceState(null,"","/platform/governance/dataflows?quality_plan=plan&plan_version=2");
 const quality={id:"quality_1",kind:"quality",inputs:[{node:"source_1"}],checks:[{field:"id",kind:"required",blocking:true}]};
 const withQuality={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],quality,{...graph.nodes[1],inputs:[{node:"quality_1",port:"good"}]}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[withQuality],total:1}):path==="/api/v1/governance/quality-templates/plan/versions/2"?Response.json({id:"plan",version:2,state:"published",definition:{name:"通用检查",quality_template:{fields:[field],reference_fields:[],checks:quality.checks}}}):original(path,init)));
 show();await screen.findByText("使用检查方案：通用检查 · v2");await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));await userEvent.click(screen.getByText("定规则",{selector:".ant-steps-item-title"}));
 expect(screen.getByRole("button",{name:"应用到当前检查步骤"}).closest(".governance-flow-page-footer")).toBeTruthy();await userEvent.click(screen.getByRole("button",{name:"应用到当前检查步骤"}));expect(posts).toHaveLength(0);
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/dataflows/flow/versions",body:{definition:{dataflow:{nodes:[graph.nodes[0],{...quality,template:{id:"plan",version:2},checks:[]},{...graph.nodes[1],inputs:[{node:"quality_1",port:"good"}]}]}}}});
});
it("完整运行生成的模型直接打开已保存模型记录",async()=>{
 fixture([]);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  const response=await original(path,init);if(path!=="/api/v1/governance/runs/flow-run")return response;
  const result=await response.json();return Response.json({...result,workspace_id:"customer-space",result:{...result.result,unmodeled:true,generated_model:{id:"saved-model",version:1}}});
 }));
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><GovernanceFlowRunView id="flow-run"/></AntApp></ConfigProvider>);
 await userEvent.click(await screen.findByRole("button",{name:"查看已生成模型"}));
 expect(location.pathname+location.search).toBe("/platform/governance/models?model_id=saved-model&version=1&from_run=flow-run&workspace_id=customer-space");
});
it("默认按业务步骤整理数据，添加和移除规则自动衔接到结果",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();
 await screen.findByRole("button",{name:"客户治理"});
 expect(await screen.findByRole("region",{name:"业务处理步骤"})).toBeTruthy();
 expect(screen.queryByRole("img",{name:"治理数据流图"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
 expect(screen.queryByLabelText("source_1 输入 1")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"添加整理步骤"}));
 await userEvent.click(await screen.findByRole("menuitem",{name:"清理文字"}));
 expect(screen.queryByLabelText("transform_1 输入 1")).toBeNull();
 await userEvent.click(screen.getByLabelText("当前节点配置项"));await userEvent.click(await screen.findByText("2. 中文名称待补充（name）",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("name 的转换"));await userEvent.click(await screen.findByText("去除首尾空白",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByRole("button",{name:"高级编排"}));
 expect(await screen.findByRole("img",{name:"治理数据流图"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"业务步骤"}));
 await userEvent.click(screen.getByRole("button",{name:"添加整理步骤"}));
 await userEvent.click(await screen.findByRole("menuitem",{name:"检查数据质量"}));
 await userEvent.click(screen.getByRole("button",{name:"移除此步骤"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:[
  expect.objectContaining({id:"source_1"}),
  expect.objectContaining({id:"transform_1",inputs:[{node:"source_1"}],columns:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"}]}),
  expect.objectContaining({id:"output_1",inputs:[{node:"transform_1"}],model:{id:"model",version:2}})
 ]}}});
});
it("复杂关系默认可阅读业务步骤，添加步骤引导高级编排且不改动原图",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const complex={...record,definition:{...record.definition,dataflow:{nodes:[...graph.nodes,{id:"other",kind:"filter",inputs:[{node:"source_1"}],condition:{field:"id",operator:"not_null",value:null}}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[complex],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 expect(screen.getByRole("region",{name:"业务处理步骤"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"添加整理步骤"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:complex.definition.dataflow}});
});
it("按已发布版本运行真实治理流程并在独立页面查看模型结果与节点统计",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));
 expect(await screen.findByRole("img",{name:"治理数据流图"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:/^运\s*行$/}));
 await waitFor(()=>expect(posts).toEqual([{path:"/api/v1/governance/dataflows/flow/versions/1/run",body:{}}]));
 expect(await screen.findByText("合格输出：2 行")).toBeTruthy();await userEvent.click(screen.getByRole("tab",{name:"处理结果"}));expect(await screen.findByText("Alice")).toBeTruthy();expect(screen.queryByRole("dialog")).toBeNull();expect(screen.getByRole("region",{name:"数据流编排工作区"})).toBeTruthy();
});
it("修订通过节点配置添加清洗，保存独立版本并保留来源与模型版本",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加处理算子"}));await userEvent.click(await screen.findByRole("menuitem",{name:"添加文本转换"}));
 await userEvent.click(screen.getByLabelText("当前节点配置项"));await userEvent.click(await screen.findByText("2. 中文名称待补充（name）",{selector:".ant-select-item-option-content"}));await userEvent.click(screen.getByLabelText("name 的转换"));await userEvent.click(await screen.findByText("去除首尾空白",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].path).toBe("/api/v1/governance/dataflows/flow/versions");expect(posts[0].body).toMatchObject({base_version:1,definition:{dataflow:{nodes:expect.arrayContaining([expect.objectContaining({kind:"source",source:{resource:{resource_type:"table_resource",resource_id:"source-resource",contract_version:3},fields:["id","name"]}}),expect.objectContaining({kind:"transform",columns:expect.arrayContaining([{source:"name",target:"name",rule:"trim"}])}),expect.objectContaining({kind:"output",model:{id:"model",version:2}})])}}});
 expect(screen.queryByRole("dialog")).toBeNull();expect(screen.getByRole("region",{name:"数据流编排工作区"})).toBeTruthy();
});
it("同名来源按类型与编号区分，并保留当前库表契约",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/metadata?")?Response.json({items:[source,{...source,object_id:"old-registry",resource_type:"raw_object"}],total:2,page:1,page_size:20}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));await userEvent.click(screen.getByLabelText("来源资源"));
 expect(await screen.findByText("public.customers · 数据库表 · v3 · source-r",{selector:".ant-select-item-option-content"})).toBeTruthy();expect(await screen.findByText("public.customers · 采集对象 · v3 · old-regi",{selector:".ant-select-item-option-content"})).toBeTruthy();expect(screen.queryByRole("dialog")).toBeNull();expect(screen.getByRole("region",{name:"数据流编排工作区"})).toBeTruthy();expect(posts).toHaveLength(0);
});

it("先添加算子再选择来源，会把字段带入连续的空节点，文本清洗可保存",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(screen.getByRole("button",{name:"新建数据流"}));
 await userEvent.type(screen.getByLabelText("数据流名称"),"先搭处理步骤");
 for(const name of ["添加字段投影","添加文本转换"]){await userEvent.click(screen.getByRole("button",{name:"添加处理算子"}));await userEvent.click(await screen.findByRole("menuitem",{name}));}
 expect(screen.getByText("上一步还没有可处理的字段")).toBeTruthy();
 fireEvent.click(screen.getByRole("button",{name:"数据来源 source_1"}));
 await userEvent.click(screen.getByLabelText("来源资源"));await userEvent.click(await screen.findByText("public.customers · 数据库表 · v3 · source-r",{selector:".ant-select-item-option-content"}));
 fireEvent.click(screen.getByRole("button",{name:"文本转换 transform_1"}));
 expect(screen.getByText("上一步提供 2 个字段；本节点输出 2 个字段。只输出已配置的字段，其余字段不会继续传递。")).toBeTruthy();
 await userEvent.click(screen.getByLabelText("当前节点配置项"));await userEvent.click(await screen.findByText("2. 中文名称待补充（name）",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("name 的转换"));await userEvent.click(await screen.findByText("去除首尾空白",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:expect.arrayContaining([
  expect.objectContaining({kind:"project",columns:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"copy"}]}),
  expect.objectContaining({kind:"transform",columns:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"}]})
 ])}}});expect(screen.queryByRole("dialog")).toBeNull();expect(screen.getByRole("region",{name:"数据流编排工作区"})).toBeTruthy();
});

it("既有空转换可以明确带入字段，删除配置后不擅自补回，数字不提供文本清洗",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const empty={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],{id:"transform_1",kind:"transform",inputs:[{node:"source_1"}],columns:[]},{...graph.nodes[1],inputs:[{node:"transform_1"}]}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[empty],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));fireEvent.click(screen.getByRole("button",{name:"文本转换 transform_1"}));
 await userEvent.click(screen.getByRole("button",{name:"带入上一步全部字段"}));
 await userEvent.click(screen.getByLabelText("当前节点配置项"));await userEvent.click(await screen.findByText("1. 中文名称待补充（id）",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("id 的转换"));
 expect((await screen.findByText("去除首尾空白",{selector:".ant-select-item-option-content"})).closest(".ant-select-item")?.classList.contains("ant-select-item-option-disabled")).toBe(true);
 await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByRole("button",{name:"移除此字段"}));await userEvent.click(screen.getByRole("button",{name:"移除此字段"}));
 expect(screen.getByText("上一步字段已就绪，本节点尚未配置输出")).toBeTruthy();expect(screen.queryByLabelText("来源字段")).toBeNull();expect(posts).toHaveLength(0);
});

it("字段带入按依赖顺序处理，保留已配置的改名和清洗，其他分支不受影响",()=>{
 const resources:Record<string,MetadataResource>={"source-resource":source};
 const graph:GovernanceDataflow={nodes:[
  {id:"last",kind:"transform",inputs:[{node:"renamed"}],columns:[]},
  {id:"renamed",kind:"transform",inputs:[{node:"project"}],columns:[{source:"name",target:"customer_name",rule:"trim"}]},
  {id:"project",kind:"project",inputs:[{node:"source"}],columns:[]},
  {id:"source",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"source-resource",contract_version:3},fields:["id","name"]}},
  {id:"other",kind:"transform",inputs:[{node:"unrelated"}],columns:[]},
  {id:"unrelated",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"source-resource",contract_version:3},fields:["id"]}}
 ]};
 const next=initializeFlowColumns(graph,"source",resources);
 expect(next.nodes.find(n=>n.id==="project")?.columns).toHaveLength(2);
 expect(next.nodes.find(n=>n.id==="renamed")?.columns).toEqual([{source:"name",target:"customer_name",rule:"trim"}]);
 expect(next.nodes.find(n=>n.id==="last")?.columns).toEqual([{source:"customer_name",target:"customer_name",rule:"copy"}]);
 expect(flowEditorFields(next,{node:"last"},resources).map(f=>f.name)).toEqual(["customer_name"]);
 expect(next.nodes.find(n=>n.id==="other")?.columns).toEqual([]);expect(graph.nodes[2].columns).toEqual([]);
});

it("进入页面直接展开首个数据流，列表与节点配置可收起，所有命令保留在工作区",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 expect(await screen.findByRole("img",{name:"治理数据流图"})).toBeTruthy();expect(screen.queryByRole("dialog")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"收起数据流列表"}));expect(screen.queryByRole("complementary",{name:"数据流列表"})).toBeNull();
 expect(screen.getByRole("button",{name:/^运\s*行$/}).closest("footer")?.getAttribute("aria-label")).toBe("数据流操作");
 await userEvent.click(screen.getByRole("button",{name:"收起节点配置"}));expect(screen.queryByRole("region",{name:"节点配置"})).toBeNull();
 fireEvent.click(screen.getByRole("button",{name:"public.customers public.customers"}));expect(screen.getByRole("region",{name:"节点配置"})).toBeTruthy();expect(posts).toHaveLength(0);
});

it("左右切换数据流前保护未保存修改，继续编辑与明确放弃都不创建弹窗",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const product={...record,id:"product-flow",definition:{...record.definition,name:"商品治理"}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[record,product],total:2,page:1,page_size:20}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await screen.findByRole("button",{name:"基于此版本修订"});await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加处理算子"}));await userEvent.click(await screen.findByRole("menuitem",{name:"添加文本转换"}));
 await userEvent.click(screen.getByRole("button",{name:"商品治理"}));expect(screen.getByText("当前配置尚未保存")).toBeTruthy();expect(screen.queryByRole("dialog")).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"继续编辑"}));expect(screen.getByRole("button",{name:"文本转换 transform_1"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"商品治理"}));await userEvent.click(screen.getByRole("button",{name:"放弃修改并切换"}));
 expect(await screen.findByRole("heading",{name:"数据流设计 · 商品治理"})).toBeTruthy();expect(screen.queryByRole("button",{name:"文本转换 transform_1"})).toBeNull();expect(posts).toHaveLength(0);
});

it("保存并切换会先保存当前修订，再展示选中的另一数据流",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const product={...record,id:"product-flow",definition:{...record.definition,name:"商品治理"}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[record,product],total:2,page:1,page_size:20}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加处理算子"}));await userEvent.click(await screen.findByRole("menuitem",{name:"添加文本转换"}));
 await userEvent.click(screen.getByRole("button",{name:"商品治理"}));await userEvent.click(screen.getByRole("button",{name:"保存后切换"}));
 expect(await screen.findByRole("heading",{name:"数据流设计 · 商品治理"})).toBeTruthy();expect(posts).toHaveLength(1);
 expect(posts[0]).toMatchObject({path:"/api/v1/governance/dataflows/flow/versions",body:{base_version:1,definition:{name:"客户治理",dataflow:{nodes:expect.arrayContaining([expect.objectContaining({kind:"transform"})])}}}});
 expect(screen.queryByRole("dialog")).toBeNull();
});
it("新建数据流默认先处理后建模，也能切换已有模型",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(screen.getByRole("button",{name:"新建数据流"}));
 fireEvent.click(screen.getByRole("button",{name:"结果输出 output_1"}));
 expect(screen.getByText("根据结果生成模型",{exact:true})).toBeTruthy();
 expect(screen.queryByLabelText("输出模型版本")).toBeNull();
 await userEvent.click(screen.getByLabelText("结果结构"));
 await userEvent.click(await screen.findByText("使用已有模型",{selector:".ant-select-item-option-content"}));
 expect(screen.getByLabelText("输出模型版本")).toBeTruthy();
 expect(posts).toHaveLength(0);
});
it("新建专注编排，检查缺失项能定位且不会保存或启动",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(screen.getByRole("button",{name:"新建数据流"}));
 expect(screen.queryByRole("tab",{name:"前置与字段核对"})).toBeNull();expect(screen.queryByRole("tab",{name:"运行结果"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"保存并运行"}));
 expect(screen.getByText("请选择要读取的数据表。")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"定位数据来源 · source_1"}));expect(screen.getByLabelText("来源资源")).toBeTruthy();expect(posts).toHaveLength(0);
});
it("保存并运行依次调用保存、发布校验和运行，自动打开结果",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"&&path.endsWith("/publish")){posts.push({path,body:JSON.parse(String(init.body))});return Response.json(record)}
  return original(path,init);
 }));show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"保存并运行"}));
 await waitFor(()=>expect(posts.map(p=>p.path)).toEqual(["/api/v1/governance/dataflows/flow/versions","/api/v1/governance/dataflows/flow/versions/1/publish","/api/v1/governance/dataflows/flow/versions/1/run"]));
 expect(await screen.findByText("Alice")).toBeTruthy();expect(screen.getByRole("tab",{name:"运行结果"})).toBeTruthy();
});
it("发布校验失败保留已保存草稿，不发起运行",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(init?.method==="POST"&&path.endsWith("/publish")){posts.push({path,body:JSON.parse(String(init.body))});return Response.json({code:"MODEL_MIGRATION_REQUIRED",message:"请说明如何保留旧版本",trace_id:"test"},{status:409})}
  return original(path,init);
 }));show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"保存并运行"}));
 await screen.findByText(/检查未完成/);expect(posts).toHaveLength(2);expect(posts.some(p=>p.path.endsWith("/run"))).toBe(false);
 expect(screen.getByRole("button",{name:"检查并运行"})).toBeTruthy();expect(screen.getByLabelText("破坏性变更迁移说明")).toBeTruthy();expect(screen.queryByRole("dialog")).toBeNull();
});

it("关联未选择输出字段时保存定位节点，补齐两侧字段后可保存且重复添加不会产生重名",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const joined={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],{...graph.nodes[0],id:"source_2"},{id:"join_1",kind:"join",inputs:[{node:"source_1"},{node:"source_2"}],join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[]}},{id:"output_1",kind:"output",infer_model:true,inputs:[{node:"join_1"}]}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>!init?.method&&path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[joined],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));expect(posts).toHaveLength(0);
 expect(screen.getByText("请选择关联结果要保留的字段：添加左侧或右侧输出字段。")).toBeTruthy();expect(screen.getByRole("heading",{name:"多表关联 · join_1"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"添加左侧输出字段"}));await userEvent.click(screen.getByRole("button",{name:"添加左侧输出字段"}));await userEvent.click(screen.getByRole("button",{name:"添加右侧输出字段"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:expect.arrayContaining([expect.objectContaining({id:"join_1",join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[{side:"left",source:"id",target:"id"},{side:"left",source:"name",target:"name"},{side:"right",source:"id",target:"right_id"},{side:"right",source:"name",target:"right_name"}]}})])}}});
});

it("来源节点和右侧详情显示所选表的业务名称、技术表名与介绍",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/metadata?")?Response.json({items:[{...source,display_name:"教育局学生基础信息",description:"用于按儿童编号补充户籍信息",provider_name:"教育局"}],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));
 fireEvent.click(await screen.findByRole("button",{name:"教育局学生基础信息 public.customers"}));
 expect(screen.getByRole("heading",{name:"教育局学生基础信息"})).toBeTruthy();expect(screen.getByText("用于按儿童编号补充户籍信息")).toBeTruthy();expect(screen.getByText("教育局",{selector:".ant-descriptions-item-content"})).toBeTruthy();expect(posts).toHaveLength(0);
});

it("两份输入接好后带入关联输出，保留原有关联配置和其他分支",()=>{
 const base:GovernanceDataflow={nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:source.object_id,contract_version:3},fields:["id","name"]}},{id:"source_2",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:source.object_id,contract_version:3},fields:["id","name"]}},{id:"join_1",kind:"join",inputs:[{node:"source_1"},{node:"source_2"}],join:{kind:"inner",left_keys:[],right_keys:[],columns:[]}},{id:"output_1",kind:"output",infer_model:true,inputs:[{node:"join_1"}]}]};
 const initialized=initializeFlowColumns(base,"join_1",{[source.object_id]:source});
 expect(initialized.nodes[2].join?.columns.map(c=>c.target)).toEqual(["id","name","right_id","right_name"]);
 expect(initialized.nodes[2].join?.left_keys).toEqual([]);
 const configured:GovernanceDataflow={nodes:initialized.nodes.map(n=>n.id==="join_1"?{...n,join:{...n.join!,columns:[{side:"left",source:"name",target:"student_name"}]}}:n)};
 expect(initializeFlowColumns(configured,"source_1",{[source.object_id]:source}).nodes[2].join?.columns).toEqual([{side:"left",source:"name",target:"student_name"}]);
});

it("基础信息在右侧编辑，切换节点保留输入并按原契约保存",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));await userEvent.click(await screen.findByRole("button",{name:"客户治理"}));await userEvent.click(screen.getByRole("button",{name:"基于此版本修订"}));
 expect(screen.queryByRole("region",{name:"基础信息"})).toBeNull();expect(screen.getByLabelText("数据流名称").closest("header")).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"基础信息"}));
 const description=screen.getByPlaceholderText("请说明处理目标与使用范围");expect(description.closest("section")).toBe(screen.getByRole("region",{name:"基础信息"}));
 fireEvent.change(description,{target:{value:"补充学生户籍信息"}});
 fireEvent.click(screen.getByRole("button",{name:"public.customers public.customers"}));expect(screen.getByRole("region",{name:"节点配置"})).toBeTruthy();expect(screen.queryByRole("region",{name:"基础信息"})).toBeNull();
 await userEvent.click(screen.getByRole("button",{name:"基础信息"}));expect(screen.getByPlaceholderText("请说明处理目标与使用范围")).toHaveProperty("value","补充学生户籍信息");
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0].body).toMatchObject({definition:{name:"客户治理",description:"补充学生户籍信息",owner_id:"governor",steward_id:"governor"}});
});
it("业务步骤可添加关联表、选择匹配字段并保存真实多表关系",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const household:MetadataResource={...source,object_id:"household-resource",qualified_name:"public.households",display_name:"户籍信息",field_labels:{id:"儿童编号",guardian_name:"监护人姓名"},fields:[field,{...field,name:"guardian_name",logical_type:"string",primary_key:false}]};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(!init?.method&&path.startsWith("/api/v1/governance/metadata?"))return Response.json({items:[source,household],total:2,page:1,page_size:20});
  if(path.includes("/metadata/household-resource/observations"))return Response.json({object:household,items:[]});
  return original(path,init);
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加关联表"}));
 expect(screen.queryByRole("img",{name:"治理数据流图"})).toBeNull();
 await userEvent.click(screen.getByLabelText("来源资源"));
 await userEvent.click(await screen.findByText(/户籍信息（public.households）/,{selector:".ant-select-item-option-content"}));
 await userEvent.click(within(screen.getByRole("navigation",{name:"业务阶段"})).getByText("定规则"));
 await userEvent.click(screen.getByRole("button",{name:/^关联数据表/}));
 expect(screen.queryByLabelText("join_1 输入 1")).toBeNull();
 await userEvent.click(screen.getByLabelText("已有数据的对应字段"));
 await userEvent.click(await screen.findByText("中文名称待补充（id）",{selector:".ant-select-item-option-content"}));
 await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByLabelText("关联表的对应字段"));
 await userEvent.click(await screen.findByText("儿童编号（id）",{selector:".ant-select-item-option-content"}));
 await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByRole("button",{name:"高级编排"}));
 expect(await screen.findByRole("img",{name:"治理数据流图"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"业务步骤"}));
 await userEvent.click(within(screen.getByRole("navigation",{name:"业务阶段"})).getByText("选数据"));
 expect(screen.getByRole("button",{name:"添加关联表"})).toBeTruthy();
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:[
  expect.objectContaining({id:"source_1"}),
  expect.objectContaining({id:"source_2",source:expect.objectContaining({resource:expect.objectContaining({resource_id:"household-resource"}),fields:["id","guardian_name"]})}),
  expect.objectContaining({id:"join_1",inputs:[{node:"source_1"},{node:"source_2"}],join:{kind:"left",left_keys:["id"],right_keys:["id"],columns:[{side:"left",source:"id",target:"id"},{side:"left",source:"name",target:"name"},{side:"right",source:"id",target:"right_id"},{side:"right",source:"guardian_name",target:"right_guardian_name"}]}}),
  expect.objectContaining({id:"output_1",inputs:[{node:"join_1"}]})
 ]}}});
});
it("已有关联可在业务模式移除关联表，原主表和输出模型保持不变",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const joined={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],{...graph.nodes[0],id:"source_2"},{id:"join_1",kind:"join",inputs:[{node:"source_1"},{node:"source_2"}],join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[{side:"left",source:"id",target:"id"}]}},{...graph.nodes[1],inputs:[{node:"join_1"}]}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[joined],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 const sources=screen.getAllByRole("button",{name:/public.customers/});
 await userEvent.click(sources.find(button=>button.textContent?.includes("关联表"))!);
 await userEvent.click(screen.getByRole("button",{name:"移除关联表"}));
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:graph}});
});
it("已有两表关联时新增第三张表接到已有结果之后，保留原字段映射",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const extra:MetadataResource={...source,object_id:"third-resource",display_name:"房产资料",qualified_name:"public.properties",field_labels:{id:"儿童编号"}};
 const existingJoin={id:"join_1",kind:"join",inputs:[{node:"source_1"},{node:"source_2"}],join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[{side:"left",source:"id",target:"id"},{side:"right",source:"name",target:"existing_name"}]}};
 const joined={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],{...graph.nodes[0],id:"source_2"},existingJoin,{...graph.nodes[1],inputs:[{node:"join_1"}]}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  if(!init?.method&&path.startsWith("/api/v1/governance/dataflows?"))return Response.json({items:[joined],total:1});
  if(path.startsWith("/api/v1/governance/metadata?"))return Response.json({items:[source,extra],total:2});
  if(path.includes("/metadata/third-resource/observations"))return Response.json({object:extra,items:[]});
  return original(path,init);
 }));
 show();await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.click(screen.getByRole("button",{name:"添加关联表"}));
 await userEvent.click(screen.getByLabelText("来源资源"));
 await userEvent.click(await screen.findByText(/房产资料（public.properties）/,{selector:".ant-select-item-option-content"}));
 await userEvent.click(within(screen.getByRole("navigation",{name:"业务阶段"})).getByText("定规则"));
 await userEvent.click(screen.getByRole("button",{name:/^关联数据表 · 房产资料/}));
 await userEvent.click(screen.getByLabelText("已有数据的对应字段"));
 await userEvent.click(await screen.findByText("中文名称待补充（id）",{selector:".ant-select-item-option-content"}));await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByLabelText("关联表的对应字段"));
 await userEvent.click(await screen.findByText("儿童编号（id）",{selector:".ant-select-item-option-content"}));await userEvent.keyboard("{Escape}");
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:[
  expect.objectContaining({id:"source_1"}),expect.objectContaining({id:"source_2"}),existingJoin,
  expect.objectContaining({id:"source_3"}),
  expect.objectContaining({id:"join_2",inputs:[{node:"join_1"},{node:"source_3"}],join:expect.objectContaining({columns:expect.arrayContaining([{side:"left",source:"existing_name",target:"existing_name"},{side:"right",source:"id",target:"right_id"}])})}),
  expect.objectContaining({id:"output_1",inputs:[{node:"join_2"}]})
 ]}}});
});
it("批量数据流独立提供定时运行，隐藏持续运行",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();await screen.findByRole("heading",{name:"数据流设计 · 客户治理"});
 expect(screen.queryByRole("tab",{name:"持续运行"})).toBeNull();
 await userEvent.click(screen.getByRole("tab",{name:"定时运行"}));
 await userEvent.click(screen.getByRole("button",{name:"新建调度"}));
 const dialog=screen.getByRole("dialog");
 expect(within(dialog).getByText("客户治理 · v1")).toBeTruthy();
 expect(within(dialog).queryByText("工作流版本")).toBeNull();
 await userEvent.type(within(dialog).getByLabelText("首次触发（所选时区）"),"2030-01-01 09:00{Enter}");
 await waitFor(()=>expect(within(dialog).getByRole("button",{name:"保存并启用调度"}).hasAttribute("disabled")).toBe(false));await userEvent.click(within(dialog).getByRole("button",{name:"保存并启用调度"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0]).toMatchObject({path:"/api/v1/governance/schedules",body:{dataflow:{id:"flow",version:1},parameters:{},cron:"0 * * * *",interval_seconds:0}});
 expect(posts[0].body).not.toHaveProperty("workflow");
});
it("流式版本仅提供持续运行入口",async()=>{
 fixture([]);const original=globalThis.fetch;
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>{
  const response=await original(path,init);if(!path.startsWith("/api/v1/governance/dataflows?"))return response;
  return Response.json({items:[{...record,execution_mode:"continuous"}],total:1});
 }));show();await screen.findByRole("heading",{name:"数据流设计 · 客户治理"});
 expect(screen.queryByRole("tab",{name:"定时运行"})).toBeNull();
 expect(screen.getByRole("tab",{name:"持续运行"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:"运行"})).toBeNull();
});

it("输出绑定模型继承业务用途，改用推导结果可选用途且重新绑定清除重复分层",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const summaryModel={...model,definition:{...model.definition,model:{...model.definition.model,data_layer:"dws"}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.includes("/models/model/versions/2")?Response.json(summaryModel):path.startsWith("/api/v1/governance/models?")?Response.json({items:[summaryModel],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 fireEvent.click(screen.getByRole("button",{name:"结果输出 output_1"}));
 expect(await screen.findByText("业务用途：汇总可复用的统计指标")).toBeTruthy();
 expect(screen.queryByLabelText("成果分层")).toBeNull();expect(screen.queryByLabelText("成果用途")).toBeNull();
 await userEvent.click(screen.getByLabelText("结果结构"));
 await userEvent.click(await screen.findByText("根据结果生成模型",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("成果用途"));
 await userEvent.click(await screen.findByText("提供特定报表或应用所需数据",{selector:".ant-select-item-option-content"}));
 expect(screen.getByText("对应分层：应用服务 · ADS")).toBeTruthy();
 await userEvent.click(screen.getByLabelText("结果结构"));
 await userEvent.click(await screen.findByText("使用已有模型",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("输出模型版本"));
 await userEvent.click(await screen.findByText("客户输出模型 · v2",{selector:".ant-select-item-option-content"}));
 await screen.findByText("业务用途：汇总可复用的统计指标");
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:expect.arrayContaining([expect.objectContaining({kind:"output",model:{id:"model",version:2}})])}}});
 expect(JSON.stringify(posts[0].body)).not.toContain('"data_layer"');
});
it("旧模型未标注用途时重新绑定仍保留原成果用途",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const legacy={...record,definition:{...record.definition,dataflow:{nodes:[graph.nodes[0],{...graph.nodes[1],data_layer:"ads"}]}}};
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.startsWith("/api/v1/governance/dataflows?")?Response.json({items:[legacy],total:1}):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 fireEvent.click(screen.getByRole("button",{name:"结果输出 output_1"}));
 await screen.findByText("业务用途：提供特定报表或应用所需数据");
 await userEvent.click(screen.getByLabelText("结果结构"));await userEvent.click(await screen.findByText("根据结果生成模型",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("结果结构"));await userEvent.click(await screen.findByText("使用已有模型",{selector:".ant-select-item-option-content"}));
 await userEvent.click(screen.getByLabelText("输出模型版本"));await userEvent.click(await screen.findByText("客户输出模型 · v2",{selector:".ant-select-item-option-content"}));
 await screen.findByText("业务用途：提供特定报表或应用所需数据");
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));await waitFor(()=>expect(posts).toHaveLength(1));
 expect(posts[0].body).toMatchObject({definition:{dataflow:{nodes:expect.arrayContaining([expect.objectContaining({kind:"output",data_layer:"ads",model:{id:"model",version:2}})])}}});
});
it("模型用途尚未加载时禁止切换结果结构以免丢失分层",async()=>{
 fixture([]);const original=globalThis.fetch;const pending:((response:Response)=>void)[]=[];
 vi.stubGlobal("fetch",vi.fn((path:string,init?:RequestInit)=>path.includes("/models/model/versions/2")?new Promise<Response>(resolve=>pending.push(resolve)):original(path,init)));
 show();await userEvent.click(await screen.findByRole("button",{name:"高级编排"}));
 await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 fireEvent.click(screen.getByRole("button",{name:"结果输出 output_1"}));
 expect(screen.getByLabelText("结果结构").hasAttribute("disabled")).toBe(true);
 await waitFor(()=>expect(pending.length).toBeGreaterThan(0));
 for(const resolve of pending)resolve(Response.json({...model,definition:{...model.definition,model:{...model.definition.model,data_layer:"ads"}}}));
 await screen.findByText("业务用途：提供特定报表或应用所需数据");
 expect(screen.getByLabelText("结果结构").hasAttribute("disabled")).toBe(false);
 await userEvent.click(screen.getByLabelText("结果结构"));await userEvent.click(await screen.findByText("根据结果生成模型",{selector:".ant-select-item-option-content"}));
 expect(screen.getByText("对应分层：应用服务 · ADS")).toBeTruthy();
});

it("模型回绑先复核最新版本，保留最新步骤并仅在明确保存时建立新草稿",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);const original=globalThis.fetch;
 const latest={...record,latest_version:3,version:3,definition:{...record.definition,name:"最新处理方案",description:"保留后续修订"}};
 const candidate={...latest.definition,dataflow:{nodes:[graph.nodes[0],{...graph.nodes[1],model:{id:"model",version:4}}]}};
 history.replaceState(null,"","/platform/governance/dataflows?definition_id=flow&version=1&from_run=flow-run&bind_model=model&model_version=4");
 vi.stubGlobal("fetch",vi.fn(async(path:string,init?:RequestInit)=>path.includes("/models/model/versions/4")?Response.json({...model,version:4}):path.includes("/model-binding?")?Response.json({base:latest,definition:candidate,source_flow:{id:"flow",version:1},output_node:"output_1",changed:true}):path==="/api/v1/governance/dataflows/flow/versions/1"?Response.json(record):original(path,init)));
 show();await screen.findByText("来源数据流已有新版本 v3，请核对变更后绑定");
 expect(posts).toHaveLength(0);await userEvent.click(screen.getByRole("button",{name:"核对并创建待复核修改"}));
 expect(posts).toHaveLength(0);expect((await screen.findByLabelText("数据流名称") as HTMLInputElement).value).toBe("最新处理方案");
 await userEvent.click(screen.getByRole("button",{name:"保存数据流草稿"}));
 await waitFor(()=>expect(posts).toHaveLength(1));expect(posts[0]).toMatchObject({path:"/api/v1/governance/dataflows/flow/versions",body:{base_version:3,definition:candidate}});
});

it("带回绑参数的导航保护当前未保存修改",async()=>{
 const posts:{path:string;body:unknown}[]=[];fixture(posts);show();
 await userEvent.click(await screen.findByRole("button",{name:"基于此版本修订"}));
 await userEvent.clear(screen.getByLabelText("数据流名称"));await userEvent.type(screen.getByLabelText("数据流名称"),"尚未保存的修改");
 navigate("/platform/governance/dataflows?definition_id=flow&version=1&from_run=flow-run&bind_model=model&model_version=2");
 await screen.findByRole("button",{name:"继续编辑"});
 expect((screen.getByLabelText("数据流名称") as HTMLInputElement).value).toBe("尚未保存的修改");expect(location.search).toBe("");expect(posts).toHaveLength(0);
 await userEvent.click(screen.getByRole("button",{name:"继续编辑"}));
 expect((screen.getByLabelText("数据流名称") as HTMLInputElement).value).toBe("尚未保存的修改");
});
