The worker configures **`RegistrationValidationService` for `IRegistrationValidationService`, with a scoped lifetime**, using:

```csharp
services.AddScoped<IRegistrationValidationService, RegistrationValidationService>();
```

This appears in the worker's `ConfigureServices` block, alongside registration of `ValidateRegistrationConsumer` with `ValidateRegistrationConsumerDefinition` (`src/Sample.Worker/Program.cs:26–89`).

The call site is **`ValidateRegistrationConsumer.Consume(ConsumeContext<AddEventAttendee> context)`**. The consumer receives `IRegistrationValidationService` through its constructor, stores it in `_validationService`, and awaits `ValidateRegistration(context.Message.EventId, context.Message.MemberId, context.Message.RegistrationId)` (`src/Sample.Components/Consumers/ValidateRegistrationConsumer.cs:9–25`, specifically the call at line 23).

`RegistrationValidationService` declares that it implements this interface. Its `ValidateRegistration` method publishes a `RegistrationValidated` message carrying the supplied `RegistrationId` through its injected `IPublishEndpoint` (`src/Sample.Components/Services/RegistrationValidationService.cs:7–21`). The interface declares the matching three-argument method (`src/Sample.Components/Services/IRegistrationValidationService.cs:1–6`).

These are source/configuration facts: they establish the configured implementation and lifetime and the interface-based call site. They do **not** establish which concrete instance a running container actually selected, the active scope, or that the consumer executed or its published message was delivered. No runtime resolution or execution evidence was obtained.

Evidence limitation: source excerpts came from the logged `search_context` response. `list_repos` and a subsequent `read_source` request both returned `graph tools are closed`, so those requests supplied no additional evidence.

Isolation note: This was an authorized same-task retry after an initial zero-evidence infrastructure failure; the prior attempt was preserved unchanged. I used only the permitted instructions, prompt, catalog, and logged public-client evidence. I did not inspect host source, other runs, gold material, or the web, and did not delegate or consult other agents. No isolation deviations occurred. The prior attempt had a sandbox `PermissionError` followed by a stopped-client response; this retry had no local CLI errors, but the two product-tool errors noted above.
