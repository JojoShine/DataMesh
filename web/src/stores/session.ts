import { create } from "zustand";
import type { Principal } from "../api/types";
interface SessionState {
  user: Principal | null;
  ready: boolean;
  epoch: number;
  identityError: string | null;
  setUser: (user: Principal | null) => void;
  reset: () => void;
}
export const useSession = create<SessionState>((set) => ({
  user: null,
  ready: false,
  epoch: 0,
  identityError: null,
  setUser: (user) =>
    set((state) => ({
      user,
      ready: true,
      epoch: state.epoch + 1,
      identityError: null,
    })),
  reset: () =>
    set((state) => ({
      user: null,
      ready: true,
      epoch: state.epoch + 1,
      identityError: null,
    })),
}));
