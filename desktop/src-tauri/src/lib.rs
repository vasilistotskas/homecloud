//! Desktop shell for the homecloud agent. The window shows the same web app the
//! agent serves; this side only finds the running agent and offers a few
//! operating-system actions. The agent itself runs separately and is never
//! supervised here.

use std::path::{Path, PathBuf};
use std::process::Command;

use serde::{Deserialize, Serialize};
use tauri::Manager;
use tauri_plugin_opener::OpenerExt;

/// The agent's discovery file inside its state directory (internal/auth).
const ENDPOINT_FILE: &str = "agent.json";

/// Name of the agent's logon task (Windows) or systemd user unit (Linux), as
/// registered by `homecloud install`.
const AGENT_NAME: &str = "homecloud-agent";

/// The agent's discovery record (internal/auth.Endpoint).
#[derive(Deserialize, Serialize)]
struct Endpoint {
    url: String,
    token: String,
    agent_version: String,
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

/// Reads the running agent's address and token. It fails when the agent is
/// not running, because the agent removes the file when it stops.
#[tauri::command]
fn agent_endpoint() -> Result<Endpoint, String> {
    let path = state_dir()?.join(ENDPOINT_FILE);
    let data = std::fs::read(&path)
        .map_err(|e| format!("the agent is not running ({}: {e})", path.display()))?;
    serde_json::from_slice(&data).map_err(|e| format!("decode {}: {e}", path.display()))
}

/// Starts the agent through the registration `homecloud install` created.
#[tauri::command]
fn start_agent() -> Result<(), String> {
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
}

/// Shows an archived file in the system file manager. Only revealing is
/// offered: nothing is opened or executed.
#[tauri::command]
fn reveal_in_folder(app: tauri::AppHandle, path: String) -> Result<(), String> {
    let p = Path::new(&path);
    if !p.is_absolute() || !p.is_file() {
        return Err(format!("not an existing file: {path}"));
    }
    app.opener()
        .reveal_item_in_dir(p)
        .map_err(|e| e.to_string())
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
        .invoke_handler(tauri::generate_handler![
            agent_endpoint,
            start_agent,
            reveal_in_folder
        ])
        .run(tauri::generate_context!())
        .expect("error while running homecloud");
}
