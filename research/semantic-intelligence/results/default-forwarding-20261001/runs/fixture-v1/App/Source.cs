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
class Overflow : IDefault0<One.Message>, IDefault1<One.Message>, IDefault2<One.Message>, IDefault3<One.Message>, IDefault4<One.Message>, IDefault5<One.Message>, IDefault6<One.Message>, IDefault7<One.Message>, IDefault8<One.Message>, IDefault9<One.Message>, IDefault10<One.Message>, IDefault11<One.Message>, IDefault12<One.Message>, IDefault13<One.Message>, IDefault14<One.Message>, IDefault15<One.Message>, IDefault16<One.Message>, IDefault17<One.Message>, IDefault18<One.Message>, IDefault19<One.Message>, IDefault20<One.Message>, IDefault21<One.Message>, IDefault22<One.Message>, IDefault23<One.Message>, IDefault24<One.Message>, IDefault25<One.Message>, IDefault26<One.Message>, IDefault27<One.Message>, IDefault28<One.Message>, IDefault29<One.Message>, IDefault30<One.Message>, IDefault31<One.Message>, IDefault32<One.Message> {}
