using MassTransit;
using Shared;
namespace Publishers;
public sealed class Producer
{
 public Task Send(IPublishEndpoint endpoint)
 {
#if DEBUG
  return endpoint./*gold:publish-debug*/Publish(new Notice());
#else
  return endpoint./*gold:publish-release*/Publish<Notice>(new Notice());
#endif
 }
}
