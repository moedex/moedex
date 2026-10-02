#!/usr/bin/env python3
"""Source-authored public-tool walkthroughs, never independent agent scores.

File basenames are development hints. Paths/content come from public responses.
Assertions check retrieved evidence snippets, not autonomous answer quality.
"""
import argparse
import json
from pathlib import Path
import client

# Each task independently discovers paths and retrieves complete source files.
CASES = {
 'locate-persistence': {'RegistrationService': ['new Registration', 'AddAsync(registration)', 'Publish(new RegistrationSubmitted', 'SaveChangesAsync()']},
 'flow-notification': {'RegistrationController': ['SubmitRegistration('], 'IRegistrationService': ['SubmitRegistration('], 'RegistrationService': ['IRegistrationService', 'Publish(new RegistrationSubmitted'], 'NotifyRegistrationConsumer': ['IConsumer<RegistrationSubmitted>'], 'Program': ['AddScoped<IRegistrationService, RegistrationService>', 'AddConsumer<NotifyRegistrationConsumer>']},
 'flow-email': {'RegistrationStateMachine': ['RegistrationSubmitted', 'Publish(context => new SendRegistrationEmail'], 'SendRegistrationEmailConsumer': ['IConsumer<SendRegistrationEmail>', 'LogInformation', 'Task.CompletedTask'], 'Program': ['AddConsumer<SendRegistrationEmailConsumer>']},
 'flow-validation': {'ValidateRegistrationConsumer': ['IConsumer<AddEventAttendee>', 'ValidateRegistration('], 'IRegistrationValidationService': ['ValidateRegistration('], 'RegistrationValidationService': ['IRegistrationValidationService', 'Publish(new RegistrationValidated'], 'Program': ['AddScoped<IRegistrationValidationService, RegistrationValidationService>']},
 'flow-runtime-proof': {'RegistrationController': ['Ok(', 'SubmitRegistration('], 'RegistrationService': ['Publish(new RegistrationSubmitted', 'SaveChangesAsync()'], 'RegistrationStateMachine': ['SendRegistrationEmail'], 'SendRegistrationEmailConsumer': ['LogInformation', 'Task.CompletedTask'], 'RegistrationStateMachineDefinition': ['UseMessageRetry', 'UseEntityFrameworkOutbox'], 'Program': ['UseBusOutbox', 'ConfigureEndpoints']},
 'impact-submitted': {'RegistrationSubmitted': ['RegistrationId'], 'RegistrationService': ['RegistrationId = registration.RegistrationId'], 'NotifyRegistrationConsumer': ['IConsumer<RegistrationSubmitted>'], 'RegistrationStateMachine': ['CorrelateById', 'Message.RegistrationId']},
 'impact-validation-signature': {'IRegistrationValidationService': ['ValidateRegistration('], 'RegistrationValidationService': ['ValidateRegistration('], 'ValidateRegistrationConsumer': ['ValidateRegistration('], 'Program': ['AddScoped<IRegistrationValidationService, RegistrationValidationService>']},
 'impact-entity-uniqueness': {'RegistrationDbContext': ['MemberId', 'EventId', 'IsUnique', 'RegistrationId'], 'RegistrationService': ['PostgresErrorCodes.UniqueViolation', 'DuplicateRegistrationException'], 'RegistrationController': ['DuplicateRegistrationException', 'Conflict(']},
 'impact-attendee': {'AddEventAttendee': ['RegistrationId'], 'AddEventAttendeeConsumer': ['IConsumer<AddEventAttendee>'], 'ValidateRegistrationConsumer': ['IConsumer<AddEventAttendee>'], 'RegistrationStateMachine': ['Publish(context => new AddEventAttendee'], 'Program': ['AddConsumer<AddEventAttendeeConsumer>', 'AddConsumer<ValidateRegistrationConsumer']},
}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--endpoint', required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--catalog', type=Path, required=True)
    p.add_argument('--tasks', type=Path, required=True)
    args = p.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    prompts = {t['id']: t['prompt'] for t in json.loads(args.tasks.read_text())['tasks']}
    reports = []
    for task, subjects in CASES.items():
        prompt = args.output / (task + '.txt')
        prompt.write_text(prompts[task])
        directory = args.output / task
        client.initialize(directory, args.endpoint, prompt, args.catalog)
        def call(name, arguments):
            raw, accounting = client.request(directory, 'tools/call', {'name': name, 'arguments': arguments})
            assert accounting['response_complete'] and accounting['http_status'] == 200
            wire = json.loads(raw)
            assert 'error' not in wire and not wire['result'].get('isError'), wire
            return wire['result']['structuredContent']
        evidence, missing = [], []
        for subject, snippets in subjects.items():
            response = call('search_context', {'repo': 'Sample-Outbox', 'query': subject, 'top_k': 5, 'token_budget': 2200})
            paths = sorted({b['rel_path'] for b in response['blocks'] if b['rel_path'].endswith('/' + subject + '.cs')})
            texts = []
            for path in paths:
                source = call('read_source', {'repo': 'Sample-Outbox', 'path': path})
                assert not source.get('truncated') and source['start_line'] == 1 and source['end_line'] == source['lines']
                texts.append((path, source['content']))
            for snippet in snippets:
                hits = [{'path': path, 'line': i, 'text': line.strip()} for path, content in texts for i, line in enumerate(content.splitlines(), 1) if snippet in line]
                evidence.append({'subject': subject, 'snippet': snippet, 'citations': hits})
                if not hits:
                    missing.append({'subject': subject, 'snippet': snippet})
        state = json.loads((directory/'state.json').read_text())
        report = {'task': task, 'classification': 'source_authored_public_tool_walkthrough', 'calls': state['calls'], 'response_bytes': state['response_bytes'], 'stopped': state['stopped'], 'evidence': evidence, 'missing_evidence': missing,
                  'limitations': ['Basenames and snippet assertions are source-authored hints.', 'No independent agent quality or completeness score.', 'Source configuration does not prove runtime activation, dispatch, delivery or transactions.']}
        client.atomic_json(directory/'report.json', report)
        reports.append(report)
        print(task, report['calls'], report['response_bytes'], 'missing', len(missing), flush=True)
    client.atomic_json(args.output/'report.json', reports)

if __name__ == '__main__':
    main()
