import type {AssetPage,DirectoryDefinition,Field,Page,PublicAssetDetail,ResourceDirectory} from "./types";

type PublicReader=<T>(path:string)=>Promise<T>;
type Projection={available:false}|{available:true;data:unknown};
let directoryCache:{until:number;data:Promise<PublicAssetDetail[]>}|undefined;
function publicDirectories(read:PublicReader){
 if(directoryCache&&directoryCache.until>Date.now())return directoryCache.data;
 const data=(async()=>{
  const first=await read<AssetPage>("/api/public/assets?page=1&page_size=100");
  const assets=[...first.items];
  for(let page=2;assets.length<first.total;page++){
   const next=await read<AssetPage>(`/api/public/assets?page=${page}&page_size=100`);
   if(!next.items.length)break;
   assets.push(...next.items);
  }
  const details:PublicAssetDetail[]=[];
  for(let offset=0;offset<assets.length;offset+=5)
   details.push(...await Promise.all(assets.slice(offset,offset+5).map(asset=>read<PublicAssetDetail>(`/api/public/assets/${encodeURIComponent(asset.dataset_id)}`))));
  return details;
 })();
 const entry={until:Date.now()+15000,data};directoryCache=entry;
 void data.catch(()=>{if(directoryCache===entry)directoryCache=undefined});
 return data;
}
const directoryPaths=new Set(["/api/v1/resource-directories","/api/v1/assets/directories"]);

// Project only already-public DTOs into the existing directory UI. Never query
// privileged APIs or manufacture empty operational records for the shared account.
export async function publicExperienceRead(path:string,read:PublicReader):Promise<Projection>{
 const url=new URL(path,"https://experience.invalid");
 if(url.pathname==="/api/v1/resource-access")return {available:true,data:{manage_resource:false,review_directory:false}};
 if(!directoryPaths.has(url.pathname))return {available:false};
 const details=await publicDirectories(read);
 const search=(url.searchParams.get("search")??"").toLocaleLowerCase();
 const filtered=details.filter(asset=>{
  const d=asset.details;
  return (!search||`${asset.name} ${asset.description} ${d.resource_code}`.toLocaleLowerCase().includes(search))&&
   (!url.searchParams.get("classification")||d.classification===url.searchParams.get("classification"))&&
   (!url.searchParams.get("sharing_type")||d.sharing_type===url.searchParams.get("sharing_type"))&&
   (!url.searchParams.get("data_layer")||d.data_layer===url.searchParams.get("data_layer"))&&
   (!url.searchParams.get("resource_id")||asset.dataset_id===url.searchParams.get("resource_id"));
 });
 const page=Math.max(1,Number(url.searchParams.get("page"))||1),size=Math.min(100,Math.max(1,Number(url.searchParams.get("page_size"))||15));
 const items:ResourceDirectory[]=filtered.slice((page-1)*size,page*size).map(asset=>{
  const d=asset.details;
  const definition:DirectoryDefinition={...d,name:asset.name,description:asset.description,usage_restrictions:asset.usage_restrictions,
   dictionary_id:"",provider_code:"",responsible_user_id:"",non_sharing_reason:"",open_to_public:true,
   department_approval_ref:"",assessment_ref:"",related_resource_codes:[]};
  const fields:Field[]=d.items.map(item=>({name:item.name,business_name:item.business_name,logical_type:item.logical_type,nullable:item.nullable,sensitive:item.sensitive,precision:item.precision,scale:item.scale,primary_key:false}));
  return {directory_id:asset.dataset_id,version:asset.contract_version,state:"published",department_id:asset.provider_department_id,
   revision_id:"",revision_state:"approved",published_revision_id:"",definition,reason:"",available:true,
   sharing_snapshot:null,permissions:{},resource:{resource_type:"governed_dataset",resource_id:asset.dataset_id,contract_version:asset.contract_version},fields};
 });
 const data:Page<ResourceDirectory>={items,total:filtered.length,page,page_size:size};
 return {available:true,data};
}
