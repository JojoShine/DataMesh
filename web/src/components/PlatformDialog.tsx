import {Button,Modal,Space,type ModalProps} from "antd";
import {createContext,useContext,useState,type ReactNode} from "react";
import {createPortal} from "react-dom";
import "./PlatformDialog.css";

export const DialogActionTarget=createContext<HTMLElement|null|undefined>(undefined);

// Nested tabs and run views share the current dialog's fixed action area.
export function DialogActions({children,visible=true}:{children:ReactNode;visible?:boolean}){
 const footer=useContext(DialogActionTarget);
 if(!visible)return null;
 if(footer===undefined)return <Space size={16} wrap className="platform-dialog-actions governance-dialog-actions">{children}</Space>;
 return footer?createPortal(children,footer):null;
}

type PlatformDialogProps=Omit<ModalProps,"footer"|"styles"|"onCancel"> & {
 onCancel?:()=>void;
 footer?:ReactNode;
 styles?:Exclude<ModalProps["styles"],Function>;
 closeLabel?:string;
 busy?:boolean;
};

// Both platforms keep navigation and commands in the same fixed footer.
export function PlatformDialog({children,footer,closeLabel="关闭",busy=false,onCancel,className,styles,keyboard=true,...props}:PlatformDialogProps){
 const [target,setTarget]=useState<HTMLDivElement|null>(null);
 return <DialogActionTarget.Provider value={target}><Modal {...props} centered className={["platform-dialog",className].filter(Boolean).join(" ")} onCancel={busy?undefined:onCancel} keyboard={keyboard&&!busy&&Boolean(onCancel)} closable={false} mask={{closable:false}} styles={{...styles,container:{...styles?.container,display:"flex",flexDirection:"column",maxHeight:"calc(100dvh - 32px)"},header:{...styles?.header,flexShrink:0},body:{minHeight:0,overflowY:"auto",overflowX:"hidden",...styles?.body},footer:{...styles?.footer,flexShrink:0}}} footer={<div className="platform-dialog-footer governance-dialog-footer"><Button disabled={busy||!onCancel} onClick={onCancel}>{closeLabel}</Button><div className="platform-dialog-extra-actions" ref={setTarget}/>{footer}</div>}>{children}</Modal></DialogActionTarget.Provider>;
}
