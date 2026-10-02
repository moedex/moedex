"""Minimal read-only pprof protobuf summarizer; no third-party dependencies.
Manual protobuf interpretation, not Go's pprof tool. Inline frames counted once
per sample for cumulative totals; flat cost charged to innermost first frame.
"""
import collections, gzip, pathlib, sys

def varint(data, pos):
    value = shift = 0
    while True:
        b = data[pos]; pos += 1
        value |= (b & 127) << shift
        if b < 128: return value, pos
        shift += 7
        if shift > 70: raise ValueError('invalid varint')

def fields(data):
    pos = 0
    result = collections.defaultdict(list)
    while pos < len(data):
        tag, pos = varint(data, pos)
        field, wire = tag >> 3, tag & 7
        if wire == 0: value, pos = varint(data, pos)
        elif wire == 2:
            n, pos = varint(data, pos); value = data[pos:pos+n]; pos += n
        elif wire in (1, 5):
            n = 8 if wire == 1 else 4; value = data[pos:pos+n]; pos += n
        else: raise ValueError(f'unsupported wire type {wire}')
        result[field].append(value)
    return result

def scalar(record, key): return record.get(key, [0])[0]
def repeated(record, key):
    result = []
    for entry in record.get(key, []):
        if isinstance(entry, int): result.append(entry); continue
        pos = 0
        while pos < len(entry):
            value, pos = varint(entry, pos); result.append(value)
    return result

source = pathlib.Path(sys.argv[1])
profile = fields(gzip.decompress(source.read_bytes()))
strings = [s.decode('utf-8', errors='replace') for s in profile[6]]
functions = {}
for raw in profile[5]:
    f = fields(raw); functions[scalar(f, 1)] = strings[scalar(f, 2)]
locations = {}
for raw in profile[4]:
    loc = fields(raw)
    locations[scalar(loc, 1)] = [functions.get(scalar(fields(line), 1), '?') for line in loc.get(4, [])]
types = [(strings[scalar(fields(t), 1)], strings[scalar(fields(t), 2)]) for t in profile[1]]
index = next(i for i, t in enumerate(types) if t == ('cpu', 'nanoseconds'))
flat, cumulative, stacks = collections.Counter(), collections.Counter(), collections.Counter()
total = 0
for raw in profile[2]:
    sample = fields(raw); weight = repeated(sample, 2)[index]; total += weight
    stack = tuple(name for lid in repeated(sample, 1) for name in locations.get(lid, ['?']))
    if stack: flat[stack[0]] += weight
    for name in set(stack): cumulative[name] += weight
    stacks[stack] += weight
print('Manual protobuf interpretation; CPU sample weights, not wall-clock phase timings.')
print(f'Source: {source}\nSample types: {types}\nTotal sampled CPU: {total / 1e9:.3f} s')
for title, counts in [('Flat', flat), ('Cumulative (overlapping)', cumulative)]:
    print('\n' + title)
    for name, value in counts.most_common(40): print(f'{value / 1e9:9.3f}s {100 * value / total:6.2f}% {name}')
print('\nTop complete stacks (innermost first)')
for stack, value in stacks.most_common(20): print(f'{value / 1e9:.3f}s ' + ' <- '.join(stack))
