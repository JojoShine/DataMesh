// @vitest-environment jsdom
import {expect,it} from "vitest";
import {governanceFieldLabel} from "../src/api/governanceFieldLabels";
import {flowEditorFields} from "../src/pages/GovernanceFlowGraph";
import type {MetadataResource,GovernanceDataflow} from "../src/api/governance";
it("中文名称来自各自资源，改名后继续保留，不改变技术字段",()=>{
 const fields=[{name:"registration_date",logical_type:"date",nullable:false,primary_key:false,sensitive:false,precision:null,scale:null}];
 const resources:Record<string,MetadataResource>={tax:{object_id:"tax",source_id:"db",qualified_name:"tax",schema_version:1,fields,field_labels:{registration_date:"税务登记日期"}},market:{object_id:"market",source_id:"db",qualified_name:"market",schema_version:1,fields,field_labels:{registration_date:"成立日期"}}};
 const graph:GovernanceDataflow={nodes:[{id:"tax",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"tax",contract_version:1},fields:["registration_date"]}},{id:"market",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:"market",contract_version:1},fields:["registration_date"]}},{id:"rename",kind:"project",inputs:[{node:"tax"}],columns:[{source:"registration_date",target:"tax_date",rule:"copy"}]}]};
 expect(governanceFieldLabel(flowEditorFields(graph,{node:"rename"},resources)[0])).toBe("税务登记日期（tax_date）");
 expect(governanceFieldLabel(flowEditorFields(graph,{node:"market"},resources)[0])).toBe("成立日期（registration_date）");
 expect(fields[0].name).toBe("registration_date");
 expect(governanceFieldLabel({name:"unknown"})).toBe("中文名称待补充（unknown）");
});
