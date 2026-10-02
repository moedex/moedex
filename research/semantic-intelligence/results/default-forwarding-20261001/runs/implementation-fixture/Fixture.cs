class Disposable : System.IDisposable { public void /*metadata*/Dispose() {} }
interface IRun { void Run(); }
interface IOther { void Run(); }
interface IOver { void Run(int value); }
interface IGeneric<T> { void Run(); }
interface IMethod { void Run<T>(); }
interface IDefault { void Run() {} }
interface IStatic { static abstract void Run(); }
class Implicit : IRun { public void /*implicit*/Run() {} }
class Explicit : IRun { void IRun./*explicit*/Run() {} }
class Multiple : IRun, IOther { public void /*multiple*/Run() {} }
class Overloaded : IOver { public void /*overload*/Run(int value) {} public void /*unrelated-overload*/Run() {} }
class ShapeOnly { public void /*shape-only*/Run() {} }
abstract class Abstract : IRun { public abstract void /*abstract*/Run(); }
class Generic<T> : IRun { public void /*generic-class*/Run() {} }
class GenericInterface : IGeneric<int> { public void /*generic-interface*/Run() {} }
class GenericMethod : IMethod { public void /*generic-method*/Run<T>() {} }
class Default : IDefault { public void /*default*/Run() {} }
class Static : IStatic { public static void /*static*/Run() {} }
class Derived : Implicit { }
interface IOverflow0 { void Run(); }
interface IOverflow1 { void Run(); }
interface IOverflow2 { void Run(); }
interface IOverflow3 { void Run(); }
interface IOverflow4 { void Run(); }
interface IOverflow5 { void Run(); }
interface IOverflow6 { void Run(); }
interface IOverflow7 { void Run(); }
interface IOverflow8 { void Run(); }
interface IOverflow9 { void Run(); }
interface IOverflow10 { void Run(); }
interface IOverflow11 { void Run(); }
interface IOverflow12 { void Run(); }
interface IOverflow13 { void Run(); }
interface IOverflow14 { void Run(); }
interface IOverflow15 { void Run(); }
interface IOverflow16 { void Run(); }
interface IOverflow17 { void Run(); }
interface IOverflow18 { void Run(); }
interface IOverflow19 { void Run(); }
interface IOverflow20 { void Run(); }
interface IOverflow21 { void Run(); }
interface IOverflow22 { void Run(); }
interface IOverflow23 { void Run(); }
interface IOverflow24 { void Run(); }
interface IOverflow25 { void Run(); }
interface IOverflow26 { void Run(); }
interface IOverflow27 { void Run(); }
interface IOverflow28 { void Run(); }
interface IOverflow29 { void Run(); }
interface IOverflow30 { void Run(); }
interface IOverflow31 { void Run(); }
interface IOverflow32 { void Run(); }
class Overflow : IOverflow0,IOverflow1,IOverflow2,IOverflow3,IOverflow4,IOverflow5,IOverflow6,IOverflow7,IOverflow8,IOverflow9,IOverflow10,IOverflow11,IOverflow12,IOverflow13,IOverflow14,IOverflow15,IOverflow16,IOverflow17,IOverflow18,IOverflow19,IOverflow20,IOverflow21,IOverflow22,IOverflow23,IOverflow24,IOverflow25,IOverflow26,IOverflow27,IOverflow28,IOverflow29,IOverflow30,IOverflow31,IOverflow32 { public void /*overflow*/Run() {} }
