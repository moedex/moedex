namespace SDKProbe; public interface IProbe { int Run(); } public sealed class Probe : IProbe { public int Run() => 42; }
