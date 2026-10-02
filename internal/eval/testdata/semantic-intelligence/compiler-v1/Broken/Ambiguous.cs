namespace BrokenFixture;

public interface ILeft { }
public interface IRight { }

public static class Ambiguous
{
    public static string Choose(ILeft value) => "left";
    public static string Choose(IRight value) => "right";
    public static string Run() => Choose(null);
}
