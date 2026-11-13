// @vitest-environment jsdom
import {useState} from "react";
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {cleanup,render,screen,within} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {GovernanceFlowSteps} from "../src/pages/GovernanceFlowSteps";
import type {GovernanceDataflow} from "../src/api/governance";
const graph:GovernanceDataflow={nodes:[{id:"source",kind:"source",inputs:[]},{id:"quality",kind:"quality",inputs:[{node:"source"}],checks:[]},{id:"output",kind:"output",inputs:[{node:"quality",port:"good"}],infer_model:true}]};
beforeEach(()=>{Object.defineProperty(window,"matchMedia",{writable:true,value:vi.fn(()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}}))})});
afterEach(cleanup);
function Example({value=graph}:{value?:GovernanceDataflow}){const [selected,setSelected]=useState("source");return <GovernanceFlowSteps graph={value} selected={selected} onSelect={setSelected} resources={{}} editing onAddSource={()=>undefined}/>}
it("三个阶段使用横向导航，仅展示当前阶段，切换会选中对应配置",async()=>{
 render(<Example/>);
 const navigation=screen.getByRole("navigation",{name:"业务阶段"});
 expect(within(navigation).getByText("选数据")).toBeTruthy();
 expect(screen.getByRole("button",{name:"添加关联表"})).toBeTruthy();
 expect(screen.queryByRole("button",{name:/^检查数据质量/})).toBeNull();
 await userEvent.click(within(navigation).getByText("定规则"));
 expect(screen.getByRole("button",{name:/^检查数据质量/}).getAttribute("aria-pressed")).toBe("true");
 expect(screen.queryByRole("button",{name:"添加关联表"})).toBeNull();
 await userEvent.click(within(navigation).getByText("确认输出"));
 expect(screen.getByRole("button",{name:/^确认输出结果/}).getAttribute("aria-pressed")).toBe("true");
 await userEvent.click(within(navigation).getByText("选数据"));
 expect(screen.getByRole("button",{name:"添加关联表"})).toBeTruthy();
 expect(graph.nodes[2].inputs).toEqual([{node:"quality",port:"good"}]);
});
it("空规则阶段仍可进入，并可直接切换到输出，不会退回数据阶段",async()=>{
 render(<Example value={{nodes:[graph.nodes[0],{...graph.nodes[2],inputs:[{node:"source"}]}]}}/>);
 const navigation=screen.getByRole("navigation",{name:"业务阶段"});
 await userEvent.click(within(navigation).getByText("定规则"));
 expect(screen.getByText(/暂不需要整理也可以直接输出/)).toBeTruthy();
 expect(screen.queryByRole("button",{name:/^选择数据/})).toBeNull();
 await userEvent.click(within(navigation).getByText("确认输出"));
 expect(screen.getByRole("button",{name:/^确认输出结果/})).toBeTruthy();
});
