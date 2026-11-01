"""Helpers to extend api/openapi.json by hand-authored specs (PLAN-S2-GO A.5). Usage: import oas; oas.load(); ...; oas.save()."""
import json, os

PATH = os.path.join(os.path.dirname(__file__), '..', 'api', 'openapi.json')
doc = None

def load():
    global doc
    doc = json.load(open(PATH))

def save():
    with open(PATH, 'w') as f:
        f.write(json.dumps(doc, indent=2) + "\n")

def typ(t):
    """Mini language: s i32 i64 b num any obj, ?T nullable, [T] array, $Name ref, enum:a|b, {k:T} not supported."""
    if t.startswith('?'):
        inner = typ(t[1:])
        if '$ref' in inner:
            return {'oneOf': [{'type': 'null'}, inner]}
        if 'enum' in inner:
            return {'oneOf': [{'type': 'null'}, inner]}
        if isinstance(inner.get('type'), str):
            inner = dict(inner); inner['type'] = [inner['type'], 'null']; return inner
        return {'oneOf': [{'type': 'null'}, inner]}
    if t.startswith('['):
        return {'type': 'array', 'items': typ(t[1:-1])}
    if t.startswith('$'):
        return {'$ref': '#/components/schemas/' + t[1:]}
    if t.startswith('enum:'):
        return {'type': 'string', 'enum': t[5:].split('|')}
    return {'s': {'type': 'string'}, 'i32': {'type': 'integer', 'format': 'int32'}, 'i64': {'type': 'integer', 'format': 'int64'}, 'b': {'type': 'boolean'}, 'num': {'type': 'number'},
            'any': {}, 'obj': {'type': 'object', 'additionalProperties': True}}[t]

def schema(name, props, required=None):
    doc['components']['schemas'][name] = {'type': 'object', 'required': list(props.keys()) if required is None else required, 'properties': {k: typ(v) for k, v in props.items()}}

def page(item):
    schema('Page_' + item, {'items': '[$%s]' % item, 'next_cursor': '?s'}, ['items'])

def idschema(name):
    doc['components']['schemas'][name] = {'type': 'string'}

PROBLEM = {'description': 'Problem details (RFC 9457) for every 4xx/5xx', 'content': {'application/problem+json': {'schema': {'$ref': '#/components/schemas/Problem'}}}}

def hdr(name):
    return {'name': name, 'in': 'header', 'required': True, 'schema': {'type': 'string'}}

def op(method, path, op_id, tag, params=(), ifmatch=False, idem=False, body=None, ok=200, resp=None, query=()):
    p = [{'name': 'orgId', 'in': 'path', 'required': True, 'schema': {'$ref': '#/components/schemas/OrgId'}}]
    for n, sch in params:
        p.append({'name': n, 'in': 'path', 'required': True, 'schema': {'$ref': '#/components/schemas/' + sch}})
    for n, t, desc in query:
        e = {'name': n, 'in': 'query', 'required': False, 'schema': typ(t)}
        if desc: e['description'] = desc
        p.append(e)
    if ifmatch: p.append(hdr('If-Match'))
    if idem: p.append(hdr('Idempotency-Key'))
    if method != 'get': p.append(hdr('X-CSRF-Token'))
    o = {'tags': [tag], 'operationId': op_id, 'parameters': p}
    if body:
        o['requestBody'] = {'content': {'application/json': {'schema': {'$ref': '#/components/schemas/' + body}}}, 'required': True}
    r = {'description': ''}
    if ok == 202:
        r['headers'] = {'Location': {'schema': {'type': 'string'}}}
    elif ifmatch or method in ('put', 'post') and resp and not idem:
        r['headers'] = {'ETag': {'schema': {'type': 'string'}}}
    if resp:
        r['content'] = {'application/json': {'schema': typ(resp)}}
    o['responses'] = {str(ok): r, 'default': PROBLEM}
    o['security'] = [{'sessionCookie': []}]
    full = '/api/v1/orgs/{orgId}' + path
    doc['paths'].setdefault(full, {})[method] = o

def add_props(name, props):
    s = doc['components']['schemas'][name]
    for k, v in props.items():
        s['properties'][k] = typ(v)
        if k not in s['required']: s['required'].append(k)
