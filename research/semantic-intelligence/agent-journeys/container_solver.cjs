#!/usr/bin/env node
'use strict';

// The container has no network interface other than loopback and no host files.
// Only this JSONL bridge can ask the host to perform provider or MCP exchanges.
const http = require('node:http');
const readline = require('node:readline');
const { spawn } = require('node:child_process');
const fs = require('node:fs');

const MAX_MESSAGE_BYTES = 96 * 1024 * 1024;
const MAX_BODY_BYTES = 64 * 1024 * 1024;
const ALLOWED_RESPONSE_HEADERS = new Set(['content-type', 'mcp-session-id', 'mcp-protocol-version']);
let sequence = 0;
let started = false;
let ending = false;
let child;
let servers = [];
let deadline;
let requestTimeout = 60000;
const pending = new Map();

function emit(value) { process.stdout.write(JSON.stringify(value) + '\n'); }
function fail(type) { emit({ channel: 'error', error_type: type }); }
function stop(code, errorType) {
  if (ending) return;
  ending = true;
  if (errorType) fail(errorType);
  clearTimeout(deadline);
  for (const [id, waiting] of pending) {
    clearTimeout(waiting.timer);
    if (!waiting.response.writableEnded) waiting.response.destroy();
    pending.delete(id);
  }
  for (const server of servers) server.close();
  if (child && child.exitCode === null && child.signalCode === null) {
    child.kill('SIGTERM');
    setTimeout(() => {
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    }, 1000).unref();
  }
  process.exitCode = code;
  input.close();
  process.stdin.destroy();
}

function relay(channel) {
  return http.createServer(async (request, response) => {
    if (ending) { response.writeHead(503).end(); return; }
    if ((channel === 'provider' && (request.method !== 'POST' || request.url !== '/v1/responses')) ||
        (channel === 'mcp' && (!['POST', 'GET', 'DELETE'].includes(request.method) || request.url !== '/mcp'))) {
      response.writeHead(405).end(); return;
    }
    const chunks = [];
    let size = 0;
    try {
      for await (const chunk of request) {
        size += chunk.length;
        if (size > MAX_BODY_BYTES) { response.writeHead(413).end(); return; }
        chunks.push(chunk);
      }
    } catch { fail('local_request_read_error'); response.destroy(); return; }
    const id = String(++sequence);
    const headers = {};
    if (channel === 'mcp') {
      for (const name of ['mcp-session-id', 'mcp-protocol-version']) {
        if (typeof request.headers[name] === 'string') headers[name] = request.headers[name];
      }
    }
    const timer = setTimeout(() => {
      pending.delete(id);
      if (!response.writableEnded) response.writeHead(504).end();
      fail('relay_response_timeout');
    }, requestTimeout);
    pending.set(id, { response, timer, channel });
    emit({ channel, id, method: request.method, path: request.url, headers, body_base64: Buffer.concat(chunks).toString('base64') });
  });
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve(server.address().port));
  });
}
function toml(value) { return JSON.stringify(value); }
function configArgs(config, providerPort, mcpPort) {
  const pairs = {
    model_provider: 'recording_bridge',
    model_reasoning_effort: config.reasoning_effort,
    model_reasoning_summary: config.reasoning_summary || 'none',
    approval_policy: 'never',
    web_search: 'disabled',
    'analytics.enabled': false,
    'feedback.enabled': false,
    'features.skip_host_skill_discovery': true,
    'model_providers.recording_bridge.name': 'Recorded isolated provider bridge',
    'model_providers.recording_bridge.base_url': `http://127.0.0.1:${providerPort}/v1`,
    'model_providers.recording_bridge.env_key': 'SOLVER_BRIDGE_NONSECRET_TOKEN',
    'model_providers.recording_bridge.wire_api': 'responses',
    'model_providers.recording_bridge.requires_openai_auth': false,
    'mcp_servers.retrieval.url': `http://127.0.0.1:${mcpPort}/mcp`,
    'mcp_servers.retrieval.required': true,
    'mcp_servers.retrieval.enabled_tools': config.enabled_tools,
    'mcp_servers.retrieval.startup_timeout_sec': Math.max(1, Math.ceil(requestTimeout / 1000)),
    'mcp_servers.retrieval.tool_timeout_sec': Math.max(1, Math.ceil(requestTimeout / 1000)),
    tool_output_token_limit: config.tool_output_token_limit || 65536,
  };
  const args = [];
  for (const [key, value] of Object.entries(pairs)) args.push('-c', `${key}=${toml(value)}`);
  // Code Mode host supplies this model's tool wrapper. Filesystem and network
  // isolation are enforced by Docker, including for any remaining built-ins.
  for (const feature of ['apps', 'browser_use', 'browser_use_external', 'browser_use_full_cdp_access',
    'computer_use', 'image_generation', 'view_image', 'hooks', 'multi_agent', 'multi_agent_v2',
    'shell_tool', 'unified_exec', 'shell_snapshot', 'memories', 'skill_search',
    'skill_mcp_dependency_install', 'tool_suggest', 'workspace_dependencies',
    'in_app_browser', 'realtime_conversation', 'standalone_web_search', 'goals', 'sleep_tool']) {
    args.push('--disable', feature);
  }
  args.push('--enable', 'code_mode_host');
  return args;
}

async function start(config) {
  if (started) throw new Error('duplicate_start');
  started = true;
  if (!config || typeof config.model !== 'string' || !config.model ||
      typeof config.reasoning_effort !== 'string' || typeof config.prompt !== 'string' ||
      !Array.isArray(config.enabled_tools) || config.enabled_tools.some(x => typeof x !== 'string') ||
      !Number.isInteger(config.timeout_ms) || config.timeout_ms < 1 || config.timeout_ms > 3600000) {
    throw new Error('invalid_start_config');
  }
  requestTimeout = Math.min(config.timeout_ms, Number.isInteger(config.exchange_timeout_ms) && config.exchange_timeout_ms > 0 ? config.exchange_timeout_ms : 60000);
  const provider = relay('provider');
  const mcp = relay('mcp');
  servers = [provider, mcp];
  const providerPort = await listen(provider);
  const mcpPort = await listen(mcp);
  const workdir = '/tmp/solver-workspace';
  fs.mkdirSync(workdir, { recursive: true });
  const args = ['exec', '--ignore-user-config', '--ignore-rules', '--ephemeral',
    '--skip-git-repo-check', '--json', '--color', 'never', '--sandbox', 'read-only',
    '--model', config.model, '--cd', workdir, '--output-last-message', '/tmp/solver-answer.json',
    ...configArgs(config, providerPort, mcpPort), '-'];
  child = spawn('codex', args, {
    cwd: workdir,
    env: { PATH: process.env.PATH, LANG: 'C.UTF-8', SOLVER_BRIDGE_NONSECRET_TOKEN: 'nonsecret-container-loopback-only' },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const output = readline.createInterface({ input: child.stdout });
  output.on('line', line => {
    if (line.length > MAX_MESSAGE_BYTES) { stop(1, 'solver_event_too_large'); return; }
    try { emit({ channel: 'event', event: JSON.parse(line) }); }
    catch { stop(1, 'invalid_solver_event_json'); }
  });
  // Preserve exact diagnostic bytes, including when a chunk splits UTF-8.
  child.stderr.on('data', bytes => emit({ channel: 'diagnostic', body_base64: bytes.toString('base64') }));
  child.on('error', () => stop(1, 'solver_spawn_error'));
  child.on('close', (code, signal) => {
    try {
      const answerPath = '/tmp/solver-answer.json';
      if (fs.existsSync(answerPath)) {
        if (fs.statSync(answerPath).size > MAX_BODY_BYTES) throw new Error('answer_too_large');
        emit({ channel: 'answer', body_base64: fs.readFileSync(answerPath).toString('base64') });
      }
    } catch { fail('solver_answer_read_error'); code = 1; }
    emit({ channel: 'exit', code, signal });
    stop(code === 0 ? 0 : 1);
    input.close();
    process.stdin.destroy();
  });
  deadline = setTimeout(() => stop(1, 'solver_assignment_timeout'), config.timeout_ms);
  child.stdin.end(config.prompt + '\n');
}

function respond(message) {
  if (typeof message.id !== 'string' || !pending.has(message.id)) throw new Error('unknown_response_id');
  if (!Number.isInteger(message.status) || message.status < 100 || message.status > 599 ||
      typeof message.body_base64 !== 'string' || message.body_base64.length > Math.ceil(MAX_BODY_BYTES * 4 / 3) + 4 ||
      !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(message.body_base64)) {
    throw new Error('invalid_host_response');
  }
  const body = Buffer.from(message.body_base64, 'base64');
  if (body.length > MAX_BODY_BYTES) throw new Error('host_response_too_large');
  const waiting = pending.get(message.id);
  pending.delete(message.id);
  clearTimeout(waiting.timer);
  const headers = {};
  if (message.headers && typeof message.headers === 'object') {
    for (const [key, value] of Object.entries(message.headers)) {
      if (ALLOWED_RESPONSE_HEADERS.has(key.toLowerCase()) && typeof value === 'string' && !/[\r\n]/.test(value)) {
        headers[key.toLowerCase()] = value;
      }
    }
  }
  if (typeof message.content_type === 'string' && !/[\r\n]/.test(message.content_type)) headers['content-type'] = message.content_type;
  headers['content-length'] = body.length;
  waiting.response.writeHead(message.status, headers);
  waiting.response.end(body);
}

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
input.on('line', line => {
  if (line.length > MAX_MESSAGE_BYTES) { stop(1, 'host_message_too_large'); return; }
  let message;
  try { message = JSON.parse(line); }
  catch { stop(1, 'invalid_host_json'); return; }
  if (message.channel === 'start') start(message.config).catch(error => stop(1, error.message === 'duplicate_start' || error.message === 'invalid_start_config' ? error.message : 'container_start_error'));
  else if (message.channel === 'response') {
    try { respond(message); } catch (error) { stop(1, error.message); }
  } else stop(1, 'unknown_host_channel');
});
input.on('close', () => { if (!ending) stop(1, 'host_input_closed'); });
process.on('SIGTERM', () => stop(1, 'container_terminated'));
