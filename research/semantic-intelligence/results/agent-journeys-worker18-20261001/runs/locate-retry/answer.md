The deliberate failure is in `src/Sample.Components/StateMachines/RegistrationStateMachine.cs:33–34`, inside the initial `RegistrationSubmitted` handler (`:16–17`):

```csharp
.If(context => context.Saga.Payment < 50m && context.GetRetryAttempt() == 0,
    fail => fail.Then(_ => throw new ApplicationException("Totally random, but you didn't pay enough for quality service")))
```

Both conditions must hold: payment is strictly below decimal `50` (exactly `50` does not qualify), and the retry attempt is exactly `0`. A nonzero retry attempt bypasses this particular throw. Despite the exception message, the condition is deterministic. The saga's payment is copied from the submitted message (`:23`). The failure appears after the `SendRegistrationEmail` publish activity and before the `AddEventAttendee` publish activity (`:26–40`).

The saga definition configures message retry with `Intervals(10, 50, 100, 1000, 1000, 1000, 1000, 1000)` and the Entity Framework outbox: `src/Sample.Components/StateMachines/RegistrationStateDefinition.cs:12–14`.

These are indexed source/configuration findings, not observed execution; they do not prove successful retries or delivery of either message.

Isolation note: Used only the permitted setup files and logged public tool responses. No evidence-access deviations or local CLI errors. The initial setup catalog display was truncated; its tool descriptions and input schemas were then read from the same permitted catalog. All three client calls completed successfully.
