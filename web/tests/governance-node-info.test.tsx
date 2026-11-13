// @vitest-environment jsdom
import {afterEach,expect,it,vi} from "vitest";
import {cleanup,renderHook,waitFor} from "@testing-library/react";
import type {GovernanceDataflow,GovernanceFlowExecution,GovernanceFlowRun,MetadataResource} from "../src/api/governance";
import {useGovernanceRunNodeContext} from "../src/hooks/useGovernanceRunNodeContext";
import {flowEditorFields} from "../src/api/governanceFlowFields";
import {governanceNodeInfo,governanceRunNodeContext} from "../src/api/governanceNodeInfo";
const field={name:"amount",logical_type:"decimal",precision:18,scale:2,nullable:false,primary_key:false,sensitive:false,business_name:"金额"};
const source:MetadataResource={object_id:"table",source_id:"source",qualified_name:"public.orders",display_name:"订单明细",schema_version:2,resource_type:"table_resource",fields:[field,{...field,name:"region",logical_type:"string",business_name:"区域"}]};
const graph:GovernanceDataflow={nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"table",contract_version:2},fields:["region","amount"]}},{id:"aggregate_1",kind:"aggregate",inputs:[{node:"source_1"}],group_by:["region"],aggregates:[{source:"amount",target:"total",operation:"sum"}]},{id:"output_1",kind:"output",inputs:[{node:"aggregate_1"}],model:{id:"model",version:3}}]};
const pinned:GovernanceFlowExecution["pinned"]=[{kind:"model",ref:{id:"model",version:3},definition:{name:"区域金额汇总",description:"",owner_id:"owner",steward_id:"owner"}}];
afterEach(()=>{cleanup();vi.unstubAllGlobals()});
it("来源、聚合和输出展示已选对象与字段，不仅显示组件名称",()=>{
 const context={graph,resources:{table:source},pinned};
 expect(governanceNodeInfo("source_1",context).description).toContain("订单明细");expect(governanceNodeInfo("source_1",context).description).toContain("金额（amount）");
 expect(governanceNodeInfo("aggregate_1",context).description).toBe("按区域（region）分组；金额（amount）求和 → total");
 expect(governanceNodeInfo("output_1",context).description).toContain("区域金额汇总 · v3");
});
it("历史运行从固定快照读配置，来源名称未保存时显示真实资源和所选字段",()=>{
 const definition={flow:{id:"flow",version:1},pinned:[{kind:"dataflow" as const,ref:{id:"flow",version:1},definition:{name:"旧版本",description:"",owner_id:"owner",steward_id:"owner",dataflow:graph}},...pinned],inputs:{source_1:{resource:{resource_type:"table_resource" as const,resource_id:"table",contract_version:2},fields:[field]}}};
 const context=governanceRunNodeContext(definition);
 expect(governanceNodeInfo("source_1",context).description).toContain("table · v2");expect(governanceNodeInfo("source_1",context).description).toContain("金额（amount）");expect(governanceNodeInfo("aggregate_1",context).description).toContain("求和 → total");
});
it("缺失节点和草稿未选择时明确显示待补充，不虚构已选信息",()=>{
 expect(governanceNodeInfo("missing",{graph}).description).toBe("本次记录未保存此节点的配置");
 expect(governanceNodeInfo("source",{graph:{nodes:[{id:"source",kind:"source",inputs:[]}]}}).description).toContain("尚未选择");
});
it("质量、关联、清洗、筛选和代码节点保留实际选择，循环配置不阻塞渲染",()=>{
 const steps:GovernanceDataflow={nodes:[...graph.nodes,{id:"quality",kind:"quality",inputs:[{node:"source_1"}],checks:[{field:"amount",kind:"range",blocking:true,parameters:{min:"0.01",max:"100.00"}}]},{id:"clean",kind:"transform",inputs:[{node:"source_1"}],columns:[{source:"region",target:"area",rule:"trim"}]},{id:"filter",kind:"filter",inputs:[{node:"source_1"}],condition:{field:"amount",operator:"gte",value:"0.01"}},{id:"join",kind:"join",inputs:[{node:"source_1"},{node:"source_1"}],join:{kind:"left",left_keys:["region"],right_keys:["region"],columns:[{side:"left",source:"amount",target:"amount"}]}},{id:"script",kind:"script",inputs:[],script:{language:"python",runtime_version:"3.12",source:"private code",fields:[]}},{id:"loop",kind:"filter",inputs:[{node:"loop"}],condition:{field:"amount",operator:"not_null"}}]};
 const context={graph:steps,resources:{table:source}};
 expect(governanceNodeInfo("quality",context).description).toContain("下限 0.01，上限 100.00");expect(governanceNodeInfo("clean",context).description).toContain("去除首尾空白");expect(governanceNodeInfo("filter",context).description).toContain("大于等于 0.01");expect(governanceNodeInfo("join",context).description).toContain("保留左侧全部记录");expect(governanceNodeInfo("join",context).description).toContain("左表.金额（amount） → amount");expect(governanceNodeInfo("script",context).description).toContain("Python");expect(governanceNodeInfo("script",context).description).not.toContain("private code");expect(governanceNodeInfo("loop",context).description).toContain("非空");
});
it("同契约版本只补充来源名称与中文标签，运行输入字段仍使用固定快照",async()=>{
 const definition={flow:{id:"flow",version:1},pinned:[{kind:"dataflow" as const,ref:{id:"flow",version:1},definition:{name:"处理",description:"",owner_id:"owner",steward_id:"owner",dataflow:graph}}],inputs:{source_1:{resource:graph.nodes[0].source!.resource,fields:[field]}}};
 const run:GovernanceFlowRun={id:"run",workspace_id:"space",kind:"dataflow",state:"succeeded",processed:1,error_code:"",created_at:"",started_at:null,finished_at:null,retry_of:null,cancel_requested:false,definition,result:null};
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({object:{...source,fields:[{...field,logical_type:"string"}],field_labels:{amount:"金额"}}})));
 const {result}=renderHook(()=>useGovernanceRunNodeContext(run));await waitFor(()=>expect(result.current.context.resources.table?.display_name).toBe("订单明细"));
 expect(flowEditorFields(graph,{node:"source_1"},result.current.context.resources,new Set(),definition.inputs)[0].logical_type).toBe("decimal");
 expect(governanceNodeInfo("source_1",result.current.context).description).toContain("订单明细");
});
it("新契约版本的来源名称不混入旧运行；读取失败保留可见错误",async()=>{
 const definition={flow:{id:"flow",version:1},pinned:[{kind:"dataflow" as const,ref:{id:"flow",version:1},definition:{name:"处理",description:"",owner_id:"owner",steward_id:"owner",dataflow:graph}}],inputs:{}};
 const run:GovernanceFlowRun={id:"run",workspace_id:"space",kind:"dataflow",state:"succeeded",processed:1,error_code:"",created_at:"",started_at:null,finished_at:null,retry_of:null,cancel_requested:false,definition,result:null};
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({object:{...source,schema_version:3,display_name:"新版来源"}})));
 const {result,unmount}=renderHook(()=>useGovernanceRunNodeContext(run));await waitFor(()=>expect(result.current.loading).toBe(false));expect(governanceNodeInfo("source_1",result.current.context).description).not.toContain("新版来源");unmount();
 vi.stubGlobal("fetch",vi.fn(async()=>Response.json({code:"UNAVAILABLE",message:"来源说明暂不可用"},{status:503})));
 const failed=renderHook(()=>useGovernanceRunNodeContext(run));await waitFor(()=>expect(failed.result.current.errors).toHaveLength(1));expect(governanceNodeInfo("source_1",failed.result.current.context).description).toContain("table · v2");
});

it("来源契约不一致时不会把新版字段说明当成旧选择",()=>{
 const info=governanceNodeInfo("source_1",{graph,resources:{table:{...source,schema_version:9,display_name:"新版来源"}}});
 expect(info.description).not.toContain("新版来源");expect(info.description).toContain("table · v2");
});
