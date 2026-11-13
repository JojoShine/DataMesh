import {governanceExperience,isGovernanceExperiencePath,ExperienceError} from "./experience/governance";
import {publicExperienceRead} from "./publicExperience";
import {governanceWorkspacePath} from "./governanceWorkspace";
import { useSession } from "../stores/session";
import {applicationURL} from "../app/basePath";
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public traceId: string,
    public retryKey?: string,
  ) {
    super(message);
  }
}
function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function parseStreamRecords(text: string): unknown {
  // Validate first so quoting a token cannot repair an invalid numeric object key.
  JSON.parse(text);
  // Preserve raw integer tokens before JSON.parse can round business identifiers.
  const exact = text.replace(/"(?:\\.|[^"\\])*"|(-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?)/g, (token: string, numeric: string | undefined) => {
    if (numeric === undefined) return token;
    const value = Number(numeric);
    return Number.isInteger(value) && !Number.isSafeInteger(value) ? JSON.stringify(numeric) : token;
  });
  return JSON.parse(exact);
}
export async function request<T>(
  path: string,
  options: RequestInit = {},
  timeout = 15000,
  onResponse?: (response: Response) => void,
): Promise<T> {
  const experienceUser=useSession.getState().user;
  const publicTest=experienceUser?.public_test;
  if(publicTest&&isGovernanceExperiencePath(path)){
    try{return await governanceExperience(governanceWorkspacePath(path)??path,options,experienceUser) as T}
    catch(error){if(error instanceof ExperienceError)throw new ApiError(error.status,error.code,error.message,"");throw error}
  }
  if(publicTest && path.startsWith("/api/v1/") && path!=="/api/v1/me") {
    if((options.method??"GET").toUpperCase()!=="GET")
      throw new ApiError(403,"EXPERIENCE_READ_ONLY","公开测试账号可查看完整功能页面，不能提交、修改、上传或执行任务","");
    const projection=await publicExperienceRead(path, (publicPath)=>request(publicPath,{...options,signal:undefined},timeout));
    if(!projection.available)throw new ApiError(403,"EXPERIENCE_DATA_PRIVATE","此区域的内部记录未公开","");
    return projection.data as T;
  }
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(new DOMException("请求超时", "TimeoutError")),
    timeout,
  );
  const headers = new Headers(options.headers);
  headers.set("Accept", "application/json");
  if (options.body && !(options.body instanceof FormData)) headers.set("Content-Type", "application/json");
  if (options.method && options.method !== "GET") {
    const csrf = useSession.getState().user?.csrf_token;
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }
  try {
    const response = await fetch(applicationURL(governanceWorkspacePath(path)??path), {
      ...options,
      headers,
      credentials: "same-origin",
      signal: options.signal
        ? AbortSignal.any([options.signal, controller.signal])
        : controller.signal,
    });
    const body: unknown = await (/^\/api\/v1\/governance\/(?:workspaces\/[^/?]+\/)?streams\/[^/?]+\/records(?:\?|$)/.test(path)
      ? response.text().then(parseStreamRecords)
      : response.json()).catch(() => null);
    if (!response.ok) {
      if (response.status === 401) {
        const current = useSession.getState();
        if (current.user || !current.ready) current.reset();
      }
      if (
        response.status === 403 &&
        useSession.getState().user &&
        path !== "/api/v1/me"
      ) {
        void request<import("./types").Principal>("/api/v1/me")
          .then((next) => {
            const current = useSession.getState().user;
            if (
              current &&
              JSON.stringify([
                current.roles,
                current.platforms,
                current.department_id,
                current.public_test,
                current.full_experience,
              ]) !==
                JSON.stringify([next.roles, next.platforms, next.department_id, next.public_test, next.full_experience])
            )
              useSession.getState().setUser(next);
          })
          .catch((error: unknown) => {
            if (useSession.getState().user)
              useSession.setState({ identityError: errorMessage(error) });
          });
      }
      const value = isObject(body) ? body : {};
      throw new ApiError(
        response.status,
        typeof value.code === "string" ? value.code : "REQUEST_FAILED",
        typeof value.message === "string"
          ? value.message
          : "请求失败，请稍后重试",
        typeof value.trace_id === "string" ? value.trace_id : "",
      );
    }
    if (response.status === 204) return undefined as T;
    if (body === null)
      throw new ApiError(502, "INVALID_RESPONSE", "后台响应格式不正确", "");
    onResponse?.(response);
    return body as T;
  } finally {
    clearTimeout(timer);
  }
}
export function query(values: Record<string, string | number | undefined>) {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(values))
    if (value !== undefined && value !== "") q.set(key, String(value));
  return q.toString();
}
export function mutation<T>(
  path: string,
  body: unknown = {},
  key: string = crypto.randomUUID(),
  signal?: AbortSignal,
) {
  return request<T>(path, {
    method: "POST",
    body: JSON.stringify(body),
    headers: { "Idempotency-Key": key },
    signal,
  });
}
export function errorMessage(error: unknown) {
  if (error instanceof ApiError && error.code === "GOVERNANCE_OUTPUT_STALE")
    return "这份数据已超过约定的使用时间，暂时不能使用或发布。请到“数据流设计”重新运行对应的数据流，生成新结果后再试。";
  if (error instanceof ApiError)
    return `${error.message}${error.traceId ? `（追踪号 ${error.traceId}）` : ""}`;
  if (error instanceof Error && error.name === "TimeoutError")
    return "请求等待超时，执行结果可能尚未确认，请使用原请求编号核对";
  return "暂时无法连接后台，请检查服务状态后重试";
}
export function isOperation(
  value: unknown,
): value is import("./types").Operation {
  return (
    isObject(value) &&
    typeof value.operation_id === "string" &&
    typeof value.status === "string"
  );
}
