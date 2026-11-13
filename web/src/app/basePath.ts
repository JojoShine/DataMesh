function prefix():string {
 const base=import.meta.env.BASE_URL.replace(/\/+$/,'');
 return base==='/'?'':base;
}

export function logicalPath(path:string):string {
 const base=prefix();
 if(base&&path.startsWith(base)&&['','/','?','#'].includes(path.charAt(base.length))){
  const rest=path.slice(base.length);
  return rest.startsWith('/')?rest:`/${rest}`;
 }
 return path;
}

export function applicationURL(path:string):string {
 if(!path.startsWith('/')||path.startsWith('//'))return path;
 return prefix()+logicalPath(path);
}
