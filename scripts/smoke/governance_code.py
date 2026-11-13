#!/usr/bin/env python3
"""Exercise an explicitly configured private code runner; never prints row values or tokens."""
import argparse,json,time
from pathlib import Path
from urllib.request import Request,urlopen
from urllib.error import HTTPError

def main():
 parser=argparse.ArgumentParser();parser.add_argument('--state',required=True);args=parser.parse_args();state=json.loads(Path(args.state).read_text());token=Path(state['token_file']).read_text().strip();origin=state['origin'];jobs=[]
 def call(method,path,body=None):
  try:
   with urlopen(Request(origin+path,data=None if body is None else json.dumps(body).encode(),method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'}),timeout=135) as response:return response.status,json.loads(response.read() or 'null')
  except HTTPError as e:return e.code,json.load(e)
 def create(language,source,timeout=120000):
  runtime=next(r for r in state['runtimes'] if r['language']==language);status,result=call('POST','/v1/jobs',{'language':language,'runtime_version':runtime['runtime_version'],'source':source,'context':{},'timeout_ms':timeout});assert status==201,(status,result);jobs.append(result['job_id']);return result['job_id']
 def batch(job,values):return call('POST','/v1/jobs/'+job+'/batches',{'sequence':1,'rows':[{'ordinal':1,'values':values}]})
 try:
  for language,source in [('python','from decimal import Decimal\ndef transform(row, context):\n row["name"] = row["name"].strip()\n row["amount"] = str(Decimal(row["amount"]) + Decimal("0.01"))\n return row'),('javascript','function transform(row, context) { row.name=row.name.trim();row.amount=new Decimal(row.amount).plus("0.01").toFixed(2);return row; }')]:
   job=create(language,source);status,result=batch(job,{'name':' 客户甲 ','id':'9223372036854775807','amount':'9999999999999999.99'});assert status==200,(language,status,result);values=result['rows'][0]['values'];assert values=={'name':'客户甲','id':'9223372036854775807','amount':'10000000000000000.00'};assert call('DELETE','/v1/jobs/'+job)[0]==204;print(language+': cleaning, Unicode, integer and exact decimal passed')
  for language,source in [('python','def transform(row, context):\n raise ValueError("SENSITIVE_VALUE")'),('javascript','function transform(row, context) {\n throw new Error("SENSITIVE_VALUE");\n}')]:
   job=create(language,source);status,result=batch(job,{});assert result.get('error') and 'SENSITIVE_VALUE' not in json.dumps(result);assert result['error']['line']>0;call('DELETE','/v1/jobs/'+job);print(language+': safe diagnostics and user line passed')
  job=create('python','def transform(row, context):\n while True: pass',1000);status,result=batch(job,{});assert status>=400;call('DELETE','/v1/jobs/'+job);print('timeout: task terminated')
  job=create('javascript','function transform(row, context) {return row}');assert call('DELETE','/v1/jobs/'+job)[0]==204;assert batch(job,{})[0]==404;print('cancel: task removed')
 finally:
  for job in jobs:call('DELETE','/v1/jobs/'+job)
if __name__=='__main__':main()
