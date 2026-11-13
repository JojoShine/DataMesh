// @vitest-environment jsdom
import {useEffect} from "react";
import {act,cleanup,render,waitFor} from "@testing-library/react";
import {afterEach,expect,it,vi} from "vitest";
import type {ReactFlowProps} from "@xyflow/react";
import {FlowGraph} from "../src/pages/GovernanceFlowGraph";
import type {GovernanceDataflow} from "../src/api/governance";
const state=vi.hoisted(()=>({props:null as ReactFlowProps|null,setViewport:vi.fn(),viewport:{x:10,y:20,zoom:1}}));
vi.mock("@xyflow/react",async original=>({...await original<typeof import("@xyflow/react")>(),ReactFlow:(props:ReactFlowProps)=>{
 state.props=props;
 useEffect(()=>{props.onInit?.({getViewport:()=>state.viewport,setViewport:state.setViewport} as unknown as Parameters<NonNullable<ReactFlowProps["onInit"]>>[0])},[]);
 return <div/>;
}}));
afterEach(cleanup);
it("更新配置保留测量信息，相同视角回调不重复保存",async()=>{
 const graph:GovernanceDataflow={nodes:[{id:"source",kind:"source",inputs:[]},{id:"output",kind:"output",infer_model:true,inputs:[{node:"source"}]}]};
 const layout={revision:0,positions:{source:{x:0,y:0},output:{x:300,y:0}},viewport:state.viewport};const save=vi.fn();
 const view=render(<FlowGraph graph={graph} selected="source" onSelect={()=>{}} layout={layout} onLayoutChange={save}/>);
 await waitFor(()=>expect(state.props?.nodes).toHaveLength(2));
 act(()=>state.props?.onNodesChange?.([{id:"source",type:"dimensions",dimensions:{width:216,height:88}},{id:"output",type:"dimensions",dimensions:{width:216,height:88}}]));
 const viewportCalls=state.setViewport.mock.calls.length;
 view.rerender(<FlowGraph graph={structuredClone(graph)} selected="output" onSelect={()=>{}} layout={{...layout,viewport:{...layout.viewport}}} onLayoutChange={save}/>);
 await waitFor(()=>expect(state.props?.nodes?.every(node=>node.measured?.width===216&&node.measured?.height===88)).toBe(true));
 expect(state.props?.edges).toHaveLength(1);expect(state.setViewport.mock.calls).toHaveLength(viewportCalls);
 act(()=>state.props?.onMoveEnd?.(null,{...state.viewport}));expect(save).not.toHaveBeenCalled();
 act(()=>state.props?.onMoveEnd?.(null,{x:30,y:20,zoom:1}));expect(save).toHaveBeenCalledOnce();
});
