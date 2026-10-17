// SPDX-License-Identifier: MIT
//
// gswitch Focus Reporter: exports the focused application's Shell.App id, its
// WM_CLASS and the input purpose of the focused text field on the session bus
// for the gswitch-auto daemon (internal/gswitch/focus). No window titles,
// text or process ids are exposed.

import Clutter from 'gi://Clutter';
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import IBus from 'gi://IBus';
import Shell from 'gi://Shell';

import {Extension} from 'resource:///org/gnome/shell/extensions/extension.js';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import {getIBusManager} from 'resource:///org/gnome/shell/misc/ibusManager.js';

const BUS_NAME = 'org.gnome.Shell.Extensions.GswitchFocus';
const OBJECT_PATH = '/org/gnome/Shell/Extensions/GswitchFocus';
const INTERFACE_VERSION = 1;

const IFACE_XML = `<node>
  <interface name="org.gnome.Shell.Extensions.GswitchFocus">
    <property name="Version" type="u" access="read"/>
    <property name="FocusedApp" type="s" access="read"/>
    <property name="FocusedWmClass" type="s" access="read"/>
    <property name="InputPurpose" type="s" access="read"/>
    <method name="GetFocus">
      <arg type="s" name="app_id" direction="out"/>
      <arg type="s" name="wm_class" direction="out"/>
      <arg type="s" name="purpose" direction="out"/>
    </method>
    <method name="ListRunningApps">
      <arg type="a(sss)" name="apps" direction="out"/>
    </method>
    <signal name="FocusChanged">
      <arg type="s" name="app_id"/>
      <arg type="s" name="wm_class"/>
    </signal>
    <signal name="InputPurposeChanged">
      <arg type="s" name="purpose"/>
    </signal>
    <signal name="Disabling">
      <arg type="s" name="reason"/>
    </signal>
  </interface>
</node>`;

// Purpose names on the bus, keyed by enum member name. The numeric values are
// looked up in the installed typelibs at run time (no enum value is
// hard-coded); members a typelib does not have are skipped.
const IBUS_PURPOSE_NAMES = [
    ['FREE_FORM', 'normal'],
    ['ALPHA', 'alpha'],
    ['DIGITS', 'digits'],
    ['NUMBER', 'number'],
    ['PHONE', 'phone'],
    ['URL', 'url'],
    ['EMAIL', 'email'],
    ['NAME', 'name'],
    ['PASSWORD', 'password'],
    ['PIN', 'pin'],
    ['TERMINAL', 'terminal'],
    ['DATE', 'date'],
    ['TIME', 'time'],
    ['DATETIME', 'datetime'],
];

const CLUTTER_PURPOSE_NAMES = [
    ['NORMAL', 'normal'],
    ['ALPHA', 'alpha'],
    ['DIGITS', 'digits'],
    ['NUMBER', 'number'],
    ['PHONE', 'phone'],
    ['URL', 'url'],
    ['EMAIL', 'email'],
    ['NAME', 'name'],
    ['PASSWORD', 'password'],
    ['DATE', 'date'],
    ['TIME', 'time'],
    ['DATETIME', 'datetime'],
    ['TERMINAL', 'terminal'],
];

// Content hints that mark a field as secret whatever its purpose. Toolkits
// such as Qt 6 on Wayland (text-input-v3) report password line edits as
// purpose NORMAL with these hints; gnome-shell maps the Clutter
// SENSITIVE_DATA hint to IBus PRIVATE (misc/inputMethod.js). Such a field is
// reported as 'password'. Values are looked up at run time like the purposes.
const IBUS_SECRET_HINTS = ['HIDDEN_TEXT', 'PRIVATE'];
const CLUTTER_SECRET_HINTS = ['HIDDEN_TEXT', 'SENSITIVE_DATA'];

/**
 * @param {object} flagsObject - GI flags such as IBus.InputHints
 * @param {Array<string>} names - flag member names
 * @returns {number} - the bitwise OR of the members the typelib has
 */
function hintMask(flagsObject, names) {
    let mask = 0;
    for (const name of names) {
        const value = flagsObject?.[name];
        if (typeof value === 'number')
            mask |= value;
    }
    return mask;
}

/**
 * @param {string} name - the bus name of a purpose
 * @param {number} hints - the field's content hints
 * @param {number} secretMask - hintMask of the secret hints
 * @returns {string} - 'password' for a secret field that does not already
 *   report password or pin, else name
 */
function securePurpose(name, hints, secretMask) {
    if ((hints & secretMask) !== 0 && name !== 'password' && name !== 'pin')
        return 'password';
    return name;
}

/**
 * @param {object} enumObject - a GI enum such as IBus.InputPurpose
 * @param {Array<Array<string>>} names - [member, bus name] pairs
 * @returns {Map<number, string>} - enum value to bus name
 */
function purposeTable(enumObject, names) {
    const table = new Map();
    for (const [member, name] of names) {
        const value = enumObject?.[member];
        if (value !== undefined && !table.has(value))
            table.set(value, name);
    }
    return table;
}

class FocusReporter {
    constructor() {
        this._ibusPurposes = purposeTable(IBus.InputPurpose, IBUS_PURPOSE_NAMES);
        this._clutterPurposes = purposeTable(Clutter.InputContentPurpose, CLUTTER_PURPOSE_NAMES);
        this._ibusSecretHints = hintMask(IBus.InputHints, IBUS_SECRET_HINTS);
        this._clutterSecretHints = hintMask(Clutter.InputContentHintFlags, CLUTTER_SECRET_HINTS);

        // An IBus 'focus-in' was seen since the last 'focus-out'.
        this._imFocused = false;
        // IBus delivered purpose information at least once; until then the
        // Clutter input method is the fallback source.
        this._ibusSeen = false;

        [this._appId, this._wmClass] = this._readFocus();
        this._purpose = this._initialPurpose();

        this._dbusImpl = Gio.DBusExportedObject.wrapJSObject(IFACE_XML, this);
        this._dbusImpl.export(Gio.DBus.session, OBJECT_PATH);
        this._ownName = Gio.DBus.session.own_name(BUS_NAME,
            Gio.BusNameOwnerFlags.NONE, null, null);

        global.display.connectObject(
            'notify::focus-window', () => this._updateFocus(true),
            this);
        Shell.WindowTracker.get_default().connectObject(
            'notify::focus-app', () => this._updateFocus(false),
            this);
        getIBusManager().connectObject(
            'focus-in', () => this._onIBusFocusIn(),
            'focus-out', () => this._onIBusFocusOut(),
            'set-content-type', (_manager, purpose, hints) => this._onIBusContentType(purpose, hints),
            this);
        Main.inputMethod?.connectObject(
            'notify::content-purpose', () => this._onClutterPurpose(),
            'notify::content-hints', () => this._onClutterPurpose(),
            this);
    }

    destroy() {
        if (this._dbusImpl) {
            // Last word to the daemon: a lock disables user-mode extensions,
            // and the daemon fails closed on a vanish without this signal.
            const reason = Main.sessionMode.isLocked ? 'locked' : 'disabled';
            try {
                this._dbusImpl.emit_signal('Disabling', new GLib.Variant('(s)', [reason]));
            } catch (e) {
                console.warn(`gswitch-focus: failed to emit Disabling: ${e}`);
            }
        }

        global.display.disconnectObject(this);
        Shell.WindowTracker.get_default().disconnectObject(this);
        getIBusManager().disconnectObject(this);
        Main.inputMethod?.disconnectObject(this);

        if (this._ownName) {
            Gio.DBus.session.unown_name(this._ownName);
            this._ownName = 0;
        }

        if (this._dbusImpl) {
            try {
                this._dbusImpl.unexport();
            } catch (e) {
                console.warn(`gswitch-focus: failed to unexport ${OBJECT_PATH}: ${e}`);
            }
            this._dbusImpl = null;
        }
    }

    // D-Bus properties

    get Version() {
        return INTERFACE_VERSION;
    }

    get FocusedApp() {
        return this._appId;
    }

    get FocusedWmClass() {
        return this._wmClass;
    }

    get InputPurpose() {
        return this._purpose;
    }

    // D-Bus methods

    GetFocus() {
        return [this._appId, this._wmClass, this._purpose];
    }

    ListRunningApps() {
        return Shell.AppSystem.get_default().get_running().map(app => {
            const [window] = app.get_windows();
            return [app.get_id() ?? '', app.get_name() ?? '', window?.get_wm_class() ?? ''];
        });
    }

    // Focus window

    _readFocus() {
        const window = global.display.focus_window;
        if (!window)
            return ['', ''];

        // Windows without a matching .desktop file get a window-backed app
        // whose id is "window:N".
        const app = Shell.WindowTracker.get_default().get_window_app(window);
        return [app?.get_id() ?? '', window.get_wm_class() ?? ''];
    }

    /**
     * @param {boolean} always - emit FocusChanged even when the app id and
     *   WM_CLASS are unchanged (every focus-window change, also within one app)
     */
    _updateFocus(always) {
        if (!this._dbusImpl)
            return;

        const [appId, wmClass] = this._readFocus();
        const appChanged = appId !== this._appId;
        const wmClassChanged = wmClass !== this._wmClass;
        if (!always && !appChanged && !wmClassChanged)
            return;

        this._appId = appId;
        this._wmClass = wmClass;
        if (appChanged)
            this._dbusImpl.emit_property_changed('FocusedApp', new GLib.Variant('s', appId));
        if (wmClassChanged)
            this._dbusImpl.emit_property_changed('FocusedWmClass', new GLib.Variant('s', wmClass));
        this._dbusImpl.emit_signal('FocusChanged', new GLib.Variant('(ss)', [appId, wmClass]));
    }

    // Input purpose

    _initialPurpose() {
        if (Main.inputMethod?.currentFocus)
            return this._clutterPurpose();
        return 'unknown';
    }

    _clutterPurpose() {
        const im = Main.inputMethod;
        return securePurpose(this._clutterPurposes.get(im.contentPurpose) ?? 'unknown',
            im.contentHints ?? 0, this._clutterSecretHints);
    }

    _onIBusFocusIn() {
        this._ibusSeen = true;
        this._imFocused = true;
    }

    _onIBusFocusOut() {
        this._ibusSeen = true;
        this._imFocused = false;
        this._setPurpose('none');
    }

    _onIBusContentType(purpose, hints) {
        this._ibusSeen = true;
        // Pairing rule: a content type counts only while an input context is
        // focused. ibusManager does not re-emit 'focus-in' for gnome-shell's
        // own input context (misc/ibusManager.js:202-205), which serves every
        // Wayland client and shell entry, so a focused Clutter input method
        // stands in for that focus-in.
        if (!this._imFocused && !Main.inputMethod?.currentFocus)
            return;
        this._setPurpose(securePurpose(this._ibusPurposes.get(purpose) ?? 'unknown',
            hints ?? 0, this._ibusSecretHints));
    }

    _onClutterPurpose() {
        // Fallback when IBus is absent: used only until IBus reports anything.
        if (this._ibusSeen)
            return;
        this._setPurpose(this._clutterPurpose());
    }

    _setPurpose(purpose) {
        if (!this._dbusImpl || purpose === this._purpose)
            return;

        this._purpose = purpose;
        this._dbusImpl.emit_property_changed('InputPurpose', new GLib.Variant('s', purpose));
        this._dbusImpl.emit_signal('InputPurposeChanged', new GLib.Variant('(s)', [purpose]));
    }
}

export default class GswitchFocusExtension extends Extension {
    enable() {
        this._reporter = new FocusReporter();
    }

    disable() {
        this._reporter?.destroy();
        this._reporter = null;
    }
}
