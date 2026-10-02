The deliberate failure is in `RegistrationStateMachine` while initially handling `RegistrationSubmitted`:

```csharp
.If(context => context.Saga.Payment < 50m && context.GetRetryAttempt() == 0,
    fail => fail.Then(_ => throw new ApplicationException("Totally random, but you didn't pay enough for quality service")))
```

Source: `src/Sample.Components/StateMachines/RegistrationStateMachine.cs:33–34`.

Both conditions must hold: payment is strictly below decimal `50` (exactly `50` does not qualify), and the retry-attempt count is exactly `0` (the initial attempt). A nonzero retry count bypasses this deliberate exception even with the same low payment. Despite the exception's wording, this branch is deterministic, with no random test.

The saga copies payment from the submitted message at line 23. The failure appears after the `SendRegistrationEmail` publish activity and before the `AddEventAttendee` publish activity (`src/Sample.Components/StateMachines/RegistrationStateMachine.cs:16–40`). Its saga definition configures `UseMessageRetry(r => r.Intervals(10, 50, 100, 1000, 1000, 1000, 1000, 1000))` and an Entity Framework outbox (`src/Sample.Components/StateMachines/RegistrationStateDefinition.cs:9–14`).

These conclusions describe source logic and configuration; no runtime execution, message delivery, or successful retry was observed.

Isolation note: This is the same-task retry after a zero-evidence infrastructure failure; the original attempt was preserved unchanged. I used only the permitted setup and logged public-client evidence, with no host-source, gold/rubric, web, other-run inspection, or delegation. The original attempt encountered a sandbox `PermissionError` and then a stopped-journey response. In this successor run, `list_repos` returned the tool error `graph tools are closed`; `search_context` subsequently supplied the cited source. No other deviations or local CLI errors occurred.
