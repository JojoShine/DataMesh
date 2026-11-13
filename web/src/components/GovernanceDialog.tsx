import {type ReactNode} from "react";
import {PlatformDialog} from "./PlatformDialog";
import {GovernanceGuide,type GovernanceGuideTopic} from "./GovernanceGuide";
import "../pages/GovernanceCatalog.css";
export {DialogActionTarget as GovernanceActionTarget,DialogActions as GovernanceDialogActions} from "./PlatformDialog";

export function GovernanceDialog({header,children,onClose,width=1100,busy=false,guide,actions,closeLabel="关闭"}:{header:ReactNode;children:ReactNode;onClose:()=>void;width?:number;busy?:boolean;guide?:GovernanceGuideTopic;actions?:ReactNode;closeLabel?:string}){
 return <PlatformDialog open className="governance-dialog" title={header} width={`min(${width}px, calc(100vw - 32px))`} onCancel={onClose} busy={busy} closeLabel={closeLabel} footer={actions}>{guide&&<GovernanceGuide topic={guide}/>} {children}</PlatformDialog>;
}
