using Microsoft.Extensions.DependencyInjection;
namespace Orders;
public class Startup {
    public void Configure(IServiceCollection services) {
        services.AddScoped<IOrderStore, OrderStore>();
    }
}
