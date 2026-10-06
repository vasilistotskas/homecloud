fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        // Only these app commands exist; capabilities grant them per window.
        tauri_build::AppManifest::new().commands(&[
            "connection_settings",
            "connection_credentials",
            "connect_remote",
            "use_this_pc",
            "device_name",
            "start_agent",
            "reveal_in_folder",
        ]),
    ))
    .expect("failed to run tauri-build");
}
