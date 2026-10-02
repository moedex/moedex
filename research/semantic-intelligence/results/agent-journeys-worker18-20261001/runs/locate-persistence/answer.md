The flow is in `RegistrationService.SubmitRegistration`, `src/Sample.Components/RegistrationService.cs:21–53`. Its source order is:

1. **Create** a `Registration` with `NewId.NextGuid()`, `DateTime.UtcNow`, and the supplied member, event, and payment values (lines 23–30).
2. **Add the entity** with `await _dbContext.Set<Registration>().AddAsync(registration)` (line 32).
3. **Publish** through `await _publishEndpoint.Publish(new RegistrationSubmitted { ... })`, copying the registration ID, date, member, and event plus the payment argument (lines 34–41).
4. **Request persistence** with `await _dbContext.SaveChangesAsync()` (line 45). A database-update exception wrapping a PostgreSQL unique-violation error becomes `DuplicateRegistrationException` (lines 47–50). The method then returns the registration (line 52).

The HTTP entry point calls this service from `RegistrationController.Submit` at `src/Sample.Api/Controllers/RegistrationController.cs:19–27`.

This is indexed source evidence: creation → entity addition → awaited publication call → awaited save call. It does not establish actual database insertion, message delivery, successful execution, or a runtime transaction/atomicity guarantee. No runtime observations were collected.

Isolation note: Used only the permitted setup files and logged public-tool responses. No source files, implementation, web, other runs, or other agents were consulted. No client/CLI errors occurred. The initial setup-file display was truncated; the relevant catalog input schemas were subsequently read from the permitted catalog file.
