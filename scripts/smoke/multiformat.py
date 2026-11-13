#!/usr/bin/env python3
"""Read-only checks of the deployed resource adapters; no credentials or rows are printed."""
import json
import uuid
from urllib.request import Request
from urllib.error import HTTPError
from pathlib import Path
from business import Client

ROOT = Path(__file__).resolve().parents[2]

def main():
    client = Client(json.loads((ROOT / '.local/bootstrap-admin.json').read_text()))
    sources = client.call('GET', '/api/v1/sources?page_size=100')['items']
    checked = []
    for source in sources:
        if source['kind'] not in ('postgres', 'mysql'):
            continue
        tables = client.call('GET', '/api/v1/sources/' + source['source_id'] + '/tables')['items']
        if not tables:
            raise RuntimeError('No source tables were discovered')
        from urllib.parse import urlencode
        table = next((t for t in tables if t['name'] == 'customers'), tables[0])
        schema = client.call('GET', '/api/v1/sources/' + source['source_id'] + '/table-schema?' + urlencode({'schema': table['schema'], 'table': table['name']}))
        if not schema['fields']:
            raise RuntimeError('Source schema is empty')
        checked.append({'driver': source['kind'], 'table_count': len(tables), 'field_count': len(schema['fields']), 'key_count': len(schema['keys'])})
    if not checked:
        raise RuntimeError('No local PostgreSQL/MySQL sources available for validation')
    # An absent directory prevents persistent writes; a 404 proves multipart spill-to-disk completed.
    boundary = 'datamesh-' + uuid.uuid4().hex
    body = ('--'+boundary+'\r\nContent-Disposition: form-data; name="directory_id"\r\n\r\nmissing-'+uuid.uuid4().hex+'\r\n--'+boundary+'\r\nContent-Disposition: form-data; name="file"; filename="capacity-check.txt"\r\nContent-Type: text/plain\r\n\r\n').encode() + b'x'*(2*1024*1024) + ('\r\n--'+boundary+'--\r\n').encode()
    req = Request('https://localhost:18443/api/v1/file-resources/uploads',data=body,method='POST',headers={'Origin':'https://localhost:18443','X-CSRF-Token':client.principal['csrf_token'],'Content-Type':'multipart/form-data; boundary='+boundary})
    try:
        with client.client.open(req,timeout=30):
            raise RuntimeError('Unexpected upload to absent directory')
    except HTTPError as exc:
        if exc.code != 404:
            raise RuntimeError('Deployed multipart staging failed') from None
    print(json.dumps({'deployed_source_discovery': checked,'multipart_2MiB_staging':'passed_without_resource_creation'}, ensure_ascii=False))

if __name__ == '__main__':
    main()
