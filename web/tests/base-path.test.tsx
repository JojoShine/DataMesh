// @vitest-environment jsdom
import {afterEach,beforeEach,expect,it,vi} from 'vitest';
import {applicationURL,logicalPath} from '../src/app/basePath';
import {navigate,registerNavigationGuard} from '../src/app/navigation';

beforeEach(()=>{vi.stubEnv('BASE_URL','/datamesh/');window.scrollTo=vi.fn()});
afterEach(()=>{vi.unstubAllEnvs();history.replaceState(null,'','/')});

it('应用前缀只添加一次，保留查询与片段，外部或相对地址不改写',()=>{
 expect(applicationURL('/api/v1/me')).toBe('/datamesh/api/v1/me');
 expect(applicationURL('/datamesh/platform/governance?file_id=a%2Fb#sql')).toBe('/datamesh/platform/governance?file_id=a%2Fb#sql');
 for(const url of ['https://example.com/api','//example.com/static/a.png','#section','blob:sample','?page=2'])expect(applicationURL(url)).toBe(url);
 expect(logicalPath('/datamesh/platform/governance?file_id=a%2Fb#sql')).toBe('/platform/governance?file_id=a%2Fb#sql');
 expect(logicalPath('/datameshx/api')).toBe('/datameshx/api');
});

it('前缀下导航保留空间与私人文件边界，守卫处理逻辑路径',()=>{
 history.replaceState(null,'','/datamesh/platform/governance/dataflows?workspace_id=space');
 navigate('/platform/governance/models');
 expect(location.pathname+location.search).toBe('/datamesh/platform/governance/models?workspace_id=space');
 navigate('/platform/governance/sandbox?file_id=private&workspace_id=space');
 expect(location.pathname+location.search).toBe('/datamesh/platform/governance/sandbox?file_id=private');
 const guard=vi.fn(()=>false);const stop=registerNavigationGuard(guard);
 try{navigate('/platform/governance/lightweight');expect(guard).toHaveBeenCalledWith('/platform/governance/lightweight');expect(location.pathname).toBe('/datamesh/platform/governance/sandbox')}finally{stop()}
});

it('根目录部署保持原有地址',()=>{
 vi.stubEnv('BASE_URL','/');
 expect(applicationURL('/api/v1/me')).toBe('/api/v1/me');
 expect(logicalPath('/platform/governance')).toBe('/platform/governance');
});
