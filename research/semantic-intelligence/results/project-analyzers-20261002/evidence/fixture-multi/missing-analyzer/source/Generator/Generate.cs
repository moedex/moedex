using Microsoft.CodeAnalysis;
[Generator] public sealed class Generate : ISourceGenerator {
public void Initialize(GeneratorInitializationContext c) {}
public void Execute(GeneratorExecutionContext c) {
#if DEBUG
c.AddSource("Value.g.cs", "public static class FixtureGenerated { public const int Value = 42; }");
#else
c.AddSource("Value.g.cs", "public static class FixtureGenerated { public const int Value = 84; }");
#endif
} }
