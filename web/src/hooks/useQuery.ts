import {governanceWorkspacePath} from "../api/governanceWorkspace";
import { useEffect, useRef, useState } from "react";
import { request } from "../api/client";
import { useSession } from "../stores/session";
export function useQuery<T>(inputPath: string | null, keepDataOnReload = false) {
 const path=governanceWorkspacePath(inputPath);
  const epoch = useSession((s) => s.epoch);
  const key = `${epoch}:${path ?? ""}`;
  const [state, setState] = useState<{
    key: string;
    data: T | null;
    loading: boolean;
    error: unknown;
    updatedAt: Date | null;
  }>({ key, data: null, loading: Boolean(path), error: null, updatedAt: null });
  const [revision, setRevision] = useState(0);
  const sequence = useRef(0);
  useEffect(() => {
    const current = ++sequence.current;
    const controller = new AbortController();
    setState(previous => keepDataOnReload && previous.key === key ? {
      ...previous,
      loading: Boolean(path),
      error: null,
    } : {
      key,
      data: null,
      loading: Boolean(path),
      error: null,
      updatedAt: null,
    });
    if (path)
      request<T>(path, { signal: controller.signal })
        .then((data) => {
          if (!controller.signal.aborted && current === sequence.current)
            setState({
              key,
              data,
              loading: false,
              error: null,
              updatedAt: new Date(),
            });
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted && current === sequence.current)
            setState(previous => ({
              key,
              data: keepDataOnReload && previous.key === key ? previous.data : null,
              loading: false,
              error,
              updatedAt: null,
            }));
        });
    return () => controller.abort();
  }, [path, epoch, revision, keepDataOnReload]);
  const visible =
    state.key === key
      ? state
      : {
          key,
          data: null,
          loading: Boolean(path),
          error: null,
          updatedAt: null,
        };
  return { ...visible, reload: () => setRevision((n) => n + 1) };
}
