import React, {useEffect, useMemo, useRef, useState} from 'react';
import readline from 'node:readline';
import {Box, Text, render, useApp, useStdout} from 'ink';
import type {Scope, Service} from './service.ts';
import {ServiceManager} from './service.ts';

type Mode = 'list' | 'search' | 'menu' | 'form' | 'confirm' | 'view' | 'help';
type FormKind = 'create' | 'set' | 'unset' | 'clone';
type FormField = {label: string; key: string; required?: boolean};
type Form = {kind: FormKind; fields: FormField[]; values: Record<string, string>; index: number; draft: string};
type Confirm = {message: string; run: () => Promise<string>; nextSelected?: string};
type View = {title: string; content: string[]; offset: number};

type Action = {label: string; hint?: string; kind: string};
function actions(row: Service): Action[] {
  return [
    {label: 'Status', hint: 'Live state and recent output', kind: 'status'},
    {label: 'Recent logs', hint: 'Last 100 journal lines', kind: 'logs'},
    {label: 'Unit file', hint: 'Effective systemd configuration', kind: 'unit'},
    {label: row.live === 'running' ? 'Restart' : 'Start', kind: row.live === 'running' ? 'restart' : 'start'},
    ...(row.live === 'running' ? [{label: 'Stop', kind: 'stop'}] : []),
    {label: row.boot.startsWith('enabled') ? 'Disable at boot' : 'Enable at boot', kind: row.boot.startsWith('enabled') ? 'disable' : 'enable'},
    {label: row.boot.startsWith('masked') ? 'Unmask' : 'Mask', kind: row.boot.startsWith('masked') ? 'unmask' : 'mask'},
    {label: 'Change setting', hint: 'Writes a systemd drop-in', kind: 'set'},
    {label: 'Remove setting', hint: 'Removes a setting made by services', kind: 'unset'},
    {label: 'Clone service', kind: 'clone'},
    {label: 'Remove local service', hint: 'Stops and deletes a local unit', kind: 'remove'}
  ];
}

function formFor(kind: FormKind): Form {
  const fields: Record<FormKind, FormField[]> = {
    create: [{label: 'Service name', key: 'name', required: true}, {label: 'Command to run', key: 'exec', required: true}, {label: 'Description', key: 'description'}, {label: 'Working directory', key: 'workdir'}, {label: 'Run as user', key: 'user'}],
    set: [{label: 'Setting key (e.g. restart or env.PORT)', key: 'key', required: true}, {label: 'Value', key: 'value'}],
    unset: [{label: 'Setting key to remove', key: 'key', required: true}],
    clone: [{label: 'New service name', key: 'name', required: true}]
  };
  return {kind, fields: fields[kind], values: {}, index: 0, draft: ''};
}
const stripAnsi = (text: string) => text.replace(/\x1b\[[0-9;?]*[ -/]*[@-~]/g, '').replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '');
const short = (name: string) => name.replace(/\.service$/, '');

type Key = {escape: boolean; return: boolean; backspace: boolean; delete: boolean; ctrl: boolean; ctrlC: boolean; meta: boolean; downArrow: boolean; upArrow: boolean; pageDown: boolean; pageUp: boolean; tab: boolean};
function useKeys(handler: (input: string, key: Key) => void): void {
  const latest = useRef(handler);
  latest.current = handler;
  useEffect(() => {
    readline.emitKeypressEvents(process.stdin);
    const onKey = (input: string, key: readline.Key) => latest.current(input ?? '', {
      escape: key.name === 'escape', return: key.name === 'return', backspace: key.name === 'backspace', delete: key.name === 'delete',
      ctrl: !!key.ctrl, ctrlC: !!key.ctrl && key.name === 'c', meta: !!key.meta, downArrow: key.name === 'down', upArrow: key.name === 'up',
      pageDown: key.name === 'pagedown', pageUp: key.name === 'pageup', tab: key.name === 'tab'
    });
    process.stdin.on('keypress', onKey);
    return () => { process.stdin.off('keypress', onKey); };
  }, []);
}

function App({manager, scope}: {manager: ServiceManager; scope: Scope}) {
  const {exit} = useApp();
  const {stdout: inkStdout} = useStdout();
  const stdout = inkStdout as NodeJS.WriteStream;
  const [size, setSize] = useState({width: stdout.columns || 80, height: stdout.rows || 24});
  const [rows, setRows] = useState<Service[]>([]);
  const [selectedName, setSelectedName] = useState('');
  const [mode, setMode] = useState<Mode>('list');
  const [showAll, setShowAll] = useState(false);
  const [query, setQuery] = useState('');
  const [searchBefore, setSearchBefore] = useState('');
  const [menuIndex, setMenuIndex] = useState(0);
  const [form, setForm] = useState<Form | null>(null);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [view, setView] = useState<View | null>(null);
  const [message, setMessage] = useState('Loading services…');
  const [busy, setBusy] = useState(false);
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const resize = () => setSize({width: stdout.columns || 80, height: stdout.rows || 24});
    stdout.on('resize', resize);
    return () => { stdout.off('resize', resize); };
  }, [stdout]);
  useEffect(() => {
    let alive = true;
    manager.list().then(next => {
      if (alive) {
        setRows(next);
        setMessage(current => current === 'Loading services…' || current === 'Refreshing…' ? (next.length ? '' : 'No service units found') : current);
      }
    }).catch(error => { if (alive) setMessage(`Could not load services: ${String(error.message ?? error)}`); });
    return () => { alive = false; };
  }, [manager, generation]);

  const visible = useMemo(() => rows.filter(row =>
    (showAll || row.live === 'running' || row.live === 'failed' || row.boot.startsWith('enabled')) &&
    (!query || `${row.name} ${row.description}`.toLowerCase().includes(query.toLowerCase()))
  ), [rows, showAll, query]);
  const currentIndex = Math.max(0, visible.findIndex(row => row.name === selectedName));
  const current = visible[currentIndex];
  const menu = current ? actions(current) : [];
  const running = rows.filter(row => row.live === 'running').length;
  const failed = rows.filter(row => row.live === 'failed').length;
  const width = Math.max(30, size.width);
  const height = Math.max(12, size.height);
  const listHeight = Math.max(4, height - 12);
  const first = Math.max(0, currentIndex - listHeight + 1);
  const list = visible.slice(first, first + listHeight);

  const refresh = () => setGeneration(value => value + 1);
  const runTask = async (task: () => Promise<string>, nextSelected?: string) => {
    setBusy(true);
    setMode('list');
    try {
      const result = await task();
      setMessage(result);
      if (nextSelected) {
        setSelectedName(nextSelected.endsWith('.service') ? nextSelected : `${nextSelected}.service`);
        setShowAll(true);
        setQuery('');
      }
      refresh();
    } catch (error) { setMessage(String((error as Error).message ?? error)); }
    finally { setBusy(false); }
  };
  const openView = async (kind: 'status' | 'unit' | 'logs') => {
    if (!current) return;
    setBusy(true);
    try {
      const content = await manager.read(kind, current.name);
      setView({title: `${short(current.name)} / ${kind}`, content: stripAnsi(content || '(No output)').split('\n'), offset: 0});
      setMode('view');
    } catch (error) { setMessage(String((error as Error).message ?? error)); }
    finally { setBusy(false); }
  };
  const submitForm = (next: Form) => {
    const values = {...next.values, [next.fields[next.index]!.key]: next.draft.trim()};
    const field = next.fields[next.index]!;
    if (field.required && !values[field.key]) { setMessage(`${field.label} is required`); return; }
    if (next.index < next.fields.length - 1) {
      setForm({...next, values, index: next.index + 1, draft: ''});
      setMessage('');
      return;
    }
    setForm(null);
    if (next.kind === 'create') {
      setConfirm({message: `Create ${values.name}.service with command ${values.exec}?`, nextSelected: values.name, run: () => manager.create({name: values.name!, exec: values.exec!, description: values.description, workdir: values.workdir, user: values.user})});
    } else if (next.kind === 'set' && current) {
      setConfirm({message: `Set ${values.key} on ${current.name}?`, run: () => manager.set(current.name, values.key!, values.value ?? '')});
    } else if (next.kind === 'unset' && current) {
      setConfirm({message: `Remove ${values.key} override from ${current.name}?`, run: () => manager.unset(current.name, values.key!)});
    } else if (next.kind === 'clone' && current) {
      setConfirm({message: `Clone ${current.name} to ${values.name}.service?`, nextSelected: values.name, run: () => manager.clone(current.name, values.name!)});
    }
    setMode('confirm');
  };
  const chooseAction = (kind: string) => {
    if (!current) return;
    if (kind === 'status' || kind === 'unit' || kind === 'logs') { void openView(kind); return; }
    if (kind === 'set' || kind === 'unset' || kind === 'clone') { setForm(formFor(kind)); setMode('form'); return; }
    if (kind === 'remove') {
      setConfirm({message: `Stop, disable, and DELETE ${current.name}? This cannot be undone.`, run: () => manager.remove(current.name)});
      setMode('confirm'); return;
    }
    const action = kind as 'start' | 'stop' | 'restart' | 'enable' | 'disable' | 'mask' | 'unmask';
    setConfirm({message: `${action[0]!.toUpperCase() + action.slice(1)} ${current.name}?`, run: () => manager.control(action, current.name)});
    setMode('confirm');
  };

  useKeys((input, key) => {
    if (key.ctrlC) { exit(); return; }
    if (busy) return;
    if (mode === 'search') {
      if (key.escape) { setQuery(searchBefore); setMode('list'); }
      else if (key.return) setMode('list');
      else if (key.backspace || key.delete) setQuery(value => value.slice(0, -1));
      else if (!key.ctrl && !key.meta && input) setQuery(value => value + input);
      return;
    }
    if (mode === 'form' && form) {
      if (key.escape) { setForm(null); setMode('list'); setMessage('Cancelled'); }
      else if (key.return) submitForm(form);
      else if (key.backspace || key.delete) setForm({...form, draft: form.draft.slice(0, -1)});
      else if (!key.ctrl && !key.meta && input) setForm({...form, draft: form.draft + input});
      return;
    }
    if (mode === 'confirm') {
      if (input.toLowerCase() === 'y' && confirm) { void runTask(confirm.run, confirm.nextSelected); setConfirm(null); }
      else if (key.escape || input.toLowerCase() === 'n' || key.return) { setConfirm(null); setMode('list'); setMessage('Cancelled'); }
      return;
    }
    if (mode === 'view' && view) {
      if (key.escape || input === 'q' || key.return) { setMode('list'); setView(null); }
      else if (key.downArrow || input === 'j') setView({...view, offset: Math.min(Math.max(0, view.content.length - (height - 9)), view.offset + 1)});
      else if (key.upArrow || input === 'k') setView({...view, offset: Math.max(0, view.offset - 1)});
      else if (key.pageDown) setView({...view, offset: Math.min(Math.max(0, view.content.length - (height - 9)), view.offset + (height - 9))});
      else if (key.pageUp) setView({...view, offset: Math.max(0, view.offset - (height - 9))});
      return;
    }
    if (mode === 'help') { if (key.escape || key.return || input === '?' || input === 'q') setMode('list'); return; }
    if (mode === 'menu') {
      if (key.escape || input === 'q') setMode('list');
      else if (key.downArrow || input === 'j') setMenuIndex(i => Math.min(menu.length - 1, i + 1));
      else if (key.upArrow || input === 'k') setMenuIndex(i => Math.max(0, i - 1));
      else if (key.return && menu[menuIndex]) chooseAction(menu[menuIndex].kind);
      return;
    }
    if (input === 'q' || key.escape) exit();
    else if (key.downArrow || input === 'j') setSelectedName(visible[Math.min(visible.length - 1, currentIndex + 1)]?.name ?? '');
    else if (key.upArrow || input === 'k') setSelectedName(visible[Math.max(0, currentIndex - 1)]?.name ?? '');
    else if (key.tab) { setShowAll(value => !value); setMessage(''); }
    else if (input === '/') { setSearchBefore(query); setMode('search'); }
    else if (input === 'n') { setForm(formFor('create')); setMode('form'); }
    else if (input === 'a' && current) { setMenuIndex(0); setMode('menu'); }
    else if (key.return && current) void openView('status');
    else if (input === '?') setMode('help');
    else if (input === 'r') { setMessage('Refreshing…'); refresh(); }
  });

  const accent = failed ? 'red' : 'cyan';
  const listWidth = width >= 100 ? Math.floor(width * .62) : width;
  const serviceColumnWidth = Math.max(8, listWidth - 25);
  const menuHeight = Math.max(4, height - 10);
  const menuStart = Math.max(0, menuIndex - menuHeight + 1);
  if (size.width < 54 || size.height < 15) return <Box borderStyle="round" borderColor="yellow" flexDirection="column" paddingX={1}>
    <Text bold color="cyan">SERVICES</Text>
    <Text>Terminal too small. Resize to at least 54 × 15.</Text>
    <Text color="gray">Press q to quit.</Text>
  </Box>;
  return <Box flexDirection="column" width={width}>
    <Box paddingX={1} justifyContent="space-between">
      <Text color="cyan" bold>SERVICES <Text color="gray">/ {scope.toUpperCase()}</Text></Text>
      <Text><Text color="green">● {running} running</Text>  <Text color={failed ? 'red' : 'gray'}>● {failed} failed</Text></Text>
    </Box>
    <Box paddingX={1} marginBottom={1} justifyContent="space-between">
      <Text color="gray">Browse and manage systemd services</Text>
      <Text color={accent}>{showAll ? 'ALL SERVICES' : 'FOCUS'} <Text color="gray">· {visible.length} shown</Text></Text>
    </Box>
    {mode === 'view' && view ? <Box borderStyle="round" borderColor="cyan" flexDirection="column" paddingX={1} height={Math.max(6, height - 6)}>
      <Text bold color="cyan">{view.title}</Text>
      {view.content.slice(view.offset, view.offset + Math.max(1, height - 10)).map((line, index) => <Text key={`${view.offset}-${index}`} wrap="truncate-end">{line || ' '}</Text>)}
      <Text color="gray">{view.offset + 1}–{Math.min(view.content.length, view.offset + height - 10)} / {view.content.length}</Text>
    </Box> : mode === 'help' ? <Box borderStyle="round" borderColor="cyan" flexDirection="column" paddingX={2}>
      <Text bold color="cyan">KEYBOARD</Text>
      <Text>↑ ↓ / j k    Select service</Text><Text>Enter        Status</Text><Text>a            Actions for selected service</Text>
      <Text>n            Create a service</Text><Text>/            Search names and descriptions</Text><Text>Tab          Focus / all services</Text>
      <Text>r            Refresh</Text><Text>q            Quit</Text><Text color="gray">Esc cancels a prompt. Confirm changes with y.</Text>
    </Box> : mode === 'menu' ? <Box borderStyle="round" borderColor="cyan" flexDirection="column" paddingX={2}>
      <Text bold color="cyan">{current?.name} / ACTIONS</Text>
      {menu.slice(menuStart, menuStart + menuHeight).map((item, offset) => {
        const index = menuStart + offset;
        return <Box key={item.kind}><Text color={index === menuIndex ? 'cyan' : undefined} bold={index === menuIndex}>{index === menuIndex ? '› ' : '  '}{item.label}</Text>{width >= 90 && <Text color="gray">{item.hint ? `  ${item.hint}` : ''}</Text>}</Box>;
      })}
      <Text color="gray">{menuStart + 1}–{Math.min(menu.length, menuStart + menuHeight)} of {menu.length}</Text>
    </Box> : mode === 'form' && form ? <Box borderStyle="round" borderColor="cyan" flexDirection="column" paddingX={2}>
      <Text bold color="cyan">{form.kind.toUpperCase()} SERVICE <Text color="gray">· step {form.index + 1} of {form.fields.length}</Text></Text>
      {form.fields.map((field, index) => <Text key={field.key} color={index === form.index ? 'white' : 'gray'}>{index === form.index ? '› ' : '  '}{field.label}{index < form.index ? `: ${form.values[field.key] || '—'}` : ''}</Text>)}
      <Box marginTop={1}><Text color="cyan">{form.fields[form.index]!.label}: </Text><Text>{form.draft}<Text inverse> </Text></Text></Box>
    </Box> : mode === 'confirm' && confirm ? <Box borderStyle="round" borderColor="yellow" flexDirection="column" paddingX={2}>
      <Text color="yellow" bold>CONFIRM CHANGE</Text>
      <Text wrap="wrap">{confirm.message}</Text>
      <Text color="gray">Press y to continue, n or Esc to cancel.</Text>
    </Box> : <Box flexDirection="row" gap={1}>
      <Box borderStyle="round" borderColor="cyan" flexDirection="column" width={listWidth} height={listHeight + 5} paddingX={1}>
        <Box justifyContent="space-between"><Text bold> SERVICE</Text><Text color="gray">LIVE      BOOT </Text></Box>
        {list.length ? list.map(row => <Box key={row.name} justifyContent="space-between">
          <Box width={serviceColumnWidth}><Text color={row.name === current?.name ? 'cyan' : undefined} bold={row.name === current?.name} wrap="truncate-end">{row.name === current?.name ? '› ' : '  '}{short(row.name)}</Text></Box>
          <Text color={row.live === 'failed' ? 'red' : row.live === 'running' ? 'green' : 'gray'}>{row.live.padEnd(9).slice(0, 9)} <Text color="gray">{row.boot.padEnd(9).slice(0, 9)}</Text></Text>
        </Box>) : <Text color="gray">No matches. Try Tab or change the search.</Text>}
        <Box flexGrow={1}/><Text color="gray">{visible.length ? `${first + 1}–${Math.min(first + listHeight, visible.length)} of ${visible.length}` : '0 services'}</Text>
      </Box>
      {width >= 100 && <Box borderStyle="round" borderColor="gray" flexDirection="column" width={width - Math.floor(width * .62) - 1} height={listHeight + 5} paddingX={1}>
        <Text bold color="cyan">SELECTED SERVICE</Text>
        <Text wrap="truncate-end">{current ? short(current.name) : 'None selected'}</Text>
        <Box marginTop={1} flexDirection="column"><Text color="gray">Live state</Text><Text color={current?.live === 'failed' ? 'red' : current?.live === 'running' ? 'green' : 'yellow'}>{current?.live || '—'}</Text></Box>
        <Box marginTop={1} flexDirection="column"><Text color="gray">Boot state</Text><Text>{current?.boot || '—'}</Text></Box>
        <Box marginTop={1} flexDirection="column"><Text color="gray">Description</Text><Text wrap="truncate-end">{current?.description || 'No description available'}</Text></Box>
        <Box flexGrow={1}/><Text color="gray">Enter status  ·  a actions</Text>
      </Box>}
    </Box>}
    {mode === 'search' && <Box paddingX={1}><Text color="cyan">Search / </Text><Text>{query}<Text inverse> </Text></Text></Box>}
    <Box paddingX={1} marginTop={1}><Text color={message.includes('Could not') || message.includes('Invalid') ? 'red' : 'gray'} wrap="truncate-end">{busy ? 'Working…' : message || (mode === 'list' ? '↑↓ move  ·  Enter status  ·  a actions  ·  n new  ·  / search  ·  Tab all  ·  ? help  ·  q quit' : '↑↓ move  ·  Enter select  ·  Esc back')}</Text></Box>
  </Box>;
}

export async function launchTUI(manager: ServiceManager, scope: Scope): Promise<void> {
  if (!process.stdin.isTTY || !process.stdout.isTTY) throw new Error('The TUI needs an interactive terminal');
  // Ink owns the layout; readline handles input because Bun does not reliably
  // emit the `readable` events expected by Ink's useInput on every PTY.
  process.stdin.setRawMode(true);
  process.stdin.resume();
  process.stdout.write('\x1b[?1049h\x1b[?25l');
  try {
    const instance = render(<App manager={manager} scope={scope}/>, {interactive: true, exitOnCtrlC: false});
    await instance.waitUntilExit();
  } finally {
    process.stdin.setRawMode(false);
    process.stdin.pause();
    process.stdout.write('\x1b[0m\x1b[?25h\x1b[?1049l');
  }
}
