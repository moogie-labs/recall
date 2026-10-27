import type { HostAPI } from "./api/types";
import { Workspace } from "./navigation/workspace";
import { SaveCoordinator } from "./saving/coordinator";
export function registerShutdown(
  host: HostAPI,
  workspace: Workspace,
  _coordinator: SaveCoordinator,
) {
  let pending = false;
  const off = host.on(
    "desktop:quit-requested",
    ({ token }: { token: string }) => {
      if (pending) return;
      pending = true;
      void workspace
        .flushBefore(async () => {
          const r = await host.completeQuit(token);
          if (r.problem) throw new Error(r.problem.message);
        })
        .catch(async () => {
          await host.cancelQuit?.(token);
        })
        .finally(() => {
          pending = false;
        });
    },
  );
  void host.ready();
  return off;
}
