using Microsoft.Extensions.DependencyInjection;
namespace DomainGold.Incomplete;
public interface IService { }
public sealed class Service : IService { }
public static class Broken
{
    public static void Configure(IServiceCollection services)
    {
        services./*gold:incomplete-resolved-di*/AddScoped<IService, Service>();
        MissingEndpoint./*gold:unresolved-publish*/Publish(new Service());
    }
}
