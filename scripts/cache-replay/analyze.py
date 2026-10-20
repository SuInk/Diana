#!/usr/bin/env python3
"""Join raw provider usage to Runtime traces and report reproducible cohorts."""
import argparse
import collections
import csv
import datetime
import hashlib
import json
import pathlib


def usage_values(usage):
    usage=usage or {}
    input_tokens=usage.get('input_tokens',usage.get('prompt_tokens',0))
    output_tokens=usage.get('output_tokens',usage.get('completion_tokens',0))
    details=usage.get('input_tokens_details',usage.get('prompt_tokens_details',{})) or {}
    cached=usage.get('cached_input_tokens',usage.get('prompt_cache_hit_tokens',details.get('cached_tokens',0)))
    return int(input_tokens or 0),int(output_tokens or 0),int(cached or 0)


def digest(value):
    return hashlib.sha256(json.dumps(value,ensure_ascii=False,sort_keys=True).encode()).hexdigest()


def aggregate(rows):
    success=[r for r in rows if r['status']==200 and r.get('response_complete')]
    known=[r for r in success if r.get('cache_field_present')]
    inputs=sum(r['input_tokens'] for r in known)
    cached=sum(r['cached_input_tokens'] for r in known)
    return {'http_attempts':len(rows),'successful_responses':len(success),'cache_known_calls':len(known),
        'cache_unknown_calls':len(success)-len(known),'known_input_tokens':inputs,'cached_input_tokens':cached,
        'weighted_hit_percent':round(100*cached/inputs,3) if inputs else None,
        'failures_or_interruptions':len(rows)-len(success)}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory',type=pathlib.Path)
    args=parser.parse_args()
    root=args.directory
    manifest=json.loads((root/'manifest.json').read_text())
    events=json.loads((root/'events.json').read_text())
    traces=json.loads((root/'traces.json').read_text())
    wire=[json.loads(line) for line in (root/'wire.jsonl').read_text().splitlines()]
    by_event={r['event']['id']:r for r in events if r.get('event')}
    calls=[]
    tools=collections.Counter()
    coverage=[]
    memory_nonempty=[]
    for event_id,trace in traces.items():
        record=by_event[event_id]
        event_tools=set()
        for step in trace['steps']:
            meta=step.get('metadata',{})
            if meta.get('phase')=='agent_tool_completed':
                tool=meta.get('tool','')
                tools[tool]+=1
                event_tools.add(tool)
                if tool=='memory':
                    try:
                        output=json.loads(meta.get('tool_output','{}'))
                    except (ValueError,TypeError):
                        output={}
                    memory_nonempty.append({'event_id':event_id,'operation':meta.get('tool_input',{}).get('operation'),
                        'returned_nonempty':bool(output.get('items') or output.get('item') or output.get('memory')),'output':output})
            if meta.get('phase')=='model_request':
                usage=meta.get('response',{}).get('usage') or meta.get('usage') or {}
                calls.append({'event_id':event_id,'ordinal':record['ordinal'],'case':record['case'],
                    'actor':record['fixture']['actor'],'group':record['fixture']['group'],'burst':record['burst'],
                    'purpose':meta.get('purpose','unknown'),'trace_sequence':meta.get('sequence',0),
                    'signature':usage_values(usage),'trace_at':step.get('created_at'),'matched':False,
                    'adapted_usage':usage})
        expected=set(record['fixture'].get('expect_tools',[]))
        if expected:
            coverage.append({'event_id':event_id,'ordinal':record['ordinal'],'expected':sorted(expected),
                'observed':sorted(event_tools),'missing':sorted(expected-event_tools)})
    first={}
    for c in calls:
        if c['purpose']=='reply':
            first[c['event_id']]=min(first.get(c['event_id'],c['trace_sequence']),c['trace_sequence'])
    ambiguity=0
    logged_usage=json.loads((root/'usage.json').read_text()) if (root/'usage.json').exists() else []
    by_message={r['message_id']:r for r in events}
    consumed_logs=set()
    for row in wire:
        signature=usage_values(row.get('usage'))
        row.update(input_tokens=signature[0],output_tokens=signature[1],cached_input_tokens=signature[2])
        candidates=[c for c in calls if not c['matched'] and c['signature']==signature and signature[0]>0]
        if len(candidates)>1:
            ambiguity+=1
        if candidates:
            end=datetime.datetime.fromisoformat(row['started_at'])+datetime.timedelta(milliseconds=row['duration_ms'])
            candidates.sort(key=lambda c:abs((datetime.datetime.fromisoformat(c['trace_at'])-end).total_seconds()))
            chosen=candidates[0]
            chosen['matched']=True
            row.update({k:chosen[k] for k in ('event_id','ordinal','case','actor','group','burst','purpose','trace_sequence')})
            row['first_reply_step']=chosen['purpose']=='reply' and chosen['trace_sequence']==first.get(chosen['event_id'])
        else:
            row['purpose']='unmatched'
            row['first_reply_step']=False
        log_candidates=[entry for entry in logged_usage if entry['id'] not in consumed_logs and usage_values(entry['metadata'])==signature and signature[0]>0]
        if log_candidates:
            end=datetime.datetime.fromisoformat(row['started_at'])+datetime.timedelta(milliseconds=row['duration_ms'])
            log_candidates.sort(key=lambda entry:abs((datetime.datetime.fromisoformat(entry['created_at'])-end).total_seconds()))
            log_entry=log_candidates[0]
            consumed_logs.add(log_entry['id'])
            row['adapted_usage_matches_raw']=usage_values(log_entry['metadata'])==signature
            if row['purpose']=='unmatched':
                meta=log_entry['metadata']
                row['purpose']=meta.get('purpose','unknown')
                event_record=by_message.get(str(meta.get('message_id','')))
                if event_record:
                    row.update(event_id=event_record['event']['id'],ordinal=event_record['ordinal'],case=event_record['case'],
                        actor=event_record['fixture']['actor'],group=event_record['fixture']['group'],burst=event_record['burst'])
        request=row.get('request') or {}
        row['instructions_sha256']=digest(request.get('instructions',''))
        row['tools_sha256']=digest(request.get('tools',[]))
        row['tool_continuation']=any(item.get('type')=='function_call_output' for item in request.get('input',[]) if isinstance(item,dict))
    by_purpose=collections.defaultdict(list)
    for row in wire:
        by_purpose[row['purpose']].append(row)
    initial=[]
    warm=[]
    owners=[]
    bursts=[]
    seen=set()
    first_rows=sorted([r for r in wire if r['first_reply_step']],key=lambda r:r['ordinal'])
    for row in first_rows:
        if row['burst']:
            bursts.append(row)
        elif row['group'] not in seen:
            initial.append(row)
            seen.add(row['group'])
        elif row['actor']=='owner':
            owners.append(row)
        else:
            warm.append(row)
    serial_history=[]
    previous={}
    for row in first_rows:
        prev=previous.get(row['group'])
        if prev:
            a=prev['request'].get('instructions','')
            b=row['request'].get('instructions','')
            common=0
            for x,y in zip(a,b):
                if x!=y:
                    break
                common+=1
            serial_history.append({'previous_ordinal':prev['ordinal'],'ordinal':row['ordinal'],
                'previous_actor':prev['actor'],'actor':row['actor'],'group':row['group'],
                'instructions_equal':a==b,'instructions_common_chars':common,'instructions_chars':len(b),
                'tools_equal':prev['tools_sha256']==row['tools_sha256'],
                'first_changed_instruction_previous':a[common:common+100],
                'first_changed_instruction_current':b[common:common+100]})
        previous[row['group']]=row
    outcomes=collections.Counter(r.get('event',{}).get('outcome','incomplete') for r in events)
    summary={'source_revision':manifest['source_revision'],'profile_id':manifest['profile_id'],'model':manifest['model'],
        'complete':manifest.get('complete',False),'inbound_events':len(events),'outcomes':dict(outcomes),
        'http_statuses':dict(collections.Counter(str(r['status']) for r in wire)),
        'all_calls':aggregate(wire),'by_purpose':{k:aggregate(v) for k,v in sorted(by_purpose.items())},
        'cohorts':{name:aggregate(rows) for name,rows in [('first_reply_per_group',initial),('warm_serial_member_first_steps',warm),
            ('owner_first_steps',owners),('burst_first_steps',bursts),('reply_tool_continuations',[r for r in wire if r['purpose']=='reply' and r['tool_continuation']])]},
        'tools_completed':dict(tools),'expected_tool_coverage':coverage,'memory_reads':memory_nonempty,
        'raw_to_trace_join':{'matched':sum(r['purpose']!='unmatched' for r in wire),'unmatched':sum(r['purpose']=='unmatched' for r in wire),
            'raw_adapted_usage_equal':sum(r.get('adapted_usage_matches_raw',False) for r in wire),
            'nonunique_usage_signatures_resolved_by_timestamp':ambiguity},
        'background_before':manifest.get('background_before'),'background_after':manifest.get('background_after'),
        'warm_first_step_samples':[{'sequence':r['sequence'],'ordinal':r['ordinal'],'actor':r['actor'],'group':r['group'],
            'event_id':r['event_id'],'input':r['input_tokens'],'cached':r['cached_input_tokens']} for r in warm],
        'instruction_changes':serial_history}
    scoped_path=root/'profile-state.json'
    if scoped_path.exists():
        summary['profile_state_verified_after_run']=json.loads(scoped_path.read_text())
    (root/'analysis.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2))
    fields=['sequence','status','response_complete','purpose','case','ordinal','actor','group','burst','first_reply_step',
        'tool_continuation','input_tokens','output_tokens','cached_input_tokens','cache_field_present','duration_ms','event_id','instructions_sha256','tools_sha256']
    with (root/'requests.csv').open('w',newline='') as output:
        writer=csv.DictWriter(output,fieldnames=fields,extrasaction='ignore')
        writer.writeheader()
        writer.writerows(sorted(wire,key=lambda r:r['sequence']))
    (root/'joined.json').write_text(json.dumps(wire,ensure_ascii=False))
    answers=[{'ordinal':r['ordinal'],'event_id':r.get('event',{}).get('id'),'case':r['case'],
        'actor':r['fixture']['actor'],'group':r['fixture']['group'],'question':r['fixture']['text'],
        'status':r.get('event',{}).get('status'),'outcome':r.get('event',{}).get('outcome'),
        'answer':r.get('event',{}).get('reply','')} for r in events if r['fixture'].get('reply')]
    (root/'answers.json').write_text(json.dumps(answers,ensure_ascii=False,indent=2))
    print(json.dumps({k:summary[k] for k in ('inbound_events','outcomes','all_calls','cohorts','tools_completed','raw_to_trace_join')},ensure_ascii=False))


if __name__=='__main__':
    main()
