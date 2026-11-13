import {expect,it} from "vitest";
import type {GovernanceDataflow} from "../src/api/governance";
import {connectFlow,disconnectFlow,removeFlowNode,validateFlowConnection,businessFlowSequence,insertBusinessFlowStep,insertBusinessFlowJoin,removeBusinessFlowStep} from "../src/pages/governanceFlowEditing";
const graph:GovernanceDataflow={nodes:[{id:"source",kind:"source",inputs:[]},{id:"clean",kind:"transform",inputs:[{node:"source"}],columns:[{source:"name",target:"customer_name",rule:"trim"}]},{id:"quality",kind:"quality",inputs:[{node:"clean"}],checks:[]},{id:"output",kind:"output",inputs:[{node:"quality",port:"good"}]}]};
it("业务步骤按依赖排序，插入和移除自动衔接且保留质量合格出口",()=>{
 expect(businessFlowSequence({nodes:[...graph.nodes].reverse()})?.map(n=>n.id)).toEqual(["source","clean","quality","output"]);
 const next=insertBusinessFlowStep(graph,{id:"filter",kind:"filter",inputs:[]},"quality");
 expect(next.nodes.find(n=>n.id==="filter")?.inputs).toEqual([{node:"quality",port:"good"}]);
 expect(next.nodes.find(n=>n.id==="output")?.inputs).toEqual([{node:"filter"}]);
 const removed=removeBusinessFlowStep(next,"filter");
 expect(removed.nodes.find(n=>n.id==="output")?.inputs).toEqual([{node:"quality",port:"good"}]);
 expect(removeBusinessFlowStep(graph,"quality").nodes.find(n=>n.id==="output")?.inputs).toEqual([{node:"clean"}]);
 expect(graph.nodes[3].inputs).toEqual([{node:"quality",port:"good"}]);
 expect(removed.nodes.find(n=>n.id==="clean")?.columns).toEqual([{source:"name",target:"customer_name",rule:"trim"}]);
});
it("复杂、断开、循环或不合格分支保持原图，业务操作拒绝自动改线",()=>{
 const cases:GovernanceDataflow[]=[
  {nodes:[...graph.nodes,{id:"extra",kind:"filter",inputs:[{node:"source"}]}]},
  {nodes:graph.nodes.map(n=>n.id==="clean"?{...n,inputs:[{node:"missing"}]}:n)},
  {nodes:graph.nodes.map(n=>n.id==="output"?{...n,inputs:[{node:"quality",port:"bad"}]}:n)},
  {nodes:graph.nodes.map(n=>n.id==="clean"?{...n,inputs:[{node:"quality",port:"good"}]}:n)},
  {nodes:graph.nodes.map(n=>n.id==="clean"?{...n,kind:"join",inputs:[{node:"source"},{node:"source"}]}:n)}
 ];
 for(const complex of cases){expect(businessFlowSequence(complex)).toBeNull();expect(()=>insertBusinessFlowStep(complex,{id:"new",kind:"filter",inputs:[]},"source")).toThrow();expect(()=>removeBusinessFlowStep(complex,"clean")).toThrow();}
 expect(()=>removeBusinessFlowStep(graph,"source")).toThrow();expect(()=>removeBusinessFlowStep(graph,"output")).toThrow();
});
it("拒绝循环、自连、已有输入和不合格分支进入成果",()=>{
 expect(validateFlowConnection(graph,{source:"clean",target:"clean",targetIndex:0})).toContain("自身");
 expect(validateFlowConnection(graph,{source:"source",target:"clean",targetIndex:0})).toContain("已连接");
 const open=disconnectFlow(graph,"clean",0);expect(validateFlowConnection(open,{source:"quality",sourcePort:"good",target:"clean",targetIndex:0})).toContain("循环");
 expect(validateFlowConnection(disconnectFlow(graph,"output",0),{source:"quality",sourcePort:"bad",target:"output",targetIndex:0})).toContain("不合格");
});
it("重连保留字段配置，删除节点保留可辨识的未连接输入",()=>{
 const open=disconnectFlow(graph,"quality",0);const next=connectFlow(open,{source:"source",target:"quality",targetIndex:0});
 expect(next.nodes[1].columns).toEqual(graph.nodes[1].columns);expect(graph.nodes[2].inputs).toEqual([{node:"clean"}]);
 expect(removeFlowNode(next,"source").nodes.find(n=>n.id==="quality")?.inputs).toEqual([{node:""}]);
});
it("关联节点的左右输入独立，非质量节点不能冒用质量分支",()=>{
 const join:GovernanceDataflow={nodes:[...graph.nodes,{id:"join",kind:"join",inputs:[],join:{kind:"inner",left_keys:[],right_keys:[],columns:[]}}]};
 const left=connectFlow(join,{source:"source",target:"join",targetIndex:0});const right=connectFlow(left,{source:"clean",target:"join",targetIndex:1});
 expect(right.nodes.at(-1)?.inputs).toEqual([{node:"source"},{node:"clean"}]);expect(validateFlowConnection(join,{source:"source",sourcePort:"bad",target:"join",targetIndex:0})).toContain("分支");
});

it("关联缺少对应字段、数量不一致或未保留结果字段时给出具体提示",async()=>{
 const {flowRunIssues}=await import("../src/pages/governanceFlowEditing");
 const joined:GovernanceDataflow={nodes:[{id:"source",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"table",contract_version:1},fields:["id"]}},{id:"join",kind:"join",inputs:[{node:"source"},{node:"source"}],join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[]}},{id:"output",kind:"output",infer_model:true,inputs:[{node:"join"}]}]};
 expect(flowRunIssues(joined)).toEqual([{node:"join",message:"请选择关联结果要保留的字段：添加左侧或右侧输出字段。"}]);
 joined.nodes[1].join!.columns=[{side:"left",source:"id",target:"id"}];expect(flowRunIssues(joined)).toEqual([]);
 joined.nodes[1].join!.right_keys=[];expect(flowRunIssues(joined)[0].message).toContain("互相对应");
 joined.nodes[1].join!.right_keys=["id","other"];expect(flowRunIssues(joined)[0].message).toContain("数量需要一致");
});

it("业务模式连续添加关联表，整理步骤与质量出口仍可自动衔接",()=>{
 const first=insertBusinessFlowJoin(graph,{id:"households",kind:"source",inputs:[]},{id:"join_households",kind:"join",inputs:[],join:{kind:"left",left_keys:[],right_keys:[],columns:[]}},"quality");
 expect(businessFlowSequence(first)?.map(n=>n.id)).toEqual(["source","clean","quality","join_households","output"]);
 expect(first.nodes.find(n=>n.id==="join_households")?.inputs).toEqual([{node:"quality",port:"good"},{node:"households"}]);
 const second=insertBusinessFlowJoin(first,{id:"property",kind:"source",inputs:[]},{id:"join_property",kind:"join",inputs:[],join:{kind:"inner",left_keys:["id"],right_keys:["id"],columns:[{side:"left",source:"id",target:"id"}]}},"join_households");
 expect(businessFlowSequence(second)?.map(n=>n.id)).toEqual(["source","clean","quality","join_households","join_property","output"]);
 const filtered=insertBusinessFlowStep(second,{id:"filter",kind:"filter",inputs:[]},"join_households");
 expect(filtered.nodes.find(n=>n.id==="join_property")?.inputs).toEqual([{node:"filter"},{node:"property"}]);
 const removed=removeBusinessFlowStep(filtered,"join_households");
 expect(removed.nodes.some(n=>n.id==="households")).toBe(false);
 expect(removed.nodes.find(n=>n.id==="filter")?.inputs).toEqual([{node:"quality",port:"good"}]);
 expect(removed.nodes.find(n=>n.id==="join_property")?.join?.left_keys).toEqual(["id"]);
 expect(removed.nodes.find(n=>n.id==="output")?.inputs).toEqual([{node:"join_property"}]);
 expect(businessFlowSequence(removed)).not.toBeNull();
 expect(graph.nodes).toHaveLength(4);
});
it("业务关联拒绝来源共享、右侧处理分支、重复编号及超过节点上限",()=>{
 const joined=insertBusinessFlowJoin(graph,{id:"extra",kind:"source",inputs:[]},{id:"join",kind:"join",inputs:[],join:{kind:"left",left_keys:[],right_keys:[],columns:[]}},"clean");
 expect(businessFlowSequence({nodes:joined.nodes.map(n=>n.id==="join"?{...n,inputs:[{node:"clean"},{node:"source"}]}:n)})).toBeNull();
 expect(businessFlowSequence({nodes:[...joined.nodes,{id:"branch",kind:"filter",inputs:[{node:"extra"}]}]})).toBeNull();
 expect(()=>insertBusinessFlowJoin(graph,{id:"source",kind:"source",inputs:[]},{id:"join",kind:"join",inputs:[]},"clean")).toThrow();
 const full:GovernanceDataflow={nodes:[{id:"root",kind:"source",inputs:[]},...Array.from({length:37},(_,i)=>({id:`step_${i}`,kind:"filter" as const,inputs:[{node:i?`step_${i-1}`:"root"}]})),{id:"end",kind:"output",inputs:[{node:"step_36"}]}]};
 expect(()=>insertBusinessFlowJoin(full,{id:"extra",kind:"source",inputs:[]},{id:"join",kind:"join",inputs:[]},"root")).toThrow();
});
