import { useSyncExternalStore } from "react";
import {applicationURL,logicalPath} from "./basePath";
type NavigationGuard=(path:string)=>boolean;
let guard:NavigationGuard|null=null;
let rememberedPath=logicalPath(location.pathname+location.search);
export function registerNavigationGuard(next:NavigationGuard){guard=next;rememberedPath=logicalPath(location.pathname+location.search);return ()=>{if(guard===next)guard=null}}
export function navigate(path: string, replace = false) {
 path=logicalPath(path);
 const current=logicalPath(location.pathname+location.search),pathname=logicalPath(location.pathname);
 const preserveScroll=pathname==="/platform/governance/lightweight"&&path.split("?")[0]===pathname;
 if(["/platform/governance/lightweight","/platform/governance/sandbox"].includes(path.split("?")[0])){const url=new URL(path,location.origin);url.searchParams.delete("workspace_id");path=url.pathname+url.search}
 if(pathname.startsWith("/platform/governance/")&&path.startsWith("/platform/governance/")&&!path.startsWith("/platform/governance/workspaces")&&!["/platform/governance/lightweight","/platform/governance/sandbox"].includes(path.split("?")[0])){const scope=new URLSearchParams(location.search).get("workspace_id");if(scope){const url=new URL(path,location.origin);if(!url.searchParams.has("workspace_id"))url.searchParams.set("workspace_id",scope);path=url.pathname+url.search}}
 if(path!==current&&guard&&!guard(path))return;
 if(replace)history.replaceState(null,"",applicationURL(path));else history.pushState(null, "", applicationURL(path));
 rememberedPath=path;
 window.dispatchEvent(new PopStateEvent("popstate"));
 if(!preserveScroll)window.scrollTo(0, 0);
}
const subscribe = (callback: () => void) => {
 const onChange=()=>{const next=logicalPath(location.pathname+location.search);if(next!==rememberedPath&&guard&&!guard(next)){history.pushState(null,"",applicationURL(rememberedPath));return}rememberedPath=next;callback()};
 window.addEventListener("popstate", onChange);
 return () => window.removeEventListener("popstate", onChange);
};
export function usePath() {
 return useSyncExternalStore(subscribe,()=>logicalPath(window.location.pathname+window.location.search));
}
