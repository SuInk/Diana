#!/usr/bin/env python3
"""Run synthetic multi-person chats through a running Diana and real DeepSeek.

Uses public authenticated configuration/event APIs and the actual OneBot adapter.
No credentials appear in command lines, reports, or conversation fixtures.
"""
import argparse
import datetime
import http.cookiejar
import json
import os
import pathlib
import re
import sqlite3
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parents[2]


class API:
    def __init__(self, base, credentials):
        parsed = urllib.parse.urlsplit(base)
        if parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', 'localhost', '::1'):
            raise ValueError('Only an explicitly local Diana instance is supported')
        self.base = base.rstrip('/')
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        match = re.fullmatch(r'username: (.+)\npassword: (.+)\n', credentials.read_text())
        if not match or not self.call('/api/auth/login', {'username':match[1], 'password':match[2]}).get('ok'):
            raise RuntimeError('Local administrator login failed')

    def call(self, path, payload=None):
        return http_json(self.opener, self.base + path, payload, self.base)


def http_json(opener, url, payload=None, origin=None):
    body = None if payload is None else json.dumps(payload, ensure_ascii=False).encode()
    request = urllib.request.Request(url, data=body,
        headers={'Content-Type':'application/json', 'Origin':origin or url})
    try:
        with opener.open(request, timeout=180) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(f'Local replay API returned HTTP {error.code} at {urllib.parse.urlsplit(url).path}') from None


def write_json(path, value, secret=''):
    encoded = json.dumps(value, ensure_ascii=False, indent=2)
    if secret:
        encoded = encoded.replace(secret, '[redacted]')
    encoded = re.sub(r'sk-[A-Za-z0-9_-]{12,}', '[redacted]', encoded)
    path.write_text(encoded)


def snapshot_background(database, profile_id=None):
    if not database or not database.exists():
        return {'available':False}
    with sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True) as connection:
        connection.execute('BEGIN')
        filters = {}
        if profile_id:
            prefix = profile_id + ':'
            scoped = ('substr(session,1,?)=?', (len(prefix), prefix))
            filters = {
                'memory_jobs': scoped,
                'memory_items': ('substr(scope_key,1,?)=?', (len(prefix), prefix)),
                'notebook_entries': ('scope_key=? OR substr(scope_key,1,?)=?',
                    ('bot:' + profile_id, len(prefix), prefix)),
                'group_prompt_sessions': scoped,
            }
        def rows(table, projection, group=''):
            where, values = filters.get(table, ('1=1', ()))
            return connection.execute('SELECT ' + projection + ' FROM ' + table + ' WHERE (' + where + ')' + group, values)
        return {'available':True, 'scope':'profile' if profile_id else 'database', 'profile_id':profile_id,
            'jobs':dict(rows('memory_jobs', 'status,COUNT(*)', ' GROUP BY status')),
            'memory_items':rows('memory_items', 'COUNT(*)').fetchone()[0],
            'notebook_entries':rows('notebook_entries', 'COUNT(*)').fetchone()[0],
            'prompt_sessions':rows('group_prompt_sessions', 'COUNT(*)').fetchone()[0]}


def export_usage(database, profile_id):
    if not database or not database.exists():
        return []
    with sqlite3.connect(database.resolve().as_uri()+'?mode=ro',uri=True) as connection:
        rows=connection.execute('SELECT id,metadata,created_at FROM app_logs WHERE action=? AND json_extract(metadata,?)=?',
            ('llm_usage','$.profile_id',profile_id))
        return [{'id':row[0],'metadata':json.loads(row[1]),'created_at':row[2]} for row in rows]


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base',default='http://127.0.0.1:18080')
    parser.add_argument('--credentials',type=pathlib.Path,required=True)
    parser.add_argument('--source-profile',default='deepseek-cache-audit-responses')
    parser.add_argument('--fixtures',type=pathlib.Path,default=ROOT/'docs/testing/context-simplification.zh.json')
    parser.add_argument('--simulator',type=pathlib.Path,required=True)
    parser.add_argument('--out',type=pathlib.Path,required=True)
    parser.add_argument('--database',type=pathlib.Path)
    parser.add_argument('--case',action='append',default=[])
    parser.add_argument('--max-turns',type=int,default=0,help='Optional smoke-test cap; incomplete coverage is reported')
    parser.add_argument('--drain-seconds',type=int,default=90)
    parser.add_argument('--source-revision',help='Source revision of the running binary when testing a separate build')
    parser.add_argument('--agent-mode',choices=('safe','standard'),default='safe')
    args=parser.parse_args()
    os.umask(0o077)
    bundle=json.loads(args.fixtures.read_text(encoding='utf-8'))
    if bundle.get('schema_version')!=1:
        raise ValueError('Unknown fixture schema')
    cases=[case for case in bundle['cases'] if not args.case or case['id'] in args.case]
    if not cases or set(args.case)-{case['id'] for case in bundle['cases']}:
        raise ValueError('Unknown or empty case selection')
    out=args.out.resolve()
    out.mkdir(parents=True,exist_ok=False)
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    control='http://127.0.0.1:19090'
    try:
        http_json(opener,control+'/health')
    except urllib.error.URLError:
        pass
    else:
        raise RuntimeError('Replay port 19090 already occupied; no service was changed')
    api=API(args.base,args.credentials)
    existing_bots=api.call('/api/assistant/config').get('profiles',[])
    for existing in existing_bots:
        endpoint=urllib.parse.urlsplit(existing.get('onebot_ws_endpoint',''))
        if existing.get('enabled') and endpoint.hostname in ('127.0.0.1','localhost','::1') and endpoint.port==19090:
            raise RuntimeError('An enabled profile already owns the replay peer; pause that local replay profile first')
    profiles=api.call('/api/llm/config?include_secrets=true')['profiles']
    source=next(profile for profile in profiles if profile['id']==args.source_profile)
    if urllib.parse.urlsplit(source['base_url']).hostname!='api.deepseek.com' or not source.get('api_key'):
        raise RuntimeError('Source must be an existing direct DeepSeek configuration with a saved key')
    key=source['api_key']
    models=api.call('/api/llm/models',source)['models']
    model=next((m['id'] for m in models if m['id']==source['model']),None)
    if model is None:
        raise RuntimeError('The configured model is no longer listed by DeepSeek')
    started=datetime.datetime.now(datetime.timezone.utc).isoformat()
    revision=args.source_revision or subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
    if not re.fullmatch(r'[0-9a-f]{7,40}', revision):
        raise ValueError('Source revision must be a Git object ID')
    expected_turns=sum(len(step.get('batch',[step])) for case in cases for step in case['turns'])
    manifest={'started_at':started,'source_revision':revision,'model':model,'api_format':source.get('api_format'),
        'cases':[case['id'] for case in cases],'max_turns':args.max_turns,'expected_turns':expected_turns,'agent_mode':args.agent_mode,
        'simulation':'real Diana Runtime and SQLite, real DeepSeek, synthetic OneBot peer; no real QQ delivery'}
    proxy_id='deepseek-local-replay-recorder'
    proxy=dict(source,id=proxy_id,name='DeepSeek / 本地回放采样',group='cache_audit',
        base_url=control+'/deepseek',models=models,max_output_tokens=4096,timeout_ms=90000)
    for field in ('profiles','api_key_preview','role_bindings'):
        proxy.pop(field,None)
    api.call('/api/llm/config',proxy)
    simulator=None
    profile_id=None
    results=[]
    failure=None
    try:
        with (out/'simulator.log').open('wb') as output:
            simulator=subprocess.Popen([str(args.simulator.resolve()),'--out',str(out),'--max-requests','240'],
                stdin=subprocess.DEVNULL,stdout=output,stderr=subprocess.STDOUT)
        for _ in range(40):
            if simulator.poll() is not None:
                raise RuntimeError('Simulator exited; inspect its local log')
            try:
                http_json(opener,control+'/health')
                break
            except urllib.error.URLError:
                time.sleep(0.25)
        else:
            raise RuntimeError('Simulator startup timed out')
        cfg=api.call('/api/assistant/config/defaults?platform=onebot-v11')
        cfg.update(name='本地回放 · DS '+out.name,enabled=True,onebot_transport='forward_ws',
            onebot_ws_endpoint='ws://127.0.0.1:19090/onebot?run='+urllib.parse.quote(out.name),onebot_reverse_ws_endpoint='',
            bot_account='710000099',owner_id=str(bundle['actors']['owner']),group_triggers=['Diana'],
            response_mode='quiet',agent_mode=args.agent_mode,agent_max_steps=8,debug_mode_enabled=True,
            qq_typing_enabled=False,typing_delay_enabled=False,send_chunk_interval_ms=1,
            llm_streaming_enabled=True,notebook_shared_scope_enabled=True,recent_history_token_budget=16000,
            request_timeout_ms=180000,model_request_timeout_ms=90000,
            model_roles={role:{'profile_id':proxy_id,'model':model,'routing_strategy':'failover'}
                for role in ('chat','intent','reply_assist','background')})
        saved=api.call('/api/assistant/config/new',cfg)
        profile_id=saved['id']
        manifest['profile_id']=profile_id
        manifest['background_before']=snapshot_background(args.database,profile_id)
        write_json(out/'manifest.json',manifest,key)
        api.call('/api/assistant/start',{})
        for _ in range(60):
            if http_json(opener,control+'/health')['connected']:
                break
            time.sleep(0.5)
        else:
            raise RuntimeError('Diana did not connect to the local OneBot peer')
        print(json.dumps({'phase':'ready','profile_id':profile_id,'model':model,'cases':manifest['cases']},ensure_ascii=False),flush=True)
        sent=0
        stop=False
        references={}
        for case in cases:
            if stop:
                break
            for step in case['turns']:
                batch=step.get('batch',[step])
                pending=[]
                for turn in batch:
                    if args.max_turns and sent>=args.max_turns:
                        stop=True
                        break
                    payload={'user_id':bundle['actors'][turn['actor']],'group_id':bundle['groups'][turn['group']],
                        'text':turn['text'],'mention':turn.get('reply',False)}
                    if turn.get('reply_to'):
                        reference=references.get(turn['reply_to'])
                        if not reference or reference['group']!=turn['group']:
                            raise ValueError('reply_to must name an earlier fixture in the same group')
                        payload['reply_to']=int(reference['message_id'])
                    receipt=http_json(opener,control+'/emit',payload)
                    if turn.get('id'):
                        if turn['id'] in references:
                            raise ValueError('Duplicate fixture id')
                        references[turn['id']]={'group':turn['group'],'message_id':receipt['message_id']}
                    record=dict(case=case['id'],ordinal=sent,fixture=turn,message_id=str(receipt['message_id']),burst='batch' in step)
                    results.append(record)
                    pending.append(record)
                    sent+=1
                    if 'batch' in step:
                        time.sleep(step.get('gap_ms',350)/1000)
                deadline=time.monotonic()+240
                while pending and time.monotonic()<deadline:
                    events=api.call('/api/assistant/events?'+urllib.parse.urlencode(
                        {'profile':profile_id,'range':'all','mode':'list','limit':100}))['events']
                    by_message={str(event.get('message_id')):event for event in events}
                    for record in pending[:]:
                        event=by_message.get(record['message_id'])
                        if event and event.get('status') in ('done','failed','dead','cancelled','superseded'):
                            record['event']=event
                            pending.remove(record)
                            print(json.dumps({'case':case['id'],'turn':record['ordinal'],'actor':record['fixture']['actor'],
                                'group':record['fixture']['group'],'status':event.get('status'),'outcome':event.get('outcome'),
                                'event_id':event['id']},ensure_ascii=False),flush=True)
                    if pending:
                        time.sleep(1)
                for record in pending:
                    record['timeout']=True
                write_json(out/'events.json',results,key)
                if pending:
                    raise RuntimeError('An inbound event did not reach a terminal state; preserving the incomplete run')
        # Keep the backend alive while its real background workers run.
        drain_deadline=time.monotonic()+args.drain_seconds
        while time.monotonic()<drain_deadline:
            state=snapshot_background(args.database,profile_id)
            if state.get('available') and not sum(state.get('jobs',{}).get(s,0) for s in ('pending','running','processing')):
                break
            time.sleep(3)
        manifest['background_after']=snapshot_background(args.database,profile_id)
        traces={}
        for record in results:
            if record.get('event'):
                event_id=record['event']['id']
                traces[event_id]=api.call('/api/assistant/events/'+urllib.parse.quote(event_id,safe='')+'/trace')
        write_json(out/'traces.json',traces,key)
        write_json(out/'usage.json',export_usage(args.database,profile_id),key)
        write_json(out/'events.json',results,key)
        manifest['turns_sent']=len(results)
        manifest['complete']=len(results)==expected_turns
    except BaseException as error:
        failure=type(error).__name__+': '+str(error)
        manifest['error']=failure
        raise
    finally:
        # Pause only the profile this run created; leave its records in the WebUI.
        if profile_id:
            try:
                api.call('/api/assistant/config/profile-enabled',{'profile_id':profile_id,'enabled':False})
            except Exception as error:
                manifest['pause_error']=type(error).__name__
        if simulator:
            simulator.terminate()
            try:
                simulator.wait(timeout=10)
            except subprocess.TimeoutExpired:
                simulator.kill()
                simulator.wait()
        manifest['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
        manifest['error']=failure
        write_json(out/'events.json',results,key)
        write_json(out/'manifest.json',manifest,key)
        print(json.dumps({'phase':'finished','turns':len(results),'error':failure,'output':str(out)},ensure_ascii=False),flush=True)


if __name__=='__main__':
    main()
