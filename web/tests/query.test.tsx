// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { useQuery } from "../src/hooks/useQuery";
import { useSession } from "../src/stores/session";
beforeEach(() =>
  useSession.setState({
    user: null,
    ready: true,
    epoch: 0,
    identityError: null,
  }),
);
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("快速切换查询时，取消旧请求并忽略无法取消的旧响应", async () => {
  let finishOld!: (value: Response) => void;
  let oldSignal: AbortSignal | null = null;
  vi.stubGlobal(
    "fetch",
    vi.fn((path: string, init: RequestInit) => {
      if (path === "/slow") {
        oldSignal = init.signal as AbortSignal;
        return new Promise<Response>((resolve) => {
          finishOld = resolve;
        });
      }
      return Promise.resolve(
        new Response(JSON.stringify({ name: "new-selection" })),
      );
    }),
  );
  const { result, rerender, unmount } = renderHook(
    ({ path }) => useQuery<{ name: string }>(path),
    { initialProps: { path: "/slow" } },
  );
  rerender({ path: "/new" });
  await waitFor(() => expect(result.current.data?.name).toBe("new-selection"));
  expect(oldSignal!.aborted).toBe(true);
  await act(async () =>
    finishOld(new Response(JSON.stringify({ name: "stale-selection" }))),
  );
  expect(result.current.data?.name).toBe("new-selection");
  unmount();
});
it("身份代次变化立即清理受限结果，再重新获取", async () => {
  let pending!: (value: Response) => void;
  let count = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(() => {
      count++;
      return count === 1
        ? Promise.resolve(
            new Response(JSON.stringify({ name: "old-department" })),
          )
        : new Promise<Response>((resolve) => {
            pending = resolve;
          });
    }),
  );
  const { result } = renderHook(() =>
    useQuery<{ name: string }>("/restricted"),
  );
  await waitFor(() => expect(result.current.data?.name).toBe("old-department"));
  act(() => useSession.setState({ epoch: 1 }));
  expect(result.current.data).toBeNull();
  await act(async () =>
    pending(new Response(JSON.stringify({ name: "new-department" }))),
  );
  await waitFor(() => expect(result.current.data?.name).toBe("new-department"));
});
