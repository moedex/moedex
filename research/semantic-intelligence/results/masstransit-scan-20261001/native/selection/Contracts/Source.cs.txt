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
public interface IBase0 { void Run0(object value); }
public interface IDefault0<T> : IBase0 { void IBase0.Run0(object value) {} }
public interface IBase1 { void Run1(object value); }
public interface IDefault1<T> : IBase1 { void IBase1.Run1(object value) {} }
public interface IBase2 { void Run2(object value); }
public interface IDefault2<T> : IBase2 { void IBase2.Run2(object value) {} }
public interface IBase3 { void Run3(object value); }
public interface IDefault3<T> : IBase3 { void IBase3.Run3(object value) {} }
public interface IBase4 { void Run4(object value); }
public interface IDefault4<T> : IBase4 { void IBase4.Run4(object value) {} }
public interface IBase5 { void Run5(object value); }
public interface IDefault5<T> : IBase5 { void IBase5.Run5(object value) {} }
public interface IBase6 { void Run6(object value); }
public interface IDefault6<T> : IBase6 { void IBase6.Run6(object value) {} }
public interface IBase7 { void Run7(object value); }
public interface IDefault7<T> : IBase7 { void IBase7.Run7(object value) {} }
public interface IBase8 { void Run8(object value); }
public interface IDefault8<T> : IBase8 { void IBase8.Run8(object value) {} }
public interface IBase9 { void Run9(object value); }
public interface IDefault9<T> : IBase9 { void IBase9.Run9(object value) {} }
public interface IBase10 { void Run10(object value); }
public interface IDefault10<T> : IBase10 { void IBase10.Run10(object value) {} }
public interface IBase11 { void Run11(object value); }
public interface IDefault11<T> : IBase11 { void IBase11.Run11(object value) {} }
public interface IBase12 { void Run12(object value); }
public interface IDefault12<T> : IBase12 { void IBase12.Run12(object value) {} }
public interface IBase13 { void Run13(object value); }
public interface IDefault13<T> : IBase13 { void IBase13.Run13(object value) {} }
public interface IBase14 { void Run14(object value); }
public interface IDefault14<T> : IBase14 { void IBase14.Run14(object value) {} }
public interface IBase15 { void Run15(object value); }
public interface IDefault15<T> : IBase15 { void IBase15.Run15(object value) {} }
public interface IBase16 { void Run16(object value); }
public interface IDefault16<T> : IBase16 { void IBase16.Run16(object value) {} }
public interface IBase17 { void Run17(object value); }
public interface IDefault17<T> : IBase17 { void IBase17.Run17(object value) {} }
public interface IBase18 { void Run18(object value); }
public interface IDefault18<T> : IBase18 { void IBase18.Run18(object value) {} }
public interface IBase19 { void Run19(object value); }
public interface IDefault19<T> : IBase19 { void IBase19.Run19(object value) {} }
public interface IBase20 { void Run20(object value); }
public interface IDefault20<T> : IBase20 { void IBase20.Run20(object value) {} }
public interface IBase21 { void Run21(object value); }
public interface IDefault21<T> : IBase21 { void IBase21.Run21(object value) {} }
public interface IBase22 { void Run22(object value); }
public interface IDefault22<T> : IBase22 { void IBase22.Run22(object value) {} }
public interface IBase23 { void Run23(object value); }
public interface IDefault23<T> : IBase23 { void IBase23.Run23(object value) {} }
public interface IBase24 { void Run24(object value); }
public interface IDefault24<T> : IBase24 { void IBase24.Run24(object value) {} }
public interface IBase25 { void Run25(object value); }
public interface IDefault25<T> : IBase25 { void IBase25.Run25(object value) {} }
public interface IBase26 { void Run26(object value); }
public interface IDefault26<T> : IBase26 { void IBase26.Run26(object value) {} }
public interface IBase27 { void Run27(object value); }
public interface IDefault27<T> : IBase27 { void IBase27.Run27(object value) {} }
public interface IBase28 { void Run28(object value); }
public interface IDefault28<T> : IBase28 { void IBase28.Run28(object value) {} }
public interface IBase29 { void Run29(object value); }
public interface IDefault29<T> : IBase29 { void IBase29.Run29(object value) {} }
public interface IBase30 { void Run30(object value); }
public interface IDefault30<T> : IBase30 { void IBase30.Run30(object value) {} }
public interface IBase31 { void Run31(object value); }
public interface IDefault31<T> : IBase31 { void IBase31.Run31(object value) {} }
public interface IBase32 { void Run32(object value); }
public interface IDefault32<T> : IBase32 { void IBase32.Run32(object value) {} }
