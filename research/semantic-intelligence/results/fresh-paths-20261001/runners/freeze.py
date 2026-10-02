import hashlib,json,subprocess
from pathlib import Path
root=Path.cwd(); local=root/'.local/fresh-paths'; out=root/'research/semantic-intelligence/results/fresh-paths-20261001'; out.mkdir(exist_ok=False)
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
configs=[('eShopOnWeb','dotnet-architecture/eShopOnWeb','src/Web/Web.csproj',['Web','ApplicationCore','Infrastructure','BlazorAdmin','BlazorShared'],[
('mediator-orders','src/Web/Controllers/OrderController.cs','Send(new GetMyOrders','Send','mediator dispatch of GetMyOrders; handler GetMyOrdersHandler.Handle','domain'),
('mediator-details','src/Web/Controllers/OrderController.cs','Send(new GetOrderDetails','Send','mediator dispatch of GetOrderDetails; handler GetOrderDetailsHandler.Handle','domain'),
('orders-handler','src/Web/Features/MyOrders/GetMyOrdersHandler.cs','Handle(GetMyOrders','Handle','IRequestHandler<GetMyOrders,IEnumerable<OrderViewModel>> implementation','implementation'),
('details-handler','src/Web/Features/OrderDetails/GetOrderDetailsHandler.cs','Handle(GetOrderDetails','Handle','IRequestHandler<GetOrderDetails,OrderDetailViewModel> implementation','implementation'),
('assembly-scan','src/Web/Configuration/ConfigureWebServices.cs','RegisterServicesFromAssembly(typeof(BasketViewModelService).Assembly)','RegisterServicesFromAssembly','MediatR assembly scanning configuration; runtime handler selection unproved','domain'),
('order-registration','src/Web/Configuration/ConfigureCoreServices.cs','AddScoped<IOrderService, OrderService>','AddScoped','scoped IOrderService to OrderService registration','domain'),
('open-repository','src/Web/Configuration/ConfigureCoreServices.cs','AddScoped(typeof(IReadRepository<>), typeof(EfRepository<>))','AddScoped','open generic IReadRepository to EfRepository registration; no concrete runtime dispatch proof','domain'),
('checkout-call','src/Web/Pages/Basket/Checkout.cshtml.cs','CreateOrderAsync(BasketModel.Id','CreateOrderAsync','resolved interface call IOrderService.CreateOrderAsync','binding'),
('order-implementation','src/ApplicationCore/Services/OrderService.cs','CreateOrderAsync(int basketId','CreateOrderAsync','IOrderService.CreateOrderAsync implemented by OrderService','implementation'),
('repository-call','src/Web/Features/MyOrders/GetMyOrdersHandler.cs','ListAsync(specification','ListAsync','resolved inherited generic repository ListAsync call; concrete implementation is not source-proved','binding')]),
('Sample-ForkJoint','MassTransit/Sample-ForkJoint','src/ForkJoint.Api/ForkJoint.Api.csproj',['ForkJoint.Api','ForkJoint.Contracts'],[
('fry-consumer','src/ForkJoint.Api/Components/Consumers/CookFryConsumer.cs','Consume(ConsumeContext<CookFry>','Consume','IConsumer<CookFry> implementation','implementation'),
('shake-consumer','src/ForkJoint.Api/Components/Consumers/PourShakeConsumer.cs','Consume(ConsumeContext<PourShake>','Consume','IConsumer<PourShake> implementation','implementation'),
('fry-response','src/ForkJoint.Api/Components/Consumers/CookFryConsumer.cs','RespondAsync<FryReady>','RespondAsync','MassTransit typed response FryReady','domain'),
('onion-publish','src/ForkJoint.Api/Components/ItineraryPlanners/BurgerItineraryPlanner.cs','Publish<OrderOnionRings>','Publish','conditional MassTransit publish OrderOnionRings; delivery unproved','domain'),
('consumer-scan','src/ForkJoint.Api/Startup.cs','AddConsumersFromNamespaceContaining<CookOnionRingsConsumer>','AddConsumersFromNamespaceContaining','consumer namespace scanning configuration; does not prove runtime registration','domain'),
('activity-scan','src/ForkJoint.Api/Startup.cs','AddActivitiesFromNamespaceContaining<GrillBurgerActivity>','AddActivitiesFromNamespaceContaining','activity namespace scanning configuration','domain'),
('fryer-registration','src/ForkJoint.Api/Startup.cs','TryAddSingleton<IFryer, Fryer>','TryAddSingleton','conditional singleton registration IFryer to Fryer','domain'),
('fryer-implementation','src/ForkJoint.Api/Services/Fryer.cs','CookFry(Size size)','CookFry','IFryer.CookFry implemented by Fryer','implementation'),
('route-post','src/ForkJoint.Api/OrderRoutes.cs','MapPost("/order", SubmitOrder)','MapPost','POST /order configuration to local SubmitOrder method','domain'),
('request-response','src/ForkJoint.Api/OrderRoutes.cs','GetResponse<OrderCompleted, OrderFaulted>','GetResponse','IRequestClient<SubmitOrder> request with two response contracts','domain'),
('routing-slip','src/ForkJoint.Api/Components/ItineraryPlanners/BurgerItineraryPlanner.cs','AddActivity(nameof(GrillBurgerActivity)','AddActivity','routing-slip activity using formatter-computed exchange address; deployment reachability unproved','domain')])]
manifest={'protocol':'Source-authored development baseline, not an independent blind solver score. Freeze before worker9 capture. Retain missing facts as gaps, never rewrite labels to match output. Every anchor must have a resolved compiler binding; requested domain and implementation evidence are measured separately. No claim of runtime delivery, invocation, or deployment compatibility.', 'worker_version':'9','apps':{},'frozen_tools':{}}
for repo,upstream,project,closure,rows in configs:
 source=local/'corpus'/repo; gold={'repo':repo,'upstream':'https://github.com/'+upstream,'commit':subprocess.check_output(['git','-C',str(source),'rev-parse','HEAD'],text=True).strip(),'root_project':project,'framework':'net8.0','configuration':'Debug','reviewed_closure':{'projects':sorted('src/'+x+'/'+x+'.csproj' for x in closure),'source_sha256':{}},'expectations':[]}
 for id,path,anchor,token,claim,layer in rows:
  data=(source/path).read_bytes(); assert data.count(anchor.encode())==1,(id,path); offset=data.index(anchor.encode()); assert data[offset:offset+len(token)]==token.encode(); gold['reviewed_closure']['source_sha256'][path]=sha(source/path)
  gold['expectations'].append(dict(id=id,path=path,raw_sha256=sha(source/path),byte_offset=offset,byte_length=len(token),token=token,claim=claim,layer=layer))
 target=out/(repo+'-source-gold.json');target.write_text(json.dumps(gold,indent=2)+'\n');manifest['apps'][repo]={'commit':gold['commit'],'gold_sha256':sha(target),'expectations':len(rows)}
for p in [root/'.local/default-forwarding/moedex-final',root/'.local/default-forwarding/worker/bin/Debug/net10.0/Moedex.SemanticWorker.dll',*sorted((root/'tools/semantic-dotnet').glob('*.cs'))]: manifest['frozen_tools'][str(p.relative_to(root))]=sha(p)
(out/'pre-capture-freeze.json').write_text(json.dumps(manifest,indent=2)+'\n')
print(json.dumps(manifest['apps'],indent=2))
