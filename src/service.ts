import {spawn} from 'node:child_process';
import {promises as fs} from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export type Scope = 'system' | 'user';
export type Service = {name: string; boot: string; live: string; description: string};
export type Result = {output: string; code: number};
export type Runner = (program: string, args: string[]) => Promise<Result>;

export const runProcess: Runner = (program, args) => new Promise((resolve, reject) => {
  const child = spawn(program, args, {env: {...process.env, SYSTEMD_COLORS: '0'}, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '';
  let stderr = '';
  child.stdout.setEncoding('utf8').on('data', chunk => { stdout += chunk; });
  child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
  child.on('error', reject);
  child.on('close', code => resolve({output: stdout + (stderr ? (stdout ? '\n' : '') + stderr : ''), code: code ?? 1}));
});

export function serviceName(raw: string): string {
  const base = raw.endsWith('.service') ? raw.slice(0, -8) : raw;
  if (!/^[A-Za-z0-9][A-Za-z0-9_.@-]*$/.test(base) || base === '.' || base === '..') throw new Error(`Invalid service name: ${raw}`);
  return `${base}.service`;
}

function validValue(value: string): string {
  if (/[\r\n\0]/.test(value)) throw new Error('Values cannot contain newlines or NUL bytes');
  return value;
}

function quoteEnv(key: string, value: string): string {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error(`Invalid environment key: ${key}`);
  return `"${`${key}=${validValue(value)}`.replaceAll('\\', '\\\\').replaceAll('"', '\\"').replaceAll('%', '%%')}"`;
}

const aliases: Record<string, string> = {
  exec: 'Service.ExecStart', workdir: 'Service.WorkingDirectory', user: 'Service.User',
  group: 'Service.Group', restart: 'Service.Restart', 'restart-sec': 'Service.RestartSec',
  type: 'Service.Type', description: 'Unit.Description', after: 'Unit.After',
  wants: 'Unit.Wants', 'wanted-by': 'Install.WantedBy'
};

function directive(input: string): [string, string] {
  const name = aliases[input.toLowerCase()] ?? input;
  const match = /^([A-Za-z][A-Za-z0-9]*)\.([A-Za-z][A-Za-z0-9]*)$/.exec(name);
  if (!match) throw new Error(`Invalid setting ${input}; use an alias or Section.Key`);
  return [match[1]!, match[2]!];
}

type Unit = Map<string, Map<string, string>>;
function parseUnit(text: string): Unit {
  const unit: Unit = new Map();
  let section = '';
  for (const [index, raw] of text.split('\n').entries()) {
    const line = raw.trim();
    if (!line || line.startsWith('#') || line.startsWith(';')) continue;
    if (line.startsWith('[') && line.endsWith(']')) {
      section = line.slice(1, -1);
      if (!/^[A-Za-z][A-Za-z0-9]*$/.test(section)) throw new Error(`Invalid section on line ${index + 1}`);
      continue;
    }
    const at = line.indexOf('=');
    const key = line.slice(0, at);
    if (!section || at < 0 || !/^[A-Za-z][A-Za-z0-9]*$/.test(key)) throw new Error(`Invalid directive on line ${index + 1}`);
    if (!unit.has(section)) unit.set(section, new Map());
    unit.get(section)!.set(key, line.slice(at + 1));
  }
  return unit;
}
function renderUnit(unit: Unit): string {
  return [...unit.keys()].sort().map(section => {
    const entries = [...unit.get(section)!.entries()].sort(([a], [b]) => a.localeCompare(b));
    return `[${section}]\n` + entries.map(([key, value]) => `${section === 'Service' && key === 'ExecStart' ? 'ExecStart=\n' : ''}${key}=${value}\n`).join('') + '\n';
  }).join('');
}
async function atomicWrite(filename: string, data: string): Promise<void> {
  await fs.mkdir(path.dirname(filename), {recursive: true});
  const temp = path.join(path.dirname(filename), `.services-${process.pid}-${Math.random().toString(36).slice(2)}`);
  try { await fs.writeFile(temp, data, {mode: 0o644, flag: 'wx'}); await fs.rename(temp, filename); }
  finally { await fs.rm(temp, {force: true}); }
}
async function readOrEmpty(filename: string): Promise<string> {
  try { return await fs.readFile(filename, 'utf8'); }
  catch (error) { if ((error as NodeJS.ErrnoException).code === 'ENOENT') return ''; throw error; }
}

export class ServiceManager {
  readonly unitDir: string;
  constructor(readonly scope: Scope, private readonly runner: Runner = runProcess, unitDir?: string) {
    this.unitDir = unitDir ?? (scope === 'user' ? path.join(os.homedir(), '.config/systemd/user') : '/etc/systemd/system');
  }
  private async command(program: string, args: string[], allowFailure = false): Promise<string> {
    const result = await this.runner(program, args);
    if (result.code !== 0 && !allowFailure) throw new Error(result.output.trim() || `${program} exited with code ${result.code}`);
    return result.output.trimEnd();
  }
  systemctl(...args: string[]): Promise<string> {
    return this.command('systemctl', [...(this.scope === 'user' ? ['--user'] : []), ...args]);
  }
  async list(): Promise<Service[]> {
    const files = await this.systemctl('list-unit-files', '--type=service', '--no-legend', '--no-pager', '--plain');
    const rows = new Map<string, Service>();
    for (const line of files.split('\n')) {
      const [name, boot] = line.trim().split(/\s+/);
      if (name?.endsWith('.service') && boot) rows.set(name, {name, boot, live: 'inactive', description: ''});
    }
    try {
      const live = await this.systemctl('list-units', '--all', '--type=service', '--no-legend', '--no-pager', '--plain');
      for (const line of live.split('\n')) {
        const fields = line.trim().split(/\s+/);
        if (fields[0] === '●') fields.shift();
        const row = rows.get(fields[0] ?? '');
        if (row && fields.length >= 4) { row.live = fields[3]!; row.description = fields.slice(4).join(' '); }
      }
    } catch { /* Boot state and unit names are still useful. */ }
    const rank = (row: Service) => row.live === 'failed' ? 0 : row.live === 'running' ? 1 : row.boot.startsWith('enabled') ? 2 : 3;
    return [...rows.values()].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
  }
  async read(kind: 'status' | 'unit' | 'logs', rawName: string): Promise<string> {
    const name = serviceName(rawName);
    if (kind === 'logs') return this.command('journalctl', ['--no-pager', '-n', '100', ...(this.scope === 'user' ? ['--user-unit'] : ['-u']), name], true);
    return this.command('systemctl', [...(this.scope === 'user' ? ['--user'] : []), '--no-pager', '--plain', kind === 'unit' ? 'cat' : 'status', name], true);
  }
  async control(action: 'start' | 'stop' | 'restart' | 'enable' | 'disable' | 'mask' | 'unmask', rawName: string): Promise<string> {
    const name = serviceName(rawName);
    await this.systemctl(action, name);
    return `${action} ${name} complete`;
  }
  async create(values: {name: string; exec: string; description?: string; workdir?: string; user?: string}): Promise<string> {
    const name = serviceName(values.name);
    for (const value of Object.values(values)) validValue(value ?? '');
    if (!values.exec.trim()) throw new Error('ExecStart command is required');
    const lines = ['[Unit]', `Description=${values.description || name.slice(0, -8)}`];
    if (this.scope === 'system') lines.push('After=network.target');
    lines.push('', '[Service]', `ExecStart=${values.exec}`);
    if (values.workdir) lines.push(`WorkingDirectory=${values.workdir}`);
    if (values.user) lines.push(`User=${values.user}`);
    lines.push('Restart=on-failure', '', '[Install]', `WantedBy=${this.scope === 'user' ? 'default.target' : 'multi-user.target'}`, '');
    await fs.mkdir(this.unitDir, {recursive: true});
    await fs.writeFile(path.join(this.unitDir, name), lines.join('\n'), {flag: 'wx', mode: 0o644});
    await this.systemctl('daemon-reload');
    return `Created ${name}`;
  }
  private dropin(name: string): string { return path.join(this.unitDir, `${name}.d`, '50-services.conf'); }
  private envPath(name: string, key: string): string { return path.join(this.unitDir, `${name}.d`, `60-services-env-${key}.conf`); }
  private async ensureLoaded(name: string): Promise<void> {
    const state = await this.systemctl('show', name, '--property=LoadState', '--value');
    if (state.trim() !== 'loaded') throw new Error(`${name} is not loaded`);
  }
  async set(rawName: string, key: string, value: string): Promise<string> {
    const name = serviceName(rawName);
    validValue(value);
    await this.ensureLoaded(name);
    if (key.startsWith('env.')) {
      const envKey = key.slice(4);
      await atomicWrite(this.envPath(name, envKey), `[Service]\nEnvironment=${quoteEnv(envKey, value)}\n`);
    } else {
      const [section, setting] = directive(key);
      const unit = parseUnit(await readOrEmpty(this.dropin(name)));
      if (!unit.has(section)) unit.set(section, new Map());
      unit.get(section)!.set(setting, value);
      await atomicWrite(this.dropin(name), renderUnit(unit));
    }
    await this.systemctl('daemon-reload');
    return `Updated ${name}. Restart it to apply runtime changes.`;
  }
  async unset(rawName: string, key: string): Promise<string> {
    const name = serviceName(rawName);
    await this.ensureLoaded(name);
    if (key.startsWith('env.')) {
      const envKey = key.slice(4);
      quoteEnv(envKey, '');
      await fs.rm(this.envPath(name, envKey), {force: true});
    } else {
      const [section, setting] = directive(key);
      const filename = this.dropin(name);
      const unit = parseUnit(await readOrEmpty(filename));
      unit.get(section)?.delete(setting);
      if (unit.get(section)?.size === 0) unit.delete(section);
      if (unit.size) await atomicWrite(filename, renderUnit(unit));
      else await fs.rm(filename, {force: true});
    }
    await this.systemctl('daemon-reload');
    return `Updated ${name}. Restart it to apply runtime changes.`;
  }
  async clone(rawSource: string, rawDest: string): Promise<string> {
    const source = serviceName(rawSource), dest = serviceName(rawDest);
    if (source === dest) throw new Error('Source and destination must differ');
    const fragment = (await this.systemctl('show', source, '--property=FragmentPath', '--value')).trim();
    if (!fragment) throw new Error(`No unit file found for ${source}`);
    const data = await fs.readFile(fragment);
    const dropins = (await this.systemctl('show', source, '--property=DropInPaths', '--value')).trim().split(/\s+/).filter(Boolean);
    const copies = await Promise.all(dropins.map(async filename => ({name: path.basename(filename), data: await fs.readFile(filename)})));
    await fs.mkdir(this.unitDir, {recursive: true});
    await fs.writeFile(path.join(this.unitDir, dest), data, {flag: 'wx', mode: 0o644});
    for (const copy of copies) { await fs.mkdir(path.join(this.unitDir, `${dest}.d`), {recursive: true}); await fs.writeFile(path.join(this.unitDir, `${dest}.d`, copy.name), copy.data, {flag: 'wx', mode: 0o644}); }
    if (copies.length) await atomicWrite(path.join(this.unitDir, `${dest}.d`, '.services-clone-manifest'), copies.map(copy => copy.name).join('\n') + '\n');
    await this.systemctl('daemon-reload');
    return `Cloned ${source} to ${dest}. Review its command before starting.`;
  }
  async remove(rawName: string): Promise<string> {
    const name = serviceName(rawName);
    const filename = path.join(this.unitDir, name);
    const stat = await fs.lstat(filename);
    if (!stat.isFile()) throw new Error(`Only local regular unit files can be removed: ${filename}`);
    await this.systemctl('disable', '--now', name);
    await fs.unlink(filename);
    const dir = path.join(this.unitDir, `${name}.d`);
    const manifest = await readOrEmpty(path.join(dir, '.services-clone-manifest'));
    const owned = new Set(['50-services.conf', '.services-clone-manifest', ...manifest.split('\n').filter(item => item.endsWith('.conf') && path.basename(item) === item)]);
    try {
      for (const entry of await fs.readdir(dir, {withFileTypes: true})) {
        if (entry.isFile() && (owned.has(entry.name) || /^60-services-env-.*\.conf$/.test(entry.name))) await fs.rm(path.join(dir, entry.name));
      }
      await fs.rmdir(dir).catch(() => {});
    } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
    await this.systemctl('daemon-reload');
    return `Removed ${name}`;
  }
}
