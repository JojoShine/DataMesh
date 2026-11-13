import {expect,it} from "vitest";
import {scheduleWallTime,scheduleLocalInput,scheduleRule} from "../src/components/governanceScheduleTime";
it("uses selected timezone rather than browser timezone",()=>{
 expect(scheduleWallTime("2026-10-28T09:30","Asia/Shanghai")).toBe("2026-10-28T01:30:00.000Z");
 expect(scheduleLocalInput("2026-10-28T01:30:00Z","Asia/Shanghai")).toBe("2026-10-28T09:30");
});
it("rejects nonexistent and ambiguous first-run wall times explicitly",()=>{
 expect(()=>scheduleWallTime("2026-03-08T02:30","America/New_York")).toThrow(/不存在/);
 expect(()=>scheduleWallTime("2026-11-01T01:30","America/New_York")).toThrow(/重复/);
 expect(scheduleWallTime("2026-11-01T03:30","America/New_York")).toBe("2026-11-01T08:30:00.000Z");
 expect(()=>scheduleWallTime("2026-02-30T09:00","UTC")).toThrow();
});
it("expands familiar periods and interval units into existing API rules",()=>{
 expect(scheduleRule({timing:"workday",clock:"09:30"})).toEqual({cron:"30 9 * * 1-5",interval_seconds:0});
 expect(scheduleRule({timing:"weekly",clock:"09:30",weekday:2})).toEqual({cron:"30 9 * * 2",interval_seconds:0});
 expect(scheduleRule({timing:"interval",interval_count:2,interval_unit:3600})).toEqual({cron:"",interval_seconds:7200});
});
it("preserves the effective instant when displaying another timezone",()=>{
 const at="2026-10-28T01:30:00.000Z";
 expect(scheduleWallTime(scheduleLocalInput(at,"America/New_York"),"America/New_York")).toBe(at);
 expect(()=>scheduleWallTime("2026-10-28T09:00","Mars/Olympus")).toThrow();
 expect(scheduleRule({timing:"daily",clock:"08:15"})).toEqual({cron:"15 8 * * *",interval_seconds:0});
 expect(scheduleRule({timing:"hourly"})).toEqual({cron:"0 * * * *",interval_seconds:0});
});
