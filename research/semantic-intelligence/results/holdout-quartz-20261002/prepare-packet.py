import hashlib,json,subprocess,shutil
from pathlib import Path
base=Path('.local/holdout-quartz');root=base/'source';out=base/'packet'
out.mkdir(exist_ok=False)
def sha(b):return hashlib.sha256(b).hexdigest()
def save(n,v):(out/n).write_text(json.dumps(v,indent=2)+'\n')
def cite(p,a,b):
 raw=(root/p).read_bytes();lines=raw.decode('utf-8-sig').splitlines();assert 1<=a<=b<=len(lines)
 return dict(path=p,start_line=a,end_line=b,raw_sha256=sha(raw),excerpt='\n'.join(lines[a-1:b]))
H='src/Quartz/Hosting/QuartzHostedService.cs';O='src/Quartz/Hosting/QuartzHostedServiceOptions.cs';R='src/Quartz/Hosting/QuartzServiceCollectionExtensions.cs';F='src/Quartz/Impl/MicrosoftDependencyInjectionJobFactory.cs';B='src/Quartz/CronScheduleBuilder.cs';T='src/Quartz/Impl/Triggers/CronTriggerImpl.cs';E='src/Quartz/CronTriggerMisfireInstruction.cs'
tasks=[]
def task(id,prompt,atoms):
 tasks.append(dict(id=id,prompt=prompt+' Cite precise checked-in source ranges. Describe static behavior and its conditions; do not claim that a scheduler or job actually ran.',atoms=[dict(id=f'{id}.{i}',requirement=c,evidence=[cite(*r) for r in refs]) for i,(c,refs) in enumerate(atoms,1)]))
task('locate-host-options','Where is hosted scheduler registration implemented? Explain shared versus named options, the built-in startup/shutdown option defaults, and how a derived hosted service is retained when registrations overlap.',[
 ('The ordinary AddQuartzHostedService overload delegates to the generic QuartzHostedService registration.',[(R,22,27)]),
 ('Generic registration uses ConfigureAll for a provided delegate and registers the hosted service.',[(R,36,56)]),
 ('Named registration validates a nonblank name and uses PostConfigure(name, configure), so named settings refine shared settings independent of registration order.',[(R,71,90)]),
 ('WaitForJobsToComplete defaults false, StartDelay null, AwaitApplicationStarted true and AutoStart true.',[(O,31,53),(O,78,78)]),
 ('AddHostedService searches implementation-type IHostedService registrations assignable to QuartzHostedService; adds singleton if absent, replaces the existing base type when requested type differs, otherwise keeps existing. Do not claim general deduplication of arbitrary factory/instance registrations or all competing subclasses.',[(R,102,123)])])
task('locate-job-resolution','Where does MicrosoftDependencyInjectionJobFactory resolve a job for a named scheduler? Explain the lookup order and exactly when it may construct a job without a dependency-injection scope.',[
 ('Constructor derives schedulerKey from SchedulerScopedServiceProvider and permits the unscoped optimization only for the exact built-in factory type.',[(F,69,83)]),
 ('ResolveJob validates resolved job type, tries scheduler-keyed registration when a key exists, then unkeyed registration, then activator cache; FromContainer distinguishes these paths.',[(F,238,256)]),
 ('FindUnscopedConstructor rejects derived factories or a configured scope hook and requires an already loaded job type; decisions are cached by type per factory.',[(F,267,289),(F,56,56)]),
 ('Unscoped eligibility requires a concrete non-value nongeneric-open IJob type with exactly one public parameterless constructor.',[(F,309,323)]),
 ('It also requires service-registration introspection and no unkeyed registration; a named scheduler also needs keyed introspection and no matching keyed registration. Otherwise retain the scoped path.',[(F,325,338)])])
task('flow-cron-misfire','Trace a cron misfire policy from CronScheduleBuilder into CronTriggerImpl.UpdateAfterMisfire. Contrast SmartPolicy, DoNothing, and FireAndProceed, including calendar handling. Does that method itself execute a job?',[
 ('Builder defaults to SmartPolicy, stores the typed policy as an integer, and copies it to the built CronTriggerImpl.',[(B,98,104),(B,139,146),(B,283,287)]),
 ('FireAndProceed maps to the FireOnceNow constant; DoNothing and SmartPolicy map to their corresponding constants.',[(E,31,67)]),
 ('UpdateAfterMisfire translates SmartPolicy to FireOnceNow.',[(T,520,527)]),
 ('DoNothing asks for the next fire after current time, advances past calendar-excluded candidates, stops when no next time exists or the give-up year is exceeded, then sets NextFireTimeUtc.',[(T,529,549)]),
 ('FireOnceNow sets NextFireTimeUtc to current time without the calendar-filter loop; this method changes trigger state, contains no job execution call, and does not prove actual firing.',[(T,520,555)])])
task('flow-host-start','Trace QuartzHostedService.StartAsync when some schedulers await application startup and others do not. Include scheduler creation, per-name options, AutoStart, delay, cancellation, and startup failure handling.',[
 ('CreateSchedulers obtains the default factory and default options, then registered named keyed factories with per-name options; no schedulers produces configuration error.',[(H,155,179)]),
 ('StartAsync waits for creation then chooses the deferred path if any AutoStart+AwaitApplicationStarted scheduler exists; awaits already-completed startup tasks, otherwise allows host startup; the other path awaits inline starts.',[(H,97,123)]),
 ('Deferred path first starts non-waiting schedulers, waits for linked startup-cancellation or ApplicationStarted signal, then starts waiting schedulers only if startup was not canceled, using ApplicationStopping token.',[(H,181,197)]),
 ('StartSchedulers returns when application stopping; skips AutoStart=false and mismatched wait mode, and selects StartDelayed or Start. AutoStart=false does not skip creation.',[(H,155,171),(H,203,235)]),
 ('StartAsync catches OperationCanceledException without rethrow; other exceptions observed in its try cause ShutdownSchedulers with CancellationToken.None and rethrow. Do not imply this catch observes every later detached startup-task failure or prove runtime execution.',[(H,108,135)])])
task('impact-host-shutdown','A change is proposed to how hosted schedulers stop. Identify the coordination points for repeated/concurrent StopAsync calls, startup in progress, dynamically added schedulers, waiting for jobs, and multiple shutdown failures. Bound the analysis to this hosted-service path.',[
 ('StopAsync serializes creation of one cached stopTask; subsequent calls join it and do not substitute their token.',[(H,264,276)]),
 ('StopCore returns if its scheduler list is empty; otherwise waits for startupTask or cancellation delay and always calls ShutdownSchedulers in finally.',[(H,281,302)]),
 ('ShutdownSchedulers atomically takes the hosted list and adds runtime live schedulers with options fetched by scheduler name.',[(H,338,347)]),
 ('All shutdown calls are initiated before awaiting their tasks, passing each WaitForJobsToComplete option and common cancellation token; synchronous throws become failed tasks.',[(H,349,363),(O,33,37)]),
 ('Each task is awaited with errors collected, then AggregateException is thrown if any failed. Changes must preserve that attempt-all policy; declarations alone prove no observed shutdown or completion.',[(H,365,382)])])
task('impact-job-cleanup','A change is proposed to job creation and cleanup in MicrosoftDependencyInjectionJobFactory. Review ownership and failure handling for scoped container-resolved jobs, factory-activated jobs, unscoped jobs, and derived factories replacing JobScope.State. What prevents duplicate disposal of its own scope state?',[
 ('Scoped creation opens scope, calls ConfigureScope before ResolveJob, and records disposeJob=!fromContainer in ScopeState; container-resolved jobs are owned by the scope.',[(F,125,135)]),
 ('Creation failure disposes the newly created scope before rethrowing through ExceptionDispatchInfo; normal ReturnJob cannot be relied on for this failed creation path.',[(F,137,149)]),
 ('Unscoped construction returns Unscoped sentinel state; ReturnJob disposes its job directly.',[(F,106,120),(F,178,182)]),
 ('ReturnJob delegates ScopeState disposal to that state, but for replacement state it disposes the replacement and leaves the job alone; derived factories own cascading cleanup or must override ReturnJob.',[(F,160,187)]),
 ('ScopeState uses Interlocked.Exchange to permit disposal once; disposes only factory-activated jobs and disposes the scope in finally. DisposeScope prefers IAsyncDisposable, falling back to synchronous Dispose.',[(F,363,385),(F,190,199)])])
paths=subprocess.check_output(['git','-C',str(root),'ls-files','-z']).decode().split('\0');commit=subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD']).decode().strip()
save('source-manifest.json',dict(repository='https://github.com/quartznet/quartznet',commit=commit,files=[dict(path=p,bytes=(root/p).stat().st_size,sha256=sha((root/p).read_bytes())) for p in paths if p]))
save('source-gold.json',dict(status='coordinator-authored; independent review pending; no product queries or capture',tasks=tasks,scoring={'correctness':'1 explicit correct; 0.5 materially incomplete; 0 absent/wrong','evidence':'1 precise complete support; 0.5 broad/incomplete; 0 absent/contradictory','strict':'all atoms correctness/evidence=1, no material unsupported claim, runtime distinction maintained'},scope='Selected static paths, not exhaustive impact, end-to-end execution, database behavior or framework-completeness proof. Equivalent precise checked-in source evidence accepted.'))
p=json.loads(Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002/protocol.json').read_text());p.update(corpus_commit=commit,repository='https://github.com/quartznet/quartznet',tasks=[dict(id=t['id'],prompt=t['prompt']) for t in tasks],capture_status='Not attempted. Target src/Quartz/Quartz.csproj, Debug/net10.0, SDK10.0.401; includes project-referenced analyzer. Restore/build prerequisite readiness remains separately recorded.',status='prepared; independent oracle review required before capture or queries')
p['presentation'].update(browser='agent-journeys/view.py wrapping browse.py',onboarding='Native initialize/initialized/catalog before assignment with same network permissions. Solver reads every initialization instruction page through view.py first.',view_log='Exact stdout/stderr plus args, hash and timestamps; 8192-byte cap; assignment.json deadline_monotonic on this host; no direct browser/client/raw-file access by solver.')
p['freeze_policy']+=' Product and harness frozen before capture. Any failure retained; changing product makes this development evidence. No silent source-only substitution; source-only would be a separately declared arm.'
save('protocol.json',p)
save('selection.json',dict(repository=p['repository'],commit=commit,selection='First candidate selected for this new packet. Scheduler lifecycle, DI ownership and cron misfires diversify previous application/outbox corpora.',prior_use_check='rg -il quartznet|quartz.net over research/semantic-intelligence and docs/plans/semantic-intelligence returned no matches before clone. This is current-record novelty only, not training or all-session novelty.',source_selection='Public default-branch HEAD shallow clone, then immutable commit pin; no candidate replacement or product queries.'))
for t in tasks:
 (out/(t['id']+'.txt')).write_text(t['prompt']+'\n')
shutil.copy2(__file__,out/'prepare-packet.py')
print(json.dumps(dict(commit=commit,tasks=len(tasks),atoms=sum(len(t['atoms']) for t in tasks),tracked_files=len([p for p in paths if p]))))
