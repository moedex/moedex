Uniqueness is configured at the database model: `RegistrationDbContext.MapRegistration` declares a composite unique index on `(MemberId, EventId)` with `HasIndex(...).IsUnique()`. It is the pair that must be unique, independently of the generated registration ID (`src/Sample.Components/RegistrationDbContext.cs:35–51`).

The duplicate reaches the HTTP response through this chain:

1. `RegistrationController.Submit` calls `IRegistrationService.SubmitRegistration(model.EventId, model.MemberId, model.Payment)` (`src/Sample.Api/Controllers/RegistrationController.cs:19–27`).
2. `RegistrationService.SubmitRegistration` creates and adds the registration, calls `Publish` for `RegistrationSubmitted`, then calls `SaveChangesAsync`. A `DbUpdateException` whose inner exception is a PostgreSQL `UniqueViolation` is translated into `DuplicateRegistrationException("Duplicate registration", exception)` (`src/Sample.Components/RegistrationService.cs:21–49`). The filter checks the error category, not a particular constraint name.
3. The controller catches `DuplicateRegistrationException` and returns `Conflict(new { model.MemberId, model.EventId })`: HTTP 409 with the submitted member/event identifiers (`src/Sample.Api/Controllers/RegistrationController.cs:36–43`).

This is source/configuration evidence of the intended path; no database constraint deployment or live duplicate HTTP request was observed.

Isolation note: Repository evidence came only from the logged public client, with the permitted prompt/catalog used for setup and the assigned solver instructions read. No host source, gold, web, other runs, or agent contacts were used. No isolation deviations or local CLI errors occurred. The first public `list_repos` call returned the tool error “graph tools are closed”; subsequent `search_context` calls supplied the cited source.
