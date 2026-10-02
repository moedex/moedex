using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;
var tree=CSharpSyntaxTree.ParseText("""
interface IBase { void Run(object x); }
interface IGeneric<T>:IBase { void Run(T x); void IBase.Run(object x)=>Run((T)x); }
class Msg {}
class Handler:IGeneric<Msg> { public void Run(Msg x) {} }
class Override:IGeneric<Msg> { public void Run(Msg x) {} public void Run(object x) {} }
""");
var refs=((string)AppContext.GetData("TRUSTED_PLATFORM_ASSEMBLIES")!).Split(Path.PathSeparator).Select(x=>MetadataReference.CreateFromFile(x));
var c=CSharpCompilation.Create("test",new[]{tree},refs,new CSharpCompilationOptions(OutputKind.DynamicallyLinkedLibrary));
foreach(var d in c.GetDiagnostics()) Console.WriteLine(d);
var model=c.GetSemanticModel(tree);
foreach(var n in tree.GetRoot().DescendantNodes().OfType<ClassDeclarationSyntax>()) {
var t=(INamedTypeSymbol)model.GetDeclaredSymbol(n)!;
foreach(var i in t.AllInterfaces) foreach(var m in i.GetMembers().OfType<IMethodSymbol>()) {
var s=t.FindImplementationForInterfaceMember(m);
Console.WriteLine($"{t} | {m} => {s} | {s?.ContainingType.TypeKind} | {s?.OriginalDefinition.GetDocumentationCommentId()} | refs={s?.DeclaringSyntaxReferences.Length}");
}}
