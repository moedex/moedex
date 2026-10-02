using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;
using Microsoft.CodeAnalysis.Operations;

static partial class Worker
{
    // A single same-receiver call with one explicit type-parameter cast. This
    // describes the source computation, not cast success or runtime execution.
    static object? DefaultForwarding(INamedTypeSymbol type, IMethodSymbol selected)
    {
        var definition = selected.OriginalDefinition;
        if (definition.IsAsync || definition.ReturnsByRef || definition.ReturnsByRefReadonly ||
            definition.Parameters.Length != 1 || definition.Parameters[0].RefKind != RefKind.None ||
            definition.DeclaringSyntaxReferences.Length != 1 ||
            definition.DeclaringSyntaxReferences[0].GetSyntax() is not MethodDeclarationSyntax syntax) return null;
        var expression = syntax.ExpressionBody?.Expression ?? (syntax.Body?.Statements.Count == 1 ? syntax.Body.Statements[0] switch {
            ReturnStatementSyntax r => r.Expression,
            ExpressionStatementSyntax e when definition.ReturnsVoid => e.Expression,
            _ => null
        } : null);
        if (expression is not InvocationExpressionSyntax call) return null;
        var owners = States.Where(s => s.Complete && s.Compilation is not null &&
            s.Compilation.Assembly.Identity.Equals(definition.ContainingAssembly.Identity) &&
            s.Compilation.SyntaxTrees.Contains(call.SyntaxTree)).Take(2).ToArray();
        if (owners.Length != 1) return null;
        var owner = owners[0];
        var model = owner.Compilation!.GetSemanticModel(call.SyntaxTree);
        if (model.GetDeclaredSymbol(syntax) is not IMethodSymbol template ||
            model.GetOperation(call) is not IInvocationOperation op ||
            op.Instance is not IInstanceReferenceOperation { ReferenceKind: InstanceReferenceKind.ContainingTypeInstance } ||
            op.Arguments.Length != 1 || op.Arguments[0].ArgumentKind != ArgumentKind.Explicit ||
            op.Arguments[0].Value is not IConversionOperation { IsImplicit: false } cast ||
            cast.IsTryCast || cast.Syntax is not CastExpressionSyntax || !cast.Conversion.Exists || cast.Conversion.IsUserDefined || cast.OperatorMethod is not null ||
            cast.Operand is not IParameterReferenceOperation parameter ||
            !SymbolEqualityComparer.Default.Equals(parameter.Parameter, template.Parameters[0]) ||
            cast.Type is not ITypeParameterSymbol { TypeParameterKind: TypeParameterKind.Type } arg ||
            !SymbolEqualityComparer.Default.Equals(arg.ContainingSymbol, template.ContainingType) ||
            arg.Ordinal >= selected.ContainingType.TypeArguments.Length) return null;
        var target = op.TargetMethod;
        if (target.MethodKind != MethodKind.Ordinary || !target.IsAbstract || target.IsStatic || target.IsGenericMethod ||
            target.Parameters.Length != 1 || target.Parameters[0].RefKind != RefKind.None ||
            !SymbolEqualityComparer.Default.Equals(target.Parameters[0].Type, arg) ||
            !SymbolEqualityComparer.Default.Equals(target.ContainingType, template.ContainingType) ||
            !SymbolEqualityComparer.Default.Equals(target.ReturnType, template.ReturnType)) return null;
        var closed = selected.ContainingType.GetMembers(target.Name).OfType<IMethodSymbol>()
            .Where(m => m.OriginalDefinition.GetDocumentationCommentId() == target.OriginalDefinition.GetDocumentationCommentId()).Take(2).ToArray();
        if (closed.Length != 1 || !ClosedInterfaceMethod(closed[0]) ||
            type.FindImplementationForInterfaceMember(closed[0]) is not IMethodSymbol implementation ||
            implementation.IsAbstract || implementation.IsStatic || implementation.IsExtern || implementation.IsGenericMethod ||
            implementation.MethodKind is not (MethodKind.Ordinary or MethodKind.ExplicitInterfaceImplementation) ||
            !SymbolEqualityComparer.Default.Equals(implementation.ContainingType, type) ||
            !implementation.Locations.Any(l => l.IsInSource)) return null;
        var token = call.Expression switch {
            SimpleNameSyntax n => n.Identifier,
            MemberAccessExpressionSyntax { Expression: ThisExpressionSyntax, Name: var n } => n.Identifier,
            _ => default
        };
        var source = owner.Sources.FirstOrDefault(s => s.Tree == call.SyntaxTree);
        if (token.RawKind == 0 || source is null) return null;
        return new {
            Rule = "csharp-default-forward-v1", Receiver = "this", Conversion = "explicit_type_parameter_cast", TypeParameterOrdinal = arg.Ordinal,
            CastType = Symbol(selected.ContainingType.TypeArguments[arg.Ordinal]), InterfaceSymbol = Symbol(closed[0]), ImplementationSymbol = Symbol(implementation),
            CallProject = Relative(owner.Project.FilePath!), CallContext = owner.Context, CallPath = source.Path, CallSha256 = source.Sha, CallSpan = Span(source, token.Span)
        };
    }
}
