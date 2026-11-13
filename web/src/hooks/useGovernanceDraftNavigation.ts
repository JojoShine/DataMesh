import {useEffect,useRef,useState} from "react";
import {navigate,registerNavigationGuard} from "../app/navigation";
export function useGovernanceDraftNavigation(dirty:boolean){
 const [pending,setPending]=useState<string|null>(null);const stop=useRef<(()=>void)|null>(null);
 useEffect(()=>{if(!dirty)return;stop.current=registerNavigationGuard(path=>{setPending(path);return false});const unload=(event:BeforeUnloadEvent)=>{event.preventDefault();event.returnValue=""};window.addEventListener("beforeunload",unload);return()=>{stop.current?.();stop.current=null;window.removeEventListener("beforeunload",unload)}},[dirty]);
 function discard(){if(!pending)return false;stop.current?.();stop.current=null;setPending(null);navigate(pending);return true}
 return {pending,stay:()=>setPending(null),discard,saved:discard};
}
