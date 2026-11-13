import { Tabs } from "antd";
import { navigate, usePath } from "../app/navigation";
import { useSession } from "../stores/session";
import { ResourceDirectory } from "./ResourceDirectory";
import { DirectoryWorkspace } from "./DirectoryWorkspace";

export function DirectoryCenter() {
  const path = usePath();
  const user = useSession((s) => s.user)!;
  const canMaintain = user.public_test || user.roles.some((role) => ["admin", "provider", "operator"].includes(role));
  const params = new URLSearchParams(window.location.search);
  const legacyWorkspace = /\/(workspace|mounts)$/.test(window.location.pathname);
  const maintaining = canMaintain && (legacyWorkspace || params.get("view") === "manage");

  return <>
    {canMaintain && <Tabs activeKey={maintaining ? "manage" : "published"}
      onChange={(view) => navigate(`/platform/exchange/directory${view === "manage" ? "?view=manage" : ""}`)}
      items={[{ key: "published", label: "已发布目录" }, { key: "manage", label: "目录维护" }]} />}
    {maintaining ? <DirectoryWorkspace key={path} /> : <ResourceDirectory />}
  </>;
}
