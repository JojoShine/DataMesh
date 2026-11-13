#!/usr/bin/env python3
"""Validate actual HTTP responses against the frozen OpenAPI schemas."""
import json
import sys
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker
from business import Client, ROOT


def main():
    spec=json.loads((ROOT/'api/openapi/platform.yaml').read_text())
    accounts=json.loads((ROOT/'.local/business-accounts.json').read_text())
    ids=json.loads((ROOT/'.local/smoke/business-result.json').read_text())
    provider=Client(accounts['provider']);consumer=Client(accounts['consumer'])
    paths=[('/api/v1/resource-directories','DirectoryList'),('/api/v1/resource-mounts','MountList'),('/api/v1/sources','SourceList'),('/api/v1/tasks','TaskList'),('/api/v1/rules','RuleList'),('/api/v1/datasets','DatasetList'),('/api/v1/applications','ApplicationList'),('/api/v1/grants','GrantList'),('/api/v1/sources/'+ids['object_id'].removesuffix('-customers')+'/objects','SourceObjectList'),('/api/v1/datasets/'+ids['dataset_id']+'/quality','Quality'),('/api/v1/datasets/'+ids['dataset_id']+'/lineage','Lineage'),('/api/public/assets','PublicAssetList')]
    verified=[]
    def validate(schema,value):
        root={'$ref':'#/components/schemas/'+schema,'components':spec['components']}
        Draft202012Validator(root,format_checker=FormatChecker()).validate(value)
        verified.append(schema)
    for path,schema in paths:validate(schema,provider.call('GET',path))
    source_id=ids['object_id'].removesuffix('-customers')
    validate('Preflight',provider.call('POST',f'/api/v1/sources/{source_id}/preflight-cdc'))
    validate('DatasetPage',consumer.call('GET',f'/delivery/v1/datasets/{ids["dataset_id"]}/records?contract_version=1&limit=5'))
    validate('RawPage',consumer.call('GET',f'/delivery/v1/raw/{ids["object_id"]}/records?contract_version=1&limit=5'))
    for client in [provider,consumer]:client.call('POST','/auth/logout')
    result={'result':'passed','http_response_schemas':verified}
    (ROOT/'.local/smoke/business-contract-result.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result))


if __name__=='__main__':
    try:main()
    except Exception as exc:
        # Validator payloads may contain real data; report the schema path without the instance.
        if hasattr(exc,'schema_path'):
            print('HTTP contract mismatch at schema path '+'.'.join(map(str,exc.schema_path)),file=sys.stderr)
        else:print(str(exc) if isinstance(exc,RuntimeError) else 'HTTP contract validation failed',file=sys.stderr)
        sys.exit(1)
