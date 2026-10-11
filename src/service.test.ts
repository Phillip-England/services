import {expect, test} from 'bun:test';
import {mkdtemp, readFile, writeFile, mkdir, readdir, rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {ServiceManager, serviceName, type Runner} from './service.ts';

test('service names reject paths and unsafe characters', () => {
  expect(serviceName('api')).toBe('api.service');
  expect(serviceName('api.service')).toBe('api.service');
  expect(() => serviceName('../api')).toThrow();
  expect(() => serviceName('api\n[Install]')).toThrow();
});

test('list combines boot and live state and prioritizes failures', async () => {
  const runner: Runner = async (_program, args) => {
    if (args.includes('list-unit-files')) return {code: 0, output: 'api.service enabled\nbackup.service disabled\nworker.service disabled\n'};
    if (args.includes('list-units')) return {code: 0, output: 'api.service loaded active running API server\n● worker.service loaded failed failed Background worker\n'};
    throw new Error(`Unexpected ${args.join(' ')}`);
  };
  const rows = await new ServiceManager('system', runner).list();
  expect(rows.map(row => row.name)).toEqual(['worker.service', 'api.service', 'backup.service']);
  expect(rows[0]?.live).toBe('failed');
  expect(rows[1]?.description).toBe('API server');
});

test('create, set, unset and remove a local unit', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'services-test-'));
  const commands: string[] = [];
  const runner: Runner = async (_program, args) => {
    commands.push(args.join(' '));
    if (args.includes('show')) return {code: 0, output: 'loaded\n'};
    return {code: 0, output: ''};
  };
  const manager = new ServiceManager('system', runner, dir);
  try {
    await manager.create({name: 'api', exec: '/bin/echo hello', description: 'API'});
    expect(await readFile(path.join(dir, 'api.service'), 'utf8')).toContain('ExecStart=/bin/echo hello');
    await manager.set('api', 'restart', 'always');
    expect(await readFile(path.join(dir, 'api.service.d', '50-services.conf'), 'utf8')).toContain('Restart=always');
    await manager.set('api', 'env.PORT', '8080');
    expect(await readFile(path.join(dir, 'api.service.d', '60-services-env-PORT.conf'), 'utf8')).toContain('Environment="PORT=8080"');
    await manager.unset('api', 'restart');
    expect(await readdir(path.join(dir, 'api.service.d'))).not.toContain('50-services.conf');
    await manager.remove('api');
    expect(await readdir(dir)).toEqual([]);
    expect(commands).toContain('disable --now api.service');
  } finally { await rm(dir, {recursive: true, force: true}); }
});

test('clone copies unit and drop-ins', async () => {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'services-clone-'));
  const source = path.join(dir, 'source.service');
  const dropin = path.join(dir, 'source.service.d', 'override.conf');
  await mkdir(path.dirname(dropin));
  await writeFile(source, '[Service]\nExecStart=/bin/true\n');
  await writeFile(dropin, '[Service]\nRestart=always\n');
  const runner: Runner = async (_program, args) => {
    if (args.includes('--property=FragmentPath')) return {code: 0, output: source};
    if (args.includes('--property=DropInPaths')) return {code: 0, output: dropin};
    return {code: 0, output: ''};
  };
  try {
    const manager = new ServiceManager('system', runner, dir);
    await manager.clone('source', 'copy');
    expect(await readFile(path.join(dir, 'copy.service'), 'utf8')).toContain('ExecStart=/bin/true');
    expect(await readFile(path.join(dir, 'copy.service.d', 'override.conf'), 'utf8')).toContain('Restart=always');
  } finally { await rm(dir, {recursive: true, force: true}); }
});
