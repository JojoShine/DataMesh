export function governanceFieldLabel(field:{name:string;business_name?:string;description?:string;comment?:string}):string {
 const text=field.business_name?.trim()||field.description?.trim()||field.comment?.split("；")[0]?.trim();
 return `${text||"中文名称待补充"}（${field.name}）`;
}
