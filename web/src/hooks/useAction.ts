import {usePath} from "../app/navigation";
import {isGovernanceExperiencePath} from "../api/experience/governance";
import {useSession} from "../stores/session";
import {useGovernanceWorkspace} from "../components/GovernanceWorkspaceContext";
import { useEffect, useRef, useState } from "react";
import { ApiError, isOperation, mutation, request } from "../api/client";
import type { Operation } from "../api/types";
function delay(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const stop = () => {
      clearTimeout(timer);
      reject(new DOMException("已取消", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", stop);
      resolve();
    }, ms);
    signal.addEventListener("abort", stop, { once: true });
  });
}
export function useAction() {
 const publicTest=useSession(s=>Boolean(s.user?.public_test));const {workspace}=useGovernanceWorkspace();const currentPath=usePath();const readOnly=(publicTest&&!currentPath.startsWith("/platform/governance/"))||workspace?.state==="archived";
  const [busy, setBusy] = useState(false);
  const [operation, setOperation] = useState<Operation | null>(null);
  const controllers = useRef(new Set<AbortController>());
  const pending = useRef<{ fingerprint: string; key: string } | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      for (const c of controllers.current) c.abort();
    };
  }, []);
  async function run<T>(path: string, body: unknown = {}): Promise<T> {
    if(publicTest&&!isGovernanceExperiencePath(path))throw new ApiError(403,"EXPERIENCE_READ_ONLY","可浏览完整功能页面并填写表单了解流程；公开测试账号不能提交或执行业务操作","");
    if(readOnly)throw new ApiError(409,"GOVERNANCE_WORKSPACE_ARCHIVED","此空间已归档，不能修改或启动任务","");
    if (busy) throw new Error("已有操作等待完成");
    const fingerprint = path + JSON.stringify(body);
    const key =
      pending.current?.fingerprint === fingerprint
        ? pending.current.key
        : crypto.randomUUID();
    pending.current = { fingerprint, key };
    const controller = new AbortController();
    controllers.current.add(controller);
    setBusy(true);
    setOperation(null);
    try {
      const response = await mutation<T>(path, body, key, controller.signal);
      if (!isOperation(response)) {
        pending.current = null;
        return response;
      }
      setOperation(response);
      let op: Operation = response;
      for (let count = 0; count < 30; count++) {
        if (op.status === "succeeded") {
          pending.current = null;
          return response;
        }
        if (op.status === "failed" || op.status === "unknown")
          throw new ApiError(
            409,
            op.status,
            String(op.result?.message ?? "操作尚未确认，请按操作编号核对"),
            op.operation_id,
            key,
          );
        while (document.hidden) {
          await delay(1000, controller.signal);
        }
        await delay(1000, controller.signal);
        op = await request<Operation>(
          "/api/v1/operations/" + response.operation_id,
          { signal: controller.signal },
        );
        if (mounted.current) setOperation(op);
      }
      throw new ApiError(
        408,
        "OPERATION_PENDING",
        "操作仍在执行，请按操作编号查询",
        response.operation_id,
        key,
      );
    } catch (error) {
      if (
        error instanceof ApiError &&
        error.status >= 400 &&
        error.status < 500 &&
        error.status !== 408 &&
        error.code !== "unknown"
      )
        pending.current = null;
      throw error;
    } finally {
      controllers.current.delete(controller);
      if (mounted.current) setBusy(false);
    }
  }
  return { run, busy, readOnly, operation };
}
