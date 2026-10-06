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
use tauri_plugin_opener::OpenerExt;

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

/// Accepts the address of a homecloud on another device: https on a Tailscale
/// name, lowercase, nothing after the host (as web.public_origin).
fn validate_remote_url(url: &str) -> Result<(), String> {
    let host = url
        .strip_prefix("https://")
        .ok_or_else(|| format!("{url} is not an https address"))?;
    if host.is_empty()
        || !host.ends_with(REMOTE_DOMAIN)
        || host.len() == REMOTE_DOMAIN.len()
        || host != host.to_lowercase()
        || host.contains(['/', '?', '#', '@', ':'])
    {
        return Err(format!(
            "{url} is not a homecloud address on your tailnet, like https://homecloud.example{REMOTE_DOMAIN}"
        ));
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
    blocking(move || read_settings(&app)).await
}

/// The address and token to call the agent with. For this PC's agent they
/// come from its discovery file, which exists only while it runs; for a paired
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
            let token = app
                .state::<Keychain>()
                .entry(&url)?
                .get_password()
                .map_err(|e| format!("read this device's credential for {url}: {e}"))?;
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
    blocking(move || {
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
    .await
}

/// Uses this PC's agent again and forgets the paired device's credential.
#[tauri::command]
async fn use_this_pc(app: tauri::AppHandle) -> Result<(), String> {
    blocking(move || {
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
    .await
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
    blocking(|| {
        #[cfg(windows)]
        let mut cmd = {
            use std::os::windows::process::CommandExt;
            // CREATE_NO_WINDOW: no console flashes up for schtasks.
            const CREATE_NO_WINDOW: u32 = 0x0800_0000;
            let mut cmd = Command::new("schtasks");
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
    .await
}

/// Shows an archived file in the system file manager. Only revealing is
/// offered: nothing is opened or executed.
#[tauri::command]
async fn reveal_in_folder(app: tauri::AppHandle, path: String) -> Result<(), String> {
    blocking(move || {
        let p = Path::new(&path);
        if !p.is_absolute() || !p.is_file() {
            return Err(format!("not an existing file: {path}"));
        }
        app.opener()
            .reveal_item_in_dir(p)
            .map_err(|e| e.to_string())
    })
    .await
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
        .plugin(tauri_plugin_log::Builder::new().build())
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
