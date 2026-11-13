// @vitest-environment jsdom
import {afterEach,expect,it,vi} from 'vitest';
import {act,cleanup,fireEvent,render,screen,waitFor} from '@testing-library/react';
import {DataSandbox} from '../src/pages/DataSandbox';
import {useSession} from '../src/stores/session';
const calls=vi.hoisted(()=>[] as string[]);
vi.mock('../src/hooks/useQuery',()=>({useQuery:(path:string|null)=>{if(path)calls.push(path);return {data:!path?undefined:path.includes('/records')?{items:[],total:0,fields:[]}:path==='/api/v1/data-sandboxes'?{items:[{id:'box',name:'我的沙箱',state:'active'}]}:path?.includes('/tables')?{items:[{id:'table',name:'orders',state:'ready',fields:[],field_labels:{}}]}:{items:[],total:0},loading:false,error:null,reload:vi.fn()}}}));
vi.mock('../src/api/client',()=>({mutation:vi.fn(async()=>({id:'q',state:'queued',sql:'SELECT 1'})),request:vi.fn(),errorMessage:()=>''}));
vi.mock('antd',async()=>{const actual=await vi.importActual<typeof import('antd')>('antd');return {...actual,Select:({onChange}:{onChange:(id:string)=>void})=><button onClick={()=>onChange('box')}>选择测试沙箱</button>}});
afterEach(()=>{cleanup();calls.length=0;vi.clearAllMocks()});
it('排队查询不读取旧表结果，结果表名独立，历史按标准分页请求，登录变化清空草稿',async()=>{
 vi.stubGlobal('ResizeObserver',class {observe(){} unobserve(){} disconnect(){}});
 Object.defineProperty(window,'matchMedia',{writable:true,value:()=>({matches:false,addListener(){},removeListener(){},addEventListener(){},removeEventListener(){}})});
 useSession.setState({user:null,epoch:1});render(<DataSandbox/>);
 fireEvent.click(screen.getByText('选择测试沙箱'));
 expect(calls).toContain('/api/v1/data-sandboxes/box/queries?page=1&page_size=15');
 fireEvent.click(screen.getByRole('button',{name:'orders'}));
 fireEvent.change(screen.getByLabelText('新结果表名'),{target:{value:'summary'}});
 fireEvent.click(screen.getByRole('button',{name:'保存为新逻辑表'}));
 const {mutation}=await import('../src/api/client');
 await waitFor(()=>expect(mutation).toHaveBeenCalledWith('/api/v1/data-sandboxes/box/queries',{sql:'SELECT * FROM orders LIMIT 100',mode:'materialize',table_name:'summary'}));
 expect(calls.some(path=>path.includes('/queries/q/records'))).toBe(false);
 act(()=>useSession.setState({epoch:2}));
 expect((screen.getByLabelText('沙箱名称') as HTMLInputElement).value).toBe('');
 expect(screen.queryByLabelText('新结果表名')).toBeNull();
});
