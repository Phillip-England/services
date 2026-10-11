#!/usr/bin/env bun
import {spawnSync} from 'node:child_process';
import {StringDecoder} from 'node:string_decoder';
import {ServiceManager} from './service.ts';
import {launchTUI} from './tui.tsx';

function readPassword(): Promise<string> {
  return new Promise((resolve, reject) => {
    if (!process.stdin.isTTY || !process.stdout.isTTY || !process.stdin.setRawMode) {
      reject(new Error('An interactive terminal is required to enter the sudo password'));
      return;
    }
    let password = '';
    const decoder = new StringDecoder('utf8');
    const done = (error?: Error) => {
      process.stdin.off('data', onData);
      process.stdin.setRawMode(false);
      process.stdin.pause();
      process.stdout.write('\n');
      if (error) reject(error); else resolve(password);
    };
    const onData = (chunk: Buffer) => {
      for (const byte of chunk) {
        if (byte === 13 || byte === 10) { done(); return; }
        if (byte === 3) { done(new Error('Cancelled')); return; }
        if (byte === 127 || byte === 8) password = [...password].slice(0, -1).join('');
        else if (byte >= 32) password += decoder.write(Buffer.from([byte]));
      }
    };
    process.stdin.setRawMode(true);
    process.stdin.resume();
    process.stdin.on('data', onData);
    process.stdout.write('\x1b[36mSERVICES\x1b[0m  /  system\nSudo password: ');
  });
}

async function main() {
  const args = process.argv.slice(2);
  const scope = args.includes('--user') ? 'user' : 'system';
  const elevated = args.includes('--elevated');
  if (args.includes('--help') || args.includes('-h')) {
    console.log('Usage: services [--user]\n\nLaunch the interactive service manager. System mode asks for a sudo password before opening.');
    return;
  }
  if (args.some(arg => !['--user', '--elevated'].includes(arg))) throw new Error('Usage: services [--user]');
  if (!process.stdin.isTTY || !process.stdout.isTTY) throw new Error('An interactive terminal is required');
  if (scope === 'system' && process.getuid?.() !== 0) {
    const password = await readPassword();
    const verified = spawnSync('sudo', ['-k', '-S', '-p', '', '-v'], {input: `${password}\n`, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe']});
    if (verified.error) throw verified.error;
    if (verified.status !== 0) throw new Error(`Sudo password verification failed${verified.stderr?.trim() ? `: ${verified.stderr.trim()}` : ''}`);
    const source = typeof Bun !== 'undefined' && Bun.main.endsWith('.tsx');
    const command = source ? [process.execPath, Bun.main, '--elevated'] : [process.execPath, '--elevated'];
    const child = spawnSync('sudo', ['-n', '--', ...command], {stdio: 'inherit'});
    if (child.error) throw child.error;
    if (child.status !== 0) throw new Error(`Elevated TUI exited with status ${child.status ?? 'unknown'}`);
    return;
  }
  if (elevated && process.getuid?.() !== 0) throw new Error('Elevated mode must be started by sudo');
  await launchTUI(new ServiceManager(scope), scope);
}

main().catch(error => { console.error(`services: ${String(error.message ?? error)}`); process.exitCode = 1; });
