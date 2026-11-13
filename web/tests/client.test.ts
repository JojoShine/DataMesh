import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, errorMessage, mutation, request } from "../src/api/client";
import { useSession } from "../src/stores/session";
const user = {
  user_id: "u",
  username: "reader",
  department_id: "d",
  platforms: ["exchange" as const],
  roles: ["consumer"],
  csrf_token: "csrf-in-memory",
};
afterEach(() => {
  vi.unstubAllGlobals();
  useSession.getState().reset();
});
describe("统一请求边界", () => {
  it("真实业务体验读取现有记录并向原接口提交真实任务，保留请求保护", async () => {
    useSession.getState().setUser({...user, public_test:false, full_experience:true, roles:["governor"], platforms:["governance"]});
    const existing = {items:[{id:"existing-flow", version:3}], total:1};
    const fetcher = vi.fn().mockResolvedValueOnce(Response.json(existing)).mockResolvedValueOnce(Response.json({id:"real-run",state:"queued"}));
    vi.stubGlobal("fetch",fetcher);
    await expect(request("/api/v1/governance/dataflows")).resolves.toEqual(existing);
    await expect(mutation("/api/v1/governance/dataflows/existing-flow/versions/3/run",{version:3},"real-run-key")).resolves.toMatchObject({id:"real-run"});
    expect(fetcher.mock.calls.map(([path])=>path)).toEqual(["/api/v1/governance/dataflows","/api/v1/governance/dataflows/existing-flow/versions/3/run"]);
    const init=fetcher.mock.calls[1][1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect(JSON.parse(String(init.body))).toEqual({version:3});
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(user.csrf_token);
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("real-run-key");
  });
  it("权限刷新同步体验模式，即使业务角色未改变", async () => {
    useSession.getState().setUser({...user, public_test:false, full_experience:true});
    vi.stubGlobal("fetch",vi.fn().mockResolvedValueOnce(Response.json({code:"FORBIDDEN",message:"无权操作"},{status:403})).mockResolvedValueOnce(Response.json({...user,public_test:true,full_experience:false})));
    await expect(request("/api/v1/sources")).rejects.toMatchObject({status:403});
    await vi.waitFor(()=>expect(useSession.getState().user).toMatchObject({public_test:true,full_experience:false}));
  });
  it("结果到期提示说明原因和下一步，保留原始错误供排查", () => {
    const error = new ApiError(409, "GOVERNANCE_OUTPUT_STALE", "治理成果已超过模型鲜度期限", "support-trace");
    expect(errorMessage(error)).toContain("请到“数据流设计”重新运行对应的数据流");
    expect(errorMessage(error)).not.toMatch(/鲜度|追踪号|support-trace/);
    expect(error).toMatchObject({status:409, code:"GOVERNANCE_OUTPUT_STALE", traceId:"support-trace"});
    expect(errorMessage(new ApiError(503, "UNAVAILABLE", "服务暂时不可用", "support-trace"))).toContain("support-trace");
  });
  it("发送同源 Cookie、CSRF 和指定幂等键，不向 URL 传播秘密", async () => {
    useSession.getState().setUser(user);
    const fetcher = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ ok: true }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetcher);
    await mutation("/api/v1/applications", { purpose: "测试" }, "same-key");
    const [path, init] = fetcher.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/v1/applications");
    expect(init.credentials).toBe("same-origin");
    expect(new Headers(init.headers).get("X-CSRF-Token")).toBe(user.csrf_token);
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("same-key");
  });
  it("401 立即清理身份与缓存代次", async () => {
    useSession.getState().setUser(user);
    const epoch = useSession.getState().epoch;
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({
              code: "UNAUTHENTICATED",
              message: "登录失效",
              trace_id: "trace",
            }),
            { status: 401 },
          ),
        ),
    );
    await expect(request("/api/v1/me")).rejects.toBeInstanceOf(ApiError);
    expect(useSession.getState().user).toBeNull();
    expect(useSession.getState().epoch).toBeGreaterThan(epoch);
  });
  it("503 保持失败语义，不伪装为空数组", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({
              code: "UNAVAILABLE",
              message: "依赖未就绪",
              trace_id: "t",
            }),
            { status: 503 },
          ),
        ),
    );
    await expect(request("/api/v1/datasets")).rejects.toMatchObject({
      status: 503,
      code: "UNAVAILABLE",
      traceId: "t",
    });
  });
  it("取消请求传递到网络层", async () => {
    const controller = new AbortController();
    const fetcher = vi.fn(
      (_path: string, init: RequestInit) =>
        new Promise((_resolve, reject) => {
          init.signal?.addEventListener(
            "abort",
            () => reject(init.signal?.reason),
            { once: true },
          );
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const pending = request("/api/v1/datasets", { signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  });
  it("204 退出成功无需 JSON 内容", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(null, { status: 204 })),
    );
    await expect(
      request("/auth/logout", { method: "POST" }),
    ).resolves.toBeUndefined();
  });
});
