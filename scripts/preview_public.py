#!/usr/bin/env python3
"""Read-only HTTP preview of public pages; never forwards cookies or authenticated writes."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import ssl
from urllib.request import urlopen
from urllib.error import HTTPError
from urllib.parse import urlsplit

ROOT=Path(__file__).resolve().parents[1]
DIST=ROOT/'web/dist'
CA=ssl.create_default_context(cafile=str(ROOT/'.local/certs/server.crt'))
class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):
        pass
    def do_GET(self):
        path=urlsplit(self.path).path
        if path=='/api/v1/me':
            self.send_response(401);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{"code":"UNAUTHENTICATED","message":"Public preview"}');return
        if path.startswith('/api/public/assets'):
            try:
                with urlopen('https://localhost:18443'+self.path,context=CA,timeout=15) as response:
                    body=response.read(1024*1024);code=response.status
            except HTTPError as exc:
                body=exc.read(1024*1024);code=exc.code
            self.send_response(code);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(body);return
        if path.startswith('/static/'):
            target=(DIST/path.lstrip('/')).resolve()
            if not target.is_relative_to(DIST.resolve()) or not target.is_file():self.send_error(404);return
            mime='text/javascript' if target.suffix=='.js' else 'text/css' if target.suffix=='.css' else 'application/octet-stream'
        elif path in ['/','/login']:
            target=DIST/'index.html';mime='text/html; charset=utf-8'
        else:
            self.send_error(404);return
        self.send_response(200);self.send_header('Content-Type',mime);self.end_headers();self.wfile.write(target.read_bytes())
    def do_POST(self):
        self.send_error(403,'Read-only preview; sign in through the HTTPS application')
if __name__=='__main__':
    print('Read-only public preview: http://localhost:15173',flush=True)
    ThreadingHTTPServer(('127.0.0.1',15173),Handler).serve_forever()
