`IRegistrationValidationService` is configured to use `RegistrationValidationService` with a **scoped lifetime**, via `services.AddScoped<IRegistrationValidationService, RegistrationValidationService>()` in `src/Sample.Worker/Program.cs:61` (within the host's `ConfigureServices` block).

The caller is `ValidateRegistrationConsumer.Consume`, which handles `AddEventAttendee`. Its constructor receives an `IRegistrationValidationService` and stores it in `_validationService` (`src/Sample.Components/Consumers/ValidateRegistrationConsumer.cs:9–19`). `Consume` awaits `_validationService.ValidateRegistration(context.Message.EventId, context.Message.MemberId, context.Message.RegistrationId)` at lines 21–24. The worker also registers this consumer with `ValidateRegistrationConsumerDefinition` (`src/Sample.Worker/Program.cs:76`).

The implementation declares `IRegistrationValidationService` and receives an `IPublishEndpoint`; its `ValidateRegistration` method publishes `RegistrationValidated` containing the supplied registration ID (`src/Sample.Components/Services/RegistrationValidationService.cs:7–20`).

These are source/configuration findings. They establish the configured mapping, lifetime, and interface-based call site; they do not establish which concrete object was selected in any running process, whether the consumer executed, or whether the published message was delivered. No runtime resolution or execution evidence was inspected.

Isolation note: Used only the permitted setup files and logged public responses (two public calls). No source checkout, implementation internals, web, or other runs were inspected. No CLI errors or isolation deviations occurred; the initial setup-catalog display was truncated and was reread in compact form.
