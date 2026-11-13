"""Build a checksummed deployment archive without runtime secrets or business data."""
import argparse
from datetime import datetime
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

ROOT=Path(__file__).resolve().parents[1]
CONTENT=['README.md','compose.yaml','go.mod','go.sum','.dockerignore','cmd','internal','migrations','configs','deploy','scripts','api','contracts','web','tests','docs/operations']

def schema_version(folder):
 up={path.name.removesuffix('.up.sql') for path in folder.glob('*.up.sql')}
 down={path.name.removesuffix('.down.sql') for path in folder.glob('*.down.sql')}
 if not up or up!=down:
  raise RuntimeError('迁移文件必须完整配对，无法确定发行结构版本')
 versions=[]
 for name in up:
  match=re.fullmatch(r'([0-9]{6})_[a-zA-Z0-9_]+',name)
  if not match:
   raise RuntimeError('迁移文件名称不符合版本约定')
  versions.append(int(match.group(1)))
 if sorted(versions)!=list(range(1,max(versions)+1)):
  raise RuntimeError('迁移版本重复或不连续，拒绝发行')
 return max(versions)

def file_hash(path):
 digest=hashlib.sha256()
 with path.open('rb') as stream:
  while block:=stream.read(1024*1024):digest.update(block)
 return digest.hexdigest()

def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--release',default='datamesh-'+datetime.now().strftime('%Y%m%d-%H%M%S'))
 parser.add_argument('--image',required=True,help='Previously verified local application image tag')
 parser.add_argument('--include-image',action='store_true')
 args=parser.parse_args()
 schema=schema_version(ROOT/'migrations')
 if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9._-]{0,100}',args.release):raise RuntimeError('发行名称仅允许字母、数字、点、下划线和横线')
 if not (ROOT/'web/dist/index.html').is_file():raise RuntimeError('请先完成前端构建')
 image=json.loads(subprocess.run(['docker','image','inspect',args.image],check=True,capture_output=True,text=True).stdout)[0]
 output=ROOT/'.local/release-packages';output.mkdir(mode=0o700,parents=True,exist_ok=True)
 archive=output/(args.release+'.tar.gz')
 if archive.exists():raise RuntimeError('发行包已存在，请使用新的发行名称')
 with tempfile.TemporaryDirectory(dir=output) as temporary:
  folder=Path(temporary)/args.release;folder.mkdir(mode=0o700)
  for item in CONTENT:
   source=ROOT/item
   files=source.rglob('*') if source.is_dir() else [source]
   for path in files:
    relative=path.relative_to(ROOT)
    if any(part in {'.local','.git','__pycache__','node_modules'} for part in relative.parts) or path.is_symlink() or not path.is_file():continue
    target=folder/relative;target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(path,target)
  if args.include_image:
   subprocess.run(['docker','image','save','--output',str(folder/'application-image.tar'),args.image],check=True,capture_output=True)
  files={str(path.relative_to(folder)):file_hash(path) for path in sorted(folder.rglob('*')) if path.is_file()}
  manifest={'release':args.release,'schema':schema,'application_image':args.image,'image_id':image['Id'],'os':image['Os'],'architecture':image['Architecture'],'image_included':args.include_image,'files':files,'runtime_config_included':False,'business_data_included':False,'instructions':'docs/operations/deployment-readiness.md'}
  (folder/'release.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
  with tarfile.open(archive,'w:gz') as stream:stream.add(folder,arcname=args.release)
 archive.chmod(0o600)
 digest=file_hash(archive);archive.with_suffix(archive.suffix+'.sha256').write_text(digest+'  '+archive.name+'\n')
 print(json.dumps({'release':args.release,'archive':str(archive),'sha256':digest,'schema':schema,'architecture':image['Architecture'],'files':len(files)},ensure_ascii=False))

if __name__=='__main__':
 try:main()
 except (OSError,ValueError,RuntimeError,subprocess.SubprocessError) as error:
  print(str(error) if isinstance(error,RuntimeError) else '发行打包失败，未输出运行配置或凭据。');raise SystemExit(1)
