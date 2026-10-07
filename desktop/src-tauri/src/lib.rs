//! Desktop shell for the homecloud agent. The window shows the same web app the
//! agent serves; this side only finds the agent, keeps the credential for a
//! homecloud on another device, and offers a few operating-system actions.
//! The agent itself runs separately and is never supervised here.
//!
//! Every command is async and does its blocking work (files, the credential
//! store, child processes) on a blocking thread: Tauri runs synchronous
//! commands on the main thread, where they would freeze the window.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, Mutex};

use keyring_core::{CredentialStore, Entry};
use serde::{Deserialize, Serialize};
use tauri::Manager;
use tauri_plugin_log::{RotationStrategy, WEBVIEW_TARGET, log};
use tauri_plugin_opener::OpenerExt;
use url::{Host, Url};

/// The agent's discovery file inside its state directory (internal/auth).
const ENDPOINT_FILE: &str = "agent.json";

/// Which agent the app uses, in the app's config directory. Absent means this
/// PC's agent.
const CONNECTION_FILE: &str = "connection.json";

/// The credential store service under which a paired device's token is kept,
/// one entry per homecloud address.
const KEYRING_SERVICE: &str = "homecloud";

/// The domain a homecloud on another device is reached at: Tailscale Serve
/// (deploy/compose.yaml). The content security policy allows only it.
const REMOTE_DOMAIN: &str = ".ts.net";

/// Name of the agent's logon task (Windows) or systemd user unit (Linux), as
/// registered by `homecloud install`.
const AGENT_NAME: &str = "homecloud-agent";

/// The app's log, in the app's log directory (%LOCALAPPDATA%<identifier>logs,
/// ~/.local/share/<identifier>/logs): rotated at this size, keeping this many
/// files. It records what this side did and what the page reported, never a
/// token.
const LOG_FILE_BYTES: u128 = 1 << 20;
const LOG_FILES: usize = 5;

/// Logs a command's failure (the page shows it too, but only the log keeps it).
fn logged<T>(command: &str, result: Result<T, String>) -> Result<T, String> {
    if let Err(e) = &result {
        log::warn!("{command} failed: {e}");
    }
    result
}

/// The agent's discovery record (internal/auth.Endpoint); other fields are
/// ignored.
#[derive(Deserialize)]
struct Endpoint {
    url: String,
    token: String,
}

/// The agent's per-user state directory (internal/platform.StateDir).
#[cfg(windows)]
fn state_dir() -> Result<PathBuf, String> {
    std::env::var_os("LOCALAPPDATA")
        .map(|d| PathBuf::from(d).join("homecloud"))
        .ok_or_else(|| "LOCALAPPDATA is not set".to_owned())
}

/// The agent's per-user state directory (internal/platform.StateDir): the XDG
/// Base Directory specification treats an unset or relative XDG_STATE_HOME as
/// $HOME/.local/state.
#[cfg(target_os = "linux")]
fn state_dir() -> Result<PathBuf, String> {
    if let Some(dir) = std::env::var_os("XDG_STATE_HOME")
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
    {
        return Ok(dir.join("homecloud"));
    }
    std::env::var_os("HOME")
        .map(|h| {
            PathBuf::from(h)
                .join(".local")
                .join("state")
                .join("homecloud")
        })
        .ok_or_else(|| "HOME is not set".to_owned())
}

/// Which agent the app uses: this PC's, or a homecloud on another device the
/// app is paired with. The token of a paired device is not part of it.
#[derive(Deserialize, Serialize, Debug, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
enum ConnectionSettings {
    ThisPc,
    Remote { url: String, device_id: String },
}

/// The address and token the app calls the agent with.
#[derive(Serialize)]
struct Credentials {
    url: String,
    token: String,
}

/// The operating system's credential store, opened on first use: on Linux,
/// opening it connects to the Secret Service over D-Bus, which a window that
/// never pairs does not need.
#[derive(Default)]
struct Keychain(Mutex<Option<Arc<CredentialStore>>>);

impl Keychain {
    fn store(&self) -> Result<Arc<CredentialStore>, String> {
        let mut store = self
            .0
            .lock()
            .map_err(|_| "the credential store lock is poisoned".to_owned())?;
        if let Some(s) = store.as_ref() {
            return Ok(Arc::clone(s));
        }
        let opened = open_store()?;
        *store = Some(Arc::clone(&opened));
        Ok(opened)
    }

    /// The entry holding the token for the homecloud at url.
    fn entry(&self, url: &str) -> Result<Entry, String> {
        let modifiers = entry_modifiers(url);
        let modifiers: HashMap<&str, &str> =
            modifiers.iter().map(|(k, v)| (*k, v.as_str())).collect();
        self.store()?
            .build(KEYRING_SERVICE, url, Some(&modifiers))
            .map_err(|e| format!("open the credential for {url}: {e}"))
    }
}

/// Windows Credential Manager (Microsoft's CredWrite/CredRead API).
#[cfg(windows)]
fn open_store() -> Result<Arc<CredentialStore>, String> {
    let store: Arc<CredentialStore> = windows_native_keyring_store::Store::new()
        .map_err(|e| format!("open Windows Credential Manager: {e}"))?;
    Ok(store)
}

/// The Secret Service (GNOME Keyring, KDE Wallet) over D-Bus.
#[cfg(target_os = "linux")]
fn open_store() -> Result<Arc<CredentialStore>, String> {
    let store: Arc<CredentialStore> = zbus_secret_service_keyring_store::Store::new()
        .map_err(|e| format!("open the Secret Service: {e}"))?;
    Ok(store)
}

/// Windows: a device token stays on this computer (Local), never roaming with
/// the user profile, which Credential Manager's default (Enterprise) allows.
#[cfg(windows)]
fn entry_modifiers(_url: &str) -> HashMap<&'static str, String> {
    HashMap::from([("persistence", "Local".to_owned())])
}

/// Linux: a readable name in the keyring app.
#[cfg(target_os = "linux")]
fn entry_modifiers(url: &str) -> HashMap<&'static str, String> {
    HashMap::from([("label", format!("homecloud device token for {url}"))])
}

/// The Windows system directory (System32). A program named without a path
/// would be looked up next to this app first, in a folder the user can write.
#[cfg(windows)]
fn system_dir() -> Result<PathBuf, String> {
    use std::os::windows::ffi::OsStringExt;
    use windows_sys::Win32::System::SystemInformation::GetSystemDirectoryW;
    const MAX_PATH: usize = 260;
    let mut buf = [0u16; MAX_PATH];
    // SAFETY: buf is a writable buffer of MAX_PATH UTF-16 units, as passed.
    let n = unsafe { GetSystemDirectoryW(buf.as_mut_ptr(), MAX_PATH as u32) } as usize;
    if n == 0 || n >= MAX_PATH {
        return Err(format!(
            "locate the system directory: {}",
            std::io::Error::last_os_error()
        ));
    }
    Ok(PathBuf::from(std::ffi::OsString::from_wide(&buf[..n])))
}

/// Runs blocking work off the main thread.
async fn blocking<T: Send + 'static>(
    work: impl FnOnce() -> Result<T, String> + Send + 'static,
) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(work)
        .await
        .map_err(|e| format!("background work failed: {e}"))?
}

fn connection_path(app: &tauri::AppHandle) -> Result<PathBuf, String> {
    app.path()
        .app_config_dir()
        .map(|d| d.join(CONNECTION_FILE))
        .map_err(|e| format!("locate the app's config directory: {e}"))
}

/// Accepts the address of a homecloud on another device: an https origin on a
/// Tailscale name, with nothing after the host (as web.public_origin). It is
/// read with the WHATWG URL parser, as the webview reads it, and must come
/// back unchanged: anything the parser would rewrite (a backslash, capitals,
/// a user, a path) or add (a port) is refused.
fn validate_remote_url(url: &str) -> Result<(), String> {
    let refused = || {
        format!(
            "{url} is not a homecloud address on your tailnet, like https://homecloud.example{REMOTE_DOMAIN}"
        )
    };
    let parsed = Url::parse(url).map_err(|_| refused())?;
    let Some(Host::Domain(host)) = parsed.host() else {
        return Err(refused());
    };
    if parsed.scheme() != "https"
        || parsed.port().is_some()
        || parsed.origin().ascii_serialization() != url
        || host.len() <= REMOTE_DOMAIN.len()
        || !host.ends_with(REMOTE_DOMAIN)
    {
        return Err(refused());
    }
    Ok(())
}

fn read_settings(app: &tauri::AppHandle) -> Result<ConnectionSettings, String> {
    let path = connection_path(app)?;
    match std::fs::read(&path) {
        Ok(data) => {
            serde_json::from_slice(&data).map_err(|e| format!("decode {}: {e}", path.display()))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(ConnectionSettings::ThisPc),
        Err(e) => Err(format!("read {}: {e}", path.display())),
    }
}

/// Which agent the app uses. It never fails for a missing file: this PC's
/// agent is the default.
#[tauri::command]
async fn connection_settings(app: tauri::AppHandle) -> Result<ConnectionSettings, String> {
    logged(
        "connection_settings",
        blocking(move || read_settings(&app)).await,
    )
}

/// The address and token to call the agent with. For this PC's agent they
/// come from its discovery file, which exists only while it runs (the page
/// asks again until it does, so that failure is not logged); for a paired
/// device, from the credential store.
#[tauri::command]
async fn connection_credentials(app: tauri::AppHandle) -> Result<Credentials, String> {
    blocking(move || match read_settings(&app)? {
        ConnectionSettings::ThisPc => {
            let path = state_dir()?.join(ENDPOINT_FILE);
            let data = std::fs::read(&path)
                .map_err(|e| format!("the agent is not running ({}: {e})", path.display()))?;
            let ep: Endpoint = serde_json::from_slice(&data)
                .map_err(|e| format!("decode {}: {e}", path.display()))?;
            Ok(Credentials {
                url: ep.url,
                token: ep.token,
            })
        }
        ConnectionSettings::Remote { url, .. } => {
            let token = logged(
                "connection_credentials",
                app.state::<Keychain>().entry(&url).and_then(|entry| {
                    entry
                        .get_password()
                        .map_err(|e| format!("read this device's credential for {url}: {e}"))
                }),
            )?;
            Ok(Credentials { url, token })
        }
    })
    .await
}

/// Uses the homecloud at url from now on, with the token pairing returned.
#[tauri::command]
async fn connect_remote(
    app: tauri::AppHandle,
    url: String,
    device_id: String,
    token: String,
) -> Result<(), String> {
    validate_remote_url(&url)?;
    let paired = format!("{url} as device {device_id}");
    let done = blocking(move || {
        app.state::<Keychain>()
            .entry(&url)?
            .set_password(&token)
            .map_err(|e| format!("store this device's credential: {e}"))?;
        let path = connection_path(&app)?;
        let data = serde_json::to_vec_pretty(&ConnectionSettings::Remote { url, device_id })
            .map_err(|e| format!("encode the connection: {e}"))?;
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
        }
        // Write beside the file and rename, so a crash never leaves half a file.
        let tmp = path.with_extension("json.tmp");
        std::fs::write(&tmp, data).map_err(|e| format!("write {}: {e}", tmp.display()))?;
        std::fs::rename(&tmp, &path).map_err(|e| format!("write {}: {e}", path.display()))
    })
    .await;
    if done.is_ok() {
        log::info!("paired: using {paired}");
    }
    logged("connect_remote", done)
}

/// Uses this PC's agent again and forgets the paired device's credential.
#[tauri::command]
async fn use_this_pc(app: tauri::AppHandle) -> Result<(), String> {
    let done = blocking(move || {
        log::info!("using this computer's agent again");
        if let ConnectionSettings::Remote { url, .. } = read_settings(&app)? {
            match app.state::<Keychain>().entry(&url)?.delete_credential() {
                Ok(()) | Err(keyring_core::Error::NoEntry) => {}
                Err(e) => return Err(format!("forget this device's credential: {e}")),
            }
        }
        let path = connection_path(&app)?;
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(format!("remove {}: {e}", path.display())),
        }
    })
    .await;
    logged("use_this_pc", done)
}

/// This computer's name, offered as the paired device's name.
#[tauri::command]
async fn device_name() -> Result<String, String> {
    blocking(|| {
        #[cfg(windows)]
        let name = std::env::var("COMPUTERNAME").ok();
        #[cfg(target_os = "linux")]
        let name = std::fs::read_to_string("/proc/sys/kernel/hostname").ok();
        Ok(name
            .map(|n| n.trim().to_owned())
            .filter(|n| !n.is_empty())
            .unwrap_or_else(|| "This computer".to_owned()))
    })
    .await
}

/// Starts the agent through the registration `homecloud install` created.
#[tauri::command]
async fn start_agent() -> Result<(), String> {
    let done = blocking(|| {
        log::info!("starting the agent ({AGENT_NAME})");
        #[cfg(windows)]
        let mut cmd = {
            use std::os::windows::process::CommandExt;
            // CREATE_NO_WINDOW: no console flashes up for schtasks.
            const CREATE_NO_WINDOW: u32 = 0x0800_0000;
            let mut cmd = Command::new(system_dir()?.join("schtasks.exe"));
            cmd.args(["/Run", "/TN", AGENT_NAME])
                .creation_flags(CREATE_NO_WINDOW);
            cmd
        };
        #[cfg(target_os = "linux")]
        let mut cmd = {
            let mut cmd = Command::new("systemctl");
            cmd.args(["--user", "start", &format!("{AGENT_NAME}.service")]);
            cmd
        };
        let output = cmd.output().map_err(|e| format!("start the agent: {e}"))?;
        if output.status.success() {
            Ok(())
        } else {
            Err(format!(
                "start the agent: {} {}",
                output.status,
                String::from_utf8_lossy(&output.stderr).trim()
            ))
        }
    })
    .await;
    logged("start_agent", done)
}

/// Whether p names a place on this computer's own drives. On Windows that is
/// a drive letter path: a UNC path (\\server\share, \\?\UNC\…) would make
/// the file manager, or even a check that the file exists, contact another
/// computer with this user's credentials.
#[cfg(windows)]
fn on_local_drive(p: &Path) -> bool {
    use std::path::{Component, Prefix};
    matches!(p.components().next(), Some(Component::Prefix(pre)) if matches!(pre.kind(), Prefix::Disk(_)))
        && p.is_absolute()
}

#[cfg(target_os = "linux")]
fn on_local_drive(p: &Path) -> bool {
    p.is_absolute()
}

/// Shows an archived file in the system file manager. Only revealing is
/// offered: nothing is opened or executed.
#[tauri::command]
async fn reveal_in_folder(app: tauri::AppHandle, path: String) -> Result<(), String> {
    let done = blocking(move || {
        let p = Path::new(&path);
        if !on_local_drive(p) || !p.is_file() {
            return Err(format!("not an existing file on this computer: {path}"));
        }
        app.opener()
            .reveal_item_in_dir(p)
            .map_err(|e| e.to_string())
    })
    .await;
    logged("reveal_in_folder", done)
}

/// Builds and runs the desktop app.
pub fn run() {
    tauri::Builder::default()
        // Must be registered first: a second launch focuses the existing window.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.unminimize();
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_window_state::Builder::default().build())
        .plugin(tauri_plugin_opener::init())
        .plugin(
            tauri_plugin_log::Builder::new()
                // This app and the page at info; the libraries' own chatter
                // (asset lookups, credential store calls) only when it matters.
                .level(log::LevelFilter::Warn)
                .level_for("homecloud_desktop_lib", log::LevelFilter::Info)
                .level_for(WEBVIEW_TARGET, log::LevelFilter::Info)
                .max_file_size(LOG_FILE_BYTES)
                .rotation_strategy(RotationStrategy::KeepSome(LOG_FILES))
                .build(),
        )
        .setup(|app| {
            log::info!("homecloud desktop {} starting", app.package_info().version);
            Ok(())
        })
        .manage(Keychain::default())
        .invoke_handler(tauri::generate_handler![
            connection_settings,
            connection_credentials,
            connect_remote,
            use_this_pc,
            device_name,
            start_agent,
            reveal_in_folder
        ])
        .run(tauri::generate_context!())
        .expect("error while running homecloud");
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A real file path on whatever drive this machine keeps its temporary
    /// files, and the other forms naming the same file, which go through the
    /// network or a device instead of the drive.
    #[cfg(windows)]
    #[test]
    fn local_drives_only() {
        use std::path::{Component, Prefix};
        let local = std::env::temp_dir().join("photo.jpg");
        let Some(Component::Prefix(prefix)) = local.components().next() else {
            panic!("{} has no prefix", local.display());
        };
        let Prefix::Disk(letter) = prefix.kind() else {
            panic!("{} is not on a drive letter", local.display());
        };
        let drive = char::from(letter);
        let path = local.to_str().expect("a UTF-8 temporary folder");
        let rest = &path[prefix.as_os_str().len()..]; // \…\photo.jpg
        assert!(on_local_drive(&local), "{path} refused");
        for other in [
            format!(r"\\localhost\{drive}${rest}"),
            format!(r"\\?\UNC\localhost\{drive}${rest}"),
            format!(r"\\.\{drive}:"),
            format!("{drive}:photo.jpg"),
            rest.to_owned(),
        ] {
            assert!(!on_local_drive(Path::new(&other)), "{other} accepted");
        }
    }

    #[cfg(windows)]
    #[test]
    fn schtasks_from_the_system_directory() {
        let dir = system_dir().expect("system directory");
        assert!(
            dir.join("schtasks.exe").is_file(),
            "{} has no schtasks.exe",
            dir.display()
        );
    }

    #[test]
    fn remote_urls() {
        for ok in [
            "https://homecloud.tail1234.ts.net",
            "https://nas.example.ts.net",
        ] {
            assert!(validate_remote_url(ok).is_ok(), "{ok} rejected");
        }
        for bad in [
            "http://homecloud.tail1234.ts.net",
            "https://homecloud.tail1234.ts.net/",
            "https://homecloud.tail1234.ts.net:8443",
            "https://HOMECLOUD.tail1234.ts.net",
            "https://example.com",
            "https://.ts.net",
            "https://evil.com#.ts.net",
            "https://evil.com\\.ts.net",
            "https://homecloud.tail1234.ts.net:443",
            "https://user@homecloud.tail1234.ts.net",
            "https://homecloud.tail1234.ts.net?x",
            "https://hömecloud.tail1234.ts.net",
            "https://100.64.0.1",
            "homecloud.tail1234.ts.net",
        ] {
            assert!(validate_remote_url(bad).is_err(), "{bad} accepted");
        }
    }

    #[test]
    fn connection_settings_round_trip() {
        let remote = ConnectionSettings::Remote {
            url: "https://nas.example.ts.net".to_owned(),
            device_id: "d1".to_owned(),
        };
        let json = serde_json::to_string(&remote).unwrap();
        assert_eq!(
            json,
            r#"{"kind":"remote","url":"https://nas.example.ts.net","device_id":"d1"}"#
        );
        assert_eq!(
            serde_json::from_str::<ConnectionSettings>(&json).unwrap(),
            remote
        );
        assert_eq!(
            serde_json::from_str::<ConnectionSettings>(r#"{"kind":"this_pc"}"#).unwrap(),
            ConnectionSettings::ThisPc
        );
    }
}
