#!/usr/bin/env python3
"""Build pinned isolated runtimes and configure the private runner without printing its secret."""
import hashlib,json,os,secrets,subprocess
from pathlib import Path
from dev_env import ROOT,ENV_FILE,load_env

def main():
 env=load_env();images={}
 for language in ('python','javascript'):
  tag='datamesh-code-'+language+':1'
  subprocess.run(['docker','build','-f','deploy/docker/governance-code/'+language+'.Dockerfile','-t',tag,'.'],cwd=ROOT,env=env,check=True)
  result=subprocess.run(['docker','image','inspect',tag],cwd=ROOT,env=env,check=True,capture_output=True,text=True);images[language]=json.loads(result.stdout)[0]['Id']
 folder=ROOT/'.local/code-runner';folder.mkdir(mode=0o700,exist_ok=True);token=folder/'token'
 # Compose may have created an empty bind-source directory before runtime setup.
 if token.is_dir() and not any(token.iterdir()):token.rmdir()
 if token.exists() and not token.is_file():raise RuntimeError('Runner token path must be a private file')
 if not token.exists():
  fd=os.open(token,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
  with os.fdopen(fd,'w') as stream:stream.write(secrets.token_hex(32))
 if token.stat().st_mode&0o077:raise RuntimeError('Runner token must be readable only by its owner')
 content=ENV_FILE.read_text().splitlines();keys={'CODE_PYTHON_IMAGE':images['python'],'CODE_JAVASCRIPT_IMAGE':images['javascript']};content=[line for line in content if not any(line.startswith(key+'=') for key in keys)];content += [key+'='+value for key,value in keys.items()];ENV_FILE.write_text('\n'.join(content)+'\n')
 manifest={'images':images,'wrappers':{name:hashlib.sha256((ROOT/'deploy/docker/governance-code'/name).read_bytes()).hexdigest() for name in ('python.py','javascript.cjs')}};(folder/'runtime-images.json').write_text(json.dumps(manifest,indent=2)+'\n');print('Two pinned code runtimes configured; private token retained.')
if __name__=='__main__':main()
