export type ScheduleTiming="hourly"|"daily"|"workday"|"weekly"|"interval"|"calendar";
export function scheduleLocalInput(instant:string,zone:string):string {
 const parts=new Intl.DateTimeFormat("sv-SE",{timeZone:zone,year:"numeric",month:"2-digit",day:"2-digit",hour:"2-digit",minute:"2-digit",hourCycle:"h23"}).formatToParts(new Date(instant));
 const part=(key:string)=>parts.find(p=>p.type===key)!.value;
 return `${part("year")}-${part("month")}-${part("day")}T${part("hour")}:${part("minute")}`;
}
// Enumerate real offsets around the requested day so DST gaps and folds cannot
// silently move a user's first execution to a different wall-clock time.
export function scheduleWallTime(value:string,zone:string):string {
 if(!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value))throw new Error("请选择有效的首次触发时间");
 const wall=Date.parse(value+":00Z");
 if(!Number.isFinite(wall)||new Date(wall).toISOString().slice(0,16)!==value)throw new Error("请选择有效的首次触发时间");
 const offsets=new Set<number>();
 for(let hour=-36;hour<=36;hour+=6){const at=wall+hour*3600000;offsets.add(Date.parse(scheduleLocalInput(new Date(at).toISOString(),zone)+":00Z")-at)}
 const matches=[...offsets].map(offset=>wall-offset).filter(at=>scheduleLocalInput(new Date(at).toISOString(),zone)===value);
 if(matches.length===0)throw new Error("该时区的首次触发时刻不存在（夏令时跳时），请选择其他时刻");
 if(matches.length>1)throw new Error("该时区的首次触发时刻重复（夏令时回拨），请选择无歧义的时刻");
 return new Date(matches[0]).toISOString();
}
export function scheduleRule(v:{timing:ScheduleTiming;clock?:string;weekday?:number;cron?:string;interval_count?:number;interval_unit?:number}):{cron:string;interval_seconds:number}{
 if(v.timing==="interval")return {cron:"",interval_seconds:(v.interval_count??1)*(v.interval_unit??3600)};
 if(v.timing==="calendar")return {cron:v.cron??"",interval_seconds:0};
 const [hour,minute]=(v.clock??"09:00").split(":").map(Number);
 return {cron:v.timing==="hourly"?"0 * * * *":`${minute} ${hour} * * ${v.timing==="workday"?"1-5":v.timing==="weekly"?v.weekday??1:"*"}`,interval_seconds:0};
}
