using System;
using System.Threading.Tasks;
using Microsoft.Extensions.DependencyInjection;
namespace One { class Message {} }
namespace Two { class Message {} }
interface IBridge { Task Handle(object value); }
interface IHandler<in T> : IBridge {
 Task Handle(T value);
 Task IBridge./*default-bridge*/Handle(object value) => Handle((T)value);
}
class First : IHandler<One.Message> { public Task /*first*/Handle(One.Message value) => Task.CompletedTask; }
class Second : IHandler<Two.Message> { public Task /*second*/Handle(Two.Message value) => Task.CompletedTask; }
class Generic<T> : IHandler<T> { public Task /*open*/Handle(T value) => Task.CompletedTask; }
class ArrayHandler : IHandler<int[]> { public Task /*array*/Handle(int[] value) => Task.CompletedTask; }
class Shape { public Task /*shape*/Handle(One.Message value) => Task.CompletedTask; }
class Fake { public void AddKeyedTransient<T,H>(object key) {} }
static class Helpers {
 public static void Register<T,H>(IServiceCollection services) where H:class,IBridge {
  services.AddKeyedTransient<IBridge,H>(typeof(T));
 }
 public static void Conditional<T,H>(IServiceCollection services,bool enabled) where H:class,IBridge {
  if(enabled) services.AddKeyedTransient<IBridge,H>(typeof(T));
 }
 public static void Factory<T,H>(IServiceCollection services) where H:class,IBridge,new() {
  services.AddKeyedTransient<IBridge,H>(typeof(T),(sp,key)=>new H());
 }
 public static void Nested<T,H>(IServiceCollection services) where H:class,IBridge { Register<T,H>(services); }
 public static void DifferentName<T,H>(IServiceCollection services) where H:class,IBridge {
  services.AddKeyedSingleton<IBridge,H>(typeof(T));
 }
}
class Calls {
 void Run(IServiceCollection services, IHandler<One.Message> one, IHandler<Two.Message> two) {
  services./*direct*/AddKeyedScoped<IBridge,First>(typeof(One.Message));
  Helpers./*helper-one*/Register<One.Message,First>(services);
  Helpers./*helper-two*/Register<Two.Message,Second>(services);
  Helpers./*renamed*/DifferentName<One.Message,First>(services);
  Helpers./*conditional*/Conditional<One.Message,First>(services,true);
  Helpers./*factory*/Factory<One.Message,First>(services);
  Helpers./*nested*/Nested<One.Message,First>(services);
  services./*string-key*/AddKeyedTransient<IBridge,First>("key");
  new Fake()./*lookalike*/AddKeyedTransient<IBridge,First>(typeof(One.Message));
  one./*call-one*/Handle(new One.Message()); two./*call-two*/Handle(new Two.Message());
 }
 void Open<T>(IServiceCollection services) { Helpers./*open-helper*/Register<T,First>(services); }
}
