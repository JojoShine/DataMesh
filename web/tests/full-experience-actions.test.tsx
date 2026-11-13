// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from "vitest";
import {act,cleanup,renderHook,waitFor} from "@testing-library/react";
import {GovernanceWorkspaceProvider} from "../src/components/GovernanceWorkspaceContext";
import {useAction} from "../src/hooks/useAction";
import {useSession} from "../src/stores/session";

beforeEach(()=>{
 history.replaceState(null,"","/platform/governance/dataflows?workspace_id=existing-space");
 useSession.setState({user:{user_id:"operator",username:"experience",department_id:"dept",roles:["governor"],platforms:["governance"],csrf_token:"csrf-fixture",public_test:false,full_experience:true},ready:true,epoch:0});
});
afterEach(()=>{cleanup();useSession.getState().reset();vi.unstubAllGlobals()});

it.each(["active","archived"] as const)("真实业务体验保留 %s 空间的原操作权限",async(state)=>{
 const fetcher=vi.fn(async(path:string,init?:RequestInit)=>{
  if(path==="/api/v1/governance/workspaces/existing-space")return Response.json({id:"existing-space",name:"现有治理空间",state});
  if(path==="/api/v1/governance/dataflows/existing-flow/versions/2/run?workspace_id=existing-space"&&init?.method==="POST")return Response.json({id:"actual-run",state:"queued"});
  throw new Error("意外请求："+path);
 });
 vi.stubGlobal("fetch",fetcher);
 const {result}=renderHook(()=>useAction(),{wrapper:GovernanceWorkspaceProvider});
 await waitFor(()=>expect(result.current).not.toBeNull());
 expect(result.current.readOnly).toBe(state==="archived");
 if(state==="archived"){
  await expect(result.current.run("/api/v1/governance/dataflows/existing-flow/versions/2/run",{version:2})).rejects.toMatchObject({code:"GOVERNANCE_WORKSPACE_ARCHIVED"});
  expect(fetcher).toHaveBeenCalledTimes(1);
 }else{
  await act(async()=>{await expect(result.current.run("/api/v1/governance/dataflows/existing-flow/versions/2/run",{version:2})).resolves.toMatchObject({id:"actual-run"})});
  const [path,init]=fetcher.mock.calls[1];
  expect(path).toBe("/api/v1/governance/dataflows/existing-flow/versions/2/run?workspace_id=existing-space");
  expect(JSON.parse(String(init?.body))).toEqual({version:2});
  expect(new Headers(init?.headers).get("X-CSRF-Token")).toBe("csrf-fixture");
 }
});
