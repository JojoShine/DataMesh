import {applicationURL} from "../app/basePath";
import { navigate } from "../app/navigation";

export function Brand({ compact = false, subtitle = false }: { compact?: boolean; subtitle?: boolean }) {
  return <button className="brand" onClick={() => navigate("/")} aria-label="返回资产首页">
    <img className="brand-mark" src={applicationURL("/static/design/brand-mark.png")} alt="" />
    {!compact && <><strong>DataMesh</strong>{subtitle && <span className="brand-subtitle">数据资产与协同平台</span>}</>}
  </button>;
}
