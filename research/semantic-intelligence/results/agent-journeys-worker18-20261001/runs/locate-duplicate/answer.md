Uniqueness is configured at the database mapping, and the duplicate is translated into an HTTP conflict:

1. `RegistrationDbContext.MapRegistration` defines a **unique composite index on `(MemberId, EventId)`** with `HasIndex(...).IsUnique()`. This is the event/member uniqueness rule (`src/Sample.Components/RegistrationDbContext.cs:34–51`, especially 47–51).
2. `RegistrationService.SubmitRegistration` creates and adds the registration, calls `Publish`, then saves. Around `SaveChangesAsync`, it catches `DbUpdateException` only when the inner exception is a PostgreSQL `PostgresException` whose `SqlState` is `PostgresErrorCodes.UniqueViolation`. It throws `DuplicateRegistrationException("Duplicate registration", exception)` (`src/Sample.Components/RegistrationService.cs:21–50`). The filter checks the error category, not a particular constraint name.
3. The POST action `RegistrationController.Submit` awaits that service call and catches `DuplicateRegistrationException`, returning `Conflict(new { model.MemberId, model.EventId })`: **HTTP 409 with the submitted member/event identifiers** (`src/Sample.Api/Controllers/RegistrationController.cs:19–43`, especially 27 and 37–43).

These are source/configuration findings; no live database constraint, duplicate request, HTTP response, or message delivery was observed.

Isolation note: Used only the permitted setup files and logged public-tool responses. No source checkout, implementation, web, or other-run evidence was inspected. No local CLI errors or boundary deviations occurred; the initial catalog display was truncated and its relevant entries were subsequently read from the same permitted setup file.
