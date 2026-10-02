using Contracts;
namespace One { public class Message {} }
namespace Two { public class Message {} }
class First : IHandler<One.Message> { public void Run(One.Message value) {} }
class Second : IHandler<Two.Message> { public void Run(Two.Message value) {} }
class MostSpecific : IMore<One.Message> { public void Run(One.Message value) {} }
class Diamond : ILeft<One.Message>, IRight<One.Message> { public void Run(One.Message value) {} }
class Inherited : First {}
class ArbitraryBody : IBody<One.Message> {}
class Override : IHandler<One.Message> {
 public void Run(One.Message value) {} public void Run(object value) {}
}
class ExplicitOverride : IHandler<One.Message> {
 public void Run(One.Message value) {} void IBase.Run(object value) {}
}
class Reabstract : IReabstract<One.Message> {
 public void Run(One.Message value) {} public void Run(object value) {}
}
abstract class Abstract : IHandler<One.Message> { public abstract void Run(One.Message value); }
class Open<T> : IHandler<T> { public void Run(T value) {} }
class Array : IHandler<int[]> { public void Run(int[] value) {} }
class Plain : IPlain {}
struct Value : IHandler<One.Message> { public void Run(One.Message value) {} }
