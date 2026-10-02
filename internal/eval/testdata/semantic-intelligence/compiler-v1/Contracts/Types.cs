namespace CompilerFixture.Contracts;

public interface IWorker
{
    string Work(int value);
}

public class Worker : IWorker
{
    public string Work(int value) => value.ToString();
    public string Work(string value) => value;
}

public static class Overloads
{
    public static string Pick(int value) => value.ToString();
    public static string Pick(string value) => value;
    public static T Echo<T>(T value) => value;
}

public class Outer<T>
{
    public class Inner
    {
        public static T Keep(T value) => value;
    }
}

public partial class PartialWidget
{
    public int PartOne() => 1;
}

public static class Box<T>
{
    public static int Marker() => 1;
}

public static class Box<T, U>
{
    public static int Marker() => 2;
}
