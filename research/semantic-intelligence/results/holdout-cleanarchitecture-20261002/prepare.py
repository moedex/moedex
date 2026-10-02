import hashlib,json,subprocess
from pathlib import Path
root=Path('.local/holdout-cleanarchitecture/source')
out=Path('research/semantic-intelligence/results/holdout-cleanarchitecture-20261002')
out.mkdir(parents=True,exist_ok=False)
def sha(b):return hashlib.sha256(b).hexdigest()
def save(name,value): (out/name).write_text(json.dumps(value,indent=2)+'\n')
def cite(path,start,end):
 raw=(root/path).read_bytes();lines=raw.decode('utf-8-sig').splitlines()
 assert 1<=start<=end<=len(lines)
 return {'path':path,'start_line':start,'end_line':end,'raw_sha256':sha(raw),'excerpt':'\n'.join(lines[start-1:end])}
A='src/Application/'; I='src/Infrastructure/'; D='src/Domain/'; W='src/Web/'
app=A+'DependencyInjection.cs';infra=I+'DependencyInjection.cs';validation=A+'Common/Behaviours/ValidationBehaviour.cs';validator=A+'TodoLists/Commands/CreateTodoList/CreateTodoListCommandValidator.cs';config=I+'Data/Configurations/TodoListConfiguration.cs';entity=D+'Entities/TodoItem.cs';dispatch=I+'Data/Interceptors/DispatchDomainEventsInterceptor.cs';handler=A+'TodoItems/EventHandlers/LogTodoItemCompleted.cs';update=A+'TodoItems/Commands/UpdateTodoItem/UpdateTodoItem.cs';endpoint=W+'Endpoints/TodoItems.cs'
tasks=[]
def task(id,prompt,atoms):
 tasks.append({'id':id,'prompt':prompt+' Cite precise checked-in source ranges and separate configuration from observed runtime behavior.',
 'atoms':[{'id':f'{id}.{n}','requirement':claim,'evidence':[cite(*x) for x in citations]} for n,(claim,citations) in enumerate(atoms,1)]})
task('locate-title-validation','Where is validation of a new todo-list title defined, what rules does it enforce, and how are its validators and validation pipeline registered?',[
 ('CreateTodoListCommandValidator validates CreateTodoListCommand and depends on IApplicationDbContext.',[(validator,5,11)]),
 ('Title is nonempty and has maximum length 200.',[(validator,13,15)]),
 ('Async uniqueness rejects an existing equal title, uses cancellation, and supplies Unique error code.',[(validator,16,24)]),
 ('Application services scan the executing assembly for validators and register ValidationBehaviour as an open MediatR behavior.',[(app,14,22)])])
task('locate-db-wiring','How are the application database abstraction and save-change interceptors wired, and which database provider alternatives appear in the checked-in configuration?',[
 ('Two scoped ISaveChangesInterceptor registrations: AuditableEntityInterceptor and DispatchDomainEventsInterceptor.',[(infra,20,21)]),
 ('ApplicationDbContext options resolve and attach all ISaveChangesInterceptor services.',[(infra,23,25)]),
 ('IApplicationDbContext is scoped and resolves the registered ApplicationDbContext through a factory.',[(infra,42,42)]),
 ('Provider alternatives are PostgreSQL when UsePostgreSQL, SQL Server when UseSqlServer, otherwise SQLite; do not infer deployed selection.',[(infra,26,32)]),
 ('Web startup calls application, infrastructure, and web registration methods; source configuration is not runtime execution proof.',[(W+'Program.cs',10,14)])])
task('flow-completion-event','Trace how changing a todo item to done can produce and dispatch a completion notification. Include the trigger condition, event storage, save interception, clearing, and declared notification handler. Does this prove a notification ran after a successful database commit?',[
 ('Done emits only on false-to-true transition and then assigns its backing field.',[(entity,13,25)]),
 ('TodoItemCompletedEvent holds the item, derives from BaseEvent, whose INotification contract makes it a MediatR notification.',[(D+'Events/TodoItemCompletedEvent.cs',3,10),(D+'Common/BaseEvent.cs',5,7)]),
 ('BaseEntity keeps domain events in a nonmapped read-only exposed collection and AddDomainEvent appends.',[(D+'Common/BaseEntity.cs',11,19)]),
 ('Update command handler sets Done and then awaits SaveChangesAsync.',[(update,23,33)]),
 ('SavingChanges and SavingChangesAsync dispatch before base saving callbacks; this is not evidence of post-successful-commit execution.',[(dispatch,17,29)]),
 ('Dispatcher reads tracked BaseEntity events, materializes them, clears entity collections before awaiting Publish for each.',[(dispatch,32,48)]),
 ('LogTodoItemCompleted declares INotificationHandler<TodoItemCompletedEvent> and logs its event type; application registers assembly scanning. This does not prove runtime activation.',[(handler,6,19),(app,16,23)])])
task('flow-validation-error','Trace request validation failures through the application pipeline to the configured HTTP error response. What happens before the next handler is called, and what evidence connects the web exception handler?',[
 ('ValidationBehaviour receives IEnumerable<IValidator<TRequest>> and runs async validators with the cancellation token.',[(validation,8,21)]),
 ('It aggregates errors, throws the application ValidationException when failures exist, and only otherwise invokes next.',[(validation,23,32)]),
 ('ValidationException groups messages by property name into Errors.',[(A+'Common/Exceptions/ValidationException.cs',13,21)]),
 ('ProblemDetailsExceptionHandler maps that exception to HTTP400 ValidationProblemDetails and writes the response.',[(W+'Infrastructure/ProblemDetailsExceptionHandler.cs',17,23),(W+'Infrastructure/ProblemDetailsExceptionHandler.cs',46,50)]),
 ('Web DI registers the exception handler, startup invokes Web services and enables exception middleware; application registers validation behavior.',[(W+'DependencyInjection.cs',19,19),(W+'Program.cs',10,12),(W+'Program.cs',38,38),(app,21,21)])])
task('impact-done-semantics','If the semantics of TodoItem.Done change, which endpoint, request handler, event trigger, and notification consumers should be reviewed along the update/completion path? Explain why static declarations alone cannot establish successful delivery.',[
 ('TodoItems maps the authorized PUT update endpoint, rejects mismatched URL/payload IDs and sends the update command.',[(endpoint,11,18),(endpoint,32,39)]),
 ('UpdateTodoItemCommand has Done and its handler copies it into entity.Done before SaveChangesAsync.',[(update,5,11),(update,23,33)]),
 ('Review false-to-true emission in entity.Done and the event item payload.',[(entity,13,25),(D+'Events/TodoItemCompletedEvent.cs',3,10)]),
 ('Review tracked-event clearing/publication and declared LogTodoItemCompleted notification handler.',[(dispatch,36,48),(handler,6,19)]),
 ('Assembly scan and save interceptor wiring establish configured paths, not actual delivery or a successful commit.',[(app,16,23),(infra,20,25),(dispatch,17,29)])])
task('impact-title-uniqueness','Review a proposed change to todo-list title uniqueness and length. Compare the create-command validator with the TodoList entity configuration: what needs coordination, and do those two files establish database-enforced uniqueness or protection against concurrent duplicate creation?',[
 ('Create validator enforces nonempty title and maximum200; entity configuration also requires title with maximum200, so changes must coordinate.',[(validator,13,18),(config,9,16)]),
 ('Uniqueness currently uses an async AnyAsync equality lookup through IApplicationDbContext, not a database constraint declaration.',[(validator,21,24)]),
 ('Complete TodoListConfiguration defines title constraints and owned Colour, with no unique index configured in that class.',[(config,7,18)]),
 ('These two files do not establish database-enforced uniqueness or concurrent-creation protection; a lookup alone cannot guarantee it. Do not broaden absence to all migrations or a deployed database.',[(validator,21,24),(config,7,18)])])
files=subprocess.check_output(['git','-C',str(root),'ls-files','-z']).decode().split('\0')
manifest=[{'path':p,'bytes':len((root/p).read_bytes()),'sha256':sha((root/p).read_bytes())} for p in files if p]
commit=subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD']).decode().strip()
save('source-manifest.json',{'repository':'https://github.com/jasontaylordev/CleanArchitecture','commit':commit,'files':manifest})
save('source-gold.json',{'status':'coordinator-authored source oracle; independent review pending; frozen before product queries','tasks':tasks,'scoring':{'correctness':'1 explicit correct; 0.5 materially incomplete; 0 absent/wrong','evidence':'1 precise complete source support; 0.5 broad/incomplete; 0 absent/contradictory','strict':'all atom scores 1; no material unsupported claim; runtime distinction maintained'},'scope':'Selected paths, not exhaustive repository impact. Equivalent precise source citations accepted. No product output used.'})
save('protocol.json',{'version':1,'status':'source packet prepared; not launched','corpus_commit':commit,'tasks':[{'id':t['id'],'prompt':t['prompt']} for t in tasks],'budgets':{'attempted_calls':24,'complete_response_bytes':131072,'assignment_wall_seconds':600},'presentation':{'browser':'agent-journeys/browse.py','default_display_bytes':8192,'initial_catalog':'uncharged names index; open full descriptors as needed','local_rereads':'no new RPC charge; full solver wall deadline still applies','required':'Follow next_offset or select fields until cited evidence has been inspected. Preserve transcript of all displayed pages. Never treat a partial view as full response.'},'execution':'Fresh solver per task and arm; no gold/workspace inspection; fresh independent source reviewer; single-threaded coordinator preparing this packet does not serve as blind solver. No agents launched under current no-subagents instruction. Future solver phase requires explicit user delegation authorization.','comparison':'Same source commit, prompts, budgets and scoring for both arms. Native catalogs may differ. Report setup failure separately; no Moedex-vs-CodeGraph claim unless paired arm runs. No aggregation with development corpus.','freeze_policy':'Record independent oracle amendments before any product query. Freeze product, worker, dependencies, capture inputs, browser/client, corpus and rubric before launch. Do not tune on holdout outputs or silently replace failed tasks.','capture_status':'not attempted; source global.json requires SDK10.0.401; existing SDK10.0.100 does not satisfy. Template conditional variants must be explicitly recorded without modifying pinned source.'})
print(json.dumps({'commit':commit,'tasks':len(tasks),'atoms':sum(len(t['atoms']) for t in tasks),'source_files':len(manifest)},indent=2))
