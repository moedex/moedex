using System.Threading.Tasks;
interface IBase { void Run(object value); }
class Message {}
interface IDirect<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) => Accept((T)value); }
class Direct : IDirect<Message> { public void Accept(Message value) {} }
interface IThis<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) => this.Accept((T)value); }
class This : IThis<Message> { public void Accept(Message value) {} }
interface IBlock<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) { Accept((T)value); } }
class Block : IBlock<Message> { public void Accept(Message value) {} }
interface IConditional<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) { if(value != null) Accept((T)value); } }
class Conditional : IConditional<Message> { public void Accept(Message value) {} }
interface IOther<T> : IBase where T:class { void Accept(T value); IOther<T> Other {get;} void IBase.Run(object value) => Other.Accept((T)value); }
class Other : IOther<Message> { public IOther<Message> Other => this; public void Accept(Message value) {} }
interface INewArgument<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) => Accept((T)new object()); }
class NewArgument : INewArgument<Message> { public void Accept(Message value) {} }
interface ITwoCalls<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) { Accept((T)value); Accept((T)value); } }
class TwoCalls : ITwoCalls<Message> { public void Accept(Message value) {} }
interface INestedCast<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) => Accept((T)(object)value); }
class NestedCast : INestedCast<Message> { public void Accept(Message value) {} }
interface IAsCast<T> : IBase where T:class { void Accept(T value);  void IBase.Run(object value) => Accept(value as T); }
class AsCast : IAsCast<Message> { public void Accept(Message value) {} }

interface IAsyncBase { Task Run(object value); }
interface ITask<T> : IAsyncBase { Task Accept(T value); Task IAsyncBase.Run(object value) { return Accept((T)value); } }
class TaskReturn : ITask<Message> { public Task Accept(Message value) => Task.CompletedTask; }
interface IAwait<T> : IAsyncBase { Task Accept(T value); async Task IAsyncBase.Run(object value) { await Accept((T)value); } }
class AsyncBody : IAwait<Message> { public Task Accept(Message value) => Task.CompletedTask; }
interface IOrdinal<A,B> : IBase { void Accept(B value); void IBase.Run(object value) => Accept((B)value); }
class Ordinal : IOrdinal<string,Message> { public void Accept(Message value) {} }
interface IGenericCall<T> : IBase { void Accept<U>(U value); void IBase.Run(object value) => Accept<T>((T)value); }
class GenericCall : IGenericCall<Message> { public void Accept<U>(U value) {} }
interface IMultiArgument<T> : IBase { void Accept(T value, int n); void IBase.Run(object value) => Accept((T)value, 1); }
class MultiArgument : IMultiArgument<Message> { public void Accept(Message value, int n) {} }
interface INoCast<T> : IBase { void Accept(object value); void IBase.Run(object value) => Accept(value); }
class NoCast : INoCast<Message> { public void Accept(object value) {} }
