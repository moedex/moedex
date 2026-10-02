using MassTransit;
namespace Shared;
public sealed class Notice { }
public sealed class Producer
{
 public Task Send(IPublishEndpoint endpoint) => endpoint./*gold:distractor*/Publish(new Notice());
}
