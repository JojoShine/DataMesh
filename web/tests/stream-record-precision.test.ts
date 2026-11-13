// @vitest-environment jsdom
import {afterEach, beforeEach, expect, it, vi} from "vitest";
import {request, ApiError} from "../src/api/client";
import {useSession} from "../src/stores/session";

beforeEach(()=>useSession.setState({user:null,ready:true,epoch:0,identityError:null}));
afterEach(()=>vi.unstubAllGlobals());

it("持续成果保留大整数原文、精确小数和字符串，同时保持分页数字",async()=>{
 const body='{"items":[{"id":9223372036854775807,"negative":-9223372036854775808,"safe":9007199254740991,"amount":"12345678901234567890.123456","label":"escaped \\\"9223372036854775807\\\""}],"total":1,"page":1}';
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(new Response(body))));
 const result=await request<{items:Record<string,unknown>[];total:number}>("/api/v1/governance/streams/abc/records?page=1");
 expect(result.items[0]).toEqual({id:"9223372036854775807",negative:"-9223372036854775808",safe:9007199254740991,amount:"12345678901234567890.123456",label:'escaped "9223372036854775807"'});
 expect(result.total).toBe(1);
});

it("工作空间成果同样保留精度，错误响应仍按既有协议拒绝",async()=>{
 vi.stubGlobal("fetch",vi.fn()
  .mockResolvedValueOnce(new Response('{"items":[{"id":9007199254740992}],"total":1}'))
  .mockResolvedValueOnce(new Response('{"code":"NO_ACCESS","message":"不可读取","trace_id":"trace"}',{status:403})));
 const result=await request<{items:{id:unknown}[]}>("/api/v1/governance/workspaces/ws/streams/abc/records");
 expect(result.items[0].id).toBe("9007199254740992");
 await expect(request("/api/v1/governance/streams/abc/records")).rejects.toMatchObject({status:403,code:"NO_ACCESS",traceId:"trace"} satisfies Partial<ApiError>);
});

it("损坏的成果 JSON 不会被精度处理修复为成功响应",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(new Response('{"items":[{"id":01}]}'))));
 await expect(request("/api/v1/governance/streams/abc/records")).rejects.toMatchObject({code:"INVALID_RESPONSE"});
});

it("未加引号的大整数对象键仍是无效 JSON",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(new Response('{"items":[{9223372036854775807:"x"}],"total":1}'))));
 await expect(request("/api/v1/governance/streams/abc/records")).rejects.toMatchObject({code:"INVALID_RESPONSE"});
});

it("其他接口含类似搜索文字时保持原有数值契约",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(new Response('{"value":9007199254740992}'))));
 for(const path of ["/api/v1/governance/datasets?search=/streams/abc/records","/other/streams/abc/records"]){
  const result=await request<{value:unknown}>(path);
  expect(typeof result.value).toBe("number");
 }
});
