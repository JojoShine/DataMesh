import {applicationURL} from "../app/basePath";
import { SyncOutlined, ContainerFilled, ProductFilled, SafetyCertificateFilled } from "@ant-design/icons";
import type { Platform } from "../api/types";
const icons = { exchange: SyncOutlined, governance: ContainerFilled, assets: ProductFilled, security: SafetyCertificateFilled };

export function ModuleIcon({ platform, illustrated = false }: { platform: Platform; illustrated?: boolean }) {
  const Icon = icons[platform];
  return illustrated ? <img className="module-illustration" src={applicationURL(`/static/design/${platform}.png`)} alt="" /> : <Icon aria-hidden />;
}
