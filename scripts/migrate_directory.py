#!/usr/bin/env python3
"""Report directory migration gaps; stage drafts without issuing codes or approvals."""
import argparse
import json
import subprocess
import sys

from dev_env import compose


def sql(value):
    return compose('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'datamesh', '-v', 'ON_ERROR_STOP=1', '-At', '-c', value, capture=True).stdout.strip()


def report():
    return json.loads(sql("""SELECT json_build_object(
      'resources', (SELECT count(*) FROM business_records WHERE kind IN ('object','dataset')),
      'unmounted_resources', (SELECT COALESCE(json_agg(json_build_object('kind',r.kind,'id',r.id,'department_id',r.department_id)), '[]'::json) FROM business_records r WHERE r.kind IN ('object','dataset') AND NOT EXISTS (SELECT 1 FROM business_records m WHERE m.kind='mount' AND m.state='active' AND m.body->>'resource_key'=(CASE WHEN r.kind='object' THEN 'raw_object' ELSE 'governed_dataset' END)||':'||r.id||':'||r.version)),
      'legacy_active_grants', (SELECT count(*) FROM business_records WHERE kind='grant' AND state='active' AND (body->'sharing_snapshot' IS NULL OR body->'sharing_snapshot'='null'::jsonb)),
      'active_assignments', (SELECT count(*) FROM business_records WHERE kind='assignment' AND state='active' AND (body->>'expires_at')::timestamptz>now()),
      'verified_dictionaries', (SELECT count(*) FROM business_records WHERE kind='directory_dictionary' AND state='active' AND body->>'verified'='true'),
      'published_assets_requiring_open_review', (SELECT count(*) FROM business_records d WHERE d.kind='dataset' AND d.state='published' AND NOT EXISTS(SELECT 1 FROM business_records m JOIN business_records mr ON mr.kind='mount_revision' AND mr.id=m.body->>'active_id' JOIN business_records c ON c.kind='directory' AND c.id=m.parent_id JOIN business_records cr ON cr.kind='directory_revision' AND cr.id=c.body->>'published_id' WHERE m.kind='mount' AND m.state='active' AND mr.body->'resource'->>'resource_id'=d.id AND c.state='published' AND cr.body->'definition'->>'open_to_public'='true'))
    )"""))


def stage_drafts():
    # Stable IDs make reruns safe. The classification is a provisional department
    # bucket; nothing here claims a formal code, approval or available service.
    sql("""BEGIN;
    INSERT INTO business_records(kind,id,department_id,owner_id,parent_id,state,version,body)
    SELECT 'directory','legacy-directory-'||r.id,r.department_id,r.owner_id,r.id,'draft',1,
      jsonb_build_object('draft_id','legacy-revision-'||r.id,'published_id','','resource_code','')
    FROM business_records r WHERE r.kind IN ('object','dataset')
    AND NOT EXISTS(SELECT 1 FROM business_records m JOIN business_records mr ON mr.kind='mount_revision' AND mr.parent_id=m.id WHERE m.kind='mount' AND mr.body->'resource'->>'resource_id'=r.id)
    ON CONFLICT DO NOTHING;
    INSERT INTO business_records(kind,id,department_id,owner_id,parent_id,state,version,body)
    SELECT 'directory_revision','legacy-revision-'||r.id,r.department_id,r.owner_id,'legacy-directory-'||r.id,'draft',1,
      jsonb_build_object('definition',jsonb_build_object(
        'name',COALESCE(r.body->'definition'->>'name',r.body->>'qualified_name',r.id),
        'description','存量资源待补编：请核对业务责任人、目录分类、编码及共享条件。',
        'classification','department','classification_path','','resource_code','','dictionary_id','',
        'provider_name',r.department_id,'provider_code','','responsible_user_id',r.owner_id,
        'update_frequency','','format','database','data_layer',CASE WHEN r.kind='object' THEN 'raw_object' ELSE 'governed_dataset' END,
        'items',COALESCE((SELECT jsonb_agg(f||jsonb_build_object('business_name',f->>'name','length',NULL,'description','')) FROM jsonb_array_elements(COALESCE(r.body->'definition'->'fields',r.body->'fields','[]'::jsonb)) f),'[]'::jsonb),
        'sharing_type','conditional','sharing_conditions','','non_sharing_reason','','legal_basis','',
        'sharing_method','query','usage_restrictions','','security_classification','','open_to_public',false,
        'open_conditions','','department_approval_ref','','assessment_ref','','related_resource_codes','[]'::jsonb),
        'submitted_by','','department_reviewer','','authority_reviewer','','reason','待补编，原创建人仅作暂定责任人')
    FROM business_records r WHERE r.kind IN ('object','dataset') AND EXISTS(SELECT 1 FROM business_records d WHERE d.kind='directory' AND d.id='legacy-directory-'||r.id)
    ON CONFLICT DO NOTHING;
    COMMIT;""")


def main():
    p=argparse.ArgumentParser(description=__doc__)
    mode=p.add_mutually_exclusive_group(required=True)
    mode.add_argument('--report',action='store_true')
    mode.add_argument('--stage-drafts',action='store_true')
    mode.add_argument('--verify-cutover',action='store_true')
    args=p.parse_args()
    if args.stage_drafts:
        stage_drafts()
    result=report()
    result['mode']='stage-drafts' if args.stage_drafts else 'verify-cutover' if args.verify_cutover else 'report'
    result['legacy_grants_are_usable']=False
    print(json.dumps(result,ensure_ascii=False,indent=2))
    if args.verify_cutover and (result['unmounted_resources'] or result['verified_dictionaries']==0 or result['active_assignments']==0):
        print('仍有资源待编目或挂载；新版本门禁会阻止其交付。',file=sys.stderr)
        return 1
    return 0


if __name__=='__main__':
    try:
        sys.exit(main())
    except (OSError,ValueError,subprocess.SubprocessError):
        print('目录迁移检查失败；未输出连接信息。',file=sys.stderr)
        sys.exit(1)
