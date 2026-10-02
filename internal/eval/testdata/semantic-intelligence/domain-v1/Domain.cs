using MassTransit;
using Microsoft.EntityFrameworkCore;
using Microsoft.Extensions.DependencyInjection;
using MessageAlias = DomainGold.Notice;

namespace DomainGold;
public interface IService { }
public sealed class Service : IService { }
public sealed class Notice { public int Id { get; set; } }
public sealed class Envelope<T> { }
public sealed class OpenService<T> { }

public static class Registration
{
    public static void Configure(IServiceCollection services)
    {
        services./*gold:di-singleton*/AddSingleton<IService, Service>();
        services./*gold:di-scoped*/AddScoped<IService, Service>();
        services./*gold:di-self*/AddTransient<Service>();
        services./*gold:di-factory*/AddSingleton<IService>(sp => new Service());
        services./*gold:di-open*/AddTransient(typeof(OpenService<>));
        services./*gold:di-instance*/AddSingleton(new Service());
        new LookalikeServices()./*gold:di-lookalike*/AddScoped<IService, Service>();
    }
}

public sealed class Producer
{
    public async Task Send(IPublishEndpoint endpoint)
    {
        await endpoint./*gold:publish-explicit*/Publish<Notice>(new Notice());
        await endpoint./*gold:publish-inferred*/Publish(new Notice());
        await endpoint./*gold:publish-alias*/Publish<MessageAlias>(new Notice());
        await endpoint./*gold:publish-open-target*/Publish<Envelope<Notice>>(new Envelope<Notice>());
        await new LookalikeEndpoint()./*gold:publish-lookalike*/Publish(new Notice());
        dynamic unknown = endpoint;
        await unknown./*gold:publish-dynamic*/Publish(new Notice());
    }
}

public sealed class Consumer : /*gold:consumes*/IConsumer<Notice>
{
    public Task Consume(ConsumeContext<Notice> context) => Task.CompletedTask;
}
public sealed class GenericConsumer : /*gold:consumes-generic*/IConsumer<Envelope<Notice>>
{
    public Task Consume(ConsumeContext<Envelope<Notice>> context) => Task.CompletedTask;
}

public sealed class Store : DbContext
{
    public /*gold:dbset*/DbSet<Notice> Notices => Set<Notice>();
    public /*gold:dbset-generic*/DbSet<Envelope<Notice>> GenericNotices => Set<Envelope<Notice>>();
    public void Configure(ModelBuilder model, string dynamicName)
    {
        model.Entity<Notice>()./*gold:table-literal*/ToTable("events");
        const string table = "audit_events";
        model.Entity<Notice>()./*gold:table-constant-schema*/ToTable(table, "audit");
        model.Entity<Notice>()./*gold:table-dynamic*/ToTable(dynamicName);
        model.Entity<Notice>()./*gold:table-whitespace*/ToTable("   ");
        model.Entity<Notice>()./*gold:table-nul-schema*/ToTable("events", "bad\0schema");
        model.Entity<Notice>()./*gold:table-long*/ToTable("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx");
        new LookalikeTable<Notice>()./*gold:table-lookalike*/ToTable("fake");
    }
}

public sealed class LookalikeServices { public void AddScoped<TService,TImplementation>() { } }
public sealed class LookalikeEndpoint { public Task Publish<T>(T message) => Task.CompletedTask; }
public sealed class LookalikeTable<T> { public void ToTable(string name) { } }
