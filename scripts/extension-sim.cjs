// SPDX-License-Identifier: MIT
//
// Behaviour test of the gswitch-focus extension's purpose reporting, run by
// scripts/check-extension.sh when node is available. Both trees are loaded
// into a vm context with stubbed GNOME Shell modules (no GNOME needed) and
// driven through the signals the Shell would emit.
//
// Usage: node scripts/extension-sim.cjs ESM_EXTENSION_JS LEGACY_EXTENSION_JS
// Prints one "ok<TAB>msg" or "fail<TAB>msg" line per check; exits 1 on a
// failure.
'use strict';

const fs = require('fs');
const vm = require('vm');

const [esmPath, legacyPath] = process.argv.slice(2);
if (!esmPath || !legacyPath) {
    console.error('usage: extension-sim.cjs ESM_EXTENSION_JS LEGACY_EXTENSION_JS');
    process.exit(2);
}

class Emitter {
    constructor() {
        this._handlers = new Map();
        this._next = 1;
    }

    connect(signal, cb) {
        const id = this._next++;
        this._handlers.set(id, {signal, cb, owner: null});
        return id;
    }

    connectObject(...args) {
        const owner = args.pop();
        for (let i = 0; i + 1 < args.length; i += 2)
            this._handlers.set(this._next++, {signal: args[i], cb: args[i + 1], owner});
    }

    disconnect(id) {
        this._handlers.delete(id);
    }

    disconnectObject(owner) {
        for (const [id, h] of this._handlers) {
            if (h.owner === owner)
                this._handlers.delete(id);
        }
    }

    emit(signal, ...args) {
        for (const h of [...this._handlers.values()]) {
            if (h.signal === signal)
                h.cb(this, ...args);
        }
    }
}

const CLUTTER_PURPOSES = ['NORMAL', 'ALPHA', 'DIGITS', 'NUMBER', 'PHONE', 'URL', 'EMAIL', 'NAME',
    'PASSWORD', 'DATE', 'TIME', 'DATETIME', 'TERMINAL'];
const IBUS_PURPOSES = ['FREE_FORM', 'ALPHA', 'DIGITS', 'NUMBER', 'PHONE', 'URL', 'EMAIL', 'NAME',
    'PASSWORD', 'PIN', 'TERMINAL'];

function enumOf(names) {
    const e = {};
    names.forEach((n, i) => {
        e[n] = i;
    });
    return e;
}

// env builds the stubs for one run; wayland selects Meta.is_wayland_compositor().
function env(wayland) {
    const display = new Emitter();
    display.focus_window = null;
    const stage = new Emitter();
    const im = new Emitter();
    im.currentFocus = null;
    im.contentPurpose = 0;
    im.contentHints = 0;
    const ibus = new Emitter();
    const tracker = new Emitter();
    tracker.get_window_app = () => null;
    const signals = [];
    const gi = {
        Clutter: {
            InputContentPurpose: enumOf(CLUTTER_PURPOSES),
            InputContentHintFlags: {HIDDEN_TEXT: 1, SENSITIVE_DATA: 2},
        },
        Gio: {
            DBusExportedObject: {
                wrapJSObject: () => ({
                    export() {},
                    unexport() {},
                    emit_property_changed() {},
                    emit_signal(name, variant) {
                        signals.push([name, variant.value]);
                    },
                }),
            },
            DBus: {session: {own_name: () => 1, unown_name() {}}},
            BusNameOwnerFlags: {NONE: 0},
        },
        GLib: {
            Variant: class {
                constructor(type, value) {
                    this.type = type;
                    this.value = value;
                }
            },
        },
        IBus: {InputPurpose: enumOf(IBUS_PURPOSES), InputHints: {HIDDEN_TEXT: 1, PRIVATE: 2}},
        Meta: {is_wayland_compositor: () => wayland},
        Shell: {
            WindowTracker: {get_default: () => tracker},
            AppSystem: {get_default: () => ({get_running: () => []})},
        },
    };
    const main = {inputMethod: im, sessionMode: {isLocked: false}};
    const global = {display, stage};
    return {gi, main, ibus, global, display, stage, im, signals};
}

function loadLegacy(path, e) {
    const ctx = vm.createContext({
        imports: {gi: e.gi, ui: {main: e.main}, misc: {ibusManager: {getIBusManager: () => e.ibus}}},
        global: e.global,
        console,
    });
    vm.runInContext(`${fs.readFileSync(path, 'utf8')}\n;globalThis.__ext = init();`, ctx, {filename: path});
    return ctx.__ext;
}

function loadESM(path, e) {
    let src = fs.readFileSync(path, 'utf8');
    src = src.replace(/^import (\w+) from 'gi:\/\/(\w+)';$/gm, 'const $1 = __gi.$2;');
    src = src.replace(/^import \{Extension\} from '[^']+';$/m, 'const Extension = class {};');
    src = src.replace(/^import \* as Main from '[^']+';$/m, 'const Main = __main;');
    src = src.replace(/^import \{getIBusManager\} from '[^']+';$/m, 'const getIBusManager = () => __ibus;');
    src = src.replace(/^export default class /m, 'globalThis.__Ext = class ');
    if (/^(import|export) /m.test(src))
        throw new Error(`${path}: unhandled import/export line`);
    const ctx = vm.createContext({__gi: e.gi, __main: e.main, __ibus: e.ibus, global: e.global, console});
    vm.runInContext(src, ctx, {filename: path});
    return new ctx.__Ext();
}

let failures = 0;
function check(cond, msg) {
    if (cond) {
        console.log(`ok\t${msg}`);
    } else {
        console.log(`fail\t${msg}`);
        failures++;
    }
}

// run drives one tree through the scenario and returns the purposes seen.
function run(tree, load, path, wayland) {
    const e = env(wayland);
    const ext = load(path, e);
    ext.enable();
    const rep = ext._reporter;
    const purpose = () => rep.GetFocus()[2];
    const label = `${tree} (${wayland ? 'Wayland' : 'X11'}, no IBus)`;

    check(purpose() === 'unknown', `${label}: purpose starts unknown`);

    // Overview search: a Shell entry takes the input focus.
    e.im.currentFocus = {};
    e.im.contentPurpose = 0; // NORMAL
    e.im.emit('notify::content-purpose');
    check(purpose() === 'normal', `${label}: the overview search entry reports normal`);

    // Esc: the entry loses the input focus, the stage key focus moves; the
    // focus window does not change.
    e.im.currentFocus = null;
    e.stage.emit('notify::key-focus');
    const afterEsc = purpose();

    // Back to an X11 client window (a GTK password entry never focuses IBus,
    // and here there is no IBus at all).
    e.display.focus_window = {get_wm_class: () => 'Zenity'};
    e.display.emit('notify::focus-window');
    const afterWindow = purpose();

    if (wayland) {
        check(afterEsc === 'normal' && afterWindow === 'normal',
            `${label}: the Clutter input method stays the purpose source (unchanged)`);
    } else {
        check(afterEsc === 'unknown', `${label}: purpose is unknown once the Shell entry lost the input focus (was ${afterEsc})`);
        check(afterWindow === 'unknown', `${label}: purpose is unknown after the focus moved to an X11 window (was ${afterWindow})`);
        // A Clutter purpose change without a focused Shell entry is not a field.
        e.im.contentPurpose = 0;
        e.im.emit('notify::content-hints');
        check(purpose() === 'unknown', `${label}: a Clutter change without a focused Shell entry stays unknown`);
    }

    // Once IBus reported anything, the Clutter fallback is ignored.
    e.ibus.emit('focus-in');
    e.ibus.emit('set-content-type', 8, 0); // PASSWORD
    check(purpose() === 'password', `${label}: IBus password content type is reported`);
    e.im.currentFocus = {};
    e.im.contentPurpose = 0;
    e.im.emit('notify::content-purpose');
    e.stage.emit('notify::key-focus');
    check(purpose() === 'password', `${label}: Clutter changes are ignored once IBus was seen`);

    ext.disable();
    const leaked = [e.display, e.stage, e.im, e.ibus].some(em => em._handlers.size > 0);
    check(!leaked, `${label}: disable() disconnects every handler`);
}

for (const wayland of [false, true]) {
    run('legacy', loadLegacy, legacyPath, wayland);
    run('ESM', loadESM, esmPath, wayland);
}
process.exit(failures > 0 ? 1 : 0);
