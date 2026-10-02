namespace Contracts;
public interface IBase { void Run(object value); }
public interface IHandler<T> : IBase {
 void Run(T value);
 void IBase.Run(object value) => Run((T)value);
}
public interface IMore<T> : IHandler<T> {
 void IBase.Run(object value) => Run((T)value);
}
public interface ILeft<T> : IHandler<T> {}
public interface IRight<T> : IHandler<T> {}
public interface IReabstract<T> : IHandler<T> { abstract void IBase.Run(object value); }
public interface IPlain : IBase { void IBase.Run(object value) {} }
public interface IBody<T> : IBase { void IBase.Run(object value) { System.Console.WriteLine(value); } }
