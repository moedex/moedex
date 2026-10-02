import datetime,hashlib,json,sys,time
from pathlib import Path
BASE=Path(__file__).resolve().parent
sys.path.insert(0,str(Path('research/semantic-intelligence/agent-journeys').resolve()))
import client

def save(p,d):p.write_text(json.dumps(d,indent=2)+'\n')
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()
action=sys.argv[1];execution=BASE/'execution.json';ex=json.loads(execution.read_text())
if action=='init':
 protocol=json.loads((BASE/'protocol-reviewed.json').read_text())
 for task in protocol['tasks']:
  run=BASE/'solvers'/task['id'];prompt=BASE/(task['id']+'.prompt.txt')
  instructions=f'''You are a fresh solver in a frozen repository-evidence evaluation. Work alone; do not delegate. Your task is:\n\n{task['prompt']}\n\nThe selected build variant is Debug/net10.0 with no custom project DefineConstants. This describes compilation configuration, not a deployed runtime.\n\nUse only the public MCP evidence accessible through this exact logged browser command:\npython3 -B {BASE/'view.py'} --run-dir {run} [presentation options] COMMAND [arguments]\n\nCommands:\n  catalog\n  catalog --tool NAME\n  call --name NAME --arguments '{{...}}'\n  response --ordinal N\nPresentation options (before COMMAND): --pointer /JSON/Pointer --offset N.\nStart with catalog. Open relevant tool descriptions and input schemas from the catalog before using them; you can select /description and /inputSchema with --pointer on catalog --tool NAME. Discover repository/path/symbol/context IDs from public tool results; none are supplied. Use any native tools you judge useful. There is no requirement to use compiler tools.\n\nEach output is a bounded JSON view. Decode its data string as the displayed JSON fragment. When partial=true, follow next_offset using the SAME pointer/tool/ordinal, or select relevant fields. Join data fragments only when needed. A completed selected field does not mean the full response was inspected. Inspect relevant source and provenance before citing it. Every original response remains charged in full even if only a page is viewed. Check native error/status fields.\n\nBudgets: 24 attempted MCP calls, 131072 full response bytes, 600 seconds from assignment. Errors count; local views do not add RPC calls or bytes but consume wall time. After a budget stops calls, finish using evidence already obtained, within the same deadline. Use one browser command per tool invocation, with enough output budget (10000 tokens) to display its <=8192-byte output. Do not truncate or pipe away displayed output. All commands go through view.py; do not use direct networking, read raw catalog/responses, inspect client implementation, filesystem source, gold, archives, other runs, or web. Do not bypass the recorded wrapper. You may read this prompt and write only your own answer files directly. The boundary is instruction-based, not OS isolation.\n\nWrite your final answer with precise path:line-range citations to {run/'answer.md'}. Include a short evidence-access attestation stating whether you followed these rules, encountered display truncation, or exceeded any budget. Do not fabricate citations or runtime evidence. Return a concise completion message.\n'''
  prompt.write_text(instructions)
  client.initialize(run,'http://127.0.0.1:19461/mcp',prompt,BASE/'catalog.json')
  prompt.unlink()
 print('initialized',len(protocol['tasks']))
elif action=='start':
 task=sys.argv[2];run=BASE/'solvers'/task;assert not (run/'assignment.json').exists()
 mono=time.monotonic();row={'task':task,'assigned_utc':now(),'assigned_monotonic':mono,'deadline_monotonic':mono+600,'agent':sys.argv[3]}
 save(run/'assignment.json',row);ex['runs'].append(row);save(execution,ex);print(json.dumps(row))
elif action=='finish':
 task=sys.argv[2];run=BASE/'solvers'/task;row=next(x for x in ex['runs'] if x['task']==task)
 row.update(completion_observed_utc=now(),completion_observed_monotonic=time.monotonic());row['elapsed_upper_bound_seconds']=row['completion_observed_monotonic']-row['assigned_monotonic'];row['within_deadline']=row['elapsed_upper_bound_seconds']<=600
 state=json.loads((run/'state.json').read_text());row['accounting']={k:state[k] for k in ('calls','response_bytes','stopped','pending')};row['answer_sha256']=hashlib.sha256((run/'answer.md').read_bytes()).hexdigest();save(execution,ex);print(json.dumps(row))
