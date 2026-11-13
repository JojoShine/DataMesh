#!/usr/bin/env python3
"""Verify live permission isolation, retaining a usable local demonstration afterward."""
import json
import sys

from business import Client, ROOT
from directory import prepare_processing


def main():
    accounts=json.loads((ROOT/'.local/business-accounts.json').read_text())
    ids=json.loads((ROOT/'.local/smoke/business-result.json').read_text())
    admin=Client(json.loads((ROOT/'.local/bootstrap-admin.json').read_text()))
    provider=Client(accounts['provider']);reviewer=Client(accounts['directory_reviewer']);consumer=Client(accounts['consumer'])
    governed=f'/delivery/v1/datasets/{ids["dataset_id"]}/records?contract_version=1&limit=1'
    raw=f'/delivery/v1/raw/{ids["object_id"]}/records?contract_version=1&limit=1'
    for client in [provider,reviewer]:
        client.call('GET',governed,expected=403)
        client.call('GET',raw,expected=403)
    admin.call('GET',governed)
    admin.call('GET',raw)
    pending=reviewer.call('GET','/api/v1/processing-authorizations?page_size=100')['items']
    current=next(r for r in pending if r['state']=='active' and r['definition']['user_id']==provider.principal['user_id'] and r['definition']['resource']['resource_id']==ids['object_id'])
    objects=provider.call('GET','/api/v1/resources/raw?page_size=100')['items']
    obj=next(r for r in objects if r['object_id']==ids['object_id'])
    reviewer.call('POST','/api/v1/processing-authorizations/'+current['id']+'/revoke',{'expected_version':current['version'],'reason':'LOCAL-TEST：验证加工与消费授权隔离'})
    try:
        consumer.call('GET',governed,expected=403)
        provider.call('POST','/api/v1/datasets/'+ids['dataset_id']+'/check',expected=403)
        result=consumer.call('GET',raw)
        if not result.get('events'):raise RuntimeError('Independent raw grant became unavailable')
    finally:
        prepare_processing(reviewer,provider,obj)
    result=consumer.call('GET',governed)
    if not result.get('records'):raise RuntimeError('Governed grant did not recover after renewed processing basis')
    output={'result':'passed','checks':['admin-full-content-access','operator-no-implicit-content','reviewer-no-implicit-content','processing-revocation-blocks-derived-delivery','processing-revocation-blocks-new-quality-action','raw-and-governed-independent','renewed-basis-restores-valid-delivery']}
    (ROOT/'.local/smoke/directory-permissions-result.json').write_text(json.dumps(output,indent=2)+'\n')
    for client in [admin,provider,reviewer,consumer]:client.call('POST','/auth/logout')
    print(json.dumps(output))


if __name__=='__main__':
    try:main()
    except (OSError,ValueError,StopIteration,RuntimeError) as exc:
        print(str(exc) if isinstance(exc,RuntimeError) else 'Directory permission verification failed; no credentials logged',file=sys.stderr)
        sys.exit(1)
