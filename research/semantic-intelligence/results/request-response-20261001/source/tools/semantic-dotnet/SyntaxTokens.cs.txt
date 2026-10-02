using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp.Syntax;

static partial class Worker
{
    static SyntaxToken TypeToken(TypeSyntax type) => type switch
    {
        SimpleNameSyntax name => name.Identifier,
        QualifiedNameSyntax qualified => TypeToken(qualified.Right),
        AliasQualifiedNameSyntax alias => alias.Name.Identifier,
        _ => type.GetFirstToken()
    };

    static SyntaxToken DeclarationToken(SyntaxNode node) => node switch
    {
        BaseTypeDeclarationSyntax n => n.Identifier,
        DelegateDeclarationSyntax n => n.Identifier,
        MethodDeclarationSyntax n => n.Identifier,
        ConstructorDeclarationSyntax n => n.Identifier,
        DestructorDeclarationSyntax n => n.Identifier,
        PropertyDeclarationSyntax n => n.Identifier,
        EventDeclarationSyntax n => n.Identifier,
        EnumMemberDeclarationSyntax n => n.Identifier,
        VariableDeclaratorSyntax n => n.Identifier,
        ParameterSyntax n => n.Identifier,
        TypeParameterSyntax n => n.Identifier,
        IndexerDeclarationSyntax n => n.ThisKeyword,
        OperatorDeclarationSyntax n => n.OperatorToken,
        ConversionOperatorDeclarationSyntax n => n.ImplicitOrExplicitKeyword,
        LocalFunctionStatementSyntax n => n.Identifier,
        _ => default
    };

}
