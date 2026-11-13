"""Provision one explicitly public consumer account and enable its login-page hint.

Run only where a shared testing account is intended. Never publishes existing
business credentials and never grants administrative or source-processing roles.
"""
import json
import os
from pathlib import Path
import secrets
import ssl
import subprocess
from urllib.request import Request, urlopen
from dev_env import ROOT, LOCAL, ENV_FILE, load_env

def write_private(path,value,mode=0o600):
 temporary=path.with_suffix('.tmp')
 with os.fdopen(os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,mode),'w') as stream:
  json.dump(value,stream,ensure_ascii=False,indent=2);stream.write('\n')
 temporary.chmod(mode);temporary.replace(path)

def main():
 env=load_env();base=['docker','compose','--project-name','datamesh','--env-file',str(ENV_FILE)]
 folder=LOCAL/'public-test-accounts';folder.mkdir(mode=0o755,parents=True,exist_ok=True);folder.chmod(0o755)
 private=LOCAL/'public-test-account-provisioning.json';username='experience_viewer'
 def sql(statement):
  return subprocess.run(base+['exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','datamesh_owner','-d','datamesh','-At','-c',statement],cwd=ROOT,env=env,capture_output=True,text=True,check=True).stdout.strip()
 existing=sql("SELECT count(*) FROM users WHERE username='experience_viewer'")!='0'
 if not private.exists():
  if existing:raise RuntimeError('体验账号名称已被占用，未修改现有账号')
  write_private(private,{'username':username,'password':secrets.token_urlsafe(15),'department_id':'测试体验用户','display_name':'平台体验用户','roles':['consumer'],'platforms':['exchange','assets']})
 account=json.loads(private.read_text());account['public_test']=True
 if account['username']!=username:raise RuntimeError('体验账号初始化记录不一致')
 if not existing:
  subprocess.run(base+['run','--rm','--no-deps','-T','--entrypoint','/app/manage','server','create-user'],input=json.dumps(account),cwd=ROOT,env=env,text=True,capture_output=True,check=True)
 subprocess.run(base+['run','--rm','--no-deps','-T','--entrypoint','/app/manage','server','restrict-test-user'],input=json.dumps({'username':username}),cwd=ROOT,env=env,text=True,capture_output=True,check=True)
 origin=env['APP_ORIGIN'].rstrip('/');context=ssl.create_default_context(cafile=str(LOCAL/'certs/server.crt'))
 request=Request(origin+'/auth/login',data=json.dumps({'username':username,'password':account['password']}).encode(),headers={'Content-Type':'application/json','Origin':origin})
 with urlopen(request,context=context,timeout=20) as response:principal=json.load(response)
 if principal['roles']!=['consumer'] or set(principal['platforms'])!={'exchange','assets'}:raise RuntimeError('体验账号权限已变化，未公开凭据')
 version=int(sql("SELECT version FROM users WHERE username='experience_viewer' AND active=true"))
 write_private(folder/'accounts.json',{'items':[{'username':username,'password':account['password'],'version':version,'label':'四平台功能体验','description':'治理支持独立浏览器体验空间，可保存配置并运行示例数据；其余模块只读。体验操作不修改正式业务，不执行任意代码或连接外部数据源。'}]},0o644)
 lines=ENV_FILE.read_text().splitlines();setting='PUBLIC_TEST_ACCOUNTS_FILE=/public-test-accounts/accounts.json'
 previous=[line for line in lines if line.startswith('PUBLIC_TEST_ACCOUNTS_FILE=')]
 if previous and previous[0] not in ['PUBLIC_TEST_ACCOUNTS_FILE=',setting]:raise RuntimeError('已有其他体验账号配置，未覆盖')
 temporary=ENV_FILE.with_suffix('.test-accounts.tmp')
 with os.fdopen(os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600),'w') as stream:stream.write('\n'.join(line for line in lines if not line.startswith('PUBLIC_TEST_ACCOUNTS_FILE='))+'\n'+setting+'\n')
 temporary.replace(ENV_FILE)
 print(json.dumps({'status':'configured','username':username,'roles':['consumer'],'restart_required':True},ensure_ascii=False))

if __name__=='__main__':
 try:main()
 except (OSError,ValueError,KeyError,RuntimeError,subprocess.SubprocessError) as error:
  print(str(error) if isinstance(error,RuntimeError) else '体验账号初始化失败；未输出凭据，请检查私密配置和组件状态。');raise SystemExit(1)
