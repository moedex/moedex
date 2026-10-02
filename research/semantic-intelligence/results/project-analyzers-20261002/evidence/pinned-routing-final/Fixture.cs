using System;
using System.Threading.Tasks;
using System.Collections.Generic;
using MassTransit;
namespace Fixture {
class Args {}
class A:IExecuteActivity<Args> {public Task<ExecutionResult> Execute(ExecuteContext<Args> c)=>null;}
class B:IExecuteActivity<Args> {public Task<ExecutionResult> Execute(ExecuteContext<Args> c)=>null;}
class FakeBuilder {public void AddActivity(string name,Uri address,object arguments) {}}
class Direct {
 readonly Uri _address;
 
 public Direct(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Direct*/AddActivity(nameof(A),_address,new {}); }
}
class Named {
 readonly Uri _address;
 
 public Named(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Named*/AddActivity(arguments:new {},executeAddress:_address,name:nameof(A)); }
}
class Branch {
 readonly Uri _address;
 
 public Branch(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  if(enabled) builder./*Branch*/AddActivity(nameof(A),_address,new {}); }
}
class This {
 readonly Uri _address;
 
 public This(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*This*/AddActivity(nameof(A),this._address,new {}); }
}
class SecondField {
 readonly Uri _address;
 readonly Uri _other;
 public SecondField(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); _other=new Uri($"exchange:{formatter.ExecuteActivity<B,Args>()}"); }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*SecondField*/AddActivity(nameof(A),_address,new {}); }
}
class Mismatch {
 readonly Uri _address;
 
 public Mismatch(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Mismatch*/AddActivity(nameof(B),_address,new {}); }
}
class Literal {
 readonly Uri _address;
 
 public Literal(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Literal*/AddActivity("A",_address,new {}); }
}
class Mutable {
 Uri _address;
 
 public Mutable(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Mutable*/AddActivity(nameof(A),_address,new {}); }
}
class Public {
 public readonly Uri _address;
 
 public Public(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Public*/AddActivity(nameof(A),_address,new {}); }
}
class Initialized {
 readonly Uri _address=new Uri("exchange:init");
 
 public Initialized(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Initialized*/AddActivity(nameof(A),_address,new {}); }
}
class MultipleConstructors {
 readonly Uri _address;
 public MultipleConstructors():this(null) {}
 public MultipleConstructors(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*MultipleConstructors*/AddActivity(nameof(A),_address,new {}); }
}
class ConditionalAssignment {
 readonly Uri _address;
 
 public ConditionalAssignment(IEndpointNameFormatter formatter) { if(formatter!=null) _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*ConditionalAssignment*/AddActivity(nameof(A),_address,new {}); }
}
class RepeatedAssignment {
 readonly Uri _address;
 
 public RepeatedAssignment(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*RepeatedAssignment*/AddActivity(nameof(A),_address,new {}); }
}
class RefEscape {
 readonly Uri _address;
 static void Mutate(ref Uri address) {}
 public RefEscape(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); Mutate(ref _address); }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*RefEscape*/AddActivity(nameof(A),_address,new {}); }
}
class Prefix {
 readonly Uri _address;
 
 public Prefix(IEndpointNameFormatter formatter) { _address=new Uri($"queue:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Prefix*/AddActivity(nameof(A),_address,new {}); }
}
class Formatted {
 readonly Uri _address;
 
 public Formatted(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>(),10}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Formatted*/AddActivity(nameof(A),_address,new {}); }
}
class Factory {
 readonly Uri _address;
 static Uri BuildAddress(IEndpointNameFormatter formatter)=>new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");
 public Factory(IEndpointNameFormatter formatter) { _address=BuildAddress(formatter);  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Factory*/AddActivity(nameof(A),_address,new {}); }
}
class AddressVariable {
 readonly Uri _address;
 
 public AddressVariable(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) { var local=_address; builder./*AddressVariable*/AddActivity(nameof(A),local,new {}); }
}
class OtherInstance {
 readonly Uri _address;
 
 public OtherInstance(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled, OtherInstance other) {  builder./*OtherInstance*/AddActivity(nameof(A),other._address,new {}); }
}
class NoArguments {
 readonly Uri _address;
 
 public NoArguments(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*NoArguments*/AddActivity(nameof(A),_address); }
}
class Dictionary {
 readonly Uri _address;
 
 public Dictionary(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  builder./*Dictionary*/AddActivity(nameof(A),_address,new Dictionary<string,object>()); }
}
class FakeAPI {
 readonly Uri _address;
 
 public FakeAPI(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}");  }
 void Run(IItineraryBuilder builder,bool enabled) {  new FakeBuilder()./*FakeAPI*/AddActivity(nameof(A),_address,new {}); }
}
class StatementLimit {
 readonly Uri _address;
 public StatementLimit(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); ;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;; }
 void Run(IItineraryBuilder builder) {builder./*StatementLimit*/AddActivity(nameof(A),_address,new {});}
}
class NodeLimit {
 readonly Uri _address;
 public NodeLimit(IEndpointNameFormatter formatter) { _address=new Uri($"exchange:{formatter.ExecuteActivity<A,Args>()}"); var values=new int[] {0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0}; }
 void Run(IItineraryBuilder builder) {builder./*NodeLimit*/AddActivity(nameof(A),_address,new {});}
}
}
