export const sourceDefinitions = [
 {kind:"postgres",name:"PostgreSQL",category:"通用关系型",port:5432,database_label:"数据库名称",objects_label:"库表"},
 {kind:"mysql",name:"MySQL",category:"通用关系型",port:3306,database_label:"数据库名称",objects_label:"库表"},
 {kind:"oracle",name:"Oracle",category:"通用关系型",port:1521,database_label:"服务名（Service Name）",objects_label:"库表"},
 {kind:"sqlserver",name:"SQL Server",category:"通用关系型",port:1433,database_label:"数据库名称",objects_label:"库表"},
 {kind:"dm",name:"达梦 DM",category:"国产数据库",port:5236,database_label:"模式（Schema）",objects_label:"库表"},
 {kind:"kingbase",name:"人大金仓 KingbaseES",category:"国产数据库",port:54321,database_label:"数据库名称",objects_label:"库表"},
 {kind:"highgo",name:"瀚高 HighGo",category:"国产数据库",port:5866,database_label:"数据库名称",objects_label:"库表"},
 {kind:"vastbase",name:"海量 Vastbase",category:"国产数据库",port:5432,database_label:"数据库名称",objects_label:"库表"},
 {kind:"doris",name:"Apache Doris",category:"分析型数据库",port:9030,database_label:"数据库名称",objects_label:"库表"},
 {kind:"clickhouse",name:"ClickHouse",category:"分析型数据库",port:8123,database_label:"数据库名称",objects_label:"库表"},
 {kind:"mongodb",name:"MongoDB",category:"非关系型",port:27017,database_label:"数据库名称",objects_label:"集合"},
 {kind:"elasticsearch",name:"Elasticsearch",category:"非关系型",port:9200,database_label:"索引或别名（单一名称）",objects_label:"索引"},
 {kind:"redis",name:"Redis",category:"非关系型",port:6379,database_label:"逻辑库编号（0–15）",objects_label:"键空间"},
 {kind:"hbase",name:"HBase",category:"非关系型",port:8080,database_label:"命名空间（Namespace）",objects_label:"表（REST 接入）"},
] as const;
export type SourceKind=typeof sourceDefinitions[number]["kind"];
export interface SourceType {
 kind:string;name:string;category:string;port:number;database_label:string;objects_label:string;
 http:boolean;cdc:boolean;write:boolean;reader:boolean;status:"ready"|"driver_missing"|"driver_invalid"|"reader_unavailable";max_rows:number;
}
export const sourceNames:Record<string,string>=Object.fromEntries(sourceDefinitions.map(t=>[t.kind,t.name]));
export const sourcePorts:Record<string,number>=Object.fromEntries(sourceDefinitions.map(t=>[t.kind,t.port]));
export const sourceOptions=[...new Set(sourceDefinitions.map(t=>t.category))].map(label=>({label,options:sourceDefinitions.filter(t=>t.category===label).map(t=>({value:t.kind,label:t.name}))}));
export const sourceSupportsCDC=(kind:string)=>["postgres","mysql","oracle"].includes(kind);
export const sourceOptionalCredentials=(kind:string)=>["mongodb","elasticsearch","redis","hbase","clickhouse"].includes(kind);
export const sourceConnectionScheme=(kind:string)=>["mongodb","elasticsearch","redis","hbase","clickhouse"].includes(kind);
export const sourceDefinition=(kind:string)=>sourceDefinitions.find(t=>t.kind===kind)??sourceDefinitions[0];
export const sourceStatusNames:Record<SourceType["status"],string>={ready:"读取服务已就绪",driver_missing:"需安装厂商驱动",driver_invalid:"厂商驱动待修复",reader_unavailable:"读取服务待就绪"};
