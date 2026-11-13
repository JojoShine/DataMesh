"""Deterministic fictional business records; no real personal data or credentials."""
from dataclasses import dataclass
from datetime import date, timedelta
from decimal import Decimal
import hashlib
import json

VERSION = '2026.11.1'
AS_OF = date(2026, 10, 31)
SCENARIOS = {
 'enrollment': '招生报名', 'population': '人口基础库', 'corporate': '法人基础库',
 'deceased': '死亡人口专项库', 'mediation': '矛盾调解', 'real_estate': '不动产数据应用',
}
SURNAMES = '赵钱孙李周吴郑王冯陈褚卫蒋沈韩杨朱秦许何吕施张孔曹严华金魏陶姜谢邹喻柏水窦章云苏'
GIVEN = ['子涵','明轩','雨桐','思远','佳宁','文博','雅琪','承泽','语嫣','浩然','书瑶','景行','若溪','晨曦','知远','欣悦','嘉禾','泽宇','安然','梓萱','逸凡','清扬','舒然','星辰','婉清','亦航','一诺','锦程','芷晴','修远','沐阳','以宁']
ROADS = ['文澜路','春晖路','清和路','望江路','书院路','长宁路','锦绣路','松涛路']
COMMUNITIES = ['春晖社区','文澜社区','清和社区','望江社区','书院社区','长宁社区']
INDUSTRIES = ['信息技术服务','食品制造','批发零售','建筑装饰','商务服务','交通运输','文化创意','医疗器械']

def person(i: int) -> str: return f'RK{i:08d}'
def guardian(i: int) -> str: return person(3000+i)
def entity(i: int) -> str: return f'FR2026{i:07d}'
def household(i: int) -> str: return f'HJ{i:08d}'
def person_name(i: int) -> str:
 return SURNAMES[(i-1)%len(SURNAMES)] + GIVEN[((i-1)//len(SURNAMES))%len(GIVEN)]
def company(i: int) -> str:
 return ['宁江','清和','望江','嘉禾','锦程','瑞丰','文澜','长宁'][(i-1)%8]+['启航','致远','恒盛','新程','华信','同创','博源','嘉诚'][((i-1)//8)%8]+'瑞泽诚安泰宏景和悦兴盛荣嘉明远博信达华清润丰源智联恒新启正康宁金银蓝云锦松竹兰星海天辰峰川岳阳光宁文德成富建立承伟东南西北中'[ ((i-1)//64)%64 ]+INDUSTRIES[(i-1)%8]+('有限公司' if i%5 else '经营部')
def address(i: int) -> str:
 return f'宁江市清和区{ROADS[(i-1)%len(ROADS)]}{1+(i-1)%180}号{1+(i-1)//180%18}幢{101+(i-1)%24}室'
def birth(i: int) -> str:
 year=2021 if i<=3000 else (1940+i%35 if i>7800 else 1968+i%30)
 return date(year,1+i%12,1+i%28).isoformat()
def money(value: int, cents: int=0) -> str: return str(Decimal(value)+Decimal(cents)/100)

def column(name: str, label: str, typ: str='text', *, nullable: bool=False, sensitive: bool=False, description: str='') -> dict:
 logical={'text':'string','integer':'integer','date':'date','numeric(12,2)':'decimal','numeric(18,2)':'decimal','boolean':'boolean'}[typ]
 out={'name':name,'business_name':label,'sql_type':typ,'logical_type':logical,'nullable':nullable,'sensitive':sensitive,'description':description or label}
 if logical=='decimal':out.update(precision=18 if typ=='numeric(18,2)' else 12,scale=2)
 return out
C=column
@dataclass
class Table:
 scenario: str
 name: str
 title: str
 provider: str
 columns: list[dict]
 rows: list[tuple]
 @property
 def qualified(self) -> str: return self.scenario+'.'+self.name
 @property
 def fields(self) -> list[dict]:
  return [{k:v for k,v in c.items() if k!='sql_type'} for c in self.columns]
 @property
 def digest(self) -> str:
  return hashlib.sha256(json.dumps(self.rows,ensure_ascii=False,separators=(',',':')).encode()).hexdigest()

def build_tables() -> list[Table]:
 tables=[]
 def add(s,n,title,provider,cols,rows):
  table=Table(s,n,title,provider,cols,list(rows))
  if not 1000<=len(table.rows)<=10000:raise ValueError('Business table scale is outside 1000–10000: '+table.qualified)
  if len({r[0] for r in table.rows})!=len(table.rows):raise ValueError('Duplicate business key: '+table.qualified)
  if any(len(r)!=len(cols) for r in table.rows):raise ValueError('Column count differs: '+table.qualified)
  tables.append(table)
 common=C('guardian_id','监护人编号')
 add('enrollment','education_students','招生学生基础信息','教育部门',[
  C('student_id','报名编号'),C('child_id','儿童编号'),C('student_name','学生姓名',sensitive=True),C('birth_date','出生日期','date'),C('gender_code','性别代码'),C('enrollment_year','招生年度','integer'),C('entry_stage','报名学段'),C('kindergarten_name','就读幼儿园'),C('registration_area','报名区域'),C('declared_guardian_id','申报监护人编号'),C('application_date','预登记日期','date'),C('application_status','报名状态')],
  ((f'BM2027{i:07d}',person(i),person_name(i),birth(i),str(1+i%2),2027,'primary',['清和第一幼儿园','文澜幼儿园','春晖幼儿园','望江幼儿园'][i%4],'清和区',guardian(i),'2026-10-01','pending' if i%13==0 else 'registered') for i in range(1,3001)))
 add('enrollment','police_children','适龄儿童家庭登记','公安部门',[
  C('child_id','儿童编号'),C('child_name','儿童姓名',sensitive=True),C('birth_date','出生日期','date'),common,C('guardian_name','监护人姓名',sensitive=True),C('guardian_relation','监护关系'),C('household_id','户号'),C('household_address','户籍地址',sensitive=True),C('residence_permit_no','居住登记编号'),C('permit_valid_until','居住登记有效期','date'),C('residence_address','居住地址',nullable=True,sensitive=True)],
  ((person(i),person_name(i),birth(i),guardian(i),(' '+person_name(3000+i)+' ') if i%29==0 else person_name(3000+i),str(1+i%2),household(i),address(i),f'JZ2026{i:07d}','2028-08-31',None if i%75==0 else address(i)) for i in range(1,3001)))
 add('enrollment','civil_marriages','监护人婚姻登记','民政部门',[
  C('marriage_id','婚姻登记编号'),common,C('spouse_id','配偶编号'),C('spouse_name','配偶姓名',sensitive=True),C('marital_status','婚姻状态'),C('registration_date','登记日期','date')],
  ((f'HY{i:09d}',guardian(i),person(6001+(i-1)%1800),person_name(6001+(i-1)%1800),'2' if i%17==0 else '1','2015-05-20') for i in range(1,3001)))
 add('enrollment','property_ownership','监护人不动产权属','不动产登记部门',[
  C('property_id','不动产单元编号'),common,C('owner_name','权利人姓名',sensitive=True),C('property_address','坐落地址',sensitive=True),C('property_area','建筑面积（平方米）','numeric(12,2)'),C('ownership_share','持有份额（百分比）','integer'),C('registration_date','登记日期','date')],
  ((f'BDCDY{i:09d}',guardian(i),person_name(3000+i),address(i),money(65+i%100,50),50 if i<=500 else 100,'2019-06-18') for i in range(1,3001)))
 add('enrollment','social_insurance','监护人社会保险缴费','人社部门',[
  C('payment_id','缴费汇总编号'),common,C('employer_name','参保单位'),C('period_start','统计期开始','date'),C('period_end','统计期结束','date'),C('paid_months','缴费月数','integer'),C('paid_amount','累计缴费金额（元）','numeric(12,2)'),C('insurance_status','参保状态')],
  ((f'SB2026{i:07d}',guardian(i),company(i),'2025-11-01','2026-10-31',12-i%5,money((12-i%5)*(650+i%400)),'0' if i%23==0 else '1') for i in range(1,3001)))
 add('enrollment','market_entities','监护人经营主体','市场监管部门',[
  C('entity_code','主体编号'),common,C('entity_name','主体名称'),C('entity_type','主体类型'),C('business_status','经营状态'),C('registration_date','成立日期','date'),C('business_address','经营地址')],
  ((entity(i),guardian(i),company(i),'2' if i%5==0 else '1','0' if i%31==0 else '1','2020-03-01',address(i)) for i in range(1,3001)))
 add('enrollment','tax_registrations','经营主体税务登记','税务部门',[
  C('tax_registration_id','税务登记编号'),C('entity_code','主体编号'),C('taxpayer_name','纳税人名称'),C('tax_status','税务状态'),C('registration_date','税务登记日期','date'),C('tax_authority','主管税务机关')],
  ((f'SWDJ{i:09d}',entity(i),company(i),'0' if i%31==0 else '1','2020-03-08','清和区税务局') for i in range(1,3001)))
 add('population','households','居民户籍家庭','公安部门',[
  C('household_id','户号'),C('household_type','户别'),C('community_name','所属社区'),C('household_address','户籍地址',sensitive=True),C('registered_at','立户日期','date')],
  ((household(i),'家庭户',COMMUNITIES[i%6],address(i),'2014-06-01') for i in range(1,3001)))
 add('population','residents','人口基础信息','公安部门',[
  C('person_id','人员编号'),C('person_name','姓名',sensitive=True),C('gender_code','性别代码'),C('birth_date','出生日期','date'),C('household_id','户号'),C('population_status','人口状态'),C('updated_at','更新日期','date')],
  ((person(i),person_name(i),str(1+i%2),birth(i),household(1+(i-1)%3000),'死亡' if i>7800 else '正常','2026-10-31') for i in range(1,9001)))
 add('population','residence_records','人口居住登记','公安部门',[
  C('residence_id','居住记录编号'),C('person_id','人员编号'),C('residence_address','居住地址',sensitive=True),C('residence_type','居住类型'),C('community_name','所属社区'),C('registered_at','登记日期','date')],
  ((f'JZJL{i:09d}',person(i),address(1+(i-1)%3000),'自有住房' if i%4 else '租赁住房',COMMUNITIES[i%6],'2025-09-01') for i in range(1,9001)))
 add('corporate','enterprises','法人及经营主体基础信息','市场监管部门',[
  C('entity_code','主体编号'),C('entity_name','主体名称'),C('entity_type','主体类型'),C('industry_name','行业门类'),C('business_status','经营状态'),C('registration_date','成立日期','date'),C('registered_capital','注册资本（万元）','numeric(18,2)'),C('business_address','经营地址')],
  ((entity(i),company(i),'个体工商户' if i%5==0 else '有限责任公司',INDUSTRIES[i%8],'注销' if i%31==0 else '存续','2020-03-01',money(50+i%1950),address(i)) for i in range(1,3001)))
 add('corporate','representatives','经营主体负责人登记','市场监管部门',[
  C('representative_id','负责人登记编号'),C('entity_code','主体编号'),C('person_id','负责人编号'),C('representative_name','负责人姓名',sensitive=True),C('position_name','职务'),C('effective_date','任职日期','date')],
  ((f'FZRDJ{i:09d}',entity(i),guardian(i),person_name(3000+i),'经营者' if i%5==0 else '法定代表人','2020-03-01') for i in range(1,3001)))
 add('corporate','annual_tax','经营主体年度纳税汇总','税务部门',[
  C('tax_summary_id','年度汇总编号'),C('entity_code','主体编号'),C('fiscal_year','所属年度','integer'),C('declared_revenue','申报收入（元）','numeric(18,2)'),C('tax_paid','已缴税额（元）','numeric(18,2)'),C('filing_status','申报状态')],
  ((f'NSHZ{2024+(i-1)//3000}{1+(i-1)%3000:07d}',entity(1+(i-1)%3000),2024+(i-1)//3000,money(120000+(i*7919)%8000000),money(3600+(i*237)%240000),'已申报') for i in range(1,6001)))
 add('deceased','death_registrations','死亡人口登记','卫生健康部门',[
  C('death_record_id','死亡登记编号'),C('person_id','人员编号'),C('person_name','姓名',sensitive=True),C('death_date','死亡日期','date'),C('registration_date','登记日期','date'),C('reporting_unit','报告单位'),C('verification_status','核验状态')],
  ((f'SWDJ2026{i:07d}',person(7800+i),person_name(7800+i),(date(2026,9,20)-timedelta(days=i%300)).isoformat(),(date(2026,9,23)-timedelta(days=i%300)).isoformat(),['清和区人民医院','宁江市中心医院','清和区卫生服务中心'][i%3],'已核验') for i in range(1,1201)))
 add('deceased','household_cancellations','死亡人口户籍注销办理','公安部门',[
  C('cancellation_id','注销办理编号'),C('death_record_id','死亡登记编号'),C('person_id','人员编号'),C('cancellation_status','办理状态'),C('cancellation_date','注销日期','date',nullable=True),C('handling_unit','办理单位')],
  ((f'HJZX2026{i:07d}',f'SWDJ2026{i:07d}',person(7800+i),'待核查' if i%10==0 else '已注销',None if i%10==0 else (date(2026,9,28)-timedelta(days=i%300)).isoformat(),'清和区户政服务中心') for i in range(1,1201)))
 add('mediation','cases','矛盾纠纷受理信息','司法行政部门',[
  C('case_id','案件编号'),C('case_type','纠纷类型'),C('community_name','发生社区'),C('accepted_date','受理日期','date'),C('case_status','办理状态'),C('handling_unit','承办机构'),C('dispute_amount','争议金额（元）','numeric(12,2)')],
  ((f'TJ2026{i:07d}',['邻里纠纷','物业服务纠纷','合同纠纷','劳动争议','家庭纠纷'][i%5],COMMUNITIES[i%6],(date(2026,9,1)-timedelta(days=i%300)).isoformat(),'办理中' if i%7==0 else '已办结','清和区人民调解中心',money(500+(i*137)%100000)) for i in range(1,2401)))
 add('mediation','parties','矛盾纠纷当事人登记','司法行政部门',[
  C('party_id','当事人登记编号'),C('case_id','案件编号'),C('person_id','人员编号'),C('party_name','姓名',sensitive=True),C('party_role','当事人角色')],
  ((f'DSR{i:09d}',f'TJ2026{1+(i-1)//2:07d}',person(3001+(i-1)%4800),person_name(3001+(i-1)%4800),'申请方' if i%2 else '被申请方') for i in range(1,4801)))
 add('mediation','resolutions','矛盾纠纷调解办理结果','司法行政部门',[
  C('resolution_id','调解结果编号'),C('case_id','案件编号'),C('resolution_status','调解结果'),C('agreement_date','协议日期','date',nullable=True),C('agreed_amount','协议金额（元）','numeric(12,2)'),C('performance_status','履行状态')],
  ((f'TJJG{i:09d}',f'TJ2026{i:07d}','继续调解' if i%7==0 else '达成协议',None if i%7==0 else (date(2026,9,11)-timedelta(days=i%300)).isoformat(),money(0 if i%7==0 else (500+(i*137)%100000)*8//10),'待履行' if i%7==0 else '已履行') for i in range(1,2401)))
 add('real_estate','property_units','不动产单元基础信息','自然资源部门',[
  C('property_id','不动产单元编号'),C('property_address','坐落地址',sensitive=True),C('property_type','房屋用途'),C('property_area','建筑面积（平方米）','numeric(12,2)'),C('completion_year','竣工年度','integer'),C('registration_date','登记日期','date')],
  ((f'BDCDY{i:09d}',address(i),'住宅' if i%8 else '商业',money(65+i%100,50),2005+i%15,'2019-06-18') for i in range(1,3501)))
 add('real_estate','ownerships','不动产权利人登记','自然资源部门',[
  C('ownership_id','权属登记编号'),C('property_id','不动产单元编号'),C('person_id','权利人编号'),C('owner_name','权利人姓名',sensitive=True),C('ownership_share','权属份额（百分比）','integer'),C('certificate_no','权属证书编号')],
  ((f'QS{i:09d}',f'BDCDY{(i if i<=3500 else i-3500):09d}',person((3000+i) if i<=3500 else (6000+i-3500)),person_name((3000+i) if i<=3500 else (6000+i-3500)),50 if i<=500 or i>3500 else 100,f'NJ2026QS{i:08d}') for i in range(1,4001)))
 add('real_estate','transactions','不动产交易登记','自然资源部门',[
  C('transaction_id','交易登记编号'),C('property_id','不动产单元编号'),C('transaction_date','交易日期','date'),C('transaction_type','交易类型'),C('transaction_amount','成交金额（元）','numeric(18,2)'),C('registration_status','登记状态')],
  ((f'BDCZY2026{i:07d}',f'BDCDY{i:09d}',(date(2026,8,31)-timedelta(days=i%250)).isoformat(),'存量房转移',money(650000+(i*9127)%3500000),'已登簿') for i in range(1,1801)))
 return tables

def sql_literal(value):
 if value is None:return 'NULL'
 if isinstance(value,bool):return 'true' if value else 'false'
 if isinstance(value,int):return str(value)
 return "'"+str(value).replace("'","''")+"'"

def table_schema_sql(table: Table) -> str:
 cols=[f'"{c["name"]}" {c["sql_type"]}'+('' if c['nullable'] else ' NOT NULL') for c in table.columns]
 cols.append('PRIMARY KEY ("'+table.columns[0]['name']+'")')
 sql=f'CREATE TABLE {table.qualified} ('+', '.join(cols)+');\n'
 sql+='COMMENT ON TABLE '+table.qualified+' IS '+sql_literal(table.title)+';\n'
 sql+='\n'.join('COMMENT ON COLUMN '+table.qualified+'.'+c['name']+' IS '+sql_literal(c['business_name']+'；'+c['description'])+';' for c in table.columns)
 return sql

def insertion_sql(table: Table) -> str:
 chunks=[]
 for start in range(0,len(table.rows),500):
  chunks.append('INSERT INTO '+table.qualified+' VALUES '+','.join('('+','.join(sql_literal(v) for v in row)+')' for row in table.rows[start:start+500])+';')
 return '\n'.join(chunks)
